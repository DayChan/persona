"""Keep minimum-Xcode platform preparation ahead of destination validation."""

from pathlib import Path
import os
import plistlib
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/ci/apple.sh"
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_runtime
import apple_entitlements
import apple_diagnostics


class AppleCIPreparationTests(unittest.TestCase):
    def test_hosted_runner_initializes_the_same_minimum_system_toolchain(self):
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        selection = 'ci_run \'Select system Xcode 26.0\' 60 sudo xcode-select --switch "$DEVELOPER_DIR"'
        initialization = 'ci_run \'Initialize Xcode 26.0 platform services\' 900 sudo env DEVELOPER_DIR="$DEVELOPER_DIR" xcodebuild -runFirstLaunch'
        self.assertIn('DEVELOPER_DIR: /Applications/Xcode_26.0.1.app/Contents/Developer', workflow)
        self.assertLess(workflow.index(selection), workflow.index(initialization))
        self.assertLess(workflow.index(initialization), workflow.index('run: scripts/ci/apple.sh'))
        self.assertIn('source scripts/ci/common.sh', workflow)
        # System-wide initialization is limited to the disposable hosted runner.
        self.assertNotIn('sudo', SCRIPT.read_text())

    def test_hosted_initialization_order_and_failures_with_fake_system_tools(self):
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        lines = workflow.split('      - name: Initialize pinned Xcode\n        run: |\n', 1)[1].splitlines()
        body = []
        for line in lines:
            if not line.startswith('          '):
                break
            body.append(line[10:])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tools = {
                'sudo': '#!/bin/bash\nexec "$@"\n',
                'xcode-select': '#!/bin/bash\nprintf "select %s\\n" "$*" >> "$CALL_LOG"\n',
                'xcodebuild': '#!/bin/bash\nprintf "initialize %s %s\\n" "$DEVELOPER_DIR" "$*" >> "$CALL_LOG"\nexit "$INITIALIZE_STATUS"\n',
            }
            for name, source in tools.items():
                path = root / name
                path.write_text(source)
                path.chmod(0o755)
            log = root / 'commands.log'
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       DEVELOPER_DIR='/Applications/Xcode_26.0.1.app/Contents/Developer',
                       CALL_LOG=str(log))
            for status in ('0', '1'):
                with self.subTest(status=status):
                    log.write_text('')
                    env['INITIALIZE_STATUS'] = status
                    result = subprocess.run(['bash', '-c', '\n'.join(body)], cwd=ROOT, env=env,
                                            text=True, capture_output=True, timeout=15)
                    self.assertEqual(result.returncode, int(status), result.stdout + result.stderr)
                    expected = [
                        'select --switch ' + env['DEVELOPER_DIR'],
                        'initialize ' + env['DEVELOPER_DIR'] + ' -runFirstLaunch',
                    ]
                    if status == '0':
                        expected.append('select --print-path')
                    self.assertEqual(log.read_text().splitlines(), expected)
                    self.assertIn('PASS' if status == '0' else 'FAIL', result.stdout)

    def test_matching_runtime_is_prepared_before_device_build(self):
        script = SCRIPT.read_text()
        self.assertLess(
            script.index('Download matching $platform 26.0 simulator runtime'),
            script.index("Unsigned iOS and embedded watch/extensions build"),
        )

    def test_existing_integration_exclusions_use_exact_swift_testing_selectors(self):
        script = SCRIPT.read_text()
        block = script.split('IOS_EXCLUSIONS=(', 1)[1].split('\n  )', 1)[0]
        actual = {line.strip().strip('"') for line in block.splitlines() if line.strip()}
        self.assertEqual(actual, {
            '-skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests',
            '-skip-testing:PersonaTests/NotificationServiceTests/opensWhatTheRelayDelivered()',
            '-skip-testing:PersonaTests/VideoFileTests/nativeVideoPlaysSeeksAndCleansUp()',
            '-skip-testing:PersonaTests/VideoFileResourceTests/realHTTPReadsMiddleTailAndRejectsChangedRevision()',
        })
        self.assertIn('-skip-testing:PersonaTests/AssistantHTMLSecurityTests/aRealInfiniteLoopCanBeDiscardedWithoutBlockingANewCard()', script)

    def test_cold_ios_build_does_not_consume_readiness_or_execution_deadlines(self):
        script = SCRIPT.read_text()
        build = script.index("ci_run 'Build iOS offline unit tests' 1800")
        create = script.index("ci_run 'Create stock iOS 26.0 test simulator' 100")
        readiness = script.index("ci_run 'Verify hosted iOS test readiness' 900")
        execution = script.index("ci_run 'iOS offline unit and design/token tests' 1800")
        self.assertLess(build, create)
        self.assertLess(create, readiness)
        self.assertLess(readiness, execution)
        self.assertIn("-destination 'generic/platform=iOS Simulator'", script[build:create])
        self.assertIn('"${IOS_UNIT_SELECTION[@]}" build-for-testing', script[build:create])
        self.assertIn('"${IOS_UNIT_SELECTION[@]}"', script[execution:])
        self.assertIn('IOS_TESTS=("${IOS_COMMON[@]}"', script)

    def test_mac_lane_excludes_the_same_old_hub_lab_suite_as_ios(self):
        # The suite is compiled into PersonaMacTests too; selected without its lab hub it is reported as a skip,
        # which result inspection rightly refuses.
        script = SCRIPT.read_text()
        mac = script.split("ci_run 'macOS offline unit tests'", 1)[1].split('result macos-unit', 1)[0]
        self.assertIn('-skip-testing:PersonaMacTests/OldHubCompatibilityIntegrationTests', mac)
        self.assertIn('-skip-testing:PersonaMacTests/ExtensionStreamSnapshotTests', mac)
        self.assertIn('-skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests', script)

    def test_reminder_round_trip_is_enabled_with_owned_simulator_permission(self):
        script = SCRIPT.read_text()
        self.assertIn('xcrun simctl privacy "$2" grant reminders "$3"', script)
        self.assertIn('"$1" --start-only --profile stock "$2"', script)
        # One attempt, with room for a slow first grant on a fresh stock simulator (#1402), never a retry loop.
        self.assertIn("ci_run 'Grant Reminders access to the owned test app' 300 bash -c", script)
        self.assertEqual(script.count('grant reminders'), 1)
        self.assertIn('export TEST_RUNNER_PERSONA_EVENTKIT_ROUNDTRIP=1', script)
        self.assertLess(script.index('Grant Reminders access'), script.index('iOS offline unit and design/token tests'))
        self.assertNotIn('skip-testing:PersonaTests/PlaceReminderTests', script)

    def test_mac_profile_preparation_keeps_unsigned_build_and_signed_tests(self):
        script = SCRIPT.read_text()
        prepare = script.index('Prepare profile-free Mac offline test entitlements')
        self.assertLess(script.index('Unsigned macOS app and notification extension build'), prepare)
        self.assertLess(prepare, script.index('macOS offline unit tests'))
        self.assertIn('"$SOURCE/apple/Mac/Resources/PersonaMac.entitlements"', script)
        self.assertIn('"$SOURCE/apple/PersonaNotify/PersonaMacNotify.entitlements"', script)
        self.assertIn('TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=-', script)
        self.assertIn('"${TEST_SIGNING[@]}"', script.split("ci_run 'macOS offline unit tests'", 1)[1])
        self.assertNotIn('ENABLE_HARDENED_RUNTIME=NO', script)

    def test_runtime_remains_pinned_to_minimum_compiler(self):
        script = SCRIPT.read_text()
        self.assertIn('for platform in iOS watchOS; do', script)
        self.assertIn('xcodebuild -downloadPlatform "$platform" -architectureVariant universal', script)
        self.assertIn('--platform "$platform"', script)
        self.assertNotIn("-buildVersion 26.0", script)
        self.assertIn('--profile stock --runtime "$4"', script)
        self.assertIn('"$OUTPUT/simulator.log" "$IOS_RUNTIME"', script)
        self.assertIn("FAIL select Xcode 26.0.x explicitly", script)


