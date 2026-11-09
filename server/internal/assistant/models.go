package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/secretkey"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// The model configuration (docs/assistant/model-config.md) is the assistant's: the four categories' records (settings
// model_config.<id>), the providers added in Persona with their keys sealed (models_launchers.go), the hidden ~/.zshrc
// launchers. Only the assistant reads and writes them; the hub hands over what the machines and the plugins have, and
// asks only what its plugins need (models_hub.go). Every model call is made from here, with the keys only the
// assistant opens.

// assistantRuntimes is what GET /model-config offers for the main chat's runtime.
var assistantRuntimes = []string{proto.AssistantRuntimeCLI, proto.AssistantRuntimeAgentSDK}

type modelRecord struct {
	Revision int64        `json:"revision"`
	Binding  ModelBinding `json:"binding"`
}

// ModelProvider is one provider as Settings › Model configuration offers it: never its key.
type ModelProvider struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	Agent    string        `json:"agent,omitempty"`
	API      string        `json:"api,omitempty"`
	Model    string        `json:"model,omitempty"`
	Machines []string      `json:"machines,omitempty"`
	Hidden   bool          `json:"hidden,omitempty"`
	HasKey   bool          `json:"has_key,omitempty"`
	Models   []ModelChoice `json:"models,omitempty"`
	Status   string        `json:"status,omitempty"`
}

type ModelChoice = assistantwire.ModelChoice

type ModelUse struct {
	Name string `json:"name"`
}

// ModelPurpose is one purpose (a category, or a use under one) with what it runs on.
type ModelPurpose struct {
	ID            string       `json:"id"`
	CategoryID    string       `json:"category_id,omitempty"`
	Uses          []ModelUse   `json:"uses,omitempty"`
	Name          string       `json:"name"`
	Group         string       `json:"group"`
	Kind          string       `json:"kind"`
	Parent        string       `json:"parent,omitempty"`
	MachineID     string       `json:"machine_id,omitempty"`
	Override      ModelBinding `json:"override"`
	Effective     ModelBinding `json:"effective"`
	Revision      int64        `json:"revision"`
	InheritedFrom string       `json:"inherited_from,omitempty"`
	Status        string       `json:"status,omitempty"`
	Limit         *ai.Pause    `json:"limit,omitempty"` // the connection is held: quota used up or busy (ai/limit.go)
	Fields        []string     `json:"fields"`
	ProviderIDs   []string     `json:"provider_ids"`
	Runtimes      []string     `json:"runtimes,omitempty"` // assistant only: the runtimes it can run on
}

var modelCategoryIDs = []string{"assistant", "agent.new", "kb", "inbox.triage", "agent.delegate"}

// The catalog also includes an opt-in override; category-default assignment must not write it.
var modelConfigIDs = append(slices.Clone(modelCategoryIDs), purposeLarkAgent)

