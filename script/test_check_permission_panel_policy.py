import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import check_permission_panel_policy as policy


class PermissionPanelPolicyPathTests(unittest.TestCase):
    def test_default_path_uses_protected_checkout_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory).resolve()
            checkout = home / "Desktop" / "source"
            checkout.mkdir(parents=True)
            with patch.object(policy.Path, "home", return_value=home):
                result = policy.default_app_path(checkout, "arm64")
            self.assertTrue(result.is_relative_to(home / "Library/Caches/MiMac/dev-builds"))
            self.assertTrue(str(result).endswith(".mygo/dev/darwin-arm64/MiMac Dev.app"))

    def test_explicit_app_path_keeps_priority_over_default(self):
        with tempfile.TemporaryDirectory() as directory:
            bundle = Path(directory).resolve() / "MiMac Dev.app"
            executable = bundle / "Contents/MacOS/MiMac Dev"
            executable.parent.mkdir(parents=True)
            executable.write_bytes(b"fixture")
            props = {
                "mainThread": 1,
                "matchedWindows": 1,
                "runtimeAvailable": 1,
                "panelCount": 1,
                "level": 3,
                "styleMask": policy.NONACTIVATING_PANEL_MASK,
                "hidesOnDeactivate": 0,
                "becomesKeyOnlyIfNeeded": 1,
                "canBecomeKey": 1,
                "canBecomeMain": 0,
                "isKey": 1,
                "isMain": 0,
                "windowCount": 1,
            }
            running = {
                "title": policy.APP_NAME,
                "bundlePath": str(bundle),
                "executablePath": str(executable),
                "pid": 42,
            }
            with patch.object(policy.sys, "platform", "darwin"), \
                    patch.object(policy, "default_app_path") as default_path, \
                    patch.object(policy, "inspect_bundle", return_value=(bundle, executable, policy.BUNDLE_ID)) as inspect, \
                    patch.object(policy, "running_app_snapshot", return_value=running), \
                    patch.object(policy, "process_executable_path", return_value=executable), \
                    patch.object(policy, "sha256_file", return_value="a" * 64), \
                    patch.object(policy, "run_lldb", return_value={"status": "observed", "detached": True, "properties": props}), \
                    patch.object(policy, "emit", return_value=0):
                self.assertEqual(policy.main(["--app", str(bundle)]), 0)
            default_path.assert_not_called()
            inspect.assert_called_once_with(bundle)


if __name__ == "__main__":
    unittest.main()
