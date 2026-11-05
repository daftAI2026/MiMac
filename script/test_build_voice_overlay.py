#!/usr/bin/env python3

from pathlib import Path
import shutil
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

    def test_unknown_profile_fails_before_touching_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            script, siri, _ = self.staged_script(Path(directory))
            result = subprocess.run(["/bin/bash", str(script), "unexpected"], capture_output=True, text=True)

            self.assertEqual(result.returncode, 2)
            self.assertTrue(siri.exists())
            self.assertIn("usage:", result.stderr)


if __name__ == "__main__":
    unittest.main()
