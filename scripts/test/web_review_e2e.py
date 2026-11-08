#!/usr/bin/env python3
"""End-to-end check of the web's Review panel (docs/web/review.md, #151): ⋯ › Review changes opens every file the
session changed against where the branch left origin/main; the Git panel's Review opens the uncommitted ones; a
comment on a line and one on a range wait in the review as drafts at their lines (deleting one, a reload keeping
them); a comment straight to the message box; lines hidden between hunks read from the file on disk; a file too big
for the page is asked for on its own; a key it does not show; the summary and Send review send one message with every
comment's block through the chat's message box; Add to message box puts them above it to edit first; and the states:
nothing changed, not a repository, an old node, no default branch, a commit picked.

Needs a running web page against the fake hub with MOCK_FILES=1 MOCK_REVIEW=1 (scripts/mock/mock_review.py: its GET
/mock/review?mode= switches the state and lists the requests) and Playwright for Python:

    PORT=18873 MOCK_FILES=1 MOCK_REVIEW=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18873 npx vite --port 5193 --strictPort &)
    python3 scripts/test/web_review_e2e.py http://127.0.0.1:5193 --hub http://127.0.0.1:18873 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs light and dark at desktop width, and light
at phone width; --shots saves each state there. --channel msedge, or --executable <path>, where there is no Chrome.
"""
import argparse
import json
import os
import sys
import urllib.request

from playwright.sync_api import TimeoutError as PlaywrightTimeout, expect, sync_playwright

