package node

import (
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
)

// An Agent the node starts runs in a pane, with its engine's environment and not the node's: herdr's server keeps the
// node's PERSONA_HOME only when the node started it (herdrServerKeep), and Orca's app runs with whatever environment
// it was started from. So what the node types into a pane says itself which data directory, and so which node, the
// Agent belongs to, and leaves out what only the assistant's processes are given: an engine started from the
// assistant's Bash tool would otherwise hand the assistant's environment to every Agent in it (#744). The files the
// node generates for its Agents (mcp.json, the hooks: generatedFiles) name the directory too, for a shell that takes
// neither.

// assistantOnlyEnv is what an Agent never takes over from its pane: what the node gives the assistant's processes alone
// (assistant_start.go, assistant_codex_spawn.go, assistant_mcp.go, assistant_guest.go, larkgate.go), and what a running
// Claude Code sets for every program it runs, marking it as that Claude's. The older spelling of each Persona variable
// goes too (assistantOnlyEnvNames). An Agent that wants CLAUDE_CODE_AUTO_COMPACT_WINDOW or ENABLE_TOOL_SEARCH gets them
// from its launcher or Claude Code's settings, not from the pane.
var assistantOnlyEnv = []string{
	"PERSONA_ASSISTANT", "PERSONA_ASSISTANT_BIN", "PERSONA_SESSION_ID", "PERSONA_SPACE", "PERSONA_GUEST",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "ENABLE_TOOL_SEARCH",
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_EFFORT",
	"CLAUDE_PID", "CLAUDE_PLUGIN_DATA",
}

// assistantOnlyEnvNames is assistantOnlyEnv with each Persona variable's older spelling after it.
func assistantOnlyEnvNames() []string {
	var out []string
	for _, k := range assistantOnlyEnv {
		current, legacy := compat.EnvNames(k)
		out = append(out, current)
		if legacy != current {
			out = append(out, legacy)
		}
	}
	return out
}

// rootAssign is `PERSONA_HOME=<dir> `, quoted with q: in front of a command another process runs (a pane's shell,
// Claude Code running a hook), it names the data directory, and so the node, that the program belongs to.
func rootAssign(dir string, q func(string) string) string {
	return "PERSONA_HOME=" + q(dir) + " "
}

// agentCommand is cmd, a command line quoted with q that starts an Agent in a pane whose shell is shells (the programs
// in front of it, takesAssign): with this node's directory in front as PERSONA_HOME where the shell takes `VAR=val
// cmd`, and, in the sh family, after `unset` of assistantOnlyEnvNames, joined with && so that a start whose shell
// refused the unset does not run. fish has no unset: there the directory alone. Another shell, or a node without a
// directory, gets cmd as it is.
func (n *Node) agentCommand(cmd string, q func(string) string, shells ...string) string {
	if n.dir == "" || !takesAssign(shells...) {
		return cmd
	}
	cmd = rootAssign(n.dir, q) + cmd
	for _, s := range shells {
		if strings.TrimPrefix(filepath.Base(s), "-") == "fish" {
			return cmd
		}
	}
	return "unset " + strings.Join(assistantOnlyEnvNames(), " ") + " && " + cmd
}
