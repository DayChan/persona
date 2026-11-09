package node

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// launchers.own_service (docs/integrations/launchers.md "Device scope and on-demand delivery"): whether an agent harness
// already has a usable model service of its own on this computer, so a Persona-managed provider that skips such
// computers is not used here. Read with what the node already checks, never a model request:
//
//   - Claude Code: a Claude launcher in the login shell's rc file or on its PATH (launchers.list), or plain claude
//     signed in or given a key or gateway by its environment (claude.auth_status, claudeauth.go).
//   - Codex: a Codex alias in the rc file, or `codex login status` succeeding (a ChatGPT or API key login).
//   - Others (Antigravity, Oh My Pi): not known.
//
// Kept ten minutes (a minute when it could not tell).

var ownServiceCache struct {
	sync.Mutex
	at map[string]time.Time
	st map[string]proto.OwnServiceState
}

var (
	ownServiceFresh = 10 * time.Minute
	ownServiceRetry = time.Minute
	// codexLoginTimeout bounds `codex login status` in the login shell.
	codexLoginTimeout = 15 * time.Second
)

// ownService is what agent's harness has of its own here (cached).
func (n *Node) ownService(ctx context.Context, agent string) proto.OwnServiceState {
	key := n.home + "\x00" + agent
	ownServiceCache.Lock()
	if at, ok := ownServiceCache.at[key]; ok {
		st := ownServiceCache.st[key]
		fresh := ownServiceFresh
		if !st.Known {
			fresh = ownServiceRetry
		}
		if time.Since(at) < fresh {
			ownServiceCache.Unlock()
			return st
		}
	}
	ownServiceCache.Unlock()
	st := n.findOwnService(ctx, agent)
	ownServiceCache.Lock()
	if ownServiceCache.at == nil {
		ownServiceCache.at, ownServiceCache.st = map[string]time.Time{}, map[string]proto.OwnServiceState{}
	}
	ownServiceCache.at[key], ownServiceCache.st[key] = time.Now(), st
	ownServiceCache.Unlock()
	return st
}

func (n *Node) findOwnService(ctx context.Context, agent string) proto.OwnServiceState {
	st := proto.OwnServiceState{Agent: agent}
	if agent == "" {
		st.Agent, agent = "claude", "claude"
	}
	switch agent {
	case "claude", "codex":
	default:
		return st // not known for this harness
	}
	if list, err := n.launchers(ctx); err == nil && ownLauncher(list, agent) {
		st.Known, st.Own, st.Via = true, true, "launcher"
		return st
	}
	if agent == "claude" {
		auth, err := n.claudeAuthStatus(ctx, "claude")
		switch {
		case err != nil:
		case auth.State == proto.ClaudeLoggedIn:
			st.Known, st.Own, st.Via = true, true, "login"
			if auth.KeySource != "" {
				st.Via = "api_key"
			}
		case auth.State == proto.ClaudeNotLoggedIn:
			st.Known = true
		}
		return st
	}
	return codexLoginStatus(ctx, st)
}

// ownLauncher: the list has a launcher of agent's that the user defined (a function, an alias or a wrapper script),
// not the plain CLI and not a runtime's.
func ownLauncher(list []proto.Launcher, agent string) bool {
	for _, l := range list {
		if l.Source != "" && l.Source != "zshrc" && l.Source != "path" {
			continue
		}
		a := l.Agent
		if a == "" {
			a = "claude"
		}
		if a == agent && l.Name != agent {
			return true
		}
	}
	return false
}

// codexLoginStatus runs `codex login status` in the login shell: exit 0 is a login (ChatGPT or API key), another
// exit says none; no codex there is none either.
func codexLoginStatus(ctx context.Context, st proto.OwnServiceState) proto.OwnServiceState {
	cctx, cancel := context.WithTimeout(ctx, codexLoginTimeout)
	defer cancel()
	cmd := loginShell(cctx, "command -v codex >/dev/null 2>&1 || exit 3; codex login status </dev/null")
	killGroupOnCancel(cmd)
	err := cmd.Run()
	if cctx.Err() != nil {
		return st // timed out: not known
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		st.Known, st.Own, st.Via = true, true, "login"
	case errors.As(err, &ee):
		st.Known = true // not signed in, or no codex
	}
	return st
}
