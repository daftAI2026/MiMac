import plistlib
from pathlib import Path
import tempfile
import unittest

from check_app_identity import BUNDLE_IDENTIFIER, IdentityConflict, check_app_identity


def write_app(root, relative_path, identifier):
    bundle = Path(root) / relative_path
    info_path = bundle / "Contents" / "Info.plist"
    info_path.parent.mkdir(parents=True, exist_ok=True)
    with info_path.open("wb") as file:
        plistlib.dump({"CFBundleIdentifier": identifier}, file)
    return bundle


class CheckAppIdentityTests(unittest.TestCase):
    def test_unique_current_formal_bundle_passes(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / "mimac"
            research = Path(directory) / "mimac-research"
            formal = write_app(project, "dist/darwin-arm64/MiMac.app", BUNDLE_IDENTIFIER)

            result = check_app_identity(project, research, "arm64")

            self.assertEqual(result["matching_apps"], [str(formal.resolve())])
            self.assertEqual(result["allowed_bundle"], str(formal.resolve()))
            self.assertFalse(result["research_root_exists"])

    def test_same_id_old_candidate_fails_and_lists_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / "mimac"
            research = Path(directory) / "mimac-research"
            formal = write_app(project, "dist/darwin-arm64/MiMac.app", BUNDLE_IDENTIFIER)
            stale = write_app(research, "candidate-old/MiMac.app", BUNDLE_IDENTIFIER)

            with self.assertRaises(IdentityConflict) as caught:
                check_app_identity(project, research, "arm64")

            message = str(caught.exception)
            self.assertIn(str(formal.resolve()), message)
            self.assertIn(str(stale.resolve()), message)

    def test_renamed_same_id_bundle_under_source_tree_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / "mimac"
            research = Path(directory) / "mimac-research"
            formal = write_app(project, "dist/darwin-arm64/MiMac.app", BUNDLE_IDENTIFIER)
            renamed = write_app(research, "source/preserved/MiMac-836c9b7a.app", BUNDLE_IDENTIFIER)

            with self.assertRaises(IdentityConflict) as caught:
                check_app_identity(project, research, "arm64")

            self.assertIn(str(formal.resolve()), str(caught.exception))
            self.assertIn(str(renamed.resolve()), str(caught.exception))

    def test_different_id_research_bundle_is_allowed(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / "mimac"
            research = Path(directory) / "mimac-research"
            formal = write_app(project, "dist/darwin-arm64/MiMac.app", BUNDLE_IDENTIFIER)
            write_app(research, "dist/darwin-arm64/Probe.app", "dev.mimac.research.probe")

            result = check_app_identity(project, research, "arm64")

            self.assertEqual(result["matching_apps"], [str(formal.resolve())])

    def test_missing_research_root_does_not_block_ci(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / "mimac"
            formal = write_app(project, "dist/darwin-amd64/MiMac.app", BUNDLE_IDENTIFIER)
            missing_research = Path(directory) / "not-present"

            result = check_app_identity(project, missing_research, "amd64")

            self.assertEqual(result["matching_apps"], [str(formal.resolve())])
            self.assertFalse(result["research_root_exists"])


if __name__ == "__main__":
    unittest.main()
