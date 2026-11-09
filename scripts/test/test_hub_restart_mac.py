"""hub-systemd.sh on a Mac hub: no systemd. It first asks the new binary to switch the running hub's edge over
(blue-green, docs/operations/hub-restart.md "Blue-green switching"); with no edge to switch on, it restarts the launchd agent that already runs
<root>/bin/personad hub."""
import os
from pathlib import Path
import plistlib
import platform
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/deploy/hub-systemd.sh'


def agents(home: Path) -> dict:
    """A node and hub use an explicit custom ~/.state-link path to the canonical root. The node sorts first,
    so the script must pass it over: it runs the same personad as node, not hub."""
    return {
        'a.node': {'Label': 'a.node', 'ProgramArguments': [str(home / '.state-link/bin/personad'), 'node']},
        'my.hub': {'Label': 'my.hub', 'ProgramArguments': [str(home / '.state-link/bin/personad'), 'hub']},
    }


class AgentFixtureIdentity(unittest.TestCase):
    def test_custom_link_is_not_the_canonical_directory(self):
        with tempfile.TemporaryDirectory() as d:
            home = Path(d)
            canonical = home / '.persona'
            canonical.mkdir()
            alias = home / '.state-link'
            os.symlink(canonical, alias)
            self.assertNotEqual(alias, canonical)
            self.assertEqual(alias.resolve(), canonical.resolve())  # the temporary directory may be behind a link (/var on macOS)
            for job in agents(home).values():
                self.assertEqual(Path(job['ProgramArguments'][0]).parent.parent.resolve(), canonical.resolve())


def switch_calls(home: Path) -> set:
    """The switch the script asks personad for: the program given (as the state directory resolves, or not)."""
    return {f'personad hub switch --binary {p}/bin/personad --timeout 8m' for p in (home / '.persona', (home / '.persona').resolve())}


@unittest.skipUnless(platform.system() == 'Darwin', 'the launchd branch runs only on macOS')
class HubRestartOnMac(unittest.TestCase):
    def personad(self, home: Path, rc: int):
        """The new binary: each call is logged, and `hub switch` exits rc (3: no edge here, 0: switched)."""
        p = home / '.persona/bin/personad'
        p.write_text(f'#!/bin/sh\necho "personad $*" >> "$HOME/calls"\nexit {rc}\n')
        p.chmod(0o755)

    def run_script(self, home: Path, jobs: dict) -> subprocess.CompletedProcess:
        (home / 'Library/LaunchAgents').mkdir(parents=True)
        for name, job in jobs.items():
            with open(home / 'Library/LaunchAgents' / f'{name}.plist', 'wb') as f:
                plistlib.dump(job, f)
        fake = home / 'fakebin'
        fake.mkdir()
        (fake / 'launchctl').write_text('#!/bin/sh\necho "launchctl $*" >> "$HOME/calls"\n')
        (fake / 'launchctl').chmod(0o755)
        env = {**os.environ, 'HOME': str(home), 'PATH': f'{fake}:{os.environ["PATH"]}',
               '_PERSONA_STATE_DIR': str(home / '.persona')}
        return subprocess.run(['/bin/bash', str(SCRIPT)], capture_output=True, text=True, env=env)

    def test_restarts_the_agent_running_this_roots_hub(self):
        with tempfile.TemporaryDirectory() as d:
            home = Path(d)
            (home / '.persona/bin').mkdir(parents=True)
            self.personad(home, 3)  # no edge to switch on
            os.symlink(home / '.persona', home / '.state-link')  # the agent may name an explicit custom symlink
            r = self.run_script(home, agents(home))
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn('restarted launchd agent my.hub', r.stdout)
            calls = (home / 'calls').read_text().splitlines()
            self.assertEqual(len(calls), 2, calls)
            self.assertIn(calls[0], switch_calls(home))
            self.assertEqual(calls[1], f'launchctl kickstart -k gui/{os.getuid()}/my.hub')

    def test_switches_the_edge_instead_of_restarting(self):
        with tempfile.TemporaryDirectory() as d:
            home = Path(d)
            (home / '.persona/bin').mkdir(parents=True)
            self.personad(home, 0)  # the running edge switched to the new binary
            os.symlink(home / '.persona', home / '.state-link')
            r = self.run_script(home, agents(home))
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn('switched to the new binary', r.stdout)
            calls = (home / 'calls').read_text().splitlines()
            self.assertEqual(len(calls), 1, calls)
            self.assertIn(calls[0], switch_calls(home))

    def test_no_agent_is_an_error_not_a_silent_success(self):
        with tempfile.TemporaryDirectory() as d:
            home = Path(d)
            (home / '.persona/bin').mkdir(parents=True)
            r = self.run_script(home, {})
            self.assertEqual(r.returncode, 1)
            self.assertIn('no launchd agent', r.stderr)
            self.assertFalse((home / 'calls').exists())


if __name__ == '__main__':
    unittest.main()