class AppleOfflineEntitlementsTests(unittest.TestCase):
    def test_only_profile_authorized_permissions_are_removed(self):
        original = {
            'com.apple.security.app-sandbox': True,
            'com.apple.security.network.client': True,
            'com.apple.security.get-task-allow': True,
            'com.apple.developer.unrelated': ['preserve this'],
            'com.apple.developer.aps-environment': 'development',
            'com.apple.developer.usernotifications.time-sensitive': True,
            'keychain-access-groups': ['org.example.persona.ci.push'],
        }
        prepared = apple_entitlements.offline_entitlements(original)
        self.assertEqual(original.keys() - prepared.keys(), apple_entitlements.PROFILE_ENTITLEMENTS)
        for key, value in prepared.items():
            self.assertEqual(value, original[key])
        self.assertIn('keychain-access-groups', original)

    def test_missing_or_disabled_sandbox_is_rejected(self):
        for sandbox in (None, False, 'true'):
            with self.subTest(sandbox=sandbox), self.assertRaises(ValueError):
                apple_entitlements.offline_entitlements({'com.apple.security.app-sandbox': sandbox})

    def test_all_snapshot_inputs_are_validated_before_any_write(self):
        with tempfile.TemporaryDirectory() as directory:
            first = Path(directory) / 'host.entitlements'
            second = Path(directory) / 'extension.entitlements'
            original = plistlib.dumps({'com.apple.security.app-sandbox': True,
                                       'keychain-access-groups': ['org.example.persona.ci']})
            first.write_bytes(original)
            second.write_bytes(plistlib.dumps({}))
            with self.assertRaises(ValueError):
                apple_entitlements.prepare([first, second])
            self.assertEqual(first.read_bytes(), original)
            second.write_bytes(original)
            apple_entitlements.prepare([first, second])
            for path in (first, second):
                self.assertEqual(plistlib.loads(path.read_bytes()), {'com.apple.security.app-sandbox': True})


