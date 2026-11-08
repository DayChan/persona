#!/usr/bin/env python3
"""Remote Hub publication with optional matching node packages: invoked over authenticated SSH by the publisher."""
import base64
import fcntl
import gzip
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
from pathlib import Path
import time
import urllib.request

from hub_release_paths import release_directory, validate_release_paths
from node_artifacts import activate_nodes, managed_nodes, node_manifest, prepare_nodes, prune_nodes
from node_package_mirror import prepare_downloads, activate_downloads


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def validate_next(previous, candidate):
    if set(candidate['files']) != {'linux-amd64'} or candidate.get('programs'):
        raise ValueError('Only the Hub platform is permitted')
    if previous:
        if candidate == previous:
            return
        if candidate['build'] <= previous['build']:
            raise ValueError('Release build must increase')
        key = bytes.fromhex(previous['commit'][:12])
        ancestors = base64.b64decode(candidate['ancestors'], validate=True)
        if not ancestors or len(ancestors) % 6:
            raise ValueError('Invalid ancestor list')
        if key not in [ancestors[i:i+6] for i in range(0, len(ancestors), 6)]:
            raise ValueError('Candidate does not contain previous published commit')


def api(root, path, method='GET'):
    cfg = json.loads((root / 'hub.json').read_text())
    address = cfg.get('listen', ':18660')
    port = address.rsplit(':', 1)[1]
    req = urllib.request.Request(f'http://127.0.0.1:{port}/api/v1/{path}',
                                 method=method, data=b'{}' if method == 'POST' else None,
                                 headers={'Authorization': 'Bearer ' + cfg['token'],
                                          'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=30) as response:
        return json.load(response)


def nodes(root):
    return {m['id']: {'version': m.get('node_version'), 'online': m.get('online'),
                     'deploying': bool(m.get('deploying'))}
            for m in api(root, 'machines')['machines'] if m.get('kind') != 'local'}


def promote(payload):
    root, staged = Path(payload['root']), Path(payload['staged'])
    namespace = payload.get('release_namespace', 'hub')
    releases = validate_release_paths(root, namespace, staged, create=True)
    # Every feed shares the existing lock because the installed Hub and node pointer are global.
    lock_path = release_directory(root) / '.publish.lock'
    descriptor = os.open(lock_path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    with os.fdopen(descriptor, 'w') as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
            raise ValueError('Publication lock must be a regular file')
        fcntl.flock(lock, fcntl.LOCK_EX)
        validate_release_paths(root, namespace, staged)
        opened, current = os.fstat(lock.fileno()), lock_path.lstat()
        if (opened.st_dev, opened.st_ino) != (current.st_dev, current.st_ino):
            raise ValueError('Publication lock changed while acquiring it')
        cfg = json.loads((root / 'hub.json').read_text())
        if cfg.get('auto_update_nodes', False) is not False:
            raise RuntimeError('Hub-only policy must be disabled explicitly or by default')
        if not managed_nodes(root):
            raise RuntimeError('Unmanaged node packages must remain isolated')
        state = api(root, 'hub/update')
        if not state['auto'] or not state['can_update']:
            raise RuntimeError('Bootstrap self-update source and enable automatic updates first')
        before = nodes(root)
        if any(m['deploying'] for m in before.values()):
            raise RuntimeError('Node deployment in progress')
        manifest = json.loads((staged / 'latest.json').read_text())
        packages = node_manifest(staged, manifest) if payload.get('publish_nodes', False) else None
        if payload.get('publish_nodes', False) and packages is None:
            raise ValueError('Matching node packages are required by this publisher')
        latest_state = state.get('latest') or {}
        if latest_state.get('build') == manifest['build'] and latest_state.get('bad'):
            raise RuntimeError('This Hub build already rolled back; publish a corrected commit')
        latest = releases / 'latest.json'
        previous = json.loads(latest.read_text()) if latest.exists() else None
        validate_next(previous, manifest)
        artifact = manifest['files']['linux-amd64']
        name = artifact['name']
        if Path(name).name != name or not name.endswith('.gz'):
            raise ValueError('Invalid artifact name')
        source = staged / name
        if source.stat().st_size != artifact['size'] or digest(source) != artifact['sha256']:
            raise ValueError('Compressed artifact checksum mismatch')
        with gzip.open(source, 'rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != artifact['binary_sha256']:
                raise ValueError('Binary checksum mismatch')
        destination = releases / name
        if destination.exists() and digest(destination) != artifact['sha256']:
            raise ValueError('Immutable artifact changed')
        if not destination.exists():
            temporary = releases / ('.' + name + '.next')
            with source.open('rb') as reader, temporary.open('wb') as writer:
                shutil.copyfileobj(reader, writer)
                writer.flush()
                os.fsync(writer.fileno())
            if digest(temporary) != artifact['sha256']:
                raise ValueError('Copied Hub artifact checksum mismatch')
            os.replace(temporary, destination)
        prepared = prepare_nodes(root, staged, packages) if packages else None
        downloads = prepare_downloads(root, namespace, staged, packages, prepared) if packages else None
        os.replace(staged / 'latest.json', latest)
        descriptor = os.open(releases, os.O_RDONLY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        print(json.dumps({'phase': 'published', 'commit': manifest['commit'],
                          'build': manifest['build'], 'binary_sha256': artifact['binary_sha256']}), flush=True)
        checked = api(root, 'hub/update/check', 'POST')
        if (checked.get('latest') or {}).get('bad'):
            raise RuntimeError('This Hub build already rolled back; publish a corrected commit')
        deadline = time.monotonic() + payload.get('timeout', 1200)
        while time.monotonic() < deadline:
            try:
                health = api(root, 'health')
                status = api(root, 'hub/update')
            except (OSError, ValueError) as error:
                print('Waiting for slot:', type(error).__name__, flush=True)
                time.sleep(5)
                continue
            if status['state'] in {'failed', 'rolled_back'}:
                raise RuntimeError(f'Update {status["state"]}: {status.get("error", "")}')
            if (health.get('ok') and health.get('build') == manifest['build']
                    and health.get('version') == manifest['version'] and status['confirmed']):
                after = nodes(root)
                if set(before) != set(after) or any(
                        after[k]['version'] != v['version'] or after[k]['deploying']
                        or (v['online'] and not after[k]['online']) for k, v in before.items()):
                    raise RuntimeError('Node inventory changed during Hub-only update')
                if digest(root / 'bin/personad') != artifact['binary_sha256']:
                    raise RuntimeError('Installed binary checksum mismatch')
                # Keep the newest three immutable downloads; a file already open by
                # an updater remains readable, and rollback uses bin/personad.prev.
                artifacts = sorted(releases.glob('personad-linux-amd64-*.gz'), key=lambda p: p.stat().st_mtime)
                for old in artifacts[:-3]:
                    if old != destination:
                        old.unlink()
                slot = subprocess.check_output([str(root / 'bin/personad'), 'hub', 'switch', '--status'],
                                               text=True, env=dict(os.environ, PERSONA_HOME=str(root)))
                match = re.search(r'pid (\d+)', slot)
                if not match or digest(Path('/proc') / match[1] / 'exe') != artifact['binary_sha256']:
                    raise RuntimeError('Serving process checksum mismatch')
                if packages:
                    activate_nodes(root, prepared, packages, health)
                    published_downloads = activate_downloads(root, namespace, downloads, packages, health)
                    prune_nodes(root)
                elif not managed_nodes(root):
                    raise RuntimeError('Unmanaged node packages were created')
                history = api(root, 'hub/update/history')
                download_receipt = ({'directory': str(published_downloads.relative_to(root)),
                                     'manifest_sha256': digest(published_downloads / 'manifest.json'),
                                     'version': packages['version'], 'commit': packages['commit'],
                                     'build': packages['build']} if packages else None)
                print(json.dumps({'node_packages': download_receipt, 'phase':'confirmed', 'health': health, 'nodes':after,
                                  'history':history, 'slot':slot.strip(), 'sha256':artifact['binary_sha256']}), flush=True)
                shutil.rmtree(staged)
                for abandoned in releases.glob('.upload-*'):
                    if (re.fullmatch(r'\.upload-[0-9]{12}-[0-9a-f]{32}', abandoned.name)
                            and abandoned.is_dir() and not abandoned.is_symlink()
                            and time.time() - abandoned.stat().st_mtime > 86400):
                        shutil.rmtree(abandoned)
                return
            time.sleep(5)
        raise TimeoutError('Published release not confirmed; leave updater/rollback in control')
