"""Offline regression for the original publisher's explicit manager transport."""
import datetime as dt
import ast
import io
import json
import hashlib
import shlex
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manager_target as target_module
import promote_manager_release as promoter

snapshot_impl = promoter.snapshot


def idle_row(session_id='main-fixture', machine_id='box'):
    return dict(session_id=session_id, machine_id=machine_id, manager='m-fixture',
                assistant=True, live=True, state='idle', status='idle', rev=1, starting=False,
                background=[], subagents=[], compaction=None, pending_interactions=0)


class TransportTests(unittest.TestCase):
    def test_cloudflare_fixed_proxy_preserves_strict_ssh_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()  # macOS: /var/folders is a link into /private/var
            key, hosts, executable = root / 'key', root / 'known_hosts', root / 'cloudflared client'
            for path in (key, hosts): path.write_text('fixture')
            executable.write_bytes(b'#!/bin/sh\nexit 0\n'); executable.chmod(0o700)
            cfg = dict(kind='cloudflare-ssh', host='ssh.example.invalid', port=22, user='fixture',
                       identity_file=str(key), known_hosts_file=str(hosts), cloudflared_path=str(executable),
                       cloudflared_sha256=hashlib.sha256(executable.read_bytes()).hexdigest())
            command = promoter.transport_argv(cfg)
            proxy = next(arg for arg in command if arg.startswith('ProxyCommand='))
            self.assertEqual(shlex.split(proxy.split('=', 1)[1]),
                             [str(executable), 'access', 'ssh', '--hostname', 'ssh.example.invalid'])
            for flag in ('StrictHostKeyChecking=yes', 'BatchMode=yes', 'IdentitiesOnly=yes',
                         'GlobalKnownHostsFile=/dev/null', 'UserKnownHostsFile=' + str(hosts)):
                self.assertIn(flag, command)
            self.assertEqual(command[command.index('-F') + 1], '/dev/null')
            self.assertEqual(command[command.index('-i') + 1], str(key))
            for changes in ({'proxy_command': 'anything'}, {'host': 'host%h'}, {'host': 'a;id'},
                            {'port': 2222}, {'cloudflared_sha256': '0' * 64},
                            {'cloudflared_path': str(root / 'missing')},
                            {'cloudflared_path': 'cloudflared'}, {'cloudflared_path': '/tmp/%h'}):
                with self.subTest(changes=changes), self.assertRaises((ValueError, FileNotFoundError)):
                    promoter.transport_argv(dict(cfg, **changes))
            link = root / 'link'; link.symlink_to(executable)
            with self.assertRaises(ValueError): promoter.transport_argv(dict(cfg, cloudflared_path=str(link)))
            executable.chmod(0o600)
            with self.assertRaises(ValueError): promoter.transport_argv(cfg)

    def test_missing_transport_cannot_infer_identity(self):
        for cfg in ({}, {'kind': 'link'}, {'kind': 'ssh', 'host': 'example.invalid'}):
            with self.subTest(cfg=cfg), self.assertRaises(ValueError):
                promoter.transport_argv(cfg)

    def test_explicit_pins_and_key_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()  # transport_argv refuses a path through a link
            key, hosts = root / 'key', root / 'known_hosts'
            key.write_text('fixture'); hosts.write_text('fixture')
            cfg = dict(kind='ssh', host='example.invalid', user='fixture', port=22,
                       identity_file=str(key), known_hosts_file=str(hosts))
            command = promoter.transport_argv(cfg)
            self.assertIn('StrictHostKeyChecking=yes', command)
            self.assertIn('BatchMode=yes', command)
            self.assertIn('IdentitiesOnly=yes', command)
            self.assertIn('GlobalKnownHostsFile=/dev/null', command)
            self.assertEqual(command[-1], 'example.invalid')
            for field, invalid in [('host', '-oProxyCommand=bad'), ('port', 0), ('user', 'a b')]:
                with self.subTest(field=field), self.assertRaises(ValueError):
                    promoter.transport_argv(dict(cfg, **{field: invalid}))

    def test_missing_pin_or_symlink_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); (root / 'key').write_text('fixture')
            (root / 'link').symlink_to(root / 'key')
            for hosts in [root / 'missing', root / 'link']:
                with self.assertRaises((ValueError, FileNotFoundError)):
                    promoter.transport_argv(dict(kind='ssh', host='example.invalid', user='fixture', port=22,
                                                  identity_file=str(root / 'key'), known_hosts_file=str(hosts)))

    def test_freeze_and_expired_window_reject_before_any_connection(self):
        now = dt.datetime.now(dt.timezone.utc)
        for cfg in ({}, {'freeze': True, 'activation_enabled': True},
                    {'freeze': False, 'activation_enabled': True, 'bootstrap_authorized': True,
                     'bootstrap_window_until': (now - dt.timedelta(seconds=1)).isoformat()}):
            with self.subTest(cfg=cfg), self.assertRaises((ValueError, KeyError)):
                promoter.validate_activation(cfg, now)

    def test_remote_argument_is_shell_quoted_and_errors_propagate(self):
        result = type('Result', (), {'returncode': 0, 'stdout': b'{"phase":"checked"}', 'stderr': b''})()
        with patch.object(promoter.subprocess, 'run', return_value=result) as run:
            self.assertEqual(promoter.remote(['ssh', 'example.invalid'], ['a b', '--plan', 'c d']),
                             {'phase': 'checked'})
            self.assertEqual(run.call_args.args[0][-1], "'a b' --plan 'c d'")
        result.returncode = 255; result.stderr = b'connection refused'
        with patch.object(promoter.subprocess, 'run', return_value=result), self.assertRaisesRegex(RuntimeError, 'connection refused'):
            promoter.remote(['ssh', 'example.invalid'], ['true'])


