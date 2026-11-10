#!/usr/bin/env python3
"""Verify the actual Assistant trays using only synthetic HTTP/WS data.

Build web first. Run with a loopback production preview URL and --shots <directory>.
The test covers wide/narrow light/dark, the entries' capsule size and selected state, stable live task rows,
every open task in Progress (a stopped one too) with the closed ones folded, plans waiting for Start under their own
To start entry, decisions and draft sends.
"""
import argparse
import copy
import json
import re
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright

AT = "2026-11-10T08:00:00Z"
MAIN = "demo-main"
MACHINE = {"id": "demo", "name": "Computer A", "online": True, "os": "linux"}
TITLES = ["Assistant layout · web", "Extension migration", "Release verification", "Assistant layout · mobile", "Diet card"]
PLAN = {"id": "demo-plan", "conversation": MAIN, "created_at": AT, "state": "started", "tasks": [{"title": title, "prompt": "Run synthetic checks", "machine": "demo", "cwd": "/demo/project", "todo_id": f"demo-task-{i}"} for i, title in enumerate(TITLES)]}
OPEN_PLAN = {"id": "demo-next-plan", "conversation": MAIN, "created_at": AT, "state": "open", "tasks": [{"title": "Prepare the next demo", "prompt": "Prepare a synthetic demo", "machine": "demo", "cwd": "/demo/project"}]}
SESSIONS = [{"machine_id": "demo", "session_id": MAIN, "title": "Persona", "state": "idle", "status": "idle", "cwd": "/demo", "updated_at": AT, "started_by_persona": True, "capabilities": {"send": True, "interrupt": True}}] + [{"machine_id": "demo", "session_id": f"worker-{i}", "title": title, "state": ["working", "working", "needs_you", "unread", "idle"][i], "status": ["working", "working", "needs_you", "unread", "idle"][i], "cwd": "/demo/project", "updated_at": AT, "agent": "claude", "plan": {"done": 3, "total": 4, "current": "Run the remaining checks"}} for i, title in enumerate(TITLES)]
TODOS = [{"id": f"demo-task-{i}", "title": title, "origin": MAIN, "plan_id": PLAN["id"], "created_at": AT, "updated_at": AT, "agents": [{"machine_id": "demo", "session_id": f"worker-{i}", "linked_at": AT}], "pr_url": f"https://github.com/example/demo/pull/{2801 + i}", "supervision": {"supervisor": MAIN, "state": "accepted" if i == 3 else "needs_user" if i == 2 else "", "progress": {"kind": "blocked" if i == 2 else "progress", "step": i + 1, "steps": 5, "text": "Waiting for a decision" if i == 2 else "Run the demo checks", "at": AT}}, **({"done_at": AT} if i == 3 else {})} for i, title in enumerate(TITLES)]
ITEMS = [{"id": "demo-user", "seq": 1, "role": "user", "kind": "text", "text": "Simplify the conversation and dispatch the work.", "ts": AT}, {"id": "demo-answer", "seq": 2, "role": "assistant", "kind": "text", "text": "I split the work into separate tasks. Decisions stay in Pending; each task has one live row in Progress.", "ts": AT}]
FEED = [{"id": "demo-plan-event", "conversation": MAIN, "at": AT, "kind": "plan", "ref": PLAN["id"]}] + [{"id": f"demo-task-event-{i}", "conversation": MAIN, "at": AT, "kind": "task", "ref": f"demo-task-{i}"} for i in range(len(TITLES))] + [{"id": "demo-review-event", "conversation": MAIN, "at": AT, "kind": "review", "ref": "demo-task-3", "body": {"verdict": "accepted", "note": "Synthetic acceptance result"}}, {"id": "demo-report-event", "conversation": MAIN, "at": AT, "kind": "report", "ref": "demo-task-0", "body": {"kind": "progress", "text": "Synthetic progress report"}}, {"id": "demo-receipt", "conversation": MAIN, "at": AT, "kind": "receipt", "ref": "demo-task-3", "body": {"state": "done", "title": TITLES[3], "summary": "Synthetic completion receipt"}}, {"id": "demo-draft", "conversation": MAIN, "at": AT, "kind": "message", "ref": "demo-draft-ref", "body": {"sender": "Demo sender", "chat": "Demo group", "text": "When are the checks ready?", "draft": "The demo checks are ready for review."}}]
CARD_METADATA = [{"id": f"plan:demo-plan:{i}", "plan_id": PLAN["id"], "index": i, "todo_id": f"demo-task-{i}", "at": AT, "updated_at": AT, "state": "working"} for i in range(len(TITLES))]
ASK = {"id": "demo-permission", "machine_id": "demo", "session_id": "worker-2", "kind": "permission", "agent": "claude", "tool_name": "Bash", "tool_input": {"command": "npm test", "description": "Run the synthetic checks"}, "created_at": AT}


