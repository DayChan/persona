package node

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// runtimeStart starts a session in the runtime offering the launcher; nil when no runtime does.
func (n *Node) runtimeStart(ctx context.Context, p runtimeStartParams) (*proto.Session, error, bool) {
	h := n.runtimes.launcherRuntime(p.Launcher)
	if h == nil {
		if m, ok := n.managedLauncher(p.Launcher); ok && m.AgentType() == "agy" {
			h = n.runtimes.launcherRuntime("agy")
			if p.Model == "" {
				p.Model = m.Model
			}
		}
	}
	if h == nil || shellLauncherNames()[p.Launcher] { // the login shell's launcher of that name runs, as the form lists
		return nil, nil, false
	}
	s, err := n.runtimeStartOn(ctx, h, p)
	return s, err, true
}

// runtimeStartOn starts a session in runtime h (session.start): the directory, the first message, the launcher picked
// and what the form chose with it (model, permission mode, effort), the session to continue. Fields are sent only when
// set: a runtime that knows none of them still starts its own way.
func (n *Node) runtimeStartOn(ctx context.Context, h *runtimeConn, p runtimeStartParams) (*proto.Session, error) {
	if p.PermissionMode == "plan" {
		// Runtime starts have no contract to enforce this mode before the first prompt.
		// Even a listed Claude provider may disappear and fall back to a writable provider.
		return nil, readonlyStartUnsupported()
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	params := map[string]any{"cwd": p.Cwd, "prompt": p.Prompt}
	for k, v := range map[string]string{"launcher": p.Launcher, "model": p.Model, "permission_mode": p.PermissionMode,
		"effort": p.Effort, "workspace_id": p.WorkspaceID, "resume": p.Resume} {
		if v != "" {
			params[k] = v
		}
	}
	if p.Shell {
		params["shell_launcher"] = true
	}
	if p.OpenElsewhere {
		params["open_elsewhere"] = true
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	if err := h.peer.Call(ctx, "session.start", params, &res); err != nil {
		return nil, err
	}
	return n.listStarted(h, res.Session)
}

// listStarted lists a session h just started right away, before the runtime's next runtime.sessions, so the chat
// that opens next finds it. Who answers for it is decided as for any list (recomputeOwners): a runtime that starts
// sessions elsewhere (pi's companion starts them in pi-web) reports them offline, and the one running it, if it has
// listed it already, keeps it.
func (n *Node) listStarted(h *runtimeConn, started proto.Session) (*proto.Session, error) {
	list := normalizeRuntimeSessions(h, []proto.Session{started})
	if len(list) == 0 {
		return nil, fmt.Errorf("%s started a session without a valid id", h.label())
	}
	s := list[0]
	b := n.runtimes
	b.mu.Lock()
	h.mu.Lock()
	found := false
	for i := range h.sessions {
		if h.sessions[i].SessionID == s.SessionID {
			h.sessions[i], found = s, true
		}
	}
	if !found {
		h.sessions = append(h.sessions, s)
	}
	h.mu.Unlock()
	b.recomputeOwners()
	b.remember(h, s.SessionID, time.Now())
	if o := b.owner[s.SessionID]; o != nil && o != h {
		o.mu.Lock()
		for _, r := range o.sessions {
			if r.SessionID == s.SessionID {
				s = r // the running one's row: its status, title, agent
			}
		}
		o.mu.Unlock()
	}
	b.mu.Unlock()
	n.publish(n.sessionList(context.Background(), false))
	return &s, nil
}
