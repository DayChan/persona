#!/usr/bin/env python3
"""Localization checks for the Apple apps (docs/apple/ios.md "UI languages", docs/protocol/i18n.md).

  apple_l10n.py check                 English-source catalogs have translated en / zh-Hans / ja units, compatible
                                      numbered format arguments, and no Chinese or Japanese keys; Chinese source
                                      literals require `// i18n-ignore`, explicit localized literals require their
                                      own table's key, BeautifulUI's LocalizedStringKey literals have app-catalog keys
                                      (the library looks them up in the main bundle, #5574), InfoPlist catalogs'
                                      en matches Info.plist, and Persona Link's own catalog (`link_problems`)
                                      (fast, no build; run by scripts/test/test_apple_l10n.py)
  apple_l10n.py strict <DerivedData>  after a build with SWIFT_EMIT_LOC_STRINGS=YES: every Chinese literal the
                                      compiler did not extract as localizable is marked `// i18n-ignore`, and no
                                      extracted key differs from a catalog key only in its format specifiers
                                      (an interpolated String where the catalog expects an Int, #5544)
  apple_l10n.py sync <DerivedData>    adds the strings the compiler extracted to the catalogs (xcstringstool sync),
                                      the step Xcode's IDE does on build and xcodebuild does not
  apple_l10n.py merge <ours> <theirs> <out>
                                      merges two versions of a catalog by key (a rebase conflict in the JSON: git's
                                      text merge breaks it): every key of both; where both have it, the complete one

Markers: `// i18n-ignore` at the end of the literal's line or alone on the line above it; `// i18n-ignore-file:
<reason>` in a file's first 20 lines. For text that is not UI: prompts and messages for the hub / agents, protocol
values, matching hub or terminal text, logs, previews, DEBUG-only pages.
"""

import glob
import json
import os
import plistlib
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import swift_literals  # noqa: E402

ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))
APPLE = os.path.join(ROOT, "apple")
LANGS = ["en", "zh-Hans", "ja"]
SOURCE = "en"
# The app targets' sources and PersonaKit's, whose text is looked up in the app's main bundle (#5575, #5758); not the
# tests, the test apps or the other SwiftPM packages (BeautifulUI's keys: `beautifului_problems`).
SOURCE_DIRS = ["Persona", "Shared", "Mac", "Vision", "PersonaWatch", "PersonaWidgets", "LiveActivity", "PersonaNotify", "PersonaShare",
               "Packages/PersonaKit/Sources"]
CATALOGS = [
    "Shared/Localization/Localizable.xcstrings",
    "Persona/Intents/AppShortcuts.xcstrings",
    "Persona/Resources/InfoPlist.xcstrings",
    "Mac/Resources/InfoPlist.xcstrings",
    "Vision/Resources/InfoPlist.xcstrings",
    "PersonaWatch/InfoPlist.xcstrings",
]
# Persona Link (apple/PersonaLink, docs/operations/link.md "Menu bar App") is its own SwiftPM package: its text is looked
# up in its own catalog through `linkText`, so its keys are checked apart and never satisfy the apps' source keys. Its
# older UI text is hard-coded Chinese; LINK_CJK is each file's count of such literals, which may only go down: new text
# goes through the catalog. Keys are paths under LINK_SOURCES.
LINK_SOURCES = "PersonaLink/Sources/PersonaLink"
LINK_CATALOG = LINK_SOURCES + "/Resources/Localizable.xcstrings"
LINK_CJK = {"LinkModel.swift": 28, "MenuView.swift": 48, "PairLink.swift": 2, "PairView.swift": 36, "PersonaLinkApp.swift": 2,
            "RenameView.swift": 5, "Snapshots.swift": 28}
# The targets whose extracted strings go into the catalogs (sync / strict).
TARGETS = ["Persona", "PersonaMac", "PersonaVision", "PersonaWatch", "PersonaWidgets", "PersonaNotify", "PersonaShare"]

SPEC = re.compile(r"%(?:(\d+)\$)?(#@\w+@|[-+ 0#]*\d*(?:\.\d+)?(?:ll|hh|l|h|z|t|j|q|L)?[@dDiuUxXoOfeEgGcCsSpaA])")
CJK = swift_literals.CJK


def _specifier_type(spec):
    """Compare argument types, not display flags; integer widths and signedness remain significant."""
    match = re.fullmatch(r"[-+ 0#]*\d*(?:\.\d+)?(ll|hh|l|h|z|t|j|q|L)?([@dDiuUxXoOfeEgGcCsSpaA])", spec)
    if not match:
        raise ValueError(f"invalid formatSpecifier {spec!r}")
    length, conversion = match.groups()
    length = length or ""
    if conversion in "di":
        return ("" if length in ("h", "hh") else length) + "d"
    if conversion in "uoxX":
        return ("" if length in ("h", "hh") else length) + "u"
    if conversion in "fFeEgGaA":
        return "Lf" if length == "L" else "f"
    return length + conversion


