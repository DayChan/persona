package assistant

import (
	"slices"
	"time"

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
//   - Whether an item notifies (Notify) is the phone's push rule (announce → tell): not a muted agent (mutedAs), nor
//     one whose only cards are questions the assistant is still deciding on (delegating: pushed only once it hands
//     one to the user), as the web app's system notifications have it too (lookingOnly, isMuted).
//
// spec/fixtures/attention holds the manager and the web app to both.
//
// The Live Activity counts otherwise (liveCounts: muted agents left out, questions being decided on counted) and does
// not change.

// linkTextMax and linkDetailMax are how long an item's title and place, and its detail, get in runes.
const (
	linkTextMax   = 80
	linkDetailMax = 60
)

// linkAttention is the summary of the agents and pending interactions (fleetInputs), without Rev and WebURL: the
// devices fill them in for each computer.
func linkAttention(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction) link.Attention {
	counts := func(a fleetInput) bool { return !a.Session.Assistant && !a.Session.Quiet }
	// A question the assistant is still deciding on stands for its agent only when every pending card of the agent is
	// one (lookingOnly): the item is about the card the user has to answer, and an item about such a question never
	// notifies. Beside a card that does not stand (an elicitation) the agent is a screen item.
	looking := lookingOnly(ixs)
	stands := func(ix proto.Interaction) bool {
		return cardStands(ix) && (!ix.Delegating || looking[ix.MachineID+"/"+ix.SessionID])
	}
	needs := needsYou(agents, ixs, counts, stands)
	// The newest first: past the cap the oldest go to More, so an agent that just started waiting is always listed,
	// and Persona Link can notify it.
	slices.SortStableFunc(needs, func(a, b needer) int { return waitingSince(b).Compare(waitingSince(a)) })
	out := link.Attention{NeedsYou: len(needs), Items: []link.AttentionItem{}}
	for i, n := range needs {
		if i == link.AttentionMaxItems {
			out.More = len(needs) - i
			break
		}
		it := linkItem(l, n)
		it.Notify = !it.Muted && !looking[it.MachineID+"/"+it.SessionID]
		out.Items = append(out.Items, it)
	}
	return out
}

// lookingOnly is the agents (machine/session) whose every pending card is a question the assistant is still deciding
// on, as the web app's lookingOnly has them: nothing was pushed for them yet (announce hands such a question to the
// assistant first, and tell pushes it only once it is the user's).
func lookingOnly(ixs []proto.Interaction) map[string]bool {
	looking := map[string]bool{}
	other := map[string]bool{}
	for _, ix := range ixs {
		key := ix.MachineID + "/" + ix.SessionID
		if ix.Delegating {
			looking[key] = true
		} else {
			other[key] = true
		}
	}
	for key := range other {
		delete(looking, key)
	}
	return looking
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

// linkItem is one agent of the summary, by the card that stands for it.
func linkItem(l i18n.Lang, n needer) link.AttentionItem {
	sess := n.agent.Session
	it := link.AttentionItem{MachineID: sess.MachineID, SessionID: sess.SessionID, Title: agentTitle(sess, linkTextMax),
		Place: agentPlace(n.agent.Machine, sess, linkTextMax), Kind: proto.IxScreen, Detail: i18n.T(l, "hub-liveactivity.screen"),
		Muted: n.agent.Muted, Route: sessionPath(sess.MachineID, sess.SessionID, false)}
	if ix := n.card; ix != nil {
		it.Kind, it.Detail, it.InteractionID = ix.Kind, clip(oneLine(interactionDetail(l, *ix)), linkDetailMax), ix.ID
	}
	if since := waitingSince(n); !since.IsZero() {
		it.Since = since.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	return it
}
