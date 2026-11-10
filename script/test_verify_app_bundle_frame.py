import hashlib
import plistlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import verify_app_bundle as bundle_check


class FrameExportContract(unittest.TestCase):
    def verify_fixture(self, with_frame, development=False, bundle_id="dev.mimac.app"):
        with tempfile.TemporaryDirectory() as directory:
            app = Path(directory) / "MiMac.app"
            contents = app / "Contents"
            resources = contents / "Resources"
            resources.mkdir(parents=True)
            (contents / "MacOS").mkdir()
            (contents / "MacOS/MiMac").write_bytes(b"fixture")
            with (contents / "Info.plist").open("wb") as file:
                plistlib.dump({"CFBundleIdentifier": bundle_id, "CFBundleName": "MiMac Dev" if bundle_id.endswith(".dev") else "MiMac", "CFBundleExecutable": "MiMac"}, file)
            for name in [*bundle_check.REQUIRED, bundle_check.SIRI]:
                (resources / name).write_bytes(b"fixture")

            def exports(arguments, **_):
                name = Path(arguments[-1]).name
                symbol = bundle_check.REQUIRED.get(name, "mimac_siri_glass_abi_version")
                result = "00000000 T _" + symbol + "\n"
                if name == bundle_check.SIRI and with_frame:
                    result += "00000004 T _mimac_siri_glass_update_frame\n"
                return result

            def command(arguments, **_):
                if arguments[0] == "/usr/bin/lipo":
                    self.assertTrue(Path(arguments[1]).is_file())
                    self.assertEqual(arguments[2:], ["-verify_arch", "arm64"])

            with patch.object(bundle_check.subprocess, "run", side_effect=command), patch.object(bundle_check.subprocess, "check_output", side_effect=exports):
                return bundle_check.verify(app, "arm64", require_siri=True, development=development)

    def test_identity_must_match_requested_lane(self):
        self.assertIsNotNone(self.verify_fixture(True, development=True, bundle_id="dev.mimac.app.dev"))
        with self.assertRaisesRegex(ValueError, "expected MiMac bundle identifier"):
            self.verify_fixture(True, development=False, bundle_id="dev.mimac.app.dev")
        with self.assertRaisesRegex(ValueError, "expected MiMac bundle identifier"):
            self.verify_fixture(True, development=True)

    def test_old_library_is_rejected_before_launch(self):
        with self.assertRaisesRegex(ValueError, "missing typed frame export"):
            self.verify_fixture(False)

    def test_matching_library_is_accepted(self):
        self.assertIsNotNone(self.verify_fixture(True))


class AudioInstallerChecksumContract(unittest.TestCase):
    def test_reads_standard_sha256sum_sidecar(self):
        with tempfile.TemporaryDirectory() as directory:
            resources = Path(directory)
            package = resources / "MiMacAudio.pkg"
            package.write_bytes(b"product package bytes")
            digest = hashlib.sha256(package.read_bytes()).hexdigest()
            (resources / "MiMacAudio.pkg.sha256").write_text(f"{digest}  MiMacAudio.pkg\n", encoding="ascii")

            result = bundle_check.verify_audio_installer(resources, "arm64")

            self.assertEqual(result, {"sha256": digest})

    def test_required_installer_checks_packager_receipt_for_app_arch(self):
        with tempfile.TemporaryDirectory() as directory:
            resources = Path(directory)
            package = resources / "MiMacAudio.pkg"
            package.write_bytes(b"product package bytes")
            digest = hashlib.sha256(package.read_bytes()).hexdigest()
            (resources / "MiMacAudio.pkg.sha256").write_text(f"{digest}  MiMacAudio.pkg\n", encoding="ascii")
            receipt = resources / "MiMacAudio.pkg.json"
            receipt.write_text("{}\n", encoding="utf-8")
            checked = {"architecture": "amd64", "component_version": "0.1.0"}
            with patch.object(bundle_check, "verify_product_package", return_value=checked) as verify:
                result = bundle_check.verify_audio_installer(resources, "amd64", required=True)

            verify.assert_called_once_with(package, receipt, "amd64")
            self.assertEqual(result, {**checked, "sha256": digest})

    def test_required_installer_and_sidecar_are_mandatory(self):
        with tempfile.TemporaryDirectory() as directory:
            resources = Path(directory)
            with self.assertRaisesRegex(ValueError, "missing required"):
                bundle_check.verify_audio_installer(resources, "arm64", required=True)

            package = resources / "MiMacAudio.pkg"
            package.write_bytes(b"product package bytes")
            (resources / "MiMacAudio.pkg.sha256").write_text("wrong  MiMacAudio.pkg\n", encoding="ascii")
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                bundle_check.verify_audio_installer(resources, "arm64", required=True)


if __name__ == "__main__":
    unittest.main()