class AppleFailureDiagnosticsTests(unittest.TestCase):
    def test_only_diagnostics_leave_output_and_result_symlinks_are_not_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output, destination = root / 'output', root / 'artifacts'
            output.mkdir()
            for name in ('simulator.log', 'simulator-boot.log', 'simulator-readiness.log', 'runtimes-iOS-before.json'):
                (output / name).write_text('diagnostic fixture')
            (output / 'config.json').write_text('configuration must stay in output')
            (output / 'private.key').write_text('key fixture must stay in output')
            result = output / 'ios-unit.xcresult'
            result.mkdir()
            (result / 'summary.json').write_text('test results')
            (result / 'external-key').symlink_to(output / 'private.key')
            calls = []
            def runner(command, **kwargs):
                calls.append((command, kwargs))
                if command[0] == 'df':
                    return subprocess.CompletedProcess(command, 0, 'disk space diagnostic', '')
                return subprocess.CompletedProcess(command, 0, '{"devices":{}}', '')
            apple_diagnostics.collect(output, destination, runner)
            self.assertEqual(calls[0][0], ['xcrun', 'simctl', 'list', '--json'])
            self.assertEqual(calls[0][1]['timeout'], 120)
            self.assertEqual(calls[1][0], ['df', '-h', str(output)])
            self.assertEqual(calls[1][1]['timeout'], 30)
            self.assertEqual((destination / 'disk-space.txt').read_text(), 'disk space diagnostic')
            self.assertEqual({path.name for path in destination.iterdir()}, {
                'simulator.log', 'simulator-boot.log', 'simulator-readiness.log', 'runtimes-iOS-before.json',
                'ios-unit.xcresult', 'simctl-list.json', 'simctl-list.stderr.txt', 'simctl-list.status.txt',
                'disk-space.txt', 'disk-space.stderr.txt', 'disk-space.status.txt',
                'simulator-launch.status.txt',
            })
            self.assertEqual((destination / 'ios-unit.xcresult/summary.json').read_text(), 'test results')
            self.assertFalse((destination / 'ios-unit.xcresult/external-key').exists())

    def test_unavailable_simctl_still_retains_existing_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'output'
            output.mkdir()
            (output / 'simulator-boot.log').write_text('boot failed')
            destination = Path(directory) / 'artifacts'
            def unavailable(*args, **kwargs):
                raise OSError('simctl unavailable')
            apple_diagnostics.collect(output, destination, unavailable)
            self.assertEqual((destination / 'simulator-boot.log').read_text(), 'boot failed')
            self.assertEqual((destination / 'simctl-list.status.txt').read_text(), 'unavailable\n')

    def test_upload_is_failure_only_and_boot_limits_and_cleanup_remain_strict(self):
        script = SCRIPT.read_text()
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        helper = (ROOT / 'scripts/app/sim-boot.sh').read_text()
        self.assertIn("ci_run 'Create stock iOS 26.0 test simulator' 100", script)
        self.assertIn("ci_run 'Verify hosted iOS test readiness' 900", script)
        self.assertIn('--create-only --profile stock', script)
        self.assertIn('2> >(tee "$5" >&2)', script)
        self.assertIn('if [ "$code" -ne 0 ]; then', script)
        self.assertLess(script.index("ci_run 'Collect Apple failure diagnostics'"), script.index("ci_run 'Shut down owned simulator'"))
        self.assertIn('exit "$code"', script)
        self.assertIn('if: failure()\n        uses: actions/upload-artifact@v4', workflow)
        self.assertIn('CI_APPLE_DIAGNOSTICS: ${{ runner.temp }}/persona-apple-diagnostics-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}', workflow)
        self.assertIn('path: ${{ runner.temp }}/persona-apple-diagnostics-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}', workflow)
        self.assertNotIn('for attempt in', helper)
        self.assertNotIn('restarting once', helper)


