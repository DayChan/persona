#!/usr/bin/env python3
"""live_update.py [runs] [case …]: LiveUpdateTests cases through the runtime CLI (apple/PersonaUITests/LiveUpdateTests.swift,
which stays as it is for now). The mock pushes a busy hub's updates on demand (GET /mock/churn); the view-body counts and
main-thread time come from the app (`perf`, launched with -perfCounts YES) instead of the hidden probe text.

  python3 scripts/mock/mock_scenarios.py --port 18763 --scenario churn &
  PERSONACTL_UDID=<udid> HUB=http://127.0.0.1:18763 scripts/app/cli-tests/live_update.py 3
"""
import os
import time

from clitest import PINNED, Fail, main, mock, to_agents

HUB = os.environ.get("HUB", "http://127.0.0.1:18763")
WIDGET = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
ROW = dict(labelContains="、给天气 App 加主屏幕小组件、")
ROUNDS = 12


def churn(t, steps=1, final=False, waiting=False):
    """`ROUNDS` rounds of pushes 400 ms apart, then waits for the last one to be applied."""
    mock(t.c, f"churn?n={ROUNDS}&ms=400&steps={steps}&final={int(final)}&inbox=1&waiting={int(waiting)}")
    deadline = time.time() + 30
    while mock(t.c, "churned").get("rounds", 0) < ROUNDS + (1 if final else 0) and time.time() < deadline:
        time.sleep(0.3)
    time.sleep(1)


def counts(t):
    return t.c.call("perf")["counts"]


def bodies(a, b, name):
    return b["bodies"].get(name, 0) - a["bodies"].get(name, 0)


def report(name, a, b):
    """Bodies and main-thread time per round of pushes, as the XCUITest prints them (PERF lines)."""
    n = float(ROUNDS)
    line = (f"PERF {name}: per round ({b['events'] - a['events']} pushes in {b['passes'] - a['passes']} passes) "
            f"{(b['ms'] - a['ms']) / n:.1f} ms on the main thread, slowest pass {b['max']:.1f} ms")
    changed = {k: (v - a["bodies"].get(k, 0)) / n for k, v in b["bodies"].items() if v - a["bodies"].get(k, 0) > 0}
    print(line + "; " + ", ".join(f"{k} {v:.1f}" for k, v in sorted(changed.items())), flush=True)


def setup(t):
    mock(t.c, "reset")
    t.c.call("defaults", remove=["dev.machineFilter", "dev.grouping", "dev.compact", "dev.folded"])


def at_list(t, row_in_view=False, row=True):
    """The Agents list with the working agent's row; `row_in_view`: that row where a touch reaches it, above the tab
    bar (XCUITest scrolls an element into view before pressing or swiping it; the CLI's touches go where they are).
    `row` False: the list at its top, as it opens (the pull that shows the search field needs the top)."""
    to_agents(t)
    t.c.call("refresh")
    t.see("the list", timeout=15, id="agents.filter.all")
    if row:  # below the cards that need you: the list builds a row only once it is near the screen
        t.reach("the working agent's row", **ROW)
    t.settle()
    if row_in_view:
        for _ in range(4):
            e = t.find(**ROW)
            if e and e["visible"] and e["frame"][1] + e["frame"][3] < 780:
                return
            t.c.swipe("up", distance=250, duration=0.5)
            t.c.call("settle", timeout=2, stable=2, every=100, pixels=600)


def open_work_stays_open(t):
    """The open work card of a working agent stays open while steps and words come in."""
    t.c.call("navigate", scene="chat", session=WIDGET, coldChats=True)
    head = t.see("the work card's head", timeout=15, id="work-head")
    if head["label"] == "展开过程":
        t.c.tap(id="work-head")
    t.see("the work card open", timeout=5, label="收起过程")
    before = counts(t)
    churn(t)
    t.true(t.find(label="收起过程"), "the work card is still open")
    report("Chat, work card open", before, counts(t))


