#!/usr/bin/env python3
"""Package a verified macOS app bundle in a Finder-styled, read-only DMG."""

import argparse
from dataclasses import dataclass
import math
import os
from pathlib import Path
import plistlib
import platform
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RENDERER = ROOT / "Assets" / "Installer" / "render_background.swift"
BACKGROUND_RELATIVE = Path(".background") / "background.png"
WINDOW_BOUNDS = "{{200, 120}, {600, 360}}"
WINDOW_WIDTH = 600
WINDOW_HEIGHT = 360
BACKGROUND_WIDTH = 1200
BACKGROUND_HEIGHT = 536
ICON_SIZE = 100
TEXT_SIZE = 12
APP_POSITION = (300, 140)
MIB = 1024 * 1024


class PackageError(RuntimeError):
    pass


@dataclass(frozen=True)
class BundleMetadata:
    display_name: str
    version: str
    volume_name: str
    bundle_name: str


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
                result.returncode,
                " ".join(command),
                "\n" + details if details else "",
            )
        )
    return result.stdout.strip() if capture else ""


def resolve_bundle(path):
    bundle = Path(path).expanduser()
    if bundle.is_symlink():
        raise PackageError("app bundle must not be a symlink")
    try:
        bundle = bundle.resolve(strict=True)
    except OSError as error:
        raise PackageError("app bundle does not exist: {}".format(path)) from error
    if not bundle.is_dir() or bundle.suffix != ".app":
        raise PackageError("input must be an existing .app directory")
    info = bundle / "Contents" / "Info.plist"
    if not info.is_file():
        raise PackageError("app bundle has no Contents/Info.plist")
    return bundle, info


def _volume_component(value):
    value = re.sub(r"[/\\:\x00-\x1f\x7f]", "-", value).strip(" .")
    return value or "MiMac"


def read_bundle_metadata(bundle, info_path=None):
    bundle = Path(bundle)
    info_path = Path(info_path) if info_path is not None else bundle / "Contents" / "Info.plist"
    try:
        info = plistlib.loads(info_path.read_bytes())
    except (OSError, plistlib.InvalidFileException, ValueError) as error:
        raise PackageError("cannot read app Info.plist: {}".format(error)) from error
    if not isinstance(info, dict):
        raise PackageError("app Info.plist must contain a dictionary")

    display_name = info.get("CFBundleDisplayName") or info.get("CFBundleName") or bundle.stem
    version = info.get("CFBundleShortVersionString")
    if not isinstance(display_name, str) or not display_name.strip() or "/" in display_name:
        raise PackageError("app Info.plist has no valid display name")
    if not isinstance(version, str) or not version.strip():
        raise PackageError("app Info.plist has no CFBundleShortVersionString")
    display_name = display_name.strip()
    version = version.strip()
    volume_name = _volume_component("{} {}".format(display_name, version))
    return BundleMetadata(display_name, version, volume_name, bundle.name)


def resolve_output(path, bundle):
    bundle = Path(bundle).resolve(strict=True)
    output = Path(path).expanduser()
    if output.name in {"", ".", ".."} or output.suffix.lower() != ".dmg":
        raise PackageError("--output must name a .dmg file")
    if os.path.lexists(output):
        raise PackageError("output already exists; refusing to overwrite: {}".format(output))
    try:
        parent = output.parent.resolve(strict=True)
    except OSError as error:
        raise PackageError("output directory does not exist: {}".format(output.parent)) from error
    if not parent.is_dir():
        raise PackageError("output parent is not a directory: {}".format(parent))
    resolved = parent / output.name
    if resolved == bundle or bundle in resolved.parents:
        raise PackageError("DMG output must be outside the signed app bundle")
    return resolved


def estimated_image_size_mib(bundle):
    total_bytes = 0
    object_count = 0

    def fail_walk(error):
        raise error

    try:
        for directory, directories, files in os.walk(bundle, followlinks=False, onerror=fail_walk):
            for name in directories + files:
                path = Path(directory) / name
                info = path.lstat()
                object_count += 1
                if not path.is_symlink():
                    total_bytes += info.st_size
    except OSError as error:
        raise PackageError("cannot size app bundle: {}".format(error)) from error

    # Sparse app resources become allocated HFS+ files. Leave margin for those
    # blocks, Finder metadata, and the background assets added after mounting.
    required = math.ceil(total_bytes * 1.10) + 64 * MIB + object_count * 4096
    mib = max(64, math.ceil(required / MIB))
    return int(math.ceil(mib / 64) * 64)


