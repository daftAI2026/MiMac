#!/usr/bin/env python3
"""Prepare and close a recorded live speech acceptance run; never opens a mic.

prepare uses build_and_run.sh, checks this connection, opens a task-owned text
fixture and starts passive observations. Only READY permits asking for a remote
test. On BLOCKED follow its stage; do not keep asking the user to press again.
finish stops the owned run, checks full reports and reports each boundary.
"""
import argparse
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
import plistlib
import signal
import subprocess
import sys
import tempfile
import time

from inspect_voice_report import inspect as inspect_voice_report
from acceptance_evidence import summarize_text_evidence
from acceptance_observer import run_observer

ROOT = Path(__file__).resolve().parent.parent
CACHE = Path.home() / "Library/Caches/MiMac/live-acceptance"
SOURCES = ROOT / "script/fixtures"
TOOL_INPUT_SOURCES = {
    "voice-doubao": "com.bytedance.inputmethod.doubaoime.pinyin",
    "voice-wetype": "com.tencent.inputmethod.wetype.pinyin",
}


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    path = Path(path)
    temp = path.with_suffix(".tmp")
    temp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    temp.replace(path)


def run(*args, **kwargs):
    return subprocess.check_output([str(a) for a in args], text=True, **kwargs)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def exact_pids(executable):
    rows = run("/bin/ps", "-axo", "pid=,comm=").splitlines()
    return [int(f[0]) for row in rows if len(f := row.strip().split(None, 1)) == 2 and f[1] == str(executable)]


def wait_for(check, seconds=12):
    end = time.monotonic() + seconds
    while True:
        value = check()
        if value:
            return value
        if time.monotonic() >= end:
            raise RuntimeError("bounded readiness observation expired")
        time.sleep(.05)


def one_pid(executable):
    pids = exact_pids(executable)
    if len(pids) > 1:
        raise RuntimeError(f"multiple instances: {pids}")
    return pids[0] if pids else None


def command(directory, action):
    directory = Path(directory)
    seq = time.monotonic_ns()
    write(directory / "command.json", {"seq": seq, "action": action})
    if action == "quit":
        return None
    def response():
        try:
            value = read(directory / "result.json")
        except (FileNotFoundError, json.JSONDecodeError):
            return None
        return value if value.get("seq") == seq else None
    result = wait_for(response, 3)
    if result.get("error"):
        raise RuntimeError(result["error"])
    return result


def check_fixture(value, pid, expected_source, prepared=False):
    if value.get("pid") != pid or value.get("window_title") != "MiMac 语音验收":
        raise RuntimeError("fixture identity mismatch")
    if not value.get("focused") or value.get("has_marked_text"):
        raise RuntimeError("fixture is not the focused, settled text client")
    if value.get("app_active") is not True or value.get("frontmost_pid") != pid:
        raise RuntimeError("fixture is not the actual foreground application")
    if prepared and not value.get("action_ready"):
        raise RuntimeError("fixture preparation handshake missing")
    if expected_source and value.get("input_source_id") != expected_source:
        raise RuntimeError("system input source does not match the selected tool")


def route_ready(log):
    # A previous generation's ready=true cannot override a later failure.
    events = [line for line in log.splitlines() if "audio route: ready=" in line]
    return bool(events and "ready=true error=<nil>" in events[-1])


def validate_audio_target(value, pid, bundle_id):
    if value.get("reported_pid") != pid or value.get("running_input_status") != 0:
        raise RuntimeError("target audio identity/status is unverified")
    if value.get("bundle_id") != bundle_id:
        raise RuntimeError("target PID does not belong to the selected tool")
    if value.get("running_input") != 0 or value.get("input_devices") != []:
        raise RuntimeError("target is not idle")


def check_permission_snapshot(snapshot):
    if snapshot.get("status") != "ok" or snapshot.get("truncated"):
        raise RuntimeError("permission state is unverified: AX snapshot is incomplete")
    for node in snapshot.get("nodes", []):
        if node.get("role") == "AXStaticText" and node.get("value") in ("需要输入监控权限", "需要辅助功能权限"):
            raise RuntimeError("permission blocked: " + node["value"])


