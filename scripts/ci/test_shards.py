import importlib.util
from pathlib import Path
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

    def test_source_server_shard_installs_the_pinned_bundler(self):
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        setup = """      - uses: oven-sh/setup-bun@v2
        if: matrix.shard == 'other'
        with:
          bun-version: '1.4.2'
"""
        self.assertIn(setup, workflow)
        self.assertLess(workflow.index(setup), workflow.index("      - name: Server checks"))
        for filename in ("ci.yml", "runtimes-test.yml"):
            self.assertIn("bun-version: '1.4.2'", (ROOT / ".github/workflows" / filename).read_text())
        source_packages = ["example/server/internal/extension/denohost", "example/server/internal/extmgr"]
        self.assertEqual(server.select_packages(source_packages, "other"), source_packages)

    def test_server_workflow_covers_build_and_every_test_shard(self):
        workflow = (ROOT / ".github/workflows/server-test.yml").read_text()
        self.assertIn("shard: [node, managers, other, build]", workflow)
        self.assertIn("fail-fast: false", workflow)
        self.assertIn('scripts/ci/server.sh "$CI_SERVER_SHARD"', workflow)
        script = (ROOT / "scripts/ci/server.sh").read_text()
        for label in ("Go formatting", "Server vet", "Server build", "Windows node build and vet", "Go SDK tests"):
            self.assertIn(label, script)
        self.assertIn('lane="${1:-all}"', script)


if __name__ == "__main__":
    unittest.main()
