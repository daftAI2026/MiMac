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
PERMISSION_ACTION_LABEL = "授权…"
DRAG_GEOMETRY_TIMEOUT = 12


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


class Acceptance:
    def __init__(self, args):
        self.bundle = args.bundle.resolve(strict=True)
        with (self.bundle / "Contents/Info.plist").open("rb") as manifest:
            info = plistlib.load(manifest)
        self.name = info["CFBundleDisplayName"]
        self.identifier = info["CFBundleIdentifier"]
        self.fixed_dev = args.fixed_dev_receipt is not None
        self.existing_input = args.existing_input
        self.resume_input_drop = args.resume_input_drop
        self.drag_regressions_only = args.drag_regressions_only
        selected_scopes = sum((self.existing_input, self.resume_input_drop, self.drag_regressions_only, args.finish))
        if selected_scopes > 1:
            raise RuntimeError("--existing-input, --resume-input-drop, --drag-regressions-only and --finish are separate acceptance scopes")
        if self.existing_input and not self.fixed_dev:
            raise RuntimeError("--existing-input requires the verified canonical Dev receipt")
        if self.resume_input_drop and not self.fixed_dev:
            raise RuntimeError("--resume-input-drop requires the verified canonical Dev receipt")
        if self.drag_regressions_only and not self.fixed_dev:
            raise RuntimeError("--drag-regressions-only requires the verified canonical Dev receipt")
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
        if self.config.exists() and not (args.finish or self.existing_input or self.resume_input_drop or self.drag_regressions_only):
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
        elif self.drag_regressions_only:
            self.output.mkdir()
            self.compile_fixture("PermissionAcceptance.swift", self.driver)
            self.compile_fixture("LiveAcceptanceAX.swift", self.ax)
            self.record("identity", {**identity, "mode": "drag-regressions-only"})
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

    def run_existing_input(self):
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
        nodes = wait_for(lambda: self.existing_input_panel(pid), "Dev permission window with Input Monitoring granted (1/2)")
        control_title = existing_input_target(nodes, self.name)
        if control_title is None:
            raise RuntimeError("--existing-input requires Input Monitoring granted (1/2) and only the device-control row pending")
        full_first_use = self.name + " 首次设置" in window_names(nodes)
        self.record("existing_input_preflight", {
            "app": pid, "settings": state["settings"], "front": state["front"],
            "page_before": state["page"], "input_already_granted": True,
            "granted_summary": "已授权 1/2", "target_row": control_title,
            "mode": "device-control segment only",
            "flow": "full-first-use" if full_first_use else "ordinary-settings",
        })

        press = self.press_permission_row(pid, control_title)
        self.record("permission_row_action", {
            "app": pid, "permission": "device-control", "press_started_at": press["ax_press_started_at"],
            "press_completed_at": press["ax_press_completed_at"], "source": "right-side 授权…"
        })
        ready = self.wait_auth_or("device-control Settings pane", ready_control)
        settings_pid = ready["settings"]
        self.record_window_latencies("device-control", press)
        self.record("settings_page_ready", {
            "permission": "device-control", "app": ready["app"], "settings": settings_pid,
            "front": ready["front"], "page": ready["page"], "observed_at": time.time()
        })
        self.record("native_drop", self.drag(
            "drag-control", permission="device-control", app_pid=pid,
            settings_pid=settings_pid, pages=CONTROL_PAGES,
        ))

        restart_observed = False
        restart_clicked = False

        def granted(state):
            return self.existing_input_segment_granted(
                state, pid, settings_pid, restart_observed, full_first_use
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
            "final_granted_summary": "全部已授权" if full_first_use else "已授权 2/2",
            "restart_prompt_observed": restart_observed,
            "same_process_authorized_ui_verified": not restart_observed,
            "verified_after_original_pid_exit": restart_observed,
            "full_first_use_verified": False
        })

    def existing_input_panel(self, pid):
        nodes = self.view(pid)
        return nodes if existing_input_target(nodes, self.name) else None

    def wait_auth_or(self, stage, ready):
        deadline = time.monotonic() + 12
        password_announced = False
        sheet_announced = False
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
            auth_visible = password_visible or (system_sheet and not recognized_restart)
            ready_result = None
            if not password_visible and (not system_sheet or recognized_restart):
                ready_result = ready(probe)
            if probe["password"]:
                if not password_announced:
                    self.record("NEEDS_PASSWORD", {"permission": stage})
                    print("请只在 macOS 系统窗口输入密码，脚本会自动继续。", file=sys.stderr, flush=True)
                    password_announced = True
                    deadline = None if self.auth_timeout == 0 else time.monotonic() + self.auth_timeout
            elif system_sheet and not recognized_restart:
                if not sheet_announced:
                    self.record("SYSTEM_SHEET_WAIT", {"permission": stage})
                    print(f"等待 macOS 的 {stage} 系统确认窗口完成。", file=sys.stderr, flush=True)
                    sheet_announced = True
                    deadline = None if self.auth_timeout == 0 else time.monotonic() + self.auth_timeout
            elif not auth_visible and (password_announced or sheet_announced) and deadline is None:
                # An unbounded wait is only for the visible authentication UI.
                # After it closes, wait briefly for the expected OS feedback and
                # report cancellation instead of polling forever.
                deadline = time.monotonic() + 12
            if ready_result:
                return ready_result
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
        input_press = self.press_permission_row(pid, INPUT_ROW_TITLE)
        self.record("permission_row_action", {
            "app": pid, "permission": "input-monitoring", "press_started_at": input_press["ax_press_started_at"],
            "press_completed_at": input_press["ax_press_completed_at"], "source": "right-side 授权…"
        })
        state = self.wait_auth_or("input-monitoring Settings pane", ready_input)
        settings_pid = state["settings"]
        self.record_window_latencies("input-monitoring", input_press)
        self.record("input_ready", state)
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
            if state["front"] != settings_pid:
                raise RuntimeError("the system restart prompt lost frontmost ownership")
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
            if (has_label(nodes, "已授权") and has_label(nodes, "已授权 1/2") and
                has_button(nodes, "在系统设置中管理设备控制和数据访问")):
                return state
            return None

        state = wait_for(resumed, "new PID with input monitoring granted and device control open")
        resumed_pid = state["app"]
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
        control_title = next((title for title in CONTROL_ROW_TITLES if has_button(self.view(resumed_pid), title)), None)
        if control_title is None:
            raise RuntimeError("the resumed permissions panel has no unique device-control title entry")
        control_press = self.press_permission_row(resumed_pid, control_title)
        self.record("permission_row_action", {
            "app": resumed_pid, "permission": "device-control", "press_started_at": control_press["ax_press_started_at"],
            "press_completed_at": control_press["ax_press_completed_at"], "source": "right-side 授权…"
        })
        state = self.wait_auth_or("device-control Settings pane", ready_control)
        self.record_window_latencies("device-control", control_press)
        self.record("device_control_ready", state)
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
                                       full_first_use=False):
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
                                   full_first_use=False):
    process_verified = existing_input_process_verified(
        state, initial_pid, settings_pid, restart_observed, original_alive, settings_alive,
        full_first_use=full_first_use,
    )
    if full_first_use:
        authorization_view = complete_permission_view(nodes, name)
    else:
        authorization_view = (name + " 系统权限" in window_names(nodes) and
                              has_label(nodes, "已授权 2/2"))
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


