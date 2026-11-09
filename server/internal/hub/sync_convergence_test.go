package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The event resume as an app lives it (#1073): the manager raises and ends cards at random, other broadcasts come
// between, and an app drops and reconnects at random, losing what was still on its way when the socket went. Every
// reconnect is the hub's own sequence (serveWS: the hello, resumed or not as the hub decides, then the pending-card
// snapshot), and the app keeps only what the protocol gives it: its cursor, the frames, and on a hello that did not
// resume its full reread of the card list. Once it has read what came after a hello, its cards are exactly the hub's
// pending ones: snapshot + replay + live equals the hub's state, whatever the interleaving.
func TestResumeConvergesToThePendingCards(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { resumeRun(t, seed) })
	}
}

type resumeApp struct {
	stream  string
	seq     int64
	cards   map[string]bool
	resumes int
	rereads int
}

// take is the app reading one frame.
func (a *resumeApp) take(t *testing.T, h *Hub, b []byte) {
	var e struct {
		Type        string             `json:"type"`
		ID          string             `json:"id"`
		Interaction *proto.Interaction `json:"interaction"`
		EventStream string             `json:"event_stream"`
		EventSeq    int64              `json:"event_seq"`
		Resumed     bool               `json:"resumed"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	switch e.Type {
	case "hello":
		a.stream = e.EventStream
		if e.Resumed {
			// the replay follows, each event after the app's cursor: the cursor moves with them
			a.resumes++
			return
		}
		// not resumed: the app rereads the card list (GET /interactions) and keeps counting from the hello
		a.seq = e.EventSeq
		a.rereads++
		a.cards = map[string]bool{}
		for _, ix := range h.pendingInteractions() {
			a.cards[ix.ID] = true
		}
		return
	case "interaction":
		a.cards[e.Interaction.ID] = true
	case "interaction_resolved":
		delete(a.cards, e.ID)
	}
	if e.EventSeq > 0 {
		if e.EventSeq <= a.seq {
			t.Fatalf("event %d (%s) again after %d", e.EventSeq, e.Type, a.seq)
		}
		a.seq = e.EventSeq
	}
}

func resumeRun(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	h := newTestHub(t)
	app := &resumeApp{cards: map[string]bool{}}
	var c *client
	connect := func() {
		c = &client{out: make(chan []byte, 8192)}
		h.mu.Lock()
		h.clients[c] = struct{}{}
		h.helloLocked(c, map[string]any{"type": "hello"}, app.stream, app.seq, app.stream != "")
		h.mu.Unlock()
		h.sendPendingInteractions(c, false)
	}
	drain := func(keep int) { // reads what is queued; past `keep` frames (when >= 0) the rest is lost with the socket
		for n := 0; ; n++ {
			select {
			case b := <-c.out:
				if keep < 0 || n < keep {
					app.take(t, h, b)
				}
			default:
				return
			}
		}
	}
	check := func(when string) {
		var want []string
		for _, ix := range h.pendingInteractions() {
			want = append(want, ix.ID)
		}
		var got []string
		for id := range app.cards {
			got = append(got, id)
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d, %s: the app shows %v, the hub has %v pending", seed, when, got, want)
		}
	}
	connect()
	drain(-1)
	check("first connection")
	next := 0
	for step := 0; step < 400; step++ {
		pending := h.pendingInteractions()
		switch r := rng.Intn(100); {
		case r < 30:
			next++
			reportInteraction(t, h, proto.Interaction{ID: fmt.Sprintf("ix%d", next), MachineID: "test-machine", SessionID: "test-session",
				Kind: proto.IxPermission, ToolName: "Bash", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)})
		case r < 55 && len(pending) > 0:
			reportResolved(t, h, pending[rng.Intn(len(pending))].ID, "")
		case r < 65:
			h.broadcast(map[string]any{"type": "todos"})
		case r < 80 && c != nil:
			drain(-1)
		case r < 90 && c != nil: // the socket goes: part of what was on its way never arrives
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			drain(rng.Intn(len(c.out) + 1))
			c = nil
		case r < 97 && c == nil:
			connect()
			drain(-1)
			check(fmt.Sprintf("step %d, after a reconnect", step))
		case c == nil: // a long time away: more than the hub replays
			for range 450 {
				h.broadcast(map[string]any{"type": "inbox"})
			}
		}
	}
	if c == nil {
		connect()
	}
	drain(-1)
	check("the end")
	if app.resumes == 0 && app.rereads < 2 {
		t.Logf("seed %d: no reconnect happened", seed)
	}
}

// The session list as a delta app follows it (#1073): the machine's sessions change at random (added, changed,
// ended, Codex history sessions coming and going) and the app applies the connection snapshot and then every delta
// the hub makes for it. After each one the app's list is the hub's record of it (have), and, Codex history aside, the
// machine's list itself: snapshot + deltas equals the full list.
func TestSessionDeltasConvergeToTheList(t *testing.T) {
	for seed := int64(1); seed <= 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var list []proto.Session
		next := 0
		mutate := func() {
			switch r := rng.Intn(100); {
			case r < 30 || len(list) == 0:
				next++
				s := proto.Session{MachineID: "m1", SessionID: fmt.Sprintf("s%d", next), Title: fmt.Sprintf("Task %d", next), Status: "working"}
				if rng.Intn(4) == 0 {
					s.SourceKind = "codex_history"
				}
				list = append(list, s)
			case r < 70:
				i := rng.Intn(len(list))
				list[i].Status = []string{"working", "blocked", "done", "idle"}[rng.Intn(4)]
				list[i].Rev = int64(rng.Intn(1000))
			case r < 85:
				i := rng.Intn(len(list))
				list = append(list[:i:i], list[i+1:]...)
			default: // a live session ends and is known from its rollout only
				i := rng.Intn(len(list))
				list[i].SourceKind = "codex_history"
			}
		}
		for range rng.Intn(5) {
			mutate()
		}
		// the snapshot on connecting (snapshotSessions)
		shown := forDelta(encodeSessions(list), nil)
		have := map[string][]byte{}
		app := map[string]string{}
		for _, s := range shown {
			have[s.id] = s.raw
			app[s.id] = string(s.raw)
		}
		for step := range 200 {
			mutate()
			var b []byte
			b, have = sessionsDelta("m1", encodeSessions(list), have)
			if b != nil {
				var d struct {
					Type   string            `json:"type"`
					Upsert []json.RawMessage `json:"upsert"`
					Remove []string          `json:"remove"`
				}
				if err := json.Unmarshal(b, &d); err != nil || d.Type != "sessions_delta" {
					t.Fatalf("seed %d step %d: %s %v", seed, step, b, err)
				}
				for _, raw := range d.Upsert {
					var s proto.Session
					_ = json.Unmarshal(raw, &s)
					app[s.SessionID] = string(raw)
				}
				for _, id := range d.Remove {
					delete(app, id)
				}
			}
			if len(app) != len(have) {
				t.Fatalf("seed %d step %d: the app has %d sessions, the hub's record of it %d", seed, step, len(app), len(have))
			}
			for id, raw := range have {
				if app[id] != string(raw) {
					t.Fatalf("seed %d step %d: session %s differs from the hub's record", seed, step, id)
				}
			}
			for _, s := range encodeSessions(list) {
				if got, ok := app[s.id]; (ok || !s.history) && got != string(s.raw) {
					t.Fatalf("seed %d step %d: session %s is %q on the app, %q on the machine", seed, step, s.id, got, s.raw)
				}
			}
		}
	}
}

// Sends as an app makes them through a bad link (#1073): messages sent and sent again with their client_msg_id, some
// at once, to a node that types them, refuses with nothing typed, or is cut off while typing (the hub's link to it
// drops), while the node reconnects and the hub restarts on the same sent_messages. Whatever the order, a message is
// typed at most once; one the hub answered 200 for was typed exactly once; and after an answer that it may have
// arrived (delivered unknown) no retry types it again.
func TestSendsAreTypedAtMostOnce(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		sendsRun(t, seed)
	}
}

func sendsRun(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	var rngMu sync.Mutex
	roll := func(n int) int { rngMu.Lock(); defer rngMu.Unlock(); return rng.Intn(n) }
	store := newSentStore()
	var mu sync.Mutex
	typed := map[string]int{}
	ok := map[string]bool{}
	unknown := map[string]int{} // typed count when it was first answered "unknown"
	newHub := func() *Hub {
		h := newTestHub(t)
		h.store = store
		return h
	}
	h := newHub()
	routes := h.routes() // built once per hub: routes() records h.apiMux, so posts at once must share it
	var hubSide *rpc.Peer
	link := func() {
		m := h.byID["box"]
		if m == nil {
			m = newMachine(h, MachineConfig{ID: "box", Name: "box"})
			h.byID["box"] = m
			h.machines = append(h.machines, m)
		}
		hubIn, nodeOut := io.Pipe()
		nodeIn, hubOut := io.Pipe()
		peer, node := rpc.New("h", hubOut), rpc.New("n", nodeOut)
		node.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "session.send" {
				return map[string]any{}, nil
			}
			var p struct {
				ClientMsgID string `json:"client_msg_id"`
			}
			_ = json.Unmarshal(params, &p)
			switch roll(10) {
			case 0: // the box holds a draft: nothing typed
				return nil, &rpc.Error{Code: "input_draft", Message: "draft", Data: json.RawMessage(`{"delivered":"none","draft":"x"}`)}
			case 1: // typing began, then the link went
				mu.Lock()
				typed[p.ClientMsgID]++
				mu.Unlock()
				peer.Close(io.ErrUnexpectedEOF)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			mu.Lock()
			typed[p.ClientMsgID]++
			mu.Unlock()
			return map[string]any{"ok": true}, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = peer.Serve(ctx, hubIn) }()
		go func() { _ = node.Serve(ctx, nodeIn) }()
		m.mu.Lock()
		m.peer, m.online = peer, true
		m.mu.Unlock()
		hubSide = peer
	}
	link()
	post := func(id string) {
		code, out := call(t, routes, "POST", sendPath, h.cfg.Token, fmt.Sprintf(`{"text":"message %s","client_msg_id":"%s"}`, id, id))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case code == 200:
			ok[id] = true
		case out["delivered"] == "unknown":
			if _, seen := unknown[id]; !seen {
				unknown[id] = typed[id]
			}
		}
	}
	for step := 0; step < 80; step++ {
		id := fmt.Sprintf("m%d", roll(6))
		switch r := roll(100); {
		case r < 55:
			post(id)
		case r < 75: // the same one twice at once (a double tap, a retry while the first is on its way)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); post(id) }()
			}
			wg.Wait()
		case r < 90:
			hubSide.Close(io.ErrUnexpectedEOF)
			link() // the node reconnects
		default:
			h = newHub() // the hub restarts on the same database
			routes = h.routes()
			link()
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for id, n := range typed {
		if n > 1 {
			t.Fatalf("seed %d: %s typed %d times", seed, id, n)
		}
	}
	for id := range ok {
		if typed[id] != 1 {
			t.Fatalf("seed %d: %s answered 200 but typed %d times", seed, id, typed[id])
		}
	}
	for id, at := range unknown {
		if typed[id] != at {
			t.Fatalf("seed %d: %s typed again after its answer said it may have arrived", seed, id)
		}
	}
}
