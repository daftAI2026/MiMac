"""Check a planned utterance and classify text around a fixture snapshot.

Both event and boundary timestamps originate in the same fixed Swift fixture's
mach_continuous_time clock. Python monotonic time is deliberately not compared.
A later commit is observed after the boundary, not necessarily caused by cleanup;
it cannot satisfy the planned utterance's pre-cleanup verdict.
"""

import unicodedata


def normalize_utterance(text):
    """Ignore ASR punctuation/spacing only; keep words, order and symbols."""
    return "".join(c for c in text if not c.isspace()
                   and not unicodedata.category(c).startswith("P"))


def assess_utterance(baseline, final, expected):
    if expected is None:
        return {"status": "NOT_REQUESTED", "scope": "no planned utterance; no accuracy verdict"}
    if not isinstance(expected, str) or not normalize_utterance(expected):
        raise ValueError("expected text must contain spoken content")
    appended = final[len(baseline):] if isinstance(final, str) and final.startswith(baseline) else None
    complete = appended is not None and normalize_utterance(appended) == normalize_utterance(expected)
    return {"status": "PASS" if complete else "NOT_PASSED", "expected": expected,
            "appended": appended, "scope": "exact planned append, ignoring punctuation and whitespace only; not general ASR accuracy"}


def _integer(value):
    return isinstance(value, int) and not isinstance(value, bool) and value > 0


def _committed(value, baseline):
    text = value.get("text")
    return (isinstance(text, str) and text.startswith(baseline)
            and len(text) > len(baseline) and value.get("has_marked_text") is False)


def summarize_text_evidence(baseline, before, after, events, fixture_pid):
    before = before or {}
    after = after or {}
    cutoff = before.get("monotonic_ns")
    valid_boundary = _integer(cutoff) and before.get("pid") == fixture_pid
    groups = {"before_boundary": [], "after_boundary": [], "unclassified": []}
    for event in events:
        if event.get("event") != "text_did_change" or not _committed(event, baseline):
            continue
        stamp = event.get("monotonic_ns")
        if not valid_boundary or event.get("pid") != fixture_pid or not _integer(stamp):
            groups["unclassified"].append(event)
        else:
            groups["before_boundary" if stamp <= cutoff else "after_boundary"].append(event)
    return {
        "scope": "fixture text commits around the pre-cleanup observation boundary; later text does not prove cleanup caused it and cannot pass normal new_text",
        "clock": "same-fixture mach_continuous_time nanoseconds",
        "boundary_verified": valid_boundary,
        "boundary_monotonic_ns": cutoff if valid_boundary else None,
        "before_cleanup": before,
        "after_cleanup": after,
        "commits": groups,
        "new_text_before_cleanup": before.get("pid") == fixture_pid and _committed(before, baseline),
        "new_text_after_cleanup": after.get("pid") == fixture_pid and _committed(after, baseline),
        "text_changed_after_boundary": (after.get("pid") == fixture_pid and before.get("pid") == fixture_pid
                                        and isinstance(after.get("text"), str) and after.get("text") != before.get("text")),
    }
