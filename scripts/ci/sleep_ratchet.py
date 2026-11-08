#!/usr/bin/env python3
"""Fail when a Go test file grows its fixed time.Sleep calls.

Each _test.go file under server/ and sdk/go/ has a committed count of time.Sleep calls
(scripts/ci/sleep_baseline.json). The count may stay the same or go down, never up, and a file
with no entry starts at zero, so a new test file cannot add an unescaped sleep. Comments and
string literals are ignored. A call on a line that carries `// sleep-ok: <reason>` does not
count; the reason must be non-empty. Whitespace and comments may sit between time, the dot,
Sleep and the opening parenthesis, so splitting the call does not hide it.

A file under its baseline fails too: run --update, which rewrites the baseline downward and
drops files that no longer sleep. --update refuses to raise a count or to add a file that still
sleeps, and it will not create a baseline (that would record the sleeps already in the tree).
Once the baseline is committed, a later edit that raises a count or adds a file is rejected
against that commit, whether or not --update was used. See docs/lessons/testing.md.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOTS = ("server", "sdk/go")
BASELINE_REL = "scripts/ci/sleep_baseline.json"
ESCAPE = re.compile(r"^//\s*sleep-ok:\s*\S")


class SleepError(Exception):
    """The check could not be run (broken baseline, or git failed)."""


def _ident_char(ch):
    return ch == "_" or ch.isalnum()


class _Scanner:
    """Count time.Sleep( calls, and remember lines whose // comment escapes them."""

    def __init__(self, text):
        self.text = text
        self.n = len(text)
        self.i = 0
        self.line = 1
        self.calls = []
        self.escaped = set()

    def count(self):
        while self.i < self.n:
            ch = self.text[self.i]
            if ch == "\n":
                self.line += 1
                self.i += 1
                continue
            if ch == "\r":
                self.i += 1
                continue
            if self.text.startswith("//", self.i):
                self._line_comment()
                continue
            if self.text.startswith("/*", self.i):
                self._block_comment()
                continue
            if ch == "`":
                self._raw_string()
                continue
            if ch in ("'", '"'):
                self._quoted(ch)
                continue
            if ch == "t" and self._boundary(self.i) and self.text.startswith("time", self.i):
                end, end_line, call_line = self._match_call(self.i, self.line)
                if call_line is not None:
                    self.calls.append(call_line)
                self.i, self.line = end, end_line
                continue
            self.i += 1
        return sum(1 for call_line in self.calls if call_line not in self.escaped)

    def _boundary(self, index):
        return index == 0 or not _ident_char(self.text[index - 1])

    def _read_ident(self, index):
        end = index
        while end < self.n and _ident_char(self.text[end]):
            end += 1
        return end

    def _line_comment(self):
        end = self.text.find("\n", self.i)
        if end < 0:
            end = self.n
        comment = self.text[self.i:end].replace("\r", "")
        if ESCAPE.match(comment):
            self.escaped.add(self.line)
        self.i = end

    def _block_comment(self):
        end = self.text.find("*/", self.i + 2)
        if end < 0:
            end = self.n
        else:
            end += 2
        self.line += self.text[self.i:end].count("\n")
        self.i = end

    def _raw_string(self):
        end = self.text.find("`", self.i + 1)
        if end < 0:
            end = self.n
        else:
            end += 1
        self.line += self.text[self.i:end].count("\n")
        self.i = end

    def _quoted(self, quote):
        """Interpreted string or rune. A raw newline ends it, so a broken literal cannot swallow the file."""
        self.i += 1
        while self.i < self.n:
            ch = self.text[self.i]
            if ch == "\n":
                return
            if ch == "\\":
                if self.i + 1 < self.n and self.text[self.i + 1] != "\n":
                    self.i += 2
                    continue
                self.i += 1
                continue
            self.i += 1
            if ch == quote:
                return

    def _skip_gap(self, index, line):
        """Whitespace and comments between the tokens of one call. Strings stop the gap."""
        while index < self.n:
            ch = self.text[index]
            if ch in " \t\r\v\f":
                index += 1
                continue
            if ch == "\n":
                line += 1
                index += 1
                continue
            if self.text.startswith("//", index):
                end = self.text.find("\n", index)
                if end < 0:
                    return self.n, line
                comment = self.text[index:end].replace("\r", "")
                if ESCAPE.match(comment):
                    self.escaped.add(line)
                index = end
                continue
            if self.text.startswith("/*", index):
                end = self.text.find("*/", index + 2)
                if end < 0:
                    return self.n, line + self.text[index:].count("\n")
                end += 2
                line += self.text[index:end].count("\n")
                index = end
                continue
            break
        return index, line

    def _match_call(self, index, line):
        """index is the start of an identifier. Return (resume, resume_line, call_line or None)."""
        end = self._read_ident(index)
        if self.text[index:end] != "time":
            return end, line, None
        dot, dot_line = self._skip_gap(end, line)
        if dot >= self.n or self.text[dot] != ".":
            return end, line, None
        name, name_line = self._skip_gap(dot + 1, dot_line)
        if name >= self.n or not _ident_char(self.text[name]):
            return end, line, None
        name_end = self._read_ident(name)
        if self.text[name:name_end] != "Sleep":
            return name, name_line, None
        paren, paren_line = self._skip_gap(name_end, name_line)
        if paren >= self.n or self.text[paren] != "(":
            return name_end, name_line, None
        return paren + 1, paren_line, paren_line


def count_sleeps(text):
    """How many time.Sleep( calls in text are real and not marked // sleep-ok: <reason>."""
    return _Scanner(text).count()


def scan(root):
    """Positive sleep counts keyed by repo-relative path. Files with none are absent."""
    counts = {}
    root = Path(root)
    for folder in ROOTS:
        base = root / folder
        if not base.is_dir():
            continue
        for dirpath, dirnames, filenames in os.walk(base, followlinks=False):
            dirnames[:] = [name for name in dirnames if name != ".git"]
            for name in sorted(filenames):
                if not name.endswith("_test.go"):
                    continue
                path = Path(dirpath) / name
                if not path.is_file():
                    continue
                text = path.read_text(encoding="utf-8", errors="backslashreplace")
                found = count_sleeps(text)
                if found:
                    counts[path.relative_to(root).as_posix()] = found
    return counts


def render(counts):
    """Canonical baseline text. Zero counts are omitted; keys are sorted."""
    kept = {path: count for path, count in sorted(counts.items()) if count > 0}
    return json.dumps(kept, indent=2, sort_keys=True) + "\n"


def parse_baseline(text, label):
    """File-to-count map. A zero is dropped. Raises SleepError on a malformed baseline."""
    try:
        data = json.loads(text)
    except json.JSONDecodeError as error:
        raise SleepError("sleep ratchet: {0} is not valid JSON ({1})".format(label, error.msg))
    if not isinstance(data, dict):
        raise SleepError("sleep ratchet: {0} must be a JSON object of file to count".format(label))
    counts = {}
    for key, value in data.items():
        if not isinstance(key, str) or "/" not in key or not key.endswith("_test.go"):
            raise SleepError("sleep ratchet: {0} has a key that is not a Go test path: {1!r}".format(label, key))
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise SleepError("sleep ratchet: {0} count for {1} must be a non-negative integer".format(label, key))
        if value > 0:
            counts[key] = value
    return counts


def over_message(path, got, allowed):
    return (
        "{0}: {1} time.Sleep call(s), baseline {2}; fixed sleeps in Go tests can only go down; "
        "use testing/synctest, a polling helper or a channel (docs/lessons/testing.md)"
    ).format(path, got, allowed)


def under_message(path, got, allowed):
    return (
        "{0}: {1} time.Sleep call(s), baseline {2}; "
        "run python3 scripts/ci/sleep_ratchet.py --update to lower the baseline"
    ).format(path, got, allowed)


def count_violations(actual, baseline):
    """Files over the baseline, then files under it. Missing on either side counts as zero."""
    found = []
    for path in sorted(set(actual) | set(baseline)):
        got = actual.get(path, 0)
        allowed = baseline.get(path, 0)
        if got > allowed:
            found.append(over_message(path, got, allowed))
        elif got < allowed:
            found.append(under_message(path, got, allowed))
    return found


def raised_against(current, previous, label):
    """Lines for counts in current that previous does not already allow."""
    found = []
    for path in sorted(current):
        got = current[path]
        if path not in previous:
            found.append(
                "sleep ratchet: baseline adds {0} with {1} time.Sleep call(s) against {2}; "
                "baselines are never raised (docs/lessons/testing.md)".format(path, got, label))
        elif got > previous[path]:
            found.append(
                "sleep ratchet: baseline raises {0} from {1} to {2} against {3}; "
                "baselines are never raised (docs/lessons/testing.md)".format(path, previous[path], got, label))
    return found


def plan_update(old, actual):
    """The lowered baseline, or (None, refusals) when a count would rise or a file would be added."""
    refusals = []
    for path in sorted(actual):
        got = actual[path]
        allowed = old.get(path, 0)
        if got > allowed:
            if allowed == 0:
                refusals.append("sleep ratchet: refusing to add {0} with {1} time.Sleep call(s)".format(path, got))
            else:
                refusals.append("sleep ratchet: refusing to raise {0} from {1} to {2}".format(path, allowed, got))
    if refusals:
        return None, refusals
    return {path: count for path, count in actual.items() if count > 0}, []


def _git(root, args):
    try:
        return subprocess.run(["git", "-C", str(root), *args], capture_output=True, timeout=60)
    except subprocess.TimeoutExpired:
        raise SleepError("sleep ratchet: git {0} timed out".format(" ".join(args[:6])))


def _inside_git(root):
    result = _git(root, ["rev-parse", "--is-inside-work-tree"])
    return result.returncode == 0 and result.stdout.strip() == b"true"


def _default_ref(root):
    """The clone's default branch, local refs only: origin/HEAD, else origin/main, else origin/master."""
    symbolic = _git(root, ["symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"])
    if symbolic.returncode == 0 and symbolic.stdout.strip():
        candidates = [symbolic.stdout.decode().strip()]
    else:
        candidates = ["refs/remotes/origin/HEAD"]
    candidates.extend(["refs/remotes/origin/main", "refs/remotes/origin/master"])
    for ref in candidates:
        verify = _git(root, ["rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}"])
        if verify.returncode == 0:
            return ref
    return None


def _blob(root, spec):
    """Blob text, or None when that spec is absent. A present spec that cannot be shown is an error."""
    if _git(root, ["cat-file", "-e", spec]).returncode != 0:
        return None
    result = _git(root, ["show", spec])
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).decode("utf-8", "replace").strip()
        raise SleepError("sleep ratchet: git show {0} failed{1}".format(spec, ": " + detail if detail else ""))
    return result.stdout.decode("utf-8", "backslashreplace")


