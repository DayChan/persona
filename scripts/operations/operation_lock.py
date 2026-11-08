#!/usr/bin/env python3
"""Serialize manual and unattended operations, including deployments from another computer."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import select
import socket
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from persona_config import load  # noqa: E402
from persona_identity import data_dir, service_environment  # noqa: E402

# Deployments share a lock on the hub; releases share a lock on their build computer.
REMOTE = r'''
import fcntl, os, pathlib, sys
root=pathlib.Path(os.environ.get('PERSONA_HOME',str(pathlib.Path.home()/'.persona'))).expanduser()
p=root/'releases/hub/.publish.lock'
p.parent.mkdir(parents=True, exist_ok=True)
f=open(p,'a')
try: fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
except BlockingIOError: print('BUSY',flush=True); sys.exit(75)
print('READY',flush=True)
sys.stdin.buffer.read()
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("name", choices=("deploy", "release"))
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        parser.error("a command is required")
    # This token is scoped to the child process tree, so an outer release wrapper can
    # retain its lock through upload AND TestFlight processing without self-deadlocking.
    inherited = json.loads(os.environ.get("PERSONA_OPERATION_LOCKS", "{}"))
    if args.name in inherited:
        return subprocess.call(command)
    env = os.environ.copy()
    inherited[args.name] = True
    env["PERSONA_OPERATION_LOCKS"] = json.dumps(inherited)
    env["PERSONA_OPERATION_" + args.name.upper() + "_LOCKED"] = "1"
    remote = None
    stream = None
    try:
        host = ""
        if args.name == "deploy":
            cfg = load()
            hub = cfg["hub"]["machine"]
            machine = next(m for m in cfg["machines"] if m["id"] == hub)
            names = set(machine.get("hostnames", []))
            here = {socket.gethostname(), socket.gethostname().split(".")[0]}
            if not names.intersection(here):
                host = machine.get("ssh", hub)
        if host:
            import shlex
            prefix = subprocess.check_output(
                [sys.executable, str(ROOT / "scripts/persona_identity.py"),
                 "--service", "hub", "--prepare", "--prefix", "-"], text=True)
            remote = subprocess.Popen(
                ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host,
                 prefix + "\npython3 -u -c " + shlex.quote(REMOTE)],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
            if not select.select([remote.stdout], [], [], 30)[0]:
                raise RuntimeError("timed out acquiring the hub operation lock")
            answer = remote.stdout.readline().strip()
            if answer == "BUSY":
                print("operation lock busy: deploy", flush=True)
                return 75
            if answer != "READY":
                raise RuntimeError("could not acquire the hub operation lock")
        else:
            path = (Path(data_dir(environment=service_environment(role="hub"))) / "releases/hub/.publish.lock" if args.name == "deploy"
                    else Path.home() / ".local/state/persona/locks/release.lock")
            path.parent.mkdir(parents=True, exist_ok=True)
            stream = path.open("a")
            try:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                print("operation lock busy: " + args.name, flush=True)
                return 75
        print("operation lock acquired: " + args.name, flush=True)
        child = subprocess.Popen(command, env=env, start_new_session=True)

        def stop_child(signum, frame):
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGTERM)
                try:
                    child.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait()
            raise SystemExit(128 + signum)

        signal.signal(signal.SIGTERM, stop_child)
        signal.signal(signal.SIGINT, stop_child)
        signal.signal(signal.SIGHUP, stop_child)
        while True:
            try:
                result = child.wait(timeout=1)
                break
            except subprocess.TimeoutExpired:
                if remote and remote.poll() is not None:
                    stop_child(signal.SIGTERM, None)
        print("operation lock released: " + args.name + " result=" + str(result), flush=True)
        return result
    finally:
        if remote:
            remote.stdin.close()
            try:
                remote.wait(timeout=5)
            except subprocess.TimeoutExpired:
                remote.kill()
                remote.wait()
        if stream:
            stream.close()


if __name__ == "__main__":
    sys.exit(main())
