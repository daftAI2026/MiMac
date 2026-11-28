#!/usr/bin/env python3
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from inspect_audio_session_trace import inspect, parse_trace_line
from test_inspect_voice_report import report as valid_voice_report


SCRIPT = Path(__file__).with_name("inspect_audio_session_trace.py")
PID = 41001


def row(at, *, pid=PID, generation=4, press=0, session=0, event="route_unavailable", stage="", result="ok"):
    return (f"time={at} pid={pid} generation={generation} press={press} session={session} "
            f"event={event} stage={stage} result={result}")


class AudioSessionTraceInspectionTests(unittest.TestCase):
    def inspect_rows(self, rows, *, start="2026-11-28T10:00:00Z", end="2026-11-28T10:01:00Z",
                     pid=PID, voice_report_path=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "audio-session.log"
            path.write_text("\n".join(rows) + "\n")
            return inspect(path, pid=pid, start=start, end=end, voice_report_path=voice_report_path)

    def test_accepted_press_owns_its_terminal_and_during_press_does_not_steal_it(self):
        result = self.inspect_rows([
            row("2026-11-28T10:00:01Z", press=1, session=8, event="press"),
            row("2026-11-28T10:00:02Z", session=8, event="prepared"),
            row("2026-11-28T10:00:03Z", press=2, session=8, event="press_during_session"),
            row("2026-11-28T10:00:04Z", press=2, session=8, event="release"),
            row("2026-11-28T10:00:05Z", press=1, session=8, event="release"),
            row("2026-11-28T10:00:06Z", session=8, event="drained"),
        ])
        first, second = result["presses"]
        self.assertEqual((first["press_id"], first["accepted_session_id"], first["terminal"]), (1, 8, "drained"))
        self.assertEqual(second["press_id"], 2)
        self.assertIsNone(second["accepted_session_id"])
        self.assertEqual(second["session_field_reference_id"], 8)
        self.assertEqual(second["terminal"], "not_accepted")
        self.assertEqual(result["failure_count"], 0)

    def test_during_press_failure_does_not_receive_previous_session_terminal(self):
        result = self.inspect_rows([
            row("2026-11-28T10:00:01Z", press=1, session=8, event="press"),
            row("2026-11-28T10:00:02Z", session=8, event="prepared"),
            row("2026-11-28T10:00:03Z", press=2, event="audio_binding_unready", result="error"),
            row("2026-11-28T10:00:04Z", press=2, session=8, event="press_during_session"),
            row("2026-11-28T10:00:05Z", session=8, event="drained"),
        ])
        first, second = result["presses"]
        self.assertEqual(first["terminal"], "drained")
        self.assertEqual(second["press_id"], 2)
        self.assertIsNone(second["accepted_session_id"])
        self.assertEqual(second["session_field_reference_id"], 8)
        self.assertEqual(second["terminal"], "failed_before_session")
        self.assertIsNone(second["terminal_event"])
        self.assertEqual(second["failure_codes"][0]["event"], "audio_binding_unready")
        self.assertEqual(result["failure_count"], 1)

    def test_missing_terminal_is_unknown_even_after_prepared(self):
        result = self.inspect_rows([
            row("2026-11-28T10:00:01Z", press=3, session=9, event="press"),
            row("2026-11-28T10:00:02Z", session=9, event="prepared"),
        ])
        self.assertEqual(result["presses"][0]["prepared_count"], 1)
        self.assertEqual(result["presses"][0]["terminal"], "unknown")
        self.assertEqual(result["unknown_count"], 1)
        self.assertEqual(result["diagnostic_state"], "unknown")

    def test_session_failure_and_pre_session_failure_count_once_per_press(self):
        result = self.inspect_rows([
            row("2026-11-28T10:00:01Z", press=13, session=11, event="press"),
            row("2026-11-28T10:00:02Z", session=11, event="prepared"),
            row("2026-11-28T10:00:03Z", session=11, event="failed", stage="sink_write", result="target_stopped"),
            row("2026-11-28T10:00:04Z", press=13, event="release"),
            row("2026-11-28T10:00:05Z", press=14, event="audio_binding_unready", result="error"),
            row("2026-11-28T10:00:06Z", press=14, event="press_without_session"),
            row("2026-11-28T10:00:07Z", press=14, event="release"),
        ])
        self.assertEqual([item["terminal"] for item in result["presses"]], ["failed", "failed_before_session"])
        self.assertEqual(result["failure_count"], 2)
        self.assertEqual(result["diagnostic_state"], "failure")
        self.assertEqual(result["presses"][0]["failure_codes"][0]["stage"], "sink_write")
        self.assertEqual(result["presses"][1]["failure_codes"][0]["event"], "audio_binding_unready")

    def test_pid_and_time_filter_keep_reopened_generation_separate(self):
        result = self.inspect_rows([
            row("2026-11-28T10:00:01Z", generation=1, press=1, session=1, event="press"),
            row("2026-11-28T10:00:02Z", generation=1, session=1, event="drained"),
            row("2026-11-28T10:00:11Z", generation=2, press=1, session=1, event="press"),
            row("2026-11-28T10:00:12Z", generation=2, session=1, event="failed", stage="sink_drain", result="error"),
            row("2026-11-28T10:00:11Z", pid=PID + 1, generation=2, press=2, session=2, event="press"),
        ], start="2026-11-28T10:00:10Z", end="2026-11-28T10:00:20Z")
        self.assertEqual(len(result["presses"]), 1)
        self.assertEqual((result["presses"][0]["generation"], result["presses"][0]["terminal"]), (2, "failed"))

    def test_existing_voice_report_parser_stays_unlinked_without_process_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            report_path = Path(directory) / "voice.json"
            report_path.write_text(json.dumps(valid_voice_report()))
            result = self.inspect_rows([
                row("2026-11-28T10:00:01Z", generation=2, press=5, session=1, event="press"),
                row("2026-11-28T10:00:02Z", generation=2, session=1, event="drained"),
            ], voice_report_path=report_path)
            self.assertNotIn("voice_io", result["presses"][0])
            context = result["voice_report_context"]
            self.assertEqual(context["local_delivery"], "complete")
            self.assertFalse(context["linked_to_per_press"])
            self.assertIn("no PID", context["reason"])
            self.assertEqual(len(context["sha256"]), 64)

    def test_unknown_trace_fields_are_rejected_without_echoing_values(self):
        line = row("2026-11-28T10:00:01Z") + " transcript=private_words"
        with self.assertRaisesRegex(ValueError, "unexpected fixed-field shape") as caught:
            parse_trace_line(line, 1)
        self.assertNotIn("private", str(caught.exception))

    def test_cli_requires_explicit_process_and_time_scope_and_emits_no_payload(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "audio-session.log"
            path.write_text("\n".join([
                row("2026-11-28T10:00:01Z", press=1, session=8, event="press"),
                row("2026-11-28T10:00:02Z", session=8, event="drained"),
            ]) + "\n")
            value = valid_voice_report()
            value["unrelated_text"] = "private dictated words"
            report_path = Path(directory) / "voice.json"
            report_path.write_text(json.dumps(value))
            command = [sys.executable, str(SCRIPT), "--trace", str(path), "--pid", str(PID),
                       "--start", "2026-11-28T10:00:00Z", "--end", "2026-11-28T10:01:00Z",
                       "--voice-report", str(report_path)]
            good = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(good.returncode, 0, good.stderr)
            self.assertNotIn(value["unrelated_text"], good.stdout)
            self.assertIn('"terminal": "drained"', good.stdout)
            missing_scope = subprocess.run([sys.executable, str(SCRIPT), "--trace", str(path), "--pid", str(PID)],
                                           capture_output=True, text=True)
            self.assertEqual(missing_scope.returncode, 2)


if __name__ == "__main__":
    unittest.main()
