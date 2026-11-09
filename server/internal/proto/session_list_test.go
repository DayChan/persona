package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sessionsEvent is what the callers decode an event's params into: the list and one member beside it.
type sessionsEvent struct {
	MachineID string    `json:"machine_id"`
	Sessions  []Session `json:"sessions"`
}

// decodeEvent decodes data as the callers do with a SessionListDecoder: ScanSessionList, the rest with
// json.Unmarshal and the list through d, or all of it with json.Unmarshal when the scan says so.
func decodeEvent(d *SessionListDecoder, data []byte) (sessionsEvent, bool, error) {
	var p sessionsEvent
	rest, rows, ok := ScanSessionList(data)
	if !ok {
		return p, false, json.Unmarshal(data, &p)
	}
	if err := json.Unmarshal(rest, &p); err != nil {
		return p, true, err
	}
	var err error
	p.Sessions, _, err = d.Decode(rows)
	return p, true, err
}

// The scan takes the fast way only for an object json.Unmarshal reads the same way in parts; everything else (a
// "sessions" in another case or twice, escapes in a name, a list that is not an array, bytes after the object) goes
// back to json.Unmarshal. Either way the event decodes as json.Unmarshal decodes it.
func TestScanSessionListMatchesUnmarshal(t *testing.T) {
	one := `{"session_id":"s1","title":"one"}`
	for _, tc := range []struct {
		data string
		fast bool
	}{
		{`{"machine_id":"m","sessions":[` + one + `,{"session_id":"s2"}],"muted":["s1"]}`, true},
		{" \n{ \"sessions\" : [ " + one + " , null ] }\n\t", true},
		{`{"sessions":[]}`, true},
		{`{"Machine_ID":"m","sessions":[` + one + `]}`, true},
		{`{"sessions":[` + one + `,` + one + `]}`, true}, // the same session twice
		{`{"sessions":[{"session_id":"a","session_id":"b","started_by_jarvis":true}]}`, true},
		{"{\"sessions\":[{\"title\":\"bad \xff utf-8\"}]}", true},
		{`{"sessions":[{"title":5}]}`, true},
		{`{"sessions":[1]}`, true},
		{`{"machine_id":5,"sessions":[]}`, true},
		{`{"Sessions":[` + one + `]}`, false},
		{`{"SESSIONS":[` + one + `]}`, false},
		{`{"sessions":[` + one + `],"Sessions":[{"session_id":"s2"}]}`, false},
		{`{"sessions":[` + one + `],"sessions":[{"session_id":"s2"}]}`, false},
		{`{"machine_id":"a","sessions":[],"machine_id":"b"}`, false},
		{"{\"\u017fessions\":[" + one + "]}", false}, // U+017F folds to s
		{`{"ses_sions":[` + one + `]}`, false},
		{`{"` + `\u0073essions":[` + one + `]}`, false},
		{`{"sessions":null}`, false},
		{`{"sessions":{}}`, false},
		{`{"machine_id":"m"}`, false},
		{`{"sessions":[` + one + `]} {}`, false},
		{`{"sessions":[` + one + `]`, false},
		{`[` + one + `]`, false},
		{``, false},
	} {
		var want sessionsEvent
		wantErr := json.Unmarshal([]byte(tc.data), &want)
		var d SessionListDecoder
		for round := range 2 { // fresh, then from the memo
			got, fast, err := decodeEvent(&d, []byte(tc.data))
			if fast != tc.fast {
				t.Fatalf("%s: fast %v, want %v", tc.data, fast, tc.fast)
			}
			if (err != nil) != (wantErr != nil) || err == nil && !reflect.DeepEqual(got, want) {
				t.Fatalf("%s (round %d):\n got %+v, %v\nwant %+v, %v", tc.data, round, got, err, want, wantErr)
			}
		}
	}
}

