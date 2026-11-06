#!/usr/bin/env python3
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from inspect_voice_report import inspect


def operation(**outcomes):
    counts = dict.fromkeys(("ok", "eof", "cancelled", "deadline", "backpressure", "error"), 0)
    counts.update(outcomes)
    total = sum(counts.values())
    return {**counts, "calls_started": total, "calls_completed": total, "calls_inflight": 0}


def report():
    session = {
        "session": 1, "generation": 2, "calls_started": 10, "calls_completed": 10, "calls_inflight": 0,
        "source_read_samples": 480, "sink_accepted_samples": 480,
        "source_read_samples_complete": True, "sink_accepted_samples_known": True,
        "sink_unknown_acceptance_calls": 0, "error_calls": 0, "detail_spans_dropped": 0,
        "source_read": operation(ok=2, eof=1), "sink_try_write": operation(ok=2, backpressure=1),
        **{key: operation(ok=1) for key in ("source_stop", "source_close", "sink_drain", "sink_close")},
    }
    return {"schema": 2, "io": {"sessions": [session], "session_summary_coverage": "complete",
                               "session_overflow_calls": 0, "dropped": 0}}


class VoiceReportInspectionTests(unittest.TestCase):
    def test_complete_is_local_only(self):
        result = inspect(report())
        self.assertEqual(result["local_delivery"], "complete")
        self.assertIn("recognized_text", result["not_verified"])
        self.assertEqual(result["sessions"][0]["backpressure_calls"], 1)

    def test_detail_budget_does_not_mean_pcm_loss(self):
        value = report()
        value["io"]["dropped"] = 10000
        value["io"]["sessions"][0]["detail_spans_dropped"] = 10000
        self.assertEqual(inspect(value)["local_delivery"], "complete")

    def test_lost_samples_are_not_hidden_by_successful_cleanup(self):
        value = report()
        value["io"]["sessions"][0]["sink_accepted_samples"] = 240
        result = inspect(value)
        self.assertEqual(result["local_delivery"], "needs_review")
        self.assertIn("sample_counts_differ", result["sessions"][0]["reasons"])

    def test_unknown_acceptance_never_passes_equal_counts(self):
        for field, unknown in (("sink_accepted_samples_known", False), ("source_read_samples_complete", False),
                               ("sink_unknown_acceptance_calls", 1)):
            with self.subTest(field=field):
                value = report()
                value["io"]["sessions"][0][field] = unknown
                self.assertEqual(inspect(value)["sessions"][0]["local_delivery"], "unknown")

    def test_inflight_is_pending(self):
        value = report()
        session = value["io"]["sessions"][0]
        session.update(calls_started=11, calls_inflight=1)
        session["sink_close"].update(calls_started=1, calls_completed=0, calls_inflight=1, ok=0)
        self.assertEqual(inspect(value)["sessions"][0]["local_delivery"], "pending")

    def test_error_or_missing_end_never_passes(self):
        for field in ("source_stop", "source_close", "sink_drain", "sink_close"):
            with self.subTest(field=field):
                value = report()
                value["io"]["sessions"][0][field] = operation(error=1)
                value["io"]["sessions"][0]["error_calls"] = 1
                self.assertEqual(inspect(value)["local_delivery"], "needs_review")
        value = report()
        value["io"]["sessions"][0]["source_read"] = operation(ok=3)
        self.assertIn("source_eof_not_once", inspect(value)["sessions"][0]["reasons"])

    def test_no_audio_is_not_success(self):
        value = report()
        value["io"]["sessions"][0].update(source_read_samples=0, sink_accepted_samples=0)
        self.assertEqual(inspect(value)["local_delivery"], "needs_review")
        value["io"]["sessions"] = []
        self.assertEqual(inspect(value)["local_delivery"], "needs_review")

    def test_operation_failure_cannot_be_hidden_by_aggregate(self):
        for failure in ("error", "cancelled", "deadline", "eof"):
            with self.subTest(failure=failure):
                value = report()
                session = value["io"]["sessions"][0]
                session["sink_try_write"] = operation(ok=2, backpressure=1, **{failure: 1})
                session.update(calls_started=11, calls_completed=11)
                with self.assertRaises(ValueError):
                    inspect(value)

    def test_mixed_overflow_does_not_pass_from_retained_sessions(self):
        value = report()
        value["io"].update(session_summary_coverage="overflow", session_overflow_calls=15)
        self.assertEqual(inspect(value)["local_delivery"], "needs_review")

    def test_identity_is_generation_and_session(self):
        value = report()
        second = copy.deepcopy(value["io"]["sessions"][0])
        value["io"]["sessions"].append(second)
        with self.assertRaisesRegex(ValueError, "duplicate"):
            inspect(value)
        second["generation"] = 3
        self.assertEqual(inspect(value)["local_delivery"], "complete")

    def test_missing_summary_and_inconsistent_counters_fail_closed(self):
        for change in (lambda v: v["io"].pop("sessions"),
                       lambda v: v["io"]["sessions"][0].pop("sink_close"),
                       lambda v: v["io"]["sessions"][0].update(calls_started=11),
                       lambda v: v["io"]["sessions"][0]["sink_close"].update(ok=2),
                       lambda v: v["io"].update(session_overflow_calls=1),
                       lambda v: v["io"]["sessions"][0].update(source_read_samples=True)):
            value = report()
            change(value)
            with self.assertRaises(ValueError):
                inspect(value)

    def test_cli_status_and_no_text_output(self):
        script = Path(__file__).with_name("inspect_voice_report.py")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "voice.json"
            value = report()
            value["unrelated_text"] = "must not be printed"
            path.write_text(json.dumps(value))
            good = subprocess.run([sys.executable, str(script), str(path)], capture_output=True, text=True)
            self.assertEqual(good.returncode, 0, good.stderr)
            self.assertNotIn(value["unrelated_text"], good.stdout)
            self.assertEqual(len(json.loads(good.stdout)["report_sha256"]), 64)
            value["io"]["sessions"] = []
            path.write_text(json.dumps(value))
            self.assertEqual(subprocess.run([sys.executable, str(script), str(path)], capture_output=True).returncode, 1)
            path.write_text("not JSON")
            self.assertEqual(subprocess.run([sys.executable, str(script), str(path)], capture_output=True).returncode, 2)


if __name__ == "__main__":
    unittest.main()
