"""Counterexamples for manager idle evidence; no network or product mutation."""
import copy
import datetime as dt
from pathlib import Path
import unittest

from manager_idle import collect, SnapshotPending


def session(sid='main', **extra):
    return dict(dict(manager='m-fixture', machine_id='box', session_id=sid,
                     rev=1, live=True, assistant=True, state='idle', status='idle', starting=False,
                     pending_interactions=0, background=[], subagents=[], compaction=None), **extra)


class IdleEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.target = dict(manager_id='m-fixture', machine_id='box', main='main')
        self.answers = {
            'managers': dict(managers=[dict(id='m-fixture', online=True, deploy='service')]),
            'machines': dict(machines=[dict(id='box', manager='m-fixture', machine_uid='uid', online=True)]),
            'assistant?manager=m-fixture&archived=true': dict(manager='m-fixture', machine_id='box', main='main',
                                                             conversations=[session()], update_inventory=dict(schema=1, complete=True, revision='r1', observed_at=dt.datetime.now(dt.timezone.utc).isoformat())),
            'sessions?machine_id=box': dict(sessions=[], update_inventory=dict(schema=1, complete=True, revision='r1', observed_at=dt.datetime.now(dt.timezone.utc).isoformat())),
        }
        self.calls = []

    def read(self, root, path):
        self.calls.append(path)
        return copy.deepcopy(self.answers[path])

    def collect(self, **kwargs):
        return collect(Path('/fixture'), self.target, self.read, **kwargs)

    def add(self, row):
        self.answers['sessions?machine_id=box']['sessions'].append(row)

    def test_main_covered_by_authoritative_conversations_without_display_row(self):
        _, identity, digest = self.collect()
        self.assertEqual(identity['main'], 'main')
        self.assertEqual(len(digest), 64)
        self.assertEqual(len(self.calls), 8)

    def test_same_revision_duplicate_is_allowed_but_conflicts_never_choose_a_winner(self):
        self.add(session())
        self.collect()
        for changes in (dict(rev=2), dict(status='working'), dict(manager='m-other')):
            self.answers['sessions?machine_id=box']['sessions'] = [session(**changes)]
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.collect()

    def test_no_main_does_not_become_idle_even_if_display_sessions_are_idle(self):
        self.answers['assistant?manager=m-fixture&archived=true']['conversations'] = []
        self.add(session('other'))
        with self.assertRaises(SnapshotPending): self.collect()

    def test_old_omitted_activity_fields_are_not_empty(self):
        row = self.answers['assistant?manager=m-fixture&archived=true']['conversations'][0]
        for field in ('background', 'subagents', 'compaction', 'starting', 'rev'):
            value = row.pop(field)
            with self.subTest(field=field), self.assertRaises(ValueError): self.collect()
            row[field] = value

    def test_background_subagents_starting_and_compaction_independently_block(self):
        for changes in (dict(background=[dict(kind='command')]),
                        dict(subagents=[dict(running=True)]), dict(subagents=[{}]),
                        dict(starting=True), dict(compaction=dict(state='running')),
                        dict(compaction={}), dict(pending_interactions=1)):
            self.answers['sessions?machine_id=box']['sessions'] = [session('child', **changes)]
            with self.subTest(changes=changes), self.assertRaises(ValueError): self.collect()

    def test_history_does_not_hide_a_working_child_or_unknown(self):
        history = session('history', assistant=False, live=False, source_kind='codex_history',
                          state='offline', status='offline')
        self.add(history)
        self.collect()
        for changes in (dict(state='working', status='working', is_subagent=True),
                        dict(state='unknown', status='interrupted'), dict(state='needs_you', status='blocked')):
            self.answers['sessions?machine_id=box']['sessions'] = [history, session('child', **changes)]
            with self.subTest(changes=changes), self.assertRaises(ValueError): self.collect()

    def test_offline_source_alone_is_not_terminal_evidence(self):
        for changes in (dict(live=True), dict(live=None), dict(background=[{}]), dict(assistant=True)):
            row = session('history', assistant=False, live=False, source_kind='codex_history',
                          state='offline', status='offline')
            row.update(changes)
            self.answers['sessions?machine_id=box']['sessions'] = [row]
            with self.subTest(changes=changes), self.assertRaises(ValueError): self.collect()

    def test_missing_foreign_or_duplicate_machine_identity_rejected(self):
        for machines in ([], [dict(id='box', manager='m-other')],
                         [dict(id='box', manager='m-fixture', online=True)],
                         self.answers['machines']['machines'] * 2):
            with self.subTest(machines=machines):
                before = self.answers['machines']
                self.answers['machines'] = dict(machines=machines)
                with self.assertRaises(ValueError): self.collect()
                self.answers['machines'] = before

    def test_second_complete_read_detects_revision_or_identity_change(self):
        for field, value in (('rev', 2), ('state', 'working')):
            count = [0]
            def read(root, path):
                result = self.read(root, path)
                if path.startswith('assistant?'):
                    count[0] += 1
                    if count[0] == 2: result['conversations'][0][field] = value
                return result
            with self.subTest(field=field), self.assertRaises(ValueError):
                collect(Path('/fixture'), self.target, read)

    def test_partial_page_or_wrong_total_never_proves_complete(self):
        envelope = self.answers['sessions?machine_id=box']
        for change in (dict(cursor='next'), dict(has_more=True), dict(total=1)):
            envelope.update(change)
            with self.subTest(change=change), self.assertRaises(ValueError): self.collect()
            for key in change: envelope.pop(key)

    def test_slow_complete_read_does_not_get_relabelled_fresh(self):
        elapsed = [0]
        def slow(root, path):
            result = self.read(root, path)
            elapsed[0] += 11
            return result
        with self.assertRaises(TimeoutError):
            collect(Path('/fixture'), self.target, slow, clock=lambda: elapsed[0])

    def test_health_only_read_still_requires_original_main_coverage(self):
        self.answers['assistant?manager=m-fixture&archived=true']['conversations'] = []
        with self.assertRaises(SnapshotPending): self.collect(require_idle=False)

    def test_display_list_without_versioned_completeness_is_not_authoritative(self):
        for path in ('assistant?manager=m-fixture&archived=true', 'sessions?machine_id=box'):
            saved = self.answers[path].pop('update_inventory')
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, 'capability'):
                self.collect()
            self.answers[path]['update_inventory'] = saved

    def test_stale_missing_or_future_producer_timestamp_rejected(self):
        evidence = self.answers['sessions?machine_id=box']['update_inventory']
        now = dt.datetime.now(dt.timezone.utc)
        for value in (None, 'invalid', (now - dt.timedelta(seconds=31)).isoformat(),
                      (now + dt.timedelta(minutes=1)).isoformat()):
            evidence['observed_at'] = value
            with self.subTest(value=value), self.assertRaises(ValueError): self.collect()

    def test_completeness_revision_change_is_not_hidden_by_unchanged_rows(self):
        reads = [0]
        def read(root, path):
            result = self.read(root, path)
            if path == 'sessions?machine_id=box':
                reads[0] += 1
                result['update_inventory']['revision'] = str(reads[0])
            return result
        with self.assertRaisesRegex(ValueError, 'changed during acquisition'):
            collect(Path('/fixture'), self.target, read)


if __name__ == '__main__':
    unittest.main()
