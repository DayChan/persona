package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The shared chat sync scenarios (spec/fixtures/sync, #1078) checked as files: their structure, every frame decoding
// strictly into the hub's types, frames carrying only what the hub held, expectations that keep the invariants, and
// each case's end equal to the view computed from the hub's final state. No client is involved; the clients replay
// the same files against their own code.

type syncFile struct {
	Version int        `json:"version"`
	Cases   []syncCase `json:"cases"`
}

type syncCase struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Source      map[string]any `json:"source"`
	Chat        struct {
		MachineID string `json:"machine_id"`
		SessionID string `json:"session_id"`
		Assistant bool   `json:"assistant"`
		New       bool   `json:"new,omitempty"` // the chat is the one a user.start starts: the hub has no session for it yet
	} `json:"chat"`
	Devices []string `json:"devices"`
	Start   struct {
		Hub syncHub `json:"hub"`
	} `json:"start"`
	Steps []syncStep          `json:"steps"`
	End   map[string]syncView `json:"end"`
}

type syncHub struct {
	History      string        `json:"history"`
	Lines        []Item        `json:"lines"`
	ToSeq        int           `json:"to_seq"`
	Interactions []Interaction `json:"interactions"`
	Session      syncSession   `json:"session"`
	EventStream  string        `json:"event_stream"`
	EventSeq     int64         `json:"event_seq"`
	PageTurns    int           `json:"page_turns,omitempty"`
}

type syncSession struct {
	State  string   `json:"state,omitempty"`
	Queued []string `json:"queued,omitempty"`
}

type syncStep struct {
	Hub     *syncHubChange               `json:"hub,omitempty"`
	Frames  map[string][]json.RawMessage `json:"frames,omitempty"`
	Socket  *syncSocket                  `json:"socket,omitempty"`
	User    *syncUser                    `json:"user,omitempty"`
	Reply   *syncReply                   `json:"reply,omitempty"`
	Wait    *int                         `json:"wait,omitempty"`
	Hold    *syncRead                    `json:"hold,omitempty"`
	Release *syncRead                    `json:"release,omitempty"`
	Expect  map[string]syncView          `json:"expect,omitempty"`
}

type syncHubChange struct {
	Restart *struct {
		Resumable bool `json:"resumable"`
	} `json:"restart,omitempty"`
	Rewrite *struct {
		History string `json:"history"`
		Lines   []Item `json:"lines"`
		ToSeq   int    `json:"to_seq"`
	} `json:"rewrite,omitempty"`
	Append []Item `json:"append,omitempty"`
	ToSeq  *int   `json:"to_seq,omitempty"`
	End    *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		By     string `json:"by"`
	} `json:"end,omitempty"`
	Raise   *Interaction `json:"raise,omitempty"`
	Session *syncSession `json:"session,omitempty"`
}

type syncSocket struct {
	Device string `json:"device"`
	Drop   bool   `json:"drop,omitempty"`
	Open   *struct {
		Resumed bool `json:"resumed"`
	} `json:"open,omitempty"`
}

type syncUser struct {
	Device string `json:"device"`
	Send   *struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"send,omitempty"`
	Start *struct { // a new conversation's first message (POST /assistant/conversations); id is its op id
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"start,omitempty"`
	Answer *struct {
		ID         string     `json:"id"`
		Resolution Resolution `json:"resolution"`
	} `json:"answer,omitempty"`
	Retry      string `json:"retry,omitempty"`
	Discard    string `json:"discard,omitempty"`
	Open       bool   `json:"open,omitempty"`
	Close      bool   `json:"close,omitempty"`
	Background bool   `json:"background,omitempty"`
	Foreground bool   `json:"foreground,omitempty"`
	Relaunch   bool   `json:"relaunch,omitempty"`
}

type syncReply struct {
	Device  string          `json:"device"`
	Write   string          `json:"write"`
	Status  int             `json:"status,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
	Cut     bool            `json:"cut,omitempty"`
	Done    *bool           `json:"done,omitempty"`
	Receipt *struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"receipt,omitempty"`
}

type syncRead struct {
	Device string `json:"device"`
	Get    string `json:"get"`
}

type syncView struct {
	Messages *[]syncMessage     `json:"messages,omitempty"`
	Cards    *map[string]string `json:"cards,omitempty"`
	Status   *string            `json:"status,omitempty"`
	Live     syncLive           `json:"live"`
	Requests []syncRequest      `json:"requests,omitempty"`
}

