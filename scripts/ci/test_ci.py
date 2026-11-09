import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
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


class PullRequestBaseTests(unittest.TestCase):
    """A PR is compared with the base branch as it is now, not where the PR was opened.

    actions/checkout checks out refs/pull/<n>/merge (first parent the current base tip, second the PR head);
    the event's pull_request.base.sha stays where the PR was opened. The cases build a throwaway repository.
    """

    def setUp(self):
        scratch = Path(tempfile.mkdtemp(prefix="ci-pr-base-"))
        self.addCleanup(shutil.rmtree, scratch, True)
        self.repo, self.event_file, self.output_file = scratch / "repo", scratch / "event.json", scratch / "output"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.opened = self.commit("README.md", "start\n", "start")
        self.git("checkout", "-q", "-b", "pr")
        self.head = self.commit("docs/guide.md", "guide\n", "the PR's change")
        self.git("checkout", "-q", "main")
        self.tip = self.commit("web/src/App.tsx", "app\n", "landed on main after the PR was opened")
        self.git("checkout", "-q", "--detach", self.tip)
        self.git("merge", "-q", "--no-ff", "-m", "Merge the PR head into the base tip", self.head)
        self.event = {"pull_request": {"base": {"sha": self.opened}, "head": {"sha": self.head}}}

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True, text=True,
                              timeout=30).stdout.strip()

    def commit(self, path, text, message):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)
        return self.git("rev-parse", "HEAD")

    def run_paths(self, *args, event=None):
        self.event_file.write_text(json.dumps(self.event if event is None else event))
        self.output_file.write_text("")
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GITHUB_")}
        environment.update(GITHUB_EVENT_PATH=str(self.event_file), GITHUB_OUTPUT=str(self.output_file),
                           GITHUB_EVENT_NAME="pull_request")
        result = subprocess.run([sys.executable, str(Path(__file__).with_name("paths.py")), *args], cwd=self.repo,
                                env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip(), self.output_file.read_text()

    def test_merge_checkout_compares_with_the_current_base_tip(self):
        printed, output = self.run_paths("--print-base")
        self.assertEqual(printed, self.tip)
        self.assertIn("base=" + self.tip + "\n", output)
        # The privacy range then holds the PR's commits and the checkout's merge, not what main gained meanwhile.
        scanned = self.git("rev-list", printed + "..HEAD").split()
        self.assertIn(self.head, scanned)
        self.assertNotIn(self.tip, scanned)
        self.assertEqual(self.git("diff", "--name-only", printed + "...HEAD"), "docs/guide.md")
        lanes, _ = self.run_paths("--base", printed)
        self.assertFalse(json.loads(lanes)["web"])
        # The stale base would have counted main's Web change as the PR's.
        stale, _ = self.run_paths("--base", self.opened)
        self.assertTrue(json.loads(stale)["web"])
        self.assertIn(self.tip, self.git("rev-list", self.opened + "..HEAD").split())

    def test_event_base_sha_is_only_a_fallback(self):
        parents = (self.tip, self.head)
        self.assertEqual(paths.resolve_base(event=self.event, parents=parents), self.tip)
        self.assertEqual(paths.resolve_base(event=self.event), self.opened)
        self.assertEqual(paths.resolve_base(event=self.event, parents=(self.head,)), self.opened)
        self.assertEqual(paths.resolve_base(event=self.event, parents=(self.tip, self.opened)), self.opened)
        self.assertEqual(paths.resolve_base(event={"pull_request": {"base": {"sha": self.opened}}}, parents=parents), self.opened)
        self.assertEqual(paths.resolve_base("override", self.event, parents), "override")
        self.assertEqual(paths.resolve_base(event={"merge_group": {"base_sha": "queue-base"}}, parents=parents), "queue-base")
        self.assertEqual(paths.resolve_base(parents=parents), "origin/main")

    def test_head_checkout_and_newer_push_fall_back_to_the_event_base(self):
        # The event's head is not HEAD's second parent (the checkout is of another push): keep the event's base.
        newer = {"pull_request": {"base": {"sha": self.opened}, "head": {"sha": "0123456789abcdef0123456789abcdef01234567"}}}
        self.assertEqual(self.run_paths("--print-base", event=newer)[0], self.opened)
        # A checkout of the PR head itself is not a merge commit.
        self.git("checkout", "-q", "--detach", self.head)
        self.assertEqual(self.run_paths("--print-base")[0], self.opened)
        # An explicit base wins over both.
        self.assertEqual(self.run_paths("--print-base", "--base", self.tip)[0], self.tip)


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