def request_quit_once(directory, pid):
    # MyGo exits immediately on a second signal, bypassing all reports.
    marker = Path(directory) / "quit-request.json"
    if not marker.exists():
        write(marker, {"time": time.time(), "pid": pid, "method": "single_SIGTERM"})
        os.kill(pid, signal.SIGTERM)


def prepare(args):
    CACHE.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=CACHE))
    stage = "preflight"
    state = {"schema": 1, "run": str(directory), "status": "PREPARING", "source_check": args.source_check}
    write(directory / "state.json", state)
    try:
        if sys.platform != "darwin":
            raise RuntimeError("macOS required")
        tools = read(Path.home() / "Library/Application Support/MiMac/tools.json")
        tool = tools.get("current")
        if tool not in tools.get("enabled", []):
            raise RuntimeError("current voice tool is not enabled")
        expected_source = TOOL_INPUT_SOURCES.get(tool)
        if tool not in [*TOOL_INPUT_SOURCES, "voice-apple-speech"]:
            raise RuntimeError("this tool has no acceptance contract yet")
        if tool in TOOL_INPUT_SOURCES and not args.target_pid:
            raise RuntimeError("--target-pid is required for input-method receiver evidence")
        if args.target_pid is not None and args.target_pid <= 0:
            raise RuntimeError("target PID must be positive")
        backend = args.audio_backend
        if tool in TOOL_INPUT_SOURCES and backend != "mimac-compat":
            raise RuntimeError("input-method route requires explicit --audio-backend mimac-compat")
        recovery = Path.home() / "Library/Application Support/MiMac/voice-mapping-recovery.plist"
        if recovery.exists():
            raise RuntimeError("disable the remote and finish its cleanup before building")
        go = args.go or os.environ.get("MIMAC_GO", "go")
        arch = run(go, "env", "GOARCH").strip()
        bundle = ROOT / f"dist/darwin-{arch}/MiMac.app"
        executable = bundle / "Contents/MacOS/MiMac"
        if args.reuse_build:
            if not args.previous_run:
                raise RuntimeError("--reuse-build requires --previous-run to prove the fixed bundle is unchanged")
            previous = read(args.previous_run / "identity.json")
            for name, expected in previous["bundle"].items():
                if digest(bundle / name) != expected:
                    raise RuntimeError(f"fixed bundle changed since previous run: {name}")
        if bundle.exists() and not args.reuse_build:
            stage = "backup"
            run("ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", bundle, directory / "previous-app.zip")
        stage = "build"
        environment = dict(os.environ, MIMAC_GO=go, MIMAC_RUN_STDOUT=str(directory / "stdout.log"), MIMAC_RUN_STDERR=str(directory / "stderr.log"))
        with (directory / "build.log").open("w") as log:
            app_arguments = ["--show-settings", "--audio-backend", backend, "--host-report", str(directory / "host.json"), "--voice-overlay-report", str(directory / "voice.json")]
            if args.source_check:
                app_arguments += ["--source-check-report", str(directory / "source-check.json")]
            if args.reuse_build:
                if exact_pids(executable):
                    raise RuntimeError("finish the previous App run before reusing its fixed binary")
                stage = "fixed_bundle_verification"
                subprocess.run([sys.executable, str(ROOT / "script/check_app_identity.py"), "--project-root", str(ROOT), "--arch", arch], stdout=log, stderr=subprocess.STDOUT, check=True)
                subprocess.run([sys.executable, str(ROOT / "script/verify_app_bundle.py"), str(bundle), "--arch", arch], stdout=log, stderr=subprocess.STDOUT, check=True)
                run("open", "-n", "--stdout", directory / "stdout.log", "--stderr", directory / "stderr.log", bundle, "--args", *app_arguments)
            else:
                subprocess.run([str(ROOT / "script/build_and_run.sh"), "run", "--", *app_arguments], env=environment, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, check=True)
        app_pid = wait_for(lambda: one_pid(executable))
        state.update(app_pid=app_pid, app_executable=str(executable), app_bundle=str(bundle), tool=tool, input_source=expected_source, target_pid=args.target_pid, audio_backend=backend)
        write(directory / "state.json", state)  # Persist process ownership before further probes.
        stage = "freeze"
        source_files = run("git", "-C", ROOT, "ls-files", "-co", "--exclude-standard").splitlines()
        write(directory / "identity.json", {
            "head": run("git", "-C", ROOT, "rev-parse", "HEAD").strip(),
            "dirty": run("git", "-C", ROOT, "status", "--porcelain"),
            "sources": {name: digest(ROOT / name) for name in sorted(set(source_files)) if (ROOT / name).is_file()},
            "bundle": {str(p.relative_to(bundle)): digest(p) for p in [executable, *sorted((bundle / "Contents/Resources").glob("*.dylib"))]},
            "app_pid": app_pid, "executable_inode": executable.stat().st_ino,
            "system": run("sw_vers"),
        })
        mapped = run("lsof", "-a", "-p", app_pid, "-d", "txt", "-Fi")
        if f"i{executable.stat().st_ino}" not in mapped.splitlines():
            raise RuntimeError("App PID does not map the frozen executable inode")
        stage = "diagnostic_tools"
        fixture = CACHE / "MiMacLiveSpeechHost.app"
        host_exe = fixture / "Contents/MacOS/MiMacLiveSpeechHost"
        if exact_pids(host_exe):
            raise RuntimeError("another live acceptance fixture is active; finish it first")
        host_exe.parent.mkdir(parents=True, exist_ok=True)
        (fixture / "Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier": "dev.mimac.verification.live-speech", "CFBundleExecutable": host_exe.name, "CFBundleName": "MiMac 语音验收", "CFBundlePackageType": "APPL", "NSHighResolutionCapable": True}))
        probe = CACHE / "LiveAcceptanceAX"
        frozen = directory / "diagnostic-sources"
        frozen.mkdir()
        for name in ["LiveSpeechHost.swift", "LiveAcceptanceAX.swift"]:
            (frozen / name).write_bytes((SOURCES / name).read_bytes())
        run("swiftc", frozen / "LiveSpeechHost.swift", "-o", host_exe)
        run("swiftc", frozen / "LiveAcceptanceAX.swift", "-o", probe)
        run("codesign", "--force", "--sign", "-", fixture, stderr=subprocess.STDOUT)
        state.update(probe=str(probe), fixture_executable=str(host_exe), fixture_bundle=str(fixture))
        write(directory / "diagnostics.json", {"host_sha256": digest(host_exe), "probe_sha256": digest(probe), "compiled_sources": {p.name: digest(p) for p in frozen.glob("*.swift")}})
        stage = "permission_and_connection"
        run(probe, app_pid, "press", "AXButton", "设备", timeout=8)
        run(probe, app_pid, "press", "AXButton", "启用遥控器", timeout=8)
        permission_snapshot = json.loads(run(probe, app_pid, "snapshot", timeout=8))
        write(directory / "permission-check.json", permission_snapshot)
        check_permission_snapshot(permission_snapshot)
        def connected():
            log = (directory / "stderr.log").read_text() if (directory / "stderr.log").exists() else ""
            # Persistent errors are recorded, never worked around by another toggle.
            return route_ready(log)
        wait_for(connected, 15)
        if args.source_check and "source check: model_ready=true" not in (directory / "stderr.log").read_text():
            raise RuntimeError("local source-check model was not verified ready")
        stage = "target_idle"
        if args.target_pid:
            target = json.loads(run(probe, args.target_pid, "audio", timeout=3))
            validate_audio_target(target, args.target_pid, expected_source.removesuffix(".pinyin"))
            write(directory / "target-before.json", target)
            target_executable = Path(run("ps", "-p", args.target_pid, "-o", "comm=").strip())
            state.update(target_executable=str(target_executable), target_executable_sha256=digest(target_executable))
        stage = "fixture_prepare"
        field = directory / "field"
        field.mkdir()
        state.update(field=str(field))
        write(directory / "state.json", state)
        run("open", "-n", fixture, "--args", field)
        fixture_pid = wait_for(lambda: one_pid(host_exe))
        state.update(fixture_pid=fixture_pid)
        write(directory / "state.json", state)
        wait_for(lambda: (field / "pid").exists())
        ready = command(field, "prepare")
        check_fixture(ready, fixture_pid, expected_source, prepared=True)
        if not route_ready((directory / "stderr.log").read_text()):
            raise RuntimeError("audio route changed during fixture preparation")
        state.update(fixture_pid=fixture_pid, field=str(field), baseline_text=ready["text"])
        write(directory / "prepared.json", ready)
        stage = "observe"
        state["observer_schema"] = 2
        write(directory / "state.json", state)
        with (directory / "observer.log").open("w") as log:
            observer = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "observe", str(directory)], stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        state.update(observer_pid=observer.pid, status="READY", stage="awaiting_user", note="No microphone or shortcut was triggered. Keep this fixture focused while testing.")
        write(directory / "state.json", state)
        print(json.dumps(state, ensure_ascii=False))
        return 0
    except Exception as error:
        state.update(status="BLOCKED", stage=stage, error=str(error), note="Do not notify the user to press. Inspect this stage, then use finish to retire owned processes; permission refresh requires the App stopped.")
        write(directory / "state.json", state)
        print(json.dumps(state, ensure_ascii=False))
        return 1


