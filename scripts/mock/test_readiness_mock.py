"""mock_hub.py's MOCK_READINESS scenarios answer what the hub answers (docs/operations/testing.md "Readiness scenarios").

Each scenario's readiness (three languages) and its coded start refusal are compared with the hub's own answers, which
server/internal/assistant/readiness_scenarios_test.go keeps in testdata/readiness_scenarios/; the built-in providers
with presets_builtin.json; the rest (dismiss, warm, probe, adding and confirming a computer, the refusals the fake hub
cannot make) with docs/protocol/protocol.md. Each case runs its own mock hub on a free port.
"""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
GOLDEN = ROOT / "server" / "internal" / "assistant" / "testdata" / "readiness_scenarios"
PRESETS = ROOT / "server" / "internal" / "assistant" / "presets_builtin.json"
# Scenarios with no golden: demo is full on the mock's usual data, assistant_offline is the hub's own 503.
NO_GOLDEN = {"demo", "assistant_offline"}
# The fake hub of the Go test cannot refuse a start for an offline computer (test_offline_start_is_the_offline_machines).
START_ELSEWHERE = {"offline"}


class Hub:
    def __init__(self, **env):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.base = "http://127.0.0.1:%d" % port
        self.log = tempfile.TemporaryFile()
        clean = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        self.process = subprocess.Popen(["python3", "-I", str(HERE / "mock_hub.py")],
                                        env=dict(clean, PORT=str(port), **env), stdout=self.log, stderr=self.log)
        for _ in range(100):
            if self.process.poll() is not None:
                self.log.seek(0)
                out = self.log.read().decode(errors="replace")[-2000:]
                self.log.close()
                raise RuntimeError("mock hub exited: %s" % out)
            try:
                with urlopen(self.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError, ConnectionError):
                time.sleep(0.1)
        self.stop()
        raise RuntimeError("mock hub startup timed out")

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.log.close()

    def call(self, method, path, body=None, lang=None):
        headers = {"Content-Type": "application/json"}
        if lang:
            headers["Accept-Language"] = lang
        data = None if body is None else (body if isinstance(body, bytes) else json.dumps(body).encode())
        req = Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urlopen(req, timeout=5) as res:
                return res.status, json.load(res)
        except HTTPError as e:
            return e.code, json.load(e)


def hub(test, **env):
    h = Hub(**env)
    test.addCleanup(h.stop)
    return h


def blank(r):
    return dict(r, checked_at="")


class ScenarioGoldenTests(unittest.TestCase):
    def test_every_scenario_has_its_golden(self):
        import sys
        sys.path.insert(0, str(HERE))
        import readiness_mock
        names = set(readiness_mock.SCENARIOS) - NO_GOLDEN
        self.assertEqual(names, {p.stem for p in GOLDEN.glob("*.json")})

    def test_scenarios_match_the_hub(self):
        for path in sorted(GOLDEN.glob("*.json")):
            want = json.loads(path.read_text(encoding="utf-8"))
            with self.subTest(scenario=path.stem):
                h = Hub(MOCK_READINESS=path.stem)
                try:
                    for lang in ("zh-Hans", "en", "ja"):
                        status, got = h.call("GET", "/api/v1/assistant/readiness", lang=lang)
                        self.assertEqual(status, 200)
                        self.assertTrue(got.pop("checked_at").endswith("Z"))
                        self.assertEqual(blank(got), want[lang], lang)
                    for key, lang in (("start", None), ("start.en", "en")):
                        status, body = h.call("POST", "/api/v1/assistant/conversations", {"prompt": "你好", "main": True}, lang)
                        if key in want:
                            self.assertEqual({"status": status, "body": body}, want[key], key)
                        elif path.stem not in START_ELSEWHERE:
                            self.assertEqual(status, 200, body)  # it starts (the turn fails afterwards)
                    status, warm = h.call("POST", "/api/v1/assistant/warm", {})
                    self.assertEqual((status, warm["ok"], blank(warm["readiness"])), (200, True, want["zh-Hans"]))
                finally:
                    h.stop()


