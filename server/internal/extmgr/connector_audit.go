package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// auditStore keeps the audit (plugin_audit): who did what and when, never a secret or a value someone typed.
type auditStore interface {
	AddExtensionAudit(ctx context.Context, id, action, actor string, detail json.RawMessage) error
}

// What someone did to a connector, as plugin_audit records it: the plugin column holds the connector's id, and the
// actions carry a connector_ prefix so they never read as the extension's own (install, auth_signed_in, …) (#5281).
const (
	auditConnectorLoginStart     = "connector_login_start"     // a sign-in started (an empty step)
	auditConnectorSignedIn       = "connector_signed_in"       // {account}: the sign-in is done
	auditConnectorLogout         = "connector_logout"          // signed out: the credential is gone
	auditConnectorEnabled        = "connector_enabled"         // {enabled}: switched on or off
	auditConnectorFeed           = "connector_feed"            // {feed, on}: "Add to knowledge base" / "Add to Inbox"
	auditConnectorOptions        = "connector_options"         // its own settings changed (never their values)
	auditConnectorDataDeleted    = "connector_data_deleted"    // {chats, people, posts, items, inbox}: "Delete saved data"
	auditConnectorArchiveDeleted = "connector_archive_deleted" // {chats, people, posts, items}: "Delete saved knowledge base content"
	auditConnectorAuthSource     = "connector_auth_source"     // {source, machine}: where GitHub's token comes from
	auditConnectorRecall         = "connector_recall"          // {row, action}: a message the ledger has a row for was taken back
)

// The user's per-chat rules (#5278, chat_rules_routes.go): a rule made from a ledger row, and one dropped. The detail names
// the chat and the row it came from, never the row's words.
const (
	auditConnectorChatRuleAdded   = "connector_chat_rule_added"   // {row, chat}: "don't ask again in this chat" was made
	auditConnectorChatRuleRemoved = "connector_chat_rule_removed" // {chat, row?}: such a rule was dropped (off in the gate, or gone from the store)
)

// auditConnector records an action on connector id by whoever made the request (actorOf: device:<id>, or the hub
// token).
func (h *Manager) auditConnector(ctx context.Context, id, action string, detail map[string]any) {
	h.audit(ctx, id, action, actorOf(ctx), detail)
}

// audit records an action on id (a connector, the Feishu bot) by actor in plugin_audit. detail holds only what is safe
// to keep: never a credential, a value someone typed or a setting's value. A record that cannot be written does not
// undo the action, which already happened; it is logged.
func (h *Manager) audit(ctx context.Context, id, action, actor string, detail map[string]any) {
	st, _ := h.store.(auditStore)
	if st == nil {
		return
	}
	raw := json.RawMessage(`{}`)
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			raw = b
		}
	}
	// The action is done even when the app stopped waiting for the answer: its record is kept all the same.
	if err := st.AddExtensionAudit(context.WithoutCancel(ctx), id, action, actor, raw); err != nil {
		h.logf("audit %s %s: %v", id, action, err)
	}
}

// connectorLoginStep takes a step of signing in to connector id (Settings › Connectors, a channel's binding) and
// records the start (an empty step) and the account once it is done, as an extension's own sign-in is recorded. The
// values the user typed go to the connector only.
func (h *Manager) connectorLoginStep(ctx context.Context, id string, l connector.Login, step string, values map[string]string) (connector.Prompt, error) {
	p, err := l.LoginStep(ctx, step, values)
	if err != nil {
		return p, err
	}
	if step == "" {
		h.auditConnector(ctx, id, auditConnectorLoginStart, nil)
	}
	if p.Done {
		var detail map[string]any
		if p.Account != "" {
			detail = map[string]any{"account": p.Account}
		}
		h.auditConnector(ctx, id, auditConnectorSignedIn, detail)
		// A plugin may have revoked its own binding while cleanup failed. Completing a new binding must not
		// leave those old questions behind, even when the same account signs in again.
		return p, h.connectorChannelsUnbound(ctx, id)
	}
	return p, nil
}

// connectorLogout signs out of connector id, records it and clears what its channels waited on.
func (h *Manager) connectorLogout(ctx context.Context, id string, l connector.Login) error {
	if err := l.Logout(ctx); err != nil {
		return err
	}
	h.auditConnector(ctx, id, auditConnectorLogout, nil)
	return h.connectorChannelsUnbound(ctx, id)
}

// connectorChannelsUnbound waits for the assistant to forget every channel backed by this connector. An event
// queued for later could let the first message after rebinding answer a question from the previous binding.
func (h *Manager) connectorChannelsUnbound(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var errs []error
	for _, pc := range h.extensionChannels() {
		if pc.item.Connector == id {
			if err := h.callAssistant(ctx, assistantwire.MethodChannelUnbound, assistantwire.ChannelUnbound{Channel: pc.item.ID}, nil); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// optionsOf is connector id's own settings as they are now (connector.Options), nil when it has none.
func (h *Manager) optionsOf(id string) json.RawMessage {
	o, ok := connector.As[connector.Options](h.connectors.Get(id))
	if !ok {
		return nil
	}
	raw := o.Options()
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return raw
	}
	return out.Bytes()
}

// forgotten is a deletion's record: what went, and "incomplete" when part of it failed (the hub's log says why).
func forgotten(counts map[string]any, err error) map[string]any {
	if err != nil {
		counts["incomplete"] = true
	}
	return counts
}

type actorKey struct{}

// actorAssistant is who the audit names for what the assistant did through its app.calls (turning the Feishu bot's
// features on, switching a channel): not a device's request, and not the hub token either.
const actorAssistant = "assistant"

// withActor names who acts in ctx for the audit (actorOf), over the request's principal.
func withActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}
