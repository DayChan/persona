"""The main conversation's readiness and coded refusals, as the hub answers them (docs/assistant/onboarding.md 4.4–4.6, 5.1–5.4;
docs/protocol/protocol.md "Main chat readiness" "Error body"), for mock_hub.py's MOCK_READINESS scenarios (docs/operations/testing.md "Readiness scenarios").

Nothing here is copied from the server: the words come from the hub's own catalogs (server/internal/i18n/locales) and
the built-in providers from server/internal/assistant/presets_builtin.json, so a change there shows up here. The
shapes follow server/internal/assistant/readiness.go, models_presets.go and apierr/apierr.go; test_readiness_mock.py
pins them. Standard library only, no state of its own: mock_hub.py holds the scenario and the machines.
"""
import json
import os
import re
from datetime import datetime, timezone

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
LOCALES = os.path.join(ROOT, "server", "internal", "i18n", "locales")
PRESETS_JSON = os.path.join(ROOT, "server", "internal", "assistant", "presets_builtin.json")
LANGS = ("zh-Hans", "en", "ja")
DEFAULT_LANG = "zh-Hans"  # i18n.Default: the hub's words were Chinese before i18n

# MOCK_READINESS=<name>: what each scenario is (mock_hub.py sets the machines and the main conversation up from it).
#   machine  none: no computer; pending: one waiting to be confirmed (so none the assistant may use); offline;
#            outdated (a node from before model config); ok
#   claude   missing (Claude Code not installed), launcher_missing (the ~/.zshrc launcher is gone), or the machine's
#            claude_version
#   model    None: never verified (unknown + model_unverified); ok; or the turn error code of the main conversation's
#            only turn (its text in TURN_ERRORS)
#   start    the coded refusal POST /api/v1/assistant/conversations answers (None: it starts)
SCENARIOS = {
    "zero": {"machine": "none", "start": "no_machine"},
    "offline": {"machine": "offline", "start": "node_unreachable"},
    "pending": {"machine": "pending", "start": "machine_pending"},
    "no_claude": {"machine": "ok", "claude": "missing", "start": "claude_missing"},
    "not_logged_in": {"machine": "ok", "model": "not_logged_in"},
    "bad_key": {"machine": "ok", "model": "auth_failed"},
    "full": {"machine": "ok", "model": "ok", "answered": True},
    # full on the mock's usual data (the assistant on server, every page filled), for screenshots of a hub in use.
    "demo": {"machine": "ok", "model": "ok", "answered": True, "demo": True},
    # More of 4.6's codes, for their cards.
    "unverified": {"machine": "ok"},
    "launcher_missing": {"machine": "ok", "claude": "launcher_missing", "launcher": "cc_glm", "start": "launcher_missing"},
    "model_route": {"machine": "ok", "model": "model_route"},
    "rate_limited": {"machine": "ok", "model": "rate_limited"},
    "node_outdated": {"machine": "outdated"},
    "assistant_offline": {"machine": "ok", "assistant_offline": True},
}

# The main conversation's one answer in full.
ANSWER = "你好，我是 Persona。"

# What Claude Code writes when the turn fails, as the hub reads the code off it (assistant/fleet.go turnErrorCode).
TURN_ERRORS = {
    "not_logged_in": "Not logged in · Please run /login",
    "auth_failed": "Invalid API key · Please run /login",
    "model_route": "There's an issue with the selected model (deepseek-v9). It may not exist or you may not have access "
                   "to it. Run /model to pick a different model.",
    "rate_limited": "API Error: 429 Too Many Requests",
}
# turn_error_kind user: what the user must fix (assistant/fleet.go userTurnError); a rate limit passes by itself.
USER_TURN_ERRORS = ("not_logged_in", "auth_failed", "model_route")

_catalogs = {}


def catalog(lang):
    """The hub's catalog for lang: "<file>.<key>" → text (server/internal/i18n)."""
    if lang not in _catalogs:
        out = {}
        d = os.path.join(LOCALES, lang)
        for name in sorted(os.listdir(d)):
            if name.endswith(".json"):
                with open(os.path.join(d, name), encoding="utf-8") as f:
                    for k, v in json.load(f).items():
                        out[name[:-5] + "." + k] = v
        _catalogs[lang] = out
    return _catalogs[lang]


