"""Fixed time.Sleep calls in Go tests can stay or fall, never rise.

Comments, string literals and a // sleep-ok: <reason> line do not count. The cases build
throwaway trees; one case checks this checkout's committed baseline.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import sleep_ratchet

ROOT = Path(__file__).resolve().parents[2]
SERVER = "server/internal/demo/demo_test.go"
SDK = "sdk/go/ext/ext_test.go"


def source(sleeps):
    body = "\n".join("\ttime.Sleep(time.Second)" for _ in range(sleeps))
    return "package demo\n\nimport \"time\"\n\nfunc TestWait(t *testing.T) {\n" + body + "\n}\n"


class CountTests(unittest.TestCase):
    def test_calls_count_and_lookalikes_do_not(self):
        text = "\n".join([
            "package demo",
            "func TestA(t *testing.T) {",
            "\ttime.Sleep(time.Second)",
            "\ttime.Sleep(time.Millisecond)",
            "\ttime.Now()",
            "\ttime.Sleeper()",
            "\tmytime.Sleep(time.Second)",
            "\truntime.Sleep(time.Second)",
            "\ttimeout.Sleep(time.Second)",
            "}",
        ])
        self.assertEqual(2, sleep_ratchet.count_sleeps(text))

    def test_comments_and_strings_are_not_calls(self):
        text = "\n".join([
            "package demo",
            "// time.Sleep(time.Second)",
            "/* time.Sleep(time.Second)",
            "still time.Sleep(time.Second)",
            "*/",
            'const interpreted = "time.Sleep(\\"time.Sleep(\\""',
            "const raw = `time.Sleep(",
            "time.Sleep(",
            "`",
            "func TestA(t *testing.T) { time.Sleep(time.Second) }",
        ])
        self.assertEqual(1, sleep_ratchet.count_sleeps(text))

    def test_a_call_split_across_lines_still_counts(self):
        text = "package demo\nfunc TestA(t *testing.T) {\n\ttime\n\t\t.\n\t\tSleep\n\t\t(\n\t\ttime.Second)\n}\n"
        self.assertEqual(1, sleep_ratchet.count_sleeps(text))
        commented = "package demo\nfunc TestA(t *testing.T) {\n\ttime /* pkg */ . /* name */ Sleep(time.Second)\n}\n"
        self.assertEqual(1, sleep_ratchet.count_sleeps(commented))

    def test_sleep_ok_needs_a_real_comment_and_a_reason(self):
        kept = "\ttime.Sleep(time.Second) // sleep-ok: the child exits on its own"
        self.assertEqual(0, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + kept + "\n}\n"))
        empty = "\ttime.Sleep(time.Second) // sleep-ok:"
        blank = "\ttime.Sleep(time.Second) // sleep-ok:   "
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + empty + "\n}\n"))
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + blank + "\n}\n"))
        both = "\ttime.Sleep(time.Second); time.Sleep(time.Millisecond) // sleep-ok: one bounded wait"
        self.assertEqual(0, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + both + "\n}\n"))
        # The marker has to be the comment itself, on the call's line, and not text inside a string.
        decoy = '\ttime.Sleep(time.Second); s := "// sleep-ok: reason"'
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + decoy + "\n}\n"))
        raw = "\ttime.Sleep(time.Second); s := `// sleep-ok: reason`"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + raw + "\n}\n"))
        block = "\ttime.Sleep(time.Second) /* sleep-ok: reason */"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + block + "\n}\n"))
        earlier = "\t// sleep-ok: reason\n\ttime.Sleep(time.Second)"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + earlier + "\n}\n"))
        aside = "\ttime.Sleep(time.Second) // see sleep-ok: reason"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + aside + "\n}\n"))

    def test_the_escape_on_the_parenthesis_line_covers_a_split_call(self):
        split = "\ttime.\n\tSleep(time.Second) // sleep-ok: covers the call"
        self.assertEqual(0, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + split + "\n}\n"))
        wrong_line = "\ttime. // sleep-ok: not the call line\n\tSleep(time.Second)"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + wrong_line + "\n}\n"))

    def test_crlf_and_a_block_comment_do_not_shift_the_count(self):
        text = "/*\r\ntime.Sleep(time.Second)\r\n*/\r\ntime.Sleep(time.Second)\r\n"
        self.assertEqual(1, sleep_ratchet.count_sleeps(text))


class TreeTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="sleep-ratchet-"))
        self.addCleanup(self._cleanup)
        self._saved_base = os.environ.pop("CI_BASE_SHA", None)

    def _cleanup(self):
        if self._saved_base is None:
            os.environ.pop("CI_BASE_SHA", None)
        else:
            os.environ["CI_BASE_SHA"] = self._saved_base
        subprocess.run(["rm", "-rf", str(self.repo)], check=True)

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def baseline(self, counts):
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render(counts))

    def run_check(self, *args):
        return sleep_ratchet.main(["--root", str(self.repo), *args])

    def test_a_new_file_starts_at_zero_unless_every_call_is_escaped(self):
        self.baseline({})
        self.write(SERVER, source(1))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn(SERVER, err)
        self.assertIn("baseline 0", err)
        self.assertIn("docs/lessons/testing.md", err)
        self.assertIn("synctest", err)
        self.assertIn("polling helper", err)
        self.assertIn("channel", err)
        self.assertEqual(1, len([line for line in err.splitlines() if line.strip()]))
        self.write(SERVER, "package demo\n\nfunc TestWait(t *testing.T) {\n"
                   "\ttime.Sleep(time.Second) // sleep-ok: waits for the helper process\n}\n")
        code, out = self._run()
        self.assertEqual(0, code, out)

    def test_over_and_under_name_the_file_and_under_points_at_update(self):
        self.baseline({SERVER: 1})
        self.write(SERVER, source(2))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn("2 time.Sleep call(s), baseline 1", err)
        self.write(SERVER, source(0))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn("0 time.Sleep call(s), baseline 1", err)
        self.assertIn("python3 scripts/ci/sleep_ratchet.py --update", err)

    def test_other_trees_are_ignored(self):
        self.baseline({})
        self.write("web/src/demo_test.go", source(3))
        self.write("notes/demo_test.go", source(3))
        code, out = self._run()
        self.assertEqual(0, code, out)

    def test_update_lowers_and_drops_a_deleted_file_but_refuses_to_raise_or_add(self):
        self.baseline({SERVER: 2, SDK: 1})
        self.write(SERVER, source(3))
        self.write(SDK, source(1))
        code, err = self._update()
        self.assertEqual(1, code)
        self.assertIn("refusing to raise", err)
        self.assertEqual((self.repo / sleep_ratchet.BASELINE_REL).read_text(),
                         sleep_ratchet.render({SERVER: 2, SDK: 1}))
        self.write("server/internal/demo/extra_test.go", source(1))
        self.write(SERVER, source(1))
        code, err = self._update()
        self.assertEqual(1, code)
        self.assertIn("refusing to add server/internal/demo/extra_test.go", err)
        self.assertIn('"server/internal/demo/demo_test.go": 2', (self.repo / sleep_ratchet.BASELINE_REL).read_text())
        (self.repo / "server/internal/demo/extra_test.go").unlink()
        (self.repo / SDK).unlink()
        self.write(SERVER, source(1))
        code, out = self._update()
        self.assertEqual(0, code, out)
        self.assertIn("lowered", out)
        self.assertEqual({SERVER: 1}, json.loads((self.repo / sleep_ratchet.BASELINE_REL).read_text()))
        code, out = self._update()
        self.assertEqual(0, code, out)
        self.assertIn("already matches", out)

    def test_update_refuses_to_create_a_baseline(self):
        self.write(SERVER, source(1))
        code, err = self._update()
        self.assertEqual(2, code)
        self.assertIn("refusing to create", err)
        self.assertFalse((self.repo / sleep_ratchet.BASELINE_REL).exists())

    def test_a_missing_baseline_fails_closed(self):
        self.write(SERVER, source(0))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn("baseline missing", err)

    def _run(self):
        return self._capture(self.run_check)

    def _update(self):
        return self._capture(lambda: self.run_check("--update"))

    def _capture(self, run):
        from io import StringIO
        out, err = StringIO(), StringIO()
        stdout, stderr = sys.stdout, sys.stderr
        try:
            sys.stdout, sys.stderr = out, err
            code = run()
        finally:
            sys.stdout, sys.stderr = stdout, stderr
        return code, out.getvalue() + err.getvalue()


class GitTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="sleep-ratchet-git-"))
        self.addCleanup(self._cleanup)
        self._saved_base = os.environ.pop("CI_BASE_SHA", None)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.write(SERVER, source(1))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1}))
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "baseline")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

    def _cleanup(self):
        if self._saved_base is None:
            os.environ.pop("CI_BASE_SHA", None)
        else:
            os.environ["CI_BASE_SHA"] = self._saved_base
        subprocess.run(["rm", "-rf", str(self.repo)], check=True)

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True, timeout=30)

    def _capture(self, args):
        from io import StringIO
        out, err = StringIO(), StringIO()
        stdout, stderr = sys.stdout, sys.stderr
        try:
            sys.stdout, sys.stderr = out, err
            code = sleep_ratchet.main(["--root", str(self.repo), *args])
        finally:
            sys.stdout, sys.stderr = stdout, stderr
        return code, out.getvalue() + err.getvalue()

    def test_a_clean_tree_passes_and_a_hand_raised_baseline_fails(self):
        code, out = self._capture([])
        self.assertEqual(0, code, out)
        self.assertIn("1 Go test file", out)
        self.write(SERVER, source(2))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 2}))
        code, err = self._capture([])
        self.assertEqual(1, code)
        self.assertIn("baseline raises", err)
        self.assertIn("baselines are never raised", err)

    def test_the_first_snapshot_is_allowed_when_the_base_has_no_baseline(self):
        # An older commit has the test and no baseline. The matching baseline on top of that
        # commit is the first snapshot, so there is no ancestor to raise against.
        self.git("checkout", "-q", "--orphan", "older")
        self.git("rm", "-q", "-f", "--cached", sleep_ratchet.BASELINE_REL)
        (self.repo / sleep_ratchet.BASELINE_REL).unlink()
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "tests only")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1}))
        code, out = self._capture([])
        self.assertEqual(0, code, out)

    def test_update_refuses_a_hand_raised_baseline_that_already_matches_the_code(self):
        self.write(SERVER, source(2))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 2}))
        code, err = self._capture(["--update"])
        self.assertEqual(1, code, err)
        self.assertIn("never raised", err)
        self.assertEqual({SERVER: 2}, json.loads((self.repo / sleep_ratchet.BASELINE_REL).read_text()))

    def test_update_lowers_against_a_committed_baseline_and_refuses_a_raise(self):
        self.write(SERVER, source(2))
        code, err = self._capture(["--update"])
        self.assertEqual(1, code)
        self.assertIn("refusing to raise", err)
        self.assertIn('"server/internal/demo/demo_test.go": 1',
                      (self.repo / sleep_ratchet.BASELINE_REL).read_text())
        self.write(SERVER, source(0))
        code, out = self._capture(["--update"])
        self.assertEqual(0, code, out)
        self.assertEqual({}, json.loads((self.repo / sleep_ratchet.BASELINE_REL).read_text()))
        code, out = self._capture([])
        self.assertEqual(0, code, out)

    def test_a_staged_raise_fails_even_when_the_worktree_file_was_restored(self):
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 4}))
        self.git("add", "--", sleep_ratchet.BASELINE_REL)
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1}))
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertIn("baseline raises", err)

    def test_an_unresolvable_ci_base_sha_does_not_skip_the_raise_check(self):
        # A baseline already raised on HEAD must not pass just because CI_BASE_SHA is missing from
        # this clone. migration_freeze exits 2 in that case; so does this check.
        self.write(SERVER, source(3))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 3}))
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "raised")
        parent = subprocess.check_output(
            ["git", "-C", str(self.repo), "rev-parse", "HEAD~1"], text=True, timeout=30).strip()
        os.environ["CI_BASE_SHA"] = parent
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertEqual(1, err.count("baseline raises"))
        os.environ["CI_BASE_SHA"] = "0123456789abcdef0123456789abcdef01234567"
        code, err = self._capture([])
        self.assertEqual(2, code, err)
        self.assertIn("does not resolve", err)
        self.assertNotIn("Traceback", err)

    def test_a_raise_against_head_and_the_base_is_one_line(self):
        self.git("commit", "--allow-empty", "-q", "-m", "move head")
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 4}))
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertEqual(1, err.count("baseline raises"))
        self.assertIn("HEAD and ", err)

    def test_a_git_timeout_is_a_one_line_error(self):
        real = subprocess.run

        def timed_out(command, **kwargs):
            if "merge-base" in command:
                raise subprocess.TimeoutExpired(command, 60)
            return real(command, **kwargs)

        with patch("sleep_ratchet.subprocess.run", side_effect=timed_out):
            code, err = self._capture([])
        self.assertEqual(2, code, err)
        self.assertIn("timed out", err)
        self.assertNotIn("Traceback", err)


class RepoTests(unittest.TestCase):
    def test_this_checkout_matches_its_baseline(self):
        code, out = _capture_main(["--root", str(ROOT)])
        self.assertEqual(0, code, out)

    def test_the_baseline_file_is_canonical(self):
        path = ROOT / sleep_ratchet.BASELINE_REL
        text = path.read_text(encoding="utf-8")
        self.assertEqual(text, sleep_ratchet.render(sleep_ratchet.parse_baseline(text, sleep_ratchet.BASELINE_REL)))

    def test_guards_and_check_fast_run_the_checker(self):
        guards = (ROOT / "scripts/ci/guards.sh").read_text()
        check_fast = (ROOT / "scripts/test/check-fast.sh").read_text()
        self.assertIn("python3 scripts/ci/sleep_ratchet.py", guards)
        self.assertIn("python3 scripts/ci/sleep_ratchet.py", check_fast)
        self.assertIn("server/*_test.go", check_fast)
        self.assertIn("sdk/go/*_test.go", check_fast)


def _capture_main(args):
    from io import StringIO
    out, err = StringIO(), StringIO()
    stdout, stderr = sys.stdout, sys.stderr
    try:
        sys.stdout, sys.stderr = out, err
        code = sleep_ratchet.main(args)
    finally:
        sys.stdout, sys.stderr = stdout, stderr
    return code, out.getvalue() + err.getvalue()


if __name__ == "__main__":
    unittest.main()
