"""Server-test launcher isolation with synthetic profiles, SSH, resolver and Go."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("server_test_env", ROOT / "scripts/test/server_test_env.py")
ISOLATION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ISOLATION)


class ServerTestEnvironmentTests(unittest.TestCase):
    def test_parent_proxy_routes_are_removed_without_changing_the_parent_or_build_inputs(self):
        proxies = {key: "http://proxy.example.invalid:8080" for key in (
            "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy",
            "NO_PROXY", "no_proxy")}
        parent = dict(proxies, PATH="/usr/bin", GOPROXY="https://modules.example.invalid",
                      PERSONA_HOME="/example/live", CODEX_HOME="/example/live/codex")
        original = dict(parent)
        child = ISOLATION.clean_environment(parent)
        self.assertEqual(parent, original)
        self.assertFalse(set(proxies) & set(child))
        self.assertEqual(child["PATH"], "/usr/bin")
        self.assertEqual(child["GOPROXY"], parent["GOPROXY"])
        self.assertNotIn("PERSONA_HOME", child)
        self.assertNotIn("CODEX_HOME", child)
        # A fixture's explicit routing is set after launch and must remain usable.
        child["HTTPS_PROXY"] = "http://fixture.example.invalid:8080"
        self.assertEqual(child["HTTPS_PROXY"], "http://fixture.example.invalid:8080")

    def test_database_url_targets_only_the_dedicated_test_database(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "hub.json"
            for source, expected in (
                ("postgres://user:synthetic@localhost:5432/persona?sslmode=disable", "postgres://user:synthetic@localhost:5432/persona_test?sslmode=disable"),
                ("postgresql://user:synthetic@localhost/persona", "postgresql://user:synthetic@localhost/persona_test"),
                ("postgres://user:p%40ss@[::1]:5432/persona?sslmode=require", "postgres://user:p%40ss@[::1]:5432/persona_test?sslmode=require"),
                # a hub set up before the rename keeps its jarvis database (#6149): the tests sit next to it
                ("postgres://user:synthetic@localhost:18661/jarvis?sslmode=disable", "postgres://user:synthetic@localhost:18661/jarvis_test?sslmode=disable"),
            ):
                with self.subTest(source=source):
                    config.write_text(json.dumps({"database_url": source}))
                    expected_parts = urlsplit(expected)
                    query = dict(parse_qsl(expected_parts.query, keep_blank_values=True))
                    for key, value in {"passfile": "", "sslcert": "", "sslkey": "", "sslrootcert": ""}.items():
                        query.setdefault(key, value)
                    self.assertEqual(ISOLATION.test_database_url(config),
                                     urlunsplit(expected_parts._replace(query=urlencode(query))))
                    self.assertNotEqual(source, expected)
            for source in (
                "postgres://user:synthetic@localhost/custom", "postgres://user:synthetic@localhost/persona_test",
                "postgres://user:synthetic@localhost/jarvis_test", "postgres://user:synthetic@localhost/jarvis/",
                "postgres://user:synthetic@localhost/%6aarvis", "postgres://user:synthetic@localhost/persona/",
                "postgres://user:synthetic@localhost/persona#fragment", "https://example.com/persona", "postgres:///persona", 42, None,
            ):
                with self.subTest(source=source):
                    config.write_text(json.dumps({"database_url": source}))
                    with mock.patch.object(ISOLATION.subprocess, "run") as run:
                        with self.assertRaises(ValueError):
                            ISOLATION.run_tests(config, ["go", "test"], {})
                        run.assert_not_called()

    def test_query_overrides_and_ambiguous_queries_stop_before_any_child_process(self):
        queries = (
            "dbname=persona", "database=persona", "db%6Eame=persona", "%64atabase=persona",
            "DBNAME=persona", "Database=persona", "dbname=", "dbname=persona_test",
            "dbname=persona&dbname=persona_test", "dbname=persona_test&db%6Eame=persona",
            "database=persona_test&dbname=persona", "%2564bname=persona",
            "service=fixture", "serv%69ce=fixture", "servicefile=/synthetic/config",
            "service=fixture&service=other", "passfile=/synthetic/passwords",
            "host=example.invalid", "port=5432", "user=other", "password=synthetic", "options=fixture",
            "sslmode=disable&sslmode=require", "sslmode=disable&ssl%6dode=require", "SSLMode=disable",
            "sslmode=%xx", "sslmode=%", "sslmode=%FF", "sslmode=disable;dbname=persona",
            "sslmode", "=fixture", "sslmode=disable&&connect_timeout=3",
        )
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "hub.json"
            for query in queries:
                with self.subTest(query=query):
                    config.write_text(json.dumps({"database_url": "postgres://fixture:synthetic@127.0.0.1/persona?" + query}))
                    with mock.patch.object(ISOLATION.subprocess, "run") as run:
                        with self.assertRaises(ValueError):
                            ISOLATION.run_tests(config, ["go", "test"], {})
                        run.assert_not_called()

    def test_go_test_environment_has_no_runtime_profile_and_is_cleaned_up(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            profile = root / "live-profile"
            config = root / "hub.json"
            config.write_text(json.dumps({"database_url": "postgres://user:synthetic@localhost/persona"}))
            environment = {
                "HOME": str(root / "real-home"), "PATH": str(profile / "bin") + os.pathsep + "/usr/bin",
                "PERSONA_HOME": str(profile),
                "PERSONA_CONFIG": str(profile / "config.json"),
                "PERSONA_TOKEN": "synthetic-live-token", "PERSONA_DATABASE_URL": "postgres://example.invalid/production",
                "PERSONA_TEST_DATABASE_URL": "postgres://example.invalid/not_test",
                "PERSONA_OTHER_SETTING": "synthetic",
                "PGDATABASE": "persona", "PGUSER": "live-user", "PGPASSWORD": "synthetic-live-password",
                "PGHOST": "example.invalid", "PGPORT": "5432", "PGOPTIONS": "synthetic",
                "PGSERVICE": "live", "PGSERVICEFILE": str(profile / "service"),
                "PGPASSFILE": str(profile / "passwords"), "PGSSLCERT": str(profile / "certificate"),
                "PG_FUTURE_SETTING": "synthetic",
                "CLAUDE_CONFIG_DIR": str(profile / "claude"), "CODEX_HOME": str(profile / "codex"),
                "XDG_CONFIG_HOME": str(profile / "xdg"), "ZDOTDIR": str(profile / "shell"),
                "BASH_ENV": str(profile / "shellrc"), "ENV": str(profile / "envrc"), "GOTOOLCHAIN": "local",
                "HTTPS_PROXY": "http://proxy.example.invalid:8080", "https_proxy": "http://proxy.example.invalid:8080",
            }
            caches = {key: str(root / key.lower()) for key in ("GOCACHE", "GOMODCACHE", "GOPATH")}
            homes = []

            def run(command, **kwargs):
                child = kwargs["env"]
                if command[:2] == ["go", "env"]:
                    self.assertFalse(any(key.startswith(("PERSONA_", "PG")) for key in child))
                    self.assertEqual(child["HTTPS_PROXY"], environment["HTTPS_PROXY"])
                    self.assertEqual(child["https_proxy"], environment["https_proxy"])
                    return subprocess.CompletedProcess(command, 0, json.dumps(caches))
                homes.append(Path(child["HOME"]))
                self.assertTrue(homes[-1].is_dir())
                self.assertNotEqual(child["HOME"], environment["HOME"])
                self.assertTrue(Path(child["TMPDIR"]).is_dir())
                self.assertEqual(Path(child["HOME"]), Path(child["HOME"]).resolve())
                self.assertEqual({key for key in child if key.startswith(("PERSONA_"))},
                                 {"PERSONA_TEST_DATABASE_URL"})
                self.assertEqual(child['JARVIS_TEST_DATABASE_URL'], child['PERSONA_TEST_DATABASE_URL'])
                self.assertEqual(urlsplit(child["PERSONA_TEST_DATABASE_URL"]).path, "/persona_test")
                self.assertFalse(any(key.startswith("PG") for key in child))
                self.assertNotIn("HTTPS_PROXY", child)
                self.assertNotIn("https_proxy", child)
                self.assertFalse(any(str(profile) in value for value in child.values()))
                self.assertEqual(child["GOTOOLCHAIN"], "local")
                for key, value in caches.items():
                    self.assertEqual(child[key], value)
                return subprocess.CompletedProcess(command, 23)

            with mock.patch.object(ISOLATION.subprocess, "run", side_effect=run):
                self.assertEqual(ISOLATION.run_tests(config, ["go", "test", "./..."], environment), 23)
            self.assertEqual(len(homes), 1)
            self.assertFalse(homes[0].exists())

    def test_cache_failure_never_starts_the_test_command(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "hub.json"
            config.write_text(json.dumps({"database_url": "postgres://user:synthetic@localhost/persona"}))
            for result in (subprocess.CompletedProcess(["go", "env"], 1, "synthetic-private-output"),
                           subprocess.CompletedProcess(["go", "env"], 0, "not-json"),
                           subprocess.CompletedProcess(["go", "env"], 0, "{}")):
                with self.subTest(result=result.returncode, output=result.stdout):
                    with mock.patch.object(ISOLATION.subprocess, "run", return_value=result) as run:
                        with self.assertRaises(ValueError):
                            ISOLATION.run_tests(config, ["go", "test"], {"PERSONA_TOKEN": "synthetic-token"})
                        self.assertEqual(run.call_count, 1)
                        self.assertEqual(run.call_args.args[0][:2], ["go", "env"])
                        self.assertNotIn("PERSONA_TOKEN", run.call_args.kwargs["env"])

    def test_actual_shell_launcher_with_fake_remote_and_go(self):
        with tempfile.TemporaryDirectory(prefix="server-launcher-test-") as directory:
            root = Path(directory).resolve()
            checkout = root / "checkout"
            tools = root / "tools"
            remote = root / "remote"
            profile = remote / "live-profile"
            for path in (checkout / "scripts/test", checkout / "scripts/mock", checkout / "server/internal/hub",
                         checkout / "spec", checkout / "apple",
                         checkout / "docs", checkout / "sdk/go/ext", checkout / "sdk/go/wire/schema",
                         checkout / "sdk/templates/go", checkout / "sdk/ts/src", checkout / "sdk/ts/node_modules/typescript",
                         checkout / "schema/extension/examples/invalid", tools, profile):
                path.mkdir(parents=True)
            # the extension page tests bundle the TypeScript SDK's sources on the hub; its node_modules stays here (#1947)
            (checkout / "sdk/ts/src/index.ts").write_text("export const fixture = true;\n")
            (checkout / "sdk/ts/node_modules/typescript/package.json").write_text('{"name":"typescript"}\n')
            (checkout / "docs/guide.md").write_text("# Fixture guide\n")
            (checkout / "sdk/templates/go/persona-extension.json").write_text('{"template":true}\n')
            sdk = checkout / "sdk/go"
            (sdk / "go.mod").write_text("module example.invalid/fixture-sdk\n")
            (sdk / "wire/schema/view.schema.json").write_text('{"fixture":true}\n')
            sdk_capture = root / "sdk-sync.json"
            remote_sdk = remote / ".cache/persona-test-checkout/sdk/go"
            schema = checkout / "schema/extension"
            (schema / "examples/View.fixture.json").write_text('{"valid":true}\n')
            (schema / "examples/invalid/View.fixture.json").write_text('{"valid":false}\n')
            schema_capture = root / "schema-sync.json"
            remote_schema = remote / ".cache/persona-test-checkout/schema/extension"
            shutil.copy2(ROOT / "scripts/test/test-server.sh", checkout / "scripts/test/test-server.sh")
            shutil.copy2(ROOT / "scripts/test/server_test_env.py", checkout / "scripts/test/server_test_env.py")
            (checkout / "scripts/persona_config.py").write_text("print('export PERSONA_HUB_SSH=fake-hub')\n")
            identity_calls = root / "identity-calls.jsonl"
            (checkout / "scripts/persona_identity.py").write_text(
                "import json,shlex,sys\n"
                f"open({str(identity_calls)!r},'a').write(json.dumps(sys.argv[1:])+'\\n')\n"
                f"root={str(profile)!r}\n"
                "print('export PERSONA_HOME='+shlex.quote(root))\n"
                "if '--prepare' in sys.argv: print('test -f \"$PERSONA_HOME/hub.json\"')\n")
            (profile / "hub.json").write_text(json.dumps({"database_url": "postgres://user:synthetic@localhost/persona"}))
            capture = root / "go-environment.json"
            cache_capture = root / "go-cache-environment.json"
            caches = {key: str(root / key.lower()) for key in ("GOCACHE", "GOMODCACHE", "GOPATH")}

            def tool(name, body):
                path = tools / name
                path.write_text("#!" + sys.executable + "\n" + body)
                path.chmod(0o755)

            tool("ssh", "import os,subprocess,sys\n"
                 f"env=os.environ.copy(); env['HOME']={str(remote)!r}\n"
                 "command=' '.join(sys.argv[2:])\n"
                 # Map the remote toolchain directory to the synthetic tool directory, not the host's real Go.
                 f"command=command.replace('/usr/local/go/bin', {str(tools)!r})\n"
                 "sys.exit(subprocess.run(['bash','-c',command],env=env).returncode)\n")
            tool("rsync", "import json,os,pathlib,shutil,sys\n"
                 "source=pathlib.Path(sys.argv[-2]); target=sys.argv[-1].split(':',1)[1]\n"
                 "destination=pathlib.Path(os.environ['HOME'])/target\n"
                 "if source.is_file():\n assert destination.parent.is_dir(); shutil.copy2(source,destination)\n"
                 "else:\n"
                 " assert source.is_dir() and destination.parent.is_dir()\n"
                 " if '--delete' in sys.argv and destination.exists(): shutil.rmtree(destination)\n"
                 # a plain --exclude NAME leaves NAME out; with --include filters (apple/, docs/) the fixture copies all
                 " skip=[a for i,a in enumerate(sys.argv) if i and sys.argv[i-1]=='--exclude'] if '--include' not in sys.argv else []\n"
                 " shutil.copytree(source,destination,dirs_exist_ok=True,ignore=shutil.ignore_patterns(*skip) if skip else None)\n"
                 "if str(source)=='sdk/go':\n"
                 f" pathlib.Path({str(sdk_capture)!r}).write_text(json.dumps(sys.argv[1:]))\n"
                 "if str(source)=='schema/extension':\n"
                 f" pathlib.Path({str(schema_capture)!r}).write_text(json.dumps(sys.argv[1:]))\n")
            tool("go", "import json,os,pathlib,sys\n"
                 "if sys.argv[1:2]==['env']:\n"
                 f" pathlib.Path({str(cache_capture)!r}).write_text(json.dumps(dict(os.environ)))\n"
                 f" print(json.dumps({caches!r})); sys.exit(0)\n"
                 "base=pathlib.Path.cwd().parent\n"
                 "sdk=base/'sdk/go'\n"
                 "assert (sdk/'go.mod').read_text()=='module example.invalid/fixture-sdk\\n'\n"
                 "assert (sdk/'wire/schema/view.schema.json').read_text()=='{\"fixture\":true}\\n'\n"
                 "assert not (sdk/'stale.go').exists()\n"
                 "schema=base/'schema/extension'\n"
                 "assert (schema/'examples/View.fixture.json').read_text()=='{\"valid\":true}\\n'\n"
                 "assert (schema/'examples/invalid/View.fixture.json').read_text()=='{\"valid\":false}\\n'\n"
                 "assert not (schema/'stale.json').exists()\n"
                 "assert (base/'docs/guide.md').read_text()=='# Fixture guide\\n'\n"
                 "assert (base/'sdk/templates/go/persona-extension.json').read_text()=='{\"template\":true}\\n'\n"
                 "assert (base/'sdk/ts/src/index.ts').read_text()=='export const fixture = true;\\n'\n"
                 "assert not (base/'sdk/ts/node_modules').exists()\n"
                 # Read from the package directory exactly as the hub's inboxBody test helper does.
                 "inbox_source=(base/'server/internal/hub/../../../scripts/mock/inbox_events.json').read_text()\n"
                 f"pathlib.Path({str(capture)!r}).write_text(json.dumps({{'args':sys.argv[1:],'env':dict(os.environ),'sdk_source':(sdk/'ext/fixture.go').read_text(),'inbox_source':inbox_source}}))\n"
                 "sys.exit(int(os.environ.get('FAKE_GO_EXIT','0')))\n")
            environment = {
                "PATH": str(tools) + os.pathsep + os.environ["PATH"], "HOME": str(remote),
                "PERSONA_HOME": str(profile),
                "PERSONA_CONFIG": str(profile / "production-config.json"), "PERSONA_TOKEN": "synthetic-live-token",
                "PERSONA_DATABASE_URL": "postgres://example.invalid/production", "PERSONA_SOMETHING_NEW": "synthetic",
                "PERSONA_TEST_DATABASE_URL": "postgres://example.invalid/not_test",
                "CLAUDE_CONFIG_DIR": str(profile / "claude"), "CODEX_HOME": str(profile / "codex"),
                "PGDATABASE": "persona", "PGHOST": "example.invalid", "PGPASSWORD": "synthetic-live-password",
                "PGSERVICE": "live", "PGSERVICEFILE": str(profile / "service"),
                "PGPASSFILE": str(profile / "passwords"), "PGOPTIONS": "synthetic", "PG_FUTURE_SETTING": "synthetic",
            }
            for index, (args, expected, extra) in enumerate((
                ([], ["test", "-count=1", "-p", "1", "./..."], {}),
                (["-run", "TestFixture", "./internal/example"], ["test", "-count=1", "-p", "1", "-run", "TestFixture", "./internal/example"], {}),
                ([], ["test", "-count=1", "-p", "1", "./..."], {"FAKE_GO_EXIT": "19"}),
            )):
                source = f"package fixture // snapshot {index}\n"
                (sdk / "ext/fixture.go").write_text(source)
                inbox_source = json.dumps({"items": [{"id": f"fixture-{index}"}]}) + "\n"
                (checkout / "scripts/mock/inbox_events.json").write_text(inbox_source)
                if index and remote_sdk.is_dir():
                    (remote_sdk / "stale.go").write_text("package stale\n")
                if index and remote_schema.is_dir():
                    (remote_schema / "stale.json").write_text("{}\n")
                with self.subTest(args=args, extra=extra):
                    result = subprocess.run(["bash", str(checkout / "scripts/test/test-server.sh"), *args],
                                            env={**environment, **extra}, capture_output=True, text=True, timeout=30)
                    self.assertEqual(result.returncode, int(extra.get("FAKE_GO_EXIT", "0")), result.stderr)
                    recorded = json.loads(capture.read_text())
                    child = recorded["env"]
                    self.assertEqual(recorded["sdk_source"], source)
                    self.assertEqual(recorded["inbox_source"], inbox_source)
                    self.assertEqual(json.loads(sdk_capture.read_text()),
                                     ["-a", "--delete", "sdk/go/", "fake-hub:.cache/persona-test-checkout/sdk/go/"])
                    self.assertFalse((remote_sdk / "stale.go").exists())
                    self.assertEqual(json.loads(schema_capture.read_text()),
                                     ["-a", "--delete", "schema/extension/",
                                      "fake-hub:.cache/persona-test-checkout/schema/extension/"])
                    self.assertFalse((remote_schema / "stale.json").exists())
                    # the pinned Bun lives in the hub user's ~/.local/bin, which a non-interactive shell may lack (#1947)
                    self.assertIn(str(remote / ".local/bin"), child["PATH"].split(os.pathsep))
                    self.assertEqual(recorded["args"], expected)
                    self.assertEqual({key for key in child if key.startswith(("PERSONA_"))},
                                     {"PERSONA_TEST_DATABASE_URL"})
                    self.assertEqual(urlsplit(child["PERSONA_TEST_DATABASE_URL"]).path, "/persona_test")
                    self.assertEqual(urlsplit(child["PERSONA_TEST_DATABASE_URL"]).netloc, "user:synthetic@localhost")
                    self.assertFalse(any(key.startswith("PG") for key in child))
                    self.assertEqual(child['JARVIS_TEST_DATABASE_URL'], child['PERSONA_TEST_DATABASE_URL'])
                    self.assertNotEqual(child["HOME"], str(remote))
                    self.assertFalse(Path(child["HOME"]).exists())
                    self.assertFalse(any(str(profile) in value for value in child.values()))
                    cache_env = json.loads(cache_capture.read_text())
                    self.assertFalse(any(key.startswith(("PERSONA_", "PG")) for key in cache_env))
                    self.assertNotIn("synthetic-live-token", result.stdout + result.stderr)
                    self.assertNotIn("synthetic-live-password", result.stdout + result.stderr)
                    self.assertNotIn("postgres://", result.stdout + result.stderr)
                    # a test run resolves the hub's state directory and never prepares it (#1424): preparing creates the
                    # directory and its old-name alias
                    calls = [json.loads(line) for line in identity_calls.read_text().splitlines()]
                    self.assertTrue(calls)
                    self.assertFalse([call for call in calls if "--prepare" in call], calls)
            capture.unlink(missing_ok=True)
            cache_capture.unlink(missing_ok=True)
            # A missing local fixture must stop staging, even if the remote has an older copy.
            (checkout / "scripts/mock/inbox_events.json").unlink()
            result = subprocess.run(["bash", str(checkout / "scripts/test/test-server.sh")], env=environment,
                                    capture_output=True, text=True, timeout=30)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(capture.exists())
            self.assertFalse(cache_capture.exists())
            (checkout / "scripts/mock/inbox_events.json").write_text(inbox_source)
            for source in (
                "postgres://user:synthetic@localhost/custom",
                "postgres://user:synthetic@localhost/persona?dbname=persona",
                "postgres://user:synthetic@localhost/persona?database=persona",
                "postgres://user:synthetic@localhost/persona?db%6Eame=persona",
                "postgres://user:synthetic@localhost/persona?dbname=persona_test&db%6Eame=persona",
                "postgres://user:synthetic@localhost/persona?service=live",
                "postgres://user:synthetic@localhost/persona?servicefile=/synthetic/config",
                "postgres://user:synthetic@localhost/persona?sslmode=%xx",
            ):
                with self.subTest(source=source):
                    (profile / "hub.json").write_text(json.dumps({"database_url": source}))
                    result = subprocess.run(["bash", str(checkout / "scripts/test/test-server.sh")], env=environment,
                                            capture_output=True, text=True, timeout=30)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(capture.exists())
                    self.assertFalse(cache_capture.exists())
                    self.assertNotIn("postgres://", result.stdout + result.stderr)
                    self.assertNotIn("synthetic-live-password", result.stdout + result.stderr)
            (profile / "hub.json").unlink()
            result = subprocess.run(["bash", str(checkout / "scripts/test/test-server.sh")], env=environment,
                                    capture_output=True, text=True, timeout=30)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(capture.exists())


if __name__ == "__main__":
    unittest.main()