def lang_of(accept_language):
    """i18n.FromAcceptLanguage, then the default: the first entry, any Chinese zh-Hans, Japanese ja, the rest en."""
    first = (accept_language or "").split(",")[0].split(";")[0].strip()
    if not first or first == "*":
        return DEFAULT_LANG
    primary = first.replace("_", "-").split("-")[0].lower()
    return {"zh": "zh-Hans", "ja": "ja"}.get(primary, "en")


def t(lang, key, **args):
    """i18n.T: key in lang, else en, else zh-Hans, else the key; {name} filled from args."""
    for l in (lang, "en", "zh-Hans"):
        s = catalog(l).get(key)
        if s is not None:
            return re.sub(r"\{(\w+)\}", lambda m: str(args[m.group(1)]) if m.group(1) in args else m.group(0), s)
    return key


def now_z():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


# ---------- the readiness (assistant/readiness.go) ----------

def step(lang, sid, state, code, machine_name, launcher, action):
    """readinessStep: a step with its code's words and its action."""
    name = machine_name or t(lang, "assistant-readiness.this_computer")
    st = {"id": sid, "state": state, "code": code}
    if launcher and sid not in ("computer", "first_message"):
        st["launcher"] = launcher
    st["title"] = t(lang, "assistant-readiness.%s_title" % code, machine=name, launcher=launcher)
    st["detail"] = t(lang, "assistant-readiness.%s_detail" % code, machine=name, launcher=launcher)
    if action:
        st["action"] = {"id": action}
    return st


def suggestions(lang, mode):
    """readinessSuggestions: only the local two while something is missing."""
    s = lambda key: t(lang, "assistant-readiness.suggest_" + key)
    out = [{"id": "what_can_you_do", "title": s("what_can_you_do"), "local": True}]
    if mode != "full":
        return out + [{"id": "how_to_connect_model", "title": s("how_to_connect_model"), "local": True}]
    return out + [{"id": "largest_folders", "title": s("largest_folders"), "prompt": s("largest_folders_prompt")},
                  {"id": "today", "title": s("today"), "prompt": s("today_prompt"), "needs": ["phone_calendar"]},
                  {"id": "connect_mail", "title": s("connect_mail"), "prompt": s("connect_mail_prompt")}]


def blocks(st):
    """readinessBlocks: todo, or a problem other than a rate limit."""
    return st["state"] == "todo" or (st["state"] == "problem" and st.get("code") != "rate_limited")


