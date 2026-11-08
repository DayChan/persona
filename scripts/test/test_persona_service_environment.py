import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
from test_persona_script_identity import FORMER_NAME, FORMER_PREFIX

SCRIPT = ROOT / 'scripts/deploy/node-launchd.sh'


class PersonaServiceEnvironmentTests(unittest.TestCase):
    def test_selected_profile_survives_service_manager_restart(self):
        for system in ('Linux', 'Darwin'):
            for custom in (False, True):
                with self.subTest(system=system, custom=custom), tempfile.TemporaryDirectory() as directory:
                    home = Path(directory)
                    state = home / ('profile with $ & < > % chars' if custom else '.persona')
                    (state / 'bin').mkdir(parents=True)
                    binary = state / 'bin/personad'
                    binary.write_text('#!/bin/sh\nexit 0\n')
                    binary.chmod(0o755)
                    commands = home / 'commands'
                    commands.mkdir()
                    for name, body in {
                        'uname': '#!/bin/sh\nprintf "%s\\n" "' + system + '"\n',
                        'systemctl': '#!/bin/sh\nexit 0\n',
                        'launchctl': '#!/bin/sh\nexit 0\n',
                        'loginctl': '#!/bin/sh\nprintf "Linger=yes\\n"\n',
                    }.items():
                        path = commands / name
                        path.write_text(body)
                        path.chmod(0o755)
                    env = {k: v for k, v in os.environ.items() if not k.startswith(('PERSONA_', FORMER_PREFIX))}
                    env.update({'HOME': str(home), 'PATH': str(commands) + ':' + os.environ['PATH']})
                    if custom:
                        env['PERSONA_HOME'] = str(state)
                        env[FORMER_PREFIX + 'HOME'] = str(home / 'ignored-former-profile')
                    result = subprocess.run(['bash', str(SCRIPT), 'install'], env=env, capture_output=True, text=True, timeout=15)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    if system == 'Darwin':
                        with open(home / 'Library/LaunchAgents/com.persona.node.plist', 'rb') as file:
                            configuration = plistlib.load(file)
                        self.assertEqual(configuration['ProgramArguments'][0], str(binary))
                        self.assertEqual(configuration['EnvironmentVariables'], {'PERSONA_HOME': str(state), FORMER_PREFIX + 'HOME': str(state)})
                        self.assertEqual(configuration['StandardErrorPath'], str(state / 'node-attach.log'))
                        # Not throttled like a background job (#5944).
                        self.assertEqual(configuration['ProcessType'], 'Interactive')
                    else:
                        configuration = (home / '.config/systemd/user/personad-node.service').read_text()
                        self.assertIn('Environment="PERSONA_HOME=' + str(state).replace('%', '%%') + '"', configuration)
                        self.assertIn('Environment="' + FORMER_PREFIX + 'HOME=' + str(state).replace('%', '%%') + '"', configuration)
                        self.assertIn('ExecStart="' + str(binary).replace('$', '$$').replace('%', '%%') + '" node --attach', configuration)
                    self.assertFalse((home / 'ignored-former-profile').exists())
                    self.assertFalse((home / '.config/systemd/user' / (FORMER_NAME + 'd-node.service')).exists())
                    self.assertFalse((home / 'Library/LaunchAgents' / ('com.' + FORMER_NAME + '.node.plist')).exists())


    def test_restart_idle_works_with_older_systemd(self):
        """restart-idle signals the node's main process with an option every systemd reads: --kill-whom= is only
        systemd 252's new name; 241 and 245 (seen on two Linux dev machines) know only --kill-who=, which 252 and
        later still accept. With the new name the signal never went out and deploys left the resident node on its old
        binary."""
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            state = home / '.persona'
            (state / 'bin').mkdir(parents=True)
            commands = home / 'commands'
            commands.mkdir()
            calls = home / 'calls'
            for name, body in {
                'uname': '#!/bin/sh\necho Linux\n',
                # systemd 245: no --kill-whom=.
                'systemctl': '#!/bin/sh\necho "$*" >> "$HOME/calls"\n'
                             'for a in "$@"; do case $a in --kill-whom=*) echo "systemctl: unrecognized option \'$a\'" >&2; exit 1;; esac; done\n'
                             'exit 0\n',
            }.items():
                path = commands / name
                path.write_text(body)
                path.chmod(0o755)
            env = {k: v for k, v in os.environ.items() if not k.startswith(('PERSONA_', FORMER_PREFIX))}
            env.update({'HOME': str(home), 'PATH': str(commands) + ':' + os.environ['PATH']})
            result = subprocess.run(['bash', str(SCRIPT), 'restart-idle'], env=env, capture_output=True, text=True, timeout=15)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('restarts once no conversation is busy', result.stdout)
            self.assertIn('--user kill -s HUP --kill-who=main personad-node.service', calls.read_text().splitlines())


if __name__ == '__main__':
    unittest.main()
