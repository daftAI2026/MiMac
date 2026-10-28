#!/usr/bin/env python3
"""Run the production text adapter against a disposable Chrome/Electron page.

No audio, account, clipboard, Return or privacy prompts. Text events target only
the captured fixture PID. Existing browser
profiles and windows are not used. Requires already granted Accessibility access.
"""
import argparse
from dataclasses import dataclass
import http.server
import json
import os
from pathlib import Path
import plistlib
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid


@dataclass(frozen=True)
class ProcessInfo:
    pid: int
    ppid: int
    started: str
    command: str


def parse_process_table(output):
    processes = {}
    for line in output.splitlines():
        fields = line.strip().split(None, 7)
        if len(fields) != 8:
            continue
        try:
            pid, ppid = int(fields[0]), int(fields[1])
        except ValueError:
            continue
        processes[pid] = ProcessInfo(pid, ppid, " ".join(fields[2:7]), fields[7])
    return processes


def process_table():
    output = subprocess.check_output(
        ["ps", "-axo", "pid=,ppid=,lstart=,command="], text=True,
        stderr=subprocess.DEVNULL,
    )
    return parse_process_table(output)


def main_command(command, executable):
    executable = str(executable)
    return command == executable or command.startswith(executable + " ")


def find_owned(processes, work, bundle, executable):
    """Find this run's exact app root and descendants, excluding user browsers."""
    work = str(work)
    owned, depth, frontier = {}, {}, []
    for pid, proc in processes.items():
        if main_command(proc.command, executable) and work in proc.command:
            owned[pid], depth[pid] = proc, 0
            frontier.append(pid)

    def add_descendants():
        level = max(depth.values(), default=-1) + 1
        while frontier:
            parents = set(frontier)
            frontier.clear()
            for pid, proc in processes.items():
                if pid not in owned and proc.ppid in parents:
                    owned[pid], depth[pid] = proc, level
                    frontier.append(pid)
            level += 1

    add_descendants()
    # A helper reparented before our scan is owned only when its command still
    # carries both this exact bundle path and this run's unique work directory.
    for pid, proc in processes.items():
        if (pid not in owned and proc.command.startswith(str(bundle) + "/Contents/")
                and work in proc.command):
            owned[pid], depth[pid] = proc, 0
            frontier.append(pid)
    add_descendants()
    return owned, depth


def fixture_pid(owned, work, executable, pid_file):
    roots = {pid: proc for pid, proc in owned.items()
             if main_command(proc.command, executable) and str(work) in proc.command}
    try:
        candidate = int(pid_file.read_text().strip())
        if candidate in roots:
            return candidate
    except (OSError, ValueError):
        pass
    top = [proc.pid for proc in roots.values() if proc.ppid not in roots]
    return top[0] if len(top) == 1 else next(iter(roots), 0) if len(roots) == 1 else 0


def update_owned_tracking(tracked, processes, work, bundle, executable):
    discovered, depths = find_owned(processes, work, bundle, executable)
    for pid, (old, old_depth, safe) in list(tracked.items()):
        proc = processes.get(pid)
        if proc is None or proc.started != old.started:
            tracked.pop(pid)
        elif (proc.command != old.command or proc.ppid != old.ppid
              or pid not in discovered):
            tracked[pid] = (old, old_depth, False)
    for pid, proc in discovered.items():
        previous = tracked.get(pid)
        if previous is None or previous[0].started != proc.started:
            tracked[pid] = (proc, depths[pid], True)
        elif (previous[2] and previous[0].command == proc.command
              and previous[0].ppid == proc.ppid):
            tracked[pid] = (proc, depths[pid], True)
    return discovered


