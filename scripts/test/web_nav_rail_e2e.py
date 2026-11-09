#!/usr/bin/env python3
"""End-to-end check of the question navigation rail (#1739): a wide chat with several of your questions draws a slim
tick rail in its left margin (one tick per question); hovering a tick previews the question and the start of its
answer, clicking jumps to that turn (the question lands near the top, its tick becomes the current one), Tab reaches
the ticks with a visible focus state, and the rail dims after a moment of quiet. A narrow screen and a short chat
draw nothing: with --base (the same page served from origin/main) those two states must be pixel-identical.

    MOCK_LONG_HISTORY=1 MOCK_STALE_DONE=1 PORT=18861 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18861 npx vite --port 5361 --strictPort &)
    python3 scripts/test/web_nav_rail_e2e.py http://127.0.0.1:5361 [--base-url http://127.0.0.1:5362] [--shots <dir>]

MOCK_STALE_DONE leaves the long chat idle, so its own animation (a working session's spinner) does not blur the pixel
comparison; --base-url serves origin/main's web against the same mock hub (the two shots are seconds apart).

--shots saves wide light / dark (the rail at rest, the hover preview, keyboard focus), the tick a click jumped to, and
the phone-width chat; the pixel comparison runs in both themes. Prints one line per check and a verdict; exits 1 on
the first failure.
"""
import argparse
import os
import sys
import time

from playwright.sync_api import expect, sync_playwright

WIDGET = "a1f3c2d4-0000-4000-8000-000000000001"  # the weather chat; MOCK_LONG_HISTORY gives it 15 earlier questions
REVIEW = "a1f3c2d4-0000-4000-8000-000000000006"  # one question, idle: a short chat that holds still (no avatar breathing)
LONG_CHAT = "/s/macbook/" + WIDGET
SHORT_CHAT = "/s/macbook/" + REVIEW
QUESTIONS = 16  # the 15 of the history + the widget one
WIDE = {"width": 1600, "height": 900}
PHONE = {"width": 390, "height": 844}
IDLE_FADE_MS = 2000
# NavRail's `duration-200`: the idle class flips at IDLE_FADE_MS, then opacity eases to 0.35. A single read after a
# fixed wait can land inside that ease (still near 1) and fail on a rail that does dim.
FADE_MS = 200
DIMMED = 0.35


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def shot(page, shots, name):
    if shots:
        page.screenshot(path=os.path.join(shots, name + ".png"))


def wait_for(page, cond, what, timeout=8.0):
    end = time.time() + timeout
    while time.time() < end:
        if cond():
            return
        page.wait_for_timeout(120)
    fail(what)


def open_chat(page, base, token, path, theme="light"):
    """Opens a chat with the theme set explicitly (the app does not follow the browser's color scheme: index.html reads
    persona.theme before the first paint), and holds the check that it took — a shot in the wrong theme is worse than
    a failure here."""
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    page.goto(base + path)
    dark_wanted = "true" if theme == "dark" else "false"
    page.wait_for_function(f"document.documentElement.classList.contains('dark') === {dark_wanted}")
    page.wait_for_timeout(900)  # the transcript draws, the chat settles at its end


def rail(page):
    return page.locator("[data-nav-rail]")


def pixels_differ(a_path, b_path):
    from PIL import Image, ImageChops

    a = Image.open(a_path).convert("RGB")
    b = Image.open(b_path).convert("RGB")
    if a.size != b.size:
        return True
    return ImageChops.difference(a, b).getbbox() is not None


