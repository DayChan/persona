package extmgr

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

// botIdentities reports only stable identifiers of enabled bots, including an installed WeChat extension.
// The WeChat account label is localized and
// truncated, so it cannot identify conflicts; read only the bound user's ID from the sealed connector state.
func (h *Manager) botIdentities() []extwire.BotIdentity {
	if h.isMember() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []extwire.BotIdentity
	h.bots.mu.Lock()
	h.loadBotsLocked(ctx)
	if h.bots.set.Enabled {
		id := ""
		s := h.bots.set
		if s.AppID != "" && s.SecretEnc != "" {
			id = s.AppID
		} else if c := h.cfg.Bots.Feishu; c.AppID != "" && c.AppSecret != "" {
			id = c.AppID
		}
		if id != "" {
			out = append(out, extwire.BotIdentity{Kind: "feishu", ID: id})
		}
	}
	h.bots.mu.Unlock()
	if !h.wechatIdentityEnabled() {
		return out
	}
	data, err := h.secretsFor("wechat").Load(ctx)
	if err != nil || len(data) == 0 {
		return out
	}
	if id := wechatIdentity(data); id != "" {
		out = append(out, extwire.BotIdentity{Kind: "wechat", ID: id})
	}
	return out
}

func (h *Manager) wechatIdentityEnabled() bool {
	return !h.isMember() && h.connectors != nil && h.connectors.Get("wechat") != nil && h.connectors.Enabled("wechat")
}

func wechatIdentity(data []byte) string {
	var state struct {
		Creds *struct {
			UserID string `json:"user_id"`
		} `json:"creds"`
	}
	if json.Unmarshal(data, &state) == nil && state.Creds != nil {
		return state.Creds.UserID
	}
	return ""
}

// The WeChat program also saves its polling cursor and context token here. Only an identity change needs to
// rebuild the full extension report, which may include look assets; never compare or report the secret fields.
func (h *Manager) reportWeChatIdentity(data []byte) {
	id := ""
	if h.wechatIdentityEnabled() {
		id = wechatIdentity(data)
	}
	h.reportMu.Lock()
	previous := ""
	for _, bot := range h.reportBotIdentities {
		if bot.Kind == "wechat" {
			previous = bot.ID
			break
		}
	}
	h.reportMu.Unlock()
	if id != previous {
		h.sendReport()
	}
}