// modelPurposes are the uses; Name, Group and Uses hold catalog names (assistant-models), read in a language by
// localized.
var modelPurposes = []ModelPurpose{
	{ID: "assistant", Group: "group_default", Kind: "launcher", Runtimes: assistantRuntimes, Uses: []ModelUse{{Name: "use_chat"}, {Name: "use_schedule"}}},
	{ID: "agent.new", Group: "group_default", Kind: "launcher", Uses: []ModelUse{{Name: "use_new_agent"}, {Name: "use_inbox_agent"}, {Name: "use_lark_agent"}}},
	{ID: "kb", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_translate"}, {Name: "use_draft"}, {Name: "use_knowledge"}, {Name: "use_projects"}, {Name: "use_titles"}}},
	{ID: "inbox.triage", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_triage"}, {Name: "use_importance"}, {Name: "use_reply_or_agent"}}},
	// "Answering agents' questions" (delegate.go) is a category of its own, so it can run on a stronger model than the triage's (#3029).
	{ID: "agent.delegate", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_delegate"}}},
	{ID: "inbox.agent", Group: "group_agents", Kind: "launcher", Parent: "agent.new"},
	{ID: "lark.agent", Group: "group_agents", Kind: "launcher", Parent: "agent.new"},
	{ID: "inbox.judge", Group: "group_triage", Kind: "api", Parent: "inbox.triage"},
	{ID: "agent.triage", Group: "group_triage", Kind: "api", Parent: "inbox.triage"},
	{ID: "inbox.draft", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "translate", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.compress", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.project", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.dedupe", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.items", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.moments", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "assistant.title", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "agent.title", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "todo.title", Group: "group_processing", Kind: "api", Parent: "kb"}, // a task's todo (assistant/todo_titles.go)
	// Scheduled tasks (the morning briefing…) run in the assistant's directory with its instructions, memory and tools: they are the
	// assistant's conversations, on the main chat's model, not new Agents (#1544; #697 had them under Agents, plain Claude Code
	// by default, which is not logged in on every hub machine).
	{ID: "schedule", Group: "group_assistant", Kind: "launcher", Parent: "assistant"},
}

// mc is a assistant-models text in l.
func mc(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-models."+name, args...)
}

// purposeName is a built-in use's name in l.
func purposeName(l i18n.Lang, id string) string {
	return mc(l, "name_"+strings.ReplaceAll(id, ".", "_"))
}

// localized is a built-in use's definition with its texts in l.
func (p ModelPurpose) localized(l i18n.Lang) ModelPurpose {
	p.Name, p.Group = purposeName(l, p.ID), mc(l, p.Group)
	if len(p.Uses) > 0 {
		uses := make([]ModelUse, len(p.Uses))
		for i, u := range p.Uses {
			uses[i] = ModelUse{Name: mc(l, u.Name)}
		}
		p.Uses = uses
	}
	return p
}

// requestCtxLang is the language of what ctx is answering: the request's (http.go puts it in the context), else the
// user's.
func (s *Service) requestCtxLang(ctx context.Context) i18n.Lang {
	return i18n.From(ctx).Or(s.lang(ctx))
}

func modelCategoryID(id string) string {
	if strings.HasPrefix(id, "task.") {
		return "assistant"
	}
	for _, p := range modelPurposes {
		if p.ID == id && p.Parent != "" {
			return p.Parent
		}
	}
	return id
}

var errModelPurposeNotFound = i18n.Err("assistant-models.err_purpose_not_found")

var errModelConflict = i18n.Err("assistant-models.err_conflict")

func modelKey(id string) string { return "model_config." + id }

// Lark /agent overrides were retired with category-only settings. A fresh namespace keeps dormant records
// (model_config.lark.agent) inert, including their revisions. Only a new user save creates this record.
func modelRecordKey(id string) string {
	if id == purposeLarkAgent {
		return "model_config.overrides.v1.lark.agent"
	}
	return modelKey(id)
}

func modelRecordID(p ModelPurpose) string {
	if p.CategoryID != "" && p.ID != purposeLarkAgent && !isExtensionPurpose(p.ID) { // those two have records of their own
		return p.CategoryID
	}
	return p.ID
}

// modelsState is the model configuration's: one change at a time, the key that seals the providers' keys, the
// launchers' model lists, the machines' ~/.zshrc launchers as last seen.
type modelsState struct {
	mu sync.Mutex // one change of the configuration at a time (and the test stores without compare-and-set)

	secret *secretkey.Box // secret.key; a key made for this run only in tests
	quotas quotaCache     // the launchers' quota, kept a minute (models_quota.go)

	cacheMu sync.Mutex
	cacheAt map[string]time.Time
	have    map[string]map[string]bool // machine \x00 launcher → its models (ids and what aliases resolve to)

	customMu sync.Mutex // one change of the remembered typed ids at a time (models_custom.go)

	zshrcMu sync.Mutex
	zshrc   map[string][]proto.Launcher // machine → its rc-file launchers (nil: not asked since the start)
	// zshrcSync: how each machine's list came in the last time they were asked (设置 › 模型供应商's 各台电脑).
	zshrcSync map[string]LauncherSync
}

func (s *Service) modelRecord(ctx context.Context, id string) (modelRecord, error) {
	var r modelRecord
	if st := s.store(); st != nil {
		_, err := st.Setting(ctx, modelRecordKey(id), &r)
		return r, err
	}
	return r, nil
}

// saveModelRecord writes purpose id's record if it is still at revision (the database compares and sets; the test
// stores without that rely on the caller holding models.mu).
func (s *Service) saveModelRecord(ctx context.Context, id string, revision int64, b ModelBinding) error {
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-models.err_no_database")
	}
	r := modelRecord{Revision: revision + 1, Binding: b}
	if cas, ok := st.(interface {
		CompareSetting(context.Context, string, int64, any) (bool, error)
	}); ok {
		updated, err := cas.CompareSetting(ctx, modelRecordKey(id), revision, r)
		if err != nil {
			return err
		}
		if !updated {
			return errModelConflict
		}
		return nil
	}
	var old modelRecord
	if _, err := st.Setting(ctx, modelRecordKey(id), &old); err != nil {
		return err
	}
	if old.Revision != revision {
		return errModelConflict
	}
	return st.PutSetting(ctx, modelRecordKey(id), r)
}

// saveModel is saveModelRecord at r's revision, one change at a time.
func (s *Service) saveModel(ctx context.Context, id string, r modelRecord) error {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	return s.saveModelRecord(ctx, id, r.Revision, r.Binding)
}

func (s *Service) modelPurposeDef(ctx context.Context, id string) (ModelPurpose, error) {
	l := s.requestCtxLang(ctx)
	for _, p := range modelPurposes {
		if p.ID == id {
			p = p.localized(l)
			p.CategoryID = modelCategoryID(id)
			if p.Kind == "launcher" && p.ID != "agent.new" {
				p.MachineID = s.settings(ctx).MachineID
			}
			if p.ID == "inbox.agent" {
				h := s.hello(ctx)
				if m := h.Models.InboxAgentMachine; m != "" && slices.Contains(h.Assistant.Machines, m) {
					p.MachineID = m
				}
			}
			return p, nil
		}
	}
	if strings.HasPrefix(id, "task.") {
		tasks := s.taskViews(ctx)
		if tasks == nil {
			return ModelPurpose{}, i18n.Err("assistant-models.err_read_tasks")
		}
		for _, t := range tasks {
			if id == "task."+t.ID {
				return ModelPurpose{ID: id, CategoryID: "assistant", Name: t.Label, Group: mc(l, "group_assistant"), Kind: "launcher", Parent: "assistant", MachineID: t.MachineID}, nil
			}
		}
	}
	if p, ok := s.extensionPurposeDef(ctx, id); ok { // an extension's own (model_rpc.go)
		return p, nil
	}
	if strings.HasPrefix(id, "plugin.") {
		for _, p := range s.voicePlugins(ctx) {
			if id == "plugin."+p.ID {
				return ModelPurpose{ID: id, Name: p.Name, Group: mc(l, "group_extensions"), Kind: "plugin"}, nil
			}
		}
	}
	return ModelPurpose{}, errModelPurposeNotFound
}

// legacyModelConfig is hub.json's connection by its provider id, with its field-wise inheritance (Hello.Models).
func (s *Service) legacyModelConfig(ctx context.Context, id string) ai.Config {
	m := s.hello(ctx).Models
	c := m.AI
	switch id {
	case "legacy.triage":
		if m.TriageAI != nil {
			c = m.TriageAI.Over(c)
		}
	case "legacy.judge":
		if m.JudgeAI != nil {
			c = m.JudgeAI.Over(c)
		}
	case "legacy.kb":
		if m.KBAI != nil {
			c = m.KBAI.Over(c)
		} else {
			c = s.legacyModelConfig(ctx, "legacy.triage")
		}
	}
	return c
}

// voicePlugins are the voice plugins and their models, as the hub has them (plugins.voice).
func (s *Service) voicePlugins(ctx context.Context) []assistantwire.VoicePlugin {
	var res struct {
		Plugins []assistantwire.VoicePlugin `json:"plugins"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsVoice, map[string]any{}, &res); err != nil {
		s.logf("assistant: voice plugins: %v", err)
	}
	return res.Plugins
}

func (s *Service) modelProviders(ctx context.Context) []ModelProvider {
	l := s.requestCtxLang(ctx)
	out := []ModelProvider{}
	for _, x := range []struct{ id, name string }{{"legacy.ai", mc(l, "provider_legacy_ai")}, {"legacy.triage", mc(l, "provider_legacy_triage")}, {"legacy.judge", mc(l, "provider_legacy_judge")}, {"legacy.kb", mc(l, "provider_legacy_kb")}} {
		c := s.legacyModelConfig(ctx, x.id)
		if c.BaseURL == "" {
			continue
		}
		apiName := c.API
		if apiName == "" {
			apiName = "messages"
		}
		out = append(out, ModelProvider{ID: x.id, Name: x.name, Kind: "api", API: apiName, Model: c.Model, HasKey: c.APIKeyFile != "" || c.APIKey != ""})
	}
	for _, v := range s.launcherViews(ctx) {
		kind := v.Kind
		if kind == "" {
			kind = "launcher"
		}
		apiName := v.API
		if kind == "api" && apiName == "" {
			apiName = "messages"
		}
		agent := v.Agent
		if agent == "" && kind == "launcher" && (v.Name == "codex" || v.Name == "traex" || v.Name == "agy" || v.Name == "omp") {
			agent = v.Name
		}
		out = append(out, ModelProvider{ID: "launcher." + v.Name, Name: cmp.Or(v.DisplayName, v.Name), Kind: kind, Agent: agent, API: apiName, Model: v.Model, Machines: v.Machines, Hidden: v.Hidden, HasKey: v.HasKey})
	}
	if !slices.ContainsFunc(out, func(p ModelProvider) bool { return p.ID == "launcher.claude" }) {
		p := ModelProvider{ID: "launcher.claude", Name: mc(l, "provider_claude_default"), Kind: "launcher", Agent: "claude"}
		if b, ok := s.builtinClaude(ctx, s.zshrcLaunchers(ctx, false)); ok {
			p.Machines = b.Machines // where plain claude is: Settings › Model Providers lists it by machine
		}
		out = append(out, p)
	}
	for _, p := range s.voicePlugins(ctx) {
		out = append(out, ModelProvider{ID: "plugin." + p.ID, Name: p.Name, Kind: "plugin", Model: p.Default, Models: p.Models, Status: p.Error})
	}
	return out
}

func (s *Service) modelBaseline(ctx context.Context, p ModelPurpose) (ModelBinding, string, error) {
	l := s.requestCtxLang(ctx)
	if p.Parent != "" {
		b, err := s.resolveModel(ctx, p.Parent)
		return b, p.Parent, err
	}
	if p.ID == "agent.delegate" { // "Sorting"'s connection, with a stronger model on it (delegateModel)
		b, err := s.resolveModel(ctx, "inbox.triage")
		if err == nil {
			b = s.delegateBaseline(ctx, b)
		}
		return b, "inbox.triage", err // inheritedLabel names the connection and the model
	}
	if p.Kind == "api" {
		provider := "legacy.ai"
		if p.ID == "inbox.triage" {
			provider = "legacy.triage"
		}
		if p.ID == "kb" {
			provider = "legacy.kb"
		}
		c := s.legacyModelConfig(ctx, provider)
		if c.BaseURL == "" {
			// No hub.json connection for this use: fall back to a provider the assistant can call itself, instead of
			// failing while a keyed Claude connection sits unused (#388). Not stored, so a later explicit choice still
			// wins.
			if name := s.fallbackAPIProvider(ctx); name != "" {
				return ModelBinding{Provider: "launcher." + name}, mc(l, modelAutoSource), nil
			}
		}
		return ModelBinding{Provider: provider, Model: c.Model, Effort: c.ReasoningEffort}, mc(l, "source_hub_config"), nil
	}
	if p.ID == "assistant" { // the assistant's own launcher (its settings, else hub.json's)
		return ModelBinding{Provider: "launcher." + s.settings(ctx).Launcher}, mc(l, "source_assistant_default"), nil
	}
	if p.Kind == "launcher" {
		return ModelBinding{Provider: "launcher.claude"}, mc(l, "source_default"), nil
	}
	return ModelBinding{Provider: p.ID}, mc(l, "source_extension_default"), nil
}

// delegateModel is what "Answering agents' questions" runs on unless the user picks another (2026-11-05, the user's call): it decides for the
// user whether an agent's question is theirs, a cheap triage model is not up to that.
const delegateModel = "claude-opus-5-5"

// delegateBaseline is "Sorting"'s binding b with delegateModel on it when its connection serves Claude models (its
// model is one); b as it is otherwise: a connection to another vendor's models, or one that cannot be called, keeps
// its own model.
func (s *Service) delegateBaseline(ctx context.Context, b ModelBinding) ModelBinding {
	if b.Provider == "" {
		return b
	}
	c, err := s.apiConfig(ctx, b)
	if err != nil || (c.API != "" && c.API != "messages") || !strings.HasPrefix(c.Model, "claude-") {
		return b
	}
	b.Model, b.Effort = delegateModel, ""
	return b
}

// modelAutoSource is (the catalog name of) what modelBaseline calls the default of an API use that runs on
// fallbackAPIProvider's pick; "Inherited from" names the pick (inheritedLabel).
const modelAutoSource = "source_auto"

// inheritedLabel is what "Inherited from" says for p, whose default is base (from: what modelBaseline calls it). Nothing when
// p picks its own connection: none of it is inherited. Where the default is not a fixed place it names what it
// actually runs on: "Answering agents' questions"'s connection and the model on it (Opus only when that connection serves Claude models,
// delegateBaseline), the connection an unconfigured API use falls back to.
func inheritedLabel(l i18n.Lang, p ModelPurpose, base ModelBinding, from string, providers []ModelProvider) string {
	if p.Override.Provider != "" {
		return ""
	}
	name, model := base.Provider, base.Model
	for _, v := range providers {
		if v.ID == base.Provider {
			name, model = cmp.Or(v.Name, v.ID), cmp.Or(model, v.Model)
			break
		}
	}
	switch {
	case p.ID == "agent.delegate" && (p.Override.Model != "" || model == ""):
		return mc(l, "source_delegate_connection", i18n.Args{"provider": name}) // the model is the user's pick, or the connection's own
	case p.ID == "agent.delegate":
		return mc(l, "source_delegate_default", i18n.Args{"provider": name, "model": model})
	case p.ID == purposeLarkAgent, isExtensionPurpose(p.ID):
		return purposeName(l, p.Parent)
	case from == mc(l, modelAutoSource):
		return mc(l, "source_auto_named", i18n.Args{"provider": name})
	}
	return from
}

// modelNoAPIStatus explains an API use with nothing to call: launchers like "Claude Code default" only start a CLI on a
// computer, so they never show up for these uses. A catalog name.
const modelNoAPIStatus = "status_no_api"

// modelUnverified is the status of a use nothing is known to be wrong with. Not translated: the apps compare it to
// this text (iOS ModelConfigurationView, Android HttpVoiceExtensions) to leave it out.
const modelUnverified = "未验证"

// messagesAPI includes the legacy empty value for the Messages protocol.
func messagesAPI(api string) bool { return api == "" || api == "messages" }

// fallbackAPIProvider names the managed connection an unconfigured API use runs on: the first direct API provider,
// else the first keyed launcher speaking Messages. Other launcher protocols cannot serve these uses. Hidden ones and
// ones without an address, a default model or a key are skipped. "" when there is none.
func (s *Service) fallbackAPIProvider(ctx context.Context) string {
	stored := s.storedLaunchers(ctx)
	hidden := s.hiddenLaunchers(ctx)
	usable := func(l storedLauncher) bool {
		return !slices.Contains(hidden, l.Name) && l.BaseURL != "" && l.Model != "" && l.KeySealed != ""
	}
	for _, l := range stored {
		if l.Kind == "api" && usable(l) {
			return l.Name
		}
	}
	for _, l := range stored {
		if l.Kind != "api" && messagesAPI(l.API) && usable(l) {
			return l.Name
		}
	}
	return ""
}

// mergeModel puts over's choices on base: another provider starts from nothing (its model and effort are its own).
func mergeModel(base, over ModelBinding) ModelBinding {
	if over.Provider != "" && over.Provider != base.Provider {
		base = ModelBinding{Provider: over.Provider, Runtime: base.Runtime}
	}
	if over.Model != "" {
		base.Model = over.Model
	}
	if over.Effort != "" {
		base.Effort = over.Effort
	}
	if over.Runtime != "" {
		base.Runtime = over.Runtime
	}
	return base
}

// resolveModel reads category defaults, except the opt-in Lark /agent override and independent plugin models.
func (s *Service) resolveModel(ctx context.Context, id string) (ModelBinding, error) {
	return s.resolveModelRecord(ctx, id, nil)
}

// resolveModelRecord is resolveModel on id's record r when the caller has read it already (nil: it reads it).
func (s *Service) resolveModelRecord(ctx context.Context, id string, r *modelRecord) (ModelBinding, error) {
	p, err := s.modelPurposeDef(ctx, id)
	if err != nil {
		return ModelBinding{}, err
	}
	if recordID := modelRecordID(p); recordID != id { // r is id's, not recordID's
		return s.resolveModel(ctx, recordID)
	}
	base, _, err := s.modelBaseline(ctx, p)
	if err != nil {
		return base, err
	}
	if r == nil {
		read, err := s.modelRecord(ctx, id)
		if err != nil {
			return base, i18n.Err("assistant-models.err_read_config", i18n.Args{"error": err.Error()})
		}
		r = &read
	}
	return mergeModel(base, r.Binding), nil
}

func launcherProvider(name string) string {
	if name == "" {
		return ""
	}
	return "launcher." + name
}

// apiConfig is the connection an API binding calls, with its key opened.
func (s *Service) apiConfig(ctx context.Context, b ModelBinding) (ai.Config, error) {
	var c ai.Config
	if strings.HasPrefix(b.Provider, "legacy.") {
		c = s.legacyModelConfig(ctx, b.Provider)
	} else if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok {
		found := false
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name == name {
				if l.Kind != "api" && !messagesAPI(l.API) {
					return c, i18n.Err("assistant-models.err_launcher_not_responses")
				}
				c = ai.Config{API: l.API, BaseURL: l.BaseURL, Model: l.Model, Bearer: l.BearerAuth()}
				key, err := s.openKey(l.KeySealed)
				if err != nil {
					return c, i18n.Err("assistant-models.err_key_decrypt")
				}
				c.APIKey = key
				found = true
				break
			}
		}
		if !found {
			return c, i18n.Err("assistant-models.err_not_callable")
		}
	} else {
		return c, i18n.Err("assistant-models.err_not_api")
	}
	if b.Model != "" {
		c.Model = b.Model
	}
	c.ReasoningEffort = b.Effort
	if c.API != "" && c.API != "messages" && c.API != "responses" && c.API != "chat" {
		return c, i18n.Err("assistant-models.err_api_protocol")
	}
	if c.API != "responses" && b.Effort != "" {
		c.ReasoningEffort = ""
	} // Older Messages configurations carried an unused Responses-only field.
	return c, nil
}

// purposeModel is purpose id's model, resolved at the start of every request.
func (s *Service) purposeModel(id string) *ai.Client {
	return ai.NewResolved(func(ctx context.Context) (*ai.Client, error) {
		b, err := s.resolveModel(ctx, id)
		if err != nil {
			return nil, err
		}
		c, err := s.apiConfig(ctx, b)
		if err != nil {
			return nil, err
		}
		return ai.New(c), nil
	})
}

// modelUsable says whether an API binding can be called: it resolves to an address, a model and a key.
func (s *Service) modelUsable(ctx context.Context, b ModelBinding) bool {
	c, err := s.apiConfig(ctx, b)
	return err == nil && c.Configured()
}

func (s *Service) modelFingerprint(ctx context.Context, id string) (string, error) {
	b, err := s.resolveModel(ctx, id)
	if err != nil {
		return "", err
	}
	return s.modelBindingFingerprint(ctx, b), nil
}

// modelBindingFingerprint changes when what the binding runs on does (the provider's settings, the key sealed anew).
func (s *Service) modelBindingFingerprint(ctx context.Context, b ModelBinding) string {
	raw, _ := json.Marshal(b)
	if strings.HasPrefix(b.Provider, "legacy.") {
		v, _ := json.Marshal(s.legacyModelConfig(ctx, b.Provider))
		raw = append(raw, v...)
	}
	if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok {
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name == name {
				v, _ := json.Marshal(l)
				raw = append(raw, v...)
				break
			}
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// modelPurpose is purpose id as Settings › Model configuration shows it: what it is set to, what it runs on, and how that stands.
func (s *Service) modelPurpose(ctx context.Context, id string, providers []ModelProvider) (ModelPurpose, error) {
	p, err := s.modelPurposeDef(ctx, id)
	if err != nil {
		return p, err
	}
	recordID := modelRecordID(p)
	r, err := s.modelRecord(ctx, recordID)
	if err != nil {
		return p, err
	}
	p.Override, p.Revision = r.Binding, r.Revision
	readOnly := recordID != id
	if readOnly {
		p.Override = ModelBinding{}
	}
	p.Effective, err = s.resolveModel(ctx, id)
	if err != nil {
		return p, err
	}
	base, from, err := s.modelBaseline(ctx, p)
	if err != nil {
		return p, err
	}
	p.InheritedFrom = inheritedLabel(s.requestCtxLang(ctx), p, base, from, providers)
	stored := s.storedLaunchers(ctx)
	p.Fields = []string{"provider", "model"}
	p.ProviderIDs = []string{}
	for _, v := range providers {
		compatible := v.Kind == p.Kind
		if p.Kind == "launcher" && v.Kind == "api" { // a direct Messages API provider starts Claude on its gateway (#2455)
			compatible = messagesAPI(v.API)
		}
		if p.Kind == "api" && v.Kind == "launcher" {
			for _, l := range stored {
				if v.ID == "launcher."+l.Name {
					compatible = messagesAPI(l.API)
					break
				}
			}
		}
		if p.Kind == "plugin" {
			compatible = v.ID == p.ID
		}
		if compatible && p.Kind == "launcher" && len(v.Machines) > 0 && p.MachineID != "" {
			compatible = slices.Contains(v.Machines, p.MachineID)
		}
		if compatible {
			p.ProviderIDs = append(p.ProviderIDs, v.ID)
		}
		if v.ID == p.Effective.Provider {
			if p.Effective.Model == "" {
				p.Effective.Model = v.Model
			}
			if p.Kind == "api" && v.API == "responses" {
				p.Fields = append(p.Fields, "effort")
			}
		}
	}
	if p.Kind == "launcher" {
		p.Fields = append(p.Fields, "effort")
	}
	if len(p.Runtimes) > 0 {
		p.Fields = append(p.Fields, "runtime")
	}
	l := s.requestCtxLang(ctx)
	p.Status = modelUnverified
	if !slices.Contains(p.ProviderIDs, p.Effective.Provider) {
		p.Status = mc(l, "status_provider_incompatible")
		if p.Kind == "api" && strings.HasPrefix(p.Effective.Provider, "legacy.") && s.legacyModelConfig(ctx, p.Effective.Provider).BaseURL == "" {
			p.Status = mc(l, modelNoAPIStatus)
		}
	} else if p.Kind == "api" && p.Effective.Model == "" {
		p.Status = mc(l, "status_no_model")
	} else if p.Kind == "api" {
		if config, e := s.apiConfig(ctx, p.Effective); e != nil {
			p.Status = i18n.Localize(e, l)
		} else if config.APIKey == "" && config.APIKeyFile == "" {
			p.Status = mc(l, "status_no_credentials")
		} else if pause := ai.Held(config); pause != nil {
			p.Status, p.Limit = pause.Text(), pause
		}
	} else if p.Kind == "plugin" {
		for _, provider := range providers {
			if provider.ID == p.Effective.Provider && provider.Status != "" {
				p.Status = provider.Status
				break
			}
		}
	} else if p.Kind == "launcher" && p.MachineID != "" {
		if d, ok := s.device(ctx, p.MachineID); ok {
			if !d.Online {
				p.Status = mc(l, "status_machine_offline")
			} else if d.Version != "" && !d.ModelConfiguration {
				p.Status = mc(l, "status_node_outdated")
			}
		}
	}
	if readOnly {
		p.Fields = []string{}
		for _, category := range modelPurposes {
			if category.ID == recordID {
				p.InheritedFrom = purposeName(l, category.ID)
				break
			}
		}
	}
	return p, nil
}

type modelPatch struct {
	Revision *int64          `json:"revision"`
	Provider json.RawMessage `json:"provider"`
	Model    json.RawMessage `json:"model"`
	Effort   json.RawMessage `json:"effort"`
	Runtime  json.RawMessage `json:"runtime"`
}

func patchModel(b ModelBinding, in modelPatch) (ModelBinding, error) {
	if in.Provider != nil {
		var provider string
		if string(in.Provider) != "null" {
			if err := json.Unmarshal(in.Provider, &provider); err != nil {
				return b, i18n.Err("assistant-models.err_provider_type")
			}
		}
		if provider != b.Provider {
			b.Model, b.Effort = "", ""
		}
	}
	for _, f := range []struct {
		raw json.RawMessage
		dst *string
	}{{in.Provider, &b.Provider}, {in.Model, &b.Model}, {in.Effort, &b.Effort}, {in.Runtime, &b.Runtime}} {
		if f.raw == nil {
			continue
		}
		if string(f.raw) == "null" {
			*f.dst = ""
			continue
		}
		var v string
		if err := json.Unmarshal(f.raw, &v); err != nil {
			return b, i18n.Err("assistant-models.err_field_type")
		}
		if v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\x00") || len(v) > 512 {
			return b, i18n.Err("assistant-models.err_field_invalid")
		}
		*f.dst = v
	}
	if b.Runtime != "" && !slices.Contains(assistantRuntimes, b.Runtime) {
		return b, i18n.Err("assistant-models.err_runtime_invalid")
	}
	if b.Runtime == proto.AssistantRuntimeCLI {
		b.Runtime = "" // the default, stored as none: an explicit cli fingerprints like no choice
	}
	return b, nil
}

func (s *Service) validateModel(ctx context.Context, p ModelPurpose, b ModelBinding, providers []ModelProvider) error {
	var provider *ModelProvider
	for i := range providers {
		if providers[i].ID == b.Provider {
			provider = &providers[i]
			break
		}
	}
	if provider == nil {
		return i18n.Err("assistant-models.err_provider_missing")
	}
	if !slices.Contains(p.ProviderIDs, b.Provider) {
		return i18n.Err("assistant-models.err_provider_unsupported")
	}
	if b.Effort != "" && !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, b.Effort) {
		return i18n.Err("assistant-models.err_effort_unsupported")
	}
	if p.Kind == "api" {
		_, err := s.apiConfig(ctx, b)
		return err
	}
	if p.Kind == "plugin" {
		if b.Effort != "" {
			return i18n.Err("assistant-models.err_extension_effort")
		}
		if b.Model != "" {
			if provider.Status != "" {
				return errors.New(provider.Status)
			}
			if !slices.ContainsFunc(provider.Models, func(m ModelChoice) bool { return m.ID == b.Model }) {
				return i18n.Err("assistant-models.err_extension_model")
			}
		}
		return nil
	}
	if p.Kind == "launcher" {
		if len(provider.Machines) > 0 && p.MachineID != "" && !slices.Contains(provider.Machines, p.MachineID) {
			return i18n.Err("assistant-models.err_launcher_not_on_machine")
		}
		if b.Model != "" {
			machine := ""
			if _, ok := s.device(ctx, p.MachineID); ok {
				machine = p.MachineID
			} else if len(provider.Machines) > 0 {
				if _, ok := s.device(ctx, provider.Machines[0]); ok {
					machine = provider.Machines[0]
				}
			}
			if machine != "" {
				var result struct {
					Agent string `json:"agent"`
				}
				if err := s.call(ctx, machine, assistantwire.DeviceLaunchersModelCheck, s.launcherParams(ctx, machine, strings.TrimPrefix(b.Provider, "launcher."), map[string]any{"model": b.Model}), &result); err != nil {
					return i18n.Err("assistant-models.err_check_model", i18n.Args{"error": i18n.Localize(err, s.requestCtxLang(ctx))})
				}
				if p.CategoryID == "agent.new" && result.Agent != "claude" && result.Agent != "codex" && result.Agent != "agy" && result.Agent != "omp" {
					return i18n.Err("assistant-models.err_agent_launcher")
				}
				if p.CategoryID == "agent.new" && result.Agent == "codex" && b.Effort != "" {
					return i18n.Err("assistant-models.err_codex_effort")
				}
			}
		}
	}
	return nil
}

// providerModelUses are the categories that run on provider: a provider in use is not deleted underneath them.
func (s *Service) providerModelUses(ctx context.Context, provider string) ([]string, error) {
	uses := []string{}
	for _, id := range modelConfigIDs {
		p, err := s.modelPurposeDef(ctx, id)
		if err != nil {
			return nil, err
		}
		b, err := s.resolveModel(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Provider == provider {
			uses = append(uses, p.Name)
		}
	}
	return uses, nil
}

// triageInUse refuses a change that leaves inbox.triage uncallable while plugins that need it are on: 422
// triage_model_in_use, naming them (not 409: the apps read a 409 from model-config as an edit conflict and would hide
// the message; a 422 they show as it is). nil when the change may go (docs/extensions/extensions.md "General inbox rules").
func (s *Service) triageInUse(ctx context.Context, id string, effective ModelBinding) (map[string]any, error) {
	if id != "inbox.triage" || s.modelUsable(ctx, effective) {
		return nil, nil
	}
	var res struct {
		Names []string `json:"names"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsNeedTriage, map[string]any{}, &res); err != nil {
		return nil, err
	}
	if len(res.Names) == 0 {
		return nil, nil
	}
	l := s.requestCtxLang(ctx)
	return map[string]any{"code": "triage_model_in_use", "plugins": res.Names,
		"error": i18n.T(l, "assistant-models.triage_in_use", i18n.Args{"names": strings.Join(res.Names, i18n.T(l, "assistant-models.list_separator"))})}, nil
}

func (s *Service) modelConfigRoutes() {
	s.handle("GET /api/v1/model-config", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		providers := s.modelProviders(ctx)
		purposes := []ModelPurpose{}
		for _, id := range modelConfigIDs {
			p, err := s.modelPurpose(ctx, id, providers)
			if err != nil {
				apierr.Write(w, 500, err)
				return
			}
			purposes = append(purposes, p)
		}
		// The extensions' own (model_rpc.go), after the categories: each with its own record, like Lark /agent.
		for _, x := range s.modelExtensions(ctx) {
			p, err := s.modelPurpose(ctx, assistantwire.ExtensionPurposePrefix+x.StorageKey(), providers)
			if err != nil {
				apierr.Write(w, 500, err)
				return
			}
			purposes = append(purposes, p)
		}
		apierr.WriteJSON(w, map[string]any{"purposes": purposes, "providers": providers})
	})
	detail := func(w http.ResponseWriter, r *http.Request) {
		providers := s.modelProviders(r.Context())
		p, err := s.modelPurpose(r.Context(), r.PathValue("id"), providers)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errModelPurposeNotFound) {
				status = http.StatusNotFound
			}
			apierr.Write(w, status, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"purpose": p, "providers": providers})
	}
	s.handle("GET /api/v1/model-config/purposes/{id}", detail)
	s.handle("GET /api/v1/model-config/providers/{id}/models", s.providerModels)
	// Both verbs keep sparse updates and the same revision-checked write path.
	update := func(w http.ResponseWriter, r *http.Request) {
		var in modelPatch
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if in.Revision == nil || *in.Revision < 0 {
			apierr.Write(w, 400, i18n.Err("assistant-models.err_revision_required"))
			return
		}
		s.models.mu.Lock()
		defer s.models.mu.Unlock()
		ctx, id := r.Context(), r.PathValue("id")
		providers := s.modelProviders(ctx)
		p, err := s.modelPurpose(ctx, id, providers)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errModelPurposeNotFound) {
				status = http.StatusNotFound
			}
			apierr.Write(w, status, err)
			return
		}
		if len(p.Fields) == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models.err_purpose_readonly"))
			return
		}
		if p.Revision != *in.Revision {
			apierr.Write(w, 409, errModelConflict)
			return
		}
		if in.Runtime != nil && string(in.Runtime) != "null" && len(p.Runtimes) == 0 {
			apierr.Write(w, 400, i18n.Err("assistant-models.err_runtime_assistant_only"))
			return
		}
		b, err := patchModel(p.Override, in)
		if err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if p.Kind == "launcher" { // passed to an agent as --model: a model id (#1644)
			if err := checkModelID(b.Model); err != nil {
				apierr.Write(w, 400, err)
				return
			}
		}
		base, _, err := s.modelBaseline(ctx, p)
		if err != nil {
			apierr.Write(w, 500, err)
			return
		}
		effective := mergeModel(base, b)
		if body, err := s.triageInUse(ctx, id, effective); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		} else if body != nil { // plugins that need it are on
			apierr.WriteBody(w, http.StatusUnprocessableEntity, body)
			return
		}
		if in.Effort != nil && string(in.Effort) != "null" && p.Kind == "api" {
			config, e := s.apiConfig(ctx, effective)
			if e != nil {
				apierr.Write(w, 400, e)
				return
			}
			if config.API != "responses" {
				apierr.Write(w, 400, i18n.Err("assistant-models.err_api_effort"))
				return
			}
		}
		if b != (ModelBinding{}) {
			if err = s.validateModel(ctx, p, effective, providers); err != nil {
				apierr.Write(w, 400, err)
				return
			}
		}
		if err = s.saveModelRecord(ctx, id, p.Revision, b); err != nil {
			status := 500
			if errors.Is(err, errModelConflict) {
				status = 409
			}
			apierr.Write(w, status, err)
			return
		}
		detail(w, r)
	}
	s.handle("PATCH /api/v1/model-config/purposes/{id}", update)
	s.handle("PUT /api/v1/model-config/purposes/{id}", update)
}

