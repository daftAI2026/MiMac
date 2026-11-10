#!/usr/bin/env python3
"""Build and locally validate the optional MiMac AudioServerPlugIn.

The pinned libASPL checkout is treated as read-only. Build outputs and an
optional unsigned local-test installer package stay under .build/audio-compat.
This script never installs a package, requests elevation, or restarts audio.
"""

import argparse
import concurrent.futures
import hashlib
import json
import os
import pathlib
import platform
import plistlib
import shutil
import shlex
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET

ROOT = pathlib.Path(__file__).resolve().parents[1]
COMPONENT = ROOT / "components" / "audio-compat"
BUILD = ROOT / ".build" / "audio-compat"
PINNED_COMMIT = "633e0f70203edd87d320fc5a3cae901e1363aac5"
LIBASPL_REMOTE = "https://github.com/gavv/libASPL.git"
MIN_MACOS = "14.2"
BUNDLE_ID = "dev.mimac.audio.compat"
BUNDLE_NAME = "MiMacAudio"
BUNDLE_VERSION = "0.1.0"
FACTORY_UUID = "8D60DAA7-6F0E-4F00-9D9A-3A93252466E9"
PLUGIN_TYPE_UUID = "443ABAB8-E7B3-491A-B985-BEB9187030DB"
LICENSE_FILES = ("LICENSE", "LICENSE.apple2012", "LICENSE.apple2020", "AUTHORS.md")
GO_TO_NATIVE_ARCH = {"arm64": "arm64", "amd64": "x86_64"}
NATIVE_TO_GO_ARCH = {native: go for go, native in GO_TO_NATIVE_ARCH.items()}


class BuildError(RuntimeError):
    pass


def run(command, *, cwd=None, capture=False, quiet=False, env=None):
    command = [str(part) for part in command]
    if not capture and not quiet:
        print("+ " + shlex.join(command), flush=True)
    completed = subprocess.run(
        command,
        cwd=str(cwd) if cwd is not None else None,
        check=False,
        text=True,
        stdout=subprocess.PIPE if capture or quiet else None,
        stderr=subprocess.PIPE if capture or quiet else None,
        env=env,
    )
    if completed.returncode != 0:
        if capture or quiet:
            details = (completed.stdout or "") + (completed.stderr or "")
        else:
            details = ""
        raise BuildError(
            "command failed ({}): {}\n{}".format(
                completed.returncode, " ".join(command), details
            )
        )
    return completed.stdout.strip() if capture else ""


def is_inside(path, parent):
    try:
        pathlib.Path(path).relative_to(pathlib.Path(parent))
        return True
    except ValueError:
        return False


def canonical_remote(url):
    url = url.strip().lower()
    if url.endswith(".git"):
        url = url[:-4]
    return url


def verify_source(source):
    source = pathlib.Path(source).resolve(strict=True)
    if not source.is_dir():
        raise BuildError("libASPL source is not a directory: {}".format(source))

    top = pathlib.Path(run(["git", "-C", source, "rev-parse", "--show-toplevel"], capture=True)).resolve()
    if top != source:
        raise BuildError("--source must name the clean libASPL clone root")

    head = run(["git", "-C", source, "rev-parse", "HEAD"], capture=True)
    if head != PINNED_COMMIT:
        raise BuildError(
            "libASPL must be at {}; found {}".format(PINNED_COMMIT, head)
        )

    status = run(
        ["git", "-C", source, "status", "--porcelain", "--untracked-files=all"],
        capture=True,
    )
    if status:
        raise BuildError("refusing dirty or untracked libASPL source: {}".format(source))

    origin = run(["git", "-C", source, "remote", "get-url", "origin"], capture=True)
    if canonical_remote(origin) not in {
        "https://github.com/gavv/libaspl",
        "http://github.com/gavv/libaspl",
        "git@github.com:gavv/libaspl",
        "ssh://git@github.com/gavv/libaspl",
    }:
        raise BuildError("libASPL origin is unexpected: {}".format(origin))

    for name in LICENSE_FILES:
        path = source / name
        if not path.is_file() or path.stat().st_size == 0:
            raise BuildError("required upstream notice is missing: {}".format(name))
        run(["git", "-C", source, "ls-files", "--error-unmatch", name], capture=True)

    cpp_sources = sorted((source / "src").glob("*.cpp"))
    if not cpp_sources:
        raise BuildError("pinned libASPL clone contains no src/*.cpp files")
    for path in cpp_sources:
        if not path.is_file() or path.is_symlink():
            raise BuildError("unexpected libASPL source entry: {}".format(path))
    return source, cpp_sources


