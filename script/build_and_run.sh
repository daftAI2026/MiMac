#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-run}"
case "$MODE" in
  run|--verify|--debug|--logs) ;;
  *) echo "usage: $0 [run|--verify|--debug|--logs]" >&2; exit 2 ;;
esac
if (( $# > 0 )); then shift; fi
if (( $# > 0 )); then
  [[ "$1" == -- && "$MODE" == run ]] || { echo "App arguments require: run -- [arguments]" >&2; exit 2; }
  shift
fi
APP_ARGS=("$@")
[[ "$(uname -s)" == Darwin ]] || { echo "MiMac currently requires macOS" >&2; exit 2; }
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="$(command -v "${MIMAC_GO:-go}")"
export PATH="$(dirname "$GO_BIN"):$PATH"
export CGO_ENABLED=0
cd "$ROOT_DIR"
ARCH="$($GO_BIN env GOARCH)"
APP_BUNDLE="$ROOT_DIR/dist/darwin-$ARCH/MiMac.app"
APP_BINARY="$APP_BUNDLE/Contents/MacOS/MiMac"

# Only stop the executable belonging to this checkout, never a name match.
python3 - "$APP_BINARY" <<'PY'
import os, pathlib, signal, subprocess, sys, time
expected = sys.argv[1]
def owned():
    rows = subprocess.check_output(['/bin/ps', '-axo', 'pid=,comm='], text=True)
    return [int(fields[0]) for line in rows.splitlines()
            if len(fields := line.strip().split(None, 1)) == 2 and fields[1] == expected]
processes = owned()
recovery = pathlib.Path.home() / 'Library/Application Support/MiMac/voice-mapping-recovery.plist'
if recovery.exists():
    raise SystemExit('MiMac still owns a voice-key recovery record; stop the remote in MiMac before rebuilding')
for pid in processes:
    try: os.kill(pid, signal.SIGTERM)
    except ProcessLookupError: pass
deadline = time.monotonic() + 3
while owned() and time.monotonic() < deadline:
    time.sleep(.1)
if owned():
    raise SystemExit('MiMac did not exit; build cancelled')
PY

"$ROOT_DIR/script/build_native_settings.sh"
"$ROOT_DIR/script/build_voice_overlay.sh"

BUILD_ARGS=(-platform "darwin/$ARCH" -skip-dmg -skip-notarize)
if [[ -n "${MIMAC_SIGN_IDENTITY:-}" ]]; then
  BUILD_ARGS+=(-sign "$MIMAC_SIGN_IDENTITY")
fi
"$GO_BIN" tool mygo build "${BUILD_ARGS[@]}"
case "$MODE" in
  run) /usr/bin/open -n "$APP_BUNDLE" --args "${APP_ARGS[@]}" ;;
  --debug) /usr/bin/lldb -- "$APP_BINARY" ;;
  --logs)
    /usr/bin/open -n "$APP_BUNDLE"
    /usr/bin/log stream --info --style compact --predicate 'process == "MiMac"'
    ;;
  --verify)
    python3 - "$APP_BUNDLE" <<'PY'
import json, pathlib, subprocess, sys, tempfile
with tempfile.TemporaryDirectory(prefix='mimac-host-') as directory:
    report = pathlib.Path(directory) / 'host.json'
    subprocess.run(['/usr/bin/open', '-n', '-W', sys.argv[1], '--args',
                    '--show-settings', '--smoke-duration', '2s', '--host-report', str(report)],
                   check=True, timeout=15)
    result = json.loads(report.read_text())
    assert result['host_ready'] and result['tray_created'] and result['normal_exit'], result
    assert result['windows_created'] == result['windows_closed'] == result['quit_callbacks'] == 1, result
    assert result['settings_closed_before_quit'] is True, result
    print(json.dumps(result))
PY
    ;;
esac
