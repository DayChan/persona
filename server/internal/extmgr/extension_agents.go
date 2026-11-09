package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The agent_sources extension point (docs/extensions/extensions.md "Personal agent sources"): a plugin's contribution is a source of
// personal agents' sessions in the Agents tab (the hub has none of its own). The hub calls agentsrc.<method> on the
// plugin for what the apps ask (the shapes of agents/wire.go), and the plugin's agentsrc.updated event re-pulls
// sessions in the apps at once, so a plugin with a live connection to its agents needs no polling.

// extensionAgentSource is one agent_sources contribution of a running plugin, as an agents.Source.
type extensionAgentSource struct {
	item extension.AgentSourceItem
	proc *host.Proc
}

func (s *extensionAgentSource) Name() string { return s.item.ID }

func (s *extensionAgentSource) call(ctx context.Context, method string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	raw, err := extension.Targeted(params, s.item.ID)
	if err != nil {
		return err
	}
	return agentSourceError(s.proc.Call(ctx, "agentsrc."+method, raw, out))
}

// agentSourceError maps a plugin's refusal to the status the routes answer: the codes a plugin may give for a
// session it does not have, for something it does not do, or for a send it cannot take now.
func agentSourceError(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return err
	}
	switch re.Code {
	case "not_found":
		return &agents.Error{Status: http.StatusNotFound, Msg: re.Message}
	case "invalid":
		return &agents.Error{Status: http.StatusBadRequest, Msg: re.Message}
	case "conflict":
		return &agents.Error{Status: http.StatusConflict, Msg: re.Message}
	case extension.CodeUnsupported, extension.CodeUnknownMethod:
		return &agents.Error{Status: http.StatusNotImplemented, Msg: re.Message}
	}
	return err
}

// Status: the plugin's own words on top of what the manifest says (the id is always the contribution's).
func (s *extensionAgentSource) Status(ctx context.Context) (agents.Status, error) {
	var st struct {
		Enabled   *bool                  `json:"enabled"`
		Setup     string                 `json:"setup"`
		Providers []agents.AgentProvider `json:"providers"`
		Accounts  []agents.AgentAccount  `json:"accounts"`
	}
	err := s.call(ctx, "status", nil, &st)
	out := agents.Status{Source: s.item.ID, Enabled: true, Setup: st.Setup, Providers: st.Providers, Accounts: st.Accounts}
	if st.Enabled != nil {
		out.Enabled = *st.Enabled
	}
	if err != nil {
		out.Error = err.Error()
		return out, err
	}
	if out.Providers == nil {
		for _, p := range s.item.Providers {
			out.Providers = append(out.Providers, agents.AgentProvider{Provider: p, Connection: "server", Available: true})
		}
	}
	return out, nil
}

// Sessions are the plugin's with their ids made by the hub: <source>:<sessionKey>.
func (s *extensionAgentSource) Sessions(ctx context.Context) ([]agents.Session, error) {
	var res struct {
		Sessions []struct {
			agents.AgentSession
			AvatarURL string `json:"avatarUrl"`
		} `json:"sessions"`
	}
	if err := s.call(ctx, "sessions", nil, &res); err != nil {
		return nil, err
	}
	out := make([]agents.Session, 0, len(res.Sessions))
	for _, x := range res.Sessions {
		if x.SessionKey == "" {
			continue
		}
		out = append(out, agents.Wrap(s.item.ID, x.AgentSession, x.AvatarURL))
	}
	return out, nil
}

func (s *extensionAgentSource) Search(ctx context.Context, q string) ([]agents.SearchHit, error) {
	var res struct {
		Hits []agents.AgentSearchHit `json:"hits"`
	}
	err := s.call(ctx, "search", map[string]any{"q": q}, &res)
	var ae *agents.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotImplemented {
		return nil, nil // a source that does not search has nothing to add
	}
	if err != nil {
		return nil, err
	}
	out := make([]agents.SearchHit, len(res.Hits))
	for i, h := range res.Hits {
		out[i] = agents.SearchHit{ID: s.item.ID + ":" + h.SessionKey, Source: s.item.ID, AgentSearchHit: h}
	}
	return out, nil
}