def format_specs(text, substitutions=None, argument=None):
    """Return ({argument number: type}, errors), including catalog substitutions and `%arg`.

    Repeated references are legal, but conflicting types or mixed positional/sequential arguments are not.
    `argument` is the enclosing substitution's (argNum, formatSpecifier) pair.
    """
    substitutions = substitutions or {}
    out, errors, sequential, positional, n = {}, [], False, False, 0

    def add(idx, spec):
        if not isinstance(idx, int) or isinstance(idx, bool) or idx < 1:
            errors.append(f"invalid argument number {idx!r}")
            return
        try:
            typ = _specifier_type(spec)
        except (ValueError, TypeError) as exc:
            errors.append(str(exc))
            return
        if idx in out and out[idx] != typ:
            errors.append(f"argument {idx} has conflicting types {out[idx]} / {typ}")
        out[idx] = typ

    i = 0
    while i < len(text):
        if text.startswith("%%", i):
            i += 2
            continue
        if text.startswith("%arg", i):
            if argument is None:
                errors.append("%arg outside a substitution")
            else:
                add(*argument)
            i += 4
            continue
        match = SPEC.match(text, i)
        if not match:
            if text[i] == "%" and re.match(r"%(?:\d+\$|[-+ 0#]*\*)", text[i:]):
                errors.append(f"unsupported or malformed format argument near {text[i:i + 16]!r}")
            i += 1
            continue
        idx, spec = match.groups()
        if spec.startswith("#@"):
            name = spec[2:-1]
            sub = substitutions.get(name)
            if not isinstance(sub, dict):
                errors.append(f"unknown substitution {name!r}")
            else:
                number = sub.get("argNum")
                if idx is not None and int(idx) != number:
                    errors.append(f"substitution {name!r} argument {idx} ≠ argNum {number}")
                add(number, sub.get("formatSpecifier"))
        elif idx is not None:
            positional = True
            add(int(idx), spec)
        else:
            sequential = True
            n += 1
            add(n, spec)
        i = match.end()
    if positional and sequential:
        errors.append("mixed positional and sequential arguments")
    return out, errors


def specs(text):
    """The format arguments a value uses, excluding unresolved substitution references."""
    return format_specs(text)[0]


def values(unit_holder):
    """Every string value of one localization: plain, plural / device variations, substitutions."""
    out = []
    if "stringUnit" in unit_holder:
        out.append(unit_holder["stringUnit"])
    for kind in unit_holder.get("variations", {}).values():
        for v in kind.values():
            out.extend(values(v))
    for sub in unit_holder.get("substitutions", {}).values():
        out.extend(values(sub))
    return out


def _units(holder, report, path="", substitutions=None, argument=None, one=False):
    """Yield units with their path and format context, rejecting incomplete variation/substitution trees."""
    if not isinstance(holder, dict):
        report(f"{path or 'value'}: invalid localization")
        return
    substitutions = {**(substitutions or {}), **holder.get("substitutions", {})}
    if "stringUnit" in holder:
        unit = holder["stringUnit"]
        if not isinstance(unit, dict):
            report(f"{path or 'stringUnit'}: invalid stringUnit")
        else:
            yield unit, path, substitutions, argument, one
    variations = holder.get("variations", {})
    for kind, forms in variations.items():
        if not forms or (kind == "plural" and "other" not in forms):
            report(f"{path}/{kind}: needs {'other' if kind == 'plural' else 'a variation'}")
        for form, branch in forms.items():
            yield from _units(branch, report, f"{path}/{kind}/{form}", substitutions, argument,
                              one or (kind == "plural" and form == "one"))
    for name, sub in holder.get("substitutions", {}).items():
        pair = (sub.get("argNum"), sub.get("formatSpecifier"))
        _, errors = format_specs("%arg", argument=pair)
        for error in errors:
            report(f"{path}/substitutions/{name}: {error}")
        yield from _units(sub, report, f"{path}/substitutions/{name}", substitutions, pair)
    if "stringUnit" not in holder and not variations:
        report(f"{path or 'value'}: no stringUnit or variations")


def _count_argument(want):
    numeric = [idx for idx, typ in want.items() if typ.endswith(("d", "u"))]
    return numeric[0] if len(numeric) == 1 else None


