"""Canonical identity source checks without Android SDK; not a substitute for Gradle or device tests."""
import os
import re
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ANDROID_NAME = '{http://schemas.android.com/apk/res/android}name'
ANDROID_TARGET = '{http://schemas.android.com/apk/res/android}targetActivity'


class PersonaIdentifiersTest(unittest.TestCase):
    def test_go_commands_have_only_canonical_source_directories(self):
        former = 'jar' + 'vis'
        for command, retired in (('personad', former + 'd'), ('personad-wechat', former + 'd-wechat')):
            target = ROOT / 'server/cmd' / command
            self.assertTrue(target.is_dir())
            self.assertFalse(target.is_symlink())
            self.assertFalse(os.path.lexists(target.parent / retired))
        self.assertTrue((ROOT / 'server/go.mod').read_text().startswith('module github.com/nwverse/persona/server\n'))
        for path in (ROOT / 'server').rglob('*.go'):
            for module in re.findall(r'"(github\.com/[^/]+/[^/]+/server)/internal/', path.read_text()):
                self.assertEqual('github.com/nwverse/persona/server', module)

    def test_application_id_property_has_no_former_brand_fallback(self):
        source = (ROOT / 'android/app/build.gradle.kts').read_text()
        self.assertRegex(source, r'providers\.gradleProperty\("personaApplicationId"\)\s*'
                         r'\.getOrElse\("com\.example\.persona"\)')
        self.assertNotIn(('jar' + 'vis') + 'ApplicationId', source)
        self.assertIn('namespace = "persona.android"', source)

    def test_launcher_and_share_activities_are_canonical_without_brand_aliases(self):
        manifest = ET.parse(ROOT / 'android/app/src/main/AndroidManifest.xml').getroot()
        app = manifest.find('application')
        self.assertEqual('.PersonaApp', app.get(ANDROID_NAME))
        aliases = {item.get(ANDROID_NAME): item.get(ANDROID_TARGET) for item in app.findall('activity-alias')}
        self.assertEqual({}, aliases)
        self.assertTrue({'.MainActivity', '.ShareActivity'} <= {item.get(ANDROID_NAME) for item in app.findall('activity')})
        launchers = [component.get(ANDROID_NAME) for component in app if any(
            category.get(ANDROID_NAME) == 'android.intent.category.LAUNCHER'
            for category in component.findall('intent-filter/category'))]
        self.assertEqual(['.MainActivity'], launchers)
        helper = (ROOT / 'android/app/src/main/java/persona/android/ActivityComponents.kt').read_text()
        self.assertIn('Intent(context, MainActivity::class.java)', helper)
        self.assertNotIn('setClassName', helper)
        for relative in ('android/app/src/main/java/persona/android/ShareActivity.kt',
                         'android/app/src/main/java/persona/android/platform/LockScreenTests.kt',
                         'android/app/src/debug/java/persona/android/debug/DebugEntryActivity.kt'):
            caller = (ROOT / relative).read_text()
            self.assertIn('mainActivityIntent(', caller)
            self.assertNotIn('MainActivity::class.java', caller)
        self.assertEqual({('persona', 'pair'), ('persona', 'open'),
                          ('@string/compatibility_legacy_scheme', 'pair'),
                          ('@string/compatibility_legacy_scheme', 'open')}, {
            (data.get('{http://schemas.android.com/apk/res/android}scheme'),
             data.get('{http://schemas.android.com/apk/res/android}host'))
            for data in app.findall('activity/intent-filter/data')
            if data.get('{http://schemas.android.com/apk/res/android}scheme')
        })

    def test_debug_metadata_is_queried_on_the_canonical_target_not_the_alias(self):
        source = (ROOT / 'android/app/src/main/java/persona/android/MainActivity.kt').read_text()
        self.assertIn('getActivityInfo(ComponentName(this, MainActivity::class.java), PackageManager.GET_META_DATA)', source)
        self.assertNotIn('getActivityInfo(componentName,', source)
        self.assertIn('getString("persona.launchAdapter")', source)
        debug = ET.parse(ROOT / 'android/app/src/debug/AndroidManifest.xml').getroot().find('application')
        target = next(item for item in debug.findall('activity') if item.get(ANDROID_NAME) == 'persona.android.MainActivity')
        metadata = target.find('meta-data')
        self.assertEqual('persona.launchAdapter', metadata.get(ANDROID_NAME))
        self.assertEqual('persona.android.debug.DebugLaunchAdapter', metadata.get('{http://schemas.android.com/apk/res/android}value'))
        main = ET.parse(ROOT / 'android/app/src/main/AndroidManifest.xml').getroot()
        self.assertFalse(any(item.get(ANDROID_NAME) == 'persona.launchAdapter' for item in main.iter('meta-data')))
        adapter = (ROOT / 'android/app/src/debug/java/persona/android/debug/DebugLaunchAdapter.kt').read_text()
        self.assertIn('class DebugLaunchAdapter : LaunchAdapter', adapter)

    def test_pairing_wrappers_share_compatible_links_before_permission_and_networking(self):
        helper = (ROOT / 'android/app/src/main/java/persona/android/platform/PairingLinks.kt').read_text()
        self.assertIn('if (PairingInput.isLink(url)) PairingInput.parse(url) else null', helper)
        pairing_input = (ROOT / 'android/feature/settings/src/main/kotlin/persona/feature/settings/PairingInput.kt').read_text()
        self.assertIn('fun isLink(value: String): Boolean = Pairing.isLink(value)', pairing_input)
        link_return = 'if (isLink(address)) return parse(address)'
        self.assertIn(link_return, pairing_input)
        self.assertLess(pairing_input.index(link_return), pairing_input.index('val url = withScheme(address.trim())'))
        pairing = (ROOT / 'android/core/domain/src/main/kotlin/persona/core/domain/Pairing.kt').read_text()
        self.assertIn('const val scheme = "persona"', pairing)
        self.assertIn('fun isLink(text: String): Boolean = ProtocolCompatibility.isAppLink(text)', pairing)
        compatibility = (ROOT / 'android/core/contracts/src/main/kotlin/persona/core/contracts/ProtocolCompatibility.kt').read_text()
        schemes = re.search(r'val appSchemes = setOf\(([^)]+)\)', compatibility)
        self.assertIsNotNone(schemes)
        self.assertEqual(re.findall(r'"([^"]+)"', schemes.group(1)), ['persona', 'jar' + 'vis'])
        self.assertIn('appSchemes.any { text.trim().startsWith("$it://") }', compatibility)
        resources = ET.parse(ROOT / 'android/app/src/main/res/values/compatibility.xml').getroot()
        legacy = resources.find("string[@name='compatibility_legacy_scheme']")
        self.assertEqual(legacy.text, 'jar' + 'vis')
        self.assertEqual(legacy.get('translatable'), 'false')
        self.assertNotIn(('jar' + 'vis') + '://', pairing)
        self.assertNotIn('val schemes =', pairing)
        self.assertIn('PairingInput.parse(url)', helper)
        platform = (ROOT / 'android/app/src/main/java/persona/android/platform/LocalNetworkPermission.kt').read_text()
        self.assertEqual(2, platform.count('val parsed = request.parsedPairingLink() ?: request'))
        self.assertEqual(2, platform.count('permission.ensureAllowed(parsed)'))
        self.assertEqual(2, platform.count('permission.missingFor(parsed)'))
        self.assertNotIn('startsWith("persona:")', platform)
        wired = (ROOT / 'android/app/src/main/java/persona/android/di/PairingModule.kt').read_text()
        self.assertIn('val parsed = request.parsedPairingLink()', wired)
        self.assertIn('HubPairingClient.addresses(parsed)', wired)
        self.assertNotIn('startsWith("persona:")', wired)

    def test_only_one_foreground_service_identity_is_registered(self):
        manifest = ET.parse(ROOT / 'android/platform/notify/src/main/AndroidManifest.xml').getroot()
        app = manifest.find('application')
        self.assertEqual(['persona.platform.notify.HubService'], [item.get(ANDROID_NAME) for item in app.findall('service')])
        receivers = {item.get(ANDROID_NAME) for item in app.findall('receiver')}
        self.assertTrue({'persona.platform.notify.ActionReceiver'} <= receivers)
        source = (ROOT / 'android/platform/notify/src/main/kotlin/persona/platform/notify/HubService.kt').read_text()
        self.assertEqual(2, source.count('Intent(context, persona.platform.notify.HubService::class.java)'))

    def test_former_component_wrappers_are_absent_and_canonical_implementations_exist(self):
        retired_paths = (
            'android/platform/notify/src/main/kotlin/persona/platform/notify/LegacyComponents.kt',
            'android/platform/phonetools/src/main/kotlin/persona/platform/phonetools/LegacyPhoneNotificationWorker.kt',
            'android/app/src/main/java/persona/android/platform/LegacyShareRetryWorker.kt',
        )
        for relative in retired_paths:
            self.assertFalse(os.path.lexists(ROOT / relative), relative)
        implementations = {
            'android/platform/notify/src/main/kotlin/persona/platform/notify/HubService.kt': 'HubService',
            'android/platform/notify/src/main/kotlin/persona/platform/notify/ActionReceiver.kt': 'ActionReceiver',
            'android/platform/notify/src/main/kotlin/persona/platform/notify/BootReceiver.kt': 'BootReceiver',
            'android/platform/notify/src/main/kotlin/persona/platform/notify/ConnectionWatchdog.kt': 'ConnectionWatchdog',
            'android/platform/phonetools/src/main/kotlin/persona/platform/phonetools/PhoneNotifications.kt': 'PhoneNotificationWorker',
            'android/app/src/main/java/persona/android/platform/ShareQueue.kt': 'ShareRetryWorker',
        }
        for relative, name in implementations.items():
            source = (ROOT / relative).read_text()
            self.assertRegex(source, rf'\bclass {name}\b')
            self.assertRegex(source, r'^package persona\.',)
            self.assertNotRegex(source, rf'class {name}(?:\([^\n]*\))?\s*:\s*persona\.[\w.]*{name}\(')

    def test_saved_route_type_names_stay_stable(self):
        source = (ROOT / 'android/core/navigation/src/main/kotlin/persona/core/navigation/Routes.kt').read_text()
        for name in ('InboxItem', 'LarkConversation', 'Interaction'):
            self.assertIn(f'@SerialName("persona.core.navigation.RouteSource.{name}")', source)
        for name in ('AssistantRoute', 'AgentsRoute', 'InboxRoute', 'SettingsRoute', 'ChatRoute'):
            self.assertIn(f'@SerialName("persona.core.navigation.{name}")', source)

    def test_primary_tab_keys_and_retired_shell_state(self):
        source = (ROOT / 'android/core/navigation/src/main/kotlin/persona/core/navigation/Routes.kt').read_text()
        self.assertIn('enum class AppTab { Persona, Agents }', source)
        # Tabs are saved in ShellPosition; retired tab values discard the old shell instead of migrating it.
        position = (ROOT / 'android/app/src/main/java/persona/android/navigation/ShellPosition.kt').read_text()
        self.assertIn('coerceInputValues = false', position)
        self.assertIn('.getOrDefault(ShellPosition())', position)


if __name__ == '__main__':
    unittest.main()
