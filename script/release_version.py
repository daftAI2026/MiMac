#!/usr/bin/env python3
"""Plan MiMac build identity and prepare the temporary Preview configuration."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import tempfile
from datetime import date, datetime
from pathlib import Path
from typing import Any, Iterable
from zoneinfo import ZoneInfo


DATE_PATTERN = re.compile(r"[0-9]{4}\.[0-9]{2}\.[0-9]{2}\Z", re.ASCII)
BUILD_PATTERN = re.compile(r"[1-9][0-9]{0,3}\Z", re.ASCII)
COUNTER_PATTERN = re.compile(r"(?:[2-9]|[1-9][0-9]+)\Z", re.ASCII)
REVISION_PATTERN = re.compile(r"[0-9]{2}\Z", re.ASCII)
CHANNEL_TAG_PATTERN = re.compile(r"v([0-9]{4}\.[0-9]{2}\.[0-9]{2})\.(preview|stable)_([0-9]{2})\Z")
TRAIN_PATTERN = re.compile(r"[0-9]{4}(?:\.[0-9]{2}){4}\Z", re.ASCII)
RELEASE_ID_PATTERN = re.compile(
    r"v0\.([0-9]{4}(?:\.[0-9]{2}){4})\.(dev|preview|stable)_([0-9]{2})\Z", re.ASCII
)
LEGACY_DATE_TAG_PATTERN = re.compile(r"v([0-9]{4}\.[0-9]{2}\.[0-9]{2})(?:\.([0-9]+))?\Z", re.ASCII)


def build_train() -> str:
    """Freeze the release train to a Shanghai-local minute."""
    requested = os.environ.get("MIMAC_RELEASE_TRAIN")
    if requested is None:
        requested = datetime.now(ZoneInfo("Asia/Shanghai")).strftime("%Y.%m.%d.%H.%M")
    return validate_train(requested)


def build_date() -> str:
    """Compatibility helper for historical date-only release records."""
    return datetime.now(ZoneInfo("Asia/Shanghai")).strftime("%Y.%m.%d")


def validate_train(value: str) -> str:
    if not isinstance(value, str) or not TRAIN_PATTERN.fullmatch(value):
        raise ValueError("release train must use YYYY.MM.DD.HH.MM")
    try:
        parsed = datetime.strptime(value, "%Y.%m.%d.%H.%M")
    except ValueError as exc:
        raise ValueError("release train is not a valid Shanghai date and minute") from exc
    if parsed.strftime("%Y.%m.%d.%H.%M") != value:
        raise ValueError("release train must use YYYY.MM.DD.HH.MM")
    return value


def release_identity(train: str, channel: str, revision: str = "00") -> str:
    train = validate_train(train)
    if channel not in {"dev", "preview", "stable"}:
        raise ValueError(f"unknown release channel: {channel}")
    if not isinstance(revision, str) or not REVISION_PATTERN.fullmatch(revision):
        raise ValueError(f"{channel} candidate must use two digits from 00 through 99")
    if channel == "dev" and revision != "00":
        raise ValueError("Dev release identity must use candidate 00")
    return f"v0.{train}.{channel}_{revision}"


def validate_release_id(value: str) -> str:
    match = RELEASE_ID_PATTERN.fullmatch(value) if isinstance(value, str) else None
    if not match:
        raise ValueError("release identity must use v0.YYYY.MM.DD.HH.MM.channel_NN")
    validate_train(match[1])
    if match[2] == "dev" and match[3] != "00":
        raise ValueError("Dev release identity must use candidate 00")
    return value


def bundle_version(identity: str) -> str:
    match = RELEASE_ID_PATTERN.fullmatch(validate_release_id(identity))
    assert match is not None
    return f"0.{match[1]}.{match[3]}"


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
        if CHANNEL_TAG_PATTERN.fullmatch(tag):
            continue
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


def channel_identity(train: str, channel: str, revision: str = "00") -> str:
    return release_identity(train, channel, revision)


def preview_identity(train: str, revision: str = "00") -> str:
    return channel_identity(train, "preview", revision)


def validate_release_identity(release_date: str, identity: str) -> str:
    if identity == release_date:
        return validate_date(identity)
    match = CHANNEL_TAG_PATTERN.fullmatch(identity) if isinstance(identity, str) else None
    if not match or match[1] != validate_date(release_date):
        raise ValueError("release identity does not match the planned date and channel revision")
    return identity


def _stable_history_position(tag: str) -> tuple[datetime, int, int] | None:
    """Order Stable records while treating historical date-only tags as earlier that day."""
    match = RELEASE_ID_PATTERN.fullmatch(tag) if isinstance(tag, str) else None
    if match:
        if match[2] != "stable":
            return None
        train = validate_train(match[1])
        return datetime.strptime(train, "%Y.%m.%d.%H.%M"), 1, int(match[3])

    old_channel = CHANNEL_TAG_PATTERN.fullmatch(tag) if isinstance(tag, str) else None
    if old_channel:
        if old_channel[2] != "stable":
            return None
        old_date = validate_date(old_channel[1])
        midnight = datetime.strptime(old_date, "%Y.%m.%d")
        return midnight, 0, int(old_channel[3])

    old_date = LEGACY_DATE_TAG_PATTERN.fullmatch(tag) if isinstance(tag, str) else None
    if old_date:
        old_date_value = validate_date(old_date[1])
        suffix = old_date[2]
        if suffix is not None and (not COUNTER_PATTERN.fullmatch(suffix)):
            return None
        midnight = datetime.strptime(old_date_value, "%Y.%m.%d")
        return midnight, 0, int(suffix or "0")
    return None


def validate_stable_plan(identity: str, tags: Iterable[str]) -> None:
    identity = validate_release_id(identity)
    match = RELEASE_ID_PATTERN.fullmatch(identity)
    if match is None or match[2] != "stable":
        raise ValueError("Stable planning requires a Stable release identity")
    candidate_position = _stable_history_position(identity)
    assert candidate_position is not None
    for tag in tags:
        position = _stable_history_position(tag)
        if position is not None and position >= candidate_position:
            raise ValueError(f"Stable candidate must be newer than existing release or draft: {tag}")


def next_candidate_revision(train: str, channel: str, tags: Iterable[str]) -> str:
    """Return the next same-train/channel candidate without filling gaps."""
    train = validate_train(train)
    if channel not in {"preview", "stable"}:
        raise ValueError("candidate allocation is available only for Preview and Stable")
    prefix = f"v0.{train}.{channel}_"
    revisions = [int(tag[len(prefix):]) for tag in tags
                 if isinstance(tag, str) and tag.startswith(prefix)
                 and REVISION_PATTERN.fullmatch(tag[len(prefix):])]
    next_revision = max(revisions, default=-1) + 1
    if next_revision > 99:
        raise ValueError("candidate sequence is exhausted; start a new release train")
    return f"{next_revision:02d}"


def next_local_preview_revision(train: str, output_root: Path) -> str:
    train = validate_train(train)
    prefix = f"v0.{train}.preview_"
    existing = []
    if output_root.exists():
        if not output_root.is_dir():
            raise ValueError("Preview output root is not a directory")
        for path in output_root.iterdir():
            match = re.fullmatch(re.escape(prefix) + r"([0-9]{2})", path.name, re.ASCII)
            if match and path.is_dir():
                existing.append(prefix + match[1])
    return next_candidate_revision(train, "preview", existing)


def reserve_preview_directory(output_root: Path, identity: str) -> Path:
    identity = validate_release_id(identity)
    if not identity.endswith(".preview_" + identity[-2:]):
        raise ValueError("only Preview identities can reserve a local output directory")
    output_root.mkdir(parents=True, exist_ok=True)
    target = output_root / identity
    target.mkdir()  # Atomic, exclusive reservation. A competing run fails without overwriting.
    return target.resolve(strict=True)


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


def apply_release_identity(config: dict[str, Any], identity: str,
                           source_revision: str | None = None) -> dict[str, Any]:
    """Set both macOS numeric versions from the one frozen channel identity."""
    identity = validate_release_id(identity)
    numeric_version = bundle_version(identity)
    if not isinstance(config, dict):
        raise ValueError("mygo.json must contain a JSON object")
    macos = config.setdefault("macos", {})
    if not isinstance(macos, dict):
        raise ValueError("mygo.json macos field must be an object")
    info_plist = macos.setdefault("infoPlist", {})
    if not isinstance(info_plist, dict):
        raise ValueError("mygo.json macos.infoPlist field must be an object")
    config["version"] = numeric_version
    info_plist["CFBundleVersion"] = numeric_version
    if source_revision is not None:
        if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", source_revision):
            raise ValueError("expected a full Git source revision")
        info_plist["MiMacBuildCommit"] = source_revision
        info_plist["MiMacBuildDirty"] = False
    info_plist["MiMacReleaseVersion"] = identity
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


def append_outputs(path: Path, values: dict[str, str], *, exclusive: bool = False) -> None:
    """Write validated single-line values for the release plan or Actions."""
    with path.open("x" if exclusive else "a", encoding="utf-8", newline="\n") as output:
        for key, value in values.items():
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key, re.ASCII):
                raise ValueError(f"invalid output key: {key}")
            if "\n" in value or "\r" in value:
                raise ValueError(f"invalid output value for {key}")
            output.write(f"{key}={value}\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--train", help="frozen Asia/Shanghai release minute in YYYY.MM.DD.HH.MM form")
    channel = parser.add_mutually_exclusive_group()
    channel.add_argument("--dev", action="store_true", help="prepare the local Dev identity")
    channel.add_argument("--preview-revision", nargs="?", const="auto",
                         help="00–99 Preview candidate; omit the value to use the next from release history")
    channel.add_argument("--stable-revision", help="00–99 for a read-only Stable release plan")
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
        train = build_train() if args.train is None else validate_train(args.train)
        stable = args.stable_revision is not None
        preview = args.preview_revision is not None
        if stable:
            if args.existing_releases is None or args.source_revision is None:
                raise ValueError("Stable planning requires release history and a frozen source revision")
            if subprocess.check_output(["git", "status", "--porcelain"], text=True).strip():
                raise ValueError("Stable source checkout must be clean")
        if not stable and args.dev and args.source_revision is not None:
            raise ValueError("Dev identity preparation does not embed a release source revision")
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
        if stable:
            revision = args.stable_revision
            identity = channel_identity(train, "stable", revision)
            if identity in tags:
                raise ValueError(f"release already exists: {identity}")
            validate_stable_plan(identity, tags)
        elif preview or args.dev:
            revision = args.preview_revision
            if preview and revision == "auto":
                revision = next_candidate_revision(train, "preview", tags)
            identity = channel_identity(train, "preview" if preview else "dev", revision or "00")
            if identity in tags:
                raise ValueError(f"release already exists: {identity}")
        else:
            raise ValueError("select --dev, --preview-revision, or --stable-revision")

        release = {
            "release_identity": identity,
            "version": bundle_version(identity),
            "build_number": bundle_version(identity),
            "asset_stem": identity.removeprefix("v"),
        }
        if preview or stable:
            release.update({"tag": identity, "release_title": identity})

        if args.source_revision is not None:
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
            if head != args.source_revision:
                raise ValueError("checkout HEAD does not match the planned source revision")
        if not stable:
            config = json.loads(args.config.read_text(encoding="utf-8"))
            config = apply_release_identity(config, identity, args.source_revision)
            write_config(args.config, config)

        append_outputs(
            output_path,
            {
                "train": train,
                **release,
                **({"mode": "plan-only", "source_revision": args.source_revision} if stable else {}),
            },
            exclusive=stable,
        )
    except (OSError, json.JSONDecodeError, subprocess.CalledProcessError, ValueError) as exc:
        parser.error(str(exc))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
