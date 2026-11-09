package sessionlist

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestParseAndEnabled(t *testing.T) {
	for _, key := range []string{"limit", "cursor", "since", "summary", "state", "project", "group", "counts"} {
		if !Enabled(url.Values{key: {""}}) {
			t.Errorf("%s did not enable indexed behavior", key)
		}
	}
	legacy := url.Values{"machine_id": {"machine-a"}, "source": {"runtime"}, "provider": {"provider-a"}, "q": {"hello"}}
	if Enabled(legacy) || Enabled(nil) {
		t.Fatal("legacy parameters enabled indexed behavior")
	}
	o, err := Parse(legacy)
	if err != nil || o.Limit != 50 || o.Machine != "machine-a" || o.Source != "runtime" || o.Provider != "provider-a" || o.Q != "hello" {
		t.Fatalf("parsed legacy = %+v, %v", o, err)
	}
	for _, value := range []string{"true", "1"} {
		o, err := Parse(url.Values{"summary": {value}, "counts": {value}, "limit": {"200"}, "state": {"working"}, "project": {"project-a"}, "group": {"group-a"}})
		if err != nil || !o.Summary || o.Limit != 200 || o.State != "working" || o.Project != "project-a" || o.Group != "group-a" {
			t.Fatalf("parse = %+v, %v", o, err)
		}
	}
	for _, value := range []string{"false", "0"} {
		o, err := Parse(url.Values{"summary": {value}, "counts": {value}})
		if err != nil || o.Summary {
			t.Fatalf("parse boolean = %+v, %v", o, err)
		}
	}
	// The limit is Unicode characters, not bytes, and matching is caseless.
	o, err = Parse(url.Values{"q": {strings.Repeat("界", 256)}})
	if err != nil || o.Q == "" {
		t.Fatalf("Unicode q = %+v, %v", o, err)
	}
	o, err = Parse(url.Values{"q": {"MiXeD"}, "unknown": {"one", "two"}})
	if err != nil || o.Q != "mixed" {
		t.Fatalf("normalized q = %+v, %v", o, err)
	}
}

// session_id belongs only to the runtime adapter. Older hubs and the unchanged
// personal-agent adapter parse it as unknown, not as the index's composite ID.
func TestExactLookupQueryRequiresAdapterFeature(t *testing.T) {
	v := url.Values{"machine_id": {"machine-a"}, "session_id": {"missing"}}
	if Enabled(v) {
		t.Fatal("runtime-only lookup key enabled a different collection")
	}
	v.Set("summary", "0")
	v.Set("limit", "50")
	o, err := Parse(v)
	if err != nil || o.ID != "" {
		t.Fatalf("adapter-specific query = %+v, %v", o, err)
	}
	var i Index
	items := make([]Item, 60)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("s%02d", n), n)
	}
	i.Replace("batch", items)
	p := mustQuery(t, &i, o)
	if len(p.Sessions) != 50 || p.Total != 60 || !p.HasMore {
		t.Fatalf("old hub ignoring session_id = %+v", p)
	}
}

func TestMalformedParameters(t *testing.T) {
	tests := []url.Values{
		{"limit": {""}}, {"limit": {"0"}}, {"limit": {"-1"}}, {"limit": {"201"}}, {"limit": {"+50"}}, {"limit": {"050"}}, {"limit": {"1.5"}}, {"limit": {" 50"}}, {"limit": {"999999999999999999999999"}},
		{"summary": {""}}, {"summary": {"TRUE"}}, {"summary": {"yes"}}, {"counts": {"2"}},
		{"cursor": {""}}, {"cursor": {"junk"}}, {"since": {""}}, {"since": {"junk"}},
		{"q": {strings.Repeat("x", 257)}}, {"q": {strings.Repeat("界", 257)}}, {"q": {string([]byte{0xff})}},
	}
	for _, key := range []string{"limit", "cursor", "since", "summary", "state", "machine_id", "project", "group", "source", "provider", "q", "counts"} {
		tests = append(tests, url.Values{key: {"a", "b"}}, url.Values{key: nil})
	}
	for n, values := range tests {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			_, err := Parse(values)
			requireCode(t, err, "invalid_query")
		})
	}
	var i Index
	for _, o := range []Options{{Limit: -1}, {Limit: 201}, {Q: strings.Repeat("x", 257)}, {Since: "junk"}, {Cursor: "junk"}} {
		_, err := i.Query(o)
		requireCode(t, err, "invalid_query")
		_, err = i.Counts(o)
		requireCode(t, err, "invalid_query")
	}
}

