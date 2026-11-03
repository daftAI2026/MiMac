import plistlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import verify_app_bundle as bundle_check


class FrameExportContract(unittest.TestCase):
    def verify_fixture(self, with_frame):
        with tempfile.TemporaryDirectory() as directory:
            app = Path(directory) / "MiMac.app"
            contents = app / "Contents"
            resources = contents / "Resources"
            resources.mkdir(parents=True)
            (contents / "MacOS").mkdir()
            (contents / "MacOS/MiMac").write_bytes(b"fixture")
            with (contents / "Info.plist").open("wb") as file:
                plistlib.dump({"CFBundleIdentifier": "dev.mimac.app", "CFBundleExecutable": "MiMac"}, file)
            for name in [*bundle_check.REQUIRED, bundle_check.SIRI]:
                (resources / name).write_bytes(b"fixture")

            def exports(arguments, **_):
                name = Path(arguments[-1]).name
                symbol = bundle_check.REQUIRED.get(name, "mimac_siri_glass_abi_version")
                result = "00000000 T _" + symbol + "\n"
                if name == bundle_check.SIRI and with_frame:
                    result += "00000004 T _mimac_siri_glass_update_frame\n"
                return result

            with patch.object(bundle_check.subprocess, "run"), patch.object(bundle_check.subprocess, "check_output", side_effect=exports):
                return bundle_check.verify(app, "arm64", require_siri=True)

    def test_old_library_is_rejected_before_launch(self):
        with self.assertRaisesRegex(ValueError, "missing typed frame export"):
            self.verify_fixture(False)

    def test_matching_library_is_accepted(self):
        self.assertIsNotNone(self.verify_fixture(True))


if __name__ == "__main__":
    unittest.main()
