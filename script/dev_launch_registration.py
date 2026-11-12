"""Select the current MiMac Dev bundle as the Launch Services default."""

import json
import os
from pathlib import Path
import plistlib
import subprocess


DEV_BUNDLE_IDENTIFIER = "dev.mimac.app.dev"
DEV_BUNDLE_NAME = "MiMac Dev.app"
LSREGISTER = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"


def _standard_path(value):
    raw = os.fspath(value)
    if not os.path.isabs(raw):
        raise RuntimeError(f"Launch Services returned a non-absolute app path: {raw}")
    return Path(os.path.normpath(raw))


def _resolved_path(path):
    try:
        return path.resolve(strict=False)
    except (OSError, RuntimeError) as error:
        raise RuntimeError(f"Could not resolve Launch Services app path {path}: {error}") from error


def _is_relative_to(path, root):
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def _project_dev_shape(path, root):
    if not _is_relative_to(path, root):
        return False
    parts = path.relative_to(root).parts
    return (
        len(parts) >= 4
        and parts[-4] == ".mygo"
        and parts[-3] == "dev"
        and parts[-2] in ("darwin-arm64", "darwin-amd64")
        and parts[-1] == DEV_BUNDLE_NAME
    )


def _known_registration_path(path, resolved, primary_repository_root, cache_root):
    research_path = primary_repository_root.parent / "mimac-research"
    research_root = research_path.resolve(strict=False)
    roots = (primary_repository_root, research_root)
    for root in roots:
        if root == research_root and research_path != research_root:
            continue
        if _project_dev_shape(path, root) and _project_dev_shape(resolved, root):
            return True
    return (
        cache_root.resolve(strict=False) == cache_root
        and path.name == DEV_BUNDLE_NAME
        and resolved.name == DEV_BUNDLE_NAME
        and _is_relative_to(path, cache_root)
        and _is_relative_to(resolved, cache_root)
    )


def _bundle_identifier(bundle):
    if not bundle.exists():
        return None
    if not bundle.is_dir():
        raise RuntimeError(f"Registered Dev app path is not a bundle directory: {bundle}")
    info_path = bundle / "Contents" / "Info.plist"
    try:
        with info_path.open("rb") as handle:
            info = plistlib.load(handle)
    except (OSError, plistlib.InvalidFileException) as error:
        raise RuntimeError(f"Could not verify existing Launch Services bundle Info.plist at {bundle}: {error}") from error
    if not isinstance(info, dict):
        raise RuntimeError(f"Existing Launch Services bundle has an invalid Info.plist: {bundle}")
    return info.get("CFBundleIdentifier")


def _validate_bundle(bundle, expected):
    actual = _bundle_identifier(bundle)
    if actual is None:
        raise RuntimeError(f"App bundle is missing or has no CFBundleIdentifier: {bundle}")
    if actual != DEV_BUNDLE_IDENTIFIER:
        raise RuntimeError(
            f"Expected CFBundleIdentifier {DEV_BUNDLE_IDENTIFIER} at {bundle}, found {actual}"
        )
    if bundle != expected:
        raise RuntimeError(f"Only the current fixed Dev bundle may be registered: {expected}; got {bundle}")


