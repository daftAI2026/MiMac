#!/usr/bin/env python3
"""Reject incomplete native bundles before launching or packaging MiMac."""
import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import subprocess


REQUIRED = {
    "libMiMacSettings.dylib": "MiMacPreferencesCreate",
    "libMiMacATVV.dylib": "MiMacATVVTransportCreate",
    "libMiMacProcessTap.dylib": "MiMacProcessTapCreate",
    "libMiMacReadyIndicator.dylib": "MiMacReadyIndicatorABIVersion",
    "libMiMacSpeech.dylib": "MiMacAppleSpeechIsAvailable",
}
SIRI = "libSiriGlassBridge.dylib"


def verify(bundle, arch, require_siri=False):
    contents = bundle / "Contents"
    with (contents / "Info.plist").open("rb") as file:
        info = plistlib.load(file)
    if info.get("CFBundleIdentifier") != "dev.mimac.app":
        raise ValueError("expected MiMac bundle identifier dev.mimac.app")
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
        subprocess.run(["/usr/bin/lipo", "-verify_arch", native_arch, str(path)], check=True)
        hashes[str(path.relative_to(contents))] = hashlib.sha256(path.read_bytes()).hexdigest()
    for name, symbol in libraries.items():
        exports = subprocess.check_output(["/usr/bin/nm", "-gU", str(resources / name)], text=True)
        if not any(line.split()[-1:] == ["_" + symbol] for line in exports.splitlines()):
            raise ValueError(f"missing export {symbol} in {name}")
    subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(bundle)], check=True)
    return {"bundle": str(bundle.resolve()), "arch": arch, "native_libraries": len(libraries), "sha256": hashes}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--arch", choices=["arm64", "amd64"], required=True)
    parser.add_argument("--require-siri", action="store_true")
    args = parser.parse_args()
    try:
        result = verify(args.bundle, args.arch, args.require_siri)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"MiMac bundle verification failed: {error}\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
