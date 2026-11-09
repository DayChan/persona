#!/usr/bin/env python3
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from run import run
sys.path.insert(0, str(ROOT / "scripts" / "test"))
from server_test_env import clean_environment


def is_suite(path):
    """A test_*.py that runs under unittest. Some are verification scripts that take arguments (test_rolling_compatibility.py,
    test_source_development_resources.py): unittest runs no tests in them, which exits 0 on Python 3.11 but 5 ("NO TESTS
    RAN") from 3.12, so a local run on a newer Python would count them as failures."""
    return "unittest" in path.read_text(encoding="utf-8", errors="replace")


def select_suites(suites, index=0, count=1):
    if count < 1 or not 0 <= index < count:
        raise ValueError("Shard count must be positive and index must be within the count")
    return sorted(suites)[index::count]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--shard-index", type=int, default=int(os.environ.get("CI_PYTHON_SHARD_INDEX", "0")))
    parser.add_argument("--shard-count", type=int, default=int(os.environ.get("CI_PYTHON_SHARD_COUNT", "1")))
    args = parser.parse_args()
    found = [path for path in ROOT.joinpath("scripts").rglob("test_*.py") if "node_modules" not in path.parts]
    suites = [path for path in found if is_suite(path)]
    if args.shard_index == 0:
        for path in sorted(set(found) - set(suites)):
            print(f"SKIP {path.relative_to(ROOT)} (not a unittest suite)", flush=True)
    try:
        suites = select_suites(suites, args.shard_index, args.shard_count)
    except ValueError as error:
        parser.error(str(error))
    if not suites:
        sys.exit("FAIL no Python script suites discovered for shard")
    print(f"Python shard {args.shard_index + 1}/{args.shard_count}: {len(suites)} suites", flush=True)
    caches = json.loads(subprocess.check_output(["go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH"], text=True, timeout=30))
    failed = []
    with tempfile.TemporaryDirectory(prefix="persona-ci-home-", dir=os.environ.get("TMPDIR")) as home:
        cleaned = clean_environment(dict(os.environ))
        os.environ.clear()
        os.environ.update(cleaned)
        os.environ.update(caches)
        os.environ.update(HOME=home)
        reservations = [socket.socket(), socket.socket()]
        try:
            for reservation in reservations:
                reservation.bind(("127.0.0.1", 0))
            os.environ["MOCK_MESH_TEST_PORTS"] = ",".join(str(reservation.getsockname()[1]) for reservation in reservations)
        finally:
            for reservation in reservations:
                reservation.close()
        for suite in suites:
            os.chdir(suite.parent)
            label = str(suite.relative_to(ROOT))
            if run(label, 120, [sys.executable, "-B", "-m", "unittest", "-q", suite.stem]):
                failed.append(label)
    print(f"Python suites: {len(suites)}; failed: {len(failed)}")
    for suite in failed:
        print(f"FAIL {suite}")
    return bool(failed)


if __name__ == "__main__":
    sys.exit(main())
