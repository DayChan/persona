package transcript

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// unmarshalLine fails and decodes as json.Unmarshal does: every fixture line, its message's content and each block's
// content, as written and cut short, into the types the parser reads them into. Both fail, or both give the same value.
func TestLineJSONMatchesUnmarshal(t *testing.T) {
	n := 0
	same := func(data []byte) {
		n++
		for _, v := range []func() any{func() any { return new(entry) }, func() any { return new(string) }, func() any { return new([]block) }} {
			want, got := v(), v()
			wantErr, gotErr := json.Unmarshal(data, want), unmarshalLine(data, got)
			if (wantErr == nil) != (gotErr == nil) || wantErr == nil && !reflect.DeepEqual(want, got) {
				t.Fatalf("%T from %.200q: json.Unmarshal %v, unmarshalLine %v", want, data, wantErr, gotErr)
			}
		}
	}
	for _, fx := range fixtures(t) {
		raw, err := os.ReadFile(fx.path)
		if err != nil {
			t.Fatal(err)
		}
		for line := range bytes.Lines(raw) {
			line = bytes.TrimSuffix(line, []byte("\n"))
			same(line)
			same(line[:len(line)/2])
			var e entry
			if json.Unmarshal(line, &e) != nil || e.Message == nil || len(e.Message.Content) == 0 {
				continue
			}
			same(e.Message.Content)
			var blocks []block
			_ = json.Unmarshal(e.Message.Content, &blocks)
			for _, b := range blocks {
				if len(b.Content) > 0 {
					same(b.Content)
				}
			}
		}
	}
	// Values of the wrong type fail too, wherever they are, and so does anything after the value.
	for _, line := range []string{`{"type":5}`, `{"type":"user","timestamp":"now"}`, `{"message":{"content":[{"type":1}]}}`, `{"type":"user"}x`} {
		same([]byte(line))
	}
	if n < 500 {
		t.Fatalf("only %d inputs compared", n)
	}
}

// unmarshalLine reads a line once: one broken at its end has its start decoded already when it fails, where
// json.Unmarshal checks the whole line first and leaves the value as it was. Every caller drops the value on an error.
func TestLineJSONReadsOnce(t *testing.T) {
	line := []byte(`{"type":"user","uuid":"u1","message":{"role":"us`)
	var once, twice entry
	if unmarshalLine(line, &once) == nil || json.Unmarshal(line, &twice) == nil {
		t.Fatal("a broken line decoded")
	}
	if once.Type != "user" || twice.Type != "" {
		t.Fatalf("type %q, %q: want the line read once, as it was decoded", once.Type, twice.Type)
	}
}
