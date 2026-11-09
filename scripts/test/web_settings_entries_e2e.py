#!/usr/bin/env python3
"""Check Settings categories and entries against MOCK_CHANNELS=1 MOCK_EXTENSIONS=1.

Run with a dedicated fake hub and the web dev server pointing to it:
    python3 scripts/test/web_settings_entries_e2e.py <web address> [--shots <directory>]

Checks both layouts, old links, cross-links and Back; screenshots contain only mock data.
"""
import argparse
import json
import os

from playwright.sync_api import expect, sync_playwright


def check(browser, base, width, theme, shots):
    context = browser.new_context(viewport={"width": width, "height": 900}, color_scheme=theme, locale="zh-CN")
    context.add_init_script("localStorage.setItem('persona.theme', " + repr(theme) + "); localStorage.setItem('persona.language', 'zh-Hans')")
    page = context.new_page()
    page.set_default_timeout(15000)
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    try:
        page.goto(base + "/settings?token=mock")
        main = page.locator("main")
        nav = main.get_by_role("navigation", name="设置分类")
        expect(nav.get_by_role("button").filter(has=page.get_by_text("渠道", exact=True))).to_be_visible()
        labels = nav.get_by_role("button").all_text_contents()
        labels = [label.strip() for label in labels]
        expected = ["通用", "Persona", "通知", "快捷键", "模型", "Agent"] if width < 640 else ["通用", "Persona", "通知", "模型", "Agent"]
        assert [label for label in labels if label in expected] == expected, labels
        for label in ("模型供应商", "模型配置", "Claude Code", "Codex", "Antigravity", "omp", "翻译", "助手"):
            assert label not in labels, f"old category remains: {label}"
        assert next(i for i, label in enumerate(labels) if label.startswith("渠道")) + 1 == next(i for i, label in enumerate(labels) if label.startswith("连接器")), labels
        expect(main.get_by_text("认识 Persona", exact=True)).to_have_count(0)
        assert main.evaluate("e => e.scrollWidth <= e.clientWidth"), "settings page overflows horizontally"
        if width >= 640:
            assert nav.evaluate("e => e.scrollWidth <= e.clientWidth"), "settings categories overflow"
            region = main.get_by_role("region", name="通用", exact=True)
            assert nav.evaluate("e => e.getBoundingClientRect().right") <= region.evaluate("e => e.getBoundingClientRect().left"), "categories are not left of content"
            assert main.locator("header nav").count() == 0, "categories remain in the header"
            expect(nav.locator('[aria-current="page"]')).to_have_count(1)
            expect(nav.get_by_role("button", name="通用", exact=True)).to_have_attribute("aria-current", "page")
            expect(region).to_have_attribute("tabindex", "0")
            nav.get_by_role("button", name="通用", exact=True).focus()
            page.keyboard.press("Tab")
            expect(nav.get_by_role("button", name="通知", exact=True)).to_be_focused()
            page.keyboard.press("Enter")
            expect(page).to_have_url(base + "/settings?tab=notifications")
            expect(main.get_by_role("region", name="通知", exact=True)).to_be_visible()
            page.reload()
            expect(nav.get_by_role("button", name="通知", exact=True)).to_have_attribute("aria-current", "page")
            expect(main.get_by_role("region", name="通知", exact=True)).to_be_visible()
            nav.get_by_role("button", name="通用", exact=True).click()
            expect(page).to_have_url(base + "/settings")

            # Short windows make both sides scroll; neither wheel changes the other region's position.
            page.set_viewport_size({"width": width, "height": 520})
            assert nav.evaluate("e => e.scrollHeight > e.clientHeight"), "sidebar is not scrollable"
            expect(region).to_be_visible()
            assert region.evaluate("e => e.scrollHeight > e.clientHeight"), "content is not scrollable"
            region.hover()
            page.mouse.wheel(0, 400)
            # Wait on the actual scroll position, not a fixed delay.
            page.wait_for_function("id => document.getElementById(id).scrollTop > 0", arg=region.get_attribute("id"))
            assert nav.evaluate("e => e.scrollTop") == 0, "content scrolling moved the sidebar"
            content_top = region.evaluate("e => e.scrollTop")
            nav.hover()
            page.mouse.wheel(0, 500)
            page.wait_for_function("() => document.querySelector('main nav').scrollTop > 0")
            assert region.evaluate("e => e.scrollTop") == content_top, "sidebar scrolling moved the content"
            expect(nav.get_by_role("button").last).to_be_in_viewport()
            page.set_viewport_size({"width": width, "height": 900})
            nav.evaluate("e => e.scrollTop = 0")
            region.evaluate("e => e.scrollTop = 0")
        if shots:
            page.wait_for_timeout(400)
            main.screenshot(path=os.path.join(shots, f"settings-{width}-{theme}.png"))

        for old, new, section in (
            ("?tab=models", "?tab=models", "模型供应商"),
            ("?tab=model-config&purpose=agent.triage", "?tab=models&purpose=agent.triage#settings-model-config", "模型配置"),
            ("?tab=claude", "?tab=agents#settings-claude", "Claude Code"),
            ("?tab=codex", "?tab=agents#settings-codex", "Codex"),
            ("?tab=agy", "?tab=agents#settings-agy", "Antigravity"),
            ("?tab=omp", "?tab=agents#settings-omp", "omp"),
            ("?tab=translate", "?tab=general#settings-translation", "翻译"),
            ("?tab=assistant", "?tab=persona", "形象"),
        ):
            page.goto(base + "/settings" + old)
            expect(page).to_have_url(base + "/settings" + new)
            expect(main.get_by_role("heading", name=section, exact=True).first).to_be_visible()
            if new.startswith("?tab=agents"):
                for agent in ("Claude Code", "Codex", "Antigravity", "omp"):
                    expect(main.get_by_role("heading", name=agent, exact=True)).to_be_visible()
            if "#settings-" in new:
                expect(page.locator("#" + new.split("#", 1)[1])).to_be_in_viewport()
            page.reload()
            expect(main.get_by_role("heading", name=section, exact=True).first).to_be_visible()
            if width < 640:
                cancel = page.get_by_role("button", name="取消", exact=True)
                if cancel.count():
                    cancel.click()
                main.get_by_role("button", name="返回设置", exact=True).click()
                expect(page).to_have_url(base + "/settings")
                expect(nav.get_by_role("button").filter(has=page.get_by_text("渠道", exact=True))).to_be_visible()
        page.goto(base + "/settings?tab=models")
        expect(main.get_by_role("heading", name="模型供应商", exact=True)).to_be_visible()
        expect(main.get_by_role("heading", name="模型配置", exact=True)).to_be_visible()
        if shots:
            main.screenshot(path=os.path.join(shots, f"models-{width}-{theme}.png"))
        page.goto(base + "/settings?tab=general")

        nav.get_by_role("button").filter(has=page.get_by_text("渠道", exact=True)).click()
        expect(page).to_have_url(base + "/settings?tab=channels")
        expect(main.get_by_role("heading", name="渠道", exact=True).last).to_be_visible()
        expect(main.get_by_role("button", name="扩展", exact=True)).to_have_count(0 if width < 640 else 1)
        expect(main.get_by_text("Example Rooms", exact=True)).to_be_visible()
        if shots:
            page.wait_for_timeout(400)
            main.screenshot(path=os.path.join(shots, f"channels-{width}-{theme}.png"))

        main.get_by_role("button").filter(has=page.get_by_text("Example Rooms", exact=True)).click()
        expect(page).to_have_url(base + "/settings?tab=extensions&extension=-market%2Fexample%2Fexample-rooms")
        page.goto(base + "/settings?tab=channels")
        main.get_by_role("button").filter(has=page.get_by_text("微信", exact=True)).click()
        expect(page).to_have_url(base + "/settings?tab=channels&channel=wechat")
        expect(main.get_by_role("heading", name="微信", exact=True)).to_be_visible()
        main.get_by_role("button", name="渠道", exact=True).last.click()
        expect(page).to_have_url(base + "/settings?tab=channels")
        if width < 640:
            main.get_by_role("button", name="返回设置", exact=True).click()
            expect(page).to_have_url(base + "/settings")
            expect(nav.get_by_role("button").filter(has=page.get_by_text("渠道", exact=True))).to_be_visible()

        page.goto(base + "/settings?tab=extensions")
        expect(main.get_by_text("扩展给 Persona 加上新的连接器", exact=False)).to_be_visible()
        expect(main.get_by_role("heading", name="渠道", exact=True)).to_have_count(0)
        expect(main.get_by_role("button", name="渠道", exact=True)).to_have_count(0 if width < 640 else 1)
        expect(main.get_by_text("和 Persona 对话的入口：", exact=False)).to_have_count(0)

        for old in ("?tab=plugins&plugin=-channels%2Fwechat", "?tab=extensions&extension=-channels%2Fwechat"):
            page.goto(base + "/settings" + old)
            expect(main.get_by_role("heading", name="微信", exact=True)).to_be_visible()
            main.get_by_role("button", name="渠道", exact=True).last.click()
            expect(page).to_have_url(base + "/settings?tab=channels")
        page.goto(base + "/settings?tab=extensions&extension=-channels")
        expect(main.get_by_role("heading", name="渠道", exact=True).last).to_be_visible()
        main.get_by_role("button").filter(has=page.get_by_text("飞书机器人", exact=True)).click()
        expect(main.get_by_role("heading", name="飞书机器人", exact=True)).to_be_visible()
        main.get_by_role("button", name="更多设置", exact=True).click()
        expect(page).to_have_url(base + "/settings?tab=connectors&connector=feishu-bot")
        expect(page.get_by_role("dialog")).to_be_visible()
        page.keyboard.press("Escape")
        expect(page.get_by_role("dialog")).to_have_count(0)
        expect(main.get_by_text("在 设置 › 渠道 里设置飞书机器人", exact=False)).to_be_visible()
        main.get_by_text("在 设置 › 渠道 里设置飞书机器人", exact=False).click()
        expect(page).to_have_url(base + "/settings?tab=channels&channel=feishu")
        assert not errors, errors
        print(f"PASS settings entries {width}px {theme}", flush=True)
    except Exception:
        print(f"FAIL settings entries {width}px {theme}; path={page.url.removeprefix(base)}; page errors={errors}", flush=True)
        if shots:
            page.screenshot(path=os.path.join(shots, f"failure-{width}-{theme}.png"))
        raise
    finally:
        context.close()


