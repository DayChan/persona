"""The iOS navigation fixture mirrors the read-only index and target-window contract."""
import importlib.util
from pathlib import Path
import unittest

MODULE = Path(__file__).resolve().parents[1] / "mock" / "mock_navigation.py"
spec = importlib.util.spec_from_file_location("navigation_fixture", MODULE)
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class Handler:
    def send_json(self, data, status=200):
        self.data, self.status = data, status


class NavigationFixtureTests(unittest.TestCase):
    def setUp(self):
        def last_turns(items, count):
            starts = [i for i, item in enumerate(items) if item.get("role") == "user"
                      and item.get("kind") == "text" and not item.get("injected")]
            start = starts[-count] if len(starts) >= count else 0
            return items[start:], start > 0
        self.namespace = {"ASK1": "demo-main", "ITEMS": {}, "PAGING": {"on": False}, "HISTORY_NOW": {},
                          "ASSISTANT": [{"session_id": "demo-main"}], "last_turns": last_turns}
        fixture.install(self.namespace)
        self.prefix = "/api/v1/sessions/server/demo-main"

    def get(self, path, query=None):
        handler = Handler()
        self.assertTrue(fixture.route(handler, path, query or {}, self.namespace))
        return handler.data, handler.status

    def test_latest_index_and_older_prefetch(self):
        page, status = self.get(self.prefix + "/navigation")
        self.assertEqual(status, 200)
        self.assertEqual(len(page["messages"]), 40)
        self.assertEqual(page["messages"][0]["seq"], 410)
        self.assertEqual(page["messages"][-1]["answer"], "Brief answer 80")
        older, _ = self.get(self.prefix + "/navigation", {"before_seq": [str(page["next_before_seq"])], "history": [page["history"]]})
        self.assertEqual(len(older["messages"]), 40)
        self.assertFalse(older["has_more"])
        self.assertEqual(older["messages"][0]["id"], "navigation-user-1#0")

    def test_latest_body_is_not_the_whole_index_and_target_loads(self):
        latest, _ = self.get(self.prefix + "/items", {"turns": ["2"]})
        real = [item for item in latest["items"] if item["role"] == "user" and item["kind"] == "text" and not item.get("injected")]
        self.assertEqual([item["seq"] for item in real], [790, 800])
        around, _ = self.get(self.prefix + "/items", {"around_seq": ["400"], "limit": ["40"], "history": [fixture.HISTORY]})
        self.assertEqual(len(around["items"]), 40)
        self.assertTrue(around["has_more"] and around["more_after"])
        self.assertTrue(any(item["id"] == "navigation-user-40#0" for item in around["items"]))
        self.assertLess(around["to_seq"], latest["to_seq"])

    def test_reset_restores_the_navigation_fixture_and_clears_requests(self):
        self.get(self.prefix + "/navigation")
        self.namespace["PAGING"]["on"] = False
        self.namespace["HISTORY_NOW"]["token"] = "ordinary-mock-history"
        fixture.install(self.namespace)
        self.assertTrue(self.namespace["PAGING"]["on"])
        self.assertEqual(self.namespace["HISTORY_NOW"]["token"], fixture.HISTORY)
        self.assertEqual(len(fixture.MESSAGES), 80)
        log, _ = self.get("/mock/navigation")
        self.assertEqual(log["requests"], [])

    def test_history_guard_and_observable_requests(self):
        _, status = self.get(self.prefix + "/items", {"around_seq": ["400"], "history": ["stale"]})
        self.assertEqual(status, 409)
        self.get(self.prefix + "/navigation")
        self.get(self.prefix + "/items", {"around_seq": ["400"]})
        log, _ = self.get("/mock/navigation")
        self.assertEqual([entry["kind"] for entry in log["requests"]], ["navigation", "around"])
        log, _ = self.get("/mock/navigation", {"reset": ["1"]})
        self.assertEqual(log["requests"], [])


if __name__ == "__main__":
    unittest.main()
