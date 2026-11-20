#!/usr/bin/env python3
"""Resolve channel identity and short architecture-specific artifact names."""

import argparse

from release_version import build_date, channel_identity

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
    commands.add_parser("date")
    identity = commands.add_parser("identity")
    identity.add_argument("date")
    identity.add_argument("revision", nargs="?", default="")
    identity.add_argument("--channel", choices=("dev", "preview", "stable"), default="preview")
    stem = commands.add_parser("stem")
    stem.add_argument("arch", choices=ARCHITECTURE_LABELS)
    stem.add_argument("--channel", choices=ARTIFACT_PREFIXES, default="preview")
    args = parser.parse_args()
    try:
        if args.command == "date":
            print(build_date())
        elif args.command == "identity":
            print(channel_identity(args.date, args.channel, args.revision))
        else:
            print(artifact_stem(args.channel, args.arch))
    except ValueError as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
