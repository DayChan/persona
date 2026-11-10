package node

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// A runtime that runs Claude Code through its SDK (Paseo, docs/integrations/paseo.md) has sessions whose conversation is an
// ordinary Claude transcript on this machine. Its row names that transcript (runtime.sessions' transcript: the Claude
// session id), and the node serves the session's content from it as for an agent in a terminal — items, images, the
// watch, the session info, subagents: the same parser and the same rendering as in herdr. The runtime keeps the rest
// (status, sending, interrupting, cards, modes) and pushes the live text (session.delta) while the phone watches. The
// transcript's own row (a Claude session not running in any pane: "Not running") is not listed beside it. A transcript this
// node cannot find (a config directory it does not know) leaves the content to the runtime.

// runtimeTranscripts reads the transcript of each session in a runtime.sessions push: the Claude session holding its
// conversation, by id. Only ids Claude itself uses (validID).
func runtimeTranscripts(params json.RawMessage) map[string]string {
	var p struct {
		Sessions []struct {
			SessionID  string `json:"session_id"`
			Transcript string `json:"transcript"`
		} `json:"sessions"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	out := map[string]string{}
	for _, s := range p.Sessions {
		if namesTranscript(s.SessionID, s.Transcript) {
			out[s.SessionID] = s.Transcript
		}
	}
	return out
}

// namesTranscript: a row of session id names Claude session t as its transcript.
func namesTranscript(id, t string) bool {
	return t != "" && validID(t) && runtimeIDRe.MatchString(id)
}

// foundTranscripts keeps the transcripts this machine has. One not written yet (a session's first turn just started,
// or a config directory the node does not know) leaves the session's content with the runtime until it is there; the
// next list that names it then moves the session to it (transcriptMoved).
func (n *Node) foundTranscripts(m map[string]string) map[string]string {
	for id, t := range m {
		if n.transcriptPath(t) == "" {
			delete(m, id)
		}
	}
	return m
}

// setTranscripts replaces the transcripts this runtime's sessions are in and returns the sessions whose transcript
// changed (a new one, another one, or none any more).
func (h *runtimeConn) setTranscripts(m map[string]string) (changed []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, t := range m {
		if h.transcripts[id] != t {
			changed = append(changed, id)
		}
	}
	for id := range h.transcripts {
		if _, ok := m[id]; !ok {
			changed = append(changed, id)
		}
	}
	h.transcripts = m
	return changed
}

// transcriptOf is the Claude session holding the conversation of session id, as the runtime answering for it lists it;
// "" for any other session. A subagent of such a session ("<session>@<agent>") is not one: callers split it.
func (b *runtimeRegistry) transcriptOf(id string) string {
	if b == nil || id == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.owner[id]
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.transcripts[id]
}

// runtimeTranscript is the transcript the node serves session id's content from: its runtime names one and this
// machine has it. A subagent ("<session>@<agent>") goes with its session.
func (n *Node) runtimeTranscript(id string) string {
	parent := id
	if p, _, ok := strings.Cut(id, subagentSep); ok {
		parent = p
	}
	t := n.runtimes.transcriptOf(parent)
	if t == "" || n.transcriptPath(t) == "" {
		return ""
	}
	return t
}

// transcriptCalls are the calls about a session that its transcript answers when the runtime names one.
var transcriptCalls = map[string]bool{"session.items": true, "session.item": true, "session.image": true}

// runtimeTranscriptCall serves the watch and the session info of a session of h whose conversation is a transcript
// here (runtimeTranscript); handled is false for the calls the runtime answers as for any of its sessions.
func (n *Node) runtimeTranscriptCall(ctx context.Context, h *runtimeConn, method string, p runtimeCallParams,
	call func(m string, params any) (json.RawMessage, error)) (res any, err error, handled bool) {
	parent, _, sub := strings.Cut(p.SessionID, subagentSep)
	switch {
	case method == "session.watch":
		// The transcript's lines from the node, the live text from the runtime (it pushes no items for this session).
		if err := n.watch(p.SessionID, p.SinceSeq); err != nil {
			return nil, err, true
		}
		if !sub {
			n.runtimes.watch(h, p.SessionID, 0)
			if _, err := call("session.watch", map[string]any{"session_id": p.SessionID, "since_seq": 0, "transcript": true}); err != nil {
				n.logf("runtime %s: watch %s: %v", h.id(), p.SessionID, err)
			}
		}
		return okResult(nil), nil, true
	case method == "session.unwatch":
		n.unwatch(p.SessionID)
		if !sub {
			n.runtimes.unwatch(p.SessionID)
			_, _ = call("session.unwatch", map[string]any{"session_id": p.SessionID})
		}
		return okResult(nil), nil, true
	case method == "session.info":
		info, err := n.sessionInfo(ctx, p.SessionID)
		if err != nil || sub {
			return info, err, true
		}
		// What the runtime runs the session with now: a change from the phone shows before the next prompt.
		if s, ok := h.row(parent); ok {
			for k, v := range map[string]string{"model": s.Model, "permission_mode": s.PermissionMode, "effort": s.Effort} {
				if v != "" {
					info[k] = v
				}
			}
		}
		return info, nil, true
	}
	return nil, nil, false
}

// transcriptMoved follows a session whose transcript the runtime changed (it names one now, another one, or none
// any more: a session created from the phone has none until its first turn): what the node read of the old one goes,
// and while the hub watches the session the watch moves to where its items come from now. The pushes from there carry
// another history, so the apps take the conversation again from the start (docs/protocol/protocol.md "Continuity of items").
func (n *Node) transcriptMoved(h *runtimeConn, id string) {
	n.forgetFile(id)
	if !n.runtimes.isWatched(id) {
		return
	}
	n.unwatch(id)
	if n.runtimeTranscript(id) != "" {
		if err := n.watch(id, 0); err != nil {
			n.logf("runtime %s: watch %s's transcript: %v", h.id(), id, err)
		}
	}
	go n.runtimeWatch(h, id, 0)
}

// forgetFile drops what was read of session id's transcript (it moved to another file).
func (n *Node) forgetFile(id string) {
	n.mu.Lock()
	delete(n.files, id)
	delete(n.paths, id)
	delete(n.pathsAt, id)
	n.mu.Unlock()
}

// isWatched: the hub watches session id, one of a runtime's.
func (b *runtimeRegistry) isWatched(id string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.watching[id]
	return ok
}

// mergeRuntimeTranscript completes the row of a runtime session whose conversation is a transcript on this machine
// with what the transcript says, as for an agent in a terminal: the last words, what it is doing, background tasks,
// subagents, the revision the apps pull on. The runtime's status, title, model and modes stay. Returns the Claude
// session id the transcript is (its own row is left out of the list), "" when the row has none here.
func (n *Node) mergeRuntimeTranscript(s *proto.Session) string {
	t := n.runtimes.transcriptOf(s.SessionID)
	if t == "" {
		return ""
	}
	p := n.transcriptPath(s.SessionID)
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	m := n.meta(p, st)
	if s.Title == "" || s.Title == "Claude Code" {
		s.Title = m.title
	}
	if s.Model == "" {
		s.Model = m.model
	}
	if s.PermissionMode == "" && s.Effort == "" {
		s.PermissionMode, s.Effort = n.currentModes(s.SessionID, m)
	}
	setContext(s, m)
	s.ModelFallback = m.modelFallback
	s.LastText, s.Rev = snippet(m.lastText), st.Size()
	if u := lastUsed(m, st); u.After(s.UpdatedAt) {
		s.UpdatedAt = u.UTC()
	}
	s.Activity, s.ActivityAt = m.activityOf()
	s.WokeByTask = m.wake
	if s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked {
		s.Queued = m.queue
	}
	n.setBackground(s, m) // as for a pane's session: its workflows, and the background tasks not shown as them
	s.Loop, s.Goal, s.Plan = m.loop, m.goal, m.plan
	s.Subagents = n.subagents(s.SessionID)
	return t
}
