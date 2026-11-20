#!/usr/bin/env python3
"""Prepare the date and build versions for a MiMac preview release."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import tempfile
from datetime import date
from pathlib import Path
from typing import Any, Iterable


DATE_PATTERN = re.compile(r"[0-9]{4}\.[0-9]{2}\.[0-9]{2}\Z", re.ASCII)
BUILD_PATTERN = re.compile(r"[1-9][0-9]{0,3}\Z", re.ASCII)
COUNTER_PATTERN = re.compile(r"(?:[2-9]|[1-9][0-9]+)\Z", re.ASCII)


def validate_date(value: str) -> str:
    """Return a canonical YYYY.MM.DD date, rejecting impossible dates."""
    if not isinstance(value, str) or not DATE_PATTERN.fullmatch(value):
        raise ValueError("date must use YYYY.MM.DD")
    try:
        parsed = date.fromisoformat(value.replace(".", "-"))
    except ValueError as exc:
        raise ValueError("date is not a valid calendar date") from exc
    if parsed.strftime("%Y.%m.%d") != value:
        raise ValueError("date must use YYYY.MM.DD")
    return value


def validate_build_number(value: str | int) -> int:
    """Validate the numeric CFBundleVersion component (1 through 9999)."""
    if isinstance(value, bool):
        raise ValueError("build number must be an integer from 1 through 9999")
    if isinstance(value, int):
        number = value
    elif isinstance(value, str) and BUILD_PATTERN.fullmatch(value):
        number = int(value)
    else:
        raise ValueError("build number must be an integer from 1 through 9999")
    if not 1 <= number <= 9999:
        raise ValueError("build number must be an integer from 1 through 9999")
    return number


def next_release_tag(release_date: str, tags: Iterable[str]) -> str:
    """Choose the base date tag, then monotonically increment same-day tags."""
    release_date = validate_date(release_date)
    base = f"v{release_date}"
    has_base = False
    counters: list[int] = []

    for tag in tags:
        if tag == base:
            has_base = True
            continue
        if not tag.startswith(base + "."):
            continue

        raw_counter = tag[len(base) + 1 :]
        if not COUNTER_PATTERN.fullmatch(raw_counter):
            raise ValueError(f"malformed same-day release tag: {tag}")
        counters.append(int(raw_counter))

    if counters:
        return f"{base}.{max(counters) + 1}"
    if has_base:
        return f"{base}.2"
    return base


def release_tags_from_pages(pages: Any) -> list[str]:
    """Extract release tag names from gh api --paginate --slurp output."""
    if isinstance(pages, dict):
        if "message" in pages:
            raise ValueError(f"GitHub releases API error: {pages['message']}")
        raise ValueError("existing releases JSON must be an array of pages")
    if not isinstance(pages, list):
        raise ValueError("existing releases JSON must be an array of pages")

    tags: list[str] = []
    for page_number, page in enumerate(pages, start=1):
        if isinstance(page, dict) and "message" in page:
            raise ValueError(f"GitHub releases API error on page {page_number}: {page['message']}")
        if not isinstance(page, list):
            raise ValueError(f"existing releases page {page_number} must be an array")
        for release_number, release in enumerate(page, start=1):
            if not isinstance(release, dict):
                raise ValueError(
                    f"release {release_number} on page {page_number} must be an object"
                )
            if "message" in release:
                raise ValueError(
                    f"GitHub releases API error on page {page_number}: {release['message']}"
                )
            if "tag_name" not in release:
                raise ValueError(
                    f"release {release_number} on page {page_number} is missing tag_name"
                )
            tag = release["tag_name"]
            if not isinstance(tag, str) or not tag:
                raise ValueError(
                    f"release {release_number} on page {page_number} has an invalid tag_name"
                )
            tags.append(tag)
    return tags


def release_title(release_date: str, tag: str) -> str:
    release_date = validate_date(release_date)
    base = f"v{release_date}"
    if tag == base:
        return f"MiMac {release_date}"
    if not tag.startswith(base + "."):
        raise ValueError("release tag does not match release date")
    suffix = tag[len(base) + 1 :]
    if not COUNTER_PATTERN.fullmatch(suffix):
        raise ValueError("release tag has an invalid same-day counter")
    return f"MiMac {release_date} (build {suffix})"


def preview_identity(release_date: str, revision: str = "") -> str:
    release_date = validate_date(release_date)
    if not revision:
        return release_date
    if not re.fullmatch(r"[0-9]{2}", revision):
        raise ValueError("preview revision must be 00 through 99")
    return f"v{release_date}.preview_{revision}"


def validate_release_identity(release_date: str, identity: str) -> str:
    if identity == release_date:
        return validate_date(identity)
    prefix = re.escape(f"v{validate_date(release_date)}.preview_")
    if not isinstance(identity, str) or not re.fullmatch(prefix + r"[0-9]{2}", identity):
        raise ValueError("release identity does not match the planned date and preview revision")
    return identity


def apply_versions(config: dict[str, Any], release_date: str, build_number: str | int,
                   source_revision: str | None = None, release_identity: str | None = None) -> dict[str, Any]:
    """Write the planned versions and, when provided, embedded source identity."""
    release_date = validate_date(release_date)
    build_number = validate_build_number(build_number)
    if not isinstance(config, dict):
        raise ValueError("mygo.json must contain a JSON object")

    macos = config.setdefault("macos", {})
    if not isinstance(macos, dict):
        raise ValueError("mygo.json macos field must be an object")
    info_plist = macos.setdefault("infoPlist", {})
    if not isinstance(info_plist, dict):
        raise ValueError("mygo.json macos.infoPlist field must be an object")

    config["version"] = release_date
    info_plist["CFBundleVersion"] = str(build_number)
    if source_revision is not None:
        if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", source_revision):
            raise ValueError("expected a full Git source revision")
        info_plist["MiMacBuildCommit"] = source_revision
        info_plist["MiMacBuildDirty"] = False
    if release_identity is not None:
        info_plist["MiMacReleaseVersion"] = validate_release_identity(release_date, release_identity)
    return config


def write_config(path: Path, config: dict[str, Any]) -> None:
    """Atomically write the temporary checkout's MyGo configuration."""
    existing_mode = path.stat().st_mode & 0o777
    descriptor, temporary_name = tempfile.mkstemp(prefix=".mygo.", suffix=".json", dir=path.parent)
    temporary_path = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as output:
            json.dump(config, output, ensure_ascii=False, indent=2)
            output.write("\n")
        temporary_path.chmod(existing_mode)
        os.replace(temporary_path, path)
    except BaseException:
        temporary_path.unlink(missing_ok=True)
        raise


