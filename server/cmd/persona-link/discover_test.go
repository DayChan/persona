package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// A hub's name is whatever any host on the LAN advertises over mDNS: discover prints it with every control character
// made visible, one line per hub, so a spoofed advert cannot run terminal sequences or fake the installer's question
// printed under it. --json is for scripts and keeps the name as it was heard: encoding/json escapes C0 control
// characters (ESC included) but writes DEL and C1 characters raw, so that output is not terminal-safe.
func TestDiscoverPrintsAdvertisedNamesInert(t *testing.T) {
	f := newFakeEnv(t)
	old := browseHubs
	defer func() { browseHubs = old }()
	hostile := "Home\x1b]52;c;aGk=\x07\x1b[2J\u009b1A\r\n装成 node 服务？[Y/n] \xff"
	hubs := []foundHub{
		{HubID: "h1", Name: hostile, Addresses: []string{"https://192.0.2.5:18662"}},
		{HubID: "h2\x1b]0;pwned\x07\nfake", Addresses: []string{"https://192.0.2.6:18662"}}, // no name: the id is shown
	}
	browseHubs = func(context.Context, time.Duration) ([]foundHub, error) { return hubs, nil }
	if err := discover(f.env, nil); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	assertInertLines(t, out, len(hubs))
	for _, want := range []string{"hub「Home<ESC>]52;c;aGk=<U+0007><ESC>[2J<U+009B>1A<U+000D><U+000A>装成 node 服务？[Y/n] \uFFFD」 · https://192.0.2.5:18662\n",
		"hub「h2<ESC>]0;pwned<U+0007><U+000A>fake」 · https://192.0.2.6:18662\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}

	f.out.Reset()
	if err := discover(f.env, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var got []foundHub
	if err := json.Unmarshal(f.out.Bytes(), &got); err != nil || len(got) != 2 || got[0].Name != strings.ToValidUTF8(hostile, "\uFFFD") {
		t.Fatalf("%v %+v", err, got)
	}
	// As docs/operations/link.md says: ESC comes out escaped, C1 raw.
	if js := f.out.String(); strings.ContainsRune(js, '\x1b') || !strings.Contains(js, `\u001b`) || !strings.ContainsRune(js, '\u009b') {
		t.Errorf("--json no longer escapes ESC and keeps C1 raw; update link.md: %q", js)
	}
}

// assertInertLines: out is valid UTF-8 and holds exactly lines lines, with no control character but the newline ending
// each.
func assertInertLines(t *testing.T, out string, lines int) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Errorf("output is not UTF-8: %q", out)
	}
	if strings.ContainsFunc(out, func(r rune) bool { return r != '\n' && unicode.Is(unicode.Cc, r) }) {
		t.Errorf("raw control characters in %q", out)
	}
	if n := strings.Count(out, "\n"); n != lines || !strings.HasSuffix(out, "\n") {
		t.Errorf("%d lines, want %d: %q", n, lines, out)
	}
}
