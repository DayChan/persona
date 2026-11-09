// Package sessionorigin classifies where runtime sessions were opened.
package sessionorigin

import (
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Of returns where a runtime session was opened, matching the clients'
// sessionSource rule. SourceKind describes transport, not pane/host origin.
// Desktop markers take precedence even when a row also carries a terminal pane.
func Of(s proto.Session) string {
	if s.SpaceKind == "codex_app" || s.Host == "paseo" || (s.SourceKind == "runtime" && s.SourceInstanceID == "orca") {
		return "desktop"
	}
	if s.PaneID == "" {
		return "unknown"
	}
	if host.ParsePane(s.PaneID).Kind == host.Botmux {
		return "feishu"
	}
	return "terminal"
}
