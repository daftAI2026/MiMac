#!/usr/bin/env python3
import unittest
from unittest.mock import patch
from pathlib import Path
import contextlib
import io

from live_acceptance import assess, check_fixture, route_ready, validate_audio_target, finish
from test_inspect_voice_report import report


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

    def test_finish_preserves_an_already_recorded_result(self):
        for saved_status, expected_exit in [("PASS", 0), ("NOT_PASSED", 1)]:
            with self.subTest(saved_status=saved_status), patch("live_acceptance.managed_run", return_value=(Path("/owned/run"), {"status": "FINISHED"})), patch("live_acceptance.read", return_value={"status": saved_status}), patch("live_acceptance.write") as write, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(finish("/owned/run"), expected_exit)
                write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
