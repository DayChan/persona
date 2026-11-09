#!/usr/bin/env python3
"""End-to-end check of dropdown arrows on the web (#2314): every single-line <select> draws the icon set's chevron
(styles.css --select-chevron) inside its own right padding, the browser draws no arrow of its own, the text starts at
the field's left padding, and a select that draws its own arrow (appearance-none, Machines › Usage's filter pills)
gets no second one.

The bug: the selects kept the browser's native arrow on top of our field styling. Chrome pinned a bold black arrow
2 px from the right edge while the text sat 14.5 px from the left; Safari dropped the field's padding altogether and
drew ⇕ 6 px from the right.

Needs a running web page against the fake hub and Playwright for Python with Pillow (it drives the installed Chrome;
WebKit too when Playwright's WebKit runs on this machine):

    PORT=<mock port> python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=<mock hub URL> npx vite --port <web port> --strictPort &)
    python3 scripts/test/web_select_chevron_e2e.py <web URL> [--browser chromium|webkit|all] [--shots <dir>]

It looks at every visible select on Settings › Agents (Claude Code, Codex, …), General, Persona and Models and on a
machine's page, wide and at phone width, light and dark. Prints one line per page and a verdict; exits 1 on the first
failure. --shots saves each select.
"""
import argparse
import io
import os
import sys

from PIL import Image, ImageChops
from playwright.sync_api import sync_playwright

PAGES = ["/settings?tab=agents", "/settings?tab=general", "/settings?tab=persona", "/settings?tab=models", "/machines/studio"]
SIZES = (("wide", (1280, 860), False), ("narrow", (390, 844), True))
SCALE = 2


def ok(msg):
    print("ok   " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def grab(sel, style=""):
    """The select's pixels at SCALE, with an inline style added for the shot."""
    sel.evaluate("(e, s) => { e.dataset.probeStyle = e.getAttribute('style') ?? ''; e.setAttribute('style', e.dataset.probeStyle + ';' + s) }", style)
    try:
        return Image.open(io.BytesIO(sel.screenshot(animations="disabled"))).convert("RGB")
    finally:
        sel.evaluate("e => { const s = e.dataset.probeStyle; if (s) e.setAttribute('style', s); else e.removeAttribute('style'); delete e.dataset.probeStyle }")


def ink_box(a, b):
    """Where two shots differ, in CSS px (left, top, right, bottom), or None."""
    box = ImageChops.difference(a, b).point(lambda v: 255 if v > 24 else 0).getbbox()
    return None if box is None else tuple(v / SCALE for v in box)


def check_select(sel, what, shots, name):
    s = sel.evaluate("""e => { const c = getComputedStyle(e); return { appearance: c.appearance, image: c.backgroundImage,
        pl: parseFloat(c.paddingLeft), pr: parseFloat(c.paddingRight), own: e.classList.contains('appearance-none') } }""")
    if s["own"]:
        if s["image"] != "none":
            fail(f"{what}: a select that draws its own arrow got the chevron too ({s['image'][:40]}…)")
        return "own arrow"
    if s["appearance"] != "none":
        fail(f"{what}: appearance is {s['appearance']}, so the browser still draws its own arrow")
    if "data:image/svg+xml" not in s["image"]:
        fail(f"{what}: no chevron background ({s['image'][:60]})")
    shown = grab(sel)
    plain = grab(sel, "background-image: none")
    bare = grab(sel, "background-image: none; color: transparent")
    w, h = shown.size[0] / SCALE, shown.size[1] / SCALE
    if shots:
        shown.save(os.path.join(shots, name))

    # The chevron: what changes when its background goes away. It sits in the right padding, off the edge, centred.
    chevron = ink_box(shown, plain)
    if chevron is None:
        fail(f"{what}: the chevron draws nothing")
    left, top, right, bottom = chevron
    if left < w - s["pr"] or right > w - 4:
        fail(f"{what}: chevron at x {left}–{right} of {w}, not inside the {s['pr']} px right padding (and 4 px off the edge)")
    if abs((top + bottom) / 2 - h / 2) > 1.5:
        fail(f"{what}: chevron at y {top}–{bottom} of {h}, not centred")

    # No native arrow: with the chevron and the text gone, the right padding is the field's plain fill.
    fill = bare.getpixel((bare.size[0] // 2, bare.size[1] // 2))
    region = bare.crop((int((w - s["pr"] + 1) * SCALE), 4 * SCALE, int((w - 3) * SCALE), int((h - 4) * SCALE)))
    flat = Image.new("RGB", region.size, fill)
    stray = ImageChops.difference(region, flat).point(lambda v: 255 if v > 24 else 0).getbbox()
    if stray is not None:
        fail(f"{what}: something besides the chevron is drawn in the right padding (a native arrow?) at {stray}")

    # The text starts at the left padding (Safari used to drop it, Chrome to add its own).
    text = ink_box(plain, bare)
    if text is None:
        fail(f"{what}: no text")
    if abs(text[0] - s["pl"]) > 3:
        fail(f"{what}: text starts {text[0]} px in, the padding is {s['pl']} px")
    return f"text {text[0]:.1f} px from the left, chevron {w - right:.1f} px from the right"


def run(p, engine, base, a):
    browser = p.webkit.launch() if engine == "webkit" else p.chromium.launch(channel=a.channel)
    try:
        for label, (width, height), mobile in SIZES:
            for theme in ("light", "dark"):
                opts = {"viewport": {"width": width, "height": height}, "device_scale_factor": SCALE, "color_scheme": theme}
                if mobile and engine != "webkit":
                    opts.update(is_mobile=True, has_touch=True)
                ctx = browser.new_context(**opts)
                ctx.add_init_script(f"localStorage.setItem('persona.theme', '{theme}')")
                page = ctx.new_page()
                page.set_default_timeout(20000)
                page.goto(base + "/overview?token=" + a.token)
                page.wait_for_function("localStorage.getItem('persona.token')")
                for path in PAGES:
                    page.goto(base + path)
                    selects = page.locator("select")
                    selects.first.wait_for()
                    page.wait_for_timeout(500)  # rows fade in
                    page.mouse.move(0, 0)
                    seen = []
                    for i in range(selects.count()):
                        sel = selects.nth(i)
                        if not sel.is_visible():
                            continue
                        sel.scroll_into_view_if_needed()
                        what = f"{engine} {label} {theme} {path} select {i}"
                        tag = path.strip("/").replace("/", "-").replace("?tab=", "-")
                        seen.append(check_select(sel, what, a.shots, f"{engine}-{label}-{theme}-{tag}-{i}.png"))
                    if not seen:
                        fail(f"{engine} {label} {theme} {path}: no visible select")
                    ok(f"{engine} {label} {theme} {path}: {len(seen)} selects; " + "; ".join(sorted(set(seen))))
                ctx.close()
    finally:
        browser.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--browser", default="chromium", choices=["chromium", "webkit", "all"])
    ap.add_argument("--channel", default="chrome", help="the Chromium to drive (chrome, msedge)")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    a = ap.parse_args()
    base = a.base.rstrip("/")
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    engines = ["chromium", "webkit"] if a.browser == "all" else [a.browser]
    with sync_playwright() as p:
        for engine in engines:
            run(p, engine, base, a)
    print("PASS web select chevron e2e")


if __name__ == "__main__":
    main()
