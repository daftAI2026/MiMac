#!/usr/bin/env python3
"""Name MiMac artifacts from the verified release identity."""

import argparse
import json
import re
import subprocess

from release_version import validate_build_number

ARCHITECTURE_LABELS = {"arm64": "arm64", "amd64": "x86_64"}
SOURCE_REVISION = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})")
PRODUCT_VERSION = re.compile(r"(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*)){2}")


def validate_revision(revision):
    if not SOURCE_REVISION.fullmatch(revision):
        raise ValueError("expected a full Git source revision")


def source_product_version(revision):
    validate_revision(revision)
    config = json.loads(subprocess.check_output(["git", "show", f"{revision}:mygo.json"], text=True))
    return config["version"]


def artifact_stem(version, build_number, revision, arch=None):
    if not isinstance(version, str) or not PRODUCT_VERSION.fullmatch(version):
        raise ValueError("expected a MAJOR.MINOR.PATCH product version")
    build_number = validate_build_number(build_number)
    validate_revision(revision)
    stem = f"MiMac-{version}-build{build_number}-{revision[:7]}"
    if arch is None:
        return stem
    if arch not in ARCHITECTURE_LABELS:
        raise ValueError(f"unsupported architecture: {arch}")
    return f"{stem}-{ARCHITECTURE_LABELS[arch]}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("build_number")
    parser.add_argument("revision")
    parser.add_argument("--arch", choices=ARCHITECTURE_LABELS)
    args = parser.parse_args()
    try:
        print(artifact_stem(source_product_version(args.revision), args.build_number, args.revision, args.arch))
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
