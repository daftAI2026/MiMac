#!/usr/bin/env python3
"""Resolve channel identity and short architecture-specific artifact names."""

import argparse
from pathlib import Path

from release_version import (
    build_train,
    bundle_version,
    channel_identity,
    next_local_preview_revision,
    reserve_preview_directory,
)

ARCHITECTURE_LABELS = {"arm64": "arm64", "amd64": "x86_64"}
ARTIFACT_PREFIXES = {"preview": "MiMacPreview", "stable": "MiMac"}


def artifact_stem(channel, arch):
    if channel not in ARTIFACT_PREFIXES:
        raise ValueError(f"{channel} has no distribution artifacts")
    if arch not in ARCHITECTURE_LABELS:
        raise ValueError(f"unsupported architecture: {arch}")
    return f"{ARTIFACT_PREFIXES[channel]}-{ARCHITECTURE_LABELS[arch]}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("train")
    identity = commands.add_parser("identity")
    identity.add_argument("train")
    identity.add_argument("revision", nargs="?", default="00")
    identity.add_argument("--channel", choices=("dev", "preview", "stable"), default="preview")
    numeric = commands.add_parser("bundle-version")
    numeric.add_argument("identity")
    next_preview = commands.add_parser("next-preview-revision")
    next_preview.add_argument("train")
    next_preview.add_argument("output_root", type=Path)
    reserve = commands.add_parser("reserve-preview")
    reserve.add_argument("output_root", type=Path)
    reserve.add_argument("identity")
    stem = commands.add_parser("stem")
    stem.add_argument("arch", choices=ARCHITECTURE_LABELS)
    stem.add_argument("--channel", choices=ARTIFACT_PREFIXES, default="preview")
    args = parser.parse_args()
    try:
        if args.command == "train":
            print(build_train())
        elif args.command == "identity":
            print(channel_identity(args.train, args.channel, args.revision))
        elif args.command == "bundle-version":
            print(bundle_version(args.identity))
        elif args.command == "next-preview-revision":
            print(next_local_preview_revision(args.train, args.output_root))
        elif args.command == "reserve-preview":
            print(reserve_preview_directory(args.output_root, args.identity))
        else:
            print(artifact_stem(args.channel, args.arch))
    except (OSError, ValueError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
