import copy
import gzip
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

import pinned_node_release as worker
import promote_node_release as promoter
from test_node_artifacts import release, native_result


TARGET = dict(machine_id='work-node', manager_id='m-example', machine_uid='example-uid',
              executable='/opt/persona/bin/personad', os='darwin', arch='arm64')


class PinnedNodeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'runtime'
        self.root.mkdir()
        (self.root / 'hub.json').write_text(json.dumps({'auto_update_nodes': False}))
        self.staged = self.root / 'releases/hub/.upload-node-test'
        self.manifest = release(self.staged)
        (self.staged / 'node-manifest.json').rename(self.staged / 'manager-manifest.json')
        latest = copy.deepcopy(self.manifest)
        latest['files'] = {'linux-amd64': latest['files']['linux-amd64']}
        (self.staged / 'latest.json').write_text(json.dumps(latest))
        (self.staged / 'worker-receipt.json').write_text(json.dumps(dict(
            sha=self.manifest['commit'], build=self.manifest['build'], scope='manager', result='validated')))
        self.rows = [dict(id='work-node', machine_uid='example-uid', node_version='old'),
                     dict(id='another-node', node_version='untouched')]
        self.calls = []
        self.running = 'old'
        self.transferred = bytearray()
        self.info = dict(version='old', capabilities=['node_update', 'manager_update_inventory_v1'])
        artifact = self.manifest['files']['darwin-arm64']
        self.candidate = dict(source=self.manifest['commit'], version=self.manifest['version'],
                              binary_sha256=artifact['binary_sha256'],
                              binary_size=len(gzip.decompress((self.staged / artifact['name']).read_bytes())))
        self.payload = dict(candidate=self.candidate, root=str(self.root), staged=str(self.staged), target=TARGET,
                            source=self.manifest['commit'], run_id='run-one', target_identity='f' * 64)
        self.bad_offset = False
        self.commit_error = None
        self.confirm_error = None

    def relay(self, root, target, action, params=None):
        self.assertEqual(root, self.root)
        self.assertEqual(target, TARGET)
        self.calls.append(action)
        if action == 'info':
            return dict(self.info, version=self.running)
        if action == 'sessions':
            return {'sessions': []}
        if action == 'begin':
            artifact = self.manifest['files']['darwin-arm64']
            self.assertEqual(params['sha256'], artifact['binary_sha256'])
            self.assertEqual(params['version'], self.manifest['version'])
            return {'id': 'tx', 'offset': 0}
        if action == 'chunk':
            import base64
            self.assertEqual(params['id'], 'tx')
            self.assertEqual(params['offset'], len(self.transferred))
            self.transferred += base64.b64decode(params['data'])
            return {'offset': len(self.transferred) + int(self.bad_offset)}
        if action == 'commit':
            if self.commit_error:
                raise self.commit_error
            self.running = self.manifest['version']
            return {'accepted': True}
        if action == 'confirm':
            if self.confirm_error:
                raise self.confirm_error
            return {'ok': True}
        if action == 'abort':
            return {}
        raise AssertionError(action)

    def promote(self, apply=False):
        with patch.object(promoter, 'machine_inventory', side_effect=lambda root: copy.deepcopy(self.rows)), \
                patch.object(promoter, 'relay', side_effect=self.relay), \
                patch('node_artifacts.subprocess.check_output', return_value=native_result(self.manifest)):
            return promoter.promote(dict(self.payload, apply=apply))

    def test_fixed_manager_artifacts_reused_without_node_manifest_or_build(self):
        self.assertEqual(worker.validated_artifact(self.staged, self.manifest['commit']), self.manifest)
        self.assertFalse((self.staged / 'node-manifest.json').exists())

    def test_default_check_never_installs_or_changes_global_pointers(self):
        result = self.promote()
        self.assertEqual(result['phase'], 'checked')
        self.assertEqual(self.calls, ['info'])
        self.assertFalse((self.root / 'bin/nodes').exists())
        self.assertFalse((self.root / 'releases/nodes').exists())

    def test_apply_single_target_exact_bytes_confirmed_no_pointer_or_hub_activation(self):
        result = self.promote(True)
        self.assertEqual(result['phase'], 'confirmed')
        self.assertEqual(self.calls[-1], 'confirm')
        artifact = self.manifest['files']['darwin-arm64']
        self.assertEqual(hashlib.sha256(self.transferred).hexdigest(), artifact['binary_sha256'])
        self.assertFalse((self.root / 'bin/nodes').exists())
        self.assertFalse((self.root / 'releases/hub/latest.json').exists())
        self.assertEqual(self.rows[1]['node_version'], 'untouched')

    def test_shape_unknown_and_incomplete_target_rejected(self):
        for value in ({}, dict(TARGET, machine_id='../another'), dict(TARGET, command='exec'),
                      dict(TARGET, executable='relative'), dict(TARGET, arch='unknown')):
            with self.subTest(value=value), self.assertRaises(ValueError):
                worker.validate_target(value)

    def test_manifest_source_mismatch_and_tamper_rejected(self):
        with self.assertRaises(ValueError):
            worker.validated_artifact(self.staged, 'b' * 40)
        name = self.manifest['files']['darwin-arm64']['name']
        (self.staged / name).write_bytes(b'corrupt')
        with self.assertRaises(ValueError):
            worker.validated_artifact(self.staged, self.manifest['commit'])

    def test_prior_checked_or_failure_receipt_is_not_validation(self):
        for result in ('checked', 'failure', 'pending'):
            (self.staged / 'worker-receipt.json').write_text(json.dumps(dict(
                sha=self.manifest['commit'], build=self.manifest['build'], scope='manager', result=result)))
            with self.subTest(result=result), self.assertRaises(ValueError):
                worker.validated_artifact(self.staged, self.manifest['commit'])

    def test_uid_changed_refuses_before_node_rpc(self):
        self.rows[0]['machine_uid'] = 'different'
        with self.assertRaisesRegex(ValueError, 'identity'):
            self.promote(True)
        self.assertEqual(self.calls, [])

    def test_auto_fleet_policy_or_existing_deploy_refuses(self):
        (self.root / 'hub.json').write_text('{"auto_update_nodes":true}')
        with self.assertRaisesRegex(ValueError, 'fleet'):
            self.promote(True)
        (self.root / 'hub.json').write_text('{"auto_update_nodes":false}')
        self.rows[1]['deploying'] = True
        with self.assertRaisesRegex(ValueError, 'window'):
            self.promote(True)
        self.assertEqual(self.calls, [])

    def test_bad_chunk_aborts_and_never_commits(self):
        self.bad_offset = True
        with self.assertRaisesRegex(ValueError, 'offset'):
            self.promote(True)
        self.assertEqual(self.calls[-1], 'abort')
        self.assertNotIn('commit', self.calls)

    def test_rejected_commit_is_not_lost_reply_or_success(self):
        self.commit_error = urllib.error.HTTPError('http://localhost', 409, 'busy', {}, None)
        with self.assertRaises(urllib.error.HTTPError):
            self.promote(True)
        self.assertEqual(self.calls[-1], 'abort')
        self.assertNotIn('confirm', self.calls)

    def test_missing_producer_capability_never_confirms(self):
        self.info['capabilities'] = ['node_update']
        with self.assertRaisesRegex(ValueError, 'producer'):
            self.promote(True)
        self.assertNotIn('confirm', self.calls)

    def test_same_reported_version_does_not_fabricate_native_transfer_proof(self):
        self.running = self.manifest['version']
        with self.assertRaisesRegex(ValueError, 'native hash transfer proof'):
            self.promote(True)
        self.assertNotIn('begin', self.calls)
        self.assertNotIn('confirm', self.calls)

    def test_failed_confirmation_is_not_success(self):
        self.confirm_error = RuntimeError('guardian rejected confirmation')
        with self.assertRaisesRegex(RuntimeError, 'guardian'):
            self.promote(True)

    def test_original_publication_lock_is_respected(self):
        import fcntl
        lock = self.root / 'releases/hub/.publish.lock'
        with lock.open('w') as held:
            fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                self.promote(True)
        self.assertEqual(self.calls, [])

    def test_transient_reconnect_is_bounded_and_reads_again(self):
        original = self.relay
        attempt = 0
        def reconnect(root, target, action, params=None):
            nonlocal attempt
            if action == 'info' and 'commit' in self.calls and not attempt:
                attempt += 1
                raise urllib.error.HTTPError('http://localhost', 503, 'offline', {}, None)
            return original(root, target, action, params)
        with patch.object(promoter, 'machine_inventory', return_value=self.rows), \
                patch.object(promoter, 'relay', side_effect=reconnect), \
                patch.object(promoter.time, 'sleep'), \
                patch('node_artifacts.subprocess.check_output', return_value=native_result(self.manifest)):
            self.assertEqual(promoter.promote(dict(self.payload, apply=True))['phase'], 'confirmed')
        self.assertEqual(attempt, 1)


