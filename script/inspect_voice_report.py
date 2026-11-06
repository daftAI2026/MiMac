#!/usr/bin/env python3
"""Inspect an existing --voice-overlay-report without starting MiMac or audio.

Exit 0 means every recorded session completed local PCM delivery and cleanup.
It does not prove target consumption, recognized text, remote STOP, or animation.
Exit 1 means incomplete/unknown/no sessions; exit 2 means invalid input.
"""

import argparse
import hashlib
import json
from pathlib import Path
import sys


OPERATIONS = ("source_stop", "source_close", "sink_drain", "sink_close")
OUTCOMES = ("ok", "eof", "cancelled", "deadline", "backpressure", "error")


def count(record, key):
    value = record.get(key)
    if type(value) is not int or value < 0:
        raise ValueError(f"missing or invalid count: {key}")
    return value


def calls(record):
    if not isinstance(record, dict):
        raise ValueError("operation summary must be an object")
    started = count(record, "calls_started")
    completed = count(record, "calls_completed")
    inflight = count(record, "calls_inflight")
    if started != completed + inflight:
        raise ValueError("call totals do not reconcile")
    return started, completed, inflight


def operation(record):
    totals = calls(record)
    if sum(count(record, key) for key in OUTCOMES) != totals[1]:
        raise ValueError("operation outcomes do not reconcile")
    return totals


def inspect_session(session):
    if not isinstance(session, dict):
        raise ValueError("session summary must be an object")
    identity = {key: count(session, key) for key in ("session", "generation")}
    _, _, inflight = calls(session)
    source = count(session, "source_read_samples")
    accepted = count(session, "sink_accepted_samples")
    unknown = count(session, "sink_unknown_acceptance_calls")
    errors = count(session, "error_calls")
    reasons = []
    known = (session.get("source_read_samples_complete") is True and
             session.get("sink_accepted_samples_known") is True and unknown == 0)
    if not known:
        reasons.append("sample_accounting_unknown")
    if source == 0:
        reasons.append("no_source_samples")
    if known and source != accepted:
        reasons.append("sample_counts_differ")
    if inflight:
        reasons.append("calls_inflight")
    if errors:
        reasons.append("io_errors")
    read = session.get("source_read")
    operation(read)
    if read["eof"] != 1:
        reasons.append("source_eof_not_once")
    for name in OPERATIONS:
        value = session.get(name)
        totals = operation(value)
        if totals != (1, 1, 0) or value["ok"] != 1:
            reasons.append(name + "_not_completed_once")
    write = session.get("sink_try_write")
    operation(write)
    # Backpressure is a known refusal before acceptance, not a lost sample.
    return {
        **identity,
        "local_delivery": "complete" if not reasons else "pending" if inflight else "incomplete" if known else "unknown",
        "source_samples": source,
        "accepted_samples": accepted,
        "sample_accounting_known": known,
        "backpressure_calls": write["backpressure"],
        "detail_spans_dropped": count(session, "detail_spans_dropped"),
        "reasons": reasons,
    }


def inspect(report):
    if not isinstance(report, dict) or report.get("schema") != 2:
        raise ValueError("expected a schema 2 voice-overlay report")
    io = report.get("io")
    if not isinstance(io, dict) or not isinstance(io.get("sessions"), list):
        raise ValueError("missing session summaries; detail spans cannot replace them")
    results = [inspect_session(session) for session in io["sessions"]]
    identities = [(s["generation"], s["session"]) for s in results]
    if len(set(identities)) != len(identities):
        raise ValueError("duplicate session identity")
    coverage = io.get("session_summary_coverage")
    if coverage not in ("complete", "overflow"):
        raise ValueError("unknown session summary coverage")
    overflow = count(io, "session_overflow_calls")
    if coverage == "complete" and overflow:
        raise ValueError("complete coverage contradicts overflow calls")
    complete = bool(results) and coverage == "complete" and all(
        s["local_delivery"] == "complete" for s in results)
    return {
        "scope": "local_pcm_delivery_and_cleanup_only",
        "local_delivery": "complete" if complete else "needs_review",
        "session_summary_coverage": coverage,
        "session_overflow_calls": overflow,
        "detail_spans_dropped": count(io, "dropped"),
        "sessions": results,
        "not_verified": ["target_consumption", "recognized_text", "remote_stop_ack", "visible_animation"],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path, help="existing --voice-overlay-report JSON")
    args = parser.parse_args()
    try:
        raw = args.report.read_bytes()
        result = inspect(json.loads(raw))
        result["report_sha256"] = hashlib.sha256(raw).hexdigest()
    except (OSError, ValueError, TypeError, KeyError) as error:
        print(json.dumps({"error": str(error)}, ensure_ascii=False), file=sys.stderr)
        return 2
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result["local_delivery"] == "complete" else 1


if __name__ == "__main__":
    raise SystemExit(main())
