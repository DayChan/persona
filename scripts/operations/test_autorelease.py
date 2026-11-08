#!/usr/bin/env python3
"""Integration tests using local Git repositories and isolated fake argv adapters."""
from __future__ import annotations

import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("autorelease.py")
SPEC = importlib.util.spec_from_file_location("autorelease", SCRIPT)
autorelease = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(autorelease)

FAKE = r'''
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

kind, name, mode_path, log_path, *args = sys.argv[1:]
mode = Path(mode_path).read_text().strip()
entry = {"kind": kind, "target": name, "args": args, "cwd": os.getcwd()}
if kind == "action":
    entry["head"] = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    entry["checkpoint"] = os.environ["PERSONA_AUTORELEASE_CHECKPOINT"]
    entry["selected_environment"] = os.environ.get("EXAMPLE_SETTING")
with Path(log_path).open("a") as stream:
    stream.write(json.dumps(entry) + "\n")
print("EXAMPLE_SECRET_SENTINEL", flush=True)
print("EXAMPLE_SECRET_SENTINEL", file=sys.stderr, flush=True)
if mode == "checkpoint":
    checkpoint = Path(entry["checkpoint"])
    if not checkpoint.exists():
        checkpoint.write_text(json.dumps({"uploaded": True}))
        checkpoint.chmod(0o600)
        with Path(log_path).open("a") as stream:
            stream.write(json.dumps({"kind": "upload", "build": args[2]}) + "\n")
    sys.exit(9 if Path(mode_path + ".invalid").exists() else 0)
if mode == "timeout":
    # The leader exits on TERM, but the descendant deliberately ignores TERM.
    child = subprocess.Popen([sys.executable, "-c",
        "import signal,time; from pathlib import Path; "
        "signal.signal(signal.SIGTERM, signal.SIG_IGN); "
        "Path(__import__('sys').argv[1]).write_text(str(__import__('os').getpid())); time.sleep(60)",
        mode_path + ".pid"])
    time.sleep(60)
if mode == "wait":
    deadline = time.monotonic() + 10
    while not Path(mode_path + ".gate").exists():
        if time.monotonic() >= deadline:
            sys.exit(8)
        time.sleep(0.02)
if mode == "dirty":
    Path("adapter-untracked").write_text("preserve me")
if mode == "change-head":
    subprocess.check_call(["git", "checkout", "--detach", "HEAD~1"], stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL)
if mode == "fail":
    sys.exit(9)
if mode == "busy":
    sys.exit(75)
'''


class AutoreleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="autorelease-tests-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        self.env.update(HOME=str(self.root), GIT_CONFIG_NOSYSTEM="1", LC_ALL="C")
        self.origin = self.root / "origin.git"
        self.repo = self.root / "repo"
        self.worktree = self.root / "release"
        self.state_dir = self.root / "state"
        self.config_path = self.root / "private.json"
        self.fake = self.root / "fake.py"
        self.fake.write_text(FAKE)
        self.action_log = self.root / "actions.jsonl"
        self.notify_log = self.root / "notifications.jsonl"
        self.notify_mode = self.root / "notify-mode"
        self.notify_mode.write_text("success")
        self.modes = {}
        self.git(self.root, "init", "--bare", str(self.origin))
        self.git(self.root, "init", "-b", "main", str(self.repo))
        self.git(self.repo, "config", "user.name", "Example Contributor")
        self.git(self.repo, "config", "user.email", "contributor@example.com")
        self.git(self.repo, "config", "commit.gpgsign", "false")
        self.git(self.repo, "remote", "add", "origin", str(self.origin))
        self.initial = self.commit({"server/a.go": "initial", "web/a.js": "initial",
                                    "apple/a.swift": "initial", ".gitignore": "ignored/\n"})
        self.config = {
            "repo": str(self.repo), "worktree": str(self.worktree),
            "state_dir": str(self.state_dir), "git_proxy": "",
            "targets": {}, "environment": {"EXAMPLE_SETTING": "configured-value"},
            "notify_command": [sys.executable, str(self.fake), "notify", "worker",
                               str(self.notify_mode), str(self.notify_log), "{message}"],
            "git_timeout": 10, "notify_timeout": 1,
        }
        self.target("hub", ["server/"])
        self.write_config()

    def git(self, cwd, *args):
        return subprocess.check_output(["git", "-C", str(cwd), *args], env=self.env,
                                       stderr=subprocess.DEVNULL, timeout=10).decode().strip()

    def commit(self, files, push=True):
        for relative, content in files.items():
            path = self.repo / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            if content is None:
                path.unlink()
            else:
                path.write_text(content)
        self.git(self.repo, "add", "-A")
        self.git(self.repo, "commit", "-m", "Update fixture")
        head = self.git(self.repo, "rev-parse", "HEAD")
        if push:
            self.git(self.repo, "push", "origin", "main")
        return head

    def target(self, name, paths, timeout=3):
        mode = self.root / (name + "-mode")
        mode.write_text("success")
        self.modes[name] = mode
        self.config["targets"][name] = {
            "paths": paths, "timeout": timeout,
            "command": [sys.executable, str(self.fake), "action", name, str(mode),
                        str(self.action_log), "{commit}", "{worktree}", "{build}"],
        }

    def write_config(self):
        self.config_path.write_text(json.dumps(self.config))
        self.config_path.chmod(0o600)

    def worker_argv(self, *flags):
        self.write_config()
        return [sys.executable, "-B", str(SCRIPT), "--config", str(self.config_path), *flags]

    def invoke(self, *flags, code=0, env=None):
        result = subprocess.run(self.worker_argv(*flags), env=env or self.env,
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, code, result.stderr + result.stdout)
        self.assertEqual(len(result.stdout.splitlines()), 1)
        self.assertIn("verdict=", result.stdout)
        self.assertNotIn("EXAMPLE_SECRET_SENTINEL", result.stdout + result.stderr)
        self.assertNotIn(str(self.fake), result.stdout + result.stderr)
        return result

    def state(self):
        return json.loads((self.state_dir / "state.json").read_text())

    def entries(self, path):
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def actions(self, target=None):
        return [entry for entry in self.entries(self.action_log)
                if entry["kind"] == "action" and (target is None or entry["target"] == target)]

    def wait_for(self, predicate, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(0.02)
        self.fail("Timed out waiting for the fixture condition")

    def launch(self, *flags):
        process = subprocess.Popen(self.worker_argv(*flags), env=self.env,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.addCleanup(self.stop, process)
        return process

    def stop(self, process):
        if process.poll() is None:
            process.terminate()
        try:
            process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=3)

    def test_initial_runs_all_targets_without_a_baseline(self):
        self.target("web", ["never-matched/"])
        self.target("testflight", ["also-absent/*.swift"])
        self.invoke()
        self.assertEqual({entry["target"] for entry in self.actions()}, {"hub", "web", "testflight"})
        for entry in self.actions():
            self.assertEqual(entry["args"][0], self.initial)
            self.assertEqual(entry["head"], self.initial)
            self.assertEqual(entry["cwd"], str(self.worktree))
            self.assertEqual(entry["selected_environment"], "configured-value")
            self.assertTrue(Path(entry["checkpoint"]).is_relative_to(self.state_dir))
        self.assertTrue(all(record["last_success"] == self.initial
                            for record in self.state()["targets"].values()))
        self.assertFalse(self.notify_log.exists())
        self.assertEqual(self.git(self.repo, "symbolic-ref", "--short", "HEAD"), "main")
        self.assertEqual(self.git(self.repo, "status", "--porcelain"), "")
        self.assertEqual((self.state_dir / "state.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.state_dir / "autorelease.lock").stat().st_mode & 0o777, 0o600)
        self.invoke()
        self.assertEqual(len(self.actions()), 3)

    def test_unrelated_does_not_advance_success_and_diff_uses_success(self):
        self.target("web", ["web/"])
        self.invoke()
        head = self.commit({"notes.txt": "unrelated"})
        self.invoke()
        state = self.state()["targets"]
        self.assertEqual(state["hub"]["last_success"], self.initial)
        self.assertEqual(state["hub"]["last_checked"], head)
        self.assertEqual(len(self.actions()), 2)
        relevant = self.commit({"server/a.go": "changed"})
        self.modes["hub"].write_text("fail")
        self.invoke(code=1)
        self.assertEqual(self.state()["targets"]["hub"]["last_success"], self.initial)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)
        newest = self.commit({"notes.txt": "another unrelated change"})
        self.modes["hub"].write_text("success")
        self.invoke()
        self.assertEqual(self.actions("hub")[-2]["args"][0], relevant)
        self.assertEqual(self.actions("hub")[-1]["args"][0], newest)
        self.assertEqual(self.state()["targets"]["hub"]["last_success"], newest)
        self.assertEqual(self.state()["targets"]["web"]["last_success"], self.initial)
        self.assertEqual(len(self.entries(self.notify_log)), 1)

    def test_two_failures_halt_and_new_head_resets_budget(self):
        self.modes["hub"].write_text("fail")
        self.invoke(code=1)
        first = self.actions()[0]
        self.invoke(code=1)
        self.assertEqual(first["args"][2], self.actions()[1]["args"][2])
        before = (self.state_dir / "state.json").read_bytes()
        result = self.invoke(code=1)
        self.assertIn("result=halted", result.stderr)
        self.assertEqual(len(self.actions()), 2)
        self.assertEqual((self.state_dir / "state.json").read_bytes(), before)
        self.assertEqual(len(self.entries(self.notify_log)), 2)
        self.commit({"server/a.go": "intermediate"})
        newest = self.commit({"server/a.go": "latest"})
        self.invoke(code=1)
        self.assertEqual(len(self.actions()), 3)
        self.assertEqual(self.actions()[-1]["args"][0], newest)
        self.assertNotEqual(self.actions()[-1]["args"][2], first["args"][2])
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)
        self.modes["hub"].write_text("success")
        self.invoke()
        self.assertEqual(self.state()["targets"]["hub"]["last_success"], newest)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 0)

    def test_failure_continues_other_targets_and_all_steps_must_succeed(self):
        self.target("web", ["web/"])
        hub = self.config["targets"]["hub"]
        successful = list(hub["command"])
        separate = self.root / "step-one-mode"
        separate.write_text("success")
        successful[4] = str(separate)
        hub["command"] = [successful, hub["command"]]
        self.modes["hub"].write_text("fail")
        self.invoke(code=1)
        records = self.state()["targets"]
        self.assertNotIn("last_success", records["hub"])
        self.assertEqual(records["hub"]["failures"], 1)
        self.assertEqual(records["web"]["last_success"], self.initial)
        self.assertIn("result=step-2-exit-9", self.entries(self.notify_log)[0]["args"][0])
        self.assertEqual(len(self.actions("hub")), 2)

    def test_checkpoint_preserves_build_and_does_not_repeat_upload(self):
        self.config["targets"] = {}
        self.target("testflight", ["apple/"])
        self.modes["testflight"].write_text("checkpoint")
        invalid = Path(str(self.modes["testflight"]) + ".invalid")
        invalid.write_text("not VALID yet")
        self.invoke(code=1)
        self.assertNotIn("last_success", self.state()["targets"]["testflight"])
        invalid.unlink()
        self.invoke()
        self.assertEqual(self.actions()[0]["args"][2], self.actions()[1]["args"][2])
        self.assertEqual(len([e for e in self.entries(self.action_log) if e["kind"] == "upload"]), 1)
        self.assertEqual(self.state()["targets"]["testflight"]["last_success"], self.initial)

    def test_outbox_retries_next_round_without_extra_action_failure(self):
        self.modes["hub"].write_text("fail")
        self.notify_mode.write_text("fail")
        self.invoke(code=1)
        message = self.state()["outbox"][0]
        self.assertEqual(len(self.entries(self.notify_log)), 1)
        self.notify_mode.write_text("success")
        self.modes["hub"].write_text("success")
        self.invoke()
        self.assertEqual(self.state()["outbox"], [])
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 0)
        self.assertEqual([entry["args"][0] for entry in self.entries(self.notify_log)], [message, message])

    def test_dry_run_is_read_only_even_with_outbox_and_halted_budget(self):
        self.invoke("--dry-run")
        self.assertFalse(self.state_dir.exists())
        self.assertFalse(self.worktree.exists())
        self.assertFalse(self.action_log.exists())
        self.assertFalse(self.notify_log.exists())
        self.modes["hub"].write_text("fail")
        self.notify_mode.write_text("fail")
        self.invoke(code=1)
        self.invoke(code=1)
        state_bytes = (self.state_dir / "state.json").read_bytes()
        actions_bytes = self.action_log.read_bytes()
        notifications_bytes = self.notify_log.read_bytes()
        previous_head = self.git(self.worktree, "rev-parse", "HEAD")
        self.commit({"server/a.go": "new remote head"})
        # Clear the locally updated tracking ref to prove dry-run never fetches.
        self.git(self.repo, "update-ref", "refs/remotes/origin/main", previous_head)
        self.invoke("--dry-run", code=1)
        self.assertEqual(self.git(self.repo, "rev-parse", "origin/main"), previous_head)
        self.assertEqual(self.git(self.worktree, "rev-parse", "HEAD"), previous_head)
        self.assertEqual((self.state_dir / "state.json").read_bytes(), state_bytes)
        self.assertEqual(self.action_log.read_bytes(), actions_bytes)
        self.assertEqual(self.notify_log.read_bytes(), notifications_bytes)

    def test_injected_failure_only_notifies_with_separate_outbox(self):
        self.invoke()
        before = (self.state_dir / "state.json").read_bytes()
        actions_before = self.action_log.read_bytes()
        self.notify_mode.write_text("fail")
        self.invoke("--inject-failure", code=1)
        injection = json.loads((self.state_dir / "injection.json").read_text())
        self.assertEqual(len(injection["outbox"]), 1)
        self.assertEqual(injection["targets"], {})
        self.notify_mode.write_text("success")
        self.invoke("--inject-failure", code=1)
        self.assertEqual(json.loads((self.state_dir / "injection.json").read_text())["outbox"], [])
        self.assertEqual((self.state_dir / "state.json").read_bytes(), before)
        self.assertEqual(self.action_log.read_bytes(), actions_before)
        self.assertEqual(len(self.entries(self.notify_log)), 3)

    def test_injected_failure_needs_no_checkout_or_fetch(self):
        self.config["repo"] = str(self.root / "absent-repo")
        self.invoke("--inject-failure", code=1)
        self.assertFalse(self.worktree.exists())
        self.assertFalse((self.state_dir / "state.json").exists())
        self.assertFalse(self.action_log.exists())
        self.assertEqual(len(self.entries(self.notify_log)), 1)

    def test_concurrent_worker_lock_does_not_charge_budget(self):
        self.modes["hub"].write_text("wait")
        first = self.launch()
        self.wait_for(lambda: len(self.actions()) == 1)
        state_before = (self.state_dir / "state.json").read_bytes()
        result = self.invoke(code=75)
        self.assertIn("verdict=lock-busy", result.stdout)
        self.assertEqual((self.state_dir / "state.json").read_bytes(), state_before)
        self.assertEqual(len(self.actions()), 1)
        self.assertFalse(self.notify_log.exists())
        Path(str(self.modes["hub"]) + ".gate").write_text("release")
        first.communicate(timeout=10)
        self.assertEqual(first.returncode, 0)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 0)

    def test_worktree_lock_covers_different_state_dirs(self):
        self.modes["hub"].write_text("wait")
        first = self.launch()
        self.wait_for(lambda: len(self.actions()) == 1)
        original_dir = self.state_dir
        self.state_dir = self.root / "second-state"
        self.config["state_dir"] = str(self.state_dir)
        self.invoke(code=75)
        self.assertFalse((self.state_dir / "state.json").exists())
        self.assertFalse(self.notify_log.exists())
        self.assertEqual(len(self.actions()), 1)
        self.assertEqual(json.loads((original_dir / "state.json").read_text())["targets"]["hub"]["failures"], 0)
        Path(str(self.modes["hub"]) + ".gate").write_text("release")
        first.communicate(timeout=10)
        self.assertEqual(first.returncode, 0)

    def test_action_exit_75_is_deferred_and_other_targets_continue(self):
        self.target("web", ["web/"])
        self.modes["hub"].write_text("busy")
        self.invoke(code=75)
        self.invoke(code=75)
        self.invoke(code=75)
        hub = self.state()["targets"]["hub"]
        self.assertEqual(hub["failures"], 0)
        self.assertNotIn("last_success", hub)
        self.assertEqual(self.state()["targets"]["web"]["last_success"], self.initial)
        self.assertEqual(len(self.actions("hub")), 3)
        self.assertEqual(len(self.actions("web")), 1)
        self.assertFalse(self.notify_log.exists())
        self.modes["hub"].write_text("success")
        self.invoke()
        self.assertEqual(self.state()["targets"]["hub"]["last_success"], self.initial)
        self.assertEqual(len({entry["args"][2] for entry in self.actions("hub")}), 1)

    def test_custom_lock_busy_code(self):
        self.state_dir.mkdir()
        self.config["lock_busy_exit_code"] = 76
        with (self.state_dir / "autorelease.lock").open("w") as stream:
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.invoke(code=76)
        self.assertFalse((self.state_dir / "state.json").exists())

    def test_timeout_kills_descendant_even_when_leader_exits_on_term(self):
        self.config["targets"]["hub"]["timeout"] = 0.4
        self.modes["hub"].write_text("timeout")
        started = time.monotonic()
        self.invoke(code=1)
        self.assertLess(time.monotonic() - started, 5)
        pid = int(Path(str(self.modes["hub"]) + ".pid").read_text())

        def gone():
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                return True
            path = Path(f"/proc/{pid}/stat")
            return path.exists() and path.read_text().split()[2] == "Z"

        self.wait_for(gone)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)
        self.assertIn("result=step-1-timeout", self.entries(self.notify_log)[0]["args"][0])

    def test_notify_timeout_retains_outbox(self):
        self.config["notify_timeout"] = 0.4
        self.modes["hub"].write_text("fail")
        self.notify_mode.write_text("timeout")
        self.invoke(code=1)
        self.assertEqual(len(self.state()["outbox"]), 1)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)

    def test_dirty_tracked_or_untracked_worktree_is_never_cleaned(self):
        self.invoke()
        baseline = (self.state_dir / "state.json").read_bytes()
        tracked = self.worktree / "server/a.go"
        tracked.write_text("preserve tracked change")
        untracked = self.worktree / "private-untracked"
        untracked.write_text("preserve untracked change")
        self.commit({"server/a.go": "new remote content"})
        self.invoke(code=1)
        self.assertEqual(tracked.read_text(), "preserve tracked change")
        self.assertEqual(untracked.read_text(), "preserve untracked change")
        self.assertEqual(self.git(self.worktree, "rev-parse", "HEAD"), self.initial)
        self.assertEqual(self.state()["targets"], json.loads(baseline)["targets"])
        self.assertEqual(len(self.actions()), 1)
        self.assertIn("worktree-dirty", self.entries(self.notify_log)[0]["args"][0])
        # Revert only the fixture's own tracked edit, leaving the untracked file.
        tracked.write_text("initial")
        self.invoke(code=1)
        self.assertEqual(untracked.read_text(), "preserve untracked change")
        self.assertEqual(len(self.actions()), 1)

    def test_ignored_artifacts_are_allowed_but_never_overwritten(self):
        self.invoke()
        artifact = self.worktree / "ignored/cache"
        artifact.parent.mkdir()
        artifact.write_text("retain cache")
        newest = self.commit({"server/a.go": "new content"})
        self.invoke()
        self.assertEqual(artifact.read_text(), "retain cache")
        self.assertEqual(self.state()["targets"]["hub"]["last_success"], newest)
        (self.repo / "ignored").mkdir()
        (self.repo / "ignored/cache").write_text("new tracked file")
        self.git(self.repo, "add", "-f", "ignored/cache")
        self.git(self.repo, "commit", "-m", "Track former artifact")
        self.git(self.repo, "push", "origin", "main")
        self.invoke(code=1)
        self.assertEqual(artifact.read_text(), "retain cache")
        self.assertEqual(self.git(self.worktree, "rev-parse", "HEAD"), newest)

    def test_rejects_shared_checkout_wrong_repo_and_attached_worktree(self):
        self.config["worktree"] = str(self.repo)
        self.invoke(code=1)
        self.assertFalse(self.action_log.exists())
        self.config["worktree"] = str(self.worktree)
        self.git(self.repo, "worktree", "add", "-b", "fixture-branch", str(self.worktree), "HEAD")
        self.invoke(code=1)
        self.assertEqual(self.git(self.worktree, "symbolic-ref", "--short", "HEAD"), "fixture-branch")
        wrong = self.root / "wrong"
        self.git(self.root, "clone", "--no-local", "-b", "main", str(self.origin), str(wrong))
        self.git(wrong, "checkout", "--detach")
        self.config["worktree"] = str(wrong)
        self.config["state_dir"] = str(self.root / "wrong-state")
        result = self.invoke(code=1)
        self.assertIn("worktree-wrong-repository", result.stderr)
        self.assertFalse(self.action_log.exists())

    def test_success_requires_clean_unchanged_head_after_action(self):
        self.invoke()
        self.commit({"server/a.go": "new content"})
        self.modes["hub"].write_text("change-head")
        self.invoke(code=1)
        self.assertEqual(self.state()["targets"]["hub"]["last_success"], self.initial)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)
        self.assertIn("worktree-head-changed", self.entries(self.notify_log)[0]["args"][0])

    def test_deletions_renames_globs_and_nul_paths(self):
        self.target("web", ["web/*.js"])
        self.invoke()
        self.commit({"server-other/a.go": "boundary should not match"})
        self.invoke()
        self.assertEqual(len(self.actions()), 2)
        head = self.commit({"server/a.go": None, "moved/a.go": "initial",
                            "web/nested/a\nname.js": "newline path"})
        self.invoke()
        self.assertEqual(self.actions("hub")[-1]["args"][0], head)
        self.assertEqual(self.actions("web")[-1]["args"][0], head)
        self.assertEqual(len(self.actions()), 4)

    def test_existing_shared_checkout_can_be_dirty_on_another_branch(self):
        self.git(self.repo, "checkout", "-b", "shared-user-branch")
        (self.repo / "server/a.go").write_text("shared private edits")
        (self.repo / "shared-untracked").write_text("retain")
        before = self.git(self.repo, "status", "--porcelain")
        self.invoke()
        self.assertEqual(self.git(self.repo, "symbolic-ref", "--short", "HEAD"), "shared-user-branch")
        self.assertEqual((self.repo / "server/a.go").read_text(), "shared private edits")
        self.assertEqual(self.git(self.repo, "status", "--porcelain"), before)
        self.assertEqual(self.actions()[0]["args"][0], self.initial)

    def test_git_proxy_and_environment_locators_cannot_redirect_git(self):
        calls = self.root / "git-calls.jsonl"
        directory = self.root / "bin"
        directory.mkdir()
        # Resolve the client, not an executable from Git's helper directory.
        executable = shutil.which("git", path=self.env.get("PATH"))
        wrapper = directory / "git"
        wrapper.write_text("#!" + sys.executable + "\nimport json,os,sys\n"
                           "with open(" + repr(str(calls)) + ", 'a') as f: "
                           "f.write(json.dumps(sys.argv[1:]) + '\\n')\n"
                           "os.execv(" + repr(executable) + ", [" + repr(executable) + "] + sys.argv[1:])\n")
        wrapper.chmod(0o700)
        self.config["git_proxy"] = "http://proxy.example.com:8080"
        self.config["environment"].update(PATH=str(directory) + os.pathsep + self.env["PATH"],
                                           GIT_DIR=str(self.root / "must-not-exist"),
                                           GIT_WORK_TREE=str(self.root / "must-not-exist"))
        self.invoke()
        fetches = [args for args in self.entries(calls) if "fetch" in args]
        self.assertEqual(len(fetches), 1)
        self.assertEqual(fetches[0][:2], ["-c", "http.proxy=http://proxy.example.com:8080"])
        self.assertEqual(self.actions()[0]["head"], self.initial)
        self.assertFalse((self.root / "must-not-exist").exists())

    def test_atomic_state_replace_preserves_old_file_on_failure(self):
        self.state_dir.mkdir()
        path = self.state_dir / "state.json"
        autorelease.atomic_json(path, {"original": True})
        before = path.read_bytes()
        with patch.object(autorelease.os, "replace", side_effect=OSError("private error")):
            with self.assertRaises(OSError):
                autorelease.atomic_json(path, {"replacement": True})
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.state_dir.iterdir()), [path])

    def test_busy_between_failures_does_not_consume_budget(self):
        self.modes["hub"].write_text("fail")
        self.invoke(code=1)
        self.modes["hub"].write_text("busy")
        self.invoke(code=75)
        self.invoke(code=75)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)
        self.assertEqual(len(self.entries(self.notify_log)), 1)
        self.modes["hub"].write_text("fail")
        self.invoke(code=1)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 2)
        self.invoke(code=1)
        self.assertEqual(len(self.actions()), 4)
        self.assertEqual(len(self.entries(self.notify_log)), 2)

    def test_fetch_failure_does_not_run_actions_or_charge_budget(self):
        self.invoke()
        records = self.state()["targets"]
        self.git(self.repo, "remote", "set-url", "origin", str(self.root / "missing-origin"))
        self.invoke(code=1)
        self.assertEqual(self.state()["targets"], records)
        self.assertEqual(len(self.actions()), 1)
        self.assertEqual(len(self.entries(self.notify_log)), 1)

    def test_invalid_config_and_state_are_fail_closed_and_redacted(self):
        cases = [
            lambda c: c.update(branch="invalid branch EXAMPLE_SECRET_SENTINEL"),
            lambda c: c["targets"]["hub"].update(timeout=0),
            lambda c: c["targets"]["hub"].update(command=["{unknown}"]),
            lambda c: c["targets"]["hub"].update(command=["{commit.__class__}"]),
            lambda c: c.update(environment={"SECRET": 1}),
            lambda c: c.update(state_dir=str(self.worktree / "state")),
        ]
        original = json.dumps(self.config)
        for mutate in cases:
            self.config = json.loads(original)
            mutate(self.config)
            self.invoke(code=1)
            self.assertFalse(self.action_log.exists())
        self.config = json.loads(original)
        self.state_dir.mkdir(exist_ok=True)
        (self.state_dir / "state.json").write_text("EXAMPLE_SECRET_SENTINEL")
        self.invoke(code=1)
        self.assertFalse(self.action_log.exists())
        self.assertFalse(self.notify_log.exists())

    def test_failed_spawn_counts_as_action_failure(self):
        self.config["targets"]["hub"]["command"] = [str(self.root / "missing-adapter")]
        self.invoke(code=1)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 1)
        self.assertIn("step-1-spawn-failed", self.entries(self.notify_log)[0]["args"][0])

    def test_missing_baseline_commit_never_deploys_or_counts_failure(self):
        self.invoke()
        state = self.state()
        state["targets"]["hub"]["last_success"] = "f" * 40
        autorelease.atomic_json(self.state_dir / "state.json", state)
        self.invoke(code=1)
        self.assertEqual(len(self.actions()), 1)
        self.assertEqual(self.state()["targets"]["hub"]["failures"], 0)


if __name__ == "__main__":
    unittest.main()
