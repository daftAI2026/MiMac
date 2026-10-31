#!/usr/bin/env python3

import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from release_version import (
    apply_versions,
    main,
    next_release_tag,
    release_tags_from_pages,
    validate_build_number,
    validate_date,
)


class ReleaseVersionTests(unittest.TestCase):
    def test_valid_date_and_rejects_malformed_or_impossible_dates(self):
        self.assertEqual(validate_date("2026.10.23"), "2026.10.23")
        for value in ("2026-10-23", "2026.1.23", "2026.02.29", "20261023", "2026.10.23\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_date(value)

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
        date = "2026.10.23"
        pages = [
            [{"tag_name": f"v{date}.2", "draft": True}],
            [{"tag_name": "v2026.10.22.99", "draft": True}],
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
                return_value=SimpleNamespace(stdout=f"v{date}\n"),
            ):
                main(
                    [
                        "--date",
                        date,
                        "--build-number",
                        "42",
                        "--config",
                        str(config),
                        "--output",
                        str(output),
                        "--existing-releases",
                        str(releases),
                    ]
                )

            updated = json.loads(config.read_text(encoding="utf-8"))
            self.assertEqual(updated["version"], date)
            self.assertEqual(updated["macos"]["infoPlist"]["CFBundleVersion"], "42")
            self.assertIn(f"tag=v{date}.3\n", output.read_text(encoding="utf-8"))
            self.assertIn(f"asset_stem={date}.3\n", output.read_text(encoding="utf-8"))

    def test_main_rejects_bad_release_pages_without_modifying_config(self):
        date = "2026.10.23"
        invalid_inputs = (
            "not-json",
            json.dumps({"message": "API rate limit exceeded"}),
            json.dumps([{"tag_name": f"v{date}.2"}]),
            json.dumps([[{"tag_name": f"v{date}.2"}], {"message": "API error"}]),
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
                                "--date",
                                date,
                                "--build-number",
                                "42",
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
