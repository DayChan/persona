package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/proto"
)

// The manager's part of persona_settings (docs/assistant/assistant.md "Operating Persona itself"): the settings of what moved here
// from the hub — the inbox's rules, the mail accounts, a source's own triage rules (self_settings_sources.go). The hub lists them with
// its own (extwire.HubSettingsList) and hands an action on one of them here (extwire.HubSettingsTool); the card on
// the phone is the hub's (waitInteraction). The registry is the hub's, moved as it was.

// selfSettings are the manager's settings (mail only with mail set up).
func (h *Manager) selfSettings() []selfSetting {
	l := h.userLang()
	rulesTitle, agentRulesTitle := ss(l, "title_inbox_rules"), ss(l, "title_agent_rules")
	out := append(h.sourceRulesSettings(),
		selfSetting{key: "inbox.rules", title: rulesTitle,
			about: "the user's own rules for sorting the inbox (needs you / for your information / noise), in their words; they win over the defaults " +
				"(a source with rules of its own, inbox.rules.<plugin>, uses those instead)",
			value: `a string (the whole rules text; "" clears it and falls back to hub.json)`,
			get:   func(ctx context.Context) (string, error) { return orNone(h.inboxRules(ctx)), nil },
			set: func(ctx context.Context, v json.RawMessage) (*selfChange, error) {
				return h.textSetting(ctx, v, rulesTitle, h.inboxRules, "inbox.rules")
			}},
		selfSetting{key: "inbox.agent_rules", title: agentRulesTitle,
			about: "the user's guidance for linking a needs-you inbox item to one of their running tasks; which items the AI twin takes on " +
				"is set by inbox policies (POST /api/v1/inbox/policies), not here; " + h.twinState(),
			value: `a string ("" clears it)`,
			get: func(ctx context.Context) (string, error) {
				return h.withTwinOff(orNone(h.inboxAgentRules(ctx))), nil
			},
			set: func(ctx context.Context, v json.RawMessage) (*selfChange, error) {
				c, err := h.textSetting(ctx, v, agentRulesTitle, h.inboxAgentRules, "inbox.agent_rules")
				if err != nil || c.apply == nil {
					return c, err
				}
				apply := c.apply
				c.apply = func(ctx context.Context, values map[string]string) (string, error) {
					out, err := apply(ctx, values)
					if err != nil {
						return out, err
					}
					return h.withTwinOff(out), nil
				}
				return c, nil
			}})
	if h.mail != nil {
		out = append(out, selfSetting{key: "mail.accounts", title: ss(l, "title_mail"),
			about: "the user's mail accounts: Persona reads them in the inbox and mail_* use them. add connects (IMAP and SMTP) before saving; " +
				"the password is typed on the phone, never pass one. Gmail can only be added in the app (Google sign-in)",
			value: `add: {"email", "provider": "imap|feishu" (default imap; feishu = Feishu / Lark mail), "name"?, "username"?, "imap_host"?, ` +
				`"imap_port"?, "smtp_host"?, "smtp_port"?, "smtp_security"?: "tls|starttls"}; most providers need only the email. remove: the account id`,
			get: h.getMailAccounts, add: h.addMailAccount, remove: h.removeMailAccount})
	}
	out = append(out, h.extensionSettings()...) // extension.<id> (self_settings_extensions.go)
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// settingsList is extwire.HubSettingsList's answer.
func (h *Manager) settingsList() []extwire.Setting {
	out := []extwire.Setting{}
	for _, s := range h.selfSettings() {
		out = append(out, extwire.Setting{Key: s.key, Title: s.title, About: s.about, Value: s.value, Actions: s.actions()})
	}
	return out
}

// selfSetting is one thing Persona may change. A value setting has set; a list has add / remove.
type selfSetting struct {
	key, title, about string
	value             string // what set / add take as value, for the model
	get               func(ctx context.Context) (string, error)
	set               func(ctx context.Context, v json.RawMessage) (*selfChange, error)
	add               func(ctx context.Context, v json.RawMessage) (*selfChange, error)
	remove            func(ctx context.Context, id string) (*selfChange, error)
}

// selfChange is a change waiting for the phone: what the card says, what it asks for, and how it is made.
type selfChange struct {
	summary string
	fields  []proto.Field
	apply   func(ctx context.Context, values map[string]string) (string, error)
}

func (s selfSetting) actions() []string {
	a := []string{"get"}
	if s.set != nil {
		a = append(a, "set")
	}
	if s.add != nil {
		a = append(a, "add")
	}
	if s.remove != nil {
		a = append(a, "remove")
	}
	return a
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return s
}

// ss is a hub-self_settings text in l.
func ss(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "hub-self_settings."+name, args...)
}

// clipped is a value as the card shows it, in l.
func clipped(l i18n.Lang, s string) string {
	if strings.TrimSpace(s) == "" {
		return ss(l, "none")
	}
	return clip(oneLine(s), 120)
}