class ScopedHealthTests(unittest.TestCase):
    def setUp(self):
        self.target = dict(manager_id='m-fixture', main='main-fixture', machine_id='box')
        self.answers = {
            'managers': {'managers': [dict(id='m-fixture', online=True, deploy='service', version='release')]},
            'assistant?manager=m-fixture&archived=true': dict(manager='m-fixture', main='main-fixture', machine_id='box', conversations=[idle_row()], update_inventory=dict(schema=1, complete=True, revision='r1', observed_at=dt.datetime.now(dt.timezone.utc).isoformat())),
            'machines': {'machines': [dict(id='box', manager='m-fixture', online=True, machine_uid='uid-fixture')]},
            'sessions?machine_id=box': dict(sessions=[idle_row()], update_inventory=dict(schema=1, complete=True, revision='r1', observed_at=dt.datetime.now(dt.timezone.utc).isoformat())),
        }

    def snapshot(self):
        with patch.object(promoter, 'api', side_effect=lambda root, path: self.answers[path]):
            return promoter.snapshot(Path('/fixture'), self.target)

    def test_old_hub_missing_default_field_is_not_a_capability_probe(self):
        _, identity, snapshot_hash = self.snapshot()
        self.assertEqual(identity['main'], 'main-fixture')
        self.assertEqual(len(snapshot_hash), 64)
        self.assertNotIn('default_permission_mode', self.answers['assistant?manager=m-fixture&archived=true'])

    def test_wrong_scope_or_main_and_busy_or_unknown_refused(self):
        for change in [dict(manager='m-other'), dict(main='other-main')]:
            with self.subTest(change=change), patch.dict(self.answers['assistant?manager=m-fixture&archived=true'], change), self.assertRaises(ValueError):
                self.snapshot()
        row = self.answers['sessions?machine_id=box']['sessions'][0]
        for change in [dict(state='working'), dict(state='unknown'), dict(manager='m-other'),
                       dict(compaction={'state': 'running'})]:
            with self.subTest(change=change), patch.dict(row, change), self.assertRaises(ValueError):
                self.snapshot()

    def test_missing_main_or_offline_machine_refused(self):
        self.answers['assistant?manager=m-fixture&archived=true']['conversations'] = []
        with self.assertRaises(promoter.SnapshotPending): self.snapshot()
        self.answers['machines']['machines'][0]['online'] = False
        with self.assertRaises(promoter.SnapshotPending): self.snapshot()

    def test_reconnect_absence_is_transient_but_conflicting_identity_is_not(self):
        profile = self.answers['assistant?manager=m-fixture&archived=true']
        for field in ('main', 'machine_id'):
            value = profile.pop(field)
            with self.subTest(field=field), self.assertRaises(promoter.SnapshotPending):
                self.snapshot()
            profile[field] = value
        profile['main'] = 'foreign-main'
        with self.assertRaises(ValueError) as refused:
            self.snapshot()
        self.assertNotIsInstance(refused.exception, promoter.SnapshotPending)




