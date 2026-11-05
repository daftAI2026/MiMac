#!/usr/bin/env python3
"""Verify persisted backend selection through a fixed App's real startup.

Requires MiMac to be stopped. Restores only the backend sidecar's original
bytes; never enables the remote, sends a shortcut, or starts audio capture.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import time


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() if path.exists() else None


def running(executable):
    rows = subprocess.check_output(["/bin/ps", "-axo", "pid=,comm="], text=True)
    return [int(parts[0]) for row in rows.splitlines()
            if len(parts := row.strip().split(None, 1)) == 2 and parts[1] == str(executable)]


def process_identity(pid):
    """Use the unique report argument and process start time, not path alone."""
    def field(name):
        result = subprocess.run(["/bin/ps", "-p", str(pid), "-o", name + "="],
                                capture_output=True, text=True, check=False)
        return result.stdout.strip() if result.returncode == 0 else None
    return field("command"), field("lstart")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--result-dir", type=Path, required=True)
    args = parser.parse_args()
    bundle = args.bundle.resolve(strict=True)
    executable = bundle / "Contents/MacOS/MiMac"
    if running(executable):
        raise RuntimeError("stop the fixed MiMac App before this verification")
    if (Path.home() / "Library/Application Support/MiMac/voice-mapping-recovery.plist").exists():
        raise RuntimeError("remote recovery is still pending; do not start the restart matrix")
    verify = ["python3", str(Path(__file__).with_name("verify_app_bundle.py")),
              str(bundle), "--arch", {"arm64": "arm64", "x86_64": "amd64"}[platform.machine()]]
    if (bundle / "Contents/Resources/libSiriGlassBridge.dylib").exists():
        verify.append("--require-siri")
    subprocess.run(verify, check=True, capture_output=True)
    args.result_dir.mkdir(parents=True, exist_ok=False)
    result_dir = args.result_dir.resolve()
    config_dir = Path.home() / "Library/Application Support/MiMac"
    sidecar = config_dir / "audio-backend.json"
    if sidecar.is_symlink():
        raise RuntimeError("refuse a symlinked backend preference")
    original = sidecar.read_bytes() if sidecar.exists() else None
    original_mode = sidecar.stat().st_mode & 0o777 if sidecar.exists() else None
    last_owned = original
    unchanged = {name: digest(config_dir / name)
                 for name in ("preferences.json", "tools.json", "mappings.json")}
    identity = digest(executable)
    summary = {"bundle": str(bundle), "executable_sha256": identity,
               "original_sidecar_exists": original is not None, "cases": []}
    current_launcher = None
    owned_pid = None
    owned_identity = None

    def assert_owned_sidecar():
        current = sidecar.read_bytes() if sidecar.exists() else None
        if current != last_owned:
            raise RuntimeError("backend preference changed externally; leave it untouched")

    def save(value):
        nonlocal last_owned
        assert_owned_sidecar()
        subprocess.run([str(executable), "--set-audio-backend", value],
                       check=True, capture_output=True, timeout=10)
        last_owned = sidecar.read_bytes()
        assert json.loads(last_owned).get("backend", "") == value
        assert sidecar.stat().st_mode & 0o777 == 0o600
        assert not running(executable), "setter unexpectedly started the GUI host"

    def launch(name, expected, extra=()):
        nonlocal current_launcher, owned_pid, owned_identity
        assert_owned_sidecar()
        assert digest(executable) == identity
        assert not running(executable)
        report = result_dir / (name + ".json")
        command = ["/usr/bin/open", "-n", "-W", str(bundle), "--args",
                   "--smoke-duration", "3s", "--host-report", str(report), *extra]
        expected_command = " ".join([str(executable), "--smoke-duration", "3s",
                                     "--host-report", str(report), *extra])
        current_launcher = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 15
        observed = None
        while current_launcher.poll() is None and time.monotonic() < deadline:
            pids = running(executable)
            if len(pids) > 1:
                raise RuntimeError("more than one fixed App instance appeared")
            if pids and observed is None:
                candidate_identity = process_identity(pids[0])
                if candidate_identity[0] != expected_command or not candidate_identity[1]:
                    raise RuntimeError("another launch appeared; do not claim or stop that process")
                owned_pid, owned_identity = pids[0], candidate_identity
                observed = f"{owned_pid} {candidate_identity[0]}"
            time.sleep(0.05)
        stdout, stderr = current_launcher.communicate(timeout=1)
        (result_dir / (name + ".launcher.json")).write_text(json.dumps({
            "command": command, "pid": owned_pid, "observed_process": observed,
            "observed_process_identity": owned_identity,
            "returncode": current_launcher.returncode,
            "stdout": stdout.decode(errors="replace"), "stderr": stderr.decode(errors="replace")
        }, ensure_ascii=False, indent=2) + "\n")
        assert current_launcher.returncode == 0 and observed
        assert not running(executable)
        current_launcher = None
        owned_pid = None
        owned_identity = None
        value = json.loads(report.read_text())
        assert value["host_ready"] and value["tray_created"] and value["normal_exit"], value
        assert value["audio_backend"] == expected, value
        assert not value.get("monitor_starts", 0), "remote monitoring unexpectedly started"
        assert not value["core"].get("audio_session_id", 0), "audio capture unexpectedly started"
        assert not value.get("audio_error") and not value.get("audio_cleanup_failed"), value
        assert_owned_sidecar()
        summary["cases"].append({"case": name, "expected_backend": expected, "passed": True})

    try:
        if original is not None:
            sidecar.unlink()
            last_owned = None
        launch("01-absent-default", "")
        save("mimac-compat")
        launch("02-saved-normal-start", "mimac-compat")
        launch("03-empty-one-run-override", "", ["--audio-backend="])
        launch("04-normal-restart-after-override", "mimac-compat")
        save("")
        launch("05-cleared-normal-start", "")
    finally:
        if (owned_pid is not None and owned_pid in running(executable)
                and process_identity(owned_pid) == owned_identity):
            os.kill(owned_pid, signal.SIGTERM)
            deadline = time.monotonic() + 5
            while (owned_pid in running(executable) and process_identity(owned_pid) == owned_identity
                   and time.monotonic() < deadline):
                time.sleep(0.05)
            if owned_pid in running(executable) and process_identity(owned_pid) == owned_identity:
                raise RuntimeError("owned App did not stop; preserve evidence and do not relaunch")
        if current_launcher is not None and current_launcher.poll() is None:
            current_launcher.terminate()
            current_launcher.communicate(timeout=5)
        assert_owned_sidecar()
        if original is None:
            sidecar.unlink(missing_ok=True)
        else:
            temporary = sidecar.with_name(".backend-verification-restore")
            with temporary.open("xb") as stream:
                os.chmod(temporary, original_mode)
                stream.write(original)
                stream.flush()
                os.fsync(stream.fileno())
            temporary.replace(sidecar)
        summary["sidecar_restored"] = (sidecar.read_bytes() if sidecar.exists() else None) == original
        summary["other_preferences_unchanged"] = all(
            digest(config_dir / name) == sha for name, sha in unchanged.items())
        summary["executable_unchanged"] = digest(executable) == identity
        (result_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    assert len(summary["cases"]) == 5 and all(summary[name] for name in (
        "sidecar_restored", "other_preferences_unchanged", "executable_unchanged")), summary
    print(json.dumps(summary))


if __name__ == "__main__":
    main()
