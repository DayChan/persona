#!/usr/bin/env python3
"""End-to-end check of the web sidebar's entries (#1239, #5109): Persona, the column's first row, opens the main conversation, the one-off new
conversation and New agent are the"+"of their sections, and the old links still land where they should. Persona stays pinned at
the top of the list while it scrolls and opens with Mod+Alt+P (#5581).

Needs a running web page against the fake hub and Playwright for Python (it drives the installed Chrome):

    PORT=18791 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18791 npx vite --port 5291 --strictPort &)
    python3 scripts/test/web_sidebar_e2e.py http://127.0.0.1:5291 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. --shots saves light / dark / narrow screenshots.
"""
import argparse
import json
import os
import re
import sys
import urllib.request

from playwright.sync_api import expect, sync_playwright

PERSONA = 'a[title="Persona 的主对话"]'
STARTER = "有什么要我做的？"


def ok(msg):
    print("ok  " + msg, flush=True)


def path_of(page):
    return page.evaluate("location.pathname")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    a = ap.parse_args()
    base = a.base.rstrip("/")
    req = urllib.request.Request(base + "/api/v1/assistant", headers={"Authorization": "Bearer " + a.token})
    assistant = json.load(urllib.request.urlopen(req, timeout=10))
    main_id = assistant["main"]
    other = next(c for c in assistant["conversations"] if c["session_id"] != main_id)
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)

    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            run(browser, base, a.token, main_id, other, a.shots)
        finally:
            browser.close()
    print("PASS web sidebar e2e")


