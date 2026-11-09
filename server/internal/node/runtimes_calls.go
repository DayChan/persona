package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// claimMethods are the calls that ask the connected runtimes about a session nobody claimed yet: what the apps do
// with a session they show (open it, page it, send to it, stop it).
var claimMethods = map[string]bool{"session.send": true, "session.items": true, "session.item": true, "session.image": true,
	"session.watch": true, "session.info": true, "session.interrupt": true}

// runtimeClaimTimeout bounds asking one runtime whether it has a session.
var runtimeClaimTimeout = 5 * time.Second

// runtimeFor is the runtime a hub call about session id goes to: the one listing it, else the one that listed it
// before (an older session it no longer lists), else — for a session this machine has nothing else for (no agent, no
// transcript) — the first connected runtime that answers for it. A session of a runtime that is not connected now is
// refused (errRuntimeAway): it is not a Claude session to resume in a terminal.
func (n *Node) runtimeFor(ctx context.Context, method, id string) (*runtimeConn, error) {
	if id == "" || !strings.HasPrefix(method, "session.") || method == "session.start" {
		return nil, nil
	}
	h, label, known := n.runtimes.belongs(id)
	if h != nil {
		return h, nil
	}
	if known {
		if claimMethods[method] || method == "session.rename" || method == "session.close" || method == "session.archive" || method == "session.unarchive" {
			return nil, errRuntimeAway(label)
		}
		return nil, nil // unwatching and the like: nothing to refuse
	}
	if !claimMethods[method] || !runtimeIDRe.MatchString(id) {
		return nil, nil
	}
	conns := n.runtimes.askable(id)
	if len(conns) == 0 || n.hasOwnSession(id) {
		return nil, nil
	}
	for _, c := range conns {
		cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		err := c.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, &page)
		cancel()
		if err == nil {
			n.runtimes.claim(c, id)
			return c, nil
		}
	}
	n.runtimes.unclaim(id)
	return nil, nil
}

// hasOwnSession: this machine has the session itself (a live agent in a terminal, a Claude / Codex and compatible variants record).
func (n *Node) hasOwnSession(id string) bool {
	if _, ok := n.agent(id); ok {
		return true
	}
	if n.transcriptPath(id) != "" {
		return true
	}
	for _, p := range []string{n.codexPath(id), n.traexPath(id)} {
		if _, err := os.Stat(p); p != "" && err == nil {
			return true
		}
	}
	return false
}

// errRuntimeAway: a session of a runtime that is not connected (its app is closed, the plugin is off). Nothing was
// sent; the hub may let the app send it again.
func errRuntimeAway(label string) error {
	return &rpc.Error{Message: fmt.Sprintf("%s 没有连上这台电脑的 Persona（%s 没在运行，或插件没装好），没有发出去", label, label),
		Code: "runtime_away", Data: json.RawMessage(`{"delivered":"none"}`)}
}

// errRuntimeUnsupported: a session.* call the session's runtime has nothing for (no terminal, no TUI modes, a cap it
// did not declare). Like a terminal host lacking a capability (host.ErrUnsupported), the text starts with
// "unsupported": the hub reads that prefix (a terminal stream it cannot open falls back to reading the screen).
func errRuntimeUnsupported(label, what string) error {
	return &rpc.Error{Message: fmt.Sprintf("unsupported: %s 的会话不支持%s", label, what), Code: "unsupported"}
}

// runtimeCallParams are the parameters of a hub call about a runtime's session that the node looks at.
type runtimeCallParams struct {
	SessionID      string        `json:"session_id"`
	Text           string        `json:"text"`
	Images         []proto.Image `json:"images"`
	SinceSeq       int           `json:"since_seq"`
	FoldWork       bool          `json:"fold_work"`
	LazyImages     bool          `json:"lazy_images"`
	UserInputs     bool          `json:"user_inputs"`
	AfterSeq       *int          `json:"after_seq"`
	ThroughSeq     int           `json:"through_seq"`
	Title          *string       `json:"title"`
	ItemID         string        `json:"item_id"`
	PermissionMode string        `json:"permission_mode"`
	Effort         string        `json:"effort"`
}

