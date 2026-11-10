package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (s *server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == extension.MInitialize {
		return s.initialize(params)
	}
	if rest, ok := strings.CutPrefix(method, channel.Namespace+"."); ok {
		return s.channelCall(ctx, rest, params)
	}
	if rest, ok := strings.CutPrefix(method, Namespace+"."); ok {
		s.mu.Lock()
		c := s.c
		s.mu.Unlock()
		if c != nil {
			if want := contributionOf(params); want != "" && want != c.ID() {
				return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no connector " + want}
			}
		}
		if rest == MInit {
			return nil, &rpc.Error{Code: CodeUnknownMethod, Message: "unknown method " + method}
		}
		method = rest
	} else if method == extension.MPing {
		s.mu.Lock()
		c := s.c
		s.mu.Unlock()
		if c == nil {
			return nil, &rpc.Error{Code: CodeNotInitialized, Message: "not initialized"}
		}
		return struct{}{}, nil
	}
	if method == MInit {
		var p InitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Protocol != Protocol {
			return nil, &rpc.Error{Code: CodeWrongProtocol, Message: fmt.Sprintf("protocol %d, not %d", p.Protocol, Protocol)}
		}
		return s.init(Init{Config: p.Config})
	}
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if c == nil {
		return nil, &rpc.Error{Code: CodeNotInitialized, Message: "not initialized"}
	}
	switch method {
	case MManifest:
		return ManifestOf(c), nil
	case MStatus:
		return connector.StatusOf(ctx, c), nil
	case MLoginStep:
		l, ok := connector.As[connector.Login](c)
		if !ok {
			return nil, noCapability(c, "login")
		}
		var p LoginParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return l.LoginStep(ctx, p.Step, p.Values)
	case MLogout:
		l, ok := connector.As[connector.Login](c)
		if !ok {
			return nil, noCapability(c, "login")
		}
		return struct{}{}, l.Logout(ctx)
	case MChanges:
		a, ok := connector.As[connector.Archive](c)
		if !ok {
			return nil, noCapability(c, "archive")
		}
		var p ChangesParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		b, next, more, err := a.Changes(ctx, p.Cursor, p.Limit)
		return ChangesResult{Batches: b, Next: next, More: more}, err
	case MHistoryChats, MHistory, MHistoryAnchors:
		return historyCall(ctx, c, method, params)
	case MCollect:
		in, ok := connector.As[connector.Inbox](c)
		if !ok {
			return nil, noCapability(c, "inbox")
		}
		things, err := in.Collect(ctx)
		return CollectResult{Things: things}, err
	case MReply:
		in, ok := connector.As[connector.Inbox](c)
		if !ok {
			return nil, noCapability(c, "inbox")
		}
		var p ReplyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, in.Reply(ctx, p)
	case MCatalog:
		sources, err := connector.ReadCatalog(ctx, c)
		return connector.CatalogResult{Sources: sources}, err
	case MCatalogCall:
		var p connector.CatalogQuery
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		text, err := connector.CallCatalog(ctx, c, p)
		return CallResult{Text: text}, err
	case MTools:
		t, ok := connector.As[connector.ReadTools](c)
		if !ok {
			return ToolsResult{}, nil
		}
		return ToolsResult{Tools: t.Tools()}, nil
	case MCall:
		t, ok := connector.As[connector.ReadTools](c)
		if !ok {
			return nil, noCapability(c, "tools")
		}
		var p CallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		text, err := t.Call(ctx, p.Name, p.Args)
		return CallResult{Text: text}, err
	case MActions:
		a, ok := connector.As[connector.Actions](c)
		if !ok {
			return ToolsResult{}, nil
		}
		return ToolsResult{Tools: a.Actions()}, nil
	case MDescribe:
		a, ok := connector.As[connector.Actions](c)
		if !ok {
			return nil, noCapability(c, "actions")
		}
		var p CallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		act, summary, err := a.Describe(ctx, p.Name, p.Args)
		return DescribeResult{Action: act, Summary: summary}, err
	case MThread:
		th, ok := connector.As[connector.Threads](c)
		if !ok {
			return nil, noCapability(c, "threads")
		}
		var p ThreadParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return th.Thread(ctx, p.Thread, p.Before, min(max(p.Limit, 1), connector.ThreadMost))
	case MGroupAppend:
		w, ok := connector.As[connector.EventWriter](c)
		if !ok {
			return nil, noCapability(c, "meta")
		}
		var p connector.GroupAppend
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return w.GroupAppend(ctx, p)
	case MGroupTool, MGroupFallback:
		g, ok := connector.As[connector.Grouper](c)
		if !ok {
			return nil, noCapability(c, "group")
		}
		return groupCall(ctx, g, method, params)
	case MAuthGet:
		a, ok := c.(Auther)
		if !ok {
			return nil, noCapability(c, "auth")
		}
		return a.Auth(ctx), nil
	case MAuthSet:
		a, ok := c.(Auther)
		if !ok {
			return nil, noCapability(c, "auth")
		}
		var p AuthSource
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return a.SetAuth(ctx, p)
	case MOptionsGet:
		o, ok := connector.As[connector.Options](c)
		if !ok {
			return nil, noCapability(c, "options")
		}
		return OptionsResult{Options: o.Options()}, nil
	case MOptionsSet:
		o, ok := connector.As[connector.Options](c)
		if !ok {
			return nil, noCapability(c, "options")
		}
		var p OptionsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, o.SetOptions(p.Options)
	case MSettle:
		st, ok := connector.As[connector.InboxSettler](c)
		if !ok {
			return nil, noCapability(c, "settle")
		}
		var p SettleParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return SettleResult{Settled: st.Settle(ctx, p.Threads)}, nil
	case MMachineOnline:
		m, ok := connector.As[connector.Machines](c)
		if !ok {
			return nil, noCapability(c, "machines")
		}
		var p MachineParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m.MachineOnline(p.Machine)
		return struct{}{}, nil
	case MSnapshot:
		sn, ok := connector.As[connector.Snapshot](c)
		if !ok {
			return nil, noCapability(c, "snapshot")
		}
		var p SnapshotParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return sn.Snapshot(ctx, p.TZ)
	case MItems:
		is, ok := connector.As[connector.Items](c)
		if !ok {
			return nil, noCapability(c, "items")
		}
		items, err := is.ItemSource().Items(ctx)
		if items == nil {
			items = []kbwire.Item{}
		}
		return ItemsResult{Items: items}, err
	case MDocTitle:
		is, ok := connector.As[connector.Items](c)
		if !ok {
			return nil, noCapability(c, "items")
		}
		var p DocTitleParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return DocTitleResult{Title: is.ItemSource().DocTitle(ctx, p.URL)}, nil
	case MArchiveMedia:
		am, ok := c.(connector.ArchiveMedia)
		if _, archive := connector.As[connector.Archive](c); !ok || !archive {
			return nil, noCapability(c, "archive")
		}
		var p MediaParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m, err := am.ArchiveMedia(ctx, p.Ref)
		if err == nil && len(m.Data) > connector.ArchiveMediaMost {
			return nil, fmt.Errorf("attachment %d bytes, more than %d", len(m.Data), connector.ArchiveMediaMost)
		}
		return m, err
	case MMedia:
		th, ok := connector.As[connector.Threads](c)
		if !ok {
			return nil, noCapability(c, "threads")
		}
		var p MediaParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m, err := th.Media(ctx, p.Ref)
		if err == nil && len(m.Data) > connector.MediaMost {
			return nil, fmt.Errorf("media %d bytes, more than %d", len(m.Data), connector.MediaMost)
		}
		return m, err
	case MDo:
		a, ok := connector.As[connector.Actions](c)
		if !ok {
			return nil, noCapability(c, "actions")
		}
		var p DoParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.RequestID == "" {
			return nil, errors.New("actions.do without a request_id")
		}
		return s.once.do(p.RequestID, func() (CallResult, error) {
			done, err := connector.Write(ctx, a, p.Name, p.Args)
			return CallResult{Text: done.Text, MessageRef: done.MessageRef}, err
		})
	case MRecallCan:
		r, ok := connector.RecallerOf(c)
		if !ok || !c.Manifest().Has(connector.CapRecall) {
			return nil, noCapability(c, "recall")
		}
		var p RecallCanParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Action == "" || p.AgeSeconds < 0 || p.AgeSeconds > int64((100*365*24*time.Hour)/time.Second) {
			return nil, errors.New("recall.can needs an action and an age from 0 to 100 years")
		}
		return RecallCanResult{Can: r.CanRecall(p.Action, time.Duration(p.AgeSeconds)*time.Second)}, nil
	case MRecallDo:
		r, ok := connector.RecallerOf(c)
		if !ok || !c.Manifest().Has(connector.CapRecall) {
			return nil, noCapability(c, "recall")
		}
		var p RecallDoParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.RequestID == "" || p.Action == "" || p.MessageRef == "" {
			return nil, errors.New("recall.do needs request_id, action and message_ref")
		}
		res := s.recalls.do(p.RequestID, func() RecallDoResult {
			return recallDoResult(r.Recall(ctx, sentRecallMessage(p.Action, p.MessageRef, p.Target, p.At)))
		})
		return res, nil
	case MRecall:
		r, ok := connector.RecallerOf(c)
		if !ok || !c.Manifest().Has(connector.CapRecall) {
			return nil, noCapability(c, "recall")
		}
		var p RecallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.MessageRef == "" {
			return nil, errors.New("recall without a message_ref")
		}
		if err := r.Recall(ctx, sentRecallMessage(p.Action, p.MessageRef, p.Target, p.At)); err != nil {
			return nil, recallError(err)
		}
		return RecallResult{Recalled: true}, nil
	}
	return nil, &rpc.Error{Code: CodeUnknownMethod, Message: "unknown method " + method}
}

