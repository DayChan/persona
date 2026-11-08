"""Exercise the original publisher lock, confirmation order and zero-deploy policy."""
import copy
import fcntl
import gzip
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import node_artifacts
import promote_hub_release as publisher
from test_node_artifacts import health, native_result, release


class NodeDownloadPublicationTests(unittest.TestCase):
    def test_confirmed_hub_activates_downloads_under_original_lock_without_node_calls(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            staged = root / 'releases/hub-main/.upload-fixture'
            manifest = release(staged)
            hub = copy.deepcopy(manifest)
            hub['files'] = {'linux-amd64': hub['files']['linux-amd64']}
            (staged / 'latest.json').write_text(json.dumps(hub))
            (root / 'hub.json').write_text(json.dumps({'auto_update_nodes': False}))
            binary_hash = hub['files']['linux-amd64']['binary_sha256']
            downloads = root / 'releases/hub-main/node-packages'
            events, calls = [], []
            actual_digest = publisher.digest
            actual_activate = publisher.activate_downloads

            def api(runtime, path, method='GET'):
                calls.append((path, method))
                if path == 'hub/update':
                    return dict(auto=True, can_update=True, state='ready', confirmed=True)
                if path == 'machines':
                    return {'machines': [{'id': 'fixture-node', 'node_version': 'unchanged', 'online': True}]}
                if path == 'hub/update/check':
                    self.assertEqual(method, 'POST')
                    self.assertFalse((downloads / 'latest.json').exists())
                    self.assertFalse((downloads / manifest['version']).exists())
                    events.append('hub-update-check')
                    archive = staged / hub['files']['linux-amd64']['name']
                    (root / 'bin/personad').write_bytes(gzip.decompress(archive.read_bytes()))
                    return {}
                if path == 'health':
                    events.append('hub-healthy')
                    return health(manifest)
                if path == 'hub/update/history':
                    return []
                self.fail(f'unexpected API operation: {path} {method}')

            def digest(path):
                if str(path) == '/proc/123/exe':
                    events.append('serving-sha-verified')
                    return binary_hash
                return actual_digest(path)

            def command(argv, **kwargs):
                if argv[-1] == 'node-update-check':
                    return native_result(manifest)
                self.assertEqual(argv[1:], ['hub', 'switch', '--status'])
                return 'pid 123'

            def activate(*args):
                # A separately opened descriptor must not acquire the lock held by promote.
                with (root / 'releases/hub/.publish.lock').open('w') as descriptor:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                self.assertIn('serving-sha-verified', events)
                events.append('download-activation')
                return actual_activate(*args)

            with patch.object(publisher, 'api', side_effect=api), \
                    patch.object(publisher, 'digest', side_effect=digest), \
                    patch.object(publisher.subprocess, 'check_output', side_effect=command), \
                    patch.object(publisher, 'activate_downloads', side_effect=activate):
                publisher.promote(dict(root=str(root), staged=str(staged), publish_nodes=True,
                                       release_namespace='hub-main', timeout=10))
            self.assertLess(events.index('hub-healthy'), events.index('download-activation'))
            self.assertEqual([c for c in calls if c[1] != 'GET'], [('hub/update/check', 'POST')])
            self.assertFalse(staged.exists())
            canonical = json.loads((downloads / 'latest.json').read_text())
            for artifact in canonical['files'].values():
                node_artifacts.verify_archive(downloads / manifest['version'], artifact)
            self.assertEqual(canonical['version'], manifest['version'])
            self.assertEqual(canonical['commit'], manifest['commit'])
            self.assertFalse((root / 'releases/hub/node-packages').exists())


    def test_hub_rollback_never_activates_consumer_downloads(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            staged = root / 'releases/hub-main/.upload-fixture'
            manifest = release(staged)
            hub = copy.deepcopy(manifest)
            hub['files'] = {'linux-amd64': hub['files']['linux-amd64']}
            (staged / 'latest.json').write_text(json.dumps(hub))
            (root / 'hub.json').write_text(json.dumps({'auto_update_nodes': False}))
            reads = []

            def api(runtime, path, method='GET'):
                if path == 'hub/update':
                    reads.append(path)
                    return dict(auto=True, can_update=True, state='ready' if len(reads) == 1 else 'rolled_back')
                if path == 'machines':
                    return {'machines': []}
                if path == 'hub/update/check':
                    return {}
                if path == 'health':
                    return health(manifest)
                self.fail(f'unexpected API operation: {path} {method}')

            with patch.object(publisher, 'api', side_effect=api), \
                    patch.object(publisher.subprocess, 'check_output', return_value=native_result(manifest)):
                with self.assertRaisesRegex(RuntimeError, 'rolled_back'):
                    publisher.promote(dict(root=str(root), staged=str(staged), publish_nodes=True,
                                           release_namespace='hub-main', timeout=10))
            mirror = root / 'releases/hub-main/node-packages'
            self.assertFalse((mirror / manifest['version']).exists())
            self.assertFalse((mirror / 'latest.json').exists())
            self.assertFalse((root / 'bin/nodes').exists())


if __name__ == '__main__':
    unittest.main()
