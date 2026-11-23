#!/usr/bin/env python3

import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from release_naming import artifact_stem
from release_version import (
    apply_release_identity,
    apply_versions,
    bundle_version,
    channel_identity,
    main,
    next_release_tag,
    next_local_preview_revision,
    preview_identity,
    release_tags_from_pages,
    reserve_preview_directory,
    validate_build_number,
    validate_date,
    validate_release_id,
    validate_stable_plan,
    validate_train,
)


class ReleaseVersionTests(unittest.TestCase):
    def test_preview_archive_and_files_use_distinct_identity_fields(self):
        train = "2026.11.20.08.17"
        self.assertEqual(preview_identity(train, "00"), f"v0.{train}.preview_00")
        self.assertEqual(preview_identity(train, "01"), f"v0.{train}.preview_01")
        self.assertEqual(bundle_version(preview_identity(train, "01")), f"0.{train}.01")
        self.assertEqual(artifact_stem("preview", "arm64"), "MiMacPreview-arm64")
        self.assertEqual(artifact_stem("preview", "amd64"), "MiMacPreview-x86_64")
        for value in ("0", "100", "../keep-out", "AB"):
            with self.assertRaises(ValueError):
                preview_identity("2026.11.20", value)

    def test_dev_and_stable_have_distinct_distribution_contracts(self):
        train = "2026.11.20.08.17"
        self.assertEqual(channel_identity(train, "dev"), f"v0.{train}.dev_00")
        self.assertEqual(channel_identity(train, "stable", "00"), f"v0.{train}.stable_00")
        self.assertEqual(bundle_version(channel_identity(train, "preview", "04")),
                         bundle_version(channel_identity(train, "stable", "04")))
        self.assertEqual(artifact_stem("stable", "arm64"), "MiMac-arm64")
        self.assertEqual(artifact_stem("stable", "amd64"), "MiMac-x86_64")
        with self.assertRaises(ValueError):
            artifact_stem("dev", "arm64")
        with self.assertRaises(ValueError):
            channel_identity(train, "dev", "01")
        with self.assertRaises(ValueError):
            channel_identity(train, "stable", "")

    def test_stable_plan_accepts_new_sequence_and_rejects_duplicates_and_rollback(self):
        train = "2026.11.20.08.17"
        identity = f"v0.{train}.stable_01"
        validate_stable_plan(identity, [f"v0.{train}.stable_00", "v0.2026.11.21.08.17.preview_01"])
        validate_stable_plan("v0.2026.11.21.08.17.stable_00", [identity])
        for tag in (identity, f"v0.{train}.stable_02", "v0.2026.11.21.08.17.stable_00"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                validate_stable_plan(identity, [tag])
        for old_tag in ("v2026.11.20", "v2026.11.20.2", "v2026.11.20.stable_04"):
            with self.subTest(old_tag=old_tag):
                validate_stable_plan(identity, [old_tag])

    def test_new_release_train_orders_after_legacy_date_records_on_same_day(self):
        candidate = "v0.2026.11.20.00.01.stable_00"
        for prior_record in ("v2026.11.20", "v2026.11.20.2", "v2026.11.20.stable_09"):
            validate_stable_plan(candidate, [prior_record])
        with self.assertRaisesRegex(ValueError, "newer"):
            validate_stable_plan("v0.2026.11.20.00.01.stable_00", [
                "v0.2026.11.20.00.02.stable_00",
            ])

    def test_stable_plan_is_read_only_and_checks_draft_reservations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = root / "releases.json"
            history.write_text(json.dumps([[{"tag_name": "v0.2026.11.20.08.17.stable_00", "draft": True}]]))
            output = root / "stable-plan.txt"
            missing_config = root / "mygo.json"
            sha = "0" * 40
            args = ["--train", "2026.11.20.08.17", "--source-revision", sha,
                    "--existing-releases", str(history),
                    "--config", str(missing_config), "--output", str(output)]
            with patch("release_version.subprocess.run", return_value=SimpleNamespace(stdout="")), \
                 patch("release_version.subprocess.check_output",
                       side_effect=lambda command, **_: "" if command[1] == "status" else sha):
                with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    main([*args, "--stable-revision", "00"])
                self.assertFalse(output.exists())
                main([*args, "--stable-revision", "01"])
                with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    main([*args, "--stable-revision", "01"])
            self.assertFalse(missing_config.exists())
            self.assertIn("mode=plan-only\n", output.read_text())
            self.assertIn("release_identity=v0.2026.11.20.08.17.stable_01\n", output.read_text())

    def test_stable_plan_orders_legacy_date_records_from_release_api_before_new_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = root / "releases.json"
            history.write_text(json.dumps([
                [{"tag_name": "v2026.11.20", "draft": False}],
                [
                    {"tag_name": "v2026.11.20.2", "draft": True},
                    {"tag_name": "v2026.11.20.stable_09", "draft": False},
                ],
            ]))
            output = root / "stable-plan.txt"
            missing_config = root / "mygo.json"
            sha = "0" * 40
            args = ["--train", "2026.11.20.08.17", "--source-revision", sha,
                    "--existing-releases", str(history), "--config", str(missing_config),
                    "--output", str(output), "--stable-revision", "00"]
            with patch("release_version.subprocess.run", return_value=SimpleNamespace(stdout="")), \
                 patch("release_version.subprocess.check_output",
                       side_effect=lambda command, **_: "" if command[1] == "status" else sha):
                main(args)

            self.assertFalse(missing_config.exists())
            plan = output.read_text()
            self.assertIn("mode=plan-only\n", plan)
            self.assertIn("release_identity=v0.2026.11.20.08.17.stable_00\n", plan)

    def test_preview_uses_one_release_identity_and_same_numeric_version(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / "mygo.json"
            config.write_text("{}")
            output = root / "version.txt"
            with patch("release_version.subprocess.run", return_value=SimpleNamespace(stdout="v2026.11.20.preview_00\n")):
                main(["--train", "2026.11.20.08.17", "--preview-revision", "00",
                      "--config", str(config), "--output", str(output)])
            self.assertIn("release_identity=v0.2026.11.20.08.17.preview_00\n", output.read_text())
            self.assertIn("version=0.2026.11.20.08.17.00\n", output.read_text())
            self.assertIn("build_number=0.2026.11.20.08.17.00\n", output.read_text())
            self.assertIn("tag=v0.2026.11.20.08.17.preview_00\n", output.read_text())
            updated = json.loads(config.read_text())
            self.assertEqual(updated["version"], updated["macos"]["infoPlist"]["CFBundleVersion"])

    def test_release_config_can_embed_frozen_source_identity(self):
        revision = "02765c31b7cf90f7a5e789232fea21418dd75254"
        train = "2026.11.20.08.17"
        preview = f"v0.{train}.preview_00"
        result = apply_release_identity({}, preview, revision)
        info = result["macos"]["infoPlist"]
        self.assertEqual(info["MiMacBuildCommit"], revision)
        self.assertIs(info["MiMacBuildDirty"], False)
        self.assertEqual(info["MiMacReleaseVersion"], preview)
        self.assertEqual(info["CFBundleVersion"], f"0.{train}.00")
        self.assertEqual(result["version"], info["CFBundleVersion"])
        with self.assertRaises(ValueError):
            apply_release_identity({}, "v0.2026.11.21.08.17.preview_00", "bad")

    def test_valid_date_and_rejects_malformed_or_impossible_dates(self):
        self.assertEqual(validate_date("2026.10.23"), "2026.10.23")
        for value in ("2026-10-23", "2026.1.23", "2026.02.29", "20261023", "2026.10.23\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_date(value)

    def test_release_train_validates_minute_and_round_trips(self):
        self.assertEqual(validate_train("2024.02.29.08.17"), "2024.02.29.08.17")
        for value in ("2026.02.29.08.17", "2026.04.31.08.17", "2026.13.01.08.17",
                      "2026.11.20.24.17", "2026.11.20.08.60", "2026.11.20.8.17"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_train(value)

    def test_release_identity_rejects_bad_channel_candidate_and_calendar_date(self):
        self.assertEqual(validate_release_id("v0.2026.11.20.08.17.preview_04"),
                         "v0.2026.11.20.08.17.preview_04")
        for value in ("v2026.11.20.preview_04", "v0.2026.11.20.08.17.preview_4",
                      "v0.2026.11.20.08.17.other_04", "v0.2026.02.29.08.17.stable_04",
                      "v0.2026.11.20.08.17.dev_01"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_release_id(value)

    def test_first_release_and_cross_day_release_use_base_tag(self):
        self.assertEqual(next_release_tag("2026.10.23", []), "v2026.10.23")
        self.assertEqual(
            next_release_tag("2026.10.24", ["v2026.10.23", "v2026.10.23.2"]),
            "v2026.10.24",
        )

    def test_same_day_releases_increment_without_filling_gaps(self):
        self.assertEqual(next_release_tag("2026.10.23", ["v2026.10.23"]), "v2026.10.23.2")
        self.assertEqual(
            next_release_tag("2026.10.23", ["v2026.10.23", "v2026.10.23.2", "v2026.10.23.4"]),
            "v2026.10.23.5",
        )
        self.assertEqual(next_release_tag("2026.10.23", ["v2026.10.23.2"]), "v2026.10.23.3")

    def test_draft_release_tags_advance_the_same_day_sequence(self):
        date = "2026.10.23"
        draft_base = release_tags_from_pages([[{"tag_name": f"v{date}", "draft": True}]])
        self.assertEqual(next_release_tag(date, draft_base), f"v{date}.2")
        draft_two = release_tags_from_pages([[{"tag_name": f"v{date}.2", "draft": True}]])
        self.assertEqual(next_release_tag(date, draft_two), f"v{date}.3")

        draft_two_and_three = release_tags_from_pages(
            [
                [{"tag_name": f"v{date}.2", "draft": True}],
                [{"tag_name": f"v{date}.3", "draft": True}],
            ]
        )
        self.assertEqual(next_release_tag(date, draft_two_and_three), f"v{date}.4")

    def test_release_pages_cover_all_pages_and_ignore_other_dates(self):
        date = "2026.10.23"
        tags = release_tags_from_pages(
            [
                [{"tag_name": "v2026.10.22.99", "draft": True}],
                [{"tag_name": f"v{date}.2", "draft": True}],
            ]
        )
        self.assertEqual(next_release_tag(date, tags), f"v{date}.3")
        self.assertEqual(release_tags_from_pages([]), [])
        self.assertEqual(release_tags_from_pages([[], []]), [])

    def test_duplicate_release_tags_are_allowed(self):
        date = "2026.10.23"
        tags = release_tags_from_pages(
            [[{"tag_name": f"v{date}.2"}], [{"tag_name": f"v{date}.2"}]]
        )
        self.assertEqual(next_release_tag(date, tags), f"v{date}.3")

    def test_unrelated_tags_are_ignored(self):
        self.assertEqual(
            next_release_tag(
                "2026.10.23",
                ["v2026.10.22", "v2025.10.23.9", "v2026.10.23-rc1", "release-preview"],
            ),
            "v2026.10.23",
        )

    def test_malformed_same_day_tag_fails_closed(self):
        for tag in ("v2026.10.23.1", "v2026.10.23.02", "v2026.10.23.2.extra"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                next_release_tag("2026.10.23", [tag])

    def test_build_number_must_be_between_one_and_9999(self):
        self.assertEqual(validate_build_number("1"), 1)
        self.assertEqual(validate_build_number("9999"), 9999)
        for value in ("0", "10000", "01", "1.0", " 2", "2\n", True):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_build_number(value)

    def test_versions_preserve_other_mygo_configuration(self):
        config = {
            "name": "MiMac",
            "version": "0.1.0",
            "resources": ["Assets/Social/NOTICE.md"],
            "macos": {
                "minimumSystemVersion": "14.2",
                "signingIdentity": "-",
                "infoPlist": {"LSUIElement": True, "CustomKey": "keep"},
            },
        }
        result = apply_versions(config, "2026.10.23", "42")
        self.assertEqual(result["version"], "2026.10.23")
        self.assertEqual(result["macos"]["infoPlist"]["CFBundleVersion"], "42")
        self.assertTrue(result["macos"]["infoPlist"]["LSUIElement"])
        self.assertEqual(result["macos"]["infoPlist"]["CustomKey"], "keep")
        self.assertEqual(result["macos"]["minimumSystemVersion"], "14.2")
        self.assertEqual(result["resources"], ["Assets/Social/NOTICE.md"])

    def test_main_merges_git_tags_and_paginated_release_tags(self):
        train = "2026.10.23.08.17"
        pages = [
            [{"tag_name": "v0.2026.10.23.08.17.preview_02", "draft": True}],
            [{"tag_name": "v0.2026.10.22.08.17.preview_99", "draft": True}],
        ]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / "mygo.json"
            config.write_text(
                json.dumps(
                    {
                        "name": "MiMac",
                        "version": "0.1.0",
                        "macos": {"infoPlist": {"CFBundleVersion": "0.1.0"}},
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            releases = root / "releases.json"
            releases.write_text(json.dumps(pages), encoding="utf-8")
            output = root / "outputs.txt"
            with patch(
                "release_version.subprocess.run",
                return_value=SimpleNamespace(stdout="v2026.10.22\n"),
            ):
                main(
                    [
                        "--train",
                        train,
                        "--preview-revision",
                        "03",
                        "--config",
                        str(config),
                        "--output",
                        str(output),
                        "--existing-releases",
                        str(releases),
                    ]
                )

            updated = json.loads(config.read_text(encoding="utf-8"))
            numeric = "0.2026.10.23.08.17.03"
            identity = "v0.2026.10.23.08.17.preview_03"
            self.assertEqual(updated["version"], numeric)
            self.assertEqual(updated["macos"]["infoPlist"]["CFBundleVersion"], numeric)
            self.assertIn(f"tag={identity}\n", output.read_text(encoding="utf-8"))
            self.assertIn(f"asset_stem={identity.removeprefix('v')}\n", output.read_text(encoding="utf-8"))

    def test_legacy_build_number_arguments_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "mygo.json"
            config.write_text("{}")
            output = Path(directory) / "output.txt"
            with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as caught:
                main(["--date", "2026.10.23", "--build-number", "42", "--output", str(output)])
            self.assertEqual(caught.exception.code, 2)
            self.assertFalse(output.exists())

    def test_local_preview_allocation_increments_existing_directories_and_reserves_atomically(self):
        import threading

        train = "2026.11.20.08.17"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertEqual(next_local_preview_revision(train, root), "00")
            (root / f"v0.{train}.preview_00").mkdir()
            (root / f"v0.{train}.preview_07").mkdir()
            (root / "v0.2026.11.20.08.16.preview_99").mkdir()
            (root / f"v0.{train}.stable_99").mkdir()
            self.assertEqual(next_local_preview_revision(train, root), "08")

            identity = f"v0.{train}.preview_08"
            outcomes = []
            barrier = threading.Barrier(2)

            def reserve():
                barrier.wait()
                try:
                    outcomes.append(("ok", reserve_preview_directory(root, identity)))
                except FileExistsError:
                    outcomes.append(("exists", None))

            threads = [threading.Thread(target=reserve) for _ in range(2)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
            self.assertCountEqual([result[0] for result in outcomes], ["ok", "exists"])
            self.assertTrue((root / identity).is_dir())

    def test_main_rejects_bad_release_pages_without_modifying_config(self):
        train = "2026.10.23.08.17"
        invalid_inputs = (
            "not-json",
            json.dumps({"message": "API rate limit exceeded"}),
            json.dumps([{"tag_name": f"v{train}.preview_02"}]),
            json.dumps([[{"tag_name": f"v{train}.preview_02"}], {"message": "API error"}]),
            json.dumps([[{"draft": True}]]),
            json.dumps([[{"tag_name": 23}]]),
            json.dumps([[{"tag_name": ""}]]),
            json.dumps([["not a release object"]]),
        )
        for fixture in invalid_inputs:
            with self.subTest(fixture=fixture), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config = root / "mygo.json"
                original = b'{"version":"0.1.0","macos":{"infoPlist":{"CFBundleVersion":"0.1.0"}}}\n'
                config.write_bytes(original)
                releases = root / "releases.json"
                releases.write_text(fixture, encoding="utf-8")
                output = root / "outputs.txt"
                with patch(
                    "release_version.subprocess.run",
                    return_value=SimpleNamespace(stdout=""),
                ):
                    with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as caught:
                        main(
                            [
                                "--train",
                                train,
                                "--preview-revision",
                                "00",
                                "--config",
                                str(config),
                                "--output",
                                str(output),
                                "--existing-releases",
                                str(releases),
                            ]
                        )
                self.assertEqual(caught.exception.code, 2)
                self.assertEqual(config.read_bytes(), original)
                self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