def _matches_arguments(got, want, one=False, count=None):
    """A plural `one` may omit only the counted argument, never another argument or the entire schema."""
    if got == want:
        return True
    return one and count in want and got == {idx: typ for idx, typ in want.items() if idx != count}


def catalog_problems(data, rel="catalog", infoplist=False):
    """Validate an in-memory catalog. Scoped keys may have the same visible English value, not aliases."""
    problems = []
    if data.get("sourceLanguage") != SOURCE:
        problems.append(f"{rel}: sourceLanguage must be {SOURCE}")
    for key, entry in data.get("strings", {}).items():
        if CJK.search(key):
            problems.append(f"{rel}: {key!r}: Chinese/Japanese catalog key")
        locs, by_lang = entry.get("localizations", {}), {}
        for lang, locale in locs.items():
            if isinstance(locale, dict) and "localizations" in locale:
                problems.append(f"{rel}: {key!r}: {lang!r} contains a nested string entry, not a locale")
        for lang in LANGS:
            report = lambda message, lang=lang: problems.append(f"{rel}: {key!r}: {lang} {message}")
            if lang not in locs:
                report("is missing")
                continue
            units = list(_units(locs[lang], report))
            by_lang[lang] = units
            for unit, path, _, _, _ in units:
                if unit.get("state") != "translated":
                    report(f"{path or 'stringUnit'} is {unit.get('state')!r}, needs translated")
                text = unit.get("value")
                # The catalog's empty key is intentional; it still needs an explicit translated unit.
                if not isinstance(text, str) or (not text and key != ""):
                    report(f"{path or 'stringUnit'} has no value")
        en = by_lang.get(SOURCE, [])
        root_units = [u for u in en if u[3] is None and not u[4] and isinstance(u[0].get("value"), str)]
        if not root_units:
            continue
        reference = root_units[0]
        want, _ = format_specs(reference[0]["value"], reference[2])
        if sorted(want) != list(range(1, len(want) + 1)):
            problems.append(f"{rel}: {key!r}: en argument numbers must be contiguous from 1: {want}")
        # Keys with format arguments must describe the same call-site arguments as the visible English value.
        key_want, key_errors = format_specs(key, reference[2])
        if key_want and key_want != want:
            problems.append(f"{rel}: {key!r}: en placeholders {want} ≠ key {key_want}")
        for error in key_errors:
            problems.append(f"{rel}: {key!r}: key {error}")
        if infoplist and (not reference[0]["value"] or reference[0]["value"] == key):
            problems.append(f"{rel}: {key}: needs an en value, not the InfoPlist key")
        for lang, units in by_lang.items():
            for unit, path, subs, argument, one in units:
                text = unit.get("value")
                if not isinstance(text, str):
                    continue
                got, unit_errors = format_specs(text, subs, argument)
                for error in unit_errors:
                    problems.append(f"{rel}: {key!r}: {lang} {path or 'stringUnit'}: {error}")
                expected = want if argument is None else format_specs("%arg", argument=argument)[0]
                count = _count_argument(expected) if argument is None else argument[0]
                if not _matches_arguments(got, expected, one, count):
                    problems.append(f"{rel}: {key!r}: {lang} {path or 'stringUnit'} placeholders {got} ≠ {expected}: {text!r}")
                if argument is not None:
                    number, _ = format_specs("%arg", argument=argument)
                    for idx, typ in number.items():
                        if want.get(idx) != typ:
                            problems.append(f"{rel}: {key!r}: {lang} {path}: substitution argument {idx}:{typ} ≠ en {want}")
    return problems


def check_catalog(path, problems):
    duplicates = []

    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                duplicates.append(key)
            result[key] = value
        return result

    with open(path, encoding="utf-8") as f:
        data = json.load(f, object_pairs_hook=unique_object)
    rel = os.path.relpath(path, ROOT)
    for key in duplicates:
        problems.append(f"{rel}: duplicate JSON key {key!r} (scope colliding localization keys)")
    problems.extend(catalog_problems(data, rel, path.endswith("InfoPlist.xcstrings")))
    return data


def infoplist_drift(rel, catalog, plist):
    """Where an InfoPlist catalog's en value is not its target's Info.plist value (#4790). The catalog is what the
    app shows, in every language; Info.plist is the English the catalog is translated from, and the two drifted
    apart once (#3911 changed Info.plist only). Every localizable Info.plist key needs a catalog entry too."""
    problems = []
    strings = catalog.get("strings", {})
    for key, value in sorted(plist.items()):
        if not key.endswith("UsageDescription") or not isinstance(value, str):
            continue
        if key not in strings:
            problems.append(f"{rel}: {key}: in Info.plist but not in the catalog")
            continue
        units = values(strings[key].get("localizations", {}).get("en", {}))
        en = units[0].get("value") if units else None
        if en is not None and en != value:
            problems.append(f"{rel}: {key}: en ≠ Info.plist (change both, then ja and zh-Hans):\n"
                            f"    en:         {en!r}\n    Info.plist: {value!r}")
    return problems


