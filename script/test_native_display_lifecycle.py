#!/usr/bin/env python3
"""Run the product's display lifetime model without a native event loop."""
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
NATIVE = ROOT / "internal/platform/siriglass/native"


class NativeDisplayLifecycleTests(unittest.TestCase):
    def test_input_exit_and_recovery_edges(self):
        with tempfile.TemporaryDirectory(prefix="mimac-display-lifecycle-") as directory:
            binary = Path(directory) / "lifecycle-contract"
            subprocess.run([
                "xcrun", "swiftc", "-parse-as-library",
                str(NATIVE / "Sources/SiriGlassModel/ReferenceChatInput.swift"),
                str(NATIVE / "Contracts/PresentationLifecycleContract.swift"),
                "-o", str(binary),
            ], check=True)
            subprocess.run([str(binary)], check=True)

    def test_actual_scale_targets(self):
        with tempfile.TemporaryDirectory(prefix="mimac-display-scale-") as directory:
            root = Path(directory)
            subprocess.run([
                "xcrun", "swiftc", "-parse-as-library", "-emit-library", "-emit-module",
                "-module-name", "SiriGlassModel",
                "-emit-module-path", str(root / "SiriGlassModel.swiftmodule"),
                str(NATIVE / "Sources/SiriGlassModel/ReferenceChatInput.swift"),
                "-o", str(root / "libSiriGlassModel.dylib"),
            ], check=True)
            binary = root / "scale-contract"
            subprocess.run([
                "xcrun", "swiftc", "-parse-as-library", "-I", str(root),
                "-L", str(root), "-lSiriGlassModel", "-Xlinker", "-rpath", "-Xlinker", str(root),
                str(NATIVE / "Sources/SiriGlassRenderer/ChatThinkingScale.swift"),
                str(NATIVE / "Contracts/PresentationScaleContract.swift"),
                "-o", str(binary),
            ], check=True)
            subprocess.run([str(binary)], check=True)


if __name__ == "__main__":
    unittest.main()
