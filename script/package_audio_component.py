#!/usr/bin/env python3
"""Wrap the tested MiMac audio component in a user-facing Installer package.

This script only packages files. It never runs Installer, requests elevation,
or restarts audio services. The unsigned output still needs release signing and
notarization before external distribution.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import shutil
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
BUILD_SCRIPT = ROOT / "script" / "build_audio_compat.py"
COMPONENT_INSTALL_PATH = "/Library/Audio/Plug-Ins/HAL/MiMacAudio.driver"
PAYLOAD_RELATIVE_PATH = "Library/Audio/Plug-Ins/HAL/MiMacAudio.driver"
PRODUCT_TITLE = "MiMac 遥控器音频组件"
WELCOME_HTML = """<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>MiMac 音频组件</title></head>
<body><h1>安装 MiMac 遥控器音频组件</h1>
<p>此组件会安装到这台 Mac 的系统音频插件目录。</p>
<p>安装需要管理员密码。安装完成后，请重新启动 Mac。</p>
</body></html>
"""
ARCH_TO_NATIVE = {"arm64": "arm64", "amd64": "x86_64"}
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


class PackageError(RuntimeError):
    pass


def run(command, *, capture=False):
    command = [str(part) for part in command]
    result = subprocess.run(
        command,
        check=False,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    if result.returncode:
        details = ((result.stdout or "") + (result.stderr or "")).strip()
        raise PackageError(
            "command failed ({}): {}{}".format(
                result.returncode, " ".join(command), "\n" + details if details else ""
            )
        )
    return result.stdout.strip() if capture else ""


def sha256_file(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def current_go_arch(go=None):
    go = go or os.environ.get("MIMAC_GO", "go")
    executable = shutil.which(go)
    if executable is None and Path(go).is_file():
        executable = str(Path(go).resolve())
    if executable is None:
        raise PackageError("Go is required to determine the current GOARCH")
    arch = run([executable, "env", "GOARCH"], capture=True)
    if arch not in ARCH_TO_NATIVE:
        raise PackageError("unsupported GOARCH: {}".format(arch))
    return arch


def output_path(root, go_arch):
    if go_arch not in ARCH_TO_NATIVE:
        raise PackageError("unsupported GOARCH: {}".format(go_arch))
    return Path(root) / "resources" / ("darwin-" + go_arch) / "MiMacAudio.pkg"


def package_inputs(package_arg, manifest_arg, go_arch):
    if (package_arg is None) != (manifest_arg is None):
        raise PackageError("--local-test-pkg and --manifest must be supplied together")
    if package_arg is not None:
        return Path(package_arg).expanduser().resolve(strict=True), Path(manifest_arg).expanduser().resolve(strict=True)

    run([sys.executable, BUILD_SCRIPT, "--package", "--arch", go_arch])
    manifest = ROOT / ".build" / "audio-compat" / "manifest.json"
    try:
        data = json.loads(manifest.read_text(encoding="utf-8"))
        package_name = data["package"]["path"]
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise PackageError("audio component build did not produce a usable manifest: {}".format(error)) from error
    package = Path(package_name)
    if not package.is_absolute():
        package = ROOT / package
    return package.resolve(strict=True), manifest.resolve(strict=True)


def load_and_check_manifest(path, package, go_arch):
    try:
        manifest = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        raise PackageError("cannot read component manifest: {}".format(error)) from error
    try:
        build = manifest["build"]
        bundle = manifest["bundle"]
        package_info = manifest["package"]
        source_arch = build["architecture"]
        binary_hash = bundle["binary_sha256"]
        bundle_id = bundle["identifier"]
        install_path = package_info["install_path"]
        package_hash = package_info["sha256"]
        package_bytes = package_info["bytes"]
        notices = bundle["notices"]
        abi_test = manifest["tests"]["bundle_abi"]
    except (KeyError, TypeError) as error:
        raise PackageError("manifest is missing a required component/package field") from error

    source_go_arch = source_arch if source_arch in ARCH_TO_NATIVE else {"x86_64": "amd64"}.get(source_arch)
    source_native_arch = build.get("native_architecture", ARCH_TO_NATIVE.get(source_go_arch))
    if source_go_arch != go_arch or source_native_arch != ARCH_TO_NATIVE.get(go_arch):
        raise PackageError("component architecture {} does not match GOARCH {}".format(source_arch, go_arch))
    if not SHA256_RE.fullmatch(str(binary_hash)) or not SHA256_RE.fullmatch(str(package_hash)):
        raise PackageError("manifest must contain exact lowercase SHA-256 values")
    if not isinstance(package_bytes, int) or package_bytes <= 0:
        raise PackageError("manifest has no valid component package size")
    if install_path != COMPONENT_INSTALL_PATH:
        raise PackageError("manifest install path is not the MiMac system audio plug-in path")
    if not bundle_id or not isinstance(bundle_id, str) or not isinstance(notices, list) or not notices:
        raise PackageError("manifest has no bundle identity or upstream notices")
    if package_info.get("installed") is not False or package_info.get("restarts_audio") is not False:
        raise PackageError("input manifest does not describe an uninstalled local-test package")
    if package_info.get("postinstall_scripts") is not False:
        raise PackageError("input local-test package is expected to have no postinstall scripts")
    abi_status = abi_test.get("status") if isinstance(abi_test, dict) else abi_test
    if package_info.get("payload_signature_hash_abi_verified") is not (abi_status == "passed"):
        raise PackageError("manifest overstates or misreports its Bundle ABI test")
    if manifest.get("installed") is not False or manifest.get("audio_service_restarted") is not False:
        raise PackageError("input manifest reports installation or an audio service restart")
    if Path(package).stat().st_size != package_bytes:
        raise PackageError("local-test package size differs from its manifest")
    if sha256_file(package) != package_hash:
        raise PackageError("local-test package SHA-256 differs from its manifest")
    return manifest


def build_distribution_xml(package_filename, package_id, version):
    root = ET.Element("installer-gui-script", {"minSpecVersion": "2"})
    ET.SubElement(root, "title").text = PRODUCT_TITLE
    ET.SubElement(root, "welcome", {"file": "Welcome.html"})
    ET.SubElement(root, "options", {"customize": "never", "require-scripts": "false"})
    ET.SubElement(root, "domains", {
        "enable_anywhere": "false",
        "enable_currentUserHome": "false",
        "enable_localSystem": "true",
    })
    outline = ET.SubElement(root, "choices-outline")
    ET.SubElement(outline, "line", {"choice": "default"})
    choice = ET.SubElement(root, "choice", {"id": "default", "visible": "false"})
    ET.SubElement(choice, "pkg-ref", {"id": package_id})
    reference = ET.SubElement(root, "pkg-ref", {
        "id": package_id,
        "version": version,
        "onConclusion": "RequireRestart",
    })
    reference.text = package_filename
    return ET.tostring(root, encoding="utf-8", xml_declaration=True)


def _expanded_payload(expanded):
    payload = Path(expanded) / "Payload"
    if not payload.is_dir():
        raise PackageError("expanded component package has no Payload directory")
    return payload


def _payload_files(payload):
    return {
        str(path.relative_to(payload)).replace(os.sep, "/"): path
        for path in Path(payload).rglob("*")
        if path.is_file()
    }


def _payload_digests(payload):
    return {name: sha256_file(path) for name, path in sorted(_payload_files(payload).items())}


def _package_component_directory(expanded_product, package_id):
    for info in Path(expanded_product).rglob("PackageInfo"):
        try:
            package_info = ET.parse(info).getroot()
        except (ET.ParseError, OSError):
            continue
        if package_info.get("identifier") == package_id and (info.parent / "Payload").is_dir():
            return info.parent
    raise PackageError("expanded Installer package does not contain the expected component")


def _validate_distribution(path, package_filename, package_id, version):
    try:
        root = ET.parse(path).getroot()
    except (ET.ParseError, OSError) as error:
        raise PackageError("generated Distribution XML cannot be read") from error
    domains = root.find("domains")
    if domains is None or domains.attrib != {
        "enable_anywhere": "false",
        "enable_currentUserHome": "false",
        "enable_localSystem": "true",
    }:
        raise PackageError("Distribution must permit installation only to localSystem")
    if root.findtext("title") != PRODUCT_TITLE or root.find("welcome") is None:
        raise PackageError("Distribution is missing the user-facing system component description")
    references = [
        node for node in root.findall("pkg-ref")
        if node.get("id") == package_id and node.get("onConclusion") is not None
    ]
    if len(references) != 1:
        raise PackageError("Distribution must reference exactly one audio component")
    reference = references[0]
    reference_file = (reference.text or "").removeprefix("#")
    if reference.get("version") != version or reference.get("onConclusion") != "RequireRestart" or reference_file != package_filename:
        raise PackageError("Distribution must preserve component version and require restart")
    if root.find("options") is None or root.find("options").get("require-scripts") != "false":
        raise PackageError("Distribution must not run package scripts")


def _validate_component(expanded_component, manifest, go_arch=None):
    payload = _expanded_payload(expanded_component)
    metadata_path = Path(expanded_component) / "PackageInfo"
    try:
        metadata = ET.parse(metadata_path).getroot()
    except (ET.ParseError, OSError) as error:
        raise PackageError("component PackageInfo is invalid") from error
    expected_id = manifest["bundle"]["identifier"] + ".pkg"
    if metadata.get("identifier") != expected_id or metadata.get("auth") != "root":
        raise PackageError("component package identity or root authorization differs from the manifest")
    if metadata.get("install-location") != "/" or metadata.get("postinstall-action") != "none":
        raise PackageError("local-test package must install from root without postinstall actions")
    if (Path(expanded_component) / "Scripts").exists():
        raise PackageError("local-test package unexpectedly contains scripts")

    bundle = payload / PAYLOAD_RELATIVE_PATH
    info_path = bundle / "Contents" / "Info.plist"
    binary = bundle / "Contents" / "MacOS" / "MiMacAudio"
    if go_arch is not None:
        lipo = shutil.which("lipo")
        if not lipo or go_arch not in ARCH_TO_NATIVE:
            raise PackageError("lipo and a supported target architecture are required")
        native_arch = ARCH_TO_NATIVE[go_arch]
        arches = run([lipo, "-archs", binary], capture=True).split()
        if arches != [native_arch]:
            raise PackageError("component architecture is {}; expected only {}".format(",".join(arches), native_arch))
        run([lipo, binary, "-verify_arch", native_arch])
    try:
        info = plistlib.loads(info_path.read_bytes())
    except (OSError, plistlib.InvalidFileException) as error:
        raise PackageError("component bundle Info.plist is missing or invalid") from error
    version = info.get("CFBundleShortVersionString")
    if not version or metadata.get("version") != version:
        raise PackageError("component package and bundle versions do not match")
    if info.get("CFBundleIdentifier") != manifest["bundle"]["identifier"]:
        raise PackageError("component bundle identifier differs from the manifest")
    if sha256_file(binary) != manifest["bundle"]["binary_sha256"]:
        raise PackageError("component binary SHA-256 differs from the manifest")

    files = _payload_files(payload)
    required = {
        PAYLOAD_RELATIVE_PATH + "/Contents/Info.plist",
        PAYLOAD_RELATIVE_PATH + "/Contents/MacOS/MiMacAudio",
    }
    required.update(PAYLOAD_RELATIVE_PATH + "/Contents/Resources/" + name for name in manifest["bundle"]["notices"])
    if not required.issubset(files):
        raise PackageError("component payload is missing its bundle or upstream notices")
    if any(files[name].stat().st_size == 0 for name in required):
        raise PackageError("component payload contains an empty required file")
    return payload, version, expected_id, bundle


def _receipt_for_product(package, manifest, version, package_id, payload):
    package_hash = sha256_file(package)
    bundle = manifest["bundle"]
    build = manifest["build"]
    package_manifest = manifest["package"]
    abi_test = manifest["tests"]["bundle_abi"]
    abi_status = abi_test.get("status") if isinstance(abi_test, dict) else abi_test
    abi_verified = abi_status == "passed"
    return {
        "schema_version": 1,
        "sha256": package_hash,
        "bytes": Path(package).stat().st_size,
        "architecture": build["architecture"],
        "native_architecture": build.get("native_architecture", ARCH_TO_NATIVE.get(build["architecture"], build["architecture"])),
        "component_identifier": bundle["identifier"],
        "component_package_identifier": package_id,
        "component_package_name": Path(manifest["package"]["path"]).name,
        "component_version": version,
        "component_binary_sha256": bundle["binary_sha256"],
        "source_package_sha256": package_manifest["sha256"],
        "notices": list(bundle["notices"]),
        "payload_sha256": _payload_digests(payload),
        "payload_signature_hash_abi_verified": bool(abi_verified and package_manifest.get("payload_signature_hash_abi_verified", False)),
        "bundle_abi_test": abi_test,
        "install_path": COMPONENT_INSTALL_PATH,
        "install_domain": "localSystem",
        "requires_restart": True,
        "installed": False,
        "audio_service_restarted": False,
        "signed_for_distribution": False,
        "notarized": False,
    }


def resolve_output_path(output):
    output_arg = Path(output).expanduser()
    output_absolute = output_arg if output_arg.is_absolute() else Path.cwd() / output_arg
    try:
        relative = output_absolute.relative_to(ROOT)
    except ValueError:
        relative = None
    candidates = [
        output_absolute,
        output_absolute.parent,
        output_absolute.with_name(output_absolute.name + ".sha256"),
        output_absolute.with_name(output_absolute.name + ".json"),
    ]
    if relative is not None:
        current = ROOT
        for part in relative.parts[:-1]:
            current = current / part
            candidates.append(current)
    if any(candidate.is_symlink() for candidate in candidates):
        raise PackageError("refusing symlink at output package directory or sidecar")
    return output_absolute.resolve(strict=False)


def verify_product_package(package, receipt_path=None, expected_arch=None):
    """Verify an app's bundled product package using the packager's own parser."""
    if sys.platform != "darwin":
        raise PackageError("Installer package verification requires macOS")
    package = Path(package).resolve(strict=True)
    receipt_path = Path(receipt_path) if receipt_path is not None else package.with_name(package.name + ".json")
    try:
        receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        raise PackageError("cannot read audio installer receipt: {}".format(error)) from error
    go_arch = expected_arch or receipt.get("architecture")
    if go_arch not in ARCH_TO_NATIVE:
        raise PackageError("installer receipt has an unsupported architecture")
    native_arch = ARCH_TO_NATIVE[go_arch]
    if receipt.get("schema_version") != 1 or receipt.get("architecture") != go_arch or receipt.get("native_architecture") != native_arch:
        raise PackageError("audio installer architecture does not match the app")
    if receipt.get("install_path") != COMPONENT_INSTALL_PATH or receipt.get("install_domain") != "localSystem" or receipt.get("requires_restart") is not True:
        raise PackageError("audio installer does not declare system installation and restart")
    if receipt.get("installed") is not False or receipt.get("audio_service_restarted") is not False:
        raise PackageError("audio installer receipt reports an installation or audio service restart")
    if receipt.get("signed_for_distribution") is not False or receipt.get("notarized") is not False:
        raise PackageError("this packaging path does not attest installer signing or notarization")
    if not SHA256_RE.fullmatch(str(receipt.get("sha256", ""))) or sha256_file(package) != receipt["sha256"]:
        raise PackageError("product package SHA-256 differs from its receipt")
    if package.stat().st_size != receipt.get("bytes"):
        raise PackageError("product package size differs from its receipt")
    if not SHA256_RE.fullmatch(str(receipt.get("source_package_sha256", ""))) or not SHA256_RE.fullmatch(str(receipt.get("component_binary_sha256", ""))):
        raise PackageError("installer receipt is missing exact component hashes")

    pkgutil = shutil.which("pkgutil")
    codesign = shutil.which("codesign")
    lipo = shutil.which("lipo")
    if not pkgutil or not codesign or not lipo:
        raise PackageError("pkgutil, codesign, and lipo are required")
    package_name = receipt.get("component_package_name")
    package_id = receipt.get("component_package_identifier")
    version = receipt.get("component_version")
    component_identifier = receipt.get("component_identifier")
    notices = receipt.get("notices")
    if not all(isinstance(item, str) and item for item in (package_name, package_id, version, component_identifier)) or not isinstance(notices, list) or not notices:
        raise PackageError("installer receipt has incomplete component identity")
    abi_test = receipt.get("bundle_abi_test")
    abi_status = abi_test.get("status") if isinstance(abi_test, dict) else abi_test
    if receipt.get("payload_signature_hash_abi_verified") is not (abi_status == "passed"):
        raise PackageError("installer receipt overstates or misreports its Bundle ABI test")

    with tempfile.TemporaryDirectory(prefix="audio-product-verify-") as temp_dir:
        expanded = Path(temp_dir) / "expanded"
        run([pkgutil, "--expand-full", package, expanded])
        distribution = expanded / "Distribution"
        _validate_distribution(distribution, package_name, package_id, version)
        welcome_path = expanded / "Resources" / "Welcome.html"
        try:
            welcome = welcome_path.read_text(encoding="utf-8")
        except OSError as error:
            raise PackageError("product package is missing its installation notice") from error
        for required_text in ("系统音频插件目录", "管理员密码", "重新启动 Mac"):
            if required_text not in welcome:
                raise PackageError("product package installation notice omits: " + required_text)

        component = _package_component_directory(expanded, package_id)
        manifest = {"bundle": {
            "identifier": component_identifier,
            "binary_sha256": receipt["component_binary_sha256"],
            "notices": notices,
        }}
        payload, observed_version, observed_id, bundle = _validate_component(component, manifest, go_arch)
        if observed_version != version or observed_id != package_id:
            raise PackageError("product component version or identifier differs from its receipt")
        if _payload_digests(payload) != receipt.get("payload_sha256"):
            raise PackageError("product payload or notices differ from their receipt")
        run([codesign, "--verify", "--deep", "--strict", bundle])

    return {
        "sha256": receipt["sha256"],
        "architecture": go_arch,
        "component_identifier": component_identifier,
        "component_version": version,
        "component_binary_sha256": receipt["component_binary_sha256"],
        "install_path": COMPONENT_INSTALL_PATH,
        "install_domain": "localSystem",
        "requires_restart": True,
        "payload_signature_hash_abi_verified": receipt.get("payload_signature_hash_abi_verified") is True,
        "bundle_abi_test": receipt["bundle_abi_test"],
        "signed_for_distribution": receipt.get("signed_for_distribution") is True,
        "notarized": receipt.get("notarized") is True,
    }


def package_component(package, manifest_path, go_arch, output):
    if sys.platform != "darwin":
        raise PackageError("productbuild packaging requires macOS")
    if go_arch not in ARCH_TO_NATIVE:
        raise PackageError("unsupported GOARCH: {}".format(go_arch))

    package = Path(package).resolve(strict=True)
    manifest = load_and_check_manifest(manifest_path, package, go_arch)
    productbuild = shutil.which("productbuild")
    pkgutil = shutil.which("pkgutil")
    codesign = shutil.which("codesign")
    if not productbuild or not pkgutil or not codesign:
        raise PackageError("productbuild, pkgutil, and codesign are required")

    output = resolve_output_path(output)
    if output == package or output == Path(manifest_path).resolve(strict=True):
        raise PackageError("output path must be separate from its inputs")
    build_root = ROOT / ".build"
    if build_root.is_symlink():
        raise PackageError("refusing symlink at .build output path")
    build_root.mkdir(parents=True, exist_ok=True)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="audio-product-", dir=str(build_root)) as temp_dir:
        staging = Path(temp_dir)
        source_expanded = staging / "source-expanded"
        run([pkgutil, "--expand-full", package, source_expanded])
        source_component = source_expanded
        source_payload, version, package_id, source_bundle = _validate_component(source_component, manifest, go_arch)
        run([codesign, "--verify", "--deep", "--strict", source_bundle])

        resources = staging / "resources"
        resources.mkdir()
        (resources / "Welcome.html").write_text(WELCOME_HTML, encoding="utf-8")
        distribution = staging / "Distribution"
        distribution.write_bytes(build_distribution_xml(package.name, package_id, version))
        _validate_distribution(distribution, package.name, package_id, version)

        staged_product = staging / "MiMacAudio.pkg"
        run([
            productbuild,
            "--distribution", distribution,
            "--package-path", package.parent,
            "--resources", resources,
            staged_product,
        ])

        product_expanded = staging / "product-expanded"
        run([pkgutil, "--expand-full", staged_product, product_expanded])
        product_distribution = product_expanded / "Distribution"
        _validate_distribution(product_distribution, package.name, package_id, version)
        product_welcome = product_expanded / "Resources" / "Welcome.html"
        try:
            welcome = product_welcome.read_text(encoding="utf-8")
        except OSError as error:
            raise PackageError("product package is missing its installation notice") from error
        for required_text in ("系统音频插件目录", "管理员密码", "重新启动 Mac"):
            if required_text not in welcome:
                raise PackageError("product package installation notice omits: " + required_text)

        product_component = _package_component_directory(product_expanded, package_id)
        product_payload = _expanded_payload(product_component)
        if _payload_digests(source_payload) != _payload_digests(product_payload):
            raise PackageError("product package changed the tested component payload or notices")
        _, _, _, product_bundle = _validate_component(product_component, manifest, go_arch)
        run([codesign, "--verify", "--deep", "--strict", product_bundle])

        output_stage = staging / "output.pkg"
        shutil.copyfile(staged_product, output_stage)
        sidecar_stage = staging / "output.pkg.sha256"
        receipt_stage = staging / "output.pkg.json"
        output_hash = sha256_file(output_stage)
        sidecar_stage.write_text("{}  {}\n".format(output_hash, output.name), encoding="ascii")
        if sha256_file(output_stage) != output_hash:
            raise PackageError("staged product package SHA-256 verification failed")
        receipt = _receipt_for_product(output_stage, manifest, version, package_id, product_payload)
        receipt["path"] = str(output)
        receipt_stage.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

        os.replace(output_stage, output)
        os.replace(sidecar_stage, output.with_name(output.name + ".sha256"))
        os.replace(receipt_stage, output.with_name(output.name + ".json"))

    return receipt


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--local-test-pkg", type=Path, help="a previously verified MiMacAudio-local-test.pkg")
    parser.add_argument("--manifest", type=Path, help="the manifest created alongside that exact package")
    parser.add_argument("--arch", choices=tuple(ARCH_TO_NATIVE), help="target Go architecture; defaults to go env GOARCH")
    parser.add_argument("--output", type=Path, help="product package path; defaults to resources/darwin-{GOARCH}/MiMacAudio.pkg")
    return parser


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    go_arch = args.arch or current_go_arch()
    if (args.local_test_pkg is None) != (args.manifest is None):
        parser.error("--local-test-pkg and --manifest must be supplied together")
    try:
        package, manifest = package_inputs(args.local_test_pkg, args.manifest, go_arch)
        destination = args.output or output_path(ROOT, go_arch)
        result = package_component(package, manifest, go_arch, destination)
    except (PackageError, OSError, subprocess.CalledProcessError) as error:
        print("audio component packaging failed: {}".format(error), file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2))
    print("Installer package built. Nothing was installed; audio services were not restarted.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
