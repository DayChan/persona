"""check-fast.sh runs the scripts/ unittests a change needs (#3968), and the scripts/test suites that guard docs,
markdown, apple/ and deploy/docker/.

Each test builds a throwaway repository with check-fast.sh and stubs for the repository-wide checks it always picks
for a scripts/ change (the privacy scan, the extension schema test, the PR workflow test), commits a base, makes a
change on top and runs check-fast.sh --base against the base. The default-base tests run it without --base, against
refs/remotes/origin/* refs they write themselves.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
PASSING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_ok(self):\n        pass\n"
FAILING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_broken(self):\n        self.fail('broken on purpose')\n"
# The scripts/test suites that guard files outside scripts/; stubbed passing unless a test breaks one.
GUARD_SUITES = ("test_doc_paths.py", "test_doc_go_paths.py", "test_apple_l10n.py", "test_docker_entrypoint.py")


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastScriptsTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/tools/other.py", "")
        self.write("scripts/tools/test_other.py", FAILING)  # not related to the changes below: must not run
        self.write("scripts/tools/gone.py", "")
        self.write("scripts/tools/test_gone.py", PASSING)
        for name in GUARD_SUITES:
            self.write(f"scripts/test/{name}", PASSING)
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def break_suite(self, name):
        """Make scripts/test/<name> fail and commit it into the base, so only the change made afterwards selects it."""
        self.commit_on_top(f"scripts/test/{name}", FAILING, "base")

    def check_fast(self, *args, path=None, env=None, base="base"):
        # TMPDIR inside the throwaway repo: a failed run keeps its log directory, and the cleanup removes it.
        # base=None runs without --base, so the script picks its default base.
        env = dict(os.environ, TMPDIR=str(self.repo), **({"PATH": path} if path else {}), **(env or {}))
        args = args or (("--base", base) if base else ())
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", *args], cwd=self.repo,
                             capture_output=True, text=True, timeout=300, env=env)
        return run.returncode, run.stdout + run.stderr

    def commit_on_top(self, path, text, tag):
        """Commit one more file on top of HEAD and point tag at the new commit."""
        self.write(path, text)
        self.git("add", path)
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", tag)
        self.git("tag", "-f", tag)

    def test_without_base_it_diffs_against_origin_head(self):
        # The default branch's tip carries a failing test; a stale origin/master left at the first commit would
        # select it too, and fail the run.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/main", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        self.git("update-ref", "refs/remotes/origin/master", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/main", out)
        self.assertIn("PASS     scripts/tools/test_new.py", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_origin_head_wins_over_origin_main(self):
        # A default branch that is neither main nor master; origin/main left at the first commit would select the
        # failing test on top of it.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/trunk", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/trunk", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_a_plain_origin_head_still_counts(self):
        # origin/HEAD written as a plain ref to a commit, not as a symbolic ref.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/HEAD", "mid")
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/HEAD", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_a_dangling_origin_head_falls_back_to_origin_main(self):
        # origin/HEAD is set at clone time; it can name a branch that was deleted since (here: master).
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/main", out)
        self.assertIn("PASS     scripts/tools/test_new.py", out)

    def test_without_base_a_legacy_clone_diffs_against_origin_master(self):
        self.git("update-ref", "refs/remotes/origin/master", "base")
        self.write("scripts/tools/test_new.py", FAILING)
        code, out = self.check_fast(base=None)
        self.assertEqual(1, code, out)
        self.assertIn("merge-base with refs/remotes/origin/master", out)
        self.assertIn("FAIL     scripts/tools/test_new.py", out)

    def test_without_base_or_origin_refs_it_stops_and_says_how_to_fix_it(self):
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(2, code, out)
        self.assertIn("no default base", out)
        self.assertIn("--base <ref>", out)
        self.assertNotIn("test_new.py", out)

    def test_an_explicit_base_wins_over_origin_head(self):
        self.commit_on_top("scripts/tools/test_mid.py", PASSING, "mid")
        self.git("update-ref", "refs/remotes/origin/main", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        code, out = self.check_fast("--base", "base")
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with base", out)
        self.assertIn("PASS     scripts/tools/test_mid.py", out)

    def test_an_empty_base_is_refused_not_read_as_no_base(self):
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        for args in (("--base", ""), ("--base=",)):
            with self.subTest(args=args):
                code, out = self.check_fast(*args)
                self.assertEqual(2, code, out)
                self.assertIn("--base needs a git ref", out)
                self.assertNotIn("test_new.py", out)

    def test_a_changed_test_is_run_and_its_failure_fails(self):
        self.write("scripts/tools/test_new.py", FAILING)
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_new.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_changed_script_runs_its_test_from_its_directory(self):
        # sim-boot.sh is tested by test_sim_boot.py: a dash in the name is an underscore in the test's.
        # Run from scripts/tools, as CI does: a relative fixture path resolves only there.
        self.write("scripts/tools/fixture.txt", "x")
        self.write("scripts/tools/test_tool_name.py", PASSING.replace("pass", "open('fixture.txt').close()"))
        self.git("add", "scripts/tools/test_tool_name.py", "scripts/tools/fixture.txt")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "test")
        self.git("tag", "-f", "base")
        self.write("scripts/tools/tool-name.sh", "echo changed\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/tools/test_tool_name.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_shell_script_named_with_its_extension_runs_its_test(self):
        self.write("scripts/tools/db.sh", "echo changed\n")
        self.write("scripts/tools/test_db_sh.py", FAILING)
        self.git("add", "scripts/tools/test_db_sh.py")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "test")
        self.git("tag", "-f", "base")
        self.write("scripts/tools/db.sh", "echo changed again\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_db_sh.py", out)

    def test_a_deleted_test_is_not_run(self):
        (self.repo / "scripts/tools/test_gone.py").unlink()
        (self.repo / "scripts/tools/gone.py").unlink()
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertNotIn("test_gone.py", out)

    def test_a_doc_change_runs_the_doc_path_suites(self):
        self.break_suite("test_doc_paths.py")
        self.write("docs/x.md", "see `scripts/missing.py`\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_doc_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_go_paths.py", out)
        self.assertNotIn("test_apple_l10n.py", out)
        self.assertNotIn("test_docker_entrypoint.py", out)

    def test_a_top_level_markdown_change_runs_the_doc_path_suites(self):
        self.write("CLAUDE.md", "index\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_go_paths.py", out)
        self.assertNotIn("no fast check maps to", out)

    def test_an_apple_change_runs_the_l10n_suite(self):
        self.break_suite("test_apple_l10n.py")
        self.write("apple/Persona/Views/Settings/Options.swift", 'let s = "中文"\n')
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_apple_l10n.py", out)
        self.assertIn("SKIP  apple/** builds and tests", out)  # the Mac-only checks are still reported
        self.assertNotIn("test_doc_paths.py", out)

    def test_a_docker_change_runs_the_entrypoint_suite(self):
        self.break_suite("test_docker_entrypoint.py")
        self.write("deploy/docker/entrypoint.sh", "echo changed\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_docker_entrypoint.py", out)
        self.assertNotIn("no fast check maps to", out)
        self.assertNotIn("test_doc_paths.py", out)

    def test_a_root_dockerfile_change_runs_the_entrypoint_suite(self):
        # test_docker_entrypoint.py asserts the EXPOSE line of the repo-root Dockerfile.
        self.break_suite("test_docker_entrypoint.py")
        self.write("Dockerfile", "EXPOSE 18660\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_docker_entrypoint.py", out)
        self.assertNotIn("no fast check maps to", out)

    def test_the_docker_doc_runs_both_the_doc_and_the_entrypoint_suites(self):
        self.write("docs/operations/docker.md", "changed\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_docker_entrypoint.py", out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)

    def test_all_runs_every_scripts_suite_in_one_job(self):
        # Mirror CI's Scripts tests job: --all runs every scripts/**/test_*.py, changed or not, except the ones a
        # dedicated job already runs. That stub fails under unittest, so the suite job must not pick it up. The count
        # is 7: test_other, test_gone, test_deep and the four guard suites stubbed in setUp.
        self.write("scripts/test/test_external_service_refs.py", FAILING)
        self.write("scripts/deep/nested/test_deep.py", PASSING)
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # missing node and go are core failures in --all mode
        self.assertIn("queued: scripts unittest suites (7)", out)
        self.assertIn("FAIL     scripts unittest suites", out)
        self.assertIn("7 suite(s): 1 failed, 0 timed out", out)
        self.assertIn("failed: scripts/tools/test_other.py", out)
        self.assertNotIn("failed: scripts/test/test_external_service_refs.py", out)
        self.assertNotIn("queued: scripts/tools/test_gone.py", out)  # one job, not one per suite

    def test_all_reports_a_slow_suite_as_timeout_and_runs_the_rest(self):
        self.write("scripts/tools/test_other.py", PASSING)
        self.write("scripts/tools/test_slow.py", PASSING.replace("pass", "__import__('time').sleep(30)"))
        self.write("scripts/tools/test_z_after.py", FAILING)  # sorted after the slow one: it still runs
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go(),
                                    env={"CHECK_FAST_TIMEOUT_OTHER": "2"})
        self.assertEqual(1, code, out)
        self.assertIn("8 suite(s): 1 failed, 1 timed out", out)
        self.assertIn("timed out: scripts/tools/test_slow.py", out)
        self.assertIn("failed: scripts/tools/test_z_after.py", out)
        self.write("scripts/tools/test_z_after.py", PASSING)
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go(),
                                    env={"CHECK_FAST_TIMEOUT_OTHER": "2"})
        self.assertIn("TIMEOUT  scripts unittest suites (8)", out)
        self.assertIn("1 suite(s) timed out: scripts/tools/test_slow.py", out)

    def toolchain_without_node_and_go(self):
        # A PATH with only the tools check-fast.sh itself needs, so node and go are missing on any machine.
        bin_dir = self.repo / ".bin"
        if bin_dir.is_dir():
            return str(bin_dir)
        bin_dir.mkdir()
        for tool in ("bash", "python3", "timeout", "gtimeout", "git", "mktemp", "rm", "dirname", "basename", "find",
                     "sort", "tee", "tail", "grep", "sed", "awk", "tr", "wc", "cut", "cat", "env"):
            found = shutil.which(tool)
            if found:
                (bin_dir / tool).symlink_to(found)
        return str(bin_dir)


FAKE_GO = '#!/bin/sh\nprintf "%s|%s\\n" "${PWD##*/}" "$*" >> "$FAKE_GO_LOG"\n'


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastServerInputsTests(unittest.TestCase):
    """Repository inputs outside server/ that server tests read run go test on the packages that read them."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/test/server_test_env.py", "")
        self.write("scripts/test/test_sdk_bundle.py", "")  # a schema/ change also runs the sdk bundle and sdk/go jobs
        self.write("sdk/go/go.mod", "module example.com/sdk\n")
        self.write("spec/fixtures/x.json", "{}\n")
        self.write("spec/tokens/colors.json", "{}\n")
        self.write("docs/example-guide.md", "guide\n")
        self.write("docs/other.md", "other\n")
        self.write("schema/extension/examples/Manifest.x.json", "{}\n")
        self.write("server/go.mod", "module example.com/server\n")
        self.write("server/internal/foo/foo_test.go", 'package foo\n\nconst dir = "../../../spec/fixtures/x"\n')
        self.write("server/internal/joined/joined_test.go",
                   'package joined\n\nvar dir = filepath.Join("..", "..", "..", "spec", "fixtures", "wire")\n')
        self.write("server/internal/noted/noted_test.go", 'package noted\n\n// Same as spec/fixtures/x.json.\n')
        self.write("server/internal/bar/bar_test.go", "package bar\n")
        self.write("server/internal/guide/conformance/guide_test.go",
                   'package conformance\n\nconst guide = "docs/example-guide.md"\n')
        self.write("server/internal/ext/ext_test.go",
                   'package ext\n\nvar d = filepath.Join("..", "schema", "extension", "examples")\n')
        self.write("server/internal/db/dbtest/env_test.go",
                   'package dbtest\n\nconst launcher = "../../../../scripts/test/server_test_env.py"\n')
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")
        # A fake go outside the repository records each call ("<cwd basename>|<args>") instead of building anything.
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-go-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        (bin_dir / "go").write_text(FAKE_GO)
        (bin_dir / "go").chmod(0o755)
        self.go_log = bin_dir / "calls.log"
        self.env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                    "FAKE_GO_LOG": str(self.go_log)}

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def check_fast(self):
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--base", "base"], cwd=self.repo, env=self.env,
                             capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        self.assertEqual(0, run.returncode, out)
        calls = self.go_log.read_text().splitlines() if self.go_log.exists() else []
        # The package pattern is the last argument of each recorded "go test ... ./internal/<pkg>/..." call.
        return out, sorted(call.split()[-1] for call in calls if call.startswith("server|test "))

    def test_a_spec_fixture_runs_the_packages_that_read_it(self):
        self.write("spec/fixtures/x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertIn("queued: server go test ./internal/foo/...", out)
        # filepath.Join elements name the fixtures too; a // comment that mentions them is not a reader.
        self.assertEqual(["./internal/foo/...", "./internal/joined/..."], packages, out)
        self.assertNotIn("no fast check maps to", out)

    def test_other_spec_files_stay_unmapped(self):
        self.write("spec/tokens/colors.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.assertIn("no fast check maps to: spec/tokens/colors.json", out)

    def test_a_doc_runs_only_the_package_that_reads_it(self):
        self.write("docs/example-guide.md", "guide changed\n")
        self.write("docs/other.md", "other changed\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/guide/conformance/..."], packages, out)

    def test_a_schema_example_runs_its_readers(self):
        self.write("schema/extension/examples/Manifest.x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual(["./internal/ext/..."], packages, out)

    def test_the_server_test_launcher_runs_its_reader(self):
        self.write("scripts/test/server_test_env.py", "# changed\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/db/dbtest/..."], packages, out)


if __name__ == "__main__":
    unittest.main()
