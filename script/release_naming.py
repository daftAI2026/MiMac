#!/usr/bin/env python3
"""Name MiMac artifacts from the verified release identity."""

import argparse
import re

from release_version import validate_build_number, validate_date

ARCHITECTURE_LABELS = {"arm64": "arm64", "amd64": "x86_64"}


def artifact_stem(version, build_number, revision, arch=None):
    version = validate_date(version)
    build_number = validate_build_number(build_number)
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", revision):
        raise ValueError("expected a full Git source revision")
    stem = f"MiMac-{version}-build{build_number}-{revision[:7]}"
    if arch is None:
        return stem
    if arch not in ARCHITECTURE_LABELS:
        raise ValueError(f"unsupported architecture: {arch}")
    return f"{stem}-{ARCHITECTURE_LABELS[arch]}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("build_number")
    parser.add_argument("revision")
    parser.add_argument("--arch", choices=ARCHITECTURE_LABELS)
    args = parser.parse_args()
    try:
        print(artifact_stem(args.version, args.build_number, args.revision, args.arch))
    except ValueError as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
