"""Read-only manager identity checks and isolated artifact staging on a managed Mac."""
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import subprocess


def digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def canonical(path):
    path = Path(path)
    if not path.is_absolute() or '..' in path.parts or path.resolve(strict=True) != path:
        raise ValueError('Manager paths must be canonical and cannot contain symlinks')
    return path


def inspect(target):
    """Do not create a file, modify a service or execute a candidate."""
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        raise ValueError('Manager bootstrap supports Darwin arm64 only')
    if os.getuid() != target['uid']:
        raise ValueError('Manager transport reached a different operating-system identity')
    directory, binary, plist = map(canonical, (target['dir'], target['binary'], target['plist_path']))
    if binary != directory / 'bin/personad':
        raise ValueError('Refusing a shared Hub or node executable')
    config = canonical(directory / 'manager.json')
    if json.loads(config.read_text())['id'] != target['manager_id']:
        raise ValueError('Manager configuration identity differs')
    unit = target['unit']
    service = plistlib.loads(plist.read_bytes())
    if (plist.name != unit + '.plist' or service.get('Label') != unit or
            service.get('ProgramArguments') != [str(binary), 'manager', 'run', '--dir', str(directory)]):
        raise ValueError('Original service label or command differs')
    for field, path in [('previous_sha256', binary), ('config_sha256', config), ('plist_sha256', plist)]:
        if digest(path) != target[field]:
            raise ValueError(f'Manager precondition differs: {field}')
    state = subprocess.check_output(['/bin/launchctl', 'print', f'gui/{target["uid"]}/{unit}'],
                                    text=True, timeout=20)
    match = re.search(r'^\s*pid = ([0-9]+)\s*$', state, re.M)
    if not match:
        raise ValueError('Original manager service has no running process')
    pid = int(match[1])
    image = subprocess.check_output(['/bin/ps', '-p', str(pid), '-o', 'comm='],
                                    text=True, timeout=20).strip()
    uid = subprocess.check_output(['/bin/ps', '-p', str(pid), '-o', 'uid='],
                                  text=True, timeout=20).strip()
    if image != str(binary) or uid != str(target['uid']):
        raise ValueError('Running manager executable or UID differs')
    return {'phase': 'checked', 'manager_id': target['manager_id'], 'pid': pid,
            'binary': str(binary), 'unit': unit, 'config_sha256': target['config_sha256'],
            'plist_sha256': target['plist_sha256'], 'previous_sha256': target['previous_sha256']}


def stage(target, plan, compressed):
    """Only write the candidate transaction directory; installation belongs to the release CLI."""
    inspect(target)
    parent = Path(target['dir']) / '.manager-updates'
    if parent.is_symlink():
        raise ValueError('Update staging cannot be a symlink')
    parent.mkdir(mode=0o700, exist_ok=True)
    canonical(parent)
    directory = parent / plan['run_id']
    directory.mkdir(mode=0o700, exist_ok=False)
    candidate = directory / 'candidate'
    h = hashlib.sha256()
    size = 0
    with gzip.GzipFile(fileobj=compressed) as reader, candidate.open('xb') as writer:
        while chunk := reader.read(1024 * 1024):
            size += len(chunk)
            if size > plan['size']:
                raise ValueError('Candidate exceeds release size')
            h.update(chunk)
            writer.write(chunk)
        writer.flush()
        os.fsync(writer.fileno())
    if size != plan['size'] or h.hexdigest() != plan['binary_sha256']:
        raise ValueError('Staged native checksum or size differs')
    candidate.chmod(0o700)
    metadata = directory / 'plan.json'
    with metadata.open('x') as writer:
        json.dump(plan, writer)
        writer.flush()
        os.fsync(writer.fileno())
    metadata.chmod(0o600)
    return {'candidate': str(candidate), 'plan': str(metadata)}


def refresh_plan(target, plan):
    """Bind fresh idle evidence after transfer, without touching installed configuration."""
    import tempfile
    inspect(target)
    directory = canonical(Path(target['dir']) / '.manager-updates' / plan['run_id'])
    metadata, candidate = canonical(directory / 'plan.json'), canonical(directory / 'candidate')
    previous = json.loads(metadata.read_text())
    mutable = {'idle_verified_at', 'idle_valid_until', 'idle_snapshot_sha256'}
    if {k: v for k, v in previous.items() if k not in mutable} != {k: v for k, v in plan.items() if k not in mutable}:
        raise ValueError('A refreshed idle proof cannot change the activation target or release')
    if candidate.stat().st_size != plan['size'] or digest(candidate) != plan['binary_sha256']:
        raise ValueError('Candidate changed during transfer')
    with tempfile.NamedTemporaryFile(mode='w', dir=directory, prefix='.plan-', delete=False) as writer:
        temporary = Path(writer.name)
        json.dump(plan, writer)
        writer.flush()
        os.fsync(writer.fileno())
    os.replace(temporary, metadata)
    return {'candidate': str(candidate), 'plan': str(metadata)}