def normalize(text, literal=False, raw_hashes=0, formats=True):
    """Normalize interpolation/format arguments without losing a scoped key's English text.

    Swift escapes are decoded only for source literals. The existing lexer skips strings/comments inside an
    interpolation, so a quoted ')' cannot prematurely end it. Raw strings interpolate only with matching hashes.
    """
    if literal and text.startswith("\n"):
        lines = text.split("\n")[1:]
        indent = len(lines[-1]) if lines and not lines[-1].strip() else 0
        lines = [line[indent:] for line in lines[:-1]] if indent or (lines and not lines[-1].strip()) else lines
        text = "\n".join(lines)
    escape = "\\" + "#" * raw_hashes
    out, i = [], 0
    while i < len(text):
        if literal and text.startswith(escape, i):
            j = i + len(escape)
            if text.startswith("(", j):
                out.append("\x00")
                i = swift_literals._scan(text, j + 1, len(text), [], stop_paren=True)
                continue
            unicode = re.match(r"u\{([0-9a-fA-F]+)\}", text[j:])
            if unicode:
                out.append(chr(int(unicode[1], 16)))
                i = j + unicode.end()
                continue
            if j < len(text) and text[j] in 'ntr0\\"\n':
                out.append({"n": "\n", "t": "\t", "r": "\r", "0": "\x00", "\n": ""}.get(text[j], text[j]))
                i = j + 1
                continue
        out.append(text[i])
        i += 1
    text = "".join(out)
    # A Swift literal's percent signs are text; only catalog keys contain printf placeholders and %%.
    if literal or not formats:
        return text
    text = re.sub(r"\$\{\w+\}", "\x00", text)
    # Protect %% before matching %d; %%d is a literal '%d', not an argument.
    text = text.replace("%%", "\x01")
    return SPEC.sub("\x00", text).replace("\x01", "%")


def ignored_lines(src):
    lines = src.split("\n")
    if any("i18n-ignore-file" in l for l in lines[:20]):
        return None
    marked = set()
    for i, l in enumerate(lines, 1):
        if "// i18n-ignore" in l:
            stripped = l.strip()
            marked.add(i + 1 if stripped.startswith("// i18n-ignore") else i)
    return marked


def preview_lines(src):
    """Lines inside `#Preview { … }` blocks and `#if DEBUG` … `#endif` regions are not UI that ships."""
    lines, out, stack = src.split("\n"), set(), []
    for i, l in enumerate(lines, 1):
        s = l.strip()
        if s.startswith("#if"):
            stack.append("DEBUG" in s and "!" not in s)
        elif s.startswith("#else") and stack:
            stack[-1] = False
        elif s.startswith("#endif") and stack:
            stack.pop()
        if any(stack):
            out.add(i)
    depth, opened = None, False
    for i, l in enumerate(lines, 1):
        if depth is None and l.lstrip().startswith("#Preview"):
            depth, opened = 0, False
        if depth is not None:
            out.add(i)
            depth += l.count("{") - l.count("}")
            opened = opened or "{" in l
            if opened and depth <= 0:
                depth = None
    return out


def source_files():
    for d in SOURCE_DIRS:
        yield from sorted(glob.glob(os.path.join(APPLE, d, "**", "*.swift"), recursive=True))


def cjk_literals(path):
    with open(path, encoding="utf-8") as f:
        src = f.read()
    marked = ignored_lines(src)
    if marked is None:
        return []
    skip = preview_lines(src)
    out = []
    lines = src.split("\n")
    for lit in swift_literals.literals(src):
        if not lit.has_cjk or lit.line in marked or lit.line in skip:
            continue
        before = lines[lit.line - 1][: lit.col - 1].rstrip()
        if before.endswith("comment:"):  # a translator's note, not text
            continue
        out.append(lit)
    return out


