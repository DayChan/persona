"""Select the server module's Go toolchain for isolated migration fixtures."""
import json
import os
from pathlib import Path
import subprocess


def fixture_toolchain(bootstrap, server, isolated_environment):
    # Discover only the existing module cache, with user Go configuration disabled.
    cache_environment = {key: os.environ[key] for key in ('PATH', 'HOME', 'GOPATH', 'GOMODCACHE')
                         if key in os.environ}
    cache_environment.update(GOENV='off', GOTOOLCHAIN='local', GOWORK='off')
    cache = subprocess.run([bootstrap, 'env', 'GOMODCACHE'], env=cache_environment,
                           capture_output=True, text=True, check=True, timeout=30).stdout.strip()
    if not cache or not Path(cache).is_absolute():
        raise ValueError('Go returned an invalid module cache')
    # Let Go interpret both go and toolchain directives, including future versions.
    # Selection shares only the compiler cache; the fixture HOME remains isolated.
    environment = dict(isolated_environment, GOMODCACHE=cache, GOTOOLCHAIN='auto',
                       GO111MODULE='on', GOWORK='off')
    result = subprocess.run([bootstrap, 'env', '-json', 'GOROOT'], cwd=server, env=environment,
                            capture_output=True, text=True, check=True, timeout=120)
    root = json.loads(result.stdout)['GOROOT']
    if not isinstance(root, str) or not root or not Path(root).is_absolute():
        raise ValueError('Go returned an invalid toolchain root')
    go = Path(root) / 'bin' / ('go.exe' if os.name == 'nt' else 'go')
    # Read the module directives through Go's parser without editing the source module.
    result = subprocess.run([str(go), 'mod', 'edit', '-json'], cwd=server,
                            env=dict(environment, GOTOOLCHAIN='local'),
                            capture_output=True, text=True, check=True, timeout=30)
    module = json.loads(result.stdout)
    version = module['Go']
    if not isinstance(version, str) or not version:
        raise ValueError('server module has no Go version')
    directives = f'go {version}\n'
    if module.get('Toolchain'):
        directives += f'toolchain {module["Toolchain"]}\n'
    return str(go), directives