class SystemLaunchServices:
    """Small system adapter; coordination logic is tested with an offline mock."""

    @staticmethod
    def _query(script, label):
        result = subprocess.run(
            ["/usr/bin/osascript", "-l", "JavaScript", "-e", script],
            check=False,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            detail = (result.stderr or result.stdout).strip()
            raise RuntimeError(f"NSWorkspace {label} query failed with exit code {result.returncode}: {detail}")
        try:
            return json.loads(result.stdout.strip())
        except json.JSONDecodeError as error:
            raise RuntimeError(f"NSWorkspace {label} query returned invalid JSON: {result.stdout!r}") from error

    def registered_urls(self, bundle_identifier):
        if bundle_identifier != DEV_BUNDLE_IDENTIFIER:
            raise RuntimeError(f"Only {DEV_BUNDLE_IDENTIFIER} may be queried")
        script = f"""
ObjC.import('AppKit');
const bundleID = {json.dumps(DEV_BUNDLE_IDENTIFIER)};
const urls = $.NSWorkspace.sharedWorkspace.URLsForApplicationsWithBundleIdentifier(bundleID);
const paths = [];
for (let i = 0; i < urls.count; i++) {{
  paths.push(ObjC.unwrap(urls.objectAtIndex(i).path));
}}
JSON.stringify(paths);
"""
        value = self._query(script, "registered applications")
        if not isinstance(value, list) or any(not isinstance(path, str) for path in value):
            raise RuntimeError("NSWorkspace registered-app query did not return a list of paths")
        return value

    def default_url(self, bundle_identifier):
        if bundle_identifier != DEV_BUNDLE_IDENTIFIER:
            raise RuntimeError(f"Only {DEV_BUNDLE_IDENTIFIER} may be queried")
        script = f"""
ObjC.import('AppKit');
const bundleID = {json.dumps(DEV_BUNDLE_IDENTIFIER)};
const url = $.NSWorkspace.sharedWorkspace.URLForApplicationWithBundleIdentifier(bundleID);
let path = null;
if (url !== null && url !== undefined) {{
  if (typeof url.isNil !== 'function' || !url.isNil()) {{
    path = ObjC.unwrap(url.path);
  }}
}}
JSON.stringify(path);
"""
        value = self._query(script, "default application")
        if value is not None and not isinstance(value, str):
            raise RuntimeError("NSWorkspace default-app query did not return a path or null")
        return value

    @staticmethod
    def _run_registration(flag, path):
        try:
            return subprocess.run(
                [LSREGISTER, flag, os.fspath(path)],
                check=False,
                capture_output=True,
                text=True,
            )
        except OSError as error:
            raise RuntimeError(f"Could not run lsregister {flag} for {path}: {error}") from error

    def unregister(self, path):
        return self._run_registration("-u", path)

    def register(self, path):
        return self._run_registration("-f", path)


def _require_success(result, operation, path):
    if result.returncode == 0:
        return
    detail = (result.stderr or result.stdout or "").strip()
    suffix = f": {detail}" if detail else ""
    raise RuntimeError(
        f"lsregister {operation} failed with exit code {result.returncode} for {path}{suffix}"
    )


def ensure_current_dev_registration(
    bundle_path,
    project_root,
    architecture,
    primary_repository_root,
    backend=None,
    cache_root=None,
):
    """Remove only known local duplicate Dev records, register, then verify default URL."""
    if architecture not in ("arm64", "amd64"):
        raise RuntimeError(f"Unsupported Dev architecture for Launch Services registration: {architecture}")
    project_root = Path(project_root).resolve(strict=False)
    primary_repository_root = Path(primary_repository_root).resolve(strict=False)
    config_path = primary_repository_root / "mygo.json"
    try:
        primary_config = json.loads(config_path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise RuntimeError(f"Could not verify Git primary repository root at {config_path}: {error}") from error
    if not isinstance(primary_config, dict) or primary_config.get("identifier") != "dev.mimac.app" or primary_config.get("name") != "MiMac":
        raise RuntimeError(f"Git common directory does not identify the MiMac primary repository root: {primary_repository_root}")
    expected = project_root / ".mygo" / "dev" / f"darwin-{architecture}" / DEV_BUNDLE_NAME
    selected = _standard_path(bundle_path)
    if selected != expected:
        raise RuntimeError(f"Only the current fixed Dev bundle may be registered: expected {expected}, got {selected}")
    if _resolved_path(selected) != selected:
        raise RuntimeError(f"Current fixed Dev bundle path must not traverse a symlink: {selected}")
    _validate_bundle(selected, expected)

    if backend is None:
        backend = SystemLaunchServices()
    if cache_root is None:
        cache_root = Path.home() / "Library" / "Caches" / "MiMac"
    cache_root = _standard_path(cache_root)

    # Classify and verify every record before the first Launch Services mutation.
    registered_paths = backend.registered_urls(DEV_BUNDLE_IDENTIFIER)
    removals = []
    seen = set()
    for registered in registered_paths:
        path = _standard_path(registered)
        if path in seen:
            continue
        seen.add(path)
        if path == selected:
            continue
        resolved = _resolved_path(path)
        if path.exists():
            actual = _bundle_identifier(path)
            if actual != DEV_BUNDLE_IDENTIFIER:
                raise RuntimeError(
                    f"Refusing to unregister existing bundle at {path}: expected CFBundleIdentifier "
                    f"{DEV_BUNDLE_IDENTIFIER}, found {actual}"
                )
        if not _known_registration_path(path, resolved, primary_repository_root, cache_root):
            raise RuntimeError(
                f"Refusing to change Launch Services: {DEV_BUNDLE_IDENTIFIER} is also registered at out-of-scope path {path}; "
                f"known cleanup roots are {primary_repository_root}, {primary_repository_root.parent / 'mimac-research'}, and {cache_root}"
            )
        removals.append(path)

    for path in removals:
        _require_success(backend.unregister(path), "unregister", path)
    _require_success(backend.register(selected), "register", selected)

    default_url = backend.default_url(DEV_BUNDLE_IDENTIFIER)
    actual_default = _standard_path(default_url) if default_url is not None else None
    if actual_default != selected:
        shown = os.fspath(actual_default) if actual_default is not None else "(none)"
        raise RuntimeError(
            f"Launch Services default URL does not match the current fixed Dev bundle: expected {selected}, got {shown}"
        )
    return selected
