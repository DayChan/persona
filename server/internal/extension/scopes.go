package extension

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Defined in the contract package extwire (docs/server/architecture.md "Contract packages"); the old names stay.
const (
	ScopeAgentsControl        = extwire.ScopeAgentsControl
	ScopeAgentsPermissionMode = extwire.ScopeAgentsPermissionMode
	ScopeAgentsRead           = extwire.ScopeAgentsRead
	ScopeAssistantAsk         = extwire.ScopeAssistantAsk
	ScopeCallPrefix           = extwire.ScopeCallPrefix
	ScopeInboxRead            = extwire.ScopeInboxRead
	ScopeInboxWrite           = extwire.ScopeInboxWrite
	ScopeKBRead               = extwire.ScopeKBRead
	ScopeKBWrite              = extwire.ScopeKBWrite
	ScopeMachinesRead         = extwire.ScopeMachinesRead
	ScopeNotify               = extwire.ScopeNotify
	ScopeTodosRead            = extwire.ScopeTodosRead
	ScopeTodosWrite           = extwire.ScopeTodosWrite
	ScopeModels               = extwire.ScopeModels
	ScopeAssistantRead        = extwire.ScopeAssistantRead
)

// HubScopes are the scopes, in the order the install screen lists them.
var HubScopes = []string{ScopeKBRead, ScopeKBWrite, ScopeInboxRead, ScopeInboxWrite, ScopeAgentsRead, ScopeAgentsControl,
	ScopeAgentsPermissionMode, ScopeMachinesRead, ScopeNotify, ScopeAssistantAsk, ScopeAssistantRead, ScopeTodosRead,
	ScopeTodosWrite, ScopeModels}

// InEveryScope says whether a token of every scope (an extension granted the hub's full token the old way, hub_api,
// that gets one of every scope instead) has scope s: all but agents.permission_mode, which an extension gets only when
// its manifest names it.
func InEveryScope(s string) bool { return s != ScopeAgentsPermissionMode }

// EveryScope is the scopes of a token of every scope (InEveryScope), in HubScopes' order.
func EveryScope() []string {
	return slices.DeleteFunc(slices.Clone(HubScopes), func(s string) bool { return !InEveryScope(s) })
}

var scopeTitles = map[string]string{
	ScopeKBRead:               "scope_kb_read",
	ScopeKBWrite:              "scope_kb_write",
	ScopeInboxRead:            "scope_inbox_read",
	ScopeInboxWrite:           "scope_inbox_write",
	ScopeAgentsRead:           "scope_agents_read",
	ScopeAgentsControl:        "scope_agents_control",
	ScopeAgentsPermissionMode: "scope_agents_permission_mode",
	ScopeMachinesRead:         "scope_machines_read",
	ScopeNotify:               "scope_notify",
	ScopeAssistantAsk:         "scope_assistant_ask",
	ScopeTodosRead:            "scope_todos_read",
	ScopeTodosWrite:           "scope_todos_write",
	ScopeModels:               "scope_models",
	ScopeAssistantRead:        "scope_assistant_read",
}

// ScopeTitle is how the install screen says scope s; ok false for what is not a scope.
func ScopeTitle(s string) (string, bool) { return ScopeTitleIn(i18n.Default, s) }

// ScopeTitleIn names a known scope in the requested language.
func ScopeTitleIn(l i18n.Lang, s string) (string, bool) {
	if id, ok := strings.CutPrefix(s, ScopeCallPrefix); ok {
		if !idPattern.MatchString(id) {
			return "", false
		}
		return permissionText(l, "scope_call", i18n.Args{"id": id}), true
	}
	key, ok := scopeTitles[s]
	if !ok {
		return "", false
	}
	return permissionText(l, key), true
}

// hubLine is the line for scopes (all an extension asks for, or those an upgrade adds), in HubScopes' order.
func hubLine(l i18n.Lang, title string, scopes []string) PermissionLine {
	var what []string
	for _, known := range HubScopes {
		for _, s := range scopes {
			if s == known {
				t, _ := ScopeTitleIn(l, s)
				what = append(what, t)
			}
		}
	}
	for _, s := range scopes {
		if strings.HasPrefix(s, ScopeCallPrefix) {
			t, _ := ScopeTitleIn(l, s)
			what = append(what, t)
		}
	}
	return PermissionLine{Key: "hub", Title: title, Detail: strings.Join(what, permissionText(l, "sentence_separator"))}
}
