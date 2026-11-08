#!/usr/bin/env python3
"""Check or apply one explicit manager target through the original publisher transport."""
import argparse
import json
import os
from pathlib import Path
import re
import sys
import uuid

from hub_release_paths import prepare_remote_staging, release_directory, validate_remote_upload
from manager_artifacts import manager_manifest
from publish_hub_release import connect_publisher

MODULES = ('hub_release_paths', 'node_artifacts', 'manager_artifacts', 'promote_hub_release',
           'manager_idle', 'promote_manager_release')


def remote_program(payload):
    script = 'import sys, types\n'
    for name in MODULES:
        source = Path(__file__).with_name(name + '.py').read_text()
        script += f'module = types.ModuleType({name!r}); sys.modules[{name!r}] = module\n'
        script += f'exec({source!r}, module.__dict__)\n'
    return script + 'module.promote(' + repr(payload) + ')\n'


def publish(cfg, artifact_dir, manager_id, run_id, apply=False):
    target = cfg['target']
    if target.get('manager_id') != manager_id:
        raise ValueError('Publisher target differs from the explicit worker manager ID')
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,80}', run_id):
        raise ValueError('Invalid explicit run ID')
    manifest = manager_manifest(artifact_dir)
    if manifest is None:
        raise ValueError('A formal same-source manager manifest is required')
    if apply and (target.get('freeze', True) or target.get('activation_enabled') is not True):
        raise ValueError('Manager activation remains frozen or disabled')
    payload = dict(root=cfg['remote_root'], target=target, run_id=run_id, apply=apply,
                   source=manifest['commit'], release_namespace=cfg.get('release_namespace', 'hub'),
                   target_source=Path(__file__).with_name('manager_target.py').read_text())
    client, sock = connect_publisher(cfg)
    try:
        if apply:
            staged = str(release_directory(cfg['remote_root'], payload['release_namespace']) /
                         f'.manager-upload-{run_id}-{uuid.uuid4().hex}')
            payload['staged'] = staged
            # This is the original publisher's staging mechanism, not a second deployment lane.
            with client.open_sftp() as sftp:
                prepare_remote_staging(sftp, cfg['remote_root'], payload['release_namespace'], staged)
                names = ['latest.json', 'manager-manifest.json'] + [a['name'] for a in manifest['files'].values()]
                for name in dict.fromkeys(names):
                    destination = validate_remote_upload(sftp, cfg['remote_root'], payload['release_namespace'], staged, name)
                    sftp.put(str(artifact_dir / name), destination)
        stdin, stdout, stderr = client.exec_command('python3 -', timeout=600)
        stdin.write(remote_program(payload))
        stdin.flush()
        stdin.channel.shutdown_write()
        lines = stdout.read().decode().splitlines()
        errors = stderr.read().decode()
        code = stdout.channel.recv_exit_status()
        if code:
            raise RuntimeError(f'Manager publisher failed ({code}): {errors[-2000:]}')
        if not lines:
            raise ValueError('Manager publisher returned no receipt')
        receipt = json.loads(lines[-1])
        if (receipt.get('manager_id') != manager_id or receipt.get('run_id') != run_id or
                receipt.get('source') != manifest['commit'] or
                receipt.get('phase') != ('confirmed' if apply else 'checked')):
            raise ValueError('Manager publisher returned an unbound or incomplete receipt')
        return receipt
    finally:
        client.close()
        if sock is not None:
            sock.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, type=Path)
    parser.add_argument('--artifact-dir', required=True, type=Path)
    parser.add_argument('--manager-id', required=True)
    parser.add_argument('--run-id', required=True)
    parser.add_argument('--apply', action='store_true', help='activate only with an explicit unfrozen bootstrap window')
    args = parser.parse_args()
    cfg = json.loads(args.config.read_text())
    receipt = publish(cfg, args.artifact_dir, args.manager_id, args.run_id, args.apply)
    # A checked receipt must never satisfy the worker's overall success condition.
    path = args.artifact_dir / 'manager-receipt.json'
    temporary = path.with_suffix('.tmp')
    with temporary.open('w') as writer:
        json.dump(receipt, writer, indent=2)
        writer.write('\n')
        writer.flush()
        os.fsync(writer.fileno())
    os.replace(temporary, path)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