def cached_source():
    cache_base = pathlib.Path.home() / "Library" / "Caches" / "dev.mimac.audio-compat" / "libASPL"
    pinned = cache_base / PINNED_COMMIT
    if pinned.is_symlink():
        raise BuildError("refusing symlink at pinned dependency cache path: {}".format(pinned))
    if pinned.exists():
        return verify_source(pinned)[0]

    cache_root = (pathlib.Path.home() / "Library" / "Caches").resolve()
    resolved_base = cache_base.resolve(strict=False)
    if not is_inside(resolved_base, cache_root):
        raise BuildError("dependency cache path resolves outside ~/Library/Caches")
    cache_base.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix=".libASPL-stage-", dir=str(cache_base)) as temp:
        candidate = pathlib.Path(temp) / "libASPL"
        run(["git", "clone", "--no-checkout", "--filter=blob:none", LIBASPL_REMOTE, candidate])
        run(["git", "-C", candidate, "fetch", "--no-tags", "--depth=1", "origin", PINNED_COMMIT])
        run(["git", "-C", candidate, "checkout", "--detach", PINNED_COMMIT])
        verify_source(candidate)

        if pinned.exists() or pinned.is_symlink():
            if pinned.is_symlink():
                raise BuildError("refusing symlink at pinned dependency cache path")
            return verify_source(pinned)[0]
        os.rename(candidate, pinned)

    return verify_source(pinned)[0]


def reset_build_dir():
    build_parent = BUILD.parent
    if build_parent.is_symlink() or BUILD.is_symlink():
        raise BuildError("refusing symlink in .build/audio-compat output path")
    build_parent.mkdir(parents=True, exist_ok=True)
    if BUILD.exists():
        shutil.rmtree(BUILD)
    BUILD.mkdir(parents=True)


def require_tool(name):
    path = shutil.which(name)
    if path is None:
        raise BuildError("required tool not found: {}".format(name))
    return path


def resolve_architecture(requested, host_machine):
    host_native = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "x86_64", "amd64": "x86_64"}.get(host_machine)
    if host_native is None:
        raise BuildError("unsupported native macOS architecture: {}".format(host_machine))
    host_go = NATIVE_TO_GO_ARCH[host_native]
    target_go = requested or host_go
    if target_go not in GO_TO_NATIVE_ARCH:
        raise BuildError("unsupported target architecture: {}".format(target_go))
    return target_go, GO_TO_NATIVE_ARCH[target_go], host_go, host_native


def bundle_test_execution(target_native, host_native, rosetta_available=False):
    if target_native == host_native:
        return "native", target_native
    if target_native == "x86_64" and host_native == "arm64" and rosetta_available:
        return "rosetta", "x86_64"
    return "not-run", None