def observe(directory):
    directory = Path(directory)
    state = read(directory / "state.json")
    result_path = directory / "observer-result.json"
    write(result_path, {"status": "RUNNING", "worker_joined": False})
    try:
        run_observer(
            directory, state,
            read_fixture=lambda value: command(value["field"], "snapshot"),
            read_target=(lambda value: json.loads(run(value["probe"], value["target_pid"], "audio", timeout=3))) if state.get("target_pid") else None,
            stop=lambda: (directory / "stop-observer").exists(),
            max_duration=1800,
        )
    except BaseException as error:
        write(result_path, {"status": "FAILED", "worker_joined": False, "error": str(error)})
        raise
    write(result_path, {"status": "STOPPED", "worker_joined": True, "time": time.time()})


def target_interval(row):
    # New rows bound the actual query. Legacy rows have only their recorded time.
    start, end = row.get("query_start_time", row.get("time", 0)), row.get("query_end_time", row.get("time", 0))
    if any(type(value) not in (int, float) or not math.isfinite(value) for value in (start, end)):
        raise RuntimeError("target query timestamps are unverified")
    return start, end


def fresh_target(rows, now):
    if not rows:
        raise RuntimeError("no target audio observation yet")
    row = rows[-1]
    start, end = target_interval(row)
    if "error" in row or row.get("status", "ok") != "ok" or not (0 <= now - end <= .5 and start <= end <= now):
        raise RuntimeError("target audio observation is stale or failed")
    return row["target"]