class ReadinessMockTests(unittest.TestCase):
    def test_features(self):
        h = hub(self, MOCK_READINESS="zero")
        _, health = h.call("GET", "/api/v1/health")
        for f in ("assistant_readiness", "machines_add", "machine_trust", "builtin_presets"):
            self.assertIn(f, health["features"])
        _, plain = hub(self).call("GET", "/api/v1/health")
        self.assertNotIn("assistant_readiness", plain["features"])
        self.assertNotIn("builtin_presets", plain["features"])

    def test_presets_are_the_servers(self):
        raw = json.loads(PRESETS.read_text(encoding="utf-8"))
        h = hub(self, MOCK_BUILTIN_PRESETS="1")
        _, health = h.call("GET", "/api/v1/health")
        self.assertIn("builtin_presets", health["features"])
        self.assertNotIn("assistant_readiness", health["features"])
        _, zh = h.call("GET", "/api/v1/launchers/presets")
        _, en = h.call("GET", "/api/v1/launchers/presets", lang="en-US")
        builtin = [p for p in zh["presets"] if p["plugin"] == "builtin"]
        self.assertEqual(sorted(p["id"] for p in builtin), sorted(p["id"] for p in raw))
        # Chinese first lists the vendors one opens from China; others list the international ones first.
        self.assertEqual([p["id"] for p in builtin][:2], ["builtin/deepseek", "builtin/kimi"])
        self.assertEqual(en["presets"][0]["id"], "builtin/anthropic")
        glm = next(p for p in builtin if p["id"] == "builtin/glm")
        src = next(p for p in raw if p["id"] == "builtin/glm")
        self.assertEqual(glm["display_name"], "智谱 GLM")
        self.assertNotIn("region", glm)
        for k in ("name", "kind", "api", "base_url", "auth", "model", "small_model", "models", "signup_url"):
            self.assertEqual(glm[k], src[k], k)
        self.assertTrue(glm["description"])
        # The plugins' come after the built-in ones.
        self.assertEqual([p["plugin"] for p in zh["presets"]][len(builtin):], ["example-gateway", "example-gateway"])

    def test_region_blocked_key_test(self):
        # A key with "region" in it is Anthropic refusing the network's region (#4482), in the hub's words.
        h = hub(self, MOCK_BUILTIN_PRESETS="1", MOCK_PROVIDERS="1")
        _, zh = h.call("POST", "/api/v1/launchers/test", {"preset": "builtin/anthropic", "api_key": "sk-region-1"})
        self.assertEqual((zh["ok"], zh["code"]), (False, "region_blocked"))
        self.assertIn("DeepSeek、", zh["error"])
        self.assertNotIn("国际版", zh["error"])
        _, en = h.call("POST", "/api/v1/launchers/test", {"preset": "builtin/anthropic", "api_key": "sk-region-1"}, lang="en")
        self.assertIn("region", en["error"])
        _, plain = h.call("POST", "/api/v1/launchers/test", {"preset": "builtin/anthropic", "api_key": "sk-good-1"})
        self.assertNotIn("code", plain)

    def test_offline_start_is_the_offline_machines(self):
        h = hub(self, MOCK_READINESS="offline")
        status, body = h.call("POST", "/api/v1/assistant/conversations", {"prompt": "你好", "main": True})
        self.assertEqual((status, body["code"], body["retryable"], body["delivered"]), (503, "node_unreachable", True, "none"))
        _, machines = h.call("GET", "/api/v1/machines")
        self.assertEqual([(m["id"], m["online"], m["error_code"]) for m in machines["machines"]], [("mbp", False, "link_offline")])

    def test_pending_computer(self):
        h = hub(self, MOCK_READINESS="pending", MOCK_ADD_COMPUTER_JOIN="0")
        _, machines = h.call("GET", "/api/v1/machines")
        self.assertEqual([(m["id"], m["trust"]) for m in machines["machines"]], [("mbp", "pending")])
        status, body = h.call("POST", "/api/v1/sessions", {"machine_id": "mbp", "cwd": "/Users/me"})
        self.assertEqual((status, body["code"], body["error"]), (409, "machine_pending", "这台电脑还没确认是你的，确认后才能在上面开 Agent"))
        _, r = h.call("GET", "/api/v1/assistant/readiness?probe=1")
        self.assertEqual(r["machine"], {"id": "mbp", "name": machines["machines"][0]["name"]})
        self.assertEqual((r["steps"][0]["state"], r["steps"][0]["code"], r["steps"][0]["machine"],
                          r["steps"][0]["action"]["id"], r["ready"]),
                         ("todo", "machine_pending", "mbp", "confirm_computer", False))
        self.assertNotIn("probe", r)
        self.assertEqual([s["state"] for s in r["steps"][1:]], ["unknown"] * 3)
        status, body = h.call("POST", "/api/v1/assistant/conversations", {"prompt": "Hello", "main": True})
        self.assertEqual((status, body["code"], body["action"], body["delivered"], body["machine_name"]),
                         (409, "machine_pending", "confirm_computer", "none", r["machine"]["name"]))
        # Confirmed: it is the main conversation's computer, and the readiness says so.
        self.assertEqual(h.call("POST", "/api/v1/machines/mbp/confirm", {}), (200, {"ok": True}))
        _, r = h.call("GET", "/api/v1/assistant/readiness")
        self.assertEqual((r["machine"]["id"], r["steps"][0]["state"], r["mode"], r["next"]), ("mbp", "ok", "full", "model"))
        status, _ = h.call("POST", "/api/v1/assistant/conversations", {"prompt": "你好", "main": True})
        self.assertEqual(status, 200)

    def test_zero_then_add_a_computer(self):
        h = hub(self, MOCK_READINESS="zero", MOCK_ADD_COMPUTER_JOIN="0.3")
        status, code = h.call("POST", "/api/v1/auth/pair-codes", {"add": {"name": "办公室的电脑", "os": "linux"}})
        self.assertEqual((status, code["trust"]), (200, "pending"))
        time.sleep(0.8)
        _, r = h.call("GET", "/api/v1/assistant/readiness")
        self.assertEqual((r["steps"][0]["code"], r["steps"][0]["machine"]), ("machine_pending", code["machine"]))
        h.call("POST", "/api/v1/machines/%s/confirm" % code["machine"], {})
        _, r = h.call("GET", "/api/v1/assistant/readiness")
        self.assertEqual((r["machine"]["name"], r["steps"][0]["state"]), ("办公室的电脑", "ok"))

    def test_dismiss(self):
        h = hub(self, MOCK_READINESS="zero")
        self.assertEqual(h.call("POST", "/api/v1/assistant/readiness/dismiss", b""), (200, {"ok": True}))
        self.assertTrue(h.call("GET", "/api/v1/assistant/readiness")[1]["dismissed"])
        h.call("POST", "/api/v1/assistant/readiness/dismiss", {"dismissed": False})
        self.assertFalse(h.call("GET", "/api/v1/assistant/readiness")[1]["dismissed"])
        self.assertEqual(h.call("POST", "/api/v1/assistant/readiness/dismiss", b"{")[0], 400)

    def test_probe(self):
        h = hub(self, MOCK_READINESS="unverified")
        _, r = h.call("GET", "/api/v1/assistant/readiness?probe=1")
        self.assertEqual((r["probe"]["ok"], r["steps"][2]["state"]), (True, "ok"))
        _, r = h.call("GET", "/api/v1/assistant/readiness")
        self.assertNotIn("probe", r)
        self.assertEqual(r["steps"][2]["state"], "ok")  # what the probe found is kept
        _, r = hub(self, MOCK_READINESS="bad_key").call("GET", "/api/v1/assistant/readiness?probe=1")
        self.assertEqual((r["probe"]["ok"], r["steps"][2]["code"]), (False, "auth_failed"))
        _, r = hub(self, MOCK_READINESS="offline").call("GET", "/api/v1/assistant/readiness?probe=1")
        self.assertNotIn("probe", r)  # no computer to run it on

    def test_main_conversation_turn_error(self):
        h = hub(self, MOCK_READINESS="not_logged_in")
        _, a = h.call("GET", "/api/v1/assistant")
        main = next(c for c in a["conversations"] if c["session_id"] == a["main"])
        self.assertEqual((main["machine_id"], main["turn_error_code"], main["turn_error_kind"]), ("mbp", "not_logged_in", "user"))

    def test_start_error_override(self):
        for code, status in (("machine_pending", 409), ("no_machine", 409), ("node_outdated", 409), ("assistant_offline", 503)):
            with self.subTest(code=code):
                h = Hub(MOCK_READINESS="full", MOCK_START_ERROR=code)
                try:
                    got, body = h.call("POST", "/api/v1/assistant/conversations", {"prompt": "你好", "main": True})
                    self.assertEqual((got, body["code"]), (status, code))
                    if code == "machine_pending":
                        self.assertEqual((body["action"], body["machine_name"], body["error"]),
                                         ("confirm_computer", "小林的 MacBook", "这台电脑还没确认是你的。"))
                    if code == "node_outdated":
                        self.assertNotIn("action", body)
                finally:
                    h.stop()

    def test_assistant_offline(self):
        h = hub(self, MOCK_READINESS="assistant_offline")
        for method, path in (("GET", "/api/v1/assistant/readiness"), ("POST", "/api/v1/assistant/warm"),
                             ("POST", "/api/v1/assistant/conversations")):
            status, body = h.call(method, path, None if method == "GET" else {}, lang="en")
            self.assertEqual((status, body["code"]), (503, "assistant_offline"), path)
            self.assertEqual(body["error"], "Can’t reach the assistant right now. Try again later")

    def test_bad_scenario_names(self):
        for env in ({"MOCK_READINESS": "nope"}, {"MOCK_READINESS": "zero", "MOCK_START_ERROR": "nope"}):
            with self.assertRaises(RuntimeError):
                Hub(**env)


if __name__ == "__main__":
    unittest.main()