def _swift_tokens(src, start=0, end=None):
    """Lex code (not comments or literal bodies), retaining nested interpolation code as separate streams."""
    end = len(src) if end is None else end
    word_pattern = re.compile(r"[A-Za-z_][A-Za-z_0-9]*")
    tokens, nested, i = [], [], start
    while i < end:
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = end if j < 0 else j
            continue
        if src.startswith("/*", i):
            depth, i = 1, i + 2
            while i < end and depth:
                if src.startswith("/*", i):
                    depth, i = depth + 1, i + 2
                elif src.startswith("*/", i):
                    depth, i = depth - 1, i + 2
                else:
                    i += 1
            continue
        quote = i
        while quote < end and src[quote] == "#":
            quote += 1
        if quote < end and src[quote] == '"':
            found = []
            j = swift_literals._string(src, i, quote, quote - i, end, found)
            if not found:
                i = j
                continue
            lit = found[-1]
            tokens.append(("literal", lit, i, j))
            # Re-scan only the outer body's interpolations; nested strings are parsed by their own stream.
            k = quote + (3 if src.startswith('"""', quote) else 1)
            escape = "\\" + "#" * (quote - i)
            while k < j:
                if src.startswith(escape, k):
                    body = k + len(escape)
                    if src.startswith("(", body):
                        close = swift_literals._scan(src, body + 1, j, [], stop_paren=True)
                        nested.extend(_swift_tokens(src, body + 1, close - 1))
                        k = close
                    else:
                        k = body + 1
                else:
                    k += 1
            i = j
            continue
        word = word_pattern.match(src, i, end)
        if word:
            tokens.append(("code", word[0], i, word.end()))
            i = word.end()
        else:
            if not src[i].isspace():
                tokens.append(("code", src[i], i, i + 1))
            i += 1
    return [tokens, *nested]


def _arguments(tokens, opening):
    """Split a call's top-level arguments without mistaking nested calls/closures for labels."""
    args, current, stack = [], [], [")"]
    closers = {"(": ")", "[": "]", "{": "}"}
    for token in tokens[opening + 1:]:
        text = token[1] if token[0] == "code" else None
        if text in closers:
            stack.append(closers[text])
        elif text == stack[-1]:
            stack.pop()
            if not stack:
                args.append(current)
                return args
        if text == "," and len(stack) == 1:
            args.append(current)
            current = []
        else:
            current.append(token)
    return []


def localized_literals(src, streams=None):
    """Yield (literal, table, raw hashes) for explicit localizing APIs' direct literal keys.

    Coverage is intentionally syntactic, not overload resolution: String(localized:), NSLocalizedString,
    LocalizedStringResource/Key (including .init and explicitly typed literal bindings), and interfaceText.
    Ordinary Text/Label/Button/String overloads, dynamic keys and inferred resource assignments require
    compiler extraction (`strict`/`sync`).
    Explicit table/tableName literals select their own catalog; dynamic table expressions are not guessed.
    Ignore markers exclude non-UI source literals, but do not excuse a missing explicit localized key.
    """
    names = {"NSLocalizedString", "LocalizedStringResource", "LocalizedStringKey", "interfaceText", "String"}
    for tokens in _swift_tokens(src) if streams is None else streams:
        for i, token in enumerate(tokens):
            if token[0] == "literal" and i >= 3 and tokens[i - 1][1] == "=" and \
                    tokens[i - 2][1] in ("LocalizedStringResource", "LocalizedStringKey") and tokens[i - 3][1] == ":":
                # A direct literal bound to an explicitly declared resource/key type is unambiguous.
                following = tokens[i + 1] if i + 1 < len(tokens) else None
                if following is None or following[1] in (";", "}") or \
                        "\n" in src[token[3]:following[2]]:
                    start = token[2]
                    hashes = len(src[start:]) - len(src[start:].lstrip("#"))
                    yield token[1], "Localizable", hashes
                continue
            if token[0] != "code" or token[1] != "(" or i == 0 or tokens[i - 1][0] != "code":
                continue
            name = tokens[i - 1][1]
            if name == "init" and i >= 3 and tokens[i - 2][1] == ".":
                name = tokens[i - 3][1]
            if name not in names:
                continue
            args = _arguments(tokens, i)
            if not args:
                continue
            first = args[0]
            if name == "String":
                if len(first) < 2 or [t[1] for t in first[:2]] != ["localized", ":"]:
                    continue
                first = first[2:]
            elif len(first) >= 2 and first[1][1] == ":":
                # LocalizedStringResource(key:defaultValue:) also has an explicit key.
                if first[0][1] != "key":
                    continue
                first = first[2:]
            if len(first) != 1 or first[0][0] != "literal":
                continue
            table, dynamic_table = "Localizable", False
            for arg in args[1:]:
                if len(arg) >= 2 and arg[0][1] in ("table", "tableName") and arg[1][1] == ":":
                    if len(arg) == 3 and arg[2][0] == "literal":
                        table = arg[2][1].text
                    elif len(arg) == 3 and arg[2][1] == "nil":
                        table = "Localizable"
                    else:
                        dynamic_table = True
            if dynamic_table:
                continue
            lit, start = first[0][1], first[0][2]
            hashes = len(src[start:]) - len(src[start:].lstrip("#"))
            yield lit, table, hashes


