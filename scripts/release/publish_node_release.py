#!/usr/bin/env python3
"""Deliver an existing validated release to exactly one node through its original RPC path."""
import argparse
import hashlib
import json
from pathlib import Path
import uuid

from hub_release_paths import prepare_remote_staging, release_directory, validate_remote_upload
from pinned_node_release import validated_artifact, validate_target, validate_candidate
from publish_hub_release import connect_publisher


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--artifact-dir', type=Path, required=True)
    parser.add_argument('--source', required=True)
    parser.add_argument('--config-sha256', required=True)
    parser.add_argument('--run-id', required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    raw = args.config.read_bytes()
    if hashlib.sha256(raw).hexdigest() != args.config_sha256:
        raise ValueError('Publisher configuration changed')
    cfg = json.loads(raw)
    target = validate_target(cfg['node_target'])
    if args.apply and cfg.get('activation_enabled') is not True:
        raise ValueError('Node target activation is not enabled')
    manifest = validated_artifact(args.artifact_dir, args.source)
    validate_candidate(args.artifact_dir, manifest, target, cfg.get('candidate'))
    root = cfg['remote_root']
    namespace = cfg.get('release_namespace', 'hub')
    staged = str(release_directory(root, namespace) / f'.upload-node-{uuid.uuid4().hex}')
    client, sock = connect_publisher(cfg)
    try:
        with client.open_sftp() as sftp:
            prepare_remote_staging(sftp, root, namespace, staged)
            names = ['latest.json', 'manager-manifest.json'] + [a['name'] for a in manifest['files'].values()]
            for name in dict.fromkeys(names):
                destination = validate_remote_upload(sftp, root, namespace, staged, name)
                sftp.put(str(args.artifact_dir / name), destination)
        payload = dict(root=root, staged=staged, release_namespace=namespace, target=target,
                       source=args.source, run_id=args.run_id, target_identity=args.config_sha256, candidate=cfg['candidate'],
                       apply=args.apply, timeout=cfg.get('timeout', 900))
        script = 'import sys, types, json\n'
        for name in ['hub_release_paths', 'node_artifacts', 'manager_artifacts', 'pinned_node_release', 'promote_node_release']:
            source = Path(__file__).with_name(name + '.py').read_text()
            script += f'module = types.ModuleType({name!r}); sys.modules[{name!r}] = module\n'
            script += f'exec({source!r}, module.__dict__)\n'
        script += f'print(json.dumps(module.promote({payload!r})))\n'
        stdin, stdout, stderr = client.exec_command('python3 -', timeout=cfg.get('timeout', 900) + 120)
        stdin.write(script)
        stdin.flush()
        stdin.channel.shutdown_write()
        output = stdout.read().decode()
        error = stderr.read().decode()
        code = stdout.channel.recv_exit_status()
        if output:
            print(output, end='', flush=True)
        if code:
            raise RuntimeError(f'Node publisher exited {code}: {error}')
        lines = output.splitlines()
        if not lines:
            raise ValueError('Publisher returned no receipt')
        result = json.loads(lines[-1])
        if any(result.get(key) != value for key, value in (
                ('run_id', args.run_id), ('source', args.source),
                ('target_identity', args.config_sha256),
                ('machine_id', target['machine_id']), ('machine_uid', target['machine_uid']),
                ('phase', 'confirmed' if args.apply else 'checked'))):
            raise ValueError('Publisher receipt does not match this node transaction')
        temporary = args.receipt.with_suffix('.tmp')
        temporary.write_text(json.dumps(result, indent=2) + '\n')
        temporary.replace(args.receipt)
        print(json.dumps(result))
    finally:
        client.close()
        if sock is not None:
            sock.close()


if __name__ == '__main__':
    main()
