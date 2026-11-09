#!/usr/bin/env python3
"""Export the iOS phone tools (apple/Persona/PhoneTools/*.swift) for the Android alignment test.

The hub merges phone tools of the same name from every phone (server/internal/hub/hub.go mcpTools) and may send a
call to either phone, so Android has to declare each tool exactly as iOS does: name, description, parameters (type,
description, minimum, maximum) and required ones. This reads the Swift sources and writes what Android's
PhoneToolCatalogTest compares against.

    python3 scripts/assets/export_ios_phone_tools.py           # rewrite the fixture
    python3 scripts/assets/export_ios_phone_tools.py --check   # exit 1 if the fixture is out of date

Tools of iOS-only sources (reminders and the iPhone system integrations: alarms, messages, calls, shortcuts, media,
home), write-only health tools (phone_health_write_sample) and the iPhone-only calendar, contacts and maps tools of
#2276 / #2281 / #2286 (Android has none yet) are left out.
"""
import argparse
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SOURCES = os.path.join(ROOT, "apple", "Persona", "PhoneTools")
FIXTURE = os.path.join(ROOT, "android", "platform", "phonetools", "src", "test", "resources", "ios-phone-tools.json")
IOS_ONLY = {"reminders", "alarms", "messages", "calls", "shortcuts", "media", "home"}
IOS_ONLY_TOOLS = {
    "phone_health_write_sample",
    "phone_calendar_update_event", "phone_calendar_delete_event",
    "phone_contacts_create", "phone_contacts_update",
    "phone_maps_search_nearby", "phone_maps_navigate",
    # Android has no place search yet (the fixture never had it; --check failed on main before #2281).
    "phone_location_search",
}


def matching(text, start):
    """Index just past the bracket that closes the one at text[start], skipping string literals."""
    pairs = {"(": ")", "[": "]"}
    stack = [pairs[text[start]]]
    i = start + 1
    while i < len(text):
        c = text[i]
        if text.startswith('"""', i):
            i = text.index('"""', i + 3) + 3
            continue
        if c == '"':
            i += 1
            while text[i] != '"':
                i += 2 if text[i] == "\\" else 1
            i += 1
            continue
        if c in pairs:
            stack.append(pairs[c])
        elif c in ")]":
            if c != stack.pop():
                raise ValueError("unbalanced brackets near %r" % text[start:start + 80])
            if not stack:
                return i + 1
        i += 1
    raise ValueError("unclosed bracket near %r" % text[start:start + 80])


def swift_string(literal):
    """The value of a one-line Swift string literal (without its quotes)."""
    return re.sub(r'\\(.)', lambda m: {"n": "\n", "t": "\t"}.get(m.group(1), m.group(1)), literal)


def multiline(body):
    """A Swift multi-line string: indentation of the closing delimiter removed, a trailing backslash joins lines."""
    lines = body.split("\n")
    indent = len(lines[-1]) - len(lines[-1].lstrip(" "))
    out = ""
    for line in lines[1:-1]:
        line = line[indent:]
        if line.endswith("\\"):
            out += line[:-1]
        else:
            out += line + "\n"
    return swift_string(out[:-1] if out.endswith("\n") else out)


def schema_property(call):
    if call.startswith("Schema.array("):
        m = re.match(r'^Schema\.array\(\s*("""[\s\S]*?"""|"(?:[^"\\]|\\.)*")\s*,\s*of:\s*(.*)\)$', call, re.S)
        if not m:
            raise ValueError("unsupported array schema: %s" % call)
        desc_raw = m.group(1)
        desc = multiline(desc_raw[3:-3]) if desc_raw.startswith('"""') else swift_string(desc_raw[1:-1])
        item_call = m.group(2).strip()
        return {"type": "array", "description": desc, "items": schema_property(item_call)}
    m = re.fullmatch(r'Schema\.(string|integer|boolean)\(\s*"((?:[^"\\]|\\.)*)"\s*(.*)\)', call, re.S)
    if not m:
        raise ValueError("unsupported schema: %s" % call)
    kind, description, rest = m.group(1), swift_string(m.group(2)), m.group(3)
    prop = {"type": kind, "description": description}
    for key in ("min", "max"):
        bound = re.search(r'\b%s:\s*(-?[\d_]+)' % key, rest)
        if bound:
            prop["minimum" if key == "min" else "maximum"] = int(bound.group(1).replace("_", ""))
    val_m = re.search(r'\bvalues:\s*(\w+)', rest)
    if val_m:
        val_name = val_m.group(1)
        if val_name == "metricNames":
            prop["enum"] = ["steps", "distance", "active_energy", "heart_rate", "resting_heart_rate", "sleep", "weight"]
        else:
            raise ValueError("enum values are not exported yet: %s" % call)
    return prop


def schema_object(call):
    """Schema.object([...], required: [...]) -> (properties, required)."""
    inner = call[call.index("(") + 1:-1].strip()
    properties, required = {}, []
    if inner.startswith("["):
        end = matching(inner, 0)
        entries, rest = inner[1:end - 1], inner[end:]
        if entries.strip() != ":":
            for m in re.finditer(r'"(\w+)"\s*:\s*Schema\.', entries):
                start = m.end() - len("Schema.")
                open_paren = entries.index("(", start)
                properties[m.group(1)] = schema_property(entries[start:matching(entries, open_paren)])
    else:
        rest = inner
    req = re.search(r'required:\s*\[([^\]]*)\]', rest)
    if req:
        required = re.findall(r'"(\w+)"', req.group(1))
    return properties, required


def tools():
    out = {}
    for name in sorted(os.listdir(SOURCES)):
        if not name.endswith(".swift"):
            continue
        text = open(os.path.join(SOURCES, name), encoding="utf-8").read()
        for m in re.finditer(r'PhoneTool\(\s*spec:\s*ToolSpec\(', text):
            spec_open = m.end() - 1
            spec = text[spec_open:matching(text, spec_open)]
            tool = re.search(r'name:\s*"([^"]+)"', spec).group(1)
            source = re.match(r'\s*,\s*source:\s*\.(\w+)', text[spec_open + len(spec):]).group(1)
            if source in IOS_ONLY or tool in IOS_ONLY_TOOLS:
                continue
            desc = re.search(r'description:\s*("""\n.*?\n[ ]*""")', spec, re.S)
            description = multiline(desc.group(1)[3:-3]) if desc else swift_string(
                re.search(r'description:\s*"((?:[^"\\]|\\.)*)"', spec).group(1))
            schema_at = spec.index("Schema.object(")
            schema = spec[schema_at:matching(spec, spec.index("(", schema_at))]
            properties, required = schema_object(schema)
            if tool in out:
                raise ValueError("duplicate tool %s" % tool)
            out[tool] = {"source": source, "description": description, "properties": properties, "required": required}
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--check", action="store_true", help="only compare with the fixture")
    args = parser.parse_args()
    text = json.dumps(tools(), ensure_ascii=False, indent=1, sort_keys=True) + "\n"
    if args.check:
        current = open(FIXTURE, encoding="utf-8").read() if os.path.exists(FIXTURE) else ""
        if current != text:
            print("ios-phone-tools.json 已过期：运行 python3 scripts/assets/export_ios_phone_tools.py", file=sys.stderr)
            return 1
        print("ios-phone-tools.json 与 iOS 源码一致")
        return 0
    os.makedirs(os.path.dirname(FIXTURE), exist_ok=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        f.write(text)
    print("wrote %s (%d tools)" % (os.path.relpath(FIXTURE, ROOT), text.count('"source"')))
    return 0


if __name__ == "__main__":
    sys.exit(main())
