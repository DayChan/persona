package assistant

import (
	"cmp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// Persona Link's "Needs you" summary (internal/link/attention.go, docs/operations/link.md "Needs you"): worked out in
// liveLoop from the Live Activity's inputs and handed to the devices, which send it to the computers' Persona Link
// when it changed. It reuses two rules the product has, and writes no third:
//
//   - What it lists and counts is the web app's "Needs you" badge (stateCounts().badge in web/src/lib/sessions.ts, the
//     count iOS shows) as it is: every agent in "Needs you" but quiet tasks' agents and the assistant's conversations,
//     muted ones and ones waiting only on the assistant's decision included.
//   - Whether an item notifies (Notify) is the phone's push rule: once a card of the agent is the user's (pending.shown:
//     told to the apps as one, when the phone is pushed; not while being recorded, nor while the assistant decides on
//     it, nor a screen card whose dialog was not read), and never for a muted agent (mutedAs). The web app's system
//     notifications follow the same rule for agents with cards (lookingOnly, isMuted in web/src/lib); for one in a
//     terminal dialog before its card they ring at once, where the phone waits for the screen card.
//
// spec/fixtures/attention holds the manager and the web app to both.
//
// The Live Activity counts otherwise (liveCounts: muted agents left out, questions being decided on counted) and does
// not change.

// linkTextMax and linkDetailMax are how long an item's title and place, and its detail, get in runes, the ellipsis
// that marks a cut included.
const (
	linkTextMax   = 80
	linkDetailMax = 60
)

// linkAttention is the summary of the agents and pending interactions (fleetInputs), shown the cards the apps were told
// of; without Rev and WebURL: the devices fill them in for each computer. The same input in any order gives the same
// summary: cards and items of the same time are ordered by their ids.
func linkAttention(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction, shown map[string]bool) link.Attention {
	key := func(machine, session string) string { return machine + "/" + session }
	handed := func(ix proto.Interaction) bool { return shown[ix.ID] && !ix.Delegating } // the user's to answer
	ready := map[string]bool{}
	for _, ix := range ixs {
		if handed(ix) {
			ready[key(ix.MachineID, ix.SessionID)] = true
		}
	}
	cards := slices.Clone(ixs)
	slices.SortStableFunc(cards, func(a, b proto.Interaction) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	counts := func(a fleetInput) bool { return !a.Session.Assistant && !a.Session.Quiet }
	// The card that stands for an agent: its oldest one handed to the user; while it has none, its oldest one (one
	// announce has not decided on, or a question the assistant is still deciding on). Beside a handed card that does
	// not stand (an elicitation), the agent is a screen item.
	stands := func(ix proto.Interaction) bool {
		return cardStands(ix) && (handed(ix) || !ready[key(ix.MachineID, ix.SessionID)])
	}
	needs := needsYou(agents, cards, counts, stands)
	// The newest first: past the cap the oldest go to More, so an agent that just started waiting is always listed,
	// and Persona Link can notify it.
	slices.SortStableFunc(needs, func(a, b needer) int {
		return cmp.Or(waitingSince(b).Compare(waitingSince(a)), cmp.Compare(a.agent.Session.MachineID, b.agent.Session.MachineID),
			cmp.Compare(a.agent.Session.SessionID, b.agent.Session.SessionID))
	})
	out := link.Attention{NeedsYou: len(needs), Items: []link.AttentionItem{}}
	for i, n := range needs {
		if i == link.AttentionMaxItems {
			out.More = len(needs) - i
			break
		}
		it := linkItem(l, n)
		// Notifies once a card of the agent is the user's, as the phone is pushed then: not before its card (a terminal
		// dialog is pushed by the screen card made for it), nor after the last one was answered.
		it.Notify = !n.agent.Muted && ready[key(it.MachineID, it.SessionID)]
		out.Items = append(out.Items, it)
	}
	return out
}

// waitingSince is when the agent started waiting: its card's time, else when it went "Needs you" (zero: unknown).
func waitingSince(n needer) time.Time {
	switch {
	case n.card != nil:
		return n.card.CreatedAt
	case !n.agent.Session.StateSince.IsZero():
		return n.agent.Session.StateSince
	}
	return n.agent.Session.UpdatedAt
}

// fit is s in at most n runes, the ellipsis that marks a cut included (clip adds its ellipsis after n runes).
func fit(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return clip(s, n-1)
}

// linkItem is one agent of the summary, by the card that stands for it.
func linkItem(l i18n.Lang, n needer) link.AttentionItem {
	sess := n.agent.Session
	it := link.AttentionItem{MachineID: sess.MachineID, SessionID: sess.SessionID, Title: fit(agentTitle(sess, linkTextMax), linkTextMax),
		Place: fit(agentPlace(n.agent.Machine, sess, linkTextMax), linkTextMax), Kind: proto.IxScreen,
		Detail: i18n.T(l, "hub-liveactivity.screen"), Muted: n.agent.Muted, Route: sessionPath(sess.MachineID, sess.SessionID, false)}
	if ix := n.card; ix != nil {
		it.Kind, it.Detail, it.InteractionID = ix.Kind, fit(oneLine(interactionDetail(l, *ix)), linkDetailMax), ix.ID
	}
	if since := waitingSince(n); !since.IsZero() {
		it.Since = since.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	return it
}
