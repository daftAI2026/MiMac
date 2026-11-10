#!/usr/bin/env python3

from pathlib import Path
import contextlib
import io
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import build_audio_compat as audio_build


class AudioComponentArchitectureTests(unittest.TestCase):
    def test_arch_argument_selects_arm64_and_amd64_clang_targets(self):
        self.assertEqual(audio_build.build_parser().parse_args(["--arch", "arm64"]).arch, "arm64")
        self.assertEqual(audio_build.build_parser().parse_args(["--arch", "amd64"]).arch, "amd64")
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            audio_build.build_parser().parse_args(["--arch", "x86_64"])

    def test_default_target_matches_native_go_architecture(self):
        self.assertEqual(audio_build.resolve_architecture(None, "arm64"), ("arm64", "arm64", "arm64", "arm64"))
        self.assertEqual(audio_build.resolve_architecture(None, "x86_64"), ("amd64", "x86_64", "amd64", "x86_64"))

    def test_explicit_target_is_separate_from_native_runner(self):
        self.assertEqual(audio_build.resolve_architecture("amd64", "arm64"), ("amd64", "x86_64", "arm64", "arm64"))
        self.assertEqual(audio_build.resolve_architecture("arm64", "x86_64"), ("arm64", "arm64", "amd64", "x86_64"))

    def test_bundle_abi_runs_native_or_under_available_rosetta(self):
        self.assertEqual(audio_build.bundle_test_execution("arm64", "arm64"), ("native", "arm64"))
        self.assertEqual(audio_build.bundle_test_execution("x86_64", "x86_64"), ("native", "x86_64"))
        self.assertEqual(audio_build.bundle_test_execution("x86_64", "arm64", True), ("rosetta", "x86_64"))

    def test_unavailable_cross_arch_runner_is_reported_as_not_run(self):
        self.assertEqual(audio_build.bundle_test_execution("x86_64", "arm64", False), ("not-run", None))
        self.assertEqual(audio_build.bundle_test_execution("arm64", "x86_64", False), ("not-run", None))

    def test_unsupported_native_machine_is_rejected(self):
        with self.assertRaisesRegex(audio_build.BuildError, "unsupported native"):
            audio_build.resolve_architecture(None, "riscv64")
        with self.assertRaisesRegex(audio_build.BuildError, "unsupported target"):
            audio_build.resolve_architecture("x86_64", "arm64")


if __name__ == "__main__":
    unittest.main()
