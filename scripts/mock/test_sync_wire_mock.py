"""The mock hub's frame faults and the chat truth for the sync tests (#1073): /mock/wire drops, doubles, delays and holds
frames on their way to the apps; /mock/sync/card raises a card in any session and its answer reaches every app;
/mock/sync/truth is what the chat should show."""
import base64
import json
import os
import socket
import time
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen
from test_mock_hub_mesh import Mock

ASSISTANT_CHAT = "b2e4d6f8-0000-4000-8000-000000000001"


class SyncWireMockTests(unittest.TestCase):
    def setUp(self):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.hub = Mock(port)
        self.addCleanup(self.hub.stop)

    def post(self, path, body):
        req = Request(self.hub.base + path, data=json.dumps(body).encode(), method="POST", headers={"Content-Type": "application/json"})
        try:
            with urlopen(req, timeout=5) as r:
                return r.status, json.load(r)
        except HTTPError as e:
            with e:
                return e.code, json.loads(e.read() or b"null")

    def websocket(self):
        """An app's socket: a function giving its next frame of `type` (None when none comes within `wait` seconds)."""
        sock = socket.create_connection(("127.0.0.1", self.hub.port), timeout=5)
        self.addCleanup(sock.close)
        key = base64.b64encode(os.urandom(16)).decode()
        sock.sendall((f"GET /api/v1/ws HTTP/1.1\r\nHost: 127.0.0.1:{self.hub.port}\r\nUpgrade: websocket\r\n"
                      f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
        buf = bytearray()

        def fill(n, end):  # at least n bytes buffered by `end` (time.monotonic); what came stays buffered
            while len(buf) < n:
                left = end - time.monotonic()
                if left <= 0:
                    return False
                sock.settimeout(left)
                try:
                    chunk = sock.recv(65536)
                except (socket.timeout, TimeoutError):
                    return False
                if not chunk:
                    return False
                buf.extend(chunk)
            return True

        end = time.monotonic() + 5
        while b"\r\n\r\n" not in buf:
            self.assertTrue(fill(len(buf) + 1, end), "no upgrade")
        head, _, rest = bytes(buf).partition(b"\r\n\r\n")
        self.assertIn(b" 101 ", head)
        buf[:] = rest

        def read_frame(end):
            if not fill(2, end):
                return None
            n, off = buf[1] & 0x7F, 2
            if n == 126:
                if not fill(4, end):
                    return None
                n, off = int.from_bytes(buf[2:4], "big"), 4
            elif n == 127:
                if not fill(10, end):
                    return None
                n, off = int.from_bytes(buf[2:10], "big"), 10
            if not fill(off + n, end):
                return None
            payload = bytes(buf[off:off + n])
            del buf[:off + n]
            return json.loads(payload)

        def next_of(kind, wait=2.0):
            end = time.monotonic() + wait
            while True:
                f = read_frame(end)
                if f is None:
                    return None
                if f.get("type") == kind:
                    return f
        self.assertIsNotNone(next_of("hello"))
        while read_frame(time.monotonic() + 0.5) is not None:  # the connection snapshot (machines, sessions, cards)
            pass
        return next_of

    def card(self):
        status, out = self.post("/mock/sync/card", {"session": ASSISTANT_CHAT})
        self.assertEqual(status, 200, out)
        return out["id"]

    def test_each_rule_takes_the_frames_it_names(self):
        next_of = self.websocket()
        self.post("/mock/wire", {"rules": [{"type": "interaction", "action": "drop"}]})
        self.card()
        self.assertIsNone(next_of("interaction", 1.0), "a dropped frame never arrives")
        self.post("/mock/wire", {"rules": [{"type": "interaction", "action": "dup"}]})
        doubled = self.card()
        self.assertEqual([next_of("interaction")["interaction"]["id"], next_of("interaction")["interaction"]["id"]], [doubled, doubled])
        self.post("/mock/wire", {"rules": [{"type": "interaction", "action": "delay", "ms": 1200}]})
        late = self.card()
        self.assertIsNone(next_of("interaction", 0.6), "not before its delay")
        self.assertEqual(next_of("interaction", 2.0)["interaction"]["id"], late)
        self.post("/mock/wire", {"rules": [{"type": "interaction", "action": "hold", "count": 2}]})
        first, second = self.card(), self.card()
        self.assertIsNone(next_of("interaction", 0.6), "held until released")
        self.assertEqual(self.hub.get("/mock/wire")["held"], ["interaction", "interaction"])
        self.assertEqual(self.post("/mock/wire/release", {"order": "reverse"}), (200, {"released": 2}))
        self.assertEqual([next_of("interaction")["interaction"]["id"], next_of("interaction")["interaction"]["id"]], [second, first])
        after = self.card()
        self.assertEqual(next_of("interaction")["interaction"]["id"], after, "a rule ends with its count")
        self.assertEqual([x["action"] for x in self.hub.get("/mock/wire")["log"]], ["drop", "dup", "delay", "hold", "hold"])

    def test_a_card_from_sync_is_in_the_truth_until_answered_and_every_app_hears_it_ended(self):
        a, b = self.websocket(), self.websocket()
        ix = self.card()
        self.assertEqual(a("interaction")["interaction"]["id"], ix)
        self.assertEqual(b("interaction")["interaction"]["id"], ix)
        truth = self.hub.get("/mock/sync/truth?session=" + ASSISTANT_CHAT)
        self.assertIn(ix, truth["cards"])
        self.assertTrue(truth["messages"], "the conversation's turns")
        self.assertTrue(all(m["from"] in ("user", "agent") for m in truth["messages"]))
        self.assertEqual(self.post(f"/api/v1/interactions/{ix}/resolve", {"allow": True})[0], 200)
        self.assertEqual(a("interaction_resolved")["id"], ix)
        self.assertEqual(b("interaction_resolved")["id"], ix)
        self.assertNotIn(ix, self.hub.get("/mock/sync/truth?session=" + ASSISTANT_CHAT)["cards"])

    def test_bad_rules_are_refused_and_reset_clears_them(self):
        for body in ({}, {"rules": []}, {"rules": [{"type": "items"}]}, {"rules": [{"type": "items", "action": "lose"}]}):
            self.assertEqual(self.post("/mock/wire", body)[0], 400, body)
        self.assertEqual(self.post("/mock/sync/card", {"session": "nope"})[0], 400)
        self.post("/mock/wire", {"rules": [{"type": "items", "action": "hold", "count": 5}]})
        self.hub.get("/mock/reset")
        self.assertEqual(self.hub.get("/mock/wire"), {"rules": [], "held": [], "log": []})


if __name__ == "__main__":
    unittest.main()
