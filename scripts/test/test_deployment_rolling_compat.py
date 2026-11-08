"""Pinned-baseline deployment contracts; every process uses synthetic homes and stub services."""
import gzip
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from test_persona_install import PersonaInstallTests
from test_persona_script_identity import identity, executable, environment

ROOT = Path(__file__).resolve().parents[2]
BASELINE_DIR = Path(__file__).parent / "fixtures" / "deployment"
# Original Git blob IDs and SHA-256 digests pin the transparent historical inputs.
BASELINE_FIXTURES = {
    "scripts/deploy/install.sh": (
        "install.sh.fixture", "2cb5313ca523baae86a7cc3edace37831afce285",
        "17662c97d0de9ca9b8b065d1555ee6e890fa457ba436125ed0d805f5a0e0be5e"),
    "scripts/persona_config.py": (
        "persona_config.py.fixture", "13539dc74cb2785642f67c9e46bf5fb9ed9d0493",
        "0b6390096988a44d87e66f146198669cda9299dc0966a25b873e8cd2cb6495ab"),
}
FORMER = "jar" + "vis"
PREFIX = FORMER.upper() + "_"


def baseline(path):
    name, blob, digest = BASELINE_FIXTURES[path]
    content = (BASELINE_DIR / name).read_bytes()
    header = b"blob " + str(len(content)).encode("ascii") + b"\0"
    if hashlib.sha1(header + content).hexdigest() != blob or hashlib.sha256(content).hexdigest() != digest:
        raise ValueError("historical deployment fixture digest mismatch: " + name)
    return content.decode("utf-8")


