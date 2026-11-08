package node

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// assistantAgent is the agent a launcher runs, as far as the assistant goes: claude (headless -p, stream-json) or
// codex (app-server). Anything else is refused with a reason the app shows (2026-11-01: a codex launcher used to fail
// every message on "unexpected argument '--input-format'"; Codex has its own backend since).
func (n *Node) assistantAgent(ctx context.Context, launcher string) (string, error) {
	if launcher == "claude" {
		return "claude", nil
	}
	if n.assistantRuntime(launcher) != nil {
		return assistantOnRuntime, nil
	}
	d := n.launcherProbeCached(ctx, launcher)
	switch d.Agent {
	case "", "claude": // a probe that failed: try it as claude, as before
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("启动器 %s 跑的是 %s，Persona 助手只能用启动 Claude Code、Codex 或能当助手的 runtime 的启动器（比如 DSH 插件的）：在 设置 › 助手 换一个", launcher, d.Agent)
}

// assistantResumeLauncher is what an existing conversation id resumes on when launcher was asked for. A conversation keeps the
// kind of agent it was started on: Claude Code can't resume a Codex thread, nor Codex a Claude transcript, nor either
// a runtime's conversation. The main conversation follows "Model Settings › Main Chat" on every message (#1518), so a setting
// moved to another kind made every message to it fail (`claude --resume <thread>`: "No conversation found", 2026-11-05).
// A launcher of another kind gives way to the conversation's own launcher, or the plain agent when that one is of
// another kind too (it was overwritten by such a failed start); "" for a runtime's conversation is its own (launcherOr).
// switched reports the change; a conversation of unknown kind (no transcript yet) takes launcher as asked.
func (n *Node) assistantResumeLauncher(ctx context.Context, id, launcher string, st sessionState) (own string, switched bool) {
	if id == "" || launcher == "" {
		return launcher, false
	}
	if n.runtimeAssistant(id) {
		if n.assistantRuntime(launcher) == nil {
			return "", true
		}
		return launcher, false
	}
	kind := n.conversationAgent(id)
	if kind == "" || n.launcherKind(ctx, launcher) == kind {
		return launcher, false
	}
	if st.Launcher != "" && n.launcherKind(ctx, st.Launcher) == kind {
		return st.Launcher, true
	}
	return kind, true
}

// conversationAgent is the agent whose transcript conversation id has: "claude", "codex", or "" with none yet.
func (n *Node) conversationAgent(id string) string {
	switch p := n.transcriptPath(id); {
	case p == "":
		return ""
	case strings.HasPrefix(filepath.Base(p), "rollout-"):
		return "codex"
	}
	return "claude"
}

// launcherKind is the agent launcher runs for the assistant (assistantAgent), "" when it can't run it.
func (n *Node) launcherKind(ctx context.Context, launcher string) string {
	kind, err := n.assistantAgent(ctx, launcher)
	if err != nil {
		return ""
	}
	return kind
}

// launcherOr is launcher, or the conversation's own when none is given.
func launcherOr(launcher, own string) string {
	if launcher != "" {
		return launcher
	}
	return own
}

// agentOf is assistantAgent from the probe cache, for places that only need to know (no error).
func (n *Node) agentOf(launcher string) string {
	if a, err := n.assistantAgent(context.Background(), launcher); err == nil {
		return a
	}
	return "claude"
}
