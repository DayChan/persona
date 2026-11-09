package extmgr

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The knowledge base is the assistant's, in this process (docs/assistant/knowledge.md "Who owns what"): the connectors' sources and
// posts are handed to it here (kb_sources.go, kb_posts.go) as the hub used to hand them over the bus.

// errKBOff is what the knowledge base's sources answer while manager.json turns it off.
var errKBOff = i18n.Err("hub-kb.off")

// kbOff: manager.json kb.enabled is false (the hello the manager keeps says).
func (h *Manager) kbOff() bool {
	hl := h.hello.Load()
	return hl != nil && hl.KB != nil && hl.KB.Off
}

func (h *Manager) kbReadOnly() bool {
	hl := h.hello.Load()
	return hl != nil && hl.KB != nil && !hl.KB.Off && hl.KB.ReadOnly
}

// kbAsk is a call to the knowledge base (assistantwire.MethodKB*).
func (h *Manager) kbAsk(ctx context.Context, method string, params, out any) error {
	err := h.callAssistant(ctx, method, params, out)
	if errors.Is(err, errAssistantOffline) { // coded as the bus did: an extension's program checks the code
		return &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: err.Error()}
	}
	return err
}

// kbAskWaiting is kbAsk; wait is how long the hub waited for the assistant to connect (here it is in the process).
func (h *Manager) kbAskWaiting(ctx context.Context, _ time.Duration, method string, params, out any) error {
	return h.kbAsk(ctx, method, params, out)
}

// kbOffline: err is the knowledge base not being there now (not open yet: try again).
func kbOffline(err error) bool {
	var re *rpc.Error
	return errors.Is(err, errAssistantOffline) ||
		errors.As(err, &re) && (re.Code == assistantwire.CodeAssistantOffline || re.Code == assistantwire.CodeKBUnavailable)
}

// kbKick tells the knowledge base that connector id's chats or items (scope) have something new: it goes round soon
// and reads them at that round.
func (h *Manager) kbKick(id string, scope connector.Scope) {
	if !h.kbOff() && !h.kbReadOnly() {
		h.notifyAssistant(assistantwire.EventKBKick, assistantwire.KBKick{Connector: id, Scope: string(scope)})
	}
}
