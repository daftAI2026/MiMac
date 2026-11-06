"""Run fixture focus sampling independently from target audio observation.

The caller owns stop detection and adapts its fixture and target RPCs to the
callbacks below. Focus samples are written to ``timeline.jsonl`` by this
function's thread. A single target worker writes only
``target-timeline.jsonl``; target values are never copied into focus samples.
"""
from __future__ import annotations

import json
from pathlib import Path
import threading
import time
from typing import Any, Callable

FOCUS_INTERVAL_SECONDS = 0.1
TARGET_INTERVAL_SECONDS = 0.1
TARGET_JOIN_TIMEOUT_SECONDS = 4.0


class ObserverShutdownError(RuntimeError):
    """Raised when the target worker cannot be joined within its bound."""


def _error_record(error: BaseException) -> dict[str, str]:
    return {"type": type(error).__name__, "message": str(error)}


def _write_row(log: Any, row: dict[str, Any]) -> None:
    log.write(json.dumps(row, ensure_ascii=False) + "\n")
    log.flush()


def run_observer(
    directory: str | Path,
    state: dict[str, Any],
    read_fixture: Callable[[dict[str, Any]], dict[str, Any]],
    read_target: Callable[[dict[str, Any]], dict[str, Any]] | None,
    stop: Callable[[], bool],
    max_duration: float,
    *,
    focus_interval: float = FOCUS_INTERVAL_SECONDS,
    target_interval: float = TARGET_INTERVAL_SECONDS,
    join_timeout: float = TARGET_JOIN_TIMEOUT_SECONDS,
) -> None:
    """Sample fixture focus while a sole worker polls target audio state.

    ``read_fixture(state)`` runs serially on the caller thread, which remains
    the only fixture RPC writer. ``read_target(state)`` runs on one dedicated
    worker and may block without delaying focus sampling. Both target successes
    and failures record query start/end wall and monotonic times. A failed
    query has no ``target`` value, so older data cannot be mistaken for fresh.

    The target callback must itself bound external RPCs where possible. Stop
    is requested once the callback returns true or ``max_duration`` elapses;
    the worker then gets at most ``join_timeout`` seconds to exit. A worker that
    remains blocked produces an explicit ``ObserverShutdownError``.
    """
    if max_duration < 0:
        raise ValueError("max_duration must be non-negative")
    if focus_interval <= 0 or target_interval <= 0:
        raise ValueError("observer intervals must be positive")
    if join_timeout < 0:
        raise ValueError("join_timeout must be non-negative")

    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    focus_path = directory / "timeline.jsonl"
    target_path = directory / "target-timeline.jsonl"
    target_path.touch(exist_ok=False)

    target_stop = threading.Event()
    worker_failures: list[BaseException] = []
    target_thread: threading.Thread | None = None

    def target_worker() -> None:
        try:
            with target_path.open("a", encoding="utf-8") as log:
                query_index = 0
                while not target_stop.is_set():
                    query_index += 1
                    query_start_time = time.time()
                    query_start_ns = time.monotonic_ns()
                    query_started = time.monotonic()
                    try:
                        target = read_target(state)  # type: ignore[misc]
                    except Exception as error:
                        query_end_time = time.time()
                        query_end_ns = time.monotonic_ns()
                        row = {
                            "query_index": query_index,
                            "status": "error",
                            "query_start_time": query_start_time,
                            "query_end_time": query_end_time,
                            "query_start_monotonic_ns": query_start_ns,
                            "query_end_monotonic_ns": query_end_ns,
                            "duration_seconds": (query_end_ns - query_start_ns) / 1_000_000_000,
                            "error": _error_record(error),
                        }
                    else:
                        query_end_time = time.time()
                        query_end_ns = time.monotonic_ns()
                        row = {
                            "query_index": query_index,
                            "status": "ok",
                            "query_start_time": query_start_time,
                            "query_end_time": query_end_time,
                            "query_start_monotonic_ns": query_start_ns,
                            "query_end_monotonic_ns": query_end_ns,
                            "duration_seconds": (query_end_ns - query_start_ns) / 1_000_000_000,
                            "target": target,
                        }
                    _write_row(log, row)
                    wait_seconds = max(0.0, target_interval - (time.monotonic() - query_started))
                    if target_stop.wait(wait_seconds):
                        break
        except BaseException as error:
            worker_failures.append(error)

    if read_target is not None:
        target_thread = threading.Thread(
            target=target_worker,
            name="acceptance-target-observer",
            daemon=True,
        )
        target_thread.start()

    primary_error: BaseException | None = None
    primary_traceback = None
    try:
        deadline = time.monotonic() + max_duration
        next_focus = time.monotonic()
        with focus_path.open("x", encoding="utf-8") as log:
            while time.monotonic() < deadline:
                if stop():
                    break
                query_start_time = time.time()
                query_start_ns = time.monotonic_ns()
                row: dict[str, Any] = {
                    "time": query_start_time,
                    "monotonic_ns": query_start_ns,
                    "field_query_start_time": query_start_time,
                    "field_query_start_monotonic_ns": query_start_ns,
                }
                try:
                    field = read_fixture(state)
                except Exception as error:
                    query_end_time = time.time()
                    query_end_ns = time.monotonic_ns()
                    row.update(
                        field_query_end_time=query_end_time,
                        field_query_end_monotonic_ns=query_end_ns,
                        error=str(error),
                    )
                else:
                    query_end_time = time.time()
                    query_end_ns = time.monotonic_ns()
                    row.update(
                        time=field.get("time", query_end_time),
                        field=field,
                        field_query_end_time=query_end_time,
                        field_query_end_monotonic_ns=query_end_ns,
                    )
                _write_row(log, row)

                next_focus += focus_interval
                delay = next_focus - time.monotonic()
                if delay > 0:
                    time.sleep(delay)
                else:
                    # Avoid catch-up bursts after a delayed fixture RPC.
                    next_focus = time.monotonic()
    except BaseException as error:
        primary_error = error
        primary_traceback = error.__traceback__
    finally:
        target_stop.set()
        if target_thread is not None:
            target_thread.join(join_timeout)

    if target_thread is not None and target_thread.is_alive():
        raise ObserverShutdownError(
            f"target observer worker did not stop within {join_timeout:.3f}s"
        )
    if worker_failures and primary_error is None:
        raise ObserverShutdownError(
            "target observer worker failed: " + str(worker_failures[0])
        ) from worker_failures[0]
    if primary_error is not None:
        raise primary_error.with_traceback(primary_traceback)
