#!/usr/bin/env python3
"""Read-only macOS ABI regression; no GUI, event posting, or audio capture."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


@unittest.skipUnless(sys.platform == "darwin", "macOS CoreAudio contract")
class NativeContract(unittest.TestCase):
    def test_typed_numeric_property_getter(self):
        source = Path(__file__).parent / "fixtures/LiveAcceptanceAX.swift"
        with tempfile.TemporaryDirectory(prefix="mimac-acceptance-contract-") as folder:
            binary = Path(folder) / "probe"
            subprocess.run(["swiftc", str(source), "-o", str(binary)], check=True, timeout=60)
            result = subprocess.run([str(binary), "--self-test"], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            value = json.loads(result.stdout)
            self.assertEqual(value["status"], "ok")
            self.assertEqual(value["scope"], "read_only_numeric_property_contract")
            self.assertIsInstance(value["default_output_object"], int)


if __name__ == "__main__":
    unittest.main()