// ---- what a node call runs on ----

// applyModel fills params (a node call's: session.start, assistant.send…) with what purpose id runs on; a launcher,
// model or effort already in params is this call's own pick. The permission mode is left as it is.
func (s *Service) applyModel(ctx context.Context, id string, params map[string]any) error {
	b, err := s.resolveModel(ctx, id)
	if err != nil {
		return err
	}
	if name, _ := params["launcher"].(string); name != "" && launcherProvider(name) != b.Provider {
		b = ModelBinding{Provider: launcherProvider(name), Runtime: b.Runtime}
	}
	if b.Model != "" && strings.HasPrefix(b.Provider, "launcher.") && !proto.ValidModelID(b.Model) {
		// Configured before a launcher's model was held to the model id rule (#1644; Model configuration refuses it as
		// an edit now): the launcher's own default, rather than every run of this purpose failing, the main chat's too.
		s.logf("model config %s: %q is no model id, running %s's default", id, textfmt.Clip(b.Model, 80), b.Provider)
		b.Model = ""
	}
	if model, _ := params["model"].(string); strings.TrimSpace(model) != "" {
		b.Model = strings.TrimSpace(model) // this call's pick: finishModel refuses one that is no model id
	} else {
		delete(params, "model") // none picked ("" or blanks): the binding's
	}
	if effort, _ := params["effort"].(string); effort != "" {
		b.Effort = effort
	}
	return s.finishModel(ctx, id, params, b)
}

