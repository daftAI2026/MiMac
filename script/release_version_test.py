#!/usr/bin/env python3

import unittest

from release_version import apply_versions, next_release_tag, validate_build_number, validate_date


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


if __name__ == "__main__":
    unittest.main()
