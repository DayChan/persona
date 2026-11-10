"""Manager releases stay pinned and isolated inside the existing serialized worker."""
import copy
import gzip
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import hub_release as worker

SHA = 'a' * 40


def artifacts(directory, sha, build):
    manifest = dict(version=f'master-{sha[:12]}-{build}', build=int(build), commit=sha, files={})
    for platform in ('linux-amd64', 'darwin-arm64'):
        binary = platform.encode()
        compressed = gzip.compress(binary)
        name = f'personad-{platform}.gz'
        (directory / name).write_bytes(compressed)
        manifest['files'][platform] = dict(name=name, size=len(compressed),
            sha256=hashlib.sha256(compressed).hexdigest(),
            binary_sha256=hashlib.sha256(binary).hexdigest())
    (directory / 'manager-manifest.json').write_text(json.dumps(manifest))
    latest = copy.deepcopy(manifest)
    latest['files'].pop('darwin-arm64')
    (directory / 'latest.json').write_text(json.dumps(latest))
    return manifest


class ManagerWorkerTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.publisher_config = self.root / 'publisher.json'
        self.publisher_config.write_text('{"activation":"frozen"}')
        self.config = dict(checkout=str(self.root), state_dir=str(self.root / 'state'),
            repository='example/canonical', update_url='https://example.invalid/dl',
            publish_command=['hub-publisher'], publish_nodes=True, report_commit_status=True,
            manager_targets={'m-work': dict(publish_command=['manager-publisher', '--config', str(self.publisher_config)],
                                           activation='frozen'),
                             'm-home': dict(publish_command=['home-publisher'])})
        self.calls = []

    def invoke(self, extra=(), fail=None, phase='confirmed', receipt_changes=None, configured=False, source=SHA):
        config = self.root / 'config.json'
        config.write_text(json.dumps(self.config))

        def output(args, cwd):
            if args[1] == 'remote':
                return 'https://github.com/example/canonical.git'
            return source if args[1] == 'rev-parse' else ''

        def run(args, cwd, **kwargs):
            self.calls.append(args)
            if fail and fail(args):
                raise subprocess.CalledProcessError(1, args)
            if 'scripts/release/build_hub_release.py' in args:
                artifacts(Path(args[args.index('--out') + 1]), source, args[args.index('--build') + 1])
            if args[0] in ('manager-publisher', 'home-publisher') and phase is not None:
                receipt = dict(phase=phase, source=source, manager_id=args[args.index('--manager-id') + 1],
                               run_id=args[args.index('--run-id') + 1])
                receipt.update(receipt_changes or {})
                (Path(args[args.index('--artifact-dir') + 1]) / 'manager-receipt.json').write_text(json.dumps(receipt))

        argv = ['worker', '--config', str(config)]
        if not configured:
            argv.extend(['--scope', 'manager', '--manager-id', 'm-work', '--commit', source])
        with patch('sys.argv', [*argv, *extra]), \
                patch.object(worker, 'output', output), patch.object(worker, 'run', run):
            worker.main()
        return json.loads((self.root / 'state/manager/m-work/last.json').read_text())

    def test_pinned_target_receipt_and_publisher_isolation(self):
        result = self.invoke()
        self.assertEqual(result['result'], 'success')
        self.assertEqual(result['manager_id'], 'm-work')
        self.assertEqual(result['manifest_source'], SHA)
        self.assertRegex(result['run_id'], r'^[0-9a-f]{32}$')
        self.assertRegex(result['target_identity'], r'^[0-9a-f]{64}$')
        self.assertNotIn(str(self.publisher_config), json.dumps(result))
        self.assertFalse((self.root / 'state/last.json').exists())
        builds = [c for c in self.calls if 'scripts/release/build_hub_release.py' in c]
        self.assertIn('--publish-managers', builds[0])
        self.assertNotIn('--publish-nodes', builds[0])
        publisher = next(c for c in self.calls if c[0] == 'manager-publisher')
        self.assertEqual(publisher[-4:], ['--run-id', result['run_id'], '--manager-id', 'm-work'])
        self.assertFalse(any(c[0] in ('hub-publisher', 'home-publisher') for c in self.calls))
        checks = [c for c in self.calls if c[:3] == ['git', 'merge-base', '--is-ancestor']]
        self.assertEqual(checks, [['git', 'merge-base', '--is-ancestor', SHA, 'refs/remotes/origin/main']] * 2)
        self.assertTrue(all('context=manager-release/worker' in c for c in self.calls if c[0] == 'gh'))
        receipt = json.loads((Path(result['evidence']) / 'worker-receipt.json').read_text())
        self.assertEqual(receipt['run_id'], result['run_id'])
        self.assertEqual(receipt['manifest_source'], SHA)

    def test_explicit_full_commit_required(self):
        with self.assertRaises(SystemExit):
            self.invoke(['--commit', 'HEAD'])
        config = self.root / 'config.json'
        config.write_text(json.dumps(self.config))
        with patch('sys.argv', ['worker', '--config', str(config), '--scope', 'manager',
                                '--manager-id', 'm-work']), self.assertRaises(SystemExit):
            worker.main()
        self.assertFalse((self.root / 'state').exists())

    def test_service_config_selects_explicit_manager_pin_without_cli_scope(self):
        self.config.update(scope='manager', manager_id='m-work', manager_commit=SHA)
        result = self.invoke(configured=True)
        self.assertEqual(result['scope'], 'manager')
        self.assertEqual(result['sha'], SHA)
        self.assertEqual(result['result'], 'success')
        self.assertFalse(any(c[0] == 'hub-publisher' for c in self.calls))

    def test_service_config_rejects_invalid_scope_missing_pin_and_unknown_target(self):
        for values, error in ((dict(scope='other'), SystemExit),
                              (dict(scope='manager', manager_id='m-work'), SystemExit),
                              (dict(scope='manager', manager_id='m-work', manager_commit='HEAD'), SystemExit),
                              (dict(scope='manager', manager_id='unknown', manager_commit=SHA), ValueError),
                              (dict(scope='manager', manager_commit=SHA), ValueError)):
            for key in ('scope', 'manager_id', 'manager_commit'):
                self.config.pop(key, None)
            self.config.update(values)
            with self.subTest(values=values), self.assertRaises(error):
                self.invoke(configured=True)
        self.assertFalse(any(c[0] in ('hub-publisher', 'manager-publisher') for c in self.calls))

    def test_target_never_falls_back(self):
        for manager in (None, '', '../m-work', 'm-work/other', 'unknown', 'M-work'):
            with self.subTest(manager=manager), self.assertRaises(ValueError):
                worker.publication_target(self.config, 'manager', manager)
        with self.assertRaises(ValueError):
            worker.publication_target(self.config, 'hub', 'm-work')
        for command in ('publisher', [], ['publisher', None]):
            self.config['manager_targets']['m-work']['publish_command'] = command
            with self.subTest(command=command), self.assertRaises(ValueError):
                worker.publication_target(self.config, 'manager', 'm-work')

    def test_hub_success_cannot_skip_manager(self):
        state = self.root / 'state'
        state.mkdir()
        previous = dict(sha=SHA, result='success')
        (state / 'last.json').write_text(json.dumps(previous))
        self.assertEqual(self.invoke()['result'], 'success')
        self.assertTrue(any(c[0] == 'manager-publisher' for c in self.calls))
        self.assertEqual(json.loads((state / 'last.json').read_text()), previous)

    def test_same_success_reconfirms_and_changed_target_reuses_validated_artifact(self):
        first = self.invoke()
        self.calls.clear()
        second = self.invoke()
        self.assertNotEqual(second['run_id'], first['run_id'])
        self.assertEqual(second['build'], first['build'])
        self.assertFalse(any('scripts/release/build_hub_release.py' in c for c in self.calls))
        self.assertTrue(any(c[0] == 'manager-publisher' for c in self.calls))
        self.config['manager_targets']['m-work']['activation'] = 'enabled'
        self.calls.clear()
        second = self.invoke()
        self.assertNotEqual(second['target_identity'], first['target_identity'])
        self.assertEqual(second['build'], first['build'])
        self.assertNotEqual(second['run_id'], first['run_id'])
        self.assertFalse(any('scripts/release/build_hub_release.py' in c for c in self.calls))
        self.assertTrue(any(c[0] == 'manager-publisher' for c in self.calls))

    def test_same_config_path_changed_contents_invalidate_success(self):
        first = self.invoke()
        self.publisher_config.write_text('{"activation":"enabled"}')
        self.calls.clear()
        second = self.invoke()
        self.assertNotEqual(first['target_identity'], second['target_identity'])
        self.assertEqual(first['build'], second['build'])
        self.assertFalse(any('scripts/release/build_hub_release.py' in c for c in self.calls))
        self.assertTrue(any(c[0] == 'manager-publisher' for c in self.calls))

    def test_checked_package_reused_for_new_activation_window_and_run(self):
        with self.assertRaises(SystemExit) as stopped:
            self.invoke(phase='checked')
        self.assertEqual(stopped.exception.code, 3)
        checked = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.publisher_config.write_text('{"activation":"enabled","bootstrap_window_until":"future"}')
        self.config['manager_targets']['m-work']['publish_command'].append('--apply')
        self.calls.clear()
        confirmed = self.invoke()
        self.assertEqual(confirmed['result'], 'success')
        self.assertEqual(confirmed['build'], checked['build'])
        self.assertEqual(confirmed['evidence'], checked['evidence'])
        self.assertNotEqual(confirmed['run_id'], checked['run_id'])
        self.assertFalse(any('scripts/release/build_hub_release.py' in c for c in self.calls))
        publisher = next(c for c in self.calls if c[0] == 'manager-publisher')
        self.assertIn('--apply', publisher)

    def test_new_source_cannot_reuse_prior_manager_validation(self):
        first = self.invoke()
        self.calls.clear()
        second = self.invoke(source='b' * 40)
        self.assertEqual(second['sha'], 'b' * 40)
        self.assertNotEqual(first['build'], second['build'])
        self.assertTrue(any('scripts/release/build_hub_release.py' in c for c in self.calls))

    def test_different_managers_keep_separate_state(self):
        first = self.invoke()
        self.config['manager_targets']['m-home']['publish_command'].extend(
            ['--config', str(self.publisher_config)])
        self.calls.clear()
        self.invoke(['--manager-id', 'm-home'])
        home = json.loads((self.root / 'state/manager/m-home/last.json').read_text())
        work = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(home['manager_id'], 'm-home')
        self.assertEqual(home['result'], 'success')
        self.assertEqual(first, work)
        self.assertTrue(any(c[0] == 'home-publisher' for c in self.calls))
        self.assertFalse(any(c[0] == 'manager-publisher' for c in self.calls))

    def test_target_changed_during_gates_never_publishes(self):
        def change(args):
            if 'scripts/release/build_hub_release.py' in args:
                self.publisher_config.write_text('{"activation":"changed"}')
            return False

        with self.assertRaisesRegex(ValueError, 'changed during validation'):
            self.invoke(fail=change)
        self.assertFalse(any(c[0] == 'manager-publisher' for c in self.calls))

    def test_publisher_requires_regular_config_file(self):
        for command in (['publisher'], ['publisher', '--config'],
                        ['publisher', '--config', str(self.root / 'missing')],
                        ['publisher', '--config', str(self.root)]):
            self.config['manager_targets']['m-work']['publish_command'] = command
            with self.subTest(command=command), self.assertRaises(ValueError):
                worker.publication_target(self.config, 'manager', 'm-work')

    def test_corrupt_success_cache_is_failure_without_publisher(self):
        first = self.invoke()
        manifest = json.loads((Path(first['evidence']) / 'manager-manifest.json').read_text())
        (Path(first['evidence']) / manifest['files']['darwin-arm64']['name']).write_bytes(b'corrupt')
        self.calls.clear()
        with self.assertRaises(ValueError):
            self.invoke()
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(result['result'], 'failure')
        self.assertFalse(any(c[0] == 'manager-publisher' for c in self.calls))

    def test_frozen_publisher_rejection_is_failure(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(fail=lambda c: c[0] == 'manager-publisher')
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(result['result'], 'failure')
        self.assertGreater(result['retry_after'], 0)
        self.assertFalse(any(c[0] == 'hub-publisher' for c in self.calls))

    def test_second_ancestor_check_blocks_orphan_publication(self):
        checks = 0

        def fail(args):
            nonlocal checks
            if args[:3] == ['git', 'merge-base', '--is-ancestor']:
                checks += 1
                return checks == 2
            return False

        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(fail=fail)
        self.assertFalse(any(c[0] == 'manager-publisher' for c in self.calls))

    def test_validate_only_calls_no_publisher(self):
        self.assertEqual(self.invoke(['--validate-only'])['result'], 'validated')
        self.assertFalse(any(c[0].endswith('publisher') for c in self.calls))
        statuses = [c for c in self.calls if c[0] == 'gh']
        self.assertTrue(statuses)
        self.assertTrue(all('context=manager-release/validation' in c for c in statuses))
        self.assertFalse(any('context=manager-release/worker' in c for c in statuses))

    def test_missing_historical_receipt_still_requires_fresh_confirmation(self):
        first = self.invoke()
        (Path(first['evidence']) / 'manager-receipt.json').unlink()
        self.calls.clear()
        with self.assertRaises(FileNotFoundError):
            self.invoke(phase=None)
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertNotEqual(result['run_id'], first['run_id'])
        self.assertEqual(result['build'], first['build'])
        self.assertEqual(result['result'], 'failure')
        self.assertTrue(any(c[0] == 'manager-publisher' for c in self.calls))
        self.assertFalse(any('state=success' in c for c in self.calls if c[0] == 'gh'))

    def test_historical_success_cannot_mask_current_offline_publisher(self):
        first = self.invoke()
        self.calls.clear()
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(fail=lambda c: c[0] == 'manager-publisher')
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertNotEqual(result['run_id'], first['run_id'])
        self.assertEqual(result['result'], 'failure')

    def test_checked_receipt_is_not_activation_success(self):
        with self.assertRaises(SystemExit) as stopped:
            self.invoke(phase='checked')
        self.assertEqual(stopped.exception.code, 3)
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(result['result'], 'checked')
        receipt = json.loads((Path(result['evidence']) / 'worker-receipt.json').read_text())
        self.assertEqual(receipt['result'], 'checked')
        self.assertTrue(any('state=pending' in c for c in self.calls if c[0] == 'gh'))
        self.assertFalse(any('state=success' in c for c in self.calls if c[0] == 'gh'))

    def test_unconfirmed_backoff_cannot_trigger_service_success(self):
        for phase in ('checked', 'failure'):
            with self.subTest(phase=phase):
                with self.assertRaises(SystemExit if phase == 'checked' else subprocess.CalledProcessError):
                    self.invoke(['--retry'], phase=phase,
                                fail=lambda c: phase == 'failure' and c[0] == 'manager-publisher')
                before = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
                self.calls.clear()
                with self.assertRaises(SystemExit) as stopped:
                    self.invoke()
                self.assertEqual(stopped.exception.code, 3)
                self.assertEqual(json.loads((self.root / 'state/manager/m-work/last.json').read_text()), before)
                self.assertFalse(any(c[0].endswith('publisher') for c in self.calls))

    def test_receipt_requires_this_run_target_and_source(self):
        for changes in ({'run_id': 'old'}, {'manager_id': 'm-home'}, {'source': 'b' * 40}, {'phase': 'staged'}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.invoke(['--retry'], receipt_changes=changes)
        self.assertFalse(any('state=success' in c for c in self.calls if c[0] == 'gh'))

    def test_old_confirmed_receipt_cannot_mask_missing_current_receipt(self):
        self.invoke()
        with self.assertRaises(FileNotFoundError):
            self.invoke(['--retry'], phase=None)
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(result['result'], 'failure')

    def test_existing_shared_lock_blocks_without_target_record(self):
        with patch.object(worker.fcntl, 'flock', side_effect=BlockingIOError):
            with self.assertRaises(SystemExit) as stopped:
                self.invoke()
        self.assertEqual(stopped.exception.code, 3)
        self.assertFalse((self.root / 'state/manager').exists())
        self.assertFalse(any(c[0].endswith('publisher') for c in self.calls))

    def test_existing_shared_lock_keeps_hub_exit_success(self):
        config = self.root / 'config.json'
        config.write_text(json.dumps(self.config))
        with patch('sys.argv', ['worker', '--config', str(config)]), \
                patch.object(worker, 'output', return_value='https://github.com/example/canonical.git'), \
                patch.object(worker, 'run'), \
                patch.object(worker.fcntl, 'flock', side_effect=BlockingIOError):
            self.assertIsNone(worker.main())
        self.assertFalse((self.root / 'state/manager').exists())

    def test_cache_requires_matching_manager_source_and_integrity(self):
        directory = self.root / 'cache'
        directory.mkdir()
        (directory / 'latest.json').write_text('{}')
        self.assertFalse(worker.cache_ready(directory, False, True))
        manifest = artifacts(directory, SHA, '202611070001')
        self.assertTrue(worker.cache_ready(directory, False, True))
        path = directory / 'manager-manifest.json'
        changed = copy.deepcopy(manifest)
        changed['commit'] = 'b' * 40
        path.write_text(json.dumps(changed))
        with self.assertRaises(ValueError):
            worker.cache_ready(directory, False, True)
        path.write_text(json.dumps(manifest))
        (directory / manifest['files']['darwin-arm64']['name']).write_bytes(b'corrupt')
        with self.assertRaises(ValueError):
            worker.cache_ready(directory, False, True)

    def test_default_reporting_off_preserves_checked_activation_exit(self):
        del self.config['report_commit_status']
        with self.assertRaises(SystemExit) as raised:
            self.invoke(phase='checked')
        self.assertEqual(raised.exception.code, 3)
        result = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(result['result'], 'checked')
        self.assertFalse(result['report_commit_status'])
        self.assertFalse(any(c[0] == 'gh' for c in self.calls))

    def test_reporting_off_preserves_corrupt_manager_cache_failure(self):
        self.config['report_commit_status'] = False
        result = self.invoke(['--validate-only'])
        manifest = json.loads((Path(result['evidence']) / 'manager-manifest.json').read_text())
        (Path(result['evidence']) / manifest['files']['darwin-arm64']['name']).write_bytes(b'corrupt')
        self.calls.clear()
        with self.assertRaises(ValueError):
            self.invoke()
        saved = json.loads((self.root / 'state/manager/m-work/last.json').read_text())
        self.assertEqual(saved['result'], 'failure')
        self.assertFalse(any(c[0] == 'gh' for c in self.calls))


if __name__ == '__main__':
    unittest.main()