def assess(host, voice, baseline, final, timeline, expected_source, fixture_pid=None, target_timeline=None):
    checks = {"normal_exit": host.get("normal_exit") is True, "keys_released": host.get("core", {}).get("pressed") == 0, "new_text": isinstance(final, str) and final.startswith(baseline) and len(final) > len(baseline)}
    sessions = voice.get("io", {}).get("sessions", [])
    checks["sessions_present"] = bool(sessions)
    checks["single_planned_session"] = len(sessions) == 1
    checks["audio_delivery"] = inspect_voice_report(voice)["local_delivery"] == "complete"
    coverage = []
    for session in sessions:
        start = dt.datetime.fromisoformat(session["first_started_at"]).timestamp()
        end = dt.datetime.fromisoformat(session["last_finished_at"]).timestamp()
        commits = [row.get("time", 0) for row in timeline if row.get("time", 0) >= start and isinstance(row.get("field", {}).get("text"), str) and row["field"]["text"].startswith(baseline) and len(row["field"]["text"]) > len(baseline) and row["field"].get("has_marked_text") is False]
        if commits:
            end = max(end, min(commits))
        samples = [x for x in timeline if start - .3 <= x.get("time", 0) <= end + .3]
        times = sorted(x["time"] for x in samples)
        covers_interval = bool(times) and times[0] <= start + .3 and times[-1] >= end - .3 and all(b - a <= .5 for a, b in zip(times, times[1:]))
        coverage.append(covers_interval and all(x.get("field", {}).get("focused") is True and x["field"].get("app_active") is True and x["field"].get("window_title") == "MiMac 语音验收" and x["field"].get("frontmost_pid") == x["field"].get("pid") and (fixture_pid is None or x["field"].get("pid") == fixture_pid) and (not expected_source or x["field"].get("input_source_id") == expected_source) and "error" not in x for x in samples))
    checks["session_focus_evidence"] = bool(coverage) and all(coverage)
    if expected_source:
        target_rows = timeline if target_timeline is None else target_timeline
        bundle = expected_source.removesuffix(".pinyin")
        checks["target_route_evidence"] = bool(sessions) and all(
            any(
                dt.datetime.fromisoformat(s["first_started_at"]).timestamp() <= target_interval(row)[0] <= target_interval(row)[1] <= dt.datetime.fromisoformat(s["last_finished_at"]).timestamp()
                and "error" not in row and row.get("status", "ok") == "ok"
                and row.get("target", {}).get("bundle_id") == bundle
                and row.get("target", {}).get("running_input_status") == 0
                and row.get("target", {}).get("running_input") == 1
                and [d.get("uid") for d in row.get("target", {}).get("input_devices", [])] == ["dev.mimac.audio.compat.v1"]
                for row in target_rows
            ) for s in sessions
        )
    return {"status": "PASS" if all(checks.values()) else "NOT_PASSED", "checks": checks, "scope": "one planned voice session: actual text append, local audio acceptance, bounded sampled focus and cleanup; not every target-consumed sample, transcript accuracy or Siri appearance"}