def _merge_base(root, ref):
    """The merge base of HEAD and ref, or None when ref does not resolve here."""
    verify = _git(root, ["rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}"])
    if verify.returncode != 0:
        return None
    base = _git(root, ["merge-base", "HEAD", verify.stdout.decode().strip()])
    if base.returncode != 0:
        return None
    return base.stdout.decode().strip()


def ancestor_baselines(root, base=None, env=None):
    """(label, counts) for HEAD and for the base commit, when each already has the baseline.

    A checkout that is not a git repo, or a base commit that predates the baseline, contributes
    nothing. An explicit --base or CI_BASE_SHA that does not resolve is an error: skipping it
    would let a baseline already raised on HEAD pass. A missing default branch is not an error;
    there is no base to compare with.
    """
    if not _inside_git(root):
        return []
    env = os.environ if env is None else env
    found = []
    head_text = _blob(root, "HEAD:" + BASELINE_REL)
    if head_text is not None:
        found.append(("HEAD", parse_baseline(head_text, "HEAD:" + BASELINE_REL)))
    explicit = base or env.get("CI_BASE_SHA")
    ref = explicit or _default_ref(root)
    if not ref:
        return found
    commit = _merge_base(root, ref)
    if not commit:
        if explicit:
            raise SleepError(
                "sleep ratchet: base {0} does not resolve; refusing to skip the never-raise check".format(explicit))
        return found
    head = _git(root, ["rev-parse", "--verify", "--quiet", "HEAD"])
    if head.returncode == 0 and head.stdout.decode().strip() == commit:
        return found
    text = _blob(root, commit + ":" + BASELINE_REL)
    if text is None:
        return found
    found.append((commit[:12], parse_baseline(text, commit[:12] + ":" + BASELINE_REL)))
    return found


