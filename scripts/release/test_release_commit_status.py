"""Commit status opt-in never replaces either worker's local outcome."""
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import android_release_worker as android
import hub_release as hub

SHA = 'a' * 40
OMITTED = object()


def policy(config, value):
    if value is not OMITTED:
        config['report_commit_status'] = value
    return config


class CommitStatusTests(unittest.TestCase):
    def test_android_every_transition_requires_explicit_true(self):
        for value in (OMITTED, False, True, None, 'true', 1):
            for state in ('pending', 'success', 'failure'):
                with self.subTest(policy=value, state=state):
                    config = policy(dict(repository='example/project', gh='/configured/gh'), value)
                    with patch.object(android, 'run') as run, contextlib.redirect_stdout(io.StringIO()):
                        android.status(config, SHA, state, 'description')
                    if value is True:
                        run.assert_called_once_with(['/configured/gh', 'api',
                            f'repos/example/project/statuses/{SHA}', '-f', f'state={state}',
                            '-f', 'context=release/android', '-f', 'description=description'])
                    else:
                        run.assert_not_called()

    def test_explicit_android_reporting_errors_are_not_hidden(self):
        with patch.object(android, 'run', side_effect=RuntimeError('status API denied')):
            with self.assertRaisesRegex(RuntimeError, 'status API denied'):
                android.status(dict(repository='example/project', report_commit_status=True), SHA, 'pending', 'description')

    def test_hub_success_and_all_failure_paths_retain_local_receipts(self):
        for value in (OMITTED, False, True):
            for failure in (None, 'validation', 'publication', 'status-api'):
                with self.subTest(policy=value, failure=failure), tempfile.TemporaryDirectory() as temporary:
                    root = Path(temporary)
                    config = policy(dict(checkout=str(root), state_dir=str(root / 'state'),
                        repository='example/project', update_url='file:///example', publish_command=['publisher']), value)
                    config_path = root / 'config.json'
                    config_path.write_text(json.dumps(config))
                    calls = []

                    def output(argv, cwd):
                        if argv[1] == 'remote':
                            return 'https://github.com/example/project.git'
                        return SHA if argv[1] == 'rev-parse' else ''

                    def run(argv, cwd, **kwargs):
                        if argv[:2] == ['gh', 'api']:
                            calls.append(argv)
                            if failure == 'status-api':
                                raise RuntimeError('status API denied')
                        if failure == 'validation' and 'scripts/release/build_hub_release.py' in argv:
                            raise RuntimeError('validation failed')
                        if failure == 'publication' and argv[0] == 'publisher':
                            raise RuntimeError('publication failed')

                    expected_failure = failure in ('validation', 'publication') or failure == 'status-api' and value is True
                    with patch('sys.argv', ['worker', '--config', str(config_path)]), \
                            patch.object(hub, 'output', side_effect=output), patch.object(hub, 'run', side_effect=run), \
                            patch.object(hub, 'cache_ready', return_value=False), contextlib.redirect_stdout(io.StringIO()):
                        if expected_failure:
                            with self.assertRaises(RuntimeError):
                                hub.main()
                        else:
                            hub.main()
                    result = json.loads((root / 'state/last.json').read_text())
                    receipt = json.loads((Path(result['evidence']) / 'worker-receipt.json').read_text())
                    self.assertEqual(result, receipt)
                    self.assertEqual(result['result'], 'failure' if expected_failure else 'success')
                    self.assertEqual(result['report_commit_status'], value is True)
                    self.assertTrue((Path(result['evidence']) / 'validation.log').exists() or failure == 'status-api' and value is True)
                    if value is True:
                        states = [next(arg for arg in call if arg.startswith('state=')) for call in calls]
                        self.assertEqual(states, ['state=pending', 'state=failure'] if expected_failure else ['state=pending', 'state=success'])
                    else:
                        self.assertEqual(calls, [])

    def test_android_cycle_success_failure_and_checkpoint_recovery_keep_local_outcomes(self):
        for value in (OMITTED, False, True):
            for failure in (False, True):
                with self.subTest(policy=value, failure=failure), tempfile.TemporaryDirectory() as temporary:
                    root = Path(temporary)
                    config = policy(dict(state_directory=str(root), repository='example/project',
                        application_id='com.example.app', certificate_sha256='c' * 64), value)
                    request = dict(repository=config['repository'], source_sha=SHA,
                        package=config['application_id'], version_code=100001)
                    calls = []

                    def run(argv, *args, **kwargs):
                        self.assertEqual(argv[:2], ['gh', 'api'])
                        calls.append(argv)

                    def build(config, checkout, sha, directory):
                        if failure:
                            raise RuntimeError('validation failed')
                        (directory / 'signed.apk').write_bytes(b'signed fixture')
                        android.atomic_json(directory / 'signature.json', dict(signed_sha256=android.digest(directory / 'signed.apk'),
                            certificate_sha256=config['certificate_sha256']))
                        return request

                    with patch.object(android, 'prepare', return_value=(root, SHA)), patch.object(android, 'check_history'), \
                            patch.object(android, 'version_code', return_value=100001), patch.object(android, 'build', side_effect=build) as builds, \
                            patch.object(android, 'signing') as signing, patch.object(android, 'publish') as publish, \
                            patch.object(android, 'run', side_effect=run), contextlib.redirect_stdout(io.StringIO()):
                        if failure:
                            with self.assertRaisesRegex(RuntimeError, 'validation failed'):
                                android.cycle(config)
                        else:
                            android.cycle(config)
                            self.assertEqual(json.loads((root / 'published.json').read_text()), request)
                            (root / 'published.json').unlink()
                            android.cycle(config)
                            self.assertEqual(json.loads((root / 'published.json').read_text()), request)
                            builds.assert_called_once()
                            signing.assert_called_once()
                            publish.assert_called_once()
                    directory = root / 'runs' / SHA
                    self.assertTrue((directory / 'release.log').exists())
                    if failure:
                        saved = json.loads((directory / 'failure.json').read_text())
                        self.assertEqual(saved['error'], 'validation failed')
                        self.assertFalse((directory / 'verified.json').exists())
                        self.assertFalse((root / 'published.json').exists())
                    else:
                        self.assertTrue((directory / 'verified.json').exists())
                    states = [next(arg for arg in call if arg.startswith('state=')) for call in calls]
                    expected = ['state=pending', 'state=failure'] if failure else ['state=pending', 'state=success', 'state=success']
                    self.assertEqual(states, expected if value is True else [])


if __name__ == '__main__':
    unittest.main()
