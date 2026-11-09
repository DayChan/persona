#!/usr/bin/env python3
"""End-to-end check of the to-dos' calendar on the web (#2029, docs/assistant/todos.md "Calendar"): List | Calendar
switches and is kept in the address, the week / month / day show the mock's to-dos (due today at 10:00, 16:00 and two
at 18:00), a block opens the to-do beside the calendar, a click on the empty axis makes a to-do due there, and the bar
under the calendar goes back to the list. On a phone's width the week is the day. The page runs in zh-CN, as the other
web e2e checks, so the selectors use the Chinese labels.

Needs a running web page against the fake hub, and Playwright for Python:

    PORT=18899 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18899 npx vite --port 5899 --strictPort &)
    python3 scripts/test/web_todo_calendar_e2e.py http://127.0.0.1:5899 --hub http://127.0.0.1:18899 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs light and dark, at desktop and phone
widths; --shots saves each state there. --channel chrome / msedge uses an installed browser instead of Playwright's
own Chromium.
"""
import argparse
import json
import os
import sys
import time
import urllib.request

from playwright.sync_api import expect, sync_playwright

SIZES = {"wide": {"width": 1280, "height": 860}, "narrow": {"width": 390, "height": 844}}
HOUR = 48  # px an hour on the axis (views/TodoCalendar.tsx)


def ok(msg):
    print("ok  " + msg, flush=True)


def call(hub, token, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(hub + path, data=data, method=method,
                                 headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=10))


def todos(hub, token):
    return call(hub, token, "GET", "/api/v1/todos")["todos"]


def until(check, what):
    deadline = time.time() + 10
    while time.time() < deadline:
        if check():
            return
        time.sleep(0.2)
    raise AssertionError("timed out: " + what)


def run(browser, base, hub, token, size, theme, shots):
    tag = f"{size}/{theme}"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=size == "narrow", locale="zh-CN",
                              color_scheme=theme)
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}'); localStorage.removeItem('persona.todos.view'); localStorage.removeItem('persona.todos.calendar.mode')")

    def shot(name):
        if shots:
            page.wait_for_timeout(300)
            page.screenshot(path=os.path.join(shots, f"todo-calendar-{size}-{theme}-{name}.png"))

    # List | Calendar: the address keeps the calendar, its mode and its day.
    page.goto(base + "/todos")
    page.get_by_role("radio", name="日历", exact=True).click()
    cal = page.locator('[data-testid="todo-calendar"]')
    expect(cal).to_be_visible()
    assert "view=calendar" in page.url, page.url
    # a phone's width has no week: the day instead
    expect(cal).to_have_attribute("data-mode", "week" if size == "wide" else "day")
    weather = page.locator('[data-testid="todo-calendar-entry"][data-todo-id="todo_weather"]')
    blog = page.locator('[data-testid="todo-calendar-entry"][data-todo-id="todo_blog"]')
    expect(weather).to_be_visible()
    expect(blog).to_be_visible()
    shot("week" if size == "wide" else "day")
    ok(f"{tag}: the calendar shows today's to-dos, the two at 18:00 side by side")

    page.get_by_role("radio", name="月", exact=True).click()
    expect(page.locator('[data-testid="todo-calendar-month"]')).to_be_visible()
    assert "mode=month" in page.url, page.url
    shot("month")
    page.get_by_role("radio", name="日", exact=True).click()
    expect(cal).to_have_attribute("data-mode", "day")
    if size == "wide":
        shot("day")
    ok(f"{tag}: month and day")

    # A block opens its to-do beside the calendar (on a phone: instead of it); closing comes back.
    page.locator('[data-testid="todo-calendar-entry"][data-todo-id="todo_weather"]').click()
    detail = page.locator('[data-testid="todo-calendar-detail"]')
    expect(detail).to_be_visible()
    expect(detail.get_by_role("textbox").first).to_have_value("天气 App 发 1.3 版")  # the title, edited in place
    assert "id=todo_weather" in page.url, page.url
    shot("detail")
    if size == "wide":
        detail.get_by_role("button", name="关闭").click()
    else:
        detail.get_by_role("button", name="返回日历").click()
    expect(detail).to_be_hidden()
    ok(f"{tag}: a block opens its to-do")

    # A click on the empty axis at 13:00 makes a to-do due then.
    title = f"日历里记的事 {size} {theme}"
    page.evaluate(f"document.querySelector('[data-testid=\"todo-calendar-axis\"] .overflow-y-auto').scrollTop = {12 * HOUR}")
    column = page.locator('[data-testid="todo-calendar-column"]').first
    box = column.bounding_box()
    column.click(position={"x": box["width"] * 0.8, "y": 13 * HOUR + 6})
    dialog = page.get_by_role("dialog")
    expect(dialog.locator('[data-testid="todo-new-due-time"]')).to_have_value("13:00")
    dialog.locator("input[type=text]").fill(title)
    shot("add")
    dialog.get_by_role("button", name="创建").click()
    until(lambda: any(t["title"] == title and t.get("due_at") for t in todos(hub, token)), "the new to-do due at 13:00 on the hub")
    new = next(t for t in todos(hub, token) if t["title"] == title)
    expect(page.locator(f'[data-testid="todo-calendar-entry"][data-todo-id="{new["id"]}"]')).to_be_visible()
    call(hub, token, "DELETE", f"/api/v1/todos/{new['id']}")
    ok(f"{tag}: a click on the axis makes a to-do due there")

    # Those without a due date: the bar goes back to the list.
    page.locator('[data-testid="todo-calendar-undated"]').click()
    expect(page.locator('[data-testid="todos-list-column"]')).to_be_visible()
    assert "view=calendar" not in page.url, page.url
    ok(f"{tag}: the bar under the calendar goes back to the list")
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
