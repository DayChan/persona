#!/usr/bin/env python3
"""End-to-end check of a to-do's due date, reminder and priority on the web (#1683, docs/assistant/todos.md "Due date,
reminder, priority"): the Tasks list shows them under the status line (red once overdue), the detail edits them, and
each change reaches the hub. The page runs in zh-CN, as the other web e2e checks, so the selectors use the Chinese labels.

Needs a running web page against the fake hub, and Playwright for Python:

    PORT=18899 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18899 npx vite --port 5899 --strictPort &)
    python3 scripts/test/web_todo_plan_e2e.py http://127.0.0.1:5899 --hub http://127.0.0.1:18899 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs light and dark, at desktop and phone
widths; --shots saves each state there. --channel chrome / msedge uses an installed browser instead of Playwright's
own Chromium.
"""
import argparse
import datetime
import json
import os
import sys
import time
import urllib.request

from playwright.sync_api import expect, sync_playwright

SIZES = {"wide": {"width": 1280, "height": 860}, "narrow": {"width": 390, "height": 844}}
NAS = "todo_nas"  # the mock's own to-do with a due day, a reminder and high priority


def ok(msg):
    print("ok  " + msg, flush=True)


def call(hub, token, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(hub + path, data=data, method=method,
                                 headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=10))


def todo(hub, token, tid):
    return next(t for t in call(hub, token, "GET", "/api/v1/todos")["todos"] if t["id"] == tid)


def until(check, what):
    deadline = time.time() + 10
    while time.time() < deadline:
        if check():
            return
        time.sleep(0.2)
    raise AssertionError("timed out: " + what)


def run(browser, base, hub, token, size, theme, shots):
    tag = f"{size}/{theme}"
    day = (datetime.date.today() + datetime.timedelta(days=2)).isoformat()
    remind = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=18)).strftime("%Y-%m-%dT%H:%M:%SZ")
    call(hub, token, "PATCH", f"/api/v1/todos/{NAS}", {"due_date": day, "remind_at": remind, "priority": "high"})  # the fixture
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=size == "narrow", locale="zh-CN",
                              color_scheme=theme)
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")

    def shot(name):
        if shots:
            page.wait_for_timeout(300)
            page.screenshot(path=os.path.join(shots, f"todo-plan-{size}-{theme}-{name}.png"))

    # The list: the NAS to-do's line has high priority, its due day and a reminder; the overdue photos one is red.
    page.goto(base + "/todos")
    row = page.locator('[role="button"]', has_text="给 NAS 配自动备份").first
    expect(row).to_be_visible()
    line = row.locator('[data-testid="todo-plan-line"]')
    expect(line).to_contain_text("截止")
    expect(line.get_by_role("img", name="高优先级")).to_be_visible()
    overdue = page.locator('[role="button"]', has_text="照片同步脚本支持 HEIC").first.locator('[data-testid="todo-plan-line"]')
    expect(overdue).to_contain_text("已过截止")
    shot("list")
    ok(f"{tag}: the list shows the due date, priority and the overdue one")

    # The detail: change the priority and give the due day a time; the hub gets both.
    row.click()
    plan = page.locator('[data-testid="todo-plan"]')
    expect(plan).to_be_visible()
    expect(plan.locator('[data-testid="todo-plan-due"]')).to_have_value(day)
    expect(plan.locator('[data-testid="todo-plan-priority"]')).to_have_value("high")
    shot("detail")
    plan.locator('[data-testid="todo-plan-priority"]').select_option("low")
    until(lambda: todo(hub, token, NAS).get("priority") == "low", "priority low on the hub")
    plan.locator('[data-testid="todo-plan-due-time"]').fill("18:00")
    until(lambda: bool(todo(hub, token, NAS).get("due_at")) and not todo(hub, token, NAS).get("due_date"), "due_at on the hub")
    expect(plan.locator('[data-testid="todo-plan-due-time"]')).to_have_value("18:00")
    shot("edited")
    ok(f"{tag}: priority and due time saved")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    ap.add_argument("--channel", default="")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel=a.channel or None)
        try:
            for size in SIZES:
                for theme in ("light", "dark"):
                    run(browser, a.base, a.hub, a.token, size, theme, a.shots)
        except Exception as e:  # noqa: BLE001 — one line and a verdict, as the other e2e scripts
            print(f"FAIL {e}", flush=True)
            sys.exit(1)
        finally:
            browser.close()
    print("PASS", flush=True)


if __name__ == "__main__":
    main()