SIZES = {"wide": {"width": 1440, "height": 900}, "narrow": {"width": 390, "height": 844}}
MID, SID = "macbook", "a1f3c2d4-0000-4000-8000-000000000001"  # a chat whose folder is /Users/me/code/weather


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def mock(hub, token, query=""):
    req = urllib.request.Request(f"{hub}/mock/review{query}", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def run(browser, base, hub, token, size, theme, shots, full):
    tag = f"{size}/{theme}"
    narrow = size == "narrow"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=narrow, locale="zh-CN")
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    page.evaluate("Object.keys(localStorage).filter(k => k.startsWith('persona.review')).forEach(k => localStorage.removeItem(k))")
    panel = page.locator("[data-review-panel]")

    def shot(name):
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=os.path.join(shots, f"review-{size}-{theme}-{name}.png"))

    def diff(path):
        return panel.locator(f'[data-diff-path="{path}"]')

    def review_from_menu():
        # ⋯ › Review changes: the item is there once the hub's hello said git_review, a moment after the page loads
        for _ in range(20):
            page.get_by_role("button", name="更多").first.click()
            item = page.get_by_role("menuitem", name="评审改动")
            try:
                item.wait_for(timeout=1000)
            except PlaywrightTimeout:
                page.keyboard.press("Escape")
                continue
            item.click()
            return
        fail(f"{tag}: the ⋯ menu has no Review changes")

    def open_panel(mode="1"):
        mock(hub, token, "?mode=" + mode + "&reset=1")
        page.evaluate("sessionStorage.clear()")
        page.goto(f"{base}/s/{MID}/{SID}")
        review_from_menu()
        expect(panel).to_be_visible()

    def pick_line(path, n, side="new", to=None):
        num = diff(path).locator(f'[data-ln="{side}"][data-n="{n}"]').first
        num.scroll_into_view_if_needed()
        if to is None:
            num.click()
        else:
            num.hover()
            page.mouse.down()
            diff(path).locator(f'[data-ln="{side}"][data-n="{to}"]').first.hover()
            page.mouse.up()

    # the branch: every kind of file
    open_panel()
    page.evaluate("localStorage.setItem('persona.review.target:%s/%s', JSON.stringify({scope:'branch'}))" % (MID, SID))
    panel.get_by_role("tab", name="分支").click()
    expect(diff("Sources/App/Forecast.swift")).to_be_visible()
    expect(panel).to_contain_text("origin/main")
    asked = mock(hub, token)["asked"]
    if not any(a["scope"] == "branch" for a in asked):
        fail(f"{tag}: the branch was not asked: {asked}")
    for path in ("README.md", "Sources/App/main.swift", "WeatherWidget/WidgetView.swift", "docs/hourly.md"):
        expect(diff(path)).to_be_visible()
    expect(diff("deploy/id_ed25519")).to_contain_text("不显示：这个文件含有密钥")
    expect(diff("docs/icon.png")).to_be_visible()
    expect(diff("Sources/App/Units.swift")).to_contain_text("Sources/App/Helpers.swift")
    ok(f"{tag}: the branch's files, a key not shown, a picture, a rename")
    shot("branch")

    if full:
        # a big file waits for Load diff, then is asked for on its own against the same base
        big = diff("Resources/stations.json")
        big.scroll_into_view_if_needed()
        page.wait_for_timeout(800)
        if any(a["paths"] for a in mock(hub, token)["asked"]):
            fail(f"{tag}: the big file loaded by itself: {mock(hub, token)['asked']}")
        shot("big")
        big.get_by_role("button", name="加载改动").click()
        expect(big).to_contain_text("ST-000")
        alone = next(a for a in mock(hub, token)["asked"] if a["paths"])
        if alone["paths"] != ["Resources/stations.json"] or alone["scope"] != "commit" or not alone["base"]:
            fail(f"{tag}: the big file was asked as {alone}")
        ok(f"{tag}: a big file waits for Load diff, then comes on its own against the same base")

        # hidden lines between hunks, read from the file on disk
        fc = diff("Sources/App/Forecast.swift")
        fc.scroll_into_view_if_needed()
        gap = fc.locator("div.h-8").filter(has_text="隐藏了 48 行")
        gap.get_by_role("button", name="展开全部").click()
        expect(fc).to_contain_text("Parsing")
        expect(fc).not_to_contain_text("隐藏了 48 行")
        ok(f"{tag}: lines between hunks come from the file on disk")

        # a comment on a line: a draft at its line
        pick_line("Sources/App/Forecast.swift", 11)
        box = fc.locator("textarea").first
        expect(box).to_be_visible()
        box.fill("这个字段改名后，小组件里读 rain 的地方也要改")
        shot("comment-box")
        fc.get_by_role("button", name="开始评审").click()
        expect(fc.locator("[data-thread]")).to_have_count(1)
        # a range, on the new upcoming()
        pick_line("Sources/App/Forecast.swift", 62, to=65)
        box = fc.locator("textarea").first
        box.fill("count 默认 6 和小组件的 6 小时对上了，但现在的小时要不要算进去？")
        fc.get_by_role("button", name="加入评审").click()
        expect(fc.locator("[data-thread]")).to_have_count(2)
        expect(panel).to_contain_text("评审草稿 · 2 条")
        ok(f"{tag}: comments on a line and a range wait as drafts at their lines")
        shot("drafts")

        # a reload keeps them
        page.reload()
        review_from_menu()
        expect(diff("Sources/App/Forecast.swift").locator("[data-thread]")).to_have_count(2)
        ok(f"{tag}: the review's comments are kept in this browser")

        # one more, then deleted
        pick_line("README.md", 3)
        diff("README.md").locator("textarea").first.fill("删掉我")
        diff("README.md").get_by_role("button", name="加入评审").click()
        expect(diff("README.md").locator("[data-thread]")).to_have_count(1)
        diff("README.md").get_by_role("button", name="删除").click()
        expect(diff("README.md").locator("[data-thread]")).to_have_count(0)
        ok(f"{tag}: a draft is deleted")

        # one straight to the message box
        pick_line("Sources/App/main.swift", 3)
        diff("Sources/App/main.swift").locator("textarea").first.fill("城市名为空时给个默认值")
        diff("Sources/App/main.swift").get_by_role("button", name="直接评论").click()
        expect(page.locator("[data-comment-card]")).to_have_count(1)
        ok(f"{tag}: a comment straight to the message box")

        # the summary and Send review: one message with every comment's block
        panel.get_by_role("button", name="完成评审").click()
        panel.locator("textarea").last.fill("整体可以，改完这两处再提交。")
        shot("finish")
        panel.get_by_role("button", name="发送评审").click()
        expect(page.get_by_text("已发送评审，共 2 条评论")).to_be_visible()
        bubble = page.locator("[data-user-bubble]").filter(has_text="整体可以，改完这两处再提交。").last
        expect(bubble).to_be_visible()
        # its comments go with it as cards naming the file and lines
        expect(bubble).to_contain_text("Sources/App/Forecast.swift:11")
        expect(bubble).to_contain_text("Sources/App/Forecast.swift:62-65")
        expect(diff("Sources/App/Forecast.swift").locator("[data-thread]")).to_have_count(0)
        ok(f"{tag}: Send review sends the summary and both comments as one message")
        shot("sent")
        # the comment card sent alone stayed in the box
        expect(page.locator("[data-comment-card]")).to_have_count(1)

        # Add to message box: the comments above the box, to edit first
        pick_line("WeatherWidget/Provider.swift", 9)
        diff("WeatherWidget/Provider.swift").locator("textarea").first.fill("prefix(6) 和 Forecast.upcoming 重复了")
        diff("WeatherWidget/Provider.swift").get_by_role("button", name="开始评审").click()
        panel.get_by_role("button", name="完成评审").click()
        panel.get_by_role("button", name="放进输入框").click()
        expect(page.locator("[data-comment-card]")).to_have_count(2)
        ok(f"{tag}: Add to message box puts the review's comments above it")
        shot("composed")

    # the Git panel's Review opens the uncommitted changes
    page.goto(f"{base}/s/{MID}/{SID}")
    page.get_by_role("button", name="更多").first.click()
    page.get_by_role("menuitem", name="Git").click()
    page.locator("[data-git-review]").click()
    expect(panel).to_be_visible()
    expect(panel.get_by_role("tab", name="未提交")).to_have_attribute("aria-selected", "true")
    expect(diff("WeatherWidget/Provider.swift")).to_be_visible()
    expect(diff("README.md")).to_have_count(0)
    ok(f"{tag}: the Git panel's Review opens the uncommitted changes")
    shot("uncommitted")

    # since a commit
    panel.get_by_role("tab", name="自某个提交").click()
    page.get_by_role("menuitemradio").nth(1).click()
    expect(panel.locator("[data-review-commit]")).to_contain_text("a1b2c3d1")
    ok(f"{tag}: since a commit picked from the recent ones")
    shot("since")

    for mode, text, name in (("clean", "没有未提交的改动", "clean"), ("nogit", "不是 Git 仓库", "nogit"), ("outdated", "更新这台电脑后才能评审改动", "outdated")):
        page.evaluate("localStorage.setItem('persona.review.target:%s/%s', JSON.stringify({scope:'uncommitted'}))" % (MID, SID))
        open_panel(mode)
        expect(panel).to_contain_text(text)
        ok(f"{tag}: {name}")
        shot(name)
    page.evaluate("localStorage.setItem('persona.review.target:%s/%s', JSON.stringify({scope:'branch'}))" % (MID, SID))
    open_panel("nodefault")
    expect(panel).to_contain_text("没有可以对比的默认分支")
    ok(f"{tag}: no default branch")
    shot("nodefault")
    mock(hub, token, "?mode=1&reset=1")
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
    print("PASS web review", flush=True)


if __name__ == "__main__":
    main()
