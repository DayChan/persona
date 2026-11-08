"""Validate fixed manager packages without publishing any Hub or node pointer."""
import json
from pathlib import Path
import re

from node_artifacts import PLATFORMS, verify_archive


def manager_manifest(directory, source=None, build=None):
    """Return checked metadata, None if absent; archives must retain their exact bytes."""
    directory = Path(directory)
    path = directory / 'manager-manifest.json'
    if not path.is_file():
        return None
    manifest = json.loads(path.read_text())
    latest = json.loads((directory / 'latest.json').read_text())
    if (any(manifest.get(key) != latest.get(key) for key in ('version', 'build', 'commit'))
            or set(manifest.get('files', {})) != PLATFORMS
            or manifest.get('programs') or manifest.get('nodes')
            or set(latest.get('files', {})) != {'linux-amd64'}
            or manifest['files']['linux-amd64'] != latest['files']['linux-amd64']):
        raise ValueError('Manager artifacts differ from the validated release')
    commit = manifest.get('commit', '')
    if (not re.fullmatch(r'[0-9a-f]{40}', commit)
            or not re.fullmatch(r'[0-9]{12}', str(manifest.get('build', '')))
            or manifest.get('version') != f"master-{commit[:12]}-{manifest['build']}"):
        raise ValueError('Manager version is not bound to its source and build')
    if ((source is not None and commit != source)
            or (build is not None and str(manifest['build']) != str(build))):
        raise ValueError('Manager cache differs from the pinned source and build')
    for artifact in manifest['files'].values():
        verify_archive(directory, artifact)
    return manifest
