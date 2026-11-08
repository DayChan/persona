import ast
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from urllib.error import URLError
from urllib.request import urlopen


ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = ROOT / "scripts"
ALIASES = {
    "sim.sh": "app/sim.sh", "android.sh": "app/android.sh", "ui-test.sh": "app/ui-test.sh",
    "mac_shot.sh": "app/mac_shot.sh", "deploy.sh": "deploy/deploy.sh", "publish.sh": "release/publish.sh",
    "release.sh": "release/release.sh", "test-server.sh": "test/test-server.sh",
    "check-fast.sh": "test/check-fast.sh",
}


class ScriptLayoutTests(unittest.TestCase):
    def test_top_level_contains_only_shared_tools_and_generic_relocation_entries(self):
        files = {p.name for p in SCRIPTS.iterdir() if p.is_file()}
        self.assertEqual(files, {"README.md", "persona_config.py", "persona_identity.py", "privacy_check.py",
                                 "compatibility-identifiers.json", "jarvis_config.py", *ALIASES})
        directories = {p.name for p in SCRIPTS.iterdir() if p.is_dir() and p.name != "__pycache__"}
        self.assertEqual(directories, {"app", "assets", "ci", "deploy", "diag", "gen", "hooks", "i18n", "migrate", "mock", "operations", "release", "test"})
        self.assertEqual(os.readlink(SCRIPTS / "jarvis_config.py"), "persona_config.py")

    def test_compatibility_entries_preserve_arguments_environment_stdin_and_exit(self):
        for alias, target in ALIASES.items():
            with self.subTest(alias=alias), tempfile.TemporaryDirectory(prefix="script compatibility ") as directory:
                root = Path(directory)
                entry = root / "scripts" / alias
                entry.parent.mkdir()
                shutil.copy2(SCRIPTS / alias, entry)
                implementation = root / "scripts" / target
                implementation.parent.mkdir()
                implementation.write_text(
                    "#!/usr/bin/env python3\nimport json,os,sys\n"
                    "print(json.dumps({'args':sys.argv[1:],'cwd':os.getcwd(),'env':os.environ['LAYOUT_TEST'],"
                    "'stdin':sys.stdin.read()}))\nraise SystemExit(23)\n")
                implementation.chmod(0o755)
                result = subprocess.run([str(entry), "one argument", "", "--flag", "a;$b"], cwd=root,
                                        env={**os.environ, "LAYOUT_TEST": "preserved"}, input="pipe input\n",
                                        text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode, 23, result.stderr)
                self.assertEqual(json.loads(result.stdout), {
                    "args": ["one argument", "", "--flag", "a;$b"], "cwd": str(root.resolve()),
                    "env": "preserved", "stdin": "pipe input\n",
                })

    def test_python_and_shell_syntax(self):
        for path in SCRIPTS.rglob("*"):
            if not path.is_file() or "__pycache__" in path.parts:
                continue
            with self.subTest(script=str(path.relative_to(ROOT))):
                if path.suffix == ".py":  # no syntax newer than macOS's own Python 3.9, even when run on a newer one
                    ast.parse(path.read_text(), filename=str(path), feature_version=(3, 9))
                elif path.suffix == ".sh" or path.parent.name == "hooks":
                    shell = "zsh" if path.read_text().splitlines()[0].endswith("zsh") else "bash"
                    if not shutil.which(shell):
                        continue
                    result = subprocess.run([shell, "-n", str(path)], capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode, 0, result.stderr)

    def test_annotations_run_on_the_macs_own_python(self):
        # macOS ships Python 3.9, which evaluates annotations when it runs a def or an annotated assignment: an
        # `X | None` there raises TypeError (#579) unless the module postpones them. Checked from the source, so it
        # fails on a newer Python too.
        def has_union(node):
            return any(isinstance(n, ast.BinOp) and isinstance(n.op, ast.BitOr) for n in ast.walk(node))

        for path in SCRIPTS.rglob("*.py"):
            if "__pycache__" in path.parts:
                continue
            tree = ast.parse(path.read_text(), filename=str(path))
            postponed = any(isinstance(n, ast.ImportFrom) and n.module == "__future__"
                            and any(a.name == "annotations" for a in n.names) for n in tree.body)
            annotations = []
            for node in ast.walk(tree):
                if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    args = node.args
                    annotations += [x.annotation for x in
                                    args.posonlyargs + args.args + args.kwonlyargs + [args.vararg, args.kwarg]
                                    if x is not None and x.annotation is not None]
                    annotations += [node.returns] if node.returns is not None else []
                elif isinstance(node, ast.AnnAssign):
                    annotations.append(node.annotation)
            lines = sorted(n.lineno for n in annotations if has_union(n))
            with self.subTest(script=str(path.relative_to(ROOT))):
                self.assertTrue(postponed or not lines,
                                f"`X | Y` annotations on lines {lines} need `from __future__ import annotations`")

    def test_generators_and_entitlements_resolve_the_repository_root(self):
        for name in ("assets/make_icon.py", "assets/make_alt_icons.py", "assets/make_persona.py", "release/entitlements.py"):
            path = SCRIPTS / name
            tree = ast.parse(path.read_text())
            expression = next(node.value for node in tree.body if isinstance(node, ast.Assign)
                              and any(isinstance(t, ast.Name) and t.id == "ROOT" for t in node.targets))
            found = eval(compile(ast.Expression(expression), str(path), "eval"), {"os": os, "__file__": str(path)})
            with self.subTest(script=name):
                self.assertEqual(Path(found), ROOT)

    def test_release_modules_import_shared_config_from_another_directory(self):
        with tempfile.TemporaryDirectory(prefix="script-import-test-") as directory:
            config = Path(directory) / "config.json"
            config.write_text(json.dumps({"app": {"bundle_id": "com.example.persona", "team_id": "ABCDE12345"},
                                          "app_store": {"app_id": "1234567890"}}))
            code = (
                "import importlib.util,pathlib,sys; "
                "sys.path.insert(0,sys.argv[1]); "
                "import asc,asc_signing,asc_testflight,check_bundle_privacy; "
                "assert pathlib.Path(asc.persona_config.ROOT)==pathlib.Path(sys.argv[2]); "
                "print(asc_testflight.APP_ID)"
            )
            legacy_config = Path(directory) / "legacy-config.json"
            legacy_config.write_text(json.dumps({"app": {"bundle_id": "com.example.persona", "team_id": "ABCDE12345"},
                                                 "app_store": {"app_id": "9876543210"}}))
            former_prefix = ('jar' + 'vis').upper() + '_'
            environment = {key: value for key, value in os.environ.items()
                           if key not in {"PERSONA_CONFIG", former_prefix + "CONFIG"}}
            environment['HOME'] = directory
            cases = {
                "current config": {"PERSONA_CONFIG": str(config)},
                "former config ignored": {"PERSONA_CONFIG": str(config), former_prefix + "CONFIG": str(legacy_config)},
            }
            for name, variables in cases.items():
                with self.subTest(name=name):
                    result = subprocess.run([sys.executable, "-I", "-c", code, str(SCRIPTS / "release"), str(ROOT)],
                                            cwd=directory, env={**environment, **variables},
                                            capture_output=True, text=True, timeout=15)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.strip(), "1234567890")

    def test_runtime_cli_has_one_canonical_entry(self):
        self.assertEqual(os.readlink(SCRIPTS / 'app' / (('jar' + 'vis') + 'ctl.py')), 'personactl.py')
        for name in ("personactl.py", ('jar' + 'vis') + 'ctl.py'):
            with self.subTest(entry=name):
                result = subprocess.run([sys.executable, "-I", str(SCRIPTS / "app" / name), "--help"],
                                        capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("usage:", result.stdout)

    def test_mock_scenarios_starts_its_relocated_child(self):
        reservations = []
        for _ in range(2):
            sock = socket.socket()
            sock.bind(("127.0.0.1", 0))
            reservations.append(sock)
        port, control = [sock.getsockname()[1] for sock in reservations]
        for sock in reservations:
            sock.close()
        with tempfile.TemporaryDirectory(prefix="script-mock-layout-") as directory:
            env = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
            with open(Path(directory) / "mock.log", "w+") as log:
                child = subprocess.Popen([sys.executable, "-I", str(SCRIPTS / "mock/mock_scenarios.py"),
                                          "--port", str(port), "--control", str(control), "--scenario", "quiet"],
                                         cwd=directory, env=env, stdout=log, stderr=subprocess.STDOUT,
                                         start_new_session=True)
                try:
                    deadline = time.monotonic() + 15
                    while True:
                        if child.poll() is not None:
                            log.seek(0)
                            self.fail("mock_scenarios exited: " + log.read())
                        try:
                            with urlopen(f"http://127.0.0.1:{control}/scenario", timeout=1) as response:
                                state = json.load(response)
                            break
                        except (OSError, URLError):
                            if time.monotonic() >= deadline:
                                self.fail("mock_scenarios did not start within 15 seconds")
                            time.sleep(0.05)
                    self.assertEqual(state["scenario"], "quiet")
                    with urlopen(f"http://127.0.0.1:{port}/api/v1/machines", timeout=2) as response:
                        self.assertTrue(json.load(response))
                    with urlopen(f"http://127.0.0.1:{control}/scenario?name=offline", timeout=10) as response:
                        self.assertEqual(json.load(response)["scenario"], "offline")
                finally:
                    # Kill only this isolated process group, including a child that may still be starting.
                    import signal
                    try:
                        os.killpg(child.pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                    child.wait(timeout=10)


if __name__ == "__main__":
    unittest.main()
