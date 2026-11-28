#!/usr/bin/env python3
"""Summarize fixed per-press audio-session records offline; never opens MiMac."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import sys

from inspect_voice_report import inspect as inspect_voice_report


TRACE_FIELDS = {"time", "pid", "generation", "press", "session", "event", "stage", "result"}
PRESS_EDGE_EVENTS = {"press", "press_during_session", "press_without_session"}
PRE_SESSION_FAILURE_EVENTS = {
    "local_binding_rejected", "local_audio_unavailable", "audio_unavailable",
    "action_unavailable", "action_preflight_rejected", "audio_binding_unready",
    "route_preflight_rejected",
}
TERMINAL_EVENTS = {"drained", "failed", "cleanup_failed"}
CODE = re.compile(r"^[a-z][a-z0-9_]*$")


def parse_time(value: str) -> datetime:
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ValueError("timestamps must include a UTC offset")
    return parsed.astimezone(timezone.utc)


def format_time(value: datetime) -> str:
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def parse_trace_line(line: str, line_number: int) -> dict:
    fields = {}
    for token in line.split():
        if "=" not in token:
            raise ValueError(f"line {line_number}: expected fixed key=value fields")
        key, value = token.split("=", 1)
        if key in fields:
            raise ValueError(f"line {line_number}: duplicate field")
        fields[key] = value
    if fields.keys() != TRACE_FIELDS:
        raise ValueError(f"line {line_number}: unexpected fixed-field shape")
    try:
        record = {
            "at": parse_time(fields["time"]),
            "pid": int(fields["pid"]),
            "generation": int(fields["generation"]),
            "press": int(fields["press"]),
            "session": int(fields["session"]),
            "event": fields["event"],
            "stage": fields["stage"],
            "result": fields["result"],
        }
    except (TypeError, ValueError) as error:
        raise ValueError(f"line {line_number}: invalid fixed-field value") from error
    if record["pid"] <= 0 or any(record[key] < 0 for key in ("generation", "press", "session")):
        raise ValueError(f"line {line_number}: invalid identity")
    for key in ("event", "stage", "result"):
        if record[key] and not CODE.fullmatch(record[key]):
            raise ValueError(f"line {line_number}: invalid code field")
    return record


def read_trace(path: Path, pid: int, start: datetime, end: datetime) -> list[dict]:
    if pid <= 0:
        raise ValueError("PID must be positive")
    if start > end:
        raise ValueError("start must not be after end")
    records = []
    try:
        with path.open("r", encoding="utf-8") as stream:
            for line_number, line in enumerate(stream, 1):
                if not line.strip():
                    continue
                record = parse_trace_line(line, line_number)
                if record["pid"] == pid and start <= record["at"] <= end:
                    records.append(record)
    except UnicodeDecodeError as error:
        raise ValueError("trace is not UTF-8 fixed-field text") from error
    return sorted(records, key=lambda row: (row["at"], row["generation"], row["press"], row["session"]))


def record_code(row: dict) -> dict:
    return {"at": format_time(row["at"]), "event": row["event"],
            "stage": row["stage"], "result": row["result"]}


def load_voice_report(path: Path) -> tuple[str, dict]:
    raw = path.read_bytes()
    report = json.loads(raw)
    inspected = inspect_voice_report(report)
    return hashlib.sha256(raw).hexdigest(), inspected


def summarize(records: list[dict], *, pid: int, start: datetime, end: datetime,
              trace_sha256: str, voice_report: tuple[str, dict] | None = None) -> dict:
    presses: dict[tuple[int, int], list[dict]] = {}
    for row in records:
        if row["press"]:
            presses.setdefault((row["generation"], row["press"]), []).append(row)

    owners: dict[tuple[int, int], list[tuple[int, int]]] = {}
    for key, rows in presses.items():
        edge = next((row for row in rows if row["event"] in PRESS_EDGE_EVENTS), None)
        if edge and edge["event"] == "press" and edge["session"]:
            owners.setdefault((edge["generation"], edge["session"]), []).append(key)

    lifecycle = [row for row in records if row["session"] and row["event"] in ({"prepared"} | TERMINAL_EVENTS)]
    lifecycle_by_session: dict[tuple[int, int], list[dict]] = {}
    for row in lifecycle:
        lifecycle_by_session.setdefault((row["generation"], row["session"]), []).append(row)

    voice_hash = None
    voice_summary = None
    if voice_report is not None:
        voice_hash, voice_summary = voice_report

    results = []
    attached_sessions = set()
    for (generation, press), rows in sorted(presses.items(), key=lambda item: min(r["at"] for r in item[1])):
        rows = sorted(rows, key=lambda row: row["at"])
        edge = next((row for row in rows if row["event"] in PRESS_EDGE_EVENTS), None)
        failures = [row for row in rows if row["event"] in PRE_SESSION_FAILURE_EVENTS or
                    row["result"] not in ("", "ok")]
        result = {
            "generation": generation,
            "press_id": press,
            "first_record_at": format_time(rows[0]["at"]),
            "press_event": edge["event"] if edge else None,
            "press_event_at": format_time(edge["at"]) if edge else None,
            "session_field_reference_id": edge["session"] if edge and edge["session"] else None,
            "accepted_session_id": None,
            "prepared_count": 0,
            "terminal": "unknown",
            "terminal_event": None,
            "failure_codes": [record_code(row) for row in failures],
            "release_at": None,
        }
        releases = [row for row in rows if row["event"] == "release"]
        if releases:
            result["release_at"] = format_time(releases[-1]["at"])

        accepted_session = None
        if edge and edge["event"] == "press" and edge["session"] > 0:
            accepted_session = edge["session"]
            result["accepted_session_id"] = accepted_session
            session_key = (generation, accepted_session)
            attached_sessions.add(session_key)
            if len(owners.get(session_key, [])) != 1:
                result["terminal"] = "unknown"
                result["failure_codes"].append({"event": "session_owner_conflict", "stage": "", "result": ""})
            else:
                session_rows = lifecycle_by_session.get(session_key, [])
                prepared = [row for row in session_rows if row["event"] == "prepared"]
                terminal_rows = [row for row in session_rows if row["event"] in TERMINAL_EVENTS]
                result["prepared_count"] = len(prepared)
                if len(terminal_rows) == 1:
                    terminal_row = terminal_rows[0]
                    if terminal_row["event"] == "drained" and terminal_row["result"] == "ok":
                        result["terminal"] = "drained"
                    elif terminal_row["event"] in {"failed", "cleanup_failed"} or terminal_row["result"] not in ("", "ok"):
                        result["terminal"] = "failed"
                        result["failure_codes"].append(record_code(terminal_row))
                    result["terminal_event"] = record_code(terminal_row)
                elif len(terminal_rows) > 1:
                    result["failure_codes"].append({"event": "multiple_terminal_events", "stage": "", "result": ""})
        elif edge and edge["event"] == "press_during_session":
            if failures:
                result["terminal"] = "failed_before_session"
            else:
                result["terminal"] = "not_accepted"
        elif failures:
            result["terminal"] = "failed_before_session"
        results.append(result)

    unmatched = [record_code(row) | {"generation": row["generation"], "session_id": row["session"],
                                      "press_field": row["press"]}
                 for row in lifecycle if (row["generation"], row["session"]) not in attached_sessions]
    failed_count = sum(row["terminal"] in {"failed", "failed_before_session"} for row in results)
    unknown_count = sum(row["terminal"] == "unknown" for row in results)
    not_accepted_count = sum(row["terminal"] == "not_accepted" for row in results)
    diagnostic_state = "failure" if failed_count else "unknown" if unknown_count or unmatched or not records else "observed"
    output = {
        "scope": "fixed_audio_session_log_fields_only",
        "diagnostic_state": diagnostic_state,
        "filter": {"pid": pid, "start": format_time(start), "end": format_time(end)},
        "trace_sha256": trace_sha256,
        "records_in_window": len(records),
        "press_count": len(results),
        "failure_count": failed_count,
        "unknown_count": unknown_count,
        "not_accepted_count": not_accepted_count,
        "unmatched_lifecycle_event_count": len(unmatched),
        "presses": results,
        "unmatched_lifecycle_events": unmatched,
        "not_read": ["audio", "recognized_text", "other_app_content"],
    }
    if voice_report is not None:
        output["voice_report_context"] = {
            "sha256": voice_hash,
            "linked_to_per_press": False,
            "reason": "schema 2 voice report has no PID; session numbers can repeat after process reopen",
            "scope": voice_summary["scope"],
            "session_summary_coverage": voice_summary["session_summary_coverage"],
            "session_overflow_calls": voice_summary["session_overflow_calls"],
            "local_delivery": voice_summary["local_delivery"],
        }
    return output


def inspect(path: Path, *, pid: int, start: str, end: str,
            voice_report_path: Path | None = None) -> dict:
    start_time, end_time = parse_time(start), parse_time(end)
    records = read_trace(path, pid, start_time, end_time)
    voice_report = load_voice_report(voice_report_path) if voice_report_path else None
    raw_hash = hashlib.sha256(path.read_bytes()).hexdigest()
    return summarize(records, pid=pid, start=start_time, end=end_time,
                     trace_sha256=raw_hash, voice_report=voice_report)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trace", required=True, type=Path, help="one existing fixed-field audio-session.log")
    parser.add_argument("--pid", required=True, type=int, help="exact process ID from the trace")
    parser.add_argument("--start", required=True, help="inclusive RFC3339 timestamp with UTC offset")
    parser.add_argument("--end", required=True, help="inclusive RFC3339 timestamp with UTC offset")
    parser.add_argument("--voice-report", type=Path,
                        help="optional schema 2 report; parsed independently by inspect_voice_report.py (never joined by session number)")
    args = parser.parse_args(argv)
    try:
        result = inspect(args.trace, pid=args.pid, start=args.start, end=args.end,
                         voice_report_path=args.voice_report)
    except (OSError, ValueError, TypeError, KeyError, json.JSONDecodeError) as error:
        print(json.dumps({"error": str(error)}, ensure_ascii=False), file=sys.stderr)
        return 2
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 1 if result["diagnostic_state"] in {"failure", "unknown"} else 0


if __name__ == "__main__":
    raise SystemExit(main())
