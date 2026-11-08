#!/usr/bin/env python3
"""Hub-local automation notification and read-only serving-version evidence."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from persona_identity import data_dir, service_environment  # noqa: E402


def state_root():
    return Path(data_dir(environment=service_environment(role="hub")))


def api(path, payload=None):
    cfg = json.loads((state_root() / "hub.json").read_text())
    port = cfg.get("listen", ":18660").rsplit(":", 1)[1]
    request = urllib.request.Request("http://127.0.0.1:" + port + "/api/v1/" + path,
                                    headers={"Authorization": "Bearer " + cfg["token"],
                                             "Content-Type": "application/json"},
                                    data=json.dumps(payload).encode() if payload is not None else None)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=30) as response:
        return json.load(response)


def status():
    value = api("hub/update")
    return {k: value.get(k) for k in ("version", "build", "channel", "state", "confirmed")}


def vcs_matches(info, commit):
    settings = {}
    for line in info.splitlines():
        kind, separator, value = line.strip().partition("\t")
        if kind == "build" and separator:
            key, _, content = value.partition("=")
            settings[key] = content
    return settings.get("vcs.revision") == commit and settings.get("vcs.modified") == "false"


def tree_digest(root):
    if not root.is_dir() or root.is_symlink():
        raise RuntimeError("web artifact directory is unavailable")
    digest = hashlib.sha256()
    count = 0
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise RuntimeError("web artifact contains a symlink")
        if path.is_file():
            digest.update(path.relative_to(root).as_posix().encode() + b"\0")
            digest.update(hashlib.sha256(path.read_bytes()).digest())
            count += 1
    if not count:
        raise RuntimeError("web artifact is empty")
    return digest.hexdigest()


def bundled_web_matches(receipt, commit, built, installed):
    if receipt.get("commit") != commit or not receipt.get("web_sha256"):
        return False
    try:
        return receipt["web_sha256"] == tree_digest(built) == tree_digest(installed)
    except (OSError, RuntimeError):
        return False


def verify(commit, worktree):
    root = state_root()
    expected_version = subprocess.check_output(["git", "describe", "--always", "--dirty"], cwd=worktree, text=True).strip()
    expected_binary = worktree / "server/bin/personad-linux-amd64"
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        try:
            live = status()
            slot = subprocess.check_output([str(root / "bin/personad"), "hub", "switch", "--status"], text=True)
            match = re.search(r"pid (\d+)", slot)
            if not match:
                raise RuntimeError("serving slot did not report a process ID")
            binary = Path("/proc") / match[1] / "exe"
            flags = subprocess.check_output(["go", "version", "-m", str(binary)], text=True)
            digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
            if (vcs_matches(flags, commit) and live["confirmed"]
                    and live["version"].startswith(expected_version + "-")
                    and digest(binary) == digest(expected_binary)):
                evidence = {"commit": commit, **live, "serving_pid": int(match[1]),
                            "serving_sha256": digest(binary), "matches_built_binary": True}
                print(json.dumps(evidence), flush=True)
                return evidence
        except (OSError, ValueError, subprocess.CalledProcessError, RuntimeError):
            pass
        time.sleep(5)
    raise RuntimeError("hub serving process did not confirm the requested commit within 180 seconds")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("notify", "status", "deploy", "web"))
    parser.add_argument("--message", default="")
    parser.add_argument("--title", default="")
    parser.add_argument("--commit")
    parser.add_argument("--machine")
    parser.add_argument("--worktree", type=Path)
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--receipt", type=Path)
    args = parser.parse_args()
    if args.action == "notify":
        if not args.title:
            parser.error("notify requires a configured --title")
        message = args.message[:1000]
        value = api("notify", {"title": args.title[:120], "body": message,
                               "level": "time-sensitive",
                               "id": "autorelease-" + hashlib.sha256(message.encode()).hexdigest()[:32]})
        sent = value.get("sent") is True
        receipt = {"notification_accepted": sent, "phone_receipt_confirmed": False,
                   "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                   "message": message}
        print(json.dumps(receipt), flush=True)
        if args.evidence:
            temporary = args.evidence.with_suffix(".tmp")
            temporary.write_text(json.dumps(receipt, indent=2) + "\n")
            temporary.chmod(0o600)
            temporary.replace(args.evidence)
        return 0 if sent else 1
    if args.action == "status":
        print(json.dumps(status()), flush=True)
        return 0
    if not args.commit or not args.worktree or (args.action == "deploy" and not args.machine):
        parser.error("deployment requires --commit and --worktree; hub deploy also requires --machine")
    if os.environ.get("PERSONA_OPERATION_DEPLOY_LOCKED") != "1":
        return subprocess.call([sys.executable, str(ROOT / "scripts/operations/operation_lock.py"), "deploy", "--",
                                sys.executable, str(Path(__file__).resolve()), *sys.argv[1:]])
    built_web = args.worktree / "web/dist"
    installed_web = state_root() / "web"
    if args.action == "web":
        receipt = json.loads(args.receipt.read_text()) if args.receipt and args.receipt.exists() else {}
        if bundled_web_matches(receipt, args.commit, built_web, installed_web):
            print("web deployment SUCCESS commit=" + args.commit + " matching bundled artifact", flush=True)
            return 0
        result = subprocess.call(["bash", "scripts/deploy/deploy.sh", "web"], cwd=args.worktree)
        if result:
            return result
        if tree_digest(built_web) != tree_digest(installed_web):
            raise RuntimeError("installed web artifact does not match the built tree")
        print("web deployment SUCCESS commit=" + args.commit, flush=True)
        return 0
    result = subprocess.call(["bash", "scripts/deploy/deploy.sh", args.machine], cwd=args.worktree)
    if result:
        return result
    evidence = verify(args.commit, args.worktree)
    web_sha256 = tree_digest(built_web)
    if web_sha256 != tree_digest(installed_web):
        raise RuntimeError("bundled web deployment does not match its built artifact")
    evidence["web_sha256"] = web_sha256
    evidence["verified_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    if args.evidence:
        temporary = args.evidence.with_suffix(".tmp")
        temporary.write_text(json.dumps(evidence, indent=2) + "\n")
        temporary.chmod(0o600)
        temporary.replace(args.evidence)
    print("hub deployment SUCCESS commit=" + args.commit, flush=True)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        # Network errors can embed private URLs; retain the error class, not the request.
        print("hub automation FAILURE: " + type(error).__name__, file=sys.stderr)
        sys.exit(1)
