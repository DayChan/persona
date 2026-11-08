"""Compiler-free checks for canonical Apple product identities and script entry points."""
import importlib.util
import json
from pathlib import Path
import plistlib
import re
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
APPLE = ROOT / 'apple'
PROJECT = (APPLE / 'project.yml').read_text()
TARGETS = PROJECT.split('targets:\n', 1)[1].split('schemes:\n', 1)[0]
SCHEMES = PROJECT.split('schemes:\n', 1)[1]
FORMER_NAME = ('jar' + 'vis').capitalize()


def blocks(text):
    matches = list(re.finditer(r'^  (\w+):\n', text, re.MULTILINE))
    return {m.group(1): text[m.end():matches[i + 1].start() if i + 1 < len(matches) else len(text)]
            for i, m in enumerate(matches)}


def load_tests(loader, tests, pattern):
    # Keep Apple-owned source guards in the full scripts discovery without duplicating their assertions here.
    spec = importlib.util.spec_from_file_location('apple_persona_only', APPLE / 'PersonaTests/test_persona_only.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    tests.addTests(loader.loadTestsFromModule(module))
    return tests


class AppleRenameTests(unittest.TestCase):
    def test_sources_use_only_canonical_directories_and_project(self):
        for suffix in ('', 'Watch', 'Widgets', 'Notify', 'Tests', 'MacTests', 'UITests'):
            with self.subTest(directory=suffix):
                self.assertFalse((APPLE / (FORMER_NAME + suffix)).exists())
                self.assertTrue((APPLE / ('Persona' + suffix)).is_dir())
        self.assertFalse((APPLE / 'PersonaLink/Sources' / (FORMER_NAME + 'Link')).exists())
        self.assertTrue((APPLE / 'PersonaLink/Sources/PersonaLink').is_dir())
        tracked = subprocess.run(['git', 'ls-files', '--', 'mac'], cwd=ROOT, capture_output=True, text=True, check=True).stdout
        self.assertEqual(tracked, '', 'the menu bar app is apple/PersonaLink/')
        self.assertIn('name: Persona\n', PROJECT)
        if (APPLE / 'Persona.xcodeproj').exists():
            self.assertTrue((APPLE / 'Persona.xcodeproj').is_dir())
        self.assertFalse((APPLE / 'Persona.xcodeproj').is_symlink())
        former_project = APPLE / (FORMER_NAME + '.xcodeproj')
        self.assertFalse(former_project.exists() or former_project.is_symlink())

    def test_targets_use_canonical_product_modules(self):
        targets = blocks(TARGETS)
        expected = {
            'Persona': 'Persona', 'PersonaMac': 'Persona', 'PersonaWatch': 'Persona', 'PersonaVision': 'Persona',
            'PersonaWidgets': 'PersonaWidgets', 'PersonaNotify': 'PersonaNotify', 'PersonaMacNotify': 'PersonaNotify',
            'PersonaShare': 'PersonaShare', 'PersonaVisionShare': 'PersonaShare',
            'PersonaWatchWidgets': 'PersonaWatchWidgets',
        }
        self.assertEqual(set(targets), {*expected, 'PersonaTests', 'PersonaMacTests', 'PersonaUITests', 'PersonaMacUITests'})
        for target, module in expected.items():
            with self.subTest(target=target):
                self.assertIn('PRODUCT_MODULE_NAME: ' + module + '\n', targets[target])
                self.assertIn('PRODUCT_NAME: Persona', targets[target])
        self.assertIn('TEST_TARGET_NAME: Persona\n', targets['PersonaUITests'])
        self.assertIn('TEST_TARGET_NAME: PersonaMac\n', targets['PersonaMacUITests'])
        self.assertIn('Persona.app/Persona"', targets['PersonaTests'])
        self.assertIn('Persona.app/Contents/MacOS/Persona"', targets['PersonaMacTests'])

    def test_vision_target_uses_canonical_source_and_scheme(self):
        target = blocks(TARGETS)['PersonaVision']
        self.assertIn('platform: visionOS\n', target)
        self.assertIn('- path: Vision\n', target)
        self.assertIn('PRODUCT_NAME: Persona\n', target)
        self.assertTrue((APPLE / 'Vision').is_dir())
        self.assertIn('PersonaVision: all\n', blocks(SCHEMES)['PersonaVision'])

    def test_only_canonical_schemes_build_existing_targets(self):
        schemes = blocks(SCHEMES)
        self.assertEqual(set(schemes), {'Persona', 'PersonaMac', 'PersonaWatch', 'PersonaVision', 'PersonaMacExtensionUI'})
        for name, scheme in schemes.items():
            target = 'PersonaMac' if name == 'PersonaMacExtensionUI' else name
            self.assertIn(target + ': all\n', scheme)
        self.assertIn('PersonaMacUITests: [test]', schemes['PersonaMacExtensionUI'])
        self.assertFalse((APPLE / (FORMER_NAME + '.xcodeproj')).is_symlink())

    def test_intent_control_activity_and_info_keys_use_canonical_identity(self):
        expected = {
            'Persona/Intents/AskPersonaIntent.swift': ['AskPersonaIntent'],
            'Persona/Intents/PersonaStatusIntent.swift': ['PersonaStatusIntent'],
            'Persona/Intents/PersonaShortcuts.swift': ['PersonaShortcuts'],
            'PersonaWidgets/PersonaControls.swift': ['AskPersonaControl', 'PersonaAgentsControl'],
            'LiveActivity/PersonaControlIntents.swift': ['OpenPersonaAssistantIntent', 'OpenPersonaAgentsIntent'],
            'LiveActivity/PersonaActivityAttributes.swift': ['PersonaActivityAttributes'],
        }
        for path, names in expected.items():
            source = (APPLE / path).read_text()
            for name in names:
                with self.subTest(path=path, name=name):
                    self.assertRegex(source, r'\bstruct ' + name + r'\s*:')
        controls = (APPLE / 'PersonaWidgets/PersonaControls.swift').read_text()
        self.assertIn('kind: "persona.control.ask"', controls)
        self.assertIn('kind: "persona.control.agents"', controls)
        info = plistlib.loads((APPLE / 'PersonaNotify/Info.plist').read_bytes())
        self.assertEqual(info['NSExtension']['NSExtensionPrincipalClass'], '$(PRODUCT_MODULE_NAME).NotificationService')
        self.assertIn('PersonaPushKeychainGroup', info)
        for key in ('PushKeychainGroup', 'PushRelay', 'AppGroup'):
            self.assertIn('Persona' + key + ':', PROJECT)
            self.assertNotIn(FORMER_NAME + key + ':', PROJECT)
        for relative in ('Persona/Resources/Info.plist', 'Mac/Resources/Info.plist'):
            info = plistlib.loads((APPLE / relative).read_bytes())
            self.assertEqual(info['CFBundleURLTypes'][0]['CFBundleURLSchemes'], ['persona', FORMER_NAME.lower()])

    def test_avatar_names_do_not_collide_with_persona_model(self):
        source = (APPLE / 'Persona/Views/Assistant/AssistantAvatar.swift').read_text()
        self.assertIn('struct AssistantAvatar: View', source)
        self.assertIn('enum AssistantAvatarGallery', source)
        self.assertNotIn('struct PersonaAvatar', source)
        self.assertTrue((APPLE / 'Persona/Persona/Persona.swift').is_file())

    def test_baseline_paths_resolve_without_changing_thresholds(self):
        baseline = json.loads((APPLE / 'PersonaTests/TokenBaseline.json').read_text())
        self.assertTrue(baseline)
        for path, thresholds in baseline.items():
            with self.subTest(path=path):
                self.assertFalse(path.startswith(FORMER_NAME))
                self.assertTrue((APPLE / path).is_file())
                self.assertIsInstance(thresholds, dict)
                self.assertTrue(all(isinstance(value, int) and value >= 0 for value in thresholds.values()))

    def test_launch_and_ctl_have_one_canonical_entry(self):
        launch = (APPLE / 'PersonaUITests/LaunchLayout.swift').read_text()
        self.assertEqual(len(re.findall(r'func launchPersona\(\)', launch)), 1)
        self.assertNotRegex(launch, r'func launchPersona\(\)\s*\{\s*launchPersona\(\)')
        self.assertNotIn('func launch' + FORMER_NAME + '()', launch)
        self.assertIn('func launchPinned()', (APPLE / 'PersonaUITests/UITestStyle.swift').read_text())
        command = ROOT / 'scripts/app/personactl.py'
        self.assertTrue(command.is_file())
        self.assertFalse(command.is_symlink())
        alias = command.parent / (FORMER_NAME.lower() + 'ctl.py')
        self.assertTrue(alias.is_symlink())
        self.assertEqual(alias.readlink().as_posix(), command.name)
        self.assertEqual(alias.resolve(), command.resolve())

    def test_menu_bar_package_uses_canonical_process_name(self):
        package = (APPLE / 'PersonaLink/Package.swift').read_text()
        self.assertIn('name: "PersonaLink"', package)
        self.assertIn('path: "Sources/PersonaLink"', package)
        info = plistlib.loads((APPLE / 'PersonaLink/Info.plist').read_bytes())
        self.assertEqual(info['CFBundleExecutable'], 'PersonaLink')
        script = (ROOT / 'scripts/app/mac-link.sh').read_text()
        self.assertIn('-scheme PersonaLink', script)
        self.assertIn('cp "$bin" "$APP/Contents/MacOS/PersonaLink"', script)
        self.assertIn('pkill -x PersonaLink', script)

    def test_entitlement_checker_accepts_only_the_canonical_product(self):
        spec = importlib.util.spec_from_file_location('entitlements', ROOT / 'scripts/release/entitlements.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(Path(module.PROJECT), APPLE / 'Persona.xcodeproj')
        with tempfile.TemporaryDirectory() as directory:
            former = Path(directory) / (FORMER_NAME + '.app')
            former.mkdir()
            with self.assertRaises(SystemExit):
                module.app_bundle(directory)
            app = Path(directory) / 'Persona.app'
            app.mkdir()
            self.assertEqual(Path(module.app_bundle(directory)), app)
            self.assertTrue(former.is_dir())
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(SystemExit):
                module.app_bundle(directory)


if __name__ == '__main__':
    unittest.main()
