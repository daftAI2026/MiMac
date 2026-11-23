#!/usr/bin/env python3
"""Run the native first-use permission flow for one frozen verification bundle."""

import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
CACHE = Path.home() / "Library/Caches/MiMac"
INPUT_PAGES = ("Input Monitoring", "输入监控")
CONTROL_PAGES = ("Device Control and Data Access", "Accessibility", "设备控制和数据访问", "辅助功能")
INPUT_ROW_TITLE = "在系统设置中管理输入监控"
CONTROL_ROW_TITLES = (
    "在系统设置中管理设备控制和数据访问",
    "在系统设置中管理辅助功能",
    "在系统设置中管理裝置控制與資料存取",
    "在系统设置中管理輔助使用",
)
CONTROL_PERMISSION_NAMES = tuple(title.removeprefix("在系统设置中管理") for title in CONTROL_ROW_TITLES)
BLUETOOTH_PERMISSION_NAMES = ("蓝牙", "藍牙", "Bluetooth")
BLUETOOTH_ROW_TITLES = tuple("在系统设置中管理" + name for name in BLUETOOTH_PERMISSION_NAMES)
BLUETOOTH_REQUEST_BUNDLE = "com.apple.UserNotificationCenter"
BLUETOOTH_REQUEST_PATH = "/System/Library/CoreServices/UserNotificationCenter.app"
GENERAL_INPUT_MANAGE = "管理输入监控"
GENERAL_CONTROL_MANAGE = tuple("管理" + title for title in CONTROL_PERMISSION_NAMES)
GENERAL_BLUETOOTH_MANAGE = tuple("管理" + title for title in BLUETOOTH_PERMISSION_NAMES)
PERMISSION_ACTION_LABEL = "授权…"
DRAG_GEOMETRY_TIMEOUT = 12
FOCUS_READBACK_TIMEOUT = 2
FOCUS_READBACK_INTERVAL = 0.12
RESTART_FOCUS_RECOVERY_SECONDS = 4
AUTH_PROMPT_BUNDLES = {
    "com.apple.systempreferences",
    "com.apple.SecurityAgent",
    "com.apple.LocalAuthentication.UIAgent",
}


def capture(*command):
    result = subprocess.run(command, text=True, capture_output=True, timeout=12)
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError:
        data = {"error": result.stderr.strip() or result.stdout.strip() or "no response"}
    if result.returncode:
        raise RuntimeError(data)
    return data


def wait_for(test, description, seconds=12):
    deadline = None if seconds == 0 else time.monotonic() + seconds
    while deadline is None or time.monotonic() < deadline:
        if result := test():
            return result
        time.sleep(0.18)
    raise RuntimeError(f"timed out waiting for {description}")


def read_json(path):
    return json.loads(path.read_text()) if path.is_file() else {}


def bluetooth_prompt_matches(lines, app_name):
    expected = f"Allow “{app_name}” to use Bluetooth?"
    return isinstance(lines, (list, tuple)) and expected in lines