// runtimeHandle serves a hub call for a session a runtime lists; handled is false for methods that are not about one
// session (the node answers those itself).
func (n *Node) runtimeHandle(ctx context.Context, h *runtimeConn, method string, params json.RawMessage) (res any, err error, handled bool) {
	label := h.label()
	h.mu.Lock()
	caps := h.hello.Caps
	h.mu.Unlock()
	call := func(m string, p any) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
		defer cancel()
		var out json.RawMessage
		err := h.peer.Call(ctx, m, p, &out)
		if err != nil && !isRPCError(err) {
			err = fmt.Errorf("%s: %w", label, err)
		}
		return out, err
	}
	var p runtimeCallParams
	_ = json.Unmarshal(params, &p)
	// A session whose conversation is a Claude transcript here (runtimetranscript.go): its content is the node's.
	if n.runtimeTranscript(p.SessionID) != "" {
		if transcriptCalls[method] {
			return nil, nil, false // node.handle reads the transcript (n.file)
		}
		if res, err, handled := n.runtimeTranscriptCall(ctx, h, method, p, call); handled {
			return res, err, true
		}
	}
	// A session in a terminal pane (a pi in herdr, pi.go): the pane is the engine's — the screen, the keys, closing
	// it, its tab's name — and the rest is the runtime's.
	if a, inPane := n.agent(p.SessionID); inPane && a.PaneID != "" {
		switch method {
		case "session.keys", "session.screen", "session.screen.before", "terminal.watch", "terminal.unwatch", "session.close":
			return nil, nil, false
		case "session.rename":
			if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
				return nil, nil, false
			}
		}
	}
	switch method {
	case "session.items":
		if p.UserInputs {
			index, err := n.runtimeUserInputs(ctx, p.SessionID, func(params any) (json.RawMessage, error) { return call("session.items", params) })
			return index, err, true
		}
		out, err := call("session.items", params)
		if err != nil {
			return nil, err, true
		}
		out = n.echoedRaw(p.SessionID, out, "items") // echo.go
		if !p.FoldWork || p.AfterSeq != nil || p.ThroughSeq > 0 {
			return out, nil, true
		}
		// Pages counted back from the end fold the agent's work, as for transcripts (transcript.FoldWork).
		var page map[string]json.RawMessage
		if json.Unmarshal(out, &page) != nil {
			return out, nil, true
		}
		var items []proto.Item
		if json.Unmarshal(page["items"], &items) != nil {
			return out, nil, true
		}
		folded, _ := json.Marshal(nonNil(transcript.FoldWork(items)))
		page["items"] = folded
		return page, nil, true
	case "session.image", "session.item":
		out, err := call(method, params)
		return out, err, true
	case "session.watch":
		n.runtimes.watch(h, p.SessionID, p.SinceSeq)
		_, err := call("session.watch", map[string]any{"session_id": p.SessionID, "since_seq": p.SinceSeq})
		return okResult(err), err, true
	case "session.unwatch":
		n.runtimes.unwatch(p.SessionID)
		_, err := call("session.unwatch", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.send":
		if !caps.Send {
			return nil, errRuntimeUnsupported(label, "从手机发消息"), true
		}
		// A session that ran in the terminal engine and has stopped: resumed there, then sent to (pi.go).
		if argv := h.resumeArgv(p.SessionID); len(argv) > 0 && n.runtimeStatus(h, p.SessionID) == proto.StatusOffline {
			res, err := n.resumeInEngine(ctx, h, p.SessionID, argv, params)
			return res, err, true
		}
		var opts sendOpts
		_ = json.Unmarshal(params, &opts)
		err := n.sends.once(opts.ClientMsgID, func() error {
			var after int
			if opts.ClientMsgID != "" {
				after = n.runtimes.lastPushed(p.SessionID)
				if n.runtimeTranscript(p.SessionID) != "" { // its items are the transcript's: its copy comes after its end
					after = n.echoBase(p.SessionID)
				}
			}
			return n.sendEchoed(p.SessionID, opts.ClientMsgID, p.Text, len(p.Images), after, func() error { // echo.go
				_, err := call("session.send", map[string]any{"session_id": p.SessionID, "text": p.Text, "images": p.Images,
					"client_msg_id": opts.ClientMsgID})
				return err
			})
		})
		if rpcCode(err) == runtimeHandover && ctx.Value(handingOver{}) == nil {
			res, err := n.handOver(ctx, h, p.SessionID, params)
			return res, err, true
		}
		return okResult(err), err, true
	case "session.interrupt":
		if !caps.Interrupt {
			return nil, errRuntimeUnsupported(label, "中断"), true
		}
		_, err := call("session.interrupt", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.close":
		if !caps.Close {
			return nil, errRuntimeUnsupported(label, "从手机关闭"), true
		}
		_, err := call("session.close", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.archive", "session.unarchive":
		// Its own archive (archive.go): one that has none keeps the archive Persona's alone.
		if !caps.Archive || strings.Contains(p.SessionID, subagentSep) {
			return archiveUnsupported(label + " has no archive the node can drive"), nil, true
		}
		if _, err := call(method, map[string]any{"session_id": p.SessionID}); err != nil {
			return nil, err, true
		}
		return nativeArchiveResult{Native: nativeArchiveDone, Via: "runtime"}, nil, true
	case "session.rename":
		if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
			return okResult(nil), nil, true // a tab name: these sessions have no tab
		}
		if !caps.Rename {
			return nil, errRuntimeUnsupported(label, "改名"), true
		}
		_, err := call("session.rename", map[string]any{"session_id": p.SessionID, "title": strings.TrimSpace(*p.Title)})
		return okResult(err), err, true
	case "session.screen":
		return map[string]any{"text": "", "status": n.runtimeStatus(h, p.SessionID)}, nil, true
	case "session.info":
		// The counts a transcript gives are unknown here: zero, which the apps read as "none yet" (an iOS app older
		// than this decodes them as required numbers).
		info := map[string]any{"session_id": p.SessionID, "agent": h.agent(), "context_tokens": 0, "output_tokens": 0,
			"compactions": 0, "turns": 0}
		for _, s := range h.sessionsCopy() {
			if s.SessionID == p.SessionID {
				info["cwd"], info["model"], info["agent"], info["last_activity"] = s.Cwd, s.Model, s.Agent, s.UpdatedAt
				info["permission_mode"], info["effort"] = s.PermissionMode, s.Effort
			}
		}
		// Not in its list (an older session it still has): what the node knows without the list.
		return info, nil, true
	case "session.keys", "session.screen.before", "terminal.watch":
		return nil, errRuntimeUnsupported(label, "终端"), true
	case "terminal.unwatch":
		return okResult(nil), nil, true
	case "session.modes":
		if !caps.Modes || strings.Contains(p.SessionID, subagentSep) {
			return nil, errRuntimeUnsupported(label, "这个操作"), true
		}
		if err := validModes(p.PermissionMode, p.Effort); err != nil {
			return nil, err, true
		}
		_, err := call("session.modes", map[string]any{"session_id": p.SessionID, "permission_mode": p.PermissionMode, "effort": p.Effort})
		return okResult(err), err, true
	case "session.queue.edit", "session.queue.send_now":
		return nil, errRuntimeUnsupported(label, "这个操作"), true
	}
	return nil, nil, false
}

func (n *Node) runtimeStatus(h *runtimeConn, id string) string {
	for _, s := range h.sessionsCopy() {
		if s.SessionID == id {
			return s.Status
		}
	}
	return proto.StatusUnknown
}

// runtimeHandover is the code of a send a runtime hands over instead of taking (it lists the session from disk, and
// has asked the program that runs it to start): handOver delivers it to the runtime that takes the session.
const runtimeHandover = "handover"

// runtimeHandoverWait bounds the wait for the runtime that takes a handed-over session.
var runtimeHandoverWait = 45 * time.Second

// handingOver marks the context of a send handOver forwards: the runtime it reaches cannot hand it over again.
type handingOver struct{}

// handOver delivers a send that from (the session's owner) handed over: to the connection that lists the session as
// running once one does (prefer_running), or to the first other runtime that answers for it (an older session it
// does not list; the claim path of runtimeFor), whichever comes first within runtimeHandoverWait. Nothing is sent
// when none does.
func (n *Node) handOver(ctx context.Context, from *runtimeConn, id string, params json.RawMessage) (any, error) {
	ctx = context.WithValue(ctx, handingOver{}, true)
	forward := func(to *runtimeConn) (any, error) {
		res, err, _ := n.runtimeHandle(ctx, to, "session.send", params)
		return res, err
	}
	deadline := time.Now().Add(runtimeHandoverWait)
	var asked time.Time
	for {
		if r, owner, ok := n.runtimes.rowOf(id); ok && owner != from && r.Status != proto.StatusOffline {
			return forward(owner)
		}
		if time.Since(asked) > 2*time.Second {
			asked = time.Now()
			for _, c := range n.runtimes.ready() {
				if c == from {
					continue
				}
				cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
				err := c.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, nil)
				cancel()
				if err == nil {
					n.runtimes.claim(c, id)
					return forward(c)
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, refusal("not_running", fmt.Sprintf("%s 把这个会话交给运行它的程序，%d 秒内没等到它接手，这次没有发送。",
				from.label(), int(runtimeHandoverWait.Seconds())), nil)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// rpcCode is the code of an rpc error, "" for anything else.
func rpcCode(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func okResult(err error) any {
	if err != nil {
		return nil
	}
	return map[string]any{"ok": true}
}

func isRPCError(err error) bool {
	var e *rpc.Error
	return errors.As(err, &e)
}
