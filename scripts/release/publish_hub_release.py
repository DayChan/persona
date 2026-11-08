#!/usr/bin/env python3
"""Deliver a verified release over SSH; optionally publish matching node packages without touching Android artifacts."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import time
import socket
import uuid

from node_artifacts import node_manifest
from hub_release_paths import prepare_remote_staging, release_directory, validate_remote_upload
from urllib.parse import urlsplit

import paramiko


def credential_fields(credentials):
    if 'fields' not in credentials:
        return credentials
    values = {}
    for field in credentials['fields']:
        for key in (field.get('id'), field.get('label')):
            if key in {'host', 'port', 'username', 'password'}:
                value = field.get('value', '')
                if key in values and values[key] != value:
                    raise ValueError(f'Ambiguous SSH credential field: {key}')
                values[key] = value
    return values


def connect_publisher(cfg):
    """Open the configured publisher connection with existing host-key validation."""
    credentials = credential_fields(json.loads(subprocess.check_output(cfg['credentials_command'])))
    client = paramiko.SSHClient()
    client.load_system_host_keys()
    sock = None
    try:
        for attempt in range(3):
            try:
                sock = None
                if cfg.get('ssh_proxy'):
                    proxy = urlsplit(cfg['ssh_proxy'])
                    if proxy.scheme != 'http' or not proxy.hostname:
                        raise ValueError('ssh_proxy must be an HTTP CONNECT proxy')
                    sock = socket.create_connection((proxy.hostname, proxy.port or 80), timeout=20)
                    target = f'{credentials["host"]}:{credentials.get("port",22)}'
                    sock.sendall(f'CONNECT {target} HTTP/1.1\r\nHost: {target}\r\n\r\n'.encode())
                    response = b''
                    while not response.endswith(b'\r\n\r\n'):
                        chunk = sock.recv(1)
                        if not chunk or len(response) > 16384:
                            sock.close()
                            raise OSError('Invalid proxy response')
                        response += chunk
                    if b' 200 ' not in response.split(b'\r\n',1)[0]:
                        sock.close()
                        raise OSError('SSH proxy refused CONNECT')
                client.connect(credentials['host'], port=int(credentials.get('port', 22)),
                               username=credentials['username'], password=credentials['password'],
                               look_for_keys=False, allow_agent=False, timeout=20, sock=sock)
                break
            except OSError:
                client.close()
                if sock is not None:
                    sock.close()
                if attempt == 2:
                    raise
                time.sleep(3)
        return client, sock
    except BaseException:
        client.close()
        if sock is not None:
            sock.close()
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, type=Path)
    parser.add_argument('--artifact-dir', required=True, type=Path)
    args = parser.parse_args()
    cfg = json.loads(args.config.read_text())
    manifest = json.loads((args.artifact_dir / 'latest.json').read_text())
    packages = node_manifest(args.artifact_dir, manifest) if cfg.get('publish_nodes', False) else None
    if cfg.get('publish_nodes', False) and packages is None:
        raise ValueError('Matching node packages are required by this publisher')
    remote_root = cfg['remote_root']
    release_root = release_directory(remote_root, cfg.get('release_namespace', 'hub'))
    staged = str(release_root / f'.upload-{manifest["build"]}-{uuid.uuid4().hex}')
    client, sock = connect_publisher(cfg)
    try:
        with client.open_sftp() as sftp:
            namespace = cfg.get('release_namespace', 'hub')
            prepare_remote_staging(sftp, remote_root, namespace, staged)
            names = ['latest.json', manifest['files']['linux-amd64']['name']]
            if packages:
                names += ['node-manifest.json'] + [a['name'] for a in packages['files'].values()]
            for name in dict.fromkeys(names):
                destination = validate_remote_upload(sftp, remote_root, namespace, staged, name)
                sftp.put(str(args.artifact_dir / name), destination)
        sources = {name: Path(__file__).with_name(name + '.py').read_text()
                   for name in ['hub_release_paths', 'node_artifacts', 'node_package_mirror', 'promote_hub_release']}
        payload = {'root': remote_root, 'staged': staged, 'timeout': cfg.get('timeout',1200),
                   'publish_nodes': cfg.get('publish_nodes', False),
                   'release_namespace': cfg.get('release_namespace', 'hub')}
        script = "import sys, types\n"
        for name, source in sources.items():
            script += f"module = types.ModuleType({name!r}); sys.modules[{name!r}] = module\n"
            script += f"exec({source!r}, module.__dict__)\n"
        script += 'module.promote(' + repr(payload) + ')\n'
        stdin, stdout, stderr = client.exec_command('python3 -', timeout=1500)
        stdin.write(script)
        stdin.flush()
        stdin.channel.shutdown_write()
        for line in stdout:
            print(line, end='', flush=True)
        errors = stderr.read().decode()
        if errors:
            print(errors, file=sys.stderr)
        code = stdout.channel.recv_exit_status()
        if code:
            raise RuntimeError(f'Hub publisher exited {code}')
    finally:
        client.close()
        if sock is not None:
            sock.close()


if __name__ == '__main__':
    main()