class DeploymentRollingCompatibilityTests(unittest.TestCase):
    def test_baselines_do_not_require_git_history(self):
        with patch.object(subprocess, "run", side_effect=AssertionError("Git history must not be needed")):
            for source in BASELINE_FIXTURES:
                self.assertTrue(baseline(source))

    def test_modified_baseline_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            name = BASELINE_FIXTURES["scripts/deploy/install.sh"][0]
            (Path(directory) / name).write_text("changed historical input\n")
            with patch(__name__ + ".BASELINE_DIR", Path(directory)):
                with self.assertRaisesRegex(ValueError, "fixture digest mismatch"):
                    baseline("scripts/deploy/install.sh")

    def test_baseline_and_current_installers_can_alternate_without_new_state(self):
        runner = PersonaInstallTests()
        for order in (("old", "new", "old"), ("new", "old", "new")):
            with self.subTest(order=order), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                previous = home / "baseline-install.sh"
                previous.write_text(baseline("scripts/deploy/install.sh"))
                state = home / ("." + FORMER if order[0] == "old" else ".persona")
                state.mkdir()
                payload = '{"token":"fixture-token","database_url":"postgres://fixture@localhost/existing"}\n'
                (state / "hub.json").write_text(payload)
                for version in order:
                    result = runner.run_install(home, legacy_artifacts=version == "old",
                                                script=previous if version == "old" else ROOT / "scripts/deploy/install.sh")
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual((state / "hub.json").read_text(), payload)
                    self.assertTrue(os.path.samefile(state, home / ".persona"))
                    self.assertTrue(os.path.samefile(state, home / ("." + FORMER)))
                    for name in ("personad", FORMER + "d"):
                        self.assertTrue((state / "bin" / name).is_file())
                        check = subprocess.run([str(state / "bin" / name), "version"], env=environment(home),
                                               capture_output=True, text=True, timeout=5)
                        self.assertEqual(check.returncode, 0, check.stderr)

    def test_new_installer_consumes_former_artifacts_and_old_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            old = home / "selected old profile"
            link = '#!/bin/sh\nprintf "link-fixture\\n"\n'
            result = PersonaInstallTests().run_install(home, legacy_artifacts=True, link=link,
                        extra={PREFIX + "HOME": str(old), "PERSONA_ROLE": "node", "PERSONA_LISTEN": ":18663"})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((old / "bin/personad").is_file())
            self.assertTrue((old / "bin/persona-link").is_file())
            self.assertEqual(os.readlink(old / "bin" / (FORMER + "-link")), "persona-link")
            self.assertFalse((home / ".persona").exists())

    def test_baseline_config_consumer_reads_new_environment_and_config(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            old_script = home / "baseline-config.py"
            old_script.write_text(baseline("scripts/persona_config.py"))
            new_config = home / ".config/persona/config.json"
            new_config.parent.mkdir(parents=True)
            new_config.write_text('{"hub":{"url":"https://hub.example.test"}}')
            result = subprocess.run([sys.executable, str(old_script), "env"], env=environment(home),
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("export PERSONA_HUB_URL=https://hub.example.test", result.stdout)
            self.assertIn("export " + PREFIX + "HUB_URL=https://hub.example.test", result.stdout)

    def test_environment_aliases_preserve_precedence_empty_values_and_do_not_print_secrets(self):
        values = {PREFIX + "HOME": "old", "PERSONA_HOME": "", PREFIX + "DB_PASSWORD": "private-fixture-value"}
        output = identity.environment_aliases(environment=values)
        self.assertNotIn("private-fixture-value", output)
        env = dict(values, PATH=os.environ["PATH"])
        result = subprocess.run(["sh", "-c", output + '\nprintf "%s|%s|%s" "$PERSONA_HOME" "$' + PREFIX + 'HOME" "$PERSONA_DB_PASSWORD"'],
                                env=env, capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "||private-fixture-value")

    def test_installed_former_node_identity_and_envfile_survive_install_restart_and_uninstall(self):
        for system in ("Linux", "Darwin"):
            with self.subTest(system=system), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                state = home / "custom profile"
                (state / "bin").mkdir(parents=True)
                executable(state / "bin" / (FORMER + "d"), "#!/bin/sh\nexit 0\n")
                (state / "hub.json").write_text('{"token":"unchanged-fixture"}')
                commands = home / "commands"
                commands.mkdir()
                executable(commands / "uname", '#!/bin/sh\nprintf "%s\\n" "' + system + '"\n')
                for command in ("systemctl", "launchctl", "loginctl"):
                    executable(commands / command, '#!/bin/sh\nprintf "%s %s\\n" "' + command + '" "$*" >> "$HOME/calls"\nprintf "Linger=yes\\n"\n')
                if system == "Linux":
                    installed = home / ".config/systemd/user" / (FORMER + "d-node.service")
                    installed.parent.mkdir(parents=True)
                    envfile = home / "service.env"
                    envfile.write_text(PREFIX + 'HOME="' + str(state) + '"\nAPI_KEY=private-fixture-value\n')
                    original = '[Service]\nEnvironmentFile=' + str(envfile) + '\n'
                    installed.write_text(original)
                else:
                    installed = home / "Library/LaunchAgents" / ("com." + FORMER + ".node.plist")
                    installed.parent.mkdir(parents=True)
                    installed.write_bytes(plistlib.dumps({"Label": "com." + FORMER + ".node",
                        "EnvironmentVariables": {PREFIX + "HOME": str(state), "API_KEY": "private-fixture-value"}}))
                env = environment(home, PATH=str(commands) + ":" + os.environ["PATH"])
                for action in ("install", "install", "restart-idle", "uninstall"):
                    result = subprocess.run(["bash", str(ROOT / "scripts/deploy/node-launchd.sh"), action],
                                            env=env, capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertNotIn("private-fixture-value", result.stdout + result.stderr)
                    if action == "install":
                        if system == "Linux":
                            self.assertEqual(installed.read_text(), original)
                            self.assertIn('ExecStart=', (installed.with_name(installed.name + '.d') / '90-persona-compat.conf').read_text())
                            self.assertIn("private-fixture-value", envfile.read_text())
                        else:
                            job = plistlib.loads(installed.read_bytes())
                            self.assertEqual(job["EnvironmentVariables"]["API_KEY"], "private-fixture-value")
                            self.assertEqual(job["EnvironmentVariables"]["PERSONA_HOME"], str(state))
                self.assertFalse(installed.exists())
                self.assertEqual((state / "hub.json").read_text(), '{"token":"unchanged-fixture"}')
                self.assertNotIn("personad-node.service", (home / "calls").read_text())
                self.assertNotIn("com.persona.node", (home / "calls").read_text())

    def test_former_hub_socket_and_envfile_are_reused_not_recreated(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            state = home / "hub profile"
            (state / "bin").mkdir(parents=True)
            executable(state / "bin" / (FORMER + "d"), "#!/bin/sh\nexit 3\n")
            (state / "hub.json").write_text('{"listen":":18660"}')
            unit = home / ".config/systemd/user" / (FORMER + "d.service")
            unit.parent.mkdir(parents=True)
            file = home / "root.env"
            file.write_text(PREFIX + 'HOME="' + str(state) + '"\nSECRET=private-fixture-value\n')
            original = '[Service]\nEnvironmentFile=' + str(file) + '\nEnvironment="' + PREFIX + 'HUB_EDGE=0"\n'
            unit.write_text(original)
            unit.with_suffix('.socket').write_text('[Socket]\nListenStream=18660\nService=' + FORMER + 'd.service\n')
            commands = home / "commands"
            commands.mkdir()
            executable(commands / "systemctl", '#!/bin/sh\nprintf "%s\\n" "$*" >> "$HOME/calls"\nexit 0\n')
            result = subprocess.run(["bash", str(ROOT / "scripts/deploy/hub-systemd.sh")],
                                    env=environment(home, PATH=str(commands) + ":" + os.environ["PATH"]),
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(unit.read_text(), original)
            self.assertFalse(unit.with_name('personad.service').exists())
            self.assertFalse(unit.with_name('personad.socket').exists())
            self.assertIn('Service=' + FORMER + 'd.service', unit.with_suffix('.socket').read_text())
            self.assertNotIn('private-fixture-value', result.stdout + result.stderr)
            self.assertNotIn('personad.socket', (home / 'calls').read_text())

    def test_rendered_installer_carries_credential_free_service_resolver(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            rendered = home / "install.sh"
            identity.render_installer(ROOT / "scripts/deploy/install.sh", rendered, "https://download.example.test/dl")
            text = rendered.read_text()
            self.assertNotIn("@PERSONA_DL@", text)
            self.assertNotIn("IDENTITY_CODE='@PERSONA_IDENTITY@'", text)
            self.assertNotIn(str(ROOT), text)
            subprocess.run(["sh", "-n", str(rendered)], check=True, timeout=5)
            profile = home / "remote service profile"
            unit = home / ".config/systemd/user" / (FORMER + "d.service")
            unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nEnvironment="' + PREFIX + 'HOME=' + str(profile) + '"\n')
            result = PersonaInstallTests().run_install(home, script=rendered)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((profile / "bin/personad").is_file())
            self.assertFalse((home / '.persona').exists())

    def test_artifact_aliases_are_byte_identical_and_command_aliases_do_not_loop(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            for prefix in ("personad-", "persona-link-", "personad-wechat-"):
                file = directory / (prefix + "linux-amd64.gz")
                file.write_bytes(gzip.compress(b"fixture-program", mtime=0))
            identity.artifact_aliases(directory)
            for file in directory.glob("person*.gz"):
                old = file.name.replace("personad-", FORMER + "d-", 1).replace("persona-link-", FORMER + "-link-", 1)
                self.assertEqual((directory / old).read_bytes(), file.read_bytes())
            executable(directory / "personad", "#!/bin/sh\nexit 0\n")
            identity.command_aliases(directory)
            identity.command_aliases(directory)
            self.assertEqual(os.readlink(directory / (FORMER + "d")), "personad")
            self.assertTrue(os.path.samefile(directory / "personad", directory / (FORMER + "d")))
    def test_legacy_command_layout_upgrades_without_alias_loops(self):
        for name in ("personad", "persona-link", "personad-wechat", "personad-telegram"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                legacy = name.replace("personad", FORMER + "d").replace("persona-link", FORMER + "-link")
                old, current = directory / legacy, directory / name
                executable(old, "#!/bin/sh\nexit 0\n")
                current.symlink_to(legacy)
                identity.command_aliases(directory)
                self.assertFalse(old.is_symlink())
                self.assertTrue(current.samefile(old))
                new = directory / (name + ".new")
                executable(new, "#!/bin/sh\nprintf new\n")
                current.unlink()
                new.replace(current)
                identity.command_aliases(directory)
                self.assertFalse(current.is_symlink())
                self.assertEqual(os.readlink(old), name)
                self.assertTrue(old.samefile(current))
                inode = old.lstat().st_ino
                identity.command_aliases(directory)
                self.assertEqual(old.lstat().st_ino, inode)

    def test_install_program_replaces_old_dangling_and_looped_symlinks(self):
        source = (ROOT / "scripts/deploy/deploy.sh").read_text()
        function = source.split("install_program() {", 1)[1].split("\n}\n", 1)[0]
        function = "install_program() {" + function + "\n}\n"
        for mode in ("local", "remote"):
            for layout in ("old", "dangling", "loop"):
                with self.subTest(mode=mode, layout=layout), tempfile.TemporaryDirectory() as temporary:
                    home = Path(temporary)
                    work = home / "server"
                    work.mkdir()
                    (work / "bin").mkdir()
                    scripts = home / "scripts"
                    scripts.mkdir()
                    (scripts / "persona_identity.py").write_bytes((ROOT / "scripts/persona_identity.py").read_bytes())
                    state = home / "state with spaces"
                    binaries = state / "bin"
                    binaries.mkdir(parents=True)
                    current = binaries / "personad-wechat"
                    old = binaries / (FORMER + "d-wechat")
                    if layout == "old":
                        executable(old, "#!/bin/sh\nprintf old\n")
                    elif layout == "loop":
                        old.symlink_to(current.name)
                    current.symlink_to(old.name)
                    script = r'''set -eu
LDFLAGS=fixture
PERSONA_HUB_SSH=unused
is_here() { [ "$MODE" = local ]; }
state_root() { printf '%s' "$PERSONA_HOME"; }
go() { printf '#!/bin/sh\nprintf new\n' > bin/personad-wechat-linux-amd64; chmod 755 bin/personad-wechat-linux-amd64; }
persona_alias_binaries() { python3 ../scripts/persona_identity.py --alias-binaries "$1"; }
remote_state() { eval "$2"; }
''' + function + 'install_program fixture linux-amd64 personad-wechat\n'
                    env = environment(home)
                    env.update(MODE=mode, PERSONA_HOME=str(state))
                    result = subprocess.run(["bash", "--noprofile", "--norc"], input=script,
                                            cwd=work, env=env, text=True, capture_output=True, timeout=10)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertFalse(current.is_symlink())
                    self.assertIn("printf new", current.read_text())
                    self.assertEqual(os.readlink(old), current.name)
                    self.assertTrue(old.samefile(current))

    def test_compose_config_offline_uses_one_project_database_and_both_environment_names(self):
        import shutil
        if not shutil.which('docker'):
            self.skipTest('Docker Compose is unavailable; no containers are started by this check')
        for canonical in (False, True):
            with self.subTest(canonical=canonical), tempfile.TemporaryDirectory() as directory:
                env = {'PATH': os.environ['PATH'], 'HOME': directory,
                       PREFIX + 'IMAGE': 'example.invalid/fixture-old:1', PREFIX + 'DB_PASSWORD': 'old123',
                       PREFIX + 'PUBLIC_URL': 'https://old.example.test', PREFIX + 'PORT': '19060'}
                if os.environ.get('PERSONA_TEST_DOCKER_CONFIG'):
                    env['DOCKER_CONFIG'] = os.environ['PERSONA_TEST_DOCKER_CONFIG']
                if canonical:
                    env.update(PERSONA_IMAGE='example.invalid/fixture-new:2', PERSONA_DB_PASSWORD='new456',
                               PERSONA_PUBLIC_URL='https://new.example.test', PERSONA_PORT='19061')
                result = subprocess.run(['docker', 'compose', '-f', str(ROOT / 'deploy/docker/compose.yaml'),
                                         'config', '--format', 'json'], env=env, capture_output=True, text=True, timeout=15)
                if 'is not a docker command' in result.stderr:
                    self.skipTest('Docker Compose is unavailable')
                self.assertEqual(result.returncode, 0, result.stderr)
                config = json.loads(result.stdout)
                self.assertEqual(config['name'], FORMER)
                hub = config['services']['hub']
                selected = hub['environment']
                self.assertEqual(selected['PERSONA_DATABASE_URL'], selected[PREFIX + 'DATABASE_URL'])
                self.assertEqual(selected['PERSONA_PUBLIC_URL'], selected[PREFIX + 'PUBLIC_URL'])
                self.assertIn('postgres://' + FORMER + ':' + ('new456' if canonical else 'old123'), selected['PERSONA_DATABASE_URL'])
                self.assertEqual(config['services']['postgres']['environment']['POSTGRES_USER'], FORMER)
                self.assertEqual(config['services']['postgres']['environment']['POSTGRES_DB'], FORMER)
                self.assertEqual(hub['image'], 'example.invalid/fixture-new:2' if canonical else 'example.invalid/fixture-old:1')

    def test_compose_config_needs_one_image_spelling(self):
        import shutil
        if not shutil.which('docker'):
            self.skipTest('Docker Compose is unavailable; no containers are started by this check')
        for image in ('example.invalid/fixture-new:2', None):
            with self.subTest(image=image), tempfile.TemporaryDirectory() as directory:
                env = {'PATH': os.environ['PATH'], 'HOME': directory,
                       'COMPOSE_PROJECT_NAME': 'persona-test-' + os.urandom(4).hex()}
                if os.environ.get('PERSONA_TEST_DOCKER_CONFIG'):
                    env['DOCKER_CONFIG'] = os.environ['PERSONA_TEST_DOCKER_CONFIG']
                if image:
                    env['PERSONA_IMAGE'] = image
                result = subprocess.run(['docker', 'compose', '-f', str(ROOT / 'deploy/docker/compose.yaml'),
                                         'config', '--format', 'json'], env=env, capture_output=True, text=True, timeout=15)
                if 'is not a docker command' in result.stderr:
                    self.skipTest('Docker Compose is unavailable')
                if image:
                    # The former spelling unset: Compose before 5.0.2 still evaluated a required check inside the default.
                    self.assertEqual(result.returncode, 0, result.stderr)
                    hub = json.loads(result.stdout)['services']['hub']
                    self.assertEqual((hub['image'], hub['environment']['PERSONA_IMAGE']), (image, image))
                else:
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertIn('"hub"', result.stderr)
                    self.assertIn('image', result.stderr)


if __name__ == "__main__":
    unittest.main()