class AppleRuntimeTests(unittest.TestCase):
    def runtime(self, version, available=True, name=None, platform="iOS"):
        return {"version": version, "isAvailable": available,
                "name": name or platform + " " + version,
                "identifier": "com.apple.CoreSimulator.SimRuntime." + platform + "-" + version.replace(".", "-")}

    def test_exact_and_patch_versions_are_compatible(self):
        for version in ("26.0", "26.0.1"):
            runtime = self.runtime(version)
            self.assertEqual(apple_runtime.select([runtime]), runtime["identifier"])

    def test_newer_minors_unavailable_beta_and_other_platforms_are_rejected(self):
        for runtime in (self.runtime("26.1"), self.runtime("26.5"),
                        self.runtime("26.0", available=False),
                        self.runtime("26.0", name="iOS 26.0 beta"),
                        self.runtime("26.0", platform="watchOS")):
            with self.subTest(runtime=runtime):
                self.assertIsNone(apple_runtime.select([runtime]))
        self.assertIsNone(apple_runtime.select([]))

    def test_watch_runtime_is_selected_separately_from_ios(self):
        ios = self.runtime("26.0.1")
        watch = self.runtime("26.0", platform="watchOS")
        self.assertEqual(apple_runtime.select([ios, watch], "watchOS"), watch["identifier"])
        self.assertIsNone(apple_runtime.select([ios], "watchOS"))
        self.assertIsNone(apple_runtime.select([self.runtime("26.2", platform="watchOS")], "watchOS"))

    def test_highest_compatible_patch_wins_without_selecting_newer_minor(self):
        expected = self.runtime("26.0.10")
        self.assertEqual(apple_runtime.select([self.runtime("26.0"), self.runtime("26.0.2"),
                                               self.runtime("26.5"), expected]), expected["identifier"])


if __name__ == "__main__":
    unittest.main()
