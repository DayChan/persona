#!/usr/bin/env python3
"""End-to-end check of an independent review of a session's work (#1808): the chat's ⋯ menu offers 独立审查这项工作,
clicking it asks the hub to start one (the hub's refusal, when it refuses one, is shown in its own words); the finished
review's findings arrive as one card on the session's chat — "N 条发现", the report, 不发了 / 发给 Agent — Send answers
the card with action send and the card goes, Dismiss with dismiss and the card goes; a review without a count line says
审查完成.

Needs a running web page against the fake hub with MOCK_FILES=1 MOCK_AGENT_REVIEW=1 MOCK_REVIEW=1 (scripts/mock/mock_agent_review.py: its GET
/mock/agent-review switches the state and reads back what the page asked) and Playwright for Python:

    PORT=18873 MOCK_FILES=1 MOCK_AGENT_REVIEW=1 MOCK_REVIEW=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18873 npx vite --port 5193 --strictPort &)
    python3 scripts/test/web_agent_review_e2e.py http://127.0.0.1:5193 --hub http://127.0.0.1:18873 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs light and dark at desktop and phone
widths; --shots saves each state there. --channel msedge, or --executable <path>, where there is no Chrome.
"""
import argparse
import json
import os
import sys
import urllib.request

from playwright.sync_api import TimeoutError as PlaywrightTimeoutError, expect, sync_playwright

