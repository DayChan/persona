#!/usr/bin/env python3
"""End-to-end check of the web's dev server preview panel (docs/web/preview.md, #150): the session's ⋯ › Preview opens the
running server's page; the address bar goes to another page; Pick element outlines, a click opens the comment box and
the comment lands above the message box with the page and the element; a screenshot lands in the message box; a link
elsewhere is offered in a new tab; Phone size; the servers menu; a server that is not running is started after its
command is confirmed (the node is asked with the session's workspace) and shown once it answers; and the states with
nothing to show: no launch.json, no browser on the computer, a node from before previews.

Needs a running web page against the fake hub with MOCK_PREVIEW=1 (scripts/mock/mock_preview.py: its GET
/mock/preview?mode= switches the state, and lists what the panel sent) and Playwright for Python:

    PORT=18871 MOCK_PREVIEW=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18871 npx vite --port 5871 --strictPort &)
    python3 scripts/test/web_preview_e2e.py http://127.0.0.1:5871 --hub http://127.0.0.1:18871 [--shots <dir>]

The pictures of the made-up page are drawn by the browser this computer has (PERSONA_CHROME, else Playwright's). Prints
one line per check and a verdict; exits 1 on the first failure. Runs light and dark at desktop width, and light at phone
width; --shots saves each state there. --channel msedge, or --executable <path>, where there is no Chrome.
"""
import argparse
import json
import os
import sys
import urllib.request

from playwright.sync_api import TimeoutError as PlaywrightTimeout, expect, sync_playwright

