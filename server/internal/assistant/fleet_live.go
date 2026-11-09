package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The "Fleet" Live Activity's content (docs/apple/live-activity.md): one activity per phone, on the lock screen and in the
// Dynamic Island, summing up every agent that needs you, is working, or finished unseen. The manager works it out
// (FleetState) from the session lists and the pending interactions, with the island's numbers (FleetStats), and
// tells the hub when it changes (fleet.live); the hub pushes it to each device's activity token at the pace Apple
// allows and gives each device its own part (its island's sides, its lock screen details, its persona).

type (
	FleetState    = assistantwire.FleetState
	FleetStats    = assistantwire.FleetStats
	FleetLoad     = assistantwire.FleetLoad
	FleetItem     = assistantwire.FleetItem
	FleetProgress = assistantwire.FleetProgress
)

const fleetMaxItems = 3

// fleetDetailMax is how long an item's line gets, in runes, before it is clipped (with an ellipsis after);
// fleetTitleMax and fleetPlaceMax its title and place, as the lock screen has room for them.
const (
	fleetDetailMax = 60
	fleetTitleMax  = 24
	fleetPlaceMax  = 32
)

// liveState is what was told the hub last.
type liveState struct {
	mu     sync.Mutex
	sent   string
	signal chan struct{}
}

func (l *liveState) ch() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.signal == nil {
		l.signal = make(chan struct{}, 1)
	}
	return l.signal
}

// liveChanged: what the fleet is made of may have changed; force: tell the hub even when it did not (it may have
// restarted).
func (s *Service) liveChanged(force bool) {
	if !s.fleetOn() {
		return
	}
	if force {
		s.fleet.live.mu.Lock()
		s.fleet.live.sent = ""
		s.fleet.live.mu.Unlock()
	}
	select {
	case s.fleet.live.ch() <- struct{}{}:
	default:
	}
}

// liveLoop tells the hub the fleet when it changed, at most every 100 ms, and looks at the numbers every 15 s; the
// devices get Persona Link's summary of the same agents.
func (s *Service) liveLoop(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	sig := s.fleet.live.ch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sig:
			time.Sleep(100 * time.Millisecond)
		case <-tick.C:
		}
		l := s.lang(context.Background())
		agents, ixs := s.fleetInputs()
		s.tellLive(buildFleetIn(l, agents, ixs))
		s.devs.SetAttention(linkAttention(l, agents, ixs)) // sent on only when it changed
	}
}

func (s *Service) tellLive(st FleetState) {
	msg := assistantwire.FleetLive{State: st, Stats: s.fleetStats()}
	b, _ := json.Marshal(msg)
	l := &s.fleet.live
	l.mu.Lock()
	same := l.sent == string(b)
	l.mu.Unlock()
	if same {
		return
	}
	if s.hub.Tell(assistantwire.EventFleetLive, json.RawMessage(b)) == nil {
		l.mu.Lock()
		l.sent = string(b)
		l.mu.Unlock()
	}
}

// fleetSnapshot computes the fleet from every machine's sessions and the pending interactions.
func (s *Service) fleetSnapshot() FleetState {
	agents, ixs := s.fleetInputs()
	return buildFleetIn(s.lang(context.Background()), agents, ixs)
}

// fleetInputs is every machine's agents as the fleet sees them, and the pending interactions (oldest first).
func (s *Service) fleetInputs() ([]fleetInput, []proto.Interaction) {
	var agents []fleetInput
	for _, m := range s.fleetMachines() {
		for _, sess := range s.decorated(m.ID) {
			if sess.Quiet { // a quiet task's agent: the main conversation tells about it (#1731)
				continue
			}
			agents = append(agents, fleetInput{
				// Its own space, not muted's lookup of the node's row: that scanned the machine's list once per session.
				Machine: m.Name, Session: sess, Muted: s.mutedAs(m.ID, sess.SessionID, sess.SpaceName),
				TurnStart: s.turnsBook().startOf(m.ID + "/" + sess.SessionID),
			})
		}
	}
	return agents, s.pendingInteractions()
}

// round2 keeps two significant digits: 12345 → 12000, 987 → 990. The island shows "12k", so finer changes would
// only spend the push budget.
func round2(n int64) int64 {
	if n < 100 {
		return n
	}
	p := int64(1)
	for n/p >= 100 {
		p *= 10
	}
	return (n + p/2) / p * p
}

// fleetStats turns the usage summary into the island's numbers.
func (s *Service) fleetStats() FleetStats {
	if s.devs == nil {
		return FleetStats{}
	}
	u := s.devs.Usage()
	st := FleetStats{TPM: round2(u.TPM), Tokens: round2(u.Tokens), Requests: int(round2(int64(u.Requests)))}
	for _, m := range u.Machines {
		st.Loads = append(st.Loads, FleetLoad{ID: m.ID, Name: s.machineName(m.ID), Percent: int(math.Round(m.Load*20)) * 5})
	}
	return st
}

