#!/usr/bin/env python3
"""Reject duplicate MiMac bundle identities before building or launching."""

import argparse
import json
import os
from pathlib import Path
import platform
import plistlib


BUNDLE_IDENTIFIER = "dev.mimac.app"
PRUNED_DIRECTORY_NAMES = {
    ".build",
    ".git",
    ".toolchains",
    "node_modules",
    "reference",
    "references",
    "third-party",
    "third_party",
    "vendor",
    "vendors",
}
GO_ARCHES = {"arm64": "arm64", "aarch64": "arm64", "amd64": "amd64", "x86_64": "amd64"}


class BundleInspectionError(ValueError):
    """A candidate app bundle could not be identified from its Info.plist."""


class IdentityConflict(ValueError):
    """One or more target-ID app bundles exist outside the allowed dist path."""


def go_arch(machine=None):
    machine = (machine or platform.machine()).lower()
    try:
        return GO_ARCHES[machine]
    except KeyError as error:
        raise ValueError(f"cannot infer Go architecture from {machine!r}; pass --arch") from error


def default_research_root(project_root):
    project_root = Path(project_root).resolve()
    return project_root.parent / f"{project_root.name}-research"


def iter_app_bundles(root):
    """Yield .app directories while pruning bundles and irrelevant large trees."""
    root = Path(root)
    if root.name.lower().endswith(".app"):
        if root.is_dir():
            yield root
        return

    def fail_walk(error):
        raise error

    for current, directories, _ in os.walk(root, topdown=True, onerror=fail_walk, followlinks=False):
        current_path = Path(current)
        descend = []
        for name in sorted(directories):
            lowered = name.lower()
            if lowered.endswith(".app"):
                candidate = current_path / name
                if candidate.is_dir():
                    yield candidate
                # An app bundle owns its Contents tree; do not inspect nested
                # app-looking resources or descend through its internals.
                continue
            if lowered in PRUNED_DIRECTORY_NAMES:
                continue
            descend.append(name)
        directories[:] = descend


def bundle_identifier(bundle):
    info_path = Path(bundle) / "Contents" / "Info.plist"
    try:
        with info_path.open("rb") as file:
            info = plistlib.load(file)
    except Exception as error:
        raise BundleInspectionError(f"cannot read {info_path}: {error}") from error
    if not isinstance(info, dict):
        raise BundleInspectionError(f"{info_path} does not contain a plist dictionary")
    return info.get("CFBundleIdentifier")


def check_app_identity(project_root, research_root=None, arch=None):
    project_root = Path(project_root).resolve()
    if not project_root.is_dir():
        raise ValueError(f"project root is not a directory: {project_root}")
    if research_root is None:
        research_root = default_research_root(project_root)
    research_root = Path(research_root).resolve()
    if research_root.exists() and not research_root.is_dir():
        raise ValueError(f"research root is not a directory: {research_root}")
    arch = arch or go_arch()
    if arch not in {"arm64", "amd64"}:
        raise ValueError(f"unsupported Go architecture {arch!r}; expected arm64 or amd64")

    allowed_bundle = project_root / "dist" / f"darwin-{arch}" / "MiMac.app"
    roots = [project_root]
    if research_root.exists() and research_root != project_root:
        roots.append(research_root)

    matches = []
    seen = set()
    for root in roots:
        for bundle in iter_app_bundles(root):
            bundle = bundle.resolve()
            if bundle in seen:
                continue
            seen.add(bundle)
            if bundle_identifier(bundle) == BUNDLE_IDENTIFIER:
                matches.append(bundle)

    allowed_bundle = allowed_bundle.resolve()
    conflicts = [bundle for bundle in matches if bundle != allowed_bundle]
    if conflicts:
        details = "\n".join(f"  - {path}" for path in sorted(matches))
        raise IdentityConflict(
            f"found {BUNDLE_IDENTIFIER} outside the allowed current dist bundle; "
            f"allowed path: {allowed_bundle}\nmatching app paths:\n{details}"
        )

    return {
        "bundle_identifier": BUNDLE_IDENTIFIER,
        "allowed_bundle": str(allowed_bundle),
        "matching_apps": [str(path) for path in sorted(matches)],
        "project_root": str(project_root),
        "research_root": str(research_root),
        "research_root_exists": research_root.exists(),
    }


def main(argv=None):
    default_project = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project-root", type=Path, default=default_project)
    parser.add_argument("--research-root", type=Path, help="defaults to the sibling <project>-research directory")
    parser.add_argument("--arch", choices=("arm64", "amd64"), help="Go architecture; inferred from this host by default")
    args = parser.parse_args(argv)

    try:
        result = check_app_identity(args.project_root, args.research_root, args.arch)
    except (OSError, ValueError) as error:
        parser.exit(1, f"App identity check failed: {error}\n")
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
