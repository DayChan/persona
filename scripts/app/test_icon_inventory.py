#!/usr/bin/env python3
"""icon_inventory.py reads the real Apple sources and ends with a verdict line."""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import icon_inventory  # noqa: E402


class IconInventoryTests(unittest.TestCase):
    def test_reads_the_vocabulary_and_the_views(self):
        data = icon_inventory.scan()
        s = icon_inventory.summary(data)
        self.assertGreater(s['registry_entries'], 200)
        self.assertGreater(s['symbol_images'], 100)
        self.assertEqual(data['registry']['aliases'], {}, 'two names on one symbol: alias one meaning (docs/apple/icons.md)')
        self.assertIn('apple/Persona/Resources/Assets.xcassets'.removeprefix('apple/'), data['assets']['catalogs'])

    def test_outline_is_left_alone_for_filled_designs(self):
        # The parser must not take `.filled` names (a design) for `.fill` ones.
        data = icon_inventory.scan()
        self.assertNotIn('circle.lefthalf.filled', data['registry']['aliases'])

    def test_cli_ends_with_a_verdict_and_diffs_itself_to_nothing(self):
        script = HERE / 'icon_inventory.py'
        out = subprocess.run([sys.executable, str(script)], capture_output=True, text=True, check=True).stdout
        self.assertTrue(out.strip().splitlines()[-1].startswith('verdict: '))
        snap = subprocess.run([sys.executable, str(script), '--json'], capture_output=True, text=True, check=True).stdout
        json.loads(snap)
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / 'snapshot.json'
            path.write_text(snap)
            diff = subprocess.run([sys.executable, str(script), '--diff', str(path)], capture_output=True, text=True, check=True).stdout
        lines = [l for l in diff.splitlines() if l.strip()]
        self.assertEqual(lines[:2], ['| Measure | Before | After |', '| --- | --- | --- |'])
        self.assertEqual(len(lines), 3, diff)  # header, rule, verdict: nothing changed


if __name__ == '__main__':
    unittest.main()