def case(browser, base, token, shots, base_url):
    # ── wide, light: the rail with one tick per question ──
    ctx = browser.new_context(viewport=WIDE, device_scale_factor=2, locale="zh-CN")
    page = ctx.new_page()
    page.set_default_timeout(10000)
    open_chat(page, base, token, LONG_CHAT)
    expect(rail(page)).to_have_count(1)
    ticks = rail(page).locator("button")
    expect(ticks).to_have_count(QUESTIONS)
    expect(ticks.first).to_have_attribute("aria-label", "跳到第 1 个问题：第 1 个问题：顺便看一下这一块的写法有没有问题？")
    ok(f"wide: the rail draws one tick per question ({QUESTIONS})")
    shot(page, shots, "rail-wide-light-rest")

    # hovering a tick previews the question and the start of its answer
    ticks.nth(0).hover()
    card = page.locator("[data-nav-rail-preview]")
    expect(card).to_be_visible()
    expect(card).to_contain_text("第 1 个问题：顺便看一下这一块的写法有没有问题？")
    expect(card).to_contain_text("看过了，第 1 块没有问题。")
    ok("wide: hovering a tick previews the question and its answer's start")
    shot(page, shots, "rail-wide-light-hover")

    # clicking a tick jumps there: the question lands near the top, its tick is the current one
    def near_top(words):
        return page.evaluate(
            """(words) => {
              const sc = document.querySelector("[data-chat-scroll]");
              const bubble = [...sc.querySelectorAll("[data-user-bubble]")].find((el) => el.textContent.includes(words));
              return Boolean(bubble) && bubble.getBoundingClientRect().top - sc.getBoundingClientRect().top <= 40;
            }""",
            words,
        )

    ticks.nth(3).click()
    wait_for(page, lambda: near_top("第 4 个问题"), "the clicked question did not land near the top")
    expect(ticks.nth(3)).to_have_attribute("aria-current", "true")
    ok("wide: clicking a tick jumps to that question and marks it current")
    shot(page, shots, "rail-wide-light-after-jump")

    # the keyboard reaches the ticks: focus moves between them, shows the preview, and Enter jumps
    page.keyboard.press("Shift+Tab")  # the clicked tick still holds the focus: one back is the previous question
    focused = page.evaluate("document.activeElement.getAttribute('aria-label')")
    if focused != "跳到第 3 个问题：第 3 个问题：顺便看一下这一块的写法有没有问题？":
        fail(f"Shift+Tab moves the focus between ticks (got {focused!r})")
    expect(card).to_contain_text("第 3 个问题")
    page.keyboard.press("Enter")
    wait_for(page, lambda: near_top("第 3 个问题"), "Enter on a focused tick did not jump")
    expect(ticks.nth(2)).to_have_attribute("aria-current", "true")
    ok("wide: the ticks are keyboard reachable (Shift+Tab / Enter) with the preview on focus")
    shot(page, shots, "rail-wide-light-focus")

    # quiet for a while: the rail dims (and stays there, clickable) — with the pointer and the focus off it.
    # Poll until the fade has settled. The bound covers the idle delay plus the opacity transition, with slack for
    # the timer to arm; it returns as soon as the opacity is the dimmed value, so it does not sleep the whole bound.
    page.mouse.move(WIDE["width"] * 0.7, WIDE["height"] / 2)
    page.evaluate("document.querySelector('textarea')?.focus()")
    seen = {"opacity": None}
    # A smooth scroll can still be emitting after the jump looks landed, and each event restarts the 2s idle timer,
    # so the bound is a full idle cycle past the fade — not a single sample. The loop returns as soon as it settles.
    end = time.time() + (IDLE_FADE_MS + FADE_MS + IDLE_FADE_MS) / 1000 + 1.5

    def dimmed():
        seen["opacity"] = float(rail(page).evaluate("el => getComputedStyle(el).opacity"))
        return abs(seen["opacity"] - DIMMED) <= 0.02

    while time.time() < end:
        if dimmed():
            break
        page.wait_for_timeout(50)
    else:
        fail(f"the idle rail dims (opacity {seen['opacity']})")
    ticks.nth(5).click()
    wait_for(page, lambda: near_top("第 6 个问题"), "a dimmed tick did not jump")
    ok("wide: the rail dims when idle and a dimmed tick still jumps")

    # dark: the same rail over the dark theme (rest, hover, keyboard focus)
    open_chat(page, base, token, LONG_CHAT, theme="dark")
    expect(rail(page)).to_have_count(1)
    expect(ticks).to_have_count(QUESTIONS)
    shot(page, shots, "rail-wide-dark-rest")
    ticks.nth(1).hover()
    expect(card).to_be_visible()
    shot(page, shots, "rail-wide-dark-hover")
    ticks.nth(3).click()
    page.wait_for_timeout(700)
    page.keyboard.press("Shift+Tab")
    expect(card).to_contain_text("第 3 个问题")
    shot(page, shots, "rail-wide-dark-focus")
    ok("wide dark: the rail, its preview and the focus state draw")
    ctx.close()

    # ── a phone-width chat and a short chat draw nothing ──
    for size, path, name in ((PHONE, LONG_CHAT, "phone-long"), (WIDE, "/", "short-main"), (WIDE, SHORT_CHAT, "short-wide")):
        ctx = browser.new_context(viewport=size, device_scale_factor=2, locale="zh-CN")
        page = ctx.new_page()
        page.set_default_timeout(10000)
        open_chat(page, base, token, path)
        expect(page.locator("[data-user-bubble], .prose-j").last).to_be_visible()
        expect(rail(page)).to_have_count(0)
        ok(f"{name}: the rail stays away")
        if shots and size == PHONE:
            shot(page, shots, "phone-long-light")
        ctx.close()
        # the main conversation's avatar breathes (a liveness signal reduced motion keeps): no pixel comparison there
        if base_url and name != "short-main":
            compare(browser, base, base_url, token, path, size, name, "light")
            compare(browser, base, base_url, token, path, size, name, "dark")


def compare(browser, base, base_url, token, path, size, name, theme):
    """The same no-rail state from this build and from origin/main, taken seconds apart, is pixel-identical (light and
    dark each). The shot is of the page (<main>: the chat and its margins, where the rail would be) — the sidebar's
    animated avatar is outside it and not this change's. Reduced motion and no focus: a working session's own animation
    (a spinner, a breathing dot) is exempt from reduced motion, so the mock's long chat runs idle (MOCK_STALE_DONE); a
    minute rolling over between the two shots is covered by taking the pair again once."""
    tmp = os.path.join("/tmp", "nav-rail-compare")
    os.makedirs(tmp, exist_ok=True)
    pair = {label: os.path.join(tmp, f"{name}-{theme}-{label}.png") for label in ("base", "next")}

    def take(url, label):
        ctx = browser.new_context(viewport=size, device_scale_factor=2, locale="zh-CN", reduced_motion="reduce")
        page = ctx.new_page()
        page.set_default_timeout(10000)
        open_chat(page, url, token, path, theme=theme)
        page.evaluate("document.activeElement?.blur()")
        page.wait_for_timeout(200)
        page.locator("main").screenshot(path=pair[label])
        ctx.close()

    take(base_url, "base")
    take(base, "next")
    for _ in (1, 2):
        if not pixels_differ(pair["base"], pair["next"]):
            ok(f"{name}/{theme}: pixel-identical to origin/main")
            return
        take(base_url, "base")
        take(base, "next")
    fail(f"{name}/{theme}: differs from origin/main (see {pair['base']} and {pair['next']})")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--base-url", dest="base_url", help="the same page served from origin/main, for the pixel comparison")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    a = ap.parse_args()
    base = a.base.rstrip("/")
    base_url = a.base_url.rstrip("/") if a.base_url else None
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            case(browser, base, a.token, a.shots, base_url)
        finally:
            browser.close()
    print("PASS web nav rail e2e")


if __name__ == "__main__":
    main()
