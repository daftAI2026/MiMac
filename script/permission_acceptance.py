#!/usr/bin/env python3
"""Run the native first-use permission flow for one frozen verification bundle."""

import argparse
from datetime import datetime
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
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
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
        if self.config.exists() and not args.finish:
            raise RuntimeError("use a fresh verification identity with an unused configuration directory")
        self.output = args.output.resolve()
        if not self.output.is_relative_to(CACHE.resolve()):
            raise RuntimeError("acceptance output must be in ~/Library/Caches/MiMac")
        self.auth_timeout = args.auth_timeout
        self.driver = self.output / "permission-driver"
        self.ax = self.output / "app-ax"
        identity = {"bundle": str(self.bundle), "bundle_id": self.identifier, "sha256": self.sha}
        if args.finish:
            entries = [json.loads(line) for line in (self.output / "run.ndjson").read_text().splitlines()]
            self.finish_args = finish_args(entries, identity)
            self.previous_handoff = self.confirm_past_handoff(entries[-1]["timestamp"])
            self.compile_fixture("PermissionAcceptance.swift", self.driver)
        else:
            self.previous_handoff = False
            self.output.mkdir()
            for name, destination in (("PermissionAcceptance.swift", self.driver), ("LiveAcceptanceAX.swift", self.ax)):
                self.compile_fixture(name, destination)
            self.record("identity", identity)

    def compile_fixture(self, name, destination):
        subprocess.run(["/usr/bin/xcrun", "swiftc", HERE / "fixtures" / name, "-o", destination], check=True)

    def confirm_past_handoff(self, since):
        # A late finish can run after the user has switched apps. Preserve the
        # actual handoff from the system trace instead of activating it again.
        predicate = (
            'process == "WindowServer" AND eventMessage CONTAINS "new foreground connection " '
            f'AND eventMessage ENDSWITH " {self.identifier}"'
        )
        result = subprocess.run([
            "/usr/bin/log", "show", "--start", datetime.fromtimestamp(since).strftime("%Y-%m-%d %H:%M:%S"),
            "--info", "--style", "ndjson", "--predicate", predicate,
        ], text=True, capture_output=True, check=True, timeout=12)
        return any(json.loads(line).get("eventMessage", "").endswith(" " + self.identifier)
                   for line in result.stdout.splitlines() if line.startswith("{"))

    def check_identity(self):
        subprocess.run(["/usr/bin/codesign", "--verify", "--deep", "--strict", self.bundle], check=True)
        if hashlib.sha256(self.executable.read_bytes()).hexdigest() != self.sha:
            raise RuntimeError("the executable does not match the frozen SHA-256")

    def probe(self, operation="probe"):
        return capture(self.driver, operation, self.bundle)

    def drag(self, operation):
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
        self.record("app_button", {"app": pid, "action": label, "result": result["status"]})

    def wait_auth_or(self, stage, ready):
        deadline = time.monotonic() + 12
        announced = False
        while time.monotonic() < deadline:
            probe = self.probe()
            if probe["password"]:
                if not announced:
                    self.record("NEEDS_PASSWORD", {"permission": stage})
                    print("请只在 macOS 系统窗口输入密码，脚本会自动继续。", file=sys.stderr, flush=True)
                    announced = True
                    deadline = time.monotonic() + self.auth_timeout
            elif result := ready(probe):
                return result
            time.sleep(0.18)
        raise RuntimeError(f"{stage}: expected system confirmation did not arrive")

    def run(self):
        if self.probe()["app"]:
            raise RuntimeError("the frozen verification app is already running")
        subprocess.run(["/usr/bin/open", self.bundle], check=True)
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
        route = "在系统设置中管理输入监控"
        wait_for(lambda: has_button(self.view(pid), route), "input-monitoring route")
        # Existing Settings may be routed by the card presentation itself.
        state = self.probe()
        if state["page"] not in INPUT_PAGES or state["front"] != state["settings"]:
            self.press(pid, route)
        state = wait_for(lambda: ready_input(self.probe()), "input-monitoring Settings pane")
        settings_pid = state["settings"]
        self.record("input_ready", state)
        self.record("native_drop", self.drag("drag-input"))
        state = self.wait_auth_or("input-monitoring", lambda p:
                                  p if p["app"] == pid and p["settings"] == settings_pid and p["restart"] else None)
        if state["front"] != settings_pid:
            raise RuntimeError("the system restart prompt lost frontmost ownership")
        self.record("system_restart", self.probe("restart"))

        def resumed():
            state = self.probe()
            if state["app"] in (pid, 0) or state["settings"] != settings_pid or state["page"] not in CONTROL_PAGES:
                return None
            nodes = self.view(state["app"])
            if has_label(nodes, "已授权") and has_button(nodes, "在系统设置中管理设备控制和数据访问"):
                return state
            return None

        state = wait_for(resumed, "new PID with input monitoring granted and device control open")
        resumed_pid = state["app"]
        self.record("resumed", state)
        if state["front"] != settings_pid or not state["settings_above_setup"]:
            raise RuntimeError("the first-use window took frontmost ownership or covered Settings")
        self.record("native_drop", self.drag("drag-control"))
        self.finish(pid, resumed_pid, settings_pid)

    def finish(self, pid, resumed_pid, settings_pid):
        progress = self.config / "onboarding.json"

        def granted(state):
            if (state["app"] != resumed_pid or pid_exists(settings_pid) or
                (state["front"] != resumed_pid and not self.previous_handoff)):
                return None
            nodes = self.view(resumed_pid)
            if complete_permission_view(nodes, self.name):
                return state
            return None

        state = self.wait_auth_or("device-control", granted)
        self.record("both_granted", {**state, "front_confirmed_by_log": self.previous_handoff})
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


def complete_permission_view(nodes, name):
    return (window_names(nodes) == [name + " 首次设置"] and
            has_button(nodes, "继续") and not has_button(nodes, "授权…") and
            (has_label(nodes, "已授权") or has_label(nodes, "全部已授权")))


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


def has_button(nodes, label):
    return any(node["role"] == "AXButton" and node["title"] == label and node["enabled"] for node in nodes)


def has_label(nodes, label):
    return any(label in (node["title"], node["value"], node["description"]) for node in nodes)


def window_names(nodes):
    return [node["title"] for node in nodes if node["role"] == "AXWindow"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", required=True, type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--auth-timeout", default=120, type=int)
    parser.add_argument("--interact", required=True, action="store_true",
                        help="allow the isolated flow to use system settings and the pointer")
    parser.add_argument("--finish", action="store_true",
                        help="verify and finish an existing run after its recorded device-control drop")
    args = parser.parse_args()
    try:
        acceptance = Acceptance(args)
        if args.finish:
            acceptance.finish(*acceptance.finish_args)
        else:
            acceptance.run()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"BLOCKED {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
