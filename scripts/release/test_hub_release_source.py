import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import hub_release


class SourceSelectionTests(unittest.TestCase):
    def invoke(self, directory, previous=None, branch=None, validate_only=False, commit=None, remote_repository=None, report_commit_status=None):
        root = Path(directory)
        state = root / 'state'
        state.mkdir(exist_ok=True)
        if previous:
            (state / 'last.json').write_text(json.dumps(previous))
            attempt = state / f"{previous['build']}-{'a' * 12}"
            attempt.mkdir(exist_ok=True)
            (attempt / 'latest.json').write_text('{}')
        config = {'checkout': str(root), 'state_dir': str(state),
                  'repository': 'example/canonical', 'update_url': 'file:///example',
                  'publish_command': ['publisher']}
        if report_commit_status is not None:
            config['report_commit_status'] = report_commit_status
        if branch is not None:
            config['source_branch'] = branch
        config_path = root / 'config.json'
        config_path.write_text(json.dumps(config))
        calls = []
        def output(argv, cwd):
            return 'a' * 40 if argv[1] == 'rev-parse' else (
                'https://github.com/' + (remote_repository or config['repository']) + '.git'
                if argv[1] == 'remote' else '')
        argv = ['worker', '--config', str(config_path)]
        if validate_only:
            argv.append('--validate-only')
        if commit:
            argv.extend(['--commit', commit])
        with patch('sys.argv', argv), patch.object(hub_release, 'output', output), \
                patch.object(hub_release, 'run', lambda args, cwd, **kw: calls.append(args)):
            hub_release.main()
        return calls, json.loads((state / 'last.json').read_text())

    def test_default_main_is_used_before_and_after_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            calls, result = self.invoke(directory)
        fetches = [c for c in calls if c[:2] == ['git', 'fetch']]
        self.assertEqual(fetches, [['git', 'fetch', '--no-tags', 'origin',
            'refs/heads/main:refs/remotes/origin/main']] * 2)
        self.assertIn(['git', 'merge-base', '--is-ancestor', 'a' * 40,
                       'refs/remotes/origin/main'], calls)
        self.assertEqual(result['repository'], 'example/canonical')
        self.assertEqual(result['source_branch'], 'main')
        statuses = [c for c in calls if c[:2] == ['gh', 'api']]
        self.assertEqual(statuses, [])
        self.assertFalse(result['report_commit_status'])

    def test_opted_in_statuses_name_the_validated_source_repository_and_sha(self):
        with tempfile.TemporaryDirectory() as directory:
            calls, result = self.invoke(directory, report_commit_status=True)
        statuses = [c for c in calls if c[:2] == ['gh', 'api']]
        self.assertEqual(len(statuses), 2)
        self.assertTrue(all(c[2] == 'repos/example/canonical/statuses/' + 'a' * 40
                            for c in statuses))
        self.assertTrue(result['report_commit_status'])

    def test_pinned_commit_must_remain_on_selected_branch(self):
        with tempfile.TemporaryDirectory() as directory:
            calls, result = self.invoke(directory, commit='b' * 40, report_commit_status=True)
        self.assertEqual(result['sha'], 'b' * 40)
        self.assertIn(['git', 'checkout', '--detach', 'b' * 40], calls)
        checks = [c for c in calls if c[:3] == ['git', 'merge-base', '--is-ancestor']]
        self.assertEqual(checks, [['git', 'merge-base', '--is-ancestor', 'b' * 40,
                                  'refs/remotes/origin/main']] * 2)
        self.assertLess(calls.index(checks[0]), next(i for i, c in enumerate(calls) if c[0] == 'gh'))

    def test_pin_rejects_short_or_non_commit_arguments(self):
        import argparse
        for value in ['abc123', 'HEAD', '--all', 'A' * 40, 'z' * 40, 'a' * 39 + '\\n']:
            with self.subTest(value=value), self.assertRaises(argparse.ArgumentTypeError):
                hub_release.full_commit(value)

    def test_origin_identity_must_match_configured_status_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, 'does not match source origin'):
                self.invoke(directory, remote_repository='example/other')
            self.assertFalse((Path(directory) / 'state/last.json').exists())

    def test_supported_origin_spellings_and_untrusted_urls(self):
        for url in ['https://github.com/Example/Canonical.git',
                    'https://github.com/Example/Canonical/']:
            self.assertEqual(hub_release.repository_identity(url), 'example/canonical')
        for url in ['http://github.com/example/canonical', 'https://other.invalid/example/canonical',
                    'https://github.com/example/canonical/extra', 'local/path']:
            with self.subTest(url=url), self.assertRaises(ValueError):
                hub_release.repository_identity(url)

    def test_configured_branch_is_used_consistently(self):
        with tempfile.TemporaryDirectory() as directory:
            calls, result = self.invoke(directory, branch='release/stable')
        self.assertIn(['git', 'check-ref-format', 'refs/heads/release/stable'], calls)
        self.assertEqual(result['source_branch'], 'release/stable')
        self.assertTrue(all(c[-1] == 'refs/heads/release/stable:refs/remotes/origin/release/stable'
                            for c in calls if c[:2] == ['git', 'fetch']))

    def test_other_repository_or_branch_cannot_reuse_success(self):
        for repository, branch in [('example/other', 'main'),
                                   ('example/canonical', 'other')]:
            with self.subTest(repository=repository, branch=branch), \
                    tempfile.TemporaryDirectory() as directory:
                previous = {'repository': repository, 'source_branch': branch,
                            'update_url': 'file:///example', 'sha': 'a' * 40, 'build': '202001010001', 'result': 'success'}
                calls, result = self.invoke(directory, previous)
                self.assertTrue(any('scripts/release/build_hub_release.py' in c for c in calls))
                self.assertGreater(result['build'], previous['build'])

    def test_another_feed_cannot_reuse_a_successful_build(self):
        with tempfile.TemporaryDirectory() as directory:
            previous = {'repository': 'example/canonical', 'source_branch': 'main',
                        'update_url': 'file:///other-feed', 'sha': 'a' * 40,
                        'build': '202001010001', 'result': 'success'}
            calls, result = self.invoke(directory, previous)
        self.assertTrue(any('scripts/release/build_hub_release.py' in c for c in calls))
        self.assertEqual(result['update_url'], 'file:///example')
        self.assertGreater(result['build'], previous['build'])

    def test_validation_only_never_invokes_publisher_or_claims_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            calls, result = self.invoke(directory, validate_only=True, report_commit_status=True)
            self.assertFalse((Path(result['evidence']) / 'publication.log').exists())
        self.assertEqual(result['result'], 'validated')
        self.assertFalse(any(c[0] == 'publisher' for c in calls))
        self.assertTrue(any('scripts/release/build_hub_release.py' in c for c in calls))
        statuses = [c for c in calls if c[:2] == ['gh', 'api']]
        self.assertTrue(all('context=hub-release/validation' in c for c in statuses))
        self.assertFalse(any('context=hub-release/worker' in c for c in statuses))

    def test_normal_publication_reuses_only_matching_validated_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            previous = {'repository': 'example/canonical', 'source_branch': 'main',
                        'sha': 'a' * 40, 'build': '202001010001', 'result': 'validated',
                        'update_url': 'file:///example'}
            calls, result = self.invoke(directory, previous)
        self.assertFalse(any('scripts/release/build_hub_release.py' in c for c in calls))
        self.assertTrue(any(c[0] == 'publisher' for c in calls))
        self.assertEqual(result['result'], 'success')
        self.assertEqual(result['build'], previous['build'])


if __name__ == '__main__':
    unittest.main()
