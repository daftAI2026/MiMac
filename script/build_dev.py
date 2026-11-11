#!/usr/bin/env python3
"""Build an isolated development bundle through MyGo, then verify before launch."""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import subprocess
import tempfile
import time

from verify_app_bundle import verify
from dev_launch_registration import ensure_current_dev_registration


def development_config(config, root):
    result = json.loads(json.dumps(config))
    if result.get('identifier') != 'dev.mimac.app' or result.get('name') != 'MiMac':
        raise ValueError('expected the MiMac production build configuration')
    result['name'] += ' Dev'
    result['identifier'] += '.dev'
    result['main'] = str(root / result['main'])
    result['out'] = str(root / '.mygo/dev')
    result['icon'] = str(root / result['icon'])
    if result.get('macos', {}).get('icon'):
        result['macos']['icon'] = str(root / result['macos']['icon'])
    result['resources'] = [str(root / item) for item in result.get('resources', [])]
    return result


def owned_pids(executable):
    rows = subprocess.check_output(['/bin/ps', '-axo', 'pid=,comm='], text=True)
    return [int(fields[0]) for line in rows.splitlines()
            if len(fields := line.strip().split(None, 1)) == 2 and fields[1] == str(executable)]


def stop_development(executable):
    recovery = Path.home() / 'Library/Application Support/MiMac Dev/voice-mapping-recovery.plist'
    if recovery.exists():
        raise RuntimeError('Stop the remote in MiMac Dev before rebuilding; its recovery record still exists')
    for pid in owned_pids(executable):
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + 3
    while owned_pids(executable) and time.monotonic() < deadline:
        time.sleep(.1)
    if owned_pids(executable):
        raise RuntimeError('MiMac Dev did not exit; build cancelled')


def require_device_handoff():
    # NSRunningApplication is read-only and does not require Automation access.
    script = '''ObjC.import('AppKit');
    $.NSRunningApplication.runningApplicationsWithBundleIdentifier('dev.mimac.app').count.toString();'''
    count = subprocess.check_output(['/usr/bin/osascript', '-l', 'JavaScript', '-e', script], text=True).strip()
    if int(count) != 0:
        raise RuntimeError('Quit daily MiMac before starting a development device test')
    recovery = Path.home() / 'Library/Application Support/MiMac/voice-mapping-recovery.plist'
    if recovery.exists():
        raise RuntimeError('Restore daily MiMac remote mappings before starting a development device test')


def device_access_requested(arguments):
    requested = False
    for argument in arguments:
        name, separator, value = argument.partition('=')
        if name.lstrip('-') == 'device-access':
            requested = not separator or value.lower() in ('1', 't', 'true')
    return requested


def primary_repository_root(root):
    try:
        common_dir = subprocess.check_output(
            ['git', 'rev-parse', '--path-format=absolute', '--git-common-dir'],
            cwd=root,
            text=True,
        ).strip()
    except subprocess.CalledProcessError as error:
        raise RuntimeError(
            f'git rev-parse --git-common-dir failed with exit code {error.returncode}'
        ) from error
    common_dir = Path(common_dir)
    if not common_dir.is_absolute():
        raise RuntimeError(f'git rev-parse returned a non-absolute common directory: {common_dir}')
    common_dir = common_dir.resolve(strict=False)
    if common_dir.name != '.git':
        raise RuntimeError(f'git common directory is not the expected .git directory: {common_dir}')
    repository_root = common_dir.parent
    if not (repository_root / 'mygo.json').is_file():
        raise RuntimeError(f'Git common directory does not identify the MiMac source root: {repository_root}')
    return repository_root


def launch_development(bundle, root, arch, open_options, app_args, device_access=False, **run_options):
    repository_root = primary_repository_root(root)
    ensure_current_dev_registration(bundle, root, arch, repository_root)
    if device_access:
        require_device_handoff()  # Recheck after registration and immediately before Launch Services launch.
    command = ['/usr/bin/open', *open_options, str(bundle), '--args', *app_args]
    return subprocess.run(command, check=True, **run_options)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', nargs='?', choices=['run', 'build', 'verify'], default='run')
    parser.add_argument('app_args', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    app_args = args.app_args
    if app_args[:1] == ['--']:
        app_args = app_args[1:]
    if app_args and args.mode != 'run':
        parser.error('App arguments require run -- [arguments]')
    root = Path(__file__).resolve().parent.parent
    go = shutil.which(os.environ.get('MIMAC_GO', 'go'))
    if not go:
        parser.error('Go is not available')
    env = dict(os.environ, CGO_ENABLED='0', PATH=str(Path(go).parent) + os.pathsep + os.environ['PATH'])
    arch = subprocess.check_output([go, 'env', 'GOARCH'], cwd=root, env=env, text=True).strip()
    if arch not in ('arm64', 'amd64') or os.uname().sysname != 'Darwin':
        parser.error('MiMac Dev requires macOS arm64 or amd64')
    bundle = root / '.mygo/dev' / ('darwin-' + arch) / 'MiMac Dev.app'
    executable = bundle / 'Contents/MacOS/MiMac Dev'
    device_access = device_access_requested(app_args)
    if device_access:
        require_device_handoff()
    stop_development(executable)
    for entry in ['build_native_settings.sh', 'build_voice_overlay.sh']:
        subprocess.run([str(root / 'script' / entry)], cwd=root, env=env, check=True)
    subprocess.run([sys.executable, str(root / 'script/package_audio_component.py')], cwd=root, env=env, check=True)
    config = development_config(json.loads((root / 'mygo.json').read_text()), root)
    (root / '.mygo').mkdir(exist_ok=True)
    # A tiny build configuration under the same module resolves Go's source
    # and VCS metadata normally. Never edit the production mygo.json or dist.
    with tempfile.TemporaryDirectory(prefix='config-', dir=root / '.mygo') as directory:
        staging = Path(directory)
        (staging / 'mygo.json').write_text(json.dumps(config, indent=2) + '\n')
        (staging / 'resources').symlink_to(root / 'resources', target_is_directory=True)
        command = [go, 'tool', 'mygo', 'build', '-platform', 'darwin/' + arch, '-skip-dmg', '-skip-notarize']
        if identity := os.environ.get('MIMAC_SIGN_IDENTITY'):
            command += ['-sign', identity]
        subprocess.run(command + [str(staging)], cwd=root, env=env, check=True)
    receipt = verify(bundle, arch, development=True, require_audio_installer=True)
    (bundle.parent / 'bundle-verification.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print(json.dumps(receipt), flush=True)
    if args.mode == 'build':
        return
    if args.mode == 'verify':
        with tempfile.TemporaryDirectory(prefix='mimac-dev-host-') as directory:
            report = Path(directory) / 'host.json'
            launch_development(bundle, root, arch, ['-g', '-n', '-W'],
                               ['--show-settings=false', '--smoke-duration', '1s', '--host-report', str(report)],
                               timeout=15)
            result = json.loads(report.read_text())
            assert result['host_ready'] and result['tray_created'] and result['normal_exit'], result
            assert result['app_name'] == 'MiMac Dev' and result['isolated_development'], result
            assert result['config_directory'] == str(Path.home() / 'Library/Application Support/MiMac Dev'), result
            assert result['executable'] == str(executable) and result['pid'] > 0, result
            assert not result.get('monitor_starts') and result['windows_created'] == 0, result
            (bundle.parent / 'host-verification.json').write_text(json.dumps(result, indent=2) + '\n')
            print(json.dumps(result), flush=True)
        return
    launch_development(bundle, root, arch, ['-n'], app_args, device_access=device_access)


if __name__ == '__main__':
    main()
