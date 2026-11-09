"""Synthetic independent-message navigation fixture (#2035), never a live hub."""

from datetime import datetime, timedelta, timezone
import threading

MESSAGES = []
REQUESTS = []
LOCK = threading.Lock()
SESSION = None
HISTORY = "navigation-mock-v1"


def install(namespace):
    global SESSION
    MESSAGES.clear()
    with LOCK:
        REQUESTS.clear()
    SESSION = namespace["ASK1"]
    items = []
    for number in range(1, 81):
        seq = number * 10
        timestamp = (datetime(2026, 11, 9, tzinfo=timezone.utc) + timedelta(minutes=number)).isoformat().replace("+00:00", "Z")
        user = {"id": f"navigation-user-{number}#0", "seq": seq, "ts": timestamp,
                "role": "user", "kind": "text", "text": f"Navigation question {number}",
                "client_msg_id": f"navigation-client-{number}"}
        brief = f"Brief answer {number}"
        body = brief + "\n\n" + "\n\n".join(
            f"Synthetic explanation {part}: this paragraph gives the central scroll view enough content to move while the navigator observes it without taking its gesture."
            for part in range(1, 5))
        answer = {"id": f"navigation-answer-{number}#0", "seq": seq + 1, "ts": timestamp,
                  "role": "assistant", "kind": "text", "text": body}
        items.extend([user, answer])
        MESSAGES.append({"id": user["id"], "seq": seq, "ts": timestamp, "text": user["text"], "answer": brief})
        if number % 10 == 0:
            items.extend([
                {"id": f"navigation-compact-{number}", "seq": seq + 2, "ts": timestamp,
                 "role": "system", "kind": "notice", "text": "Context compacted", "compact": {"trigger": "auto", "pre_tokens": 170000}},
                {"id": f"navigation-supervise-{number}", "seq": seq + 3, "ts": timestamp,
                 "role": "user", "kind": "text", "text": "Synthetic automatic supervision",
                 "injected": {"kind": "system", "state": "supervise", "line": "Automatic supervision"}},
            ])
    namespace["ITEMS"][SESSION] = items
    namespace["PAGING"]["on"] = True
    namespace["HISTORY_NOW"]["token"] = HISTORY
    session = next(s for s in namespace["ASSISTANT"] if s["session_id"] == SESSION)
    session.update({"title": "Synthetic navigation history", "status": "idle", "state": "idle", "last_text": "Brief answer 80"})


def _number(query, key, default=0):
    return int(query.get(key, [str(default)])[0] or default)


def _record(kind, query):
    record = {"kind": kind}
    for field in ("before_seq", "around_seq", "after_seq"):
        if field in query:
            record[field] = _number(query, field)
    if "history" in query:
        record["history"] = query["history"][0]
    with LOCK:
        REQUESTS.append(record)


def route(handler, path, query, namespace):
    if path == "/mock/navigation":
        with LOCK:
            if query.get("reset") == ["1"]:
                REQUESTS.clear()
            requests = list(REQUESTS)
        handler.send_json({"requests": requests})
        return True
    prefix = f"/api/v1/sessions/server/{SESSION}"
    if path not in (prefix + "/navigation", prefix + "/items"):
        return False
    if query.get("history", [HISTORY])[0] != HISTORY:
        handler.send_json({"code": "stale_history", "error": "Synthetic history changed"}, 409)
        return True
    if path.endswith("/navigation"):
        _record("navigation", query)
        limit = min(100, max(20, _number(query, "limit", 40)))
        before = _number(query, "before_seq")
        older = [m for m in MESSAGES if not before or m["seq"] < before]
        messages = older[-limit:]
        more = len(older) > limit
        result = {"messages": messages, "has_more": more, "history": HISTORY}
        if more:
            result["next_before_seq"] = messages[0]["seq"]
        handler.send_json(result)
        return True
    items = namespace["ITEMS"][SESSION]
    last = items[-1]["seq"]
    if "around_seq" in query:
        _record("around", query)
        target = _number(query, "around_seq")
        index = next((i for i, item in enumerate(items) if item["seq"] == target), None)
        if index is None:
            handler.send_json({"code": "navigation_target_missing", "error": "Synthetic target missing"}, 409)
            return True
        limit = min(500, max(20, _number(query, "limit", 80)))
        start = max(0, index - limit // 2)
        end = min(len(items), start + limit)
        result = {"items": items[start:end], "has_more": start > 0, "more_after": end < len(items),
                  "history": HISTORY, "from_seq": items[start]["seq"] - 1,
                  "to_seq": items[end - 1]["seq"] if end < len(items) else last}
    elif "after_seq" in query:
        _record("after", query)
        after = _number(query, "after_seq")
        following = [item for item in items if item["seq"] > after]
        page = following[:500]
        result = {"items": page, "history": HISTORY, "from_seq": after,
                  "to_seq": page[-1]["seq"] if len(following) > 500 else max(last, after), "more_after": len(following) > 500}
    elif "through_seq" in query:
        start, end = _number(query, "from_seq"), _number(query, "through_seq")
        page = [item for item in items if start <= item["seq"] <= end]
        result = {"items": page[:1000], "more": len(page) > 1000}
    else:
        _record("before" if "before_seq" in query else "latest", query)
        before = _number(query, "before_seq")
        earlier = [item for item in items if not before or item["seq"] < before]
        page, more = namespace["last_turns"](earlier, _number(query, "turns", 2))
        result = {"items": page, "has_more": more, "history": HISTORY, "to_seq": before - 1 if before else last}
    handler.send_json(result)
    return True
