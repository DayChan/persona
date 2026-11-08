#!/usr/bin/env python3
import argparse
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
SHARDS = ("node", "managers", "other")


def shard_for(package):
    for name in ("node", "hub", "voice", "assistant"):
        suffix = "/internal/" + name
        if package.endswith(suffix) or suffix + "/" in package:
            return "node" if name == "node" else "managers"
    return "other"


def select_packages(packages, shard):
    if shard not in SHARDS:
        raise ValueError(f"Unknown server shard: {shard}")
    return [package for package in packages if shard_for(package) == shard]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("shard", choices=SHARDS)
    parser.add_argument("--database-file", required=True)
    args = parser.parse_args()
    listed = subprocess.check_output(["go", "list", "./..."], cwd=ROOT / "server", text=True, timeout=120)
    packages = select_packages(listed.splitlines(), args.shard)
    if not packages:
        parser.error(f"No packages in server shard {args.shard}")
    workers = {"node": 1, "managers": 3, "other": 2}[args.shard]
    command = [sys.executable, str(ROOT / "scripts/test/server_test_env.py"),
               "--database-file", args.database_file, "--", "go", "test", "-json",
               "-p", str(workers), "-count=1", "-timeout=10m", *packages]
    slow = []
    process = subprocess.Popen(command, cwd=ROOT / "server", stdout=subprocess.PIPE, text=True)
    try:
        for line in process.stdout:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                print(line, end="", flush=True)
                continue
            if event.get("Action") == "output":
                print(event.get("Output", ""), end="", flush=True)
            if event.get("Action") in ("pass", "fail") and event.get("Test"):
                slow.append((event.get("Elapsed", 0), event["Package"], event["Test"]))
        status = process.wait()
    finally:
        process.stdout.close()
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=10)
    print(f"Server shard {args.shard}: {len(packages)} packages; exit {status}")
    print("Slowest test events (parent/subtest durations may overlap):")
    for elapsed, package, test in sorted(slow, reverse=True)[:20]:
        print(f"SLOW {elapsed:.3f}s {package} {test}")
    return status


if __name__ == "__main__":
    sys.exit(main())
