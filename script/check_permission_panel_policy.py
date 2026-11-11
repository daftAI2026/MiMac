#!/usr/bin/env python3
"""Read-only check of the running MiMac Dev permission panel's AppKit policy."""

from __future__ import annotations

import argparse
import ctypes
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil
import signal
import subprocess
import sys
import tempfile
import textwrap


BUNDLE_ID = "dev.mimac.app.dev"
APP_NAME = "MiMac Dev"
# NSWindowStyleMaskNonactivatingPanel in the macOS SDK.
NONACTIVATING_PANEL_MASK = 1 << 7
EXPRESSION_TIMEOUT_US = 3_000_000
LLDB_TIMEOUT_S = 12
MARKER = "MIMAC_PANEL_POLICY_JSON="


class Refusal(ValueError):
    """The requested running app is not the exact expected development app."""


def default_app_path() -> Path:
    machine = platform.machine().lower()
    arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}.get(machine)
    if arch is None:
        raise Refusal(f"unsupported host architecture: {machine}")
    root = Path(__file__).resolve().parents[1]
    return root / ".mygo" / "dev" / f"darwin-{arch}" / f"{APP_NAME}.app"


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def inspect_bundle(app_arg: Path) -> tuple[Path, Path, str]:
    app_arg = app_arg.expanduser()
    if not app_arg.is_absolute():
        raise Refusal("--app must be an absolute bundle path")
    try:
        bundle = app_arg.resolve(strict=True)
    except OSError as error:
        raise Refusal(f"app bundle is unavailable: {error}") from error
    if not bundle.is_dir() or bundle.suffix != ".app":
        raise Refusal("--app must name an existing .app bundle")

    info_path = bundle / "Contents" / "Info.plist"
    try:
        with info_path.open("rb") as stream:
            info = plistlib.load(stream)
    except Exception as error:
        raise Refusal(f"cannot read the app Info.plist: {error}") from error
    if not isinstance(info, dict) or info.get("CFBundleIdentifier") != BUNDLE_ID:
        found = info.get("CFBundleIdentifier") if isinstance(info, dict) else None
        raise Refusal(f"expected bundle identifier {BUNDLE_ID!r}; found {found!r}")

    executable_name = info.get("CFBundleExecutable")
    if (not isinstance(executable_name, str) or not executable_name
            or executable_name in {".", ".."} or "/" in executable_name):
        raise Refusal("app has an invalid CFBundleExecutable")
    try:
        executable = (bundle / "Contents" / "MacOS" / executable_name).resolve(strict=True)
    except OSError as error:
        raise Refusal(f"app executable is unavailable: {error}") from error
    try:
        executable.relative_to(bundle)
    except ValueError as error:
        raise Refusal("app executable resolves outside the selected bundle") from error
    if not executable.is_file() or not os.access(executable, os.X_OK):
        raise Refusal("app executable is not a runnable file")
    return bundle, executable, BUNDLE_ID


