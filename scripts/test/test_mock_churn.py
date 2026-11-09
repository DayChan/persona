"""Churn questions follow the hub's non-reusable interaction identity contract."""
import ast
import copy
from datetime import datetime, timezone
from pathlib import Path
import unittest
from unittest.mock import patch
import uuid

ROOT = Path(__file__).resolve().parents[2]


class MockChurnIdentityTests(unittest.TestCase):
    def setUp(self):
        # Execute the actual churn function without starting unrelated screenshot services.
        source = ROOT / "scripts/mock/mock_hub.py"
        tree = ast.parse(source.read_text(), filename=str(source))
        function = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "churn")
        self.frames = []
        self.interactions = [{"id": "fixture-base", "machine_id": "fixture-machine", "session_id": "fixture-session"}]
        self.scope = {
            "datetime": datetime, "timezone": timezone, "uuid": uuid,
            "SESSIONS": [], "MACHINES": [], "INBOX": [], "CHURN_INBOX": [],
            "INTERACTIONS": self.interactions, "CHURN": {"rounds": 0},
            "LIVE": [(lambda frame: self.frames.append(copy.deepcopy(frame)), set())],
            "out_by_machine": lambda rows: copy.deepcopy(rows),
            "all_sessions": lambda: [], "out_machines": lambda: [],
            "mock_interactions": lambda: self.interactions,
        }
        code = compile(ast.Module(body=[function], type_ignores=[]), str(source), "exec")
        exec(code, self.scope)

    def churn(self, rounds):
        with patch("time.sleep"):
            self.scope["churn"](rounds, 0, waiting=True)

    def assert_identity_lifecycle(self):
        shown, retired = set(), set()
        for frame in self.frames:
            if frame["type"] == "interaction":
                identity = frame["interaction"]["id"]
                self.assertNotIn(identity, retired, "a resolved ID became a different question")
                shown.add(identity)
            elif frame["type"] == "interaction_resolved":
                identity = frame["id"]
                retired.add(identity)
                shown.discard(identity)
        pending = {row["id"] for row in self.interactions if row["id"].startswith("ix_churn")}
        self.assertEqual(shown, pending, "the pushed cards and pending snapshot disagree")
        return retired

    def test_three_rounds_do_not_revive_a_resolved_question(self):
        self.churn(3)
        retired = self.assert_identity_lifecycle()
        added = [frame["interaction"]["id"] for frame in self.frames if frame["type"] == "interaction"]
        self.assertEqual(len(added), 2)
        self.assertEqual(len(set(added)), 2)
        self.assertEqual(len(retired), 1)

    def test_a_second_churn_call_also_uses_fresh_identities(self):
        self.churn(3)
        self.churn(3)
        self.assert_identity_lifecycle()
        added = [frame["interaction"]["id"] for frame in self.frames if frame["type"] == "interaction"]
        self.assertEqual(len(added), len(set(added)))
        self.assertEqual(len(added), 4)

    def test_resolving_the_last_question_is_pushed_even_with_no_pending_base(self):
        self.interactions[:] = [{"id": "ix_churn_previous", "machine_id": "fixture-machine"}]
        self.churn(1)
        self.assertEqual(self.interactions, [])
        self.assertIn({"type": "interaction_resolved", "id": "ix_churn_previous"}, self.frames)
        self.assertFalse(any(frame["type"] == "interaction" for frame in self.frames))


if __name__ == "__main__":
    unittest.main()
