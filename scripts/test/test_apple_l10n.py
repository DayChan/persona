"""English-source Apple catalogs are complete in en / zh-Hans / ja, with compatible arguments and source keys.

Runs `scripts/i18n/apple_l10n.py check` on the repository, and checks catalog validation, the Swift literal
lexer and explicit localized-key matching. See docs/apple/ios.md "UI languages".
"""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "i18n"))

import apple_l10n  # noqa: E402
import swift_literals  # noqa: E402


class CatalogsTest(unittest.TestCase):
    def test_repository_is_complete(self):
        r = subprocess.run([sys.executable, str(ROOT / "scripts/i18n/apple_l10n.py"), "check"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout[-4000:] + r.stderr[-2000:])


def unit(text, state="translated"):
    return {"stringUnit": {"state": state, "value": text}}


def catalog(key="Open", en="Open", zh="打开", ja="開く"):
    return {"sourceLanguage": "en", "strings": {key: {"localizations": {
        "en": unit(en), "zh-Hans": unit(zh), "ja": unit(ja)}}}}


class CatalogValidationTest(unittest.TestCase):
    def test_complete_entry(self):
        self.assertEqual(apple_l10n.catalog_problems(catalog()), [])

    def test_nested_string_entry_cannot_hide_as_an_extra_locale(self):
        data = catalog()
        locales = data["strings"]["Open"]["localizations"]
        locales["Desktop label"] = {"localizations": copy.deepcopy(locales), "extractionState": "manual"}
        self.assertTrue(any("nested string entry" in p for p in apple_l10n.catalog_problems(data)))

    def test_english_source_required(self):
        data = catalog()
        data["sourceLanguage"] = "zh-Hans"
        self.assertIn("catalog: sourceLanguage must be en", apple_l10n.catalog_problems(data))

    def test_cjk_keys_rejected_even_if_not_translated(self):
        for key in ("打开", "開く", "ひらく"):
            data = catalog(key)
            data["strings"][key]["shouldTranslate"] = False
            self.assertTrue(any("catalog key" in p for p in apple_l10n.catalog_problems(data)))

    def test_symbols_numbers_and_nontranslatable_entries_need_all_languages(self):
        for key in ("·", "42", "Persona", ""):
            data = catalog(key, key, key, key)
            data["strings"][key]["shouldTranslate"] = False
            self.assertEqual(apple_l10n.catalog_problems(data), [])
            del data["strings"][key]["localizations"]["zh-Hans"]
            self.assertTrue(any("zh-Hans is missing" in p for p in apple_l10n.catalog_problems(data)))

    def test_missing_english_cannot_fall_back_to_key(self):
        data = catalog()
        del data["strings"]["Open"]["localizations"]["en"]
        self.assertTrue(any("en is missing" in p for p in apple_l10n.catalog_problems(data)))

    def test_every_language_needs_a_translated_nonempty_unit(self):
        for lang in apple_l10n.LANGS:
            for state in ("needs_review", "new", "stale", None):
                data = catalog()
                data["strings"]["Open"]["localizations"][lang] = unit("Open", state)
                self.assertTrue(any(f"{lang} stringUnit is" in p for p in apple_l10n.catalog_problems(data)))
            data = catalog()
            data["strings"]["Open"]["localizations"][lang] = unit("")
            self.assertTrue(any(f"{lang} stringUnit has no value" in p for p in apple_l10n.catalog_problems(data)))

    def test_scoped_collisions_keep_distinct_translations(self):
        data = catalog("Open [file]", "Open", "打开", "開く")
        data["strings"].update(catalog("Open [connection]", "Open", "连接", "接続")["strings"])
        self.assertEqual(apple_l10n.catalog_problems(data), [])
        self.assertEqual(data["strings"]["Open [connection]"]["localizations"]["zh-Hans"], unit("连接"))

    def test_duplicate_json_keys_are_rejected_before_data_is_lost(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "Localizable.xcstrings"
            entry = json.dumps(catalog()["strings"]["Open"])
            path.write_text('{"sourceLanguage":"en","strings":{"Open":' + entry + ',"Open":' + entry + '}}')
            problems = []
            apple_l10n.check_catalog(str(path), problems)
            self.assertTrue(any("duplicate JSON key 'Open'" in p for p in problems))

    def test_placeholder_types_follow_argument_numbers_not_sorted_types(self):
        data = catalog("%lld files for %@", "%lld files for %@", "%2$@ 的 %1$lld 个文件", "%2$@の%1$lld件")
        self.assertEqual(apple_l10n.catalog_problems(data), [])
        data["strings"]["%lld files for %@"]["localizations"]["zh-Hans"] = unit("%1$@ 的 %2$lld 个文件")
        self.assertTrue(any("zh-Hans stringUnit placeholders" in p for p in apple_l10n.catalog_problems(data)))

    def test_key_and_visible_english_must_use_the_same_argument_types(self):
        data = catalog("%@ %lld [scope]", "%lld %@", "%lld %@", "%lld %@")
        self.assertTrue(any("≠ key" in p for p in apple_l10n.catalog_problems(data)))

    def test_width_flags_do_not_change_type_but_integer_width_does(self):
        data = catalog("Value %lld", "Value %lld", "值 %1$03lld", "値 %1$+lld")
        self.assertEqual(apple_l10n.catalog_problems(data), [])
        data["strings"]["Value %lld"]["localizations"]["ja"] = unit("値 %d")
        self.assertTrue(any("ja stringUnit placeholders" in p for p in apple_l10n.catalog_problems(data)))

    def test_conflicting_or_mixed_format_parameters_fail(self):
        data = catalog("%@ %lld", "%@ %lld", "%1$@ %1$lld", "%@ %2$lld")
        problems = apple_l10n.catalog_problems(data)
        self.assertTrue(any("conflicting types" in p for p in problems))
        self.assertTrue(any("mixed positional" in p for p in problems))

    def test_plural_one_can_omit_only_the_count(self):
        data = catalog("%lld files for %@", "%lld files for %@", "%2$@ 的 %1$lld 个文件", "%2$@の%1$lld件")
        loc = data["strings"]["%lld files for %@"]["localizations"]
        loc["en"] = {"variations": {"plural": {"one": unit("A file for %2$@"), "other": unit("%lld files for %@")}}}
        self.assertEqual(apple_l10n.catalog_problems(data), [])
        loc["en"]["variations"]["plural"]["one"] = unit("A file")
        self.assertTrue(any("en /plural/one placeholders" in p for p in apple_l10n.catalog_problems(data)))
        loc["en"]["variations"]["plural"]["one"] = unit("A file for %2$@")
        loc["en"]["variations"]["plural"]["other"] = unit("Files for %2$@")
        self.assertTrue(apple_l10n.catalog_problems(data))

    def test_nested_variations_check_every_state_and_type(self):
        data = catalog("Value %@", "Value %@", "值 %@", "値 %@")
        loc = data["strings"]["Value %@"]["localizations"]
        loc["ja"] = {"variations": {"device": {"iphone": unit("値 %@"), "other": {
            "variations": {"plural": {"one": unit("値 %lld", "needs_review"), "other": unit("値 %@")}}}}}}
        problems = apple_l10n.catalog_problems(data)
        self.assertTrue(any("needs_review" in p for p in problems))
        self.assertTrue(any("/device/other/plural/one placeholders" in p for p in problems))

    def test_plural_other_and_empty_variation_branches_are_required(self):
        data = catalog()
        loc = data["strings"]["Open"]["localizations"]
        loc["en"] = {"variations": {"plural": {"one": unit("Open")}}}
        loc["ja"] = {"variations": {"device": {"other": {}}}}
        problems = apple_l10n.catalog_problems(data)
        self.assertTrue(any("needs other" in p for p in problems))
        self.assertTrue(any("no stringUnit or variations" in p for p in problems))

    def substitution_catalog(self):
        data = catalog("Uploading %@ and %lld files", "Uploading %@ and %lld files", "上传 %1$@ 等 %2$lld 个文件", "%1$@など%2$lld件")
        loc = data["strings"]["Uploading %@ and %lld files"]["localizations"]
        loc["en"] = {**unit("Uploading %1$@ and %#@count@"), "substitutions": {"count": {
            "argNum": 2, "formatSpecifier": "lld", "variations": {"plural": {
                "one": unit("a file"), "other": unit("%arg files")}}}}}
        return data, loc

    def test_substitutions_expand_the_en_argument_schema(self):
        data, _ = self.substitution_catalog()
        self.assertEqual(apple_l10n.catalog_problems(data), [])

    def test_substitution_presence_does_not_skip_plain_unit_checks(self):
        data, loc = self.substitution_catalog()
        loc["en"]["stringUnit"]["value"] = "Uploading %1$lld and %#@count@"
        self.assertTrue(any("≠ key" in p for p in apple_l10n.catalog_problems(data)))

    def test_substitution_units_and_states_are_checked(self):
        data, loc = self.substitution_catalog()
        forms = loc["en"]["substitutions"]["count"]["variations"]["plural"]
        forms["other"] = unit("%1$@ files", "needs_review")
        problems = apple_l10n.catalog_problems(data)
        self.assertTrue(any("needs_review" in p for p in problems))
        self.assertTrue(any("/substitutions/count/plural/other placeholders" in p for p in problems))

    def test_translated_substitution_argnum_and_type_are_checked(self):
        data, loc = self.substitution_catalog()
        loc["ja"] = copy.deepcopy(loc["en"])
        loc["ja"]["substitutions"]["count"]["argNum"] = 1
        problems = apple_l10n.catalog_problems(data)
        self.assertTrue(any("substitution argument 1:lld ≠ en" in p for p in problems))
        data, loc = self.substitution_catalog()
        loc["ja"] = copy.deepcopy(loc["en"])
        loc["ja"]["substitutions"]["count"]["formatSpecifier"] = "@"
        self.assertTrue(any("substitution argument 2:@ ≠ en" in p for p in apple_l10n.catalog_problems(data)))

    def test_unknown_or_invalid_substitutions_fail(self):
        for change in ("unknown", "argnum", "type"):
            data, loc = self.substitution_catalog()
            if change == "unknown":
                loc["en"]["stringUnit"]["value"] = "Uploading %1$@ and %#@missing@"
            elif change == "argnum":
                loc["en"]["substitutions"]["count"]["argNum"] = 0
            else:
                loc["en"]["substitutions"]["count"]["formatSpecifier"] = "banana"
            self.assertTrue(apple_l10n.catalog_problems(data))

    def test_argument_numbers_cannot_have_gaps_or_malformed_specifiers(self):
        data = catalog("%1$@ %3$lld", "%1$@ %3$lld", "%1$@ %3$lld", "%1$@ %3$lld")
        self.assertTrue(any("contiguous" in p for p in apple_l10n.catalog_problems(data)))
        self.assertTrue(apple_l10n.format_specs("%1$Q")[1])
        self.assertTrue(apple_l10n.format_specs("%*.*f")[1])

    def test_escaped_percent_is_not_a_format_argument(self):
        self.assertEqual(apple_l10n.format_specs("%%d %lld %%"), ({1: "lld"}, []))


class SourceKeysTest(unittest.TestCase):
    def check_source(self, src, keys=("Open",), **tables):
        return apple_l10n.source_problems(src, {"Localizable": keys, **tables})

    def test_unknown_english_explicit_localized_keys_fail(self):
        for expression in ('String(localized: "Missing")', 'NSLocalizedString("Missing", comment: "Note")',
                           'LocalizedStringKey("Missing")', 'LocalizedStringResource("Missing")',
                           'LocalizedStringResource.init("Missing")', 'interfaceText("Missing")'):
            self.assertTrue(any("localized key not in Localizable" in p for p in self.check_source(expression)), expression)

    def test_known_keys_interpolations_and_nested_calls(self):
        src = 'String(localized: "Files for \\(name): \\(count)", comment: "Note")'
        self.assertEqual(self.check_source(src, ("Files for %@: %lld",)), [])
        src = 'String(localized: "\\(String(localized: "Open")) files")'
        self.assertEqual(self.check_source(src, ("Open", "%@ files")), [])
        src = 'String(localized: "\\(String(localized: "Missing")) files")'
        self.assertTrue(any("Missing" in p for p in self.check_source(src, ("%@ files",))))

    def test_scope_is_part_of_the_key_not_a_visible_english_alias(self):
        self.assertEqual(self.check_source('String(localized: "Open [connection]")', ("Open [connection]",)), [])
        self.assertTrue(self.check_source('String(localized: "Open")', ("Open [connection]",)))

    def test_table_name_is_not_a_union_of_catalogs(self):
        src = 'String(localized: "Shortcut", table: "AppShortcuts")'
        self.assertEqual(self.check_source(src, AppShortcuts=("Shortcut",)), [])
        self.assertTrue(self.check_source(src, keys=("Shortcut",), AppShortcuts=()))
        src = 'NSLocalizedString("Open", tableName: "AppShortcuts", bundle: .main, comment: "Note")'
        self.assertTrue(self.check_source(src, AppShortcuts=()))
        self.assertEqual(self.check_source('String(localized: "Open", table: nil)'), [])

    def test_comments_multiline_calls_and_direct_literals_only(self):
        src = 'String(\n /* note */ localized: /* key */ "Open",\n comment: "Missing")\n// String(localized: "Missing")'
        self.assertEqual(self.check_source(src), [])
        src = 'String(localized: someKey)\ninterfaceText(resource)\nString(localized: "prefix" + suffix)'
        self.assertEqual(self.check_source(src), [])

    def test_nonlocalizing_overloads_are_not_guessed(self):
        src = '\n'.join(('Text(verbatim: "Missing")', 'Text("Missing")', 'Label("Missing", systemImage: "star")',
                         'Button("Missing") {}', 'String("Missing")', 'print("String(localized: \\"Missing\\")")'))
        self.assertEqual(self.check_source(src), [])

    def bui(self, src, keys=("Open",)):
        return apple_l10n.beautifului_problems(src, set(keys))

    def test_beautifului_key_calls_need_app_catalog_entries(self):
        # BeautifulUI's Text/Button/… literals are looked up in the app's main bundle (#5574).
        for call in ('Text("Missing")', 'Button("Missing") {}', 'Label("Missing", systemImage: "x")',
                     '.accessibilityLabel("Missing")', '.accessibilityValue(open ? "Open" : "Missing")',
                     '.accessibilityLabel(["Open", "Missing"][i])', 'Text(done ? "\\(n) Missing" : "—")'):
            self.assertTrue(any("Missing" in p for p in self.bui(call)), call)
        self.assertEqual(self.bui('Text("Open")\n.help(open ? "Open" : "Open")'), [])
        self.assertEqual(self.bui('Text("Files: \\(n)")', ("Files: %lld",)), [])

    def test_beautifului_non_keys_are_not_guessed(self):
        # Not a LocalizedStringKey: verbatim, another bundle, a String expression, or no letters to translate.
        src = "\n".join(('Text(verbatim: "Missing")', 'Text("Missing", bundle: .module)', 'Text(name ?? "Missing")',
                         'Text(open ? title : "Missing")', 'Text(format("Missing"))', 'Text("—")', 'Text("\\(a) / \\(b)")'))
        self.assertEqual(self.bui(src), [])
    def test_personakit_is_held_to_the_app_rules(self):
        # PersonaKit looks its text up in the app's catalog and is migrated (#5575, #5758): check flags its Chinese
        # literals and its unknown keys, as in the app targets.
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp, "apple/Packages/PersonaKit/Sources/PersonaKit/Note.swift")
            source.parent.mkdir(parents=True)
            source.write_text('let note = "中文"\nlet hint = String(localized: "Missing")\n', encoding="utf-8")
            with patch.object(apple_l10n, "ROOT", tmp), patch.object(apple_l10n, "APPLE", str(Path(tmp, "apple"))):
                problems = [p for p in apple_l10n.check() if "PersonaKit" in p]
        self.assertEqual(len(problems), 2, problems)
        self.assertIn("Chinese/Japanese source literal", problems[0])
        self.assertIn("localized key not in Localizable", problems[1])

    def test_chinese_catalog_membership_never_excuses_a_source_literal(self):
        problems = self.check_source('Text("打开")', ("打开",))
        self.assertEqual(len(problems), 1)
        self.assertIn("source literal", problems[0])

    def test_ignore_markers_and_translator_comments_only_skip_nonui_literals(self):
        src = 'let prompt = "中文" // i18n-ignore\n// i18n-ignore\nlet text = "日文"\nString(localized: "Open", comment: "打开按钮")'
        self.assertEqual(self.check_source(src), [])
        self.assertEqual(self.check_source('String(localized: "Open", comment:\n "打开按钮")'), [])
        self.assertEqual(self.check_source('// i18n-ignore-file: protocol values\nlet x = "中文"'), [])
        self.assertTrue(self.check_source('String(localized: "Missing") // i18n-ignore'))

    def test_preview_and_debug_pages_remain_out_of_scope(self):
        src = '#if DEBUG\nText("中文")\nString(localized: "Missing")\n#endif\n#Preview { Text("中文") }'
        self.assertEqual(self.check_source(src), [])

    def test_explicit_resource_and_key_bindings_require_known_keys(self):
        for typ in ("LocalizedStringResource", "LocalizedStringKey"):
            self.assertEqual(self.check_source(f'let title: {typ} = "Open"'), [])
            self.assertTrue(self.check_source(f'static let title: {typ} = "Missing"'))
        self.assertEqual(self.check_source('let title: String = "Missing"'), [])

    def test_literal_percent_keys_are_exact_not_format_aliases(self):
        self.assertTrue(self.check_source('String(localized: "Value %d")', ("Value %lld",)))
        self.assertEqual(self.check_source('String(localized: "Value %lld")', ("Value %lld",)), [])

    def test_interpolated_percent_text_matches_catalog_escaped_percent(self):
        examples = ((r'String(localized: "\(left)% left")', "%lld%% left"),
                    (r'String(localized: "\(Int((r * 100).rounded()))% used")', "%lld%% used"))
        for src, key in examples:
            self.assertEqual(self.check_source(src, (key,)), [])
        self.assertTrue(self.check_source(r'String(localized: "\(left)% left")', ("%lld%% used",)))

    def test_raw_literal_interpolation_and_unicode_escapes(self):
        self.assertEqual(self.check_source('String(localized: #"Open"#)'), [])
        self.assertEqual(self.check_source(r'String(localized: #"Files \#(count)"#)', ("Files %lld",)), [])
        self.assertEqual(self.check_source(r'String(localized: "\u{4F}pen")'), [])


class WorkflowTest(unittest.TestCase):
    def test_merge_preserves_scoped_keys_and_prefers_complete_languages(self):
        with tempfile.TemporaryDirectory() as tmp:
            a, b, out = [Path(tmp) / name for name in ("a", "b", "out")]
            ours = catalog("Open [file]", "Open", "打开", "開く")
            theirs = catalog("Open [connection]", "Open", "连接", "接続")
            incomplete = catalog()["strings"]["Open"]
            del incomplete["localizations"]["zh-Hans"]
            theirs["strings"]["Open"] = incomplete
            ours["strings"].update(catalog()["strings"])
            a.write_text(json.dumps(ours))
            b.write_text(json.dumps(theirs))
            apple_l10n.merge(str(a), str(b), str(out))
            result = json.loads(out.read_text())
            self.assertEqual(set(result["strings"]), {"Open", "Open [file]", "Open [connection]"})
            self.assertEqual(apple_l10n.catalog_problems(result), [])

    def test_sync_uses_existing_targets_and_catalogs(self):
        with patch.object(apple_l10n, "stringsdata", return_value=iter(["a.stringsdata"])), \
                patch.object(apple_l10n.subprocess, "run") as run:
            apple_l10n.sync("derived")
        args = run.call_args.args[0]
        self.assertEqual(args[:3], ["xcrun", "xcstringstool", "sync"])
        self.assertIn("--skip-marking-strings-stale", args)
        self.assertEqual(args[-2:], ["--stringsdata", "a.stringsdata"])

    def test_strict_uses_compiler_extraction(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, extracted = Path(tmp) / "Source.swift", Path(tmp) / "Source.stringsdata"
            source.write_text('Text("中文")\nlet x = "消息" // i18n-ignore\n')
            extracted.write_text(json.dumps({"source": str(source), "tables": {"Localizable": [
                {"location": {"startingLine": 1}}]}}))
            with patch.object(apple_l10n, "source_files", return_value=[str(source)]), \
                    patch.object(apple_l10n, "stringsdata", return_value=[str(extracted)]):
                self.assertEqual(apple_l10n.strict(tmp), [])
            with patch.object(apple_l10n, "source_files", return_value=[str(source)]), \
                    patch.object(apple_l10n, "stringsdata", return_value=[]):
                self.assertTrue(apple_l10n.strict(tmp))


    def test_strict_flags_specifiers_that_differ_from_the_catalog(self):
        # The compiler's key follows the interpolated types: a String asks for "%@" where the catalog has "%lld" (#5544).
        with tempfile.TemporaryDirectory() as tmp:
            extracted = Path(tmp) / "Source.stringsdata"
            extracted.write_text(json.dumps({"source": str(ROOT / "apple/Persona/Source.swift"), "tables": {"Localizable": [
                {"key": "Exit code %@", "location": {"startingLine": 7}},
                {"key": "Files: %lld", "location": {"startingLine": 8}},
                {"key": "Not in the catalog %@", "location": {"startingLine": 9}}]}}))
            keys = {"Exit code %lld", "Files: %lld", "100%% done"}
            with patch.object(apple_l10n, "stringsdata", return_value=[str(extracted)]):
                problems = apple_l10n.specifier_mismatches(tmp, keys)
            self.assertEqual(len(problems), 1, problems)
            self.assertIn("apple/Persona/Source.swift:7: looks up 'Exit code %@', but the catalog has 'Exit code %lld'", problems[0])


class InfoPlistDriftTest(unittest.TestCase):
    """An InfoPlist catalog's en value is its Info.plist value (#4790)."""

    @staticmethod
    def catalog(en):
        return {"strings": {"NSMicrophoneUsageDescription": {"localizations": {
            "en": {"stringUnit": {"state": "translated", "value": en}},
            "zh-Hans": {"stringUnit": {"state": "translated", "value": "录音"}}}}}}

    def test_matching_is_fine(self):
        plist = {"NSMicrophoneUsageDescription": "Records audio.", "CFBundleName": "Persona"}
        self.assertEqual(apple_l10n.infoplist_drift("c", self.catalog("Records audio."), plist), [])

    def test_a_changed_info_plist_is_caught(self):
        problems = apple_l10n.infoplist_drift("c", self.catalog("Old words."), {"NSMicrophoneUsageDescription": "New words."})
        self.assertEqual(len(problems), 1)
        self.assertIn("NSMicrophoneUsageDescription: en ≠ Info.plist", problems[0])

    def test_a_usage_description_missing_from_the_catalog_is_caught(self):
        problems = apple_l10n.infoplist_drift("c", {"strings": {}}, {"NSCameraUsageDescription": "Scans QR codes."})
        self.assertEqual(problems, ["c: NSCameraUsageDescription: in Info.plist but not in the catalog"])


class LiteralsTest(unittest.TestCase):
    def test_interpolations_with_strings_inside(self):
        src = 'let a = "共 \\(n) 个，\\(names.joined(separator: "、"))" // 注释里的 "中文" 不算\n/* "块注释" */ let b = #"原样 \\(x)"#'
        texts = [l.text for l in swift_literals.literals(src)]
        self.assertIn('共 \\(n) 个，\\(names.joined(separator: "、"))', texts)
        self.assertIn("、", texts)  # the one inside the interpolation, found on its own
        self.assertIn("原样 \\(x)", texts)
        self.assertNotIn("中文", texts)
        self.assertNotIn("块注释", texts)

    def test_only_the_literals_own_text_counts(self):
        lit = swift_literals.literals('let a = "\\(name ?? "未知")"')[0]
        self.assertFalse(lit.has_cjk)


class NormalizeTest(unittest.TestCase):
    def test_a_literal_matches_its_catalog_key(self):
        n = apple_l10n.normalize
        self.assertEqual(n('共 \\(items.count) 个', literal=True), n("共 %lld 个"))
        self.assertEqual(n('\\(a)：\\(b(c(d)))', literal=True), n("%1$@：%2$@"))
        self.assertEqual(n('正在下载 \\(p)%', literal=True), n("正在下载 %lld%%"))
        self.assertEqual(n('新建待办 \\(\\.$todoTitle)', literal=True), n("新建待办 ${todoTitle}"))

    def test_literal_percent_is_not_a_printf_argument(self):
        n = apple_l10n.normalize
        self.assertEqual(n(r"\(left)% left", literal=True), n("%lld%% left"))
        self.assertEqual(n(r"\(Int((r * 100).rounded()))% used", literal=True), n("%lld%% used"))
        self.assertEqual(n("% left", literal=True), "% left")
        self.assertEqual(n("%% left", literal=True), "%% left")
        self.assertEqual(n("%lld%% left"), "\x00% left")

    def test_quoted_parentheses_inside_interpolation_do_not_end_it(self):
        self.assertEqual(apple_l10n.normalize('Files \\(names.joined(separator: ")"))', literal=True),
                         apple_l10n.normalize("Files %@"))

    def test_escaped_interpolation_is_literal_text(self):
        self.assertEqual(apple_l10n.normalize(r"Files \\(count)", literal=True), r"Files \(count)")
        self.assertEqual(apple_l10n.normalize("%%d %lld"), "%d \x00")

    def test_multi_line_literals_lose_their_indentation(self):
        self.assertEqual(apple_l10n.normalize("\n    第一行\n    第二行\n    ", literal=True), "第一行\n第二行")

    def test_ignore_markers(self):
        src = 'a("中") // i18n-ignore\n// i18n-ignore\nb("文")\nc("字")\n'
        self.assertEqual(apple_l10n.ignored_lines(src), {1, 3})
        self.assertIsNone(apple_l10n.ignored_lines("// i18n-ignore-file: demo data\nlet x = \"中\""))

    def test_placeholders(self):
        self.assertEqual(apple_l10n.specs("%1$@ 有 %2$lld 个 %%"), {1: "@", 2: "lld"})


if __name__ == "__main__":
    unittest.main()