// fleetInput is one agent as the fleet sees it.
type fleetInput struct {
	Machine   string // display name
	Session   proto.Session
	Muted     bool
	TurnStart time.Time // zero: unknown
}

// buildFleet sums agents up the way the Agents list does (docs/apple/ios.md): needs you first (oldest request first), then
// working (longest first), then finished unseen. Muted agents, idle / offline ones, and /loop agents waiting for
// their next tick are left out; the assistant only counts when it asks something.
func buildFleet(agents []fleetInput, ixs []proto.Interaction) FleetState {
	return buildFleetIn(i18n.Default, agents, ixs)
}

// needer is an agent that needs the user, and the card that stands for it: its oldest (nil: it waits in a terminal
// dialog without one).
type needer struct {
	agent fleetInput
	card  *proto.Interaction
}

// needsYou is the agents that need the user, one each, as the Agents list has them (#3571): those with a card by their
// oldest card that stands (ixs come oldest first), then those in a terminal dialog without one, in agents' order.
// counts says which agents count at all, stands which cards may stand for one; the assistant's own dialog never
// counts. The Live Activity and Persona Link's summary (fleet_attention.go) differ in those, not in this.
func needsYou(agents []fleetInput, ixs []proto.Interaction, counts func(fleetInput) bool, stands func(proto.Interaction) bool) []needer {
	byKey := map[string]fleetInput{}
	for _, a := range agents {
		if counts(a) {
			byKey[a.Session.MachineID+"/"+a.Session.SessionID] = a
		}
	}
	var out []needer
	asked, carded := map[string]bool{}, map[string]bool{}
	for _, ix := range ixs {
		key := ix.MachineID + "/" + ix.SessionID
		carded[key] = true
		if a, ok := byKey[key]; ok && !asked[key] && stands(ix) {
			asked[key] = true
			out = append(out, needer{agent: a, card: &ix})
		}
	}
	for _, a := range agents {
		key := a.Session.MachineID + "/" + a.Session.SessionID
		// Pending cards in its row but none in ixs: they were answered between the two reads (fleetInputs), so it is
		// not one waiting in a terminal dialog.
		answered := a.Session.PendingInteractions > 0 && !carded[key]
		if counts(a) && !asked[key] && a.Session.State == proto.StateNeedsYou && !a.Session.Assistant && !answered {
			out = append(out, needer{agent: a})
		}
	}
	return out
}

// liveCounts: the agent is on the Live Activity at all (muted agents and Codex sub-agents are not).
func liveCounts(a fleetInput) bool { return !a.Session.IsCodexSubagent() && !a.Muted }

// cardStands: the card may stand for its agent (on the Live Activity and in Persona Link's summary). An elicitation
// does not: the Fleet is shared with legacy clients, whose terminal fallback stays (the agent shows as in a dialog).
func cardStands(ix proto.Interaction) bool { return ix.Kind != proto.IxElicitation }

// buildFleetIn is buildFleet with the texts for people in l (the user's language).
func buildFleetIn(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction) FleetState {
	var st FleetState
	var needs, working, unread []FleetItem
	asked := map[string]bool{}
	for _, n := range needsYou(agents, ixs, liveCounts, cardStands) {
		sess := n.agent.Session
		key := sess.MachineID + "/" + sess.SessionID
		asked[key] = true
		item := FleetItem{ID: key, Title: agentTitle(sess, fleetTitleMax), Place: agentPlace(n.agent.Machine, sess, fleetPlaceMax),
			Kind: proto.IxScreen, Detail: i18n.T(l, "hub-liveactivity.screen")}
		if ix := n.card; ix != nil {
			item.Kind, item.Detail = ix.Kind, clip(oneLine(interactionDetail(l, *ix)), fleetDetailMax)
			item.Interaction, item.Since = ix.ID, unixSeconds(ix.CreatedAt)
			if len(ix.Fields) > 0 {
				item.Interaction = "" // what it asks for (a password) is filled in in the app: no Allow / Deny on the activity
			}
		}
		needs = append(needs, item)
	}
	for _, a := range agents {
		sess := a.Session
		key := sess.MachineID + "/" + sess.SessionID
		if !liveCounts(a) || asked[key] {
			continue
		}
		switch sess.State {
		case proto.StateWorking:
			if sess.Assistant {
				st.Assistant = true
				continue
			}
			since := a.TurnStart
			if sess.BackgroundWorking {
				var started time.Time
				for _, w := range sess.Workflows {
					if w.Running && !w.StartedAt.IsZero() && (started.IsZero() || w.StartedAt.Before(started)) {
						started = w.StartedAt
					}
				}
				if !started.IsZero() {
					since = started
				}
			}
			if since.IsZero() {
				since = sess.UpdatedAt
			}
			it := FleetItem{ID: key, Title: agentTitle(sess, fleetTitleMax), Place: agentPlace(a.Machine, sess, fleetPlaceMax), Kind: "working",
				Since: unixSeconds(since)}
			if sess.BackgroundWorking {
				it.Background, it.Detail = true, backgroundDetail(l, sess)
				it.Progress = backgroundProgress(sess)
			}
			working = append(working, it)
		case proto.StateUnread:
			if sess.Assistant || sess.Loop != nil {
				continue
			}
			unread = append(unread, FleetItem{ID: key, Title: agentTitle(sess, fleetTitleMax), Place: agentPlace(a.Machine, sess, fleetPlaceMax), Kind: "unread",
				Detail: clip(oneLine(firstLine(sess.LastText)), fleetDetailMax), Since: unixSeconds(sess.UpdatedAt)})
		}
	}
	sort.SliceStable(working, func(i, j int) bool { return working[i].Since < working[j].Since })
	sort.SliceStable(unread, func(i, j int) bool { return unread[i].Since > unread[j].Since })
	st.NeedsYou, st.Working, st.Unread = len(needs), len(working), len(unread)
	switch {
	case st.NeedsYou > 0:
		st.Phase = "needsYou"
	case st.Working > 0:
		st.Phase = "working"
	case st.Unread > 0:
		st.Phase = "done"
	}
	if len(working) > 0 {
		st.WorkingSince = working[0].Since
	}
	st.Items = append(append(append([]FleetItem{}, needs...), working...), unread...)
	if len(st.Items) > fleetMaxItems {
		st.Items = st.Items[:fleetMaxItems]
	}
	return st
}

