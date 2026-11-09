package transcript

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// supervisionUserAction keeps successful explicit user actions visible even when
// their tool calls and results are replaced by a server-side work fold.
func supervisionUserAction(run []proto.Item, uses map[string]proto.Item) bool {
	for _, result := range run {
		if result.Kind != proto.KindToolResult || result.IsError {
			continue
		}
		use, ok := uses[result.ToolUseID]
		if !ok {
			continue
		}
		name := use.ToolName
		if strings.HasPrefix(name, "mcp__") {
			if !slices.Contains(compat.MCPToolNames([]string{"notify_user", "ask_user", "supervision_finish", "task_review"}), name) {
				continue // A similarly named tool on another MCP server is not Persona's user action.
			}
			name = name[strings.LastIndex(name, "__")+2:]
		}
		switch name {
		case "notify_user", "ask_user":
			return true
		case "supervision_finish":
			var disposition struct {
				Visibility string `json:"visibility"`
			}
			if json.Unmarshal([]byte(result.Text), &disposition) == nil && disposition.Visibility == "user" {
				return true
			}
		case "task_review":
			var input struct {
				Verdict string `json:"verdict"`
			}
			if json.Unmarshal(use.ToolInput, &input) == nil && (input.Verdict == "accepted" || input.Verdict == "needs_user") {
				return true
			}
		}
	}
	return false
}