def running_app_snapshot() -> dict:
    source = textwrap.dedent(
        f"""
        ObjC.import('AppKit');
        const bundleID = {json.dumps(BUNDLE_ID)};
        const apps = $.NSRunningApplication.runningApplicationsWithBundleIdentifier(bundleID);
        const count = Number(apps.count);
        if (count !== 1) {{
          JSON.stringify({{count: count}});
        }} else {{
          const app = apps.objectAtIndex(0);
          JSON.stringify({{
            count: count,
            pid: Number(app.processIdentifier),
            title: ObjC.unwrap(app.localizedName),
            bundlePath: ObjC.unwrap(app.bundleURL.path),
            executablePath: ObjC.unwrap(app.executableURL.path)
          }});
        }}
        """
    )
    try:
        completed = subprocess.run(
            ["/usr/bin/osascript", "-l", "JavaScript", "-e", source],
            capture_output=True,
            text=True,
            timeout=4,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise Refusal(f"JXA could not query the running app: {error}") from error
    if completed.returncode != 0:
        detail = completed.stderr.strip() or f"exit {completed.returncode}"
        raise Refusal(f"JXA app query failed: {detail}")
    try:
        result = json.loads(completed.stdout.strip())
    except json.JSONDecodeError as error:
        raise Refusal("JXA returned an invalid app identity record") from error
    if not isinstance(result, dict):
        raise Refusal("JXA returned an invalid app identity record")
    if result.get("count") != 1:
        raise Refusal(f"expected one running {BUNDLE_ID} app; found {result.get('count')}")
    return result


def process_executable_path(pid: int) -> Path:
    if sys.platform != "darwin":
        raise Refusal("this checker only runs on macOS")
    try:
        libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
        proc_pidpath = libproc.proc_pidpath
    except (OSError, AttributeError) as error:
        raise Refusal(f"could not load the read-only process path query: {error}") from error
    buffer = ctypes.create_string_buffer(4096)
    proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
    proc_pidpath.restype = ctypes.c_int
    length = proc_pidpath(pid, buffer, len(buffer))
    if length <= 0:
        raise Refusal(f"proc_pidpath failed for PID {pid}")
    try:
        return Path(os.fsdecode(buffer.value)).resolve(strict=True)
    except OSError as error:
        raise Refusal(f"running process executable path is unavailable: {error}") from error


RUNNER_TEMPLATE = r"""import json
import lldb

CONFIG_PATH = __CONFIG_PATH__
MARKER = "MIMAC_PANEL_POLICY_JSON="

def member_int(value, name):
    child = value.GetChildMemberWithName(name)
    if not child.IsValid():
        raise RuntimeError("missing result field: " + name)
    raw = child.GetValue()
    if raw is None:
        raise RuntimeError("empty result field: " + name)
    return int(raw, 0)

def main():
    with open(CONFIG_PATH, "r", encoding="utf-8") as stream:
        config = json.load(stream)
    result = {"status": "error", "detached": False}
    target = None
    process = None
    try:
        target = lldb.debugger.CreateTarget(config["executable"])
        if not target.IsValid():
            raise RuntimeError("LLDB could not create the exact executable target")
        attach_error = lldb.SBError()
        process = target.AttachToProcessWithID(
            lldb.SBListener("mimac-panel-policy-check"),
            int(config["pid"]),
            attach_error,
        )
        if attach_error.Fail():
            result = {"status": "attach_error", "error": str(attach_error)}
        elif not process.IsValid():
            result = {"status": "attach_error", "error": "LLDB returned an invalid process"}
        else:
            main_thread = next((process.GetThreadAtIndex(i) for i in range(process.GetNumThreads())
                                if process.GetThreadAtIndex(i).GetQueueName() == "com.apple.main-thread"),
                               process.GetThreadAtIndex(0))
            process.SetSelectedThread(main_thread)
            expression = r'''({
              auto send0=(void*(*)(void*,void*))dlsym((void*)-2,"objc_msgSend");
              auto sendIndex=(void*(*)(void*,void*,unsigned long))dlsym((void*)-2,"objc_msgSend");
              auto sendLong=(long(*)(void*,void*))dlsym((void*)-2,"objc_msgSend");
              auto sendULong=(unsigned long(*)(void*,void*))dlsym((void*)-2,"objc_msgSend");
              auto sendBool=(unsigned char(*)(void*,void*))dlsym((void*)-2,"objc_msgSend");
              auto sendBool3=(unsigned char(*)(void*,void*,void*))dlsym((void*)-2,"objc_msgSend");
              auto sel=(void*(*)(const char*))dlsym((void*)-2,"sel_registerName");
              auto cls=(void*(*)(const char*))dlsym((void*)-2,"objc_getClass");
              auto cmp=(int(*)(const char*,const char*))dlsym((void*)-2,"strcmp");
              void* appClass=cls("NSApplication");
              void* panelClass=cls("NSPanel");
              int onMain=sendBool(cls("NSThread"),sel("isMainThread"));
              void* app=appClass && onMain ? send0(appClass,sel("sharedApplication")) : (void*)0;
              void* windows=app ? send0(app,sel("windows")) : (void*)0;
              struct R {
                int mainThread;
                int matchedWindows;
                int runtimeAvailable;
                long windowCount;
                long panelCount;
                long level;
                unsigned long styleMask;
                int hidesOnDeactivate;
                int becomesKeyOnlyIfNeeded;
                int canBecomeKey;
                int canBecomeMain;
                int isKey;
                int isMain;
              } result={};
              result.level=-1;
              result.mainThread=onMain;
              if (app && windows && panelClass) {
                result.runtimeAvailable=1;
                result.windowCount=sendLong(windows,sel("count"));
                void* onlyPanel=(void*)0;
                for (unsigned long i=0;i<(unsigned long)result.windowCount;i++) {
                  void* window=sendIndex(windows,sel("objectAtIndex:"),i);
                  const char* title=window ? (const char*)send0(send0(window,sel("title")),sel("UTF8String")) : (const char*)0;
                  if (!title || cmp(title,"MiMac Dev \u7cfb\u7edf\u6743\u9650")!=0) continue;
                  result.matchedWindows++;
                  if (sendBool3(window,sel("isKindOfClass:"),panelClass)) {
                    result.panelCount++;
                    onlyPanel=window;
                  }
                }
                if (result.panelCount==1) {
                  result.level=sendLong(onlyPanel,sel("level"));
                  result.styleMask=sendULong(onlyPanel,sel("styleMask"));
                  result.hidesOnDeactivate=sendBool(onlyPanel,sel("hidesOnDeactivate"));
                  result.becomesKeyOnlyIfNeeded=sendBool(onlyPanel,sel("becomesKeyOnlyIfNeeded"));
                  result.canBecomeKey=sendBool(onlyPanel,sel("canBecomeKeyWindow"));
                  result.canBecomeMain=sendBool(onlyPanel,sel("canBecomeMainWindow"));
                  result.isKey=sendBool(onlyPanel,sel("isKeyWindow"));
                  result.isMain=sendBool(onlyPanel,sel("isMainWindow"));
                }
              }
              result;
            })'''
            options = lldb.SBExpressionOptions()
            options.SetLanguage(lldb.eLanguageTypeObjC_plus_plus)
            options.SetTimeoutInMicroSeconds(int(config["expression_timeout_us"]))
            value = target.EvaluateExpression(expression, options)
            expression_error = value.GetError()
            if expression_error.Fail():
                result = {"status": "expression_error", "error": str(expression_error)}
            elif not value.IsValid():
                result = {"status": "expression_error", "error": "LLDB returned an invalid value"}
            else:
                fields = (
                    "mainThread", "matchedWindows", "runtimeAvailable", "windowCount", "panelCount", "level", "styleMask",
                    "hidesOnDeactivate", "becomesKeyOnlyIfNeeded", "canBecomeKey",
                    "canBecomeMain", "isKey", "isMain",
                )
                result = {
                    "status": "observed",
                    "properties": {field: member_int(value, field) for field in fields},
                }
    except BaseException as error:
        result = {"status": "runner_error", "error": f"{type(error).__name__}: {error}"}
    finally:
        if process is not None and process.IsValid():
            try:
                if process.GetState() != lldb.eStateDetached:
                    detach_error = process.Detach()
                    result["detached"] = detach_error.Success()
                    if detach_error.Fail():
                        result["detach_error"] = str(detach_error)
                else:
                    result["detached"] = True
            except BaseException as error:
                result["detached"] = False
                result["detach_error"] = f"{type(error).__name__}: {error}"
        if target is not None and target.IsValid():
            try:
                lldb.debugger.DeleteTarget(target)
            except BaseException:
                pass
        print(MARKER + json.dumps(result, separators=(",", ":")), flush=True)

main()
"""


def run_lldb(executable: Path, pid: int) -> dict:
    lldb_path = shutil.which("lldb")
    if not lldb_path:
        raise Refusal("lldb is not available on PATH")
    cache_root = Path.home() / "Library" / "Caches"
    try:
        cache_root.mkdir(parents=True, exist_ok=True)
    except OSError as error:
        raise Refusal(f"cannot prepare the temporary cache runner: {error}") from error

    with tempfile.TemporaryDirectory(prefix="mimac-panel-policy-", dir=cache_root) as directory:
        work = Path(directory)
        config_path = work / "input.json"
        runner_path = work / "runner.py"
        config_path.write_text(json.dumps({
            "executable": str(executable),
            "pid": pid,
            "expression_timeout_us": EXPRESSION_TIMEOUT_US,
        }) + "\n")
        runner_source = RUNNER_TEMPLATE.replace("__CONFIG_PATH__", repr(str(config_path)))
        runner_path.write_text(runner_source)
        command = f"script exec(open({str(runner_path)!r}, encoding='utf-8').read())"
        try:
            child = subprocess.Popen(
                [lldb_path, "-b", "-o", command, "-o", "quit"],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                start_new_session=True,
            )
        except OSError as error:
            raise Refusal(f"could not start LLDB: {error}") from error
        try:
            stdout, stderr = child.communicate(timeout=LLDB_TIMEOUT_S)
        except subprocess.TimeoutExpired:
            # Interrupt first so the embedded runner can unwind through its
            # finally/Detach path. The expression itself has a 3-second limit.
            try:
                os.killpg(child.pid, signal.SIGINT)
            except ProcessLookupError:
                pass
            try:
                stdout, stderr = child.communicate(timeout=4)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    stdout, stderr = child.communicate(timeout=2)
                except subprocess.TimeoutExpired:
                    child.kill()
                    stdout, stderr = child.communicate()
            return {
                "status": "timeout",
                "detached": False,
                "detail": "LLDB exceeded the outer timeout; inspect process state before retrying",
                "lldb_exit_code": child.returncode,
                "stderr_tail": stderr.strip()[-1200:],
            }

    records = [line[len(MARKER):] for line in stdout.splitlines() if line.startswith(MARKER)]
    if not records:
        return {
            "status": "lldb_error",
            "detached": False,
            "lldb_exit_code": child.returncode,
            "stderr_tail": stderr.strip()[-1200:],
            "stdout_tail": stdout.strip()[-1200:],
        }
    try:
        return json.loads(records[-1])
    except json.JSONDecodeError:
        return {
            "status": "lldb_error",
            "detached": False,
            "lldb_exit_code": child.returncode,
            "stderr_tail": stderr.strip()[-1200:],
            "stdout_tail": stdout.strip()[-1200:],
        }


def evaluate_policy(observed: dict) -> tuple[dict, dict]:
    props = observed.get("properties") or {}
    checks = {
        "on_native_main_thread": props.get("mainThread") == 1,
        "one_permission_window": props.get("matchedWindows") == 1,
        "runtime_available": props.get("runtimeAvailable") == 1,
        "one_ns_panel": props.get("panelCount") == 1,
        "level_3": props.get("level") == 3,
        "nonactivating_panel_mask": bool(props.get("styleMask", 0) & NONACTIVATING_PANEL_MASK),
        "hides_on_deactivate_false": props.get("hidesOnDeactivate") == 0,
        "becomes_key_only_if_needed_true": props.get("becomesKeyOnlyIfNeeded") == 1,
        "can_become_key_true": props.get("canBecomeKey") == 1,
        "can_become_main_false": props.get("canBecomeMain") == 0,
    }
    return props, checks


def emit(payload: dict, exit_code: int) -> int:
    print(json.dumps(payload, indent=2, sort_keys=True))
    return exit_code


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--app",
        type=Path,
        default=None,
        help="exact MiMac Dev .app path (defaults to this checkout's build_dev output)",
    )
    args = parser.parse_args(argv)

    try:
        if sys.platform != "darwin":
            raise Refusal("this checker only runs on macOS")
        app_arg = args.app if args.app is not None else default_app_path()
        bundle, executable, bundle_id = inspect_bundle(app_arg)
        running = running_app_snapshot()
        if running.get("title") != APP_NAME:
            raise Refusal(f"expected running app title {APP_NAME!r}; found {running.get('title')!r}")
        running_bundle = Path(running["bundlePath"]).resolve(strict=True)
        running_executable = Path(running["executablePath"]).resolve(strict=True)
        if running_bundle != bundle or running_executable != executable:
            raise Refusal("running app bundle/executable does not match the exact --app path")
        pid = int(running["pid"])
        if pid <= 1:
            raise Refusal("JXA returned an invalid process identifier")
        kernel_executable = process_executable_path(pid)
        if kernel_executable != executable:
            raise Refusal("kernel process path does not match the exact app executable")

        digest_before = sha256_file(executable)
        observation = run_lldb(executable, pid)
        digest_after = sha256_file(executable)
        props, checks = evaluate_policy(observation)
        checks["executable_sha_unchanged"] = digest_before == digest_after
        checks["lldb_detached"] = observation.get("detached") is True
        identity = {
            "bundle_identifier": bundle_id,
            "bundle_path": str(bundle),
            "executable_path": str(executable),
            "pid": pid,
            "app_title": running.get("title"),
            "executable_sha256_before": digest_before,
            "executable_sha256_after": digest_after,
            "kernel_executable_path": str(kernel_executable),
        }
        observation_ok = observation.get("status") == "observed"
        identity_ok = checks["executable_sha_unchanged"] and checks["lldb_detached"]
        passed = observation_ok and identity_ok and all(
            value for name, value in checks.items()
            if name not in {"executable_sha_unchanged", "lldb_detached"}
        )
        payload = {
            "status": "pass" if passed else (
                "mismatch" if observation_ok and identity_ok else "error"
            ),
            "identity": identity,
            "expected": {
                "window_class": "NSPanel",
                "level": 3,
                "nonactivating_panel_style_mask": NONACTIVATING_PANEL_MASK,
                "hides_on_deactivate": False,
                "becomes_key_only_if_needed": True,
                "can_become_key": True,
                "can_become_main": False,
            },
            "observed": props,
            "checks": checks,
            "debugger": observation,
        }
        return emit(payload, 0 if passed else (1 if observation_ok and identity_ok else 3))
    except Refusal as error:
        return emit({"status": "refused", "error": str(error), "bundle_identifier": BUNDLE_ID}, 2)
    except (OSError, ValueError, RuntimeError, KeyError, TypeError, subprocess.SubprocessError) as error:
        return emit({"status": "error", "error": f"{type(error).__name__}: {error}", "bundle_identifier": BUNDLE_ID}, 3)


if __name__ == "__main__":
    raise SystemExit(main())