def stop_owned(work, bundle, executable, tracked, *, snapshot=process_table,
               send_signal=os.kill, now=time.monotonic, sleep=time.sleep):
    """Stop only discovered owned processes; retain identities across cleanup scans.

    `tracked` is shared across the pre- and post-launcher scans. If a known child
    becomes reparented or changes argv, it stays uncertain until it is observed
    gone; an empty later discovery cannot silently forget it.
    """
    def refresh():
        current = snapshot()
        discovered = update_owned_tracking(tracked, current, work, bundle, executable)
        return current, discovered

    for sig, duration in ((signal.SIGTERM, 3.0), (signal.SIGKILL, 2.0)):
        sent = set()
        deadline = now() + duration
        while True:
            current, discovered = refresh()
            for pid, (old, depth, safe) in sorted(
                    tracked.items(), key=lambda item: item[1][1], reverse=True):
                if not safe or pid in sent or pid not in discovered:
                    continue
                proc = current.get(pid)
                # Re-read immediately before signalling, checking start time,
                # exact argv, parent and current owned ancestry against PID reuse.
                latest = snapshot()
                fresh = latest.get(pid)
                fresh_owned, _ = find_owned(latest, work, bundle, executable)
                if (proc is None or fresh is None or pid not in fresh_owned
                        or fresh.started != old.started or fresh.command != old.command
                        or fresh.ppid != old.ppid):
                    if fresh is None or (fresh is not None and fresh.started != old.started):
                        tracked.pop(pid, None)
                    else:
                        tracked[pid] = (old, depth, False)
                    continue
                try:
                    send_signal(pid, sig)
                    sent.add(pid)
                except ProcessLookupError:
                    pass
                except OSError:
                    pass  # Keep it tracked and preserve the profile if unconfirmed.

            refresh()
            if not tracked:
                return True, []
            if now() >= deadline:
                break
            sleep(min(.1, max(0.0, deadline - now())))

    refresh()
    return not tracked, sorted(tracked)


def stop_launcher(launcher):
    if launcher is None:
        return True
    try:
        launcher.wait(timeout=3)
        return True
    except subprocess.TimeoutExpired:
        pass
    try:
        launcher.terminate()
        launcher.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            launcher.kill()
            launcher.wait(timeout=2)
        except (subprocess.TimeoutExpired, OSError):
            return launcher.poll() is not None
    except OSError:
        return launcher.poll() is not None
    return launcher.poll() is not None


def close_server(server, thread):
    if server is None:
        return True
    try:
        if thread and thread.is_alive():
            server.shutdown()
        server.server_close()
        if thread and thread.ident is not None:
            thread.join(timeout=2)
            return not thread.is_alive()
        return True
    except Exception:
        return False


def matrix_environment(base, work, expect_unsupported):
    env = dict(base, MIMAC_TEXT_MATRIX_DIR=str(work), MIMAC_TEXT_MATRIX_WEB_ONLY="1")
    if expect_unsupported:
        env["MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED"] = "1"
    else:
        env.pop("MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED", None)
    return env


def remove_work_if_safe(work, browser_stopped, launcher_stopped, server_stopped):
    if not (browser_stopped and launcher_stopped and server_stopped):
        return False
    shutil.rmtree(work)
    return True


