#!/usr/bin/env python3
"""Writes the app's demo mode data (apple/Persona/Resources/Demo.json) from scripts/mock/mock_hub.py's made-up hub.

    python3 scripts/mock/make_demo_data.py        # rerun after changing mock_hub.py's data; commit the JSON

Demo mode (docs/apple/ios.md) lets someone without a hub, App Review included, walk through every page: the app answers its
own requests from this file (apple/Persona/App/DemoHub.swift) and never connects anywhere. Everything here is made up,
like the mock hub's: no real names, hosts or messages (scripts/privacy_check.py scans the file, check_bundle_privacy.py
the built app). Times are stored relative to when this ran ("@rel:<seconds>", "@unix:<seconds>") and the app puts
them next to its own clock, so the demo always looks current; a to-do's due moment keeps its clock time ("@day:<days>@<HH:MM>")
and its due day is so many days away ("@date:<days>"). Machine metrics are drawn by the app itself.
"""
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.environ.get("PERSONA_DEMO_OUTPUT", os.path.join(HERE, "..", "..", "apple", "Persona", "Resources", "Demo.json"))

os.environ.update(MOCK_TASKS="1", MOCK_RICH="1")
sys.path.insert(0, HERE)
import mock_hub as m  # noqa: E402

ISO = re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$")
DAY = re.compile(r"^\d{4}-\d\d-\d\d$")
T0 = m.NOW.timestamp()
TODAY = m.NOW.astimezone().date()


def relative(v, key=None):
    """Every timestamp as an offset from now (strings) or from the Unix now (`boot_time`)."""
    if isinstance(v, dict):
        return {k: relative(x, k) for k, x in v.items()}
    if isinstance(v, list):
        return [relative(x) for x in v]
    if key == "due_at" and isinstance(v, str) and ISO.match(v):
        # A to-do due at a clock time (#2029's calendar): that time of day, so many days from the day the demo opens.
        t = m.datetime.fromisoformat(v.replace("Z", "+00:00")).astimezone()
        return "@day:%d@%02d:%02d" % ((t.date() - TODAY).days, t.hour, t.minute)
    if key == "due_date" and isinstance(v, str) and DAY.match(v):
        return "@date:%d" % (m.datetime.fromisoformat(v).date() - TODAY).days
    if isinstance(v, str) and ISO.match(v):
        return "@rel:%d" % round(m.datetime.fromisoformat(v.replace("Z", "+00:00")).timestamp() - T0)
    if key == "boot_time" and isinstance(v, int):
        return "@unix:%d" % (v - int(T0))
    return v


# A few more agents, so the list has several spaces in every state and the filters have something to do.
EXTRA = [
    ("studio", "docs-site", 4, "首页文案改成中英双语", "done", "unread", 25, "中英文两套首页都好了，语言按浏览器设置自动选。"),
    ("studio", "docs-site", 4, "文档站加全文搜索", "working", "working", 1, "索引建好了，正在调中文分词。"),
    ("studio", "docs-site", 4, "修复深色模式下的代码块配色", "idle", "idle", 300, "代码块配色已经按系统深浅色切换。"),
    ("server", "infra", 2, "部署脚本支持一键回滚", "done", "unread", 40, "回滚做好了：保留最近 5 个版本，一条命令切回上一个。"),
    ("server", "infra", 2, "清理过期的 Docker 镜像", "idle", "idle", 600, "清掉了 38 GB 没在用的镜像，加了每周自动清理。"),
    ("server", "infra", 2, "给 API 加限流", "working", "working", 3, "按 token 做令牌桶限流，正在补压测。"),
    ("macbook", "weather", 1, "整理小组件的截图素材", "idle", "idle", 900, "六张截图都按 App Store 的尺寸导出了。"),
]
# The compact list needs every density on screen (docs/apple/ios.md "Agents list"): a space with many finished-unread agents, spaces
# where every agent is idle ("Idle spaces"), and a machine where nothing runs at all (the Mac mini, filtered to it).
EXTRA += [("studio", "ios-app", 5, t, "done", "unread", mins, last) for t, mins, last in [
    ("App Store 截图换成新版界面", 4, "六种尺寸的截图都换好了，深浅色各一套。"),
    ("修复横屏时键盘挡住输入框", 9, "输入框改成跟着键盘高度走，横竖屏都验证过。"),
    ("设置页加上导出数据", 14, "导出成 JSON，放进分享面板，可以存到文件。"),
    ("小组件支持深色图标", 22, "图标跟随系统，主屏幕三种尺寸都看过。"),
    ("引导页文案再精简", 31, "四页减到三页，每页一句话。"),
    ("升级依赖并跑完测试", 47, "依赖都升到最新，312 个测试全部通过。"),
]] + [("studio", "ios-app", 5, "通知分组按会话合并", "working", "working", 2, "正在改通知的 thread id。")]
EXTRA += [(mid, space, sidx, title, "done", "idle", mins, last) for mid, space, sidx, title, mins, last in [
    ("studio", "notes", 6, "笔记导出成 Markdown", 60 * 5, "导出脚本写好了，图片一起拷到 assets。"),
    ("studio", "dotfiles", 7, "终端配色换成浅色主题", 60 * 26, "浅色主题配好了，Vim 和 tmux 一起改了。"),
    ("studio", "dotfiles", 7, "整理 shell 别名", 60 * 27, "删了 12 个用不到的别名。"),
    ("mini", "home-server", 1, "家里的媒体库重新扫描", 60 * 3, "重新扫描完成，补上了 41 张封面。"),
    ("mini", "home-server", 1, "备份脚本加上校验", 60 * 20, "每次备份后对比校验和，不一致会发通知。"),
    ("mini", "scripts", 2, "照片按年份归档", 60 * 30, "按拍摄日期归到年份目录，重复的跳过。"),
    ("mini", "router", 3, "路由器配置备份", 60 * 50, "配置导出到备份盘，保留最近 10 份。"),
]]
sessions = list(m.SESSIONS)
# A standalone release session was added beside the background-task demo.
release = next(s for s in sessions if s["session_id"] == m.RELEASE)
m.ITEMS[m.RELEASE] = [m.it("user", "text", 10, text=release["title"]),
                      m.it("assistant", "text", 3, text=release["last_text"])]