type syncMessage struct {
	From  string `json:"from"`
	Text  string `json:"text"`
	State string `json:"state,omitempty"`
}

// syncLive tells "not listed" from null.
type syncLive struct {
	Set  bool
	Text *string
}

func (l *syncLive) UnmarshalJSON(b []byte) error {
	l.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	return json.Unmarshal(b, &l.Text)
}

type syncRequest struct {
	Get      string `json:"get,omitempty"`
	AfterSeq *int   `json:"after_seq,omitempty"`
	WS       string `json:"ws,omitempty"`
	SinceSeq *int   `json:"since_seq,omitempty"`
	Post     string `json:"post,omitempty"`
	ID       string `json:"id,omitempty"`
	NewOp    bool   `json:"new_op,omitempty"`
}

// The frames a step may deliver, as the hub writes them (hub/http.go, hub/elicitation.go, hub/sessions_push.go).
type syncItemsFrame struct {
	Type      string `json:"type"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Items     []Item `json:"items"`
	History   string `json:"history"`
	FromSeq   *int   `json:"from_seq,omitempty"`
	ToSeq     int    `json:"to_seq"`
	Backlog   bool   `json:"backlog,omitempty"`
	HasMore   *bool  `json:"has_more,omitempty"`
	MoreAfter bool   `json:"more_after,omitempty"`
}

type syncInteractionFrame struct {
	Type        string      `json:"type"`
	Interaction Interaction `json:"interaction"`
	EventSeq    *int64      `json:"event_seq,omitempty"`
}

type syncResolvedFrame struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	EventSeq   *int64            `json:"event_seq,omitempty"`
	AnsweredBy string            `json:"answered_by,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Answers    map[string]string `json:"answers,omitempty"`
}

type syncSessionsDeltaFrame struct {
	Type      string    `json:"type"`
	MachineID string    `json:"machine_id"`
	Upsert    []Session `json:"upsert"`
	Remove    []string  `json:"remove"`
}

