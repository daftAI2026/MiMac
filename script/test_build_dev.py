import copy
import json
from pathlib import Path
import unittest
import tempfile
from unittest.mock import patch

import build_dev


class DevelopmentBuildIsolation(unittest.TestCase):
    def test_derived_configuration_preserves_production_and_points_at_same_source(self):
        root = Path(__file__).resolve().parent.parent
        original = json.loads((root / "mygo.json").read_text())
        saved = copy.deepcopy(original)
        dev = build_dev.development_config(original, root)
        self.assertEqual(original, saved)
        self.assertEqual(dev["identifier"], "dev.mimac.app.dev")
        self.assertEqual(dev["name"], "MiMac Dev")
        self.assertEqual(dev["out"], str(root / ".mygo/dev"))
        self.assertEqual(Path(dev["main"]).resolve(), (root / original["main"]).resolve())
        self.assertTrue(all(Path(item).is_file() for item in dev["resources"]))
        self.assertEqual(dev["macos"]["infoPlist"], original["macos"]["infoPlist"])

    def test_daily_running_refuses_device_handoff(self):
        for count in ("1", "2"):
            with self.subTest(count=count), patch.object(build_dev.subprocess, "check_output", return_value=count):
                with self.assertRaisesRegex(RuntimeError, "Quit daily MiMac"):
                    build_dev.require_device_handoff()
        with tempfile.TemporaryDirectory() as directory, patch.object(build_dev.Path, "home", return_value=Path(directory)), patch.object(build_dev.subprocess, "check_output", return_value="0"):
            build_dev.require_device_handoff()

    def test_unreleased_daily_mapping_refuses_device_handoff(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(build_dev.Path, "home", return_value=Path(directory)), patch.object(build_dev.subprocess, "check_output", return_value="0"):
            recovery = Path(directory) / "Library/Application Support/MiMac/voice-mapping-recovery.plist"
            recovery.parent.mkdir(parents=True)
            recovery.write_bytes(b"retained device mapping")
            with self.assertRaisesRegex(RuntimeError, "Restore daily MiMac"):
                build_dev.require_device_handoff()
            self.assertEqual(recovery.read_bytes(), b"retained device mapping")

    def test_go_boolean_spellings_use_the_same_handoff_gate(self):
        for value in ('--device-access', '-device-access', '--device-access=true', '-device-access=1', '--device-access=True'):
            self.assertTrue(build_dev.device_access_requested([value]))
        self.assertFalse(build_dev.device_access_requested(['--device-access=false']))
        self.assertFalse(build_dev.device_access_requested(['--device-access', '--device-access=false']))

    def test_stop_scope_uses_full_executable_path(self):
        daily = "/Applications/MiMac.app/Contents/MacOS/MiMac"
        dev = Path("/tmp/app/.mygo/dev/darwin-arm64/MiMac Dev.app/Contents/MacOS/MiMac Dev")
        other = "/tmp/other/MiMac Dev.app/Contents/MacOS/MiMac Dev"
        rows = f"100 {daily}\n101 {dev}\n102 {other}\n"
        with patch.object(build_dev.subprocess, "check_output", return_value=rows):
            self.assertEqual(build_dev.owned_pids(dev), [101])

    def test_primary_repository_root_comes_from_git_common_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory).resolve()
            root = base / "web" / "mimac"
            (root / "mygo.json").parent.mkdir(parents=True)
            (root / "mygo.json").write_text(json.dumps({"identifier": "dev.mimac.app", "name": "MiMac"}) + "\n")
            common_dir = root / ".git"
            common_dir.mkdir()
            checkout = base / "web" / "mimac-research" / "permission-entry" / "worktree"
            checkout.mkdir(parents=True)
            with patch.object(build_dev.subprocess, "check_output", return_value=str(common_dir) + "\n") as query:
                self.assertEqual(build_dev.primary_repository_root(checkout), root)

        self.assertEqual(query.call_args.args[0], ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"])
        self.assertEqual(query.call_args.kwargs["cwd"], checkout)

    def test_launch_registration_and_device_handoff_precede_launch_services(self):
        events = []
        bundle = Path("/fixed/project/.mygo/dev/darwin-arm64/MiMac Dev.app")
        root = Path("/fixed/project")
        primary_root = Path("/primary/mimac")

        def record_registration(*args):
            events.append(("registration", args))

        def record_handoff():
            events.append(("device_handoff",))

        def record_open(command, **kwargs):
            events.append(("open", command, kwargs))

        with patch.object(build_dev, "primary_repository_root", return_value=primary_root), \
                patch.object(build_dev, "ensure_current_dev_registration", side_effect=record_registration), \
                patch.object(build_dev, "require_device_handoff", side_effect=record_handoff), \
                patch.object(build_dev.subprocess, "run", side_effect=record_open):
            build_dev.launch_development(
                bundle, root, "arm64", ["-n"], ["--device-access"], device_access=True
            )

        self.assertEqual([event[0] for event in events], ["registration", "device_handoff", "open"])
        self.assertEqual(events[0][1], (bundle, root, "arm64", primary_root))
        self.assertEqual(events[2][1], ["/usr/bin/open", "-n", str(bundle), "--args", "--device-access"])
        self.assertEqual(events[2][2], {"check": True})

    def test_verify_launch_keeps_background_wait_and_smoke_arguments(self):
        bundle = Path("/fixed/project/.mygo/dev/darwin-arm64/MiMac Dev.app")
        root = Path("/fixed/project")
        primary_root = Path("/primary/mimac")
        events = []

        with patch.object(build_dev, "primary_repository_root", return_value=primary_root), \
                patch.object(build_dev, "ensure_current_dev_registration") as select_registration, \
                patch.object(build_dev.subprocess, "run", side_effect=lambda command, **kwargs: events.append((command, kwargs))):
            build_dev.launch_development(
                bundle,
                root,
                "arm64",
                ["-g", "-n", "-W"],
                ["--show-settings=false", "--smoke-duration", "1s", "--host-report", "/tmp/host.json"],
                timeout=15,
            )

        select_registration.assert_called_once_with(bundle, root, "arm64", primary_root)
        self.assertEqual(events, [(
            ["/usr/bin/open", "-g", "-n", "-W", str(bundle), "--args",
             "--show-settings=false", "--smoke-duration", "1s", "--host-report", "/tmp/host.json"],
            {"check": True, "timeout": 15},
        )])


if __name__ == "__main__":
    unittest.main()