def source_problems(src, catalog_keys, rel="source"):
    """Validate source text against {table name: iterable of keys}; never match visible English aliases."""
    problems = []
    marked = ignored_lines(src)  # None: the whole file is ignored (`// i18n-ignore-file`)
    skip = preview_lines(src)
    streams = _swift_tokens(src)
    notes = {(token[1].line, token[1].col) for tokens in streams for i, token in enumerate(tokens)
             if token[0] == "literal" and i >= 2 and tokens[i - 2][1] == "comment" and tokens[i - 1][1] == ":"}
    if marked is not None:
        for lit in swift_literals.literals(src):
            if not lit.has_cjk or lit.line in marked or lit.line in skip or (lit.line, lit.col) in notes:
                continue
            problems.append(f"{rel}:{lit.line}: Chinese/Japanese source literal (use an English key or // i18n-ignore): {lit.text[:70]}")
    normalized = {table: {normalize(key) for key in keys} for table, keys in catalog_keys.items()}
    for lit, table, hashes in localized_literals(src, streams):
        if lit.line in skip:
            continue
        key = normalize(lit.text, literal=True, raw_hashes=hashes, formats=False)
        known = (key in normalized.get(table, set()) if "\x00" in key
                 else key in catalog_keys.get(table, set()))
        if not known:
            problems.append(f"{rel}:{lit.line}: localized key not in {table}.xcstrings: {lit.text[:70]}")
    return problems


# BeautifulUI (apple/Packages/BeautifulUI) is a library Persona does not edit (CLAUDE.md), but its views look their
# text up in the app's main bundle: SwiftUI's LocalizedStringKey initializers and modifiers below, called with a
# literal and no `bundle:`. So the app's Localizable catalog must have every such key, or the BeautifulUI style shows
# English in every language (#5574).
BEAUTIFULUI = "Packages/BeautifulUI/Sources"
LOCALIZED_KEY_CALLS = {
    "Text", "Button", "Label", "TextField", "SecureField", "Toggle", "Section", "Picker", "LabeledContent", "Link",
    "Menu", "DisclosureGroup", "ContentUnavailableView", "navigationTitle", "accessibilityLabel", "accessibilityHint",
    "accessibilityValue", "help", "alert", "confirmationDialog",
}


def key_literals(src, streams=None):
    """Yield (literal, raw hashes) for a LocalizedStringKey call's first, unlabelled literal argument, with no
    `bundle:` (which names the library's own bundle). `Text(verbatim:)` and a labelled first argument are not keys."""
    for tokens in _swift_tokens(src) if streams is None else streams:
        for i, token in enumerate(tokens):
            if token[0] != "code" or token[1] != "(" or i == 0 or tokens[i - 1][0] != "code":
                continue
            if tokens[i - 1][1] not in LOCALIZED_KEY_CALLS:
                continue
            args = _arguments(tokens, i)
            if not args or any(len(a) >= 2 and a[0][1] == "bundle" and a[1][1] == ":" for a in args[1:]):
                continue
            for lit, start in _key_operands(args[0]):
                yield lit, len(src[start:]) - len(src[start:].lstrip("#"))


def _key_operands(arg):
    """The literals a first argument passes as LocalizedStringKey: a literal, both branches of `c ? "a" : "b"`, or every
    element of `["a", "b"][i]`. Anything that mixes in another expression is a String and is not looked up."""
    if len(arg) == 1:
        return [(arg[0][1], arg[0][2])] if arg[0][0] == "literal" else []
    if len(arg) >= 2 and arg[0][0] == "code" and arg[1][1] == ":":
        return []  # a labelled first argument (verbatim:, key: …) is not this overload
    texts = [t[1] if t[0] == "code" else None for t in arg]
    depth, question, colon = 0, None, None
    for n, text in enumerate(texts):
        if text in ("(", "[", "{"):
            depth += 1
        elif text in (")", "]", "}"):
            depth -= 1
        elif depth == 0 and text == "?" and question is None and n + 1 < len(arg) and arg[n + 1][0] == "literal":
            question = n
        elif depth == 0 and text == ":" and question is not None:
            colon = n
    if question is not None and colon == question + 2 and len(arg) == colon + 2 and arg[colon + 1][0] == "literal":
        return [(arg[question + 1][1], arg[question + 1][2]), (arg[colon + 1][1], arg[colon + 1][2])]
    if texts and texts[0] == "[":
        close = texts.index("]") if "]" in texts else -1
        elements = arg[1:close]
        if close > 1 and texts[close + 1:close + 2] == ["["] and \
                all(t[0] == "literal" for t in elements[0::2]) and all(t[1] == "," for t in elements[1::2]):
            return [(t[1], t[2]) for t in elements[0::2]]
    return []