def assess_source_diagnostic(diagnostic, voice):
    sessions = voice.get("io", {}).get("sessions", [])
    if len(sessions) != 1:
        return False
    session = sessions[0]
    expected = session.get("source_read_samples", 0)
    stats = diagnostic.get("stats", {})
    return (
        expected > 0
        and diagnostic.get("session_count") == 1
        and diagnostic.get("session_token_id") == session.get("session")
        and diagnostic.get("device_generation") == session.get("generation")
        and diagnostic.get("source_eof") is True
        and diagnostic.get("source_close_succeeded") is True
        and diagnostic.get("complete_capture") is True
        and diagnostic.get("replay_started") is True
        and diagnostic.get("replay_completed") is True
        and diagnostic.get("replay_sample_count") == expected
        and stats.get("ObservedSamples") == stats.get("RetainedSamples") == expected
        and stats.get("OverflowSamples") == 0
        and not diagnostic.get("errors")
        and not diagnostic.get("apple_sink_retained")
    )


def observer_alive(directory, pid):
    result = subprocess.run(["ps", "-p", str(pid), "-o", "args="], capture_output=True, text=True, timeout=3)
    return result.returncode == 0 and str(directory) in result.stdout and "live_acceptance.py observe" in result.stdout


def status(directory):
    directory, state = managed_run(directory)
    if state.get("status") != "READY" or exact_pids(state["app_executable"]) != [state["app_pid"]] or exact_pids(state["fixture_executable"]) != [state["fixture_pid"]]:
        raise RuntimeError("prepared App/fixture run is not active")
    if state.get("observer_schema") == 2:
        if not state.get("observer_pid") or not observer_alive(directory, state["observer_pid"]) or read(directory / "observer-result.json").get("status") != "RUNNING":
            raise RuntimeError("live observer is not running")
    rows = (directory / "timeline.jsonl").read_text().splitlines()
    if not rows:
        raise RuntimeError("no live observation yet")
    latest = json.loads(rows[-1])
    if time.time() - latest["time"] > .5 or "error" in latest:
        raise RuntimeError("live observation is stale or failed")
    check_fixture(latest["field"], state["fixture_pid"], state.get("input_source"))
    if not route_ready((directory / "stderr.log").read_text()):
        raise RuntimeError("current audio connection is not ready")
    if state.get("target_pid"):
        if digest(state["target_executable"]) != state["target_executable_sha256"]:
            raise RuntimeError("target executable changed after preparation")
        target_path = directory / "target-timeline.jsonl"
        target_rows = [json.loads(line) for line in target_path.read_text().splitlines()] if target_path.exists() else [latest]
        target = fresh_target(target_rows, time.time())
        validate_audio_target(target, state["target_pid"], state["input_source"].removesuffix(".pinyin"))
        latest = {**latest, "target": target, "target_observation": target_rows[-1]}
    print(json.dumps({"status": "READY", "run": str(directory), "observation": latest}, ensure_ascii=False))
    return 0


def managed_run(directory):
    directory = Path(directory).resolve()
    if directory.parent != CACHE.resolve():
        raise RuntimeError("not a managed live-acceptance run")
    state = read(directory / "state.json")
    if Path(state.get("run", "")).resolve() != directory:
        raise RuntimeError("run identity mismatch")
    if state.get("app_executable"):
        executable = Path(state["app_executable"]).resolve()
        executable.relative_to((ROOT / "dist").resolve())
        if executable.name != "MiMac" or executable.parent.name != "MacOS":
            raise RuntimeError("unexpected App executable")
    return directory, state


