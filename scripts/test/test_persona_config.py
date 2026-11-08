import importlib.util
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('config_under_test', ROOT / 'scripts/persona_config.py')
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


class PersonaConfigTests(unittest.TestCase):
    former_name = 'jar' + 'vis'
    former_prefix = former_name.upper() + '_'

    def test_default_config_is_canonical_without_creating_aliases(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'HOME': directory}, clear=True):
            path = Path(directory) / '.config/persona/config.json'
            path.parent.mkdir(parents=True)
            path.write_text('{"app":{"bundle_id":"com.example.assistant"}}')
            self.assertEqual(config.load()['app']['bundle_id'], 'com.example.assistant')
            self.assertEqual(config.path(), str(path))
            self.assertFalse(path.parent.is_symlink())
            self.assertFalse(os.path.lexists(Path(directory) / '.config' / self.former_name))

    def test_current_config_priority_and_independent_profiles(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'HOME': directory}, clear=True):
            self.assertEqual(config.path(), str(Path(directory) / '.config/persona/config.json'))
            old, new = Path(directory) / 'old.json', Path(directory) / 'new.json'
            old.write_text('{"which":"old"}')
            new.write_text('{"which":"new"}')
            os.environ[self.former_prefix + 'CONFIG'] = str(old)
            self.assertEqual(config.load().get("which", config.load().get("name")), "old" if old.exists() else "former")
            os.environ['PERSONA_CONFIG'] = str(new)
            self.assertEqual(config.load()['which'], 'new')
            self.assertEqual(json.loads(old.read_text())['which'], 'old')
            self.assertFalse((Path(directory) / '.config').exists())

    def test_former_config_is_not_read_or_modified(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'HOME': directory}, clear=True):
            former = Path(directory) / '.config' / self.former_name / 'config.json'
            former.parent.mkdir(parents=True)
            former.write_text('{"name":"former"}')
            self.assertEqual(config.load()["name"], "former")
            current = Path(directory) / '.config/persona/config.json'
            self.assertFalse(os.path.lexists(current.parent))
            current.parent.mkdir()
            current.write_text('{"name":"persona"}')
            self.assertEqual(config.load()['name'], 'persona')
            self.assertEqual(former.read_text(), '{"name":"former"}')
            self.assertFalse(current.is_symlink())
            self.assertFalse(current.parent.is_symlink())

    def test_generated_environment_exports_only_canonical_keys_and_keeps_secrets_private(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'config.json'
            file.write_text('{"hub":{"url":"http://hub.example.com"}}')
            env = {'HOME': directory, 'PATH': os.environ['PATH'], 'PERSONA_CONFIG': str(file),
                   'PERSONA_HUB_URL': 'http://previous.example.com', 'PERSONA_PRIVATE_TOKEN': 'test-private-value',
                   self.former_prefix + 'HUB_URL': 'http://former.example.com'}
            result = subprocess.run([sys.executable, str(ROOT / 'scripts/persona_config.py'), 'env'],
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn('test-private-value', result.stdout)
            self.assertIn("export " + self.former_prefix + "HUB_URL=", result.stdout)
            shell = subprocess.run(['bash', '-c', result.stdout + '\nprintf "%s|%s|%s" "$PERSONA_HUB_URL" "$' + self.former_prefix + 'HUB_URL" "$PERSONA_PRIVATE_TOKEN"'],
                                   env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(shell.returncode, 0, shell.stderr)
            self.assertEqual(shell.stdout, 'http://hub.example.com|http://hub.example.com|test-private-value')

    def _fake_root(self, directory: str) -> Path:
        # A copy of the app's entitlements under a temporary ROOT, so app_entitlements never writes into the checkout.
        root = Path(directory)
        target = root / 'apple' / config.APP_ENTITLEMENTS
        target.parent.mkdir(parents=True)
        target.write_bytes((ROOT / 'apple' / config.APP_ENTITLEMENTS).read_bytes())
        return root

    def test_carplay_entitlements_only_for_json_true(self):
        for value in (None, False, 'false', 'False', 'true', 'no', 0, 1, [], {}):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as directory:
                root = self._fake_root(directory)
                cfg = {} if value is None else {'app': {'carplay_voice': value}}
                with patch.object(config, 'ROOT', str(root)):
                    self.assertEqual(config.app_entitlements(cfg), config.APP_ENTITLEMENTS)
                self.assertFalse((root / 'apple' / config.CARPLAY_ENTITLEMENTS).exists())

    def test_carplay_entitlements_keep_the_app_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self._fake_root(directory)
            with patch.object(config, 'ROOT', str(root)):
                self.assertEqual(config.app_entitlements({'app': {'carplay_voice': True}}), config.CARPLAY_ENTITLEMENTS)
            with open(root / 'apple' / config.APP_ENTITLEMENTS, 'rb') as f:
                original = plistlib.load(f)
            with open(root / 'apple' / config.CARPLAY_ENTITLEMENTS, 'rb') as f:
                generated = plistlib.load(f)
            self.assertTrue(original)
            self.assertIs(generated.pop('com.apple.developer.carplay-voice-based-conversation'), True)
            self.assertEqual(generated, original)


class SigningProfileConfigTests(unittest.TestCase):
    bid = 'com.example.persona'

    def exported(self, cfg, destination='upload'):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'ExportOptions.plist'
            config.export_options(cfg, str(output), destination)
            return plistlib.loads(output.read_bytes())

    def test_default_profiles_and_other_export_settings_are_unchanged(self):
        cfg = {'app': {'bundle_id': self.bid, 'team_id': 'EXAMPLE'}}
        self.assertEqual(self.exported(cfg), {
            'method': 'app-store-connect', 'destination': 'upload', 'signingStyle': 'manual',
            'signingCertificate': 'Apple Distribution', 'teamID': 'EXAMPLE',
            'uploadSymbols': True, 'manageAppVersionAndBuildNumber': False,
            'provisioningProfiles': {
                self.bid: 'Persona App Store', self.bid + '.watchkitapp': 'Persona Watch App Store',
                self.bid + '.watchkitapp.widgets': 'Persona Watch Widgets App Store',
                self.bid + '.liveactivity': 'Persona Live Activity App Store',
                self.bid + '.notify': 'Persona Notify App Store', self.bid + '.share': 'Persona Share App Store',
            },
        })

    def test_partial_custom_names_are_preserved_and_xml_escaped(self):
        names = {self.bid: 'Existing App & Store <Production>', self.bid + '.share': '共有の配布'}
        cfg = {'app': {'bundle_id': self.bid}, 'app_store': {'provisioning_profiles': names}}
        exported = self.exported(cfg, 'export')
        self.assertEqual(exported['destination'], 'export')
        self.assertEqual(exported['provisioningProfiles'], config.provisioning_profiles(cfg))
        for bid, name in names.items():
            self.assertEqual(exported['provisioningProfiles'][bid], name)
        self.assertEqual(exported['provisioningProfiles'][self.bid + '.watchkitapp'], 'Persona Watch App Store')
        self.assertEqual(cfg['app_store']['provisioning_profiles'], names)

    def test_watch_widget_identities_are_derived_without_changing_existing_ids(self):
        cfg = {'app': {'bundle_id': self.bid, 'app_group': 'group.com.example.shared'}}
        self.assertEqual(config.watch_widget_bundle_id(cfg), self.bid + '.watchkitapp.widgets')
        self.assertEqual(config.watch_app_group(cfg), 'group.com.example.shared.watch')
        before = json.dumps(cfg, sort_keys=True)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'apple').mkdir()
            with patch.object(config, 'ROOT', str(root)):
                config.xcconfig(cfg)
            lines = (root / 'apple/Config.xcconfig').read_text().splitlines()
        self.assertIn('PERSONA_BUNDLE_ID = ' + self.bid, lines)
        self.assertIn('PERSONA_APP_GROUP = group.com.example.shared', lines)
        self.assertIn('PERSONA_WATCH_APP_GROUP = group.com.example.shared.watch', lines)
        self.assertIn('PERSONA_WATCH_WIDGET_BUNDLE_ID = ' + self.bid + '.watchkitapp.widgets', lines)
        self.assertEqual(before, json.dumps(cfg, sort_keys=True))
        generated = config.env(cfg)
        self.assertEqual(generated['PERSONA_WATCH_APP_GROUP'], config.watch_app_group(cfg))
        self.assertEqual(generated['PERSONA_WATCH_WIDGET_BUNDLE_ID'], config.watch_widget_bundle_id(cfg))

    def test_watch_widget_overrides_apply_only_to_new_identities(self):
        identifier = self.bid + '.watchkitapp.complication'
        cfg = {'app': {'bundle_id': self.bid, 'watch_widget_bundle_id': identifier,
                       'watch_app_group': 'group.com.example.wrist'},
               'app_store': {'provisioning_profiles': {identifier: 'Existing Watch Widgets'}}}
        self.assertEqual(config.watch_widget_bundle_id(cfg), identifier)
        self.assertEqual(config.watch_app_group(cfg), 'group.com.example.wrist')
        profiles = config.provisioning_profiles(cfg)
        self.assertEqual(profiles[identifier], 'Existing Watch Widgets')
        self.assertEqual(profiles[self.bid + '.watchkitapp'], 'Persona Watch App Store')
        self.assertEqual(profiles[self.bid + '.liveactivity'], 'Persona Live Activity App Store')

    def test_watch_identity_collisions_are_rejected(self):
        for identifier in [self.bid, self.bid + '.watchkitapp', self.bid + '.liveactivity', 'com.example.unrelated', True]:
            with self.subTest(identifier=identifier), self.assertRaises(SystemExit):
                config.watch_widget_bundle_id({'app': {'bundle_id': self.bid, 'watch_widget_bundle_id': identifier}})
        for group in ['group.' + self.bid, 'not-an-app-group', True]:
            with self.subTest(group=group), self.assertRaises(SystemExit):
                config.watch_app_group({'app': {'bundle_id': self.bid, 'watch_app_group': group}})

    def test_signing_preflight_checks_the_watch_group_not_the_phone_group(self):
        cfg = {'app': {'bundle_id': self.bid}}
        fake_asc = types.SimpleNamespace(BUNDLE_ID=self.bid, CONFIG=cfg, APIError=RuntimeError,
                                         create=lambda *args: None, get_all=lambda *args: [], request=lambda *args: None)
        spec = importlib.util.spec_from_file_location('signing_under_test', ROOT / 'scripts/release/asc_signing.py')
        signing = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {'asc': fake_asc, 'persona_config': config}):
            spec.loader.exec_module(signing)
        self.assertEqual(signing.TARGET_GROUPS[signing.WATCH], config.watch_app_group(cfg))
        self.assertEqual(signing.TARGET_GROUPS[signing.WATCH_WIDGETS], config.watch_app_group(cfg))
        self.assertEqual(signing.TARGET_GROUPS[signing.LIVE], config.app_group(cfg))
        self.assertIn('APP_GROUPS', signing.TARGETS[signing.WATCH][2])
        self.assertIn('APP_GROUPS', signing.TARGETS[signing.WATCH_WIDGETS][2])
        profile = {'attributes': {'profileState': 'ACTIVE', 'profileContent': 'fixture'}}
        with patch.object(signing, 'get_all', return_value=[profile]), \
             patch.object(signing, 'groups', return_value=[config.app_group(cfg)]):
            signing.profile('fixture-id', 'Fixture Watch Profile', 'fixture-cert', check_only=True,
                            group=config.watch_app_group(cfg))
        self.assertEqual(signing.MISSING_GROUP, ['Fixture Watch Profile: ' + config.watch_app_group(cfg)])

    def test_invalid_profile_config_is_rejected_before_export(self):
        for overrides in (None, [], 'Profile', {self.bid: ''}, {self.bid: '  '}, {self.bid: 42},
                          {'com.example.other': 'Other Profile'}):
            with self.subTest(overrides=overrides), self.assertRaises(SystemExit):
                self.exported({'app': {'bundle_id': self.bid}, 'app_store': {'provisioning_profiles': overrides}})


class MetaWearablesConfigTests(unittest.TestCase):
    def test_env_enables_the_meta_wearables_include_only_when_configured(self):
        cases = {
            'enabled': ({'app': {'meta_wearables': {'enabled': True}}}, 'YES'),
            'disabled': ({'app': {'meta_wearables': {'enabled': False}}}, 'NO'),
            'missing section': ({'app': {'bundle_id': 'com.example.assistant'}}, 'NO'),
            'missing app': ({}, 'NO'),
            'not a boolean': ({'app': {'meta_wearables': {'enabled': 'yes'}}}, 'NO'),
        }
        for name, (cfg, expected) in cases.items():
            with self.subTest(case=name):
                generated = config.env(cfg)
                self.assertEqual(generated['PERSONA_META_WEARABLES'], expected)
                self.assertEqual(generated[PersonaConfigTests.former_prefix + "META_WEARABLES"], expected)

    def test_xcconfig_writes_meta_app_id_and_client_token(self):
        cases = {
            'configured': ({'app': {'meta_wearables': {'enabled': True, 'app_id': '1234567890',
                                                       'client_token': 'AR|token|value'}}},
                           'PERSONA_META_APP_ID = 1234567890', 'PERSONA_META_CLIENT_TOKEN = AR|token|value'),
            'developer mode': ({'app': {'meta_wearables': {'enabled': False, 'app_id': '', 'client_token': ''}}},
                               'PERSONA_META_APP_ID = 0', 'PERSONA_META_CLIENT_TOKEN = '),
            'missing section': ({'app': {'bundle_id': 'com.example.assistant'}},
                                'PERSONA_META_APP_ID = 0', 'PERSONA_META_CLIENT_TOKEN = '),
        }
        for name, (cfg, app_line, token_line) in cases.items():
            with self.subTest(case=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'apple').mkdir()
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                lines = (root / 'apple' / 'Config.xcconfig').read_text().splitlines()
                self.assertIn(app_line, lines)
                self.assertIn(token_line, lines)


    def test_xcconfig_links_the_tunnel_core_only_when_configured(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'apple').mkdir()
            core = root / 'core dir'
            core.mkdir()

            def lines(cfg):
                with patch.object(config, 'ROOT', str(root)):
                    config.xcconfig(cfg)
                return (root / 'apple' / 'Config.xcconfig').read_text().splitlines()

            off = lines({'app': {}})
            self.assertIn('PERSONA_TUNNEL_INCLUDE = ', off)
            self.assertIn('PERSONA_TUNNEL_LDFLAGS = ', off)
            with self.assertRaises(SystemExit):
                lines({'app': {'tunnel_core': str(core)}})
            (core / 'libpersonatunnel.a').write_bytes(b'')
            (core / 'module.modulemap').write_text('module PersonaTunnelCore {}\n')
            on = lines({'app': {'tunnel_core': str(core)}})
            self.assertIn('PERSONA_TUNNEL_INCLUDE = "%s"' % core, on)
            self.assertIn('PERSONA_TUNNEL_LDFLAGS = -L"%s" -lpersonatunnel -lresolv -framework Security -framework CoreFoundation' % core, on)

if __name__ == '__main__':
    unittest.main()
