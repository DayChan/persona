package serve

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// channelHub is the hub as the connector calls it, as a channel.
type channelHub struct{ s *server }

func (c channelHub) id() string {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.chid != "" {
		return c.s.chid
	}
	if p := c.s.cid.Load(); p != nil {
		return *p
	}
	return ""
}

func (c channelHub) Receive(ctx context.Context, m channel.Message) error {
	return c.s.peer.Call(ctx, channel.InPlugin(channel.MReceive), channel.ReceiveParams{Contribution: c.id(), Message: m}, nil)
}

func (c channelHub) Status(ctx context.Context, p channel.StatusParams) error {
	p.Contribution = c.id()
	return c.s.peer.Call(ctx, channel.InPlugin(channel.MStatus), p, nil)
}

// channelCall answers the hub's calls in the channel namespace (method without it) with the connector's Transport.
func (s *server) channelCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	s.mu.Lock()
	c, chid := s.c, s.chid
	s.mu.Unlock()
	if c == nil {
		return nil, &rpc.Error{Code: CodeNotInitialized, Message: "not initialized"}
	}
	if chid == "" {
		chid = c.ID()
	}
	if want := contributionOf(params); want != "" && want != chid {
		return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no channel " + want}
	}
	t, ok := c.(channel.Transport)
	if !ok {
		return nil, noCapability(c, "channel")
	}
	cannot := func(err error) error {
		if errors.Is(err, channel.ErrCannotSend) {
			return &rpc.Error{Code: channel.CodeCannotSend, Message: err.Error()}
		}
		return err
	}
	switch method {
	case channel.MSend:
		var p channel.SendParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Ref == "" {
			return nil, errors.New("channel.send without a ref")
		}
		res, err := s.sent.do(p.Ref, func() (channel.SendResult, error) { return t.Send(ctx, p) })
		return res, cannot(err)
	case channel.MTyping:
		var p channel.TypingParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if ty, ok := c.(channel.Typer); ok {
			return struct{}{}, ty.Typing(ctx, p.On)
		}
		return struct{}{}, nil
	case channel.MEdit:
		var p channel.EditParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ed, ok := c.(channel.Editor)
		if !ok {
			return nil, noCapability(c, "edit")
		}
		return struct{}{}, ed.Edit(ctx, p)
	}
	return nil, &rpc.Error{Code: CodeUnknownMethod, Message: "unknown method channel." + method}
}

// sentRefs runs each channel.send once by its ref (a failed one may go again): the latest doneMost.
// A send still running holds its ref, so the hub's retry of it (it timed out waiting) waits for that send
// instead of sending the message again.
type sentRefs struct {
	mu   sync.Mutex
	sent map[string]*sentRef
	// order is the refs sent successfully, oldest first; a running or failed send is not in it.
	order []string
}

type sentRef struct {
	done chan struct{}
	res  channel.SendResult
	err  error
}

func (r *sentRefs) do(ref string, send func() (channel.SendResult, error)) (channel.SendResult, error) {
	r.mu.Lock()
	if prev, ok := r.sent[ref]; ok {
		r.mu.Unlock()
		<-prev.done
		return prev.res, prev.err
	}
	if r.sent == nil {
		r.sent = map[string]*sentRef{}
	}
	cur := &sentRef{done: make(chan struct{})}
	r.sent[ref] = cur
	r.mu.Unlock()
	cur.res, cur.err = send()
	r.mu.Lock()
	if cur.err != nil {
		// Forget a failed send before waking its waiters, so the ref may be sent again.
		delete(r.sent, ref)
	} else {
		r.order = append(r.order, ref)
		if len(r.order) > doneMost {
			delete(r.sent, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.mu.Unlock()
	close(cur.done)
	return cur.res, cur.err
}