// historyCall answers history.chats, archive.history and history.anchors for a connector declaring them. The query
// is checked and its limit cut to connectorwire.HistoryMost before the connector sees it; what the connector answers,
// error included, goes back as it is.
func historyCall(ctx context.Context, c connector.Connector, method string, params json.RawMessage) (any, error) {
	h, ok := connector.As[connector.History](c)
	if !ok {
		return nil, noCapability(c, "history")
	}
	switch method {
	case MHistoryChats:
		chats, err := h.HistoryChats(ctx)
		if chats == nil {
			chats = []connectorwire.HistoryChat{}
		}
		return HistoryChatsResult{Chats: chats}, err
	case MHistory:
		var q HistoryParams
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, err
		}
		if err := q.Check(); err != nil {
			return nil, err
		}
		return h.History(ctx, q.Clamped())
	}
	a, ok := connector.As[connector.HistoryAnchors](c)
	if !ok {
		return nil, noCapability(c, "history_anchors")
	}
	var q HistoryAnchorsParams
	if err := json.Unmarshal(params, &q); err != nil {
		return nil, err
	}
	if err := q.Check(); err != nil {
		return nil, err
	}
	return a.HistoryAnchors(ctx, q.Clamped())
}

// contributionOf is the contribution a namespaced call names ("" when it names none).
func contributionOf(params json.RawMessage) string {
	var t extension.Target
	_ = json.Unmarshal(params, &t)
	return t.Contribution
}

