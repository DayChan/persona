import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts/ci" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


server = load("server-shards")
python = load("python-tests")


class ShardTests(unittest.TestCase):
    def test_every_server_package_is_selected_once(self):
        packages = ["example/server/internal/" + name for name in
                    ("node", "hub", "voice", "assistant", "devices", "extmgr", "newpackage", "nodecmd", "hubedge")]
        packages += ["example/server/internal/node/newpackage", "example/server/internal/assistant/newpackage", "example/server/cmd/personad"]
        selected = [package for shard in server.SHARDS for package in server.select_packages(packages, shard)]
        self.assertCountEqual(selected, packages)
        self.assertEqual(len(selected), len(set(selected)))
        self.assertEqual(server.select_packages(packages, "node"), [packages[0], packages[-3]])
        self.assertIn("example/server/internal/hubedge", server.select_packages(packages, "other"))
        with self.assertRaises(ValueError):
            server.select_packages(packages, "typo")

    def test_python_shards_cover_current_and_new_suites_once(self):
        suites = list(ROOT.joinpath("scripts").rglob("test_*.py"))
        suites = [suite for suite in suites if "node_modules" not in suite.parts]
        suites.append(ROOT / "scripts/test/test_new_suite.py")
        for count in (1, 2, 3):
            selected = [suite for index in range(count) for suite in python.select_suites(suites, index, count)]
            self.assertCountEqual(selected, suites)
            self.assertEqual(len(selected), len(set(selected)))
        for index, count in ((-1, 2), (2, 2), (0, 0)):
            with self.assertRaises(ValueError):
                python.select_suites(suites, index, count)

    def test_server_workflow_covers_build_and_every_test_shard(self):
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        self.assertIn("shard: [node, managers, other, build]", workflow)
        self.assertIn("fail-fast: false", workflow)
        self.assertIn('scripts/ci/server.sh "$CI_SERVER_SHARD"', workflow)
        script = (ROOT / "scripts/ci/server.sh").read_text()
        for label in ("Go formatting", "Server vet", "Server build", "Windows node build and vet", "Go SDK tests"):
            self.assertIn(label, script)
        self.assertIn('lane="${1:-all}"', script)

    def test_rpc_race_keeps_budgets_and_leaves_browser_node_isolated(self):
        # Intercept only the CI command launcher: this checks lane selection,
        # not actual Go/Postgres/browser execution or hosted acceptance.
        with tempfile.TemporaryDirectory(prefix="persona-ci-lanes-") as directory:
            root = Path(directory)
            trace = root / "commands.jsonl"
            launcher = root / "python3"
            launcher.write_text(
                f"#!{sys.executable}\n"
                "import json, os, sys\n"
                "with open(os.environ['CI_TRACE'], 'a') as stream:\n"
                "    stream.write(json.dumps(sys.argv[1:]) + '\\n')\n"
            )
            launcher.chmod(0o755)
            docker = root / "docker"
            docker.write_text("#!/bin/bash\nexit 0\n")
            docker.chmod(0o755)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "TMPDIR": str(root), "CI_TRACE": str(trace)}
            for lane in ("all", "build", "node", "managers", "other"):
                with self.subTest(lane=lane):
                    trace.write_text("")
                    subprocess.run(["bash", str(ROOT / "scripts/ci/server.sh"), lane],
                                   env=env, check=True, capture_output=True, timeout=15)
                    commands = [json.loads(line) for line in trace.read_text().splitlines()]
                    rpc = [command for command in commands if "Node RPC withdrawal race tests" in command]
                    self.assertEqual(len(rpc), int(lane in ("all", "build")))
                    if rpc:
                        command = rpc[0]
                        self.assertEqual(command[command.index("--timeout") + 1], "420")
                        self.assertIn('go test -race -p 2 -count=1 -timeout=5m -run "^TestNodeRPC" ./internal/node', command[-1])
                    labels = [command[command.index("--label") + 1] for command in commands]
                    for shard in ("node", "managers", "other"):
                        self.assertEqual(f"Server {shard} tests with Postgres" in labels,
                                         lane in ("all", shard))
                    self.assertEqual("Browser runner resources" in labels, lane in ("all", "node"))
                    self.assertEqual("Process RPC lifecycle race tests" in labels, lane in ("all", "other"))


if __name__ == "__main__":
    unittest.main()