def _tracks_baseline(root, baseline_path):
    """True when baseline_path is this repo's committed baseline, so ancestor checks apply."""
    try:
        return baseline_path.resolve() == (Path(root) / BASELINE_REL).resolve()
    except OSError:
        return False


def _strict_previous(ancestors):
    """One previous map: the lowest count any ancestor allows, absent when one of them lacks the path."""
    if not ancestors:
        return None, ""
    paths = set()
    for _, previous in ancestors:
        paths.update(previous)
    strict = {}
    for path in paths:
        counts = []
        for _, previous in ancestors:
            if path not in previous:
                counts = None
                break
            counts.append(previous[path])
        if counts:
            strict[path] = min(counts)
    label = " and ".join(name for name, _ in ancestors)
    return strict, label


def _ancestor_violations(root, candidate, base, env):
    previous, label = _strict_previous(ancestor_baselines(root, base, env))
    if previous is None:
        return []
    return raised_against(candidate, previous, label)


def check(root, baseline_path=None, base=None, env=None):
    """Violation lines for the worktree (and a raised index) against the baseline. Empty means pass."""
    env = os.environ if env is None else env
    root = Path(root)
    baseline_path = Path(root) / BASELINE_REL if baseline_path is None else Path(baseline_path)
    if not baseline_path.is_file():
        return ["sleep ratchet: baseline missing ({0}); refusing to invent counts for existing sleeps".format(
            baseline_path)]
    baseline = parse_baseline(baseline_path.read_text(encoding="utf-8"), str(baseline_path))
    found = count_violations(scan(root), baseline)
    if not _tracks_baseline(root, baseline_path):
        return found
    found.extend(_ancestor_violations(root, baseline, base, env))
    indexed = _blob(root, ":" + BASELINE_REL) if _inside_git(root) else None
    if indexed is not None:
        index_counts = parse_baseline(indexed, "index:" + BASELINE_REL)
        if index_counts != baseline:
            found.extend(_ancestor_violations(root, index_counts, base, env))
    # The same raise against HEAD and the base is one problem.
    unique = []
    for line in found:
        if line not in unique:
            unique.append(line)
    return unique