def run():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--electron", type=Path, help="standalone Electron.app bundle")
    parser.add_argument("--chrome", type=Path, default=Path("/Applications/Google Chrome.app"))
    parser.add_argument("--force-renderer-accessibility", action="store_true")
    parser.add_argument("--expect-unsupported", action="store_true",
                        help="expect the adapter to reject this browser fixture")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    go = shutil.which(os.environ.get("MIMAC_GO", "go"))
    if not go:
        raise SystemExit("Go not found; set MIMAC_GO")
    bundle = (args.electron or args.chrome).resolve()
    info = plistlib.loads((bundle / "Contents/Info.plist").read_bytes())
    executable = (bundle / "Contents/MacOS" / info["CFBundleExecutable"]).resolve()
    if not executable.is_file():
        raise SystemExit("Browser executable missing")
    page = (root / "internal/adapters/textoutput/testdata/BrowserMatrix.html").read_bytes()
    cache = Path.home() / "Library/Caches/MiMac/verification"
    cache.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="browser-matrix-", dir=cache))
    prefix = "/" + uuid.uuid4().hex + "/"
    ready, server, thread, launcher = threading.Event(), None, None, None
    tracked = {}
    result = 1

    try:
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def reply(self, status, data=b"", content_type="application/json"):
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Cache-Control", "no-store")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                if self.path == prefix:
                    self.reply(200, page, "text/html; charset=utf-8")
                elif self.path == prefix + "command":
                    path = work / "command.json"
                    self.reply(200, path.read_bytes() if path.exists() else b"{}")
                else:
                    self.reply(404)

            def do_POST(self):
                if self.path == prefix + "ready":
                    ready.set()
                    self.reply(204)
                elif self.path == prefix + "result":
                    length = int(self.headers.get("Content-Length", "0"))
                    if not 0 < length < 131072:
                        self.reply(400)
                        return
                    data = json.loads(self.rfile.read(length))
                    data["pid"] = int((work / "pid").read_text())
                    (work / "result.tmp").write_text(json.dumps(data))
                    (work / "result.tmp").replace(work / "result.json")
                    self.reply(204)
                else:
                    self.reply(404)

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        url = f"http://127.0.0.1:{server.server_port}{prefix}"
        if args.electron:
            app_args = [str(root / "internal/adapters/textoutput/testdata/BrowserMatrix.cjs"),
                        url, str(work)]
        else:
            app_args = [f"--user-data-dir={work / 'profile'}", "--no-first-run",
                        "--no-default-browser-check", "--disable-background-networking",
                        "--disable-component-update", "--disable-sync", "--disable-extensions",
                        "--new-window", url]
        if args.force_renderer_accessibility:
            app_args.insert(0, "--force-renderer-accessibility")
        if args.electron:
            # Standalone Electron's CLI loads this test-owned JS entry point.
            with (work / "engine.log").open("w") as log:
                launcher = subprocess.Popen([str(executable), *app_args], stdout=log, stderr=subprocess.STDOUT)
        else:
            launcher = subprocess.Popen(["open", "-n", "-W", "--stdout", str(work / "engine.log"),
                                         "--stderr", str(work / "engine.log"), str(bundle), "--args", *app_args])

        pid = 0
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            current = process_table()
            owned = update_owned_tracking(tracked, current, work, bundle, executable)
            pid = fixture_pid(owned, work, executable, work / "pid")
            if pid:
                (work / "pid").write_text(str(pid))
            if pid and ready.is_set():
                break
            if launcher.poll() is not None:
                raise RuntimeError("Browser exited before fixture readiness")
            time.sleep(.05)
        if not pid or not ready.is_set():
            raise RuntimeError(f"Browser fixture did not become ready: pid={pid}, pageReady={ready.is_set()}")

        current = process_table()
        owned = update_owned_tracking(tracked, current, work, bundle, executable)
        if pid not in owned or not main_command(owned[pid].command, executable):
            raise RuntimeError("Browser fixture PID changed before AX activation")
        subprocess.run(["osascript", "-e", "tell application \"System Events\"",
                        "-e", f"set frontmost of (first application process whose unix id is {pid}) to true",
                        "-e", "end tell"], check=True, timeout=5)
        print(json.dumps({"engine": "electron" if args.electron else "chrome",
                          "version": info.get("CFBundleShortVersionString"), "pid": pid,
                          "mode": "unsupported-rejection" if args.expect_unsupported
                          else "editable-support"}), flush=True)
        env = matrix_environment(os.environ, work, args.expect_unsupported)
        result = subprocess.run(
            [go, "test", "./internal/adapters/textoutput", "-run",
             "^TestNative(Editable|UnsupportedWeb)Matrix$", "-count=1", "-v"],
            cwd=root, env=env, timeout=90,
        ).returncode
    finally:
        original_error = sys.exc_info()[0] is not None
        if (original_error or result != 0) and (work / "engine.log").exists():
            print("Fixture engine output:\n" + (work / "engine.log").read_text(errors="replace")[-6000:], file=sys.stderr)
        problems = []
        try:
            browser_stopped, survivors = stop_owned(work, bundle, executable, tracked)
            if not browser_stopped:
                problems.append(f"browser PIDs remain or became uncertain: {survivors}")
        except Exception as error:
            browser_stopped = False
            problems.append(f"browser cleanup failed: {error}")
        try:
            launcher_stopped = stop_launcher(launcher)
        except Exception as error:
            launcher_stopped = False
            problems.append(f"open launcher cleanup failed: {error}")
        if not launcher_stopped:
            problems.append("open launcher did not exit")

        # Recheck after Launch Services has exited. `tracked` retains every
        # first-scan owner so changed argv/reparented children cannot vanish.
        try:
            browser_stopped_after, survivors = stop_owned(work, bundle, executable, tracked)
            browser_stopped = browser_stopped_after
            if not browser_stopped_after:
                problems.append(f"browser PIDs remain or became uncertain after launcher exit: {survivors}")
        except Exception as error:
            browser_stopped = False
            problems.append(f"final browser check failed: {error}")
        try:
            server_stopped = close_server(server, thread)
        except Exception as error:
            server_stopped = False
            problems.append(f"HTTP server cleanup failed: {error}")
        if not server_stopped:
            problems.append("HTTP fixture server did not stop")

        try:
            remove_work_if_safe(work, browser_stopped, launcher_stopped, server_stopped)
        except OSError as error:
            problems.append(f"temporary directory removal failed: {error}")
        if work.exists() and not problems:
            problems.append("shutdown was not fully confirmed")
        if work.exists():
            reason = "; ".join(dict.fromkeys(problems))
            print(f"Preserved browser fixture directory {work}: {reason}", file=sys.stderr, flush=True)
            if not original_error:
                raise RuntimeError(f"browser fixture cleanup incomplete; preserved {work}: {reason}")

    return result


if __name__ == "__main__":
    raise SystemExit(run())
