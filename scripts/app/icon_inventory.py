#!/usr/bin/env python3
"""Inventory the Apple apps' icons: SF Symbols, how they are drawn, status dots and image assets.

Read-only. Prints a Markdown report (or JSON with --json) and ends with a one-line verdict.

  scripts/app/icon_inventory.py                 # report for the working tree
  scripts/app/icon_inventory.py --json > a.json # machine-readable, for before/after diffs
  scripts/app/icon_inventory.py --diff a.json   # compare the working tree with an earlier --json

Symbol names are recognised by the registry (`Theme.Icon` in apple/Shared/Design/Icons.swift), by the argument they are
passed as (`systemName:`, `systemImage:`, `symbol:`), and on macOS by asking AppKit whether a dotted literal is a real
SF Symbol. Lines ending in `// icon: <why>` are another product's names (Lark card tokens) and are listed separately.
"""
import argparse
import collections
import json
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
APPLE = ROOT / 'apple'
TARGETS = ('Persona', 'Shared', 'PersonaWatch', 'Mac', 'Vision', 'PersonaWidgets', 'LiveActivity', 'PersonaShare', 'PersonaNotify')
ICONS = APPLE / 'Shared/Design/Icons.swift'

LITERAL = re.compile(r'"([a-z0-9]+(?:\.[a-z0-9]+)*)"')
ARGUMENT = re.compile(r'\b(?:systemName|systemImage|symbol):\s*')
IMAGE = re.compile(r'Image\(systemName:|ThemedIcon\(|Label\([^)]*systemImage:')
MODIFIERS = {
    'font': re.compile(r'\.font\(([^()]*(?:\([^()]*\))?[^()]*)\)'),
    'fontWeight': re.compile(r'\.fontWeight\(\.?(\w+)\)'),
    'imageScale': re.compile(r'\.imageScale\(\.(\w+)\)'),
    'renderingMode': re.compile(r'\.symbolRenderingMode\(\.(\w+)\)'),
    'variant': re.compile(r'\.symbolVariant\(\.(\w+)\)'),
    'resizable': re.compile(r'\.resizable\(\)'),
}
DOT = re.compile(r'Circle\(\)')
DOT_SIZE = re.compile(r'(?:scaledFrame|frame)\(width:\s*([\w.]+)')


def swift_files():
    for t in TARGETS:
        for p in sorted((APPLE / t).rglob('*.swift')):
            if p != ICONS:
                yield p


def registry():
    src = ICONS.read_text().split('enum Brand')[0]
    names = collections.defaultdict(list)
    for m in re.finditer(r'static let (\w+) = "([^"]+)"', src):
        names[m.group(2)].append(m.group(1))
    brands = dict(re.findall(r'static let (\w+) = "([^"]+)"', ICONS.read_text().split('enum Brand')[1]))
    return dict(names), brands


def real_symbols(candidates):
    """The subset AppKit knows as SF Symbols; None when not on macOS (then only context decides)."""
    if sys.platform != 'darwin' or not shutil.which('swift'):
        return None
    probe = 'import AppKit\nwhile let l = readLine() { if NSImage(systemSymbolName: l, accessibilityDescription: nil) != nil { print(l) } }\n'
    with tempfile.TemporaryDirectory() as d:
        script = Path(d) / 'probe.swift'
        script.write_text(probe)
        out = subprocess.run(['swift', str(script)], input='\n'.join(sorted(candidates)), capture_output=True, text=True, check=True)
    return set(out.stdout.split())


def statement(lines, i):
    """The line at i plus following lines that continue its modifier chain."""
    text = lines[i]
    j = i + 1
    while j < len(lines) and lines[j].lstrip().startswith('.'):
        text += lines[j].strip()
        j += 1
    return text


def scan():
    reg, brands = registry()
    files = {p: p.read_text().split('\n') for p in swift_files()}
    rel = lambda p: str(p.relative_to(APPLE))

    candidates = set()
    for lines in files.values():
        for line in lines:
            candidates.update(m.group(1) for m in LITERAL.finditer(line) if '.' in m.group(1))
    known = real_symbols(candidates)

    literals, external, images, dots, assets_used = [], [], [], [], []
    for p, lines in files.items():
        for n, line in enumerate(lines):
            if line.strip().startswith('//'):
                continue
            in_arg = set()
            for m in ARGUMENT.finditer(line):
                in_arg.update(l.group(1) for l in LITERAL.finditer(line[m.end():m.end() + 160]))
            for m in LITERAL.finditer(line):
                s = m.group(1)
                is_symbol = s in in_arg or ('.' in s and (s in reg or (known is not None and s in known)))
                if not is_symbol or s in brands.values():
                    continue
                entry = {'file': rel(p), 'line': n + 1, 'symbol': s, 'registered': reg.get(s, [])}
                (external if '// icon:' in line else literals).append(entry)
            if IMAGE.search(line):
                st = statement(lines, n)
                mods = {k: r.findall(st) for k, r in MODIFIERS.items()}
                kind = 'themed' if 'ThemedIcon(' in line else 'label' if 'systemImage:' in line and 'Image(systemName:' not in line else 'raw'
                images.append({'file': rel(p), 'line': n + 1, 'kind': kind, **{k: v for k, v in mods.items() if v}})
            if DOT.search(line):
                st = statement(lines, n)
                if '.fill(' in st or 'fill(' in st:
                    size = DOT_SIZE.search(st)
                    dots.append({'file': rel(p), 'line': n + 1, 'size': size.group(1) if size else None})
            for m in re.finditer(r'Image\("([^"]+)"', line):
                assets_used.append({'file': rel(p), 'line': n + 1, 'name': m.group(1)})

    catalogs = {}
    for cat in sorted(APPLE.rglob('*.xcassets')):
        if 'Packages' in cat.parts:
            continue
        catalogs[rel(cat)] = sorted(str(s.relative_to(cat)) for s in cat.rglob('*') if s.suffix in ('.imageset', '.appiconset', '.iconset', '.solidimagestack', '.brandassets'))
    icon_files = sorted(rel(p) for p in APPLE.rglob('*.icon') if 'Packages' not in p.parts)

    refs = collections.Counter()
    for lines in files.values():
        for line in lines:
            refs.update(re.findall(r'Theme\.Icon\.(\w+)', line))
    all_names = {n for v in reg.values() for n in v}
    return {
        'registry': {'entries': sum(len(v) for v in reg.values()), 'symbols': len(reg),
                     'aliases': {s: v for s, v in sorted(reg.items()) if len(v) > 1},
                     'unused': sorted(all_names - set(refs))},
        'literals': literals,
        'external': external,
        'images': images,
        'dots': dots,
        'assets': {'catalogs': catalogs, 'icon_files': icon_files, 'image_literals': assets_used},
        'symbol_check': 'appkit' if known is not None else 'context-only',
    }


