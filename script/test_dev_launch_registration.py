import json
import plistlib
import subprocess
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

from dev_launch_registration import (
    DEV_BUNDLE_IDENTIFIER,
    LSREGISTER,
    SystemLaunchServices,
    ensure_current_dev_registration,
)


class MockLaunchServices:
    def __init__(self, registered_by_id=None, defaults=None, unregister_code=0, register_code=0, select_on_register=True):
        self.registered_by_id = {key: list(value) for key, value in (registered_by_id or {}).items()}
        self.defaults = dict(defaults or {})
        self.unregister_code = unregister_code
        self.register_code = register_code
        self.select_on_register = select_on_register
        self.queried_ids = []
        self.unregister_calls = []
        self.register_calls = []
        self.default_queries = []

    def registered_urls(self, bundle_identifier):
        self.queried_ids.append(bundle_identifier)
        return list(self.registered_by_id.get(bundle_identifier, []))

    def default_url(self, bundle_identifier):
        self.default_queries.append(bundle_identifier)
        return self.defaults.get(bundle_identifier)

    def unregister(self, path):
        self.unregister_calls.append(Path(path))
        result = subprocess.CompletedProcess(["lsregister", "-u", str(path)], self.unregister_code, "", "mock unregister failure" if self.unregister_code else "")
        if self.unregister_code == 0:
            for bundle_identifier, paths in self.registered_by_id.items():
                self.registered_by_id[bundle_identifier] = [item for item in paths if Path(item) != Path(path)]
            if self.defaults.get(DEV_BUNDLE_IDENTIFIER) == str(path):
                remaining = self.registered_by_id.get(DEV_BUNDLE_IDENTIFIER, [])
                self.defaults[DEV_BUNDLE_IDENTIFIER] = remaining[0] if remaining else None
        return result

    def register(self, path):
        self.register_calls.append(Path(path))
        result = subprocess.CompletedProcess(["lsregister", "-f", str(path)], self.register_code, "", "mock register failure" if self.register_code else "")
        if self.register_code == 0:
            paths = self.registered_by_id.setdefault(DEV_BUNDLE_IDENTIFIER, [])
            if str(path) not in paths:
                paths.append(str(path))
            if self.select_on_register:
                self.defaults[DEV_BUNDLE_IDENTIFIER] = str(path)
        return result


class DevLaunchRegistrationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        base = Path(self.temporary.name).resolve()
        self.primary_repository_root = base / "web" / "mimac"
        (self.primary_repository_root / "mygo.json").parent.mkdir(parents=True, exist_ok=True)
        (self.primary_repository_root / "mygo.json").write_text(json.dumps({"identifier": "dev.mimac.app", "name": "MiMac"}) + "\n")
        self.research_root = self.primary_repository_root.parent / "mimac-research"
        self.project_root = self.research_root / "permission-entry-20261111" / "worktree"
        self.cache_root = base / "Caches" / "MiMac"
        self.selected = self.project_root / ".mygo" / "dev" / "darwin-arm64" / "MiMac Dev.app"
        self._make_bundle(self.selected, DEV_BUNDLE_IDENTIFIER)

    def tearDown(self):
        self.temporary.cleanup()

    @staticmethod
    def _make_bundle(path, bundle_identifier):
        info_path = path / "Contents" / "Info.plist"
        info_path.parent.mkdir(parents=True, exist_ok=True)
        with info_path.open("wb") as handle:
            plistlib.dump({"CFBundleIdentifier": bundle_identifier}, handle)
        return path

    def _select(self, backend):
        return ensure_current_dev_registration(
            self.selected,
            self.project_root,
            "arm64",
            self.primary_repository_root,
            backend=backend,
            cache_root=self.cache_root,
        )

    def test_old_default_is_removed_before_current_dev_becomes_reopen_target(self):
        old = self._make_bundle(
            self.research_root / "alignment-worktree" / ".mygo" / "dev" / "darwin-arm64" / "MiMac Dev.app",
            DEV_BUNDLE_IDENTIFIER,
        )
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: [str(old), str(self.selected)]},
            defaults={DEV_BUNDLE_IDENTIFIER: str(old)},
        )

        result = self._select(backend)

        self.assertEqual(result, self.selected)
        self.assertEqual(backend.unregister_calls, [old])
        self.assertEqual(backend.register_calls, [self.selected])
        self.assertEqual(backend.default_url(DEV_BUNDLE_IDENTIFIER), str(self.selected))
        self.assertEqual(backend.registered_by_id[DEV_BUNDLE_IDENTIFIER], [str(self.selected)])

    def test_missing_known_dev_bundle_record_is_unregistered_safely(self):
        missing = (
            self.research_root
            / "delivery-daily-use-20261110"
            / ".mygo"
            / "dev"
            / "darwin-arm64"
            / "MiMac Dev.app"
        )
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: [str(missing)]},
            defaults={DEV_BUNDLE_IDENTIFIER: str(missing)},
        )

        self._select(backend)

        self.assertFalse(missing.exists())
        self.assertEqual(backend.unregister_calls, [missing])
        self.assertEqual(backend.register_calls, [self.selected])
        self.assertEqual(backend.default_url(DEV_BUNDLE_IDENTIFIER), str(self.selected))

    def test_registration_queries_and_changes_only_dev_identity(self):
        daily_path = "/Applications/MiMac.app"
        old = self._make_bundle(
            self.cache_root / "old-copy" / "MiMac Dev.app",
            DEV_BUNDLE_IDENTIFIER,
        )
        backend = MockLaunchServices(
            registered_by_id={
                DEV_BUNDLE_IDENTIFIER: [str(old)],
                "dev.mimac.app": [daily_path],
            },
            defaults={DEV_BUNDLE_IDENTIFIER: str(old), "dev.mimac.app": daily_path},
        )

        self._select(backend)

        self.assertEqual(backend.queried_ids, [DEV_BUNDLE_IDENTIFIER])
        self.assertEqual(backend.default_queries, [DEV_BUNDLE_IDENTIFIER])
        self.assertNotIn(Path(daily_path), backend.unregister_calls)
        self.assertEqual(backend.registered_by_id["dev.mimac.app"], [daily_path])
        self.assertEqual(backend.defaults["dev.mimac.app"], daily_path)

    def test_out_of_scope_duplicate_refuses_before_any_mutation(self):
        outside = "/Applications/MiMac Dev.app"
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: [outside, str(self.selected)]},
            defaults={DEV_BUNDLE_IDENTIFIER: outside},
        )

        with self.assertRaisesRegex(RuntimeError, "out-of-scope path") as context:
            self._select(backend)

        self.assertIn("out-of-scope path", str(context.exception))
        self.assertEqual(backend.unregister_calls, [])
        self.assertEqual(backend.register_calls, [])

    def test_existing_wrong_bundle_id_refuses_before_any_mutation(self):
        wrong = self._make_bundle(
            self.research_root / "wrong-id" / ".mygo" / "dev" / "darwin-arm64" / "MiMac Dev.app",
            "com.example.unrelated",
        )
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: [str(wrong)]},
            defaults={DEV_BUNDLE_IDENTIFIER: str(wrong)},
        )

        with self.assertRaisesRegex(RuntimeError, "expected CFBundleIdentifier"):
            self._select(backend)

        self.assertEqual(backend.unregister_calls, [])
        self.assertEqual(backend.register_calls, [])

    def test_unregister_error_code_stops_before_register(self):
        old = self._make_bundle(
            self.research_root / "old" / ".mygo" / "dev" / "darwin-arm64" / "MiMac Dev.app",
            DEV_BUNDLE_IDENTIFIER,
        )
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: [str(old)]},
            defaults={DEV_BUNDLE_IDENTIFIER: str(old)},
            unregister_code=19,
        )

        with self.assertRaisesRegex(RuntimeError, "exit code 19"):
            self._select(backend)

        self.assertEqual(backend.unregister_calls, [old])
        self.assertEqual(backend.register_calls, [])
        self.assertEqual(backend.default_queries, [])

    def test_register_error_code_is_reported_and_default_is_not_claimed(self):
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: []},
            register_code=23,
        )

        with self.assertRaisesRegex(RuntimeError, "exit code 23"):
            self._select(backend)

        self.assertEqual(backend.register_calls, [self.selected])
        self.assertEqual(backend.default_queries, [])

    def test_default_url_mismatch_is_an_error(self):
        backend = MockLaunchServices(
            registered_by_id={DEV_BUNDLE_IDENTIFIER: []},
            select_on_register=False,
        )

        with self.assertRaisesRegex(RuntimeError, "default URL does not match"):
            self._select(backend)

        self.assertEqual(backend.register_calls, [self.selected])
        self.assertEqual(backend.default_queries, [DEV_BUNDLE_IDENTIFIER])

    def test_system_adapter_queries_public_nsworkspace_apis_for_exact_dev_id(self):
        backend = SystemLaunchServices()
        with patch.object(backend, "_query", return_value=["/known/MiMac Dev.app"]) as query:
            self.assertEqual(backend.registered_urls(DEV_BUNDLE_IDENTIFIER), ["/known/MiMac Dev.app"])
            self.assertIn("URLsForApplicationsWithBundleIdentifier", query.call_args.args[0])
            self.assertIn(DEV_BUNDLE_IDENTIFIER, query.call_args.args[0])

        with patch.object(backend, "_query", return_value="/known/MiMac Dev.app") as query:
            self.assertEqual(backend.default_url(DEV_BUNDLE_IDENTIFIER), "/known/MiMac Dev.app")
            self.assertIn("URLForApplicationWithBundleIdentifier", query.call_args.args[0])
            self.assertIn(DEV_BUNDLE_IDENTIFIER, query.call_args.args[0])

        with patch.object(backend, "_query") as query:
            with self.assertRaisesRegex(RuntimeError, DEV_BUNDLE_IDENTIFIER):
                backend.registered_urls("dev.mimac.app")
            query.assert_not_called()

    def test_system_adapter_registers_or_unregisters_only_the_passed_bundle_path(self):
        backend = SystemLaunchServices()
        path = self.selected
        result = subprocess.CompletedProcess([], 0, "", "")
        with patch("dev_launch_registration.subprocess.run", return_value=result) as run:
            backend.unregister(path)
            backend.register(path)

        self.assertEqual(run.call_args_list[0].args[0], [LSREGISTER, "-u", str(path)])
        self.assertEqual(run.call_args_list[1].args[0], [LSREGISTER, "-f", str(path)])
        self.assertTrue(all(call.kwargs["check"] is False for call in run.call_args_list))


if __name__ == "__main__":
    unittest.main()
