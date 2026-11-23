#!/usr/bin/env python3
"""Exercise Preview CLI parsing without building or touching an app."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("package_preview.sh")


class PackagePreviewCLITests(unittest.TestCase):
    def run_script(self, *args: str) -> subprocess.CompletedProcess:
        env = {**os.environ, "MIMAC_PREVIEW_PROFILE": "invalid-test-profile"}
        return subprocess.run(["/bin/bash", str(SCRIPT), *args], env=env,
                              capture_output=True, text=True, check=False)

    def test_no_arguments_uses_default_output_and_reaches_profile_validation(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 2)
        self.assertIn("Expected core-only or siri-glass", result.stderr)
        self.assertNotIn("usage:", result.stderr)

    def test_output_option_is_accepted_without_building(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "preview"
            result = self.run_script("--output", str(output))
            self.assertEqual(result.returncode, 2)
            self.assertIn("Expected core-only or siri-glass", result.stderr)
            self.assertNotIn("usage:", result.stderr)
            self.assertFalse(output.exists())

    def test_old_positional_build_number_is_rejected(self):
        result = self.run_script("62")
        self.assertEqual(result.returncode, 2)
        self.assertIn("do not pass a build number", result.stderr)


if __name__ == "__main__":
    unittest.main()
