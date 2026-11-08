"""Serving evidence uses VCS settings and exact installed web artifact receipts."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("hub_action.py")
spec = importlib.util.spec_from_file_location("hub_action", SCRIPT)
action = importlib.util.module_from_spec(spec)
spec.loader.exec_module(action)
COMMIT = "a" * 40


class ServingReceiptTests(unittest.TestCase):
    def test_trimmed_build_metadata_does_not_need_linker_flags(self):
        info = "binary: go1.27.1\n\tbuild\t-trimpath=true\n\tbuild\tvcs.revision=" + COMMIT + "\n\tbuild\tvcs.modified=false\n"
        self.assertTrue(action.vcs_matches(info, COMMIT))

    def test_wrong_or_dirty_revision_is_not_accepted(self):
        for revision, modified in (("b" * 40, "false"), (COMMIT, "true")):
            self.assertFalse(action.vcs_matches("build\tvcs.revision=" + revision + "\nbuild\tvcs.modified=" + modified, COMMIT))

    def test_linker_flag_without_vcs_provenance_is_not_accepted(self):
        self.assertFalse(action.vcs_matches("build\t-ldflags=-X main.commit=" + COMMIT, COMMIT))

    def test_missing_clean_flag_is_not_accepted(self):
        self.assertFalse(action.vcs_matches("build\tvcs.revision=" + COMMIT, COMMIT))

    def test_matching_bundled_web_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            built, installed = Path(directory) / "built", Path(directory) / "installed"
            built.mkdir(); installed.mkdir()
            for root in (built, installed):
                (root / "index.html").write_text("same web artifact")
            receipt = {"commit": COMMIT, "web_sha256": action.tree_digest(built)}
            self.assertTrue(action.bundled_web_matches(receipt, COMMIT, built, installed))
            self.assertFalse(action.bundled_web_matches(receipt, "b" * 40, built, installed))
            (installed / "index.html").write_text("another deployment")
            self.assertFalse(action.bundled_web_matches(receipt, COMMIT, built, installed))

    def test_missing_empty_and_symlink_trees_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(RuntimeError):
                action.tree_digest(root / "missing")
            with self.assertRaises(RuntimeError):
                action.tree_digest(root)
            (root / "actual").write_text("artifact")
            (root / "link").symlink_to(root / "actual")
            with self.assertRaises(RuntimeError):
                action.tree_digest(root)

    def test_receipt_requires_exact_paths_as_well_as_content(self):
        with tempfile.TemporaryDirectory() as directory:
            built, installed = Path(directory) / "built", Path(directory) / "installed"
            built.mkdir(); installed.mkdir()
            (built / "index.html").write_text("same bytes")
            (installed / "different.html").write_text("same bytes")
            receipt = {"commit": COMMIT, "web_sha256": action.tree_digest(built)}
            self.assertFalse(action.bundled_web_matches(receipt, COMMIT, built, installed))


if __name__ == "__main__":
    unittest.main()