// agentTitle is the agent's title in one line of at most n runes: its project's folder when it has none.
func agentTitle(sess proto.Session, n int) string {
	t := transcript.Title(sess.Title)
	if t == "" || t == "Claude Code" {
		t = path.Base(sess.Cwd)
	}
	return clip(oneLine(t), n)
}

// agentPlace is "machine · space › tab", the subtitle pushes use too, in at most n runes.
func agentPlace(machine string, sess proto.Session, n int) string {
	place := machine
	if sess.SpaceName != "" {
		place += " · " + sess.SpaceName
		if sess.SlotName != "" && sess.SlotName != fmt.Sprint(sess.SlotIndex) {
			place += " › " + sess.SlotName
		}
	}
	return clip(oneLine(place), n)
}

func interactionDetail(l i18n.Lang, ix proto.Interaction) string {
	switch ix.Kind {
	case proto.IxPermission:
		if ix.Summary != "" {
			return ix.Summary
		}
		return ix.ToolName
	case proto.IxPlan:
		return planTitle(ix.Plan)
	case proto.IxQuestion:
		if len(ix.Questions) > 0 {
			return ix.Questions[0].Question
		}
		return ix.Summary
	}
	return i18n.T(l, "hub-liveactivity.screen")
}

func unixSeconds(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.Unix())
}

// shownWorkflow is the running workflow a background-working agent's item names: the first one (Workflows are newest
// first), as the web's workflow line picks it.
func shownWorkflow(sess proto.Session) (proto.Workflow, bool) {
	for _, w := range sess.Workflows {
		if w.Running {
			return w, true
		}
	}
	return proto.Workflow{}, false
}

// backgroundDetail is what a background-working agent runs, in a line: its running workflow ("parity-audit · Verify ·
// 169/185"), else how many background tasks. A long name or phase is what gets clipped, never the counts at the end:
// they are what the bar shows (backgroundProgress), so the line changes whenever the bar does and the bar costs no
// push of its own (#3437).
func backgroundDetail(l i18n.Lang, sess proto.Session) string {
	if w, ok := shownWorkflow(sess); ok {
		parts := []string{cmp.Or(w.Name, w.Summary, "workflow")}
		if w.Phase != "" {
			parts = append(parts, w.Phase)
		}
		counts := fmt.Sprintf(" · %d/%d", w.AgentsDone, w.AgentsStarted)
		return clip(strings.Join(parts, " · "), max(fleetDetailMax-utf8.RuneCountInString(counts), 0)) + counts
	}
	n := len(sess.Background)
	for _, a := range sess.Subagents {
		if a.Running {
			n++
		}
	}
	return clip(i18n.T(l, "hub-liveactivity.background", i18n.Args{"count": n}), fleetDetailMax)
}

// backgroundProgress is the bar under a background-working agent's item (#3437): the counts its detail prints (which
// clipping never cuts), of the same workflow, so the bar and the line beside it agree and the bar never changes the
// content by itself (the push budget, docs/live-activity.md). Nil without a running workflow, or before it started an
// agent.
func backgroundProgress(sess proto.Session) *FleetProgress {
	w, ok := shownWorkflow(sess)
	if !ok || w.AgentsStarted <= 0 {
		return nil
	}
	return &FleetProgress{Done: min(max(w.AgentsDone, 0), w.AgentsStarted), Started: w.AgentsStarted}
}