for n, (mid, space, sidx, title, status, state, minutes, last) in enumerate(EXTRA):
    sid = "c7e9f1a3-0000-4000-8000-%012d" % (n + 1)
    slot = 10 + n
    # Tabs as herdr names them when nobody did: a number in their space.
    tab = str(1 + sum(1 for e in EXTRA[:n] if e[0] == mid and e[1] == space))
    s = m.sess(mid, sid, title, "/Users/me/code/" + space if mid != "server" else "/home/me/code/" + space, status, state,
               space, sidx, tab, slot, minutes, last, agent="codex" if n % 3 == 1 else "claude")
    if state == "idle":
        s["seen_at"] = m.ago(minutes=minutes - 1)
    sessions.append(s)
    m.ITEMS[sid] = [m.it("user", "text", minutes + 6, text=title), m.it("assistant", "text", minutes, text=last)]

# The link in the rich reply opens in the app: somewhere that exists.
for x in m.ITEMS[m.RICH]:
    if x["kind"] == "text" and x["role"] == "assistant":
        x["text"] = x["text"].replace("http://192.0.2.10:23140/", "https://example.com/")

# Feishu chats behind the inbox's Feishu items (InboxItem.larkConversation).
def lark(feed, n, sender, text, minutes, mine=False):
    return {"id": "om_%s_%d" % (feed, n), "position": str(n), "sender_id": "u_" + sender, "sender_name": sender,
            "is_mine": mine, "type": "TEXT", "text": text, "recalled": False, "reply_count": 0, "created_at": m.ago(minutes=minutes),
            "parts": [{"kind": "text", "text": text}]}


LARK = {
    "f1": [lark("f1", 98, "我", "候选包周四晚上能出来吗？", 60 * 26, mine=True), lark("f1", 99, "韩梅梅", "应该可以，我明天再确认一下", 60 * 25),
           lark("f1", 100, "韩梅梅", "周五的发布能赶上吗？", 40), lark("f1", 101, "韩梅梅", "下午我要和客户确认时间", 18)],
    "f2": [lark("f2", 199, "李雷", "大家下午 3 点评审新版图标", 120), lark("f2", 200, "Tom", "收到", 100),
           lark("f2", 201, "李雷", "新版图标放在设计稿第 3 页了", 90), lark("f2", 202, "李雷", "@你 下午评审前看一眼？", 55)],
    "f3": [lark("f3", 299, "Tom", "这周六去后山徒步，报名的接龙", 120), lark("f3", 300, "Lucy", "1. Lucy", 110),
           lark("f3", 301, "Tom", "周六早上 8 点地铁站集合，记得带水", 30)],
}

MAIL = {
    "501": {"account_id": "acc1", "mailbox": "INBOX", "uid": 501, "subject": "下周的分享能不能提前到周二？",
            "from": {"name": "Lucy Wang", "email": "lucy@example.com"}, "to": [{"name": "我", "email": "me@example.com"}],
            "date": m.ago(hours=3), "seen": False,
            "text": "Hi，\n\n周四我临时要出差，下周的分享能不能挪到周二下午？时间和会议室我来约。\n\n谢谢！\nLucy"},
    "502": {"account_id": "acc1", "mailbox": "INBOX", "uid": 502, "subject": "你的订单已发货",
            "from": {"name": "示例商城", "email": "shop@example.com"}, "to": [{"email": "me@example.com"}], "date": m.ago(hours=5),
            "seen": False, "text": "订单 2026102800123 已由快递揽收，预计周五送达。"},
    "503": {"account_id": "acc1", "mailbox": "INBOX", "uid": 503, "subject": "本周精选：10 个 SwiftUI 技巧",
            "from": {"name": "Weekly Digest", "email": "news@example.com"}, "to": [{"email": "me@example.com"}], "date": m.ago(hours=8),
            "seen": True, "text": "这周我们整理了 10 个 SwiftUI 的小技巧：从列表性能到动画曲线……"},
}

