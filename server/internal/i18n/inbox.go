package i18n

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// InboxText recognizes only hub-owned inbox labels and errors, including old stored or RPC-flattened text.
// User/model content and arbitrary connector diagnostics are not reverse-translated.
func InboxText(l Lang, text string) string {
	if l == "" || text == "" {
		return text
	}
	forms := inboxTemplates()
	for _, n := range forms.candidates(text) {
		form := &forms.forms[n]
		match := form.match(text)
		if match == nil {
			continue
		}
		args := Args{}
		for i, name := range form.names {
			value := match[i+1]
			if name == "count" {
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return text
				}
				args[name] = n
			} else {
				args[name] = value
			}
		}
		return T(l, form.key, args)
	}
	return text
}

var inboxTemplates = sync.OnceValue(func() *templateIndex {
	return indexTemplates(makeCatalogTemplates(func(key string) bool {
		for _, prefix := range []string{"inbox-errors.", "assistant-inbox_agent.", "assistant-inbox_triage.", "assistant-inbox_admit.", "assistant-inbox_chain."} {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return key == "assistant-inbox.by_rules" || key == "assistant-inbox.muted"
	}))
})

// InboxJSON translates copies at the public response boundary, including saved reply receipts. Only known
// item/status/error slots are visited: metadata, context, summaries, suggestions, names and user text stay intact.
// Return the original bytes when no recognized text changes, avoiding any impact on unrelated responses.
func InboxJSON(l Lang, raw []byte) []byte {
	if l == "" || !json.Valid(raw) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) != nil {
		return raw
	}
	changed := false
	translate := func(m map[string]any, key string) {
		if text, ok := m[key].(string); ok {
			if localized := InboxText(l, text); localized != text {
				m[key], changed = localized, true
			}
		}
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				visit(item)
			}
		case map[string]any:
			translate(x, "error")
			if id, ok := x["id"].(string); ok && id != "" && x["source"] != nil && x["kind"] != nil {
				for _, field := range []string{"agent", "handling", "doc", "moment"} {
					if part, ok := x[field].(map[string]any); ok {
						translate(part, "error")
					}
				}
				if x["triaged_by"] == "rules" {
					translate(x, "reason")
				}
				if chain, ok := x["chain"].([]any); ok {
					for _, entry := range chain {
						if e, ok := entry.(map[string]any); ok {
							translate(e, "text")
							if e["kind"] == "failed" {
								translate(e, "detail")
							}
						}
					}
				}
				return
			}
			for _, field := range []string{"items", "item", "changed"} {
				if nested, ok := x[field]; ok {
					visit(nested)
				}
			}
			if triage, ok := x["triage"].(map[string]any); ok {
				translate(triage, "reason")
				if sources, ok := triage["sources"].([]any); ok {
					for _, source := range sources {
						if s, ok := source.(map[string]any); ok {
							translate(s, "reason")
							translate(s, "read_reason")
						}
					}
				}
			}
		}
	}
	visit(value)
	if !changed {
		return raw
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return out
}
