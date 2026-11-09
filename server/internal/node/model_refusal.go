package node

import "github.com/nwverse/persona/server/internal/proto"

// mergeModelFallback uses the existing incremental scan: a session-scoped fallback can be far before the quick
// tail, including on the first read after a node restart. A pending scan leaves the tail's evidence alone.
func (n *Node) mergeModelFallback(path string, m *quickMeta) {
	n.mu.Lock()
	sc := n.bgScans[path]
	n.mu.Unlock()
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.pending || sc.off > m.size {
		// The file may grow between the tail read and the scan; a later turn cannot explain this snapshot's error.
		return
	}
	m.modelFallback = nil
	if r := sc.meta.ModelFallback; r != nil {
		copy := *r
		m.modelFallback = &copy
		m.model = r.FallbackModel
	}
	// Only the latest failed turn has a code. Never revive an error cleared by a new prompt or a successful reply.
	if m.turnErr.text != "" {
		m.turnErr.code = ""
		if m.turnErr.text == sc.meta.TurnError {
			m.turnErr.code = sc.meta.TurnErrorCode
		}
		if m.turnErr.code == proto.TurnErrorModelFallbackUnavailable {
			m.turnErr.kind = proto.TurnErrorUser
		}
	}
}