def run(browser, base, token, main_id, other, shots):
    ctx = browser.new_context(viewport={"width": 1280, "height": 820}, device_scale_factor=2, locale="zh-CN")
    page = ctx.new_page()
    page.set_default_timeout(10000)

    # 1. The page opens on Persona's main conversation.
    page.goto(base + "/?token=" + token)
    persona = page.locator(PERSONA)
    expect(persona).to_have_attribute("aria-current", "page")
    expect(page.get_by_text(STARTER)).to_have_count(0)
    assert path_of(page) == "/", path_of(page)
    ok("/ opens the main conversation, Persona selected")

    # Persona, with the app's mark, is the column's first row, a line under it, above the pages (#2004, #5109).
    first = page.locator("aside [data-sidebar-rows] a").first
    expect(first).to_have_attribute("title", "Persona 的主对话")
    # one picture, the page theme's (ui/AppMark.tsx): the other theme's is not fetched
    expect(first.locator('img[src$="/app-icon.png"], img[src$="/app-icon-dark.png"]')).to_have_count(1)
    expect(first.get_by_text("主对话", exact=True)).to_have_count(0)
    expect(page.locator("aside [data-persona-divider]")).to_have_count(1)
    below = page.evaluate("""() => {
      const line = document.querySelector('aside [data-persona-divider]').getBoundingClientRect();
      const row = document.querySelector('aside a[title="Persona 的主对话"]').getBoundingClientRect();
      const pages = [...document.querySelectorAll('aside button')].find((b) => b.textContent.trim() === '页面').getBoundingClientRect();
      return row.bottom <= line.top && line.bottom <= pages.top;
    }""")
    assert below, "the line is not between Persona and the pages"
    chats = page.locator("aside .group\\/sec", has=page.locator("button", has_text="Persona 对话")).locator("xpath=..")
    expect(chats.locator(PERSONA)).to_have_count(0)
    ok("Persona + app mark is the first row, a line under it, above the pages; not in「Persona 对话」")
    # folding"Persona chats"and Pages keeps the main row on show
    for title in ("Persona 对话", "页面"):
        head = page.locator("aside button[aria-expanded]", has_text=title).first
        head.click()
        expect(head).to_have_attribute("aria-expanded", "false")
        expect(persona).to_be_visible()
        head.click()
        expect(head).to_have_attribute("aria-expanded", "true")
    ok("folding「Persona 对话」or 页面 leaves the main row on show")

    # Persona stays pinned at the top of the list while the rest scrolls under it (#5581), and shows its shortcut.
    list_box = page.locator("aside [data-sidebar-rows]").bounding_box()
    top = persona.bounding_box()["y"]
    page.mouse.move(list_box["x"] + list_box["width"] / 2, list_box["y"] + list_box["height"] / 2)
    page.mouse.wheel(0, 400)
    page.wait_for_function("document.querySelector('aside [data-sidebar-rows]').scrollTop > 100")
    page.wait_for_timeout(300)
    assert abs(persona.bounding_box()["y"] - top) < 1, (top, persona.bounding_box())
    expect(page.locator("aside [data-persona-fog]")).to_have_class(re.compile(r"\bopacity-100\b"))
    expect(persona.locator("[data-persona-chord]")).to_have_text(re.compile(r"^(⌥⌘P|Ctrl\+Alt\+P)$"))
    page.evaluate("document.querySelector('aside [data-sidebar-rows]').scrollTop = 0")
    ok("Persona stays at the top while the list scrolls, the fog under it, its ⌥⌘P / Ctrl+Alt+P on the row")

    # 2. "Overview" and back to Persona.
    page.locator('aside a[href="/overview"]').click()
    page.wait_for_function("location.pathname === '/overview'")
    expect(persona).not_to_have_attribute("aria-current", "page")
    persona.click()
    page.wait_for_function("location.pathname === '/'")
    expect(persona).to_have_attribute("aria-current", "page")
    ok("总览 is /overview; clicking Persona goes back to the main conversation")
    page.locator('aside a[href="/overview"]').click()
    page.wait_for_function("location.pathname === '/overview'")
    page.keyboard.press("ControlOrMeta+Alt+P")
    page.wait_for_function("location.pathname === '/'")
    expect(persona).to_have_attribute("aria-current", "page")
    ok("Mod+Alt+P opens the main conversation, as the click does")

    # 3. The main conversation is not listed twice.
    expect(page.locator(f'aside a[href="/persona/{main_id}"]')).to_have_count(0)
    expect(page.locator(PERSONA)).to_have_count(1)
    expect(page.locator("aside").get_by_text("主对话", exact=True)).to_have_count(0)
    ok("the main conversation has one entry")

    if shots:
        for theme in ("light", "dark"):
            page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
            page.reload()
            expect(persona).to_have_attribute("aria-current", "page")
            page.wait_for_timeout(800)
            page.screenshot(path=os.path.join(shots, f"wide-persona-{theme}.png"))
        page.evaluate("localStorage.setItem('persona.theme', 'light')")
        page.reload()

    # 4. A new one-off conversation is the "+" of "Persona chats", without a main chat button on it.
    page.locator('aside a[aria-label="新对话"]').click()
    page.wait_for_function("location.pathname === '/persona/new'")
    expect(page.get_by_text(STARTER)).to_be_visible()
    expect(page.locator("main").get_by_role("button", name="主对话")).to_have_count(0)
    expect(persona).not_to_have_attribute("aria-current", "page")
    ok('Persona chats + opens a new conversation at /persona/new without a main-chat button')

    # 5. "New agent" is the "+" of Agents and still opens the dialog.
    page.locator('aside button[aria-label="新建 Agent"]').click()
    expect(page.get_by_role("dialog").get_by_text("新建 Agent").first).to_be_visible()
    if shots:
        page.wait_for_timeout(400)
        page.screenshot(path=os.path.join(shots, "wide-new-agent.png"))
    page.keyboard.press("Escape")
    expect(page.get_by_role("dialog")).to_have_count(0)
    ok("Agents + opens 新建 Agent")

    # 6. Canonical links open Persona; retired routes stay unknown and show Overview.
    for path in ('/persona', f'/persona/{main_id}'):
        page.goto(base + path)
        expect(persona).to_have_attribute('aria-current', 'page')
        expect(page.get_by_text(STARTER)).to_have_count(0)
        ok(f'{path} opens the main conversation')
    for retired in ('/j', f'/j/{main_id}', f'/j/{other["session_id"]}'):
        page.goto(base + retired)
        page.wait_for_function('location.pathname === ' + json.dumps(retired))
        expect(page.locator('aside a[href="/overview"]')).to_have_attribute('aria-current', 'page')
        expect(persona).not_to_have_attribute('aria-current', 'page')
        expect(page.locator('main textarea')).to_have_count(0)
        ok(f'{retired} is not a Persona route and does not redirect')
    page.goto(base + '/persona/' + other['session_id'])
    expect(page.locator(f'aside a[href="/persona/{other["session_id"]}"]')).to_have_attribute('aria-current', 'page')
    expect(persona).not_to_have_attribute('aria-current', 'page')
    ok('/persona/<id> opens that conversation with its row selected')
    for old, key in (("/history", "/history"), ("/inbox", "/inbox"), ("/settings", "/settings")):
        page.goto(base + old)
        expect(page.locator(f'aside a[href="{key}"]')).to_have_attribute("aria-current", "page")
    ok("/history, /inbox, /settings still open")
    ctx.close()

    # 7. Narrow: the drawer has the same order, Persona closes it on the main conversation.
    for theme in ("light", "dark"):
        ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, has_touch=True, locale="zh-CN")
        page = ctx.new_page()
        page.set_default_timeout(10000)
        page.goto(base + "/overview?token=" + token)
        # signed in (the link's token traded for this browser's) before the reload, or the reload lands on the login form
        expect(page.locator("aside")).to_be_attached()
        page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
        page.reload()
        page.locator("main").get_by_role("button", name="侧边栏").first.click()
        persona = page.locator(PERSONA)
        expect(persona).to_be_in_viewport()
        expect(page.locator("aside [data-sidebar-rows] a").first).to_have_attribute("title", "Persona 的主对话")
        if shots:
            page.wait_for_timeout(500)
            page.screenshot(path=os.path.join(shots, f"narrow-drawer-{theme}.png"))
        persona.click()
        page.wait_for_function("location.pathname === '/'")
        expect(persona).not_to_be_in_viewport()
        if shots:
            page.wait_for_timeout(800)
            page.screenshot(path=os.path.join(shots, f"narrow-persona-{theme}.png"))
        ctx.close()
    ok("narrow: the drawer starts with Persona, which opens the main conversation and closes the drawer")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:  # one line of verdict on any failure
        print(f"FAIL web sidebar e2e: {e}", file=sys.stderr)
        sys.exit(1)
