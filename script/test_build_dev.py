import copy
import json
from pathlib import Path
import unittest
import tempfile
from unittest.mock import patch

import build_dev


class DevelopmentBuildIsolation(unittest.TestCase):
    def test_derived_configuration_preserves_production_and_points_at_same_source(self):
        root = Path(__file__).resolve().parent.parent
        original = json.loads((root / "mygo.json").read_text())
        saved = copy.deepcopy(original)
        dev = build_dev.development_config(original, root)
        self.assertEqual(original, saved)
        self.assertEqual(dev["identifier"], "dev.mimac.app.dev")
        self.assertEqual(dev["name"], "MiMac Dev")
        self.assertEqual(dev["out"], str(root / ".mygo/dev"))
        self.assertEqual(Path(dev["main"]).resolve(), (root / original["main"]).resolve())
        self.assertTrue(all(Path(item).is_file() for item in dev["resources"]))
        self.assertEqual(dev["macos"]["infoPlist"], original["macos"]["infoPlist"])

    def test_daily_running_refuses_device_handoff(self):
        for count in ("1", "2"):
            with self.subTest(count=count), patch.object(build_dev.subprocess, "check_output", return_value=count):
                with self.assertRaisesRegex(RuntimeError, "Quit daily MiMac"):
                    build_dev.require_device_handoff()
        with patch.object(build_dev.subprocess, "check_output", return_value="0"):
            build_dev.require_device_handoff()

    def test_unreleased_daily_mapping_refuses_device_handoff(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(build_dev.Path, "home", return_value=Path(directory)), patch.object(build_dev.subprocess, "check_output", return_value="0"):
            recovery = Path(directory) / "Library/Application Support/MiMac/voice-mapping-recovery.plist"
            recovery.parent.mkdir(parents=True)
            recovery.write_bytes(b"retained device mapping")
            with self.assertRaisesRegex(RuntimeError, "Restore daily MiMac"):
                build_dev.require_device_handoff()
            self.assertEqual(recovery.read_bytes(), b"retained device mapping")

    def test_go_boolean_spellings_use_the_same_handoff_gate(self):
        for value in ('--device-access', '-device-access', '--device-access=true', '-device-access=1', '--device-access=True'):
            self.assertTrue(build_dev.device_access_requested([value]))
        self.assertFalse(build_dev.device_access_requested(['--device-access=false']))
        self.assertFalse(build_dev.device_access_requested(['--device-access', '--device-access=false']))

    def test_stop_scope_uses_full_executable_path(self):
        daily = "/Applications/MiMac.app/Contents/MacOS/MiMac"
        dev = Path("/tmp/app/.mygo/dev/darwin-arm64/MiMac Dev.app/Contents/MacOS/MiMac Dev")
        other = "/tmp/other/MiMac Dev.app/Contents/MacOS/MiMac Dev"
        rows = f"100 {daily}\n101 {dev}\n102 {other}\n"
        with patch.object(build_dev.subprocess, "check_output", return_value=rows):
            self.assertEqual(build_dev.owned_pids(dev), [101])


if __name__ == "__main__":
    unittest.main()
