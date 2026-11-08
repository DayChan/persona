"""Fail-closed, bounded collection of one manager's update-idle evidence."""
import datetime as dt
import hashlib
import json
import time
from urllib.parse import urlencode


MAX_ACQUISITION_SECONDS = 30


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


class SnapshotPending(ValueError):
    """The authenticated inventory has not finished reconnecting."""


def _rows(response, field):
    rows = response.get(field)
    if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
        raise ValueError(f'Incomplete manager inventory: {field}')
    # A display page or delta must never stand in for the complete inventory.
    if response.get('cursor') or response.get('next_cursor') or response.get('has_more'):
        raise ValueError('Paged manager inventory is not complete')
    if 'total' in response and response['total'] != len(rows):
        raise ValueError('Manager inventory total differs from complete read')
    return rows


def _coverage(response):
    """Only the versioned update-inventory producer can attest completeness.

    Ordinary display lists omit archived/runtime detail and have no such claim.
    An old endpoint must fail closed rather than be promoted by its row count.
    """
    evidence = response.get('update_inventory')
    if (not isinstance(evidence, dict) or evidence.get('schema') != 1
            or evidence.get('complete') is not True
            or not isinstance(evidence.get('revision'), str) or not evidence['revision']):
        raise ValueError('Authoritative manager update inventory capability is unavailable')
    try:
        timestamp = evidence.get('observed_at')
        if not isinstance(timestamp, str):
            raise ValueError('observation time must be a string')
        observed = dt.datetime.fromisoformat(timestamp.replace('Z', '+00:00'))
        age = (dt.datetime.now(dt.timezone.utc) - observed).total_seconds()
    except (KeyError, TypeError, ValueError) as error:
        raise ValueError('Manager inventory observation time is unavailable') from error
    if not -5 <= age < MAX_ACQUISITION_SECONDS:
        raise ValueError('Manager inventory observation is stale or its clock conflicts')
    return evidence['revision']


def _idle(row):
    # Omitted fields on legacy endpoints are unknown, not an empty task list.
    for field in ('background', 'subagents'):
        if not isinstance(row.get(field), list):
            raise ValueError(f'Manager idle capability unavailable: explicit {field} required')
    if 'compaction' not in row or row['compaction'] is not None and not isinstance(row['compaction'], dict):
        raise ValueError('Manager idle capability unavailable: explicit compaction required')
    if row['background']:
        raise ValueError('Manager session has outstanding background work')
    if any(not isinstance(child, dict) or child.get('running') is not False for child in row['subagents']):
        raise ValueError('Manager subagent is running or its state is unknown')
    compaction = row['compaction']
    if compaction is not None and compaction.get('state') not in ('done', 'failed'):
        raise ValueError('Manager compaction is running or its state is unknown')
    if row.get('live') is not True:
        raise ValueError('Manager live-session provenance is unavailable')
    if row.get('starting') is not False:
        raise ValueError('Manager starting-state capability unavailable or session is starting')
    if row.get('state') not in ('idle', 'unread') or row.get('status') not in ('idle', 'done'):
        raise ValueError('Target session is busy or its state is unknown')
    if type(row.get('pending_interactions')) is not int or row['pending_interactions'] != 0:
        raise ValueError('Manager session has pending or unknown interactions')


