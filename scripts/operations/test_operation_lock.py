"""Operation locks must serialize unrelated callers and allow nested release steps."""
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("operation_lock.py")


class OperationLockTests(unittest.TestCase):
    def test_busy_release_returns_75_without_running_command(self):
        with tempfile.TemporaryDirectory() as home:
            lock = Path(home) / ".local/state/persona/locks/release.lock"
            lock.parent.mkdir(parents=True)
            with lock.open("a") as stream:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                env = dict(os.environ, HOME=home)
                env.pop("PERSONA_OPERATION_LOCKS", None)
                result = subprocess.run([sys.executable, str(SCRIPT), "release", "--", sys.executable,
                                         "-c", "raise Exception('must not run')"], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 75, result.stderr)
                self.assertIn("busy", result.stdout)

    def test_nested_release_and_result(self):
        with tempfile.TemporaryDirectory() as home:
            env = dict(os.environ, HOME=home)
            env.pop("PERSONA_OPERATION_LOCKS", None)
            command = [sys.executable, str(SCRIPT), "release", "--", sys.executable,
                       str(SCRIPT), "release", "--", sys.executable, "-c", "raise SystemExit(3)"]
            result = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 3, result.stderr)
            self.assertEqual(result.stdout.count("acquired"), 1)

    def test_deploy_uses_existing_promotion_lock(self):
        with tempfile.TemporaryDirectory() as home:
            root = Path(home) / ".persona"
            lock = root / "releases/hub/.publish.lock"
            lock.parent.mkdir(parents=True)
            import socket
            cfg = Path(home) / "config.json"
            cfg.write_text(json.dumps({"hub": {"machine": "hub"}, "machines": [
                {"id": "hub", "hostnames": [socket.gethostname()]}]}))
            with lock.open("a") as stream:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                env = dict(os.environ, HOME=home, PERSONA_CONFIG=str(cfg), PERSONA_HOME=str(root))
                env.pop("PERSONA_OPERATION_LOCKS", None)
                result = subprocess.run([sys.executable, str(SCRIPT), "deploy", "--", sys.executable,
                                         "-c", "raise Exception('must not run')"], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 75, result.stderr)


if __name__ == "__main__":
    unittest.main()
