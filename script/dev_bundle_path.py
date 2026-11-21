"""Resolve the one stable local output path for a MiMac Dev checkout."""

import hashlib
import os
from pathlib import Path


DEV_OUTPUT_SUFFIX = Path(".mygo") / "dev"
DEV_BUNDLE_NAME = "MiMac Dev.app"
PROTECTED_SOURCE_DIRECTORIES = ("Desktop", "Documents", "Downloads")


def development_output_root(project_root, home=None):
    """Keep Dev builds from protected user folders in a stable ordinary cache path."""
    root = Path(project_root).expanduser().resolve(strict=False)
    home_path = Path(home) if home is not None else Path.home()
    home_path = home_path.expanduser().resolve(strict=False)

    protected = []
    for name in PROTECTED_SOURCE_DIRECTORIES:
        path = home_path / name
        try:
            protected.append(path.resolve(strict=False))
        except (OSError, RuntimeError):
            protected.append(path)

    if not any(_is_relative_to(root, directory) for directory in protected):
        return root / DEV_OUTPUT_SUFFIX

    checkout_key = hashlib.sha256(os.fsencode(os.fspath(root))).hexdigest()
    return (
        home_path
        / "Library"
        / "Caches"
        / "MiMac"
        / "dev-builds"
        / checkout_key
        / DEV_OUTPUT_SUFFIX
    )


def development_bundle_path(project_root, architecture, home=None):
    if architecture not in ("arm64", "amd64"):
        raise ValueError(f"unsupported Dev architecture: {architecture}")
    return development_output_root(project_root, home) / f"darwin-{architecture}" / DEV_BUNDLE_NAME


def legacy_development_bundle_path(project_root, architecture):
    """Return the former in-checkout path for validating a pre-migration receipt."""
    if architecture not in ("arm64", "amd64"):
        raise ValueError(f"unsupported Dev architecture: {architecture}")
    root = Path(project_root).expanduser().resolve(strict=False)
    return root / DEV_OUTPUT_SUFFIX / f"darwin-{architecture}" / DEV_BUNDLE_NAME


def _is_relative_to(path, parent):
    try:
        path.relative_to(parent)
        return True
    except ValueError:
        return False
