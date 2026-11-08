#!/usr/bin/env python3
"""Run a resumable TestFlight release on the build computer in a dedicated worktree."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time


def run(argv, cwd=None, capture=False):
    return subprocess.check_output(argv, cwd=cwd, text=True).strip() if capture else subprocess.check_call(argv, cwd=cwd)


def save(path, value):
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.chmod(0o600)
    temporary.replace(path)


def prepare(repo, worktree, commit):
    repo, worktree = repo.resolve(), worktree.resolve()
    if repo == worktree:
        raise ValueError("release worktree must not be the shared checkout")
    run(["git", "fetch", "origin", "refs/heads/main:refs/remotes/origin/main"], repo)
    run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], repo)
    if not worktree.exists():
        run(["git", "worktree", "add", "--detach", str(worktree), commit], repo)
    else:
        common = run(["git", "rev-parse", "--git-common-dir"], worktree, True)
        source = run(["git", "rev-parse", "--git-common-dir"], repo, True)
        if (worktree / common).resolve() != (repo / source).resolve():
            raise ValueError("release worktree belongs to another repository")
        if subprocess.run(["git", "symbolic-ref", "-q", "HEAD"], cwd=worktree, stdout=subprocess.DEVNULL).returncode == 0:
            raise ValueError("release worktree must be detached")
        if run(["git", "status", "--porcelain"], worktree, True):
            raise ValueError("release worktree is not clean; refusing to discard changes")
        run(["git", "checkout", "--detach", commit], worktree)
    if run(["git", "rev-parse", "HEAD"], worktree, True) != commit:
        raise ValueError("release worktree revision does not match the requested commit")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--worktree", type=Path, required=True)
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    args = parser.parse_args()
    # The outer operation lock is intentionally retained until wait + membership verification finish.
    if os.environ.get("PERSONA_OPERATION_RELEASE_LOCKED") != "1":
        lock = Path(__file__).with_name("operation_lock.py")
        return subprocess.call([sys.executable, str(lock), "release", "--", sys.executable, str(Path(__file__).resolve()), *sys.argv[1:]])
    prepare(args.repo, args.worktree, args.commit)
    args.state_dir.mkdir(parents=True, exist_ok=True)
    checkpoint = args.state_dir / "testflight-checkpoint.json"
    state = json.loads(checkpoint.read_text()) if checkpoint.exists() else {}
    sys.path.insert(0, str(args.worktree / "scripts/release"))
    import asc_testflight as asc
    if state.get("commit") != args.commit:
        # A timestamp remains monotonic across manual and unattended release callers.
        build = max(int(time.strftime("%Y%m%d%H%M", time.gmtime())), asc.next_build())
        state = {"commit": args.commit, "build": str(build), "phase": "prepared"}
        save(checkpoint, state)
    build = state["build"]
    print("TestFlight commit=" + args.commit + " build=" + build + " phase=" + state["phase"], flush=True)
    found = asc.build_by_version(build)
    if not found and state["phase"] in ("prepared", "upload_failed"):
        state["phase"] = "upload_started"
        save(checkpoint, state)
        environment = os.environ.copy()
        environment["NO_PUBLISH"] = "1"
        environment["UPLOADER"] = "asc"
        result = subprocess.call(["bash", "scripts/release/release.sh", build], cwd=args.worktree, env=environment)
        state["phase"] = "uploaded" if result == 0 else "upload_failed"
        save(checkpoint, state)
        if result:
            return result
    # After an interrupted upload, wait for its known number instead of uploading twice.
    run([sys.executable, "-u", "scripts/release/asc_testflight.py", "wait", "45", build], args.worktree)
    found = asc.build_by_version(build)
    if not found or found["attributes"]["processingState"] != "VALID":
        raise RuntimeError("TestFlight processing did not finish VALID")
    gid = asc.group_id()
    group = asc.request("GET", "/v1/betaGroups/" + gid)["data"]
    visible = group["attributes"].get("hasAccessToAllBuilds") or any(
        item["id"] == found["id"] for item in asc.get_all("/v1/betaGroups/" + gid + "/builds"))
    if not visible:
        raise RuntimeError("VALID build is not available to the Internal group")
    state["phase"] = "complete"
    save(checkpoint, state)
    print("TestFlight SUCCESS commit=" + args.commit + " build=" + build + " processingState=VALID Internal=available", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