def update(root, base=None, env=None):
    """Lower the baseline to the current counts. Refuses to raise, to add a file, or to create one."""
    env = os.environ if env is None else env
    root = Path(root)
    path = root / BASELINE_REL
    if not path.is_file():
        raise SleepError(
            "sleep ratchet: no baseline at {0}; refusing to create one, which would record existing sleeps".format(
                BASELINE_REL))
    old = parse_baseline(path.read_text(encoding="utf-8"), BASELINE_REL)
    planned, refusals = plan_update(old, scan(root))
    if refusals:
        return refusals
    refusals = _ancestor_violations(root, planned, base, env)
    if refusals:
        return refusals
    rendered = render(planned)
    current = path.read_text(encoding="utf-8")
    if current != rendered:
        path.write_text(rendered)
        print("sleep ratchet: lowered the baseline to {0} file(s)".format(len(planned)))
    else:
        print("sleep ratchet: baseline already matches ({0} file(s))".format(len(planned)))
    return []


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--base", help="base ref (default: CI_BASE_SHA, else origin/HEAD, else origin/main)")
    parser.add_argument("--root", default=".", help="repository root (default: the working directory)")
    parser.add_argument("--update", action="store_true",
                        help="lower the baseline to the current counts; refuses to raise or to add a file")
    args = parser.parse_args(argv)
    try:
        if args.update:
            problems = update(args.root, args.base)
        else:
            problems = check(args.root, base=args.base)
    except SleepError as error:
        print(error, file=sys.stderr)
        return 2
    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    if not args.update:
        tracked = len(parse_baseline((Path(args.root) / BASELINE_REL).read_text(encoding="utf-8"), BASELINE_REL))
        print("sleep ratchet: {0} Go test file(s) are at the baseline".format(tracked))
    return 0


if __name__ == "__main__":
    sys.exit(main())
