"""Section names quoted after a doc path must name a section of that doc.

Code and docs point at a part of a doc by name: `docs/protocol/protocol.md` ("Error body"),
docs/apple/mac.md「Main window」. Headings get renamed, merged and translated, and a name left behind sends the reader
looking for a section that is not there. test_doc_paths.py checks that the file exists; this checks the name.

A reference is a path ending in .md followed by "Name", “Name”, ("Name") or 「Name」; the path may be backticked or
the target of a Markdown link. A quoted "Name" starts with a capital letter, a digit, a backtick or a Chinese
character: lower-case text in quotes is prose. The name resolves when it matches a heading, a **bold** label or an
<a id> anchor of the doc, after dropping leading section numbers (3.2, 1b., IV ·, E.), backticks and a trailing
parenthetical, and ignoring the kind of quotes and dashes, case and extra spaces. A shorter name may stand for a longer
target if it ends at a word boundary. It also resolves when it is the whole opening phrase of a list item, up to " ("
or the end of the first sentence: the lessons files name each entry that way. Files under docs/archive are never
scanned but stay valid targets.

The sources are the docs test_doc_paths.py checks plus the tracked files under SOURCE_DIRS, except translation
catalogs (their text follows the UI strings it translates) and the released files pinned by hash. Names that drifted
in lines other open work was editing, or inside UI strings, are listed in doc_sections_known.txt (source, doc, name).
The list only shrinks: a new miss fails, and so does an entry that no longer misses.
"""
from collections import defaultdict
from pathlib import Path
import os
import re
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_doc_paths  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
KNOWN = Path(__file__).resolve().with_name("doc_sections_known.txt")

SOURCE_DIRS = ("server", "web/src", "web/e2e", "scripts", "sdk", "apple", "android", "integrations", "spec",
               "schema", "CLAUDE.md", "README.md")
# Translation catalogs, and copies of released files that test_deployment_rolling_compat.py pins by hash.
NOT_SOURCES = re.compile(r"\.xcstrings$|/res/values[^/]*/|^server/internal/i18n/locales/"
                         r"|^scripts/test/fixtures/deployment/")

# What follows the path: "Name", ("Name") or “Name”, after a closing backtick or a Markdown link's target, or 「Name」.
# The regex starts at the literal .md so it skips most text quickly; the path is read backwards from there.
REFERENCE = re.compile(r"\.md(?:(?:`|\]\([^)\s]*\))?\)?,? \(?[\"“]"
                       r"([A-Z0-9`][^\"”\n]{2,89}|[\u3400-\u9fff][^\"”\n]{1,89})[\"”]"
                       r"|`?[ \t]*[「（(]?[ \t]*「([^」\n]{1,60})」)")
PATH_CHAR = frozenset("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-")

HEADING = re.compile(r"^#{1,6}\s+(.*?)\s*#*\s*$", re.M)
BOLD = re.compile(r"\*\*([^*\n]{2,160})\*\*")
ANCHOR = re.compile(r"<a\s+(?:id|name)=\"([^\"]+)\"")
# A list item's opening phrase ends at " (" or with its first sentence. docs/lessons has few headings and names each
# entry that way: "- `opacity(0)` cannot hide a button inside a glass container (2026-11-05, #1337). The ...".
LIST_ITEM = re.compile(r"^[ \t]*(?:[-*+]|\d+\.)[ \t]+(.+)", re.M)
LEAD_END = re.compile(r" \(|[.。](?:\s|$)")
# Quotes inside a name are written either way: a heading's "open" becomes 'open' inside a "…" reference.
PUNCTUATION = str.maketrans("‘’“”\"–—", "'''''--")


def strip_parenthetical(name):
    """Drop trailing parentheticals, nested ones too: "Pairing (QR (v2))" becomes "Pairing"."""
    name = name.rstrip()
    while name.endswith((")", "）")):
        depth = 0
        for i in range(len(name) - 1, -1, -1):
            if name[i] in ")）":
                depth += 1
            elif name[i] in "(（":
                depth -= 1
                if depth == 0:
                    break
        else:
            return name
        name = name[:i].rstrip()
    return name


