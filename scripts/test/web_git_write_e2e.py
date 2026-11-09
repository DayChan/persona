#!/usr/bin/env python3
"""End-to-end check of the Git panel's writes (#6121): a file's checkbox stages it, Commit commits what is staged,
Push asks first (a new branch: it says so and sets the upstream), the branch picker switches or creates (asking first
when there are uncommitted changes), a refusal says what happened and lists its files, stashes push / pop / drop
(drop asks first). The branch's pull request (#6121 step 3): opening one asks first, its checks and open review threads
show, a thread goes to the message box as a comment, merging and merging when ready each ask first; gh missing is said.

Needs the fake hub with MOCK_GIT_WRITE=1, a running web page against it, and Playwright for Python:

    MOCK_GIT_WRITE=1 PORT=18960 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18960 npx vite --port 18961 --strictPort &)
    python3 scripts/test/web_git_write_e2e.py http://127.0.0.1:18961 --hub http://127.0.0.1:18960 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. --shots saves wide / phone × light / dark
screenshots of each state.
"""
import argparse
import json
import os
import urllib.request

from playwright.sync_api import expect, sync_playwright

MID, SID = "macbook", "a1f3c2d4-0000-4000-8000-000000000001"  # a chat whose folder is /Users/me/code/weather


def ok(msg):
    print("ok  ", msg, flush=True)


def hub(base, path):
    with urllib.request.urlopen(base + path) as r:
        return json.load(r)


def fresh(browser, token, viewport, scheme="light"):
    ctx = browser.new_context(viewport=viewport, color_scheme=scheme, device_scale_factor=2, locale="zh-CN")
    ctx.add_init_script(f"localStorage.setItem('persona.token', {token!r}); localStorage.setItem('persona.theme', {scheme!r})")
    page = ctx.new_page()
    page.set_default_timeout(10000)
    return ctx, page


def open_git(page, base, phone=False):
    page.goto(f"{base}/s/{MID}/{SID}")
    if phone:
        page.locator("main header").get_by_role("button", name="更多", exact=True).click()
        page.get_by_role("menuitem", name="文件").or_(page.get_by_role("menuitemcheckbox", name="文件")).click()
    else:
        page.get_by_role("button", name="文件", exact=True).click()
    page.locator("aside[aria-label=文件] header button[title=Git]").click()
    panel = page.locator("aside[aria-label=Git]")
    expect(panel).to_be_visible()
    expect(panel.get_by_text("widget-hourly").first).to_be_visible()
    page.wait_for_timeout(300)
    return panel


def shot(page, shots, name):
    if shots:
        page.wait_for_timeout(250)
        page.screenshot(path=os.path.join(shots, name))