def readiness(sc, machine, lang, launcher="claude", dismissed=False, probe=None):
    """GET /api/v1/assistant/readiness for scenario sc on machine (the main conversation's: a dict with id, name,
    online, pending, claude_version, node_outdated; None when there is none). probe: the one-word check's result, already run."""
    out = {"version": 1, "mode": "full", "ready": True, "next": None, "machine": None, "launcher": launcher}
    if machine is None:
        computer = step(lang, "computer", "todo", "no_machine", "", launcher, "add_computer")
    elif machine.get("pending"):
        computer = step(lang, "computer", "todo", "machine_pending", machine["name"], launcher, "confirm_computer")
    elif not machine.get("online"):
        computer = step(lang, "computer", "problem", "node_unreachable", machine["name"], launcher, "wake_computer")
    elif machine.get("node_outdated"):
        computer = step(lang, "computer", "problem", "node_outdated", machine["name"], launcher, "")
    else:
        computer = {"id": "computer", "state": "ok"}
    if machine is not None:
        out["machine"] = {"id": machine["id"], "name": machine["name"]}
        computer = dict(computer, machine=machine["id"])
        computer = {k: computer[k] for k in ("id", "state", "code", "machine", "title", "detail", "action") if k in computer}
    steps = [computer]
    if computer["state"] != "ok":
        steps += [{"id": sid, "state": "unknown"} for sid in ("claude", "model", "first_message")]
    else:
        name, mid = machine["name"], machine["id"]
        model_code = sc.get("model")
        if model_code == "ok":
            model = {"id": "model", "state": "ok", "machine": mid, "launcher": launcher}
        elif model_code:
            action, alt = {"not_logged_in": ("connect_model", "login_claude"),
                           "auth_failed": ("edit_provider", "test_assistant"),
                           "model_route": ("connect_model", "test_assistant")}.get(
                model_code, ("connect_model", ""))
            model = with_machine(step(lang, "model", "problem", model_code, name, launcher, action), mid)
            if alt:
                model["alt_actions"] = [{"id": alt}]
        else:
            model = with_machine(step(lang, "model", "unknown", "model_unverified", name, launcher, "test_assistant"), mid)
        model_ok = model["state"] == "ok"
        claude = sc.get("claude")
        version = machine.get("claude_version") or ""
        if claude == "missing" and not model_ok:
            st = with_machine(step(lang, "claude", "problem", "claude_missing", name, launcher, "install_claude"), mid)
            st["alt_actions"] = [{"id": "connect_model"}]
        elif claude == "launcher_missing" and not model_ok:
            st = with_machine(step(lang, "claude", "problem", "launcher_missing", name, launcher, "connect_model"), mid)
        elif version or model_ok:
            st = {"id": "claude", "state": "ok"}
            if version:
                st["version"] = version
        else:
            st = {"id": "claude", "state": "unknown"}  # an old node with no self-check
        if sc.get("answered"):
            first = {"id": "first_message", "state": "ok"}
        elif model_code and model_code != "ok":
            first = step(lang, "first_message", "problem", model_code, name, "", "test_assistant")
        else:
            first = step(lang, "first_message", "todo", "never_tried", name, "", "test_assistant")
        # one button for the try, on the first message's step where the try shows how it went (#6317)
        if (model.get("action") or {}).get("id") == "test_assistant" and (first.get("action") or {}).get("id") == "test_assistant":
            del model["action"]
        steps += [st, model, first]
    for st in steps:
        if st["id"] != "first_message" and blocks(st):
            out["mode"] = "setup"
        if out["next"] is None and st["state"] not in ("ok", "skipped"):
            out["next"] = st["id"]
    out["ready"] = out["mode"] == "full"
    out["steps"] = steps
    out["suggestions"] = suggestions(lang, out["mode"])
    if probe is not None and computer["state"] == "ok":
        out["probe"] = probe
    out["dismissed"] = dismissed
    out["checked_at"] = now_z()
    return out


def with_machine(st, mid):
    """The step with machine after code, where readiness.go's struct puts it."""
    out = {}
    for k, v in st.items():
        out[k] = v
        if k == "code":
            out["machine"] = mid
    return out


def probe_result(sc, launcher="claude"):
    """POST …/launchers/{name}/check on the main conversation's machine, as the scenario's model would answer it."""
    model = sc.get("model")
    if sc.get("claude") == "missing":
        return {"ok": False, "agent": "claude", "millis": 40, "error": "zsh:1: command not found: claude"}
    if sc.get("claude") == "launcher_missing":
        return {"ok": False, "millis": 40, "error": "zsh:1: command not found: " + launcher}
    if model and model != "ok":
        return {"ok": False, "agent": "claude", "millis": 2140, "error": TURN_ERRORS[model]}
    return {"ok": True, "agent": "claude", "model": "claude-sonnet-5-5", "answer": "好", "millis": 1830}


# ---------- the coded refusals (apierr.Call, docs/protocol/protocol.md "Error body") ----------

START_DETAILS = {
    "no_machine": "assistant machine is not configured",
    "machine_pending": "this computer is waiting to be confirmed in the Persona app",
}
CODE_ACTIONS = {"no_machine": "add_computer", "machine_pending": "confirm_computer", "claude_missing": "install_claude",
                "launcher_missing": "connect_model"}