// applyPickedModel is applyModel for a launcher/model picked for one send (the chat's menu): with no model the
// launcher's own default stays, instead of the purpose's configured one.
func (s *Service) applyPickedModel(ctx context.Context, params map[string]any) error {
	name, _ := params["launcher"].(string)
	if name == "" {
		return nil
	}
	b := ModelBinding{Provider: launcherProvider(name)}
	if m, _ := params["model"].(string); strings.TrimSpace(m) != "" {
		b.Model = strings.TrimSpace(m)
	} else {
		delete(params, "model") // the launcher's own default
	}
	if e, _ := params["effort"].(string); e != "" {
		b.Effort = e
	}
	// A pick is a launcher and model for one message; the runtime stays the main chat's.
	configured, err := s.resolveModel(ctx, "assistant")
	if err != nil {
		return err
	}
	b.Runtime = configured.Runtime
	return s.finishModel(ctx, "assistant", params, b)
}

// finishModel puts a final binding into params: the launcher name, the model / effort set, the execution fingerprint.
// A launcher's model goes to the agent as --model, so it has to be a model id (#1644): a call's own pick that is not
// one is refused here (a configured one that is not was dropped by applyModel).
func (s *Service) finishModel(ctx context.Context, id string, params map[string]any, b ModelBinding) error {
	name, ok := strings.CutPrefix(b.Provider, "launcher.")
	if !ok {
		if strings.HasPrefix(id, "plugin.") {
			params["model"] = b.Model
			return nil
		}
		return i18n.Err("assistant-models.err_not_launcher")
	}
	if err := checkModelID(b.Model); err != nil {
		return err
	}
	for _, provider := range s.storedLaunchers(ctx) {
		// A direct API provider speaking Messages starts Claude on its gateway (node, contract #2455 §2);
		// other direct API protocols cannot.
		if provider.Name == name && provider.Kind == "api" && !messagesAPI(provider.API) {
			return i18n.Err("assistant-models.err_api_as_launcher")
		}
	}
	if b.Runtime != "" {
		params["runtime"] = b.Runtime
	} else {
		delete(params, "runtime") // the CLI: nothing a caller left
	}
	params["launcher"] = name
	if b.Model != "" {
		params["model"] = b.Model
	}
	if b.Effort != "" {
		params["effort"] = b.Effort
	}
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(append([]byte(s.modelBindingFingerprint(ctx, b)), raw...))
	params["model_fingerprint"] = hex.EncodeToString(sum[:])
	return nil
}
