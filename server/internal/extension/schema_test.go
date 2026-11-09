package extension

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const formSchema = `{"type": "object", "properties": {
  "url": {"type": "string", "title": "地址", "format": "uri"},
  "mode": {"type": "string", "oneOf": [{"const": "fast", "title": "快"}, {"const": "safe", "title": "稳"}], "default": "safe"},
  "count": {"type": "integer", "minimum": 1, "maximum": 10, "default": 3},
  "ratio": {"type": "number", "minimum": 0, "maximum": 1},
  "on": {"type": "boolean"},
  "mail": {"type": "string", "format": "email"},
  "host": {"type": "string", "format": "hostname"},
  "code": {"type": "string", "pattern": "^[A-Z]{3}$", "minLength": 3, "maxLength": 3},
  "tags": {"type": "array", "items": {"type": "string", "enum": ["a", "b", "c"]}, "uniqueItems": true, "maxItems": 2},
  "proxy": {"type": "object", "properties": {"host": {"type": "string"}, "port": {"type": "integer", "default": 8080}}, "required": ["host"]}
}, "required": ["url"]}`

func TestSchemaOrderAndForm(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(formSchema))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range s.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "url,mode,count,ratio,on,mail,host,code,tags,proxy" {
		t.Fatal("the order written:", names)
	}
	if !s.Fields[0].Required || s.Fields[1].Required {
		t.Fatal("required")
	}
	if len(s.Fields[1].Enum) != 2 || s.Fields[1].Enum[0].Title != "快" {
		t.Fatalf("%+v", s.Fields[1].Enum)
	}
	if s.Fields[9].Fields[0].Name != "host" || !s.Fields[9].Fields[0].Required {
		t.Fatalf("%+v", s.Fields[9])
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "pattern\":null") || !strings.Contains(string(b), `"items":{"name":"items","type":"string"`) {
		t.Fatal(string(b))
	}
}

func TestSchemaChecks(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(formSchema))
	if err != nil {
		t.Fatal(err)
	}
	ok := []string{
		`{"url": "https://example.com"}`,
		`{"url": "https://example.com", "mode": "fast", "count": 10, "ratio": 0.5, "on": true, "mail": "me@example.com",
		  "host": "example.com", "code": "ABC", "tags": ["a", "c"], "proxy": {"host": "p.example.com"}}`,
		`{"url": "https://example.com", "mode": null}`,
	}
	for _, v := range ok {
		if err := s.Check(json.RawMessage(v)); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	bad := map[string]string{
		`{}`:                               "url",
		`{"url": "nope"}`:                  "url",
		`{"url": "https://e.com", "x": 1}`: "x",
		`{"url": "https://e.com", "mode": "slow"}`:                      "mode",
		`{"url": "https://e.com", "count": 1.5}`:                        "count",
		`{"url": "https://e.com", "count": 11}`:                         "count",
		`{"url": "https://e.com", "ratio": "1"}`:                        "ratio",
		`{"url": "https://e.com", "on": "yes"}`:                         "on",
		`{"url": "https://e.com", "mail": "Me <me@example.com>"}`:       "mail",
		`{"url": "https://e.com", "host": "a b"}`:                       "host",
		`{"url": "https://e.com", "code": "abc"}`:                       "code",
		`{"url": "https://e.com", "tags": ["a", "a"]}`:                  "tags",
		`{"url": "https://e.com", "tags": ["d"]}`:                       "tags",
		`{"url": "https://e.com", "tags": ["a","b","c"]}`:               "tags",
		`{"url": "https://e.com", "proxy": {}}`:                         "proxy.host",
		`{"url": "https://e.com", "proxy": {"host": "h", "user": "u"}}`: "proxy.user",
		`[]`: "",
	}
	for v, path := range bad {
		err := s.Check(json.RawMessage(v))
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Path != path {
			t.Errorf("%s: %v (want at %q)", v, err, path)
		}
	}
	var none *Schema
	if none.Check(json.RawMessage(`{}`)) != nil || none.Check(nil) != nil || none.Check(json.RawMessage(`{"a":1}`)) == nil {
		t.Fatal("no schema: only an empty object")
	}
}

func TestSchemaEffective(t *testing.T) {
	s, _ := ParseSchema(json.RawMessage(formSchema))
	var got map[string]any
	_ = json.Unmarshal(s.Effective(json.RawMessage(`{"url": "https://e.com", "proxy": {"host": "h"}}`)), &got)
	if got["mode"] != "safe" || got["count"] != float64(3) || got["proxy"].(map[string]any)["port"] != float64(8080) {
		t.Fatal(got)
	}
	if _, has := got["ratio"]; has {
		t.Fatal("no default, no value")
	}
}