SIZES = {"wide": {"width": 1440, "height": 900}, "narrow": {"width": 390, "height": 844}}
MID, SID = "macbook", "a1f3c2d4-0000-4000-8000-000000000006"  # the weather app's idle chat
BUSY = "a1f3c2d4-0000-4000-8000-000000000001"  # the same folder's chat, working


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def mock(hub, token, query="", route="agent-review"):
    req = urllib.request.Request(f"{hub}/mock/{route}{query}", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def run(browser, base, hub, token, size, theme, shots):
    tag = f"{size}/{theme}"
    narrow = size == "narrow"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=narrow, locale="zh-CN")
    page = ctx.new_page()
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.set_default_timeout(20000)
    # the app's theme is its own localStorage pick, not the browser's color scheme
    page.goto(f"{base}/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")

    def shot(name):
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=os.path.join(shots, f"agent-review-{size}-{theme}-{name}.png"))

    def open_chat(card=True):
        mock(hub, token, "?state=" + ("card" if card else "none") + "&count=1&refuse=&running=0")
        page.goto(f"{base}/s/{MID}/{SID}")
        expect(page).to_have_url(f"{base}/s/{MID}/{SID}")
        try:
            page.wait_for_selector("[data-composer], textarea")
        except PlaywrightTimeoutError:
            print(f"{tag}: composer missing at {page.url}; page errors: {errors}", flush=True)
            print(page.locator("body").inner_text(timeout=5000)[:2000], flush=True)
            raise
        page.wait_for_function("dark => document.documentElement.classList.contains('dark') === dark", arg=theme == "dark")

    def menu_item():
        page.get_by_role("button", name="更多").first.click()
        return page.get_by_role("menuitem", name="独立审查这项工作")

    def close_menu():
        page.keyboard.press("Escape")
        # On phones closing the menu schedules history.back(); wait for it before navigating again.
        page.wait_for_function("!history.state?.personaOverlay")

    # the menu entry starts a review; the hub notes the ask
    mock(hub, token, "?mode=1", route="review")
    open_chat(card=False)
    mock(hub, token, "?reset=1")
    item = menu_item()
    expect(item).to_be_visible()
    expect(item).to_be_enabled()
    shot("menu")
    item.click()
    expect(page.get_by_text("已开始独立审查，完成后这里会出现结果卡片")).to_be_visible()
    starts = mock(hub, token)["starts"]
    if len(starts) != 1 or starts[0]["machine"] != MID or starts[0]["session"] != SID:
        fail(f"{tag}: the hub was not asked to review {SID}: {starts}")
    ok(f"{tag}: the menu entry starts a review of this session")
    close_menu()

    # a busy chat keeps the entry, disabled with its reason
    mock(hub, token, "?state=none&refuse=")
    page.goto(f"{base}/s/{MID}/{BUSY}")
    page.wait_for_selector("[data-composer], textarea")
    page.get_by_role("button", name="更多").first.click()
    busy = page.get_by_role("menuitem", name="独立审查这项工作")
    expect(busy).to_be_visible()
    expect(busy).to_be_disabled()
    expect(busy).to_contain_text("它正在忙")
    ok(f"{tag}: a busy chat keeps the entry, disabled with its reason")
    close_menu()

    # a clean repository keeps the entry disabled with its reason
    mock(hub, token, "?mode=clean", route="review")
    open_chat(card=False)
    clean = menu_item()
    expect(clean).to_be_disabled()
    expect(clean).to_contain_text("没有可审查的改动")
    shot("clean")
    ok(f"{tag}: a clean repository disables the entry with its reason")
    close_menu()
    mock(hub, token, "?mode=1", route="review")

    # a refusal comes back in the hub's own words
    open_chat(card=False)
    mock(hub, token, "?refuse=session_busy")
    item = menu_item()
    expect(item).to_be_enabled()
    item.click()
    expect(page.get_by_text("这个 Agent 正在工作，等它停下来再审查")).to_be_visible()
    ok(f"{tag}: the hub's refusal is shown in its own words")
    close_menu()

    # the finished review's card: the count line, the report, both buttons
    open_chat(card=True)
    card = page.locator(f'[data-card^="review_"]')
    expect(card).to_be_visible()
    expect(card).to_contain_text("3 条发现")
    expect(card).to_contain_text("Forecast.swift:42")
    expect(card.get_by_role("button", name="发给 Agent")).to_be_visible()
    expect(card.get_by_role("button", name="不发了")).to_be_visible()
    shot("card")
    ok(f"{tag}: the findings card shows the count, the report and both buttons")

    # Send forwards the findings and the card goes
    mock(hub, token, "?reset=1")
    card.get_by_role("button", name="发给 Agent").click()
    expect(card).to_have_count(0)
    answers = mock(hub, token)["answers"]
    if len(answers) != 1 or answers[0].get("action") != "send":
        fail(f"{tag}: Send did not answer the card: {answers}")
    ok(f"{tag}: Send answers the card and it goes")

    # Dismiss closes it without sending
    open_chat(card=True)
    card = page.locator(f'[data-card^="review_"]')
    mock(hub, token, "?reset=1")
    card.get_by_role("button", name="不发了").click()
    expect(card).to_have_count(0)
    answers = mock(hub, token)["answers"]
    if len(answers) != 1 or not answers[0].get("dismiss"):
        fail(f"{tag}: Dismiss did not answer the card: {answers}")
    ok(f"{tag}: Dismiss closes the card without sending")

    # a review that did not count its findings says so, with no number
    mock(hub, token, "?count=0")
    page.reload()
    page.wait_for_selector("[data-composer], textarea")
    card = page.locator(f'[data-card^="review_"]')
    expect(card).to_be_visible()
    expect(card).to_contain_text("审查完成")
    expect(card).not_to_contain_text("条发现")
    shot("no-count")
    ok(f"{tag}: a review without a count says 审查完成")
    if errors:
        fail(f"{tag}: uncaught page errors: {errors}")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    ap.add_argument("--channel", default="chrome", help="the installed browser Playwright drives (chrome, msedge)")
    ap.add_argument("--executable", help="a browser to drive instead of --channel")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=a.executable) if a.executable else p.chromium.launch(channel=a.channel)
        for size, theme in (("wide", "light"), ("wide", "dark"), ("narrow", "light"), ("narrow", "dark")):
            run(browser, a.base, a.hub, a.token, size, theme, a.shots)
        browser.close()
    print("PASS web agent review", flush=True)


if __name__ == "__main__":
    main()
