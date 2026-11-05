#!/usr/bin/env python3

import copy
import hashlib
import json
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from release_version import apply_versions
from verify_app_bundle import REQUIRED, SIRI
from verify_release import SIRI_ABI_HOST_GATE, verify_build_info, verify_release, verify_source, verify_versions


RELEASE_DATE = "2026.11.01"
BUILD_NUMBER = "42"
REVISION = "0123456789abcdef0123456789abcdef01234567"


def git(repo: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo), *args],
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    return result.stdout.strip()


def fixture_config(version: str = "0.1.0", build_number: str | None = None) -> dict:
    info_plist = {"LSUIElement": True}
    if build_number is not None:
        info_plist["CFBundleVersion"] = build_number
    return {
        "name": "MiMac",
        "identifier": "dev.mimac.app",
        "version": version,
        "main": "./cmd/mimac",
        "macos": {
            "minimumSystemVersion": "14.2",
            "signingIdentity": "-",
            "infoPlist": info_plist,
        },
        "resources": ["Assets/Social/NOTICE.md"],
    }


def write_json(path: Path, value: dict) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def make_repo(root: Path, config: dict | None = None) -> tuple[Path, Path, str]:
    root.mkdir(parents=True)
    git(root, "init", "-q")
    git(root, "config", "user.email", "verify-release@example.invalid")
    git(root, "config", "user.name", "MiMac release verifier test")
    git(root, "config", "commit.gpgsign", "false")

    (root / ".gitignore").write_text("resources/darwin-*/\n", encoding="utf-8")
    config_path = root / "mygo.json"
    write_json(config_path, config or fixture_config())
    source_path = root / "cmd" / "mimac" / "main.go"
    source_path.parent.mkdir(parents=True)
    source_path.write_text("package main\n", encoding="utf-8")

    git(root, "add", ".gitignore", "mygo.json", "cmd/mimac/main.go")
    git(root, "commit", "-q", "-m", "fixture source")
    return root, config_path, git(root, "rev-parse", "HEAD")


def prepare_versions(config_path: Path, version: str = RELEASE_DATE, build_number: str = BUILD_NUMBER) -> None:
    config = json.loads(config_path.read_text(encoding="utf-8"))
    write_json(config_path, apply_versions(config, version, build_number))


def build_info(arch: str = "arm64", revision: str = REVISION, modified: str = "true") -> dict:
    return {
        "Path": "mimac.local/mimac/cmd/mimac",
        "Main": {"Path": "mimac.local/mimac"},
        "GoVersion": "go1.27.3",
        "Settings": [
            {"Key": "GOOS", "Value": "darwin"},
            {"Key": "GOARCH", "Value": arch},
            {"Key": "vcs", "Value": "git"},
            {"Key": "vcs.revision", "Value": revision},
            {"Key": "vcs.modified", "Value": modified},
        ],
    }


def set_build_setting(info: dict, key: str, value: str) -> None:
    for setting in info["Settings"]:
        if setting["Key"] == key:
            setting["Value"] = value
            return
    info["Settings"].append({"Key": key, "Value": value})


def make_bundle(root: Path, include_siri: bool) -> Path:
    bundle = root / "MiMac.app"
    contents = bundle / "Contents"
    resources = contents / "Resources"
    (contents / "MacOS").mkdir(parents=True)
    resources.mkdir(parents=True)
    (contents / "Info.plist").write_bytes(plistlib.dumps({
        "CFBundleIdentifier": "dev.mimac.app",
        "CFBundleExecutable": "MiMac",
        "CFBundleShortVersionString": RELEASE_DATE,
        "CFBundleVersion": BUILD_NUMBER,
    }))
    (contents / "MacOS" / "MiMac").write_bytes(b"fixture executable")
    for name in REQUIRED:
        (resources / name).write_bytes(("fixture " + name).encode())
    if include_siri:
        (resources / SIRI).write_bytes(b"fixture Siri bridge")
    return bundle