func (s *extensionAgentSource) Messages(ctx context.Context, key, cursor string, limit int) (*agents.MessagePage, error) {
	var p agents.AgentMessagePage
	if err := s.call(ctx, "messages", map[string]any{"session_key": key, "cursor": cursor, "limit": limit}, &p); err != nil {
		return nil, err
	}
	out := &agents.MessagePage{Messages: p.Messages, HasMore: p.HasMore, NextCursor: p.NextCursor}
	if out.Messages == nil {
		out.Messages = []agents.Message{}
	}
	if p.Session != nil {
		x := agents.Wrap(s.item.ID, *p.Session, "")
		out.Session = &x
	}
	return out, nil
}

func (s *extensionAgentSource) Send(ctx context.Context, key, clientID, text string) (json.RawMessage, error) {
	var res json.RawMessage
	if err := s.call(ctx, "send", map[string]any{"session_key": key, "client_id": clientID, "text": text}, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *extensionAgentSource) DeleteMessage(ctx context.Context, key, clientID string) error {
	return s.call(ctx, "delete_message", map[string]any{"session_key": key, "client_id": clientID}, nil)
}

func (s *extensionAgentSource) MarkRead(ctx context.Context, key string) error {
	err := s.call(ctx, "mark_read", map[string]any{"session_key": key}, nil)
	var ae *agents.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotImplemented {
		return nil // nothing unread to clear
	}
	return err
}

// addAgentSources registers a running plugin's agent_sources with the agents service; a contribution whose id the
// hub already has (another plugin's) is skipped and logged.
func (ps *extensions) addAgentSources(id string, m *extension.Manifest, hp *hubExtension) {
	if ps.h == nil || ps.h.agents == nil || hp.proc == nil {
		return
	}
	for _, item := range m.AgentSources() {
		if ps.h.agents.Add(&extensionAgentSource{item: item, proc: hp.proc}) {
			ps.mu.Lock()
			hp.agentSources = append(hp.agentSources, item.ID)
			ps.mu.Unlock()
			ps.h.agentsChanged([]string{})
		} else {
			ps.logf("plugin %s: agent source %q: the hub has a source by that name already", id, item.ID)
		}
	}
}

// removeAgentSources takes a plugin's sources out of the Agents tab (it stopped, was turned off or removed).
func (ps *extensions) removeAgentSources(hp *hubExtension) {
	if ps.h == nil || ps.h.agents == nil {
		return
	}
	ps.mu.Lock()
	ids := hp.agentSources
	hp.agentSources = nil
	ps.mu.Unlock()
	for _, id := range ids {
		ps.h.agents.Remove(id)
	}
}

// agentSourceUpdated is a plugin's agentsrc.updated {contribution?, session_keys?}: those sessions changed (none: the
// list did). Only for a source this run of it (hp) registered.
func (ps *extensions) agentSourceUpdated(id string, hp *hubExtension, params json.RawMessage) {
	if hp == nil || ps.h == nil || ps.h.agents == nil {
		return
	}
	var ev struct {
		Contribution string   `json:"contribution"`
		SessionKeys  []string `json:"session_keys"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	ps.mu.Lock()
	mine := slices.Clone(hp.agentSources)
	ps.mu.Unlock()
	src := ev.Contribution
	if src == "" && len(mine) == 1 {
		src = mine[0]
	}
	if !slices.Contains(mine, src) {
		ps.logf("plugin %s: agentsrc.updated for %s, which is not its agent source: dropped", id, fmt.Sprintf("%q", src))
		return
	}
	ps.h.agents.Updated(src, ev.SessionKeys)
}