// A random sequence of lists, each a small change of the last, decodes through one decoder as json.Unmarshal decodes
// each: rows changed, added, removed, reordered, listed twice, null, of a wrong type, the outer object's members in
// another case or twice. What a caller does to a list it got (MachineID and Rev, as the hub and the manager write
// them) never reaches a later one.
func TestSessionListDecoderMatchesUnmarshal(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var d SessionListDecoder
	rows := []string{}
	for i := range 8 {
		rows = append(rows, sessionRow(rng, i))
	}
	for step := range 400 {
		switch op := rng.IntN(10); {
		case op < 4 && len(rows) > 0:
			rows[rng.IntN(len(rows))] = sessionRow(rng, rng.IntN(12))
		case op == 4:
			rows = append(rows, sessionRow(rng, rng.IntN(12)))
		case op == 5 && len(rows) > 0:
			i := rng.IntN(len(rows))
			rows = append(rows[:i], rows[i+1:]...)
		case op == 6:
			rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		case op == 7 && len(rows) > 0:
			rows = append(rows, rows[rng.IntN(len(rows))]) // listed twice
		}
		list := slicesClone(rows)
		switch rng.IntN(12) {
		case 0:
			list = append(list, "null")
		case 1:
			list = append(list, `{"session_id":"bad","rev":"x"}`)
		case 2:
			list = append(list, `"x"`)
		}
		key := []string{`"sessions"`, `"sessions"`, `"sessions"`, `"Sessions"`}[rng.IntN(4)]
		data := `{"machine_id":"box",` + key + `:[` + strings.Join(list, ",") + `]`
		if rng.IntN(10) == 0 {
			data += `,"sessions":[]`
		}
		data += `}`
		var want sessionsEvent
		wantErr := json.Unmarshal([]byte(data), &want)
		got, _, err := decodeEvent(&d, []byte(data))
		if (err != nil) != (wantErr != nil) || err == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("step %d %s:\n got %+v, %v\nwant %+v, %v", step, data, got, err, want, wantErr)
		}
		for i := range got.Sessions { // as the callers write them on the list they got
			got.Sessions[i].MachineID, got.Sessions[i].Rev = "changed", -1
		}
	}
}

// sessionRow is a session's bytes as a manager sends them: one of a few ids, in one of a few versions.
func sessionRow(rng *rand.Rand, id int) string {
	at := time.Unix(1700000000+int64(rng.IntN(3)), 0)
	s := Session{SessionID: fmt.Sprintf("s%02d", id), Title: []string{"one", "<b>two</b>", "\u2028 three"}[rng.IntN(3)],
		Status: StatusIdle, Rev: int64(rng.IntN(3)), UpdatedAt: at, StartedByPersona: rng.IntN(2) == 0}
	if rng.IntN(3) == 0 {
		s.ActivityAt, s.Queued = &at, []string{"next"}
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// oneChangedLists are a busy machine's list (sessionListJSON) and the same list with one session's text changed.
func oneChangedLists(tb testing.TB) (before, after []byte) {
	before = sessionListJSON(tb)
	after = bytes.Replace(before, []byte(strings.Repeat("x", 600)), []byte(strings.Repeat("y", 600)), 1)
	if bytes.Equal(before, after) {
		tb.Fatal("no session changed")
	}
	return before, after
}

// BenchmarkDecodeSessionListOneChanged decodes a busy machine's list with one session changed since the last, as
// the hub and the manager do on every change (UnmarshalSessionList decodes it whole).
func BenchmarkDecodeSessionListOneChanged(b *testing.B) {
	before, after := oneChangedLists(b)
	var d SessionListDecoder
	b.SetBytes(int64(len(after)))
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		data := before
		if n++; n%2 == 0 {
			data = after
		}
		_, rows, ok := ScanSessionList(data)
		if list, _, err := d.Decode(rows); !ok || err != nil || len(list) != 150 {
			b.Fatal(ok, err)
		}
	}
}

// A list with one session changed costs its scan, the list it decodes into and that one session: 14 allocations
// measured (237 KB, most of it the 150 sessions of the list itself), 517 (900 KB) decoded whole.
func TestSessionListOneChangedAllocations(t *testing.T) {
	before, after := oneChangedLists(t)
	var d SessionListDecoder
	n := 0
	decode := func() {
		data := before
		if n++; n%2 == 0 {
			data = after
		}
		_, rows, ok := ScanSessionList(data)
		if list, _, err := d.Decode(rows); !ok || err != nil || len(list) != 150 {
			t.Fatal(ok, err)
		}
	}
	decode()
	if allocs := testing.AllocsPerRun(20, decode); allocs > 15 && !raceEnabled {
		t.Fatalf("decoding a list with one session changed allocates %v times", allocs)
	}
}

// raceEnabled: built with -race, whose instrumentation allocates on its own (race_test.go).
var raceEnabled bool

// AppendSession's bytes are what the hub's json.Encoder wrote for a session (without its newline): HTML, U+2028 and
// bad UTF-8 escaped the same way. A time json refuses is left to json (false).
func TestAppendSessionMatchesEncoder(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, text := range wireStrings {
		for _, at := range wireTimes {
			s := Session{SessionID: "s1", Title: text, LastText: text, Queued: []string{text}, UpdatedAt: at,
				Background: []BackgroundTask{{ID: "t1", Description: text, StartedAt: at}}}
			buf.Reset()
			wantErr := enc.Encode(&s)
			got, ok := AppendSession(nil, &s)
			want := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
			if !ok && wantErr == nil || ok && (wantErr != nil || !bytes.Equal(got, want)) {
				t.Fatalf("%q at %v:\n got %s, %v\nwant %s, %v", text, at, got, ok, want, wantErr)
			}
		}
	}
}
