package node

import "github.com/nwverse/persona/server/internal/proto"

// A restart takes herdr's panes with it: the agents that ran in them are gone, and herdr brings one back only once a
// client attaches — which nobody does to a headless server (#1879). The conversation then just reads "Ended", and the
// only way to carry on is to dig it out of the history, which is not how the user thinks of it: they had it open.
//
// The hub keeps, per session, the pane Persona started it in (state.put, session_state.go). A session with a pane in that
// record, started before this node ran, that has no live agent now is one the restart cut off. It is listed with
// StatusInterrupted so the apps can offer "Continue" — and "Continue" is the ordinary path for a session that is not running
// (session.start with resume, herdrresume.go). Nothing is resumed on its own: a restart can land in the middle of a
// tool call, and continuing that without the user asking is not ours to do.

// markInterrupted gives the offline rows the restart cut off their own status. It runs over the offline rows only, so
// a session that is running now is never marked.
func (n *Node) markInterrupted(rows []proto.Session, live map[string]bool) {
	for i := range rows {
		s := &rows[i]
		if s.Status != proto.StatusOffline || live[s.SessionID] {
			continue
		}
		st, ok := n.state.get(s.SessionID)
		if !ok || st.Pane == "" || st.StartedAt.IsZero() {
			continue // Persona never started it in a pane: it was the user's own terminal, or a conversation of its own
		}
		if !st.StartedAt.Before(n.bootAt) {
			continue // started by this node: it ended on its own, not with a restart
		}
		s.Status = proto.StatusInterrupted
	}
}
