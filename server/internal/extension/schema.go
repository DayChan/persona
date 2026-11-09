package extension

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// ConfigMost is the largest configuration value kept.
const ConfigMost = 64 << 10

// Schema is a plugin's configuration: the subset of JSON Schema every client can draw as a form
// (docs/plans/plan-extensions.md "configuration"), its properties in the order the manifest has them.
type Schema struct {
	Fields []Field `json:"fields"`
}

// Field is one property, as the form draws it.
type Field struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"` // string | integer | number | boolean | array | object
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Enum        []Choice        `json:"enum,omitempty"`
	Format      string          `json:"format,omitempty"` // uri | email | hostname | machine | launcher
	// OS, for format machine: only machines of these systems (x-os: darwin, linux, windows); none, any.
	OS []string `json:"os,omitempty"`
	// Confirm (x-confirm): what a form asks before a switch is turned on (a boolean, or a machine of a machine list),
	// for settings with a cost the user should agree to (the plugin cua's "Don't auto-lock while the extension is on", #1507).
	Confirm     string   `json:"confirm,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	MinLength   *int     `json:"min_length,omitempty"`
	MaxLength   *int     `json:"max_length,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	Items       *Field   `json:"items,omitempty"` // array: its items (a string field)
	MinItems    *int     `json:"min_items,omitempty"`
	MaxItems    *int     `json:"max_items,omitempty"`
	UniqueItems bool     `json:"unique_items,omitempty"`
	Fields      []Field  `json:"fields,omitempty"` // object: its properties

	pattern *regexp.Regexp
}

// Choice is one value of an enum, with its label.
type Choice struct {
	Value json.RawMessage `json:"value"`
	Title string          `json:"title,omitempty"`
}

var (
	describing = []string{"title", "description", "default", "x-confirm"}
	keysFor    = map[string][]string{
		"string":  {"enum", "oneOf", "format", "x-os", "pattern", "minLength", "maxLength"},
		"integer": {"enum", "oneOf", "minimum", "maximum"},
		"number":  {"enum", "oneOf", "minimum", "maximum"},
		"boolean": {},
		"array":   {"items", "minItems", "maxItems", "uniqueItems"},
		"object":  {"properties", "required"},
	}
	// machine: one of the hub's machines, by id: a form draws a list of them as a switch each and one of them as a
	// picker (x-os narrows both). launcher: one of the hub's launchers, by name: a form draws a picker of them.
	formats      = []string{"uri", "email", "hostname", "machine", "launcher"}
	systems      = []string{"darwin", "linux", "windows"}
	hostnameLike = lazyre.New(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

// ParseSchema reads a configuration schema, refusing anything outside the subset; nil, nil when raw is empty.
func ParseSchema(raw json.RawMessage) (*Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, errors.New("an object")
	}
	for k := range root {
		if !slices.Contains([]string{"$schema", "type", "properties", "required", "title", "description"}, k) {
			return nil, fmt.Errorf("%q is not in the subset the forms draw", k)
		}
	}
	if typeOf(root) != "object" {
		return nil, errors.New(`the root is {"type": "object", "properties": {…}}`)
	}
	fields, err := parseProperties(root, "", 0)
	if err != nil {
		return nil, err
	}
	return &Schema{Fields: fields}, nil
}

func typeOf(m map[string]json.RawMessage) string {
	var t string
	_ = json.Unmarshal(m["type"], &t)
	return t
}

func parseProperties(obj map[string]json.RawMessage, at string, depth int) ([]Field, error) {
	names, values, err := ordered(obj["properties"])
	if err != nil {
		return nil, fmt.Errorf("%sproperties: %v", at, err)
	}
	var required []string
	if raw, ok := obj["required"]; ok {
		if err := json.Unmarshal(raw, &required); err != nil {
			return nil, fmt.Errorf("%srequired: a list of names", at)
		}
	}
	for _, r := range required {
		if !slices.Contains(names, r) {
			return nil, fmt.Errorf("%srequired: %q is not a property", at, r)
		}
	}
	fields := make([]Field, 0, len(names))
	for i, name := range names {
		f, err := parseField(name, values[i], at+name+".", depth)
		if err != nil {
			return nil, err
		}
		f.Required = slices.Contains(required, name)
		fields = append(fields, f)
	}
	return fields, nil
}

// ordered are an object's keys in the order written, with their values.
func ordered(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil, errors.New("missing")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("an object")
	}
	var names []string
	var values []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if slices.Contains(names, name) {
			return nil, nil, fmt.Errorf("%q twice", name)
		}
		names = append(names, name)
		values = append(values, v)
	}
	return names, values, nil
}