class Acceptance:
    def __init__(self, args):
        self.bundle = args.bundle.resolve(strict=True)
        with (self.bundle / "Contents/Info.plist").open("rb") as manifest:
            info = plistlib.load(manifest)
        self.name = info["CFBundleDisplayName"]
        self.identifier = info["CFBundleIdentifier"]
        self.fixed_dev = args.fixed_dev_receipt is not None
        self.existing_input = args.existing_input
        self.general_permissions = args.general_permissions
        self.resume_input_drop = args.resume_input_drop
        self.drag_regressions_only = args.drag_regressions_only
        self.cancel_reentry = getattr(args, "cancel_reentry", False)
        self.focus_check = getattr(args, "focus_check", False)
        self.two_list_permissions_only = getattr(args, "two_list_permissions_only", False)
        self.bluetooth_only = getattr(args, "bluetooth_only", False)
        self.bluetooth_allow_actor = getattr(args, "bluetooth_allow_actor", None) or "runner"
        if getattr(args, "bluetooth_allow_actor", None) and (
            self.existing_input or self.two_list_permissions_only or self.cancel_reentry or
            self.drag_regressions_only or self.focus_check or self.resume_input_drop or args.finish
        ):
            raise RuntimeError("--bluetooth-allow-actor is only valid for full first-use, full general permissions, or --bluetooth-only")
        if self.two_list_permissions_only and not self.general_permissions:
            raise RuntimeError("--two-list-permissions-only requires --general-permissions")
        self.focus_recovery_attempts = set()
        self.focus_visit_sequence = 0
        selected_scopes = sum((self.existing_input, self.general_permissions, self.resume_input_drop,
                               self.drag_regressions_only, self.cancel_reentry, self.focus_check,
                               self.bluetooth_only, args.finish))
        if selected_scopes > 1:
            raise RuntimeError("--existing-input, --general-permissions, --cancel-reentry, --resume-input-drop, --drag-regressions-only, --focus-check, --bluetooth-only and --finish are separate acceptance scopes")
        if self.existing_input and not self.fixed_dev:
            raise RuntimeError("--existing-input requires the verified canonical Dev receipt")
        if self.general_permissions and not self.fixed_dev:
            raise RuntimeError("--general-permissions requires the verified canonical Dev receipt")
        if self.cancel_reentry and not self.fixed_dev:
            raise RuntimeError("--cancel-reentry requires the verified canonical Dev receipt")
        if self.resume_input_drop and not self.fixed_dev:
            raise RuntimeError("--resume-input-drop requires the verified canonical Dev receipt")
        if self.drag_regressions_only and not self.fixed_dev:
            raise RuntimeError("--drag-regressions-only requires the verified canonical Dev receipt")
        if self.focus_check and not self.fixed_dev:
            raise RuntimeError("--focus-check requires the verified canonical Dev receipt")
        if self.bluetooth_only and not self.fixed_dev:
            raise RuntimeError("--bluetooth-only requires the verified canonical Dev receipt")
        if self.fixed_dev:
            from live_acceptance import verify_fixed_dev
            fixed, _ = verify_fixed_dev(args.fixed_dev_receipt)
            if (self.bundle != fixed or self.identifier != "dev.mimac.app.dev" or self.name != "MiMac Dev"):
                raise RuntimeError("the receipt and tested Dev must identify the same app")
        else:
            if not self.bundle.is_relative_to(CACHE.resolve()) or not re.fullmatch(
                r"dev\.mimac\.verification\.[a-zA-Z0-9.-]+", self.identifier
            ):
                raise RuntimeError("use a separate verification bundle in ~/Library/Caches/MiMac")
            if not self.name.startswith("MiMac ") or self.name in ("MiMac", "MiMac Dev") or "/" in self.name:
                raise RuntimeError("the verification bundle needs its own configuration identity")
        executable_name = info["CFBundleExecutable"]
        if Path(executable_name).name != executable_name:
            raise RuntimeError("invalid bundle executable name")
        self.executable = self.bundle / "Contents/MacOS" / executable_name
        self.sha = args.sha256.lower()
        self.check_identity()
        self.config = Path.home() / "Library/Application Support" / self.name
        if self.config.exists() and not (args.finish or self.existing_input or self.general_permissions or
                                         self.cancel_reentry or self.resume_input_drop or self.drag_regressions_only or
                                         self.focus_check or self.bluetooth_only):
            raise RuntimeError("use a fresh verification identity with an unused configuration directory")
        self.output = args.output.resolve()
        if not self.output.is_relative_to(CACHE.resolve()):
            raise RuntimeError("acceptance output must be in ~/Library/Caches/MiMac")
        self.auth_timeout = args.auth_timeout
        if self.auth_timeout < 0:
            raise RuntimeError("--auth-timeout must be zero or greater")
        self.driver = self.output / "permission-driver"
        self.ax = self.output / "app-ax"
        identity = {"bundle": str(self.bundle), "bundle_id": self.identifier, "sha256": self.sha}
        self.identity = identity
        if args.finish:
            entries = [json.loads(line) for line in (self.output / "run.ndjson").read_text().splitlines()]
            self.finish_args = finish_args(entries, identity)
            self.confirmed_control = entries[-1].get("stage") == "system_control_confirmed"
            self.compile_fixture("PermissionAcceptance.swift", self.driver)
        elif self.resume_input_drop:
            entries = [json.loads(line) for line in (self.output / "run.ndjson").read_text().splitlines()]
            self.resume_input_args = resume_input_drop_args(entries, identity)
            self.compile_fixture("PermissionAcceptance.swift", self.driver)
            self.compile_fixture("LiveAcceptanceAX.swift", self.ax)
        elif self.drag_regressions_only or self.general_permissions or self.cancel_reentry or self.focus_check or self.bluetooth_only:
            self.output.mkdir()
            self.compile_fixture("PermissionAcceptance.swift", self.driver)
            if not self.focus_check:
                self.compile_fixture("LiveAcceptanceAX.swift", self.ax)
            mode = (
                "drag-regressions-only" if self.drag_regressions_only else
                "focus-recovery-only" if self.focus_check else
                "bluetooth-only" if self.bluetooth_only else
                "ordinary-cancel-reentry" if self.cancel_reentry else
                "ordinary-general-two-list-only" if self.two_list_permissions_only else
                "ordinary-general-permissions"
            )
            self.record("identity", {**identity, "mode": mode})
        else:
            self.output.mkdir()
            for name, destination in (("PermissionAcceptance.swift", self.driver), ("LiveAcceptanceAX.swift", self.ax)):
                self.compile_fixture(name, destination)
            self.record("identity", {**identity, "mode": "existing-input-device-control" if self.existing_input else "full-first-use"})

    @contextmanager
    def observe_feedback(self):
        with (self.output / "windows.ndjson").open("a") as window_evidence, \
             (self.output / "feedback.ndjson").open("a") as ax_evidence:
            observers = [
                subprocess.Popen([self.driver, "observe-windows", self.bundle], stdout=window_evidence),
                subprocess.Popen([self.driver, "observe-ax-feedback", self.bundle], stdout=ax_evidence),
            ]
            try:
                yield
            finally:
                for observer in observers:
                    observer.terminate()
                for observer in observers:
                    observer.wait(timeout=4)

    def compile_fixture(self, name, destination):
        subprocess.run(["/usr/bin/xcrun", "swiftc", HERE / "fixtures" / name, "-o", destination], check=True)

    def check_identity(self):
        subprocess.run(["/usr/bin/codesign", "--verify", "--deep", "--strict", self.bundle], check=True)
        if hashlib.sha256(self.executable.read_bytes()).hexdigest() != self.sha:
            raise RuntimeError("the executable does not match the frozen SHA-256")

    def probe(self, operation="probe"):
        result = capture(self.driver, operation, self.bundle)
        if result.get("extra_access_warning"):
            raise RuntimeError("an extra device-control prompt appeared for the tested app")
        return result

    def focus_recovery_candidate(self, state, app_pid, settings_pid, page):
        return focus_recovery_candidate(
            state, app_pid, settings_pid, page, self.identifier, str(self.bundle)
        )

    def wait_for_settings_focus_readback(self, initial, *, app_pid, settings_pid, page,
                                         original_state, flow_stage):
        deadline = time.monotonic() + FOCUS_READBACK_TIMEOUT
        current = initial
        samples = 0
        while True:
            samples += 1
            transition_safe = focus_readback_transition_safe(
                current, app_pid, settings_pid, page, self.identifier, str(self.bundle), original_state
            )
            if transition_safe and settings_focus_readback_ready(
                current, app_pid, settings_pid, page, self.identifier, str(self.bundle)
            ):
                return current, samples
            if not transition_safe:
                self.record("settings_focus_readback_failed", {
                    "flow_stage": flow_stage, "reason": "identity_or_modal_state_changed",
                    "observation": focus_probe_summary(current), "samples": samples,
                })
                raise RuntimeError("Settings focus readback changed identity or entered a blocked state")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.record("settings_focus_readback_failed", {
                    "flow_stage": flow_stage, "reason": "timed_out_waiting_for_exact_page_and_focus",
                    "observation": focus_probe_summary(current), "samples": samples,
                })
                raise RuntimeError("timed out waiting for the exact Settings page and foreground readback")
            time.sleep(min(FOCUS_READBACK_INTERVAL, remaining))
            try:
                current = self.probe()
            except RuntimeError as error:
                self.record("settings_focus_readback_failed", {
                    "flow_stage": flow_stage, "reason": "probe_failed",
                    "error": str(error), "observation": focus_probe_summary(current),
                    "samples": samples,
                })
                raise

    def restore_settings_focus_once(self, state, *, app_pid, settings_pid, page, stage, visit_id=None):
        if not self.focus_recovery_candidate(state, app_pid, settings_pid, page):
            self.record("settings_focus_recovery_blocked", {
                "flow_stage": stage, "app_pid": app_pid, "settings_pid": settings_pid,
                "expected_page": page, "observation": focus_probe_summary(state),
                "activation_performed": False,
            })
            raise RuntimeError("the exact existing Settings page is not safe to restore")
        if state.get("front") == settings_pid:
            return state, {"attempted": False, "reason": "settings_already_frontmost"}

        visit_id = visit_id or getattr(self, "current_focus_visit_id", "direct-check")
        key = (visit_id, app_pid, settings_pid, page)
        if key in self.focus_recovery_attempts:
            self.record("settings_focus_recovery_blocked", {
                "flow_stage": stage, "app_pid": app_pid, "settings_pid": settings_pid,
                "expected_page": page, "visit_id": visit_id,
                "observation": focus_probe_summary(state),
                "activation_performed": False, "reason": "one_attempt_already_used",
            })
            raise RuntimeError("Settings focus recovery was already attempted once for this page")
        self.focus_recovery_attempts.add(key)

        request = {
            "flow_stage": stage, "app_pid": app_pid, "settings_pid": settings_pid,
            "expected_page": page, "visit_id": visit_id, "attempt": 1,
            "initial_frontmost_pid": state.get("front"),
            "initial_frontmost_bundle_id": state.get("frontmost_bundle_id"),
            "native_initial_focus_verified": False,
            "activation_performed": False,
            "permission_change_requested": False,
            "native_drag_performed": False,
        }
        self.record("settings_focus_recovery_requested", request)
        try:
            action = capture(
                self.driver, "recover-settings-focus", self.bundle,
                str(app_pid), str(settings_pid), page,
            )
        except RuntimeError as error:
            detail = error.args[0] if error.args else str(error)
            self.record("settings_focus_recovery_failed", {
                **request, "error": detail,
                "activation_performed": bool(
                    isinstance(detail, dict) and detail.get("activation_performed")
                ),
            })
            raise
        self.record("settings_focus_recovery_native", {**request, **action})
        if (action.get("activation_performed") is not True and
                action.get("already_frontmost") is not True):
            raise RuntimeError("the native helper neither activated Settings nor found it already frontmost")

        try:
            readback, readback_samples = self.wait_for_settings_focus_readback(
                self.probe(), app_pid=app_pid, settings_pid=settings_pid, page=page,
                original_state=state, flow_stage=stage,
            )
        except RuntimeError as error:
            self.record("settings_focus_recovery_readback_failed", {
                "flow_stage": stage, "app_pid": app_pid, "settings_pid": settings_pid,
                "expected_page": page, "error": str(error),
                "activation_performed": action.get("activation_performed") is True,
                "permission_change_requested": False, "native_drag_performed": False,
            })
            raise
        readback_ok = True
        self.record("settings_focus_recovery_readback", {
            "flow_stage": stage, "app_pid": app_pid, "settings_pid": settings_pid,
            "expected_page": page, "readback": focus_probe_summary(readback),
            "settings_frontmost": readback.get("front") == settings_pid,
            "exact_page_verified": readback.get("page") == page,
            "permission_change_requested": False,
            "native_drag_performed": False,
            "samples": readback_samples, "passed": readback_ok,
        })
        if not readback_ok:
            raise RuntimeError("Settings focus recovery failed its independent PID and page readback")
        return readback, {
            "attempted": action.get("activation_performed") is True,
            "helper_called": True, "action": action,
        }

    def prepare_settings_for_drag(self, state, *, app_pid, pages, stage):
        settings_pid = state.get("settings")
        page = state.get("page")
        if page not in pages or not isinstance(settings_pid, int) or settings_pid <= 0:
            raise RuntimeError("the expected Settings page did not open in a unique process")
        self.focus_visit_sequence = getattr(self, "focus_visit_sequence", 0) + 1
        self.current_focus_visit_id = f"{stage}:{self.focus_visit_sequence}"
        native_initial_focus_verified = state.get("front") == settings_pid
        self.record("settings_page_opened", {
            "flow_stage": stage, "app_pid": app_pid, "settings_pid": settings_pid,
            "page": page, "front_at_first_probe": state.get("front"),
            "native_initial_focus_verified": native_initial_focus_verified,
            "settings_page_opened": True,
        })
        ready, recovery = self.restore_settings_focus_once(
            state, app_pid=app_pid, settings_pid=settings_pid, page=page, stage=stage,
            visit_id=self.current_focus_visit_id,
        )
        return ready, {
            "native_initial_focus_verified": native_initial_focus_verified,
            "focus_recovery_attempted": recovery["attempted"],
            "focus_recovery_helper_called": recovery.get("helper_called", False),
            "focus_recovery": recovery.get("action"),
        }

    def run_focus_check(self):
        state = self.preflight_no_existing_sheet("focus-check")
        app_pid = state.get("app")
        settings_pid = state.get("settings")
        page = state.get("page")
        if not focus_check_preflight_ready(
            state, self.identifier, str(self.bundle), INPUT_PAGES + CONTROL_PAGES
        ):
            self.record("FOCUS_CHECK_BLOCKED", {
                "app_pid": app_pid, "settings_pid": settings_pid, "page": page,
                "observation": focus_probe_summary(state),
                "permission_change_requested": False, "native_drag_performed": False,
            })
            raise RuntimeError(
                "--focus-check requires the exact fixed Dev, visible permission card, one open Input Monitoring or Device Control Settings page, and no password, sheet, or restart UI"
            )
        app_pid = int(app_pid)
        settings_pid = int(settings_pid)
        page = str(page)
        window_state = self.latest_window_observation()
        self.record("focus_check_preflight", {
            "app": app_pid, "settings": settings_pid, "page": page,
            "front_before": state.get("front"),
            "window_observation": window_state,
            "permission_change_requested": False, "native_drag_performed": False,
            "permission_rows_evaluated": False,
            "initial_focus_was_native": state.get("front") == settings_pid,
        })
        ready, recovery = self.restore_settings_focus_once(
            state, app_pid=app_pid, settings_pid=settings_pid, page=page, stage="focus-check",
        )
        try:
            ready, readback_samples = self.wait_for_settings_focus_readback(
                self.probe(), app_pid=app_pid, settings_pid=settings_pid, page=page,
                original_state=state, flow_stage="focus-check-verification",
            )
        except RuntimeError as error:
            self.record("FOCUS_CHECK_READBACK_FAILED", {
                "app": app_pid, "settings": settings_pid, "page": page,
                "flow_stage": "focus-check-verification",
                "error": str(error), "permission_change_requested": False,
                "native_drag_performed": False,
            })
            raise
        if not settings_focus_readback_ready(
            ready, app_pid, settings_pid, page, self.identifier, str(self.bundle)
        ):
            self.record("FOCUS_CHECK_READBACK_FAILED", {
                "app": app_pid, "settings": settings_pid, "page": page,
                "observation": focus_probe_summary(ready),
                "permission_change_requested": False, "native_drag_performed": False,
            })
            raise RuntimeError("--focus-check did not read back the same Settings PID and exact page in front")
        final_windows = self.latest_window_observation()
        self.record("FOCUS_CHECK_PASS", {
            "app": app_pid, "settings": settings_pid, "page": page,
            "initial_frontmost_pid": state.get("front"),
            "initial_frontmost_bundle_id": state.get("frontmost_bundle_id"),
            "final_frontmost_pid": ready.get("front"),
            "recovery_attempted": recovery["attempted"],
            "recovery_count": int(recovery["attempted"]),
            "focus_helper_called": recovery.get("helper_called", False),
            "readback_samples": readback_samples,
            "permission_change_requested": False, "native_drag_performed": False,
            "permission_rows_evaluated": False,
            "window_readback": final_windows,
        })

    def preflight_no_existing_sheet(self, scope):
        state = self.probe()
        if state.get("password") or state.get("system_sheet"):
            self.record("BLOCKED_EXISTING_SYSTEM_SHEET", {
                "scope": scope, "password_field": state.get("password"),
                "settings": state.get("settings"), "page": state.get("page"),
                "sheet_titles": state.get("system_sheet_titles", []),
                "action": "stopped before app launch or permission press"
            })
            raise RuntimeError("preflight found an existing macOS security or Settings sheet; resolve it manually before this run")
        return state

    def run_general_permissions(self):
        allow_bluetooth_pending = getattr(self, "two_list_permissions_only", False)
        state = self.preflight_no_existing_sheet("ordinary-general-permissions")
        if not state["app"]:
            subprocess.run([
                "/usr/bin/open", "--stderr", str(self.output / "app-stderr.log"),
                "-a", str(self.bundle), "--args", "--show-settings",
            ], check=True)
            state = wait_for(
                lambda: (current if (current := self.probe()).get("app") else None),
                "frozen Dev settings window",
            )
        pid = state["app"]
        if not pid:
            raise RuntimeError("--general-permissions could not find the frozen Dev process")

        nodes = wait_for(
            lambda: (current if ordinary_general_startup_ready(
                pid, current := self.view_if_windows_available(pid), self.name
            ) else None),
            "the frozen Dev main window and visible General button",
        )
        if not ordinary_general_surface_ready(nodes, self.name):
            self.press(pid, "通用")
            nodes = wait_for(
                lambda: (current if ordinary_general_surface_ready(current := self.view(pid), self.name) else None),
                "the frozen Dev General settings page",
            )
        if not ordinary_permission_guide_card_present(nodes, self.name):
            self.press(pid, "管理输入监控")

        if not allow_bluetooth_pending:
            self.ensure_bluetooth_permission(pid, scope="ordinary-general-full")

        initial_nodes = wait_for(
            lambda: (current if general_input_action_target(
                current := self.view(pid), self.name,
                allow_bluetooth_pending=allow_bluetooth_pending,
            ) else None),
            "the ordinary permission guide with Input Monitoring pending and every permission row accounted for",
        )
        input_title = general_input_action_target(
            initial_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
        )
        if input_title != INPUT_ROW_TITLE:
            raise RuntimeError("--general-permissions requires Input Monitoring to be pending in the ordinary guide")
        permission_rows = ordinary_permission_counts(initial_nodes, self.name)
        if permission_rows is None:
            raise RuntimeError("--general-permissions could not read every recognized permission row on both surfaces")
        self.record("general_permissions_preflight", {
            "app": pid, "settings": state["settings"], "front": state["front"],
            "flow": "ordinary-general-two-list-only" if allow_bluetooth_pending else "ordinary-general",
            "verified_permission_scope": ["input", "device_control"] if allow_bluetooth_pending else None,
            "input_pending": True,
            "device_control_pending": permission_rows["rows"].get("device_control") == "pending",
            "permission_rows": permission_rows,
            "onboarding_window_present": False,
            "permission_row_action": INPUT_ROW_TITLE,
        })

        press = self.press_permission_row(pid, INPUT_ROW_TITLE)
        self.record("permission_row_action", {
            "app": pid, "permission": "input-monitoring",
            "press_started_at": press["ax_press_started_at"],
            "press_completed_at": press["ax_press_completed_at"],
            "source": "right-side 授权…", "flow": "ordinary-general",
        })
        ready = self.wait_auth_or(
            "input-monitoring Settings pane",
            lambda current: current if settings_page_opened(current, pid, INPUT_PAGES) else None,
        )
        settings_pid = ready["settings"]
        self.record_window_latencies("input-monitoring", press)
        ready, focus = self.prepare_settings_for_drag(
            ready, app_pid=pid, pages=INPUT_PAGES, stage="ordinary-general-input-monitoring",
        )
        self.record("input_ready", {**ready, **focus, "flow": "ordinary-general"})
        self.record("native_drop", self.drag(
            "drag-input", permission="input-monitoring", app_pid=pid,
            settings_pid=settings_pid, pages=INPUT_PAGES,
        ))

        def input_granted_or_reopened(current):
            if current.get("app", 0) == pid and current.get("restart") and current.get("front") == settings_pid:
                return current
            current_pid = current.get("app", 0)
            if not current_pid or current.get("settings", 0) not in (0, settings_pid):
                return None
            try:
                current_nodes = self.view_waiting_for_windows(
                    current_pid, "the reopened Dev permission-window AX snapshot"
                )
            except RuntimeError:
                if pid_exists(current_pid):
                    raise
                return None
            if self.name + " 首次设置" in window_names(current_nodes):
                return None
            if (existing_input_target(current_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending) or
                    ordinary_permission_guide_complete(
                        current_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
                    )):
                return current
            return None

        post_input = self.wait_auth_or("ordinary-general input-monitoring continuation", input_granted_or_reopened)
        if post_input["app"] == pid and post_input.get("restart"):
            restart = self.probe("restart")
            self.record("system_restart", {**restart, "flow": "ordinary-general"})

            def reopened_general():
                current = self.probe()
                if current.get("app", 0) in (0, pid):
                    return None
                if current.get("settings", 0) not in (0, settings_pid):
                    return None
                current_nodes = self.view_waiting_for_windows(
                    current["app"], "the reopened Dev General-page AX snapshot"
                )
                if self.name + " 首次设置" in window_names(current_nodes):
                    return None
                if (existing_input_target(current_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending) or
                        ordinary_permission_guide_complete(
                            current_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
                        )):
                    return current
                return None

            post_input = wait_for(reopened_general, "ordinary General permission guide after OS reopen")

        self.record("GENERAL_INPUT_PASS", {
            "scope": "ordinary-general-input-monitoring",
            "initial_pid": pid, "final_pid": post_input["app"],
            "settings_pid_before": settings_pid, "settings_pid_after": post_input["settings"],
            "settings_reopened_app": post_input["app"] != pid,
            "onboarding_window_present": False,
        })
        completed_nodes = self.view(post_input["app"])
        if ordinary_permission_guide_complete(
            completed_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
        ):
            self.record_general_permissions_pass(
                post_input, completed_nodes, scope="ordinary-general-input-monitoring",
            )
            return post_input

        if not existing_input_target(
            completed_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
        ):
            raise RuntimeError("input grant did not restore the ordinary General guide with Device Control as the only remaining list permission")
        final = self.run_existing_input(allow_bluetooth_pending=allow_bluetooth_pending)
        final_nodes = self.view(final["app"])
        permission_counts = ordinary_permission_counts(final_nodes, self.name)
        if (permission_counts is None or
            not ordinary_permission_guide_complete(
                final_nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
            ) or
            (not allow_bluetooth_pending and
             permission_counts["granted_row_count"] != permission_counts["row_count"])):
            raise RuntimeError("ordinary permission completion did not retain the required rows as granted")
        self.record_general_permissions_pass(
            final, final_nodes, scope="ordinary-general-input-and-device-control",
        )
        return final

    def record_general_permissions_pass(self, state, nodes, *, scope):
        counts = ordinary_permission_counts(nodes, self.name)
        if counts is None:
            raise RuntimeError("the General permission rows no longer agree with the permission card")
        rows = counts["rows"]
        if getattr(self, "two_list_permissions_only", False):
            if rows.get("input") != "granted" or rows.get("device_control") != "granted":
                raise RuntimeError("the two list permissions are not both granted")
            self.record("GENERAL_TWO_LIST_PASS", {
                "scope": scope, "verified_permission_scope": ["input", "device_control"],
                "app": state["app"], "settings": state["settings"],
                "input_granted": True, "device_control_granted": True,
                "bluetooth_status": rows.get("bluetooth", "not_present"),
                "permission_rows": counts, "full_first_use_verified": False,
            })
            return
        if counts["granted_row_count"] != counts["row_count"]:
            raise RuntimeError("ordinary permission completion did not retain all actual rows as granted")
        self.record("GENERAL_PERMISSIONS_PASS", {
            "scope": scope, "app": state["app"], "settings": state["settings"],
            "input_granted": rows.get("input") == "granted",
            "device_control_granted": rows.get("device_control") == "granted",
            "bluetooth_present": "bluetooth" in rows,
            "bluetooth_granted": rows.get("bluetooth") == "granted",
            "permission_rows": counts, "full_first_use_verified": False,
        })

    def run_cancel_reentry(self):
        state = self.preflight_no_existing_sheet("ordinary-cancel-reentry")
        if not state.get("app"):
            subprocess.run([
                "/usr/bin/open", "--stderr", str(self.output / "app-stderr.log"),
                "-a", str(self.bundle), "--args", "--show-settings",
            ], check=True)
            state = wait_for(
                lambda: (current if (current := self.probe()).get("app") else None),
                "the frozen Dev process",
            )
        pid = state.get("app", 0)
        if not isinstance(pid, int) or isinstance(pid, bool) or pid <= 0:
            raise RuntimeError("--cancel-reentry could not identify the frozen Dev process")

        nodes = wait_for(
            lambda: (current if ordinary_general_startup_ready(
                pid, current := self.view_if_windows_available(pid), self.name
            ) else None),
            "the frozen Dev main window and visible General button",
        )
        if not ordinary_general_surface_ready(nodes, self.name):
            self.press(pid, "通用")
            nodes = wait_for(
                lambda: (current if ordinary_general_surface_ready(
                    current := self.view_if_windows_available(pid), self.name
                ) else None),
                "the Dev General permission rows",
            )
        if not ordinary_permission_guide_card_present(nodes, self.name):
            self.press(pid, GENERAL_INPUT_MANAGE)

        def readable_baseline():
            current = self.view_if_windows_available(pid)
            counts = cancel_reentry_permission_counts(current, self.name)
            return (current, counts) if counts is not None else None

        _, baseline = wait_for(
            readable_baseline,
            "Input Monitoring pending with every non-target row readable",
        )
        self.record("cancel_reentry_preflight", {
            "app": pid, "settings": state.get("settings"), "front": state.get("front"),
            "flow": "ordinary-General", "target_permission": "input-monitoring",
            "permission_rows_before": baseline,
            "input_monitoring_pending": True,
            "non_target_rows_are_baseline_driven": True,
            "credential_input": "never automated",
        })

        press = self.press_permission_row(pid, INPUT_ROW_TITLE)
        self.record("permission_row_action", {
            "app": pid, "permission": "input-monitoring",
            "press_started_at": press["ax_press_started_at"],
            "press_completed_at": press["ax_press_completed_at"],
            "source": "ordinary General permission card",
        })
        ready = self.wait_auth_or(
            "input-monitoring Settings pane",
            lambda current: current if settings_page_opened(current, pid, INPUT_PAGES) else None,
        )
        settings_pid = ready["settings"]
        self.record_window_latencies("input-monitoring", press)
        ready, focus = self.prepare_settings_for_drag(
            ready, app_pid=pid, pages=INPUT_PAGES, stage="ordinary-cancel-reentry-input-monitoring",
        )
        self.record("input_ready", {**ready, **focus, "flow": "ordinary-cancel-reentry"})
        self.record("native_drop", self.drag(
            "drag-input", permission="input-monitoring", app_pid=pid,
            settings_pid=settings_pid, pages=INPUT_PAGES,
        ))

        def password_prompt_visible():
            current = self.probe()
            if current.get("app") != pid or current.get("settings") != settings_pid:
                raise RuntimeError("Dev or Settings identity changed before the Cancel action")
            if current.get("page") not in INPUT_PAGES:
                raise RuntimeError("Input Monitoring Settings page changed before the Cancel action")
            if current.get("restart"):
                raise RuntimeError("an unexpected restart prompt appeared; no authentication action was sent")
            if current.get("password"):
                return current
            if current.get("system_sheet"):
                raise RuntimeError("an unexpected non-password system sheet appeared; no authentication action was sent")
            return None

        prompt = wait_for(password_prompt_visible, "this Dev Input Monitoring password prompt", self.auth_timeout)
        self.record("cancel_password_prompt_matched", {
            "app": pid, "settings": settings_pid, "page": prompt["page"],
            "password_detected_by": "fresh PermissionAcceptance probe",
            "credential_input": "never automated",
        })
        try:
            cancel_result = capture(
                self.driver, "cancel-auth", self.bundle, str(pid), str(settings_pid)
            )
        except RuntimeError as error:
            self.record("system_auth_cancel_failed", {
                "app": pid, "settings": settings_pid,
                "reason": concise_tool_error(error), "text_entered": False,
            })
            raise
        if not verified_cancel_result(cancel_result, pid, settings_pid, self.bundle):
            self.record("system_auth_cancel_failed", {
                "app": pid, "settings": settings_pid,
                "reason": "Cancel driver result did not prove the exact attached password sheet and unique Cancel action",
                "result": cancel_result,
                "text_entered": False,
            })
            raise RuntimeError("the exact Settings password dialog Cancel action was not proven")
        self.record("system_auth_cancelled", cancel_result)

        def cancelled_surface_ready():
            current = self.probe()
            if current.get("app") != pid or current.get("settings") != settings_pid:
                raise RuntimeError("Dev or Settings identity changed after authentication Cancel")
            if current.get("restart"):
                raise RuntimeError("an unexpected restart prompt appeared after authentication Cancel")
            if current.get("page") not in INPUT_PAGES or current.get("front") != settings_pid:
                return None
            if current.get("password") or current.get("system_sheet"):
                return None
            current_nodes = self.view_waiting_for_windows(
                pid, "Dev permission rows after authentication Cancel"
            )
            current_rows = ordinary_permission_counts(current_nodes, self.name)
            return (current, current_nodes, current_rows) if current_rows is not None else None

        cancelled_state, cancelled_nodes, cancelled_rows = wait_for(
            cancelled_surface_ready,
            "the closed password dialog and actual General/permission-card row readback",
        )
        unchanged_after_cancel = permission_rows_unchanged(baseline, cancelled_rows)
        self.record("cancel_permission_readback", {
            "app": pid, "settings": settings_pid,
            "front": cancelled_state["front"], "page": cancelled_state["page"],
            "permission_rows_before": baseline,
            "permission_rows_after_cancel": cancelled_rows,
            "input_monitoring_still_pending": cancelled_rows["rows"].get("input") == "pending",
            "non_target_rows_unchanged": non_target_permission_rows_unchanged(baseline, cancelled_rows),
            "all_permission_rows_unchanged": unchanged_after_cancel,
            "grant_success_expected": False,
        })
        if not unchanged_after_cancel:
            raise RuntimeError("Cancel did not preserve the target and every non-target permission row")

        self.press(pid, "关闭系统权限面板")

        def card_closed():
            current_nodes = self.view_waiting_for_windows(pid, "Dev AX windows after closing the permission card")
            return not any(
                item.get("role") == "AXWindow" and item.get("title") == self.name + " 系统权限"
                for item in current_nodes
            )

        wait_for(card_closed, "the permission card to close after Cancel")
        self.record("cancel_permission_card_closed", {
            "app": pid, "input_monitoring_state": cancelled_rows["rows"]["input"],
            "permission_rows": cancelled_rows,
        })
        self.press(pid, GENERAL_INPUT_MANAGE)

        def card_reentered():
            current_nodes = self.view_waiting_for_windows(pid, "Dev AX windows after reopening the permission card")
            current_rows = ordinary_permission_counts(current_nodes, self.name)
            if current_rows is None or "invalid" in current_rows["rows"].values():
                return None
            return current_nodes, current_rows

        reentered_nodes, reentered_rows = wait_for(
            card_reentered,
            "the reopened permission card with every actual row readable",
        )
        unchanged_after_reentry = permission_rows_unchanged(baseline, reentered_rows)
        self.record("cancel_permission_card_reentered", {
            "app": pid,
            "permission_rows_before": baseline,
            "permission_rows_after_reentry": reentered_rows,
            "input_monitoring_still_pending": reentered_rows["rows"].get("input") == "pending",
            "non_target_rows_unchanged": non_target_permission_rows_unchanged(baseline, reentered_rows),
            "all_permission_rows_unchanged": unchanged_after_reentry,
        })
        if not unchanged_after_reentry:
            raise RuntimeError("permission-card re-entry changed the target or a non-target permission row")
        self.record("CANCEL_REENTRY_PASS", {
            "scope": "ordinary-cancel-reentry",
            "app": pid, "settings": settings_pid,
            "auth_dialog_cancelled": True,
            "permission_card_closed_and_reopened": True,
            "permission_rows_before": baseline,
            "permission_rows_after_cancel": cancelled_rows,
            "permission_rows_after_reentry": reentered_rows,
            "input_monitoring_granted": False,
            "business_grant_verified": False,
            "non_target_rows_preserved": True,
        })

    def drag(self, operation, *, permission=None, app_pid=None, settings_pid=None, pages=()):
        if permission is not None:
            self.wait_for_draggable_geometry(permission, app_pid, settings_pid, pages)
        cutoff = time.monotonic() + 4
        while True:
            try:
                return self.probe(operation)
            except RuntimeError as error:
                message = error.args[0]
                pending = isinstance(message, dict) and message.get("error") in (
                    "the card or list geometry is unavailable",
                    "the drag geometry does not match the current windows",
                )
                if not pending or time.monotonic() >= cutoff:
                    raise
                time.sleep(0.18)

    def wait_for_draggable_geometry(self, permission, app_pid, settings_pid, pages):
        started_at = time.time()
        deadline = time.monotonic() + DRAG_GEOMETRY_TIMEOUT
        last_observation = None
        last_probe = None
        while time.monotonic() < deadline:
            observation = self.latest_window_observation()
            sample = observation.get("state") if isinstance(observation, dict) else None
            if isinstance(sample, dict):
                last_observation = observation
                if draggable_window_geometry_ready(sample, app_pid, settings_pid):
                    probe_started_at = time.time()
                    try:
                        current = self.probe()
                    except RuntimeError:
                        self.record("drag_geometry_blocked", {
                            "permission": permission, "app_pid": app_pid,
                            "settings_pid": settings_pid, "wait_started_at": started_at,
                            "blocked_at": time.time(), "reason": "fresh_probe_failed",
                            "window_sample_completed_at": observation.get("sample_completed_at"),
                            "geometry": drag_geometry_summary(sample),
                        })
                        raise
                    probe_completed_at = time.time()
                    last_probe = current
                    if current.get("password") or current.get("system_sheet") or current.get("restart"):
                        self.record("drag_geometry_blocked", {
                            "permission": permission, "app_pid": app_pid,
                            "settings_pid": settings_pid, "wait_started_at": started_at,
                            "blocked_at": probe_completed_at,
                            "reason": "authentication_or_restart_ui_visible",
                            "window_sample_completed_at": observation.get("sample_completed_at"),
                            "geometry": drag_geometry_summary(sample),
                            "fresh_probe": drag_page_probe_summary(current),
                            "fresh_probe_started_at": probe_started_at,
                            "fresh_probe_completed_at": probe_completed_at,
                        })
                        raise RuntimeError("authentication or restart UI appeared before the permission drag")
                    if current.get("app") not in (0, app_pid) or current.get("settings") not in (0, settings_pid):
                        self.record("drag_geometry_blocked", {
                            "permission": permission, "app_pid": app_pid,
                            "settings_pid": settings_pid, "wait_started_at": started_at,
                            "blocked_at": probe_completed_at, "reason": "app_or_settings_identity_changed",
                            "window_sample_completed_at": observation.get("sample_completed_at"),
                            "geometry": drag_geometry_summary(sample),
                            "fresh_probe": drag_page_probe_summary(current),
                            "fresh_probe_started_at": probe_started_at,
                            "fresh_probe_completed_at": probe_completed_at,
                        })
                        raise RuntimeError("the app or Settings identity changed before the permission drag")
                    if drag_page_probe_ready(current, app_pid, settings_pid, pages):
                        self.record("drag_geometry_ready", {
                            "permission": permission, "app_pid": app_pid,
                            "settings_pid": settings_pid,
                            "window_sample_completed_at": observation.get("sample_completed_at"),
                            "geometry": drag_geometry_summary(sample),
                            "fresh_probe": drag_page_probe_summary(current),
                            "fresh_probe_started_at": probe_started_at,
                            "fresh_probe_completed_at": probe_completed_at,
                            "wait_started_at": started_at, "ready_at": time.time(),
                        })
                        return current
            time.sleep(0.1)

        last_sample = last_observation.get("state", {}) if last_observation else {}
        final_probe_started_at = time.time()
        final_probe_failed = False
        try:
            last_probe = self.probe()
        except RuntimeError:
            final_probe_failed = True
        final_probe_completed_at = time.time()
        self.record("drag_geometry_blocked", {
            "permission": permission, "app_pid": app_pid, "settings_pid": settings_pid,
            "wait_started_at": started_at, "blocked_at": time.time(),
            "timeout_seconds": DRAG_GEOMETRY_TIMEOUT,
            "last_window_sample_completed_at": (
                last_observation.get("sample_completed_at") if last_observation else None
            ),
            "last_geometry": drag_geometry_summary(last_sample),
            "last_fresh_probe": drag_page_probe_summary(last_probe) if last_probe else None,
            "last_fresh_probe_failed": final_probe_failed,
            "last_fresh_probe_started_at": final_probe_started_at,
            "last_fresh_probe_completed_at": final_probe_completed_at,
        })
        raise RuntimeError("timed out waiting for the attached permission card and expected Settings page before drag")

    def drag_regressions(self, app_pid=None, settings_pid=None, page=None):
        for operation in ("drag-cancel-escape", "drag-wrong-target"):
            try:
                if app_pid is not None and settings_pid is not None and page is not None:
                    result = self.drag(operation, permission="drag-regression", app_pid=app_pid,
                                       settings_pid=settings_pid, pages=(page,))
                else:
                    result = self.drag(operation)
            except RuntimeError as error:
                self.record("native_drag_regression_failed", {"case": operation, "error": error.args[0]})
                raise
            snapshot_before = result.get("permission_state_before")
            snapshot_after = result.get("permission_state_after")
            snapshot_matches = (
                isinstance(snapshot_before, dict) and isinstance(snapshot_after, dict) and
                snapshot_before == snapshot_after and
                isinstance(snapshot_before.get("summary"), list) and bool(snapshot_before["summary"]) and
                isinstance(snapshot_before.get("dev_controls"), list) and bool(snapshot_before["dev_controls"])
            )
            if (result.get("permission_state_unchanged") is not True or
                not snapshot_matches or
                result.get("drag_started_overlay_observed") is not True or
                result.get("drag_ended_overlay_removed") is not True or
                result.get("icon_hit_test") is not True or
                (operation == "drag-cancel-escape" and
                 result.get("permission_target_reached_while_overlay_visible") is not True) or
                (operation == "drag-wrong-target" and
                 (result.get("target_kind") != "unpermissioned-settings-title" or
                  result.get("target_role") != "AXStaticText"))):
                self.record("native_drag_regression_failed", {"case": operation, "result": result})
                raise RuntimeError(f"{operation} did not prove an icon-origin drag, observed start/end, and unchanged permission state")
            self.record("native_drag_regression", {"case": operation, **result})

    def run_drag_regressions_only(self):
        state = self.preflight_no_existing_sheet("drag-regressions-only")
        if (not state["app"] or not state["settings"] or state["front"] != state["settings"] or
            state["page"] not in INPUT_PAGES + CONTROL_PAGES):
            raise RuntimeError("--drag-regressions-only requires an already visible Dev permission card and frontmost Settings permission page")
        def visible_card_probe():
            current = self.probe()
            if (current["app"] != state["app"] or current["settings"] != state["settings"] or
                current["front"] != state["settings"] or current["page"] != state["page"]):
                return None
            return current if current.get("permission_card_visible") else None

        ready = wait_for(visible_card_probe, "Dev permission CGWindow before drag regressions")
        if not drag_regressions_snapshot_ready(ready):
            raise RuntimeError("--drag-regressions-only requires a readable permission snapshot from the visible Dev card")
        self.record("drag_regressions_preflight", {
            "app": state["app"], "settings": state["settings"], "front": state["front"],
            "page": state["page"], "opened_app_or_settings": False,
            "permission_change_requested": False,
            "permission_card_visible": ready["permission_card_visible"],
            "permission_summary_before": ready["permission_summary"]
        })
        self.drag_regressions(state["app"], state["settings"], state["page"])
        final = self.probe()
        if (final["app"] != state["app"] or final["settings"] != state["settings"] or
            final["front"] != state["settings"] or final["page"] != state["page"]):
            raise RuntimeError("Dev or Settings identity changed during drag-only regression")
        self.record("DRAG_REGRESSIONS_PASS", {
            "app": final["app"], "settings": final["settings"], "page": final["page"],
            "permission_state_change_requested": False
        })

    def record(self, stage, values):
        entry = {"stage": stage, "timestamp": time.time(), **values}
        with (self.output / "run.ndjson").open("a") as log:
            log.write(json.dumps(entry, ensure_ascii=False) + "\n")
        print(json.dumps(entry, ensure_ascii=False), flush=True)

    def view(self, pid):
        result = capture(self.ax, str(pid), "snapshot")
        if result.get("pid") != pid or not result.get("ax_trusted"):
            raise RuntimeError("the AX snapshot is not from the verification app")
        return result["nodes"]

    def view_if_windows_available(self, pid):
        try:
            nodes = self.view(pid)
        except RuntimeError as error:
            if is_transient_ax_windows_error(error, pid) and pid_exists(pid):
                return None
            raise
        return nodes if nodes else None

    def view_waiting_for_windows(self, pid, description):
        """Retry only this PID's launch-time AXCannotComplete while its process remains alive."""
        return wait_for(lambda: self.view_if_windows_available(pid), description)

    def press(self, pid, label):
        result = capture(self.ax, str(pid), "press", "AXButton", label)
        self.record("app_button", {
            "app": pid, "action": label, "result": result["status"],
            "ax_press_started_at": result["ax_press_started_at"],
            "ax_press_completed_at": result["ax_press_completed_at"],
            "ax_press_duration_ms": result["ax_press_duration_ms"],
            "scope": "exact_role_and_label"
        })
        return result

    def press_permission_row(self, pid, row_title):
        panel_title = self.name + " 系统权限"
        before = self.latest_window_observation()
        try:
            result = capture(self.ax, str(pid), "press-row", panel_title, row_title, PERMISSION_ACTION_LABEL)
        except RuntimeError as error:
            self.record("permission_row_target_blocked", {
                "app": pid, "panel_title": panel_title, "row_title": row_title,
                "scope_error": error.args[0], "title_entry_recorded_separately": True,
                "action": "no AXPress performed"
            })
            raise
        self.record("app_button", {
            "app": pid, "panel_title": panel_title, "row_title": row_title,
            "action": PERMISSION_ACTION_LABEL, "result": result["status"],
            "row_anchor_bounds": result["row_anchor_bounds"], "action_bounds": result["action_bounds"],
            "ax_press_started_at": result["ax_press_started_at"],
            "ax_press_completed_at": result["ax_press_completed_at"],
            "ax_press_duration_ms": result["ax_press_duration_ms"],
            "window_state_before_press": before,
            "scope": "permissions_panel_and_row"
        })
        return result

    def ensure_bluetooth_permission(self, pid, *, scope):
        actor_requested = getattr(self, "bluetooth_allow_actor", "runner")
        initial = wait_for(
            lambda: bluetooth_permission_target(self.view(pid), self.name, scope),
            "the exact Dev permission rows, including any Bluetooth row",
        )
        if initial["state"] == "not_present":
            self.record("bluetooth_permission_readback", {
                "scope": scope, "app": pid, "permission": "bluetooth",
                "status": "not_applicable", "authorization_actor": "not_applicable",
                "action": "none", "permission_rows": initial["counts"],
                "source": "same_app_accessibility_row_snapshot",
            })
            return initial
        if initial["state"] == "granted":
            actor = "user" if actor_requested == "user" else "preexisting_or_external"
            self.record("bluetooth_permission_readback", {
                "scope": scope, "app": pid, "permission": "bluetooth",
                "status": "granted_before_run", "authorization_actor": actor,
                "actor_source": "operator_declaration" if actor == "user" else "not_observed_by_this_run",
                "action": "readback_only", "permission_rows": initial["counts"],
                "source": "same_app_accessibility_row_snapshot",
            })
            return initial
        if initial["state"] != "pending" or not initial.get("row_title"):
            raise RuntimeError("the exact Dev Bluetooth row is not in a readable pending or granted state")

        existing_request = capture(self.driver, "inspect-bluetooth-request", self.bundle, str(pid))
        if existing_request.get("status") == "ready":
            press = None
        elif existing_request.get("status") == "absent":
            press = self.press_permission_row(pid, initial["row_title"])
        else:
            raise RuntimeError("an unrelated or ambiguous system request is visible; no Allow button was pressed")
        request_event = {
            "scope": scope, "app": pid, "permission": "bluetooth",
            "row_title": initial["row_title"],
            "authorization_actor_requested": actor_requested,
            "permission_state_before": initial["state"],
            "existing_system_request": press is None,
        }
        if press is not None:
            request_event.update({
                "row_press_started_at": press["ax_press_started_at"],
                "row_press_completed_at": press["ax_press_completed_at"],
            })
        self.record("bluetooth_permission_request", request_event)
        outcome = self.wait_for_bluetooth_state(
            pid, scope, timeout=self.auth_timeout, request_expected=True,
        )
        if outcome["kind"] == "granted":
            actor = "user" if actor_requested == "user" else "external_before_runner"
            self.record("bluetooth_permission_readback", {
                "scope": scope, "app": pid, "permission": "bluetooth",
                "status": "granted", "authorization_actor": actor,
                "actor_source": "user_mode_and_state_transition" if actor == "user" else "grant_seen_before_runner_action",
                "action": "manual_allow_observed" if actor == "user" else "no_runner_action",
                "system_request": outcome.get("request"), "permission_rows": outcome["target"]["counts"],
                "source": "same_app_accessibility_row_snapshot",
            })
            return outcome["target"]

        request = outcome["request"]
        self.record("bluetooth_system_request_matched", {
            "scope": scope, "app": pid, "permission": "bluetooth", **request,
        })
        if actor_requested == "runner":
            action = capture(self.driver, "allow-bluetooth", self.bundle, str(pid))
            if (action.get("performed") != "AXPress Allow" or
                action.get("authorization_actor") != "acceptance_runner" or
                action.get("requested_app") != self.name or
                action.get("target_bundle_id") != self.identifier or
                action.get("target_app_pid") != pid or
                action.get("target_bundle_path") != str(self.bundle) or
                action.get("system_process_trusted") is not True or
                action.get("system_bundle_id") != BLUETOOTH_REQUEST_BUNDLE or
                action.get("system_bundle_path") != BLUETOOTH_REQUEST_PATH or
                not bluetooth_prompt_matches([action.get("matched_prompt_line")], self.name) or
                action.get("exact_prompt_line_matched") is not True or
                action.get("requested_app_name_matched") is not True or
                action.get("allow_button_count") != 1 or
                action.get("button_label") != "Allow"):
                raise RuntimeError("the native Bluetooth Allow action did not prove the exact target and AX button")
            self.record("bluetooth_system_allow_action", {"scope": scope, **action})
        else:
            print("请只在已核对的 macOS 蓝牙授权窗口中点击 Allow / 允许；验收脚本不会代点。", file=sys.stderr, flush=True)

        final_timeout = self.auth_timeout if actor_requested == "user" else 12
        final = self.wait_for_bluetooth_state(pid, scope, timeout=final_timeout)
        if final["kind"] != "granted":
            raise RuntimeError("the same Dev permission row did not read Bluetooth as authorized after Allow")
        final = final["target"]
        self.record("bluetooth_permission_readback", {
            "scope": scope, "app": pid, "permission": "bluetooth",
            "status": "granted", "authorization_actor": "acceptance_runner" if actor_requested == "runner" else "user",
            "actor_source": "native_ax_press" if actor_requested == "runner" else "operator_selected_manual_mode",
            "action": "AXPress Allow" if actor_requested == "runner" else "manual_allow_observed",
            "system_request": request, "permission_rows": final["counts"],
            "source": "same_app_accessibility_row_snapshot",
        })
        return final

    def run_bluetooth_only(self):
        state = self.preflight_no_existing_sheet("bluetooth-only")
        pid = state["app"]
        if not pid:
            raise RuntimeError("--bluetooth-only could not find the canonical Dev process")
        nodes = self.view(pid)
        scope = "full-first-use" if self.name + " 首次设置" in window_names(nodes) else "ordinary-general-full"
        target = bluetooth_permission_target(nodes, self.name, scope)
        if target is None:
            raise RuntimeError("--bluetooth-only requires the exact Dev permission card already open")
        if target["state"] == "not_present":
            self.record("BLUETOOTH_ONLY_NOT_APPLICABLE", {
                "app": pid, "scope": scope, "permission_rows": target["counts"],
                "action_performed": False,
            })
            raise RuntimeError("the current Dev device profile has no Bluetooth permission row")
        final = self.ensure_bluetooth_permission(pid, scope=scope)
        if final is None or final["state"] != "granted":
            raise RuntimeError("--bluetooth-only did not read back the same Dev Bluetooth row as authorized")
        self.record("BLUETOOTH_ONLY_PASS", {
            "app": pid, "bundle_id": self.identifier, "bundle": str(self.bundle),
            "executable_sha256": self.sha, "scope": scope,
            "authorization_actor_requested": getattr(self, "bluetooth_allow_actor", "runner"),
            "permission_rows": final["counts"], "same_app_authorized_row_verified": True,
            "other_permissions_reset": False,
        })

    def wait_for_bluetooth_state(self, pid, scope, *, timeout, request_expected=False):
        actor_requested = getattr(self, "bluetooth_allow_actor", "runner")
        deadline = None if timeout == 0 else time.monotonic() + timeout
        while deadline is None or time.monotonic() < deadline:
            target = bluetooth_permission_target(self.view(pid), self.name, scope)
            if target is None or target["state"] not in ("pending", "granted"):
                raise RuntimeError("the Dev Bluetooth permission row changed identity or became unreadable")
            state = self.probe()
            if state.get("app") != pid or state.get("bundle_id") != self.identifier:
                raise RuntimeError("the Bluetooth authorization request changed the verified Dev process")
            request = capture(self.driver, "inspect-bluetooth-request", self.bundle, str(pid))
            if request.get("status") == "ready" and target["state"] == "pending" and request_expected:
                if (request.get("exact_prompt_line_matched") is not True or
                    not bluetooth_prompt_matches([request.get("matched_prompt_line")], self.name)):
                    raise RuntimeError("the system request title is not the exact Bluetooth prompt for this app")
                return {"kind": "request", "request": request, "target": target}
            if request.get("status") == "absent" and target["state"] == "granted":
                return {"kind": "granted", "target": target}
            if request.get("status") not in ("absent", "ready"):
                raise RuntimeError("an unrelated or ambiguous system request is visible; no Allow button was pressed")
            time.sleep(0.18)
        self.record("BLUETOOTH_AUTH_TIMEOUT", {
            "scope": scope, "app": pid, "authorization_actor_requested": actor_requested,
            "permission_state": "pending", "request_expected": request_expected,
            "system_action_performed_by_runner": False,
        })
        raise RuntimeError("the exact Dev Bluetooth request or authorized row did not appear")

    def window_observations(self):
        path = self.output / "windows.ndjson"
        if not path.is_file():
            return []
        observations = []
        for line in path.read_text().splitlines():
            try:
                observations.append(json.loads(line))
            except json.JSONDecodeError:
                continue
        return observations

    def latest_window_observation(self):
        observations = self.window_observations()
        return observations[-1] if observations else None

    def record_window_latencies(self, permission, press_result):
        started = press_result["ax_press_started_at"]
        completed = press_result["ax_press_completed_at"]
        observations = self.window_observations()
        before = next((entry for entry in reversed(observations)
                       if entry.get("sample_completed_at", 0) <= started), None)
        after = [entry for entry in observations if entry.get("sample_completed_at", 0) >= completed]
        checks = (("card_attached", lambda state: state.get("attachment", {}).get("attached") is True),)
        for stage, matches in checks:
            before_state = before.get("state", {}) if before else {}
            preexisting = matches(before_state)
            sample = None if preexisting else next((entry for entry in after if matches(entry.get("state", {}))), None)
            if preexisting or sample:
                observed = started if preexisting else sample["sample_completed_at"]
                state = before_state if preexisting else sample.get("state", {})
                self.record(stage, {
                    "permission": permission, "source": "CGWindowList",
                    "preexisting_before_press": preexisting,
                    "observed_at": observed,
                    "sample_interval_ms": 0 if preexisting else
                        (sample["sample_completed_at"] - sample.get("sample_started_at", observed)) * 1_000,
                    "ax_press_started_at": started, "ax_press_completed_at": completed,
                    "press_to_observed_ms": None if preexisting else (observed - completed) * 1_000,
                    "settings_pid": state.get("settings_pid"),
                    "frontmost_pid": state.get("frontmost_pid"),
                    "attachment": state.get("attachment")
                })

    def permission_row_available(self, pid, row_title):
        nodes = self.view(pid)
        return (self.name + " 系统权限" in window_names(nodes) and
                has_button(nodes, row_title) and has_button(nodes, PERMISSION_ACTION_LABEL))

    def run_existing_input(self, *, allow_bluetooth_pending=False):
        state = self.preflight_no_existing_sheet("existing-input-device-control")
        pid = state["app"]
        if not pid:
            subprocess.run(["/usr/bin/open", "--stderr", str(self.output / "app-stderr.log"), self.bundle], check=True)
            def visible_dev():
                current = self.probe()
                return current if current["app"] else None
            state = wait_for(visible_dev, "existing Dev process")
            pid = state["app"]
        if not pid:
            raise RuntimeError("--existing-input could not find the canonical Dev process")
        bluetooth_requirement = "Bluetooth may remain pending" if allow_bluetooth_pending else "any Bluetooth row already granted"
        nodes = wait_for(
            lambda: self.existing_input_panel(pid, allow_bluetooth_pending=allow_bluetooth_pending),
            f"Dev permission guide with Input Monitoring granted, Device Control pending, and {bluetooth_requirement}",
        )
        full_first_use = self.name + " 首次设置" in window_names(nodes)
        control_title = existing_input_target(
            nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
        )
        if control_title is None:
            if full_first_use:
                raise RuntimeError("--existing-input requires every recognized first-use permission row except Device Control to be granted, with the summary matching the actual row count")
            raise RuntimeError("--existing-input requires the ordinary General page and permission card to show Input Monitoring granted, Device Control pending, and " + bluetooth_requirement)
        self.record("existing_input_preflight", {
            "app": pid, "settings": state["settings"], "front": state["front"],
            "page_before": state["page"], "input_already_granted": True,
            "granted_summary": permission_progress_summary(nodes, self.name),
            "permission_rows": ordinary_permission_counts(nodes, self.name) if not full_first_use else None,
            "target_row": control_title,
            "mode": "device-control segment only",
            "flow": "full-first-use" if full_first_use else "ordinary-settings",
        })

        press = self.press_permission_row(pid, control_title)
        self.record("permission_row_action", {
            "app": pid, "permission": "device-control", "press_started_at": press["ax_press_started_at"],
            "press_completed_at": press["ax_press_completed_at"], "source": "right-side 授权…"
        })
        ready = self.wait_auth_or(
            "device-control Settings pane",
            lambda current: current if settings_page_opened(current, pid, CONTROL_PAGES) else None,
        )
        settings_pid = ready["settings"]
        self.record_window_latencies("device-control", press)
        ready, focus = self.prepare_settings_for_drag(
            ready, app_pid=pid, pages=CONTROL_PAGES, stage="existing-input-device-control",
        )
        self.record("settings_page_ready", {
            "permission": "device-control", "app": ready["app"], "settings": settings_pid,
            "front": ready["front"], "page": ready["page"], "observed_at": time.time(),
            **focus,
        })
        self.record("native_drop", self.drag(
            "drag-control", permission="device-control", app_pid=pid,
            settings_pid=settings_pid, pages=CONTROL_PAGES,
        ))

        restart_observed = False
        restart_clicked = False

        def granted(state):
            return self.existing_input_segment_granted(
                state, pid, settings_pid, restart_observed, full_first_use,
                allow_bluetooth_pending=allow_bluetooth_pending,
            )

        def confirm_or_finish(state):
            nonlocal restart_observed, restart_clicked
            if result := granted(state):
                return result
            if (state["restart"] and state["front"] == settings_pid and not restart_clicked):
                restart_observed = True
                restart = self.probe("restart")
                restart_clicked = True
                self.record("system_restart", restart)
                return None
            if state["restart"] and state["front"] == settings_pid:
                restart_observed = True
            if state["front"] != settings_pid or state["page"] not in CONTROL_PAGES:
                return None
            try:
                confirmation = self.probe("confirm-control")
            except RuntimeError as error:
                detail = error.args[0]
                if isinstance(detail, dict) and detail.get("error") in (
                    "the verification app and expected Settings front window are required",
                    "an unobstructed Dev device-control row is required",
                ):
                    return None
                raise
            if confirmation.get("performed") or confirmation.get("confirmed"):
                self.record("system_control_confirmed", confirmation)
            return None

        state = self.wait_auth_or("device-control", confirm_or_finish)
        final_nodes = self.view(state["app"]) if not full_first_use else []
        final_rows = ordinary_permission_counts(final_nodes, self.name) if not full_first_use else None
        self.record("SEGMENT_PASS", {
            "scope": "existing-input-device-control",
            "initial_pid": pid, "final_pid": state["app"], "settings_pid": settings_pid,
            "input_preserved": True, "input_granted_before_run": True,
            "device_control_granted": True,
            "flow": "full-first-use" if full_first_use else "ordinary-settings",
            "authorized_panel_visible": not full_first_use,
            "first_use_continue_visible": full_first_use,
            "settings_exited": state["settings"] == 0,
            "final_frontmost_pid": state["front"], "settings_pid_after": state["settings"],
            "final_granted_summary": (
                "全部已授权" if full_first_use else permission_progress_summary(
                    final_nodes, self.name
                )
            ),
            "permission_rows": final_rows,
            "bluetooth_status": final_rows["rows"].get("bluetooth", "not_present") if final_rows else None,
            "verified_permission_scope": (
                ["input", "device_control"] if allow_bluetooth_pending else None
            ),
            "restart_prompt_observed": restart_observed,
            "same_process_authorized_ui_verified": not restart_observed,
            "verified_after_original_pid_exit": restart_observed,
            "full_first_use_verified": False
        })
        return state

    def existing_input_panel(self, pid, *, allow_bluetooth_pending=False):
        nodes = self.view(pid)
        return nodes if existing_input_target(
            nodes, self.name, allow_bluetooth_pending=allow_bluetooth_pending,
        ) else None

    def wait_for_restart_prompt_focus(self, initial, app_pid, settings_pid):
        """Wait for stable Settings ownership without activating either app."""
        started = time.monotonic()
        deadline = started + RESTART_FOCUS_RECOVERY_SECONDS
        current = initial
        consecutive_settings_samples = 0
        observations = []
        self.record("restart_prompt_observed", {
            "app_pid": app_pid, "settings_pid": settings_pid,
            "initial_frontmost_pid": initial.get("front"),
            "restart_prompt_visible": initial.get("restart") is True,
            "restart_action_performed": False,
        })
        while True:
            summary = restart_focus_observation(current)
            observations.append(summary)
            if restart_prompt_focus_ready(current, app_pid, settings_pid):
                consecutive_settings_samples += 1
                if consecutive_settings_samples >= 2:
                    self.record("restart_prompt_focus_recovered", {
                        "app_pid": app_pid, "settings_pid": settings_pid,
                        "consecutive_settings_samples": consecutive_settings_samples,
                        "observations": observations,
                        "restart_action_performed": False,
                    })
                    return current
            else:
                consecutive_settings_samples = 0
                if (current.get("app", 0) not in (0, app_pid) and
                    current.get("settings", 0) == settings_pid and not pid_exists(app_pid)):
                    self.record("restart_completed_during_focus_wait", {
                        "previous_app_pid": app_pid, "settings_pid": settings_pid,
                        "reopened_app_pid": current.get("app"),
                        "frontmost_pid": current.get("front"),
                        "restart_action_performed_by_runner": False,
                    })
                    return current
            if time.monotonic() >= deadline:
                reason = (
                    "restart_prompt_disappeared" if not current.get("restart") else
                    "settings_did_not_regain_frontmost_ownership"
                )
                self.record("RESTART_FOCUS_INTERRUPTED", {
                    "stage": "restart_prompt_before_click",
                    "reason": reason,
                    "last_observation": summary,
                    "observations": observations,
                    "observation_count": len(observations),
                    "wait_seconds": RESTART_FOCUS_RECOVERY_SECONDS,
                    "restart_action_performed": False,
                    "resume_scope": "--resume-input-drop with this output directory after focus is resolved",
                })
                raise RuntimeError(
                    "Settings did not regain stable frontmost ownership for the system restart prompt; "
                    "no restart action was sent, and the run is resumable from its recorded input drop"
                )
            current = self.probe()
            time.sleep(0.18)

    def wait_for_resumed_settings_focus(self, initial, app_pid, settings_pid):
        """Keep the resumed page's real Settings-front assertion, allowing transient focus to settle."""
        started = time.monotonic()
        deadline = started + RESTART_FOCUS_RECOVERY_SECONDS
        current = initial
        consecutive_settings_samples = 0
        observations = []
        while True:
            summary = restart_focus_observation(current)
            observations.append(summary)
            if resumed_settings_focus_ready(current, app_pid, settings_pid):
                consecutive_settings_samples += 1
                if consecutive_settings_samples >= 2:
                    self.record("restart_resumed_focus_verified", {
                        "app_pid": app_pid, "settings_pid": settings_pid,
                        "consecutive_settings_samples": consecutive_settings_samples,
                        "observations": observations,
                        "settings_frontmost": True, "settings_above_setup": True,
                    })
                    return current
            else:
                consecutive_settings_samples = 0
            if time.monotonic() >= deadline:
                self.record("RESTART_FOCUS_INTERRUPTED", {
                    "stage": "resumed_permission_page",
                    "reason": "settings_did_not_retain_frontmost_ownership_or_cover_order",
                    "last_observation": summary,
                    "observation_count": len(observations),
                    "wait_seconds": RESTART_FOCUS_RECOVERY_SECONDS,
                    "restart_action_performed": True,
                    "resume_scope": "--resume-input-drop with this output directory after focus is resolved",
                })
                raise RuntimeError(
                    "the resumed permission page did not regain stable Settings ownership; "
                    "the run is resumable from its recorded input drop"
                )
            current = self.probe()
            time.sleep(0.18)

    def wait_auth_or(self, stage, ready):
        deadline = time.monotonic() + 12
        password_announced = False
        sheet_announced = False
        last_auth_kind = None
        last_auth_state = None
        while deadline is None or time.monotonic() < deadline:
            probe = self.probe()
            auth_state = redacted_auth_state(probe)
            if auth_state != last_auth_state:
                self.record("AUTH_STATE_CHANGED", {"permission": stage, "state": auth_state})
                last_auth_state = auth_state
            password_visible = bool(probe["password"])
            recognized_restart = bool(probe.get("restart"))
            system_sheet = bool(probe.get("system_sheet"))
            auth_kind = (
                "password" if password_visible else
                "settings-sheet" if system_sheet and not recognized_restart else
                None
            )
            ready_result = None
            if auth_kind is None:
                ready_result = ready(probe)
            if auth_kind is not None and auth_kind != last_auth_kind:
                # Each visible authentication edge gets its own configured
                # interaction window. Reopened prompts must not inherit the
                # deadline from an earlier prompt.
                deadline = None if self.auth_timeout == 0 else time.monotonic() + self.auth_timeout
                if auth_kind == "password":
                    self.record("NEEDS_PASSWORD", {"permission": stage})
                    if not password_announced:
                        print("请只在 macOS 系统窗口输入密码，脚本会自动继续。", file=sys.stderr, flush=True)
                        password_announced = True
                elif not sheet_announced:
                    self.record("SYSTEM_SHEET_WAIT", {"permission": stage})
                    print(f"等待 macOS 的 {stage} 系统确认窗口完成。", file=sys.stderr, flush=True)
                    sheet_announced = True
                else:
                    self.record("SYSTEM_SHEET_WAIT", {"permission": stage})
            elif auth_kind is None and last_auth_kind is not None and not ready_result:
                # Once the UI disappears, keep the existing 12-second window
                # for the OS feedback. A later prompt edge starts a fresh
                # configured authentication window above.
                deadline = time.monotonic() + 12

            if ready_result:
                return ready_result
            last_auth_kind = auth_kind
            time.sleep(0.18)
        self.record("AUTH_WAIT_TIMEOUT", {"permission": stage, "last_state": last_auth_state})
        raise RuntimeError(f"{stage}: expected system confirmation did not arrive")

    def run(self):
        state = self.preflight_no_existing_sheet("full-first-use")
        if state["app"]:
            raise RuntimeError("the frozen verification app is already running")
        subprocess.run(["/usr/bin/open", "--stderr", str(self.output / "app-stderr.log"), self.bundle], check=True)
        pid = wait_for(lambda: self.probe()["app"], "verification process")
        progress = self.config / "onboarding.json"
        wait_for(lambda: read_json(progress).get("started"), "first-use record")
        for step in (-1, 0, 1):
            wait_for(lambda: read_json(progress).get("step", 0) == step and
                     has_button(self.view(pid), "继续"), f"onboarding step {step}")
            self.press(pid, "继续")
        wait_for(lambda: read_json(progress).get("step") == 2 and
                 has_button(self.view(pid), "授权…"), "permission summary")
        self.press(pid, "授权…")
        wait_for(lambda: self.permission_row_available(pid, INPUT_ROW_TITLE), "input-monitoring permission row")
        self.ensure_bluetooth_permission(pid, scope="full-first-use")
        initial_permission_nodes = self.view(pid)
        if first_use_input_target(initial_permission_nodes, self.name) != INPUT_ROW_TITLE:
            summary = first_use_progress_summary(initial_permission_nodes)
            self.record("BLOCKED_PERMISSION_ROWS", {
                "scope": "full-first-use", "permission_summary": summary[2] if summary else None,
                "reason": "Input Monitoring and Device Control must be the only pending rows; optional Bluetooth must already be granted",
            })
            raise RuntimeError("full-first-use requires Input Monitoring and Device Control as the only pending permission rows; pregrant Bluetooth in this Dev before starting")
        input_press = self.press_permission_row(pid, INPUT_ROW_TITLE)
        self.record("permission_row_action", {
            "app": pid, "permission": "input-monitoring", "press_started_at": input_press["ax_press_started_at"],
            "press_completed_at": input_press["ax_press_completed_at"], "source": "right-side 授权…"
        })
        state = self.wait_auth_or(
            "input-monitoring Settings pane",
            lambda current: current if settings_page_opened(current, pid, INPUT_PAGES) else None,
        )
        settings_pid = state["settings"]
        self.record_window_latencies("input-monitoring", input_press)
        state, focus = self.prepare_settings_for_drag(
            state, app_pid=pid, pages=INPUT_PAGES, stage="full-first-use-input-monitoring",
        )
        self.record("input_ready", {**state, **focus})
        self.record("native_drop", self.drag(
            "drag-input", permission="input-monitoring", app_pid=pid,
            settings_pid=settings_pid, pages=INPUT_PAGES,
        ))

        self.finish_input_drop(pid, settings_pid)

    def finish_input_drop(self, pid, settings_pid):
        entries = [json.loads(line) for line in (self.output / "run.ndjson").read_text().splitlines()]
        recorded_pid, recorded_settings_pid = resume_input_drop_args(entries, self.identity)
        if (pid, settings_pid) != (recorded_pid, recorded_settings_pid):
            raise RuntimeError("the supplied PIDs do not match the recorded input drop")
        self.record("input_drop_resume_started", {
            "initial_pid": pid, "settings_pid": settings_pid,
            "source": "verified-recorded-input-drop", "replay_drag": False
        })

        def restart_available(state):
            if state["settings"] != settings_pid:
                return None
            if state["app"] == pid and state["restart"]:
                return state
            if state["app"] not in (0, pid) and not pid_exists(pid):
                return state
            return None

        state = self.wait_auth_or("input-monitoring", restart_available)
        if state["app"] == pid:
            state = self.wait_for_restart_prompt_focus(state, pid, settings_pid)
        if state["app"] == pid:
            self.record("system_restart", self.probe("restart"))
        else:
            if not any(entry.get("stage") == "reopened_between_probes" and
                       entry.get("previous_pid") == pid and entry.get("app") == state["app"]
                       for entry in entries):
                self.record("reopened_between_probes", {"previous_pid": pid, "app": state["app"],
                                                        "settings": settings_pid})

        def resumed():
            state = self.probe()
            if state["app"] in (pid, 0) or state["settings"] != settings_pid or state["page"] not in CONTROL_PAGES:
                return None
            nodes = self.view(state["app"])
            if first_use_existing_input_target(nodes, self.name):
                return state
            return None

        state = wait_for(resumed, "new PID with input monitoring granted and device control open")
        resumed_pid = state["app"]
        state = self.wait_for_resumed_settings_focus(state, resumed_pid, settings_pid)
        if not any(entry.get("stage") == "resumed" and entry.get("app") == resumed_pid and
                   entry.get("settings") == settings_pid for entry in entries):
            self.record("resumed", state)
        if not any(entry.get("stage") == "restart_resumed_page" and entry.get("app") == resumed_pid and
                   entry.get("settings") == settings_pid for entry in entries):
            self.record("restart_resumed_page", {
                "trigger": "system_restart", "app": resumed_pid, "settings": settings_pid,
                "page": state["page"], "front": state["front"],
                "observed_at": time.time(), "automatic_page_switch": True
            })
        if state["front"] != settings_pid or not state["settings_above_setup"]:
            raise RuntimeError("the first-use window took frontmost ownership or covered Settings")
        control_title = first_use_existing_input_target(self.view(resumed_pid), self.name)
        if control_title is None:
            raise RuntimeError("the resumed first-use guide does not show the only pending Device Control row with all other recognized rows granted")
        control_press = self.press_permission_row(resumed_pid, control_title)
        self.record("permission_row_action", {
            "app": resumed_pid, "permission": "device-control", "press_started_at": control_press["ax_press_started_at"],
            "press_completed_at": control_press["ax_press_completed_at"], "source": "right-side 授权…"
        })
        state = self.wait_auth_or(
            "device-control Settings pane",
            lambda current: current if settings_page_opened(current, resumed_pid, CONTROL_PAGES) else None,
        )
        self.record_window_latencies("device-control", control_press)
        state, focus = self.prepare_settings_for_drag(
            state, app_pid=resumed_pid, pages=CONTROL_PAGES, stage="resumed-device-control",
        )
        self.record("device_control_ready", {**state, **focus})
        self.record("native_drop", self.drag(
            "drag-control", permission="device-control", app_pid=resumed_pid,
            settings_pid=settings_pid, pages=CONTROL_PAGES,
        ))
        self.finish(pid, resumed_pid, settings_pid)

    def finish(self, pid, resumed_pid, settings_pid):
        progress = self.config / "onboarding.json"

        def granted(state):
            return self.granted_after_restart(state, pid, settings_pid, resumed_pid)

        confirmed = getattr(self, "confirmed_control", False)
        restart_clicked = False

        def confirm_or_finish(state):
            nonlocal confirmed, restart_clicked
            result = granted(state)
            if result:
                return result
            if state["restart"] and state["front"] == settings_pid and not restart_clicked:
                restart = self.probe("restart")
                restart_clicked = True
                self.record("system_restart", restart)
                return None
            if confirmed or not self.fixed_dev or state["front"] != settings_pid or state["page"] not in CONTROL_PAGES:
                return None
            try:
                confirmation = self.probe("confirm-control")
            except RuntimeError as error:
                detail = error.args[0]
                if isinstance(detail, dict) and detail.get("error") in (
                    "the verification app and expected Settings front window are required",
                    "an unobstructed Dev device-control row is required",
                ):
                    return None
                raise
            if confirmation.get("performed"):
                confirmed = True
                self.record("system_control_confirmed", confirmation)
            return None

        state = self.wait_auth_or("device-control", confirm_or_finish)
        self.record("both_granted", state)
        self.press(resumed_pid, "继续")
        start = "开始使用 " + self.name
        wait_for(lambda: has_button(self.view(resumed_pid), start), "completion page")
        self.press(resumed_pid, start)
        wait_for(lambda: read_json(progress).get("completed") and
                 not any(name.endswith(" 首次设置") for name in window_names(self.view(resumed_pid))), "first-use completed")
        self.check_identity()
        self.record("first_use_verified", {"initial_pid": pid, "resumed_pid": resumed_pid, "settings_pid": settings_pid})
        self.close(resumed_pid)
        self.record("PASSED", {"initial_pid": pid, "resumed_pid": resumed_pid, "settings_pid": settings_pid})

    def granted_after_restart(self, state, initial_pid, settings_pid, expected_pid=None):
        app_pid = state["app"]
        if (app_pid in (initial_pid, 0) or (expected_pid is not None and app_pid != expected_pid) or
            state.get("settings", 0) != 0 or
            pid_exists(initial_pid) or pid_exists(settings_pid) or state["front"] != app_pid):
            return None
        nodes = self.view(app_pid)
        if complete_permission_view(nodes, self.name):
            return state
        return None

    def existing_input_segment_granted(self, state, initial_pid, settings_pid, restart_observed,
                                       full_first_use=False, *, allow_bluetooth_pending=False):
        app_pid = state["app"]
        original_alive = pid_exists(initial_pid)
        settings_alive = pid_exists(settings_pid)
        if not existing_input_process_verified(
            state, initial_pid, settings_pid, restart_observed, original_alive, settings_alive,
            full_first_use=full_first_use,
        ):
            return None
        nodes = self.view(app_pid) if app_pid else []
        if not existing_input_segment_granted(
            state, initial_pid, settings_pid, restart_observed,
            original_alive=original_alive, settings_alive=settings_alive,
            nodes=nodes, name=self.name,
            full_first_use=full_first_use,
            allow_bluetooth_pending=allow_bluetooth_pending,
        ):
            return None
        return state

    def close(self, pid):
        observed = self.probe()["app"]
        if observed == pid:
            self.record("quit_verification", self.probe("quit-app"))
        elif observed:
            raise RuntimeError("the verification process changed before cleanup")
        wait_for(lambda: not pid_exists(pid), "verification process exit")


