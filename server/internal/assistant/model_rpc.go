package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Model calls by extensions (#6102, docs/extensions/extensions.md "Model calls"). The manager checks the models scope and hands
// the call here with the extension's id; it runs on the extension's purpose extension.<id>, which inherits
// "Knowledge base"'s model until the user picks another in Settings › Models. Keys stay here. No budget and no cap
// (the user's decision): each call's usage goes back to the extension and into one log line.

const (
	modelCallMaxTokens   = 4096
	modelCallTokensLimit = 64000
	modelCallTimeout     = 120 * time.Second
	modelCallTimeoutMost = 600 * time.Second
	modelCallSchemaMost  = 64 << 10
	modelCallLabelMost   = 64
)

// modelExtensions are the extensions with a model purpose of their own, as the manager has them (plugins.models).
func (s *Service) modelExtensions(ctx context.Context) []assistantwire.ModelExtension {
	var res struct {
		Extensions []assistantwire.ModelExtension `json:"extensions"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsModels, map[string]any{}, &res); err != nil {
		s.logf("assistant: model extensions: %v", err)
	}
	return res.Extensions
}

// extensionPurposeDef is extension.<id>'s purpose: an API use of its own under "Knowledge base", inheriting it.
func (s *Service) extensionPurposeDef(ctx context.Context, id string) (ModelPurpose, bool) {
	ext, ok := strings.CutPrefix(id, assistantwire.ExtensionPurposePrefix)
	if !ok {
		return ModelPurpose{}, false
	}
	for _, x := range s.modelExtensions(ctx) {
		if x.StorageKey() == ext {
			l := s.requestCtxLang(ctx)
			return ModelPurpose{ID: id, CategoryID: "kb", Name: x.Name, Group: mc(l, "group_extensions"), Kind: "api", Parent: "kb"}, true
		}
	}
	return ModelPurpose{}, false
}

func isExtensionPurpose(id string) bool {
	return strings.HasPrefix(id, assistantwire.ExtensionPurposePrefix)
}

func invalidModelParams(format string, args ...any) error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: "model.complete: " + fmt.Sprintf(format, args...)}
}

// extensionComplete answers models.extension_complete.
func (s *Service) extensionComplete(ctx context.Context, raw json.RawMessage) (any, error) {
	var call assistantwire.ExtensionComplete
	if err := json.Unmarshal(raw, &call); err != nil || call.Extension == "" {
		return nil, invalidModelParams("no extension")
	}
	var p assistantwire.ModelCompleteParams
	dec := json.NewDecoder(bytes.NewReader(call.Params))
	dec.DisallowUnknownFields()
	if len(call.Params) == 0 {
		return nil, invalidModelParams("params are required")
	}
	if err := dec.Decode(&p); err != nil {
		return nil, invalidModelParams("%v", err)
	}
	if len(p.Messages) == 0 {
		return nil, invalidModelParams("messages are required")
	}
	turns := make([]ai.Message, 0, len(p.Messages)+2)
	for i, m := range p.Messages {
		if m.Role != ai.RoleUser && m.Role != ai.RoleAssistant {
			return nil, invalidModelParams("messages[%d].role must be user or assistant", i)
		}
		if strings.TrimSpace(m.Content) == "" {
			return nil, invalidModelParams("messages[%d].content is empty", i)
		}
		if i > 0 && m.Role == p.Messages[i-1].Role {
			return nil, invalidModelParams("messages[%d]: user and assistant turns alternate", i)
		}
		turns = append(turns, ai.Message{Role: m.Role, Content: m.Content})
	}
	if p.Messages[0].Role != ai.RoleUser || p.Messages[len(p.Messages)-1].Role != ai.RoleUser {
		return nil, invalidModelParams("messages start and end with the user's")
	}
	switch {
	case p.MaxTokens == 0:
		p.MaxTokens = modelCallMaxTokens
	case p.MaxTokens < 1 || p.MaxTokens > modelCallTokensLimit:
		return nil, invalidModelParams("max_tokens is 1 to %d", modelCallTokensLimit)
	}
	timeout := modelCallTimeout
	switch {
	case p.TimeoutSeconds == 0:
	case p.TimeoutSeconds < 1 || time.Duration(p.TimeoutSeconds)*time.Second > modelCallTimeoutMost:
		return nil, invalidModelParams("timeout_seconds is 1 to %d", int(modelCallTimeoutMost/time.Second))
	default:
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	if len(p.Label) > modelCallLabelMost {
		return nil, invalidModelParams("label is at most %d bytes", modelCallLabelMost)
	}
	var schema map[string]any
	system := p.System
	if len(p.JSONSchema) > 0 && string(p.JSONSchema) != "null" {
		if len(p.JSONSchema) > modelCallSchemaMost {
			return nil, invalidModelParams("json_schema is at most %d bytes", modelCallSchemaMost)
		}
		var err error
		if schema, err = compileSchema(p.JSONSchema); err != nil {
			return nil, invalidModelParams("%v", err)
		}
		var indented bytes.Buffer
		_ = json.Indent(&indented, p.JSONSchema, "", "  ")
		system = prompts.RenderJSONSchema(prompts.JSONSchema{System: p.System, Schema: indented.String()})
	}

	purpose := assistantwire.ExtensionPurposePrefix + call.StorageKey()
	if _, ok := s.extensionPurposeDef(ctx, purpose); !ok {
		return nil, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: the extension has no model purpose (is it on and granted models?)"}
	}
	b, err := s.resolveModel(ctx, purpose)
	if err != nil {
		return nil, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: i18n.Localize(err, s.lang(ctx))}
	}
	config, err := s.apiConfig(ctx, b)
	if err != nil {
		return nil, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: i18n.Localize(err, s.lang(ctx))}
	}
	if !config.Configured() {
		return nil, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: nothing to call for this extension; pick a model for it in Settings › Model configuration"}
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := ai.New(config)
	var usage assistantwire.ModelUsage
	add := func(u ai.Usage) {
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheReadInputTokens += u.CacheReadInputTokens
		usage.CacheCreationInputTokens += u.CacheCreationInputTokens
		usage.Requests++
	}
	started := time.Now()
	logged := func(outcome string) {
		s.logf("assistant: extension %s model call%s on %s: %s in %s, %d request(s), %d input + %d output tokens (cache read %d, written %d)",
			call.Extension, labelPart(p.Label), config.Model, outcome, time.Since(started).Round(time.Millisecond), usage.Requests,
			usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	}
	var reply ai.Reply
	var value any
	for attempt := 0; ; attempt++ {
		reply, err = client.Chat(cctx, ai.Request{System: system, Messages: turns, MaxTokens: p.MaxTokens})
		if err != nil {
			logged("failed")
			return nil, modelCallError(cctx, err)
		}
		add(reply.Usage)
		if schema == nil {
			break
		}
		problem := ""
		if value, err = decodeReply(reply.Text); err != nil {
			problem = err.Error()
		} else {
			problem = validateSchema(schema, schema, value, "$", 0)
		}
		if problem == "" {
			break
		}
		if reply.Truncated {
			problem += " (the reply was cut off at max_tokens)"
		}
		if attempt > 0 {
			logged("invalid output")
			data, _ := json.Marshal(assistantwire.ModelInvalidOutput{Problem: problem, Text: reply.Text, Usage: usage})
			return nil, &rpc.Error{Code: assistantwire.CodeInvalidOutput, Message: "model.complete: the reply does not match json_schema: " + problem, Data: data}
		}
		turns = append(turns, ai.Message{Role: ai.RoleAssistant, Content: cmpOr(reply.Text, "(empty)")},
			ai.Message{Role: ai.RoleUser, Content: prompts.RenderJSONSchemaRetry(problem)})
	}
	logged("done")
	out := assistantwire.ModelCompleteResult{Text: reply.Text, Model: reply.Model, StopReason: "end_turn", Usage: usage}
	if reply.Truncated {
		out.StopReason = "max_tokens"
	}
	if schema != nil {
		out.JSON, _ = json.Marshal(value)
	}
	return out, nil
}

func labelPart(label string) string {
	if label == "" {
		return ""
	}
	return " (" + label + ")"
}

func cmpOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// modelCallError codes a failed request for the extension: held connection, nothing set up, out of time, or the
// upstream's refusal.
func modelCallError(ctx context.Context, err error) error {
	var paused *ai.PausedError
	switch {
	case errors.As(err, &paused):
		data, _ := json.Marshal(assistantwire.ModelPause{Reason: paused.Reason, Until: paused.Until.UTC().Format(time.RFC3339)})
		return &rpc.Error{Code: assistantwire.CodeModelPaused, Message: "model.complete: " + paused.Text(), Data: data}
	case ai.IsNotConfigured(err):
		return &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: " + err.Error()}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &rpc.Error{Code: assistantwire.CodeModelTimeout, Message: "model.complete: timed out"}
	}
	return &rpc.Error{Code: assistantwire.CodeModelFailed, Message: "model.complete: " + err.Error()}
}
