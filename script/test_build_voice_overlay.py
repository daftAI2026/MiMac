#!/usr/bin/env python3

from pathlib import Path
import shutil
import os
import platform
import subprocess
import tempfile
import unittest


SOURCE_SCRIPT = Path(__file__).with_name("build_voice_overlay.sh")


class BuildVoiceOverlayProfileTests(unittest.TestCase):
    def staged_script(self, root: Path) -> tuple[Path, Path, Path]:
        script = root / "script" / SOURCE_SCRIPT.name
        script.parent.mkdir(parents=True)
        shutil.copy2(SOURCE_SCRIPT, script)
        resource_dir = root / "resources" / "darwin-arm64"
        resource_dir.mkdir(parents=True)
        siri = resource_dir / "libSiriGlassBridge.dylib"
        siri.write_bytes(b"stale generated Siri bridge")
        unrelated = resource_dir / "libMiMacSpeech.dylib"
        unrelated.write_bytes(b"unrelated native resource")
        return script, siri, unrelated

    def test_core_only_removes_only_the_owned_siri_resource(self):
        with tempfile.TemporaryDirectory() as directory:
            script, siri, unrelated = self.staged_script(Path(directory))
            result = subprocess.run(["/bin/bash", str(script), "core-only"], capture_output=True, text=True)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("core-only", result.stdout)
            self.assertFalse(siri.exists())
            self.assertEqual(unrelated.read_bytes(), b"unrelated native resource")

    @unittest.skipUnless(platform.system() == "Darwin" and platform.machine() == "arm64"
                         and int(platform.mac_ver()[0].split(".")[0] or 0) >= 26,
                         "requires the native display public API host")
    def test_unavailable_toolchain_preserves_optional_and_required_modes(self):
        for mode, expected in (("auto", 0), ("siri-required", 1)):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                script, siri, unrelated = self.staged_script(root)
                native = root / "internal/platform/siriglass/native"
                native.mkdir(parents=True)
                (native / "Package.swift").write_text("// fixture manifest\n")
                environment = {**os.environ, "DEVELOPER_DIR": str(root / "missing-developer")}
                result = subprocess.run(["/bin/bash", str(script), mode], env=environment,
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertIn("SwiftPM build failed", result.stdout + result.stderr)
                self.assertFalse(siri.exists())
                self.assertEqual(unrelated.read_bytes(), b"unrelated native resource")

    def test_unknown_profile_fails_before_touching_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            script, siri, _ = self.staged_script(Path(directory))
            result = subprocess.run(["/bin/bash", str(script), "unexpected"], capture_output=True, text=True)

            self.assertEqual(result.returncode, 2)
            self.assertTrue(siri.exists())
            self.assertIn("usage:", result.stderr)

    @unittest.skipUnless(platform.system() == "Darwin" and platform.machine() == "arm64"
                         and int(platform.mac_ver()[0].split(".")[0] or 0) >= 26,
                         "requires the native display public API host")
    def test_missing_package_preserves_optional_and_required_modes(self):
        for mode, expected in (("auto", 0), ("siri-required", 1)):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                script, siri, unrelated = self.staged_script(Path(directory))
                result = subprocess.run(["/bin/bash", str(script), mode],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertIn("native package is missing", result.stdout + result.stderr)
                self.assertFalse(siri.exists())
                self.assertEqual(unrelated.read_bytes(), b"unrelated native resource")


if __name__ == "__main__":
    unittest.main()
