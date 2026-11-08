from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
REPO = "${{ github.event.pull_request.head.repo.full_name || github.repository }}"
BRANCH = "${{ github.head_ref || github.ref_name }}"
CI_LANE = "${{ github.event_name == 'pull_request' && 'pr-fast' || inputs.lanes || 'auto' }}"
APPLE_LANE = "${{ inputs.lane || 'ios' }}"


def workflow(filename):
    return yaml.load((ROOT / '.github/workflows' / filename).read_text(), Loader=yaml.BaseLoader)


def group(filename, repo='example/project', branch='main', event='workflow_dispatch', lanes='auto', lane='ios'):
    template = workflow(filename)['concurrency']['group']
    return (template.replace(REPO, repo).replace(BRANCH, branch)
            .replace(CI_LANE, 'pr-fast' if event == 'pull_request' else lanes)
            .replace(APPLE_LANE, lane))


class ConcurrencyTests(unittest.TestCase):
    def test_all_manual_entry_points_cancel_same_branch_lane_not_same_commit_key(self):
        for filename, prefix in [('ci.yml', 'ci'), ('apple-test.yml', 'apple'),
                                 ('server-test.yml', 'server'), ('android-test.yml', 'android'),
                                 ('scripts-tests.yml', 'scripts')]:
            with self.subTest(filename=filename):
                config = workflow(filename)['concurrency']
                self.assertEqual(config['cancel-in-progress'], 'true')
                self.assertTrue(config['group'].startswith(prefix + '-'))
                self.assertIn(REPO, config['group'])
                self.assertIn(BRANCH, config['group'])
                self.assertNotIn('github.sha', config['group'])
                self.assertNotIn('github.run_id', config['group'])
                self.assertNotIn('${{', group(filename))
                self.assertNotEqual(group(filename, repo='example/project'), group(filename, repo='another/project'))
                self.assertNotEqual(group(filename, branch='main'), group(filename, branch='feature'))

    def test_lanes_and_caller_groups_remain_independent(self):
        self.assertNotEqual(group('ci.yml', lanes='server'), group('ci.yml', lanes='android'))
        self.assertNotEqual(group('ci.yml', event='pull_request'), group('ci.yml', lanes='auto'))
        self.assertNotEqual(group('apple-test.yml', lane='ios'), group('apple-test.yml', lane='macos'))
        self.assertNotEqual(group('ci.yml', lanes='apple'), group('apple-test.yml', lane='ios'))
        # Stable file prefixes deduplicate direct/reusable lane dispatches without
        # reusing the caller's github.workflow group, which would cancel the caller.
        for filename in ('apple-test.yml', 'server-test.yml', 'android-test.yml', 'scripts-tests.yml'):
            self.assertNotIn('github.workflow', workflow(filename)['concurrency']['group'])
        self.assertIn('workflow_dispatch', workflow('ci.yml')['on'])
        self.assertIn('pull_request', workflow('ci.yml')['on'])
        self.assertIn('workflow_call', workflow('apple-test.yml')['on'])
        self.assertIn('workflow_dispatch', workflow('apple-test.yml')['on'])

    def test_heavy_matrices_keep_all_shards_with_bounded_parallelism(self):
        for filename, job, maximum, size in [('server-test.yml', 'server', 2, 4),
                                              ('android-test.yml', 'android', 2, 5),
                                              ('scripts-tests.yml', 'scripts', 1, 2)]:
            with self.subTest(filename=filename):
                strategy = workflow(filename)['jobs'][job]['strategy']
                self.assertEqual(int(strategy['max-parallel']), maximum)
                self.assertEqual(strategy['fail-fast'], 'false')
                self.assertEqual(len(next(iter(strategy['matrix'].values()))), size)


if __name__ == '__main__':
    unittest.main()
