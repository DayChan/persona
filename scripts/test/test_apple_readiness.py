"""Execute the CI preparation block with owned fake devices and real result validation."""
import json
import os
from pathlib import Path
import subprocess
import textwrap
import unittest

import test_sim_boot
from test_sim_boot import FAKE_XCRUN, NEW, PHONE, ROOT, WATCH

SCRIPT = ROOT / "scripts/ci/apple.sh"

FAKE_XCODE = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, signal, sys
    from pathlib import Path
    args = sys.argv[1:]
    root = Path(os.environ["FAKE"])
    with (root / "xcode-calls.jsonl").open("a") as log:
        log.write(json.dumps(args) + "\\n")
    if args[-1] == "build-for-testing":
        assert args[args.index("-destination") + 1] == "generic/platform=iOS Simulator"
        sys.exit(int(os.environ.get("BUILD_STATUS", "0")))
    assert args[args.index("-destination") + 1] == "platform=iOS Simulator,id=" + os.environ["OWNED_UDID"]
    probe = "-only-testing:PersonaTests/TypeScaleTests" in args
    if probe:
        (root / "probe.pid").write_text(str(os.getpid()))
        if os.environ.get("PROBE_HANG") == "1":
            def stop(*_):
                (root / "probe.stopped").write_text("terminated")
                sys.exit(143)
            signal.signal(signal.SIGTERM, stop)
            signal.pause()
        if os.environ.get("PROBE_STATUS", "0") != "0":
            sys.exit(int(os.environ["PROBE_STATUS"]))
        bundle = Path(args[args.index("-resultBundlePath") + 1])
        bundle.mkdir()
        status = os.environ.get("RESULT_STATUS", "Passed")
        suite = os.environ.get("RESULT_SUITE", "TypeScaleTests")
        tests = [] if status == "empty" else [{"nodeType": "Test Case",
            "nodeIdentifier": "PersonaTests/" + suite + "/usesTextTokens()", "result": status}]
        (bundle / "tests.json").write_text(json.dumps({"testNodes": tests}))
        # Xcode manages the test destination; success does not keep it booted for simctl privacy.
        state = json.loads((root / "state.json").read_text())
        device = next(d for devices in state["devices"].values() for d in devices
                      if d["udid"] == os.environ["OWNED_UDID"])
        device["state"] = os.environ.get("POST_PROBE_STATE", "Shutdown")
        device["isAvailable"] = os.environ.get("POST_PROBE_AVAILABLE", "1") == "1"
        (root / "state.json").write_text(json.dumps(state))
    else:
        (root / "full-suite-started").write_text("yes")
        sys.exit(int(os.environ.get("FULL_STATUS", "0")))
    ''')

FAKE_XCRUN_WRAPPER = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, signal, sys
    from pathlib import Path
    args = sys.argv[1:]
    root = Path(os.environ["FAKE"])
    if args[:1] == ["xcresulttool"]:
        (root / "result.pid").write_text(str(os.getpid()))
        if os.environ.get("RESULT_HANG") == "1":
            def stop(*_):
                (root / "result.stopped").write_text("terminated")
                sys.exit(143)
            signal.signal(signal.SIGTERM, stop)
            signal.pause()
        bundle = Path(args[args.index("--path") + 1])
        print((bundle / "tests.json").read_text())
    elif args[:2] == ["simctl", "privacy"]:
        assert args[2:] == [os.environ["OWNED_UDID"], "grant", "reminders", "org.example.persona.ci"], args
        (root / "privacy-attempted").write_text("yes")
        state = json.loads((root / "state.json").read_text())
        device = next(d for devices in state["devices"].values() for d in devices
                      if d["udid"] == args[2])
        if device["state"] != "Booted":
            print("SimError 405: Unable to lookup in current state: " + device["state"], file=sys.stderr)
            sys.exit(149)
        if os.environ.get("PRIVACY_STATUS", "0") != "0":
            sys.exit(int(os.environ["PRIVACY_STATUS"]))
        (root / "privacy-granted").write_text("yes")
    else:
        if args[:2] == ["simctl", "boot"] and os.environ.get("BOOT_HANG") == "1":
            assert args[2:] == [os.environ["OWNED_UDID"]], args
            def stop(*_):
                (root / "boot.stopped").write_text("terminated")
                sys.exit(143)
            signal.signal(signal.SIGTERM, stop)
            signal.pause()
        os.execv(str(root / "bin/fake-simctl"), ["xcrun", *args])
    ''')