type syncDeltaFrame struct {
	Type      string `json:"type"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type syncRestartingFrame struct {
	Type     string `json:"type"`
	BackInMS int    `json:"back_in_ms"`
}

func strictDecode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

var syncName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestSyncScenarios(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(wd, "../../../spec/fixtures/sync")
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no sync scenarios in %s: %v", dir, err)
	}
	names := map[string]string{}
	total := 0
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f syncFile
		if err := strictDecode(b, &f); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		if f.Version != 1 || len(f.Cases) == 0 {
			t.Fatalf("%s: version %d with %d cases", filepath.Base(path), f.Version, len(f.Cases))
		}
		for _, c := range f.Cases {
			if prev, ok := names[c.Name]; ok {
				t.Errorf("%s: %s is also in %s", filepath.Base(path), c.Name, prev)
			}
			names[c.Name] = filepath.Base(path)
			total++
			t.Run(c.Name, func(t *testing.T) { checkSyncCase(t, c) })
		}
	}
	t.Logf("%d cases in %d files", total, len(paths))
}

// ── the hub's state, step by step ──

type syncEvent struct {
	seq  int64
	kind string // raise | end
	id   string
}

type syncTruth struct {
	history  string
	lines    []Item
	toSeq    int
	pending  map[string]Interaction
	order    []string
	raised   map[string]Interaction // the last raise of each id
	raisedAt map[string]int64       // its broadcast's number (0: in the start state)
	ended    map[string]int64       // the end's broadcast number
	state    string
	states   map[string]bool // every state the session has had
	stream   string
	seq      int64
	events   []syncEvent
	seen     map[string]Item // every item the hub held, by history and id
	maxTo    map[string]int  // the furthest line read, by history
	started  bool            // the chat exists on the hub (always, unless chat.new and no start was carried out yet)
}

func (h *syncTruth) note(items []Item) {
	for _, it := range items {
		h.seen[h.history+"\x00"+it.ID] = it
	}
	if h.toSeq > h.maxTo[h.history] {
		h.maxTo[h.history] = h.toSeq
	}
}

func (h *syncTruth) raise(ix Interaction, broadcast bool) {
	if _, ok := h.pending[ix.ID]; !ok {
		h.order = append(h.order, ix.ID)
	}
	h.pending[ix.ID], h.raised[ix.ID] = ix, ix
	delete(h.ended, ix.ID)
	if broadcast {
		h.seq++
		h.raisedAt[ix.ID] = h.seq
		h.events = append(h.events, syncEvent{h.seq, "raise", ix.ID})
	}
}

// ── what each device has been told and has done ──

type syncSend struct {
	id, text  string
	atToSeq   int    // the lines the hub had read when it was sent
	outcome   string // inflight | accepted | refused
	status    int    // the last answer's status
	receipt   bool   // a retry gets the hub's receipt
	discarded bool
	start     bool // the first message of a new conversation (user.start)
}

type syncDeviceState struct {
	stream  string
	seq     int64
	online  bool
	sends   []*syncSend
	answers map[string]string // answering | ok | failed
	ended   map[string]bool   // cards this device knows are over
	holds   map[string]bool
}

func (d *syncDeviceState) send(id string) *syncSend {
	for _, s := range d.sends {
		if s.id == id {
			return s
		}
	}
	return nil
}

func checkSyncCase(t *testing.T, c syncCase) {
	if !syncName.MatchString(c.Name) || strings.TrimSpace(c.Description) == "" || len(c.Source) == 0 {
		t.Fatalf("needs a snake_case name, a description and a source")
	}
	if c.Chat.MachineID == "" || c.Chat.SessionID == "" || len(c.Devices) == 0 || len(c.Steps) == 0 {
		t.Fatalf("needs chat ids, devices and steps")
	}
	start := c.Start.Hub
	if start.History == "" || start.EventStream == "" || validState(start.Session.State) == c.Chat.New {
		t.Fatalf("start.hub needs history, event_stream and a session state (none for a new chat)")
	}
	if c.Chat.New && (len(start.Lines) > 0 || start.ToSeq != 0 || len(start.Interactions) > 0 || len(start.Session.Queued) > 0) {
		t.Fatalf("a new chat starts with nothing on the hub")
	}
	h := &syncTruth{history: start.History, lines: append([]Item(nil), start.Lines...), toSeq: start.ToSeq,
		pending: map[string]Interaction{}, raised: map[string]Interaction{}, raisedAt: map[string]int64{}, ended: map[string]int64{},
		state: start.Session.State, states: map[string]bool{}, stream: start.EventStream, seq: start.EventSeq,
		seen: map[string]Item{}, maxTo: map[string]int{}, started: !c.Chat.New}
	if h.started {
		h.states[h.state] = true
	}
	checkLines(t, "start", h.lines, h.toSeq)
	h.note(h.lines)
	for _, ix := range start.Interactions {
		checkCard(t, "start", c, ix)
		h.raise(ix, false)
	}
	devs := map[string]*syncDeviceState{}
	for _, name := range c.Devices {
		if !syncName.MatchString(name) || devs[name] != nil {
			t.Fatalf("device %q", name)
		}
		devs[name] = &syncDeviceState{stream: h.stream, seq: h.seq, online: true, answers: map[string]string{}, ended: map[string]bool{}, holds: map[string]bool{}}
	}
	dev := func(where, name string) *syncDeviceState {
		d := devs[name]
		if d == nil {
			t.Fatalf("%s: no device %q", where, name)
		}
		return d
	}

	for i, st := range c.Steps {
		where := fmt.Sprintf("step %d", i+1)
		actions := 0
		for _, set := range []bool{st.Hub != nil, st.Frames != nil, st.Socket != nil, st.User != nil, st.Reply != nil, st.Wait != nil, st.Hold != nil, st.Release != nil} {
			if set {
				actions++
			}
		}
		opening := st.Socket != nil && st.Socket.Open != nil
		if actions != 1 && !(actions == 2 && opening && st.Frames != nil) {
			t.Fatalf("%s: one action per step (an opening socket may carry frames), got %d", where, actions)
		}
		switch {
		case st.Hub != nil:
			applyHubChange(t, where, c, h, st.Hub)
		case st.Socket != nil:
			d := dev(where, st.Socket.Device)
			if st.Socket.Drop == opening {
				t.Fatalf("%s: a socket step either drops or opens", where)
			}
			if st.Socket.Drop {
				d.online = false
				break
			}
			if d.online {
				t.Fatalf("%s: %s opens a socket while connected", where, st.Socket.Device)
			}
			d.online = true
			if st.Socket.Open.Resumed {
				if d.stream != h.stream || h.seq-d.seq > 400 {
					t.Fatalf("%s: resumed is only possible on the stream %s last saw, within 400 events", where, st.Socket.Device)
				}
				var missed, replayed []int64
				for _, e := range h.events {
					if e.seq > d.seq {
						missed = append(missed, e.seq)
					}
				}
				for _, raw := range st.Frames[st.Socket.Device] {
					if n := frameEvent(raw); n > d.seq {
						replayed = append(replayed, n)
					}
				}
				if !reflect.DeepEqual(missed, replayed) {
					t.Fatalf("%s: a resumed hello replays every missed broadcast in order: missed %v, frames %v", where, missed, replayed)
				}
			} else {
				for id := range h.ended { // the full refresh reads the card list
					d.ended[id] = true
				}
			}
			// the pending-card snapshot every hello is followed by: each pending card once, without event_seq,
			// after the replayed broadcasts
			var snapshot []string
			replays := true
			for _, raw := range st.Frames[st.Socket.Device] {
				var f syncInteractionFrame
				if json.Unmarshal(raw, &f) != nil || f.Type != "interaction" {
					continue
				}
				if f.EventSeq != nil {
					if !replays {
						t.Fatalf("%s: a replayed broadcast after the pending-card snapshot", where)
					}
					continue
				}
				replays = false
				snapshot = append(snapshot, f.Interaction.ID)
			}
			want, listed := []string{}, map[string]bool{}
			for _, id := range h.order {
				if _, ok := h.pending[id]; ok && !listed[id] {
					want, listed[id] = append(want, id), true
				}
			}
			if snapshot == nil {
				snapshot = []string{}
			}
			if !reflect.DeepEqual(snapshot, want) {
				t.Fatalf("%s: a hello is followed by every pending card %v, the step's frames have %v", where, want, snapshot)
			}
			d.stream, d.seq = h.stream, h.seq
		case st.User != nil:
			d := dev(where, st.User.Device)
			u := st.User
			switch {
			case u.Start != nil:
				if !c.Chat.New || u.Start.ID == "" || strings.TrimSpace(u.Start.Text) == "" || d.send(u.Start.ID) != nil || slicesAny(d.sends, func(s *syncSend) bool { return s.start }) {
					t.Fatalf("%s: a start is one new id and words, once, in a new chat", where)
				}
				d.sends = append(d.sends, &syncSend{id: u.Start.ID, text: u.Start.Text, atToSeq: h.toSeq, outcome: "inflight", start: true})
			case u.Send != nil:
				if u.Send.ID == "" || strings.TrimSpace(u.Send.Text) == "" || d.send(u.Send.ID) != nil {
					t.Fatalf("%s: a send needs a new id and words", where)
				}
				if c.Chat.New && !slicesAny(d.sends, func(s *syncSend) bool { return s.start && (s.outcome == "accepted" || s.receipt) }) {
					t.Fatalf("%s: a send in a new chat comes after this device's start was carried out and answered (or has its receipt)", where)
				}
				d.sends = append(d.sends, &syncSend{id: u.Send.ID, text: u.Send.Text, atToSeq: h.toSeq, outcome: "inflight"})
			case u.Answer != nil:
				if _, ok := h.raised[u.Answer.ID]; !ok || d.ended[u.Answer.ID] || d.answers[u.Answer.ID] == "answering" {
					t.Fatalf("%s: %s answers %s, which it cannot see as open", where, u.Device, u.Answer.ID)
				}
				d.answers[u.Answer.ID] = "answering"
			case u.Retry != "":
				s := d.send(u.Retry)
				if s == nil || s.outcome != "refused" {
					t.Fatalf("%s: retry of %s, which did not fail", where, u.Retry)
				}
				s.outcome, s.receipt = "inflight", false
			case u.Discard != "":
				s := d.send(u.Discard)
				if s == nil || s.outcome == "inflight" {
					t.Fatalf("%s: discard of %s, which is not settled", where, u.Discard)
				}
				s.discarded = true
			case u.Open || u.Close || u.Background || u.Foreground || u.Relaunch:
			default:
				t.Fatalf("%s: a user step needs one action", where)
			}
		case st.Reply != nil:
			applyReply(t, where, c, h, dev(where, st.Reply.Device), st.Reply)
		case st.Wait != nil:
			if *st.Wait <= 0 {
				t.Fatalf("%s: wait needs milliseconds", where)
			}
		case st.Hold != nil:
			d := dev(where, st.Hold.Device)
			if !syncReadPath(st.Hold.Get) || d.holds[st.Hold.Get] {
				t.Fatalf("%s: hold of %q", where, st.Hold.Get)
			}
			d.holds[st.Hold.Get] = true
		case st.Release != nil:
			d := dev(where, st.Release.Device)
			if !d.holds[st.Release.Get] {
				t.Fatalf("%s: release of %q, which was not held", where, st.Release.Get)
			}
			delete(d.holds, st.Release.Get)
		}
		for name, frames := range st.Frames {
			d := dev(where, name)
			for k, raw := range frames {
				deliverFrame(t, fmt.Sprintf("%s, %s's frame %d", where, name, k+1), c, h, d, raw, opening)
			}
		}
		for name, v := range st.Expect {
			checkView(t, where+", expect "+name, dev(where, name), v, false)
		}
	}

	for name, d := range devs {
		if len(d.holds) > 0 {
			t.Errorf("%s still holds reads at the end", name)
		}
		for _, s := range d.sends {
			if s.outcome == "inflight" && !s.receipt {
				t.Errorf("%s's send %s is still waiting for an answer at the end", name, s.id)
			}
		}
		for id, a := range d.answers {
			if a == "answering" {
				t.Errorf("%s's answer to %s is still waiting at the end", name, id)
			}
		}
		v, ok := c.End[name]
		if !ok || v.Messages == nil || v.Cards == nil {
			t.Fatalf("end needs messages and cards for %s", name)
		}
		checkView(t, "end "+name, d, v, true)
		want := truthMessages(h, d)
		if !reflect.DeepEqual(*v.Messages, want) {
			t.Errorf("end %s: messages differ from the hub's final state\n got  %s\n want %s", name, jsonString(*v.Messages), jsonString(want))
		}
		open := []string{}
		for id, state := range *v.Cards {
			switch state {
			case "open":
				open = append(open, id)
			case "answering":
				t.Errorf("end %s: %s is still answering", name, id)
			}
		}
		sort.Strings(open)
		pending := []string{}
		for id := range h.pending {
			pending = append(pending, id)
		}
		sort.Strings(pending)
		if !reflect.DeepEqual(open, pending) {
			t.Errorf("end %s: open cards %v, the hub has %v pending", name, open, pending)
		}
		if v.Status != nil && *v.Status != h.state {
			t.Errorf("end %s: status %s, the hub says %s", name, *v.Status, h.state)
		}
		if v.Live.Set && v.Live.Text != nil {
			t.Errorf("end %s: nothing streams once everything settled", name)
		}
	}
	for name := range c.End {
		if devs[name] == nil {
			t.Errorf("end lists %q, which is not a device", name)
		}
	}
}

func validState(s string) bool {
	return s == "working" || s == "needs_you" || s == "unread" || s == "idle"
}

func syncReadPath(p string) bool { return p == "/interactions" || p == "/sessions" || p == "/items" }

func checkLines(t *testing.T, where string, lines []Item, toSeq int) {
	last := 0
	for _, it := range lines {
		if it.ID == "" || it.Seq <= 0 || it.Seq < last || it.Role == "" || it.Kind == "" || it.TS.IsZero() {
			t.Fatalf("%s: line %q needs an id, a seq in order, a role, a kind and a time", where, it.ID)
		}
		last = it.Seq
	}
	if toSeq < last {
		t.Fatalf("%s: to_seq %d is before the last line %d", where, toSeq, last)
	}
}

func checkCard(t *testing.T, where string, c syncCase, ix Interaction) {
	if ix.ID == "" || ix.MachineID != c.Chat.MachineID || ix.SessionID != c.Chat.SessionID || ix.Kind == "" || ix.CreatedAt.IsZero() || ix.ExpiresAt.IsZero() {
		t.Fatalf("%s: card %q needs the chat's ids, a kind and its times", where, ix.ID)
	}
}

func applyHubChange(t *testing.T, where string, c syncCase, h *syncTruth, ch *syncHubChange) {
	if !h.started && (ch.Rewrite != nil || len(ch.Append) > 0 || ch.ToSeq != nil || ch.End != nil || ch.Raise != nil || ch.Session != nil) {
		t.Fatalf("%s: the chat's state changes before the hub started it", where)
	}
	if ch.Restart != nil {
		if !ch.Restart.Resumable {
			h.stream += "+"
		}
	}
	if ch.Rewrite != nil {
		if ch.Rewrite.History == "" || ch.Rewrite.History == h.history {
			t.Fatalf("%s: a rewrite has a new history", where)
		}
		h.history, h.lines, h.toSeq = ch.Rewrite.History, append([]Item(nil), ch.Rewrite.Lines...), ch.Rewrite.ToSeq
		checkLines(t, where, h.lines, h.toSeq)
		h.note(h.lines)
	}
	if len(ch.Append) > 0 {
		last := h.toSeq
		for _, it := range ch.Append {
			if it.Seq <= last {
				t.Fatalf("%s: appended line %q at seq %d, the hub had read up to %d", where, it.ID, it.Seq, h.toSeq)
			}
			last = it.Seq
		}
		h.lines = append(h.lines, ch.Append...)
		if last > h.toSeq {
			h.toSeq = last
		}
		if ch.ToSeq == nil {
			t.Fatalf("%s: append needs to_seq", where)
		}
	}
	if ch.ToSeq != nil {
		if *ch.ToSeq < h.toSeq || (len(ch.Append) == 0 && *ch.ToSeq == h.toSeq) {
			t.Fatalf("%s: to_seq %d does not move on from %d", where, *ch.ToSeq, h.toSeq)
		}
		h.toSeq = *ch.ToSeq
	}
	checkLines(t, where, h.lines, h.toSeq)
	h.note(h.lines)
	if ch.End != nil {
		if _, ok := h.pending[ch.End.ID]; !ok {
			t.Fatalf("%s: end of %s, which is not pending", where, ch.End.ID)
		}
		switch ch.End.Status {
		case "answered", "dismissed", "cancelled", "expired":
		default:
			t.Fatalf("%s: end status %q", where, ch.End.Status)
		}
		if ch.End.By == "" {
			t.Fatalf("%s: end needs by", where)
		}
		delete(h.pending, ch.End.ID)
		h.seq++
		h.ended[ch.End.ID] = h.seq
		h.events = append(h.events, syncEvent{h.seq, "end", ch.End.ID})
	}
	if ch.Raise != nil {
		checkCard(t, where, c, *ch.Raise)
		if _, ok := h.pending[ch.Raise.ID]; ok {
			t.Fatalf("%s: %s is pending already", where, ch.Raise.ID)
		}
		h.raise(*ch.Raise, true)
	}
	if ch.Session != nil {
		if ch.Session.State != "" {
			if !validState(ch.Session.State) {
				t.Fatalf("%s: session state %q", where, ch.Session.State)
			}
			h.state = ch.Session.State
			h.states[h.state] = true
		}
	}
}

func applyReply(t *testing.T, where string, c syncCase, h *syncTruth, d *syncDeviceState, r *syncReply) {
	if r.Cut == (r.Status != 0) {
		t.Fatalf("%s: a reply has a status or is cut", where)
	}
	done := r.Status >= 200 && r.Status < 300
	if r.Done != nil {
		done = *r.Done
	}
	if r.Cut && r.Done == nil {
		t.Fatalf("%s: a cut reply says whether the hub carried the write out", where)
	}
	if r.Receipt != nil && !done {
		t.Fatalf("%s: only a write the hub carried out has a receipt", where)
	}
	if r.Cut && done && r.Receipt == nil {
		t.Fatalf("%s: a cut write the hub carried out needs the receipt its retries get", where)
	}
	status := r.Status
	if r.Cut && r.Receipt != nil {
		status = r.Receipt.Status
	}
	if s := d.send(r.Write); s != nil {
		if s.outcome != "inflight" || s.receipt {
			t.Fatalf("%s: reply to %s, which is not in flight", where, r.Write)
		}
		if s.start && done {
			startChat(t, where, c, h, r)
		}
		switch {
		case r.Cut && r.Receipt == nil:
			// the client tries again; the next reply answers it
		case r.Cut:
			s.receipt, s.status = true, status
			s.outcome = outcomeOf(status, done)
		default:
			s.status = status
			s.outcome = outcomeOf(status, done)
		}
		return
	}
	a, ok := d.answers[r.Write]
	if !ok || a != "answering" {
		t.Fatalf("%s: reply to %s, which this device is not writing", where, r.Write)
	}
	if r.Cut && r.Receipt == nil {
		return
	}
	switch {
	case status >= 200 && status < 300, status == 410:
		d.answers[r.Write] = "ok"
		d.ended[r.Write] = true
	default:
		d.answers[r.Write] = "failed"
	}
}

// startChat is the hub carrying out a start: the answer the app gets (or its retries get) is the new session, which
// is the case's chat. One op id starts one conversation; a second carried out start would be another chat.
func startChat(t *testing.T, where string, c syncCase, h *syncTruth, r *syncReply) {
	if h.started {
		t.Fatalf("%s: the hub carries out a start, but the chat exists already", where)
	}
	body := r.Body
	if r.Receipt != nil {
		body = r.Receipt.Body
	}
	var answer struct {
		Session Session `json:"session"`
	}
	if err := strictDecode(body, &answer); err != nil {
		t.Fatalf("%s: a carried out start answers {\"session\"}: %v", where, err)
	}
	if answer.Session.MachineID != c.Chat.MachineID || answer.Session.SessionID != c.Chat.SessionID || !validState(answer.Session.State) {
		t.Fatalf("%s: the started session is the case's chat, with a state", where)
	}
	h.started, h.state = true, answer.Session.State
	h.states[h.state] = true
}

func slicesAny(sends []*syncSend, f func(*syncSend) bool) bool {
	for _, s := range sends {
		if f(s) {
			return true
		}
	}
	return false
}

func outcomeOf(status int, done bool) string {
	if status >= 200 && status < 300 && done {
		return "accepted"
	}
	if done {
		return "unsure" // carried out, but the client was told it failed (delivered unknown)
	}
	return "refused"
}

func frameEvent(raw json.RawMessage) int64 {
	var e struct {
		EventSeq int64 `json:"event_seq"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.EventSeq
}