def finish_args(entries, identity):
    if not entries or entries[0].get("stage") != "identity" or any(
        entries[0].get(key) != value for key, value in identity.items()
    ):
        raise RuntimeError("the existing run belongs to a different frozen bundle")
    last = entries[-1]
    if last.get("stage") == "system_control_confirmed" and len(entries) > 1:
        drop = entries[-2]
        if last.get("settings") != drop.get("settings") or last.get("app") != drop.get("app"):
            raise RuntimeError("permission confirmation belongs to a different visit")
        last = drop
    restart = next((entry for entry in entries if entry["stage"] == "system_restart"), None)
    resumed = next((entry for entry in entries if entry["stage"] == "resumed"), None)
    if (restart is None or resumed is None or last.get("stage") != "native_drop" or
        last.get("performed") != "drag-control" or restart["settings"] != resumed["settings"] or
        last["settings"] != resumed["settings"] or last["app"] != resumed["app"] or
        restart["app"] == resumed["app"] or
        resumed["front"] != resumed["settings"] or not resumed["settings_above_setup"]
    ):
        raise RuntimeError("the existing run has no verified pending final grant")
    return restart["app"], resumed["app"], resumed["settings"]


def resume_input_drop_args(entries, identity):
    if not entries or entries[0].get("stage") != "identity" or any(
        entries[0].get(key) != value for key, value in identity.items()
    ):
        raise RuntimeError("the existing run belongs to a different frozen bundle")
    if entries[0].get("mode") != "full-first-use":
        raise RuntimeError("--resume-input-drop requires a full-first-use run")

    drops = [entry for entry in entries if entry.get("stage") == "native_drop" and
             entry.get("performed") == "drag-input"]
    if len(drops) != 1:
        raise RuntimeError("the existing run must contain exactly one completed input-monitoring drop")
    drop = drops[0]
    initial_pid, settings_pid = drop.get("app"), drop.get("settings")
    if not isinstance(initial_pid, int) or initial_pid <= 0 or not isinstance(settings_pid, int) or settings_pid <= 0:
        raise RuntimeError("the recorded input drop has no valid app and Settings PIDs")

    drop_index = entries.index(drop)
    prior = entries[:drop_index]
    ready = [entry for entry in prior if entry.get("stage") == "input_ready" and
             entry.get("app") == initial_pid and entry.get("settings") == settings_pid]
    input_action = []
    for index, entry in enumerate(prior):
        if (entry.get("stage") != "permission_row_action" or
            entry.get("permission") != "input-monitoring" or
            entry.get("source") != "right-side 授权…" or
            entry.get("app", initial_pid) != initial_pid):
            continue
        matching_button = any(
            button.get("stage") == "app_button" and button.get("app") == initial_pid and
            button.get("panel_title") == "MiMac Dev 系统权限" and button.get("row_title") == INPUT_ROW_TITLE and
            button.get("action") == PERMISSION_ACTION_LABEL and button.get("scope") == "permissions_panel_and_row" and
            button.get("ax_press_started_at") == entry.get("press_started_at") and
            button.get("ax_press_completed_at") == entry.get("press_completed_at")
            for button in prior[:index]
        )
        if matching_button:
            input_action.append(entry)
    if not ready or not input_action:
        raise RuntimeError("the completed drop is missing its verified input-monitoring setup stages")

    forbidden = {
        "device_control_ready", "system_control_confirmed", "both_granted",
        "first_use_verified", "PASSED"
    }
    if any(entry.get("stage") in forbidden for entry in entries):
        raise RuntimeError("the existing run has already progressed beyond the input-monitoring grant")
    if any(entry.get("stage") == "native_drop" and entry.get("performed") == "drag-control"
           for entry in entries):
        raise RuntimeError("the existing run has already performed the device-control drop")
    if any(entry.get("stage") == "permission_row_action" and entry.get("permission") == "device-control"
           for entry in entries):
        raise RuntimeError("the existing run has already pressed the device-control permission row")
    return initial_pid, settings_pid


