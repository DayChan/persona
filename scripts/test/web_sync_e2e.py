#!/usr/bin/env python3
"""End-to-end sync check of a Persona conversation in a real browser against the mock hub, which injects faults into
what it sends (#1073, #1144). Seeded rounds: the user sends a message (the mock writes it into the transcript), the
agent replies, a card comes and the user answers it, and before some of them a fault is set for the next frame of a
kind: dropped, doubled, delayed, or held and sent out of order; or the hub is gone for a moment.

At every look, no message the user sent is missing or shown twice (a bubble, the transcript's copy, or the words back
in the composer), and no card the user answered can be answered again. After each round has settled, what the page
shows, its messages and the cards it can answer, equals what the mock holds (GET /mock/sync/truth).

    MOCK_ECHO_SENT=1 MOCK_SYNC_REVS=1 PORT=19041 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:19041 npx vite --port 19042 --strictPort &)
    python3 scripts/test/web_sync_e2e.py http://127.0.0.1:19042 --hub http://127.0.0.1:19041 [--seed 1] [--rounds 24] [--browser <chrome>]

Prints one line per round and a verdict; exits 1 at the first failure, with the seed, the round and the difference.
"""
import argparse
import json
import random
import re
import sys
import time
import urllib.parse
import urllib.request

from playwright.sync_api import sync_playwright

CHAT = ("server", "b2e4d6f8-0000-4000-8000-000000000001")  # a Persona conversation of the mock's
# Faults the hub can show: an items push lost (the node's watch died, #759); any frame twice (a replay); items late or out
# of order against the session lists and against each other, as they come from the node's watch while the lists come
# from the hub. Not a card or its end dropped or turned around (the hub sends both under one lock, #3937), nor the
# session lists out of order among themselves (built in order under one lock, one socket).
FAULTS = {"drop": ("items",), "dup": ("items", "interaction", "interaction_resolved", "sessions_delta", "sessions"),
          "delay": ("items",), "hold": ("items",)}
SETTLE_S = 14  # the page's own catch-up: a gap fill after 1.5 s, the stale-page pull after 4 s of quiet, 10 s apart

# What the page shows, as the sync cases' view (web/src/views/chat/sync/project.ts): the user's messages and each
# turn's answer in screen order, the words in the composer, the cards and whether they can be answered.
PROJECT = """() => {
  const text = (el) => (el.textContent || "").replace(/\\s+/g, " ").trim();
  const messages = [];
  for (const el of document.querySelectorAll("[data-chat-scroll] [data-user-bubble], [data-chat-scroll] [data-answer]")) {
    if (el.hasAttribute("data-answer")) {
      if (el.dataset.answer !== "streaming") messages.push({ from: "agent", text: text(el) });
      continue;
    }
    const words = el.querySelector("[data-bubble-text]");
    const state = el.dataset.sendState;
    messages.push({ from: "user", text: words ? text(words) : "", state: state === "failed" ? "failed" : state ? "pending" : "shown" });
  }
  const box = document.querySelector("[data-chat-dock] textarea")?.value ?? "";
  const cards = {};
  for (const el of document.querySelectorAll("[data-card]")) {
    const live = [...el.querySelectorAll("[data-card-actions] button, button[data-card-answer]")].some((b) => !b.disabled);
    cards[el.dataset.card] = live ? "open" : "locked";
  }
  return { messages, box, cards };
}"""


