#!/usr/bin/env python3
import unittest
from unittest.mock import patch
from pathlib import Path
import contextlib
import copy
from datetime import datetime, timedelta, timezone
import io
import json
import plistlib
import signal
import tempfile
import live_acceptance
from dev_bundle_path import legacy_development_bundle_path

from live_acceptance import assess, check_fixture, route_ready, validate_audio_target, finish, assess_source_diagnostic
from test_inspect_voice_report import report


class InstalledBetaTests(unittest.TestCase):
    def test_release_manifest_binds_installed_files_and_native_libraries(self):
        with tempfile.TemporaryDirectory() as root:
            bundle = Path(root) / "MiMac.app"
            contents = bundle / "Contents"
            for directory in ["MacOS", "Resources"]:
                (contents / directory).mkdir(parents=True)
            (contents / "MacOS/MiMac").write_bytes(b"frozen executable")
            (contents / "Resources/libMiMacSettings.dylib").write_bytes(b"frozen library")
            (contents / "Info.plist").write_bytes(plistlib.dumps({
                "CFBundleIdentifier": "dev.mimac.app", "CFBundleExecutable": "MiMac",
                "CFBundleShortVersionString": "2026.11.18", "CFBundleVersion": "47",
            }))
            path = Path(root) / "release.json"
            manifest = {"source": {"revision": "test"}, "binary": {"revision": "test"},
                        "version": "2026.11.18", "build_number": "47", "native_libraries": 1,
                        "sha256": {str(p.relative_to(contents)): live_acceptance.digest(p)
                                   for p in contents.rglob("*") if p.is_file()}}
            path.write_text(json.dumps(manifest))
            with patch.object(live_acceptance, "run") as run:
                self.assertEqual(live_acceptance.verify_installed_beta(path, bundle), manifest)
                run.assert_called_once_with("/usr/bin/codesign", "--verify", "--deep", "--strict", bundle, stderr=live_acceptance.subprocess.STDOUT)
                (contents / "MacOS/MiMac").write_bytes(b"another build")
                with self.assertRaisesRegex(RuntimeError, "MacOS/MiMac"):
                    live_acceptance.verify_installed_beta(path, bundle)
                (contents / "Resources/extra.dylib").touch()
                with self.assertRaisesRegex(RuntimeError, "native libraries differ"):
                    live_acceptance.verify_installed_beta(path, bundle)


class FixedDevTests(unittest.TestCase):
    def test_receipt_pins_checkout_and_payloads(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            with patch.object(live_acceptance, "ROOT", root):
                bundle = live_acceptance.fixed_dev_bundle("arm64")
                bundle.mkdir(parents=True)
                path = root / "receipt.json"
                receipt = {"arch": "arm64", "bundle": str(bundle), "sha256": {"MacOS/MiMac Dev": "known"}}
                path.write_text(json.dumps(receipt))
                with patch.object(live_acceptance, "verify", return_value=receipt) as verify:
                    self.assertEqual(live_acceptance.verify_fixed_dev(path), (bundle, "arm64"))
                    verify.assert_called_once_with(bundle, "arm64", development=True, require_audio_installer=True)
                with patch.object(live_acceptance, "verify", return_value={"sha256": {}}):
                    with self.assertRaisesRegex(RuntimeError, "differs from the verified receipt"):
                        live_acceptance.verify_fixed_dev(path)
                receipt["bundle"] = str(root / "other/MiMac Dev.app")
                path.write_text(json.dumps(receipt))
                with self.assertRaisesRegex(RuntimeError, "checkout's canonical bundle"):
                    live_acceptance.verify_fixed_dev(path)

    def test_receipt_pins_stable_cache_bundle_for_protected_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary).resolve()
            root = home / "Desktop" / "source"
            with patch.object(live_acceptance.Path, "home", return_value=home), \
                    patch.object(live_acceptance, "ROOT", root):
                bundle = live_acceptance.fixed_dev_bundle("arm64")
                bundle.mkdir(parents=True)
                receipt = {"arch": "arm64", "bundle": str(bundle), "sha256": {"MacOS/MiMac Dev": "known"}}
                receipt_path = home / "receipt.json"
                receipt_path.write_text(json.dumps(receipt))
                with patch.object(live_acceptance, "verify", return_value=receipt) as verify:
                    self.assertEqual(live_acceptance.verify_fixed_dev(receipt_path), (bundle, "arm64"))
                    verify.assert_called_once_with(bundle, "arm64", development=True, require_audio_installer=True)
                with patch.object(live_acceptance, "verify", return_value={"sha256": {"MacOS/MiMac Dev": "different-bytes"}}):
                    with self.assertRaisesRegex(RuntimeError, "differs from the verified receipt"):
                        live_acceptance.verify_fixed_dev(receipt_path)

    def test_pre_migration_receipt_can_verify_byte_identical_cache_bundle(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary).resolve()
            root = home / "Desktop" / "source"
            with patch.object(live_acceptance.Path, "home", return_value=home), \
                    patch.object(live_acceptance, "ROOT", root):
                bundle = live_acceptance.fixed_dev_bundle("arm64")
                bundle.mkdir(parents=True)
                old_path = legacy_development_bundle_path(root, "arm64")
                receipt = {"arch": "arm64", "bundle": str(old_path), "sha256": {"MacOS/MiMac Dev": "same-bytes"}}
                receipt_path = home / "receipt.json"
                receipt_path.write_text(json.dumps(receipt))
                with patch.object(live_acceptance, "verify", return_value=receipt) as verify:
                    self.assertEqual(live_acceptance.verify_fixed_dev(receipt_path), (bundle, "arm64"))
                    verify.assert_called_once_with(bundle, "arm64", development=True, require_audio_installer=True)


class AcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.voice = report()
        self.voice["io"]["sessions"][0].update(first_started_at="2026-11-06T10:00:00+00:00", last_finished_at="2026-11-06T10:00:02+00:00")
        self.host = {"normal_exit": True, "core": {"pressed": 0}}
        self.source = "com.bytedance.inputmethod.doubaoime.pinyin"
        self.field = {"pid": 42, "window_title": "MiMac 语音验收", "focused": True, "app_active": True, "frontmost_pid": 42, "has_marked_text": False, "action_ready": True, "input_source_id": self.source}
        self.target = {"reported_pid": 99, "bundle_id": self.source.removesuffix(".pinyin"), "running_input_status": 0, "running_input": 1, "input_devices": [{"uid": "dev.mimac.audio.compat.v1"}]}
        self.timeline = [{"time": 1793959200 + i / 5, "field": self.field, "target": self.target} for i in range(11)]

    def result(self, final="前文新增"):
        return assess(self.host, self.voice, "前文", final, self.timeline, self.source)

    def test_complete_evidence_passes(self):
        self.assertEqual(self.result()["status"], "PASS")

    def test_planned_utterance_checks_the_whole_append_not_only_new_text(self):
        expected = "声音来自遥控器，最后是测试完成。"
        for text, passed in [
            ("声音来自遥控器 最后是测试完成", True),
            ("声音来自遥控器", False),
            ("最后是测试完成", False),
            ("声音来自最后是测试完成", False),
            ("声音来自遥控器最后是测试完成旧结果", False),
        ]:
            with self.subTest(text=text):
                result = assess(self.host, self.voice, "前文", "前文" + text,
                                self.timeline, self.source, expected_text=expected)
                self.assertEqual(result["checks"]["planned_text_complete"], passed)
                self.assertEqual(result["status"], "PASS" if passed else "NOT_PASSED")

    def test_expected_text_never_matches_the_existing_prefix(self):
        result = assess(self.host, self.voice, "原有内容", "原有内容新增",
                        self.timeline, self.source, expected_text="原有内容新增")
        self.assertFalse(result["checks"]["planned_text_complete"])

    def test_no_scripted_utterance_is_not_an_accuracy_pass(self):
        result = self.result()
        self.assertNotIn("planned_text_complete", result["checks"])
        self.assertEqual(result["utterance"]["status"], "NOT_REQUESTED")

    def test_no_text_does_not_pass_audio_delivery(self):
        self.assertTrue(self.result("前文")["checks"]["audio_delivery"])
        self.assertEqual(self.result("前文")["status"], "NOT_PASSED")

    def test_changed_prefix_does_not_count_as_append(self):
        self.assertFalse(self.result("其他内容")["checks"]["new_text"])

    def test_equal_counts_do_not_hide_unknown_acceptance(self):
        self.voice["io"]["sessions"][0]["sink_accepted_samples_known"] = False
        self.assertEqual(self.result()["status"], "NOT_PASSED")

    def test_focused_preflight_cannot_replace_session_coverage(self):
        self.timeline = [{**row, "time": row["time"] - 100} for row in self.timeline]
        self.assertFalse(self.result()["checks"]["session_focus_evidence"])

    def test_focus_change_and_input_source_change_fail(self):
        for key, value in [("focused", False), ("input_source_id", "other")]:
            with self.subTest(key=key):
                previous = self.field[key]
                self.field[key] = value
                self.assertEqual(self.result()["status"], "NOT_PASSED")
                self.field[key] = previous

    def test_wrong_audio_receiver_does_not_pass(self):
        self.target["input_devices"] = [{"uid": "other-input"}]
        self.assertFalse(self.result()["checks"]["target_route_evidence"])

    def test_one_text_result_cannot_pass_multiple_sessions(self):
        self.voice["io"]["sessions"].append({**self.voice["io"]["sessions"][0], "session": 2})
        self.assertFalse(self.result()["checks"]["single_planned_session"])
        self.assertEqual(self.result()["status"], "NOT_PASSED")

    def test_sparse_focus_points_do_not_prove_interval_coverage(self):
        self.timeline = self.timeline[5:6]
        self.assertFalse(self.result()["checks"]["session_focus_evidence"])

    def test_foreground_identity_must_match_fixture(self):
        self.field["frontmost_pid"] = 123
        self.assertFalse(self.result()["checks"]["session_focus_evidence"])

    def test_target_stream_is_independent_and_query_must_fit_session(self):
        focus = [{"time": row["time"], "field": row["field"]} for row in self.timeline]
        target = {"status": "ok", "query_start_time": 1793959200.5, "query_end_time": 1793959201.1, "target": self.target}
        result = assess(self.host, self.voice, "前文", "前文新增", focus, self.source, 42, [target])
        self.assertEqual(result["status"], "PASS")
        for changes in [{"query_start_time": 1793959199.9}, {"query_end_time": 1793959202.1}, {"error": "query failed"}]:
            with self.subTest(changes=changes):
                result = assess(self.host, self.voice, "前文", "前文新增", focus, self.source, 42, [{**target, **changes}])
                self.assertFalse(result["checks"]["target_route_evidence"])
                self.assertTrue(result["checks"]["session_focus_evidence"])

    def test_latest_target_error_or_stale_query_cannot_reuse_success(self):
        good = {"status": "ok", "query_start_time": 100, "query_end_time": 100.1, "target": self.target}
        self.assertEqual(live_acceptance.fresh_target([good], 100.2), self.target)
        slow = {**good, "query_end_time": 100.7}
        self.assertEqual(live_acceptance.fresh_target([slow], 100.8), self.target)
        for rows, now in [([], 100.2), ([good], 100.7), ([good, {"status": "error", "error": "failed", "query_start_time": 100.1, "query_end_time": 100.2}], 100.3)]:
            with self.subTest(rows=rows), self.assertRaises(RuntimeError):
                live_acceptance.fresh_target(rows, now)

    def test_status_rejects_a_stopped_observer_before_reading_fresh_samples(self):
        state = {"status": "READY", "app_pid": 11, "app_executable": "/app/MiMac", "fixture_pid": 42, "fixture_executable": "/fixture", "observer_pid": 15, "observer_schema": 2}
        with patch("live_acceptance.managed_run", return_value=(Path("/owned/run"), state)), patch("live_acceptance.exact_pids", side_effect=lambda path: [11] if path == "/app/MiMac" else [42]), patch("live_acceptance.observer_alive", return_value=False), self.assertRaisesRegex(RuntimeError, "observer is not running"):
            live_acceptance.status("/owned/run")

    def test_source_diagnostic_is_independent_of_primary_text(self):
        value = {"session_count": 1, "session_token_id": 1, "device_generation": 2, "source_eof": True, "source_close_succeeded": True, "complete_capture": True, "replay_started": True, "replay_completed": True, "replay_sample_count": 480, "secondary_text": "", "stats": {"ObservedSamples": 480, "RetainedSamples": 480, "OverflowSamples": 0}}
        self.assertTrue(assess_source_diagnostic(value, self.voice))
        self.assertFalse(self.result("前文")["checks"]["new_text"])
        for field, wrong in [("session_token_id", 2), ("replay_sample_count", 479), ("replay_completed", False), ("apple_sink_retained", True), ("errors", ["native failure"])]:
            with self.subTest(field=field):
                self.assertFalse(assess_source_diagnostic({**value, field: wrong}, self.voice))

    def test_current_connection_readiness_is_required(self):
        self.assertTrue(route_ready("audio route: ready=false error=<nil>\naudio route: ready=true error=<nil>"))
        self.assertFalse(route_ready("audio route: ready=true error=<nil>\naudio route: ready=false error=failed"))

    def test_prepare_handshake_is_required(self):
        check_fixture(self.field, 42, self.source, prepared=True)
        for key, value in [("action_ready", False), ("pid", 43), ("focused", False), ("has_marked_text", True)]:
            candidate = {**self.field, key: value}
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                check_fixture(candidate, 42, self.source, prepared=True)

    def test_target_identity_is_required_even_when_idle(self):
        idle = {**self.target, "running_input": 0, "input_devices": []}
        validate_audio_target(idle, 99, self.target["bundle_id"])
        with self.assertRaises(RuntimeError):
            validate_audio_target({**idle, "bundle_id": "another.app"}, 99, self.target["bundle_id"])

    def test_permission_snapshot_explains_exact_refusal(self):
        for text in ["需要输入监控权限", "需要辅助功能权限"]:
            with self.subTest(text=text), self.assertRaisesRegex(RuntimeError, text):
                live_acceptance.check_permission_snapshot({"status": "ok", "nodes": [{"role": "AXStaticText", "value": text}]})
        live_acceptance.check_permission_snapshot({"status": "ok", "nodes": [{"role": "AXStaticText", "value": "已启用"}]})
        with self.assertRaisesRegex(RuntimeError, "unverified"):
            live_acceptance.check_permission_snapshot({"status": "partial"})

    def test_native_permission_status_group_blocks_voice_preparation(self):
        snapshot = {"status": "ok", "nodes": [
            {"role": "AXGroup", "description": "需要输入监控权限"},
            {"role": "AXButton", "title": "启用遥控器"},
        ]}
        with self.assertRaisesRegex(RuntimeError, "需要输入监控权限"):
            live_acceptance.check_permission_snapshot(snapshot)

    def test_quit_marker_prevents_a_second_signal_after_interruption(self):
        with tempfile.TemporaryDirectory() as directory, patch("live_acceptance.os.kill") as kill:
            live_acceptance.request_quit_once(directory, 11)
            self.assertEqual(live_acceptance.read(Path(directory) / "quit-request.json")["pid"], 11)
            live_acceptance.request_quit_once(directory, 11)
            kill.assert_called_once_with(11, signal.SIGTERM)

    def test_finish_uses_one_cleanup_owner(self):
        state = {"run": "/owned/run", "status": "READY", "app_pid": 11, "app_executable": "/app/MiMac", "probe": "/probe"}
        alive = True
        def pids(path):
            return [11] if alive else []
        def quit_once(pid, sig):
            nonlocal alive
            self.assertEqual((pid, sig), (11, signal.SIGTERM))
            alive = False
        with patch("live_acceptance.managed_run", return_value=(Path("/owned/run"), state)), patch("live_acceptance.exact_pids", side_effect=pids), patch("live_acceptance.os.kill", side_effect=quit_once) as kill, patch("live_acceptance.run", return_value='{"nodes":[{"title":"停用遥控器"}]}') as action, patch("live_acceptance.read", side_effect=lambda path: self.host if Path(path).name == "host.json" else self.voice), patch("live_acceptance.write"), patch("live_acceptance.assess", return_value={"status": "PASS", "checks": {}}), contextlib.redirect_stdout(io.StringIO()):
            finish("/owned/run")
            kill.assert_called_once()
            action.assert_not_called()  # App quit owns cleanup; no overlapping AX stop.

    def test_finish_keeps_a_post_cleanup_snapshot_and_late_events(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder)
            field = directory / "field"
            field.mkdir()
            state = {"run": folder, "status": "READY", "app_pid": 11, "app_executable": "/app/MiMac", "fixture_pid": 42, "fixture_executable": "/fixture", "field": str(field), "baseline_text": "前文", "expected_text": "迟到"}
            alive = {"/app/MiMac": True, "/fixture": True}
            before = {"pid": 42, "text": "前文", "has_marked_text": False, "monotonic_ns": 100}
            late = {**before, "text": "前文迟到", "monotonic_ns": 200}
            def request(directory, pid):
                alive["/app/MiMac"] = False
                (field / "events.jsonl").write_text(live_acceptance.json.dumps({**late, "event": "text_did_change"}) + "\n")
            def command(folder, action):
                if action == "quit":
                    alive["/fixture"] = False
                    return None
                return before if alive["/app/MiMac"] else late
            def read(path):
                name = Path(path).name
                if name == "host.json": return self.host
                if name == "voice.json": return self.voice
                return live_acceptance.json.loads(Path(path).read_text())
            with patch("live_acceptance.managed_run", return_value=(directory, state)), patch("live_acceptance.exact_pids", side_effect=lambda path: ([11] if str(path) == "/app/MiMac" else [42]) if alive[str(path)] else []), patch("live_acceptance.request_quit_once", side_effect=request), patch("live_acceptance.command", side_effect=command) as rpc, patch("live_acceptance.read", side_effect=read), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(finish(directory), 1)
            result = live_acceptance.json.loads((directory / "acceptance.json").read_text())
            self.assertFalse(result["checks"]["new_text"])
            self.assertFalse(result["checks"]["planned_text_complete"])
            self.assertEqual(result["utterance"]["expected"], "迟到")
            self.assertTrue(result["text_evidence"]["new_text_after_cleanup"])
            self.assertEqual(result["text_evidence"]["commits"]["after_boundary"][0]["text"], "前文迟到")
            self.assertEqual([call.args[1] for call in rpc.call_args_list], ["snapshot", "snapshot", "quit"])

    def test_pending_cleanup_retains_fixture_and_original_boundary_on_retry(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder)
            field = directory / "field"
            field.mkdir()
            state = {"run": folder, "status": "READY", "app_pid": 11, "app_executable": "/app/MiMac", "fixture_pid": 42, "fixture_executable": "/fixture", "field": str(field), "baseline_text": "前文"}
            alive = {"/app/MiMac": True, "/fixture": True}
            before = {"pid": 42, "text": "前文", "has_marked_text": False, "monotonic_ns": 100}
            late = {**before, "text": "前文迟到", "monotonic_ns": 200}
            snapshots = 0
            def command(folder, action):
                nonlocal snapshots
                if action == "quit":
                    alive["/fixture"] = False
                    return None
                snapshots += 1
                if snapshots == 1: return before
                (field / "events.jsonl").write_text(live_acceptance.json.dumps({**late, "event": "text_did_change"}) + "\n")
                return late
            def read(path):
                if Path(path).name == "host.json": return self.host
                if Path(path).name == "voice.json": return self.voice
                return live_acceptance.json.loads(Path(path).read_text())
            def bounded(check, seconds=12):
                value = check()
                if not value: raise RuntimeError("test cleanup still pending")
                return value
            with patch("live_acceptance.managed_run", return_value=(directory, state)), patch("live_acceptance.exact_pids", side_effect=lambda path: ([11] if str(path) == "/app/MiMac" else [42]) if alive[str(path)] else []), patch("live_acceptance.os.kill") as kill, patch("live_acceptance.command", side_effect=command) as rpc, patch("live_acceptance.read", side_effect=read), patch("live_acceptance.wait_for", side_effect=bounded), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(finish(directory), 1)
                self.assertEqual(state["status"], "BLOCKED")
                self.assertTrue(alive["/fixture"])
                self.assertNotIn("quit", [call.args[1] for call in rpc.call_args_list])
                alive["/app/MiMac"] = False  # A native Quit completes outside the signal path.
                self.assertEqual(finish(directory), 1)
                kill.assert_called_once_with(11, signal.SIGTERM)
            result = live_acceptance.json.loads((directory / "acceptance.json").read_text())
            self.assertEqual(state["status"], "FINISHED")
            self.assertEqual(result["text_evidence"]["before_cleanup"]["text"], "前文")
            self.assertFalse(result["checks"]["new_text"])
            self.assertTrue(result["text_evidence"]["new_text_after_cleanup"])

    def test_stuck_observer_blocks_before_fixture_rpc_and_saves_result(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder)
            state = {"run": folder, "status": "READY", "observer_pid": 15, "app_pid": 11, "app_executable": "/app/MiMac", "fixture_pid": 42, "fixture_executable": "/fixture"}
            process = live_acceptance.subprocess.CompletedProcess([], 0, stdout="python live_acceptance.py observe " + folder)
            with patch("live_acceptance.managed_run", return_value=(directory, state)), patch("live_acceptance.subprocess.run", return_value=process), patch("live_acceptance.wait_for", side_effect=RuntimeError("still active")), patch("live_acceptance.os.getpgid", return_value=15), patch("live_acceptance.os.killpg", side_effect=PermissionError("group kill refused")), patch("live_acceptance.exact_pids", side_effect=lambda path: [11] if path == "/app/MiMac" else [42]), patch("live_acceptance.command") as rpc, patch("live_acceptance.request_quit_once") as quit_app, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(finish(directory), 1)
                rpc.assert_not_called()
                quit_app.assert_not_called()
            result = live_acceptance.read(directory / "acceptance.json")
            self.assertEqual(result["stage"], "observer_cleanup")
            self.assertTrue(any("group kill refused" in message for message in result["cleanup_errors"]))
            self.assertEqual(state["status"], "BLOCKED")

    def test_finish_preserves_an_already_recorded_result(self):
        for saved_status, expected_exit in [("PASS", 0), ("NOT_PASSED", 1)]:
            with self.subTest(saved_status=saved_status), patch("live_acceptance.managed_run", return_value=(Path("/owned/run"), {"status": "FINISHED"})), patch("live_acceptance.read", return_value={"status": saved_status}), patch("live_acceptance.write") as write, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(finish("/owned/run"), expected_exit)
                write.assert_not_called()