def flow(browser, base, hubu, token, shots):
    hub(hubu, "/mock/git-write?reset=1")
    ctx, page = fresh(browser, token, {"width": 1440, "height": 900})
    panel = open_git(page, base)
    boxes = panel.get_by_role("checkbox")
    expect(boxes).to_have_count(4)
    expect(panel.get_by_role("button", name="提交", exact=True)).to_be_disabled()
    shot(page, shots, "panel-wide-light.png")
    ok("the panel shows a checkbox per change, Commit off until something is staged and described")

    panel.get_by_role("checkbox", name="暂存 WeatherWidget/Provider.swift").click()
    expect(panel.get_by_role("checkbox", name="取消暂存 WeatherWidget/Provider.swift")).to_have_attribute("aria-checked", "true")
    panel.get_by_role("textbox", name="提交说明").fill("小组件：逐小时预报的降雨条")
    expect(panel.get_by_role("button", name="提交", exact=True)).to_be_enabled()
    shot(page, shots, "commit-ready-wide-light.png")
    panel.get_by_role("button", name="提交", exact=True).click()
    expect(page.get_by_text("已提交 eeeeeee")).to_be_visible()
    expect(panel.get_by_role("textbox", name="提交说明")).to_have_value("")
    ok("a checked file is staged, Commit commits it and clears the message")

    panel.get_by_role("button", name="推送", exact=True).click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("远端还没有 widget-hourly")
    expect(dialog).to_contain_text("不会强制推送")
    shot(page, shots, "push-confirm-wide-light.png")
    dialog.get_by_role("button", name="推送", exact=True).click()
    expect(page.get_by_text("已推送 widget-hourly")).to_be_visible()
    expect(panel.get_by_text("origin/widget-hourly")).to_be_visible()
    expect(panel.get_by_role("button", name="推送", exact=True)).to_have_count(0)
    ok("Push asks first, a new branch's push sets its upstream, then Push goes away")

    panel.get_by_title("分支：切换或新建").click()
    view = page.locator("aside[aria-label=Git]")
    expect(view.get_by_text("origin/release-2.4")).to_be_visible()
    shot(page, shots, "branches-wide-light.png")
    view.get_by_role("searchbox").or_(view.get_by_role("textbox")).first.fill("fix/rain-icon")
    expect(view.get_by_role("button", name="从这里新建分支 fix/rain-icon")).to_be_visible()
    shot(page, shots, "branch-create-wide-light.png")
    view.get_by_role("searchbox").or_(view.get_by_role("textbox")).first.fill("")
    ok("the branch picker lists local and remote branches and offers to create a typed name")

    hub(hubu, "/mock/git-write?fail=git_conflict&files=WeatherWidget/Provider.swift,Sources/App/main.swift")
    view.get_by_role("button", name="main", exact=False).first.click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("未提交的改动会一起带过去")
    shot(page, shots, "switch-confirm-wide-light.png")
    dialog.get_by_role("button", name="切换", exact=True).click()
    refusal = page.get_by_role("dialog")
    expect(refusal).to_contain_text("会覆盖未提交的改动")
    expect(refusal).to_contain_text("Sources/App/main.swift")
    shot(page, shots, "refusal-wide-light.png")
    refusal.get_by_role("button", name="知道了").click()
    ok("switching with uncommitted changes asks first; a refused switch lists the files and git's words")

    page.locator("aside[aria-label=Git] header").get_by_role("button", name="返回").click()
    panel.get_by_role("button", name="· 贮藏 1").click()
    view = page.locator("aside[aria-label=Git]")
    expect(view.get_by_text("alternate palette", exact=False)).to_be_visible()
    view.get_by_role("textbox", name="这些改动是什么（可不填）").fill("降雨条的另一种画法")
    shot(page, shots, "stashes-wide-light.png")
    view.get_by_role("button", name="收起改动").click()
    expect(page.get_by_text("已收起改动")).to_be_visible()
    expect(view.get_by_text("降雨条的另一种画法")).to_be_visible()
    view.get_by_role("button", name="丢弃").nth(1).click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("会被删除")
    shot(page, shots, "drop-confirm-wide-light.png")
    dialog.get_by_role("button", name="丢弃", exact=True).click()
    expect(view.get_by_text("alternate palette", exact=False)).to_have_count(0)
    ok("stash puts the changes away with a message; dropping one asks first")
    ctx.close()