def stop_observer(directory, state, errors):
    write(directory / "stop-observer", {"at": time.time()})
    pid = state.get("observer_pid")
    if not pid:
        return True

    def alive():
        return observer_alive(directory, pid)

    try:
        if not alive():
            return True
        try:
            # A fixture RPC (3s) plus target-worker join (4s) may be active.
            wait_for(lambda: not alive(), 8)
            return True
        except Exception as error:
            errors.append("observer graceful cleanup: " + str(error))
        try:
            if alive():
                # Only this diagnostic session group; never the MyGo App/target.
                if os.getpgid(pid) != pid:
                    raise RuntimeError("observer session group identity mismatch")
                if alive():
                    os.killpg(pid, signal.SIGTERM)
                wait_for(lambda: not alive(), 2)
        except ProcessLookupError:
            pass  # Exited between identity check and process-group lookup.
        except Exception as error:
            errors.append("observer forced cleanup: " + str(error))
        return not alive()
    except Exception as error:
        errors.append("observer cleanup identity: " + str(error))
        return False


def finish(directory):
    directory, state = managed_run(directory)
    if state.get("status") == "FINISHED":
        result = read(directory / "acceptance.json")
        print(json.dumps(result, ensure_ascii=False))
        return 0 if result["status"] == "PASS" else 1
    if state.get("app_bundle") and (directory / "identity.json").exists():
        identity = read(directory / "identity.json")
        for name, expected in identity["bundle"].items():
            if digest(Path(state["app_bundle"]) / name) != expected:
                raise RuntimeError("frozen bundle changed; preserve run before cleanup")
    errors = []
    if not stop_observer(directory, state, errors):
        # An active observer still owns fixture RPC. Never create a second writer.
        result = {"status": "NOT_PASSED", "stage": "observer_cleanup", "cleanup_errors": errors,
                  "remaining_app_pids": exact_pids(state["app_executable"]) if state.get("app_executable") else [],
                  "remaining_fixture_pids": exact_pids(state["fixture_executable"]) if state.get("fixture_executable") else []}
        write(directory / "acceptance.json", result)
        state.update(status="BLOCKED", stage="observer_cleanup", acceptance="NOT_PASSED")
        write(directory / "state.json", state)
        print(json.dumps(result, ensure_ascii=False))
        return 1
    final = None
    post_cleanup = None
    text_events = []
    fixture_owned = state.get("fixture_pid") and exact_pids(state["fixture_executable"]) == [state["fixture_pid"]]
    try:
        if fixture_owned:
            # A retry must not reclassify late text as pre-cleanup success.
            final_path = directory / "final-field.json"
            if final_path.exists():
                final = read(final_path)
            else:
                final = command(state["field"], "snapshot")
                write(final_path, final)
    except Exception as error:
        errors.append("final field: " + str(error))
    try:
        if state.get("app_pid") in exact_pids(state["app_executable"]):
            # App.OnBeforeQuit owns the audio shutdown. An AX remote stop here
            # races that same owner. MyGo's SECOND signal exits immediately,
            # so persist the attempt before sending and never send it twice.
            request_quit_once(directory, state["app_pid"])
            wait_for(lambda: state["app_pid"] not in exact_pids(state["app_executable"]), 45 if state.get("source_check") else 12)
    except Exception as error:
        errors.append("App cleanup: " + str(error))
    finally:
        if fixture_owned:
            try:
                post_cleanup = command(state["field"], "snapshot")
                write(directory / "post-cleanup-field.json", post_cleanup)
            except Exception as error:
                errors.append("post-cleanup field: " + str(error))
            # Keep a failed App's text client alive for late results/recovery.
            # A later finish waits on the original single quit, never signals twice.
            if state.get("app_pid") not in exact_pids(state["app_executable"]):
                try:
                    command(state["field"], "quit")
                    wait_for(lambda: state["fixture_pid"] not in exact_pids(state["fixture_executable"]), 5)
                except Exception as error:
                    errors.append("fixture cleanup: " + str(error))
            try:
                events_path = Path(state["field"]) / "events.jsonl"
                text_events = [json.loads(line) for line in events_path.read_text().splitlines()] if events_path.exists() else []
            except Exception as error:
                errors.append("text event evidence: " + str(error))
    try:
        def reports():
            try:
                return read(directory / "host.json"), read(directory / "voice.json")
            except (FileNotFoundError, json.JSONDecodeError):
                return None
        host, voice = wait_for(reports, 5)
        timeline_path = directory / "timeline.jsonl"
        timeline = [json.loads(line) for line in timeline_path.read_text().splitlines()] if timeline_path.exists() else []
        target_path = directory / "target-timeline.jsonl"
        target_timeline = [json.loads(line) for line in target_path.read_text().splitlines()] if target_path.exists() else None
        result = assess(host, voice, state.get("baseline_text", ""), final.get("text") if final else None, timeline, state.get("input_source"), state.get("fixture_pid"), target_timeline)
        result["checks"]["final_text_settled"] = final is not None and final.get("has_marked_text") is False
        if state.get("target_pid") and state.get("probe"):
            target = json.loads(run(state["probe"], state["target_pid"], "audio", timeout=3))
            write(directory / "target-after.json", target)
            validate_audio_target(target, state["target_pid"], state["input_source"].removesuffix(".pinyin"))
        if state.get("source_check"):
            diagnostic = read(directory / "source-check.json")
            result["source_check"] = diagnostic
            result["checks"]["source_diagnostic_complete"] = assess_source_diagnostic(diagnostic, voice)
        if not all(result["checks"].values()):
            result["status"] = "NOT_PASSED"
    except Exception as error:
        result = {"status": "NOT_PASSED", "error": str(error), "stage": "report_analysis"}
    if state.get("observer_schema") == 2:
        try:
            observer = read(directory / "observer-result.json")
            result["observer"] = observer
            result.setdefault("checks", {})["observer_stopped_cleanly"] = observer.get("status") == "STOPPED" and observer.get("worker_joined") is True
        except Exception as error:
            result.setdefault("checks", {})["observer_stopped_cleanly"] = False
            errors.append("observer result: " + str(error))
        if not result["checks"]["observer_stopped_cleanly"]:
            result["status"] = "NOT_PASSED"
    result["text_evidence"] = summarize_text_evidence(state.get("baseline_text", ""), final, post_cleanup, text_events, state.get("fixture_pid"))
    if errors:
        result.update(status="NOT_PASSED", cleanup_errors=errors)
    result["remaining_app_pids"] = exact_pids(state["app_executable"]) if state.get("app_executable") else []
    result["remaining_fixture_pids"] = exact_pids(state["fixture_executable"]) if state.get("fixture_executable") else []
    if result["remaining_app_pids"] or result["remaining_fixture_pids"]:
        result["status"] = "NOT_PASSED"
    write(directory / "acceptance.json", result)
    if result["remaining_app_pids"] or result["remaining_fixture_pids"]:
        state.update(status="BLOCKED", stage="cleanup", acceptance=result["status"], note="Owned cleanup is pending. Preserve the fixture and original boundary; do not send a second quit signal.")
    else:
        state.update(status="FINISHED", acceptance=result["status"])
    write(directory / "state.json", state)
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result["status"] == "PASS" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    p = commands.add_parser("prepare", help="build, verify and prepare; no microphone/shortcut")
    p.add_argument("--go")
    p.add_argument("--source-check", action="store_true", help="explicit in-memory same-source local ASR diagnostic on App exit; never writes PCM")
    p.add_argument("--reuse-build", action="store_true", help="verify/relaunch the existing stopped binary after permission refresh; never rebuild")
    p.add_argument("--previous-run", type=Path, help="previous run whose bundle SHA values must match for --reuse-build")
    p.add_argument("--audio-backend", default="", choices=["", "mimac-compat"])
    p.add_argument("--target-pid", type=int)
    for action in ["observe", "finish", "status"]:
        commands.add_parser(action).add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.action == "prepare":
        return prepare(args)
    if args.action == "observe":
        observe(args.directory)
        return 0
    if args.action == "status":
        return status(args.directory)
    return finish(args.directory)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "BLOCKED", "error": str(error)}, ensure_ascii=False))
        raise SystemExit(1)