class PinnedNodeWorkerTests(unittest.TestCase):
    setUp = PinnedNodeTests.setUp
    def invoke_worker(self, apply=False, failure=None, retry=False):
        import argparse
        config = self.root / 'publisher.json'
        config.write_text(json.dumps({'node_target': TARGET, 'candidate': self.candidate, 'remote_root': str(self.root)}))
        self.state = self.root / 'worker'
        cfg = dict(checkout=str(self.root), state_dir=str(self.state), repository='example/project',
                   node_targets={'work-node': {'publisher_config': str(config)}})
        args = argparse.Namespace(commit=self.manifest['commit'], node_id='work-node',
                                  artifact_dir=self.staged, manager_id=None, validate_only=not apply, retry=retry)
        calls = []
        def run(command, **kwargs):
            calls.append(command)
            if 'publish_node_release.py' in command[1]:
                if failure:
                    raise failure
                receipt = Path(command[command.index('--receipt') + 1])
                receipt.write_text(json.dumps(dict(run_id=command[command.index('--run-id') + 1],
                    source=args.commit, target_identity=command[command.index('--config-sha256') + 1],
                    phase='confirmed' if '--apply' in command else 'checked')))
        with patch.object(worker.subprocess, 'run', side_effect=run), \
                patch.object(worker.subprocess, 'check_output', return_value='https://github.com/example/project.git'):
            worker.run_pinned_node(args, cfg)
        return calls, json.loads((self.state / 'node/work-node' / ('last.json' if apply else 'last-check.json')).read_text())

    def test_worker_preserves_original_artifact_and_never_builds_or_checks_out_old_source(self):
        original = {path.name: path.read_bytes() for path in self.staged.iterdir()}
        calls, result = self.invoke_worker()
        self.assertEqual(result['phase'], 'checked')
        self.assertFalse(any('build_hub_release.py' in str(call) or 'checkout' in call for call in calls))
        self.assertNotIn('--apply', calls[-1])
        self.assertEqual(original, {path.name: path.read_bytes() for path in self.staged.iterdir()})

    def test_worker_apply_requires_confirmed_receipt(self):
        calls, result = self.invoke_worker(True)
        self.assertIn('--apply', calls[-1])
        self.assertEqual(result['phase'], 'confirmed')

    def test_worker_publisher_failure_persists_failed_bound_receipt(self):
        import subprocess
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke_worker(True, subprocess.CalledProcessError(1, ['publisher']))
        result = json.loads((self.state / 'node/work-node/last.json').read_text())
        self.assertEqual(result['phase'], 'failure')
        self.assertEqual(result['source'], self.manifest['commit'])
        self.assertTrue(result['run_id'])

    def test_failure_is_retained_and_check_cannot_authorize_an_automatic_retry(self):
        import subprocess
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke_worker(True, subprocess.CalledProcessError(1, ['publisher']))
        original = (self.state / 'node/work-node/last.json').read_bytes()
        self.invoke_worker(False)
        self.assertEqual((self.state / 'node/work-node/last.json').read_bytes(), original)
        with self.assertRaisesRegex(ValueError, 'explicit --retry'):
            self.invoke_worker(True)
        self.assertEqual((self.state / 'node/work-node/last.json').read_bytes(), original)
        self.assertEqual(self.invoke_worker(True, retry=True)[1]['phase'], 'confirmed')

    def test_configured_node_scope_defaults_to_check(self):
        import hub_release
        config = self.root / 'worker.json'
        config.write_text(json.dumps(dict(scope='node', node_id='work-node', node_commit=self.manifest['commit'],
                                          node_artifact_dir=str(self.staged))))
        for extra, check in (({}, True), ({'node_apply': True}, False)):
            current = json.loads(config.read_text())
            current.update(extra)
            config.write_text(json.dumps(current))
            with patch('sys.argv', ['worker', '--config', str(config)]), \
                    patch.object(worker, 'run_pinned_node') as run:
                hub_release.main()
            args = run.call_args.args[0]
            self.assertEqual(args.validate_only, check)
            self.assertEqual(args.node_id, 'work-node')
            self.assertEqual(args.commit, self.manifest['commit'])
        with patch('sys.argv', ['worker', '--config', str(config), '--validate-only']), \
                patch.object(worker, 'run_pinned_node') as run:
            hub_release.main()
        self.assertTrue(run.call_args.args[0].validate_only)


class CandidatePinTests(unittest.TestCase):
    setUp = PinnedNodeTests.setUp
    def test_candidate_hash_size_source_and_version_are_mandatory(self):
        for pin in (None, {}, dict(self.candidate, source='b' * 40),
                    dict(self.candidate, version='other'), dict(self.candidate, binary_sha256='b' * 64),
                    dict(self.candidate, binary_size=self.candidate['binary_size'] + 1)):
            with self.subTest(pin=pin), self.assertRaises(ValueError):
                worker.validate_candidate(self.staged, self.manifest, TARGET, pin)