def beautifului_problems(src, keys, rel="source"):
    """Every key a BeautifulUI view looks up (with a letter in it: a lone dash or symbol reads the same in every
    language) must be in the app's Localizable catalog."""
    problems = []
    normalized = {normalize(key) for key in keys}
    for lit, hashes in key_literals(src):
        key = normalize(lit.text, literal=True, raw_hashes=hashes, formats=False)
        if not re.search(r"[^\W\d_]", key.replace("\x00", "")):
            continue
        known = key in normalized if "\x00" in key else key in keys
        if not known:
            problems.append(f"{rel}:{lit.line}: BeautifulUI looks this key up in the app's Localizable.xcstrings, "
                            f"which lacks it: {lit.text[:70]}")
    return problems


def link_problems(apple=APPLE, allowed=LINK_CJK):
    """Persona Link's catalog is complete, every `linkText` literal is one of its keys, and no file gains Chinese text."""
    problems = []
    catalog_path = os.path.join(apple, LINK_CATALOG)
    if not os.path.exists(catalog_path):
        return [f"apple/{LINK_CATALOG}: missing"]
    data = check_catalog(catalog_path, problems)
    keys = {normalize(key) for key in data.get("strings", {})}
    sources = os.path.join(apple, LINK_SOURCES)
    for path in sorted(glob.glob(os.path.join(sources, "**", "*.swift"), recursive=True)):
        rel, name = os.path.relpath(path, ROOT), os.path.relpath(path, sources)
        with open(path, encoding="utf-8") as f:
            src = f.read()
        for tokens in _swift_tokens(src):
            for i, token in enumerate(tokens):
                if token[1] != "(" or i == 0 or tokens[i - 1][1] != "linkText":
                    continue
                args = _arguments(tokens, i)
                if len(args) != 1 or len(args[0]) != 1 or args[0][0][0] != "literal":
                    continue  # the helper's own declaration, or a key that is not a literal
                lit, start = args[0][0][1], args[0][0][2]
                hashes = len(src[start:]) - len(src[start:].lstrip("#"))
                if normalize(lit.text, literal=True, raw_hashes=hashes, formats=False) not in keys:
                    problems.append(f"{rel}:{lit.line}: linkText key not in apple/{LINK_CATALOG}: {lit.text[:70]}")
        count = len(cjk_literals(path))
        if count > allowed.get(name, 0):
            problems.append(f"{rel}: {count} Chinese/Japanese literals, at most {allowed.get(name, 0)}: new text goes through "
                            f"linkText and apple/{LINK_CATALOG}")
    return problems


def check():
    problems, catalog_keys = [], {}
    for c in CATALOGS:
        path = os.path.join(APPLE, c)
        if not os.path.exists(path):
            problems.append(f"apple/{c}: missing")
            continue
        data = check_catalog(path, problems)
        plist = os.path.join(os.path.dirname(path), "Info.plist")
        if c.endswith("InfoPlist.xcstrings") and os.path.exists(plist):
            with open(plist, "rb") as f:
                problems.extend(infoplist_drift(f"apple/{c}", data, plistlib.load(f)))
        table = os.path.basename(c).removesuffix(".xcstrings")
        catalog_keys.setdefault(table, set()).update(data.get("strings", {}))
    for path in source_files():
        with open(path, encoding="utf-8") as f:
            problems.extend(source_problems(f.read(), catalog_keys, os.path.relpath(path, ROOT)))
    for path in sorted(glob.glob(os.path.join(APPLE, BEAUTIFULUI, "**", "*.swift"), recursive=True)):
        with open(path, encoding="utf-8") as f:
            problems.extend(beautifului_problems(f.read(), catalog_keys.get("Localizable", set()), os.path.relpath(path, ROOT)))
    return problems + link_problems()


def stringsdata(derived, targets=TARGETS):
    pattern = os.path.join(derived, "Build", "Intermediates.noindex", "**", "*.stringsdata")
    for f in glob.glob(pattern, recursive=True):
        parts = f.split(os.sep)
        if not any(f"{t}.build" in parts for t in targets):
            continue
        yield f