class AppleReadinessTests(unittest.TestCase):
    def setUp(self):
        self.fixture = test_sim_boot.SimBootTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.tearDown)
        self.root = self.fixture.dir
        self.fixture.state["bootstatus_hang"] = True
        self.fixture.save()
        for name, body in (("xcodebuild", FAKE_XCODE), ("xcrun", FAKE_XCRUN_WRAPPER),
                           ("fake-simctl", FAKE_XCRUN)):
            path = self.fixture.bin / name
            path.write_text(body)
            path.chmod(0o755)
        self.output = self.root / "output"
        self.output.mkdir()
        (self.output / "config.json").write_text(json.dumps({"app": {"bundle_id": "org.example.persona.ci"}}))

    def run_preparation(self, **options):
        source = SCRIPT.read_text()
        start = source.index('  IOS_COMMON=(')
        block = source[start:source.index('  result ios-unit', start)]
        shell = '''set -euo pipefail
ci_run() {
  local label="$1" budget="$2"; shift 2
  printf '%s|%s\\n' "$label" "$budget" >> "$FAKE/budgets"
  python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$TEST_STEP_TIMEOUT" -- "$@"
}
XCODE=(xcodebuild)
TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER=)
''' + block
        env = dict(os.environ, PATH=str(self.fixture.bin) + os.pathsep + os.environ["PATH"],
                   CI_ROOT=str(ROOT), SOURCE=str(ROOT), OUTPUT=str(self.output),
                   PERSONA_CONFIG=str(self.output / "config.json"), SIM_NAME="owned readiness fixture",
                   IOS_RUNTIME="com.apple.CoreSimulator.SimRuntime.iOS-27-0", OWNED_UDID=NEW,
                   FAKE=str(self.root), TEST_STEP_TIMEOUT="10")
        env.update(options)
        result = subprocess.run(["bash", "-c", shell], cwd=ROOT, env=env,
                                capture_output=True, text=True, timeout=25)
        calls = self.root / "xcode-calls.jsonl"
        return result, [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []

    def test_xcode_probe_replaces_the_hung_bootstatus_and_preserves_full_selection(self):
        result, calls = self.run_preparation()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(calls), 3)
        self.assertEqual([call[-1] for call in calls], ["build-for-testing", "test-without-building", "test-without-building"])
        selectors = lambda args: [a for a in args if a.startswith(("-only-testing:", "-skip-testing:"))]
        self.assertEqual(selectors(calls[0]), selectors(calls[2]))
        self.assertEqual(selectors(calls[1]), ["-only-testing:PersonaTests/TypeScaleTests"])
        self.assertIn("-only-testing:PersonaTests", calls[2])
        self.assertIn("ARCHS=" + os.uname().machine, calls[0])
        self.assertIn("ONLY_ACTIVE_ARCH=YES", calls[0])
        for call in calls[1:]:
            self.assertFalse(any(arg.startswith(("ARCHS=", "ONLY_ACTIVE_ARCH=")) for arg in call))
        for call in calls:
            self.assertIn("CODE_SIGNING_ALLOWED=YES", call)
            self.assertEqual(call[call.index("-derivedDataPath") + 1], str(self.output / "ios-tests"))
        context = json.loads((self.output / "simulator-readiness-context.json").read_text())
        self.assertEqual(context["udid"], NEW)
        self.assertEqual(context["bundle_id"], "org.example.persona.ci")
        self.assertEqual(set(context), {"udid", "bundle_id", "started_at"})
        sim_calls = (self.root / "calls").read_text()
        self.assertNotIn("simctl bootstatus", sim_calls)
        self.assertEqual([line for line in sim_calls.splitlines() if "simctl boot " in line],
                         ["xcrun simctl boot " + NEW])
        self.assertEqual(sim_calls.count("simctl create"), 1)
        self.assertTrue((self.root / "privacy-granted").exists())
        self.assertTrue((self.root / "full-suite-started").exists())
        self.assertEqual(self.fixture.device(PHONE)["state"], "Shutdown")
        self.assertEqual(self.fixture.device(WATCH)["state"], "Shutdown")
        self.assertIn("1 test cases, zero failed/skipped", (self.output / "simulator-readiness.log").read_text())
        self.assertEqual((self.root / "budgets").read_text().splitlines(), [
            "Build iOS offline unit tests|1800", "Create stock iOS 26.0 test simulator|100",
            "Verify hosted iOS test readiness|900", "Grant Reminders access to the owned test app|300",
            "iOS offline unit and design/token tests|1800",
        ])

    def test_build_failure_stops_before_creating_any_device(self):
        result, calls = self.run_preparation(BUILD_STATUS="7")
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertFalse((self.root / "calls").exists())

    def test_probe_command_failure_stops_before_permissions_and_full_suite(self):
        result, calls = self.run_preparation(PROBE_STATUS="65")
        self.assertEqual(result.returncode, 65, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertFalse((self.root / "privacy-granted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())

    def test_probe_results_must_be_nonempty_passed_and_from_the_selected_suite(self):
        for options in ({"RESULT_STATUS": "Failed"}, {"RESULT_STATUS": "Skipped"},
                        {"RESULT_STATUS": "empty"}, {"RESULT_SUITE": "UnrelatedTests"}):
            with self.subTest(options=options):
                # Each attempt owns a separate device fixture; no retry of a failed probe.
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(**options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 2)
                    self.assertFalse((case.root / "full-suite-started").exists())
                    self.assertFalse((case.root / "privacy-granted").exists())
                finally:
                    case.doCleanups()

    def test_probe_and_result_inspection_share_one_deadline(self):
        for key, pid_name in (("PROBE_HANG", "probe.pid"), ("RESULT_HANG", "result.pid")):
            with self.subTest(phase=key):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, _ = case.run_preparation(**{key: "1", "TEST_STEP_TIMEOUT": "1"})
                    self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
                    self.assertFalse((case.root / "full-suite-started").exists())
                    self.assertTrue((case.root / pid_name).exists())
                    # The deadline terminates the probe/result process, not just its shell.
                    self.assertEqual((case.root / pid_name.replace(".pid", ".stopped")).read_text(), "terminated")
                finally:
                    case.doCleanups()

    def test_probe_leaving_device_booted_does_not_boot_it_again(self):
        result, calls = self.run_preparation(POST_PROBE_STATE="Booted")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(calls), 3)
        self.assertNotIn("simctl boot", (self.root / "calls").read_text())
        self.assertTrue((self.root / "privacy-granted").exists())

    def test_permission_handoff_rejects_unavailable_or_transitional_device(self):
        for options in ({"POST_PROBE_STATE": "Booting"}, {"POST_PROBE_STATE": "Shutting Down"},
                        {"POST_PROBE_AVAILABLE": "0"}):
            with self.subTest(options=options):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(**options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 2)
                    self.assertNotIn("simctl boot", (case.root / "calls").read_text())
                    self.assertFalse((case.root / "privacy-attempted").exists())
                    self.assertFalse((case.root / "full-suite-started").exists())
                finally:
                    case.doCleanups()

    def test_failed_start_prevents_permission_and_full_suite(self):
        self.fixture.state["boot_rc"] = 7
        self.fixture.save()
        result, calls = self.run_preparation()
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertFalse((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "calls").read_text().count("simctl boot "), 1)

    def test_permission_failure_is_not_retried_or_allowed_to_start_full_suite(self):
        result, calls = self.run_preparation(PRIVACY_STATUS="149")
        self.assertEqual(result.returncode, 149, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "privacy-granted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "calls").read_text().count("simctl boot "), 1)

    def test_start_is_inside_the_existing_permission_deadline(self):
        result, calls = self.run_preparation(BOOT_HANG="1", TEST_STEP_TIMEOUT="1")
        self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertEqual((self.root / "boot.stopped").read_text(), "terminated")
        self.assertFalse((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "budgets").read_text().splitlines()[-1],
                         "Grant Reminders access to the owned test app|300")

    def test_full_suite_failure_is_not_masked_by_successful_readiness(self):
        result, calls = self.run_preparation(FULL_STATUS="7")
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(len(calls), 3)


if __name__ == "__main__":
    unittest.main()
