"""Reuse a validated full release for one explicitly selected node, without building."""
import fcntl
import hashlib
import gzip
import shutil
import tempfile
import json
from pathlib import Path
import re
import subprocess
import sys
import uuid

from manager_artifacts import manager_manifest
from node_artifacts import verify_binary


def validate_target(target):
    required = {'machine_id', 'manager_id', 'machine_uid', 'executable', 'os', 'arch'}
    if (not isinstance(target, dict) or set(target) != required
            or any(not isinstance(value, str) or not value for value in target.values())
            or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}', target['machine_id'])
            or not re.fullmatch(r'[a-z][a-z0-9-]{0,31}', target['manager_id'])
            or not Path(target['executable']).is_absolute()
            or f"{target['os']}-{target['arch']}" not in {'linux-amd64', 'darwin-arm64'}):
        raise ValueError('An explicit machine, manager, UID, executable and supported platform are required')
    return target


def validated_artifact(directory, commit):
    directory = Path(directory)
    receipt = json.loads((directory / 'worker-receipt.json').read_text())
    if (receipt.get('sha') != commit or receipt.get('scope') != 'manager'
            or receipt.get('result') != 'validated'):
        raise ValueError('An existing complete manager validation receipt is required; no rebuild is permitted')
    manifest = manager_manifest(directory, commit, receipt.get('build'))
    if manifest is None:
        raise ValueError('Validated full manager artifacts are required')
    with tempfile.TemporaryDirectory(prefix='persona-node-verify-') as temporary:
        for platform, artifact in manifest['files'].items():
            binary = Path(temporary) / platform
            with gzip.open(directory / artifact['name'], 'rb') as source, binary.open('wb') as output:
                shutil.copyfileobj(source, output)
            verify_binary(binary, platform, manifest)
    return manifest


def validate_candidate(directory, manifest, target, candidate):
    required = {'source', 'version', 'binary_sha256', 'binary_size'}
    platform = f"{target['os']}-{target['arch']}"
    artifact = manifest['files'][platform]
    if (not isinstance(candidate, dict) or set(candidate) != required
            or candidate['source'] != manifest['commit'] or candidate['version'] != manifest['version']
            or candidate['binary_sha256'] != artifact['binary_sha256']
            or type(candidate['binary_size']) is not int or candidate['binary_size'] <= 0):
        raise ValueError('Explicit candidate source, version, native hash and size must match the fixed manifest')
    size = 0
    with gzip.open(Path(directory) / artifact['name'], 'rb') as stream:
        while chunk := stream.read(1024 * 1024):
            size += len(chunk)
            if size > candidate['binary_size']:
                raise ValueError('Candidate native size mismatch')
    if size != candidate['binary_size']:
        raise ValueError('Candidate native size mismatch')


def run_pinned_node(args, cfg):
    if not args.commit or not args.node_id or args.artifact_dir is None:
        raise ValueError('Node scope requires --commit, --node-id and --artifact-dir')
    if args.manager_id:
        raise ValueError('Node scope cannot select a manager publication')
    target_config = cfg.get('node_targets', {}).get(args.node_id)
    if not isinstance(target_config, dict) or set(target_config) != {'publisher_config'}:
        raise ValueError('Node target requires one explicit publisher_config')
    config = Path(target_config['publisher_config'])
    if not config.is_absolute() or config.is_symlink() or not config.is_file():
        raise ValueError('Node publisher config must be an absolute regular file')
    raw_config = config.read_bytes()
    publisher = json.loads(raw_config)
    target = validate_target(publisher['node_target'])
    if target['machine_id'] != args.node_id:
        raise ValueError('Node target identity differs from --node-id')
    checkout, state = Path(cfg['checkout']), Path(cfg['state_dir'])
    state.mkdir(parents=True, exist_ok=True)
    with (state / 'lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        from hub_release import repository_identity
        origin = subprocess.check_output(['git', 'remote', 'get-url', 'origin'], cwd=checkout, text=True).strip()
        if repository_identity(origin) != cfg['repository'].casefold():
            raise ValueError('Configured repository differs from the original release checkout')
        branch = cfg.get('source_branch', 'main')
        ref = f'refs/heads/{branch}'
        subprocess.run(['git', 'check-ref-format', ref], cwd=checkout, check=True)
        subprocess.run(['git', 'fetch', '--no-tags', 'origin', ref], cwd=checkout, check=True)
        subprocess.run(['git', 'merge-base', '--is-ancestor', args.commit, 'FETCH_HEAD'], cwd=checkout, check=True)
        manifest = validated_artifact(args.artifact_dir, args.commit)
        validate_candidate(args.artifact_dir, manifest, target, publisher.get('candidate'))
        identity = hashlib.sha256(raw_config).hexdigest()
        run_id = uuid.uuid4().hex
        target_state = state / 'node' / args.node_id
        target_state.mkdir(parents=True, exist_ok=True)
        last = target_state / ('last-check.json' if args.validate_only else 'last.json')
        previous = json.loads(last.read_text()) if last.exists() else {}
        if (not args.validate_only and not getattr(args, 'retry', False)
                and previous.get('source') == args.commit and previous.get('target_identity') == identity
                and previous.get('phase') in ('pending', 'failure', 'applied', 'confirmed')):
            raise ValueError('Previous node transaction is retained; an explicit --retry is required')
        result = dict(run_id=run_id, source=args.commit, version=manifest['version'],
                      target_identity=identity, phase='pending')
        receipt = target_state / (run_id + '.json')
        command = [sys.executable, str(Path(__file__).with_name('publish_node_release.py')),
                   '--config', str(config), '--artifact-dir', str(args.artifact_dir.resolve()),
                   '--source', args.commit, '--config-sha256', identity, '--run-id', run_id,
                   '--receipt', str(receipt)]
        if not args.validate_only:
            command.append('--apply')
        def save_result():
            temporary = target_state / 'last.tmp'
            temporary.write_text(json.dumps(result, indent=2) + '\n')
            temporary.replace(last)

        save_result()
        try:
            with (target_state / (run_id + '.log')).open('w') as log:
                subprocess.run(command, cwd=checkout, check=True, stdout=log, stderr=subprocess.STDOUT)
            received = json.loads(receipt.read_text())
            if (received.get('run_id') != run_id or received.get('source') != args.commit
                    or received.get('target_identity') != identity
                    or received.get('phase') != ('checked' if args.validate_only else 'confirmed')):
                raise ValueError('Node publisher has not confirmed this exact target')
            result = received
        except BaseException:
            result.update(phase='failure')
            raise
        finally:
            save_result()
