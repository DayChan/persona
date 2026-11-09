"""Validate and atomically publish immutable node packages beside a confirmed Hub."""
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess

PLATFORMS = {'linux-amd64', 'darwin-arm64'}


def digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def node_manifest(artifact_dir, hub_manifest):
    path = Path(artifact_dir) / 'node-manifest.json'
    if not path.exists():
        return None
    manifest = json.loads(path.read_text())
    for key in ('version', 'build', 'commit'):
        if manifest.get(key) != hub_manifest.get(key):
            raise ValueError('Node source differs from the Hub release')
    if set(manifest.get('files', {})) != PLATFORMS or manifest.get('programs') or manifest.get('nodes'):
        raise ValueError('Unexpected node platform or program')
    if not re.fullmatch(r'master-[0-9a-f]{12}-[0-9]{12}', manifest['version']):
        raise ValueError('Invalid node version')
    if not re.fullmatch(r'[0-9a-f]{40}', manifest['commit']):
        raise ValueError('Invalid node source')
    if manifest['version'] != f"master-{manifest['commit'][:12]}-{manifest['build']}":
        raise ValueError('Node version is not bound to its source and build')
    if manifest['files']['linux-amd64'] != hub_manifest['files']['linux-amd64']:
        raise ValueError('Linux node must be the verified Hub binary')
    return manifest


def verify_archive(directory, artifact):
    name = artifact['name']
    if Path(name).name != name or not name.endswith('.gz'):
        raise ValueError('Invalid node artifact name')
    source = Path(directory) / name
    if not stat.S_ISREG(source.lstat().st_mode):
        raise ValueError('Node archive must be a regular non-symlink file')
    if source.stat().st_size != artifact['size'] or digest(source) != artifact['sha256']:
        raise ValueError('Node compressed checksum mismatch')
    with gzip.open(source, 'rb') as stream:
        if hashlib.file_digest(stream, 'sha256').hexdigest() != artifact['binary_sha256']:
            raise ValueError('Node binary checksum mismatch')
    return source


def verify_binary(path, platform, manifest):
    """Read the executable header and inline Go build info without executing foreign code."""
    data = Path(path).read_bytes()
    if platform == 'linux-amd64':
        if data[:6] != b'\x7fELF\x02\x01' or data[18:20] != b'\x3e\x00':
            raise ValueError('Node executable platform mismatch')
    elif platform == 'darwin-arm64':
        if data[:8] != b'\xcf\xfa\xed\xfe\x0c\x00\x00\x01':
            raise ValueError('Node executable platform mismatch')
    else:
        raise ValueError('Unexpected node platform')
    start = data.find(b'\xff Go buildinf:')
    if start < 0 or start % 16 or data[start + 14:start + 16] != b'\x08\x02':
        raise ValueError('Unsupported Go build info')

    def read_string(position):
        size = 0
        for shift in range(0, 70, 7):
            if position >= len(data):
                raise ValueError('Truncated Go build info')
            value = data[position]
            position += 1
            size |= (value & 127) << shift
            if value < 128:
                end = position + size
                if end > len(data):
                    raise ValueError('Truncated Go build info')
                return data[position:end], end
        raise ValueError('Invalid Go build info length')

    _, position = read_string(start + 32)
    module, _ = read_string(position)
    # Go surrounds the module text with fixed 16-byte framing markers.
    lines = module[16:-16].decode().splitlines()
    settings = dict(line[6:].split('=', 1) for line in lines if line.startswith('build\t'))
    system, architecture = platform.split('-')
    expected = {'GOOS': system, 'GOARCH': architecture, 'CGO_ENABLED': '0',
                'vcs.revision': manifest['commit'], 'vcs.modified': 'false'}
    if (not lines or lines[0] != 'path\tgithub.com/nwverse/persona/server/cmd/personad'
            or any(settings.get(key) != value for key, value in expected.items())):
        raise ValueError('Node source or build platform mismatch')


def managed_nodes(root):
    """An existing package pointer is trusted only with its exact immutable manifest."""
    root = Path(root)
    pointer = root / 'bin/nodes'
    if not pointer.exists() and not pointer.is_symlink():
        return True
    if not pointer.is_symlink():
        return False
    directory = pointer.resolve(strict=True)
    if directory.parent != (root / 'releases/nodes').resolve():
        return False
    manifest = json.loads((directory / 'manifest.json').read_text())
    if manifest.get('version') != directory.name or set(manifest.get('files', {})) != PLATFORMS:
        return False
    for platform, artifact in manifest['files'].items():
        binary = directory / ('personad-' + platform)
        if (directory / (binary.name + '.version')).read_text().strip() != manifest['version']:
            return False
        if digest(binary) != artifact['binary_sha256']:
            return False
        verify_binary(binary, platform, manifest)
    return True


