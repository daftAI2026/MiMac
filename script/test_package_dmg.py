#!/usr/bin/env python3
"""Focused safety and cleanup tests for script/package_dmg.py."""

import plistlib
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

import package_dmg


def tiny_png(width=package_dmg.BACKGROUND_WIDTH, height=package_dmg.BACKGROUND_HEIGHT):
    return (
        b"\x89PNG\r\n\x1a\n"
        + b"\x00\x00\x00\x0dIHDR"
        + width.to_bytes(4, "big")
        + height.to_bytes(4, "big")
        + b"\x08\x06\x00\x00\x00"
    )


class PackageDMGTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.bundle = self.root / "MiMac Dev.app"
        info_dir = self.bundle / "Contents"
        info_dir.mkdir(parents=True)
        (info_dir / "Info.plist").write_bytes(plistlib.dumps({
            "CFBundleDisplayName": "MiMac Dev",
            "CFBundleShortVersionString": "4.2.1-dev.7",
        }))

    def tearDown(self):
        self.temporary.cleanup()

    def test_reads_display_name_and_version_from_app_plist(self):
        _, info_path = package_dmg.resolve_bundle(self.bundle)
        metadata = package_dmg.read_bundle_metadata(self.bundle, info_path)

        self.assertEqual(metadata.display_name, "MiMac Dev")
        self.assertEqual(metadata.version, "4.2.1-dev.7")
        self.assertEqual(metadata.volume_name, "MiMac Dev 4.2.1-dev.7")
        self.assertEqual(metadata.bundle_name, "MiMac Dev.app")

    def test_requires_bundle_version_and_valid_app_directory(self):
        info_path = self.bundle / "Contents" / "Info.plist"
        info_path.write_bytes(plistlib.dumps({"CFBundleDisplayName": "MiMac"}))
        with self.assertRaisesRegex(package_dmg.PackageError, "CFBundleShortVersionString"):
            package_dmg.read_bundle_metadata(self.bundle, info_path)

        not_an_app = self.root / "not-an-app"
        not_an_app.mkdir()
        with self.assertRaisesRegex(package_dmg.PackageError, r"\.app directory"):
            package_dmg.resolve_bundle(not_an_app)

    def test_output_requires_new_dmg_outside_app_and_existing_parent(self):
        valid = self.root / "MiMac.dmg"
        expected = valid.parent.resolve() / valid.name
        self.assertEqual(package_dmg.resolve_output(valid, self.bundle), expected)

        existing = self.root / "existing.dmg"
        existing.write_bytes(b"keep")
        with self.assertRaisesRegex(package_dmg.PackageError, "refusing to overwrite"):
            package_dmg.resolve_output(existing, self.bundle)

        with self.assertRaisesRegex(package_dmg.PackageError, r"\.dmg file"):
            package_dmg.resolve_output(self.root / "MiMac.zip", self.bundle)

        with self.assertRaisesRegex(package_dmg.PackageError, "outside the signed app bundle"):
            package_dmg.resolve_output(self.bundle / "nested.dmg", self.bundle)

        with self.assertRaisesRegex(package_dmg.PackageError, "output directory does not exist"):
            package_dmg.resolve_output(self.root / "missing" / "MiMac.dmg", self.bundle)

    def test_validates_background_png_dimensions(self):
        image = self.root / "background.png"
        image.write_bytes(tiny_png())
        package_dmg.validate_background_png(image)

        image.write_bytes(tiny_png(width=1, height=1))
        with self.assertRaisesRegex(package_dmg.PackageError, "expected 1200x536"):
            package_dmg.validate_background_png(image)

    def test_detaches_private_mount_after_copy_failure_and_cleans_work_directory(self):
        output = self.root / "failed.dmg"
        commands = []

        def runner(command, *, capture=False):
            command = [str(part) for part in command]
            commands.append(command)
            if command[:2] == ["hdiutil", "attach"]:
                return "/dev/disk42 Apple_HFS /private/mount\n"
            if command[0] == "ditto":
                raise package_dmg.PackageError("injected copy failure")
            return ""

        def renderer(path, *, runner):
            Path(path).write_bytes(tiny_png())

        with self.assertRaisesRegex(package_dmg.PackageError, "injected copy failure"):
            package_dmg._mount_image(
                self.bundle,
                output,
                package_dmg.read_bundle_metadata(self.bundle),
                runner=runner,
                diskutil_supported=False,
                renderer=renderer,
                dsstore_writer=lambda *args, **kwargs: None,
                sign_identity=None,
            )

        self.assertIn(["hdiutil", "detach", "/dev/disk42"], commands)
        self.assertFalse(output.exists())
        self.assertEqual(list(self.root.glob(".mimac-dmg-*")), [])

    def test_successful_packaging_detaches_before_conversion_and_places_output(self):
        output = self.root / "MiMac.dmg"
        commands = []

        def runner(command, *, capture=False):
            command = [str(part) for part in command]
            commands.append(command)
            if command[:2] == ["hdiutil", "create"]:
                # Blank-image creation accepts -type; -format needs a source.
                self.assertNotIn("-format", command)
                self.assertEqual(command[command.index("-type") + 1], "UDIF")
            if command[:2] == ["hdiutil", "attach"]:
                return "/dev/disk43 Apple_HFS /private/mount\n"
            if command[0] == "ditto":
                source, destination = Path(command[-2]), Path(command[-1])
                import shutil
                shutil.copytree(source, destination, symlinks=True)
            elif command[:2] == ["hdiutil", "convert"]:
                Path(command[-1]).write_bytes(b"compressed image")
            return ""

        def renderer(path, *, runner):
            Path(path).write_bytes(tiny_png())

        def write_store(path, **kwargs):
            Path(path).write_bytes(b"finder metadata")
            self.assertEqual(kwargs["app_name"], "MiMac Dev.app")
            self.assertEqual(Path(kwargs["background_path"]).name, "background.png")

        result = package_dmg._mount_image(
            self.bundle,
            output,
            package_dmg.read_bundle_metadata(self.bundle),
            runner=runner,
            diskutil_supported=False,
            renderer=renderer,
            dsstore_writer=write_store,
            sign_identity="Test Identity",
        )

        self.assertEqual(result, output)
        self.assertEqual(output.read_bytes(), b"compressed image")
        detach_index = commands.index(["hdiutil", "detach", "/dev/disk43"])
        convert_index = next(i for i, command in enumerate(commands) if command[:2] == ["hdiutil", "convert"])
        sign_index = next(i for i, command in enumerate(commands) if command[0] == "codesign")
        self.assertLess(detach_index, convert_index)
        self.assertLess(convert_index, sign_index)
        self.assertFalse(list(self.root.glob(".mimac-dmg-*")))

    def test_packaging_rejects_non_macos_hosts_before_side_effects(self):
        with mock.patch.object(package_dmg.platform, "system", return_value="Linux"):
            with self.assertRaisesRegex(package_dmg.PackageError, "only be packaged on macOS"):
                package_dmg.package_dmg(self.bundle, self.root / "MiMac.dmg")


if __name__ == "__main__":
    unittest.main()
