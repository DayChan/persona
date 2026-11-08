import importlib.util
import json
import os
from pathlib import Path
import plistlib
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/persona_identity.py'
GIT = shutil.which('git')
spec = importlib.util.spec_from_file_location('runtime_identity_under_test', SCRIPT)
identity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(identity)


def environment(home, **extra):
    return {'HOME': str(home), 'PATH': os.environ['PATH'], 'PYTHONDONTWRITEBYTECODE': '1', **extra}


def executable(path, text):
    path.write_text(text)
    path.chmod(0o755)


FORMER_NAME = 'jar' + 'vis'
FORMER_PREFIX = FORMER_NAME.upper() + '_'


class PersonaScriptIdentityTests(unittest.TestCase):
    def test_environment_uses_only_the_current_prefix_including_explicit_empty(self):
        for values, expected in (({}, ''), ({FORMER_PREFIX + 'HOME': 'old'}, 'old'),
                                 ({'PERSONA_HOME': 'new', FORMER_PREFIX + 'HOME': 'old'}, 'new'),
                                 ({'PERSONA_HOME': '', FORMER_PREFIX + 'HOME': 'old'}, '')):
            self.assertEqual(identity.env_value('PERSONA_HOME', values), expected)
        self.assertEqual(identity.env_value('PERSONA_PERSONA_ID', {'PERSONA_PERSONA_ID': 'correct', 'PERSONA_ID': 'wrong'}), 'correct')

    def test_lexical_custom_roots_do_not_create_default_aliases(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            for value, expected in (('~', home), ('~/state with spaces', home / 'state with spaces'),
                                    ('one/../two', home / 'two'), ('/tmp/a/../b', Path('/tmp/b'))):
                self.assertEqual(identity.data_dir(home, {'PERSONA_HOME': value}), str(expected))
            custom = home / 'profile with $ " \\ % characters'
            self.assertEqual(identity.prepare_dir(home, {'PERSONA_HOME': str(custom), FORMER_PREFIX + 'HOME': 'ignored'}), str(custom))
            self.assertFalse((home / '.persona').exists())
            self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))
            (home / 'hop').symlink_to(custom, target_is_directory=True)
            self.assertEqual(identity.data_dir(home, {'PERSONA_HOME': 'hop/../second'}), str(home / 'second'))

    def test_default_query_is_pure_and_initialization_preserves_one_store(self):
        for initial in ('fresh', 'former', 'current', 'independent'):
            with self.subTest(initial=initial), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                former, current = home / ('.' + FORMER_NAME), home / '.persona'
                if initial in ('former', 'independent'):
                    former.mkdir()
                    (former / 'old.fixture').write_text('old-data')
                if initial in ('current', 'independent'):
                    current.mkdir()
                    (current / 'new.fixture').write_text('new-data')
                before = sorted(p.name for p in home.iterdir())
                if initial == 'independent':
                    with self.assertRaisesRegex(OSError, 'independent'):
                        identity.prepare_dir(home, {})
                    self.assertEqual(sorted(p.name for p in home.iterdir()), before)
                    self.assertFalse(os.path.samefile(former, current))
                    continue
                expected = former if initial == 'former' else current
                self.assertEqual(identity.data_dir(home, {}), str(expected))
                self.assertEqual(sorted(p.name for p in home.iterdir()), before)
                identity.prepare_dir(home, {})
                identity.prepare_dir(home, {})
                if initial == 'former':
                    # a former-name store gets the current name as an alias (#1445)
                    self.assertTrue(os.path.samefile(former, current))
                    self.assertEqual((current / 'old.fixture').read_text(), 'old-data')
                else:
                    # a current-name store gets no former-name alias (#1445)
                    self.assertFalse(os.path.lexists(former))
                    self.assertEqual(sorted(p.name for p in home.iterdir()), ['.persona'])

    def test_dangling_canonical_root_is_not_repaired(self):
        for target in ('./missing', './.persona/.'):
            with self.subTest(target=target), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                current = home / '.persona'
                current.symlink_to(target, target_is_directory=True)
                with self.assertRaisesRegex(OSError, 'unavailable'):
                    identity.prepare_dir(home, {})
                self.assertEqual(os.readlink(current), target)
                self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))
                self.assertFalse((home / 'missing').exists())

    def test_external_dangling_cycles_and_symlink_parent_escape_are_rejected(self):
        for kind in ('external', 'cycle', 'parent-escape', 'custom'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                home = Path(directory) / 'home'
                home.mkdir()
                external = Path(directory) / 'external'
                external.mkdir()
                env = {}
                if kind == 'external':
                    (home / '.persona').symlink_to(external / 'missing')
                elif kind == 'cycle':
                    (home / '.persona').symlink_to('.persona')
                elif kind == 'parent-escape':
                    (external / 'nested').mkdir()
                    (home / 'hop').symlink_to(external / 'nested')
                    (home / '.persona').symlink_to('hop/../.persona')
                else:
                    (home / 'custom').symlink_to(external / 'missing')
                    env['PERSONA_HOME'] = str(home / 'custom')
                with self.assertRaises(OSError):
                    identity.prepare_dir(home, env)
                self.assertFalse((external / 'missing').exists())
                self.assertFalse((external / '.persona').exists())

    def test_former_name_alias_only_for_a_former_name_store(self):
        # #1445: a current-name store gets no alias under the former name, and one an operator left (or removed)
        # stays as it is; a former-name store gets the current name as an alias to the same store
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            former = home / ('.' + FORMER_NAME)
            identity.prepare_dir(home, {})
            identity.prepare_dir(home, {})
            self.assertFalse(os.path.lexists(former))
            former.symlink_to('.persona')
            identity.prepare_dir(home, {})
            self.assertEqual(os.readlink(former), '.persona')
            former.unlink()
            identity.prepare_dir(home, {})
            self.assertFalse(os.path.lexists(former))
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            former = home / ('.' + FORMER_NAME)
            former.mkdir()
            identity.prepare_dir(home, {})
            self.assertEqual(os.readlink(home / '.persona'), '.' + FORMER_NAME)
            self.assertTrue(os.path.samefile(home / '.persona', former))

    def test_service_root_survives_an_ssh_environment_without_prefixes(self):
        for name in ('personad.service', 'personad-node.service', 'persona-link.service'):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                root = str(home / 'profile with $ " \\ %h chars')
                unit = home / '.config/systemd/user' / name
                unit.parent.mkdir(parents=True)
                quote = lambda value: '"' + value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
                unit.write_text('[Service]\nEnvironment=' + quote('PERSONA_HOME=ignored') + '\nEnvironment=' + quote('PERSONA_HOME=' + root) + '\n')
                self.assertEqual(identity.data_dir(home, identity.service_environment(home, {})), root)
                self.assertEqual(identity.service_environment(home, {'PERSONA_HOME': '', FORMER_PREFIX + 'HOME': 'explicit'}), {'PERSONA_HOME': '', FORMER_PREFIX + 'HOME': 'explicit'})
                unit.write_text('[Service]\nEnvironment="PERSONA_HOME=%h/profile"\n')
                self.assertEqual(identity.data_dir(home, identity.service_environment(home, {})), str(home / 'profile'))

    def test_role_selection_never_borrows_the_colocated_hubs_root(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            units = home / '.config/systemd/user'; units.mkdir(parents=True)
            for name, role in [('personad.service', 'hub'), ('personad-node.service', 'node'), ('persona-link.service', 'link')]:
                (units / name).write_text('[Service]\nEnvironment="PERSONA_HOME=' + str(home / role) + '"\n')
            for role in ('hub', 'node', 'link'):
                self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, role)), str(home / role))
            with self.assertRaisesRegex(OSError, 'different state roots'):
                identity.service_environment(home, {})
            binary = home / 'link/bin/persona-link'; binary.parent.mkdir(parents=True)
            executable(binary, '#!/bin/sh\nprintf "%s" "$PERSONA_HOME"\n')
            result = subprocess.run(['bash', str(ROOT / 'scripts/deploy/link-systemd.sh'), 'status'], env=environment(home), capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, str(home / 'link'))

    def test_current_unit_without_environment_keeps_its_default_root(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            current = home / '.persona'; current.mkdir()
            units = home / '.config/systemd/user'; units.mkdir(parents=True)
            (units / 'personad-node.service').write_text('[Service]\nExecStart=%h/.persona/bin/personad node --attach\n')
            (units / 'persona-link.service').write_text('[Service]\nEnvironment="PERSONA_HOME=other-link"\n')
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, 'node')), str(current))
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, 'link')), str(home / 'other-link'))

    def test_former_service_definitions_fail_closed_on_missing_required_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            units = home / '.config/systemd/user'
            units.mkdir(parents=True)
            for suffix in ('d.service', 'd-node.service', '-link.service'):
                (units / (FORMER_NAME + suffix)).write_text('[Service]\nEnvironmentFile=/unread/former.env\n')
            for role in ('auto', 'hub', 'node', 'link'):
                with self.assertRaises(FileNotFoundError):
                    identity.service_environment(home, {}, role)
            (units / (FORMER_NAME + 'd.service')).write_text('[Service]\nEnvironment="' + FORMER_PREFIX + 'HOME=old-profile"\n')
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, 'hub')), str(home / 'old-profile'))
            self.assertFalse((home / 'old-profile').exists())

    def test_launchd_services_choose_the_matching_role(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            agents = home / 'Library/LaunchAgents'; agents.mkdir(parents=True)
            for role in ('node', 'link'):
                (agents / ('com.persona.' + role + '.plist')).write_bytes(plistlib.dumps({'EnvironmentVariables': {'PERSONA_HOME': role + '-profile'}}))
            for role in ('node', 'link'):
                self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, role)), str(home / (role + '-profile')))

    def test_services_with_the_same_canonical_root_are_not_ambiguous(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            identity.prepare_dir(home, {})
            units = home / '.config/systemd/user'; units.mkdir(parents=True)
            (units / 'personad.service').write_text('[Service]\nEnvironment="PERSONA_HOME=' + str(home / '.persona') + '"\n')
            (units / 'personad-node.service').write_text('[Service]\nEnvironment="PERSONA_HOME=' + str(home / '.persona') + '"\n')
            self.assertTrue(os.path.samefile(identity.data_dir(home, identity.service_environment(home, {})), home / '.persona'))

    def test_unit_resets_and_ordered_dropins_match_the_effective_root(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            unit = home / '.config/systemd/user/personad.service'; unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nEnvironment="PERSONA_HOME=old"\nEnvironment=\nEnvironment="PERSONA_HOME=after-reset"\n')
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, 'hub')), str(home / 'after-reset'))
            dropins = unit.with_name(unit.name + '.d'); dropins.mkdir()
            (dropins / '10-root.conf').write_text('[Service]\nEnvironment="PERSONA_HOME=first"\n')
            (dropins / '20-root.conf').write_text('[Service]\nEnvironment="PERSONA_HOME=last"\n')
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, 'hub')), str(home / 'last'))
            (dropins / '30-empty.conf').write_text('[Service]\nEnvironment="PERSONA_HOME="\n')
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {}, 'hub')), str(home / '.persona'))
            (dropins / '40-external.conf').write_text('[Service]\nEnvironmentFile=/unread/fixture.env\n')
            with self.assertRaisesRegex(FileNotFoundError, 'unavailable'):
                identity.service_environment(home, {}, 'hub')

    def test_hub_unit_writer_and_reader_agree_with_xdg_config_home(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            commands = home / 'commands'; commands.mkdir()
            executable(commands / 'systemctl', '#!/bin/sh\n[ -f "$XDG_CONFIG_HOME/systemd/user/personad.service" ] || exit 4\n')
            config = home / 'xdg config'
            root = home / 'hub profile'
            env = environment(home, PERSONA_HOME=str(root), XDG_CONFIG_HOME=str(config))
            env['PATH'] = str(commands) + ':' + env['PATH']
            result = subprocess.run(['bash', str(ROOT / 'scripts/deploy/hub-systemd.sh')], env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse((home / '.config').exists())
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {'XDG_CONFIG_HOME': str(config)}, 'hub')), str(root))

    def test_xdg_service_and_launchd_environment_are_read_without_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / 'alternate config'
            unit = config / 'systemd/user/persona-link.service'
            unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nEnvironment="PERSONA_HOME=profile"\n')
            values = identity.service_environment(home, {'XDG_CONFIG_HOME': str(config)})
            self.assertEqual(identity.data_dir(home, values), str(home / 'profile'))
            plist = home / 'Library/LaunchAgents/com.persona.node.plist'
            plist.parent.mkdir(parents=True)
            plist.write_bytes(plistlib.dumps({'EnvironmentVariables': {'PERSONA_HOME': str(home / 'Mac profile')}}))
            self.assertEqual(identity.data_dir(home, identity.service_environment(home, {})), str(home / 'Mac profile'))

    def test_embedded_remote_resolver_preserves_binary_stdin_and_uses_remote_home(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            local, remote = base / 'local', base / 'remote'
            local.mkdir(); remote.mkdir()
            root = remote / 'custom remote'
            unit = remote / '.config/systemd/user/personad.service'
            unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nEnvironment="PERSONA_HOME=' + str(root) + '"\n')
            result = subprocess.run([sys.executable, str(SCRIPT), '--prepare', '--prefix', '-'],
                                    env=environment(local, PERSONA_HOME=str(local / 'local profile')), capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            command = result.stdout + '\ncat > "$PERSONA_HOME/payload.fixture"\nprintf "%s" "$PERSONA_HOME"\n'
            payload = bytes(range(256)) * 64
            run = subprocess.run(['bash', '-c', command], input=payload, env=environment(remote), capture_output=True, timeout=10)
            self.assertEqual(run.returncode, 0, run.stderr)
            self.assertEqual(run.stdout.decode(), str(root))
            self.assertEqual((root / 'payload.fixture').read_bytes(), payload)
            self.assertFalse((local / 'local profile').exists())
            self.assertFalse((remote / '.persona').exists())

    def test_hub_unit_retains_custom_root_and_one_service_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            commands = home / 'commands'; commands.mkdir()
            executable(commands / 'systemctl', '#!/bin/sh\nprintf "%s\\n" "$*" >> "$HOME/calls.fixture"\n')
            root = home / 'profile $ with " \\ %h'
            env = environment(home, PERSONA_HOME=str(root), **{FORMER_PREFIX + 'HOME': 'ignored'})
            env['PATH'] = str(commands) + ':' + env['PATH']
            result = subprocess.run(['bash', str(ROOT / 'scripts/deploy/hub-systemd.sh')], env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            unit = home / '.config/systemd/user/personad.service'
            values = identity.service_environment(home, {})
            self.assertEqual(identity.data_dir(home, values), str(root))
            self.assertIn('personad', (home / 'calls.fixture').read_text())
            self.assertEqual(list(unit.parent.glob('*.service')), [unit])
            line = next(line for line in unit.read_text().splitlines() if line.startswith('ExecStart='))
            self.assertEqual(shlex.split(line.split('=', 1)[1])[0].replace('$$', '$').replace('%%', '%'), str(root / 'bin/personad'))

    def test_link_wrapper_exports_only_the_discovered_canonical_service_root(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / 'link profile'
            binary = root / 'bin/persona-link'; binary.parent.mkdir(parents=True)
            executable(binary, '#!/bin/sh\nprintf "%s|%s|%s" "$PERSONA_HOME" "${' + FORMER_PREFIX + 'HOME-unset}" "$1"\n')
            unit = home / '.config/systemd/user/persona-link.service'; unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nEnvironment="PERSONA_HOME=' + str(root) + '"\n')
            result = subprocess.run(['bash', str(ROOT / 'scripts/deploy/link-systemd.sh'), 'status'], env=environment(home), capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, f'{root}|{root}|status')

    def test_cli_keeps_explicit_token_sources_and_accepts_current_bundle_variable(self):
        spec = importlib.util.spec_from_file_location('runtime_ctl_under_test', ROOT / 'scripts/app/personactl.py')
        ctl = importlib.util.module_from_spec(spec); spec.loader.exec_module(ctl)
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / 'profile'; root.mkdir()
            (root / 'hub.json').write_text(json.dumps({'token': 'fixture-only-token'}))
            token = home / 'token.fixture'; token.write_text('explicit-fixture')
            with patch.dict(os.environ, environment(home, PERSONA_HOME=str(root), PERSONA_BUNDLE_ID='com.example.persona', **{FORMER_PREFIX + 'BUNDLE_ID': 'com.example.legacy'}), clear=True):
                self.assertEqual(ctl.Ctl(udid='fixture-unused').bundle, 'com.example.persona')
                self.assertEqual(ctl.hub_token(), 'fixture-only-token')
                os.environ['PERSONACTL_HUB_TOKEN_FILE'] = str(token)
                self.assertEqual(ctl.hub_token(), 'explicit-fixture')
                os.environ['PERSONACTL_HUB_TOKEN'] = 'environment-fixture'
                self.assertEqual(ctl.hub_token(), 'environment-fixture')
                os.environ['PERSONA_BUNDLE_ID'] = ''
                self.assertEqual(ctl.Ctl(udid='fixture-unused').bundle, 'com.example.persona')


# A hang guard for one whole fixture deploy, not a speed check (#2264). The hub deploy starts about 48 Python processes
# and a nested bash for each fake ssh hop: about 7s of CPU, whose wall time grows with the machine's load (24-27s at
# load 45-50 on an 18-core Mac, past 30s at 80) without any step hanging.
DEPLOY_TIMEOUT = 120


class PersonaDeploymentScriptTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='persona-script-deploy-')
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.repo = self.base / 'repo'; self.repo.mkdir()
        (self.repo / 'server').mkdir(); (self.repo / 'web').mkdir()
        shutil.copytree(ROOT / 'scripts', self.repo / 'scripts', ignore=shutil.ignore_patterns('__pycache__'))
        (self.repo / 'web/package.json').write_text('{}')
        (self.repo / 'web/package-lock.json').write_text('{}')
        (self.repo / 'web/node_modules').mkdir()
        self.local = self.base / 'developer'; self.local.mkdir()
        self.remote = self.base / 'remote'; self.remote.mkdir()
        self.state = self.remote / 'deployed profile with $ " %'
        self.state.mkdir()
        (self.state / 'hub.json').write_text(json.dumps({'database_url': 'postgres://persona@localhost/persona?sslmode=disable'}))
        unit = self.remote / '.config/systemd/user/personad.service'; unit.parent.mkdir(parents=True)
        unit.write_text('[Service]\nEnvironment=' + shlex.quote('PERSONA_HOME=' + str(self.state)) + '\n')
        (unit.parent / 'personad-node.service').write_text(unit.read_text())
        self.commands = self.base / 'commands'; self.commands.mkdir()
        executable(self.commands / 'ssh', '#!' + sys.executable + '\n' + r'''
import os, subprocess, sys
args = sys.argv[1:]
while args and args[0].startswith('-'):
    key = args.pop(0)
    if key == '-o': args.pop(0)
args.pop(0)
env = {key:value for key,value in os.environ.items() if not key.startswith(('PERSONA_', 'JARVIS_'))}
env['HOME'] = env['FIXTURE_REMOTE_HOME']
result = subprocess.run(['bash', '-c', ' '.join(args)], env=env)
sys.exit(result.returncode)
''')
        executable(self.commands / 'go', '#!' + sys.executable + '\n' + r'''
import os, pathlib, sys
if sys.argv[1] == 'generate': sys.exit(0)
if sys.argv[1] == 'build':
    p = pathlib.Path(sys.argv[sys.argv.index('-o')+1]); p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text('#!/bin/sh\nprintf "fixture-binary\\n"\n'); p.chmod(0o755); sys.exit(0)
sys.exit(2)
''')
        executable(self.commands / 'npm', '#!' + sys.executable + '\nfrom pathlib import Path\np=Path("dist");p.mkdir(exist_ok=True);(p/"index.html").write_text("Persona fixture web")\n')
        executable(self.commands / 'git', '#!/bin/sh\ncase "$1" in describe) printf "fixture-version\\n";; rev-parse) printf "fixture-commit\\n";; esac\n')
        executable(self.commands / 'hostname', '#!/bin/sh\nprintf "fixture-local\\n"\n')
        executable(self.commands / 'scutil', '#!/bin/sh\nexit 1\n')
        executable(self.commands / 'systemctl', '#!/bin/sh\nprintf "%s\\n" "$*" >> "$HOME/systemctl.fixture"\n')
        executable(self.commands / 'docker', '#!/bin/sh\ncase "$1" in ps) printf "persona-postgres\\n";; inspect) printf "POSTGRES_USER=persona\\n";; exec) case "$3" in psql) printf "1\\n";; esac;; esac\n')
        config = self.base / 'config.json'
        config.write_text(json.dumps({'hub': {'machine': 'hub-fixture'}, 'machines': [
            {'id': 'hub-fixture', 'ssh': 'hub.example.test', 'os': 'linux', 'hostnames': ['fixture-remote']},
            {'id': 'node-fixture', 'ssh': 'node.example.test', 'os': 'darwin', 'hostnames': ['fixture-node']},
            {'id': 'via-fixture', 'ssh': 'via.example.test', 'os': 'darwin', 'via': 'hub-fixture', 'hostnames': ['fixture-via']},
        ]}))
        self.env = environment(self.local, PERSONA_CONFIG=str(config), PERSONA_HOME=str(self.local / 'do-not-copy-this-root'),
                               PERSONA_SKIP_HERDR='1', FIXTURE_REMOTE_HOME=str(self.remote))
        self.env['PATH'] = str(self.commands) + ':' + self.env['PATH']

    def run_script(self, relative, *args):
        result = subprocess.run(['bash', str(self.repo / relative), *args], env=self.env, capture_output=True, text=True, timeout=DEPLOY_TIMEOUT)
        self.assertEqual(result.returncode, 0, result.stdout + '\n' + result.stderr)
        return result

    def test_actual_hub_deploy_replaces_service_binary_and_web_in_its_installed_profile(self):
        self.run_script('scripts/deploy/deploy.sh', '--admin-access', 'lan', 'hub-fixture')
        # Every platform a node can run, the ones "Redeploy" takes a machine's build from (#3103).
        for name in ('personad', 'personad-wechat', 'nodes/personad-linux-amd64', 'nodes/personad-linux-arm64',
                     'nodes/personad-darwin-amd64', 'nodes/personad-darwin-arm64'):
            self.assertTrue((self.state / 'bin' / name).is_file(), name)
        self.assertEqual(os.readlink(self.state / 'bin' / (FORMER_NAME + 'd')), 'personad')
        self.assertEqual(os.readlink(self.state / 'bin' / (FORMER_NAME + 'd-wechat')), 'personad-wechat')
        self.assertEqual((self.state / 'web/index.html').read_text(), 'Persona fixture web')
        self.assertEqual(identity.data_dir(self.remote, identity.service_environment(self.remote, {})), str(self.state))
        self.assertFalse((self.remote / '.persona').exists())
        self.assertFalse((self.local / 'do-not-copy-this-root').exists())

    def test_node_deploy_uses_the_node_profile_not_the_colocated_hub(self):
        node = self.remote / 'separate node'
        unit = self.remote / '.config/systemd/user/personad-node.service'
        unit.write_text('[Service]\nEnvironment="PERSONA_HOME=' + str(node) + '"\n')
        self.run_script('scripts/deploy/deploy.sh', 'via-fixture')
        self.assertTrue((node / 'bin/personad').is_file())
        self.assertFalse((self.state / 'bin/personad').exists())
        self.assertEqual(identity.data_dir(self.remote, identity.service_environment(self.remote, {}, 'hub')), str(self.state))

    def test_node_deploy_over_nested_ssh_preserves_binary_stdin(self):
        self.run_script('scripts/deploy/deploy.sh', 'via-fixture')
        self.assertEqual((self.state / 'bin/personad').read_text(), '#!/bin/sh\nprintf "fixture-binary\\n"\n')
        self.assertFalse((self.state / 'bin/personad').is_symlink())
        self.assertEqual(os.readlink(self.state / 'bin' / (FORMER_NAME + 'd')), 'personad')

    def test_web_only_deploy_keeps_the_selected_remote_profile(self):
        self.run_script('scripts/deploy/deploy.sh', 'web')
        self.assertEqual((self.state / 'web/index.html').read_text(), 'Persona fixture web')
        self.assertFalse((self.remote / '.persona').exists())

    def test_a_build_that_cannot_run_there_fails_the_deploy_before_installed(self):
        # The #3103 symptom: the wrong platform's build was copied in and the deploy printed "installed" anyway. A
        # binary that cannot exec here stands in for it; the `version` check has to catch it before the hub restarts.
        executable(self.commands / 'go', '#!' + sys.executable + '\n' + r'''
import pathlib, sys
if sys.argv[1] == 'generate': sys.exit(0)
if sys.argv[1] == 'build':
    p = pathlib.Path(sys.argv[sys.argv.index('-o')+1]); p.parent.mkdir(parents=True, exist_ok=True)
    p.write_bytes(b'\x7fELF built for another platform'); p.chmod(0o755); sys.exit(0)
sys.exit(2)
''')
        result = subprocess.run(['bash', str(self.repo / 'scripts/deploy/deploy.sh'), '--admin-access', 'lan', 'hub-fixture'],
                                env=self.env, capture_output=True, text=True, timeout=DEPLOY_TIMEOUT)
        self.assertNotEqual(result.returncode, 0, result.stdout + '\n' + result.stderr)
        self.assertIn('does not run there', result.stderr)
        self.assertNotIn('hub restarted', result.stdout)

    def test_a_checkout_missing_master_commits_deploys_nothing(self):
        # merge-base --is-ancestor fails: HEAD does not contain origin/master (#1314)
        executable(self.commands / 'git', '#!/bin/sh\ncase "$1" in merge-base) exit 1;; describe) printf "fixture-version\\n";; rev-parse) printf "fixture-commit\\n";; esac\n')
        result = subprocess.run(['bash', str(self.repo / 'scripts/deploy/deploy.sh'), 'web'], env=self.env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn('missing commits on origin/master', result.stderr)
        self.assertFalse((self.state / 'web/index.html').exists())
        env = dict(self.env, PERSONA_DEPLOY_ANY_COMMIT='1')
        result = subprocess.run(['bash', str(self.repo / 'scripts/deploy/deploy.sh'), 'web'], env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.state / 'web/index.html').read_text(), 'Persona fixture web')

    def git(self, cwd, *args):
        result = subprocess.run([GIT, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.com', *args], cwd=cwd,
                                env=self.env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def clone_of(self, default, origin_head):
        """Make the fixture checkout a clone of a fresh bare remote whose default branch is `default`, run with the real
        git. origin_head: the branch the clone's refs/remotes/origin/HEAD names (one the remote may no longer have, as
        in a clone made before the remote's default changed), None for none (a remote added by hand)."""
        self.env['GIT_CONFIG_NOSYSTEM'] = '1'
        remote = self.base / 'origin.git'
        shutil.rmtree(remote, ignore_errors=True)
        shutil.rmtree(self.repo / '.git', ignore_errors=True)
        self.git(self.base, 'init', '-q', '--bare', '--initial-branch=' + default, str(remote))
        self.git(self.repo, 'init', '-q', '--initial-branch=' + default)
        self.git(self.repo, 'commit', '-q', '--allow-empty', '-m', 'fixture')  # the guard compares commits, not files
        self.git(self.repo, 'remote', 'add', 'origin', str(remote))
        self.git(self.repo, 'push', '-q', 'origin', default)
        if origin_head:
            self.git(self.repo, 'update-ref', 'refs/remotes/origin/' + origin_head, 'HEAD')
            self.git(self.repo, 'symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/' + origin_head)
        (self.commands / 'git').unlink(missing_ok=True)  # setUp's fake git

    def advance_remote(self, branch):
        """Someone else's commit lands on the remote's branch; the checkout does not have it."""
        later = self.git(self.repo, 'commit-tree', 'HEAD^{tree}', '-p', 'HEAD', '-m', 'later')
        self.git(self.repo, 'push', '-q', 'origin', later + ':refs/heads/' + branch)

    def assert_refused(self, message):
        """deploy.sh web exits 2 with `message` on stderr and deploys nothing."""
        result = subprocess.run(['bash', str(self.repo / 'scripts/deploy/deploy.sh'), 'web'], env=self.env,
                                capture_output=True, text=True, timeout=DEPLOY_TIMEOUT)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn(message, result.stderr)
        self.assertFalse((self.state / 'web/index.html').exists())

    def test_an_up_to_date_clone_of_a_main_repository_deploys(self):
        # The remote has main and no master (#1064).
        self.clone_of('main', 'main')
        self.run_script('scripts/deploy/deploy.sh', 'web')
        self.assertEqual((self.state / 'web/index.html').read_text(), 'Persona fixture web')

    def test_a_checkout_behind_the_remote_default_branch_deploys_nothing(self):
        for default, origin_head in (('main', 'main'), ('main', None), ('main', 'master'), ('master', 'master')):
            with self.subTest(default=default, origin_head=origin_head):
                self.clone_of(default, origin_head)
                self.advance_remote(default)
                self.assert_refused('missing commits on origin/' + default)

    def test_a_remote_that_names_no_valid_default_branch_falls_back_to_origin_head(self):
        # ls-remote names nothing, or a name git does not allow for a branch (':' would make the fetch a refspec).
        for listed in ('', 'ref: refs/heads/x:refs/heads/y\\tHEAD\\n'):
            with self.subTest(listed=listed):
                self.clone_of('main', 'main')
                self.advance_remote('main')
                executable(self.commands / 'git', '#!/bin/sh\n[ "$1" = ls-remote ] && { printf ' + shlex.quote(listed) +
                           '; exit 0; }\nexec ' + shlex.quote(GIT) + ' "$@"\n')
                self.assert_refused('missing commits on origin/main')

    def test_an_unreachable_remote_deploys_nothing_unless_any_commit_is_set(self):
        # ls-remote and fetch both fail (set -e, pipefail): the message names the clone's origin/HEAD, else master.
        for origin_head, branch in (('main', 'main'), (None, 'master')):
            with self.subTest(origin_head=origin_head):
                self.clone_of('main', origin_head)
                self.git(self.repo, 'remote', 'set-url', 'origin', str(self.base / 'missing.git'))
                self.assert_refused('could not fetch origin/' + branch + ' to ')
        # PERSONA_DEPLOY_ANY_COMMIT=1 skips the check, so it needs no remote.
        self.env['PERSONA_DEPLOY_ANY_COMMIT'] = '1'
        self.run_script('scripts/deploy/deploy.sh', 'web')
        self.assertEqual((self.state / 'web/index.html').read_text(), 'Persona fixture web')

    def test_a_default_branch_named_like_an_option_is_fetched_as_a_branch(self):
        # Passed bare, this name would be git fetch's --upload-pack and run the command on this machine.
        self.clone_of('main', 'main')
        executable(self.commands / 'fixture-upload-pack', '#!/bin/sh\ntouch "$HOME/upload-pack.fixture"\nexit 1\n')
        branch = 'refs/heads/--upload-pack=fixture-upload-pack'
        remote = '--git-dir=' + str(self.base / 'origin.git')
        self.git(self.base, remote, 'update-ref', branch, 'main')
        self.git(self.base, remote, 'symbolic-ref', 'HEAD', branch)
        self.run_script('scripts/deploy/deploy.sh', 'web')
        self.assertFalse((self.local / 'upload-pack.fixture').exists())
        self.assertEqual((self.state / 'web/index.html').read_text(), 'Persona fixture web')

    def test_android_apk_publish_uses_the_remote_profile_and_config_entrypoint(self):
        # This integration fixture tests identity resolution and binary transport;
        # provide SDK output for its synthetic APK while retaining production checks.
        executable(self.commands / 'aapt2', '#!' + sys.executable + '\n' + r'''
import argparse
from pathlib import Path
parser = argparse.ArgumentParser()
parser.add_argument('command', choices=['dump'])
parser.add_argument('format', choices=['badging'])
parser.add_argument('apk', type=Path)
args = parser.parse_args()
assert args.apk.read_bytes() == b'anonymous-signed-apk-fixture'
print("package: name='com.example.persona' versionCode='1' versionName='1.0'")
print("minSdkVersion:'26'")
''')
        executable(self.commands / 'apksigner', '#!' + sys.executable + '\n' + r'''
import argparse
from pathlib import Path
parser = argparse.ArgumentParser()
parser.add_argument('command', choices=['verify'])
parser.add_argument('--verbose', action='store_true', required=True)
parser.add_argument('--print-certs', action='store_true', required=True)
parser.add_argument('apk', type=Path)
args = parser.parse_args()
assert args.apk.read_bytes() == b'anonymous-signed-apk-fixture'
print('Number of signers: 1')
print('V3.0 Signer: certificate SHA-256 digest: ' + 'a' * 64)
''')
        apk = self.base / 'fixture.apk'; apk.write_bytes(b'anonymous-signed-apk-fixture')
        self.run_script('scripts/release/publish_android_apk.sh', str(apk), '--version-code', '1',
                        '--version-name', '1.0', '--min-sdk', '26',
                        '--expected-package', 'com.example.persona', '--expected-cert-sha256', 'a' * 64)
        manifest = json.loads((self.state / 'apps/android/latest.json').read_text())
        self.assertEqual(manifest['version_code'], 1)
        self.assertEqual(manifest['package_name'], 'com.example.persona')
        self.assertEqual(manifest['certificate_sha256'], 'a' * 64)
        self.assertEqual((self.state / 'apps/android' / (manifest['sha256'] + '.apk')).read_bytes(), apk.read_bytes())


if __name__ == '__main__':
    unittest.main()


class PrefixParsesInSystemBash(unittest.TestCase):
    """The --prefix preamble is piped to `ssh host 'bash -s'`; on a Mac hub that is /bin/bash 3.2, which broke on a
    heredoc inside $(...) ("unexpected EOF while looking for matching `")."""

    @unittest.skipUnless(os.path.exists('/bin/bash'), 'no /bin/bash')
    def test_prefix_parses_and_resolves(self):
        with tempfile.TemporaryDirectory() as home:
            env = environment(home)
            out = subprocess.run([sys.executable, str(SCRIPT), '--service', 'hub', '--prefix', '-'],
                                 check=True, capture_output=True, text=True, env=env).stdout
            subprocess.run(['/bin/bash', '-n'], input=out, check=True, text=True)
            got = subprocess.run(['/bin/bash', '-c', 'eval "$(cat)"; printf %s "$PERSONA_HOME"'], input=out,
                                 check=True, capture_output=True, text=True, env=env).stdout
            self.assertTrue(got.startswith(home), got)

