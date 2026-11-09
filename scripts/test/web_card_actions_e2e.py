#!/usr/bin/env python3
"""End-to-end check that a waiting card's answer buttons stay in view when the card is taller than the space above the
input (#2017): a permission card opened on its raw parameters (a long command), and two cards waiting at once, on the
wide layout, light and dark. After the raw parameters open, the button row (`data-card-actions`) must be inside the
viewport and inside the visible area of the box above the input (`data-chat-dock-above`), without scrolling anything;
rejecting the second of two cards must leave its receipt (`data-answered`) at its full height.

Needs the fake hub with the long command, a web page against it, and Playwright for Python (it drives the installed Chrome):

    MOCK_ASSISTANT_CARDS=long PORT=18799 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18799 npx vite --port 5299 --strictPort &)
    python3 scripts/test/web_card_actions_e2e.py http://127.0.0.1:5299 [--shots <dir>] [--size 1280x800]

The two-card check answers one of the hub's cards, so each run needs its own hub. Prints one line per check and a verdict;
exits 1 on the first failure.
"""
import argparse
import os
import sys

from playwright.sync_api import expect, sync_playwright

ASK1 = "/persona/b2e4d6f8-0000-4000-8000-000000000001"  # Persona's main chat: the hub's approvals wait there
CARD = "[data-card]"
ACTIONS = f"{CARD} [data-card-actions]"
DOCK_ABOVE = "[data-chat-dock-above]"

# The row's box against the viewport and against the visible part of the scrolling box above the input (its padding
# edge); the cards sit inside it, so a row that fits both is in view without a scroll.
MEASURE = """(sel) => {
  const row = document.querySelector(sel);
  const above = document.querySelector('[data-chat-dock-above]');
  const r = row.getBoundingClientRect(), a = above.getBoundingClientRect();
  return { top: r.top, bottom: r.bottom, height: r.height, aboveTop: a.top, aboveBottom: a.bottom,
           viewport: window.innerHeight, scrollable: above.scrollHeight > above.clientHeight + 1,
           canScroll: above.scrollHeight - above.clientHeight - above.scrollTop };
}"""


def ok(msg):
    print("ok  " + msg, flush=True)


def open_chat(browser, base, token, size, dark):
    ctx = browser.new_context(viewport={"width": size[0], "height": size[1]}, device_scale_factor=2, locale="zh-CN")
    ctx.add_init_script(f"localStorage.setItem('persona.theme', '{'dark' if dark else 'light'}')")
    page = ctx.new_page()
    page.set_default_timeout(10000)
    page.goto(f"{base}{ASK1}?token={token}")
    page.wait_for_selector(ACTIONS)
    page.wait_for_timeout(600)  # the card fades up in 350 ms
    return ctx, page


def shot(page, path):
    """The cards and the input (the dock) with a margin above, not the whole page: that is where the check looks."""
    box = page.evaluate("(() => { const b = document.querySelector('[data-chat-dock]').getBoundingClientRect();"
                        " return { x: b.x - 16, y: Math.max(0, b.y - 60), width: b.width + 32, height: b.height + 60 + 16 }; })()")
    box["height"] = min(box["height"], page.viewport_size["height"] - box["y"])
    page.screenshot(path=path, clip=box)


def assert_in_view(page, sel, what):
    m = page.evaluate(MEASURE, sel)
    assert m["height"] > 0, f"{what}: the button row has no size"
    assert m["top"] >= 0 and m["bottom"] <= m["viewport"] + 0.5, f"{what}: the buttons are outside the viewport {m}"
    assert m["top"] >= m["aboveTop"] - 0.5 and m["bottom"] <= m["aboveBottom"] + 0.5, f"{what}: the buttons are outside the box above the input {m}"
    return m


def run(browser, base, token, size, shots):
    # The first card waiting is the approval with the long command; the second is the connector's send.
    for theme in ("light", "dark"):
        dark = theme == "dark"
        ctx, page = open_chat(browser, base, token, size, dark)
        page.locator(f"{CARD} button[aria-expanded]").first.click()
        page.wait_for_timeout(400)
        m = page.evaluate(MEASURE, ACTIONS)
        assert m["scrollable"], f"the opened card should be taller than the space above the input ({theme}); lengthen the command {m}"
        m = assert_in_view(page, ACTIONS, f"first card opened, {theme}")
        ok(f"opened on its raw parameters ({theme}, {size[0]}x{size[1]}): the buttons are in view without scrolling "
           f"(row {m['top']:.0f}-{m['bottom']:.0f}, box {m['aboveTop']:.0f}-{m['aboveBottom']:.0f})")
        if shots:
            shot(page, os.path.join(shots, f"card-actions-{theme}.png"))
        # scrolled to the top of the box the row stays at its bottom edge
        page.evaluate("document.querySelector('[data-chat-dock-above]').scrollTop = 0")
        assert_in_view(page, ACTIONS, f"scrolled to the top, {theme}")
        ctx.close()
    ok("scrolling the box keeps the first card's buttons in view (light and dark)")

    # Two cards waiting; reject the second: its receipt keeps its full height instead of being squeezed.
    ctx, page = open_chat(browser, base, token, size, False)
    expect(page.locator(ACTIONS)).to_have_count(2)
    page.locator(f"{CARD} button[aria-expanded]").first.click()
    page.wait_for_timeout(400)
    page.locator(ACTIONS).nth(1).locator("button").first.click()  # the first button of a permission card is the refusal
    page.wait_for_selector("[data-answered]")
    page.wait_for_timeout(500)
    h = page.evaluate("""() => { const a = document.querySelector('[data-answered]');
                               return { box: a.getBoundingClientRect().height, content: a.scrollHeight }; }""")
    assert h["box"] >= h["content"] - 0.5, f"the receipt was squeezed: {h}"
    ok(f"the receipt of a rejected card keeps its full height ({h['box']:.0f}px of {h['content']}px)")
    if shots:
        shot(page, os.path.join(shots, "card-actions-receipt-light.png"))
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    ap.add_argument("--size", default="1280x800")
    a = ap.parse_args()
    base = a.base.rstrip("/")
    size = tuple(int(n) for n in a.size.split("x"))
    assert size[0] >= 1024, "the sticky row is for the wide layout (1024 px and up)"
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            run(browser, base, a.token, size, a.shots)
        finally:
            browser.close()
    print("PASS web card actions e2e")


if __name__ == "__main__":
    try:
        main()
    except AssertionError as e:
        print("FAIL " + str(e), flush=True)
        sys.exit(1)
