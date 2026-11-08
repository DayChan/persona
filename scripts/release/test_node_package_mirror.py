"""Consumer downloads, using only isolated synthetic release packages."""
import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import node_artifacts as artifacts
import node_package_mirror as mirror
from test_node_artifacts import BUILD, health, native_result, release


class NodeDownloadTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'runtime'
        (self.root / 'bin').mkdir(parents=True)
        (self.root / 'releases/hub-main').mkdir(parents=True)
        (self.root / 'releases/hub').mkdir()
        self.staged = self.root / 'releases/hub-main/.upload-fixture'
        self.manifest = release(self.staged)
        self.manifest.update(node_protocol=1, node_protocol_min=1,
                             node_capabilities=['fixture_capability'], channel='release')
        (self.staged / 'node-manifest.json').write_text(json.dumps(self.manifest))
        with patch.object(artifacts.subprocess, 'check_output', return_value=native_result(self.manifest)):
            self.native = artifacts.prepare_nodes(self.root, self.staged, self.manifest)
        self.downloads = self.root / 'releases/hub-main/node-packages'

    def prepare(self):
        return mirror.prepare_downloads(self.root, 'hub-main', self.staged, self.manifest, self.native)

    def activate(self, prepared):
        return mirror.activate_downloads(self.root, 'hub-main', prepared, self.manifest, health(self.manifest))

    def test_preserves_archive_bytes_and_metadata_in_standard_layout(self):
        prepared = self.prepare()
        self.assertFalse((self.downloads / self.manifest['version']).exists())
        self.assertFalse((self.downloads / 'latest.json').exists())
        destination = self.activate(prepared)
        canonical = json.loads((destination / 'manifest.json').read_text())
        self.assertEqual(json.loads((destination / 'source-manifest.json').read_text()), self.manifest)
        for platform, artifact in canonical['files'].items():
            self.assertEqual(artifact['name'], f'personad-{platform}.gz')
            expected = self.manifest['files'][platform]
            self.assertEqual({k: v for k, v in artifact.items() if k != 'name'},
                             {k: v for k, v in expected.items() if k != 'name'})
            self.assertEqual((destination / artifact['name']).read_bytes(),
                             (self.staged / expected['name']).read_bytes())
        self.assertEqual(json.loads((self.downloads / 'latest.json').read_text()), canonical)
        for key in ('version', 'build', 'commit', 'channel', 'node_protocol', 'node_protocol_min', 'node_capabilities'):
            self.assertEqual(canonical[key], self.manifest[key])
        self.assertEqual((destination / 'manifest.json').stat().st_mode & 0o777, 0o644)
        self.assertFalse((self.root / 'releases/hub/node-packages').exists())
        self.assertFalse((self.root / 'bin/nodes').exists())

    def test_restrictive_publisher_umask_preserves_read_only_provider_access(self):
        previous = os.umask(0o077)
        try:
            destination = self.activate(self.prepare())
        finally:
            os.umask(previous)
        for directory in (self.downloads, destination):
            self.assertEqual(directory.stat().st_mode & 0o777, 0o755)
        for path in list(destination.iterdir()) + [self.downloads / 'latest.json']:
            self.assertEqual(path.stat().st_mode & 0o777, 0o644)

    def test_existing_native_is_reused_without_build_or_execution(self):
        before = {p.name: (p.stat().st_ino, p.read_bytes()) for p in self.native.iterdir()}
        with patch.object(artifacts.subprocess, 'check_output', side_effect=AssertionError('must reuse native')):
            native = artifacts.prepare_nodes(self.root, self.staged, self.manifest)
            self.activate(self.prepare())
        self.assertEqual(native, self.native)
        self.assertEqual(before, {p.name: (p.stat().st_ino, p.read_bytes()) for p in self.native.iterdir()})

    def test_retry_after_upload_cleanup_never_repacks(self):
        destination = self.activate(self.prepare())
        before = {p.name: (p.stat().st_ino, p.read_bytes()) for p in destination.iterdir()}
        for path in self.staged.iterdir():
            path.unlink()
        self.assertEqual(self.prepare(), destination)
        self.activate(destination)
        self.assertEqual(before, {p.name: (p.stat().st_ino, p.read_bytes()) for p in destination.iterdir()})

    def test_health_version_build_gate_before_activation(self):
        prepared = self.prepare()
        for invalid in ({}, dict(health(self.manifest), ok=False),
                        dict(health(self.manifest), version='different'), dict(health(self.manifest), build=BUILD+1)):
            with self.subTest(health=invalid), self.assertRaises(RuntimeError):
                mirror.activate_downloads(self.root, 'hub-main', prepared, self.manifest, invalid)
            self.assertFalse((self.downloads / self.manifest['version']).exists())
            self.assertFalse((self.downloads / 'latest.json').exists())

    def test_bad_archive_cleans_partial_copy(self):
        path = self.staged / self.manifest['files']['darwin-arm64']['name']
        path.write_bytes(path.read_bytes() + b'damaged')
        with self.assertRaises(ValueError):
            self.prepare()
        self.assertEqual(list(self.downloads.iterdir()), [])

    def test_identity_and_platform_changes_fail_closed(self):
        for field, value in [('version', '../escape'), ('commit', 'b'*40), ('build', BUILD+1),
                             ('files', {'linux-amd64': {}}), ('programs', {'unexpected': {}})]:
            candidate = copy.deepcopy(self.manifest)
            candidate[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                mirror.prepare_downloads(self.root, 'hub-main', self.staged, candidate, self.native)

    def test_immutable_manifests_and_archives_cannot_change(self):
        destination = self.activate(self.prepare())
        for name in ['manifest.json', 'source-manifest.json', 'personad-darwin-arm64.gz']:
            path = destination / name
            original = path.read_bytes()
            path.write_bytes(b'{}' if name.endswith('.json') else b'bad gzip')
            with self.subTest(name=name), self.assertRaises(ValueError):
                self.prepare()
            path.write_bytes(original)

    def test_staged_archive_link_is_rejected(self):
        path = self.staged / self.manifest['files']['darwin-arm64']['name']
        outside = Path(self.temporary.name) / 'outside.gz'
        path.rename(outside)
        path.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, 'non-symlink'):
            self.prepare()

    def test_mirror_version_latest_and_archive_links_are_rejected(self):
        destination = self.activate(self.prepare())
        for path in [destination / 'personad-darwin-arm64.gz', self.downloads / 'latest.json', destination]:
            original = path.with_name(path.name + '-original')
            path.rename(original)
            path.symlink_to(original, target_is_directory=original.is_dir())
            with self.subTest(path=path.name), self.assertRaises(ValueError):
                self.prepare()
            path.unlink()
            original.rename(path)

    def test_parent_link_cannot_escape_namespace(self):
        self.downloads.symlink_to(self.root / 'bin', target_is_directory=True)
        before = list((self.root / 'bin').iterdir())
        with self.assertRaisesRegex(ValueError, 'links'):
            self.prepare()
        self.assertEqual(before, list((self.root / 'bin').iterdir()))

    def test_other_namespace_cannot_activate_prepared_directory(self):
        prepared = self.prepare()
        with self.assertRaises(ValueError):
            mirror.activate_downloads(self.root, 'hub', prepared, self.manifest, health(self.manifest))
        self.assertFalse((self.root / 'releases/hub/node-packages/latest.json').exists())

    def test_tampering_after_prepare_is_detected(self):
        prepared = self.prepare()
        (prepared / 'personad-linux-amd64.gz').write_bytes(b'changed')
        with self.assertRaises(ValueError):
            self.activate(prepared)
        self.assertFalse((self.downloads / 'latest.json').exists())

    def test_native_from_another_source_cannot_supply_downloads(self):
        binary = self.native / 'personad-darwin-arm64'
        binary.write_bytes(binary.read_bytes() + b'changed')
        with self.assertRaises(ValueError):
            self.prepare()


    def test_native_parent_version_and_staging_links_are_rejected(self):
        parent = self.native.parent
        for path in [self.native, parent / ('.stage-' + self.manifest['version']), parent]:
            exists = path.exists()
            moved = path.with_name(path.name + '-original')
            if exists:
                path.rename(moved)
            else:
                moved.mkdir()
            path.symlink_to(moved, target_is_directory=True)
            with self.subTest(path=path.name), self.assertRaises(ValueError):
                artifacts.prepare_nodes(self.root, self.staged, self.manifest)
            path.unlink()
            if exists:
                moved.rename(path)
            else:
                moved.rmdir()


    def test_unlisted_file_invalidates_managed_download_directory(self):
        destination = self.activate(self.prepare())
        (destination / 'unlisted.json').write_text('{}')
        with self.assertRaisesRegex(ValueError, 'contents'):
            self.prepare()


if __name__ == '__main__':
    unittest.main()