func TestSchemaRefusesOutsideTheSubset(t *testing.T) {
	for _, bad := range []string{
		`[]`,
		`{"type": "array"}`,
		`{"type": "object", "properties": {}, "additionalProperties": true}`,
		`{"type": "object", "properties": {"a": {"type": "string", "$ref": "#/x"}}}`,
		`{"type": "object", "properties": {"a": {"anyOf": []}}}`,
		`{"type": "object", "properties": {"a": {"type": "null"}}}`,
		`{"type": "object", "properties": {"a": {"type": "string", "minimum": 1}}}`,
		`{"type": "object", "properties": {"a": {"type": "array", "items": {"type": "object", "properties": {}}}}}`,
		`{"type": "object", "properties": {"a": {"type": "object", "properties": {"b": {"type": "object", "properties": {}}}}}}`,
		`{"type": "object", "properties": {"a": {"type": "string", "format": "date"}}}`,
		`{"type": "object", "properties": {"a": {"type": "string", "pattern": "("}}}`,
		`{"type": "object", "properties": {"a": {"type": "string", "enum": [1]}}}`,
		`{"type": "object", "properties": {"a": {"type": "string", "default": 1}}}`,
		`{"type": "object", "properties": {"a": {"type": "string", "oneOf": [{"const": "x", "if": {}}]}}}`,
		`{"type": "object", "properties": {"a": {"type": "string"}}, "required": ["b"]}`,
		`{"type": "object", "properties": {"a": {"type": "string"}, "a": {"type": "string"}}}`,
		`{"type": "object", "properties": {"a b": {"type": "string"}}}`,
	} {
		if _, err := ParseSchema(json.RawMessage(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

// x-confirm: what a form asks before turning a switch on, on any field; it must be a string.
func TestSchemaConfirm(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(`{"type": "object", "properties": {
	  "nolock": {"type": "array", "items": {"type": "string", "format": "machine"}, "x-confirm": "它会一直不锁屏"},
	  "on": {"type": "boolean", "x-confirm": "确定？"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Fields[0].Confirm != "它会一直不锁屏" || s.Fields[1].Confirm != "确定？" {
		t.Fatalf("%+v", s.Fields)
	}
	if _, err := ParseSchema(json.RawMessage(`{"type": "object", "properties": {"on": {"type": "boolean", "x-confirm": true}}}`)); err == nil {
		t.Fatal("x-confirm is words")
	}
}

// A list of the hub's machines (format machine): the form draws a switch per machine, of the systems x-os names.
func TestSchemaMachines(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(`{"type": "object", "properties": {
	  "macs": {"type": "array", "title": "能操控的电脑", "items": {"type": "string", "format": "machine", "x-os": ["darwin"]}, "uniqueItems": true}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.Fields[0])
	if !strings.Contains(string(b), `"items":{"name":"items","type":"string","format":"machine","os":["darwin"]}`) {
		t.Fatal("the form says it:", string(b))
	}
	if err := s.Check(json.RawMessage(`{"macs": ["studio", "macbook"]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"macs": [""]}`, `{"macs": [" studio"]}`, `{"macs": ["studio", "studio"]}`} {
		if s.Check(json.RawMessage(bad)) == nil {
			t.Errorf("%s passed", bad)
		}
	}
	for _, bad := range []string{
		`{"type": "object", "properties": {"m": {"type": "string", "x-os": ["darwin"]}}}`,
		`{"type": "object", "properties": {"m": {"type": "string", "format": "machine", "x-os": ["beos"]}}}`,
		`{"type": "object", "properties": {"m": {"type": "string", "format": "machine", "x-os": []}}}`,
		`{"type": "object", "properties": {"m": {"type": "array", "x-os": ["darwin"], "items": {"type": "string"}}}}`,
	} {
		if _, err := ParseSchema(json.RawMessage(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

// One of the hub's machines and one of its launchers (format machine / launcher on a string): the form draws a picker
// of them; the value is the machine's id or the launcher's name.
func TestSchemaMachineAndLauncherPickers(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(`{"type": "object", "properties": {
	  "where": {"type": "string", "title": "在哪台电脑上跑", "format": "machine", "x-os": ["darwin", "linux"]},
	  "launcher": {"type": "string", "title": "用的启动器", "format": "launcher"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.Fields)
	for _, want := range []string{`"name":"where","type":"string","title":"在哪台电脑上跑","format":"machine","os":["darwin","linux"]`,
		`"name":"launcher","type":"string","title":"用的启动器","format":"launcher"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("the form says %s, not %s", b, want)
		}
	}
	for _, good := range []string{`{}`, `{"where": "studio", "launcher": "claude"}`, `{"launcher": "work_gw"}`} {
		if err := s.Check(json.RawMessage(good)); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{`{"launcher": ""}`, `{"launcher": " claude"}`, `{"launcher": 3}`, `{"where": ""}`} {
		if s.Check(json.RawMessage(bad)) == nil {
			t.Errorf("%s passed", bad)
		}
	}
	for _, bad := range []string{
		`{"type": "object", "properties": {"l": {"type": "string", "format": "launcher", "x-os": ["darwin"]}}}`,
		`{"type": "object", "properties": {"l": {"type": "integer", "format": "launcher"}}}`,
		`{"type": "object", "properties": {"l": {"type": "string", "format": "model"}}}`,
	} {
		if _, err := ParseSchema(json.RawMessage(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}
