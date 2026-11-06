"""Read-only CoreGraphics construction contract: no posting or microphone."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


@unittest.skipUnless(sys.platform == "darwin", "macOS CoreGraphics contract")
class NativeEventConstruction(unittest.TestCase):
    def test_constructor_edge_is_not_the_native_modifier_event_type(self):
        source = Path(__file__).parent / "fixtures/KeyEventConstruction.swift"
        with tempfile.TemporaryDirectory(prefix="mimac-event-contract-") as folder:
            binary = Path(folder) / "probe"
            subprocess.run(["swiftc", str(source), "-o", str(binary)], check=True, timeout=60)
            value = json.loads(subprocess.check_output([str(binary)], text=True, timeout=5))
        self.assertEqual(len(value["rows"]), 8)
        for row in value["rows"]:
            with self.subTest(code=row["code"], down=row["constructor_down"]):
                if row["code"] in [54, 61, 63]:
                    expected = value["enum_flagsChanged"]
                else:
                    expected = value["enum_keyDown"] if row["constructor_down"] else value["enum_keyUp"]
                self.assertEqual(row["type_before_flags"], expected)
                self.assertEqual(row["type_after_flags"], expected)
                self.assertEqual(row["event_user_data"], 0x4D694D61634B6579)
                if not row["constructor_down"]:
                    self.assertEqual(row["flags"], 0)


if __name__ == "__main__":
    unittest.main()