def existing_input_segment_granted(state, initial_pid, settings_pid, restart_observed,
                                   original_alive, settings_alive, nodes, name,
                                   full_first_use=False, *, allow_bluetooth_pending=False):
    process_verified = existing_input_process_verified(
        state, initial_pid, settings_pid, restart_observed, original_alive, settings_alive,
        full_first_use=full_first_use,
    )
    if full_first_use:
        authorization_view = complete_permission_view(nodes, name)
    else:
        authorization_view = ordinary_permission_guide_complete(
            nodes, name, allow_bluetooth_pending=allow_bluetooth_pending,
        )
    return process_verified and authorization_view


def existing_input_process_verified(state, initial_pid, settings_pid, restart_observed,
                                    original_alive, settings_alive, full_first_use=False):
    app_pid = state.get("app", 0)
    settings_exited = (settings_pid > 0 and state.get("settings") == 0 and not settings_alive)
    if full_first_use:
        if restart_observed:
            return (app_pid not in (0, initial_pid) and not original_alive and
                    settings_exited and state.get("front") == app_pid)
        return (app_pid == initial_pid and original_alive and not state.get("restart") and
                settings_exited and state.get("front") == initial_pid)

    expected_app = (app_pid not in (0, initial_pid) and not original_alive) if restart_observed else (
        app_pid == initial_pid and original_alive and not state.get("restart")
    )
    if not expected_app:
        return False
    settings_visible = settings_pid > 0 and state.get("settings") == settings_pid and settings_alive
    if settings_visible:
        return state.get("front") == settings_pid and state.get("page") in CONTROL_PAGES
    return settings_exited and state.get("front") == app_pid