def row_menu_holds_still(t):
    """A row's long-press menu stays open, where it was, through updates of that very agent."""
    at_list(t, row_in_view=True)
    t.c.longpress(**ROW)
    e = t.see("the long-press menu opened", label="重命名…", timeout=5)
    y = e["frame"][1]
    before = counts(t)
    churn(t)
    e = t.find(label="重命名…")
    t.true(e, "the long-press menu is still open")
    t.true(abs(e["frame"][1] - y) <= 1, f"the long-press menu did not move ({y} → {e['frame'][1]})")
    after = counts(t)
    t.c._idb("tap", 201, 70)  # outside the menu, as the XCUITest taps (0.5, 0.08)
    report("Agents list, long-press menu open", before, after)
    t.eq(bodies(before, after, "RowMenu.content"), 0, "the open long-press menu's content was rebuilt by the pushes")
    t.gone("the menu closes", label="重命名…", timeout=5)


def row_more_menu_stays_open(t):
    """A row's ⋯ menu (#1244: the rows do not swipe) stays open while its agent keeps working."""
    at_list(t, row_in_view=True)
    t.c.tap(id="agents.row.more", labelContains="给天气 App 加主屏幕小组件")
    t.see("the row's menu", label="关闭 Agent…", trait="button", timeout=5)
    churn(t)
    t.true(t.find(label="关闭 Agent…", trait="button"), "the menu is still open")
    t.c._idb("tap", 201, 70)  # outside the menu
    t.gone("the menu closes", label="关闭 Agent…", timeout=5)


def search_keeps_typed(t):
    """Typing into the search field: the text and the focus stay while the list behind it changes."""
    at_list(t, row=False)
    t.c.drag((201, 300), (201, 520), 0.8)  # the pull that shows the folded search field
    t.until("the search field shows", lambda: (t.find(trait="search") or {"frame": [0, 0, 0, 0]})["frame"][3] > 30, 5)
    t.c.type("platform", trait="search")
    t.until("typed", lambda: t.find(trait="search")["value"] == "platform", 5)
    before = counts(t)
    churn(t)
    t.eq(t.find(trait="search")["value"], "platform", "what was typed is still there")
    try:
        t.c.call("type", text="")  # "no text field has focus" once the field lost it (the keyboard went down)
    except Exception:
        raise Fail("the search field lost its focus (the keyboard went down)")
    report("Agents list, searching", before, counts(t))
    t.c.call("dismiss-keyboard")


def list_menu_holds_still(t):
    """The list's ⋯ menu stays open where it was, scrolled to the machines, while updates come in."""
    at_list(t)
    t.c.tap(labelContains="分组：", trait="button")
    t.see("the menu opened", label="全部机器", trait="button", timeout=5)
    t.settle()
    t.c.swipe("up", duration=0.6, label="全部机器", trait="button")  # down to the machines, as when choosing one
    t.settle()
    last = t.see("the last machine", label="训练机", trait="button", timeout=3)
    first = t.find(label="全部机器", trait="button")
    before = counts(t)
    churn(t, waiting=True)
    last_after, first_after = t.find(label="训练机", trait="button"), t.find(label="全部机器", trait="button")
    after = counts(t)
    report("Agents list, ⋯ menu open", before, after)
    t.true(last_after, "the menu is still open")
    t.true(abs(last_after["frame"][1] - last["frame"][1]) <= 1 and abs(first_after["frame"][1] - first["frame"][1]) <= 1,
           f"the menu did not scroll or jump ({last['frame'][1]} → {last_after['frame'][1]})")
    t.true(bodies(before, after, "ListMenu.content") <= 1, "the open menu's content is not rebuilt for each push")
    t.c._idb("tap", 362, 830)  # close it, as the XCUITest taps (0.9, 0.95)


CASES = [open_work_stays_open, row_menu_holds_still, row_more_menu_stays_open, search_keeps_typed, list_menu_holds_still]

if __name__ == "__main__":
    main(CASES, setup, lambda c: c.launch(HUB, PINNED + ["-perfCounts", "YES", "-openChatsFresh", "YES", "-openAgentsFresh", "YES"]))