START_CODES = tuple(CODE_ACTIONS) + ("node_unreachable", "node_outdated", "assistant_offline")
# What devices/machine.go call() refuses assistant.send / session.start with on a node from before model config once a model
# is configured (apierr.Call: 409, the node's words, no action).
NODE_OUTDATED_ERROR = "这台电脑的 node 还不支持统一模型配置，请更新 node"


def refusal(code, lang, machine_name="", launcher="claude"):
    """(status, body) of a start refused with code: the four coded ones 409 with the hub's sentence, the shell's words
    in detail and what to do; node_unreachable 503 as an offline machine's call; node_outdated 409 as an old node's
    call once a model is configured; assistant_offline 503."""
    if code == "assistant_offline":
        return 503, {"error": t(lang, "hub-assistant_link.offline"), "code": "assistant_offline"}
    if code == "node_outdated":
        return 409, {"error": NODE_OUTDATED_ERROR, "code": "node_outdated"}
    if code == "node_unreachable":
        return 503, {"error": "%s: machine is offline" % (machine_name or "mbp"), "code": "node_unreachable",
                     "retryable": True, "delivered": "none"}
    body = {"code": code, "delivered": "none"}
    if code in ("claude_missing", "launcher_missing"):
        cmd = "claude" if code == "claude_missing" else launcher
        detail = "%s 没有启动起来（1.2s 后退出），消息没有送达：zsh:1: command not found: %s" % (launcher, cmd)
        body["launcher"] = launcher
    else:
        detail = START_DETAILS[code]
    if machine_name and code != "no_machine":
        body["machine_name"] = machine_name
    key = "apierr-apierr." + code
    if not machine_name and key + "_here" in catalog("zh-Hans"):
        key += "_here"
    body["error"] = t(lang, key, machine=machine_name, launcher=launcher)
    body["detail"] = detail
    body["action"] = CODE_ACTIONS[code]
    return 409, body


def pending_agent_refusal(lang):
    """POST /api/v1/sessions on a computer still waiting to be confirmed (hub/http.go)."""
    return 409, {"error": t(lang, "hub-machine_trust.start_pending"), "code": "machine_pending"}


# ---------- the built-in providers (assistant/models_presets.go) ----------

def builtin_presets(lang):
    """builtinLauncherPresets: plugin builtin, the names and descriptions in lang, the ones that open from where lang
    is spoken first (Chinese: the vendors in China), each group in the file's order."""
    with open(PRESETS_JSON, encoding="utf-8") as f:
        raw = json.load(f)
    head, tail = [], []
    for b in raw:
        region = b.get("region", "")
        slug = b["id"].split("/", 1)[-1].replace("-", "_")
        pr = {"id": b["id"], "plugin": "builtin", "name": b["name"]}
        desc = t(lang, "assistant-models_presets.desc_" + slug)
        if desc:
            pr["description"] = desc
        for k in ("kind", "api", "base_url", "model", "small_model", "models", "key_hint", "auth", "signup_url"):
            if k in b and (b[k] or k in ("kind", "base_url")):
                pr[k] = b[k]
        pr["display_name"] = t(lang, "assistant-models_presets.name_" + slug)
        first = region != "global" if lang == "zh-Hans" else region != "cn"
        (head if first else tail).append(pr)
    return head + tail


def region_blocked(lang):
    """models_probe.go failure for Anthropic's region block (#4482): the text naming the built-in vendors that are not
    global-only, and the code."""
    with open(PRESETS_JSON, encoding="utf-8") as f:
        regions = {b["id"]: b.get("region", "") for b in json.load(f)}
    names = [pr["display_name"] for pr in builtin_presets(lang) if regions.get(pr["id"]) != "global"]
    vendors = t(lang, "assistant-models_probe.list_sep").join(names)
    return t(lang, "assistant-models_probe.region_blocked", vendors=vendors), "region_blocked"


def merge_presets(builtin, plugins):
    """mergePresets: the plugins' after the built-in ones; one with a built-in id takes its place."""
    out = list(builtin)
    for pr in plugins:
        i = next((n for n, b in enumerate(out) if b["id"] == pr["id"]), None)
        if i is None:
            out.append(pr)
        else:
            out[i] = pr
    return out