def existing_input_target(nodes, name, *, allow_bluetooth_pending=False):
    names = window_names(nodes)
    if name + " 首次设置" in names:
        return first_use_existing_input_target(nodes, name)
    return ordinary_existing_input_target(
        nodes, name, allow_bluetooth_pending=allow_bluetooth_pending,
    )


def first_use_existing_input_target(nodes, name):
    """Target Device Control after every other recognized first-use row is granted."""
    names = window_names(nodes)
    panel_title = name + " 系统权限"
    if names.count(name + " 首次设置") != 1 or names.count(panel_title) != 1:
        return None
    panel_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == panel_title)
    card = nodes_in_window(nodes, panel_window)
    rows = permission_route_rows(card) if card is not None else None
    summary = first_use_progress_summary(nodes, len(rows) if rows is not None else None)
    if rows is None or summary is None or set(rows) not in (
        {"input", "device_control"}, {"input", "device_control", "bluetooth"}
    ):
        return None
    states = {kind: permission_row_state(card, row) for kind, row in rows.items()}
    granted_count = sum(state == "granted" for state in states.values())
    pending_control = states.get("device_control") == "pending"
    bluetooth_ready = "bluetooth" not in states or states["bluetooth"] == "granted"
    summary_granted, summary_total, _ = summary
    if (states.get("input") != "granted" or not pending_control or not bluetooth_ready or
        any(state not in ("granted", "pending") for state in states.values()) or
        sum(state == "pending" for state in states.values()) != 1 or
        exact_button_count(card, PERMISSION_ACTION_LABEL) != 1 or
        (summary_granted, summary_total) != (granted_count, len(rows))):
        return None
    return rows["device_control"]["route_title"]


