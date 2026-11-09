package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A start may name the host it opens in (session.start's host, #2576): one of the machine's enabled hosts, instead of
// its default host. Carried in the start's context, like an adopted pane (panes_actions.go).

type startHostKey struct{}

// withStartHost has the start in ctx open in host kind ("" : the default host).
func withStartHost(ctx context.Context, kind string) context.Context {
	if kind == "" {
		return ctx
	}
	return context.WithValue(ctx, startHostKey{}, kind)
}

// startKind is the host a start in ctx opens in: the one it named, else the machine's default host.
func (n *Node) startKind(ctx context.Context) string {
	if k, _ := ctx.Value(startHostKey{}).(string); k != "" {
		return k
	}
	return n.defaultKind()
}

// checkStartHost refuses a start naming a host the machine does not enable, or one that cannot start sessions
// (herdr-core); nothing is started. "" is the default host, always fine here (the empty set is refused later).
func (n *Node) checkStartHost(kind string) error {
	if kind == "" {
		return nil
	}
	if !proto.IsEngineHost(kind) {
		return fmt.Errorf("unknown host %q", kind)
	}
	name := proto.EngineNames[kind]
	if !n.hostEnabled(kind) {
		return refusal(engineMissing, fmt.Sprintf("这台电脑没有启用 %s，不能在 %s 里新开会话，这次没有发送。", name, name), nil)
	}
	if kind == proto.EnginePaseo {
		return nil // its runtime starts sessions (runtimecontrol.go)
	}
	if _, ok := n.hosts.Get(kind).(host.Launcher); !ok {
		return cannotStartRefusal(kind)
	}
	return nil
}

// launcherFor is the host of kind as a Launcher. One that cannot start sessions (herdr-core) gets checkStartHost's
// refusal, so a start on the machine's default host does not show "unsupported: herdr-core cannot do this".
func (n *Node) launcherFor(kind string) (host.Launcher, error) {
	launch, err := hostCapFor[host.Launcher](n, kind)
	if errors.Is(err, host.ErrUnsupported) {
		return nil, cannotStartRefusal(kind)
	}
	return launch, err
}

// cannotStartRefusal refuses a start in host kind, which cannot start sessions; nothing is sent. Herdr App's says
// which engines can.
func cannotStartRefusal(kind string) error {
	msg := fmt.Sprintf("%s 不支持从手机新开会话，这次没有发送。", proto.EngineNames[kind])
	if kind == proto.EngineHerdrCore {
		msg += "要从手机新开，在 设置 › 机器 › 这台电脑 › 引擎 里改用 herdr 或 herdr + Herdr App。"
	}
	return refusal(engineMissing, msg, nil)
}

// hostCapFor is the host of kind as capability C (hostCap for a start that named its host).
func hostCapFor[C any](n *Node, kind string) (C, error) {
	var none C
	if kind == "" {
		return none, noEngineRefusal()
	}
	h := n.hosts.Get(kind)
	if h == nil {
		return none, fmt.Errorf("no %s host on this machine", kind)
	}
	c, ok := h.(C)
	if !ok {
		return none, unsupported(h)
	}
	return c, nil
}