func deliverFrame(t *testing.T, where string, c syncCase, h *syncTruth, d *syncDeviceState, raw json.RawMessage, opening bool) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	if !d.online {
		t.Fatalf("%s: delivered while %s is disconnected", where, head.Type)
	}
	if !h.started && head.Type != "hub_restarting" {
		t.Fatalf("%s: %s for a chat the hub has not started", where, head.Type)
	}
	switch head.Type {
	case "items":
		var f syncItemsFrame
		if err := strictDecode(raw, &f); err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		if f.MachineID != c.Chat.MachineID || f.SessionID != c.Chat.SessionID {
			t.Fatalf("%s: items for another chat", where)
		}
		if f.ToSeq > h.maxTo[f.History] {
			t.Fatalf("%s: to_seq %d, the hub read %d lines of %q", where, f.ToSeq, h.maxTo[f.History], f.History)
		}
		for _, it := range f.Items {
			held, ok := h.seen[f.History+"\x00"+it.ID]
			if !ok || !sameJSON(held, it) {
				t.Fatalf("%s: item %s is not what the hub held", where, it.ID)
			}
			if (f.FromSeq != nil && it.Seq <= *f.FromSeq) || it.Seq > f.ToSeq {
				t.Fatalf("%s: item %s at %d is outside the batch's span", where, it.ID, it.Seq)
			}
		}
	case "interaction":
		var f syncInteractionFrame
		if err := strictDecode(raw, &f); err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		held, ok := h.raised[f.Interaction.ID]
		if !ok || !sameJSON(held, f.Interaction) {
			t.Fatalf("%s: card %s is not one the hub raised", where, f.Interaction.ID)
		}
		if f.EventSeq == nil {
			if _, pending := h.pending[f.Interaction.ID]; !opening || !pending {
				t.Fatalf("%s: a card without event_seq is the snapshot after a hello, of a pending card", where)
			}
		} else {
			if *f.EventSeq != h.raisedAt[f.Interaction.ID] {
				t.Fatalf("%s: card %s broadcast as %d, raised as %d", where, f.Interaction.ID, *f.EventSeq, h.raisedAt[f.Interaction.ID])
			}
			d.seq = max(d.seq, *f.EventSeq)
		}
	case "interaction_resolved":
		var f syncResolvedFrame
		if err := strictDecode(raw, &f); err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		n, ok := h.ended[f.ID]
		if !ok || f.EventSeq == nil || *f.EventSeq != n {
			t.Fatalf("%s: resolve of %s, which the hub did not end as that broadcast", where, f.ID)
		}
		d.seq = max(d.seq, n)
		d.ended[f.ID] = true
	case "sessions_delta":
		var f syncSessionsDeltaFrame
		if err := strictDecode(raw, &f); err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		for _, s := range f.Upsert {
			if s.MachineID != c.Chat.MachineID || s.SessionID != c.Chat.SessionID || !h.states[s.State] {
				t.Fatalf("%s: a session row the hub never had (%s/%s, %s)", where, s.MachineID, s.SessionID, s.State)
			}
		}
	case "delta":
		var f syncDeltaFrame
		if err := strictDecode(raw, &f); err != nil {
			t.Fatalf("%s: %v", where, err)
		}
	case "hub_restarting":
		var f syncRestartingFrame
		if err := strictDecode(raw, &f); err != nil || f.BackInMS <= 0 {
			t.Fatalf("%s: hub_restarting needs back_in_ms: %v", where, err)
		}
	default:
		t.Fatalf("%s: frame type %q is not one the scenarios know", where, head.Type)
	}
}