def verify_fixture(bundle: Path, arch: str, profile: str, host: dict | None = None) -> dict:
    def fake_check_output(command, text=None):
        if command[:3] == ["go", "version", "-m"]:
            return json.dumps(build_info(arch))
        if command[0] == "/usr/bin/nm":
            name = Path(command[-1]).name
            if name == SIRI:
                return "_mimac_siri_glass_abi_version\n_mimac_siri_glass_update_frame\n"
            return "_" + REQUIRED[name] + "\n"
        raise AssertionError("unexpected command: " + " ".join(command))

    def fake_run(command, **kwargs):
        if command[0] not in {"/usr/bin/lipo", "/usr/bin/codesign"}:
            raise AssertionError("unexpected command: " + " ".join(command))
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    with patch("verify_release.verify_source", return_value={"revision": REVISION}), \
         patch("verify_release.current_siri_abi_host", return_value=host), \
         patch("verify_release.subprocess.check_output", side_effect=fake_check_output), \
         patch("verify_release.subprocess.run", side_effect=fake_run):
        return verify_release(bundle, arch, Path(bundle.parent), REVISION, RELEASE_DATE, BUILD_NUMBER, profile)


class VerifyReleaseTests(unittest.TestCase):
    def test_manifest_records_explicit_profiles_and_component_hashes(self):
        cases = (
            ("core-only", "arm64", False, None),
            ("core-only", "amd64", False, None),
            ("siri-glass", "arm64", True, SIRI_ABI_HOST_GATE),
        )
        for profile, arch, included, host in cases:
            with self.subTest(profile=profile, arch=arch), tempfile.TemporaryDirectory() as directory:
                bundle = make_bundle(Path(directory), included)
                result = verify_fixture(bundle, arch, profile, host)
                siri_contract = result["component_contract"]["siri_glass"]
                siri_hash = hashlib.sha256((bundle / "Contents" / "Resources" / SIRI).read_bytes()).hexdigest() if included else None

                self.assertEqual(result["schema_version"], 2)
                self.assertEqual(result["component_contract"]["profile"], profile)
                self.assertEqual(siri_contract["selection"], "required" if included else "excluded")
                self.assertIs(siri_contract["included"], included)
                self.assertEqual(siri_contract["sha256"], siri_hash)
                self.assertEqual(result["sha256"].get("Resources/" + SIRI), siri_hash)
                self.assertIs(result["optional_components"]["siri_glass"]["included"], included)
                self.assertEqual(result["identifier"], "dev.mimac.app")
                self.assertEqual(result["executable"], "MiMac")
                self.assertEqual(result["sha256"]["Info.plist"], hashlib.sha256((bundle / "Contents" / "Info.plist").read_bytes()).hexdigest())
                gate = siri_contract["private_abi_host_gate"]
                self.assertEqual(gate["required"], SIRI_ABI_HOST_GATE)
                self.assertEqual(gate["status"], "satisfied" if included else "not-required")
                self.assertEqual(gate["observed"], host)

    def test_profiles_reject_component_and_host_mismatches(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            siri_bundle = make_bundle(root / "with-siri", True)
            with self.assertRaisesRegex(ValueError, "Siri glass to be forbidden"):
                verify_fixture(siri_bundle, "arm64", "core-only")

            missing_siri_bundle = make_bundle(root / "without-siri", False)
            with self.assertRaisesRegex(ValueError, "missing native libraries: libSiriGlassBridge.dylib"):
                verify_fixture(missing_siri_bundle, "arm64", "siri-glass", SIRI_ABI_HOST_GATE)

            with self.assertRaisesRegex(ValueError, "only for arm64"):
                verify_fixture(siri_bundle, "amd64", "siri-glass", SIRI_ABI_HOST_GATE)

            unsupported_host = {**SIRI_ABI_HOST_GATE, "build_version": "unsupported-build"}
            with self.assertRaisesRegex(ValueError, "requires host"):
                verify_fixture(siri_bundle, "arm64", "siri-glass", unsupported_host)

    def test_planned_version_edit_is_the_only_allowed_source_change(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, config_path, revision = make_repo(Path(directory) / "repo")
            prepare_versions(config_path)
            generated = repo / "resources" / "darwin-arm64" / "libMiMacSettings.dylib"
            generated.parent.mkdir(parents=True)
            generated.write_bytes(b"ignored generated library")

            result = verify_source(repo, revision, RELEASE_DATE, BUILD_NUMBER)

            self.assertEqual(result["revision"], revision)
            self.assertEqual(
                result["prepared_config_sha256"],
                hashlib.sha256(config_path.read_bytes()).hexdigest(),
            )
            self.assertEqual(set(result["allowed_modified_paths"]), {"mygo.json"})

    def test_clean_source_with_matching_planned_versions_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, config_path, revision = make_repo(
                Path(directory) / "repo",
                fixture_config(version=RELEASE_DATE, build_number=BUILD_NUMBER),
            )

            result = verify_source(repo, revision, RELEASE_DATE, BUILD_NUMBER)

            self.assertEqual(result["revision"], revision)
            self.assertEqual(
                result["prepared_config_sha256"],
                hashlib.sha256(config_path.read_bytes()).hexdigest(),
            )

    def test_source_rejects_extra_config_change(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, config_path, revision = make_repo(Path(directory) / "repo")
            prepare_versions(config_path)
            config = json.loads(config_path.read_text(encoding="utf-8"))
            config["name"] = "Unexpected name"
            write_json(config_path, config)

            with self.assertRaises(ValueError):
                verify_source(repo, revision, RELEASE_DATE, BUILD_NUMBER)

    def test_source_rejects_tracked_source_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, config_path, revision = make_repo(Path(directory) / "repo")
            prepare_versions(config_path)
            source_path = repo / "cmd" / "mimac" / "main.go"
            source_path.write_text("package main\n// unexpected edit\n", encoding="utf-8")

            with self.assertRaises(ValueError):
                verify_source(repo, revision, RELEASE_DATE, BUILD_NUMBER)

    def test_source_rejects_untracked_files(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, config_path, revision = make_repo(Path(directory) / "repo")
            prepare_versions(config_path)
            (repo / "unexpected.txt").write_text("untracked input\n", encoding="utf-8")

            with self.assertRaises(ValueError):
                verify_source(repo, revision, RELEASE_DATE, BUILD_NUMBER)

    def test_source_rejects_revision_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, config_path, revision = make_repo(Path(directory) / "repo")
            prepare_versions(config_path)
            wrong_revision = "f" * 40
            self.assertNotEqual(revision, wrong_revision)

            with self.assertRaises(ValueError):
                verify_source(repo, wrong_revision, RELEASE_DATE, BUILD_NUMBER)

    def test_plist_versions_must_match_release_plan(self):
        valid = {
            "CFBundleShortVersionString": RELEASE_DATE,
            "CFBundleVersion": BUILD_NUMBER,
        }
        verify_versions(valid, RELEASE_DATE, BUILD_NUMBER)

        invalid_infos = (
            {**valid, "CFBundleShortVersionString": "0.1.0"},
            {**valid, "CFBundleVersion": "41"},
            {"CFBundleShortVersionString": RELEASE_DATE},
            {"CFBundleVersion": BUILD_NUMBER},
        )
        for info in invalid_infos:
            with self.subTest(info=info), self.assertRaises(ValueError):
                verify_versions(info, RELEASE_DATE, BUILD_NUMBER)

    def test_build_info_matches_source_target_and_records_dirty_state(self):
        for arch, modified in (("arm64", "true"), ("amd64", "false")):
            with self.subTest(arch=arch, modified=modified):
                result = verify_build_info(build_info(arch=arch, modified=modified), arch, REVISION)
                self.assertEqual(result["go_version"], "go1.27.3")
                self.assertEqual(result["revision"], REVISION)
                self.assertIs(result["modified"], modified == "true")

    def test_build_info_rejects_wrong_identity_or_missing_vcs_evidence(self):
        invalid_cases = (
            ("Path", "mimac.local/other/cmd/mimac"),
            ("Main.Path", "mimac.local/other"),
            ("GOOS", "linux"),
            ("GOARCH", "amd64"),
            ("vcs", "hg"),
            ("vcs.revision", "f" * 40),
            ("vcs.modified", "maybe"),
        )
        for key, value in invalid_cases:
            with self.subTest(key=key, value=value):
                info = copy.deepcopy(build_info())
                if key == "Path":
                    info["Path"] = value
                elif key == "Main.Path":
                    info["Main"]["Path"] = value
                else:
                    set_build_setting(info, key, value)
                with self.assertRaises(ValueError):
                    verify_build_info(info, "arm64", REVISION)

        for key in ("vcs", "vcs.revision", "vcs.modified"):
            with self.subTest(missing=key):
                info = copy.deepcopy(build_info())
                info["Settings"] = [setting for setting in info["Settings"] if setting["Key"] != key]
                with self.assertRaises(ValueError):
                    verify_build_info(info, "arm64", REVISION)


if __name__ == "__main__":
    unittest.main()