// changeSummary is the card's "change title: before → after" (in Chinese).
func changeSummary(l i18n.Lang, title, before, after string) string {
	return ss(l, "change", i18n.Args{"title": title, "before": clipped(l, before), "after": clipped(l, after)})
}

func (h *Manager) settingText(ctx context.Context, key, fallback string) string {
	if h.store != nil {
		var v string
		if ok, err := h.store.Setting(ctx, key, &v); err == nil && ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(fallback)
}

func (h *Manager) inboxRules(ctx context.Context) string {
	return h.settingText(ctx, "inbox.rules", h.cfg.Inbox.Rules)
}
func (h *Manager) inboxAgentRules(ctx context.Context) string {
	return h.settingText(ctx, "inbox.agent_rules", h.cfg.Inbox.Agent.Rules)
}

// twinState tells the assistant, next to inbox.agent_rules, whether the AI twin runs at all: with it off, a rule
// written there drafts nothing, yet the assistant told the user drafts would come (#5865).
func (h *Manager) twinState() string {
	if h.cfg.Inbox.Agent.Enabled {
		return "the AI twin is on"
	}
	return "the AI twin is off on this hub (inbox.agent.enabled is false): it drafts nothing for any item, whatever this setting " +
		"or the inbox policies say, until the user turns it on"
}

// withTwinOff adds to what reading or changing inbox.agent_rules returns that the AI twin is off, when it is.
func (h *Manager) withTwinOff(s string) string {
	if h.cfg.Inbox.Agent.Enabled {
		return s
	}
	return s + "\nNote: " + h.twinState() + "."
}

// textSetting changes a text setting kept in the manager's settings under key ("" deletes the override).
func (h *Manager) textSetting(ctx context.Context, v json.RawMessage, title string, now func(context.Context) string, key string) (*selfChange, error) {
	var text string
	if err := json.Unmarshal(v, &text); err != nil {
		return nil, errors.New("value must be a string")
	}
	text = strings.TrimSpace(text)
	before := now(ctx)
	l := h.userLang()
	if text == before {
		return &selfChange{summary: ss(l, "no_change", i18n.Args{"title": title})}, nil
	}
	if h.store == nil {
		return nil, errors.New("no database on this manager")
	}
	return &selfChange{summary: changeSummary(l, title, before, text),
		apply: func(ctx context.Context, _ map[string]string) (string, error) {
			if err := h.store.PutSetting(ctx, key, text); err != nil {
				return "", err
			}
			return title + " updated; it applies from the next items sorted.", nil
		}}, nil
}

func (h *Manager) getMailAccounts(context.Context) (string, error) {
	as, err := h.mail.ListAccounts()
	if err != nil {
		return "", err
	}
	if len(as) == 0 {
		return "（无）", nil
	}
	var b strings.Builder
	for _, a := range as {
		fmt.Fprintf(&b, "- %s (id %s): %s, provider %s\n", a.Name, a.ID, a.Email, a.Provider)
	}
	return b.String(), nil
}

func (h *Manager) addMailAccount(_ context.Context, v json.RawMessage) (*selfChange, error) {
	l := h.userLang()
	req, summary, err := mailAccountRequest(l, v)
	if err != nil {
		return nil, err
	}
	return &selfChange{summary: summary,
		fields: []proto.Field{{Key: "password", Label: ss(l, "field_password"), Placeholder: ss(l, "field_password_hint"), Secret: true}},
		apply: func(_ context.Context, values map[string]string) (string, error) {
			req.Password = values["password"]
			a, err := h.mail.AddAccount(req)
			if err != nil {
				return "", fmt.Errorf("could not add %s: %w (nothing saved; a login error usually means the provider wants an app password / 授权码)", req.Email, err)
			}
			h.broadcast(map[string]any{"type": "mail_accounts"})
			return fmt.Sprintf("Added %s (account id %s, provider %s): the inbox reads it from its next collection; mail_* can use it now.",
				a.Email, a.ID, a.Provider), nil
		}}, nil
}

// mailAccountRequest reads add's value into the request and what the card says. Gmail is an error: Google sign-in
// only happens in the app.
func mailAccountRequest(l i18n.Lang, v json.RawMessage) (mail.AddAccountRequest, string, error) {
	var a struct {
		Email        string `json:"email"`
		Provider     string `json:"provider"`
		Name         string `json:"name"`
		Username     string `json:"username"`
		IMAPHost     string `json:"imap_host"`
		IMAPPort     int    `json:"imap_port"`
		SMTPHost     string `json:"smtp_host"`
		SMTPPort     int    `json:"smtp_port"`
		SMTPSecurity string `json:"smtp_security"`
	}
	if err := json.Unmarshal(v, &a); err != nil {
		return mail.AddAccountRequest{}, "", fmt.Errorf("value: %w", err)
	}
	email := strings.ToLower(strings.TrimSpace(a.Email))
	if !strings.Contains(email, "@") {
		return mail.AddAccountRequest{}, "", errors.New("email is required")
	}
	if a.Provider == "gmail" || strings.HasSuffix(email, "@gmail.com") || strings.HasSuffix(email, "@googlemail.com") {
		return mail.AddAccountRequest{}, "", errors.New("Gmail is added with Google sign-in, which only works in the app: " +
			"ask the user to open 设置 › 邮箱 › 添加邮箱, pick Gmail and tap 使用 Google 账号登录")
	}
	provider := a.Provider
	if provider == "" {
		provider = mail.ProviderIMAP
	}
	req := mail.AddAccountRequest{Name: strings.TrimSpace(a.Name), Email: email, Provider: provider, Auth: mail.AuthPassword,
		Username: a.Username, IMAPHost: a.IMAPHost, IMAPPort: a.IMAPPort, SMTPHost: a.SMTPHost, SMTPPort: a.SMTPPort,
		SMTPSecurity: a.SMTPSecurity}
	servers := ss(l, "servers_auto")
	if provider == mail.ProviderFeishu {
		servers = ss(l, "servers_feishu")
	} else if a.IMAPHost != "" || a.SMTPHost != "" {
		servers = fmt.Sprintf("IMAP %s · SMTP %s", hostPort(l, a.IMAPHost, a.IMAPPort), hostPort(l, a.SMTPHost, a.SMTPPort))
	}
	summary := ss(l, "add_mail", i18n.Args{"email": email, "servers": servers})
	// QQ / NetEase sign in with an authorization code, not the account's password: the card says where to make one
	// (#5123), unless the servers were given (then they may not be the provider's).
	if g := mail.PasswordGuide(l, email); g != "" && provider == mail.ProviderIMAP && a.IMAPHost == "" && a.SMTPHost == "" {
		summary += "\n" + g
	}
	return req, summary, nil
}

func hostPort(l i18n.Lang, host string, port int) string {
	switch {
	case host == "":
		return ss(l, "auto")
	case port == 0:
		return host
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func (h *Manager) removeMailAccount(_ context.Context, id string) (*selfChange, error) {
	as, err := h.mail.ListAccounts()
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		if a.ID == id || strings.EqualFold(a.Email, id) {
			acct := a
			return &selfChange{summary: ss(h.userLang(), "remove_mail", i18n.Args{"email": acct.Email}),
				apply: func(context.Context, map[string]string) (string, error) {
					if err := h.mail.DeleteAccount(acct.ID); err != nil {
						return "", err
					}
					h.broadcast(map[string]any{"type": "mail_accounts"})
					return "Removed " + acct.Email + ".", nil
				}}, nil
		}
	}
	return nil, fmt.Errorf("no mail account %q (get lists them)", id)
}

func (h *Manager) toolPersonaSettings(ctx context.Context, machineID, sessionID string, args json.RawMessage) (string, error) {
	var a struct {
		Action string          `json:"action"`
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
		ID     string          `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	all := h.selfSettings()
	var s *selfSetting
	for i := range all {
		if all[i].key == a.Key {
			s = &all[i]
		}
	}
	if s == nil {
		return "", fmt.Errorf("no setting %q (action list shows them)", a.Key)
	}
	var change *selfChange
	var err error
	switch {
	case a.Action == "get":
		return s.get(ctx)
	case a.Action == "set" && s.set != nil:
		change, err = s.set(ctx, a.Value)
	case a.Action == "add" && s.add != nil:
		change, err = s.add(ctx, a.Value)
	case a.Action == "remove" && s.remove != nil:
		change, err = s.remove(ctx, a.ID)
	default:
		return "", fmt.Errorf("%s does not take %q (it takes %s)", s.key, a.Action, strings.Join(s.actions(), ", "))
	}
	if err != nil {
		return "", err
	}
	if change.apply == nil {
		return change.summary, nil // nothing to change
	}
	shown, _ := json.Marshal(map[string]string{"setting": s.title, "action": a.Action})
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machineID, SessionID: sessionID, Kind: proto.IxPermission,
		ToolName: "persona_settings", Summary: change.summary, ToolInput: shown, Fields: change.fields, SessionTitle: "Persona",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(15 * time.Minute).UTC()}
	r, err := h.waitInteraction(ctx, ix)
	if err != nil {
		return "", err
	}
	if r.Allow == nil || !*r.Allow {
		return "The user declined on the phone: nothing changed.", nil
	}
	for _, f := range change.fields {
		if !f.Optional && strings.TrimSpace(r.Values[f.Key]) == "" {
			return "", fmt.Errorf("%s was left empty on the phone: nothing changed", f.Label)
		}
	}
	return change.apply(ctx, r.Values)
}
