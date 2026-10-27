import importlib.util
from pathlib import Path
import signal
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("verify_browser_text_output.py")
SPEC = importlib.util.spec_from_file_location("verify_browser_text_output", SCRIPT)
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class Clock:
    value = 0.0

    def now(self):
        return self.value

    def sleep(self, duration):
        self.value += duration


class BrowserCleanupTests(unittest.TestCase):
    def setUp(self):
        self.work = Path("/tmp/browser-matrix-owned-test")
        self.bundle = Path("/Applications/Google Chrome.app")
        self.executable = self.bundle / "Contents/MacOS/Google Chrome"
        self.main = runner.ProcessInfo(
            401, 1, "Tue Oct 27 12:00:00 2026",
            f"{self.executable} --user-data-dir={self.work}/profile",
        )
        self.child = runner.ProcessInfo(
            402, 401, "Tue Oct 27 12:00:01 2026",
            f"{self.bundle}/Contents/Frameworks/Chrome Helper.app/Contents/MacOS/Helper --type=renderer",
        )
        self.user_chrome = runner.ProcessInfo(
            501, 1, "Tue Oct 27 11:00:00 2026",
            f"{self.executable} --user-data-dir=/Users/test/Library/Application Support/Google/Chrome",
        )

    def test_discovery_uses_unique_path_exact_bundle_and_ancestry(self):
        processes = {p.pid: p for p in (self.main, self.child, self.user_chrome)}
        owned, depths = runner.find_owned(processes, self.work, self.bundle, self.executable)
        self.assertEqual(set(owned), {401, 402})
        self.assertEqual(depths[402], 1)

    def test_ps_parser_preserves_spaces_in_bundle_and_args(self):
        line = f"701 1 Tue Oct 27 12:00:00 2026 {self.executable} --profile={self.work}/profile\n"
        parsed = runner.parse_process_table(line)
        self.assertEqual(parsed[701].command,
                         f"{self.executable} --profile={self.work}/profile")

    def test_cleanup_signals_only_owned_processes(self):
        processes = {p.pid: p for p in (self.main, self.child, self.user_chrome)}
        signals = []

        def send(pid, sig):
            signals.append((pid, sig))
            processes.pop(pid, None)

        clock = Clock()
        clean, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, {}, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertTrue(clean)
        self.assertEqual(survivors, [])
        self.assertEqual({pid for pid, _ in signals}, {401, 402})
        self.assertTrue(all(sig == signal.SIGTERM for _, sig in signals))
        self.assertIn(501, processes)

    def test_pid_reuse_after_term_is_not_killed(self):
        processes = {401: self.main}
        signals = []

        def send(pid, sig):
            signals.append((pid, sig))
            processes[pid] = runner.ProcessInfo(pid, 1, self.user_chrome.started, self.user_chrome.command)

        clock = Clock()
        clean, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, {}, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertTrue(clean)
        self.assertEqual(survivors, [])
        self.assertEqual(signals, [(401, signal.SIGTERM)])

    def test_live_owned_process_escalates_to_kill(self):
        processes = {401: self.main}
        signals = []
        clock = Clock()

        def send(pid, sig):
            signals.append((pid, sig))
            if sig == signal.SIGKILL:
                processes.pop(pid, None)

        clean, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, {}, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertTrue(clean)
        self.assertEqual(survivors, [])
        self.assertEqual(signals, [(401, signal.SIGTERM), (401, signal.SIGKILL)])

    def test_changed_command_same_start_stays_uncertain(self):
        processes = {401: self.main}
        signals = []
        clock = Clock()

        def send(pid, sig):
            signals.append((pid, sig))
            processes[pid] = runner.ProcessInfo(
                pid, 1, self.main.started, f"{self.executable} --changed-argv",
            )

        clean, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, {}, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertFalse(clean)
        self.assertEqual(survivors, [401])
        self.assertEqual(signals, [(401, signal.SIGTERM)])

    def test_reparented_child_remains_tracked_across_launcher_scans(self):
        processes = {p.pid: p for p in (self.main, self.child)}
        tracked, signals = {}, []

        def send(pid, sig):
            signals.append((pid, sig))
            if pid == self.main.pid:
                processes.pop(pid, None)
                processes[self.child.pid] = runner.ProcessInfo(
                    self.child.pid, 1, self.child.started, self.child.command,
                )

        clock = Clock()
        first, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, tracked, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertFalse(first)
        self.assertEqual(survivors, [self.child.pid])
        second, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, tracked, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertFalse(second)
        self.assertEqual(survivors, [self.child.pid])
        processes.pop(self.child.pid)
        third, survivors = runner.stop_owned(
            self.work, self.bundle, self.executable, tracked, snapshot=lambda: dict(processes),
            send_signal=send, now=clock.now, sleep=clock.sleep,
        )
        self.assertTrue(third)
        self.assertEqual(survivors, [])

    def test_work_directory_removed_only_after_confirmed_shutdown(self):
        with tempfile.TemporaryDirectory(prefix="browser-cleanup-test-") as parent:
            work = Path(parent) / "owned-profile"
            work.mkdir()
            marker = work / "state"
            marker.write_text("synthetic")
            self.assertFalse(runner.remove_work_if_safe(work, False, True, True))
            self.assertEqual(marker.read_text(), "synthetic")
            self.assertTrue(runner.remove_work_if_safe(work, True, True, True))
            self.assertFalse(work.exists())

    def test_unsupported_mode_is_opt_in_and_clears_inherited_flag(self):
        inherited = {"MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED": "1", "OTHER": "kept"}
        default = runner.matrix_environment(inherited, self.work, False)
        self.assertNotIn("MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED", default)
        self.assertEqual(default["MIMAC_TEXT_MATRIX_WEB_ONLY"], "1")
        explicit = runner.matrix_environment({}, self.work, True)
        self.assertEqual(explicit["MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED"], "1")



if __name__ == "__main__":
    unittest.main()