func checkView(t *testing.T, where string, d *syncDeviceState, v syncView, end bool) {
	if v.Messages != nil {
		for _, m := range *v.Messages {
			switch {
			case m.From == "user" && (m.State == "pending" || m.State == "failed" || m.State == "shown"):
			case m.From == "agent" && m.State == "":
			default:
				t.Errorf("%s: message %q from %q in state %q", where, m.Text, m.From, m.State)
			}
		}
		// never missing, never twice
		for _, s := range d.sends {
			if s.discarded {
				continue
			}
			n := 0
			for _, m := range *v.Messages {
				if m.From == "user" && m.Text == s.text {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s: sent message %s shows %d times", where, s.id, n)
			}
		}
	}
	if v.Cards != nil {
		for id, state := range *v.Cards {
			switch state {
			case "open":
				if d.ended[id] || d.answers[id] == "answering" || d.answers[id] == "ok" {
					t.Errorf("%s: %s is open again, though this device knows it was answered or is answering it", where, id)
				}
			case "answering":
				if d.answers[id] != "answering" && !end {
					t.Errorf("%s: %s is answering, but this device has no answer in flight", where, id)
				}
			case "answered":
			default:
				t.Errorf("%s: card %s in state %q", where, id, state)
			}
		}
	}
	if v.Status != nil && !validState(*v.Status) {
		t.Errorf("%s: status %q", where, *v.Status)
	}
	for _, r := range v.Requests {
		ok := (r.Get == "/items" && r.WS == "" && r.Post == "") ||
			(r.WS == "subscribe" && r.Get == "" && r.Post == "") ||
			((r.Post == "messages" || r.Post == "resolve" || r.Post == "conversations") && r.Get == "" && r.WS == "" && (r.ID != "") != r.NewOp)
		if !ok {
			t.Errorf("%s: request %s", where, jsonString(r))
		}
	}
	if end && len(v.Requests) > 0 {
		t.Errorf("%s: end lists no requests", where)
	}
}

// truthMessages is what a device shows once everything settled: each turn's user message and its answer (the agent's
// last words in it), the device's failed sends where they were sent, and its accepted sends the transcript does not
// have yet at the bottom.
func truthMessages(h *syncTruth, d *syncDeviceState) []syncMessage {
	type entry struct {
		at  int
		msg syncMessage
	}
	var out []entry
	answer := -1
	for _, it := range h.lines {
		switch {
		case it.Role == "user" && it.Kind == "text" && it.Injected == nil && it.From == "":
			out = append(out, entry{it.Seq, syncMessage{From: "user", Text: it.Text, State: "shown"}})
			answer = -1
		case it.Role == "assistant" && it.Kind == "text":
			if answer >= 0 {
				out[answer] = entry{it.Seq, syncMessage{From: "agent", Text: it.Text}}
			} else {
				out = append(out, entry{it.Seq, syncMessage{From: "agent", Text: it.Text}})
				answer = len(out) - 1
			}
		}
	}
	inTranscript := func(s *syncSend) bool {
		for _, it := range h.lines {
			if it.Role != "user" || it.Kind != "text" {
				continue
			}
			if it.ClientMsgID == s.id || (it.ClientMsgID == "" && it.Text == s.text && it.Seq > s.atToSeq) {
				return true
			}
		}
		return false
	}
	var bottom []syncMessage
	for _, s := range d.sends {
		if s.discarded || inTranscript(s) {
			continue
		}
		if s.outcome == "accepted" {
			bottom = append(bottom, syncMessage{From: "user", Text: s.text, State: "pending"})
			continue
		}
		at := len(out)
		for k, e := range out {
			if e.at > s.atToSeq {
				at = k
				break
			}
		}
		out = append(out[:at], append([]entry{{s.atToSeq, syncMessage{From: "user", Text: s.text, State: "failed"}}}, out[at:]...)...)
	}
	msgs := make([]syncMessage, 0, len(out)+len(bottom))
	for _, e := range out {
		msgs = append(msgs, e.msg)
	}
	return append(msgs, bottom...)
}

// sameJSON compares as the wire would: raw fields (tool_input) differ in layout between where a file writes them.
func sameJSON(a, b any) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
