package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// The cards the manager makes itself (fleet_ix.go): a TUI dialog read off the screen, the Codex cards a node lists,
// a plan answered in the terminal. Moved from the hub's tests with them.

// Screens with a dialog the manager can read (screen.ParseDialog).
const (
	pickModel    = "  Pick a model\n\n› 1. gpt-6\n  2. gpt-6-mini\n\n  Press enter to confirm or esc to go back\n"
	pickEffort   = "  Pick a reasoning level\n\n› 1. low\n  2. high\n\n  Press enter to confirm or esc to go back\n"
	dialogScreen = "────────────────────\n Do you want to proceed?\n ❯ 1. Yes\n   2. Type something.\n   3. No\n"
)

// cardNode is a node that shows a screen (session.screen) and takes Codex answers (codex.answer). Like a current
// node it says the revision of the dialog on the screen it gives (dialog_rev), unless dialogRev says otherwise: ""
// is a node from before #5538 that says none, anything else the revision a node of another build parses.
type cardNode struct {
	mu        sync.Mutex
	answers   []map[string]any
	err       error
	screen    string
	dialogRev *string
	keysErr   error // what session.keys answers
}

func (c *cardNode) setScreen(s string) {
	c.mu.Lock()
	c.screen = s
	c.mu.Unlock()
}

// newCardFleet is a manager whose machine "mac" has a cardNode.
func newCardFleet(t *testing.T, store bool) (*fleetTest, *cardNode) {
	t.Helper()
	ft := newFleetTestNoStore(t)
	if store {
		ft = newFleetTest(t)
	}
	c := &cardNode{}
	ft.d.mu.Lock()
	ft.d.machines["mac"].call = func(method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		c.mu.Lock()
		defer c.mu.Unlock()
		switch method {
		case "session.screen":
			if c.screen == "node-error" {
				return nil, errNode
			}
			res := map[string]any{"text": c.screen}
			if c.dialogRev != nil {
				if *c.dialogRev != "" {
					res["dialog_rev"] = *c.dialogRev
				}
			} else if d := screen.ParseDialog(c.screen); d != nil && p["lines"] == float64(screen.DialogLines) {
				res["dialog_rev"] = d.Rev
			}
			return res, nil
		case "session.keys":
			if c.keysErr != nil {
				return nil, c.keysErr
			}
		case "codex.answer":
			c.answers = append(c.answers, p)
			if c.err != nil {
				return nil, c.err
			}
			return map[string]any{"ok": true}, nil
		}
		return map[string]any{}, nil
	}
	ft.d.mu.Unlock()
	return ft, c
}

func pendingIDs(s *Service) map[string]string {
	out := map[string]string{}
	for _, ix := range s.pendingInteractions() {
		out[ix.ID] = ix.Kind
	}
	return out
}

// waitCards waits up to d for n interactions and says how long it took.
func waitCards(s *Service, n int, d time.Duration) (map[string]string, time.Duration) {
	start := time.Now()
	for len(pendingIDs(s)) < n && time.Since(start) < d {
		time.Sleep(10 * time.Millisecond)
	}
	return pendingIDs(s), time.Since(start)
}

func blockSession(ft *fleetTest, sess proto.Session) {
	sess.Status = proto.StatusBlocked
	ft.d.setSessions("mac", []proto.Session{sess})
	ft.s.syncScreenInteractions("mac", []proto.Session{sess})
}

func listCodex(s *Service, ixs ...proto.Interaction) {
	b, _ := json.Marshal(map[string]any{"interactions": ixs})
	s.syncCodexInteractions("mac", b)
}

// endedCards records the cards the channels are told are over, for a test to ask about.
func endedCards(s *Service) func(id string) bool {
	var mu sync.Mutex
	seen := map[string]bool{}
	s.fleetEnded = func(ix proto.Interaction, _ *proto.Resolution) {
		mu.Lock()
		seen[ix.ID] = true
		mu.Unlock()
	}
	return func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return seen[id]
	}
}

// ---- screen cards ----

// A Claude Code without Persona's hooks (the user's own profile) asks only on the screen: its card is made at once,
// not after the head start a hook's card gets (#751). One with hooks (a hook event came, or Persona started it) waits.
func TestScreenCardWaitsOnlyForHooks(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     proto.Session
		event bool
		min   time.Duration
		max   time.Duration
	}{
		{"no hooks", proto.Session{SessionID: "s1", Agent: "claude"}, false, 0, 300 * time.Millisecond},
		{"a hook event", proto.Session{SessionID: "s1", Agent: "claude"}, true, 1400 * time.Millisecond, 3 * time.Second},
		{"started by Persona", proto.Session{SessionID: "s1", Agent: "claude", StartedByPersona: true}, false, 1400 * time.Millisecond, 3 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft, n := newCardFleet(t, false)
			n.setScreen(pickModel)
			if c.event {
				ft.event("mac", "hook.event", map[string]any{"session_id": "s1", "event": "UserPromptSubmit"})
			}
			blockSession(ft, c.s)
			got, took := waitCards(ft.s, 1, 5*time.Second)
			if len(got) != 1 || took < c.min || took > c.max {
				t.Fatalf("%v after %v, want one card in [%v, %v]", got, took, c.min, c.max)
			}
		})
	}
}

func TestScreenCardWait(t *testing.T) {
	claude, codex := proto.Session{Agent: "claude"}, proto.Session{Agent: "codex"}
	if screenCardWait(claude, false) != 0 || screenCardWait(claude, true) != 1500*time.Millisecond ||
		screenCardWait(codex, false) != 3*time.Second {
		t.Fatal("waits")
	}
}