def check_capabilities(browser, base):
    for channels, extensions in ((True, False), (False, True)):
        context = browser.new_context(viewport={"width": 390, "height": 900}, locale="zh-CN")
        context.add_init_script("localStorage.setItem('persona.language', 'zh-Hans')")

        def socket(route):
            server = route.connect_to_server()

            def message(raw):
                data = json.loads(raw)
                if data.get("type") == "hello":
                    data["features"] = [feature for feature in data.get("features", [])
                        if (channels or feature != "channels")
                        and (extensions or not feature.startswith(("extension", "plugin")))]
                route.send(json.dumps(data))

            server.on_message(message)

        context.route_web_socket("**/api/v1/ws?*", socket)
        try:
            page = context.new_page()
            page.goto(base + "/settings?token=mock")
            nav = page.locator("main").get_by_role("navigation", name="设置分类")
            expect(nav.get_by_role("button").filter(has=page.get_by_text("渠道", exact=True))).to_have_count(int(channels))
            expect(nav.get_by_role("button").filter(has=page.get_by_text("扩展", exact=True))).to_have_count(int(extensions))
            if channels:
                nav.get_by_role("button").filter(has=page.get_by_text("渠道", exact=True)).click()
                expect(page).to_have_url(base + "/settings?tab=channels")
                expect(page.locator("main").get_by_text("Example Rooms", exact=True)).to_be_visible()
            print(f"PASS independent capabilities channels={channels} extensions={extensions}", flush=True)
        finally:
            context.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base")
    parser.add_argument("--shots")
    parser.add_argument("--cdp", help="Drive a shared Chrome through its CDP address; close only this test's contexts.")
    args = parser.parse_args()
    if args.shots:
        os.makedirs(args.shots, exist_ok=True)
    with sync_playwright() as playwright:
        browser = playwright.chromium.connect_over_cdp(args.cdp) if args.cdp else playwright.chromium.launch(channel="chrome")
        try:
            check_capabilities(browser, args.base.rstrip("/"))
            for width in (1440, 640, 390, 320):
                for theme in ("light", "dark"):
                    check(browser, args.base.rstrip("/"), width, theme, args.shots)
        finally:
            if not args.cdp:
                browser.close()
    print("PASS web settings entries e2e")


if __name__ == "__main__":
    main()
