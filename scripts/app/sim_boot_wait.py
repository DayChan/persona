#!/usr/bin/env python3
"""Wait for one simulator, collecting scoped evidence before its boot deadline."""
import argparse
import os
import signal
import subprocess
import sys
import time
import uuid

DIAGNOSTIC_WINDOW = 10
TERMINATE_GRACE = 5


def log(message):
    print(f"sim-boot: {message}", file=sys.stderr, flush=True)


def kill_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass  # The owned process group already exited.


def stop(process, grace=0):
    kill_group(process, signal.SIGTERM if grace else signal.SIGKILL)
    try:
        process.wait(timeout=grace)
    except subprocess.TimeoutExpired:
        kill_group(process, signal.SIGKILL)
        process.wait()


def wait_booted(udid, timeout):
    deadline = time.monotonic() + timeout
    diagnostics = []
    try:
        boot = subprocess.Popen(["xcrun", "simctl", "bootstatus", udid, "-b"],
                                start_new_session=True)
    except OSError as error:
        log(f"could not start bootstatus: {error}")
        return 127
    try:
        # Diagnostics share the original deadline; they never reset the boot timer.
        try:
            code = boot.wait(timeout=max(0, deadline - time.monotonic()
                                         - min(DIAGNOSTIC_WINDOW, timeout / 2)))
        except subprocess.TimeoutExpired:
            log(f"bootstatus still pending for {udid}; collecting live diagnostics (pid {boot.pid})")
            commands = [
                ["sample", str(boot.pid), "2", "-file", "/dev/stderr"],
                ["xcrun", "simctl", "spawn", udid, "launchctl", "list"],
            ]
            for command in commands:
                log("diagnostic: " + " ".join(command))
                try:
                    diagnostics.append(subprocess.Popen(command, start_new_session=True,
                                                        stdin=subprocess.DEVNULL,
                                                        stdout=sys.stderr, stderr=sys.stderr))
                except OSError as error:
                    log(f"could not start diagnostic: {error}")
            try:
                code = boot.wait(timeout=max(0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                log(f"bootstatus deadline reached for {udid}")
                return 124
        return code if code >= 0 else 128 - code
    finally:
        # No diagnostic may outlive this attempt or hold the stderr artifact open.
        for process in diagnostics:
            code = process.poll()
            # A finished leader can still have children holding the log pipe.
            # Each diagnostic owns a new session; never signal a caller's group.
            stop(process)
            if code is None:
                log(f"diagnostic pid {process.pid} stopped at end of boot wait")
            else:
                log(f"diagnostic pid {process.pid} exited {code}")
        if boot.poll() is None:
            stop(boot, TERMINATE_GRACE)


def interrupted(signum, _frame):
    raise SystemExit(128 + signum)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--udid", type=uuid.UUID, required=True)
    parser.add_argument("--timeout", type=float, default=900)
    args = parser.parse_args()
    if not 0 < args.timeout < float("inf"):
        parser.error("timeout must be positive and finite")
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    sys.exit(wait_booted(str(args.udid).upper(), args.timeout))
