"""The mock hub's several managers (docs/assistant/managers.md "Clients (step 4)"), served the way the real hub serves them.

Starts scripts/mock/mock_hub.py on a free port with a HOME of its own (MOCK_EXTENSIONS=1 MOCK_MANAGER_NAS=1) and checks:
- a POST with a body that is not JSON (an image upload) is answered as before: only the move routes read JSON;
- the merged extensions list has the built-ins under every installed manager (each manager's extmgr adds them);
- a computer of an offline manager is offline with the hub's words and no error_code, its session (there is one) in state
  unknown;
- each manager keeps its own inbox mutes and rules: one set for another manager covers only its items, and an offline
  one answers 503 assistant_offline.

    python3 scripts/test/test_mock_hub_managers.py
"""
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class MockHubManagersTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.port = free_port()
        cls.home = tempfile.mkdtemp(prefix="mock-managers-")
        env = {k: v for k, v in os.environ.items() if not k.lower().endswith("_proxy")}
        env.update(HOME=cls.home, PORT=str(cls.port), MOCK_EXTENSIONS="1", MOCK_MANAGER_NAS="1")
        cls.proc = subprocess.Popen([sys.executable, str(ROOT / "scripts/mock/mock_hub.py")], env=env,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + 30
        while time.time() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", cls.port), timeout=0.5):
                    return
            except OSError:
                time.sleep(0.2)
        cls.proc.kill()
        raise RuntimeError("the mock hub did not start within 30 s")

    @classmethod
    def tearDownClass(cls):
        cls.proc.terminate()
        try:
            cls.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.proc.kill()

    def call(self, method, path, body=None, content_type="application/json", manager=None):
        headers = {"Authorization": "Bearer mock", "Content-Type": content_type}
        if manager:
            headers["X-Persona-Manager"] = manager
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}{path}", data=body, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, json.loads(resp.read() or b"null")
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"null")

    def test_personal_agent_entry_states_are_consistent(self):
        for state in ("configured", "empty", "missing", "disabled", "no_accounts", "none"):
            with self.subTest(state=state):
                status, answer = self.call("GET", "/mock/personal-agents?reset=1&state=" + state)
                self.assertEqual(status, 200, answer)
                _, sources = self.call("GET", "/api/v1/agents")
                _, sessions = self.call("GET", "/api/v1/agents/sessions")
                self.assertEqual(bool(sessions["sessions"]), state == "configured")
                if state == "none":
                    self.assertEqual(sources["sources"], [])
                else:
                    source = sources["sources"][0]
                    self.assertEqual(bool(source["accounts"]), state in ("configured", "empty"))
                    self.assertEqual(source.get("setup"), state if state in ("missing", "disabled", "no_accounts") else None)
        self.call("GET", "/mock/personal-agents?reset=1")

    def test_a_raw_body_post_is_answered(self):
        image = bytes(range(256)) * 4  # not JSON
        status, answer = self.call("POST", "/api/v1/lark/conversations/f1/files?client_id=c1&name=a.png", image, "image/png")
        self.assertEqual(status, 200, answer)
        self.assertTrue(answer.get("message_id"), answer)

    def test_the_move_routes_read_json_only(self):
        status, _ = self.call("POST", "/api/v1/managers/m-3fa21c/machines", b"\x89PNG not json", "image/png")
        self.assertEqual(status, 400)
        status, answer = self.call("POST", "/api/v1/managers/m-3fa21c/machines", json.dumps({"machine_id": "studio"}).encode())
        self.assertEqual((status, answer.get("manager"), answer.get("from")), (200, "m-3fa21c", "assistant"), answer)
        status, _ = self.call("POST", "/api/v1/managers/assistant/machines", json.dumps({"machine_id": "studio"}).encode())
        self.assertEqual(status, 200)

    def test_built_ins_are_every_managers(self):
        status, answer = self.call("GET", "/api/v1/extensions")
        self.assertEqual(status, 200, answer)
        items = answer["extensions"]
        builtins = sorted({x["id"] for x in items if x.get("source") == "builtin"})
        self.assertTrue(builtins, "the mock lists built-in extensions")
        installed = sorted(["assistant", "m-3fa21c", "m-8b04d2"])
        for ext in builtins:
            self.assertEqual(sorted(x["manager"] for x in items if x["id"] == ext), installed, ext)
        # the default's answer first, every item stamped, the others' own ones under them
        self.assertEqual(items[0]["manager"], "assistant")
        self.assertTrue(all(x.get("manager") for x in items))
        for mid in ("m-3fa21c", "m-8b04d2"):
            self.assertTrue([x for x in items if x["manager"] == mid and x.get("source") != "builtin"], mid)

    def test_an_offline_managers_computer(self):
        _, answer = self.call("GET", "/api/v1/machines")
        nas = next(m for m in answer["machines"] if m["id"] == "nas")
        self.assertEqual((nas["manager"], nas["online"], nas.get("error_code")), ("m-8b04d2", False, None))
        self.assertTrue(nas.get("error"))
        _, answer = self.call("GET", "/api/v1/sessions")
        theirs = [s for s in answer["sessions"] if s.get("manager") == "m-8b04d2"]
        self.assertTrue(theirs, "the offline manager's computer has a session to check")
        for s in theirs:
            self.assertEqual((s.get("machine_id"), s.get("state")), ("nas", "unknown"), s)

    def test_each_managers_mutes_and_rules(self):
        _, listed = self.call("GET", "/api/v1/inbox?sources=all")
        theirs = next(x for x in listed["items"] if x["manager"] == "m-3fa21c" and x.get("mutes"))
        tag = theirs["mutes"][0]["tag"]
        status, answer = self.call("POST", "/api/v1/inbox/mutes", json.dumps({"item_id": theirs["id"], "tag": tag}).encode(), manager="m-3fa21c")
        self.assertEqual(status, 200, answer)
        _, mine = self.call("GET", "/api/v1/inbox/mutes")
        _, work = self.call("GET", "/api/v1/inbox/mutes", manager="m-3fa21c")
        self.assertNotIn(answer["mute"]["id"], [m["id"] for m in mine["mutes"]])
        self.assertIn(answer["mute"]["id"], [m["id"] for m in work["mutes"]])
        status, _ = self.call("DELETE", "/api/v1/inbox/mutes/" + answer["mute"]["id"], manager="m-3fa21c")
        self.assertEqual(status, 200)
        status, answer = self.call("POST", "/api/v1/inbox/policies", json.dumps({"item_id": theirs["id"], "tag": tag, "tier": "fyi"}).encode(), manager="m-3fa21c")
        self.assertEqual(status, 200, answer)
        _, mine = self.call("GET", "/api/v1/inbox/policies")
        self.assertNotIn(answer["policy"]["id"], [p["id"] for p in mine["policies"]])
        status, _ = self.call("DELETE", "/api/v1/inbox/policies/" + answer["policy"]["id"], manager="m-3fa21c")
        self.assertEqual(status, 200)
        for path in ("/api/v1/inbox/mutes", "/api/v1/inbox/policies"):
            status, answer = self.call("GET", path, manager="m-8b04d2")
            self.assertEqual((status, answer.get("code")), (503, "assistant_offline"), path)


if __name__ == "__main__":
    unittest.main()