def append_outputs(path: Path, values: dict[str, str]) -> None:
    """Append validated single-line values using the Actions output file."""
    with path.open("a", encoding="utf-8", newline="\n") as output:
        for key, value in values.items():
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key, re.ASCII):
                raise ValueError(f"invalid output key: {key}")
            if "\n" in value or "\r" in value:
                raise ValueError(f"invalid output value for {key}")
            output.write(f"{key}={value}\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--date", required=True, help="frozen Asia/Shanghai date in YYYY.MM.DD form")
    parser.add_argument("--build-number", required=True, help="GitHub Actions run number, 1 through 9999")
    parser.add_argument("--preview-revision", help="00–99 for a deliverable preview, empty for a local date-only package")
    parser.add_argument("--source-revision", help="full Git commit for the embedded release identity")
    parser.add_argument("--config", type=Path, default=Path("mygo.json"))
    parser.add_argument("--output", type=Path, default=os.environ.get("GITHUB_OUTPUT"))
    parser.add_argument(
        "--existing-releases",
        type=Path,
        help="JSON output from gh api --paginate --slurp for the repository releases endpoint",
    )
    args = parser.parse_args(argv)

    try:
        output_path = Path(args.output) if args.output is not None else None
        if output_path is None:
            raise ValueError("GITHUB_OUTPUT or --output is required")
        release_date = validate_date(args.date)
        build_number = validate_build_number(args.build_number)
        tags = subprocess.run(
            ["git", "tag", "--list"],
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
        ).stdout.splitlines()
        if args.existing_releases is not None:
            pages = json.loads(args.existing_releases.read_text(encoding="utf-8"))
            tags.extend(release_tags_from_pages(pages))
        identity = None if args.preview_revision is None else preview_identity(release_date, args.preview_revision)
        if args.preview_revision:
            tag = identity
            if tag in tags:
                raise ValueError(f"preview already exists: {tag}")
            title = tag
        else:
            tag = next_release_tag(release_date, tags)
            title = release_title(release_date, tag)
        asset_stem = tag.removeprefix("v")

        config = json.loads(args.config.read_text(encoding="utf-8"))
        if args.source_revision is not None:
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
            if head != args.source_revision:
                raise ValueError("checkout HEAD does not match the planned source revision")
        config = apply_versions(config, release_date, build_number, args.source_revision, identity)
        write_config(args.config, config)

        append_outputs(
            output_path,
            {
                "date": release_date,
                "version": release_date,
                "build_number": str(build_number),
                "tag": tag,
                "release_title": title,
                "asset_stem": asset_stem,
                **({"release_identity": identity} if identity is not None else {}),
            },
        )
    except (OSError, json.JSONDecodeError, subprocess.CalledProcessError, ValueError) as exc:
        parser.error(str(exc))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
