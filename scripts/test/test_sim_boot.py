"""scripts/app/sim-boot.sh against a fake xcrun and simslim (no real simulator is touched).

python3 -B scripts/test/test_sim_boot.py
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "app" / "sim-boot.sh"
PHONE = "11111111-1111-1111-1111-111111111111"
WATCH = "22222222-2222-2222-2222-222222222222"
NEW = "33333333-3333-3333-3333-333333333333"

# The fakes read their world from $FAKE/state.json and append every call to $FAKE/calls.
FAKE_XCRUN = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, sys
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
    elif a[0] == "bootstatus":
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


class SimBootTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sim-boot ")
        self.dir = Path(self.tmp.name)
        self.bin = self.dir / "bin"
        self.bin.mkdir()
        for name, body in (("xcrun", FAKE_XCRUN), ("simslim", FAKE_SIMSLIM)):
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

    def run_boot(self, *args, simslim=True, env=None):
        path = [str(self.bin)] if simslim else []
        if not simslim:
            nobin = self.dir / "nosimslim"
            nobin.mkdir(exist_ok=True)
            (nobin / "xcrun").write_text(FAKE_XCRUN)
            (nobin / "xcrun").chmod(0o755)
            path = [str(nobin)]
        e = {"PATH": ":".join(path + ["/usr/bin", "/bin"]), "HOME": str(self.dir), "FAKE": str(self.dir), "SIM_BOOT_TIMEOUT": "20"}
        e.update(env or {})
        r = subprocess.run(["bash", str(SCRIPT), *args], capture_output=True, text=True, env=e, timeout=120, cwd=self.dir)
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
