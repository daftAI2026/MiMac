#!/usr/bin/env python3

import hashlib
import json
from pathlib import Path
import plistlib
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

sys.path.insert(0, str(Path(__file__).resolve().parent))
import package_audio_component as package_audio


class PackageAudioComponentTests(unittest.TestCase):
    def test_default_resource_path_uses_go_arch(self):
        root = Path("/tmp/mimac")
        self.assertEqual(
            package_audio.output_path(root, "arm64"),
            root / "resources" / "darwin-arm64" / "MiMacAudio.pkg",
        )
        self.assertEqual(
            package_audio.output_path(root, "amd64"),
            root / "resources" / "darwin-amd64" / "MiMacAudio.pkg",
        )
        with self.assertRaises(package_audio.PackageError):
            package_audio.output_path(root, "x86_64")

    def test_input_package_and_manifest_must_be_a_pair(self):
        with self.assertRaisesRegex(package_audio.PackageError, "supplied together"):
            package_audio.package_inputs("component.pkg", None, "arm64")
        with self.assertRaisesRegex(package_audio.PackageError, "supplied together"):
            package_audio.package_inputs(None, "manifest.json", "arm64")

    def test_manifest_hash_arch_and_uninstalled_state_are_required(self):
        with tempfile.TemporaryDirectory(prefix="mimac-audio-manifest-test-") as temp:
            root = Path(temp)
            package = root / "MiMacAudio-local-test.pkg"
            package.write_bytes(b"tested component package")
            manifest = {
                "build": {"architecture": "arm64"},
                "bundle": {
                    "identifier": "dev.mimac.audio.compat",
                    "binary_sha256": "b" * 64,
                    "notices": ["libASPL-LICENSE"],
                },
                "package": {
                    "bytes": package.stat().st_size,
                    "sha256": hashlib.sha256(package.read_bytes()).hexdigest(),
                    "install_path": package_audio.COMPONENT_INSTALL_PATH,
                    "installed": False,
                    "restarts_audio": False,
                    "postinstall_scripts": False,
                    "payload_signature_hash_abi_verified": True,
                },
                "tests": {"bundle_abi": {"status": "passed"}},
                "installed": False,
                "audio_service_restarted": False,
            }
            manifest_path = root / "manifest.json"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

            self.assertEqual(
                package_audio.load_and_check_manifest(manifest_path, package, "arm64"),
                manifest,
            )
            with self.assertRaisesRegex(package_audio.PackageError, "does not match GOARCH"):
                package_audio.load_and_check_manifest(manifest_path, package, "amd64")

            original_bytes = package.read_bytes()
            package.write_bytes(b"X" + original_bytes[1:])
            with self.assertRaisesRegex(package_audio.PackageError, "SHA-256 differs"):
                package_audio.load_and_check_manifest(manifest_path, package, "arm64")

    def test_distribution_limits_install_to_local_system_and_requires_restart(self):
        xml = package_audio.build_distribution_xml("MiMacAudio-local-test.pkg", "dev.mimac.audio.compat.pkg", "0.1.0")
        with tempfile.TemporaryDirectory(prefix="mimac-audio-dist-test-") as temp:
            path = Path(temp) / "Distribution"
            path.write_bytes(xml)
            package_audio._validate_distribution(path, "MiMacAudio-local-test.pkg", "dev.mimac.audio.compat.pkg", "0.1.0")

            root = ET.fromstring(xml)
            root.findall("pkg-ref")[-1].text = "#MiMacAudio-local-test.pkg"
            path.write_bytes(ET.tostring(root))
            package_audio._validate_distribution(path, "MiMacAudio-local-test.pkg", "dev.mimac.audio.compat.pkg", "0.1.0")

            root.find("domains").set("enable_anywhere", "true")
            path.write_bytes(ET.tostring(root))
            with self.assertRaisesRegex(package_audio.PackageError, "only to localSystem"):
                package_audio._validate_distribution(path, "MiMacAudio-local-test.pkg", "dev.mimac.audio.compat.pkg", "0.1.0")

            root.find("domains").set("enable_anywhere", "false")
            root.find("pkg-ref").set("onConclusion", "none")
            path.write_bytes(ET.tostring(root))
            with self.assertRaisesRegex(package_audio.PackageError, "require restart"):
                package_audio._validate_distribution(path, "MiMacAudio-local-test.pkg", "dev.mimac.audio.compat.pkg", "0.1.0")

    def test_component_validation_checks_package_identity_version_binary_and_notice(self):
        with tempfile.TemporaryDirectory(prefix="mimac-audio-component-test-") as temp:
            expanded = Path(temp)
            payload = expanded / "Payload"
            bundle = payload / package_audio.PAYLOAD_RELATIVE_PATH
            contents = bundle / "Contents"
            resources = contents / "Resources"
            macos = contents / "MacOS"
            resources.mkdir(parents=True)
            macos.mkdir()
            binary = macos / "MiMacAudio"
            binary.write_bytes(b"verified audio bundle executable")
            (resources / "libASPL-LICENSE").write_text("upstream notice", encoding="utf-8")
            (contents / "Info.plist").write_bytes(plistlib.dumps({
                "CFBundleIdentifier": "dev.mimac.audio.compat",
                "CFBundleShortVersionString": "0.1.0",
            }))
            (expanded / "PackageInfo").write_text(
                '<pkg-info identifier="dev.mimac.audio.compat.pkg" version="0.1.0" '
                'install-location="/" auth="root" postinstall-action="none"/>',
                encoding="utf-8",
            )
            manifest = {
                "bundle": {
                    "identifier": "dev.mimac.audio.compat",
                    "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                    "notices": ["libASPL-LICENSE"],
                }
            }

            _, version, package_id, _ = package_audio._validate_component(expanded, manifest)
            self.assertEqual((version, package_id), ("0.1.0", "dev.mimac.audio.compat.pkg"))

            (expanded / "Scripts").mkdir()
            with self.assertRaisesRegex(package_audio.PackageError, "contains scripts"):
                package_audio._validate_component(expanded, manifest)

    def test_expanded_payload_comparison_catches_changed_notice(self):
        with tempfile.TemporaryDirectory(prefix="mimac-audio-payload-test-") as temp:
            root = Path(temp)
            first = root / "first"
            second = root / "second"
            first.mkdir()
            second.mkdir()
            (first / "notice.txt").write_bytes(b"original")
            (second / "notice.txt").write_bytes(b"changed")
            self.assertNotEqual(
                package_audio._payload_digests(first),
                package_audio._payload_digests(second),
            )

    def test_receipt_does_not_claim_cross_arch_abi_test_when_it_was_skipped(self):
        with tempfile.TemporaryDirectory(prefix="mimac-audio-receipt-test-") as temp:
            root = Path(temp)
            package = root / "MiMacAudio.pkg"
            package.write_bytes(b"product package")
            payload = root / "Payload"
            payload.mkdir()
            (payload / "notice.txt").write_bytes(b"notice")
            manifest = {
                "build": {"architecture": "amd64", "native_architecture": "x86_64"},
                "bundle": {
                    "identifier": "dev.mimac.audio.compat",
                    "binary_sha256": "a" * 64,
                    "notices": ["libASPL-LICENSE"],
                },
                "package": {
                    "path": ".build/audio-compat/MiMacAudio-local-test.pkg",
                    "sha256": "b" * 64,
                    "payload_signature_hash_abi_verified": False,
                },
                "tests": {"bundle_abi": {"status": "not_run_cross_arch", "mode": "not-run"}},
            }

            receipt = package_audio._receipt_for_product(package, manifest, "0.1.0", "dev.mimac.audio.compat.pkg", payload)

            self.assertFalse(receipt["payload_signature_hash_abi_verified"])
            self.assertEqual(receipt["bundle_abi_test"]["status"], "not_run_cross_arch")

    def test_output_package_and_parent_symlinks_are_rejected_before_resolution(self):
        with tempfile.TemporaryDirectory(prefix="mimac-audio-output-test-") as temp:
            root = Path(temp)
            target = root / "target.pkg"
            target.write_bytes(b"existing target")
            package_link = root / "package-link.pkg"
            package_link.symlink_to(target)
            with self.assertRaisesRegex(package_audio.PackageError, "refusing symlink"):
                package_audio.resolve_output_path(package_link)

            output_dir = root / "output-dir"
            output_dir.symlink_to(root, target_is_directory=True)
            with self.assertRaisesRegex(package_audio.PackageError, "refusing symlink"):
                package_audio.resolve_output_path(output_dir / "MiMacAudio.pkg")


if __name__ == "__main__":
    unittest.main()