var fieldName = lazyre.New(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

func parseField(name string, raw json.RawMessage, at string, depth int) (Field, error) {
	f := Field{Name: name}
	fail := func(format string, args ...any) (Field, error) {
		return Field{}, fmt.Errorf(strings.TrimSuffix(at, ".")+": "+format, args...)
	}
	if !fieldName.MatchString(name) {
		return fail("not a usable property name")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fail("an object")
	}
	f.Type = typeOf(m)
	allowed, ok := keysFor[f.Type]
	if !ok {
		return fail("type %q: string, integer, number, boolean, array or object", f.Type)
	}
	for k := range m {
		if k != "type" && !slices.Contains(describing, k) && !slices.Contains(allowed, k) {
			return fail("%q is not in the subset the forms draw for a %s", k, f.Type)
		}
	}
	str := func(k string, out *string) error {
		if raw, ok := m[k]; ok {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("%s: a string", k)
			}
		}
		return nil
	}
	num := func(k string) (*float64, error) {
		raw, ok := m[k]
		if !ok {
			return nil, nil
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s: a number", k)
		}
		return &v, nil
	}
	count := func(k string) (*int, error) {
		raw, ok := m[k]
		if !ok {
			return nil, nil
		}
		var v int
		if err := json.Unmarshal(raw, &v); err != nil || v < 0 {
			return nil, fmt.Errorf("%s: a whole number, 0 or more", k)
		}
		return &v, nil
	}
	var errs []error
	errs = append(errs, str("title", &f.Title), str("description", &f.Description), str("format", &f.Format), str("pattern", &f.Pattern), str("x-confirm", &f.Confirm))
	var err error
	if f.MinLength, err = count("minLength"); err != nil {
		errs = append(errs, err)
	}
	if f.MaxLength, err = count("maxLength"); err != nil {
		errs = append(errs, err)
	}
	if f.MinItems, err = count("minItems"); err != nil {
		errs = append(errs, err)
	}
	if f.MaxItems, err = count("maxItems"); err != nil {
		errs = append(errs, err)
	}
	if f.Minimum, err = num("minimum"); err != nil {
		errs = append(errs, err)
	}
	if f.Maximum, err = num("maximum"); err != nil {
		errs = append(errs, err)
	}
	if raw, ok := m["uniqueItems"]; ok && json.Unmarshal(raw, &f.UniqueItems) != nil {
		errs = append(errs, errors.New("uniqueItems: true or false"))
	}
	if err := errors.Join(errs...); err != nil {
		return fail("%v", err)
	}
	if f.Format != "" && !slices.Contains(formats, f.Format) {
		return fail("format %q: uri, email, hostname, machine or launcher", f.Format)
	}
	if raw, ok := m["x-os"]; ok {
		if f.Format != "machine" {
			return fail("x-os: only with format machine")
		}
		if json.Unmarshal(raw, &f.OS) != nil || len(f.OS) == 0 || slices.ContainsFunc(f.OS, func(os string) bool { return !slices.Contains(systems, os) }) {
			return fail("x-os: a list of darwin, linux, windows")
		}
	}
	if f.Pattern != "" {
		re, err := regexp.Compile(f.Pattern)
		if err != nil {
			return fail("pattern: %v", err)
		}
		f.pattern = re
	}
	if _, has := m["enum"]; has {
		var values []json.RawMessage
		if err := json.Unmarshal(m["enum"], &values); err != nil || len(values) == 0 {
			return fail("enum: a list of values")
		}
		for _, v := range values {
			f.Enum = append(f.Enum, Choice{Value: v})
		}
	}
	if _, has := m["oneOf"]; has {
		if f.Enum != nil {
			return fail("enum and oneOf: one of them")
		}
		var choices []map[string]json.RawMessage
		if err := json.Unmarshal(m["oneOf"], &choices); err != nil || len(choices) == 0 {
			return fail(`oneOf: only [{"const": …, "title": "…"}] (an enum with labels)`)
		}
		for _, c := range choices {
			v, ok := c["const"]
			var title string
			if !ok || len(c) > 2 || (len(c) == 2 && json.Unmarshal(c["title"], &title) != nil) {
				return fail(`oneOf: only [{"const": …, "title": "…"}] (an enum with labels)`)
			}
			f.Enum = append(f.Enum, Choice{Value: v, Title: title})
		}
	}
	switch f.Type {
	case "array":
		raw, ok := m["items"]
		if !ok {
			return fail("items: required")
		}
		item, err := parseField("items", raw, at+"items.", depth+1)
		if err != nil {
			return Field{}, err
		}
		if item.Type != "string" {
			return fail("items: only strings (a list of objects is not in the subset)")
		}
		f.Items = &item
	case "object":
		if depth > 0 {
			return fail("an object only at the first level")
		}
		var err error
		if f.Fields, err = parseProperties(m, at, depth+1); err != nil {
			return Field{}, err
		}
		for _, sub := range f.Fields {
			if sub.Type == "object" {
				return fail("an object only at the first level")
			}
		}
	}
	for _, c := range f.Enum {
		if err := f.check(c.Value, at); err != nil {
			return fail("enum value %s: %v", c.Value, err)
		}
	}
	if raw, ok := m["default"]; ok {
		if err := f.check(raw, at); err != nil {
			return fail("default: %v", err)
		}
		f.Default = raw
	}
	return f, nil
}