def pr_flow(browser, base, hubu, token, shots):
    hub(hubu, "/mock/git-write?reset=1")
    ctx, page = fresh(browser, token, {"width": 1440, "height": 900})
    panel = open_git(page, base)
    expect(panel.get_by_text("先推送分支，再创建拉取请求")).to_be_visible()
    expect(panel.get_by_role("button", name="创建拉取请求")).to_be_disabled()
    panel.get_by_role("button", name="推送", exact=True).click()
    page.get_by_role("dialog").get_by_role("button", name="推送", exact=True).click()
    expect(panel.get_by_text("这个分支还没有拉取请求")).to_be_visible()
    ok("a branch with unpushed commits cannot open a pull request; once pushed it can")

    panel.get_by_role("button", name="创建拉取请求").click()
    view = page.locator("aside[aria-label=Git]")
    expect(view.get_by_role("textbox", name="标题")).not_to_have_value("")
    view.get_by_role("textbox", name="标题").fill("小组件：逐小时预报")
    view.get_by_role("textbox", name="描述（可不填）").fill("逐小时的降雨条和温度曲线。")
    shot(page, shots, "pr-create-wide-light.png")
    view.get_by_role("button", name="创建拉取请求").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("能看到这个仓库的人都看得到")
    shot(page, shots, "pr-create-confirm-wide-light.png")
    dialog.get_by_role("button", name="创建拉取请求").click()
    expect(page.get_by_text("已创建拉取请求")).to_be_visible()
    expect(view.locator("header").get_by_text("#128")).to_be_visible()
    expect(view.get_by_text("build (macos-15)")).to_be_visible()
    ok("opening a pull request asks first, then shows it with its checks")

    hub(hubu, "/mock/git-write?pr=review")
    view.get_by_role("button", name="重新读取拉取请求").click()
    expect(view.get_by_text("未解决的评审意见 · 2")).to_be_visible()
    expect(view.get_by_text("unit tests")).to_be_visible()
    shot(page, shots, "pr-review-wide-light.png")
    view.get_by_role("button", name="交给 Agent", exact=True).first.click()
    expect(page.get_by_text("已放进消息").first).to_be_visible()
    composer = page.locator("main")
    expect(composer.get_by_text("拉取请求 #128 在 WeatherWidget/Provider.swift:42 的评审意见").first).to_be_visible()
    shot(page, shots, "pr-handed-wide-light.png")
    ok("a review thread goes to the message box as a comment that names the pull request and the line")

    view.get_by_role("button", name="通过后自动合并").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("不再询问")
    expect(dialog).to_contain_text("分支会保留")
    shot(page, shots, "pr-auto-confirm-wide-light.png")
    dialog.get_by_role("button", name="通过后自动合并").click()
    expect(view.get_by_role("button", name="取消自动合并")).to_be_visible()
    ok("merge when ready asks first, then can be taken back")

    hub(hubu, "/mock/git-write?pr=ready")
    view.get_by_role("button", name="重新读取拉取请求").click()
    view.get_by_role("button", name="合并", exact=True).click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("合进 main")
    shot(page, shots, "pr-merge-confirm-wide-light.png")
    dialog.get_by_role("button", name="合并", exact=True).click()
    expect(page.get_by_text("已合并 #128")).to_be_visible()
    expect(view.get_by_text("已合并", exact=True).locator("visible=true").first).to_be_visible()
    ok("merge asks first and the page then says it is merged")

    page.locator("aside[aria-label=Git] header").get_by_role("button", name="返回").click()
    hub(hubu, "/mock/git-write?pr_fail=no_gh")
    panel.locator("header").get_by_role("button", name="刷新").click()
    expect(panel.get_by_text("这台电脑没装 gh")).to_be_visible()
    hub(hubu, "/mock/git-write?pr_fail=")
    ok("no gh on the computer is said in the card")
    ctx.close()


def pr_shots(browser, base, hubu, token, shots):
    for scheme in ("dark", "light"):
        for name, viewport, phone in (("wide", {"width": 1440, "height": 900}, False), ("phone", {"width": 390, "height": 844}, True)):
            if scheme == "light" and name == "wide":
                continue
            hub(hubu, "/mock/git-write?reset=1&pr=review")
            ctx, page = fresh(browser, token, viewport, scheme)
            panel = open_git(page, base, phone)
            expect(panel.get_by_text("#128")).to_be_visible()
            shot(page, shots, f"pr-card-{name}-{scheme}.png")
            panel.get_by_text("#128").click()
            expect(page.locator("aside[aria-label=Git]").get_by_text("未解决的评审意见 · 2")).to_be_visible()
            shot(page, shots, f"pr-review-{name}-{scheme}.png")
            ctx.close()
    ok("pull request: dark and phone-width screenshots")


def other_shots(browser, base, hubu, token, shots):
    for scheme in ("dark", "light"):
        for name, viewport, phone in (("wide", {"width": 1440, "height": 900}, False), ("phone", {"width": 390, "height": 844}, True)):
            if scheme == "light" and name == "wide":
                continue
            hub(hubu, "/mock/git-write?reset=1")
            ctx, page = fresh(browser, token, viewport, scheme)
            panel = open_git(page, base, phone)
            shot(page, shots, f"panel-{name}-{scheme}.png")
            panel.get_by_role("button", name="推送", exact=True).click()
            expect(page.get_by_role("dialog")).to_be_visible()
            shot(page, shots, f"push-confirm-{name}-{scheme}.png")
            ctx.close()
    ok("dark and phone-width screenshots")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=os.environ.get("PERSONA_E2E_CHROMIUM") or None)
        try:
            flow(browser, a.base, a.hub, a.token, a.shots)
            pr_flow(browser, a.base, a.hub, a.token, a.shots)
            if a.shots:
                other_shots(browser, a.base, a.hub, a.token, a.shots)
                pr_shots(browser, a.base, a.hub, a.token, a.shots)
        finally:
            browser.close()
    print("PASS")


if __name__ == "__main__":
    main()
