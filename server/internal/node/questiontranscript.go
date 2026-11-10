package node

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type transcriptQuestion struct {
	id          string
	completed   bool
	ambiguous   bool
	outstanding []string
}

// restoreQuestionFromTranscript recovers an attempt whose Node was replaced before it could keep the PreToolUse
// pairing in memory. PermissionRequest still has no tool_use_id, but the transcript names the call and, when already
// answered in the terminal, its tool_result. The return reports ambiguity: the caller hands that attempt back to the
// terminal instead of waiting for a PostToolUse that may never come.
func (n *Node) restoreQuestionFromTranscript(in hookInput, attempt string, now time.Time) bool {
	if attempt == "" {
		return false
	}
	recovered := n.questionFromTranscript(in)
	if recovered.ambiguous {
		return n.questions.handBack(in, attempt, recovered.outstanding, now)
	}
	if recovered.id != "" {
		n.questions.restore(in, attempt, recovered.id, recovered.completed, now)
	}
	return false
}

// questionFromTranscript finds one matching call in the current turns. More than one identical call is ambiguous:
// even completed + outstanding does not say whether this resumed attempt is the old or the new one.
func (n *Node) questionFromTranscript(in hookInput) transcriptQuestion {
	f, err := n.file(in.SessionID)
	if err != nil || n.refresh(in.SessionID, f) != nil {
		return transcriptQuestion{}
	}
	items, _, _ := f.PageTurns(0, 2, 2000)
	results := map[string]bool{}
	for _, it := range items {
		if it.Role == "user" && it.Kind == proto.KindToolResult && it.ToolUseID != "" {
			results[it.ToolUseID] = true
		}
	}
	key := permissionKey(in)
	var calls []proto.Item
	for _, it := range items {
		if it.Role != "assistant" || it.Kind != proto.KindToolUse || it.ToolName != "AskUserQuestion" || it.ToolUseID == "" {
			continue
		}
		candidate := in
		candidate.ToolInput = it.ToolInput
		if permissionKey(candidate) == key {
			calls = append(calls, it)
		}
	}
	if len(calls) > 1 {
		recovered := transcriptQuestion{ambiguous: true}
		for _, call := range calls {
			if !results[call.ToolUseID] {
				recovered.outstanding = append(recovered.outstanding, call.ToolUseID)
			}
		}
		return recovered
	}
	if len(calls) == 0 {
		return transcriptQuestion{}
	}
	id := calls[0].ToolUseID
	return transcriptQuestion{id: id, completed: results[id]}
}