func TestMalformedTokens(t *testing.T) {
	var i Index
	i.Replace("batch", []Item{testItem("a", 1), testItem("b", 2)})
	page := mustQuery(t, &i, Options{Limit: 1})
	revision := i.Revision()
	revisionParts := strings.Split(revision, ".")
	cursorParts := strings.Split(page.NextCursor, ".")
	malformedRevisions := []string{"v2." + i.epoch + ".0", "v1." + i.epoch + ".-1", "v1." + i.epoch + ".+1", "v1." + i.epoch + ".01", "v1." + i.epoch + ".", "v1." + i.epoch + ".18446744073709551616", "v1.invalid.1", revision + ".extra", "v1." + i.epoch + "." + fmt.Sprint(i.revision+1)}
	for _, s := range malformedRevisions {
		_, err := i.Query(Options{Since: s})
		requireCode(t, err, "invalid_query")
	}
	malformedCursors := []string{page.NextCursor + ".extra", "c2." + strings.Join(cursorParts[1:], ".")}
	for _, position := range []string{"0", "-1", "01", "", "18446744073709551616"} {
		parts := append([]string(nil), cursorParts...)
		parts[3] = position
		malformedCursors = append(malformedCursors, strings.Join(parts, "."))
	}
	parts := append([]string(nil), cursorParts...)
	parts[4] = randomToken() // syntactically valid, but not the signature
	malformedCursors = append(malformedCursors, strings.Join(parts, "."))
	// Even a correctly signed offset outside the result must be rejected.
	malformedCursors = append(malformedCursors, i.cursorToken(cursorParts[2], 100))
	for _, s := range malformedCursors {
		_, err := i.Query(Options{Cursor: s})
		requireCode(t, err, "invalid_query")
	}
	o, err := Parse(url.Values{"limit": {"1"}, "cursor": {page.NextCursor}, "since": {revisionParts[0] + "." + revisionParts[1] + ".0"}})
	if err != nil || o.Cursor != page.NextCursor || o.Since == "" {
		t.Fatalf("parse valid tokens = %+v, %v", o, err)
	}
	// Case-normalized search text binds equivalently on continuation pages.
	filtered := mustQuery(t, &i, Options{Limit: 1, Q: "TITLE"})
	mustQuery(t, &i, Options{Cursor: filtered.NextCursor, Q: "title"})
}

func TestPaneSourceParameters(t *testing.T) {
	for _, source := range []string{"terminal", "feishu", "desktop", "unknown"} {
		values := url.Values{"pane_source": {source}}
		o, err := Parse(values)
		if !Enabled(values) || err != nil || o.PaneSource != source {
			t.Fatalf("%s: %+v %v", source, o, err)
		}
	}
	for _, values := range []url.Values{
		{"pane_source": {""}}, {"pane_source": {"runtime"}}, {"pane_source": {"Terminal"}},
		{"pane_source": {"terminal,feishu"}}, {"pane_source": {" terminal"}},
		{"pane_source": {"terminal", "terminal"}}, {"pane_source": nil},
	} {
		_, err := Parse(values)
		requireCode(t, err, "invalid_query")
	}
	var i Index
	_, err := i.Query(Options{PaneSource: "runtime"})
	requireCode(t, err, "invalid_query")
	_, err = i.Counts(Options{PaneSource: "runtime"})
	requireCode(t, err, "invalid_query")
}