// FieldError is a configuration value that does not fit: where (dotted) and why.
type FieldError struct {
	cause   error
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Message }

// Check holds a configuration value to the schema: an object with only the schema's properties, each of the right
// kind, the required ones present (or defaulted). A nil schema takes only an empty object.
func (s *Schema) Check(raw json.RawMessage) error {
	if len(raw) > ConfigMost {
		return fieldError("", i18n.ErrEnglish("extension-status.schema_bytes", i18n.Args{"count": len(raw), "max": ConfigMost}))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var fields []Field
	if s != nil {
		fields = s.Fields
	}
	return checkObject(fields, raw, "")
}

// CheckSaved checks the saved values but lets required fields added by a new version be missing, leaving them to
// needs_setup.
func (s *Schema) CheckSaved(raw json.RawMessage) error {
	if s == nil {
		return s.Check(raw)
	}
	copy := *s
	var optional func(Field) Field
	optional = func(f Field) Field {
		f.Required = false
		f.Fields = slices.Clone(f.Fields)
		for i := range f.Fields {
			f.Fields[i] = optional(f.Fields[i])
		}
		if f.Items != nil {
			item := optional(*f.Items)
			f.Items = &item
		}
		return f
	}
	copy.Fields = slices.Clone(s.Fields)
	for i := range copy.Fields {
		copy.Fields[i] = optional(copy.Fields[i])
	}
	return copy.Check(raw)
}

func checkObject(fields []Field, raw json.RawMessage, at string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return fieldError(strings.TrimSuffix(at, "."), i18n.Err("extension-status.schema_object"))
	}
	for k := range m {
		if !slices.ContainsFunc(fields, func(f Field) bool { return f.Name == k }) {
			return fieldError(at+k, i18n.Err("extension-status.schema_unknown_field"))
		}
	}
	for _, f := range fields {
		v, ok := m[f.Name]
		if !ok || string(v) == "null" {
			if f.Required && f.Default == nil {
				return fieldError(at+f.Name, i18n.Err("extension-status.schema_required"))
			}
			continue
		}
		if err := f.check(v, at+f.Name); err != nil {
			var fe *FieldError
			if errors.As(err, &fe) {
				return fe
			}
			return fieldError(at+f.Name, err)
		}
	}
	return nil
}