class MultiplePlannedUtteranceTests(unittest.TestCase):
    def evidence(self, merge_both_before_second=False, wrong_target_pid=False, lose_focus=False):
        base_time = datetime.now(timezone.utc)
        stamp = lambda offset: (base_time + timedelta(seconds=offset)).timestamp()
        iso = lambda offset: (base_time + timedelta(seconds=offset)).isoformat()
        voice = report()
        first = copy.deepcopy(voice["io"]["sessions"][0])
        first.update(session=21, generation=8, first_started_at=iso(.5), last_finished_at=iso(1.3))
        second = copy.deepcopy(first)
        second.update(session=22, first_started_at=iso(2.0), last_finished_at=iso(2.8))
        voice["io"]["sessions"] = [first, second]
        source = "com.bytedance.inputmethod.doubaoime.pinyin"
        fixture_pid = live_acceptance.os.getpid()
        target_pid = fixture_pid + 1
        base_field = {"pid": fixture_pid, "window_title": "MiMac 语音验收", "focused": True,
                      "app_active": True, "frontmost_pid": fixture_pid, "has_marked_text": False,
                      "input_source_id": source, "status": "ok"}

        def text_at(offset):
            if merge_both_before_second and offset >= 1.6:
                return "prefixalphabeta"
            if offset >= 3.0:
                return "prefixalphabeta"
            if offset >= 1.6:
                return "prefixalpha"
            return "prefix"

        timeline = []
        for tick in range(1, 17):
            offset = tick * .2
            field = {**base_field, "time": stamp(offset), "text": text_at(offset)}
            if lose_focus and offset == .8:
                field["focused"] = False
            timeline.append({"time": stamp(offset), "field": field})
        target = {"reported_pid": target_pid + 1 if wrong_target_pid else target_pid,
                  "bundle_id": source.removesuffix(".pinyin"),
                  "running_input_status": 0, "running_input": 1,
                  "input_devices": [{"uid": "dev.mimac.audio.compat.v1"}]}
        target_timeline = [
            {"query_start_time": stamp(offset - .01), "query_end_time": stamp(offset + .01),
             "target": target}
            for offset in (.7, 1.1, 2.2, 2.6)
        ]
        final_field = {**base_field, "time": stamp(3.2), "text": "prefixalphabeta"}
        result = live_acceptance.assess(
            {"normal_exit": True, "core": {"pressed": 0}}, voice, "prefix", final_field["text"],
            timeline, source, fixture_pid, target_timeline,
            expected_texts=["alpha", "beta"], final_field=final_field,
            expected_target_pid=target_pid,
        )
        return result

    def test_each_press_has_its_own_audio_route_focus_and_exact_text(self):
        result = self.evidence()
        self.assertEqual(result["status"], "PASS", result)
        self.assertEqual([item["text"]["status"] for item in result["planned_utterances"]], ["PASS", "PASS"])
        self.assertTrue(result["checks"]["session_audio_evidence"])
        self.assertTrue(result["checks"]["target_route_evidence"])
        self.assertTrue(result["checks"]["session_focus_evidence"])
        self.assertTrue(result["checks"]["committed_before_next_session"])

    def test_correct_total_text_cannot_hide_a_missing_per_session_boundary(self):
        result = self.evidence(merge_both_before_second=True)
        self.assertEqual(result["planned_utterances"][-1]["text"]["expected"], "beta")
        self.assertFalse(result["checks"]["planned_text_complete"])
        self.assertFalse(result["checks"]["committed_before_next_session"])
        self.assertEqual(result["status"], "NOT_PASSED")

    def test_legacy_state_and_single_cli_value_remain_one_sentence(self):
        self.assertEqual(live_acceptance.expected_texts_from_state({"schema": 1, "expected_text": "alpha"}), ["alpha"])
        self.assertEqual(live_acceptance.expected_texts_from_state({"schema": 1, "expected_text": None}), [])
        self.assertEqual(live_acceptance.planned_text_values("alpha"), ["alpha"])
        self.assertEqual(live_acceptance.expected_texts_from_state({
            "schema": 1, "expected_text": "alphabeta", "expected_texts": ["alpha", "beta"]
        }), ["alpha", "beta"])

    def test_cli_collects_each_predeclared_sentence_in_order(self):
        argv = ["live_acceptance.py", "prepare", "--expected-text", "alpha", "--expected-text", "beta"]
        with patch.object(live_acceptance.sys, "argv", argv), patch.object(live_acceptance, "prepare", return_value=0) as prepare:
            self.assertEqual(live_acceptance.main(), 0)
            self.assertEqual(prepare.call_args.args[0].expected_text, ["alpha", "beta"])

    def test_multi_session_target_pid_mismatch_fails_route_evidence(self):
        result = self.evidence(wrong_target_pid=True)
        self.assertFalse(result["checks"]["target_route_evidence"])
        self.assertEqual(result["status"], "NOT_PASSED")

    def test_focus_loss_in_a_planned_session_fails_the_pair(self):
        result = self.evidence(lose_focus=True)
        self.assertFalse(result["checks"]["session_focus_evidence"])
        self.assertEqual(result["status"], "NOT_PASSED")


if __name__ == "__main__":
    unittest.main()
