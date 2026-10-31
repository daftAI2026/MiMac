#!/usr/bin/env python3
"""Verify a preview's source and bundle identity, emitting a sidecar manifest."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import re
import subprocess

from release_version import apply_versions, validate_build_number, validate_date
from verify_app_bundle import SIRI, verify


def git(repo: Path, *args: str) -> str:
    return subprocess.check_output(["git", "-C", str(repo), *args], text=True)


def verify_source(repo: Path, revision: str, version: str, build_number: str) -> dict:
    version = validate_date(version)
    build_number = str(validate_build_number(build_number))
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", revision):
        raise ValueError("expected a full Git source commit")
    if git(repo, "rev-parse", "HEAD").strip() != revision:
        raise ValueError("checkout HEAD does not match the planned source commit")
    changed = set(filter(None, git(repo, "diff", "--name-only", "-z", revision, "--").split("\0")))
    changed.update(filter(None, git(repo, "ls-files", "--others", "--exclude-standard", "-z").split("\0")))
    if changed - {"mygo.json"}:
        raise ValueError("unexpected source changes: " + ", ".join(sorted(changed - {"mygo.json"})))
    baseline = json.loads(git(repo, "show", revision + ":mygo.json"))
    expected = apply_versions(baseline, version, build_number)
    config_bytes = (repo / "mygo.json").read_bytes()
    actual = json.loads(config_bytes)
    # Compare JSON types as well as values (Python otherwise treats True == 1).
    if json.dumps(actual, sort_keys=True) != json.dumps(expected, sort_keys=True):
        raise ValueError("prepared mygo.json differs from the planned version-only update")
    return {
        "revision": revision,
        "prepared_config_sha256": hashlib.sha256(config_bytes).hexdigest(),
        "allowed_modified_paths": sorted(changed),
    }


def verify_versions(info: dict, version: str, build_number: str) -> None:
    expected = {
        "CFBundleShortVersionString": validate_date(version),
        "CFBundleVersion": str(validate_build_number(build_number)),
    }
    for key, value in expected.items():
        if info.get(key) != value:
            raise ValueError(f"bundle {key} does not match release plan: expected {value!r}, got {info.get(key)!r}")


def verify_build_info(info: dict, arch: str, revision: str) -> dict:
    if not isinstance(info, dict) or info.get("Path") != "mimac.local/mimac/cmd/mimac":
        raise ValueError("binary is not the MiMac main package")
    if not isinstance(info.get("Main"), dict) or info["Main"].get("Path") != "mimac.local/mimac":
        raise ValueError("binary is not the MiMac module")
    settings = {}
    entries = info.get("Settings")
    if not isinstance(entries, list):
        raise ValueError("binary has no Go build settings")
    for item in entries:
        if not isinstance(item, dict) or not isinstance(item.get("Key"), str) or not isinstance(item.get("Value"), str):
            raise ValueError("invalid Go build setting")
        key = item["Key"]
        if key in settings:
            raise ValueError(f"duplicate Go build setting: {key}")
        settings[key] = item["Value"]
    for key, value in {"GOOS": "darwin", "GOARCH": arch, "vcs": "git", "vcs.revision": revision}.items():
        if settings.get(key) != value:
            raise ValueError(f"binary {key} does not match release plan")
    if settings.get("vcs.modified") not in {"true", "false"}:
        raise ValueError("binary has no valid vcs.modified state")
    if not isinstance(info.get("GoVersion"), str) or not info["GoVersion"].startswith("go"):
        raise ValueError("binary has no Go version")
    return {
        "go_version": info["GoVersion"],
        "revision": settings["vcs.revision"],
        # The version planner legitimately changes tracked mygo.json. Record
        # this flag; source validation above bounds the allowed changes.
        "modified": settings["vcs.modified"] == "true",
    }


def verify_release(bundle: Path, arch: str, repo: Path, revision: str, version: str, build_number: str, require_siri: bool = False) -> dict:
    source = verify_source(repo, revision, version, build_number)
    contents = bundle / "Contents"
    with (contents / "Info.plist").open("rb") as stream:
        info = plistlib.load(stream)
    verify_versions(info, version, build_number)
    executable_name = info.get("CFBundleExecutable")
    if not isinstance(executable_name, str) or executable_name in {"", ".", ".."} or Path(executable_name).name != executable_name:
        raise ValueError("invalid bundle executable name")
    executable = contents / "MacOS" / executable_name
    build_info = json.loads(subprocess.check_output(["go", "version", "-m", "-json", str(executable)], text=True))
    binary = verify_build_info(build_info, arch, revision)
    native = verify(bundle, arch, require_siri)
    hashes = dict(native["sha256"])
    hashes["Info.plist"] = hashlib.sha256((contents / "Info.plist").read_bytes()).hexdigest()
    return {
        "schema_version": 1,
        "bundle": bundle.name,
        "identifier": info["CFBundleIdentifier"],
        "executable": executable_name,
        "arch": arch,
        "version": version,
        "build_number": str(validate_build_number(build_number)),
        "source": source,
        "binary": binary,
        "native_libraries": native["native_libraries"],
        "sha256": hashes,
        "optional_components": {
            "siri_glass": {"included": "Resources/" + SIRI in native["sha256"]},
        },
        "distribution": "preview; not a Developer ID or notarization attestation",
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--arch", choices=["arm64", "amd64"], required=True)
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--revision", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--build-number", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--require-siri", action="store_true")
    args = parser.parse_args(argv)
    try:
        manifest = verify_release(args.bundle, args.arch, args.repo, args.revision, args.version, args.build_number, args.require_siri)
        # Never overwrite a previous successful manifest after a failed check.
        # The workflow uses its per-job temporary directory for these sidecars.
        with args.output.open("x", encoding="utf-8") as stream:
            json.dump(manifest, stream, indent=2)
            stream.write("\n")
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"MiMac release verification failed: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