def existing_input_target(nodes, name):
    if name + " 系统权限" not in window_names(nodes) or not has_label(nodes, "已授权 1/2"):
        return None
    route_titles = {
        label for node in nodes if node.get("role") == "AXButton"
        for label in (node.get("title"), node.get("description"))
        if isinstance(label, str) and label.startswith("在系统设置中管理")
    }
    candidates = [title for title in CONTROL_ROW_TITLES if title in route_titles]
    if (len(route_titles) != 2 or INPUT_ROW_TITLE not in route_titles or len(candidates) != 1 or
        not has_button(nodes, PERMISSION_ACTION_LABEL)):
        return None
    return candidates[0]


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


def ready_input(state):
    if state["page"] in INPUT_PAGES and state["settings"] and state["front"] == state["settings"] and not state["password"]:
        return state
    return None


def ready_control(state):
    if state["page"] in CONTROL_PAGES and state["settings"] and state["front"] == state["settings"] and not state["password"]:
        return state
    return None


def has_button(nodes, label):
    return any(node["role"] == "AXButton" and label in (node["title"], node["description"]) and node["enabled"] for node in nodes)


def has_label(nodes, label):
    return any(label in (node["title"], node["value"], node["description"]) for node in nodes)


def window_names(nodes):
    return [node["title"] for node in nodes if node["role"] == "AXWindow"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", required=True, type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--fixed-dev-receipt", type=Path,
                        help="exercise the canonical Dev bundle after verifying all frozen artifacts")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--auth-timeout", default=120, type=int)
    parser.add_argument("--interact", required=True, action="store_true",
                        help="allow the isolated flow to use system settings and the pointer")
    parser.add_argument("--existing-input", action="store_true",
                        help="verify device control on a Dev with input monitoring already granted")
    parser.add_argument("--drag-regressions-only", action="store_true",
                        help="run only the non-granting native drag regressions on an already open Dev/Settings pair")
    parser.add_argument("--resume-input-drop", action="store_true",
                        help="continue after the recorded input-monitoring drop without repeating it")
    parser.add_argument("--finish", action="store_true",
                        help="verify and finish an existing run after its recorded device-control drop")
    args = parser.parse_args()
    try:
        acceptance = Acceptance(args)
        with acceptance.observe_feedback():
            if args.existing_input:
                acceptance.run_existing_input()
            elif args.drag_regressions_only:
                acceptance.run_drag_regressions_only()
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