def validate_background_png(path):
    try:
        data = Path(path).read_bytes()
    except OSError as error:
        raise PackageError("background renderer did not create its PNG: {}".format(error)) from error
    if len(data) < 24 or data[:8] != b"\x89PNG\r\n\x1a\n" or data[12:16] != b"IHDR":
        raise PackageError("background renderer output is not a PNG")
    width = int.from_bytes(data[16:20], "big")
    height = int.from_bytes(data[20:24], "big")
    if (width, height) != (BACKGROUND_WIDTH, BACKGROUND_HEIGHT):
        raise PackageError(
            "background PNG is {}x{}; expected {}x{}".format(
                width, height, BACKGROUND_WIDTH, BACKGROUND_HEIGHT
            )
        )


def render_background(output, *, runner=run):
    if not RENDERER.is_file():
        raise PackageError("installer background renderer is missing")
    runner(["swift", RENDERER, output])
    validate_background_png(output)


def metadata_dependencies():
    try:
        from ds_store import DSStore
        from mac_alias import Alias
    except ImportError as error:
        raise PackageError(
            "DMG metadata dependencies are missing for {}; install "
            "script/requirements-packaging.txt with this Python interpreter".format(sys.executable)
        ) from error
    return DSStore, Alias


def write_ds_store(path, *, volume_name, app_name, background_path):
    DSStore, Alias = metadata_dependencies()

    try:
        alias = Alias.for_file(str(background_path)).to_bytes()
        bwsp = {
            "ShowStatusBar": False,
            "WindowBounds": WINDOW_BOUNDS,
            "ContainerShowSidebar": False,
            "PreviewPaneVisibility": False,
            "SidebarWidth": 180,
            "ShowTabView": False,
            "ShowToolbar": False,
            "ShowPathbar": False,
            "ShowSidebar": False,
        }
        icvp = {
            "viewOptionsVersion": 1,
            "backgroundType": 2,
            "backgroundImageAlias": alias,
            "gridOffsetX": 0.0,
            "gridOffsetY": 0.0,
            "gridSpacing": 100.0,
            "arrangeBy": "none",
            "showIconPreview": True,
            "showItemInfo": False,
            "labelOnBottom": True,
            "textSize": float(TEXT_SIZE),
            "iconSize": float(ICON_SIZE),
            "backgroundColorRed": 1.0,
            "backgroundColorGreen": 1.0,
            "backgroundColorBlue": 1.0,
        }
        with DSStore.open(str(path), "w+") as store:
            store["."]["vSrn"] = ("long", 1)
            store["."]["bwsp"] = bwsp
            store["."]["icvp"] = icvp
            store["."]["icvl"] = ("type", b"icnv")
            store[app_name]["Iloc"] = APP_POSITION
    except (OSError, ValueError, TypeError, KeyError) as error:
        raise PackageError("cannot write Finder layout: {}".format(error)) from error


def attached_device(output):
    for line in output.splitlines():
        fields = line.split()
        if fields and re.fullmatch(r"/dev/disk\d+", fields[0]):
            return fields[0]
    return None