def specifier_mismatches(derived, keys):
    """Keys the compiler extracted that the catalog has only with other format specifiers (#5544): the lookup key
    follows each interpolated value's type (String or Substring → %@, Int → %lld, Double → %lf…), so "Exit code
    \\(text)" asks for "Exit code %@" while the catalog has "Exit code %lld", and every language shows the English
    key. Only the compiler knows the types, so this reads its .stringsdata (the app targets, and BeautifulUI and
    PersonaKit, which look their text up in the app's catalog)."""
    shapes = {}
    for key in keys:
        shapes.setdefault(normalize(key), []).append(key)
    problems, seen = [], set()
    for f in stringsdata(derived, TARGETS + ["BeautifulUI", "PersonaKit"]):
        with open(f, encoding="utf-8") as stream:
            d = json.load(stream)
        src = d.get("source", "")
        rel = os.path.relpath(src, ROOT) if src.startswith(ROOT) else src.split("/apple/", 1)[-1]
        for e in d.get("tables", {}).get("Localizable", []):
            key = e.get("key", "")
            if key in keys or not SPEC.search(key.replace("%%", "")):
                continue
            others = sorted(set(shapes.get(normalize(key), [])) - {key})
            line = e.get("location", {}).get("startingLine", "?")
            if others and (rel, line, key) not in seen:
                seen.add((rel, line, key))
                problems.append(f"{rel}:{line}: looks up {key!r}, but the catalog has {', '.join(map(repr, others))} "
                                "(the interpolated value's type picks the specifier)")
    return problems


def strict(derived, keys=None):
    if keys is None:
        with open(os.path.join(APPLE, "Shared/Localization/Localizable.xcstrings"), encoding="utf-8") as f:
            keys = set(json.load(f).get("strings", {}))
    extracted = {}
    for f in stringsdata(derived):
        with open(f, encoding="utf-8") as stream:
            d = json.load(stream)
        src = os.path.realpath(d.get("source", ""))
        for entries in d.get("tables", {}).values():
            for e in entries:
                loc = e.get("location")
                if loc:
                    extracted.setdefault(src, set()).add(loc["startingLine"])
    problems = []
    for path in source_files():
        lines = extracted.get(os.path.realpath(path), set())
        with open(path, encoding="utf-8") as f:
            src_lines = f.read().split("\n")
        for lit in cjk_literals(path):
            before = src_lines[lit.line - 1][: lit.col - 1]
            # Already a resource the compiler does not list with a place: App Shortcut phrases and App Intents'
            # parameter summaries (their own tables), and String(localized:) in a `case` pattern.
            if "\\(.applicationName)" in lit.text or "\\(\\.$" in lit.text or before.rstrip().endswith("String(localized:"):
                continue
            if lit.line not in lines:
                problems.append(f"{os.path.relpath(path, ROOT)}:{lit.line}: not localized: {lit.text[:70]}")
    return problems + specifier_mismatches(derived, keys)


def sync(derived):
    files = sorted(stringsdata(derived))
    if not files:
        sys.exit("no .stringsdata: build with SWIFT_EMIT_LOC_STRINGS=YES into this DerivedData first")
    catalogs = [os.path.join(APPLE, "Shared/Localization/Localizable.xcstrings"),
                os.path.join(APPLE, "Persona/Intents/AppShortcuts.xcstrings")]
    args = ["xcrun", "xcstringstool", "sync", *catalogs, "--skip-marking-strings-stale"]
    for f in files:
        args += ["--stringsdata", f]
    subprocess.run(args, check=True)
    print(f"synced {len(files)} .stringsdata into the catalogs")


def merge(ours, theirs, out):
    with open(ours, encoding="utf-8") as f:
        a = json.load(f)
    with open(theirs, encoding="utf-8") as f:
        b = json.load(f)
    def complete(e):
        locs = e.get("localizations", {})
        return all(lang in locs for lang in LANGS)
    merged = dict(b.get("strings", {}))
    for key, entry in a.get("strings", {}).items():
        if key not in merged or (complete(entry) and not complete(merged[key])):
            merged[key] = entry
    b["strings"] = dict(sorted(merged.items()))
    with open(out, "w", encoding="utf-8") as f:
        json.dump(b, f, ensure_ascii=False, indent=2, separators=(",", " : "))
    print(f"merged {len(merged)} strings into {out}")


def main():
    if len(sys.argv) >= 5 and sys.argv[1] == "merge":
        merge(*sys.argv[2:5])
        return
    if len(sys.argv) < 2 or sys.argv[1] not in ("check", "strict", "sync"):
        sys.exit(__doc__)
    cmd = sys.argv[1]
    if cmd == "sync":
        sync(sys.argv[2])
        return
    problems = check() if cmd == "check" else strict(sys.argv[2])
    if problems:
        print("\n".join(problems))
        print(f"FAIL: {sum(1 for p in problems if not p.startswith('  '))} problems")
        sys.exit(1)
    print("OK: English-source Apple catalogs are complete in en, zh-Hans and ja")


if __name__ == "__main__":
    main()
