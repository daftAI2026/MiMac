import unittest

from permission_record_repair import stale_registration


class StaleRegistrationTests(unittest.TestCase):
    old_hash = "a" * 40
    current_hash = "b" * 40

    def mismatch(self, service="ListenEvent", bundle="dev.mimac.app.dev", current=None):
        return (
            "2026-11-20 00:13:40.001 Df tccd [com.apple.TCC:access] "
            f"Failed to match existing code requirement for subject {bundle} and service kTCCService{service}\n"
            f'\tcdhash H"{self.old_hash}"\n'
            f'\tcdhash H"{current or self.current_hash}"\n'
        )

    def change(self, kind, service="ListenEvent", bundle="dev.mimac.app.dev"):
        return (
            "2026-11-20 00:13:44.272 Df tccd [com.apple.TCC:events] "
            f"Publishing <TCCDEvent: type={kind}, service=kTCCService{service}, "
            f"identifier_type=Bundle ID, identifier={bundle}> to 0 subscribers: {{"
        )

    def check(self, log, service="ListenEvent"):
        return stale_registration(log, service, self.current_hash)

    def test_only_exact_service_bundle_and_current_code(self):
        self.assertTrue(self.check(self.mismatch()))
        self.assertTrue(self.check(self.mismatch(service="Accessibility"), "Accessibility"))
        self.assertFalse(self.check(self.mismatch(service="Accessibility")))
        self.assertFalse(self.check(self.mismatch(bundle="dev.mimac.app")))
        self.assertFalse(self.check(self.mismatch(current="c" * 40)))
        self.assertFalse(self.check(self.mismatch(current=self.old_hash)))
        self.assertFalse(self.check(self.mismatch().splitlines()[0]))
        self.assertFalse(self.check(""))

    def test_only_current_record_can_be_repaired(self):
        for kind in ("Delete", "Modify", "Insert"):
            self.assertFalse(self.check(self.mismatch() + self.change(kind)))
        self.assertTrue(self.check(self.mismatch() + self.change("Modify", service="Accessibility")))
        self.assertTrue(self.check(self.mismatch() + self.change("Modify", bundle="dev.mimac.app")))
        self.assertTrue(self.check(self.change("Modify") + "\n" + self.mismatch()))
        self.assertFalse(self.check(self.mismatch() + self.mismatch(current="c" * 40)))