def diskutil_image_create_supported():
    executable = shutil.which("diskutil")
    if not executable:
        return False
    result = subprocess.run(
        [executable, "image", "create", "from", "-h"],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def _detach_target(target, *, runner):
    if target:
        runner(["hdiutil", "detach", target])


def _mount_image(bundle, output, metadata, *, runner, diskutil_supported, renderer, dsstore_writer, sign_identity):
    image_size = estimated_image_size_mib(bundle)
    with tempfile.TemporaryDirectory(prefix=".mimac-dmg-", dir=output.parent) as temporary:
        work = Path(temporary).resolve()
        rw_image = work / "writable.dmg"
        compressed_image = work / "compressed.dmg"
        mountpoint = work / "mount"
        background = work / "background.png"
        mountpoint.mkdir(mode=0o700)
        renderer(background, runner=runner)

        runner([
            "hdiutil", "create", "-size", "{}m".format(image_size),
            "-volname", metadata.volume_name, "-fs", "HFS+",
            "-type", "UDIF", rw_image,
        ])

        attached_target = None
        attachment_attempted = False
        try:
            attachment_attempted = True
            attach_output = runner([
                "hdiutil", "attach", "-readwrite", "-noverify", "-noautoopen", "-nobrowse",
                "-mountpoint", mountpoint, rw_image,
            ], capture=True)
            attached_target = attached_device(attach_output)
            if not attached_target:
                attached_target = str(mountpoint)
                raise PackageError("hdiutil attach did not report its mounted device")

            app_destination = mountpoint / metadata.bundle_name
            runner([
                "ditto", "--noclone", "--rsrc", "--extattr", "--acl", bundle, app_destination,
            ])
            background_dir = mountpoint / BACKGROUND_RELATIVE.parent
            background_dir.mkdir(mode=0o755)
            background_in_image = mountpoint / BACKGROUND_RELATIVE
            shutil.copyfile(background, background_in_image)
            background_in_image.chmod(0o644)
            dsstore_writer(
                mountpoint / ".DS_Store",
                volume_name=metadata.volume_name,
                app_name=metadata.bundle_name,
                background_path=background_in_image,
            )

            runner(["hdiutil", "detach", attached_target])
            attached_target = None
        except BaseException as error:
            cleanup_target = attached_target or (str(mountpoint) if attachment_attempted else None)
            if cleanup_target:
                try:
                    _detach_target(cleanup_target, runner=runner)
                except Exception as cleanup_error:
                    raise PackageError(
                        "{}; cleanup could not detach {}: {}".format(error, cleanup_target, cleanup_error)
                    ) from error
            raise

        if diskutil_supported:
            runner([
                "diskutil", "image", "create", "from", "--format", "ULMO", rw_image, compressed_image,
            ])
        else:
            runner([
                "hdiutil", "convert", rw_image, "-format", "ULMO", "-o", compressed_image,
            ])

        if sign_identity and sign_identity != "-":
            runner([
                "codesign", "--force", "--timestamp", "--sign", sign_identity, compressed_image,
            ])

        try:
            os.link(compressed_image, output)
        except FileExistsError as error:
            raise PackageError("output already exists; refusing to overwrite: {}".format(output)) from error
        except OSError as error:
            raise PackageError("cannot place completed DMG at {}: {}".format(output, error)) from error
    return output


def required_tools(sign_identity=None):
    tools = ["hdiutil", "ditto", "swift"]
    if sign_identity and sign_identity != "-":
        tools.append("codesign")
    missing = [tool for tool in tools if shutil.which(tool) is None]
    if missing:
        raise PackageError("required macOS tools are missing: " + ", ".join(missing))


def package_dmg(bundle_path, output_path, *, sign_identity=None):
    if platform.system() != "Darwin":
        raise PackageError("Finder-styled DMGs can only be packaged on macOS")
    bundle, info_path = resolve_bundle(bundle_path)
    metadata = read_bundle_metadata(bundle, info_path)
    output = resolve_output(output_path, bundle)
    required_tools(sign_identity)
    metadata_dependencies()
    return _mount_image(
        bundle,
        output,
        metadata,
        runner=run,
        diskutil_supported=diskutil_image_create_supported(),
        renderer=render_background,
        dsstore_writer=write_ds_store,
        sign_identity=sign_identity,
    )


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path, help="verified .app bundle to package")
    parser.add_argument("--output", type=Path, required=True, help="new output .dmg path (must not exist)")
    parser.add_argument("--sign-identity", help="optional code-signing identity for the completed DMG")
    args = parser.parse_args(argv)
    try:
        result = package_dmg(args.bundle, args.output, sign_identity=args.sign_identity)
    except (OSError, PackageError, subprocess.SubprocessError) as error:
        parser.exit(1, "MiMac DMG packaging failed: {}\n".format(error))
    print(result)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