// After a screen card's answer the agent asks again while herdr keeps it blocked: no status change comes, so the
// manager looks at the screen again and the new dialog gets its card. The answered dialog still on screen gets none.
func TestScreenCardAfterAnAnswerWhileStillBlocked(t *testing.T) {
	old, oldFor := screenRecheck, screenRecheckFor
	screenRecheck, screenRecheckFor = 50*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { screenRecheck, screenRecheckFor = old, oldFor })
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	var first string
	for id := range got {
		first = id
	}
	if first == "" {
		t.Fatal("no card")
	}
	if err := s.resolve(first, proto.Resolution{Option: "1"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := pendingIDs(s); len(got) != 0 {
		t.Fatalf("a card again for the answered dialog: %v", got)
	}
	n.setScreen(pickEffort)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, took := waitCards(s, 1, 2*time.Second)
	second := ""
	for id := range got {
		second = id
	}
	if second == "" || second == first || took > time.Second {
		t.Fatalf("next dialog: %v after %v", got, took)
	}
	if err := s.resolve(second, proto.Resolution{Option: "2"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	n.setScreen("  ⏺ Thinking…\n")
	time.Sleep(80 * time.Millisecond)
	n.setScreen(pickModel)
	got, took = waitCards(s, 1, 2*time.Second)
	if len(got) != 1 || got[first] == "" || took > 300*time.Millisecond {
		t.Fatalf("after the second answer, with no status change: %v after %v", got, took)
	}
	// The keys went to the node with the card's revision.
	if keys := ft.d.callsTo("mac", "session.keys"); len(keys) != 2 || !strings.Contains(string(keys[0]), `"rev"`) {
		t.Fatalf("session.keys %s", keys)
	}
}

// Blocked a moment before the dialog is drawn: the card made then has no dialog. Once the dialog shows, its card
// replaces that one.
func TestScreenCardUpgradesWhenTheDialogShows(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	shortScreenUpgrade(s)
	n.setScreen("  ✢ Percolating… (running PreToolUse hooks… 1/2 · 3s)\n")
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	if len(got) != 1 || got["scr_mac_s1"] == "" {
		t.Fatalf("the card before the dialog: %v", got)
	}
	n.setScreen(pickModel)
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		if ids := pendingIDs(s); len(ids) == 1 && ids["scr_mac_s1"] == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	list := s.pendingInteractions()
	if len(list) != 1 || list[0].ID == "scr_mac_s1" || list[0].Dialog == nil || len(list[0].Dialog.Options) != 2 {
		t.Fatalf("after the dialog showed: %+v", list)
	}
}

// When a screen card is ended while upgradeScreenCard is polling, the replacement check
// must not re-finish or recreate the card (#5013).
func TestScreenCardUpgradeAbortedWhenCardEnded(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	shortScreenUpgrade(s)
	n.setScreen("  ✢ Percolating…\n")
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	if len(got) != 1 || got["scr_mac_s1"] == "" {
		t.Fatalf("card before dialog: %v", got)
	}
	// Finish the card before the dialog is parsed.
	s.finishInteraction("scr_mac_s1")
	n.setScreen(pickModel)
	time.Sleep(100 * time.Millisecond)
	if list := s.pendingInteractions(); len(list) != 0 {
		t.Fatalf("expected no replacement card created after card was ended, got: %+v", list)
	}
}

// Another manager's upgrade loop is still running at the default timing, as one an earlier test leaves behind:
// shortening this manager's timing touches nothing that loop reads, so -race has nothing to report (#4554), and this
// manager's card still upgrades.
func TestScreenCardUpgradeTimingIsPerService(t *testing.T) {
	other, on := newCardFleet(t, false)
	on.setScreen("  ✢ Percolating…\n")
	blockSession(other, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(other.s, 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("the other card: %v", got)
	}
	t.Cleanup(func() { // its loop ends at its next look
		other.d.setSessions("mac", []proto.Session{{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking}})
	})

	ft, n := newCardFleet(t, false)
	s := ft.s
	shortScreenUpgrade(s)
	n.setScreen("  ✢ Percolating…\n")
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(s, 1, 2*time.Second); len(got) != 1 || got["scr_mac_s1"] == "" {
		t.Fatalf("the card before the dialog: %v", got)
	}
	n.setScreen(pickModel)
	// upgradeScreenCard ends the card without a dialog, then makes the dialog's: wait for that one, not for a gap.
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(10 * time.Millisecond) {
		if ids := pendingIDs(s); len(ids) == 1 && ids["scr_mac_s1"] == "" {
			break
		}
	}
	if list := s.pendingInteractions(); len(list) != 1 || list[0].ID == "scr_mac_s1" || list[0].Dialog == nil {
		t.Fatalf("after the dialog showed: %+v", list)
	}
}

func waitReplaced(s *Service, oldID string, d time.Duration) (string, bool) {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); {
		if got := pendingIDs(s); len(got) == 1 && got[oldID] == "" {
			for id := range got {
				return id, true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", false
}

// noDialogPlaceholder blocks a session whose screen has no dialog yet, with the upgrade loop's timing set per
// Service before the card is made (#4665): the loop reads its timing as it starts.
func noDialogPlaceholder(t *testing.T, screenText string, poll, window time.Duration) (*fleetTest, *cardNode) {
	t.Helper()
	ft, n := newCardFleet(t, false)
	ft.s.fleet.upgradeEvery, ft.s.fleet.upgradeFor = poll, window
	n.setScreen(screenText)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	waitCards(ft.s, 1, 2*time.Second)
	return ft, n
}

// A card made while its dialog is not drawn yet goes to the apps but does not ring (#4450: it used to ring twice).
func TestScreenCardWithoutDialogShowsWithoutAPush(t *testing.T) {
	ft, n := noDialogPlaceholder(t, "  ✢ Percolating… (running PreToolUse hooks… 1/2 · 3s)\n", 20*time.Millisecond, 600*time.Millisecond)
	told := ""
	for _, params := range ft.toldOf(assistantwire.EventFleetInteraction) {
		told += string(params)
	}
	if !strings.Contains(told, `"id":"scr_mac_s1"`) {
		t.Fatal("the placeholder was not put on the apps")
	}
	n.setScreen(pickModel)
	waitReplaced(ft.s, "scr_mac_s1", 2*time.Second)
}

// The dialog shows during the window, after reads failed, or on the poll that crosses the window's end: the
// placeholder never rings; only its replacement card does.
func TestScreenCardDialogRingsOnceForTheRealCard(t *testing.T) {
	for _, c := range []struct {
		name             string
		poll, window, at time.Duration
		failFor          time.Duration
	}{
		{"within the window", 20 * time.Millisecond, 600 * time.Millisecond, 100 * time.Millisecond, 0},
		{"after reads failed for a while", 20 * time.Millisecond, 600 * time.Millisecond, 150 * time.Millisecond, 150 * time.Millisecond},
		{"on the poll crossing the window's end", 400 * time.Millisecond, 600 * time.Millisecond, 450 * time.Millisecond, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft, n := noDialogPlaceholder(t, "  ✢ Percolating…\n", c.poll, c.window)
			if c.failFor > 0 {
				n.setScreen("node-error") // a screen read the node cannot serve
			}
			time.Sleep(c.at)
			n.setScreen(pickModel)
			realID, ok := waitReplaced(ft.s, "scr_mac_s1", 2*time.Second)
			if !ok {
				t.Fatalf("the dialog's card did not replace the placeholder: %v", pendingIDs(ft.s))
			}
			if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, 2*time.Second); len(pushes) != 1 ||
				pushes[0].Interaction == nil || pushes[0].Interaction.ID != realID {
				t.Fatalf("want one push for %s, got %+v", realID, pushes)
			}
		})
	}
}

// Window's end, still blocked and unreadable: the card already on the apps is announced once, not before or again.
func TestScreenCardUnreadableAnnouncedOnceAfterTheWindow(t *testing.T) {
	ft, _ := noDialogPlaceholder(t, "  Waiting for something\n", 20*time.Millisecond, 600*time.Millisecond)
	if early := ft.pushesOf(assistantwire.NotifyInput, 1, 300*time.Millisecond); len(early) != 0 {
		t.Fatalf("rang before the window ended: %d pushes", len(early))
	}
	pushes := ft.pushesOf(assistantwire.NotifyInput, 1, 2*time.Second)
	if len(pushes) != 1 || pushes[0].Interaction == nil || pushes[0].Interaction.ID != "scr_mac_s1" {
		t.Fatalf("want one late push for the placeholder, got %+v", pushes)
	}
}

// The dialog answered on the computer: the agent moves on and its screen card goes, cancelled in the history too.
func TestScreenCardGoneWhenTheAgentMovesOnIsRecorded(t *testing.T) {
	ft, n := newCardFleet(t, true)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	var id string
	for k := range got {
		id = k
	}
	if id == "" {
		t.Fatal("no card")
	}
	ft.sessions("mac", proto.Session{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking})
	if len(pendingIDs(s)) != 0 {
		t.Fatalf("card still pending: %v", pendingIDs(s))
	}
	if status, _, _ := ft.st.InteractionOutcome(t.Context(), id); status != "cancelled" {
		t.Fatalf("history says %q, want cancelled", status)
	}
}

// The node's connection drops and comes back while the agent still waits on the same dialog: its card comes back on
// the apps without ringing the phone a second time (#4252).
func TestScreenCardBackAfterTheNodeReconnectsIsQuiet(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(pushes) != 1 {
		t.Fatalf("first: pushed %d times", len(pushes))
	}
	s.NodeDetached("mac") // the session still shows blocked while its node reconnects
	back, _ := waitCards(s, 1, 2*time.Second)
	if len(back) != 1 {
		t.Fatalf("the card did not come back: %v", back)
	}
	for id := range got {
		if back[id] == "" {
			t.Fatalf("another card came back: %v, had %v", back, got)
		}
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, 300*time.Millisecond); len(pushes) != 1 {
		t.Fatalf("the card that came back was pushed again: %d pushes", len(pushes))
	}
}

// The agent moved on while its node was away and later asks the same dialog again: that is a new question, pushed.
func TestScreenCardAskedAgainAfterTheNodeWasAwayIsPushed(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(s, 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	ft.d.setSessions("mac", []proto.Session{{SessionID: "s1", Agent: "claude", Status: proto.StatusUnknown}})
	s.NodeDetached("mac") // gone past the grace: its sessions show unknown, no card is read
	if got := pendingIDs(s); len(got) != 0 {
		t.Fatalf("cards while the node is away: %v", got)
	}
	ft.sessions("mac", proto.Session{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking}) // back, working
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(s, 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(pushes) != 2 {
		t.Fatalf("asked again after moving on: pushed %d times", len(pushes))
	}
}

// Right after the reconnect the screen is read before the dialog is drawn again: that card, and the one made once the
// dialog shows, are the card that was pushed already (#4252).
func TestScreenCardBackHalfDrawnAfterTheNodeReconnectsIsQuiet(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	shortScreenUpgrade(s)
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	n.setScreen("  ✢ Percolating…\n")
	s.NodeDetached("mac")
	if back, _ := waitCards(s, 1, 2*time.Second); len(back) != 1 || back["scr_mac_s1"] == "" {
		t.Fatalf("the card before the dialog: %v", back)
	}
	n.setScreen(pickModel)
	var first string
	for id := range got {
		first = id
	}
	// upgradeScreenCard ends the card without a dialog, then makes the dialog's: wait for that one, not for a gap.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pendingIDs(s)[first] == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if ids := pendingIDs(s); len(ids) != 1 || ids[first] == "" {
		t.Fatalf("the dialog's card did not come back: %v, had %v", ids, got)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, 300*time.Millisecond); len(pushes) != 1 {
		t.Fatalf("pushed %d times", len(pushes))
	}
}

// A half-drawn reconnect-back card that stays unreadable past the window rang before the detach: no late ring (#4450/#4252).
func TestScreenCardBackHalfDrawnUnreadableStaysQuiet(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	s.fleet.upgradeEvery, s.fleet.upgradeFor = 20*time.Millisecond, 600*time.Millisecond
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(pushes) != 1 {
		t.Fatalf("first: pushed %d times", len(pushes))
	}
	n.setScreen("  Waiting for something\n")
	s.NodeDetached("mac")
	if back, _ := waitCards(s, 1, 2*time.Second); len(back) != 1 || back["scr_mac_s1"] == "" {
		t.Fatalf("the placeholder did not come back: %v", back)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(pushes) != 1 {
		t.Fatalf("the back placeholder rang past the window: %d pushes", len(pushes))
	}
	// Join the expired quiet loop: equal call counts 100ms apart (20ms polls) are past its last read.
	calls := len(ft.d.callsTo("mac", "session.screen"))
	time.Sleep(100 * time.Millisecond)
	if got := len(ft.d.callsTo("mac", "session.screen")); got != calls {
		t.Fatalf("the look-again loop kept polling: %d then %d", calls, got)
	}
}

// placeholderBack: a card made with no dialog to read (#4450) whose node detaches inside its look-again window and is
// back within resumeGrace; the card read then is the same id (an exact-id mark, not the session's). Nothing has rung.
func placeholderBack(t *testing.T) (*fleetTest, *cardNode) {
	t.Helper()
	ft, n := noDialogPlaceholder(t, "  ✢ Percolating…\n", 20*time.Millisecond, 600*time.Millisecond)
	s := ft.s
	if got := pendingIDs(s); len(got) != 1 || got["scr_mac_s1"] == "" {
		t.Fatalf("the placeholder: %v", got)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, 100*time.Millisecond); len(pushes) != 0 {
		t.Fatalf("the placeholder rang inside its window: %d pushes", len(pushes))
	}
	s.NodeDetached("mac")
	if back, _ := waitCards(s, 1, 2*time.Second); len(back) != 1 || back["scr_mac_s1"] == "" {
		t.Fatalf("the placeholder did not come back: %v", back)
	}
	s.fleet.mu.Lock()
	left := len(s.fleet.screenDetached)
	s.fleet.mu.Unlock()
	if left != 0 {
		t.Fatalf("not an exact-id return: %d marks left", left)
	}
	return ft, n
}

// A placeholder that never rang, detached inside its window and back, still unreadable when the window ends: it rings
// once, as it would have without the detach (#4979).
func TestScreenCardPlaceholderBackRingsAtTheWindowsEnd(t *testing.T) {
	ft, _ := placeholderBack(t)
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, 2*time.Second); len(pushes) != 1 {
		t.Fatalf("the back placeholder never rang: %d pushes", len(pushes))
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, 300*time.Millisecond); len(pushes) != 1 {
		t.Fatalf("pushed %d times", len(pushes))
	}
}

// The same, but the real dialog shows after the reconnect: its card replaces the placeholder and rings once (#4979).
func TestScreenCardPlaceholderBackThenTheDialogRings(t *testing.T) {
	ft, n := placeholderBack(t)
	s := ft.s
	n.setScreen(pickModel)
	if id, ok := waitReplaced(s, "scr_mac_s1", 2*time.Second); !ok || id == "" {
		t.Fatalf("the dialog's card did not replace the placeholder: %v", pendingIDs(s))
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(pushes) != 1 {
		t.Fatalf("the dialog's card never rang: %d pushes", len(pushes))
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, 800*time.Millisecond); len(pushes) != 1 {
		t.Fatalf("pushed %d times", len(pushes))
	}
}

// A placeholder that rang at its window's end, detached and back within resumeGrace: the card read again is the one
// that rang, and its window's end is quiet (#4252/#4979).
func TestScreenCardPlaceholderThatRangBackStaysQuiet(t *testing.T) {
	ft, _ := noDialogPlaceholder(t, "  ✢ Percolating…\n", 20*time.Millisecond, 300*time.Millisecond)
	s := ft.s
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, 2*time.Second); len(pushes) != 1 {
		t.Fatalf("the placeholder did not ring at its window's end: %d pushes", len(pushes))
	}
	s.NodeDetached("mac")
	if back, _ := waitCards(s, 1, 2*time.Second); len(back) != 1 || back["scr_mac_s1"] == "" {
		t.Fatalf("the placeholder did not come back: %v", back)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(pushes) != 1 {
		t.Fatalf("the back placeholder rang again: %d pushes", len(pushes))
	}
}

// Away longer than resumeGrace: the dialog read once the node is back is pushed again.
func TestScreenCardBackAfterTheGraceIsPushed(t *testing.T) {
	old := resumeGrace
	resumeGrace = 50 * time.Millisecond
	t.Cleanup(func() { resumeGrace = old })
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(s, 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	ft.d.setSessions("mac", []proto.Session{{SessionID: "s1", Agent: "claude", Status: proto.StatusUnknown}})
	s.NodeDetached("mac")
	time.Sleep(2 * resumeGrace)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(s, 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(pushes) != 2 {
		t.Fatalf("back after the grace: pushed %d times", len(pushes))
	}
}

// A runtime's session (docs/integrations/runtimes.md) has no terminal: blocked there, no screen is read; a session with a terminal
// still gets its card.
func TestNoScreenCardWithoutATerminal(t *testing.T) {
	ft, n := newCardFleet(t, false)
	n.setScreen(pickModel)
	runtime := proto.Session{SessionID: "session-dsh", Status: proto.StatusBlocked, Agent: "dsh", SourceKind: proto.SourceRuntime,
		Capabilities: &proto.SessionCapabilities{Read: true, Send: true, Approve: true}}
	ft.d.setSessions("mac", []proto.Session{runtime})
	ft.s.syncScreenInteractions("mac", []proto.Session{runtime})
	time.Sleep(500 * time.Millisecond)
	if got := pendingIDs(ft.s); len(got) != 0 {
		t.Fatalf("a screen card for a session without a terminal: %v", got)
	}
	terminal := proto.Session{SessionID: "s1", Status: proto.StatusBlocked}
	ft.d.setSessions("mac", []proto.Session{runtime, terminal})
	ft.s.syncScreenInteractions("mac", []proto.Session{runtime, terminal})
	got, _ := waitCards(ft.s, 1, 5*time.Second)
	if len(got) != 1 || got["scr_mac_s1_"+screen.ParseDialog(pickModel).Rev] != proto.IxScreen {
		t.Fatalf("want one screen card, for the terminal session: %v", got)
	}
}

// #5538: the card carries the revision the node will check the answer against. A node says its own (dialog_rev), so
// a hub and a node of different builds that parse the screen differently still agree; a node from before that says
// none and checks the old formula, which the card then carries.
func TestScreenCardTakesTheNodesRevision(t *testing.T) {
	d := screen.ParseDialog(pickModel)
	for _, c := range []struct {
		name, nodeSays, want string
	}{
		{"another build", "0123456789ab", "0123456789ab"},
		{"same build", d.Rev, d.Rev},
		{"before #5538", "", d.LegacyRev},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft, n := newCardFleet(t, false)
			n.setScreen(pickModel)
			n.mu.Lock()
			n.dialogRev = &c.nodeSays
			n.mu.Unlock()
			blockSession(ft, proto.Session{SessionID: "s1"})
			got, _ := waitCards(ft.s, 1, 5*time.Second)
			if len(got) != 1 || got["scr_mac_s1_"+c.want] != proto.IxScreen {
				t.Fatalf("want the card scr_mac_s1_%s: %v", c.want, got)
			}
			for _, ix := range ft.s.pendingInteractions() {
				if ix.Dialog == nil || ix.Dialog.Rev != c.want {
					t.Fatalf("dialog %+v", ix.Dialog)
				}
				if p, err := screenAnswer("s1", ix.Dialog, proto.Resolution{Option: "2"}); err != nil || p["rev"] != c.want {
					t.Fatalf("answer %v %v", p, err)
				}
			}
		})
	}
}

// #5538: the node refuses an answer because it reads another dialog than the card's (prompt_changed). The card made
// next is named by what the node reads, so it is a new card. Named by the hub's own reading, it came back under the
// old id while the hub still read the old dialog, and every answer to it was refused again.
func TestScreenCardRebuiltAfterPromptChanged(t *testing.T) {
	ft, n := newCardFleet(t, false)
	n.setScreen(pickModel)
	first, second := "aaaaaaaaaaaa", "bbbbbbbbbbbb"
	n.mu.Lock()
	n.dialogRev = &first
	n.keysErr = &rpc.Error{Code: screen.PromptChanged, Message: "变了", Data: json.RawMessage(`{"delivered":"none"}`)}
	n.mu.Unlock()
	blockSession(ft, proto.Session{SessionID: "s1"})
	if got, _ := waitCards(ft.s, 1, 5*time.Second); len(got) != 1 || got["scr_mac_s1_"+first] != proto.IxScreen {
		t.Fatalf("first card %v", got)
	}
	n.mu.Lock()
	n.dialogRev = &second // the node now reads the screen as another dialog
	n.mu.Unlock()
	var re *rpc.Error
	if err := ft.s.resolve("scr_mac_s1_"+first, proto.Resolution{Option: "1"}); !errors.As(err, &re) || re.Code != screen.PromptChanged {
		t.Fatalf("answer: %v", err)
	}
	if got, _ := waitCards(ft.s, 1, 5*time.Second); len(got) != 1 || got["scr_mac_s1_"+second] != proto.IxScreen {
		t.Fatalf("after prompt_changed: %v", got)
	}
}

func TestScreenAnswer(t *testing.T) {
	d := screen.ParseDialog(dialogScreen)
	p, err := screenAnswer("s1", d, proto.Resolution{Option: "3"})
	if err != nil || p["rev"] != d.Rev || p["option"] != "3" || !slices.Equal(p["keys"].([]string), []string{"3"}) {
		t.Fatalf("%v %v", p, err)
	}
	p, _ = screenAnswer("s1", d, proto.Resolution{Option: "2", Text: "use the staging db"})
	if !slices.Equal(p["pre_keys"].([]string), []string{"2"}) || !slices.Equal(p["keys"].([]string), []string{"enter"}) || p["text"] != "use the staging db" {
		t.Fatalf("type something: %v", p)
	}
	p, _ = screenAnswer("s1", d, proto.Resolution{Keys: []string{"1"}})
	if p["rev"] != d.Rev || p["option"] != nil || !slices.Equal(p["keys"].([]string), []string{"1"}) {
		t.Fatalf("legacy: %v", p)
	}
	if _, err := screenAnswer("s1", d, proto.Resolution{Option: "9"}); err == nil {
		t.Fatal("no such option")
	}
	if p, _ := screenAnswer("s1", nil, proto.Resolution{Keys: []string{"esc"}}); p["rev"] != nil {
		t.Fatalf("%v", p)
	}
}

// The dialog changed under the answer: nothing pressed, the old card is gone (a new one follows); over the route the
// hub relays, 409 with the code, 410 once gone.
func TestResolveScreenChanged(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	changed := &rpc.Error{Code: screen.PromptChanged, Message: "变了", Data: json.RawMessage(`{"delivered":"none"}`)}
	put := func(id string) {
		s.fleet.mu.Lock()
		s.ixMapLocked()[id] = &pending{ix: proto.Interaction{ID: id, Kind: proto.IxScreen}, local: func(proto.Resolution) error { return changed }}
		s.fleet.mu.Unlock()
	}
	put("scr_a")
	err := s.resolve("scr_a", proto.Resolution{Option: "1"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != screen.PromptChanged {
		t.Fatalf("got %v", err)
	}
	if len(s.pendingInteractions()) != 0 {
		t.Fatal("the old card stays gone (a new one follows)")
	}
	if err := s.resolve("scr_a", proto.Resolution{}); !errors.Is(err, errNotPending) {
		t.Fatalf("second answer: %v", err)
	}
	put("scr_b")
	h := s.fleetHandler(http.NotFoundHandler())
	for _, want := range []int{http.StatusConflict, http.StatusGone} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/interactions/scr_b/resolve", strings.NewReader(`{"option":"1"}`)))
		if w.Code != want {
			t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body)
		}
		if want == http.StatusConflict && !strings.Contains(w.Body.String(), `"code":"prompt_changed"`) {
			t.Fatal(w.Body.String())
		}
	}
}

// ---- Codex cards ----

func TestCodexCardsFollowTheNodesList(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	s.fleet.mu.Lock()
	s.ixMapLocked()["scr_mac_s1"] = &pending{ix: proto.Interaction{ID: "scr_mac_s1", MachineID: "mac", SessionID: "s1", Kind: proto.IxScreen},
		local: func(proto.Resolution) error { return nil }}
	s.fleet.mu.Unlock()
	ended := endedCards(s)
	q := proto.Interaction{ID: "cxq_1", SessionID: "s1", Kind: proto.IxQuestion, Agent: "codex", ExpiresAt: time.Now().Add(time.Hour),
		Questions: []proto.Question{{Question: "Which color?", Options: []proto.QuestionOption{{Label: "Red"}}}}}
	a := proto.Interaction{ID: "cxa_1", SessionID: "s2", Kind: proto.IxPermission, Agent: "codex", Summary: "curl -sI https://example.com",
		ExpiresAt: time.Now().Add(time.Hour)}
	listCodex(s, q, a)
	if got := pendingIDs(s); len(got) != 2 || got["cxq_1"] != proto.IxQuestion || got["cxa_1"] != proto.IxPermission {
		t.Fatalf("pending %v", got)
	}
	if !ended("scr_mac_s1") {
		t.Fatal("the replaced screen card was never said to be over (the bot's card for it stays open)")
	}
	listCodex(s, q, a)
	listCodex(s, a)
	if got := pendingIDs(s); len(got) != 1 || got["cxa_1"] == "" {
		t.Fatalf("after the question was answered at the computer: %v", got)
	}
	yes := true
	if err := s.resolve("cxa_1", proto.Resolution{Allow: &yes, Always: true}); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	got := n.answers
	n.mu.Unlock()
	if len(got) != 1 || got[0]["id"] != "cxa_1" || got[0]["session_id"] != "s2" ||
		got[0]["resolution"].(map[string]any)["always"] != true {
		t.Fatalf("node asked %v", got)
	}
	if len(pendingIDs(s)) != 0 {
		t.Fatalf("still pending after the answer: %v", pendingIDs(s))
	}
}

// The node could not type the answer (a draft at the computer): the error reaches the app and the card stays; when the
// node says Codex no longer asks it, the card is gone.
func TestCodexCardAnswerRefused(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	listCodex(s, proto.Interaction{ID: "cxq_1", SessionID: "s1", Kind: proto.IxQuestion, Agent: "codex", ExpiresAt: time.Now().Add(time.Hour)})
	n.mu.Lock()
	n.err = &rpc.Error{Code: screen.InputDraft, Message: "draft"}
	n.mu.Unlock()
	err := s.resolve("cxq_1", proto.Resolution{Answers: map[string]string{"Which color?": "Red"}})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != screen.InputDraft {
		t.Fatalf("err %v", err)
	}
	if pendingIDs(s)["cxq_1"] == "" {
		t.Fatal("the card did not come back after a refused answer")
	}
	n.mu.Lock()
	n.err = &rpc.Error{Code: screen.PromptChanged, Message: "gone"}
	n.mu.Unlock()
	_ = s.resolve("cxq_1", proto.Resolution{Answers: map[string]string{"Which color?": "Red"}})
	if len(pendingIDs(s)) != 0 {
		t.Fatalf("a card Codex no longer asks came back: %v", pendingIDs(s))
	}
}

// A refused answer leaves the card on the apps: the last thing the hub hears of it is the card again, not that it is
// over, and the bot's card for it stays open.
func TestCodexCardRefusedStaysOnTheApps(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	ended := endedCards(s)
	listCodex(s, proto.Interaction{ID: "cxq_1", SessionID: "s1", Kind: proto.IxQuestion, Agent: "codex", ExpiresAt: time.Now().Add(time.Hour)})
	n.mu.Lock()
	n.err = &rpc.Error{Code: screen.InputDraft, Message: "draft"}
	n.mu.Unlock()
	if err := s.resolve("cxq_1", proto.Resolution{Answers: map[string]string{"Which color?": "Red"}}); err == nil {
		t.Fatal("a refused answer reported no error")
	}
	last := ""
	ft.f.mu.Lock()
	for _, tl := range ft.f.told {
		switch tl.event {
		case assistantwire.EventFleetInteraction:
			if strings.Contains(string(tl.params), `"id":"cxq_1"`) {
				last = "interaction"
			}
		case assistantwire.EventFleetResolved:
			if strings.Contains(string(tl.params), `"id":"cxq_1"`) {
				last = "interaction_resolved"
			}
		}
	}
	ft.f.mu.Unlock()
	if last != "interaction" {
		t.Fatalf("the hub last heard %q of a card that is still open", last)
	}
	if ended("cxq_1") {
		t.Fatal("the bot was told a card that is still open is over")
	}
}

// A node that reconnects lists its open cards again: they come back without ringing the phone a second time.
func TestCodexCardsRelistedQuietly(t *testing.T) {
	ft, _ := newCardFleet(t, false)
	s := ft.s
	q := proto.Interaction{ID: "cxq_1", SessionID: "s1", Kind: proto.IxQuestion, Agent: "codex", ExpiresAt: time.Now().Add(time.Hour)}
	listCodex(s, q)
	s.NodeDetached("mac") // the node went away
	if len(pendingIDs(s)) != 0 {
		t.Fatal("not cancelled with its node")
	}
	listCodex(s, q)
	if pendingIDs(s)["cxq_1"] == "" {
		t.Fatal("not listed again")
	}
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, 200*time.Millisecond); len(got) != 1 {
		t.Fatalf("pushed %d times", len(got))
	}
}

// An async question is not on the terminal's keyboard: no screen card for the bare input box, one for a real dialog,
// and an existing screen card stays when the question's card comes.
func TestCodexAsyncQuestionLeavesTheTerminalCard(t *testing.T) {
	ft, n := newCardFleet(t, false)
	s := ft.s
	q := proto.Interaction{ID: "cxq_1", SessionID: "s1", Kind: proto.IxQuestion, Agent: "codex", ToolName: "request_user_input_async",
		ExpiresAt: time.Now().Add(time.Hour)}
	listCodex(s, q)
	blocked := []proto.Session{{SessionID: "s1", Status: proto.StatusBlocked}}
	ft.d.setSessions("mac", blocked)
	s.syncScreenInteractions("mac", blocked)
	time.Sleep(500 * time.Millisecond)
	if got := pendingIDs(s); len(got) != 1 {
		t.Fatalf("a screen card beside the async question for a bare box: %v", got)
	}
	n.setScreen(pickModel)
	s.syncScreenInteractions("mac", blocked)
	deadline := time.Now().Add(5 * time.Second)
	for len(pendingIDs(s)) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	got := pendingIDs(s)
	screens := 0
	for _, kind := range got {
		if kind == proto.IxScreen {
			screens++
		}
	}
	if screens != 1 || got["cxq_1"] == "" {
		t.Fatalf("a dialog on screen beside the async question: %v", got)
	}
	listCodex(s, q, proto.Interaction{ID: "cxq_2", SessionID: "s1", Kind: proto.IxQuestion, Agent: "codex",
		ToolName: "request_user_input_async", ExpiresAt: time.Now().Add(time.Hour)})
	if got := pendingIDs(s); len(got) != 3 {
		t.Fatalf("the screen card went: %v", got)
	}
}

// ---- plans answered in the terminal ----

func planItems(at time.Time) []proto.Item {
	return []proto.Item{
		{ID: "call#0", Seq: 10, TS: at.Add(-time.Millisecond), Role: "assistant", Kind: proto.KindToolUse,
			ToolName: "ExitPlanMode", ToolUseID: "plan-call"},
		{ID: "result#0", Seq: 11, TS: at.Add(time.Second), Role: "user", Kind: proto.KindToolResult,
			ToolUseID: "plan-call", Text: "The plan was answered in the terminal."},
	}
}

func TestPlanCallFinishedRequiresTheMatchingToolResult(t *testing.T) {
	at := time.Now()
	ix := proto.Interaction{CreatedAt: at}
	valid := planItems(at)
	for _, tc := range []struct {
		name  string
		items []proto.Item
		id    string
		want  bool
	}{
		{"id omitted by PermissionRequest", valid, "", true},
		{"exact tool id", valid, "plan-call", true},
		{"wrong tool id", valid, "other-call", false},
		{"still waiting, even if working", valid[:1], "", false},
		{"model says approved, not a result", []proto.Item{valid[0], {Role: "assistant", Kind: proto.KindText, Text: "User approved."}}, "", false},
		{"human says approved, not a result", []proto.Item{valid[0], {Role: "user", Kind: proto.KindText, Text: "Approved."}}, "", false},
		{"another tool finished", []proto.Item{valid[0], {Role: "user", Kind: proto.KindToolResult, ToolUseID: "bash", TS: at.Add(time.Second)}}, "", false},
		{"old plan", planItems(at.Add(-time.Minute)), "", false},
		{"plan created later", planItems(at.Add(time.Minute)), "", false},
		{"two candidate calls", append(append([]proto.Item{}, valid...), proto.Item{Role: "assistant", Kind: proto.KindToolUse,
			ToolName: "ExitPlanMode", ToolUseID: "other-plan", TS: at.Add(-2 * time.Millisecond)}), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix.ToolUseID = tc.id
			if got := planCallFinished(ix, tc.items); got != tc.want {
				t.Fatalf("finished = %v, want %v", got, tc.want)
			}
		})
	}
	for _, mutate := range []func([]proto.Item){
		func(x []proto.Item) { x[0].TS = time.Time{} },
		func(x []proto.Item) { x[1].TS = time.Time{} },
		func(x []proto.Item) { x[1].TS = at.Add(-time.Second) },
		func(x []proto.Item) { x[1].Role = "assistant" },
	} {
		x := planItems(at)
		mutate(x)
		if planCallFinished(proto.Interaction{CreatedAt: at}, x) {
			t.Fatal("missing or earlier completion cannot answer a pending plan")
		}
	}
	valid[1].IsError = true
	if !planCallFinished(proto.Interaction{CreatedAt: at}, valid) {
		t.Fatal("an ended, rejected call must not leave an approval card")
	}
}

// planFleet is a manager whose machine's node reads its transcript pages with read.
func planFleet(t *testing.T, store bool, read func(int) ([]proto.Item, bool, error)) *fleetTest {
	t.Helper()
	ft := newFleetTestNoStore(t)
	if store {
		ft = newFleetTest(t)
	}
	ft.d.mu.Lock()
	ft.d.machines["mac"].call = func(method string, params json.RawMessage) (any, error) {
		if method != "session.items" {
			return nil, fmt.Errorf("unexpected %s", method)
		}
		var p struct {
			Before int `json:"before_seq"`
		}
		_ = json.Unmarshal(params, &p)
		items, more, err := read(p.Before)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": items, "has_more": more}, nil
	}
	ft.d.mu.Unlock()
	return ft
}

func putPending(s *Service, p *pending) {
	s.fleet.mu.Lock()
	s.ixMapLocked()[p.ix.ID] = p
	s.fleet.mu.Unlock()
}

func TestTerminalPlanResultClearsTheCardWithoutApprovingAnything(t *testing.T) {
	for _, trigger := range []string{"sessions", "revs"} {
		t.Run(trigger, func(t *testing.T) {
			at := time.Now()
			ft := planFleet(t, true, func(int) ([]proto.Item, bool, error) { return planItems(at), false, nil })
			s := ft.s
			ix := proto.Interaction{ID: "plan", MachineID: "mac", SessionID: "session", Kind: proto.IxPlan,
				ToolName: "ExitPlanMode", CreatedAt: at, ExpiresAt: at.Add(time.Hour), Resumable: true}
			p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
			putPending(s, p)
			s.record(ix)
			if trigger == "sessions" {
				ft.sessions("mac", proto.Session{SessionID: "session", Status: proto.StatusWorking})
			} else {
				ft.d.setSessions("mac", []proto.Session{{SessionID: "session", Status: proto.StatusWorking}})
				if !ft.event("mac", "sessions.revs", map[string]any{"revs": map[string]int{"session": 1234}}) {
					t.Fatal("the hub no longer gets the revs")
				}
			}
			ft.waitPending(0)
			select {
			case r := <-p.done:
				if !r.Dismiss || r.Approve != nil || r.Allow != nil || r.Mode != "" {
					t.Fatalf("external completion must only release the obsolete hook: %+v", r)
				}
			case <-time.After(time.Second):
				t.Fatal("the old hook stayed blocked")
			}
			if sess := s.decorated("mac")[0]; sess.PendingInteractions != 0 || sess.State == proto.StateNeedsYou {
				t.Fatalf("session still asks for approval: %+v", sess)
			}
			status, res, _ := ft.st.InteractionOutcome(context.Background(), ix.ID)
			if status != "dismissed" || res != nil {
				t.Fatalf("must not record a made-up phone approval: %s %+v", status, res)
			}
			// finished tells the hub after the hook is released: wait for it rather than look once
			waitFor(t, "the hub to get interaction_resolved", func() bool { return len(ft.toldOf(assistantwire.EventFleetResolved)) > 0 })
		})
	}
}

func TestPlanReconcileCrossesAPageAndKeepsUnrelatedApprovals(t *testing.T) {
	at := time.Now()
	items := planItems(at)
	ft := planFleet(t, false, func(before int) ([]proto.Item, bool, error) {
		if before == 0 {
			return items[1:], true, nil
		}
		return items[:1], false, nil
	})
	s := ft.s
	putPending(s, &pending{ix: proto.Interaction{ID: "old-plan", MachineID: "mac", SessionID: "session", Kind: proto.IxPlan,
		ToolName: "ExitPlanMode", CreatedAt: at}, done: make(chan proto.Resolution, 1)})
	permission := &pending{ix: proto.Interaction{ID: "permission", MachineID: "mac", SessionID: "session", Kind: proto.IxPermission}}
	question := &pending{ix: proto.Interaction{ID: "question", MachineID: "mac", SessionID: "session", Kind: proto.IxQuestion}}
	putPending(s, permission)
	putPending(s, question)
	s.syncPlanInteractions("mac")
	ft.waitPending(2)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.interactions["permission"] != permission || s.fleet.interactions["question"] != question {
		t.Fatal("finishing a plan must not answer unrelated questions or permissions")
	}
}

// #3563: Claude Code does not end a PermissionRequest hook whose dialog was answered in the terminal: the tool's card
// stayed on the phone until the hook's timeout. Its tool call's result ends it, as a plan's does; a permission card
// that names no tool call is never guessed at, and one the phone just answered keeps its answer.
func TestTerminalPermissionResultClearsTheCard(t *testing.T) {
	at := time.Now()
	items := []proto.Item{
		{ID: "call#0", Seq: 10, TS: at.Add(-time.Millisecond), Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "bash-call"},
		{ID: "result#0", Seq: 11, TS: at.Add(time.Second), Role: "user", Kind: proto.KindToolResult, ToolUseID: "bash-call", Text: "ok"},
	}
	ft := planFleet(t, true, func(int) ([]proto.Item, bool, error) { return items, false, nil })
	s := ft.s
	ix := proto.Interaction{ID: "perm", MachineID: "mac", SessionID: "session", Kind: proto.IxPermission, ToolName: "Bash",
		ToolUseID: "bash-call", CreatedAt: at, ExpiresAt: at.Add(time.Hour), Resumable: true}
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	putPending(s, p)
	s.record(ix)
	noID := &pending{ix: proto.Interaction{ID: "perm-no-id", MachineID: "mac", SessionID: "session", Kind: proto.IxPermission,
		ToolName: "Bash", CreatedAt: at}, done: make(chan proto.Resolution, 1)}
	putPending(s, noID)
	ft.sessions("mac", proto.Session{SessionID: "session", Status: proto.StatusWorking})
	ft.waitPending(1)
	select {
	case r := <-p.done:
		if !r.Dismiss || r.Allow != nil || r.Always {
			t.Fatalf("a terminal answer must only release the hook: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("the hook stayed blocked")
	}
	if status, res, _ := ft.st.InteractionOutcome(context.Background(), ix.ID); status != "dismissed" || res != nil {
		t.Fatalf("must not record a made-up phone answer: %s %+v", status, res)
	}
	s.fleet.mu.Lock()
	kept := s.fleet.interactions["perm-no-id"] == noID
	s.fleet.mu.Unlock()
	if !kept {
		t.Fatal("a permission card without its tool call's id was dismissed by another call's result")
	}
}

func TestPermissionCallIsNeverGuessed(t *testing.T) {
	at := time.Now()
	// A plan's call and result right before and after the card: a plan without an id takes them, a permission does not.
	if planCallFinished(proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", CreatedAt: at}, planItems(at)) {
		t.Fatal("a permission card without its tool call's id was ended by another call's result")
	}
	for _, p := range []*pending{
		{ix: proto.Interaction{Kind: proto.IxPermission, ToolUseID: "c", Resumable: true, Agent: "codex"}},
		{ix: proto.Interaction{Kind: proto.IxPermission, ToolUseID: "c"}},
		{ix: proto.Interaction{Kind: proto.IxPermission, Resumable: true}},
	} {
		if terminalReconciled(p) {
			t.Fatalf("only a Claude Code hook's permission naming its call is reconciled: %+v", p.ix)
		}
	}
}

func TestTerminalReconcileKeepsAPhoneAnswer(t *testing.T) {
	at := time.Now()
	ft := planFleet(t, false, func(int) ([]proto.Item, bool, error) { return planItems(at), false, nil })
	s := ft.s
	p := &pending{ix: proto.Interaction{ID: "plan", MachineID: "mac", SessionID: "session", Kind: proto.IxPlan,
		ToolName: "ExitPlanMode", ToolUseID: "plan-call", CreatedAt: at}, done: make(chan proto.Resolution, 1), claimed: true}
	putPending(s, p)
	s.checkPlanInteraction("mac", p)
	s.fleet.mu.Lock()
	still := s.fleet.interactions["plan"] == p
	s.fleet.mu.Unlock()
	if !still {
		t.Fatal("a card the phone answered was dismissed under its answer")
	}
	select {
	case r := <-p.done:
		t.Fatalf("the hook got a dismiss instead of the phone's answer: %+v", r)
	default:
	}
}

func TestAnInFlightPlanReadDoesNotDismissANewerCard(t *testing.T) {
	at := time.Now()
	started, release := make(chan struct{}), make(chan struct{})
	ft := planFleet(t, false, func(int) ([]proto.Item, bool, error) {
		close(started)
		<-release
		return planItems(at), false, nil
	})
	s := ft.s
	old := &pending{ix: proto.Interaction{ID: "plan", Kind: proto.IxPlan, ToolName: "ExitPlanMode", CreatedAt: at}}
	putPending(s, old)
	done := make(chan struct{})
	go func() { s.checkPlanInteraction("mac", old); close(done) }()
	<-started
	newer := &pending{ix: proto.Interaction{ID: "plan", Kind: proto.IxPlan, ToolName: "ExitPlanMode", CreatedAt: at.Add(time.Minute)}}
	putPending(s, newer)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read did not finish")
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.interactions["plan"] != newer {
		t.Fatal("an old read removed a new pending plan")
	}
}

func TestPlanProbeFailureKeepsTheCardAndIsThrottled(t *testing.T) {
	var calls atomic.Int32
	ft := planFleet(t, false, func(int) ([]proto.Item, bool, error) {
		calls.Add(1)
		return nil, false, fmt.Errorf("node read failed")
	})
	s := ft.s
	p := &pending{ix: proto.Interaction{ID: "plan", MachineID: "mac", SessionID: "session", Kind: proto.IxPlan,
		ToolName: "ExitPlanMode", CreatedAt: time.Now()}, done: make(chan proto.Resolution, 1)}
	putPending(s, p)
	s.syncPlanInteractions("mac")
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		s.syncPlanInteractions("mac")
	}
	if calls.Load() != 1 {
		t.Fatalf("one pending plan read, got %d", calls.Load())
	}
	ft.waitPending(1)
	select {
	case r := <-p.done:
		t.Fatalf("a failed read must not release the hook: %+v", r)
	default:
	}
}

// #2872: Claude's answer above a notice is often a numbered list. Its numbers are the conversation's words, not the
// notice's options: the card for that screen must carry no dialog (it would offer the prose as buttons, and answering
// one types a digit into whatever holds the terminal's keyboard), and it stays a card upgradeScreenCard can improve.
func TestScreenCardIgnoresANumberedAnswer(t *testing.T) {
	const noticeUnderNumberedAnswer = `❯ fix the flaky test

⏺ I'll do three things:
  1. Fix the race in the watcher
  2. Add a regression test
  3. Push the branch

✻ Cooked for 12s
────────────────────────────────────
 We're changing auto mode to no longer charge for classifier requests in Claude Code.

 Enter to continue · Esc to cancel
`
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(noticeUnderNumberedAnswer)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	got, _ := waitCards(s, 1, 2*time.Second)
	if _, ok := got["scr_mac_s1"]; !ok {
		t.Fatalf("no card for the notice: %v", got)
	}
	for _, ix := range s.pendingInteractions() {
		if ix.SessionID == "s1" && ix.Dialog != nil {
			t.Fatalf("the answer's numbered list became the card's options: %q %+v", ix.Dialog.Question, ix.Dialog.Options)
		}
	}
	n.setScreen(pickModel) // join the placeholder's look-again loop
	waitReplaced(s, "scr_mac_s1", 2*time.Second)
}

// The same Codex question asked again rings the phone again (#3766): after it was handed back to the computer, and
// after it was answered at the computer and asked again a while later. A reconnect's relisting stays quiet.
func TestCodexCardAskedAgainIsAnnounced(t *testing.T) {
	old := codexAskedAgain
	codexAskedAgain = 50 * time.Millisecond
	t.Cleanup(func() { codexAskedAgain = old })
	ft, _ := newCardFleet(t, false)
	s := ft.s
	a := proto.Interaction{ID: "cxa_1", SessionID: "s2", Kind: proto.IxPermission, Agent: "codex", Summary: "npm test",
		ExpiresAt: time.Now().Add(time.Hour)}
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(got) != 1 {
		t.Fatalf("first: pushed %d times", len(got))
	}

	// Handed back to the computer, then asked again: a push.
	if err := s.resolve("cxa_1", proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	listCodex(s)
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(got) != 2 {
		t.Fatalf("after the hand-back: pushed %d times", len(got))
	}

	// Gone from the list for a moment (a failed read, a reconnect): listed again quietly.
	listCodex(s)
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 3, 200*time.Millisecond); len(got) != 2 {
		t.Fatalf("a quick relisting pushed: %d", len(got))
	}

	// Answered at the computer, asked again a while later: a push.
	listCodex(s)
	time.Sleep(2 * codexAskedAgain)
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 3, time.Second); len(got) != 3 {
		t.Fatalf("asked again later: pushed %d times", len(got))
	}
}

// A Codex approval answered from the phone and asked again later rings again (#4105); listed again at once (the
// screen lagging the answer) it stays quiet.
func TestCodexCardAnsweredOnThePhoneAskedAgainIsAnnounced(t *testing.T) {
	old := codexAskedAgain
	codexAskedAgain = 50 * time.Millisecond
	t.Cleanup(func() { codexAskedAgain = old })
	ft, _ := newCardFleet(t, false)
	s := ft.s
	a := proto.Interaction{ID: "cxa_1", SessionID: "s2", Kind: proto.IxPermission, Agent: "codex", Summary: "npm test",
		ExpiresAt: time.Now().Add(time.Hour)}
	yes := true
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(got) != 1 {
		t.Fatalf("first: pushed %d times", len(got))
	}
	if err := s.resolve("cxa_1", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	listCodex(s)
	time.Sleep(2 * codexAskedAgain)
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(got) != 2 {
		t.Fatalf("asked again later: pushed %d times", len(got))
	}
}

// An approval answered on the phone and asked again at once rings at once (#4585): the node lists it again only after
// it saw the dialog leave the screen. A question card answered and listed again at once (its rollout lagging the
// answer) stays quiet for codexAskedAgain.
func TestCodexApprovalAskedAgainAtOnceIsAnnounced(t *testing.T) {
	ft, _ := newCardFleet(t, false)
	s := ft.s
	a := proto.Interaction{ID: "cxa_1", SessionID: "s2", Kind: proto.IxPermission, Agent: "codex", Summary: "npm test",
		ExpiresAt: time.Now().Add(time.Hour)}
	q := proto.Interaction{ID: "cxq_1", SessionID: "s3", Kind: proto.IxQuestion, Agent: "codex", ExpiresAt: time.Now().Add(time.Hour),
		Questions: []proto.Question{{Question: "Which?", Options: []proto.QuestionOption{{Label: "A"}}}}}
	yes := true
	listCodex(s, a, q)
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(got) != 2 {
		t.Fatalf("first: pushed %d times", len(got))
	}
	if err := s.resolve("cxa_1", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := s.resolve("cxq_1", proto.Resolution{Answers: map[string]string{"Which?": "A"}}); err != nil {
		t.Fatal(err)
	}
	listCodex(s, a, q) // within a second: the approval asked again, the question's rollout lagging
	got := ft.pushesOf(assistantwire.NotifyInput, 4, 300*time.Millisecond)
	if len(got) != 3 {
		t.Fatalf("pushed %d times, want the approval again and not the question", len(got))
	}
}

// codexEnded is pruned on its own, also for an id codexAnnounced no longer holds.
func TestCodexEndedPrunedOnItsOwn(t *testing.T) {
	ft, _ := newCardFleet(t, false)
	s := ft.s
	s.fleet.mu.Lock()
	s.fleet.codexEnded = map[string]time.Time{"cxa_old": time.Now().Add(-2 * codexAnnouncedFor), "cxa_new": time.Now()}
	s.fleet.mu.Unlock()
	listCodex(s)
	s.fleet.mu.Lock()
	_, old := s.fleet.codexEnded["cxa_old"]
	_, fresh := s.fleet.codexEnded["cxa_new"]
	s.fleet.mu.Unlock()
	if old || !fresh {
		t.Fatalf("pruned wrong: old kept %v, new kept %v", old, fresh)
	}
}

// shortScreenUpgrade has s's screen cards made without a dialog look again every 50 ms for a second
// (upgradeScreenCard). Call it before s gets a session list: each loop reads it as it starts.
func shortScreenUpgrade(s *Service) {
	s.fleet.upgradeEvery, s.fleet.upgradeFor = 50*time.Millisecond, time.Second
}

// shortScreenQuiet makes screenAnsweredQuiet short for a test, and waits its look after the quiet time out before the
// test ends, so it does not run into the next one.
func shortScreenQuiet(t *testing.T, d time.Duration) {
	old := screenAnsweredQuiet
	screenAnsweredQuiet = d
	t.Cleanup(func() {
		time.Sleep(d + 200*time.Millisecond)
		screenAnsweredQuiet = old
	})
}

// onlyCard is the one pending card's id.
func onlyCard(t *testing.T, s *Service) string {
	t.Helper()
	got, _ := waitCards(s, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("cards %v", got)
	}
	for id := range got {
		return id
	}
	return ""
}

// A screen card answered from a notification while the node's next list still calls the agent blocked and the
// answered dialog is still on screen (the keys not landed yet): no second card for it, no second push. The phone
// would ring again for what was just answered, and answering that card again presses the keys twice (#4391).
func TestScreenCardJustAnsweredIsNotAskedAgain(t *testing.T) {
	for _, c := range []struct{ name, screen string }{
		{"a dialog", pickModel},
		{"no dialog read", "  Waiting for something\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			shortScreenQuiet(t, time.Second)
			ft, n := newCardFleet(t, false)
			s := ft.s
			if c.screen != pickModel {
				s.fleet.upgradeEvery, s.fleet.upgradeFor = 20*time.Millisecond, 600*time.Millisecond
			}
			n.setScreen(c.screen)
			blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
			id := onlyCard(t, s)
			if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(pushes) != 1 {
				t.Fatalf("first: pushed %d times", len(pushes))
			}
			if err := s.resolve(id, proto.Resolution{Keys: []string{"enter"}, Option: map[bool]string{true: "1"}[c.screen == pickModel]}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(400 * time.Millisecond)                                // past the first looks again (recheckScreen)
			blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"}) // still blocked, the same screen
			if got, _ := waitCards(s, 1, 300*time.Millisecond); len(got) != 0 {
				t.Fatalf("the answered dialog got a card again: %v", got)
			}
			if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, 100*time.Millisecond); len(pushes) != 1 {
				t.Fatalf("the answered dialog was pushed again: %d pushes", len(pushes))
			}
			n.setScreen("  ⏺ Thinking…\n") // the answer took
			ft.sessions("mac", proto.Session{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking})
		})
	}
}

// A screen card handed back to the computer ("Hand back to the computer", a dismiss): nothing is pressed and the dialog stays on screen, so
// the node keeps listing the agent blocked. The same dialog gets no new card and no second push, however long it
// stays; once the agent moves on, the next dialog is asked as usual (#4867).
func TestScreenCardHandedBackIsNotAskedAgain(t *testing.T) {
	shortScreenQuiet(t, 200*time.Millisecond)
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	id := onlyCard(t, s)
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(pushes) != 1 {
		t.Fatalf("first: pushed %d times", len(pushes))
	}
	if err := s.resolve(id, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	for range 2 { // still blocked on the same dialog, past the answered quiet time too
		time.Sleep(300 * time.Millisecond)
		blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
		if got, _ := waitCards(s, 1, 300*time.Millisecond); len(got) != 0 {
			t.Fatalf("the dialog handed back got a card again: %v", got)
		}
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, 100*time.Millisecond); len(pushes) != 1 {
		t.Fatalf("the dialog handed back was pushed again: %d pushes", len(pushes))
	}
	n.setScreen("  ⏺ Thinking…\n") // handled at the computer: the agent works again
	ft.sessions("mac", proto.Session{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking})
	n.setScreen(pickModel) // and later asks the same thing: a new question
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got := onlyCard(t, s); got != id {
		t.Fatalf("the next ask is card %q, want %q", got, id)
	}
}

// A card handed back before its dialog could be read is that dialog once it is drawn; a dialog handed back and then
// a screen with something else on it (no dialog read) is another question, asked (#4867).
func TestScreenCardHandedBackHalfDrawn(t *testing.T) {
	t.Run("handed back before the dialog was drawn", func(t *testing.T) {
		shortScreenQuiet(t, 200*time.Millisecond)
		ft, n := newCardFleet(t, false)
		s := ft.s
		n.setScreen("  Waiting for something\n")
		blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
		if err := s.resolve(onlyCard(t, s), proto.Resolution{Dismiss: true}); err != nil {
			t.Fatal(err)
		}
		n.setScreen(pickModel)
		for range 2 {
			blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
			if got, _ := waitCards(s, 1, 400*time.Millisecond); len(got) != 0 {
				t.Fatalf("the dialog handed back half drawn got a card: %v", got)
			}
		}
	})
	t.Run("something else after the dialog handed back", func(t *testing.T) {
		shortScreenQuiet(t, 200*time.Millisecond)
		ft, n := newCardFleet(t, false)
		s := ft.s
		n.setScreen(pickModel)
		blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
		first := onlyCard(t, s)
		if err := s.resolve(first, proto.Resolution{Dismiss: true}); err != nil {
			t.Fatal(err)
		}
		n.setScreen("  Waiting for something\n")
		blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
		if got := onlyCard(t, s); got == first {
			t.Fatalf("the next question is the card handed back: %q", got)
		}
	})
}

// The answer did not take: the same dialog still there once the quiet time is over is asked again, once.
func TestScreenCardAnswerThatDidNotTakeIsAskedAgain(t *testing.T) {
	shortScreenQuiet(t, 600*time.Millisecond)
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	id := onlyCard(t, s)
	if err := s.resolve(id, proto.Resolution{Option: "1"}); err != nil {
		t.Fatal(err)
	}
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, _ := waitCards(s, 1, 200*time.Millisecond); len(got) != 0 {
		t.Fatalf("a card before the quiet time is over: %v", got)
	}
	if got, _ := waitCards(s, 1, 2*time.Second); got[id] == "" {
		t.Fatalf("the dialog still on screen got no card: %v", got)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 3, 300*time.Millisecond); len(pushes) != 2 {
		t.Fatalf("pushed %d times, want 2", len(pushes))
	}
}

// The agent worked between two prompts that read the same: the second one is a new question, asked at once.
func TestScreenCardSameDialogAfterWorkingIsAsked(t *testing.T) {
	shortScreenQuiet(t, time.Second)
	ft, n := newCardFleet(t, false)
	s := ft.s
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	id := onlyCard(t, s)
	if err := s.resolve(id, proto.Resolution{Option: "1"}); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", proto.Session{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking})
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if got, took := waitCards(s, 1, 2*time.Second); got[id] == "" || took > 500*time.Millisecond {
		t.Fatalf("the prompt asked again: %v after %v", got, took)
	}
	if pushes := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(pushes) != 2 {
		t.Fatalf("pushed %d times, want 2", len(pushes))
	}
}

// An answer the node refuses with prompt_changed ends the card too: the same approval asked again later rings.
func TestCodexCardPromptChangedAskedAgainIsAnnounced(t *testing.T) {
	old := codexAskedAgain
	codexAskedAgain = 50 * time.Millisecond
	t.Cleanup(func() { codexAskedAgain = old })
	ft, n := newCardFleet(t, false)
	s := ft.s
	a := proto.Interaction{ID: "cxa_1", SessionID: "s2", Kind: proto.IxPermission, Agent: "codex", Summary: "npm test",
		ExpiresAt: time.Now().Add(time.Hour)}
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(got) != 1 {
		t.Fatalf("first: pushed %d times", len(got))
	}
	n.mu.Lock()
	n.err = &rpc.Error{Code: screen.PromptChanged, Message: "gone"}
	n.mu.Unlock()
	yes := true
	_ = s.resolve("cxa_1", proto.Resolution{Allow: &yes})
	if len(pendingIDs(s)) != 0 {
		t.Fatalf("still pending after prompt_changed: %v", pendingIDs(s))
	}
	n.mu.Lock()
	n.err = nil
	n.mu.Unlock()
	listCodex(s, a) // listed again at once (the screen lagging): quiet
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, 200*time.Millisecond); len(got) != 1 {
		t.Fatalf("listed again at once after prompt_changed: pushed %d times", len(got))
	}
	n.mu.Lock()
	n.err = &rpc.Error{Code: screen.PromptChanged, Message: "gone"}
	n.mu.Unlock()
	_ = s.resolve("cxa_1", proto.Resolution{Allow: &yes})
	n.mu.Lock()
	n.err = nil
	n.mu.Unlock()
	listCodex(s)
	time.Sleep(2 * codexAskedAgain)
	listCodex(s, a)
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(got) != 2 {
		t.Fatalf("asked again after prompt_changed: pushed %d times", len(got))
	}
}
