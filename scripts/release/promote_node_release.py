"""Install one fixed node candidate through the Hub's restricted protocol relay."""
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import stat
import time
import urllib.error
import urllib.parse
import urllib.request

from hub_release_paths import release_directory, validate_release_paths
from manager_artifacts import manager_manifest
from node_artifacts import prepare_nodes
from pinned_node_release import validate_target, validate_candidate


def relay(root, target, action, params=None):
    cfg = json.loads((root / 'hub.json').read_text())
    port = cfg.get('listen', ':18660').rsplit(':', 1)[1]
    mid = urllib.parse.quote(target['machine_id'], safe='')
    payload = {key: value for key, value in target.items() if key != 'machine_id'}
    payload['params'] = params or {}
    request = urllib.request.Request(
        f'http://127.0.0.1:{port}/api/v1/machines/{mid}/node-update/{action}',
        method='POST', data=json.dumps(payload).encode(),
        headers={'Authorization': 'Bearer ' + cfg['token'], 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=50) as response:
        return json.load(response)


def machine_inventory(root):
    cfg = json.loads((root / 'hub.json').read_text())
    port = cfg.get('listen', ':18660').rsplit(':', 1)[1]
    request = urllib.request.Request(f'http://127.0.0.1:{port}/api/v1/machines',
                                    headers={'Authorization': 'Bearer ' + cfg['token']})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)['machines']


def busy(snapshot):
    rows = snapshot.get('sessions')
    if not isinstance(rows, list):
        raise ValueError('Node did not return a complete session array')
    for row in rows:
        if (row.get('state') in ('working', 'needs_you') or row.get('status') in ('working', 'blocked')
                or row.get('starting') or row.get('pending_interactions', 0) > 0
                or row.get('background') or any(child.get('running') for child in row.get('subagents', []))):
            return True
    return False


def install(root, target, binary, manifest, timeout):
    deadline = time.monotonic() + timeout
    while busy(relay(root, target, 'sessions')):
        if time.monotonic() >= deadline:
            raise TimeoutError('Node remains busy; no update started')
        time.sleep(2)
    with binary.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
    metadata = dict(size=binary.stat().st_size, sha256=digest, version=manifest['version'])
    begin = relay(root, target, 'begin', metadata)
    transaction = begin.get('id')
    offset = begin.get('offset')
    if not isinstance(transaction, str) or not transaction:
        raise ValueError('Node did not return a transaction')
    committed = False
    try:
        if type(offset) is not int or not 0 <= offset <= metadata['size']:
            raise ValueError('Invalid node transfer offset')
        with binary.open('rb') as stream:
            stream.seek(offset)
            while offset < metadata['size']:
                if time.monotonic() >= deadline:
                    raise TimeoutError('Node transfer timed out')
                data = stream.read(min(256 * 1024, metadata['size'] - offset))
                if not data:
                    raise ValueError('Candidate truncated during transfer')
                reply = relay(root, target, 'chunk', dict(id=transaction, offset=offset,
                                                       data=base64.b64encode(data).decode()))
                offset += len(data)
                if reply.get('offset') != offset:
                    raise ValueError('Node acknowledged another offset')
        try:
            accepted = relay(root, target, 'commit', {'id': transaction})
        except urllib.error.HTTPError:
            raise
        except (OSError, TimeoutError):
            # A lost reply is ambiguous. Only a fresh target handshake and the
            # original guardian's confirmation can turn it into success.
            committed = True
        else:
            if accepted.get('accepted') is not True:
                raise ValueError('Node did not accept installation')
            committed = True
    finally:
        if not committed:
            relay(root, target, 'abort', {'id': transaction})
    last_failure = 'no target handshake'
    while time.monotonic() < deadline:
        try:
            info = relay(root, target, 'info')
        except urllib.error.HTTPError as error:
            if error.code not in (502, 503, 504):
                raise
            last_failure = f'HTTP {error.code}'
        except (OSError, TimeoutError) as error:
            last_failure = type(error).__name__
        else:
            if info.get('version') == manifest['version']:
                if 'manager_update_inventory_v1' not in info.get('capabilities', []):
                    raise ValueError('Replacement lacks the required inventory producer capability')
                relay(root, target, 'sessions')
                print(json.dumps(dict(phase='applied', version=manifest['version'])), flush=True)
                relay(root, target, 'confirm', {'version': manifest['version']})
                return info
        time.sleep(2)
    raise TimeoutError('Node not confirmed; its existing recovery guardian owns rollback: ' + last_failure)


def promote(payload):
    root, staged = Path(payload['root']), Path(payload['staged'])
    namespace = payload.get('release_namespace', 'hub')
    validate_release_paths(root, namespace, staged, create=True)
    target = validate_target(payload['target'])
    lock_path = release_directory(root) / '.publish.lock'
    descriptor = os.open(lock_path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    with os.fdopen(descriptor, 'w') as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
            raise ValueError('Publication lock must be a regular file')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        opened, current = os.fstat(lock.fileno()), lock_path.lstat()
        if (opened.st_dev, opened.st_ino) != (current.st_dev, current.st_ino):
            raise ValueError('Publication lock changed')
        validate_release_paths(root, namespace, staged)
        cfg = json.loads((root / 'hub.json').read_text())
        if cfg.get('auto_update_nodes', False) is not False:
            raise ValueError('Automatic fleet updates must remain disabled')
        before = machine_inventory(root)
        if any(machine.get('deploying') for machine in before):
            raise ValueError('A node deployment already owns the window')
        matches = [machine for machine in before if machine.get('id') == target['machine_id']]
        if len(matches) != 1 or matches[0].get('machine_uid') != target['machine_uid']:
            raise ValueError('The selected machine identity is not present')
        manifest = manager_manifest(staged, payload['source'])
        if manifest is None:
            raise ValueError('Fixed full release manifest is missing')
        validate_candidate(staged, manifest, target, payload.get('candidate'))
        original = relay(root, target, 'info')
        result = dict(phase='checked', source=manifest['commit'], version=manifest['version'],
                      machine_id=target['machine_id'], machine_uid=target['machine_uid'],
                      run_id=payload['run_id'], target_identity=payload['target_identity'])
        if not payload.get('apply', False):
            return result
        timeout = payload.get('timeout', 900)
        if type(timeout) is not int or not 1 <= timeout <= 1800:
            raise ValueError('Node update timeout must be between 1 and 1800 seconds')
        prepared = prepare_nodes(root, staged, manifest)
        binary = prepared / f"personad-{target['os']}-{target['arch']}"
        if original.get('version') == manifest['version']:
            raise ValueError('Node already reports the target version; this invocation has no native hash transfer proof')
        install(root, target, binary, manifest, timeout)
        after = machine_inventory(root)
        def others(rows):
            return {row['id']: row.get('node_version') for row in rows if row['id'] != target['machine_id']}
        if others(before) != others(after):
            raise ValueError('Other node versions changed during the publication window')
        result['phase'] = 'confirmed'
        return result
