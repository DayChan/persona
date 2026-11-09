"""schema/extension (#1849): the generated wire types match the schemas, and the examples hold to them.

    python3 scripts/test/test_extension_schema.py

Each schema/extension/examples/<Definition>.<name>.json must validate against that definition, each file under
examples/invalid/ must not. The validator below knows the subset the generator does (README "Subset") and nothing more.
"""

import json
import math
import re
import subprocess
import sys
import unittest
from decimal import Decimal
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCHEMA_DIR = ROOT / "schema" / "extension"
GENERATOR = ROOT / "scripts" / "gen" / "extension_schema.py"
# The newest UI version the showcase snapshots (examples/showcase) cover. The UI version 6 pages (image, controls,
# chart-v6, spark, row-actions, grid, carousel, inputs-v6, styles, home-demo, markets-demo, card-v6) were written here
# with the Android renderer (#5475); the extension's own snapshots (persona-extensions, #5477) replace them when copied.
SHOWCASE_UI = 6


def load_schemas():
    files = {}
    for f in sorted(SCHEMA_DIR.glob("*.schema.json")):
        files[f.name] = json.loads(f.read_text())
    return files


class Validator:
    def __init__(self, files):
        self.files = files

    def resolve(self, ref, here):
        name, _, pointer = ref.partition("#")
        doc = self.files[name or here]
        node = doc
        for part in pointer.strip("/").split("/"):
            if part:
                node = node[part]
        return node, (name or here)

    def errors(self, schema, value, here, at="$"):
        if "$ref" in schema:
            target, f = self.resolve(schema["$ref"], here)
            errs = self.errors(target, value, f, at)
            rest = {k: v for k, v in schema.items() if k not in ("$ref", "description")}
            return errs + (self.errors(rest, value, here, at) if rest else [])
        errs = []
        for option in schema.get("allOf", []):
            errs += self.errors(option, value, here, at)
        if "anyOf" in schema and not any(not self.errors(o, value, here, at) for o in schema["anyOf"]):
            errs.append(f"{at}: no matching anyOf branch")
        if "not" in schema and not self.errors(schema["not"], value, here, at):
            errs.append(f"{at}: matches forbidden schema")
        if "oneOf" in schema:
            disc = schema.get("x-discriminator")
            if disc:
                tag = value.get(disc) if isinstance(value, dict) else None
                for option in schema["oneOf"]:
                    target, f = self.resolve(option["$ref"], here)
                    if target["properties"][disc].get("const") == tag:
                        errs += self.errors(option, value, here, at)
                        break
                else:
                    return [f"{at}: no variant with {disc} {tag!r}"]
            else:
                passing = [o for o in schema["oneOf"] if not self.errors(o, value, here, at)]
                if len(passing) != 1:
                    errs.append(f"{at}: matches {len(passing)} of oneOf, not one")
            # Every oneOf form retains sibling constraints and selected branch overlays.
        t = schema.get("type")
        types = {"string": str, "boolean": bool, "object": dict, "array": list, "null": type(None)}
        if t in ("integer", "number"):
            finite = value.is_finite() if isinstance(value, Decimal) else (
                isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value))
            ok = finite and (t == "number" or value == (value.to_integral_value() if isinstance(value, Decimal) else math.trunc(value)))
        elif t:
            ok = isinstance(value, types[t])
        else:
            ok = True
        if not ok:
            return [f"{at}: not a {t}"]
        if "const" in schema and value != schema["const"]:
            errs.append(f"{at}: not {schema['const']!r}")
        if "enum" in schema and value not in schema["enum"]:
            errs.append(f"{at}: {value!r} not one of {schema['enum']}")
        if isinstance(value, str):
            if len(value) < schema.get("minLength", 0):
                errs.append(f"{at}: shorter than {schema['minLength']}")
            if "maxLength" in schema and len(value) > schema["maxLength"]:
                errs.append(f"{at}: longer than {schema['maxLength']}")
            if "pattern" in schema and not re.search(schema["pattern"], value):
                errs.append(f"{at}: {value!r} does not match {schema['pattern']}")
        if isinstance(value, (int, float, Decimal)) and not isinstance(value, bool):
            if "minimum" in schema and value < schema["minimum"]:
                errs.append(f"{at}: below {schema['minimum']}")
            if "maximum" in schema and value > schema["maximum"]:
                errs.append(f"{at}: above {schema['maximum']}")
        if isinstance(value, list):
            if len(value) < schema.get("minItems", 0):
                errs.append(f"{at}: fewer than {schema['minItems']} items")
            if "maxItems" in schema and len(value) > schema["maxItems"]:
                errs.append(f"{at}: more than {schema['maxItems']} items")
            if schema.get("uniqueItems") is True:
                seen = set()
                for i, v in enumerate(value):
                    key = json.dumps(v, sort_keys=True)
                    if key in seen:
                        errs.append(f"{at}[{i}]: {key} twice")
                    seen.add(key)
            for i, v in enumerate(value):
                errs += self.errors(schema.get("items", {}), v, here, f"{at}[{i}]")
        if isinstance(value, dict):
            props = schema.get("properties", {})
            for k in schema.get("required", []):
                if k not in value:
                    errs.append(f"{at}: no {k}")
            if len(value) < schema.get("minProperties", 0):
                errs.append(f"{at}: fewer than {schema['minProperties']} properties")
            if "maxProperties" in schema and len(value) > schema["maxProperties"]:
                errs.append(f"{at}: more than {schema['maxProperties']} properties")
            extra = schema.get("additionalProperties", True)
            for k, v in value.items():
                if "propertyNames" in schema:
                    errs += self.errors(schema["propertyNames"], k, here, f"{at}.<key>")
                if k in props:
                    errs += self.errors(props[k], v, here, f"{at}.{k}")
                elif extra is False:
                    errs.append(f"{at}: unknown field {k}")
                elif isinstance(extra, dict):
                    errs += self.errors(extra, v, here, f"{at}.{k}")
        return errs

    def validate(self, definition, value):
        for name, doc in self.files.items():
            if definition in doc.get("$defs", {}):
                return self.errors(doc["$defs"][definition], value, name)
        raise KeyError(definition)