class PublisherTransactionTests(unittest.TestCase):
    def setUp(self):
        import gzip
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / 'releases/hub').mkdir(parents=True)
        (self.root / 'releases/hub/.publish.lock').touch()
        (self.root / 'hub.json').write_text('{"auto_update_nodes":false}')
        self.stage = self.root / 'releases/hub/.upload-fixture'
        self.stage.mkdir()
        with gzip.open(self.stage / 'fixture.gz', 'wb') as stream: stream.write(b'candidate')
        self.target = dict(manager_id='m-fixture', main='main', machine_id='box', freeze=False,
                           dir='/fixture/manager', binary='/fixture/manager/bin/personad',
                           unit='com.persona.manager.m-fixture', plist_path='/fixture/manager.plist', uid=501,
                           previous_sha256='d' * 64, config_sha256='e' * 64, plist_sha256='f' * 64,
                           activation_enabled=True, bootstrap_authorized=True,
                           bootstrap_window_until=(dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=5)).isoformat())
        self.manifest = dict(commit='a' * 40, version='master-' + 'a' * 12 + '-202611080100',
                             files={'darwin-arm64': dict(name='fixture.gz', binary_sha256='b' * 64)})
        self.identity = dict(manager='m-fixture', main='main', machine_id='box', machines=[('box', 'uid')])
        self.payload = dict(root=str(self.root), target=self.target, run_id='run-fixture', apply=True,
                            source='a' * 40, staged=str(self.stage), target_source='')
        self.commands = []
        self.statuses = iter([{'phase': 'applied', 'ready': {'pid': 42}}, {'phase': 'confirmed'}])
        self.patches = [
            patch.object(promoter, 'transport_argv', return_value=['ssh', 'example.invalid']),
            patch.object(promoter, 'inspect_target', return_value={'phase': 'checked', 'manager_id': 'm-fixture'}),
            patch.object(promoter, 'snapshot', return_value=({'version': self.manifest['version']}, self.identity, 'c' * 64)),
            patch.object(promoter, 'nodes', return_value={'box': {'version': 'old', 'deploying': False}}),
            patch.object(promoter, 'manager_manifest', return_value=self.manifest),
            patch.object(promoter, 'verify_binary'), patch.object(promoter, 'time', wraps=promoter.time),
            patch.object(promoter, 'remote', side_effect=self.remote),
        ]
        for item in self.patches:
            item.start(); self.addCleanup(item.stop)
        promoter.time.sleep = lambda _: None

    def remote(self, argv, command, data=None, timeout=30):
        if command[:2] == ['python3', '-c']:
            self.commands.append('stage' if data is not None else 'refresh')
            return {'candidate': '/fixture/candidate', 'plan': '/fixture/plan.json'}
        action = command[3]
        self.commands.append(action)
        if action == 'status': return next(self.statuses)
        return {'phase': 'checked'}

    def test_full_old_manager_first_hop_and_two_phase_confirmation(self):
        receipt = promoter.promote(self.payload)
        self.assertEqual(receipt['phase'], 'confirmed')
        self.assertEqual(receipt['source'], 'a' * 40)
        self.assertEqual(self.commands, ['stage', 'check', 'refresh', 'start', 'status', 'confirm', 'status'])

    def test_transfer_over_one_minute_refreshes_idle_proof_before_start(self):
        initial = dt.datetime.now(dt.timezone.utc)
        clock = [initial]
        plans = {}
        actual_datetime = dt.datetime
        original_remote = self.remote

        class Clock(actual_datetime):
            @classmethod
            def now(cls, timezone=None):
                return clock[0]

        def remote(argv, command, data=None, timeout=30):
            if command[:2] == ['python3', '-c']:
                call = ast.parse(command[2]).body[-1].value.args[0].args[0]
                plans['initial' if data is not None else 'fresh'] = ast.literal_eval(call.args[1])
                if data is not None:
                    clock[0] += dt.timedelta(seconds=61)
            elif command[3] == 'start':
                fresh = plans['fresh']
                self.assertEqual(actual_datetime.fromisoformat(fresh['idle_verified_at']), clock[0])
                self.assertGreater(actual_datetime.fromisoformat(fresh['idle_valid_until']), clock[0])
                self.assertEqual(fresh['idle_snapshot_sha256'], 'd' * 64)
            return original_remote(argv, command, data, timeout)

        promoter.remote.side_effect = remote
        promoter.snapshot.side_effect = lambda *args, **kwargs: (
            {'version': self.manifest['version']}, self.identity,
            ('c' if clock[0] == initial else 'd') * 64)
        with patch.object(promoter.dt, 'datetime', Clock):
            receipt = promoter.promote(self.payload)
        self.assertEqual(receipt['phase'], 'confirmed')
        self.assertLess(actual_datetime.fromisoformat(plans['initial']['idle_valid_until']), clock[0])
        mutable = {'idle_verified_at', 'idle_valid_until', 'idle_snapshot_sha256'}
        self.assertEqual({k: v for k, v in plans['initial'].items() if k not in mutable},
                         {k: v for k, v in plans['fresh'].items() if k not in mutable})

    def test_read_only_check_does_not_stage_or_start(self):
        before = sorted(str(p) for p in self.root.rglob('*'))
        receipt = promoter.promote(dict(self.payload, apply=False))
        self.assertEqual(receipt['phase'], 'checked')
        self.assertEqual(self.commands, [])
        self.assertEqual(before, sorted(str(p) for p in self.root.rglob('*')))

    def test_hub_health_failure_requests_rollback_never_confirms(self):
        promoter.snapshot.side_effect = [({'version': self.manifest['version']}, self.identity, 'c' * 64)] * 3 + [
            ({'version': 'old'}, self.identity, 'c' * 64)]
        with self.assertRaisesRegex(RuntimeError, 'live version'):
            promoter.promote(self.payload)
        self.assertIn('rollback', self.commands)
        self.assertNotIn('confirm', self.commands)

    def test_ready_transient_reconnect_retries_then_confirms(self):
        healthy = ({'version': self.manifest['version']}, self.identity, 'c' * 64)
        promoter.snapshot.side_effect = [healthy] * 3 + [
            promoter.SnapshotPending('Target machines must be known and online'),
            promoter.SnapshotPending('Original main has not resynced'), healthy]
        self.statuses = iter([{'phase': 'applied', 'ready': {'pid': 42}}] * 3 + [{'phase': 'confirmed'}])
        self.assertEqual(promoter.promote(self.payload)['phase'], 'confirmed')
        self.assertEqual(promoter.snapshot.call_count, 6)
        self.assertNotIn('rollback', self.commands)
        self.assertEqual(self.commands.count('confirm'), 1)

    def test_ready_transient_reconnect_is_bounded_and_rolls_back(self):
        healthy = ({'version': self.manifest['version']}, self.identity, 'c' * 64)
        promoter.snapshot.side_effect = [healthy] * 3 + [promoter.SnapshotPending('still offline')]
        promoter.time.monotonic.side_effect = [0, 0, 1, 61]
        with self.assertRaisesRegex(TimeoutError, '60 seconds'):
            promoter.promote(self.payload)
        self.assertIn('rollback', self.commands)
        self.assertNotIn('confirm', self.commands)

    def test_ready_actual_snapshot_waits_for_offline_main_and_session_resync(self):
        # Exercise actual classification of Hub responses, not just a mocked
        # transient exception, across the authenticated reconnect boundary.
        snapshots = [0]

        def api(root, path):
            if path == 'managers':
                snapshots[0] += 1
                return {'managers': [dict(id='m-fixture', online=snapshots[0] != 7,
                                         deploy='service', version=self.manifest['version'])]}
            if path == 'assistant?manager=m-fixture&archived=true':
                return dict(manager='m-fixture', main='' if snapshots[0] == 8 else 'main', machine_id='box',
                            conversations=[] if snapshots[0] == 9 else [idle_row('main')],
                            update_inventory=dict(schema=1, complete=True, revision='r1', observed_at=dt.datetime.now(dt.timezone.utc).isoformat()))
            if path == 'machines':
                return {'machines': [dict(id='box', manager='m-fixture', online=True, machine_uid='uid')]}
            if path == 'sessions?machine_id=box':
                rows = [idle_row('main')]
                return dict(sessions=rows, update_inventory=dict(schema=1, complete=True, revision='r1', observed_at=dt.datetime.now(dt.timezone.utc).isoformat()))
            raise AssertionError(path)

        self.statuses = iter([{'phase': 'applied', 'ready': {'pid': 42}}] * 4 + [{'phase': 'confirmed'}])
        with patch.object(promoter, 'snapshot', wraps=snapshot_impl), patch.object(promoter, 'api', side_effect=api):
            self.assertEqual(promoter.promote(self.payload)['phase'], 'confirmed')
        self.assertEqual(snapshots[0], 11)
        self.assertNotIn('rollback', self.commands)

    def test_ready_explicit_identity_conflict_rolls_back_without_retry(self):
        healthy = ({'version': self.manifest['version']}, self.identity, 'c' * 64)
        promoter.snapshot.side_effect = [healthy] * 3 + [ValueError('identity conflicts')]
        with self.assertRaisesRegex(ValueError, 'identity conflicts'):
            promoter.promote(self.payload)
        self.assertEqual(promoter.snapshot.call_count, 4)
        self.assertIn('rollback', self.commands)
        self.assertNotIn('confirm', self.commands)

    def test_controller_rollback_never_reports_success(self):
        self.statuses = iter([{'phase': 'rollback', 'error': 'failed health'}])
        with self.assertRaisesRegex(RuntimeError, 'rolled back'):
            promoter.promote(self.payload)

    def test_original_publication_lock_excludes_manager_activation(self):
        import fcntl
        with (self.root / 'releases/hub/.publish.lock').open('r+') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError): promoter.promote(self.payload)
        self.assertEqual(self.commands, [])

    def test_frozen_plan_cannot_touch_transport(self):
        with self.assertRaisesRegex(ValueError, 'frozen'):
            promoter.promote(dict(self.payload, target=dict(self.target, freeze=True)))
        promoter.transport_argv.assert_not_called()


if __name__ == '__main__':
    unittest.main()
