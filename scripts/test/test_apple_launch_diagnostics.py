"""Launch diagnostics must fail closed without exposing another simulator or raw output."""
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_launch_diagnostics as launch
import apple_diagnostics

UDID = "11111111-1111-1111-1111-111111111111"
OTHER = "22222222-2222-2222-2222-222222222222"
START = datetime(2026, 1, 1, 12, tzinfo=timezone.utc)


class LaunchDiagnosticsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.output = Path(self.tmp.name) / "output"
        self.destination = Path(self.tmp.name) / "artifacts"
        self.output.mkdir()
        self.destination.mkdir()
        self.context = {"udid": UDID, "bundle_id": launch.BUNDLE, "started_at": START.isoformat()}
        self.save()
        (self.output / "simulator.log").write_text(UDID + "\n")
        self.calls = []

    def save(self):
        (self.output / launch.CONTEXT).write_text(json.dumps(self.context))

    def entry(self, **changes):
        entry = {"timestamp": "2026-01-01 12:01:00.000000+0000",
                 "processImagePath": "/System/Library/CoreSimulatorService",
                 "eventMessage": f"Unable to launch {launch.BUNDLE} on {UDID}: service unavailable",
                 "privateConfiguration": "DO NOT EXPORT", "extraCredential": "DO NOT EXPORT"}
        return dict(entry, **changes)

    def collect(self, entries=None, reader=None):
        def fake(command):
            self.calls.append(command)
            return json.dumps([self.entry()] if entries is None else entries).encode()
        launch.collect(self.output, self.destination, reader=reader or fake,
                       now=START + timedelta(minutes=17))

    def status(self):
        return (self.destination / "simulator-launch.status.txt").read_text()

    def test_exact_owned_identity_and_original_readiness_window(self):
        self.collect()
        command, = self.calls
        self.assertEqual(command[:7], ["/usr/bin/log", "show", "--style", "json", "--info", "--debug", "--start"])
        self.assertEqual(command[7], "2026-01-01 12:00:00+00:00")
        self.assertEqual(command[command.index("--end") + 1], "2026-01-01 12:15:00+00:00")
        predicate = command[command.index("--predicate") + 1]
        self.assertIn(f'(eventMessage CONTAINS[c] "{UDID}") AND ', predicate)
        self.assertIn(f'(eventMessage CONTAINS "{launch.BUNDLE}") AND ', predicate)
        self.assertNotIn(OTHER, predicate)
        self.assertNotIn("--last", command)
        exported = json.loads((self.destination / "simulator-launch.json").read_text())
        self.assertEqual(len(exported), 1)
        self.assertEqual(set(exported[0]), {"timestamp", "process", "eventMessage"})
        self.assertNotIn("DO NOT EXPORT", json.dumps(exported))
        self.assertIn("captured 1 scoped events", self.status())

    def test_missing_invalid_or_mismatched_context_never_runs_a_command(self):
        cases = [None, {**self.context, "udid": OTHER}, {**self.context, "udid": '" OR TRUE'},
                 {**self.context, "bundle_id": "org.example.someone.else"},
                 {**self.context, "started_at": "invalid"},
                 {**self.context, "started_at": "2026-01-01T12:00:00"},
                 {**self.context, "started_at": "2026-01-01T13:00:00+00:00"},
                 {**self.context, "started_at": "2025-12-31T12:00:00+00:00"},
                 {**self.context, "credentials": "DO NOT EXPORT"}, {}]
        for context in cases:
            with self.subTest(context=context):
                path = self.output / launch.CONTEXT
                if context is None:
                    path.unlink()
                else:
                    path.write_text(json.dumps(context))
                self.collect()
                self.assertEqual(self.calls, [])
                self.assertIn("unavailable", self.status())
                self.assertFalse((self.destination / "simulator-launch.json").exists())
                self.assertFalse((self.destination / launch.CONTEXT).exists())

    def test_context_symlink_is_not_read_or_copied(self):
        path = self.output / launch.CONTEXT
        path.unlink()
        private = self.output / "private-config.json"
        private.write_text(json.dumps(self.context))
        path.symlink_to(private)
        self.collect()
        self.assertFalse(self.calls)
        self.assertEqual({p.name for p in self.destination.iterdir()}, {"simulator-launch.status.txt"})

    def test_both_scope_files_reject_links_large_files_and_fifos(self):
        for name in (launch.CONTEXT, "simulator.log"):
            for kind in ("symlink", "large", "fifo", "directory"):
                with self.subTest(name=name, kind=kind):
                    path = self.output / name
                    original = path.read_bytes()
                    path.unlink()
                    if kind == "symlink":
                        target = self.output / "private-input"
                        target.write_bytes(original)
                        path.symlink_to(target)
                    elif kind == "large":
                        path.write_bytes(b"x" * 1025)
                    elif kind == "fifo":
                        os.mkfifo(path)
                    else:
                        path.mkdir()
                    self.collect()
                    self.assertFalse(self.calls)
                    self.assertIn("unavailable", self.status())
                    self.assertFalse((self.destination / "simulator-launch.json").exists())
                    if kind == "directory":
                        path.rmdir()
                    else:
                        path.unlink()
                    path.write_bytes(original)

    def test_output_is_rechecked_and_empty_is_not_evidence(self):
        self.collect(entries=[
            self.entry(eventMessage=f"launch {launch.BUNDLE} on {OTHER}"),
            self.entry(eventMessage=f"launch {launch.BUNDLE}.other on {UDID}"),
            self.entry(processImagePath="/Applications/UnrelatedApp"),
            self.entry(timestamp="2026-01-01 11:59:59+0000"),
            self.entry(timestamp="2026-01-01 12:15:01+0000"),
            self.entry(eventMessage=f"launch {launch.BUNDLE} on {UDID} environment SECRET=DO NOT EXPORT"),
        ])
        self.assertIn("no matching launch metadata; no cause established", self.status())
        self.assertFalse((self.destination / "simulator-launch.json").exists())
        self.assertEqual(len(self.calls), 1)  # No broader fallback query.

    def test_bad_output_timeout_and_command_errors_remain_unavailable(self):
        for value in (b"not JSON DO NOT EXPORT", b"{}", b"x" * (launch.MAX_BYTES + 1),
                      TimeoutError("DO NOT EXPORT"), OSError("DO NOT EXPORT")):
            with self.subTest(kind=type(value).__name__):
                def reader(command):
                    self.calls.append(command)
                    if isinstance(value, Exception):
                        raise value
                    return value
                previous = len(self.calls)
                self.collect(reader=reader)
                self.assertEqual(len(self.calls), previous + 1)
                self.assertIn("unavailable", self.status())
                self.assertNotIn("DO NOT EXPORT", self.status())
                self.assertFalse((self.destination / "simulator-launch.json").exists())

    def test_real_reader_enforces_timeout_bytes_and_exit_status(self):
        self.assertEqual(launch.bounded_output([sys.executable, "-c", "print('ok')"]), b"ok\n")
        with self.assertRaises(TimeoutError):
            launch.bounded_output([sys.executable, "-c", "import signal; signal.pause()"], timeout=0.05)
        with self.assertRaisesRegex(ValueError, "byte limit"):
            launch.bounded_output([sys.executable, "-c", "import os; os.write(1,b'x'*4096)"], limit=64)
        with self.assertRaisesRegex(ValueError, "exit 7"):
            launch.bounded_output([sys.executable, "-c", "raise SystemExit(7)"])
        self.assertEqual(launch.TIMEOUT, 20)
        self.assertEqual(launch.MAX_BYTES, 128 * 1024)

    def test_existing_collector_calls_the_scoped_capture(self):
        def runner(command, **kwargs):
            return subprocess.CompletedProcess(command, 0, "{}", "")
        with mock.patch.object(launch, "collect") as capture:
            apple_diagnostics.collect(self.output, self.destination, runner)
        capture.assert_called_once_with(self.output, self.destination)
        self.assertEqual((self.destination / "simctl-list.status.txt").read_text(), "0\n")

    def test_record_cli_only_writes_synthetic_scope(self):
        command = [sys.executable, str(ROOT / "scripts/ci/apple_launch_diagnostics.py"),
                   "--record", str(self.output / launch.CONTEXT), "--udid", UDID, "--bundle-id", launch.BUNDLE]
        result = subprocess.run(command, text=True, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        context = json.loads((self.output / launch.CONTEXT).read_text())
        self.assertEqual(set(context), {"udid", "bundle_id", "started_at"})
        self.assertEqual(context["udid"], UDID)
        self.assertEqual(datetime.fromisoformat(context["started_at"]).utcoffset(), timedelta(0))


if __name__ == "__main__":
    unittest.main()