def collect(root, target, api, require_idle=True, clock=time.monotonic):
    """Double-read inventories; only return a digest after complete stable reads.

    Each underlying HTTP request is independently bounded by the publisher API.
    The total acquisition budget includes both reads. A timed-out/slow partial
    read never receives a fresh timestamp or digest.
    """
    started = clock()

    def read(path):
        if clock() - started >= MAX_ACQUISITION_SECONDS:
            raise TimeoutError('Manager inventory acquisition budget expired')
        result = api(root, path)
        if clock() - started >= MAX_ACQUISITION_SECONDS:
            raise TimeoutError('Manager inventory acquisition budget expired')
        if not isinstance(result, dict):
            raise ValueError('Invalid manager inventory envelope')
        return result

    def once():
        mid = target['manager_id']
        managers = _rows(read('managers'), 'managers')
        matches = [m for m in managers if m.get('id') == mid]
        if len(matches) != 1:
            raise SnapshotPending('Exact manager inventory is absent or ambiguous')
        manager = matches[0]
        if manager.get('deploy') != 'service':
            raise ValueError('Exact independent manager must be a service')
        if manager.get('online') is not True:
            raise SnapshotPending('Exact independent manager is reconnecting')
        profile = read('assistant?' + urlencode({'manager': mid, 'archived': 'true'}))
        for field, expected in (('manager', mid), ('main', target['main']), ('machine_id', target['machine_id'])):
            if not profile.get(field):
                raise SnapshotPending('Scoped assistant identity has not resynced')
            if profile[field] != expected:
                raise ValueError('Scoped assistant identity conflicts with the original target')
        machines = _rows(read('machines'), 'machines')
        owned = [m for m in machines if m.get('manager') == mid]
        ids = [m.get('id') for m in owned]
        if not owned or any(not i for i in ids) or len(set(ids)) != len(ids):
            raise ValueError('Owned machine inventory is absent or ambiguous')
        if target['machine_id'] not in ids:
            raise ValueError('Original machine ownership conflicts with the manager')
        if any(m.get('online') is not True or not m.get('machine_uid') for m in owned):
            raise SnapshotPending('Target machine identities must be known and online')
        inventory = {}
        revisions = {'assistant': _coverage(profile), 'machines': {}}

        def add(row, machine):
            if row.get('manager') != mid or row.get('machine_id') != machine or not row.get('session_id'):
                raise ValueError('Session ownership is ambiguous')
            if type(row.get('rev')) is not int or row['rev'] < 0:
                raise ValueError('Session revision is unavailable')
            key = (machine, row['session_id'])
            if key in inventory and inventory[key] != row:
                raise ValueError('Duplicate session revision or activity conflicts')
            inventory[key] = row

        conversations = _rows(profile, 'conversations')
        for row in conversations:
            if row.get('assistant') is not True:
                raise ValueError('Scoped conversation is not an assistant conversation')
            add(row, target['machine_id'])
        main_key = (target['machine_id'], target['main'])
        if main_key not in inventory:
            raise SnapshotPending('Original main is absent from scoped conversations')
        for machine in sorted(ids):
            response = read('sessions?' + urlencode({'machine_id': machine}))
            revisions['machines'][machine] = _coverage(response)
            for row in _rows(response, 'sessions'):
                add(row, machine)
        rows = [inventory[key] for key in sorted(inventory)]
        if require_idle:
            for row in rows:
                # A historical source alone is insufficient: the producer must
                # explicitly certify that no runtime owns this terminal record.
                historical = (row.get('live') is False and row.get('status') == 'offline'
                              and row.get('state') == 'offline'
                              and row.get('source_kind') in ('codex_history', 'claude_history'))
                if historical:
                    if row.get('background') != [] or row.get('subagents') != [] or row.get('compaction', 'unknown') is not None:
                        raise ValueError('Historical session activity is not proven terminal')
                    if row.get('assistant') is not False or row.get('starting') is not False:
                        raise ValueError('Historical record may still own assistant activity')
                    continue
                _idle(row)
        identity = dict(manager=mid, main=profile['main'], machine_id=profile['machine_id'],
                        machines=sorted((m['id'], m['machine_uid']) for m in owned))
        return manager, identity, rows, revisions

    first = once()
    final = once()
    if canonical(first) != canonical(final):
        raise ValueError('Manager identity, revision or activity changed during acquisition')
    # Per-row revisions are compared, never interpreted as a global revision.
    elapsed = clock() - started
    if not 0 <= elapsed < MAX_ACQUISITION_SECONDS:
        raise TimeoutError('Manager inventory acquisition budget expired')
    evidence = dict(schema=1, identity=final[1], rows=final[2], revisions=final[3], complete_reads=2,
                    acquisition_seconds=elapsed, completed_at=dt.datetime.now(dt.timezone.utc).isoformat())
    return final[0], final[1], hashlib.sha256(canonical(evidence)).hexdigest()
