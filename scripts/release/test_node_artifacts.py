import copy
import gzip
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import node_artifacts as artifacts


COMMIT = 'a' * 40
BUILD = 202611070001


def encoded_string(value):
    size, prefix = len(value), bytearray()
    while size >= 128:
        prefix.append((size & 127) | 128)
        size >>= 7
    prefix.append(size)
    return bytes(prefix) + value


def go_binary(platform, commit=COMMIT, program='personad', **overrides):
    system, architecture = platform.split('-')
    header = bytearray(64)
    if platform == 'linux-amd64':
        header[:6] = b'\x7fELF\x02\x01'
        header[18:20] = b'\x3e\x00'
    else:
        header[:8] = b'\xcf\xfa\xed\xfe\x0c\x00\x00\x01'
    settings = {'GOOS': system, 'GOARCH': architecture, 'CGO_ENABLED': '0',
                'vcs.revision': commit, 'vcs.modified': 'false'}
    settings.update(overrides)
    text = 'path\tgithub.com/nwverse/persona/server/cmd/' + program + '\n'
    text += ''.join(f'build\t{key}={value}\n' for key, value in settings.items())
    # The inline module is length-prefixed and wrapped by Go's framing markers.
    start = bytes.fromhex('3077af0c9274080241e1c107e6d618e6')
    end = bytes.fromhex('f932433186182072008242104116d8f2')
    module = start + text.encode() + end
    info = b'\xff Go buildinf:' + b'\x08\x02' + bytes(16)
    return bytes(header) + info + encoded_string(b'go1.24.13') + encoded_string(module)


def release(directory, commit=COMMIT, build=BUILD):
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    manifest = {'version': f'master-{commit[:12]}-{build}', 'build': build,
                'commit': commit, 'files': {}}
    for platform in sorted(artifacts.PLATFORMS):
        binary = go_binary(platform, commit)
        name = f'personad-{platform}-{commit[:12]}.gz'
        compressed = gzip.compress(binary, mtime=0)
        (directory / name).write_bytes(compressed)
        manifest['files'][platform] = {'name': name, 'size': len(compressed),
                                      'sha256': hashlib.sha256(compressed).hexdigest(),
                                      'binary_sha256': hashlib.sha256(binary).hexdigest()}
    (directory / 'node-manifest.json').write_text(json.dumps(manifest))
    return manifest


def health(manifest):
    return {'ok': True, 'version': manifest['version'], 'build': manifest['build']}


def native_result(manifest):
    return json.dumps({'version': manifest['version'], 'os': 'linux',
                       'arch': 'amd64', 'program': 'personad'}).encode()


class NodeArtifactTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'runtime'
        (self.root / 'bin').mkdir(parents=True)
        self.staged = Path(self.temporary.name) / 'staged'
        self.manifest = release(self.staged)
        self.hub = copy.deepcopy(self.manifest)
        self.hub['files'] = {'linux-amd64': self.hub['files']['linux-amd64']}
        self.pointer = self.root / 'bin/nodes'

    def publish(self, manifest=None, staged=None):
        manifest = manifest or self.manifest
        with patch.object(artifacts.subprocess, 'check_output', return_value=native_result(manifest)) as native:
            result = artifacts.publish_nodes(self.root, staged or self.staged, manifest, health(manifest))
        for call in native.call_args_list:
            self.assertEqual(call.args[0][-1], 'node-update-check')
            self.assertIn('linux-amd64', call.args[0][0])
        return result

    def old_publication(self):
        old_staged = Path(self.temporary.name) / 'old'
        old = release(old_staged, '0' * 40, BUILD - 1)
        self.publish(old, old_staged)
        return self.pointer.resolve()

    def test_missing_node_manifest_preserves_hub_only_default(self):
        (self.staged / 'node-manifest.json').unlink()
        self.assertIsNone(artifacts.node_manifest(self.staged, self.hub))
        self.assertFalse(self.pointer.exists())

    def test_manifest_requires_exact_source_version_build_and_platforms(self):
        mutations = [lambda m: m.update(commit='b' * 40), lambda m: m.update(build=BUILD + 1),
                     lambda m: m.update(version='other'), lambda m: m['files'].pop('darwin-arm64'),
                     lambda m: m['files'].update({'windows-amd64': {}}),
                     lambda m: m.update(programs={'other': {}}), lambda m: m.update(nodes={'other': {}}),
                     lambda m: m['files']['linux-amd64'].update(sha256='f' * 64)]
        for change in mutations:
            changed = copy.deepcopy(self.manifest)
            change(changed)
            (self.staged / 'node-manifest.json').write_text(json.dumps(changed))
            with self.subTest(manifest=changed), self.assertRaises(ValueError):
                artifacts.node_manifest(self.staged, self.hub)

    def test_manifest_rejects_unsafe_or_unbound_version(self):
        for version in ['.', '..', '../escape', '/absolute', 'master-aaaaaaaaaaaa-000000000000']:
            candidate, hub = copy.deepcopy(self.manifest), copy.deepcopy(self.hub)
            candidate['version'] = hub['version'] = version
            (self.staged / 'node-manifest.json').write_text(json.dumps(candidate))
            with self.subTest(version=version), self.assertRaises(ValueError):
                artifacts.node_manifest(self.staged, hub)

    def test_real_gzip_and_hashes_are_verified_before_publication(self):
        for kind in ['compressed', 'binary']:
            candidate = copy.deepcopy(self.manifest)
            candidate['files']['darwin-arm64']['sha256' if kind == 'compressed' else 'binary_sha256'] = 'f' * 64
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                artifacts.verify_archive(self.staged, candidate['files']['darwin-arm64'])
        path = artifacts.verify_archive(self.staged, self.manifest['files']['darwin-arm64'])
        self.assertEqual(gzip.decompress(path.read_bytes()), go_binary('darwin-arm64'))

    def test_archive_names_cannot_escape_staged_directory(self):
        for name in ['../escape.gz', '/absolute.gz', 'nested/file.gz', 'not-gzip']:
            candidate = dict(self.manifest['files']['darwin-arm64'], name=name)
            with self.subTest(name=name), self.assertRaises(ValueError):
                artifacts.verify_archive(self.staged, candidate)

    def test_binary_identity_requires_platform_source_program_and_clean_build(self):
        binary = Path(self.temporary.name) / 'binary'
        for platform in sorted(artifacts.PLATFORMS):
            binary.write_bytes(go_binary(platform))
            artifacts.verify_binary(binary, platform, self.manifest)
            cases = [go_binary(platform, commit='b' * 40), go_binary(platform, program='persona-node'),
                     go_binary(platform, GOOS='other'), go_binary(platform, GOARCH='other'),
                     go_binary(platform, CGO_ENABLED='1'), go_binary(platform, **{'vcs.modified': 'true'}),
                     b'not an executable', go_binary(platform)[:-25]]
            for content in cases:
                binary.write_bytes(content)
                with self.subTest(platform=platform, size=len(content)), self.assertRaises(ValueError):
                    artifacts.verify_binary(binary, platform, self.manifest)

    def test_prepare_checks_both_packages_without_changing_pointer(self):
        old = self.old_publication()
        with patch.object(artifacts.subprocess, 'check_output', return_value=native_result(self.manifest)):
            destination = artifacts.prepare_nodes(self.root, self.staged, self.manifest)
        self.assertEqual(self.pointer.resolve(), old)
        self.assertTrue((destination / 'manifest.json').exists())
        artifacts.activate_nodes(self.root, destination, self.manifest, health(self.manifest))
        self.assertEqual(self.pointer.resolve(), destination.resolve())
        self.assertTrue(artifacts.managed_nodes(self.root))

    def test_bad_package_never_replaces_existing_pointer_or_immutable_directory(self):
        old = self.old_publication()
        damaged = self.staged / self.manifest['files']['darwin-arm64']['name']
        damaged.write_bytes(b'damaged')
        with self.assertRaises(ValueError):
            self.publish()
        self.assertEqual(self.pointer.resolve(), old)
        self.assertFalse((self.root / 'releases/nodes' / self.manifest['version']).exists())
        self.assertFalse(list((self.root / 'releases/nodes').glob('.stage-*')))

    def test_native_self_check_failure_preserves_previous_publication(self):
        old = self.old_publication()
        wrong = json.loads(native_result(self.manifest))
        wrong['version'] = 'wrong'
        with patch.object(artifacts.subprocess, 'check_output', return_value=json.dumps(wrong).encode()):
            with self.assertRaises(ValueError):
                artifacts.publish_nodes(self.root, self.staged, self.manifest, health(self.manifest))
        self.assertEqual(self.pointer.resolve(), old)

    def test_immutable_manifest_binary_and_version_cannot_change(self):
        destination = self.publish()
        for mutation in ['manifest', 'binary', 'version']:
            with self.subTest(mutation=mutation):
                original_manifest = (destination / 'manifest.json').read_bytes()
                binary = destination / 'personad-linux-amd64'
                original_binary = binary.read_bytes()
                sidecar = destination / 'personad-linux-amd64.version'
                original_sidecar = sidecar.read_bytes()
                if mutation == 'manifest':
                    changed = copy.deepcopy(self.manifest)
                    changed['extra'] = 'unexpected'
                    (destination / 'manifest.json').write_text(json.dumps(changed))
                elif mutation == 'binary':
                    binary.write_bytes(original_binary + b'changed')
                else:
                    sidecar.write_text('wrong\n')
                with self.assertRaises((RuntimeError, ValueError)):
                    self.publish()
                self.assertEqual(self.pointer.resolve(), destination.resolve())  # macOS: /var is a link to /private/var
                (destination / 'manifest.json').write_bytes(original_manifest)
                binary.write_bytes(original_binary)
                sidecar.write_bytes(original_sidecar)

    def test_bad_version_sidecar_makes_managed_pointer_untrusted(self):
        destination = self.publish()
        (destination / 'personad-darwin-arm64.version').write_text('other\n')
        self.assertFalse(artifacts.managed_nodes(self.root))

    def test_unmanaged_and_outside_pointers_are_never_overwritten(self):
        self.pointer.mkdir()
        self.assertFalse(artifacts.managed_nodes(self.root))
        with self.assertRaises(RuntimeError):
            self.publish()
        self.pointer.rmdir()
        outside = Path(self.temporary.name) / 'outside'
        outside.mkdir()
        self.pointer.symlink_to(outside, target_is_directory=True)
        self.assertFalse(artifacts.managed_nodes(self.root))
        with self.assertRaises(RuntimeError):
            self.publish()
        self.assertEqual(self.pointer.resolve(), outside.resolve())

    def test_activation_waits_for_exact_healthy_hub(self):
        old = self.old_publication()
        with patch.object(artifacts.subprocess, 'check_output', return_value=native_result(self.manifest)):
            destination = artifacts.prepare_nodes(self.root, self.staged, self.manifest)
        for changed in [dict(health(self.manifest), ok=False), dict(health(self.manifest), version='other'),
                        dict(health(self.manifest), build=BUILD + 1)]:
            with self.subTest(health=changed), self.assertRaises(RuntimeError):
                artifacts.activate_nodes(self.root, destination, self.manifest, changed)
            self.assertEqual(self.pointer.resolve(), old)

    def test_source_replaced_after_archive_verification_cannot_be_published(self):
        old = self.old_publication()
        verify = artifacts.verify_archive
        def replaced(directory, artifact):
            source = verify(directory, artifact)
            if 'darwin-arm64' in source.name:
                source.write_bytes(gzip.compress(go_binary('darwin-arm64', commit='b' * 40)))
            return source
        with patch.object(artifacts, 'verify_archive', side_effect=replaced):
            with self.assertRaisesRegex(ValueError, 'Extracted node binary checksum'):
                self.publish()
        self.assertEqual(self.pointer.resolve(), old)
        self.assertFalse((self.root / 'releases/nodes' / self.manifest['version']).exists())
        self.assertFalse(list((self.root / 'releases/nodes').glob('.stage-*')))

    def test_prepared_binary_manifest_or_sidecar_change_prevents_activation(self):
        old = self.old_publication()
        with patch.object(artifacts.subprocess, 'check_output', return_value=native_result(self.manifest)):
            destination = artifacts.prepare_nodes(self.root, self.staged, self.manifest)
        for name in ['manifest.json', 'personad-darwin-arm64', 'personad-darwin-arm64.version']:
            target = destination / name
            original = target.read_bytes()
            if name == 'manifest.json':
                changed = copy.deepcopy(self.manifest)
                changed['extra'] = 'changed'
                target.write_text(json.dumps(changed))
            else:
                target.write_bytes(original + b'changed')
            with self.subTest(file=name), self.assertRaises(ValueError):
                artifacts.activate_nodes(self.root, destination, self.manifest, health(self.manifest))
            self.assertEqual(self.pointer.resolve(), old)
            target.write_bytes(original)

    def test_concurrent_unmanaged_pointer_change_blocks_activation(self):
        self.old_publication()
        with patch.object(artifacts.subprocess, 'check_output', return_value=native_result(self.manifest)):
            destination = artifacts.prepare_nodes(self.root, self.staged, self.manifest)
        self.pointer.unlink()
        self.pointer.mkdir()
        with self.assertRaises(RuntimeError):
            artifacts.activate_nodes(self.root, destination, self.manifest, health(self.manifest))
        self.assertTrue(self.pointer.is_dir())
        self.assertFalse(self.pointer.is_symlink())



