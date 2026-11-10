#!/usr/bin/env python3
"""Reject incomplete native bundles before launching or packaging MiMac."""
import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import subprocess

from package_audio_component import verify_product_package


REQUIRED = {
    "libMiMacSettings.dylib": "MiMacPreferencesCreate",
    "libMiMacATVV.dylib": "MiMacATVVTransportCreate",
    "libMiMacProcessTap.dylib": "MiMacProcessTapCreate",
    "libMiMacReadyIndicator.dylib": "MiMacReadyIndicatorABIVersion",
    "libMiMacSpeech.dylib": "MiMacAppleSpeechIsAvailable",
}
SIRI = "libSiriGlassBridge.dylib"


def verify_audio_installer(resources, arch, required=False):
    resources = Path(resources)
    installer = resources / "MiMacAudio.pkg"
    if not installer.is_file():
        if required:
            raise ValueError("missing required MiMac audio installer")
        return None
    digest = hashlib.sha256(installer.read_bytes()).hexdigest()
    expected = (resources / "MiMacAudio.pkg.sha256").read_text(encoding="ascii").split()
    if expected != [digest, installer.name]:
        raise ValueError("optional audio installer checksum mismatch")
    if not required:
        return {"sha256": digest}
    receipt = resources / "MiMacAudio.pkg.json"
    verified = verify_product_package(installer, receipt, arch)
    return {**verified, "sha256": digest}


def verify(bundle, arch, require_siri=False, development=False, require_audio_installer=False):
    contents = bundle / "Contents"
    with (contents / "Info.plist").open("rb") as file:
        info = plistlib.load(file)
    expected_id = "dev.mimac.app.dev" if development else "dev.mimac.app"
    if info.get("CFBundleIdentifier") != expected_id:
        raise ValueError("expected MiMac bundle identifier " + expected_id)
    if development and info.get("CFBundleName") != "MiMac Dev":
        raise ValueError("expected development bundle name MiMac Dev")
    executable = contents / "MacOS" / info["CFBundleExecutable"]
    resources = contents / "Resources"
    libraries = dict(REQUIRED)
    if require_siri or (resources / SIRI).exists():
        libraries[SIRI] = "mimac_siri_glass_abi_version"
    missing = [name for name in libraries if not (resources / name).is_file()]
    if missing:
        raise ValueError("missing native libraries: " + ", ".join(missing))
    native_arch = "x86_64" if arch == "amd64" else arch
    files = [executable] + [resources / name for name in libraries]
    hashes = {}
    for path in files:
        # Apple's lipo consumes every argument after -verify_arch as an arch.
        # Put the input first; LLVM lipo also accepts this canonical ordering.
        subprocess.run(["/usr/bin/lipo", str(path), "-verify_arch", native_arch], check=True)
        hashes[str(path.relative_to(contents))] = hashlib.sha256(path.read_bytes()).hexdigest()
    for name, symbol in libraries.items():
        exports = subprocess.check_output(["/usr/bin/nm", "-gU", str(resources / name)], text=True)
        if name == SIRI and not any(line.split()[-1:] == ["_mimac_siri_glass_update_frame"] for line in exports.splitlines()):
            raise ValueError("missing typed frame export in " + name)
        if not any(line.split()[-1:] == ["_" + symbol] for line in exports.splitlines()):
            raise ValueError(f"missing export {symbol} in {name}")
    audio_installer = verify_audio_installer(resources, arch, require_audio_installer)
    if audio_installer is not None:
        installer = resources / "MiMacAudio.pkg"
        hashes[str(installer.relative_to(contents))] = audio_installer["sha256"]
        hashes[str((resources / "MiMacAudio.pkg.sha256").relative_to(contents))] = hashlib.sha256((resources / "MiMacAudio.pkg.sha256").read_bytes()).hexdigest()
        if require_audio_installer:
            receipt = resources / "MiMacAudio.pkg.json"
            hashes[str(receipt.relative_to(contents))] = hashlib.sha256(receipt.read_bytes()).hexdigest()
    subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(bundle)], check=True)
    return {"bundle": str(bundle.resolve()), "arch": arch, "native_libraries": len(libraries), "sha256": hashes, "audio_installer": audio_installer}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--arch", choices=["arm64", "amd64"], required=True)
    parser.add_argument("--require-siri", action="store_true")
    parser.add_argument("--development", action="store_true")
    parser.add_argument("--require-audio-installer", action="store_true")
    args = parser.parse_args()
    try:
        result = verify(args.bundle, args.arch, args.require_siri, args.development, args.require_audio_installer)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"MiMac bundle verification failed: {error}\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
