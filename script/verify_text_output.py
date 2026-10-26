#!/usr/bin/env python3
"""Opt-in native text compatibility test. Opens only a synthetic fixture window.

Requires existing Accessibility access for the invoking terminal. Does not
prompt, change permissions, use the clipboard, or type into other applications.
Set MIMAC_GO to the Go executable when the toolchain is not on PATH.
"""

import json
import os
from pathlib import Path
import plistlib
import shutil
import signal
import subprocess
import sys
import tempfile
import time


def run():
    if sys.platform != "darwin":
        raise SystemExit("This verification requires macOS")
    root = Path(__file__).resolve().parent.parent
    go = shutil.which(os.environ.get("MIMAC_GO", "go"))
    if not go:
        raise SystemExit("Go not found; set MIMAC_GO")
    cache = Path.home() / "Library/Caches/MiMac/verification"
    cache.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="text-matrix-", dir=cache) as directory:
        work = Path(directory)
        app = work / "TextMatrix.app"
        executable = app / "Contents/MacOS/TextMatrix"
        executable.parent.mkdir(parents=True)
        (app / "Contents/Info.plist").write_bytes(plistlib.dumps({
            "CFBundleIdentifier": "dev.mimac.verification.textmatrix",
            "CFBundleExecutable": "TextMatrix",
            "CFBundleName": "MiMac Text Matrix",
            "CFBundlePackageType": "APPL",
            "NSHighResolutionCapable": True,
        }))
        subprocess.run([
            "swiftc", str(root / "internal/adapters/textoutput/testdata/TextMatrix.swift"),
            "-o", str(executable),
        ], check=True)
        launcher = subprocess.Popen(["open", "-n", "-W", str(app), "--args", str(work)])
        pid = None
        try:
            deadline = time.monotonic() + 15
            while not (work / "pid").exists():
                if time.monotonic() >= deadline or launcher.poll() is not None:
                    raise RuntimeError("TextMatrix did not become ready")
                time.sleep(.05)
            pid = int((work / "pid").read_text())
            environment = dict(os.environ, MIMAC_TEXT_MATRIX_DIR=str(work))
            return subprocess.run([
                go, "test", "./internal/adapters/textoutput", "-run",
                "^TestNativeEditableMatrix$", "-count=1", "-v",
            ], cwd=root, env=environment, timeout=90).returncode
        finally:
            # Graceful quit uses this fixture's private command channel.
            (work / "quit.tmp").write_text(json.dumps({"seq": 2**31-1, "action": "quit"}))
            (work / "quit.tmp").replace(work / "command.json")
            try:
                launcher.wait(timeout=5)
            except subprocess.TimeoutExpired:
                # A PID alone is insufficient: verify our exact executable.
                if pid:
                    actual = subprocess.run(["ps", "-p", str(pid), "-o", "comm="],
                                            capture_output=True, text=True).stdout.strip()
                    if actual == str(executable):
                        os.kill(pid, signal.SIGTERM)
                launcher.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(run())