// groupCall answers group.tool and group.fallback.
func groupCall(ctx context.Context, g connector.Grouper, method string, params json.RawMessage) (any, error) {
	switch method {
	case MGroupTool:
		var p connector.GroupToolCall
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return g.GroupTool(ctx, p)
	default:
		var p connector.GroupFallback
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return g.GroupFallback(ctx, p)
	}
}

// doneMost is how many writes the program remembers by request id.
const doneMost = 256

// writes runs each write (actions.do) once by its request id: the same id again answers what the first run did,
// waiting for it if it is still running.
type writes struct {
	mu   sync.Mutex
	runs map[string]*write
	// order is the finished writes (failed ones too), oldest first; a running write is not in it.
	order []string
}

type write struct {
	done chan struct{}
	res  CallResult
	err  error
}

func (w *writes) do(id string, run func() (CallResult, error)) (CallResult, error) {
	w.mu.Lock()
	if prev, ok := w.runs[id]; ok {
		w.mu.Unlock()
		<-prev.done
		return prev.res, prev.err
	}
	if w.runs == nil {
		w.runs = map[string]*write{}
	}
	cur := &write{done: make(chan struct{})}
	w.runs[id] = cur
	w.mu.Unlock()
	cur.res, cur.err = run()
	// Count the write toward doneMost only once it finished, so a write still running is never forgotten.
	w.mu.Lock()
	w.order = append(w.order, id)
	if len(w.order) > doneMost {
		delete(w.runs, w.order[0])
		w.order = w.order[1:]
	}
	w.mu.Unlock()
	close(cur.done)
	return cur.res, cur.err
}

func noCapability(c connector.Connector, what string) error {
	return &rpc.Error{Code: CodeNoCapability, Message: fmt.Sprintf("%s has no %s", c.Name(), what)}
}
