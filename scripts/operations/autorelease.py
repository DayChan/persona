#!/usr/bin/env python3
"""One-shot, standard-library release worker for a systemd service/timer.

Private JSON configuration requires absolute repo, worktree and state_dir paths,
plus targets (hub/web/testflight) and notify_command. branch defaults to main;
git_proxy defaults to an empty string. Each target has paths, command and timeout.
A command is an argv array, or an array of argv arrays for sequential steps; its
only substitutions are {commit}, {worktree} and {build}. Paths without glob
characters match a file or directory prefix (on a component boundary); glob
patterns use fnmatch, where * can cross directory boundaries. notify_command is
an argv array with {message}. Optional environment is a string-to-string map;
git_timeout and notify_timeout default to 120 and 30 seconds. lock_busy_exit_code
defaults to 75. An action exiting 75 means an external operation lock is busy:
other targets still run, with no failure notification or failure-budget charge.
No shell is involved. Child output is discarded unless a target explicitly sets
log_output=true, which retains it in private, mode-0600 per-step logs.

The worktree must be absent or a clean, detached, linked worktree of repo. The
worker never resets/cleans it or changes the shared checkout's HEAD/index/files.
state_dir must be outside the release worktree. Builds are allocated durably
before executing commands and reused for retries at the same target/commit.
Adapters receive PERSONA_AUTORELEASE_CHECKPOINT (an external JSON checkpoint
path), STATE_DIR, TARGET, COMMIT and BUILD with the same prefix. Adapters must
persist their own upload receipt there (privately/atomically), check it before
uploading, and return success only after all verification steps (including Apple
VALID) pass. The worker deliberately retries all steps, not partial pipelines.

--dry-run only reads local refs/state: no fetch, checkout, locks, notifications,
commands or state writes. --inject-failure executes only the real notification
command and uses injection.json, never normal release state. Notification
outboxes are at-least-once; receivers should tolerate duplicates after a crash.
"""
from __future__ import annotations

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import fnmatch
import hashlib
import json
import math
import os
from pathlib import Path
import re
import signal
import string
import subprocess
import sys
import tempfile
import time

TARGETS = {"hub", "web", "testflight"}
COMMIT = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")
LOG_PATH = None
GIT_LOCATORS = {
    "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
    "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_CONFIG", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL",
    "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_NAMESPACE",
}


class WorkerError(Exception):
    """An intentionally non-sensitive failure reason."""


class Busy(Exception):
    pass