def normalize(name):
    name = re.sub(r"[`*_]", "", name).strip()
    name = re.sub(r"^\d+[a-z]?(?:\.\d+)*\.?\s+", "", name)  # 3.2 Name, 1b. Name
    name = re.sub(r"^(?:[IVX]+\s*[·.]\s*(?:\d+\.?\s*)?|[A-Z]\.\s+)", "", name)  # IV · Name, II. 3. Name, E. Name
    name = strip_parenthetical(name).translate(PUNCTUATION)
    return re.sub(r"\s+", " ", name).strip(" .:,'").lower()


def targets(text):
    """The headings, **bold** labels and <a id> anchors, and apart from them the list items' opening phrases."""
    # A line starting with # inside a fenced block is a shell comment, not a heading.
    unfenced = "\n".join(line for _, line in test_doc_paths.outside_fences(text))
    names = HEADING.findall(unfenced) + BOLD.findall(text) + ANCHOR.findall(text)
    leads = (LEAD_END.split(item, 1)[0] for item in LIST_ITEM.findall(unfenced))
    return {normalize(name) for name in names} - {""}, {normalize(lead) for lead in leads} - {""}


def resolves(name, names, leads=frozenset()):
    """An exact match, the start of a target up to a word boundary, or a whole opening phrase of a list item.

    List items are many and their phrases long, so a mere start of one does not count."""
    return name in names or name in leads or any(
        t.startswith(name) and not (t[len(name)].isalnum() and name[-1].isalnum())
        for t in names if len(t) > len(name))


class Docs:
    """The tracked Markdown files indexed by file name, each read once when first named."""

    def __init__(self, root, paths):
        self.root, self.by_name, self.cache, self.answers = root, defaultdict(list), {}, {}
        for path in paths:
            self.by_name[path.rsplit("/", 1)[-1]].append(path)

    def candidates(self, written):
        found = self.by_name.get(written.rsplit("/", 1)[-1], [])
        tail = re.sub(r"^(?:\.{1,2}/)+", "", written)
        return [p for p in found if p == tail or p.endswith("/" + tail)] or found

    def names(self, path):
        if path not in self.cache:
            self.cache[path] = targets((self.root / path).read_text(encoding="utf-8", errors="replace"))
        return self.cache[path]

    def has(self, written, name):
        """None when no such doc is tracked, else whether the name resolves in one of the docs the path can mean."""
        if (written, name) not in self.answers:
            paths = self.candidates(written)
            self.answers[written, name] = any(resolves(name, *self.names(p)) for p in paths) if paths else None
        return self.answers[written, name]


def references(text):
    """Yield (line, written path, name) for each quoted section name in the text."""
    line, counted = 1, 0
    for match in REFERENCE.finditer(text):
        start = match.start()
        while start > 0 and text[start - 1] in PATH_CHAR:
            start -= 1
        written = text[start:match.start()] + ".md"
        if written.rsplit("/", 1)[-1] == ".md":
            continue
        # A ")" after the path ends a Markdown link's target; ending a parenthetical, the quote after it is prose.
        if ")" in text[match.start():match.start() + 5] and text[start - 2:start] != "](":
            continue
        line += text.count("\n", counted, start)
        counted = start
        yield line, written, match.group(1) or match.group(2)


def unresolved(root, sources, markdown):
    """(source, line, written path, name) for each reference to an existing doc that names none of its sections."""
    docs, misses = Docs(root, markdown), []
    for source in sources:
        with open(os.path.join(root, source), "rb") as file:
            data = file.read()
        if b".md" not in data:
            continue
        for line, written, name in references(data.decode("utf-8", errors="replace")):
            key = normalize(name)
            if key and docs.has(written, key) is False:
                misses.append((source, line, written, name))
    return misses


def git_files(root, *pathspecs):
    listed = subprocess.run(["git", "ls-files", "-z", "--", *pathspecs], cwd=root, check=True,
                            capture_output=True, text=True).stdout
    return [name for name in listed.split("\0") if name]


def repository_sources():
    docs = {doc.relative_to(ROOT).as_posix() for doc in test_doc_paths.doc_files()}
    # A symlink's target is scanned under its own name.
    return sorted(name for name in docs.union(git_files(ROOT, *SOURCE_DIRS))
                  if not NOT_SOURCES.search(name) and os.path.isfile(ROOT / name) and not os.path.islink(ROOT / name))


def read_known(path):
    return {tuple(line.split("\t")) for line in path.read_text(encoding="utf-8").splitlines()
            if line and not line.startswith("#")}