def can_run_x86_64_with_rosetta():
    if not pathlib.Path("/usr/bin/arch").is_file():
        return False
    result = subprocess.run(
        ["/usr/bin/arch", "-x86_64", "/bin/true"],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def sha256(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def copy_upstream_notices(source, resources):
    resources.mkdir(parents=True, exist_ok=True)
    for name in LICENSE_FILES:
        shutil.copyfile(source / name, resources / ("libASPL-" + name))


def build_package(bundle, manifest, bundle_abi_ran):
    package = BUILD / "MiMacAudio-local-test.pkg"
    component_plist = BUILD / "component.plist"
    with tempfile.TemporaryDirectory(prefix="audio-compat-package-", dir=str(BUILD)) as temporary:
        staging = pathlib.Path(temporary)
        payload = staging / "payload"
        destination = payload / "Library" / "Audio" / "Plug-Ins" / "HAL" / "MiMacAudio.driver"
        destination.parent.mkdir(parents=True)
        shutil.copytree(bundle, destination, copy_function=shutil.copy2)

        run(["pkgbuild", "--analyze", "--root", payload, component_plist])
        components = plistlib.loads(component_plist.read_bytes())
        if len(components) != 1:
            raise BuildError("expected one driver bundle in local package payload")
        components[0]["BundleIsRelocatable"] = False
        components[0]["BundleHasStrictIdentifier"] = True
        components[0]["BundleOverwriteAction"] = "upgrade"
        component_plist.write_bytes(plistlib.dumps(components))

        package_env = dict(os.environ)
        package_env["COPYFILE_DISABLE"] = "1"
        run(
            [
                "pkgbuild",
                "--root", payload,
                "--component-plist", component_plist,
                "--identifier", BUNDLE_ID + ".pkg",
                "--version", BUNDLE_VERSION,
                "--ownership", "recommended",
                "--install-location", "/",
                package,
            ],
            env=package_env,
        )

    payload_files = {
        line[2:] if line.startswith("./") else line
        for line in run(["pkgutil", "--payload-files", package], capture=True).splitlines()
    }
    required_paths = (
        "Library/Audio/Plug-Ins/HAL/MiMacAudio.driver/Contents/Info.plist",
        "Library/Audio/Plug-Ins/HAL/MiMacAudio.driver/Contents/MacOS/MiMacAudio",
    ) + tuple(
        "Library/Audio/Plug-Ins/HAL/MiMacAudio.driver/Contents/Resources/libASPL-" + name
        for name in LICENSE_FILES
    )
    if not all(path in payload_files for path in required_paths):
        raise BuildError("local package is missing the bundle or upstream notices")

    # Validate the deliverable itself; an old staging bundle must never pass
    # because the separately built binary happened to pass its ABI checks.
    with tempfile.TemporaryDirectory(prefix="audio-compat-verify-") as temporary:
        expanded = pathlib.Path(temporary) / "expanded"
        run(["pkgutil", "--expand-full", package, expanded], quiet=True)
        installed_bundle = expanded / "Payload/Library/Audio/Plug-Ins/HAL/MiMacAudio.driver"
        installed_binary = installed_bundle / "Contents/MacOS/MiMacAudio"
        if sha256(installed_binary) != manifest["bundle"]["binary_sha256"]:
            raise BuildError("package binary differs from the tested bundle")
        run(["codesign", "--verify", "--deep", "--strict", installed_bundle], quiet=True)
        if bundle_abi_ran:
            run([BUILD / "BundleTest", installed_binary])
        for name in LICENSE_FILES:
            relative = pathlib.Path("Contents/Resources") / ("libASPL-" + name)
            if (installed_bundle / relative).read_bytes() != (bundle / relative).read_bytes():
                raise BuildError("package changed upstream notice: " + name)
        metadata = ET.parse(expanded / "PackageInfo").getroot()
        if (expanded / "Scripts").exists() or metadata.get("postinstall-action") != "none":
            raise BuildError("local test package must not run scripts or restart services")

    manifest["package"] = {
        "path": str(package.relative_to(ROOT)),
        "bytes": package.stat().st_size,
        "sha256": sha256(package),
        "install_path": "/Library/Audio/Plug-Ins/HAL/MiMacAudio.driver",
        "postinstall_scripts": False,
        "restarts_audio": False,
        "installed": False,
        "payload_signature_hash_abi_verified": bool(bundle_abi_ran),
    }
    return package


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--arch",
        choices=tuple(GO_TO_NATIVE_ARCH),
        help="build for this Go architecture (defaults to the native host architecture)",
    )
    parser.add_argument(
        "--source",
        type=pathlib.Path,
        help="reuse an external, clean libASPL clone at the pinned commit",
    )
    parser.add_argument(
        "--package",
        action="store_true",
        help="also generate a local-test .pkg under .build/audio-compat (never install it)",
    )
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)

    if sys.platform != "darwin":
        raise BuildError("audio-compat is a macOS-only AudioServerPlugIn")
    if not COMPONENT.is_dir():
        raise BuildError("component sources are missing: {}".format(COMPONENT))

    if args.source is None:
        source = cached_source()
    else:
        source, _ = verify_source(args.source)

    if is_inside(source, ROOT):
        raise BuildError("libASPL must stay outside the product repository")
    if is_inside(source, BUILD) or is_inside(BUILD, source):
        raise BuildError("dependency source and build output must not overlap")

    clang = require_tool("clang++")
    libtool = require_tool("libtool")
    codesign = require_tool("codesign")
    nm = require_tool("nm")
    lipo = require_tool("lipo")
    otool = require_tool("otool")
    xcrun = require_tool("xcrun")
    if args.package:
        require_tool("pkgbuild")
        require_tool("pkgutil")

    target_go_arch, target_native_arch, host_go_arch, host_native_arch = resolve_architecture(args.arch, platform.machine())
    sdk_path = run([xcrun, "--sdk", "macosx", "--show-sdk-path"], capture=True)
    sdk_version = run([xcrun, "--sdk", "macosx", "--show-sdk-version"], capture=True)
    compiler_version = run([clang, "--version"], capture=True).splitlines()[0]

    reset_build_dir()
    source, cpp_sources = verify_source(source)
    objects_dir = BUILD / "objects"
    objects_dir.mkdir()

    common_flags = [
        "-std=c++17",
        "-fblocks",
        "-O2",
        "-fPIC",
        "-fvisibility=hidden",
        "-Wall",
        "-Wextra",
        "-Wno-invalid-offsetof",
        "-Wno-unused-parameter",
        "-Wno-reorder-init-list",
        "-arch", target_native_arch,
        "-isysroot", sdk_path,
        "-mmacosx-version-min=" + MIN_MACOS,
        "-ffile-prefix-map={}=libASPL".format(source),
        "-ffile-prefix-map={}=components/audio-compat".format(COMPONENT),
        "-I", source / "include",
        "-I", COMPONENT,
    ]

    names = [path.stem for path in cpp_sources]
    if len(names) != len(set(names)):
        raise BuildError("libASPL has colliding src/*.cpp basenames")

    def compile_dependency(path):
        output = objects_dir / ("aspl-" + path.stem + ".o")
        run([clang, *common_flags, "-c", path, "-o", output], quiet=True)
        return output

    print("Building pinned libASPL {} ({} translation units)".format(PINNED_COMMIT, len(cpp_sources)))
    with concurrent.futures.ThreadPoolExecutor(max_workers=min(4, os.cpu_count() or 1)) as pool:
        dependency_objects = list(pool.map(compile_dependency, cpp_sources))

    driver_object = objects_dir / "mimac-Driver.o"
    run([clang, *common_flags, "-c", COMPONENT / "Driver.cpp", "-o", driver_object], quiet=True)

    static_library = BUILD / "libASPL.a"
    run([libtool, "-static", "-o", static_library, *dependency_objects], quiet=True)

    bundle = BUILD / "MiMacAudio.driver"
    executable = bundle / "Contents" / "MacOS" / BUNDLE_NAME
    resources = bundle / "Contents" / "Resources"
    executable.parent.mkdir(parents=True)
    resources.mkdir(parents=True)

    run(
        [
            clang,
            "-bundle",
            "-arch", target_native_arch,
            "-isysroot", sdk_path,
            "-mmacosx-version-min=" + MIN_MACOS,
            driver_object,
            static_library,
            "-framework", "CoreFoundation",
            "-framework", "CoreAudio",
            "-o", executable,
        ],
        quiet=True,
    )

    info = {
        "CFBundleDevelopmentRegion": "en",
        "CFBundleExecutable": BUNDLE_NAME,
        "CFBundleIdentifier": BUNDLE_ID,
        "CFBundleInfoDictionaryVersion": "6.0",
        "CFBundleName": "MiMac Audio",
        "CFBundlePackageType": "BNDL",
        "CFBundleShortVersionString": BUNDLE_VERSION,
        "CFBundleSupportedPlatforms": ["MacOSX"],
        "CFBundleVersion": "1",
        "CFPlugInFactories": {FACTORY_UUID: "MiMacAudioFactory"},
        "CFPlugInTypes": {PLUGIN_TYPE_UUID: [FACTORY_UUID]},
        "AudioServerPlugIn_MachServices": [],
        "sandboxSafe": True,
    }
    (bundle / "Contents" / "Info.plist").write_bytes(plistlib.dumps(info))
    copy_upstream_notices(source, resources)

    run([codesign, "--force", "--sign", "-", "--timestamp=none", "--identifier", BUNDLE_ID, bundle], quiet=True)
    run([codesign, "--verify", "--deep", "--strict", bundle], quiet=True)

    binary_architectures = run([lipo, "-archs", executable], capture=True).split()
    if binary_architectures != [target_native_arch]:
        raise BuildError(
            "built bundle architecture is {}; expected only {}".format(
                ",".join(binary_architectures), target_native_arch
            )
        )
    run([lipo, executable, "-verify_arch", target_native_arch], quiet=True)

    exported_symbols = run([nm, "-gU", executable], capture=True)
    if "_MiMacAudioFactory" not in exported_symbols:
        raise BuildError("bundle does not export MiMacAudioFactory")
    linked_libraries = run([otool, "-L", executable], capture=True).splitlines()[1:]
    for line in linked_libraries:
        name = line.strip().split(" (compatibility version", 1)[0]
        if not name.startswith(("/System/Library/", "/usr/lib/")):
            raise BuildError("bundle has a non-system dynamic dependency: {}".format(name))
        if str(source) in name:
            raise BuildError("bundle has a runtime dependency on libASPL source")

    ring_test = BUILD / "FrameRingTest"
    run(
        [
            clang,
            "-std=c++17",
            "-Wall", "-Wextra", "-Wpedantic", "-Werror",
            "-arch", host_native_arch,
            "-isysroot", sdk_path,
            "-mmacosx-version-min=" + MIN_MACOS,
            "-pthread",
            "-I", COMPONENT,
            COMPONENT / "FrameRingTest.cpp",
            "-o", ring_test,
        ],
        quiet=True,
    )
    run([ring_test])

    rosetta_available = can_run_x86_64_with_rosetta() if host_native_arch == "arm64" and target_native_arch == "x86_64" else False
    bundle_test_mode, bundle_test_arch = bundle_test_execution(
        target_native_arch, host_native_arch, rosetta_available
    )
    bundle_test_ran = bundle_test_mode != "not-run"
    if bundle_test_ran:
        bundle_test = BUILD / "BundleTest"
        run(
            [
                clang,
                "-std=c++17",
                "-Wall", "-Wextra",
                "-arch", bundle_test_arch,
                "-isysroot", sdk_path,
                "-mmacosx-version-min=" + MIN_MACOS,
                "-I", COMPONENT,
                COMPONENT / "BundleTest.cpp",
                "-framework", "CoreFoundation",
                "-framework", "CoreAudio",
                "-o", bundle_test,
            ],
            quiet=True,
        )
        if bundle_test_mode == "rosetta":
            run(["/usr/bin/arch", "-x86_64", bundle_test, executable])
        else:
            run([bundle_test, executable])

    source, _ = verify_source(source)
    binary_hash = sha256(executable)
    manifest = {
        "dependency": {
            "name": "libASPL",
            "remote": LIBASPL_REMOTE,
            "commit": PINNED_COMMIT,
        },
        "build": {
            "architecture": target_go_arch,
            "native_architecture": target_native_arch,
            "runner_architecture": host_go_arch,
            "minimum_macos": MIN_MACOS,
            "sdk_version": sdk_version,
            "compiler": compiler_version,
            "source_translation_units": len(cpp_sources),
            "static_library_bytes": static_library.stat().st_size,
        },
        "bundle": {
            "path": str(bundle.relative_to(ROOT)),
            "identifier": BUNDLE_ID,
            "factory_symbol": "MiMacAudioFactory",
            "binary_bytes": executable.stat().st_size,
            "binary_sha256": binary_hash,
            "runtime_dependencies": [
                line.strip().split(" (compatibility version", 1)[0]
                for line in linked_libraries
            ],
            "notices": ["libASPL-" + name for name in LICENSE_FILES],
        },
        "tests": {
            "frame_ring": {"status": "passed", "runner_architecture": host_go_arch},
            "bundle_abi": {
                "status": "passed" if bundle_test_ran else "not_run_cross_arch",
                "mode": bundle_test_mode,
                "runner_architecture": bundle_test_arch if bundle_test_ran else host_native_arch,
            },
            "bundle_architecture": "passed",
            "factory_symbol": "passed",
            "codesign": "passed",
        },
        "installed": False,
        "audio_service_restarted": False,
    }
    if args.package:
        build_package(bundle, manifest, bundle_test_ran)

    manifest_path = BUILD / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    print(json.dumps(manifest, indent=2, sort_keys=True))
    if bundle_test_ran:
        print("Build and architecture checks passed; Bundle ABI test ran in {} mode. Nothing was installed; audio services were not restarted.".format(bundle_test_mode))
    else:
        print("Build and architecture checks passed; Bundle ABI was not runtime-tested for this cross-architecture target. Nothing was installed; audio services were not restarted.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (BuildError, subprocess.CalledProcessError, OSError) as error:
        print("audio-compat build failed: {}".format(error), file=sys.stderr)
        raise SystemExit(1)
