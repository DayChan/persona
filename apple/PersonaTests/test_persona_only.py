"""Compiler-free Persona-only Apple guards (#277).

Run with Python and PyYAML: python3 -B apple/PersonaTests/test_persona_only.py
These checks complement, and never replace, Apple builds and Swift tests.
"""

import collections
import json
from pathlib import Path
import plistlib
import re
import shlex
import unittest
import xml.etree.ElementTree as ET

import yaml


APPLE = Path(__file__).resolve().parents[1]


def unique_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate key: {key}")
        result[key] = value
    return result


def check_yaml_keys(node):
    """Inspect the syntax tree: safe_load otherwise silently drops duplicate keys."""
    if isinstance(node, yaml.MappingNode):
        keys = set()
        for key, value in node.value:
            if key.value in keys:
                raise ValueError(f"duplicate YAML key {key.value} at line {key.start_mark.line + 1}")
            keys.add(key.value)
            check_yaml_keys(value)
    elif isinstance(node, yaml.SequenceNode):
        for value in node.value:
            check_yaml_keys(value)


def source(path):
    return (APPLE / path).read_text(encoding="utf-8")


class PersonaOnlyTests(unittest.TestCase):
    def test_retired_native_identifiers_are_confined_to_one_compatibility_source(self):
        # The user-approved exception is one physical compatibility source, never a global relaxation.
        migration = APPLE / "Shared/NativeStateMigration.swift"
        former = "jar" + "vis"
        allowed = {migration.resolve()}
        for path in APPLE.rglob("*.swift"):
            if former in path.read_text(encoding="utf-8").lower():
                self.assertIn(path.resolve(), allowed, path.relative_to(APPLE))
        text = migration.read_text(encoding="utf-8")
        self.assertIn("Compatibility never deletes a previous store", text)

    def test_native_controls_keep_stable_kinds_and_register_interim_kinds_once(self):
        controls = source("PersonaWidgets/PersonaControls.swift")
        for stable in ["controlAskKind", "controlAgentsKind"]:
            self.assertEqual(controls.count(f"Self.configuration(kind: LegacyNames.{stable})"), 1)
        for interim in ["persona.control.ask", "persona.control.agents"]:
            self.assertEqual(controls.count(f'configuration(kind: "{interim}")'), 1)
        bundle = source("PersonaWidgets/PersonaWidgetsBundle.swift")
        for widget in ["AskPersonaControl", "PersonaAgentsControl", "CompatibleAssistantControl", "CompatibleAgentsControl",
                       "FleetLiveActivity", "CompatibleFleetLiveActivity"]:
            self.assertEqual(len(re.findall(rf"\b{widget}\(\)", bundle)), 1)

    def test_duplicate_key_guards_reject_silent_overwrites(self):
        with self.assertRaises(ValueError):
            json.loads('{"Persona": 1, "Persona": 2}', object_pairs_hook=unique_pairs)
        with self.assertRaises(ValueError):
            check_yaml_keys(yaml.compose("schemes:\n  Persona: {}\n  Persona: {}\n"))

    def test_yaml_has_unique_keys_and_build_conditions(self):
        for path in sorted(APPLE.rglob("*")):
            if path.suffix not in {".yml", ".yaml"}:
                continue
            with self.subTest(path=path.relative_to(APPLE)):
                text = path.read_text(encoding="utf-8")
                check_yaml_keys(yaml.compose(text))
                for line in text.splitlines():
                    if "SWIFT_ACTIVE_COMPILATION_CONDITIONS:" in line:
                        tokens = shlex.split(line.split(":", 1)[1].strip().strip('"'))
                        self.assertEqual(len(tokens), len(set(tokens)), line)
                    if "OTHER_SWIFT_FLAGS:" in line:
                        flags = re.findall(r"-D\s*([A-Za-z_][A-Za-z0-9_]*)", line)
                        self.assertEqual(len(flags), len(set(flags)), line)

    def test_project_uses_only_persona_targets_and_schemes(self):
        project = yaml.safe_load(source("project.yml"))
        self.assertEqual(project["name"], "Persona")
        self.assertEqual(set(project["schemes"]), {"Persona", "PersonaMac", "PersonaWatch", "PersonaVision", "PersonaMacExtensionUI"})
        targets = project["targets"]
        for name, target in targets.items():
            with self.subTest(target=name):
                self.assertTrue(name.startswith("Persona"))
                for dependency in target.get("dependencies", []):
                    if "target" in dependency:
                        self.assertIn(dependency["target"], targets)
                for item in target["sources"]:
                    self.assertTrue((APPLE / item["path"]).exists(), item["path"])
        for scheme in project["schemes"].values():
            self.assertTrue(set(scheme["build"]["targets"]).issubset(targets))
        modules = {
            "Persona": "Persona", "PersonaMac": "Persona", "PersonaVision": "Persona", "PersonaWatch": "Persona",
            "PersonaWidgets": "PersonaWidgets", "PersonaNotify": "PersonaNotify", "PersonaMacNotify": "PersonaNotify", "PersonaVisionNotify": "PersonaNotify",
            "PersonaShare": "PersonaShare", "PersonaVisionShare": "PersonaShare",
            "PersonaWatchWidgets": "PersonaWatchWidgets",
        }
        for target, module in modules.items():
            self.assertEqual(targets[target]["settings"]["base"]["PRODUCT_MODULE_NAME"], module)
        self.assertIn("*.py", targets["PersonaTests"]["sources"][0]["excludes"])
        for path in APPLE.glob("*.xcodeproj"):
            self.assertEqual(path.name, "Persona.xcodeproj")
            self.assertFalse(path.is_symlink(), "project aliases must not return")

    def test_plists_have_unique_keys_and_exact_compatible_url_schemes(self):
        registered = set()
        expected = {"Persona/Resources/Info.plist", "Mac/Resources/Info.plist",
                    "Vision/Resources/Info.plist", "PersonaLink/Info.plist", "PersonaWatch/Info.plist"}
        for path in sorted(APPLE.rglob("*")):
            if path.suffix not in {".plist", ".entitlements"}:
                continue
            relative = path.relative_to(APPLE).as_posix()
            with self.subTest(path=relative):
                tree = ET.parse(path)
                for dictionary in tree.iter("dict"):
                    keys = [key.text for key in dictionary if key.tag == "key"]
                    self.assertEqual(len(keys), len(set(keys)), keys)
                values = plistlib.loads(path.read_bytes())
                if "CFBundleURLTypes" in values:
                    self.assertIn(relative, expected)
                    schemes = [scheme for entry in values["CFBundleURLTypes"]
                               for scheme in entry["CFBundleURLSchemes"]]
                    self.assertEqual(schemes, ["persona"] if relative == "PersonaWatch/Info.plist" else ["persona", "jarvis"])
                    registered.add(relative)
        self.assertEqual(registered, expected)
        project = yaml.safe_load(source("project.yml"))
        for target in ["Persona", "PersonaMac", "PersonaVision"]:
            settings = project["targets"][target]
            properties = settings["info"]["properties"]
            self.assertEqual(len(properties["CFBundleURLTypes"]), 1)
            self.assertEqual(properties["CFBundleURLTypes"][0]["CFBundleURLSchemes"], ["persona", "jarvis"])
            self.assertEqual(properties["PersonaPushKeychainGroup"], "$(DEVELOPMENT_TEAM).$(PERSONA_BUNDLE_ID).push")
            plist = plistlib.loads((APPLE / settings["info"]["path"]).read_bytes())
            for key in ["PersonaPushRelay", "PersonaGettingStartedURL", "PersonaPushKeychainGroup"]:
                self.assertEqual(plist[key], properties[key])
        for target in ["Persona", "PersonaWidgets"]:
            settings = project["targets"][target]
            self.assertEqual(settings["info"]["properties"]["PersonaAppGroup"], "$(PERSONA_APP_GROUP)")
            self.assertEqual(settings["entitlements"]["properties"]["com.apple.security.application-groups"], ["$(PERSONA_APP_GROUP)"])
        for path, principal in [("PersonaNotify/Info.plist", "NotificationService"), ("PersonaShare/Info.plist", "ShareViewController")]:
            values = plistlib.loads((APPLE / path).read_bytes())
            self.assertEqual(values["NSExtension"]["NSExtensionPrincipalClass"], f"$(PRODUCT_MODULE_NAME).{principal}")

    def test_extension_system_targets_keep_credentials_and_renderers_separate(self):
        project = yaml.safe_load(source("project.yml"))
        targets = project["targets"]
        self.assertEqual(project["options"]["deploymentTarget"]["watchOS"], "10.0")
        self.assertEqual(targets["PersonaWatchWidgets"]["settings"]["base"]["PRODUCT_BUNDLE_IDENTIFIER"],
                         "$(PERSONA_WATCH_WIDGET_BUNDLE_ID)")
        self.assertIn({"target": "PersonaWatchWidgets"}, targets["PersonaWatch"]["dependencies"])
        for target in ["PersonaWatch", "PersonaWatchWidgets"]:
            settings = targets[target]
            self.assertEqual(settings["info"]["properties"]["PersonaAppGroup"], "$(PERSONA_WATCH_APP_GROUP)")
            self.assertEqual(settings["entitlements"]["properties"]["com.apple.security.application-groups"],
                             ["$(PERSONA_WATCH_APP_GROUP)"])
            plist = plistlib.loads((APPLE / settings["info"]["path"]).read_bytes())
            self.assertEqual(plist["PersonaAppGroup"], "$(PERSONA_WATCH_APP_GROUP)")
        for target in ["PersonaWidgets", "PersonaWatchWidgets"]:
            paths = [item["path"] for item in targets[target]["sources"]]
            for path in ["Shared/ExtensionSurfaceStore.swift", "Shared/ExtensionSurfaceView.swift", "Shared/ExtensionSystemWidget.swift"]:
                self.assertIn(path, paths)
            self.assertNotIn("Shared/APIClient.swift", paths)
            self.assertNotIn("Persona/Views/Settings/ExtensionUIRenderer.swift", paths)
            self.assertIn("WIDGET_EXTENSION", targets[target]["settings"]["base"]["SWIFT_ACTIVE_COMPILATION_CONDITIONS"])
        store = source("Shared/ExtensionSurfaceStore.swift")
        self.assertIn("completeFileProtectionUntilFirstUserAuthentication", store)
        self.assertIn("isExcludedFromBackup = true", store)
        self.assertIn("flock(descriptor, LOCK_EX)", store)
        self.assertNotIn("FleetLookStore", store)
        watch = source("PersonaWatch/WatchExtensionSurfaces.swift")
        self.assertIn("ExtensionSurfaceSync.credentials(configs", watch)
        self.assertNotIn("HubStore", watch)
        self.assertNotIn("registry", watch.replace("the phone's registry", ""))
        self.assertIn("answerOnly: true", watch)
        self.assertNotIn("/api/v1/extensions/-/ui", watch)
        self.assertNotIn("api.request(\"POST\"", watch)
        self.assertIn(".projection(link)", watch)
        self.assertNotIn(".deviceTokens()", source("Shared/ExtensionSurfaceSync.swift"))
        self.assertIn("credential.isAnswerOnly", source("Shared/ExtensionSurfaceClient.swift"))
        self.assertIn("ExtensionSurfaceCapabilitiesUnknown()", source("Shared/ExtensionSurfaceClient.swift"))
        intent = source("Shared/RunExtensionActionIntent.swift")
        self.assertIn("intent?.action.id", intent)
        self.assertIn("compiler(>=6.4)", intent)
        self.assertNotIn("intent?.action?.id", intent)
        self.assertNotIn("try IntentSupport.client", intent)

    def test_json_and_string_catalogs_have_unique_keys(self):
        for path in sorted(APPLE.rglob("*")):
            if path.suffix in {".json", ".xcstrings"}:
                with self.subTest(path=path.relative_to(APPLE)):
                    json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_pairs)
        catalog = json.loads(source("Shared/Localization/Localizable.xcstrings"))
        localized = catalog["strings"]["Persona"]["localizations"]
        for language in ["en", "zh-Hans", "ja"]:
            self.assertEqual(localized[language]["stringUnit"]["value"], "Persona")

    def test_brand_and_avatar_symbols_do_not_collide(self):
        icons = source("Shared/Design/Icons.swift")
        for mac in [False, True]:
            # The device icon has one declaration per platform, not two in the same compilation.
            platform = re.sub(r"#if os\(macOS\)\s*(.*?)#else\s*(.*?)#endif",
                              lambda match: match.group(1 if mac else 2), icons, flags=re.DOTALL)
            names = re.findall(r"\bstatic let (\w+)\s*=", platform)
            self.assertEqual(len(names), len(set(names)), [name for name, n in collections.Counter(names).items() if n > 1])
        self.assertIn('static let avatar = "person.crop.artframe"', icons)
        self.assertIn('static let persona = "sparkle"', icons)
        self.assertIn("symbol: Theme.Icon.avatar", source("Persona/Views/Settings/AppearanceSettingsView.swift"))
        launch = source("PersonaUITests/UITestStyle.swift")
        self.assertEqual(len(re.findall(r"\bfunc launchPersona\(\)", launch)), 1)
        self.assertNotRegex(launch, r"func launchPersona\(\)\s*\{\s*launchPersona\(\)")
        wire = source("Shared/Generated/ExtensionWire.swift")
        look = re.search(r"enum ExtensionBuiltinLook\s*\{([^}]+)\}", wire).group(1)
        names = re.findall(r"static let (\w+)\s*=", look)
        self.assertEqual(len(names), len(set(names)))
        self.assertIn('static let all = ["persona"]', look)
        self.assertNotIn('personaVisor', look)
        self.assertNotIn('personaCircuit', look)

    def test_only_mochi_is_drawn_and_milk_frog_is_a_package(self):
        model = source("Persona/Persona/Persona.swift")
        self.assertIn('static let builtins = [builtin]', model)
        self.assertIn('static let builtinID = PersonaSelection.builtinID', model)
        self.assertRegex(model, r'static let builtin = Persona\(id: builtinID,[\s\S]*?design: "mochi"')
        policy = source("Shared/PersonaSelection.swift")
        self.assertIn('static let builtinID = "persona"', policy)
        self.assertIn('static let packageID = "nwverse"', policy)
        design = re.search(r'enum Design[^\{]*\{([^}]+)\}', source("Shared/Design/PersonaFace.swift")).group(1)
        self.assertEqual(re.findall(r'\bcase\s+(\w+)', design), ["mochi"])
        for name in ["Visor", "Circuit", "Pixel", "Facet", "Blob"]:
            self.assertFalse((APPLE / f"Persona/Resources/Assets.xcassets/AppIcon-{name}.appiconset").exists())
            self.assertFalse((APPLE / f"Persona/Resources/Assets.xcassets/AppIcons/app-icon-{name.lower()}.imageset").exists())
        self.assertNotIn("FACES", (APPLE.parent / "scripts/assets/make_alt_icons.py").read_text())
        self.assertFalse((APPLE.parent / "scripts/assets/persona_face").exists())

    def test_context_parsers_share_the_exact_dual_contract(self):
        naming = source("Shared/HubNaming.swift")
        self.assertIn('NSRegularExpression.escapedPattern(for: LegacyNames.product)', naming)
        self.assertIn('static var contextPattern: String { productPattern + "-context" }', naming)
        self.assertIn('static var filePattern: String { productPattern + "-file" }', naming)
        for path in ["Shared/ContextText.swift", "Shared/Injected.swift", "Shared/WatchText.swift",
                     "Persona/Voice/VoiceWire.swift", "Persona/Views/Link/LinkSheet.swift"]:
            with self.subTest(path=path):
                text = source(path)
                self.assertIn("HubNaming.contextPattern", text)
                self.assertNotIn("persona|", text)
                self.assertNotIn("|persona", text)
        closing_source = source("Shared/ContextText.swift")
        pieces = re.search(r'pattern: #"([^"\n]+)"# \+ HubNaming.contextPattern \+ #"([^"\n]+)"#', closing_source)
        self.assertIsNotNone(pieces, "the sanitizer must use the shared context pattern")
        previous = re.search(r'static let product = "([^"\n]+)"', source("Shared/NativeStateMigration.swift")).group(1)
        closing = pieces.group(1) + "(?:persona|" + re.escape(previous) + ")-context" + pieces.group(2)
        regex = re.compile(closing, re.IGNORECASE)
        for product in ["persona", previous]:
            for tag in [f"</{product}-context>", f"</{product.upper()}-CONTEXT>", f"< / {product}-context\t>"]:
                self.assertEqual(regex.sub("", f"a{tag}b"), "ab")
        self.assertIsNone(regex.search("</other-context>"))
        self.assertIn('let removed = replace(text, with: "")', closing_source)
        self.assertIn('return replace(removed, with: "&lt;$1")', closing_source)
        for path in ["Shared/WatchText.swift", "Persona/Views/Chat/VideoAttach.swift"]:
            self.assertIn("HubNaming.filePattern", source(path))

    def test_link_paths_and_helpers_share_compatible_names_without_duplicates(self):
        paths = source("PersonaLink/Sources/PersonaLink/LinkStatePaths.swift")
        self.assertIn('environment["PERSONA_HOME"] ?? environment[previousHomeKey] ?? ""', paths)
        self.assertIn('LegacyNames.product.uppercased() + "_HOME"', paths)
        self.assertIn('home.appendingPathComponent(".persona", isDirectory: true)', paths)
        self.assertIn('home.appendingPathComponent("." + LegacyNames.product, isDirectory: true)', paths)
        self.assertIn("lstat(previous.path, &old)", paths)
        self.assertIn("a.st_dev == b.st_dev, a.st_ino == b.st_ino", paths)
        self.assertNotIn("moveItem(", paths)
        self.assertNotIn("removeItem(", paths)
        helper = source("PersonaLink/Sources/PersonaLink/LinkModel.swift")
        self.assertEqual(helper.count('environment["PERSONA_HOME"] = stateURL.path'), 1)
        self.assertIn('LegacyNames.product.uppercased() + "_HOME"', helper)
        route = source("Shared/HubPageLink.swift")
        self.assertIn('case "persona", LegacyNames.conversationPath:', route)
        for path in ["Shared/APIClient.swift", "Shared/ExtensionSystemLink.swift", "Mac/App/MacOpenURL.swift"]:
            self.assertIn("HubNaming.accepts(scheme:", source(path))
        self.assertIn('["persona", LegacyNames.urlScheme].contains(url.scheme?.lowercased() ?? "")',
                      source("LiveActivity/PersonaActivityAttributes.swift"))
        self.assertIn('static let schemes: Set<String> = ["persona", LegacyNames.product]',
                      source("PersonaLink/Sources/PersonaLink/PairLink.swift"))
        self.assertIn('case "persona", LegacyNames.urlScheme:', source("Persona/Views/Chat/AssistantHTMLCard.swift"))
        compatibility = APPLE / "PersonaLink/Sources/PersonaLink/NativeCompatibility.swift"
        self.assertTrue(compatibility.is_symlink())
        self.assertEqual(compatibility.resolve(), (APPLE / "Shared/NativeStateMigration.swift").resolve())
        for path in APPLE.rglob("*.swift"):
            with self.subTest(path=path.relative_to(APPLE)):
                text = path.read_text(encoding="utf-8")
                self.assertNotRegex(text, r'"persona"\s*,\s*"persona"')
                self.assertNotIn("persona|persona", text)
                self.assertNotRegex(text, r'replacingOccurrences\(of: "([^"]+)", with: "\1"\)')
                self.assertNotRegex(text, r'\["PERSONA_HOME"\][^\n]*(?:\|\||\?\?)[^\n]*\["PERSONA_HOME"\]')



if __name__ == "__main__":
    unittest.main()
