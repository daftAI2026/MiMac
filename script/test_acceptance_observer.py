#!/usr/bin/env python3
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest

from acceptance_observer import ObserverShutdownError, run_observer


WORKER_NAME = "acceptance-target-observer"


def read_jsonl(path: Path) -> list[dict]:
    return [json.loads(line) for line in path.read_text().splitlines() if line]


def worker_threads() -> list[threading.Thread]:
    return [thread for thread in threading.enumerate() if thread.name == WORKER_NAME]


class AcceptanceObserverTests(unittest.TestCase):
    def run_with_callbacks(self, directory, read_fixture, read_target, stop, **options):
        return run_observer(
            directory=directory,
            state={"fixture": "fixture-state", "target": "target-state"},
            read_fixture=read_fixture,
            read_target=read_target,
            stop=stop,
            max_duration=options.pop("max_duration", 0.5),
            **options,
        )

    def test_legacy_serial_baseline_breaks_existing_half_second_gate(self):
        """The old fixture-then-target order makes an over-0.5s focus gap."""
        focus_times = []
        fixture_calls = 0

        def read_fixture():
            nonlocal fixture_calls
            fixture_calls += 1
            focus_times.append(time.monotonic())

        def read_target():
            time.sleep(0.58)

        # Reproduce the old loop's ordering: fixture snapshot, then target RPC.
        while len(focus_times) < 2:
            read_fixture()
            read_target()

        max_gap = max(b - a for a, b in zip(focus_times, focus_times[1:]))
        self.assertGreater(
            max_gap,
            0.5,
            f"legacy serial baseline unexpectedly met the existing gate: {max_gap:.6f}s",
        )
        self.assertEqual(fixture_calls, 2)

    def test_slow_target_does_not_delay_focus_sampling(self):
        with tempfile.TemporaryDirectory() as temp:
            target_started = threading.Event()
            fixture_calls = 0
            in_flight = 0
            maximum_in_flight = 0

            def read_fixture(_state):
                nonlocal fixture_calls
                fixture_calls += 1
                return {"time": time.time(), "focused": True}

            def read_target(_state):
                nonlocal in_flight, maximum_in_flight
                in_flight += 1
                maximum_in_flight = max(maximum_in_flight, in_flight)
                target_started.set()
                try:
                    time.sleep(0.62)
                    return {"running_input": 0}
                finally:
                    in_flight -= 1

            self.run_with_callbacks(
                Path(temp),
                read_fixture,
                read_target,
                stop=lambda: False,
                max_duration=0.42,
                focus_interval=0.01,
                target_interval=0.01,
                join_timeout=1.0,
            )

            timeline = read_jsonl(Path(temp) / "timeline.jsonl")
            target_timeline = read_jsonl(Path(temp) / "target-timeline.jsonl")
            focus_times = [row["monotonic_ns"] / 1_000_000_000 for row in timeline]
            max_gap = max(b - a for a, b in zip(focus_times, focus_times[1:]))
            self.assertTrue(target_started.is_set())
            self.assertGreater(fixture_calls, 10)
            self.assertLessEqual(max_gap, 0.5, f"focus gap was {max_gap:.6f}s")
            # Scheduler delay can allow a second serial query before stop.
            # The contract is one concurrent worker, not one lifetime query.
            self.assertGreaterEqual(len(target_timeline), 1)
            self.assertEqual(maximum_in_flight, 1)
            self.assertEqual(in_flight, 0)
            self.assertEqual(target_timeline[0]["status"], "ok")
            self.assertGreater(target_timeline[0]["duration_seconds"], 0.5)
            self.assertFalse(any("target" in row for row in timeline))
            self.assertEqual(worker_threads(), [])

    def test_target_error_is_logged_without_reusing_a_previous_value(self):
        with tempfile.TemporaryDirectory() as temp:
            fixture_calls = 0
            target_failed = threading.Event()

            def read_fixture(_state):
                nonlocal fixture_calls
                fixture_calls += 1
                return {"time": time.time(), "focused": True}

            def read_target(_state):
                target_failed.set()
                raise RuntimeError("native audio query failed")

            self.run_with_callbacks(
                Path(temp),
                read_fixture,
                read_target,
                stop=lambda: target_failed.is_set() and fixture_calls >= 3,
                max_duration=1.0,
                focus_interval=0.01,
                target_interval=1.0,
                join_timeout=0.5,
            )

            timeline = read_jsonl(Path(temp) / "timeline.jsonl")
            target_timeline = read_jsonl(Path(temp) / "target-timeline.jsonl")
            self.assertEqual(len(target_timeline), 1)
            row = target_timeline[0]
            self.assertEqual(row["status"], "error")
            self.assertEqual(row["error"]["type"], "RuntimeError")
            self.assertEqual(row["error"]["message"], "native audio query failed")
            self.assertLessEqual(row["query_start_monotonic_ns"], row["query_end_monotonic_ns"])
            self.assertFalse(any("target" in focus_row for focus_row in timeline))
            self.assertEqual(worker_threads(), [])

    def test_stop_joins_target_worker(self):
        with tempfile.TemporaryDirectory() as temp:
            fixture_calls = 0
            target_started = threading.Event()
            release_target = threading.Event()

            def read_fixture(_state):
                nonlocal fixture_calls
                fixture_calls += 1
                return {"time": time.time(), "focused": True}

            def read_target(_state):
                target_started.set()
                release_target.wait(1.0)
                return {"running_input": 0}

            def stop():
                if target_started.is_set() and fixture_calls >= 5:
                    release_target.set()
                    return True
                return False

            self.run_with_callbacks(
                Path(temp),
                read_fixture,
                read_target,
                stop=stop,
                max_duration=1.0,
                focus_interval=0.01,
                target_interval=1.0,
                join_timeout=0.5,
            )

            self.assertGreaterEqual(fixture_calls, 5)
            self.assertTrue(target_started.is_set())
            self.assertEqual(worker_threads(), [])
            self.assertEqual(read_jsonl(Path(temp) / "target-timeline.jsonl")[0]["status"], "ok")

    def test_blocked_target_has_bounded_join_and_explicit_error(self):
        with tempfile.TemporaryDirectory() as temp:
            target_started = threading.Event()
            release_target = threading.Event()

            def read_fixture(_state):
                return {"time": time.time(), "focused": True}

            def read_target(_state):
                target_started.set()
                release_target.wait(1.0)
                return {"running_input": 0}

            started = time.monotonic()
            with self.assertRaisesRegex(ObserverShutdownError, "did not stop within"):
                self.run_with_callbacks(
                    Path(temp),
                    read_fixture,
                    read_target,
                    stop=lambda: False,
                    max_duration=0.05,
                    focus_interval=0.01,
                    target_interval=1.0,
                    join_timeout=0.04,
                )
            elapsed = time.monotonic() - started
            self.assertTrue(target_started.is_set())
            self.assertLess(elapsed, 0.5)

            # The callback is intentionally uncooperative until released; once
            # released, the daemon worker must finish and leave no residue.
            release_target.set()
            deadline = time.monotonic() + 0.5
            while worker_threads() and time.monotonic() < deadline:
                time.sleep(0.005)
            self.assertEqual(worker_threads(), [])


if __name__ == "__main__":
    unittest.main()
