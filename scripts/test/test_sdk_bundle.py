#!/usr/bin/env python3
"""sdk/go/bundle/sdk.tar.gz (what `personad extension new` writes extensions from) is up to date with sdk/ts and
sdk/templates, and scripts/gen/sync-sdk.sh copies a usable SDK."""

import os
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


class SDKBundle(unittest.TestCase):
    def test_bundle_is_up_to_date(self):
        r = subprocess.run([sys.executable, os.path.join(ROOT, "scripts", "gen", "sdk_bundle.py"), "--check"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_sync_copies_both_sdks(self):
        with tempfile.TemporaryDirectory() as dest:
            r = subprocess.run(["sh", os.path.join(ROOT, "scripts", "gen", "sync-sdk.sh"), dest], capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)
            for f in ["sdk/go/go.mod", "sdk/go/ext/ext.go", "sdk/go/wire/wire_gen.go", "sdk/go/exttest/exttest.go",
                      "sdk/ts/src/index.ts", "sdk/ts/src/wire.ts", "sdk/VERSION",
                      "sdk/ts/package-lock.json", "sdk/ts/tsconfig.ui.json", "sdk/ts/tsconfig.negative.json",
                      "sdk/ts/scripts/check-portable.ts", "sdk/ts/scripts/portable-ui.mjs",
                      "sdk/ts/scripts/check-negative-types.ts", "sdk/ts/typechecks/native.tsx",
                      "sdk/ts/scripts/check-source-bundle.mjs", "sdk/ts/scripts/source-authoring-worker.mjs",
                      "sdk/ts/scripts/source-authoring-tests.mjs", "sdk/ts/README.md",
                      "sdk/ts/typechecks/negative.tsx", "sdk/ts/fixtures/Manifest.custom-contributions.json",
                      "sdk/ts/fixtures/UIStreamDelta.operations.json"]:
                self.assertTrue(os.path.exists(os.path.join(dest, f)), f)
            self.assertFalse(os.path.exists(os.path.join(dest, "sdk/go/bundle")), "the bundle is personad's, not an extension's")
            for name in ["node_modules", "dist", "bin", ".git"]:
                self.assertFalse(os.path.exists(os.path.join(dest, "sdk/ts", name)), name)
            for relative in ["sdk/ts/README.md", "sdk/ts/scripts/check-source-bundle.mjs",
                             "sdk/ts/scripts/source-authoring-worker.mjs", "sdk/ts/scripts/source-authoring-tests.mjs"]:
                with open(os.path.join(ROOT, relative), "rb") as original, \
                        open(os.path.join(dest, relative), "rb") as copied, \
                        tarfile.open(os.path.join(ROOT, "sdk/go/bundle/sdk.tar.gz")) as bundle:
                    expected = original.read()
                    self.assertEqual(expected, copied.read(), relative)
                    with bundle.extractfile(relative.removeprefix("sdk/")) as packed:
                        self.assertEqual(expected, packed.read(), relative)
            for folder in ["scripts", "typechecks"]:
                source = os.path.join(ROOT, "sdk/ts", folder)
                for top, _, files in os.walk(source):
                    for name in files:
                        relative = os.path.relpath(os.path.join(top, name), ROOT)
                        with open(os.path.join(ROOT, relative), "rb") as original, open(os.path.join(dest, relative), "rb") as copied:
                            self.assertEqual(original.read(), copied.read(), relative)

    def test_bundled_and_copied_fixtures_match_the_canonical_examples(self):
        source = os.path.join(ROOT, "schema", "extension", "examples")
        names = [name for name in os.listdir(source)
                 if name == "Manifest.custom-contributions.json" or (name.startswith("UIStream") and name.endswith(".json"))]
        with tempfile.TemporaryDirectory() as dest, tarfile.open(os.path.join(ROOT, "sdk/go/bundle/sdk.tar.gz")) as bundle:
            r = subprocess.run(["sh", os.path.join(ROOT, "scripts", "gen", "sync-sdk.sh"), dest, "ts"],
                               capture_output=True, text=True, timeout=30)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertFalse(os.path.exists(os.path.join(dest, "schema")), "copy must stay inside the SDK")
            self.assertEqual(len(bundle.getnames()), len(set(bundle.getnames())))
            for name in names:
                with open(os.path.join(source, name), "rb") as original, open(os.path.join(dest, "sdk/ts/fixtures", name), "rb") as copied:
                    expected = original.read()
                    self.assertEqual(copied.read(), expected, name)
                    with bundle.extractfile("ts/fixtures/" + name) as packed:
                        self.assertEqual(packed.read(), expected, name)

    def test_selective_copy_preserves_the_other_sdk(self):
        for selected, preserved in [("go", "ts"), ("ts", "go")]:
            with self.subTest(selected=selected), tempfile.TemporaryDirectory() as dest:
                other = os.path.join(dest, "sdk", preserved)
                os.makedirs(other)
                marker = os.path.join(other, "keep.txt")
                with open(marker, "w") as f:
                    f.write("Unselected SDK fixture")
                r = subprocess.run(["sh", os.path.join(ROOT, "scripts", "gen", "sync-sdk.sh"), dest, selected],
                                   capture_output=True, text=True, timeout=30)
                self.assertEqual(r.returncode, 0, r.stderr)
                with open(marker) as f:
                    self.assertEqual(f.read(), "Unselected SDK fixture")
                self.assertTrue(os.path.isfile(os.path.join(dest, "sdk", "VERSION")))
                if selected == "ts":
                    self.assertTrue(os.path.isfile(os.path.join(dest, "sdk/ts/scripts/check-portable.ts")))
                else:
                    self.assertTrue(os.path.isfile(os.path.join(dest, "sdk/go/ext/ext.go")))


if __name__ == "__main__":
    unittest.main()
