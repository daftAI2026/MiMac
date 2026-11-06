import unittest
from acceptance_evidence import summarize_text_evidence


class TextEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.before = {"pid": 42, "text": "前文", "has_marked_text": False, "monotonic_ns": 200, "time": 20}
        self.after = {**self.before, "text": "前文迟到", "monotonic_ns": 400}

    def event(self, stamp, **changes):
        return {**self.after, "event": "text_did_change", "monotonic_ns": stamp, **changes}

    def result(self, events, before=None, after=None):
        return summarize_text_evidence("前文", self.before if before is None else before, self.after if after is None else after, events, 42)

    def test_late_commit_is_preserved_but_does_not_pass_before_cleanup(self):
        result = self.result([self.event(300)])
        self.assertFalse(result["new_text_before_cleanup"])
        self.assertTrue(result["new_text_after_cleanup"])
        self.assertEqual(len(result["commits"]["after_boundary"]), 1)
        self.assertTrue(result["text_changed_after_boundary"])

    def test_normal_committed_text_and_pre_boundary_event(self):
        before = {**self.before, "text": "前文正常"}
        result = self.result([self.event(150, text="前文正常")], before=before, after=before)
        self.assertTrue(result["new_text_before_cleanup"])
        self.assertEqual(len(result["commits"]["before_boundary"]), 1)
        self.assertFalse(result["text_changed_after_boundary"])

    def test_wall_clock_jump_does_not_reorder_fixture_events(self):
        result = self.result([self.event(100, time=500), self.event(300, time=1)])
        self.assertEqual([x["monotonic_ns"] for x in result["commits"]["before_boundary"]], [100])
        self.assertEqual([x["monotonic_ns"] for x in result["commits"]["after_boundary"]], [300])

    def test_missing_clock_wrong_identity_are_not_classified(self):
        for changes in [{"monotonic_ns": None}, {"pid": 7}, {"monotonic_ns": True}]:
            with self.subTest(changes=changes):
                result = self.result([self.event(100, **changes)])
                self.assertEqual(len(result["commits"]["unclassified"]), 1)
                self.assertFalse(result["commits"]["before_boundary"])
        result = self.result([self.event(100)], before={})
        self.assertFalse(result["boundary_verified"])
        self.assertEqual(len(result["commits"]["unclassified"]), 1)

    def test_marked_or_unrelated_text_is_not_a_commit(self):
        result = self.result([self.event(100, has_marked_text=True), self.event(100, text="其他"), self.event(100, event="other")])
        self.assertFalse(any(result["commits"].values()))


if __name__ == "__main__":
    unittest.main()
