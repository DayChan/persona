"""TestFlight retries must reuse uploaded builds and verify Internal availability."""
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

PATH = Path(__file__).with_name("testflight_action.py")
spec = importlib.util.spec_from_file_location("testflight_action", PATH)
action = importlib.util.module_from_spec(spec)
spec.loader.exec_module(action)


class TestFlightActionTests(unittest.TestCase):
    def invoke(self, home, phase, visible=True):
        state = Path(home) / "testflight-checkpoint.json"
        state.write_text(json.dumps({"commit": "a" * 40, "build": "100", "phase": phase}))
        build = {"id": "build-id", "attributes": {"processingState": "VALID"}}
        asc = types.SimpleNamespace(
            build_by_version=lambda number: build,
            group_id=lambda: "group-id",
            request=lambda *args: {"data": {"attributes": {"hasAccessToAllBuilds": visible}}},
            get_all=lambda *args: [],
        )
        argv = [str(PATH), "--repo", home, "--worktree", home + "/worktree", "--state-dir", home, "--commit", "a" * 40]
        with patch.dict(os.environ, PERSONA_OPERATION_RELEASE_LOCKED="1"), patch.object(sys, "argv", argv), \
                patch.object(action, "prepare"), patch.object(action, "run") as run, \
                patch.object(action.subprocess, "call") as upload, patch.dict(sys.modules, asc_testflight=asc):
            result = action.main()
            upload.assert_not_called()
            run.assert_called_once()
            self.assertEqual(run.call_args.args[0][-3:], ["wait", "45", "100"])
        self.assertEqual(result, 0)
        self.assertEqual(json.loads(state.read_text())["phase"], "complete")

    def test_uploaded_build_is_not_uploaded_again(self):
        with tempfile.TemporaryDirectory() as home:
            self.invoke(home, "uploaded")

    def test_interrupted_upload_found_remotely_is_resumed(self):
        with tempfile.TemporaryDirectory() as home:
            self.invoke(home, "upload_started")

    def test_available_build_after_upload_error_is_resumed(self):
        with tempfile.TemporaryDirectory() as home:
            self.invoke(home, "upload_failed")

    def test_internal_visibility_is_required(self):
        with tempfile.TemporaryDirectory() as home:
            with self.assertRaisesRegex(RuntimeError, "Internal"):
                self.invoke(home, "uploaded", visible=False)
            self.assertEqual(json.loads((Path(home) / "testflight-checkpoint.json").read_text())["phase"], "uploaded")


if __name__ == "__main__":
    unittest.main()
