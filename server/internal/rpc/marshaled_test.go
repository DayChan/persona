package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"testing"
)

// Params json.Marshal wrote go out in the line json.Marshal writes for the envelope: HTML characters, U+2028 and
// control characters escaped and invalid UTF-8 replaced in params, an event the encoder would escape or leave out
// written by the encoder, null for nil params and the same error (nothing written) for empty ones.
func TestNotifyMarshaledIsTheSameLine(t *testing.T) {
	values := []any{
		map[string]any{"sessions": []map[string]any{{"title": `<b>fix</b> & "ship" \ it`, "text": "a b c \x00\x1f\x7f", "bad": "\xff\xfe \xe2\x82"}}},
		map[string]any{"sessions": nil}, map[string]any{"sessions": []int{}}, nil, "é", 12.5, []any{true, false, map[string]any{}},
	}
	for _, event := range []string{"sessions.changed", "", "a<b", "é", `q"`} {
		for _, v := range values {
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			err = New("t", &got).Notify(event, Marshaled(raw))
			want, werr := oldNotify(event, v)
			if err != nil || werr != nil || !bytes.Equal(got.Bytes(), want) {
				t.Errorf("Notify(%q, %s):\n%q, %v\nwas %q, %v", event, raw, got.Bytes(), err, want, werr)
			}
		}
	}
	for _, raw := range []Marshaled{nil, {}} { // null, and an error
		var got bytes.Buffer
		err := New("t", &got).Notify("e", raw)
		want, werr := oldNotify("e", json.RawMessage(raw))
		if !bytes.Equal(got.Bytes(), want) || fmt.Sprint(err) != fmt.Sprint(werr) {
			t.Errorf("Notify of %#v wrote %q, %v; was %q, %v", raw, got.Bytes(), err, want, werr)
		}
	}
	if b, err := json.Marshal(map[string]any{"p": Marshaled(`{"a":1}`)}); err != nil || string(b) != `{"p":{"a":1}}` {
		t.Errorf("Marshaled inside a value: %s, %v", b, err)
	}
}

// Sending a 260 KiB session list json.Marshal wrote allocates its line, nothing more: as json.RawMessage it was 418 KB
// (#2900), the envelope's encoder copying the list into buffers it grows. The bound is the line plus 3%.
func TestNotifyMarshaledAllocatesTheLine(t *testing.T) {
	p, raw := New("t", io.Discard), marshaledList(260<<10)
	line := len(`{"event":"sessions","params":}`+"\n") + len(raw)
	per := testing.AllocsPerRun(20, func() { _ = p.Notify("sessions", raw) })
	if per > 2 { // the line, and the params' interface value
		t.Errorf("a Notify of %d bytes json.Marshal wrote allocated %.0f times, want at most twice", len(raw), per)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 20 {
		_ = p.Notify("sessions", raw)
	}
	runtime.ReadMemStats(&after)
	if per, most := float64(after.TotalAlloc-before.TotalAlloc)/20, 1.03*float64(line); per > most {
		t.Errorf("a Notify of a %d-byte line allocated %.0f bytes, want at most %.0f", line, per, most)
	}
}

// marshaledList is sessionList(size) as json.Marshal writes it (its HTML characters escaped).
func marshaledList(size int) Marshaled {
	b, err := json.Marshal(sessionList(size))
	if err != nil {
		panic(err)
	}
	return b
}

// BenchmarkNotifyMarshaled sends the session lists of BenchmarkNotifyRaw as json.Marshal wrote them, as a node sends
// sessions.changed.
func BenchmarkNotifyMarshaled(b *testing.B) {
	for _, size := range []int{39 << 10, 260 << 10} {
		b.Run(fmt.Sprintf("%dK", size>>10), func(b *testing.B) {
			p, raw := New("t", io.Discard), marshaledList(size)
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			for range b.N {
				if err := p.Notify("sessions", raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
