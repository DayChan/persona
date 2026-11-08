"""Manager-only transaction, executed by the original publisher while holding its publication lock."""
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import time
from urllib.error import HTTPError, URLError

from hub_release_paths import release_directory, validate_release_paths
from manager_artifacts import manager_manifest
from manager_idle import collect, SnapshotPending
from node_artifacts import verify_binary
from promote_hub_release import api, nodes


def snapshot_api(root, path):
    try:
        return api(root, path)
    except HTTPError as error:
        if error.code not in (502, 503, 504):
            raise
        raise SnapshotPending('Manager reconnect is temporarily unavailable') from error
    except (URLError, TimeoutError) as error:
        raise SnapshotPending('Manager reconnect request is temporarily unavailable') from error


def canonical_json(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def transport_argv(config):
    """No inferred host, credentials, reverse connection or trust-on-first-use."""
    expected = {'kind', 'host', 'port', 'user', 'identity_file', 'known_hosts_file'}
    kind = config.get('kind')
    if kind == 'cloudflare-ssh':
        expected |= {'cloudflared_path', 'cloudflared_sha256'}
    if set(config) != expected or kind not in ('ssh', 'cloudflare-ssh'):
        raise ValueError('An explicit supported SSH transport is required')
    if (not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.:-]*', config['host']) or
            not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]*', config['user'])):
        raise ValueError('Invalid explicit SSH endpoint or user')
    if type(config['port']) is not int or not 1 <= config['port'] <= 65535:
        raise ValueError('Invalid explicit SSH port')
    for key in ('identity_file', 'known_hosts_file'):
        path = Path(config[key])
        if not path.is_absolute() or path.resolve(strict=True) != path or not path.is_file():
            raise ValueError(f'Explicit transport prerequisite missing: {key}')
    command = ['ssh', '-T', '-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
            '-o', 'IdentitiesOnly=yes', '-o', 'ConnectTimeout=15', '-o', 'GlobalKnownHostsFile=/dev/null',
            '-o', f'UserKnownHostsFile={config["known_hosts_file"]}', '-i', config['identity_file']]
    if kind == 'cloudflare-ssh':
        if (config['port'] != 22 or len(config['host']) > 253 or
                not re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?', config['host'])):
            raise ValueError('Cloudflare SSH requires an explicit hostname and origin SSH port 22')
        executable = Path(config['cloudflared_path'])
        # OpenSSH expands percent tokens before invoking its fixed proxy command.
        if (any(ord(c) < 32 or c == '%' for c in str(executable)) or
                not executable.is_absolute() or executable.resolve(strict=True) != executable or
                not executable.is_file() or not os.access(executable, os.X_OK)):
            raise ValueError('Cloudflare SSH requires a canonical executable cloudflared_path on the Hub')
        digest = config['cloudflared_sha256']
        if not re.fullmatch(r'[0-9a-f]{64}', digest):
            raise ValueError('Cloudflare SSH requires an explicit cloudflared SHA256')
        with executable.open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != digest:
                raise ValueError('Cloudflare SSH executable differs from its pinned SHA256')
        proxy = shlex.join([str(executable), 'access', 'ssh', '--hostname', config['host']])
        command.extend(['-o', 'ProxyCommand=' + proxy])
    return [*command, '-p', str(config['port']), '-l', config['user'], config['host']]


def remote(argv, command, data=None, timeout=30):
    result = subprocess.run([*argv, shlex.join(command)], input=data, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, check=False)
    if result.returncode:
        # Transport diagnostics can contain private paths; they stay in the private lane log.
        raise RuntimeError('Manager transport/command failed: ' + result.stderr.decode(errors='replace')[-2000:])
    return json.loads(result.stdout)


def inspect_target(argv, target, target_source):
    source = target_source + '\nprint(json.dumps(inspect(' + repr(target) + ')))\n'
    return remote(argv, ['python3', '-c', source])


def snapshot(root, target, require_idle=True):
    return collect(root, target, snapshot_api, require_idle=require_idle)


def make_plan(target, manifest, run_id, idle_sha, now):
    artifact = manifest['files']['darwin-arm64']
    plan = {key: target[key] for key in ('manager_id', 'dir', 'binary', 'unit', 'plist_path', 'uid',
            'previous_sha256', 'config_sha256', 'plist_sha256')}
    plan.update(schema=1, run_id=run_id, source=manifest['commit'], version=manifest['version'],
                binary_sha256=artifact['binary_sha256'], size=target['candidate_size'],
                activation_enabled=target.get('activation_enabled', False),
                bootstrap_authorized=target.get('bootstrap_authorized', False),
                bootstrap_window_until=target.get('bootstrap_window_until'),
                freeze=target.get('freeze', True), idle_snapshot_sha256=idle_sha,
                idle_verified_at=now.isoformat(), idle_valid_until=(now + dt.timedelta(seconds=60)).isoformat())
    return plan


def validate_activation(target, now):
    if (target.get('freeze', True) or target.get('activation_enabled') is not True or
            target.get('bootstrap_authorized') is not True):
        raise ValueError('Manager activation is frozen, disabled or lacks a bootstrap window')
    until = dt.datetime.fromisoformat(target['bootstrap_window_until'].replace('Z', '+00:00'))
    if not now < until <= now + dt.timedelta(minutes=15):
        raise ValueError('A fresh explicit bootstrap maintenance window is required')


def promote(payload):
    root = Path(payload['root'])
    target, run_id = payload['target'], payload['run_id']
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,80}', run_id):
        raise ValueError('Invalid manager run ID')
    if not re.fullmatch(r'm-[a-z0-9-]{1,29}', target['manager_id']):
        raise ValueError('Explicit independent manager ID is required')
    if payload.get('apply'):
        validate_activation(target, dt.datetime.now(dt.timezone.utc))
    # The original Hub publisher owns this path. A read-only check never creates it.
    lock_path = release_directory(root) / '.publish.lock'
    descriptor = os.open(lock_path, os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, 'r+') as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
            raise ValueError('Publication lock must be a regular file')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        opened, current = os.fstat(lock.fileno()), lock_path.lstat()
        if (opened.st_dev, opened.st_ino) != (current.st_dev, current.st_ino):
            raise ValueError('Publication lock identity changed')
        cfg = json.loads((root / 'hub.json').read_text())
        if cfg.get('auto_update_nodes', False) is not False:
            raise ValueError('Node automatic upgrades must remain disabled')
        before_nodes = nodes(root)
        if any(m['deploying'] for m in before_nodes.values()):
            raise ValueError('Node deployment is already in progress')
        argv = transport_argv(target.get('transport', {}))
        inspected = inspect_target(argv, target, payload['target_source'])
        _, before_identity, idle_sha = snapshot(root, target)
        receipt = dict(inspected, schema=1, run_id=run_id, source=payload['source'])
        if not payload.get('apply'):
            print(json.dumps(receipt), flush=True)
            return receipt
        staged = Path(payload['staged'])
        validate_release_paths(root, payload.get('release_namespace', 'hub'), staged)
        manifest = manager_manifest(staged, payload['source'])
        if manifest is None:
            raise ValueError('Same-source formal manager manifest is required')
        artifact = manifest['files']['darwin-arm64']
        binary = staged / 'manager-candidate'
        # Publisher creates this only in its immutable attempt staging area.
        import gzip
        import shutil
        with gzip.open(staged / artifact['name'], 'rb') as src, binary.open('xb') as dest:
            shutil.copyfileobj(src, dest)
        verify_binary(binary, 'darwin-arm64', manifest)
        target = dict(target, candidate_size=binary.stat().st_size)
        _, identity, idle_sha = snapshot(root, target)
        if identity != before_identity:
            raise ValueError('Manager identity changed before staging')
        now = dt.datetime.now(dt.timezone.utc)
        validate_activation(target, now)
        plan = make_plan(target, manifest, run_id, idle_sha, now)
        script = payload['target_source'] + '\nprint(json.dumps(stage(' + repr(target) + ', ' + repr(plan) + ', __import__("sys").stdin.buffer)))\n'
        staged_target = remote(argv, ['python3', '-c', script], (staged / artifact['name']).read_bytes(), timeout=180)
        command = [staged_target['candidate'], 'manager', 'update']
        remote(argv, [*command, 'check', '--plan', staged_target['plan']])
        _, identity, idle_sha = snapshot(root, target)
        if identity != before_identity:
            raise ValueError('Manager identity changed before activation')
        # A large transfer must not consume the initial idle proof's lifetime.
        now = dt.datetime.now(dt.timezone.utc)
        validate_activation(target, now)
        plan = make_plan(target, manifest, run_id, idle_sha, now)
        refresh = payload['target_source'] + '\nprint(json.dumps(refresh_plan(' + repr(target) + ', ' + repr(plan) + ')))\n'
        remote(argv, ['python3', '-c', refresh])
        remote(argv, [*command, 'start', '--plan', staged_target['plan']], timeout=45)
        deadline = time.monotonic() + 240
        reconnect_deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            status = remote(argv, [*command, 'status', '--plan', staged_target['plan']])
            if status.get('phase') == 'rollback':
                raise RuntimeError('Manager update rolled back: ' + status.get('error', ''))
            if status.get('phase') == 'applied' and status.get('ready'):
                try:
                    try:
                        manager, identity, _ = snapshot(root, target, require_idle=False)
                    except SnapshotPending as pending:
                        if time.monotonic() >= reconnect_deadline:
                            raise TimeoutError('Manager reconnect/resync did not complete within 60 seconds') from pending
                        time.sleep(2)
                        continue
                    if manager.get('version') != manifest['version'] or identity != before_identity:
                        raise RuntimeError('Manager live version or preserved identity differs')
                    if nodes(root) != before_nodes:
                        raise RuntimeError('Node inventory changed during manager-only publication')
                    remote(argv, [*command, 'confirm', '--plan', staged_target['plan']])
                except Exception as health_error:
                    # The watchdog also rolls back if the transport has disconnected.
                    try:
                        remote(argv, [*command, 'rollback', '--plan', staged_target['plan']])
                    except Exception as rollback_error:
                        raise ExceptionGroup('Manager health failed and rollback request could not be delivered',
                                             [health_error, rollback_error])
                    raise
            if status.get('phase') == 'confirmed':
                receipt.update(status, source=manifest['commit'], phase='confirmed', transport='ssh',
                               identity_sha256=hashlib.sha256(canonical_json(before_identity)).hexdigest())
                print(json.dumps(receipt), flush=True)
                return receipt
            time.sleep(2)
        raise TimeoutError('Manager confirmation timed out; the launchd controller owns rollback')
