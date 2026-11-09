import json
import os
import re
from pathlib import Path
import subprocess
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import paths
import required


class PathTests(unittest.TestCase):
    def test_docs_do_not_start_macos(self):
        selected = paths.select(["docs/operations/testing.md"])
        self.assertTrue(selected["scripts"])
        self.assertFalse(selected["apple"])
        self.assertFalse(selected["android"])
        self.assertFalse(selected["web"])
        self.assertFalse(selected["server"])
        self.assertTrue(paths.select(["docs/assistant/prompts.md"])["server"])
        self.assertTrue(paths.select(["docs/protocol/protocol.md"])["server"])

    def test_platform_files_and_shared_inputs(self):
        for platform, filename in [("server", "server/internal/hub/hub.go"), ("web", "web/src/App.tsx"), ("android", "android/app/build.gradle.kts"), ("apple", "apple/Shared/Hub.swift"), ("runtimes", "sdk/ts/src/index.ts")]:
            self.assertTrue(paths.select([filename])[platform])
        self.assertTrue(paths.select(["scripts/mock/inbox_events.json"])["web"])
        self.assertTrue(paths.select(["schema/extension/example.json"])["apple"])

    def test_source_authoring_sdk_and_gate_paths_select_runtime_ci(self):
        for filename in ["sdk/ts/scripts/check-source-bundle.mjs", "sdk/ts/scripts/source-authoring-worker.mjs",
                         "sdk/ts/scripts/source-authoring-tests.mjs", "sdk/ts/README.md", "scripts/ci/runtimes.sh"]:
            self.assertTrue(paths.select([filename])["runtimes"], filename)

    def test_infrastructure_and_dispatch_run_all(self):
        self.assertTrue(all(paths.select([".github/workflows/ci.yml"]).values()))
        self.assertTrue(all(paths.select([], True).values()))

    def test_dispatch_base_does_not_need_pr_payload(self):
        self.assertEqual(paths.resolve_base(), "origin/main")
        self.assertEqual(paths.resolve_base(event={"inputs": {"lanes": "auto"}}), "origin/main")
        self.assertEqual(paths.resolve_base(event={"pull_request": {"base": {"sha": "pr-base"}}}), "pr-base")
        self.assertEqual(paths.resolve_base(event={"merge_group": {"base_sha": "queue-base"}}), "queue-base")
        self.assertEqual(paths.resolve_base("override", {"pull_request": {"base": {"sha": "ignored"}}}), "override")

    def test_explicit_lanes_and_invalid_values(self):
        self.assertTrue(all(paths.select_lanes([], "all").values()))
        selected = paths.select_lanes(["apple/Shared/Hub.swift"], "server, web")
        self.assertEqual({name for name, enabled in selected.items() if enabled}, {"server", "web"})
        for value in ("", "typo", "auto,web", "image", "web,"):
            with self.assertRaises(ValueError, msg=value):
                paths.select_lanes([], value)

    def test_local_cli_without_github_environment(self):
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GITHUB_")}
        command = [sys.executable, str(Path(__file__).with_name("paths.py"))]
        result = subprocess.run(command + ["--base", "HEAD"], env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), paths.select([]))
        result = subprocess.run(command + ["--print-base", "--base", "HEAD"], env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(result.stdout.strip()), 40)
        result = subprocess.run(command + ["--lanes", "server,web"], env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), paths.select_lanes([], "server,web"))

    def test_heavy_lanes_are_explicitly_manual_without_hiding_failures(self):
        selected = paths.select_lanes([], "all")
        for event in ("pull_request", "merge_group"):
            automatic = paths.apply_event_policy(selected, event)
            self.assertEqual({name for name, needed in automatic.items() if needed}, {"web", "runtimes"})
        self.assertTrue(all(paths.apply_event_policy(selected, "workflow_dispatch").values()))
        self.assertTrue(all(selected.values()))

    def test_unrelated_client_screens_do_not_start_server(self):
        for filename in ("android/feature/settings/Screen.kt", "android/app/build.gradle.kts", "apple/Mac/Screen.swift"):
            self.assertFalse(paths.select([filename])["server"], filename)
        for filename in ("android/core/contracts/Message.kt", "apple/Shared/Models.swift", "apple/Persona/Views/Interactions/InteractionViews.swift"):
            self.assertTrue(paths.select([filename])["server"], filename)

    def test_shard_implementation_changes_select_their_lane(self):
        self.assertTrue(paths.select(["scripts/ci/server-shards.py"])["server"])
        self.assertTrue(paths.select(["scripts/ci/android-shards.py"])["android"])

    def test_deleted_and_renamed_old_paths_are_relevant(self):
        self.assertTrue(paths.select(["apple/Old.swift", "docs/archive/new.md"])["apple"])