// check holds one value to the field.
func (f *Field) check(raw json.RawMessage, at string) error {
	switch f.Type {
	case "object":
		return checkObject(f.Fields, raw, at+".")
	case "array":
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return i18n.Err("extension-status.schema_array")
		}
		if f.MinItems != nil && len(items) < *f.MinItems {
			return i18n.Err("extension-status.schema_min_items", i18n.Args{"count": *f.MinItems})
		}
		if f.MaxItems != nil && len(items) > *f.MaxItems {
			return i18n.Err("extension-status.schema_max_items", i18n.Args{"count": *f.MaxItems})
		}
		seen := map[string]bool{}
		for i, item := range items {
			if err := f.Items.check(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return i18n.Err("extension-status.schema_item", i18n.Args{"count": i + 1, "err": err})
			}
			if f.UniqueItems && seen[string(item)] {
				return i18n.Err("extension-status.schema_duplicate", i18n.Args{"count": i + 1})
			}
			seen[string(item)] = true
		}
		return nil
	case "boolean":
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return i18n.Err("extension-status.schema_boolean")
		}
		return nil
	case "integer", "number":
		var n float64
		if json.Unmarshal(raw, &n) != nil {
			return i18n.Err("extension-status.schema_number")
		}
		if f.Type == "integer" && n != math.Trunc(n) {
			return i18n.Err("extension-status.schema_integer")
		}
		if f.Minimum != nil && n < *f.Minimum {
			return i18n.Err("extension-status.schema_minimum", i18n.Args{"min": *f.Minimum})
		}
		if f.Maximum != nil && n > *f.Maximum {
			return i18n.Err("extension-status.schema_maximum", i18n.Args{"max": *f.Maximum})
		}
	case "string":
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return i18n.Err("extension-status.schema_string")
		}
		n := utf8.RuneCountInString(s)
		if f.MinLength != nil && n < *f.MinLength {
			return i18n.Err("extension-status.schema_min_length", i18n.Args{"count": *f.MinLength})
		}
		if f.MaxLength != nil && n > *f.MaxLength {
			return i18n.Err("extension-status.schema_max_length", i18n.Args{"count": *f.MaxLength})
		}
		if f.pattern != nil && !f.pattern.MatchString(s) {
			return i18n.Err("extension-status.schema_format")
		}
		switch f.Format {
		case "uri":
			if u, err := url.Parse(s); err != nil || u.Scheme == "" || u.Host == "" {
				return i18n.Err("extension-status.schema_uri")
			}
		case "email":
			if a, err := mail.ParseAddress(s); err != nil || a.Address != s {
				return i18n.Err("extension-status.schema_email")
			}
		case "hostname":
			if !hostnameLike.MatchString(s) {
				return i18n.Err("extension-status.schema_hostname")
			}
		case "machine":
			if strings.TrimSpace(s) != s || s == "" {
				return i18n.Err("extension-status.schema_machine")
			}
		case "launcher":
			if strings.TrimSpace(s) != s || s == "" {
				return i18n.Err("extension-status.schema_launcher")
			}
		}
	}
	if len(f.Enum) > 0 && !slices.ContainsFunc(f.Enum, func(c Choice) bool { return sameJSON(c.Value, raw) }) {
		return i18n.Err("extension-status.schema_enum")
	}
	return nil
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(ab, bb)
}

// Effective is a configuration with the schema's defaults filled in where it has no value: what the plugin gets.
func (s *Schema) Effective(raw json.RawMessage) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	if s != nil {
		fill(s.Fields, m)
	}
	b, _ := json.Marshal(m)
	return b
}

func fill(fields []Field, m map[string]json.RawMessage) {
	for _, f := range fields {
		v, ok := m[f.Name]
		if f.Type == "object" {
			sub := map[string]json.RawMessage{}
			if ok {
				_ = json.Unmarshal(v, &sub)
			}
			fill(f.Fields, sub)
			if len(sub) > 0 {
				m[f.Name], _ = json.Marshal(sub)
			}
			continue
		}
		if (!ok || string(v) == "null") && f.Default != nil {
			m[f.Name] = f.Default
		}
	}
}