class NodeRetentionTests(unittest.TestCase):
    def test_retention_keeps_recent_versions_and_active_previous_pointer(self):
        import os
        from node_artifacts import prune_nodes
        with tempfile.TemporaryDirectory() as directory:
            # The root is reached through a linked ancestor, as validate_release_paths allows (and /var is on macOS).
            (Path(directory) / 'real').mkdir()
            (Path(directory) / 'linked').symlink_to(Path(directory) / 'real', target_is_directory=True)
            root = Path(directory) / 'linked/runtime'
            (root / 'bin').mkdir(parents=True)
            parent = root / 'releases/nodes'
            parent.mkdir(parents=True)
            versions = []
            for index in range(5):
                version = parent / f'master-aaaaaaaaaaaa-20261107000{index}'
                version.mkdir()
                (version / 'manifest.json').write_text('{}')
                os.utime(version, (index + 1, index + 1))
                versions.append(version)
            (root / 'bin/nodes').symlink_to(versions[0])
            unrelated = parent / 'operator-notes'
            unrelated.mkdir()
            prune_nodes(root)
            self.assertTrue(versions[0].exists())
            self.assertFalse(versions[1].exists())
            self.assertTrue(all(p.exists() for p in versions[2:]))
            self.assertTrue(unrelated.exists())

if __name__ == '__main__':
    unittest.main()