class AutomaticWorkflowTests(unittest.TestCase):
    def test_pr_checks_allocate_only_the_final_job(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / '.github/workflows/ci.yml').read_text()
        changes = workflow.split('\n  changes:\n', 1)[1].split('\n  server:\n', 1)[0]
        self.assertIn("if: github.event_name == 'workflow_dispatch'", changes)
        for name in ('server', 'web', 'android', 'apple', 'apple_macos', 'runtimes', 'scripts'):
            block = re.split(r'\n  [a-z_]+:\n', workflow.split('\n  ' + name + ':\n', 1)[1], maxsplit=1)[0]
            self.assertIn('needs: changes', block)
            self.assertIn('needs.changes.outputs.', block)
        gate = workflow.split('\n  required:\n', 1)[1]
        self.assertIn('if: always()', gate)
        for command in ('scripts/ci/guards.sh', 'scripts/ci/web.sh', 'scripts/ci/runtimes.sh',
                        'scripts/test/test_doc_paths.py', 'scripts/test/test_doc_go_paths.py',
                        'scripts/test/test_doc_wire.py',
                        "python3 -m unittest discover -s scripts/ci -p 'test_*.py'"):
            self.assertIn(command, gate)
        dependencies = gate.index('run: python3 -m pip install -r scripts/ci/requirements.txt')
        self.assertLess(dependencies, gate.index('python3 scripts/test/test_readme_languages.py'))
        self.assertIn("python-version: '3.11'", gate)
        manual_gate = gate.split('- name: Verify selected manual platform results', 1)[1]
        self.assertIn("if: github.event_name == 'workflow_dispatch'", manual_gate)
        self.assertIn('run: python3 scripts/ci/required.py', manual_gate)
        self.assertNotIn('continue-on-error:', workflow)


class RequiredTests(unittest.TestCase):
    def needs(self):
        outputs = {name: "false" for name in ("server", "web", "android", "apple", "runtimes", "scripts")}
        outputs["scripts"] = "true"
        needs = {name: {"result": "skipped"} for name in outputs}
        needs["scripts"] = {"result": "success"}
        needs["changes"] = {"result": "success", "outputs": outputs}
        return needs

    def test_only_unselected_checks_can_skip(self):
        self.assertEqual(required.failures(self.needs()), [])
        needs = self.needs()
        needs["scripts"]["result"] = "skipped"
        self.assertTrue(required.failures(needs))

    def test_failed_cancelled_or_missing_results_fail(self):
        for result in ("failure", "cancelled", None):
            needs = self.needs()
            needs["scripts"]["result"] = result
            self.assertTrue(required.failures(needs))
        needs = self.needs()
        del needs["changes"]["outputs"]["apple"]
        self.assertTrue(required.failures(needs))

    def test_mac_result_does_not_change_required_ios_policy(self):
        for mac_result in ('failure', 'cancelled', 'skipped', None, 'success'):
            needs = self.needs()
            needs['changes']['outputs']['apple'] = 'true'
            needs['apple']['result'] = 'success'
            needs['apple_macos'] = {'result': mac_result}
            self.assertEqual(required.failures(needs), [])
            for ios_result in ('failure', 'cancelled', 'skipped', None):
                needs['apple']['result'] = ios_result
                self.assertTrue(required.failures(needs))

    def test_guard_failure_never_passes(self):
        needs = self.needs()
        needs["changes"]["result"] = "failure"
        self.assertTrue(required.failures(needs))


class AppleWorkflowTests(unittest.TestCase):
    def test_mac_and_ios_are_independent_but_only_ios_blocks(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / '.github/workflows/ci.yml').read_text()
        ios = workflow.split('\n  apple:\n', 1)[1].split('\n  apple_macos:\n', 1)[0]
        mac = workflow.split('\n  apple_macos:\n', 1)[1].split('\n  runtimes:\n', 1)[0]
        for block, lane in ((ios, 'ios'), (mac, 'macos')):
            self.assertIn('    needs: changes\n', block)
            self.assertIn("    if: needs.changes.outputs.apple == 'true' && github.event_name == 'workflow_dispatch'", block)
            self.assertIn('    uses: ./.github/workflows/apple-test.yml', block)
            self.assertIn('      lane: ' + lane, block)
        gate = workflow.split('\n  required:\n', 1)[1]
        self.assertIn('needs: [changes, server, web, android, apple, runtimes, scripts]', gate)
        self.assertNotIn('apple_macos', gate)
        self.assertNotIn('continue-on-error:', workflow)

    def test_reusable_lane_uses_quoted_environment_not_script_interpolation(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / '.github/workflows/apple-test.yml').read_text()
        self.assertIn('options: [ios, macos]', workflow)
        self.assertIn('APPLE_CI_LANE: ${{ inputs.lane }}', workflow)
        self.assertIn('run: scripts/ci/apple.sh "$APPLE_CI_LANE"', workflow)
        self.assertNotIn('continue-on-error:', workflow)
        script = (root / 'scripts/ci/apple.sh').read_text()
        package_gate = script.split("ci_run 'PersonaKit unit tests'", 1)[0].rsplit('\nif ', 1)[1]
        self.assertIn('[ "$LANE" = ios ]', package_gate)
        for required_batch in ('Unsigned iOS and embedded watch/extensions build',
                               'iOS offline unit and design/token tests',
                               'iOS HTML card tests (separate batch)',
                               'iOS bounded HTML security tests (separate batch)'):
            self.assertIn(required_batch, script)


class RunnerTests(unittest.TestCase):
    def invoke(self, command, timeout=5):
        return subprocess.run([sys.executable, str(Path(__file__).with_name("run.py")), "--label", "fixture", "--timeout", str(timeout), "--", sys.executable, "-c", command], capture_output=True, text=True, timeout=10)

    def test_success_and_failure(self):
        self.assertEqual(self.invoke("pass").returncode, 0)
        result = self.invoke("raise SystemExit(7)")
        self.assertEqual(result.returncode, 7)
        self.assertIn("FAIL fixture", result.stdout)

    def test_timeout_is_not_a_pass(self):
        result = self.invoke("import time; time.sleep(3)", 0.1)
        self.assertEqual(result.returncode, 124)
        self.assertIn("TIMEOUT fixture", result.stdout)


if __name__ == "__main__":
    unittest.main()
