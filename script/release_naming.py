#!/usr/bin/env python3
"""Resolve preview identity and short architecture-specific artifact names."""

import argparse

from release_version import preview_identity

ARCHITECTURE_LABELS = {"arm64": "arm64", "amd64": "x86_64"}


def preview_stem(arch):
    if arch not in ARCHITECTURE_LABELS:
        raise ValueError(f"unsupported architecture: {arch}")
    return f"MiMacPreview-{ARCHITECTURE_LABELS[arch]}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    identity = commands.add_parser("identity")
    identity.add_argument("date")
    identity.add_argument("revision", nargs="?", default="")
    stem = commands.add_parser("stem")
    stem.add_argument("arch", choices=ARCHITECTURE_LABELS)
    args = parser.parse_args()
    try:
        print(preview_identity(args.date, args.revision) if args.command == "identity" else preview_stem(args.arch))
    except ValueError as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
