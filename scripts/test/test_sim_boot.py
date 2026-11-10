"""scripts/app/sim-boot.sh against a fake xcrun and simslim (no real simulator is touched).

python3 -B scripts/test/test_sim_boot.py
"""
import importlib.util
import json
import os
import signal
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "app" / "sim-boot.sh"
PHONE = "11111111-1111-1111-1111-111111111111"
WATCH = "22222222-2222-2222-2222-222222222222"
NEW = "33333333-3333-3333-3333-333333333333"

# The fakes read their world from $FAKE/state.json and append every call to $FAKE/calls.
FAKE_XCRUN = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, signal, sys
    d = os.environ["FAKE"]; st = json.load(open(d + "/state.json"))
    open(d + "/calls", "a").write("xcrun " + " ".join(sys.argv[1:]) + "\\n")
    a = sys.argv[1:]
    if a[:1] == ["--find"]: print("/usr/bin/true"); sys.exit(0)
    assert a[0] == "simctl", a
    a = a[1:]
    def save(): json.dump(st, open(d + "/state.json", "w"))
    def dev(u): return next(x for r in st["devices"].values() for x in r if x["udid"] == u)
    if a[:2] == ["list", "devices"]: print(json.dumps({"devices": st["devices"]}))
    elif a[:1] == ["list"]: print(json.dumps({"devicetypes": st["devicetypes"], "runtimes": st["runtimes"]}))
    elif a[0] == "create":
        st["devices"].setdefault(a[3], []).append({"udid": st["next"], "name": a[1], "state": "Shutdown", "isAvailable": True}); save()
        print(st["next"])
    elif a[0] == "boot":
        if st.get("boot_rc"): sys.exit(st["boot_rc"])
        dev(a[1])["state"] = "Booted"; save()
    elif a[0] == "spawn":
        open(d + "/launchctl.pid", "w").write(str(os.getpid()))
        assert a == ["spawn", st["diagnostic_udid"], "launchctl", "list"], a
        if st.get("diagnostic_hang"):
            signal.pause()
        print("owned launchd service status")
        sys.exit(st.get("diagnostic_rc", 0))
    elif a[0] == "bootstatus":
        open(d + "/bootstatus.pid", "w").write(str(os.getpid()))
        if st.get("complete_during_diagnostics"):
            signal.signal(signal.SIGUSR1, lambda *_: sys.exit(st.get("bootstatus_rc", 0)))
        if st.get("bootstatus_hang"):
            signal.pause()
        print(st.get("boot_progress", "Boot phase: waiting for SpringBoard"))
        print(st.get("boot_error", "bootstatus stderr diagnostic"), file=sys.stderr)
        sys.exit(st.get("bootstatus_rc", 0 if dev(a[1])["state"] == "Booted" else 1))
    else: sys.exit("unexpected simctl " + " ".join(a))
    ''')
FAKE_SIMSLIM = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, sys
    d = os.environ["FAKE"]; st = json.load(open(d + "/state.json"))
    open(d + "/calls", "a").write("simslim " + " ".join(sys.argv[1:]) + "\\n")
    a = sys.argv[1:]
    def save(): json.dump(st, open(d + "/state.json", "w"))
    def dev(u): return next(x for r in st["devices"].values() for x in r if x["udid"] == u)
    slim = st.setdefault("slim", {})
    if a[0] == "list": print(json.dumps([{"udid": x["udid"], "managedDisabled": 150 if x["udid"] in slim else 0} for r in st["devices"].values() for x in r]))
    elif a[0] == "verify": sys.exit(0 if slim.get(a[1]) == a[3] else 1)
    elif a[0] == "on": slim[a[1]] = a[3]; dev(a[1])["state"] = "Booted"; save()
    elif a[0] == "off": slim.pop(a[1], None); save()
    elif a[0] == "doctor": print("ok"); sys.exit(st.get("doctor_rc", 0))
    else: sys.exit("unexpected simslim " + " ".join(a))
    ''')


