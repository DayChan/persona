"""MOCK_AGENT_REVIEW=1 (#1808): the hub side of an independent review of a session's work — the POST/GET the web's
"独立审查这项工作" menu entry calls, and the findings card (kind "review") it leaves on the source session's chat.

The routes answer for the demo chat the Review e2e also uses (the weather app on the macbook machine). GET
/mock/agent-review switches what the hub would say, and reads back what the page asked:

    ?state=card|none      whether the finished review's card is up on the session
    ?count=1|0            the card's count line: "3 条发现" (1) or "审查完成" without a count (0)
    ?running=1|0          whether a review is under way (the GET's answer)
    ?reset=1              clear the recorded starts and answers
    ?refuse=<code>        the POST's refusal instead of a start: session_busy, no_change, review_running,
                          read_only_unavailable or not_supported, with the hub's words for it
    no arguments          the current state and the starts/answers the page made
"""

import threading

# The demo chat (machine "macbook", the weather app's idle session) the card goes on.
MACHINE = "macbook"
SESSION = "a1f3c2d4-0000-4000-8000-000000000006"
REVIEWER = "e5f6a7b8-0000-4000-8000-000000000003"

FINDINGS = """**P0 · Forecast.swift:42** — retry 只认 HTTP 429，超时（URLError.timedOut）不重试，弱网下刷新会直接失败。
建议把超时加进可重试错误集合，并给重试上限。

**P1 · Units.swift:17** — `celsius` 是全局常量，测试里换不了区域；格式化跟系统走而不是用户设置。
建议把单位作为参数传给 formatter。

**P2 · docs/hourly.md:8** — 09:00 一行的降水概率写成了 91%，和正文“全天不超过 40%”矛盾。

没有其它发现：改动范围里的其余文件只读了没改。"""

# The refusal each ?refuse= code answers with: the hub's own words, as the apps show them.
REFUSALS = {
    "session_busy": ("这个 Agent 正在工作，等它停下来再审查", "session_busy"),
    "no_change": ("这个目录里没有可审查的改动", "no_change"),
    "review_running": ("这个会话已经有一场审查在进行", "review_running"),
    "read_only_unavailable": ("这个 Agent 的启动器没法以只读方式启动审查", "read_only_unavailable"),
    "not_supported": ("这台电脑现在没法启动新的 Agent", "not_supported"),
}

STATE = {"card": True, "count": True, "running": False, "refuse": ""}
# RLock: state() holds it while it reads the card() of the same module.
LOCK = threading.RLock()
STARTS = []  # the POSTs the page made: {machine, session, answered}
ANSWERS = []  # the resolve bodies the page sent to the card


def ago(minutes):
    import datetime
    return (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(minutes=minutes)).isoformat().replace("+00:00", "Z")


def card():
    """The pending findings card as the hub serves it: kind "review" on the source session."""
    with LOCK:
        count, up = STATE["count"], STATE["card"]
    if not up:
        return None
    ix = {"id": "review_%s" % "a1b2c3", "machine_id": MACHINE, "session_id": SESSION, "kind": "review",
          "tool_name": "persona_review", "summary": "3 条发现" if count else "审查完成",
          "created_at": ago(2), "expires_at": ago(-7 * 24 * 60),
          "findings": FINDINGS, "review_session": REVIEWER}
    if count:
        ix["findings_count"] = 3
    return ix


def start():
    """The POST's answer once the reviewer is under way."""
    return {"running": True, "reviewer_session_id": REVIEWER, "started_at": ago(0)}


def state():
    """The GET's answer: whether a review runs, and the card waiting on the session."""
    with LOCK:
        running, c = STATE["running"], card()
    return {"running": running, "pending": c, "started_at": ago(3) if running else None}


def refused(code):
    words, _ = REFUSALS.get(code, REFUSALS["not_supported"])
    return {"error": words, "code": code, "delivered": "none"}, 409


def control(q):
    """GET /mock/agent-review: switch the state, then read it (with what the page asked so far)."""
    with LOCK:
        if q.get("reset") == ["1"]:
            STARTS.clear()
            ANSWERS.clear()
        if "state" in q:
            STATE["card"] = q["state"][0] == "card"
        if "count" in q:
            STATE["count"] = q["count"][0] == "1"
        if "running" in q:
            STATE["running"] = q["running"][0] == "1"
        if "refuse" in q:
            STATE["refuse"] = q["refuse"][0] if q["refuse"][0] in REFUSALS else ""
        out = {"state": dict(STATE), "starts": STARTS, "answers": ANSWERS}
    return out


def note_start(machine, session):
    with LOCK:
        STARTS.insert(0, {"machine": machine, "session": session})
        del STARTS[20:]


def note_answer(resolution):
    with LOCK:
        ANSWERS.insert(0, resolution)
        del ANSWERS[20:]
