import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

from persona_migration_fixtures import GO_MAIN, NODE_INSTALL, NODE_QUERY
from migration_go import fixture_toolchain

ROOT = Path(__file__).resolve().parents[2]
CONFIG = ROOT / 'scripts/persona_config.py'
INSTALL = ROOT / 'scripts/deploy/install.sh'
IDENTITY = ROOT / 'integrations/runtime-sdk/lib/identity.js'
RUNTIME = ROOT / 'integrations/runtime-sdk/lib/runtime.js'
PACKAGE = ROOT / 'integrations/runtime-sdk/lib/package.js'
FORMER_NAME = 'jar' + 'vis'
FORMER_PREFIX = FORMER_NAME.upper() + '_'


class PersonaMigrationEdgesTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='persona-migration-edges-')
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.base = Path(cls.temporary.name)
        cls.node = shutil.which('node')
        cls.go = shutil.which('go')
        cls.go_helper = None
        cls.downloads = cls.base / 'downloads'
        cls.downloads.mkdir()
        os_name = {'Darwin': 'darwin', 'Linux': 'linux'}.get(platform.system())
        arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64', 'AMD64': 'amd64'}.get(platform.machine())
        if not os_name or not arch:
            raise unittest.SkipTest('installer fixture requires a supported OS/architecture')
        name = f'personad-{os_name}-{arch}.gz'
        data = gzip.compress(b'#!/bin/sh\nif [ "$1" = version ]; then printf "fixture-version\\n"; fi\n', mtime=0)
        (cls.downloads / name).write_bytes(data)
        (cls.downloads / 'SHA256SUMS').write_text(hashlib.sha256(data).hexdigest() + '  ' + name + '\n')

    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix='home-', dir=self.base)
        self.addCleanup(directory.cleanup)
        self.home = Path(directory.name)

    def clean_env(self, home=None, **extra):
        env = {
            'HOME': str(home or self.home),
            'PATH': os.environ['PATH'],
            'TMPDIR': str(self.base),
            'PYTHONDONTWRITEBYTECODE': '1',
            'GOCACHE': str(self.base / 'go-cache'),
            'GOPATH': str(self.base / 'go-path'),
            'GOENV': 'off',
            'GOTOOLCHAIN': 'local',
            'GO111MODULE': 'off',
        }
        env.update(extra)
        return env

    def run_command(self, argv, env=None, timeout=20, cwd=None):
        return subprocess.run(argv, env=env or self.clean_env(), capture_output=True,
                              text=True, timeout=timeout, cwd=cwd)

    def install(self, home=None, **extra):
        env = self.clean_env(home, PERSONA_DL=self.downloads.as_uri(),
                             PERSONA_SKIP_HERDR='1', PERSONA_ROLE='hub',
                             PERSONA_ADMIN_ACCESS='lan')
        env.update(extra)
        return self.run_command(['sh', str(INSTALL)], env)

    def require_node(self):
        if not self.node:
            self.skipTest('node is not installed')

    def node_env(self, home=None, **extra):
        return self.clean_env(home, EDGE_IDENTITY=str(IDENTITY),
                              EDGE_RUNTIME=str(RUNTIME), EDGE_PACKAGE=str(PACKAGE), **extra)

    def node_query(self, home=None, **extra):
        self.require_node()
        result = self.run_command([self.node, '--input-type=module', '-e', NODE_QUERY],
                                  self.node_env(home, **extra))
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def ensure_go_helper(self):
        if not self.go:
            self.skipTest('go is not installed')
        cls = type(self)
        if cls.go_helper is not None:
            return cls.go_helper
        source = self.base / 'go-helper-source'
        source.mkdir(exist_ok=True)
        go, directives = fixture_toolchain(
            self.go, ROOT / "server", self.clean_env())
        (source / "go.mod").write_text(
            "module github.com/nwverse/persona/server\n\n" + directives)
        for package in ("compat", "nodeproto", "proto", "dialogwire"):
            shutil.copytree(ROOT / "server/internal" / package, source / "internal" / package,
                            ignore=shutil.ignore_patterns("*_test.go"), dirs_exist_ok=True)
        for name in ('paths.go', 'env.go'):
            text = (ROOT / 'server/internal/identity' / name).read_text()
            self.assertTrue(text.startswith('package identity\n'), name)
            (source / name).write_text(text.replace('package identity', 'package main', 1))
        (source / 'main.go').write_text(GO_MAIN)
        helper = self.base / 'go-helper'
        result = self.run_command([go, 'build', '-buildvcs=false', '-o', str(helper), '.'],
                                  self.clean_env(GO111MODULE='on', GOWORK='off'),
                                  timeout=120, cwd=source)
        self.assertEqual(result.returncode, 0, result.stderr)
        cls.go_helper = helper
        return helper

    def go_query(self, home=None, **extra):
        helper = self.ensure_go_helper()
        result = self.run_command([str(helper)], self.clean_env(home, EDGE_MODE='query', **extra))
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def assert_same_directory(self, first, second):
        self.assertTrue(os.path.samefile(first, second), f'different stores: {first} / {second}')

    def assert_former_alias(self, home, initial):
        """A former-name store is reached under both names; a current-name one gets no former-name alias (#1445)."""
        former = home / ('.' + FORMER_NAME)
        if initial == 'former':
            self.assert_same_directory(home / '.persona', former)
        else:
            self.assertFalse(os.path.lexists(former), f'former-name alias made for a current-name store: {former}')

    def write_profile(self, name):
        file = self.home / f'{name}.json'
        file.write_text(json.dumps({'app': {'bundle_id': f'com.example.{name}'},
                                    'hub': {'url': f'https://{name}.example.test'}}))
        return file

    def test_config_profile_switch_recomputes_derived_exports(self):
        a, b = self.write_profile('a'), self.write_profile('b')
        code = r'''
set -eu
export PERSONA_CONFIG="$EDGE_A"
eval "$("$EDGE_PYTHON" -I -B "$EDGE_CONFIG" env)"
export PERSONA_CONFIG="$EDGE_B"
eval "$("$EDGE_PYTHON" -I -B "$EDGE_CONFIG" env)"
printf '%s|%s\n' "$PERSONA_HUB_URL" "$PERSONA_BUNDLE_ID"
'''
        result = self.run_command(['sh', '-c', code], self.clean_env(
            EDGE_A=str(a), EDGE_B=str(b), EDGE_PYTHON=sys.executable, EDGE_CONFIG=str(CONFIG)))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(),
                         'https://b.example.test|com.example.b')

    def test_current_config_export_refreshes_former_alias(self):
        a, b = self.write_profile('a'), self.write_profile('b')
        code = r'''
set -eu
export PERSONA_CONFIG="$EDGE_A"
eval "$("$EDGE_PYTHON" -I -B "$EDGE_CONFIG" env)"
[ "${PERSONA_CONFIG}" = "$EDGE_A" ]
[ "$(printenv "${EDGE_FORMER_PREFIX}CONFIG")" = "$PERSONA_CONFIG" ]
export PERSONA_CONFIG="$EDGE_B"
eval "$("$EDGE_PYTHON" -I -B "$EDGE_CONFIG" env)"
[ "${PERSONA_CONFIG}" = "$EDGE_B" ]
[ "$(printenv "${EDGE_FORMER_PREFIX}CONFIG")" = "$PERSONA_CONFIG" ]
printf '%s|%s\n' "$PERSONA_HUB_URL" "$PERSONA_BUNDLE_ID"
'''
        result = self.run_command(['sh', '-c', code], self.clean_env(
            EDGE_A=str(a), EDGE_B=str(b), EDGE_PYTHON=sys.executable, EDGE_CONFIG=str(CONFIG),
            EDGE_FORMER_PREFIX=FORMER_PREFIX))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), 'https://b.example.test|com.example.b')

    def test_environment_export_bridges_aliases_with_canonical_precedence(self):
        config = self.write_profile('a')
        code = r'''
set -eu
exports=$("$EDGE_PYTHON" -I -B "$EDGE_CONFIG" env)
! printf '%s' "$exports" | grep -q "private-fixture-value"
eval "$exports"
printf '%s|%s\n' "${PERSONA_RUNTIME_SOCKET}" "${PERSONA_ASSISTANT}"
[ "$PERSONA_FORMER_ONLY" = "private-fixture-value" ]
[ "$(printenv "${EDGE_FORMER_PREFIX}RUNTIME_SOCKET")" = "" ]
[ "$(printenv "${EDGE_FORMER_PREFIX}ASSISTANT")" = "1" ]
'''
        result = self.run_command(['sh', '-c', code], self.clean_env(
            PERSONA_CONFIG=str(config), PERSONA_RUNTIME_SOCKET='', PERSONA_ASSISTANT='1',
            EDGE_PYTHON=sys.executable, EDGE_CONFIG=str(CONFIG), EDGE_FORMER_PREFIX=FORMER_PREFIX,
            **{FORMER_PREFIX + 'RUNTIME_SOCKET': 'ignored.sock', FORMER_PREFIX + 'ASSISTANT': '0',
               FORMER_PREFIX + 'FORMER_ONLY': 'private-fixture-value'}))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), '|1')

    def test_external_alias_dotdot_installs_in_the_runtime_lexical_root(self):
        state = self.home / 'storage/state'
        state.mkdir(parents=True)
        payload = state / 'hub.json'
        payload.write_text('{"fixture":"unchanged"}')
        (self.home / '.persona').symlink_to('storage/state')
        (self.home / ('.' + FORMER_NAME)).symlink_to('storage/state')
        override = str(self.home / '.persona') + '/../profile'
        result = self.install(PERSONA_HOME=override)
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = self.home / 'profile'
        self.assertTrue((expected / 'bin/personad').is_file())
        self.assert_same_directory(expected / 'bin/personad', expected / 'bin' / (FORMER_NAME + 'd'))
        self.assertFalse((self.home / 'storage/profile').exists())
        self.assertEqual(payload.read_text(), '{"fixture":"unchanged"}')
        self.assertEqual(self.go_query(PERSONA_HOME=override)['root'], str(expected))
        self.assertEqual(self.node_query(PERSONA_HOME=override)['root'], str(expected))
        self.assert_same_directory(self.home / '.persona', state)

    def test_default_root_spelling_creates_shared_compatibility_alias(self):
        for spelling in ('absolute-slash', 'absolute-dot', 'tilde-slash', 'relative-dot', 'dotdot'):
            with self.subTest(spelling=spelling), tempfile.TemporaryDirectory(dir=self.base) as directory:
                home = Path(directory)
                override = {
                    'absolute-slash': str(home / '.persona') + '/',
                    'absolute-dot': str(home / '.persona') + '/.',
                    'tilde-slash': '~/.persona/',
                    'relative-dot': '.persona/.',
                    'dotdot': str(home / '.persona') + '/../.persona/',
                }[spelling]
                result = self.install(home, PERSONA_HOME=override)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue((home / '.persona').is_dir())
                self.assertFalse((home / '.persona').is_symlink())
                self.assert_same_directory(home / '.persona', home / ('.' + FORMER_NAME))
                self.assertTrue((home / '.persona/bin/personad').is_file())
                self.assertEqual(Path(self.go_query(home, PERSONA_HOME=override)['root']), home / '.persona')
                self.assert_same_directory(self.node_query(home, PERSONA_HOME=override)['root'], home / '.persona')

    def test_nested_dangling_profile_does_not_touch_default_state(self):
        for target in ('.persona', '.' + FORMER_NAME):
            with self.subTest(target=target), tempfile.TemporaryDirectory(dir=self.base) as directory:
                home = Path(directory)
                (home / 'profiles').mkdir()
                active = home / 'profiles/active'
                active.symlink_to(target)
                result = self.install(home, PERSONA_HOME=str(active))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('symlink', result.stderr)
                self.assertEqual(os.readlink(active), target)
                self.assertFalse(os.path.lexists(home / '.persona'))
                self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))
                self.assertFalse(os.path.lexists(home / 'profiles' / target))

    def test_explicit_empty_home_ignores_the_former_profile(self):
        old = self.home / 'old-profile'
        env = {'PERSONA_HOME': '', FORMER_PREFIX + 'HOME': str(old)}
        result = self.install(**env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(old.exists())
        self.assert_same_directory(self.home / '.persona', self.home / ('.' + FORMER_NAME))
        self.assertEqual(self.go_query(**env)['root'], str(self.home / '.persona'))
        self.assert_same_directory(self.go_query(**env)['root'], self.node_query(**env)['root'])

    def test_socket_queries_and_daemon_construction_do_not_create_directories(self):
        for mode in ('default', 'profile', 'explicit-socket', 'daemon'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory(dir=self.base) as directory:
                home = Path(directory)
                extra = {}
                if mode == 'profile':
                    extra['PERSONA_HOME'] = str(home / 'profile')
                elif mode == 'explicit-socket':
                    extra['PERSONA_RUNTIME_SOCKET'] = str(home / 'socket-only/runtime.sock')
                elif mode == 'daemon':
                    extra['EDGE_DAEMON'] = '1'
                query = self.node_query(home, **extra)
                self.assertIsInstance(query['root'], str)
                self.assertEqual(list(home.iterdir()), [])

    def concurrent_install(self, home, include_python=False):
        helper = self.ensure_go_helper()
        self.require_node()
        gate = home / 'start.gate'
        go_ready, node_ready = home / 'go.ready', home / 'node.ready'
        go_env = self.clean_env(home, EDGE_MODE='write', EDGE_GATE=str(gate), EDGE_READY=str(go_ready))
        node_env = self.node_env(home, EDGE_GATE=str(gate), EDGE_READY=str(node_ready))
        processes = []
        try:
            processes.append(subprocess.Popen([str(helper)], env=go_env, stdout=subprocess.PIPE,
                                              stderr=subprocess.PIPE, text=True))
            processes.append(subprocess.Popen([self.node, '--input-type=module', '-e', NODE_INSTALL],
                                              env=node_env, stdout=subprocess.PIPE,
                                              stderr=subprocess.PIPE, text=True))
            ready = [go_ready, node_ready]
            if include_python:
                python_ready = home / 'python.ready'
                code = r'''
import importlib.util, json, os, pathlib, time
spec = importlib.util.spec_from_file_location('identity', os.environ['EDGE_PYTHON_IDENTITY'])
identity = importlib.util.module_from_spec(spec); spec.loader.exec_module(identity)
cached = identity.data_dir()
pathlib.Path(os.environ['EDGE_READY']).write_text('ready')
deadline = time.monotonic() + 10
while not pathlib.Path(os.environ['EDGE_GATE']).exists():
    if time.monotonic() > deadline: raise RuntimeError('gate timeout')
    time.sleep(.001)
try:
    root = identity.prepare_dir()
    pathlib.Path(root, 'python-state.fixture').write_text('python-payload')
    print(json.dumps({'cached': cached, 'root': root, 'query': identity.data_dir()}))
except OSError as error:
    print(json.dumps({'cached': cached, 'query': identity.data_dir(), 'error': str(error)}))
'''
                processes.append(subprocess.Popen([sys.executable, '-I', '-c', code],
                    env=self.clean_env(home, EDGE_PYTHON_IDENTITY=str(ROOT / 'scripts/persona_identity.py'),
                                       EDGE_GATE=str(gate), EDGE_READY=str(python_ready)),
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True))
                ready.append(python_ready)
            deadline = time.monotonic() + 15
            while not all(path.exists() for path in ready):
                self.assertTrue(all(p.poll() is None for p in processes), 'initializer exited before gate')
                self.assertLess(time.monotonic(), deadline, 'initializer readiness timeout')
                time.sleep(0.005)
            gate.write_text('start')
            outputs = []
            for process in processes:
                stdout, stderr = process.communicate(timeout=15)
                self.assertEqual(process.returncode, 0, stderr)
                outputs.append(json.loads(stdout))
            return outputs
        finally:
            for process in processes:
                if process.poll() is None:
                    process.kill()
                process.communicate(timeout=5)

    def test_actual_go_and_sdk_package_initialization_do_not_split_state(self):
        for initial in ('fresh', 'current', 'former'):
            for trial in range(12):
                with self.subTest(initial=initial, trial=trial), tempfile.TemporaryDirectory(dir=self.base) as directory:
                    home = Path(directory)
                    seed = None
                    if initial != 'fresh':
                        seed = home / ('.persona' if initial == 'current' else '.' + FORMER_NAME)
                        seed.mkdir()
                        (seed / 'existing.fixture').write_text('existing-payload')
                    go_result, sdk_result = self.concurrent_install(home)
                    for result in (go_result, sdk_result):
                        self.assertNotIn('error', result)
                        for key in ('cached', 'root', 'query'):
                            self.assert_same_directory(result[key], home / '.persona')
                    self.assert_same_directory(go_result['root'], sdk_result['root'])
                    self.assertEqual((Path(go_result['root']) / 'go-state.fixture').read_text(), 'go-payload')
                    installed = Path(sdk_result['installed'])
                    self.assertEqual((installed / 'pi/index.js').read_bytes(), b'export const fixture = true;\n')
                    self.assert_same_directory(installed, installed.parent / 'current')
                    self.assertEqual((home / '.persona').is_symlink(), initial == 'former')
                    if seed is not None:
                        self.assertEqual((seed / 'existing.fixture').read_text(), 'existing-payload')
                    self.assert_former_alias(home, initial)
                    if initial != 'fresh':
                        self.assertEqual((home / '.persona/existing.fixture').read_text(), 'existing-payload')

    def test_actual_go_sdk_and_script_initializers_share_one_store(self):
        for initial in ('fresh', 'current', 'former'):
            for trial in range(6):
                with self.subTest(initial=initial, trial=trial), tempfile.TemporaryDirectory(dir=self.base) as directory:
                    home = Path(directory)
                    if initial != 'fresh':
                        seed = home / ('.persona' if initial == 'current' else '.' + FORMER_NAME)
                        seed.mkdir()
                        (seed / 'existing.fixture').write_text('existing-payload')
                    results = self.concurrent_install(home, include_python=True)
                    self.assertEqual(len(results), 3)
                    self.assertEqual((home / '.persona').is_symlink(), initial == 'former')
                    for result in results:
                        self.assertNotIn('error', result)
                        for key in ('cached', 'root', 'query'):
                            self.assert_same_directory(result[key], home / '.persona')
                    self.assertEqual((home / '.persona/python-state.fixture').read_text(), 'python-payload')
                    if initial != 'fresh':
                        self.assertEqual((seed / 'existing.fixture').read_text(), 'existing-payload')
                    self.assert_former_alias(home, initial)
                    if initial != 'fresh':
                        self.assertEqual((home / '.persona/existing.fixture').read_text(), 'existing-payload')

    def test_dot_prefixed_dangling_canonical_root_is_not_restored(self):
        self.require_node()
        helper = self.ensure_go_helper()
        for implementation in ('go', 'sdk', 'installer'):
            for target in ('./.persona', './missing'):
                with self.subTest(implementation=implementation, target=target), tempfile.TemporaryDirectory(dir=self.base) as directory:
                    home = Path(directory)
                    current = home / '.persona'
                    current.symlink_to(target)
                    if implementation == 'go':
                        result = self.run_command([str(helper)], self.clean_env(home))
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn('error', json.loads(result.stdout))
                    elif implementation == 'sdk':
                        code = r'''
import { pathToFileURL } from 'node:url';
const { prepareStateDir } = await import(pathToFileURL(process.env.EDGE_IDENTITY));
try { prepareStateDir(); process.exitCode = 2; } catch (err) {
  if (!String(err).includes('symlink')) throw err;
}
'''
                        result = self.run_command([self.node, '--input-type=module', '-e', code], self.node_env(home))
                        self.assertEqual(result.returncode, 0, result.stderr)
                    else:
                        result = self.install(home)
                        self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(os.readlink(current), target)
                    self.assertFalse((home / 'missing').exists())
                    self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))

    def test_dot_prefixed_external_dangling_target_remains_rejected(self):
        self.require_node()
        helper = self.ensure_go_helper()
        for implementation in ('go', 'sdk', 'installer'):
            for alias in ('.persona',):
                with self.subTest(implementation=implementation, alias=alias), tempfile.TemporaryDirectory(dir=self.base) as directory:
                    home = Path(directory)
                    (home / alias).symlink_to('./missing-external')
                    if implementation == 'go':
                        result = self.run_command([str(helper)], self.clean_env(home))
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn('error', json.loads(result.stdout))
                    elif implementation == 'sdk':
                        code = r'''
import { pathToFileURL } from 'node:url';
const { prepareStateDir } = await import(pathToFileURL(process.env.EDGE_IDENTITY));
try { prepareStateDir(); process.exitCode = 2; } catch (err) {
  if (!String(err).includes('unavailable symlink target')) throw err;
}
'''
                        result = self.run_command([self.node, '--input-type=module', '-e', code], self.node_env(home))
                        self.assertEqual(result.returncode, 0, result.stderr)
                    else:
                        result = self.install(home)
                        self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(os.readlink(home / alias), './missing-external')
                    self.assertFalse((home / 'missing-external').exists())
                    self.assertFalse(os.path.lexists(home / ('.' + FORMER_NAME)))

    def test_existing_distinct_roots_are_not_merged_or_deleted(self):
        for name in ('.persona', '.' + FORMER_NAME):
            (self.home / name).mkdir()
            (self.home / name / 'existing.fixture').write_text(name)
        helper = self.ensure_go_helper()
        result = self.run_command([str(helper)], self.clean_env())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('independent state directories', json.loads(result.stdout)['error'])
        self.require_node()
        result = self.run_command([self.node, '--input-type=module', '-e', NODE_QUERY], self.node_env())
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('independent state directories', result.stderr)
        for name in ('.persona', '.' + FORMER_NAME):
            root = self.home / name
            self.assertEqual((root / 'existing.fixture').read_text(), name)
            self.assertEqual(list(root.iterdir()), [root / 'existing.fixture'])
        self.assertFalse(os.path.samefile(self.home / '.persona', self.home / ('.' + FORMER_NAME)))



if __name__ == '__main__':
    unittest.main()