def plain(text):
    """Words as a comparison sees them: the page shows Markdown drawn (no **, no list markers), the transcript has it raw."""
    lines = [re.sub(r"^\s*(?:#{1,6}\s+|\d+\.\s+|[-*+]\s+)", "", line) for line in text.split("\n")]
    return re.sub(r"\s+", "", re.sub(r"\*\*|__|`", "", "\n".join(lines)))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--rounds", type=int, default=24)
    ap.add_argument("--browser", help="a Chrome or Chromium executable (else the installed Chrome, else Playwright's own)")
    ap.add_argument("--shots", help="a folder for a screenshot of the page when a check fails")
    a = ap.parse_args()
    rng = random.Random(a.seed)
    run = format(int(time.time() * 1000) % 10**8, "08d")  # this run's words are its own, though the mock keeps earlier runs' turns

    def hub(path, body=None, **q):
        url = a.hub + path + ("?" + urllib.parse.urlencode(q) if q else "")
        req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(), method="GET" if body is None else "POST",
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as r:
            return json.load(r)

    page = None

    def fail(round_, msg):
        print(f"FAIL seed {a.seed}, round {round_}: {msg}", flush=True)
        print(f"     wire: {json.dumps(hub('/mock/wire'), ensure_ascii=False)[:600]}", flush=True)
        if page is not None:
            v = page.evaluate(PROJECT)
            print(f"     page: {[(m['from'], m.get('state'), m['text'][:40]) for m in v['messages'][-8:]]} box {v['box'][:60]!r}", flush=True)
            truth = hub("/mock/sync/truth", session=CHAT[1])
            print(f"     hub:  {[(m['from'], m['text'][:40]) for m in truth['messages'][-8:]]}", flush=True)
            print(f"     reads: {pulls[-6:]} (now {round(time.monotonic(), 1)})", flush=True)
            if a.shots:
                page.screenshot(path=f"{a.shots}/web-sync-seed{a.seed}-round{round_}.png", full_page=False)
        sys.exit(1)

    sent, answered = [], set()

    with sync_playwright() as p:
        if a.browser:
            browser = p.chromium.launch(executable_path=a.browser)
        else:
            try:
                browser = p.chromium.launch(channel="chrome")
            except Exception:
                browser = p.chromium.launch()
        page = browser.new_context(viewport={"width": 1280, "height": 900}, locale="en-US").new_page()
        pulls = []  # the page's own reads of the chat's items, for the report when a check fails
        page.on("request", lambda r: pulls.append((round(time.monotonic(), 1), r.url.split("/items")[-1][:60])) if "/items" in r.url else None)
        hub("/mock/reset")
        page.goto(a.base + "/?token=" + a.token)
        page.wait_for_timeout(800)
        for attempt in range(3):  # a cold dev server optimizes its dependencies and reloads the page once
            page.goto(f"{a.base}/s/{CHAT[0]}/{CHAT[1]}")
            try:
                page.locator("[data-chat-scroll] [data-user-bubble]").first.wait_for(timeout=15000)
                break
            except Exception:
                if attempt == 2:
                    fail(0, "the conversation did not open")
        page.wait_for_timeout(1500)

        def view():
            v = page.evaluate(PROJECT)
            truth = hub("/mock/sync/truth", session=CHAT[1])
            got = [(m["from"], plain(m["text"])) for m in v["messages"] if m.get("state") == "shown" or m["from"] == "agent"]
            want = [(m["from"], plain(m["text"])) for m in truth["messages"]]
            open_cards = sorted(k for k, s in v["cards"].items() if s == "open")
            sending = [m["text"] for m in v["messages"] if m["from"] == "user" and m.get("state") in ("pending", "failed")]
            # the page shows the latest turns (older ones come on scrolling up): it ends as the transcript does
            same = bool(got) and got == want[-len(got):] and open_cards == truth["cards"] and not sending
            return v, same, (got, want, open_cards, truth["cards"], sending)

        def invariants(round_, v):
            for words in sent:  # never missing, never twice
                copies = [m.get("state") for m in v["messages"] if m["from"] == "user" and m["text"] == words] + (["box"] if words in v["box"] else [])
                if len(copies) != 1:
                    fail(round_, f"{words!r} shows {len(copies)} times: {copies}")
            for ix in answered:
                if v["cards"].get(ix) == "open":
                    fail(round_, f"card {ix} was answered and can be answered again")

        def settle(round_):
            # what is held goes, out of order, and rules no frame took are dropped; then the page catches up on its own
            if hub("/mock/wire")["held"]:
                page.wait_for_timeout(rng.randint(200, 1500))
            hub("/mock/wire/release", {"order": "reverse", "clear": True})
            end = time.monotonic() + SETTLE_S
            while True:
                page.wait_for_timeout(1000)
                v, same, diff = view()
                invariants(round_, v)
                if same:
                    return
                if time.monotonic() >= end:
                    got, want, open_cards, pending, sending = diff
                    fail(round_, f"after {SETTLE_S} s the page shows {got[-6:]}, the transcript ends {want[-6:]}; "
                                 f"answerable {open_cards}, pending {pending}; still sending {sending}")

        for r in range(1, a.rounds + 1):
            if rng.random() < 0.6:
                fault = rng.choice(tuple(FAULTS) + ("down",))
                if fault == "down":
                    hub("/mock/down", s=rng.choice((1, 2, 3)))
                else:
                    kind = rng.choice(FAULTS[fault])
                    rule = {"type": kind, "action": fault, "count": rng.choice((1, 1, 2))}
                    if fault == "delay":
                        rule["ms"] = rng.randint(500, 3000)
                    hub("/mock/wire", {"rules": [rule]})
                    fault = f"{fault} {kind}"
            else:
                fault = "none"
            action = rng.choice(("send", "send", "reply", "card"))
            if action == "send":
                box = page.locator("[data-chat-dock] textarea")
                left = box.input_value()
                if left.strip():
                    # words still in the box (a send the page refused or that failed): the user sends those first, as they
                    # would, rather than typing over them
                    box.press("Enter")
                    page.wait_for_timeout(800)
                    if not box.input_value().strip() and left not in sent:
                        sent.append(left)
                    action = "send again"
                else:
                    words = f"Sync check {run}/{a.seed}-{r}: plan step {rng.randint(10, 99)}."
                    box.fill(words)
                    box.press("Enter")
                    page.wait_for_timeout(800)
                    if not box.input_value().strip():  # it went (the box empties when it does)
                        sent.append(words)
                    else:
                        action = "send refused"
            elif action == "reply":
                hub("/mock/say", text=f"Reply {run}/{a.seed}-{r}: done with step {rng.randint(10, 99)}.")
            else:
                ix = hub("/mock/sync/card", {"session": CHAT[1]})["id"]
                try:
                    page.locator(f'[data-card="{ix}"] [data-card-actions] button').filter(has_text="Allow").last.click(timeout=SETTLE_S * 1000)
                    answered.add(ix)
                except Exception:
                    pass  # it did not come within the wait (its frame was dropped): it is still pending, and the truth says so
            page.wait_for_timeout(300)
            invariants(r, page.evaluate(PROJECT))
            settle(r)
            print(f"ok  round {r}: {fault} + {action}", flush=True)
        print(f"ok  seed {a.seed}: {a.rounds} rounds, {len(sent)} sent, {len(answered)} cards answered; page and hub agree", flush=True)
        browser.close()


if __name__ == "__main__":
    main()
