package extwire

import (
	"net/url"
	"strings"
)

// An extension's token (docs/extensions/extensions.md "Permissions") opens the routes of the scopes the user granted the extension and
// no other; the manager made the token and checks it (extensions.authorize), with this table of the hub's routes.

// RouteScopes are the routes an extension's token may call, by the pattern they are registered with, and the scope
// each needs. A route not here is for full credentials only: deciding that a new route is open to extensions means
// adding it here (TestRouteScopes keeps the list of every route and what it was decided).
var RouteScopes = map[string]string{
	"GET /api/v1/kb/{kind}":                              ScopeKBRead,
	"GET /api/v1/kb/{kind}/{id}":                         ScopeKBRead,
	"GET /api/v1/kb/chats/{id}/context":                  ScopeKBRead,
	"GET /api/v1/kb/chats/{id}/days/{day}":               ScopeKBRead,
	"GET /api/v1/kb/moments/{id}":                        ScopeKBRead,
	"GET /api/v1/kb-search":                              ScopeKBRead,
	"PUT /api/v1/kb/{kind}/{id}":                         ScopeKBWrite,
	"GET /api/v1/inbox":                                  ScopeInboxRead,
	"GET /api/v1/inbox/{id}":                             ScopeInboxRead,
	"GET /api/v1/inbox/mutes":                            ScopeInboxRead,
	"GET /api/v1/inbox/policies":                         ScopeInboxRead,
	"POST /api/v1/inbox/done":                            ScopeInboxWrite,
	"POST /api/v1/inbox/bulk":                            ScopeInboxWrite,
	"POST /api/v1/inbox/{id}/done":                       ScopeInboxWrite,
	"POST /api/v1/inbox/{id}/draft":                      ScopeInboxWrite,
	"POST /api/v1/inbox/{id}/reply":                      ScopeInboxWrite,
	"POST /api/v1/inbox/{id}/suggestion":                 ScopeInboxWrite,
	"POST /api/v1/inbox/{id}/handoff":                    ScopeInboxWrite,
	"POST /api/v1/inbox/{id}/doc/status":                 ScopeInboxWrite,
	"PUT /api/v1/inbox/{id}/parent":                      ScopeInboxWrite,
	"GET /api/v1/inbox/tree/{kind}/{id...}":              ScopeInboxRead,
	"POST /api/v1/inbox/mutes":                           ScopeInboxWrite,
	"DELETE /api/v1/inbox/mutes/{id}":                    ScopeInboxWrite,
	"POST /api/v1/inbox/policies":                        ScopeInboxWrite,
	"PATCH /api/v1/inbox/policies/{id}":                  ScopeInboxWrite,
	"DELETE /api/v1/inbox/policies/{id}":                 ScopeInboxWrite,
	"POST /api/v1/inbox/refresh":                         ScopeInboxWrite,
	"GET /api/v1/sessions/archived":                      ScopeAgentsRead,
	"GET /api/v1/sessions":                               ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/info":              ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items":             ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full":  ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image": ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/screen":            ScopeAgentsRead,
	"GET /api/v1/interactions":                           ScopeAgentsRead,
	"GET /api/v1/interactions/history":                   ScopeAgentsRead,
	"GET /api/v1/agents":                                 ScopeAgentsRead,
	"GET /api/v1/agents/sessions":                        ScopeAgentsRead,
	"GET /api/v1/agents/sessions/{id}":                   ScopeAgentsRead,
	"GET /api/v1/agents/sessions/{id}/messages":          ScopeAgentsRead,
	"GET /api/v1/agents/sessions/{id}/avatar":            ScopeAgentsRead,
	"GET /api/v1/workspaces":                             ScopeAgentsRead,
	"GET /api/v1/workspace-board":                        ScopeAgentsRead,
	"POST /api/v1/sessions":                              ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/messages":         ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/interrupt":        ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/queue":            ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/queue/send-now":   ScopeAgentsControl, // #6088
	"POST /api/v1/sessions/{mid}/{sid}/seen":             ScopeAgentsControl,
	"POST /api/v1/interactions/{id}/choose":              ScopeAgentsControl,
	"POST /api/v1/interactions/{id}/resolve":             ScopeAgentsControl,
	"POST /api/v1/interactions/{id}/reanswer":            ScopeAgentsControl, // "Change answer": a message to the agent
	"POST /api/v1/agents/sessions/{id}/messages":         ScopeAgentsControl,
	"POST /api/v1/agents/sessions/{id}/read":             ScopeAgentsControl,
	"GET /api/v1/machines":                               ScopeMachinesRead,
	"GET /api/v1/machines/{id}/metrics":                  ScopeMachinesRead,
	"GET /api/v1/machines/{id}/usage":                    ScopeMachinesRead,
	"GET /api/v1/machines/{mid}/stat":                    ScopeMachinesRead,
	"POST /api/v1/notify":                                ScopeNotify,
	"GET /api/v1/assistant":                              ScopeAssistantAsk,
	"GET /api/v1/assistant/capabilities":                 ScopeAssistantAsk,
	"POST /api/v1/assistant/conversations":               ScopeAssistantAsk,
	"GET /api/v1/assistant/conversations/{id}/feed":      ScopeAssistantAsk,
	"POST /api/v1/assistant/conversations/{id}/stop":     ScopeAssistantAsk, // /agent's /stop (#3308)
	"GET /api/v1/assistant/reporters":                    ScopeAssistantAsk,
	// The todos (the assistant's, relayed): reading them, and adding, editing and attaching agents and spaces. Deleting
	// a todo, its decisions, the done check and a retry stay with full credentials.
	"GET /api/v1/todos":                                 ScopeTodosRead,
	"GET /api/v1/todos/{id}/decisions":                  ScopeTodosRead,
	"POST /api/v1/todos":                                ScopeTodosWrite,
	"PATCH /api/v1/todos/{id}":                          ScopeTodosWrite,
	"PUT /api/v1/todos/{id}/agents/{mid}/{sid}":         ScopeTodosWrite,
	"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}":      ScopeTodosWrite,
	"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}":    ScopeTodosWrite,
	"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}": ScopeTodosWrite,
	// Another extension's own HTTP: the scope names which (ExtensionCallScope).
	"/api/v1/extensions/{id}/": ScopeCallPrefix,
}

// ExtensionCallScope is the scope a request to an extension's own HTTP needs (extensions.call:<id>); "" for the hub's
// routes under /api/v1/extensions/<id>/-/, which are no extension's to open. path is the escaped path, the one the
// manager's mux routes the request by: its segments are read as the mux reads them (each one unescaped on its own), so
// an escaped "-" is the hub's "-" and an escaped "/" stays inside the id.
func ExtensionCallScope(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/v1/extensions/")
	if !ok {
		return ""
	}
	rawID, after, _ := strings.Cut(rest, "/")
	rawNext, _, _ := strings.Cut(after, "/")
	id, err := url.PathUnescape(rawID)
	if err != nil {
		return ""
	}
	next, err := url.PathUnescape(rawNext)
	if err != nil || id == "" || id == "-" || next == "-" {
		return ""
	}
	return ScopeCallPrefix + id
}