def compare(misses, known):
    """The misses not in the known list, and the known entries that no longer miss."""
    new = [m for m in misses if (m[0], m[2], m[3]) not in known]
    return new, sorted(known - {(source, written, name) for source, _, written, name in misses})


class DocSectionTests(unittest.TestCase):
    def test_quoted_section_names_exist(self):
        misses = unresolved(ROOT, repository_sources(), git_files(ROOT, "*.md"))
        new, stale = compare(misses, read_known(KNOWN))
        self.assertEqual([], [f"{s}:{n}: {w} \"{name}\"" for s, n, w, name in new],
                         "these names match no heading, **bold** label, <a id> or list item's opening phrase of the "
                         "doc: change each to the section's current heading, or drop the name and keep the path")
        self.assertEqual([], ["\t".join(entry) for entry in stale],
                         f"these entries resolve now or are gone: delete them from {KNOWN.relative_to(ROOT)}")

    def test_reference_forms_and_the_known_list(self):
        doc = "\n".join(["# Sample guide", "## 2. Pairing and tokens (QR (v2))", "**Hold to talk**: the button",
                         '<a id="wire-names"></a>', "## Hub’s address",
                         "- Panel kept the old look (2026-11-03, #956). Rebuild it.", ""])
        cases = [  # (source line, reported as a miss)
            ('docs/sample-guide.md "Pairing and tokens"', False),
            ('`docs/sample-guide.md` ("Pairing (old (nested) name)")', False),  # a prefix at a word boundary
            ("[guide](docs/sample-guide.md) “Hold to talk”", False),  # a bold label
            ("docs/sample-guide.md「wire-names」", False),  # an <a id>
            ('docs/sample-guide.md, "Hub\'s address"', False),
            ("docs/sample-guide.md（「配对」）", True),  # a retired heading
            ('docs/sample-guide.md "Pair"', True),  # not at a word boundary
            ('docs/sample-guide.md "lower case is prose, not a name"', False),
            ('docs/no-such-guide.md "Gone"', False),  # test_doc_paths.py reports a missing file
            ('docs/sample-guide.md, "Panel kept the old look"', False),  # a list item's opening phrase
            ('docs/sample-guide.md "Panel kept"', True),  # only the whole phrase of a list item counts
            ('docs/sample-guide.md "旧的配对"', True),  # a Chinese name in quotes is checked too
        ]
        with tempfile.TemporaryDirectory(prefix="doc-sections-") as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/sample-guide.md").write_text(doc)
            for number, (line, missed) in enumerate(cases):
                (root / f"case{number}.go").write_text(f"// see {line}\n")
                with self.subTest(line=line):
                    self.assertEqual(missed, bool(unresolved(root, [f"case{number}.go"], ["docs/sample-guide.md"])))
            misses = unresolved(root, [f"case{number}.go" for number in range(len(cases))], ["docs/sample-guide.md"])
            known = {("case5.go", "docs/sample-guide.md", "配对"), ("case0.go", "docs/sample-guide.md", "Gone")}
            new, stale = compare(misses, known)
            self.assertEqual([("case6.go", 1, "docs/sample-guide.md", "Pair"),
                              ("case10.go", 1, "docs/sample-guide.md", "Panel kept"),
                              ("case11.go", 1, "docs/sample-guide.md", "旧的配对")], new)
            self.assertEqual([("case0.go", "docs/sample-guide.md", "Gone")], stale)

    def test_headings_inside_fences_do_not_count(self):
        doc = "\n".join(["# Guide", "```sh", "# a shell comment", "```",
                         "  ```` ```mermaid ```` starts a line but is an inline span", "## After the span",
                         "````md", "```", "# still inside the longer fence", "````", "## After the long fence",
                         "~~~", "# inside tildes", "~~~", "## Last", "```", "# an unclosed fence runs to the end", ""])
        self.assertEqual({"guide", "after the span", "after the long fence", "last"}, targets(doc)[0])

    def test_known_list_is_sorted_and_unique(self):
        lines = [line for line in KNOWN.read_text(encoding="utf-8").splitlines() if not line.startswith("#")]
        self.assertEqual(sorted(set(lines)), lines, f"keep {KNOWN.name} sorted with one line per entry")
        self.assertTrue(all(line.count("\t") == 2 for line in lines), "each entry is source<TAB>doc<TAB>name")


if __name__ == "__main__":
    unittest.main()
