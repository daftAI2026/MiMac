"""Inspect or reset one proven stale macOS permission registration for MiMac Dev."""

import argparse
import json
from pathlib import Path
import platform
import re
import subprocess

from dev_launch_registration import DEV_BUNDLE_IDENTIFIER, SystemLaunchServices
from dev_bundle_path import development_bundle_path
from verify_app_bundle import verify


SERVICES = {"input-monitoring": "ListenEvent", "accessibility": "Accessibility"}
EVENT_HEADER = re.compile(r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.")
CODE_HASH = re.compile(r'cdhash H"([a-fA-F0-9]{40})"')


def stale_registration(log, service, code_hash):
    """Require a match for this code identity later than any recorded change."""
    service = "kTCCService" + service
    mismatch = f"Failed to match existing code requirement for subject {DEV_BUNDLE_IDENTIFIER} and service {service}"
    change = re.compile(
        rf"TCCDEvent: type=(?:Delete|Modify|Insert), service={service}, "
        rf"identifier_type=Bundle ID, identifier={re.escape(DEV_BUNDLE_IDENTIFIER)}>"
    )
    stale = False
    entry = []
    for line in (*log.splitlines(), "2000-01-01 00:00:00. sentinel"):
        if EVENT_HEADER.match(line):
            message = "\n".join(entry)
            if change.search(message):
                stale = False
            elif entry and entry[0].endswith(mismatch):
                hashes = [value.lower() for value in CODE_HASH.findall(message)]
                stale = len(hashes) == 2 and hashes[1] == code_hash and hashes[0] != code_hash
            entry = []
        entry.append(line)
    return stale


def inspect(bundle, service):
    signed = subprocess.run(
        ["/usr/bin/codesign", "-d", "--verbose=4", bundle],
        check=True, capture_output=True, text=True, timeout=8,
    ).stderr
    code_hash = re.search(r"^CDHash=([0-9a-f]{40})$", signed, re.MULTILINE)
    if code_hash is None:
        raise RuntimeError("cannot identify the verified code signature")
    predicate = (
        'process == "tccd" '
        f'AND eventMessage CONTAINS "{DEV_BUNDLE_IDENTIFIER}" '
        f'AND eventMessage CONTAINS "kTCCService{service}"'
    )
    log = subprocess.check_output(
        ["/usr/bin/log", "show", "--last", "5m", "--style", "compact", "--predicate", predicate],
        text=True, timeout=18,
    )
    return stale_registration(log, service, code_hash[1])


def require_stopped_default(bundle):
    if SystemLaunchServices().default_url(DEV_BUNDLE_IDENTIFIER) != str(bundle):
        raise RuntimeError("Launch Services does not select this fixed Dev bundle")
    script = (
        "ObjC.import('AppKit'); "
        f"$.NSRunningApplication.runningApplicationsWithBundleIdentifier('{DEV_BUNDLE_IDENTIFIER}').count.toString();"
    )
    result = subprocess.check_output(["/usr/bin/osascript", "-l", "JavaScript", "-e", script], text=True, timeout=8)
    if result.strip() != "0":
        raise RuntimeError("quit MiMac Dev before resetting its stale registration")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--permission", choices=SERVICES, required=True)
    parser.add_argument("--apply", action="store_true", help="reset the selected stale Dev registration")
    args = parser.parse_args()
    arch = {"arm64": "arm64", "x86_64": "amd64"}.get(platform.machine())
    if platform.system() != "Darwin" or not arch:
        parser.error("requires macOS on arm64 or amd64")
    root = Path(__file__).resolve().parent.parent
    bundle = development_bundle_path(root, arch)
    if bundle.resolve(strict=True) != bundle:
        raise RuntimeError("fixed Dev bundle must not traverse a symlink")
    verify(bundle, arch, development=True, require_audio_installer=True)
    service = SERVICES[args.permission]
    is_stale = inspect(bundle, service)
    state = "stale_registration" if is_stale else "no_stale_evidence"
    if args.apply and is_stale:
        require_stopped_default(bundle)
        subprocess.run(["/usr/bin/tccutil", "reset", service, DEV_BUNDLE_IDENTIFIER], check=True, timeout=12)
        state = "waiting_for_system_grant"
    print(json.dumps({"status": state, "permission": args.permission, "bundle": str(bundle)}))


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error)) from error
