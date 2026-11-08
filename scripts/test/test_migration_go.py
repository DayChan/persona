"""Migration fixtures select module Go before locking their isolated build to local."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from migration_go import fixture_toolchain


class MigrationGoTests(unittest.TestCase):
    def test_old_system_go_selects_new_module_compiler_with_isolated_home(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            home, cache, server, selected = [base / name for name in ('home', 'cache', 'server', 'selected')]
            for path in (home, cache, server, selected / 'bin'):
                path.mkdir(parents=True)
            module = server / 'go.mod'
            original = 'module example.invalid/fixture\n\ngo 1.27.1\ntoolchain go1.27.2\n'
            module.write_text(original)
            bootstrap = base / 'old-go'
            bootstrap.write_text(f'#!{sys.executable}\n' +
                'import json,os,pathlib,sys\n'
                'assert os.environ["GOENV"] == "off"\n'
                'assert "PERSONA_TOKEN" not in os.environ\n'
                'if sys.argv[1:] == ["env", "GOMODCACHE"]:\n'
                ' assert os.environ["GOTOOLCHAIN"] == "local"\n'
                f' print({str(cache)!r})\n'
                'else:\n'
                ' assert sys.argv[1:] == ["env", "-json", "GOROOT"]\n'
                ' assert os.environ["GOTOOLCHAIN"] == "auto"\n'
                f' assert pathlib.Path.cwd() == pathlib.Path({str(server)!r})\n'
                f' assert os.environ["HOME"] == {str(home)!r}\n'
                f' assert os.environ["GOMODCACHE"] == {str(cache)!r}\n'
                ' assert "go 1.27.1" in pathlib.Path("go.mod").read_text()\n'
                f' print(json.dumps({{"GOROOT": {str(selected)!r}}}))\n')
            compiler = selected / 'bin/go'
            compiler.write_text(f'#!{sys.executable}\n' +
                'import json,os,sys\n'
                'assert sys.argv[1:] == ["mod", "edit", "-json"]\n'
                'assert os.environ["GOTOOLCHAIN"] == "local"\n'
                f'assert os.environ["HOME"] == {str(home)!r}\n'
                'print(json.dumps({"Go":"1.27.1", "Toolchain":"go1.27.2"}))\n')
            bootstrap.chmod(0o755)
            compiler.chmod(0o755)
            isolated = {'HOME': str(home), 'PATH': os.environ['PATH'], 'GOENV': 'off',
                        'GOTOOLCHAIN': 'local', 'GOPATH': str(home / 'go')}
            with patch.dict(os.environ, {'PERSONA_TOKEN': 'synthetic-not-for-compiler'}):
                go, directives = fixture_toolchain(str(bootstrap), server, isolated)
            self.assertEqual(go, str(compiler))
            self.assertEqual(directives, 'go 1.27.1\ntoolchain go1.27.2\n')
            self.assertEqual(module.read_text(), original)
            self.assertEqual(isolated['GOTOOLCHAIN'], 'local')

    def test_failed_selection_does_not_fall_back_to_system_compiler(self):
        with tempfile.TemporaryDirectory() as directory:
            bootstrap = Path(directory) / 'go'
            bootstrap.write_text(f'#!{sys.executable}\nimport sys\n'
                                 'if sys.argv[1:] == ["env", "GOMODCACHE"]: print("/unused/cache")\n'
                                 'else: sys.exit(19)\n')
            bootstrap.chmod(0o755)
            with self.assertRaises(subprocess.CalledProcessError) as error:
                fixture_toolchain(str(bootstrap), Path(directory),
                                  {'HOME': directory, 'GOENV': 'off'})
            self.assertEqual(error.exception.returncode, 19)


if __name__ == '__main__':
    unittest.main()