# Mirror the mock's mail fallback so new inbox fixtures also open in the bundled demo.
for item in m.INBOX:
    mail = item.get("mail")
    if not mail:
        continue
    for uid in mail["uids"]:
        MAIL.setdefault(str(uid), m.MAIL_MESSAGES.get(uid) or {
            "account_id": mail["account_id"], "mailbox": mail.get("mailbox", "INBOX"), "uid": uid,
            "subject": item["title"], "from": mail["from"], "to": [{"name": "Me", "email": "me@example.com"}],
            "date": item["updated_at"], "text": m.MAIL_BODIES.get(uid, item["preview"]), "seen": False,
        })


def kb(kind, id, name, one_line, tags, minutes, extra=None):
    e = {"kind": kind, "id": id, "name": name, "one_line": one_line, "importance": "high", "relevance": 80, "tags": tags,
         "updated": m.ago(minutes=minutes)}
    if extra:
        e["extra"] = extra
    return e


KB = {
    "projects": [kb("projects", "weather", "天气 App", "主屏幕小组件这周上线", ["iOS"], 30, "product"),
                 kb("projects", "blog", "个人博客", "已迁移到 Astro", ["web"], 60 * 12, "product:done")],
    "people": [kb("people", "han-meimei", "韩梅梅", "产品经理，负责周五的发布", ["同事"], 40),
               kb("people", "li-lei", "李雷", "设计师，在做新版图标", ["同事"], 55)],
    "chats": [], "docs": [], "meetings": [], "events": [],
}

def phone_frames(cols=38, rows=30, ticks=40):
    """The made-up Claude screen laid out for a phone's width (mock_hub.phone_screen): the first frame whole, then
    the spinner's line once a tick. mock_hub.terminal_frames is 96 columns: a phone folds every line of it."""
    E = "\x1b["
    out = []
    for n in range(ticks):
        lines, at = m.phone_screen(cols, rows, "", n)
        if n == 0:
            body = E + "2J" + "".join(E + "%d;1H" % (i + 1) + l for i, l in enumerate(lines))
        else:
            body = "".join(E + "%d;1H" % (i + 1) + l + E + "K" for i, l in enumerate(lines) if "Brewing" in l)
        data = E + "?2026h" + E + "?25l" + body + E + "%d;3H" % (at + 1) + E + "?25h" + E + "?2026l"
        out.append({"seq": n + 1, "full": n == 0, "cols": cols, "rows": rows,
                    "data": m.base64.b64encode(data.encode()).decode()})
    return out


# A machine where every agent is idle ("All idle" when the list is filtered to it).
MINI = dict(next(x for x in m.MACHINES if x["id"] == "studio"), id="mini", name="Mac mini", ssh_host="mini", hostname="mini",
            icon="desktop")
MINI["system"] = dict(MINI["system"], model="Mac mini", cpu="Apple M4", cores=10, memory=32 * m.GB)
machines = m.MACHINES + [MINI]
# Demonstrate a machine already using the desktop runtime as well as the default CLI.
MINI["engine"] = "herdr-core"
MINI["engines"] = [{"id": "herdr", "name": "herdr", "available": False},
                   {"id": "herdr-core", "name": "Herdr App", "available": True},
                   {"id": "herdr-both", "name": "herdr + Herdr App", "available": False},
                   {"id": "orca", "name": "Orca", "available": True}]

data = {
    "machines": machines,
    "sessions": sessions + m.ASSISTANT,
    "items": m.ITEMS,
    "full": m.FULL,
    "interactions": m.INTERACTIONS,
    "assistant": {"machine_id": "server", "launcher": "claude", "cwd": "/home/me/.persona/assistant", "timezone": "Asia/Shanghai",
                  "notes_effective": "", "conversations": m.ASSISTANT, "main": m.ASK1},
    "tasks": m.SCHEDULE,
    "todos": m.sorted_todos(),
    "runs": m.RUNS,
    "memory": m.MEMORY,
    "inbox": m.INBOX,
    "mail_accounts": m.MAIL_ACCOUNTS,
    "mail": MAIL,
    "lark": LARK,
    "lark_conversations": m.LARK_CHATS,
    "kb": KB,
    "screen": m.TERMINAL,
    "terminal_frames": phone_frames(),
    "tokens": m.TOKENS,
    "prefs": m.PREFS,
    "pins": {"spaces": ["macbook/weather"], "sessions": []},
    "launchers": [{"name": "claude", "model": "opus"}, {"name": "codex", "model": "gpt-5.5"}],
    "dirs": m.mock_dirs("macbook", "")[0],
}

with open(OUT, "w") as f:
    json.dump(relative(data), f, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    f.write("\n")
print("wrote %s (%d KB)" % (os.path.relpath(OUT), os.path.getsize(OUT) // 1024))