SIZES = {"wide": {"width": 1440, "height": 900}, "narrow": {"width": 390, "height": 844}}
MID = "macbook"
SID = "a1f3c2d4-0000-4000-8000-000000000001"


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def mock(hub, token, query=""):
    req = urllib.request.Request(f"{hub}/mock/preview{query}", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def run(browser, base, hub, token, size, theme, shots, full):
    tag = f"{size}/{theme}"
    narrow = size == "narrow"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=narrow, locale="zh-CN")
    ctx.grant_permissions(["clipboard-read", "clipboard-write"])
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    panel = page.locator("[data-preview-panel]")
    canvas = page.locator("[data-preview-canvas]")

    def shot(name):
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=os.path.join(shots, f"preview-{size}-{theme}-{name}.png"))

    def open_panel(mode):
        mock(hub, token, "?mode=" + mode)
        page.evaluate(f"sessionStorage.clear()")
        page.goto(f"{base}/s/{MID}/{SID}")
        # ⋯ › Preview: the item is there once the hub's hello said preview, a moment after the page loads
        for _ in range(20):
            page.get_by_role("button", name="更多").first.click()
            item = page.get_by_role("menuitem", name="预览")
            try:
                item.wait_for(timeout=1000)
            except PlaywrightTimeout:
                page.keyboard.press("Escape")
                continue
            item.click()
            break
        else:
            fail(f"{tag}: the ⋯ menu has no Preview")
        expect(panel).to_be_visible()

    def drawn():
        page.wait_for_function("(() => { const c = document.querySelector('[data-preview-canvas]'); return c && c.width > 1 && getComputedStyle(c).opacity !== '0'; })()", timeout=40000)

    # the running server opens at once
    open_panel("1")
    drawn()
    expect(panel).to_contain_text("web")
    expect(panel).to_contain_text("localhost:5173")
    expect(page.locator("[data-preview-address]")).to_have_value("/")
    ok(f"{tag}: the running server's page is shown")
    shot("live")

    # the address bar
    addr = page.locator("[data-preview-address]")
    addr.click()
    addr.fill("/settings")
    addr.press("Enter")
    expect(addr).to_have_value("/settings")
    page.wait_for_timeout(1500)
    if not any(m.get("t") == "nav" and m.get("url") == "/settings" for m in mock(hub, token)["log"]):
        fail(f"{tag}: the address went to the node as {mock(hub, token)['log'][-3:]}")
    ok(f"{tag}: the address bar navigates")
    shot("settings")

    if not narrow or full:
        # picking an element and commenting on it
        panel.get_by_role("button", name="选一个元素来评论").click()
        expect(page.locator("[data-preview-picking]")).to_be_visible()
        page.wait_for_timeout(1500)
        shot("picking")
        box = canvas.bounding_box()
        page.mouse.click(box["x"] + box["width"] * 0.8, box["y"] + box["height"] * 0.5)
        line = page.locator("[data-line-box]")
        expect(line).to_be_visible()
        expect(line).to_contain_text("button.save")
        line.locator("textarea").fill("把按钮改成主色，和别处一致")
        shot("comment-box")
        line.get_by_role("button", name="添加").click()
        card = page.locator("[data-comment-card]").first
        expect(card).to_contain_text("button.save")
        expect(card).to_contain_text("/settings")
        ok(f"{tag}: a picked element becomes a comment above the message box")
        shot("commented")

        # a screenshot into the message box
        panel.get_by_role("button", name="截图").click()
        page.get_by_role("menuitem", name="可见区域").click()
        expect(page.get_by_text("截图已加到消息里")).to_be_visible()
        ok(f"{tag}: a screenshot lands in the message box")
        shot("screenshot")

        # a link elsewhere
        addr.click()
        addr.fill("https://docs.example.com/notes/api")
        addr.press("Enter")
        bar = page.locator("[data-preview-bar=external]")
        expect(bar).to_be_visible()
        expect(bar.get_by_role("link", name="在新标签页打开")).to_have_attribute("href", "https://docs.example.com/notes/api")
        ok(f"{tag}: an address elsewhere is offered in a new tab, not opened in the preview")
        shot("external")
        bar.get_by_role("button", name="关闭").click()

        # phone size
        panel.get_by_role("button", name="桌面尺寸（切换到手机）").click()
        page.wait_for_timeout(2500)
        if not any(m.get("t") == "size" and m.get("mobile") for m in mock(hub, token)["log"]):
            fail(f"{tag}: phone size was not asked")
        ok(f"{tag}: phone size")
        shot("phone")

        # the servers menu
        panel.get_by_role("button", name="服务器").click()
        menu = page.get_by_role("menu")
        expect(menu).to_contain_text("api")
        expect(menu).to_contain_text("Storybook")
        ok(f"{tag}: the servers menu lists launch.json's entries and the agent's port")
        shot("servers")
        page.keyboard.press("Escape")

    # nothing running: the list, Start after confirming its command, then the page
    open_panel("stopped")
    row = page.locator("[data-preview-server=web]")
    expect(row).to_be_visible()
    expect(row).to_contain_text("npm run dev")
    expect(page.locator("[data-preview-problems]")).to_contain_text("staging")
    ok(f"{tag}: servers not running are listed with their commands, the broken entry says why")
    shot("stopped")
    row.get_by_role("button", name="启动…").click()
    dialog = page.get_by_role("dialog")
    expect(dialog.locator("[data-preview-command]")).to_have_text("npm run dev")
    shot("confirm")
    dialog.get_by_role("button", name="启动").click()
    expect(panel).to_contain_text("正在启动 web")
    shot("starting")
    drawn()
    start = [m["start"] for m in mock(hub, token)["log"] if "start" in m]
    if not start or start[-1].get("workspace") != "w1" or start[-1].get("name") != "web":
        fail(f"{tag}: start asked {start}")
    ok(f"{tag}: Start runs the confirmed entry in the session's workspace, and the page shows once it answers")

    for mode, text, name in (("none", "还没有开发服务器", "empty"), ("old", "更新这台电脑后才能预览", "old")):
        open_panel(mode)
        expect(panel).to_contain_text(text)
        ok(f"{tag}: {name}")
        shot(name)
    open_panel("nobrowser")
    expect(panel).to_contain_text("MacBook 上没有能用来预览的浏览器")
    ok(f"{tag}: no browser on the computer")
    shot("nobrowser")
    mock(hub, token, "?mode=1")
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
        for size, theme in (("wide", "light"), ("wide", "dark"), ("narrow", "light")):
            run(browser, a.base, a.hub, a.token, size, theme, a.shots, full=size == "wide")
        browser.close()
    print("PASS web preview", flush=True)


if __name__ == "__main__":
    main()
