package assistant

import (
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// Completion policy uses source ancestry, never a title, workspace or task category.
// Claude's native subagents are carried in Subagents; independent child sessions
// from a runtime can carry the same explicit ancestry as Codex.
func completionChild(sess proto.Session) bool {
	return sess.IsSubagent || sess.ParentSessionID != "" || sess.ParentThreadID != "" ||
		(sess.RootSessionID != "" && sess.RootSessionID != sess.SessionID)
}

// Codex children are separate sessions, unlike Claude's embedded Subagents.
// Follow public, machine-scoped IDs, including nested children and missing
// intermediate parents whose source supplied the root. Raw thread IDs are not
// enough to join sessions: two Codex homes can contain the same raw ID.
func (s *Service) completionChildrenRunning(machine string, root proto.Session) bool {
	if completionChild(root) {
		return false
	}
	// Only the running children are copied out of the machine's list; of each row only its parent is kept, the last
	// row of an id counting (a parent no row names is "", as is one whose row has none).
	parentOf := map[string]string{}
	running, _ := s.nodeSessionsWhere(machine, func(sess *proto.Session) bool {
		if sess.ParentSessionID != "" {
			parentOf[sess.SessionID] = sess.ParentSessionID
		} else {
			delete(parentOf, sess.SessionID)
		}
		return sess.SessionID != root.SessionID && completionChild(*sess) &&
			(sess.Status == proto.StatusWorking || sess.Status == proto.StatusBlocked || backgroundRunning(*sess))
	})
	for _, child := range running {
		if child.RootSessionID == root.SessionID {
			return true
		}
		seen := map[string]bool{child.SessionID: true}
		for parent := child.ParentSessionID; parent != "" && !seen[parent]; parent = parentOf[parent] {
			if parent == root.SessionID {
				return true
			}
			seen[parent] = true
		}
	}
	return false
}

// Codex has no Claude Stop signature. Its source transcript revision identifies
// an unchanged completion replayed after a status flap; a new turn advances it.
// Do not deduplicate by answer text: two real turns can have the same answer.
func (s *Service) finishedDuplicate(machine string, sess proto.Session) bool {
	if (sess.Agent != "codex" && !strings.HasPrefix(sess.SourceKind, "codex_")) || sess.Rev <= 0 {
		return s.stopDuplicate(machine, sess.SessionID)
	}
	k := [2]string{machine, sess.SessionID}
	sig := fmt.Sprintf("codex-rev:%d", sess.Rev)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.pushedSig[k] == sig {
		return true
	}
	if s.fleet.pushedSig == nil {
		s.fleet.pushedSig = map[[2]string]string{}
	}
	s.fleet.pushedSig[k] = sig
	return false
}

// stopDuplicate says whether the "finished" push about to go out repeats one already sent: the same reply (by the
// signature the node hashed from the Stop hook's last_assistant_message) with no prompt in between. That is the same
// completion seen twice, as when the status flaps working → done → working → done around one turn. A session without
// hooks, or a Stop without a reply, has no signature and is never held back. Otherwise it records the push. In
// memory: after a manager restart at worst one repeat gets through.
func (s *Service) stopDuplicate(machine, sid string) bool {
	k := [2]string{machine, sid}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	sig := s.fleet.stopSig[k]
	if sig == "" {
		return false
	}
	if s.fleet.pushedSig[k] == sig {
		return true
	}
	if s.fleet.pushedSig == nil {
		s.fleet.pushedSig = map[[2]string]string{}
	}
	s.fleet.pushedSig[k] = sig
	return false
}