def first_use_input_target(nodes, name):
    """Allow the initial full-first-use drop only when Bluetooth is an already-granted optional row."""
    names = window_names(nodes)
    panel_title = name + " 系统权限"
    if names.count(name + " 首次设置") != 1 or names.count(panel_title) != 1:
        return None
    panel_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == panel_title)
    card = nodes_in_window(nodes, panel_window)
    rows = permission_route_rows(card) if card is not None else None
    allowed = ({"input", "device_control"}, {"input", "device_control", "bluetooth"})
    if rows is None or set(rows) not in allowed:
        return None
    states = {kind: permission_row_state(card, row) for kind, row in rows.items()}
    granted_count = sum(state == "granted" for state in states.values())
    summary = first_use_progress_summary(nodes, len(rows))
    bluetooth_ready = "bluetooth" not in states or states["bluetooth"] == "granted"
    if (states.get("input") != "pending" or states.get("device_control") != "pending" or
        not bluetooth_ready or any(state not in ("granted", "pending") for state in states.values()) or
        sum(state == "pending" for state in states.values()) != 2 or
        exact_button_count(card, PERMISSION_ACTION_LABEL) != 2 or summary is None or
        (summary[0], summary[1]) != (granted_count, len(rows))):
        return None
    return rows["input"]["route_title"]


def ordinary_general_surface_ready(nodes, name):
    surfaces = ordinary_general_nodes(nodes, name)
    return surfaces is not None


def ordinary_general_startup_ready(app_pid, nodes, name):
    """Wait for the real ordinary window and visible General navigation, independent of launch focus."""
    if not isinstance(app_pid, int) or isinstance(app_pid, bool) or app_pid <= 0:
        return False
    names = window_names(nodes)
    if name + " 首次设置" in names or names.count(name) != 1:
        return False
    main_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == name)
    main_nodes = nodes_in_window(nodes, main_window)
    return main_nodes is not None and exact_button_count(main_nodes, "通用") == 1


def ordinary_permission_guide_card_present(nodes, name):
    surfaces = ordinary_permission_surfaces(nodes, name)
    return surfaces is not None


def ordinary_permission_guide_complete(nodes, name, *, allow_bluetooth_pending=False):
    surfaces = ordinary_permission_surfaces(nodes, name)
    if surfaces is None:
        return False
    general, card, general_rows, card_rows = surfaces
    if set(general_rows) != set(card_rows):
        return False
    states = ordinary_permission_row_states(general, card, general_rows, card_rows)
    if states is None:
        return False
    pending = {kind for kind, state in states.items() if state == "pending"}
    if allow_bluetooth_pending:
        return (
            states.get("input") == states.get("device_control") == "granted" and
            all(state in ("granted", "pending") for state in states.values()) and
            pending.issubset({"bluetooth"}) and
            exact_button_count(card, PERMISSION_ACTION_LABEL) == len(pending)
        )
    return (not pending and all(state == "granted" for state in states.values()) and
            exact_button_count(card, PERMISSION_ACTION_LABEL) == 0)