def log(target, commit, result):
    stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
    line = f"{stamp} target={target} commit={commit} result={result}"
    print(line, file=sys.stderr, flush=True)
    if LOG_PATH is not None:
        fd = os.open(LOG_PATH, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        with os.fdopen(fd, "a") as stream:
            stream.write(line + "\n")


def positive_timeout(value):
    if (isinstance(value, bool) or not isinstance(value, (int, float))
            or not math.isfinite(value) or value <= 0):
        raise WorkerError("invalid-timeout")
    return value


def argv_template(value, fields):
    if not isinstance(value, list) or not value:
        raise WorkerError("invalid-command")
    for arg in value:
        if not isinstance(arg, str) or "\0" in arg:
            raise WorkerError("invalid-command")
        try:
            for _, field, spec, conversion in string.Formatter().parse(arg):
                if field is not None and (field not in fields or spec or conversion):
                    raise WorkerError("invalid-template")
        except ValueError:
            raise WorkerError("invalid-template") from None
    if not value[0]:
        raise WorkerError("invalid-command")
    return value


def load_config(path):
    try:
        config = json.loads(Path(path).read_text())
        if not isinstance(config, dict):
            raise WorkerError("invalid-config")
        for key in ("repo", "worktree", "state_dir"):
            if not isinstance(config[key], str) or not Path(config[key]).is_absolute():
                raise WorkerError("invalid-path")
            config[key] = Path(config[key]).resolve()
        if config["repo"] == config["worktree"]:
            raise WorkerError("shared-worktree-forbidden")
        if (config["state_dir"] == config["worktree"]
                or config["worktree"] in config["state_dir"].parents):
            raise WorkerError("state-inside-worktree")
        config.setdefault("branch", "main")
        config.setdefault("git_proxy", "")
        for key in ("branch", "git_proxy"):
            if not isinstance(config[key], str) or "\0" in config[key]:
                raise WorkerError("invalid-config")
        config["git_timeout"] = positive_timeout(config.get("git_timeout", 120))
        config["notify_timeout"] = positive_timeout(config.get("notify_timeout", 30))
        exit_code = config.setdefault("lock_busy_exit_code", 75)
        if type(exit_code) is not int or not 1 <= exit_code <= 255:
            raise WorkerError("invalid-exit-code")
        environment = config.setdefault("environment", {})
        if not isinstance(environment, dict):
            raise WorkerError("invalid-environment")
        for key, value in environment.items():
            if (not isinstance(key, str) or not key or "=" in key or "\0" in key
                    or not isinstance(value, str) or "\0" in value):
                raise WorkerError("invalid-environment")
        argv_template(config["notify_command"], {"message"})
        targets = config["targets"]
        if not isinstance(targets, dict) or not targets or not targets.keys() <= TARGETS:
            raise WorkerError("invalid-targets")
        for target in targets.values():
            if not isinstance(target, dict):
                raise WorkerError("invalid-target")
            paths = target["paths"]
            if (not isinstance(paths, list) or not paths
                    or any(not isinstance(p, str) or not p or p.startswith("/")
                           or "\0" in p or ".." in p.split("/") for p in paths)):
                raise WorkerError("invalid-path-pattern")
            target["timeout"] = positive_timeout(target["timeout"])
            if type(target.setdefault("log_output", False)) is not bool:
                raise WorkerError("invalid-log-output")
            command = target["command"]
            if not isinstance(command, list) or not command:
                raise WorkerError("invalid-command")
            steps = command if isinstance(command[0], list) else [command]
            target["steps"] = [argv_template(step, {"commit", "worktree", "build"})
                               for step in steps]
        return config
    except (OSError, ValueError, KeyError, TypeError):
        raise WorkerError("invalid-config") from None


def environment(config):
    env = dict(os.environ, **config["environment"])
    # Repository-locator/config overrides must not redirect even git -C commands.
    for key in list(env):
        if (key in GIT_LOCATORS or key.startswith(("GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_", "GIT_TRACE"))
                or key == "GIT_CURL_VERBOSE"):
            del env[key]
    env["GIT_TERMINAL_PROMPT"] = "0"
    return env


def kill_group(pid, sig):
    try:
        os.killpg(pid, sig)
    except ProcessLookupError:
        pass


def execute(argv, cwd, env, timeout, capture=False, output_path=None):
    """Bound every child session; optionally retain output in private action logs."""
    output_stream = None
    try:
        if output_path is not None:
            fd = os.open(output_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
            output_stream = os.fdopen(fd, "ab")
        process = subprocess.Popen(
            argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE if capture else output_stream or subprocess.DEVNULL,
            stderr=subprocess.STDOUT if output_stream else subprocess.DEVNULL, start_new_session=True,
        )
    except OSError:
        if output_stream:
            output_stream.close()
        raise WorkerError("spawn-failed") from None
    try:
        try:
            output, _ = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            kill_group(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
            # Always kill the group, even if its leader already exited on TERM.
            kill_group(process.pid, signal.SIGKILL)
            process.communicate()
            raise WorkerError("timeout") from None
        if process.returncode:
            raise WorkerError(f"exit-{process.returncode}")
        return output or b""
    finally:
        # Let operation-lock wrappers terminate their nested child groups before
        # releasing locks; then remove any remaining children in this session.
        kill_group(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        kill_group(process.pid, signal.SIGKILL)
        process.wait()
        if output_stream:
            output_stream.close()


def git(config, directory, *args):
    return execute(["git", "-c", "http.proxy=" + config["git_proxy"],
                    "-C", str(directory), *args], directory,
                   environment(config), config["git_timeout"], capture=True)


def git_path(config, directory, argument):
    path = Path(os.fsdecode(git(config, directory, "rev-parse", argument).rstrip(b"\n")))
    return (directory / path).resolve()


def validate_repo(config):
    repo = config["repo"]
    if git_path(config, repo, "--show-toplevel") != repo:
        raise WorkerError("repo-not-root")
    git(config, repo, "check-ref-format", "refs/heads/" + config["branch"])


def validate_worktree(config, expected=None):
    worktree = config["worktree"]
    if git_path(config, worktree, "--show-toplevel") != worktree:
        raise WorkerError("worktree-not-root")
    if (git_path(config, worktree, "--git-common-dir")
            != git_path(config, config["repo"], "--git-common-dir")):
        raise WorkerError("worktree-wrong-repository")
    head_file = git_path(config, worktree, "--git-dir") / "HEAD"
    if not COMMIT.fullmatch(head_file.read_text().strip()):
        raise WorkerError("worktree-not-detached")
    if git(config, worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all",
           "--ignore-submodules=none"):
        raise WorkerError("worktree-dirty")
    if expected and git(config, worktree, "rev-parse", "HEAD").decode().strip() != expected:
        raise WorkerError("worktree-head-changed")


def latest(config, fetch):
    if fetch:
        branch = config["branch"]
        git(config, config["repo"], "fetch", "--no-tags", "--no-recurse-submodules",
            "origin", f"+refs/heads/{branch}:refs/remotes/origin/{branch}")
    commit = git(config, config["repo"], "rev-parse", "--verify",
                 "refs/remotes/origin/" + config["branch"] + "^{commit}").decode().strip()
    if not COMMIT.fullmatch(commit):
        raise WorkerError("invalid-head")
    return commit


@contextmanager
def lock(path):
    flags = os.O_CREAT | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Busy() from None
        yield
    finally:
        os.close(fd)


def atomic_json(path, value):
    fd, name = tempfile.mkstemp(prefix=".autorelease-", dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(fd, "w") as stream:
            os.fchmod(stream.fileno(), 0o600)
            json.dump(value, stream, sort_keys=True, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        temporary.unlink(missing_ok=True)


def fresh_state():
    return {"version": 1, "targets": {}, "outbox": [], "next_build": 1}


def read_state(path, identity=None):
    if not path.exists():
        state = fresh_state()
    else:
        try:
            state = json.loads(path.read_text())
            if (not isinstance(state, dict) or state.get("version") != 1
                    or not isinstance(state["targets"], dict)
                    or not state["targets"].keys() <= TARGETS
                    or type(state["next_build"]) is not int or state["next_build"] < 1
                    or not isinstance(state["outbox"], list)):
                raise WorkerError("invalid-state")
            for record in state["targets"].values():
                if not isinstance(record, dict):
                    raise WorkerError("invalid-state")
                for key in ("last_success", "last_checked", "budget_commit", "build_commit"):
                    if record.get(key) is not None and not COMMIT.fullmatch(record[key]):
                        raise WorkerError("invalid-state")
                if type(record.get("failures", 0)) is not int or not 0 <= record.get("failures", 0) <= 2:
                    raise WorkerError("invalid-state")
                if "build" in record and (type(record["build"]) is not int or record["build"] < 1):
                    raise WorkerError("invalid-state")
            # Validate persisted messages before allowing them to enter logs/notifications.
            for message in state["outbox"]:
                if (not isinstance(message, str) or not re.fullmatch(
                        r"autorelease target=(hub|web|testflight|worker) commit=(-|[0-9a-f]{40}|[0-9a-f]{64}) "
                        r"result=[a-z0-9-]+", message)):
                    raise WorkerError("invalid-state")
        except (OSError, ValueError, TypeError, KeyError):
            raise WorkerError("invalid-state") from None
    if identity is not None:
        if state.get("identity", identity) != identity:
            raise WorkerError("state-repository-mismatch")
        state["identity"] = identity
    return state


class Worker:
    def __init__(self, config, state, path):
        self.config, self.state, self.path = config, state, path

    def save(self):
        atomic_json(self.path, self.state)

    def deliver(self, message):
        argv = [arg.format(message=message) for arg in self.config["notify_command"]]
        try:
            execute(argv, self.config["state_dir"], environment(self.config),
                    self.config["notify_timeout"])
        except WorkerError as error:
            log("worker", "-", "notify-" + str(error))
            return
        self.state["outbox"].remove(message)
        self.save()

    def retry_outbox(self):
        for message in list(self.state["outbox"]):
            self.deliver(message)

    def failure(self, target, commit, result):
        log(target, commit, result)
        message = f"autorelease target={target} commit={commit} result={result}"
        self.state["outbox"].append(message)
        self.save()
        self.deliver(message)

    def relevant(self, target, previous, head):
        if previous is None:
            return True
        if previous == head:
            return False
        paths = git(self.config, self.config["repo"], "diff", "--name-only", "-z",
                    "--no-renames", previous + ".." + head, "--").split(b"\0")
        for raw in paths:
            if not raw:
                continue
            path = os.fsdecode(raw)
            for pattern in target["paths"]:
                if any(char in pattern for char in "*?["):
                    if fnmatch.fnmatchcase(path, pattern):
                        return True
                elif path == pattern.rstrip("/") or path.startswith(pattern.rstrip("/") + "/"):
                    return True
        return False

    def run_targets(self, head, dry_run=False):
        failed = False
        deferred = False
        for name, target in self.config["targets"].items():
            record = self.state["targets"].setdefault(name, {})
            if record.get("budget_commit") != head:
                record.update(budget_commit=head, failures=0)
            if not self.relevant(target, record.get("last_success"), head):
                record["last_checked"] = head
                if not dry_run:
                    self.save()
                log(name, head, "dry-run-unrelated" if dry_run else "unrelated")
                continue
            if record.get("failures", 0) >= 2:
                failed = True
                log(name, head, "dry-run-halted" if dry_run else "halted")
                continue
            if dry_run:
                log(name, head, "dry-run-would-run")
                continue
            # Preflight problems do not consume an attempted-action failure budget.
            validate_worktree(self.config, head)
            if record.get("build_commit") != head:
                build = max(int(time.time()), self.state["next_build"])
                self.state["next_build"] = build + 1
                record.update(build=build, build_commit=head)
            record["last_checked"] = head
            self.save()
            checkpoint_dir = self.config["state_dir"] / "checkpoints" / name
            checkpoint_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
            values = {"commit": head, "worktree": str(self.config["worktree"]),
                      "build": str(record["build"])}
            env = environment(self.config)
            env.update({"PERSONA_AUTORELEASE_" + key.upper(): value for key, value in values.items()})
            env.update(PERSONA_AUTORELEASE_STATE_DIR=str(self.config["state_dir"]),
                       PERSONA_AUTORELEASE_TARGET=name,
                       PERSONA_AUTORELEASE_CHECKPOINT=str(checkpoint_dir / f"{head}-{record['build']}.json"))
            log(name, head, "started")
            try:
                for index, step in enumerate(target["steps"], 1):
                    try:
                        output_path = None
                        if target.get("log_output"):
                            logs = self.config["state_dir"] / "logs"
                            logs.mkdir(parents=True, exist_ok=True, mode=0o700)
                            output_path = logs / f"{name}-{head}-{record['build']}-step-{index}.log"
                        execute([arg.format(**values) for arg in step], self.config["worktree"],
                                env, target["timeout"], output_path=output_path)
                        validate_worktree(self.config, head)
                    except WorkerError as error:
                        if str(error) == "exit-75":
                            raise Busy() from None
                        raise WorkerError(f"step-{index}-{error}") from None
                    log(name, head, f"step-{index}-success")
            except Busy:
                deferred = True
                log(name, head, "action-lock-busy")
                continue
            except WorkerError as error:
                failed = True
                record["failures"] += 1
                self.failure(name, head, str(error))
                continue
            record.update(last_success=head, failures=0)
            self.save()
            log(name, head, "success")
        return failed, deferred


def interrupted(signum, frame):
    raise InterruptedError()


def run(config, dry_run=False, inject_failure=False):
    global LOG_PATH
    LOG_PATH = None
    directory = config["state_dir"]
    if dry_run:
        validate_repo(config)
        if config["worktree"].exists():
            validate_worktree(config)
        state = read_state(directory / "state.json")
        failed, _ = Worker(config, state, None).run_targets(latest(config, False), True)
        return 1 if failed else 0
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    LOG_PATH = directory / "worker.log"
    with lock(directory / "autorelease.lock"):
        if inject_failure:
            worker = Worker(config, read_state(directory / "injection.json"), directory / "injection.json")
            worker.retry_outbox()
            worker.failure("worker", "-", "injected-failure")
            return 1
        validate_repo(config)
        identity = hashlib.sha256((str(git_path(config, config["repo"], "--git-common-dir"))
                                   + "\0" + str(config["worktree"]) + "\0" + config["branch"]).encode()).hexdigest()
        worker = Worker(config, read_state(directory / "state.json", identity), directory / "state.json")
        worker.retry_outbox()
        try:
            if config["worktree"].exists():
                validate_worktree(config)
            head = latest(config, True)
            if not config["worktree"].exists():
                git(config, config["repo"], "worktree", "add", "--detach", str(config["worktree"]), head)
            validate_worktree(config)
            # Also protect the worktree if another config uses a different state_dir.
            with lock(git_path(config, config["worktree"], "--git-dir") / "autorelease.lock"):
                validate_worktree(config)
                git(config, config["worktree"], "checkout", "--detach", "--no-overwrite-ignore", head)
                validate_worktree(config, head)
                failed, deferred = worker.run_targets(head)
            if failed or worker.state["outbox"]:
                return 1
            return config["lock_busy_exit_code"] if deferred else 0
        except WorkerError as error:
            worker.failure("worker", "-", "preflight-" + str(error))
            return 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true")
    mode.add_argument("--inject-failure", action="store_true")
    args = parser.parse_args(argv)
    config = None
    try:
        config = load_config(args.config)
        code = run(config, args.dry_run, args.inject_failure)
        verdict = ("success" if not code else
                   "lock-busy" if code == config["lock_busy_exit_code"] and code != 1 else "failure")
    except Busy:
        code, verdict = config["lock_busy_exit_code"], "lock-busy"
    except WorkerError as error:
        log("worker", "-", str(error))
        code, verdict = 1, "failure"
    except (OSError, ValueError, TypeError, InterruptedError, KeyboardInterrupt):
        # OS/config/adapter errors may include credentials; never print exception text.
        log("worker", "-", "worker-interrupted-or-unavailable")
        code, verdict = 1, "failure"
    print(f"autorelease {'dry-run ' if args.dry_run else ''}verdict={verdict}", flush=True)
    return code


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    sys.exit(main())
