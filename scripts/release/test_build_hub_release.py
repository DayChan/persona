"""Interrupted build leftovers cannot become extra node platforms on retry."""
import gzip
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import types

import build_hub_release as builder


class PackageReleaseTests(unittest.TestCase):
    def test_retry_manifest_only_sees_this_builds_canonical_archives(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            binaries = out / 'bin'
            binaries.mkdir()
            platforms = ['linux-amd64', 'darwin-arm64']
            for platform in platforms:
                (binaries / ('personad-' + platform)).write_bytes(platform.encode())
            stale = out / ('personad-linux-amd64-' + 'a' * 64 + '.gz')
            stale.write_bytes(b'interrupted old build')

            def generate(args, root):
                source = Path(args[args.index('--dir') + 1])
                self.assertEqual({p.name for p in source.iterdir()},
                                 {'personad-' + p + '.gz' for p in platforms})
                files = {}
                for platform in platforms:
                    name = 'personad-' + platform + '.gz'
                    data = (source / name).read_bytes()
                    files[platform] = {'name': name, 'size': len(data),
                        'sha256': hashlib.sha256(data).hexdigest(),
                        'binary_sha256': hashlib.sha256(gzip.decompress(data)).hexdigest()}
                (source / 'manifest.json').write_text(json.dumps({'files': files}))

            with patch.object(builder, 'run', generate):
                manifest = builder.package_release(binaries / 'personad-linux-amd64',
                    binaries, out, platforms, 'fixture', '202611070001', 'a' * 40, out)
            self.assertEqual(set(manifest['files']), set(platforms))
            self.assertEqual(stale.read_bytes(), b'interrupted old build')
            self.assertFalse(list(out.glob('.packages-*')))
            for platform, artifact in manifest['files'].items():
                self.assertEqual(gzip.decompress((out / artifact['name']).read_bytes()), platform.encode())


class ManagerPackageScopeTests(unittest.TestCase):
    def build(self, manager=False):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        webdist = root / 'server/internal/hub/webdist'
        webdist.mkdir(parents=True)
        (webdist / 'README.md').write_text('fixture')
        out = root / 'artifacts'
        calls = []
        manifest = dict(version='master-' + 'a' * 12 + '-202611070001',
                        build=202611070001, commit='a' * 40,
                        files={'linux-amd64': {}, 'darwin-arm64': {}})

        def output(args, cwd, **kwargs):
            if args[:2] == ['go', 'env']:
                return b'{"GOCACHE":"/example/cache","GOMODCACHE":"/example/mod","GOPATH":"/example/go"}'
            return 'a' * 40 if args[1] == 'rev-parse' else b''

        environment = types.ModuleType('server_test_env')
        environment.clean_environment = lambda values: dict(values)
        argv = ['builder', '--out', str(out), '--build', '202611070001',
                '--update-url', 'https://example.invalid/dl']
        if manager:
            argv.append('--publish-managers')
        with patch('sys.argv', argv), patch.dict('sys.modules', server_test_env=environment), \
                patch.object(builder, '__file__', str(root / 'scripts/release/build_hub_release.py')), \
                patch.object(builder.subprocess, 'check_output', output), \
                patch.object(builder, 'run', lambda args, cwd, env=None: calls.append((args, env))), \
                patch.object(builder.shutil, 'copytree'), \
                patch.object(builder, 'package_release', return_value=manifest) as package:
            builder.main()
        return out, calls, package.call_args.args[3]

    def test_manager_build_has_two_platforms_and_no_node_manifest(self):
        out, calls, platforms = self.build(manager=True)
        self.assertEqual(platforms, ['linux-amd64', 'darwin-arm64'])
        self.assertEqual(set(json.loads((out / 'manager-manifest.json').read_text())['files']),
                         {'linux-amd64', 'darwin-arm64'})
        self.assertEqual(set(json.loads((out / 'latest.json').read_text())['files']), {'linux-amd64'})
        self.assertFalse((out / 'node-manifest.json').exists())
        builds = [(args, env) for args, env in calls if args[:2] == ['go', 'build']]
        self.assertEqual([env['GOOS'] for args, env in builds], ['linux', 'darwin'])
        self.assertEqual(builds[0][0][builds[0][0].index('-ldflags') + 1],
                         builds[1][0][builds[1][0].index('-ldflags') + 1])
        self.assertTrue(any(args[:2] == ['go', 'vet'] for args, _ in calls))
        self.assertTrue(any(args[:3] == ['go', 'test', '-count=1'] for args, _ in calls))
        self.assertTrue(any('scripts/release/check_bundle_privacy.py' in args for args, _ in calls))

    def test_default_hub_build_retains_single_platform(self):
        out, calls, platforms = self.build()
        self.assertEqual(platforms, ['linux-amd64'])
        self.assertFalse((out / 'manager-manifest.json').exists())
        self.assertFalse((out / 'node-manifest.json').exists())


if __name__ == '__main__':
    unittest.main()