def general_input_action_target(nodes, name, *, allow_bluetooth_pending=False):
    """Return the Input Monitoring row only for a readable ordinary cold-flow state."""
    surfaces = ordinary_permission_surfaces(nodes, name)
    if surfaces is None:
        return None
    general, card, general_rows, card_rows = surfaces
    states = ordinary_permission_row_states(general, card, general_rows, card_rows)
    if states is None or states.get("input") != "pending":
        return None
    if states.get("device_control") not in ("granted", "pending"):
        return None
    if "bluetooth" in states and states["bluetooth"] not in (
        ("granted", "pending") if allow_bluetooth_pending else ("granted",)
    ):
        return None
    expected_actions = sum(state == "pending" for state in states.values())
    if exact_button_count(card, PERMISSION_ACTION_LABEL) != expected_actions:
        return None
    return INPUT_ROW_TITLE


def ordinary_existing_input_target(nodes, name, *, allow_bluetooth_pending=False):
    surfaces = ordinary_permission_surfaces(nodes, name)
    if surfaces is None:
        return None
    general, card, general_rows, card_rows = surfaces
    states = ordinary_permission_row_states(general, card, general_rows, card_rows)
    if states is None or states.get("input") != "granted" or states.get("device_control") != "pending":
        return None
    if "bluetooth" in states and states["bluetooth"] not in (
        ("granted", "pending") if allow_bluetooth_pending else ("granted",)
    ):
        return None
    expected_actions = 2 if allow_bluetooth_pending and states.get("bluetooth") == "pending" else 1
    if exact_button_count(card, PERMISSION_ACTION_LABEL) != expected_actions:
        return None
    return card_rows["device_control"]["route_title"]


def ordinary_general_nodes(nodes, name):
    names = window_names(nodes)
    if name + " 首次设置" in names or names.count(name) != 1:
        return None
    general_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == name)
    general = nodes_in_window(nodes, general_window)
    if general is None or general_manage_rows(general) is None:
        return None
    return general, general_window


def ordinary_permission_surfaces(nodes, name):
    general_result = ordinary_general_nodes(nodes, name)
    names = window_names(nodes)
    panel_title = name + " 系统权限"
    if general_result is None or names.count(panel_title) != 1:
        return None
    general, general_window = general_result
    panel_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == panel_title)
    card = nodes_in_window(nodes, panel_window)
    if card is None:
        return None
    general_rows = general_manage_rows(general)
    card_rows = permission_route_rows(card)
    required = {"input", "device_control"}
    if (general_rows is None or card_rows is None or
        not required.issubset(general_rows) or not required.issubset(card_rows) or
        set(general_rows) != set(card_rows)):
        return None
    return (
        general, card, general_rows, card_rows,
    )


def general_manage_kind(label):
    if label == GENERAL_INPUT_MANAGE:
        return "input"
    if label in GENERAL_CONTROL_MANAGE:
        return "device_control"
    if label in GENERAL_BLUETOOTH_MANAGE:
        return "bluetooth"
    return None


def general_manage_rows(general):
    rows = {}
    for button in (item for item in general if item.get("role") == "AXButton"):
        kinds = {general_manage_kind(label) for label in (button.get("title"), button.get("description"))}
        kinds.discard(None)
        if not kinds:
            continue
        if len(kinds) != 1:
            return None
        kind = next(iter(kinds))
        if kind in rows:
            return None
        rows[kind] = button
    required = {"input", "device_control"}
    return rows if required.issubset(rows) and set(rows).issubset(required | {"bluetooth"}) else None


def permission_route_kind(label):
    if label == INPUT_ROW_TITLE:
        return "input"
    if label in CONTROL_ROW_TITLES:
        return "device_control"
    if label in BLUETOOTH_ROW_TITLES:
        return "bluetooth"
    return None


def permission_route_rows(card):
    rows = {}
    route_nodes = [node for node in card if node.get("role") == "AXButton" and any(
        isinstance(label, str) and label.startswith("在系统设置中管理")
        for label in (node.get("title"), node.get("description"))
    )]
    for button in route_nodes:
        labels = [label for label in (button.get("title"), button.get("description"))
                  if isinstance(label, str) and label.startswith("在系统设置中管理")]
        kinds = {permission_route_kind(label) for label in labels}
        if None in kinds or len(kinds) != 1:
            return None
        kind = next(iter(kinds))
        if kind in rows:
            return None
        route_title = next(label for label in labels if permission_route_kind(label) == kind)
        rows[kind] = {**button, "route_title": route_title}
    required = {"input", "device_control"}
    return rows if required.issubset(rows) and set(rows).issubset(required | {"bluetooth"}) else None


def permission_row_state(card, row):
    granted = row_exact_label_count(card, "已授权", row) == 1
    actions = row_action_count(card, row["route_title"])
    if granted and actions == 0:
        return "granted"
    if not granted and actions == 1:
        return "pending"
    return "invalid"


def ordinary_permission_row_states(general, card, general_rows, card_rows):
    if set(general_rows) != set(card_rows):
        return None
    states = {}
    for kind, card_row in card_rows.items():
        general_granted = row_exact_label_count(general, "已授权", general_rows[kind]) == 1
        card_state = permission_row_state(card, card_row)
        if card_state == "granted" and general_granted:
            states[kind] = "granted"
        elif card_state == "pending" and not general_granted:
            states[kind] = "pending"
        else:
            states[kind] = "invalid"
    expected_actions = sum(state == "pending" for state in states.values())
    if exact_button_count(card, PERMISSION_ACTION_LABEL) != expected_actions:
        return None
    return states


def first_use_progress_summary(nodes, total=None):
    summaries = []
    for node in nodes:
        for value in (node.get("title"), node.get("value"), node.get("description")):
            if isinstance(value, str) and re.fullmatch(r"已授权\s*(\d+)\s*/\s*(\d+)", value):
                match = re.fullmatch(r"已授权\s*(\d+)\s*/\s*(\d+)", value)
                summaries.append((int(match.group(1)), int(match.group(2)), value))
    unique = list(dict.fromkeys(summaries))
    if len(unique) == 1:
        return unique[0]
    if total is not None:
        all_granted = [
            value for node in nodes
            for value in (node.get("title"), node.get("value"), node.get("description"))
            if value == "全部已授权"
        ]
        if len(set(all_granted)) == 1:
            return total, total, "全部已授权"
        not_yet_authorized = [
            value for node in nodes
            for value in (node.get("title"), node.get("value"), node.get("description"))
            if value == "尚未授权"
        ]
        if len(set(not_yet_authorized)) == 1:
            return 0, total, "尚未授权"
    return None


def ordinary_permission_counts(nodes, name):
    surfaces = ordinary_permission_surfaces(nodes, name)
    if surfaces is None:
        return None
    general, card, general_rows, card_rows = surfaces
    states = ordinary_permission_row_states(general, card, general_rows, card_rows)
    if states is None:
        return None
    return {
        "row_count": len(states),
        "granted_row_count": sum(state == "granted" for state in states.values()),
        "rows": {kind: states[kind] for kind in sorted(states)},
    }


def first_use_permission_counts(nodes, name):
    """Read the first-use card as one internally consistent set of permission rows."""
    names = window_names(nodes)
    panel_title = name + " 系统权限"
    if names.count(name + " 首次设置") != 1 or names.count(panel_title) != 1:
        return None
    panel_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == panel_title)
    card = nodes_in_window(nodes, panel_window)
    rows = permission_route_rows(card) if card is not None else None
    required = {"input", "device_control"}
    if rows is None or not required.issubset(rows) or not set(rows).issubset(required | {"bluetooth"}):
        return None
    states = {kind: permission_row_state(card, row) for kind, row in rows.items()}
    granted_count = sum(state == "granted" for state in states.values())
    pending_count = sum(state == "pending" for state in states.values())
    if any(state not in ("granted", "pending") for state in states.values()):
        return None
    if exact_button_count(card, PERMISSION_ACTION_LABEL) != pending_count:
        return None
    summary = first_use_progress_summary(nodes, len(rows))
    if summary is None or summary[:2] != (granted_count, len(rows)):
        return None
    return {
        "row_count": len(states), "granted_row_count": granted_count,
        "rows": {kind: states[kind] for kind in sorted(states)},
    }


def bluetooth_permission_target(nodes, name, scope):
    """Return the exact Bluetooth card row and status for a full acceptance surface."""
    if scope == "full-first-use":
        counts = first_use_permission_counts(nodes, name)
        panel_title = name + " 系统权限"
        if counts is None:
            return None
        panel_window = next(node for node in nodes if node.get("role") == "AXWindow" and node.get("title") == panel_title)
        card = nodes_in_window(nodes, panel_window)
        rows = permission_route_rows(card) if card is not None else None
    elif scope == "ordinary-general-full":
        counts = ordinary_permission_counts(nodes, name)
        surfaces = ordinary_permission_surfaces(nodes, name)
        if counts is None or surfaces is None:
            return None
        card_rows = surfaces[3]
        rows = card_rows
    else:
        return None
    if "bluetooth" not in counts["rows"]:
        return {"state": "not_present", "row_title": None, "counts": counts}
    if not isinstance(rows, dict) or "bluetooth" not in rows:
        return None
    state = counts["rows"]["bluetooth"]
    if state not in ("pending", "granted"):
        return None
    return {"state": state, "row_title": rows["bluetooth"]["route_title"], "counts": counts}


def cancel_reentry_permission_counts(nodes, name):
    """Accept a readable ordinary 2/3-row baseline with Input Monitoring pending."""
    counts = ordinary_permission_counts(nodes, name)
    if counts is None or counts["rows"].get("input") != "pending":
        return None
    if any(state not in ("pending", "granted") for state in counts["rows"].values()):
        return None
    return counts


def permission_rows_unchanged(before, after):
    return (
        isinstance(before, dict) and isinstance(after, dict) and
        before.get("row_count") == after.get("row_count") and
        before.get("rows") == after.get("rows") and
        before.get("granted_row_count") == after.get("granted_row_count")
    )


def non_target_permission_rows_unchanged(before, after):
    if not isinstance(before, dict) or not isinstance(after, dict):
        return False
    before_rows, after_rows = before.get("rows"), after.get("rows")
    if not isinstance(before_rows, dict) or not isinstance(after_rows, dict):
        return False
    if set(before_rows) != set(after_rows):
        return False
    return all(before_rows[kind] == after_rows[kind] for kind in before_rows if kind != "input")


def verified_cancel_result(result, app_pid, settings_pid, bundle):
    if not isinstance(result, dict):
        return False
    try:
        bundle_path = str(Path(bundle).resolve())
    except (OSError, TypeError, ValueError):
        return False
    frontmost_bundle = result.get("frontmost_bundle_id", result.get("authentication_bundle"))
    unique_sheet_controls = all(
        type(result.get(key)) is int and result.get(key) == 1
        for key in ("authentication_sheet_count", "secure_field_count", "cancel_button_count")
    )
    page_sheet_provenance = (
        type(result.get("target_page_sheet_count")) is int and
        result.get("target_page_sheet_count") == 1 and
        result.get("target_sheet_role") == "AXSheet" and
        result.get("target_sheet_source") == "verified-page-subtree" and
        type(result.get("other_window_sheet_count")) is int and
        result.get("other_window_sheet_count") == 0
    )
    return (
        result.get("status") == "CANCELLED" and
        result.get("action") == "AXPress Cancel" and
        result.get("app") == app_pid and isinstance(result.get("app"), int) and
        not isinstance(result.get("app"), bool) and
        result.get("settings") == settings_pid and isinstance(result.get("settings"), int) and
        not isinstance(result.get("settings"), bool) and
        result.get("bundle_id") == "dev.mimac.app.dev" and
        result.get("bundle_path") == bundle_path and
        result.get("settings_page") in INPUT_PAGES and
        frontmost_bundle in AUTH_PROMPT_BUNDLES and
        unique_sheet_controls and
        page_sheet_provenance and
        result.get("cancel_label") in ("Cancel", "取消") and
        result.get("text_entered") is False
    )


def concise_tool_error(error):
    detail = error.args[0] if isinstance(error, RuntimeError) and error.args else None
    if isinstance(detail, dict) and isinstance(detail.get("error"), str):
        return detail["error"][:240]
    return "native Cancel action could not be verified"


def permission_progress_summary(nodes, name):
    if name + " 首次设置" in window_names(nodes):
        summary = first_use_progress_summary(nodes)
        return summary[2] if summary else None
    counts = ordinary_permission_counts(nodes, name)
    if counts is None:
        return None
    return f"{counts['granted_row_count']}/{counts['row_count']} 行在通用页与权限小卡显示已授权"


def restart_focus_observation(state):
    if not isinstance(state, dict):
        return {"state_available": False}
    return {
        "state_available": True,
        "app_pid": state.get("app"),
        "settings_pid": state.get("settings"),
        "frontmost_pid": state.get("front"),
        "settings_page": state.get("page"),
        "restart_prompt_visible": state.get("restart") is True,
        "security_popup_visible": state.get("system_sheet") is True,
        "password_prompt_visible": state.get("password") is True,
        "settings_above_setup": state.get("settings_above_setup"),
    }


def restart_prompt_focus_ready(state, app_pid, settings_pid):
    return (
        isinstance(state, dict) and state.get("app") == app_pid and
        state.get("settings") == settings_pid and state.get("front") == settings_pid and
        state.get("restart") is True and not state.get("system_sheet") and
        not state.get("password")
    )


def resumed_settings_focus_ready(state, app_pid, settings_pid):
    return (
        isinstance(state, dict) and state.get("app") == app_pid and
        state.get("settings") == settings_pid and state.get("front") == settings_pid and
        state.get("page") in CONTROL_PAGES and state.get("settings_above_setup") is True and
        not state.get("restart") and not state.get("system_sheet") and not state.get("password")
    )


def nodes_in_window(nodes, window):
    if not isinstance(nodes, (list, tuple)) or not isinstance(window, dict):
        return None
    if "root_window_index" in window:
        owner_index = window.get("root_window_index")
        if not isinstance(owner_index, int) or isinstance(owner_index, bool) or owner_index < 0:
            return None
        root_title = window.get("root_window_title")
        window_title = window.get("title")
        if isinstance(root_title, str) and isinstance(window_title, str) and root_title != window_title:
            return None
        return [
            node for node in nodes
            if isinstance(node.get("root_window_index"), int) and
            not isinstance(node.get("root_window_index"), bool) and
            node.get("root_window_index") == owner_index
        ]
    if any("root_window_index" in node for node in nodes):
        # A partially tagged snapshot must not fall back to geometry and mix roots.
        return None
    bounds = ax_bounds(window)
    if bounds is None:
        return None
    return [node for node in nodes if node_center_in_bounds(node, bounds)]