def prepare_nodes(root, staged, manifest):
    root, staged = Path(root), Path(staged)
    if not managed_nodes(root):
        raise RuntimeError('Unmanaged node package directory must remain isolated')
    parent = root / 'releases/nodes'
    parent.mkdir(parents=True, exist_ok=True)
    if not stat.S_ISDIR(parent.lstat().st_mode):
        raise ValueError('Node release parent must be a non-symlink directory')
    destination = parent / manifest['version']
    temporary = parent / ('.stage-' + manifest['version'])
    if destination.is_symlink() or temporary.is_symlink():
        raise ValueError('Node release directories must not be symlinks')
    if destination.exists():
        if json.loads((destination / 'manifest.json').read_text()) != manifest:
            raise ValueError('Immutable node manifest changed')
        for platform, artifact in manifest['files'].items():
            binary = destination / ('personad-' + platform)
            if (destination / (binary.name + '.version')).read_text().strip() != manifest['version']:
                raise ValueError('Immutable node version changed')
            verify_binary(binary, platform, manifest)
            if digest(binary) != artifact['binary_sha256']:
                raise ValueError('Immutable node binary changed')
    else:
        if temporary.exists():
            shutil.rmtree(temporary)
        temporary.mkdir(mode=0o700)
        try:
            for platform, artifact in manifest['files'].items():
                source = verify_archive(staged, artifact)
                binary = temporary / ('personad-' + platform)
                with gzip.open(source, 'rb') as reader, binary.open('wb') as writer:
                    shutil.copyfileobj(reader, writer)
                    writer.flush()
                    os.fsync(writer.fileno())
                if digest(binary) != artifact['binary_sha256']:
                    raise ValueError('Extracted node binary checksum mismatch')
                binary.chmod(0o755)
                verify_binary(binary, platform, manifest)
                if platform == 'linux-amd64':
                    checked = json.loads(subprocess.check_output([str(binary), 'node-update-check'], timeout=20))
                    expected = dict(version=manifest['version'], os='linux', arch='amd64', program='personad')
                    if checked != expected:
                        raise ValueError('Native node self-check differs from its manifest')
                with (temporary / (binary.name + '.version')).open('w') as sidecar:
                    sidecar.write(manifest['version'] + '\n')
                    sidecar.flush()
                    os.fsync(sidecar.fileno())
            with (temporary / 'manifest.json').open('w') as metadata:
                metadata.write(json.dumps(manifest, indent=2) + '\n')
                metadata.flush()
                os.fsync(metadata.fileno())
            descriptor = os.open(temporary, os.O_RDONLY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
            os.replace(temporary, destination)
            descriptor = os.open(parent, os.O_RDONLY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        except Exception:
            if temporary.exists():
                shutil.rmtree(temporary)
            raise
    return destination


def activate_nodes(root, destination, manifest, health):
    root, destination = Path(root), Path(destination)
    if not health.get('ok') or health.get('version') != manifest['version'] or health.get('build') != manifest['build']:
        raise RuntimeError('The matching Hub must be healthy before node publication')
    if not managed_nodes(root):
        raise RuntimeError('Unmanaged node package directory must remain isolated')
    if destination != root / 'releases/nodes' / manifest['version']:
        raise ValueError('Invalid prepared node directory')
    if json.loads((destination / 'manifest.json').read_text()) != manifest:
        raise ValueError('Prepared node manifest changed')
    for platform, artifact in manifest['files'].items():
        binary = destination / ('personad-' + platform)
        if digest(binary) != artifact['binary_sha256']:
            raise ValueError('Prepared node binary changed')
        if (destination / (binary.name + '.version')).read_text().strip() != manifest['version']:
            raise ValueError('Prepared node version changed')
        verify_binary(binary, platform, manifest)
    pointer = root / 'bin/nodes'
    next_pointer = root / 'bin/.nodes-next'
    next_pointer.unlink(missing_ok=True)
    next_pointer.symlink_to(destination, target_is_directory=True)
    os.replace(next_pointer, pointer)
    descriptor = os.open(pointer.parent, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    return destination


def publish_nodes(root, staged, manifest, health):
    destination = prepare_nodes(root, staged, manifest)
    return activate_nodes(root, destination, manifest, health)


def prune_nodes(root, keep=3):
    """Retain recent managed versions and the active pointer for rollback."""
    root = Path(root)
    parent = root / 'releases/nodes'
    active = (root / 'bin/nodes').resolve()
    versions = sorted((p for p in parent.iterdir() if p.is_dir() and not p.is_symlink()
                       and re.fullmatch(r'master-[0-9a-f]{12}-[0-9]{12}', p.name)
                       and (p / 'manifest.json').is_file()), key=lambda p: p.stat().st_mtime)
    for old in versions[:-keep]:
        # active is resolved: compare the version resolved too, or a root reached through a linked ancestor (allowed by
        # validate_release_paths; /var on macOS) never matches and the version in use is deleted.
        if old.resolve() != active:
            shutil.rmtree(old)
