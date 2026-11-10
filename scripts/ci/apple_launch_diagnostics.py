"""Bounded host launch metadata for one disposable CI simulator and readiness window."""

import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import selectors
import stat
import subprocess
import time
import uuid

CONTEXT = "simulator-readiness-context.json"
BUNDLE = "org.example.persona.ci"
READINESS_SECONDS = 900
TIMEOUT = 20
MAX_BYTES = 128 * 1024
PROCESSES = ("CoreSimulatorService", "launchd_sim", "runningboardd", "SpringBoard", "testmanagerd", "lsd")


def identity(udid, bundle):
    if str(uuid.UUID(udid)).upper() != udid or bundle != BUNDLE:
        raise ValueError("invalid CI identity")


def record(path, udid, bundle):
    identity(udid, bundle)
    path.write_text(json.dumps({"udid": udid, "bundle_id": bundle,
                               "started_at": datetime.now(timezone.utc).isoformat()}))


def read_scope_file(path):
    # Validate the opened inode too: a FIFO or swapped symlink must never block collection.
    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > 1024:
            raise ValueError("invalid scope file")
        data = os.read(fd, 1025)
        if len(data) > 1024:
            raise ValueError("scope file exceeded byte limit")
        return data.decode()
    finally:
        os.close(fd)


def scope(output, now):
    context = json.loads(read_scope_file(output / CONTEXT))
    if set(context) != {"udid", "bundle_id", "started_at"}:
        raise ValueError("invalid context fields")
    identity(context["udid"], context["bundle_id"])
    if read_scope_file(output / "simulator.log").strip() != context["udid"]:
        raise ValueError("owned simulator does not match")
    start = datetime.fromisoformat(context["started_at"])
    if start.utcoffset() != timedelta(0) or start > now or now - start > timedelta(hours=2):
        raise ValueError("invalid context time")
    # Never widen to the rest of the job, even if a later full-suite phase fails.
    end = min(now, start + timedelta(seconds=READINESS_SECONDS))
    if end <= start:
        raise ValueError("empty context time window")
    return context, start, end


def bounded_output(command, timeout=TIMEOUT, limit=MAX_BYTES):
    """Keep log in the collector's process group, subject to its outer deadline too."""
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    data = bytearray()
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError("launch log timed out")
                for key, _ in selector.select(remaining):
                    chunk = os.read(key.fd, min(4096, limit + 1 - len(data)))
                    if not chunk:
                        selector.unregister(key.fd)
                        break
                    data.extend(chunk)
                    if len(data) > limit:
                        raise ValueError("launch log exceeded byte limit")
        code = process.wait(timeout=max(0, deadline - time.monotonic()))
        if code:
            raise ValueError(f"launch log command failed (exit {code})")
        return bytes(data)
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdout.close()


def collect(output, destination, reader=bounded_output, now=None):
    status = destination / "simulator-launch.status.txt"
    try:
        context, start, end = scope(output, now or datetime.now(timezone.utc))
        udid, bundle = context["udid"], context["bundle_id"]
        predicate = (f'(eventMessage CONTAINS[c] "{udid}") AND '
                     f'(eventMessage CONTAINS "{bundle}") AND (' +
                     " OR ".join(f'process == "{name}"' for name in PROCESSES) + ")")
        # Explicit UTC offsets avoid interpreting CI timestamps in the host's local zone.
        command = ["/usr/bin/log", "show", "--style", "json", "--info", "--debug",
                   "--start", start.strftime("%Y-%m-%d %H:%M:%S") + "+00:00",
                   "--end", end.strftime("%Y-%m-%d %H:%M:%S") + "+00:00", "--predicate", predicate]
        raw = reader(command)
        if len(raw) > MAX_BYTES:
            raise ValueError("launch log exceeded byte limit")
        entries = json.loads(raw)
        if not isinstance(entries, list):
            raise ValueError("invalid launch log format")
        kept = []
        for entry in entries:
            message = entry.get("eventMessage", "")
            process = Path(entry.get("processImagePath", "")).name
            stamp = datetime.fromisoformat(entry.get("timestamp", ""))
            # Recheck scope before exporting anything, including with a faulty log predicate.
            if (process not in PROCESSES or stamp.utcoffset() is None or not start <= stamp <= end or
                    not re.search(r"(?<![\w-])" + re.escape(udid) + r"(?![\w-])", message, re.I) or
                    not re.search(r"(?<![\w.-])" + re.escape(bundle) + r"(?![\w.-])", message)):
                continue
            if re.search(r"environment|DYLD_|bearer\s|token=|password|secret|credential", message, re.I):
                continue
            kept.append({"timestamp": stamp.isoformat(), "process": process, "eventMessage": message})
        if not kept:
            status.write_text("unavailable: no matching launch metadata; no cause established\n")
            return
        encoded = json.dumps(kept, ensure_ascii=False).encode()
        if len(encoded) > MAX_BYTES:
            raise ValueError("export exceeded byte limit")
        (destination / "simulator-launch.json").write_bytes(encoded)
        (destination / CONTEXT).write_text(json.dumps(context))
        status.write_text(f"captured {len(kept)} scoped events; diagnosis still required\n")
    except (OSError, ValueError, TypeError, KeyError, AttributeError, subprocess.SubprocessError):
        # Never export raw command errors, invalid input, or a broader fallback query.
        status.write_text("unavailable: missing/invalid scope, command failure, timeout or output limit\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--record", type=Path, required=True)
    parser.add_argument("--udid", required=True)
    parser.add_argument("--bundle-id", required=True)
    args = parser.parse_args()
    record(args.record, args.udid, args.bundle_id)


if __name__ == "__main__":
    main()