def run(args):
    shots = Path(args.shots)
    shots.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=args.chromium, args=["--no-sandbox"])
        try:
            for width in (1280, 390):
                for theme in ("light", "dark"):
                    todos = copy.deepcopy(TODOS)
                    todos[2]["ask"] = {"outcome": "decide", "verdict": "Choose the demo rollout", "recommend": "Use the demo rollout", "asked_at": AT}
                    remaining = [copy.deepcopy(ASK)]
                    writes = []
                    sockets = []
                    context = browser.new_context(viewport={"width": width, "height": 900}, locale="en-US", color_scheme=theme, reduced_motion="reduce")
                    context.add_init_script(f"localStorage.setItem('persona.token', 'demo'); localStorage.setItem('persona.language', 'en'); localStorage.setItem('persona.theme', '{theme}');")
                    context.add_init_script("const RealDate = Date; globalThis.Date = class extends RealDate { constructor(...args) { super(...(args.length ? args : ['2026-11-10T08:10:00Z'])); } static now() { return new RealDate('2026-11-10T08:10:00Z').getTime(); } };")

                    def api(route):
                        path = urlparse(route.request.url).path.removeprefix("/api/v1")
                        data = {}
                        if path == "/machines":
                            data = {"machines": [MACHINE]}
                        elif path == "/sessions":
                            data = {"sessions": SESSIONS}
                        elif path == "/interactions":
                            data = {"interactions": remaining}
                        elif path == "/assistant":
                            data = {"machine_id": "demo", "main": MAIN, "conversations": [SESSIONS[0]], "tasks": [], "memory": []}
                        elif path == f"/assistant/conversations/{MAIN}/feed":
                            data = {"items": FEED, "plans": {PLAN["id"]: PLAN, OPEN_PLAN["id"]: OPEN_PLAN}, "task_cards": CARD_METADATA}
                        elif path == "/todos":
                            data = {"todos": todos}
                        elif path.endswith("/items"):
                            data = {"items": ITEMS, "has_more": False}
                        elif re.fullmatch(r"/sessions/demo/[^/]+", path):
                            data = {"session": next(s for s in SESSIONS if s["session_id"] == path.rsplit("/", 1)[-1])}
                        elif path.endswith("/accept") and route.request.method == "POST":
                            body = json.loads(route.request.post_data or "{}")
                            writes.append((path, body))
                            assert body.get("asked_at") == AT, body
                            todos[2].pop("ask", None)
                            todos[2]["supervision"]["state"] = ""
                            data = {"ok": True}
                        elif path.startswith("/interactions/") and route.request.method == "POST":
                            writes.append((path, json.loads(route.request.post_data or "{}")))
                            remaining.clear()
                            data = {"ok": True}
                        elif route.request.method == "POST" and "message" in path:
                            writes.append((path, json.loads(route.request.post_data or "{}")))
                            data = {"ok": True}
                        elif path in ("/extensions", "/managers"):
                            data = {"extensions": [], "managers": []}
                        route.fulfill(status=200, content_type="application/json", body=json.dumps(data))

                    def socket(ws):
                        sockets.append(ws)
                        ws.send(json.dumps({"type": "hello", "server_version": "demo", "hub_name": "Demo hub", "features": []}))
                        ws.on_message(lambda message: ws.send(json.dumps({"type": "pong"})) if "app_ping" in message else None)

                    context.route("**/api/v1/**", api)
                    context.route_web_socket("**/api/v1/ws**", socket)
                    page = context.new_page()
                    errors = []
                    page.on("pageerror", lambda error: errors.append(str(error)))
                    page.set_default_timeout(15000)
                    page.goto(args.url.rstrip("/") + "/")
                    bar = page.locator("[data-assistant-actionbar]")
                    expect(bar).to_be_visible()
                    expect(page.locator("[data-dispatch-hint]")).to_have_count(1)
                    expect(page.locator("[data-receipt], [data-task-slot]")).to_have_count(0)
                    expect(page.get_by_text("Synthetic acceptance result", exact=True)).to_have_count(0)
                    expect(page.get_by_text("Synthetic progress report", exact=True)).to_have_count(0)
                    expect(page.locator('[data-testid="message-card"]')).to_have_count(0)

                    def capture(name):
                        expect(page.locator("vite-error-overlay")).to_have_count(0)
                        assert not errors, errors
                        assert bar.evaluate("e => { const box = e.getBoundingClientRect(); return box.x >= 0 && box.right <= innerWidth && box.bottom <= innerHeight; }")
                        # Reduced motion still runs 1 ms transitions (styles.css); headless Chrome draws frames only on
                        # demand, so let two frames finish them before the shot.
                        page.evaluate("() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")
                        page.screenshot(path=str(shots / f"{width}-{theme}-{name}.png"))

                    # The entries are the dock's old "All" / task capsules (#2999): one rem-based height, fully round,
                    # content-wide in a row, and the open tray is the selected capsule.
                    entries = bar.locator("[data-tray-entry]")
                    expect(entries).to_have_count(4)
                    assert entries.evaluate_all("es => es.map(e => e.dataset.trayEntry)") == ["pending", "start", "progress", "agents"]
                    # every open task: 4 of the plan's (one is closed) and the plan task waiting for Start
                    expect(bar.locator('[data-tray-entry="progress"]')).to_have_text("Progress5")
                    expect(bar.locator('[data-tray-entry="start"]')).to_have_text("To start1")
                    boxes = entries.evaluate_all("""es => es.map(e => { const box = e.getBoundingClientRect(); const style = getComputedStyle(e);
                        return { h: box.height, w: box.width, top: box.top, radius: parseFloat(style.borderTopLeftRadius), font: style.fontSize,
                                 capsule: 2 * parseFloat(getComputedStyle(document.documentElement).fontSize) }; })""")
                    assert all(b["h"] == b["capsule"] for b in boxes), boxes
                    assert all(b["radius"] >= b["h"] / 2 for b in boxes), boxes
                    assert len({b["top"] for b in boxes}) == 1 and len({b["font"] for b in boxes}) == 1, boxes
                    bar_right = bar.evaluate("e => e.getBoundingClientRect().right")
                    if width >= 640:
                        assert sum(b["w"] for b in boxes) < bar.evaluate("e => e.getBoundingClientRect().width"), boxes
                    else:
                        # a phone-wide window scrolls the row sideways; Pending, To start and Progress are in view unscrolled
                        rights = entries.evaluate_all("es => es.map(e => e.getBoundingClientRect().right)")
                        assert all(r <= bar_right for r in rights[:3]), (rights, bar_right)
                    assert entries.evaluate_all("es => es.map(e => e.getAttribute('aria-expanded'))") == ["false"] * 4
                    print(f"  entry heights {[b['h'] for b in boxes]} px, fully round, font {boxes[0]['font']}", flush=True)
                    capture("collapsed")
                    # keyboard: the entry takes focus and Enter toggles its tray, as a click does
                    pending_entry = bar.locator('[data-tray-entry="pending"]')
                    pending_entry.focus()
                    expect(pending_entry).to_be_focused()
                    page.keyboard.press("Enter")
                    expect(pending_entry).to_have_attribute("aria-expanded", "true")
                    expect(bar.locator("section")).to_have_count(1)
                    page.keyboard.press("Enter")
                    expect(pending_entry).to_have_attribute("aria-expanded", "false")
                    expect(bar.locator("section")).to_have_count(0)
                    pending_entry.blur()
                    page.locator("[data-dispatch-hint]").click()
                    expect(bar.locator("[data-task-slot]")).to_have_count(5)
                    # what needs the user first, then the plan task waiting for Start; the stopped task is listed too
                    expect(bar.locator("[data-task-slot]").first).to_have_attribute("data-task-slot", "plan:demo-plan:2")
                    expect(bar.locator('[data-task-slot="plan:demo-next-plan:0"]')).to_contain_text("Waiting for your Start")
                    expect(bar.locator('[data-task-slot="plan:demo-plan:4"] [data-task-mark="follow_up"]')).to_have_count(1)
                    expect(bar.locator('[data-task-slot="plan:demo-plan:3"]')).to_have_count(0)
                    expect(bar.locator("[data-progress-closed]")).to_have_text("Closed 1")
                    expect(bar.get_by_text("PR #2801", exact=False)).to_be_visible()
                    expect(bar.locator('[role="progressbar"] .bg-orange')).to_have_count(1)
                    progress_entry = bar.locator('[data-tray-entry="progress"]')
                    expect(progress_entry).to_have_attribute("aria-expanded", "true")
                    expect(progress_entry).to_have_class(re.compile(r"\bbg-ink\b"))
                    assert progress_entry.evaluate("e => e.getBoundingClientRect().height") == boxes[2]["h"]
                    capture("progress")
                    row = bar.locator('[data-task-slot="plan:demo-plan:0"]')
                    row.evaluate("e => globalThis.originalTaskRow = e")
                    todos[0]["supervision"]["progress"] = {"kind": "blocked", "step": 3, "steps": 5, "text": "Waiting for demo review", "at": AT}
                    for ws in sockets:
                        ws.send(json.dumps({"type": "todos"}))
                    expect(row.get_by_text("Waiting for demo review", exact=False)).to_be_visible()
                    assert row.evaluate("e => e === globalThis.originalTaskRow")
                    todos[0]["done_at"] = AT
                    todos[0]["supervision"]["state"] = "accepted"
                    for ws in sockets:
                        ws.send(json.dumps({"type": "todos"}))
                    # closed now: it leaves the open rows and folds under Closed, where it reads Accepted
                    expect(row).to_have_count(0)
                    expect(progress_entry).to_have_text("Progress4")
                    bar.locator("[data-progress-closed]").click()
                    expect(row.get_by_text("Accepted", exact=True)).to_be_visible()
                    expect(row.get_by_text("Waiting for demo review", exact=False)).to_have_count(0)
                    expect(row.locator('[role="progressbar"]')).to_have_count(0)
                    expect(bar.locator('[data-task-slot="plan:demo-plan:3"] .text-green')).to_have_count(1)
                    capture("accepted-live")
                    # a tap on the plan task waiting for Start opens To start, where Start is one click away
                    bar.locator('[data-task-slot="plan:demo-next-plan:0"]').click()
                    start_entry = bar.locator('[data-tray-entry="start"]')
                    expect(start_entry).to_have_attribute("aria-expanded", "true")
                    expect(bar.get_by_text("Prepare the next demo", exact=False)).to_be_visible()
                    expect(bar.locator("section").get_by_role("button", name="Start", exact=True)).to_be_visible()
                    capture("start")
                    bar.locator('[data-tray-entry="pending"]').click()
                    expect(bar.get_by_role("button", name="Allow", exact=True)).to_be_visible()
                    expect(bar.locator('[data-testid="message-card"]')).to_have_count(1)
                    # not counted twice: the plan waiting for Start is in To start, not in Pending
                    expect(bar.get_by_text("Prepare the next demo", exact=False)).to_have_count(0)
                    capture("pending")
                    assert not writes, writes
                    bar.get_by_role("button", name="Allow", exact=True).click()
                    page.wait_for_timeout(150)
                    assert any(body.get("allow") is True for _, body in writes), writes
                    assert not any("message" in path for path, _ in writes), writes
                    owner = bar.get_by_role("button", name="Take recommendation", exact=True)
                    owner.scroll_into_view_if_needed()
                    expect(bar.locator('[data-brief-row="accept"]')).to_have_count(1)
                    capture("pending-owner")
                    owner.click()
                    page.wait_for_timeout(150)
                    assert any(path.endswith("/accept") and body.get("asked_at") == AT for path, body in writes), writes
                    bar.locator('[data-tray-entry="agents"]').click()
                    expect(bar.get_by_text(TITLES[0], exact=True)).to_be_visible()
                    capture("agents")
                    bar.get_by_role("button", name="Working", exact=True).click()
                    expect(bar.get_by_text(TITLES[3], exact=True)).to_have_count(0)
                    bar.locator('[data-tray-entry="progress"]').click()
                    page.locator("[data-progress-closed][aria-expanded=true]").click()
                    capture("progress-open-only")
                    assert not any("message" in path for path, _ in writes), writes
                    print(f"PASS {width} {theme}: real app; every open task in Progress; To start; real count meter; Pending/Agents; draft not sent", flush=True)
                    context.close()
        finally:
            browser.close()
    print("PASS Assistant task tray browser verification", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("--shots", required=True)
    parser.add_argument("--chromium", default="/usr/bin/google-chrome")
    run(parser.parse_args())