def ax_bounds(node):
    value = node.get("bounds") if isinstance(node, dict) else None
    if (not isinstance(value, dict) or
        any(not isinstance(value.get(key), (int, float)) or isinstance(value.get(key), bool)
            for key in ("x", "y", "width", "height"))):
        return None
    x, y, width, height = (float(value[key]) for key in ("x", "y", "width", "height"))
    if width <= 0 or height <= 0:
        return None
    return x, y, width, height


def node_center_in_bounds(node, bounds):
    rect = ax_bounds(node)
    if rect is None:
        return False
    x, y, width, height = rect
    wx, wy, wwidth, wheight = bounds
    center_x, center_y = x + width / 2, y + height / 2
    return wx <= center_x <= wx + wwidth and wy <= center_y <= wy + wheight


def exact_button_nodes(nodes, label):
    return [node for node in nodes if node.get("role") == "AXButton" and
            label in (node.get("title"), node.get("description")) and node.get("enabled") is True]


def exact_button_count(nodes, label):
    return len(exact_button_nodes(nodes, label))


def exact_label_nodes(nodes, label):
    return [node for node in nodes if label in (node.get("title"), node.get("value"), node.get("description"))]


def scoped_exact_label_count(nodes, label):
    return len(exact_label_nodes(nodes, label))


def same_ax_row(first, second):
    first_bounds = ax_bounds(first)
    second_bounds = ax_bounds(second)
    if first_bounds is None or second_bounds is None:
        return False
    first_y = first_bounds[1] + first_bounds[3] / 2
    second_y = second_bounds[1] + second_bounds[3] / 2
    return abs(first_y - second_y) <= max(16, (first_bounds[3] + second_bounds[3]) / 2 + 4)


def row_exact_label_count(nodes, label, anchor):
    return sum(1 for node in exact_label_nodes(nodes, label) if same_ax_row(node, anchor))


def row_action_count(card, row_title):
    anchors = exact_button_nodes(card, row_title)
    if len(anchors) != 1:
        return 0
    anchor = anchors[0]
    anchor_bounds = ax_bounds(anchor)
    if anchor_bounds is None:
        return 0
    anchor_mid_x = anchor_bounds[0] + anchor_bounds[2] / 2
    return sum(
        1 for action in exact_button_nodes(card, PERMISSION_ACTION_LABEL)
        if same_ax_row(action, anchor) and
        (action_bounds := ax_bounds(action)) is not None and
        action_bounds[0] + action_bounds[2] / 2 > anchor_mid_x
    )


def drag_regressions_snapshot_ready(state):
    summaries = state.get("permission_summary", [])
    return (state.get("permission_card_visible") is True and isinstance(summaries, list) and
            any(isinstance(value, str) and
                any(marker in value for marker in ("已授权", "未授权", "全部已授权"))
                for value in summaries))


def draggable_window_geometry_ready(state, app_pid, settings_pid):
    dev_pids = state.get("dev_pids") if isinstance(state, dict) else None
    if (not isinstance(state, dict) or not isinstance(dev_pids, list) or app_pid not in dev_pids or
        state.get("settings_pid") != settings_pid or settings_pid <= 0 or
        state.get("permission_card_visible") is not True or
        state.get("permission_card_title_match") is not True):
        return False
    attachment = state.get("attachment")
    if not isinstance(attachment, dict) or attachment.get("known") is not True or attachment.get("attached") is not True:
        return False
    card_bounds = rect_summary_bounds(attachment.get("permission_card_bounds"))
    settings_bounds = rect_summary_bounds(attachment.get("settings_bounds"))
    try:
        gap = float(attachment.get("horizontal_gap"))
        top_delta = float(attachment.get("top_delta"))
    except (TypeError, ValueError):
        return False
    return (card_bounds is not None and settings_bounds is not None and
            abs(gap - 16) <= 1 and abs(top_delta) <= 1)


def rect_summary_bounds(value):
    if (not isinstance(value, list) or len(value) != 4 or
        any(not isinstance(coordinate, (int, float)) or isinstance(coordinate, bool) for coordinate in value)):
        return None
    x, y, width, height = (float(coordinate) for coordinate in value)
    if width <= 0 or height <= 0:
        return None
    return x, y, width, height


def drag_page_probe_ready(state, app_pid, settings_pid, pages):
    return (isinstance(state, dict) and state.get("app") == app_pid and
            state.get("settings") == settings_pid and state.get("front") == settings_pid and
            state.get("page") in pages and not state.get("password") and
            not state.get("system_sheet") and not state.get("restart") and
            state.get("permission_card_visible") is True)


def settings_page_opened(state, app_pid, pages):
    """Accept an exact, unobstructed destination pane even if another app owns focus."""
    if not isinstance(state, dict) or not positive_pid(app_pid):
        return False
    settings_pid = state.get("settings")
    return (
        state.get("app") == app_pid and positive_pid(settings_pid) and
        state.get("page") in pages and positive_pid(state.get("front")) and
        state.get("settings_bundle_id") == "com.apple.systempreferences" and
        state.get("settings_process_count") == 1 and state.get("settings_page_count") == 1 and
        state.get("permission_card_visible") is True and
        state.get("permission_card_window_count") == 1 and
        not state.get("password") and not state.get("system_sheet") and not state.get("restart")
    )


def focus_recovery_candidate(state, app_pid, settings_pid, page, bundle_id, bundle_path):
    """Require the exact already-open pair before any one-shot Settings activation."""
    return (
        settings_page_opened(state, app_pid, (page,)) and
        state.get("settings") == settings_pid and state.get("bundle_id") == bundle_id and
        state.get("bundle_path") == bundle_path and
        isinstance(state.get("front"), int) and not isinstance(state.get("front"), bool) and
        state.get("settings_bundle_path") and
        state.get("extra_access_warning") is False
    )


def settings_focus_readback_ready(state, app_pid, settings_pid, page, bundle_id, bundle_path):
    return (focus_recovery_candidate(state, app_pid, settings_pid, page, bundle_id, bundle_path) and
            state.get("front") == settings_pid)


def focus_readback_transition_safe(state, app_pid, settings_pid, page, bundle_id, bundle_path,
                                  original_state):
    if not isinstance(state, dict) or not isinstance(original_state, dict):
        return False
    current_page = state.get("page")
    page_count = state.get("settings_page_count")
    page_is_transitioning = current_page in (None, "") and page_count == 0
    page_is_exact = current_page == page and page_count == 1
    front = state.get("front")
    original_front = original_state.get("front")
    front_is_transitioning = (
        isinstance(front, int) and not isinstance(front, bool) and
        front in (0, settings_pid, original_front)
    )
    return (
        state.get("app") == app_pid and state.get("bundle_id") == bundle_id and
        state.get("bundle_path") == bundle_path and
        state.get("settings") == settings_pid and state.get("settings_bundle_id") == "com.apple.systempreferences" and
        state.get("settings_bundle_path") == original_state.get("settings_bundle_path") and
        state.get("settings_process_count") == 1 and front_is_transitioning and
        (page_is_transitioning or page_is_exact) and
        state.get("permission_card_visible") is True and
        state.get("permission_card_window_count") == 1 and
        not state.get("password") and not state.get("system_sheet") and
        not state.get("restart") and state.get("extra_access_warning") is False
    )


def focus_check_preflight_ready(state, bundle_id, bundle_path, pages):
    return (
        isinstance(state, dict) and state.get("page") in pages and
        focus_recovery_candidate(
            state, state.get("app"), state.get("settings"), state.get("page"),
            bundle_id, bundle_path,
        )
    )


def positive_pid(value):
    return isinstance(value, int) and not isinstance(value, bool) and value > 0


def settings_page_window_ready(state, settings_pid, page):
    if not isinstance(state, dict) or state.get("settings_pid") != settings_pid:
        return False
    matches = state.get("settings_page_title_matches")
    return (isinstance(matches, list) and len(matches) == 1 and
            isinstance(matches[0], dict) and matches[0].get("title") == page)


def focus_probe_summary(state):
    if not isinstance(state, dict):
        return {"state_available": False}
    return {
        "app_pid": state.get("app"), "app_bundle_id": state.get("bundle_id"),
        "settings_pid": state.get("settings"),
        "settings_bundle_id": state.get("settings_bundle_id"),
        "settings_process_count": state.get("settings_process_count"),
        "frontmost_pid": state.get("front"),
        "frontmost_bundle_id": state.get("frontmost_bundle_id"),
        "page": state.get("page"), "settings_page_count": state.get("settings_page_count"),
        "permission_card_visible": state.get("permission_card_visible"),
        "permission_card_window_count": state.get("permission_card_window_count"),
        "password": state.get("password"), "system_sheet": state.get("system_sheet"),
        "restart": state.get("restart"),
    }


def drag_geometry_summary(state):
    attachment = state.get("attachment", {}) if isinstance(state, dict) else {}
    pages = state.get("settings_page_title_matches", []) if isinstance(state, dict) else []
    return {
        "app_identity_present": bool(state.get("dev_pids")) if isinstance(state, dict) else False,
        "settings_identity_present": bool(state.get("settings_pid")) if isinstance(state, dict) else False,
        "permission_card_visible": bool(state.get("permission_card_visible")) if isinstance(state, dict) else False,
        "permission_card_title_match": bool(state.get("permission_card_title_match")) if isinstance(state, dict) else False,
        "settings_page_titles": [entry.get("title") for entry in pages if isinstance(entry, dict)],
        "attachment": {
            key: attachment.get(key)
            for key in ("known", "attached", "horizontal_gap", "top_delta")
        } if isinstance(attachment, dict) else {},
    }


def drag_page_probe_summary(state):
    return {
        "app": state.get("app"), "settings": state.get("settings"),
        "front": state.get("front"), "page": state.get("page"),
        "permission_card_visible": bool(state.get("permission_card_visible")),
        "password_prompt": bool(state.get("password")),
        "security_popup": bool(state.get("system_sheet")),
        "restart_prompt": bool(state.get("restart")),
    } if isinstance(state, dict) else None


def complete_permission_view(nodes, name):
    return (window_names(nodes) == [name + " 首次设置"] and
            has_button(nodes, "继续") and not has_button(nodes, "授权…") and
            has_label(nodes, "全部已授权"))


def redacted_auth_state(state):
    app_pid = state.get("app", 0)
    settings_pid = state.get("settings", 0)
    front_pid = state.get("front", 0)
    if app_pid and front_pid == app_pid:
        front = "app"
    elif settings_pid and front_pid == settings_pid:
        front = "settings"
    elif front_pid:
        front = "other"
    else:
        front = "none"
    restart = bool(state.get("restart"))
    return {
        "app_present": bool(app_pid),
        "settings_present": bool(settings_pid),
        "front": front,
        "restart_prompt": restart,
        "security_popup": bool(state.get("system_sheet")) and not restart,
        "password_prompt": bool(state.get("password")),
    }


def pid_exists(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def is_transient_ax_windows_error(error, pid):
    detail = error.args[0] if isinstance(error, RuntimeError) and error.args else None
    return (
        isinstance(detail, dict) and detail.get("stage") == "ax_windows" and
        detail.get("error") == "could not read root AXWindows" and
        detail.get("status") == "error" and detail.get("ax_error_code") == -25204 and
        detail.get("target_pid") == pid
    )


def ready_input(state):
    if state["page"] in INPUT_PAGES and state["settings"] and state["front"] == state["settings"] and not state["password"]:
        return state
    return None


def ready_control(state):
    if state["page"] in CONTROL_PAGES and state["settings"] and state["front"] == state["settings"] and not state["password"]:
        return state
    return None


def has_button(nodes, label):
    return isinstance(nodes, (list, tuple)) and any(
        isinstance(node, dict) and node.get("role") == "AXButton" and
        label in (node.get("title"), node.get("description")) and node.get("enabled")
        for node in nodes
    )


def has_label(nodes, label):
    return isinstance(nodes, (list, tuple)) and any(
        isinstance(node, dict) and label in (node.get("title"), node.get("value"), node.get("description"))
        for node in nodes
    )


def window_names(nodes):
    if not isinstance(nodes, (list, tuple)):
        return []
    return [node.get("title") for node in nodes
            if isinstance(node, dict) and node.get("role") == "AXWindow"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", required=True, type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--fixed-dev-receipt", type=Path,
                        help="exercise the canonical Dev bundle after verifying all frozen artifacts")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--auth-timeout", default=120, type=int)
    parser.add_argument("--bluetooth-allow-actor", choices=("runner", "user"),
                        help="record the Bluetooth Allow actor; runner mode AXPresses only the exact verified request, user mode waits for a manual Allow")
    parser.add_argument("--bluetooth-only", action="store_true",
                        help="handle only Bluetooth on the already-open fixed Dev permission card")
    parser.add_argument("--interact", required=True, action="store_true",
                        help="allow the isolated flow to use system settings and the pointer")
    parser.add_argument("--existing-input", action="store_true",
                        help="verify device control with input monitoring already granted; any Bluetooth row must also be granted")
    parser.add_argument("--general-permissions", action="store_true",
                        help="cold-enter ordinary General settings and grant Input Monitoring; handle Bluetooth through its exact Dev request when present")
    parser.add_argument("--two-list-permissions-only", action="store_true",
                        help="with --general-permissions, grant both ordinary list rows while recording Bluetooth status separately")
    parser.add_argument("--cancel-reentry", action="store_true",
                        help="drop Input Monitoring once from the ordinary permission card, cancel its exact system password sheet, then verify every actual permission row and card re-entry are unchanged")
    parser.add_argument("--drag-regressions-only", action="store_true",
                        help="run only the non-granting native drag regressions on an already open Dev/Settings pair")
    parser.add_argument("--focus-check", action="store_true",
                        help="recover one already-open fixed Dev Settings page after focus loss; no drag or permission action")
    parser.add_argument("--resume-input-drop", action="store_true",
                        help="continue after the recorded input-monitoring drop without repeating it")
    parser.add_argument("--finish", action="store_true",
                        help="verify and finish an existing run after its recorded device-control drop")
    args = parser.parse_args()
    try:
        acceptance = Acceptance(args)
        with acceptance.observe_feedback():
            if args.bluetooth_only:
                acceptance.run_bluetooth_only()
            elif args.existing_input:
                acceptance.run_existing_input()
            elif args.general_permissions:
                acceptance.run_general_permissions()
            elif args.cancel_reentry:
                acceptance.run_cancel_reentry()
            elif args.drag_regressions_only:
                acceptance.run_drag_regressions_only()
            elif args.focus_check:
                acceptance.run_focus_check()
            elif args.resume_input_drop:
                acceptance.finish_input_drop(*acceptance.resume_input_args)
            elif args.finish:
                acceptance.finish(*acceptance.finish_args)
            else:
                acceptance.run()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"BLOCKED {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