class ExtensionSchemaTests(unittest.TestCase):
    def setUp(self):
        self.v = Validator(load_schemas())

    def test_generated_files_are_current(self):
        r = subprocess.run([sys.executable, str(GENERATOR), "--check"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_open_object_codegen_is_generic(self):
        r = subprocess.run([sys.executable, str(ROOT / "scripts/test/test_extension_codegen.py")],
                           capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_examples_hold_to_the_schema(self):
        # examples/plugin/ are the plugin-side schemas' (connector, kb): apart, as the apps' own tests read every
        # example at the top level with their types, which do not have these.
        examples = sorted((SCHEMA_DIR / "examples").glob("*.json")) + sorted((SCHEMA_DIR / "examples" / "plugin").glob("*.json"))
        self.assertGreater(len(examples), 10)
        for f in examples:
            with self.subTest(example=f.name):
                errs = self.v.validate(f.name.split(".")[0], json.loads(f.read_text(), parse_float=Decimal if f.name.startswith("UIStream") else float))
                self.assertEqual(errs, [])

    def test_invalid_examples_are_refused(self):
        examples = sorted((SCHEMA_DIR / "examples" / "invalid").glob("*.json"))
        examples += sorted((SCHEMA_DIR / "examples" / "plugin" / "invalid").glob("*.json"))
        self.assertGreater(len(examples), 5)
        for f in examples:
            with self.subTest(example=f.name):
                self.assertNotEqual(self.v.validate(f.name.split(".")[0], json.loads(f.read_text(), parse_float=Decimal if f.name.startswith("UIStream") else float)), [])

    def test_schema_keywords_are_known(self):
        # A keyword neither validator knows is silently ignored (maxProperties and uniqueItems were, #4012): every
        # keyword the schemas use is one both validate, or an annotation (description, title, format, x-*).
        validated = {"$ref", "oneOf", "allOf", "anyOf", "not", "type", "const", "enum", "minLength", "maxLength", "pattern", "minimum", "maximum",
                     "items", "minItems", "maxItems", "uniqueItems", "properties", "required", "additionalProperties",
                     "minProperties", "maxProperties", "propertyNames", "$defs"}
        annotations = {"$schema", "$id", "title", "description", "format"}
        unknown = set()

        def walk(node, where):
            if isinstance(node, list):
                for item in node:
                    walk(item, where)
            if not isinstance(node, dict):
                return
            for key, value in node.items():
                if key in ("properties", "$defs"):  # names, not keywords
                    for name, sub in value.items():
                        walk(sub, f"{where}/{name}")
                    continue
                if key.startswith("x-"):  # an annotation: what it holds is not keywords (x-methods' names)
                    continue
                if key not in validated and key not in annotations:
                    unknown.add(f"{where}: {key}")
                walk(value, where)

        for name, doc in load_schemas().items():
            walk(doc, name)
        self.assertEqual(sorted(unknown), [])

    def test_max_properties_and_unique_items(self):
        view = json.loads((SCHEMA_DIR / "examples/invalid/View.action-nine-values.json").read_text())
        self.assertIn("$.sections[0].items[0].action.values: more than 8 properties", self.v.validate("View", view))
        del view["sections"][0]["items"][0]["action"]["values"]["i"]
        self.assertEqual(self.v.validate("View", view), [])
        manifest = json.loads((SCHEMA_DIR / "examples/invalid/Manifest.off-by-default-twice.json").read_text())
        self.assertIn('$.off_by_default[1]: "voice/v" twice', self.v.validate("Manifest", manifest))
        manifest["off_by_default"] = ["voice/v"]
        self.assertEqual(self.v.validate("Manifest", manifest), [])

    def test_stream_raw_number_precision(self):
        for raw in ["9007199254740991.1", "-9007199254740991.1"]:
            self.assertNotEqual(self.v.validate("UIStreamScalar", json.loads(raw, parse_float=Decimal)), [])
        for raw in ["9007199254740990.5", "0.99999999999999999", "1e-20000"]:
            self.assertNotEqual(self.v.validate("UIStreamVersion", json.loads(raw, parse_float=Decimal)), [])
        for raw in ["1", "1e0", "9007199254740991", "9007199254740991.0"]:
            self.assertEqual(self.v.validate("UIStreamVersion", json.loads(raw, parse_float=Decimal)), [])
        for raw in ["0.5", "1e-20000", "-9007199254740991"]:
            self.assertEqual(self.v.validate("UIStreamScalar", json.loads(raw, parse_float=Decimal)), [])

    def test_stream_bounds_and_payload_validation(self):
        def check(definition, value, valid):
            self.assertEqual(not self.v.validate(definition, value), valid, (definition, value))

        base = {"protocol": 1, "session": "session-a", "epoch": "epoch-a", "version": 1,
                "base_version": 1, "operations": []}
        for version in [0, -1, 1.5, True, 9007199254740992]:
            check("UIStreamDelta", dict(base, version=version), False)
        check("UIStreamDelta", dict(base, version=9007199254740991), True)
        for size in [256, 257]:
            check("UIStreamDelta", dict(base, operations=[{"op": "remove", "key": "node"}] * size), size == 256)
        for key in ["", "x" * 128, "x" * 129]:
            check("UIStreamDelta", dict(base, session=key), len(key) == 128)
        for value in [None, False, 0, "", [], {}, [None] * 64, {"x" * 128: False}]:
            check("UIStreamValue", value, True)
        for value in [[None] * 65, {str(i): 0 for i in range(65)}, {"": 0}, {"x" * 129: 0},
                      [[0]], {"nested": {}}, "x" * 4097, float("nan"), float("inf")]:
            check("UIStreamValue", value, False)
        # oneOf payload presence must not bypass the field's typed payload or closed-object validation.
        for op in [{"op": "replace", "key": "node", "item": {"type": "text", "key": "node"}},
                   {"op": "replace", "key": "node", "item": {"type": "text", "key": "node", "text": "Demo", "html": "code"}},
                   {"op": "replace", "key": "node", "item": {"type": "divider"}},
                   {"op": "replace", "key": "node", "item": {"type": "divider", "key": ""}},
                   {"op": "replace", "key": "node", "item": {"type": "divider", "key": "node", "execute": "code"}},
                   {"op": "replace", "key": "node", "item": None},
                   {"op": "replace", "key": "node", "view": {"sections": []}, "extra": True}]:
            check("UIStreamOperation", op, False)

    def test_stream_semantic_checks_are_not_claimed_by_schema(self):
        # Existence, target kind/key equality, cycles, ordering and cross-owner authorization need the later reducer.
        check = self.v.validate
        self.assertEqual(check("UIStreamDelta", {"protocol": 1, "session": "session-a", "epoch": "epoch-a",
                         "base_version": 5, "version": 2, "operations": [{"op": "move", "key": "node", "parent": "node"}]}), [])
        self.assertEqual(check("UIContext", {"ui_version": 7}), [])  # not stream opt-in
        self.assertEqual(check("Section", {"items": []}), [])  # legacy keys remain optional

    def test_persona_card_cases_hold_to_the_schema(self):
        # examples/persona-card/<case>.json: a persona-card and the view every app turns it into (null: not drawn,
        # it stays a code block); sdk/go/wire/personacard.go is the reference (TestPersonaCardCases).
        cases = sorted((SCHEMA_DIR / "examples" / "persona-card").glob("*.json"))
        self.assertGreater(len(cases), 5)
        for f in cases:
            with self.subTest(case=f.name):
                c = json.loads(f.read_text(), parse_float=Decimal if f.name.startswith("UIStream") else float)
                self.assertEqual(self.v.validate("PersonaCard", c["card"]), [])
                if c["view"] is not None:
                    self.assertEqual(self.v.validate("View", c["view"]), [])

    def test_showcase_snapshots_hold_to_the_schema(self):
        # examples/showcase/<page>.json: every page of the component showcase extension (persona-extensions, #1852), which each
        # app's screenshot tests draw; copied from the extension's snapshots/.
        pages = sorted((SCHEMA_DIR / "examples" / "showcase").glob("*.json"))
        self.assertGreater(len(pages), 30)
        seen = set()
        for f in pages:
            with self.subTest(page=f.name):
                v = json.loads(f.read_text(), parse_float=Decimal if f.name.startswith("UIStream") else float)
                self.assertEqual(self.v.validate("View", v), [])
                seen |= {i["type"] for s in v["sections"] for i in s["items"]}
        view = self.v.files["view.schema.json"]["$defs"]
        components = [view[o["$ref"].split("/")[-1]] for o in view["Component"]["oneOf"]]
        tags = {c["properties"]["type"]["const"] for c in components
                if not c.get("x-assistant-only") and c.get("x-since", 1) <= SHOWCASE_UI}
        self.assertEqual(tags - seen, set(), "the showcase shows every extension component")

    def test_every_component_has_an_example(self):
        view = self.v.files["view.schema.json"]["$defs"]
        tags = {view[o["$ref"].split("/")[-1]]["properties"]["type"]["const"] for o in view["Component"]["oneOf"]}
        seen = set()
        for f in (SCHEMA_DIR / "examples").glob("View.*.json"):
            for s in json.loads(f.read_text(), parse_float=Decimal if f.name.startswith("UIStream") else float)["sections"]:
                seen |= {i["type"] for i in s["items"]}
        for f in (SCHEMA_DIR / "examples" / "assistant").glob("*.json"):
            v = json.loads(f.read_text(), parse_float=Decimal if f.name.startswith("UIStream") else float)
            self.assertEqual(self.v.validate("View", v), [])
            for s in v["sections"]:
                seen |= {i["type"] for i in s["items"]}
        self.assertEqual(tags - seen, set())


if __name__ == "__main__":
    unittest.main()
