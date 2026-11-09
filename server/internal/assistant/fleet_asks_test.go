package assistant

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// A list decorated with what its sessions asked in the lists before is the list decorated afresh, for the same inputs,
// as sessions' last words change, appear and go and their node starts reading them; only changed words are read
// again. The list told the hub is json.Marshal's, and an unchanged one is not told again.
func TestDecoratedListsAnswerAsAfresh(t *testing.T) {
	f := newFakeHub()
	f.hello.Assistant.MachineID = "box"
	f.hello.Assistant.Machines = []string{"box"}
	f.devices = []Device{{ID: "box", Home: "/home/u"}}
	d := newFleetDevs()
	d.add("box", "Computer")
	st := newFleetMem()
	s := New(Options{Hub: f, Devices: d, Store: func() Store { return st }})
	reads := 0
	s.fleet.asks.askOf = func(text string) string { reads++; return transcript.AskOf(text) }

	row := func(id, text string) proto.Session {
		return proto.Session{SessionID: id, Cwd: "/code/example", Agent: "claude", Status: proto.StatusDone, LastText: text}
	}
	read := func(sess proto.Session, asks string) proto.Session {
		sess.AsksRead, sess.Asks = true, asks
		return sess
	}
	first := []proto.Session{row("a", "改好了。要合并吗？"), row("b", "Tests pass. Should I push it?"), row("c", "Done."),
		row("cut", "要合并吗？还有很多…"), read(row("node", "要合并吗？"), ""), read(row("asked", "Merge?"), "Merge?"), row("empty", ""),
		row("dup", "Which one do you want?"), row("dup", "All done.")}
	changed := slices.Clone(first)
	changed[0].LastText, changed[2].LastText = "改好了。", "Done. Want me to open the PR?"
	gone := slices.Delete(slices.Clone(changed), 1, 2)
	back := append(slices.Clone(gone), first[1]) // the same words: read again, as it was forgotten
	nodeReads := slices.Clone(back)
	nodeReads[0] = read(nodeReads[0], "")
	lists := func() (out [][]byte) { // the lists told the hub
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, t := range f.told {
			if t.event == assistantwire.EventFleetSessions {
				out = append(out, t.params)
			}
		}
		return out
	}
	var last []byte // the list told last
	for _, c := range []struct {
		name  string
		list  []proto.Session
		reads int // AskOf runs for the list, the memo kept from the lists before
	}{
		{"first list", first, 5},    // a, b, c and dup twice
		{"the same list", first, 2}, // dup's two rows take turns
		{"two sessions' words change", changed, 4},
		{"a session goes", gone, 2},
		{"it comes back", back, 3},
		{"its node reads one now", nodeReads, 2},
	} {
		d.setSessions("box", slices.Clone(c.list))
		reads = 0
		got := s.decoratedUnarchived("box")
		if reads != c.reads {
			t.Errorf("%s: AskOf ran %d times, want %d", c.name, reads, c.reads)
		}
		kept := swapAsks(s, nil) // afresh
		want := s.decoratedUnarchived("box")
		swapAsks(s, kept)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: decorated with the memo\n%+v\nafresh\n%+v", c.name, got, want)
		}
		if c.name == "first list" && want[0].Asks != "要合并吗？" {
			t.Fatalf("a asks %q", want[0].Asks)
		}

		before := len(lists())
		list := s.publishList("box")
		wantTold, _ := json.Marshal(assistantwire.FleetSessions{MachineID: "box", Sessions: list, Muted: s.mutedSessions("box", list)})
		told := lists()
		if n := len(told); bytes.Equal(wantTold, last) && n != before || !bytes.Equal(wantTold, last) &&
			(n != before+1 || !bytes.Equal(told[n-1], wantTold)) {
			t.Fatalf("%s: told %d lists, not json.Marshal's once it changed", c.name, n-before)
		}
		last = wantTold
		if s.publishList("box"); len(lists()) != len(told) {
			t.Fatalf("%s: an unchanged list was told again", c.name)
		}
	}
}

// swapAsks puts machines in s's memo of what sessions ask, and returns the ones it had.
func swapAsks(s *Service, machines map[string]*askBook) map[string]*askBook {
	s.fleet.asks.mu.Lock()
	defer s.fleet.asks.mu.Unlock()
	kept := s.fleet.asks.machines
	s.fleet.asks.machines = machines
	return kept
}

// changedWriter keeps nothing while what comes is old's, and everything once it differs.
func TestChangedWriter(t *testing.T) {
	for _, c := range []struct {
		name, old string
		parts     []string
		same      bool
	}{
		{"the same", `{"a":1}`, []string{`{"a"`, `:1}`}, true},
		{"nothing before", "", []string{`{}`}, false},
		{"shorter", `{"a":12}`, []string{`{"a":1`}, false},
		{"longer", `{"a":1}`, []string{`{"a":1}`, `x`}, false},
		{"differs inside a part", `{"a":1,"b":2}`, []string{`{"a":1,`, `"b":3}`}, false},
	} {
		w := changedWriter{old: []byte(c.old)}
		for _, p := range c.parts {
			if n, err := w.Write([]byte(p)); n != len(p) || err != nil {
				t.Fatal(n, err)
			}
		}
		all := []byte("")
		for _, p := range c.parts {
			all = append(all, p...)
		}
		if same := w.same(); same != c.same || !same && !bytes.Equal(w.written(), all) {
			t.Errorf("%s: same %v, written %q", c.name, same, w.written())
		}
	}
}
