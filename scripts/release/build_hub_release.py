#!/usr/bin/env python3
"""Validate a committed checkout and build a Hub release with optional matching node platforms."""
import argparse
import gzip
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def run(args, cwd, env=None):
    print('+', ' '.join(map(str, args)), flush=True)
    subprocess.run(args, cwd=cwd, env=env, check=True)


def package_release(binary, bin_dir, out, platforms, version, build, sha, root):
    # A retry may leave old content-addressed files in out; only this build's
    # canonical platform names belong in the manifest generator input.
    with tempfile.TemporaryDirectory(prefix='.packages-', dir=out) as directory:
        packages = Path(directory)
        for platform in platforms:
            with (bin_dir / ('personad-' + platform)).open('rb') as src, \
                    gzip.open(packages / ('personad-' + platform + '.gz'), 'wb') as dest:
                shutil.copyfileobj(src, dest)
        run([str(binary), 'release', 'manifest', '--dir', str(packages), '--version', version,
             '--build', build, '--commit', sha, '--out', str(packages / 'manifest.json')], root)
        manifest = json.loads((packages / 'manifest.json').read_text())
        for platform, artifact in manifest['files'].items():
            name = f'personad-{platform}-{artifact["sha256"]}.gz'
            os.replace(packages / artifact['name'], out / name)
            artifact['name'] = name
        return manifest


def write_json(path, value):
    temporary = path.with_suffix('.tmp')
    temporary.write_text(json.dumps(value, indent=2) + '\n')
    os.replace(temporary, path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--build', required=True)
    parser.add_argument('--update-url', required=True)
    parser.add_argument('--publish-nodes', action='store_true')
    parser.add_argument('--publish-managers', action='store_true',
                        help='build matching manager platforms without publishing node packages')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=no'], cwd=root).strip():
        raise RuntimeError('Only committed source may be released')
    run(['python3', 'scripts/privacy_check.py'], root)
    run(['go', 'mod', 'verify'], root / 'server')
    run(['go', 'vet', './...'], root / 'server')
    sys.path.insert(0, str(root / 'scripts/test'))
    from server_test_env import clean_environment
    test_env = clean_environment(os.environ)
    caches = json.loads(subprocess.check_output(
        ['go', 'env', '-json', 'GOCACHE', 'GOMODCACHE', 'GOPATH'], cwd=root / 'server'))
    test_env.update(caches)
    with tempfile.TemporaryDirectory(prefix='hub-release-tests-') as home:
        test_env.update(HOME=home, ZDOTDIR=home)
        run(['go', 'test', '-count=1', './...'], root / 'server', test_env)
        run(['go', 'test', '-race', '-count=1', './internal/hubedge', './internal/update',
             './internal/devices'], root / 'server', test_env)
    for suite in sorted((root / 'scripts').rglob('test_*.py')):
        if 'node_modules' not in suite.parts:
            run([sys.executable, '-B', '-m', 'unittest', '-v', suite.stem], suite.parent)
    run(['npm', 'test'], root / 'integrations/runtime-sdk')
    run(['npm', 'ci', '--no-audit', '--no-fund', '--registry', os.environ.get('PERSONA_NPM_REGISTRY', 'https://registry.npmjs.org/')], root / 'web')
    for script in ['typecheck', 'test', 'build']:
        run(['npm', 'run', script], root / 'web')
    version = f'master-{sha[:12]}-{args.build}'
    webdist = root / 'server/internal/hub/webdist'
    try:
        shutil.copytree(root / 'web/dist', webdist, dirs_exist_ok=True)
        run(['go', 'generate', './internal/runtimepkg'], root / 'server')
        flags = (f'-s -w -X main.version={version} -X main.channel=release '
                 f'-X main.build={args.build} -X main.commit={sha} -X main.updateURL={args.update_url}')
        bin_dir = out / 'bin'
        bin_dir.mkdir(exist_ok=True)
        binary = bin_dir / 'personad-linux-amd64'
        env = dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
        run(['go', 'build', '-tags', 'public', '-trimpath', '-ldflags', flags,
             '-o', str(binary), './cmd/personad'], root / 'server', env)
        platforms = ['linux-amd64']
        if args.publish_nodes or args.publish_managers:
            darwin = bin_dir / 'personad-darwin-arm64'
            run(['go', 'build', '-tags', 'public', '-trimpath', '-ldflags', flags,
                 '-o', str(darwin), './cmd/personad'], root / 'server',
                dict(os.environ, GOOS='darwin', GOARCH='arm64', CGO_ENABLED='0'))
            platforms.append('darwin-arm64')
        run(['python3', 'scripts/release/check_bundle_privacy.py', '--whole-short', str(bin_dir)], root)
        manifest = package_release(binary, bin_dir, out, platforms, version, args.build, sha, root)
        if args.publish_nodes:
            write_json(out / 'node-manifest.json', manifest)
        if args.publish_managers:
            write_json(out / 'manager-manifest.json', manifest)
        manifest['files'] = {'linux-amd64': manifest['files']['linux-amd64']}
        write_json(out / 'latest.json', manifest)
    finally:
        for child in webdist.iterdir():
            if child.name != 'README.md':
                shutil.rmtree(child) if child.is_dir() else child.unlink()


if __name__ == '__main__':
    main()