FAKE_SAMPLE = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, signal, sys
    d = os.environ["FAKE"]; st = json.load(open(d + "/state.json"))
    open(d + "/calls", "a").write("sample " + " ".join(sys.argv[1:]) + "\\n")
    open(d + "/sample.pid", "w").write(str(os.getpid()))
    pid = int(open(d + "/bootstatus.pid").read())
    assert sys.argv[1:] == [str(pid), "2", "-file", "/dev/stderr"], sys.argv
    os.kill(pid, 0)  # The sample must target the still-live bootstatus child.
    print("live bootstatus stack", flush=True)
    if st.get("diagnostic_orphan"):
        child = os.fork()
        if child == 0:
            signal.pause()
            sys.exit(0)
        open(d + "/sample-child.pid", "w").write(str(child))
    if st.get("diagnostic_hang"):
        signal.pause()
    if st.get("complete_during_diagnostics"):
        os.kill(pid, signal.SIGUSR1)
    sys.exit(st.get("diagnostic_rc", 0))
    ''')


class SimBootTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sim-boot ")
        self.dir = Path(self.tmp.name)
        self.bin = self.dir / "bin"
        self.bin.mkdir()
        for name, body in (("xcrun", FAKE_XCRUN), ("simslim", FAKE_SIMSLIM), ("sample", FAKE_SAMPLE)):
            (self.bin / name).write_text(body)
            (self.bin / name).chmod(0o755)
        ios, watch = "com.apple.CoreSimulator.SimRuntime.iOS-27-0", "com.apple.CoreSimulator.SimRuntime.watchOS-26-5"
        phone_type = "com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro"
        self.state = {
            "next": NEW,
            "devices": {
                ios: [{"udid": PHONE, "name": "mine", "state": "Shutdown", "isAvailable": True}],
                watch: [{"udid": WATCH, "name": "watch", "state": "Shutdown", "isAvailable": True}],
            },
            "devicetypes": [{"name": "iPhone 17 Pro", "identifier": phone_type}],
            "runtimes": [
                {"identifier": "com.apple.CoreSimulator.SimRuntime.iOS-26-5", "name": "iOS 26.5", "version": "26.5", "isAvailable": True,
                 "supportedDeviceTypes": [{"identifier": phone_type}]},
                {"identifier": ios, "name": "iOS 27.0", "version": "27.0", "isAvailable": True,
                 "supportedDeviceTypes": [{"identifier": phone_type}]},
                # newer, but without the device type (iOS 27.1 on the dev Mac)
                {"identifier": "com.apple.CoreSimulator.SimRuntime.iOS-27-1", "name": "iOS 27.1", "version": "27.1", "isAvailable": True,
                 "supportedDeviceTypes": []},
            ],
        }
        self.save()

    def tearDown(self):
        self.tmp.cleanup()

    def save(self):
        (self.dir / "state.json").write_text(json.dumps(self.state))

    def load(self):
        return json.loads((self.dir / "state.json").read_text())

    def device(self, udid):
        return next(x for r in self.load()["devices"].values() for x in r if x["udid"] == udid)

    def run_boot(self, *args, simslim=True, env=None, timeout=120):
        path = [str(self.bin)] if simslim else []
        if not simslim:
            nobin = self.dir / "nosimslim"
            nobin.mkdir(exist_ok=True)
            (nobin / "xcrun").write_text(FAKE_XCRUN)
            (nobin / "xcrun").chmod(0o755)
            path = [str(nobin)]
        e = {"PATH": ":".join(path + ["/usr/bin", "/bin"]), "HOME": str(self.dir), "FAKE": str(self.dir), "SIM_BOOT_TIMEOUT": "20"}
        e.update(env or {})
        r = subprocess.run(["bash", str(SCRIPT), *args], capture_output=True, text=True, env=e, timeout=timeout, cwd=self.dir)
        calls = (self.dir / "calls").read_text().splitlines() if (self.dir / "calls").exists() else []
        return r, calls

    def verdict(self, r):
        return [l for l in r.stderr.splitlines() if l.startswith("sim-boot: ")][-1]

    def test_shutdown_simulator_is_booted_slim_with_the_profile_and_checked(self):
        r, calls = self.run_boot("mine")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), PHONE)
        self.assertEqual(self.verdict(r), f"sim-boot: slim default {PHONE}")
        on = [c for c in calls if c.startswith("simslim on")]
        self.assertEqual(len(on), 1)
        self.assertTrue(on[0].endswith("apple/simslim/default.json"), on[0])
        self.assertIn(f"simslim doctor {PHONE} --requires push,photos,widgets", calls)

    def test_booted_simulator_that_is_not_slim_is_never_rebooted(self):
        self.state["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-27-0"][0]["state"] = "Booted"
        self.save()
        r, calls = self.run_boot(PHONE)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), PHONE)
        self.assertEqual(self.verdict(r), f"sim-boot: in-use-not-slim default {PHONE}")
        self.assertFalse([c for c in calls if c.startswith(("simslim on", "simslim off", "xcrun simctl boot"))], calls)

    def test_booted_simulator_already_slim_is_only_verified(self):
        self.state["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-27-0"][0]["state"] = "Booted"
        self.state["slim"] = {PHONE: str(ROOT / "apple/simslim/watch.json")}
        self.save()
        r, calls = self.run_boot("--profile", "watch", "mine")
        self.assertEqual(self.verdict(r), f"sim-boot: slim watch {PHONE}")
        self.assertFalse([c for c in calls if c.startswith("simslim on")], calls)
        self.assertIn(f"simslim doctor {PHONE} --requires push,photos,widgets,health,imessage", calls)

    def test_reslim_reboots_your_own_booted_simulator(self):
        self.state["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-27-0"][0]["state"] = "Booted"
        self.save()
        r, calls = self.run_boot("--reslim", "mine")
        self.assertEqual(self.verdict(r), f"sim-boot: slim default {PHONE}")
        self.assertEqual(len([c for c in calls if c.startswith("simslim on")]), 1)

    def test_start_only_rejects_names_missing_devices_and_incompatible_modes(self):
        for args in (("--profile", "stock", "mine"), ("--profile", "stock", NEW),
                     (PHONE,), ("--profile", "stock", "--reslim", PHONE),
                     ("--profile", "stock", "--create-only", PHONE)):
            with self.subTest(args=args):
                r, calls = self.run_boot("--start-only", *args)
                self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
                self.assertFalse(any("simctl boot" in c or "simctl create" in c or
                                     c.startswith("simslim ") for c in calls), calls)
                self.assertEqual(self.device(PHONE)["state"], "Shutdown")
                self.assertEqual(self.device(WATCH)["state"], "Shutdown")

    def test_create_only_returns_a_new_shutdown_device_without_booting(self):
        r, calls = self.run_boot("--create-only", "--profile", "stock", "new owned test device")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), NEW)
        self.assertEqual(self.device(NEW)["state"], "Shutdown")
        self.assertEqual(self.device(PHONE)["state"], "Shutdown")
        self.assertEqual(self.device(WATCH)["state"], "Shutdown")
        self.assertFalse(any(c.startswith("simslim ") or " boot" in c for c in calls), calls)
        self.assertEqual(self.verdict(r), f"sim-boot: created stock {NEW}")

    def test_create_only_rejects_existing_devices_and_nonstock_modes(self):
        for args in (("--profile", "stock", "mine"), ("--profile", "stock", PHONE),
                     ("new device",), ("--profile", "stock", "--reslim", "new device")):
            with self.subTest(args=args):
                r, calls = self.run_boot("--create-only", *args)
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertEqual(r.stdout, "")
                self.assertFalse(any(not c.startswith("xcrun simctl list") for c in calls), calls)

    def test_a_new_name_creates_on_the_newest_runtime_that_runs_the_device_type(self):
        r, calls = self.run_boot("Persona Shots 1")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), NEW)
        self.assertIn("xcrun simctl create Persona Shots 1 com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro "
                      "com.apple.CoreSimulator.SimRuntime.iOS-27-0", calls)
        self.assertEqual(self.device(NEW)["state"], "Booted")

    def test_without_simslim_it_boots_stock_and_says_how_to_install(self):
        r, calls = self.run_boot("mine", simslim=False)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), PHONE)
        self.assertIn("brew install mobai-app/tap/simslim", r.stderr)
        self.assertEqual(self.verdict(r), f"sim-boot: stock-no-simslim default {PHONE}")
        self.assertIn(f"xcrun simctl boot {PHONE}", calls)
        self.assertEqual(self.device(PHONE)["state"], "Booted")

    def test_stock_profile_puts_a_slim_shutdown_simulator_back(self):
        self.state["slim"] = {PHONE: "x"}
        self.save()
        r, calls = self.run_boot("--profile", "stock", "mine")
        self.assertEqual(self.verdict(r), f"sim-boot: stock stock {PHONE}")
        self.assertIn(f"simslim off {PHONE} --preserve-boot-state", calls)
        self.assertEqual(self.device(PHONE)["state"], "Booted")

    def test_boot_progress_stays_on_stderr_and_stdout_is_only_the_udid(self):
        self.state.update(boot_progress="Waiting on Data Migration", boot_error="boot diagnostic")
        self.save()
        r, _ = self.run_boot("--profile", "stock", "mine", simslim=False)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), PHONE)
        self.assertIn("Waiting on Data Migration", r.stderr)
        self.assertIn("boot diagnostic", r.stderr)

    def test_failed_boot_reports_the_original_diagnostic_without_a_success_udid(self):
        self.state.update(bootstatus_rc=1, boot_error="SpringBoard did not become ready")
        self.save()
        r, _ = self.run_boot("--profile", "stock", "mine", simslim=False)
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertEqual(r.stdout, "")
        self.assertIn("SpringBoard did not become ready", r.stderr)
        self.assertIn("did not finish booting", r.stderr)

    def test_bootstatus_timeout_preserves_exit_124(self):
        self.state.update(bootstatus_rc=124)
        self.save()
        r, _ = self.run_boot("--profile", "stock", "mine", simslim=False)
        self.assertEqual(r.returncode, 124, r.stderr)
        self.assertEqual(r.stdout, "")
        self.assertIn("bootstatus exit 124", r.stderr)

    def test_boot_failure_preserves_exit_and_does_not_wait(self):
        self.state.update(boot_rc=7)
        self.save()
        r, calls = self.run_boot("--profile", "stock", "mine", simslim=False)
        self.assertEqual(r.returncode, 7, r.stderr)
        self.assertEqual(r.stdout, "")
        self.assertIn("boot exit 7", r.stderr)
        self.assertFalse(any("bootstatus" in c for c in calls))

    def test_bootstatus_diagnostics_are_preserved_without_polluting_udid(self):
        r, calls = self.run_boot('--profile', 'stock', 'mine')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), PHONE)
        self.assertIn('Boot phase: waiting for SpringBoard', r.stderr)
        self.assertIn('bootstatus stderr diagnostic', r.stderr)
        self.assertFalse(any(c.startswith("sample ") or " spawn " in c for c in calls))

    def test_failed_bootstatus_is_reported_without_automatic_retry(self):
        self.state['bootstatus_rc'] = 1
        self.save()
        r, calls = self.run_boot('--profile', 'stock', 'mine')
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertEqual(r.stdout, '')
        self.assertIn('Boot phase: waiting for SpringBoard', r.stderr)
        self.assertIn('did not finish booting', r.stderr)
        self.assertEqual(calls.count(f'xcrun simctl boot {PHONE}'), 1)
        self.assertFalse(any(call.startswith('xcrun simctl shutdown') for call in calls))

    def test_pending_boot_collects_live_scoped_diagnostics_but_still_times_out(self):
        self.state.update(bootstatus_hang=True, diagnostic_udid=PHONE)
        self.save()
        r, calls = self.run_boot("--profile", "stock", "mine", env={"SIM_BOOT_TIMEOUT": "1"})
        self.assertEqual(r.returncode, 124, r.stderr)
        self.assertEqual(r.stdout, "")
        self.assertIn("live bootstatus stack", r.stderr)
        self.assertIn("owned launchd service status", r.stderr)
        self.assertIn("bootstatus deadline reached", r.stderr)
        self.assertEqual(calls.count(f"xcrun simctl boot {PHONE}"), 1)
        self.assertEqual([c for c in calls if " spawn " in c],
                         [f"xcrun simctl spawn {PHONE} launchctl list"])
        self.assertFalse(any(WATCH in c or "shutdown" in c or "delete" in c for c in calls))
        self.assertEqual(self.device(WATCH)["state"], "Shutdown")
        pid = int((self.dir / "bootstatus.pid").read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)

    def test_diagnostic_failure_or_hang_does_not_extend_or_mask_boot_timeout(self):
        for settings in ({"diagnostic_rc": 9}, {"diagnostic_hang": True}):
            with self.subTest(settings=settings):
                self.state.update(bootstatus_hang=True, diagnostic_udid=PHONE,
                                  diagnostic_rc=0, diagnostic_hang=False)
                self.state.update(settings)
                self.state["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-27-0"][0]["state"] = "Shutdown"
                self.save()
                r, _ = self.run_boot("--profile", "stock", "mine", env={"SIM_BOOT_TIMEOUT": "1"}, timeout=5)
                self.assertEqual(r.returncode, 124, r.stderr)
                self.assertEqual(r.stdout, "")
                self.assertIn("collecting live diagnostics", r.stderr)
                if settings.get("diagnostic_rc"):
                    self.assertIn("exited 9", r.stderr)
                else:
                    self.assertIn("stopped at end of boot wait", r.stderr)
                for name in ("bootstatus", "sample", "launchctl"):
                    pid = int((self.dir / f"{name}.pid").read_text())
                    with self.assertRaises(ProcessLookupError):
                        os.kill(pid, 0)

    def test_exited_diagnostic_leader_cannot_leave_a_child_holding_stderr(self):
        self.state.update(bootstatus_hang=True, diagnostic_udid=PHONE, diagnostic_orphan=True)
        self.save()
        try:
            # Without group cleanup communicate() hangs on the child's stderr pipe.
            r, _ = self.run_boot("--profile", "stock", "mine", env={"SIM_BOOT_TIMEOUT": "1"}, timeout=5)
            self.assertEqual(r.returncode, 124, r.stderr)
            self.assertEqual(r.stdout, "")
            self.assertTrue((self.dir / "sample-child.pid").exists())
            self.assertIn("exited 0", r.stderr)
        finally:
            pid_file = self.dir / "sample.pid"
            if pid_file.exists():
                try:
                    os.killpg(int(pid_file.read_text()), signal.SIGKILL)
                except ProcessLookupError:
                    pass  # The helper already removed its diagnostic process group.

    def test_unavailable_diagnostic_does_not_mask_boot_timeout(self):
        (self.bin / "sample").write_text("invalid executable without a shebang\n")
        self.state.update(bootstatus_hang=True, diagnostic_udid=PHONE)
        self.save()
        r, _ = self.run_boot("--profile", "stock", "mine", env={"SIM_BOOT_TIMEOUT": "1"}, timeout=5)
        self.assertEqual(r.returncode, 124, r.stderr)
        self.assertEqual(r.stdout, "")
        self.assertIn("could not start diagnostic", r.stderr)
        self.assertIn("owned launchd service status", r.stderr)

    def test_completion_during_diagnostics_preserves_bootstatus_result(self):
        for code in (0, 7):
            with self.subTest(code=code):
                self.state.update(bootstatus_hang=True, diagnostic_udid=PHONE,
                                  complete_during_diagnostics=True, bootstatus_rc=code)
                self.state["devices"]["com.apple.CoreSimulator.SimRuntime.iOS-27-0"][0]["state"] = "Shutdown"
                self.save()
                r, _ = self.run_boot("--profile", "stock", "mine", env={"SIM_BOOT_TIMEOUT": "1"})
                self.assertEqual(r.returncode, code, r.stderr)
                self.assertEqual(r.stdout.strip(), PHONE if code == 0 else "")
                self.assertIn("live bootstatus stack", r.stderr)

    def test_diagnostics_share_the_original_deadline(self):
        spec = importlib.util.spec_from_file_location("sim_boot_wait", SCRIPT.with_name("sim_boot_wait.py"))
        helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(helper)
        boot = mock.Mock(pid=123, **{"wait.side_effect": [subprocess.TimeoutExpired("bootstatus", 890), 7],
                                    "poll.return_value": 7})
        diagnostic = mock.Mock(**{"poll.return_value": 0})
        with mock.patch.object(helper.time, "monotonic", side_effect=[100, 100, 993]), \
                mock.patch.object(helper.subprocess, "Popen", side_effect=[boot, diagnostic, diagnostic]), \
                mock.patch.object(helper, "log"), mock.patch.object(helper, "stop") as stop:
            self.assertEqual(helper.wait_booted(PHONE, 900), 7)
        self.assertEqual(stop.call_args_list, [mock.call(diagnostic), mock.call(diagnostic)])
        # Three seconds starting diagnostics leaves seven of the original 900.
        self.assertEqual(boot.wait.call_args_list, [mock.call(timeout=890), mock.call(timeout=7)])

    def test_usage_errors_exit_2_and_touch_nothing(self):
        for args in (("--profile", "nope", "mine"), ("44444444-4444-4444-4444-444444444444",), ()):
            with self.subTest(args=args):
                if (self.dir / "calls").exists():
                    (self.dir / "calls").unlink()
                r, calls = self.run_boot(*args)
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertEqual(r.stdout, "")
                self.assertFalse([c for c in calls if not c.startswith(("xcrun simctl list", "xcrun --find"))], calls)

    def test_profiles_are_json_simslim_accepts(self):
        allowed = {"name", "description", "except", "keep"}
        for p in (ROOT / "apple" / "simslim").glob("*.json"):
            with self.subTest(profile=p.name):
                d = json.loads(p.read_text())
                self.assertLessEqual(set(d), allowed)  # simslim rejects unknown fields
                default = json.loads((ROOT / "apple/simslim/default.json").read_text())
                self.assertLessEqual(set(default["except"]), set(d["except"]))  # every profile is at least default
                self.assertLessEqual(set(default["keep"]), set(d["keep"]))


if __name__ == "__main__":
    unittest.main()