def summary(data):
    imgs = data['images']
    count = lambda k: collections.Counter(v for i in imgs for v in i.get(k, []))
    return {
        'registry_entries': data['registry']['entries'],
        'registry_aliases': len(data['registry']['aliases']),
        'registry_unused': len(data['registry']['unused']),
        'unregistered_literals': sum(1 for l in data['literals'] if not l['registered']),
        'registered_literals': sum(1 for l in data['literals'] if l['registered']),
        'external_literals': len(data['external']),
        'symbol_images': len(imgs),
        'themed_icons': sum(1 for i in imgs if i.get('kind') == 'themed'),
        'raw_symbol_images': sum(1 for i in imgs if i.get('kind') == 'raw'),
        'fontWeight': dict(count('fontWeight')),
        'imageScale': dict(count('imageScale')),
        'renderingMode': dict(count('renderingMode')),
        'variant': dict(count('variant')),
        'resizable_symbols': sum(1 for i in imgs if i.get('resizable')),
        'raw_size_fonts': sum(1 for i in imgs for f in i.get('font', []) if 'size:' in f),
        'dots': len(data['dots']),
        'dot_sizes': dict(collections.Counter(d['size'] or '?' for d in data['dots'])),
        'image_literals': len(data['assets']['image_literals']),
    }


def markdown(data):
    s = summary(data)
    out = ['# Apple icon inventory', '', f"Symbol check: {data['symbol_check']}", '', '## Summary', '', '| Measure | Value |', '| --- | --- |']
    out += [f'| {k} | {v} |' for k, v in s.items()]
    out += ['', '## Registry aliases (one symbol, several names)', '']
    out += [f'- `{sym}`: {", ".join(names)}' for sym, names in data['registry']['aliases'].items()] or ['none']
    out += ['', '## Symbol literals outside Theme.Icon', '', '| File | Line | Symbol | Registered as |', '| --- | --- | --- | --- |']
    out += [f"| {l['file']} | {l['line']} | `{l['symbol']}` | {', '.join(l['registered'])} |" for l in data['literals']]
    out += ['', '## Symbol fonts (Image(systemName:) / Label chains)', '']
    fonts = collections.Counter(f for i in data['images'] for f in i.get('font', []))
    out += [f'- {n} × `.font({f})`' for f, n in fonts.most_common()]
    out += ['', '## Status dots (Circle().fill)', '']
    out += [f"- {d['file']}:{d['line']} size `{d['size']}`" for d in data['dots']]
    out += ['', '## Asset catalogs', '']
    for cat, sets in data['assets']['catalogs'].items():
        out += [f'- `{cat}`: {len(sets)} sets: ' + ', '.join(sets)]
    out += [f'- `{f}`' for f in data['assets']['icon_files']]
    out += ['', '## Image("…") literals', '']
    out += [f"- {a['file']}:{a['line']} `{a['name']}`" for a in data['assets']['image_literals']] or ['none']
    return '\n'.join(out)


def diff(before, after):
    b, a = summary(before), summary(after)
    lines = ['| Measure | Before | After |', '| --- | --- | --- |']
    for k in a:
        if b.get(k) != a[k]:
            lines.append(f'| {k} | {b.get(k)} | {a[k]} |')
    return '\n'.join(lines)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    ap.add_argument('--json', action='store_true', help='print the full inventory as JSON')
    ap.add_argument('--diff', metavar='BEFORE_JSON', help='compare with an earlier --json output')
    args = ap.parse_args()
    data = scan()
    if args.json:
        print(json.dumps(data, indent=1))
    elif args.diff:
        print(diff(json.loads(Path(args.diff).read_text()), data))
    else:
        print(markdown(data))
    s = summary(data)
    print(f"\nverdict: {s['unregistered_literals']} unregistered and {s['registered_literals']} registered symbol literals outside Theme.Icon, "
          f"{s['registry_aliases']} aliased symbols, {s['raw_size_fonts']} symbols sized with raw points, {s['dots']} hand-drawn dots",
          file=sys.stderr if args.json else sys.stdout)


if __name__ == '__main__':
    main()
