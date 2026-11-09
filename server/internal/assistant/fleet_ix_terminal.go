package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- plans and permissions answered in the terminal ----

// A plan or a tool's permission answered in the terminal can outlive its PermissionRequest hook: Claude Code does not
// end the hook when its own dialog is answered, so the card stayed on the phone until the hook's timeout (#3563).
// Reconcile the pending card with the completed tool call, not the agent's status or words: working is not proof
// that anyone approved anything.
func (s *Service) syncPlanInteractions(machine string) {
	now := time.Now()
	s.fleet.mu.Lock()
	var check []*pending
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID != machine || !terminalReconciled(p) || p.checking || now.Sub(p.checkedAt) < 2*time.Second {
			continue
		}
		p.checking, p.checkedAt = true, now
		check = append(check, p)
	}
	s.fleet.mu.Unlock()
	for _, p := range check {
		go s.checkPlanInteraction(machine, p)
	}
}

// terminalReconciled: p is a hook's card whose tool call's end the transcript shows: a plan, or a Claude Code hook's
// permission that names its tool call (one without an id is never guessed at; Codex's and a runtime's cards end
// through their own sources). Callers hold s.fleet.mu.
func terminalReconciled(p *pending) bool {
	if p.local != nil || p.claimed {
		return false
	}
	switch p.ix.Kind {
	case proto.IxPlan:
		return p.ix.ToolName == "ExitPlanMode"
	case proto.IxPermission:
		return p.ix.ToolUseID != "" && p.ix.Agent == "" && p.ix.Resumable
	}
	return false
}

func (s *Service) checkPlanInteraction(machine string, p *pending) {
	defer func() {
		s.fleet.mu.Lock()
		p.checking = false
		s.fleet.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var items []proto.Item
	before := 0
	// No transcript-wide scan: most plans are in the latest two turns. A few older pages cover a user continuing
	// after answering in the terminal; pages have the node's item and parse budgets too.
	for page := 0; page < 3; page++ {
		var res struct {
			Items   []proto.Item `json:"items"`
			HasMore bool         `json:"has_more"`
		}
		if err := s.nodeCall(ctx, machine, "session.items", map[string]any{"session_id": p.ix.SessionID, "turns": 2,
			"before_seq": before, "lazy_images": true}, &res); err != nil || len(res.Items) == 0 {
			return
		}
		items = append(items, res.Items...)
		if planCallFinished(p.ix, items) {
			s.finishExternalPlan(p)
			return
		}
		next := res.Items[0].Seq
		if !res.HasMore || next <= 0 || (before > 0 && next >= before) {
			return
		}
		before = next
	}
}

// planCallFinished: items hold the result of the tool call ix asks about. PermissionRequest in some Claude Code
// versions omits tool_use_id. In that case the call immediately preceding this hook is identified by its timestamp;
// ambiguous calls, missing timestamps and earlier results fail closed. The result means only that the call ended
// (approval, rejection or interruption); no resolution is invented.
func planCallFinished(ix proto.Interaction, items []proto.Item) bool {
	id := ix.ToolUseID
	if id == "" {
		if ix.Kind == proto.IxPermission || ix.CreatedAt.IsZero() { // only a plan's call is guessed at
			return false
		}
		for _, it := range items {
			if it.Role != "assistant" || it.Kind != proto.KindToolUse || it.ToolName != "ExitPlanMode" || it.ToolUseID == "" ||
				it.TS.IsZero() || it.TS.After(ix.CreatedAt) || ix.CreatedAt.Sub(it.TS) > 30*time.Second {
				continue
			}
			if id != "" && id != it.ToolUseID {
				return false // two candidate plans: neither can be safely attributed to this hook
			}
			id = it.ToolUseID
		}
	}
	if id == "" {
		return false
	}
	for _, it := range items {
		if it.Role == "user" && it.Kind == proto.KindToolResult && it.ToolUseID == id &&
			!it.TS.IsZero() && !it.TS.Before(ix.CreatedAt) {
			return true
		}
	}
	return false
}

func (s *Service) finishExternalPlan(p *pending) {
	s.fleet.mu.Lock()
	if s.fleet.interactions[p.ix.ID] != p || p.claimed {
		s.fleet.mu.Unlock()
		return // the phone answered (its answer is the history's) or this card was replaced while the read was in flight
	}
	delete(s.fleet.interactions, p.ix.ID)
	if p.orphaned != nil {
		p.orphaned.Stop()
	}
	s.fleet.mu.Unlock()
	s.recordEnd(p.ix.ID, "dismissed", nil)
	select {
	case p.done <- proto.Resolution{Dismiss: true}:
	default:
	}
	s.finished(p)
}
