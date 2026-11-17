#!/usr/bin/env python3
"""Exercise real admission code against controlled metadata without Swift/UI."""
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
NATIVE = ROOT / "internal/platform/siriglass/native"
SOURCE = NATIVE / "Sources/CGlassCompatibility/Compatibility.m"


class NativeDisplayCompatibilityTests(unittest.TestCase):
    def test_metadata_admission_requires_every_consumed_witness(self):
        with tempfile.TemporaryDirectory(prefix="mimac-display-admission-") as directory:
            output = Path(directory) / "contract"
            subprocess.run([
                "xcrun", "clang", "-Werror", "-Wno-unused-parameter",
                '-DMIMAC_COMPATIBILITY_SOURCE="' + str(SOURCE) + '"',
                "-I", str(NATIVE / "Sources/CGlassCompatibility/include"),
                str(NATIVE / "Contracts/CompatibilityContract.m"), "-o", str(output),
            ], check=True)
            subprocess.run([str(output)], check=True)


if __name__ == "__main__":
    unittest.main()
