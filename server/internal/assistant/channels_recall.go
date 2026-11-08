package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// recalledResult is the end of a run whose message the owner recalled: no answer, its progress message closed.
func recalledResult() *channel.Result { return &channel.Result{Quiet: true, End: channel.EndRecalled} }

// recallChannelRun takes a message the owner recalled (ref, the chat app's id; msgID its client_msg_id) out of the
// run answering it (#5248). A run that answers only it ends as EndRecalled with no answer, and once the message is in
// the conversation its turn is interrupted, as /stop does (one still queued behind another turn is not: that would
// stop the other). A run that answers others too goes on without it. A run whose answer is going out, or that was
// answered already (held), is left alone; a message not taken yet is held back by the caller.
func (s *Service) recallChannelRun(ch, ref, msgID string) {
	if ref == "" {
		return
	}
	c := &s.chans
	c.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	var r *channel.Run
	for _, o := range c.st.Runs {
		if o.Channel == ch && (o.ReplyTo == ref || slices.ContainsFunc(o.Folded, func(f channel.Folded) bool { return f.ReplyTo == ref })) {
			r = o
			break
		}
	}
	if r == nil || r.Held || r.Recalled || c.answering[r.ID] {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if gone, ok := r.Unfold(ref, msgID); ok {
		s.saveChannelStateLocked()
		c.mu.Unlock()
		s.logf("channels: %s: a message recalled; the others are still answered", r.ID)
		if sink, _ := s.sinkFor(ctx, ch); sink != nil {
			if rs, ok := sink.(channelRecall); ok {
				rs.recalled(ctx, gone)
			}
		}
		s.channelsChanged()
		return
	}
	r.Recalled = true
	s.saveChannelStateLocked()
	prog := c.live[r.ID]
	in := prog != nil && prog.from != 0
	c.mu.Unlock()
	if !in {
		in = s.runMessageIn(ctx, r)
	}
	s.logf("channels: %s: its message recalled; the turn is stopped (in the conversation: %v)", r.ID, in)
	s.finishChannelRun(r, recalledResult()) // not watched yet: its first check ends it
	if !in {
		return
	}
	if err := s.call(ctx, r.MachineID, "session.interrupt", map[string]any{"session_id": r.SessionID}, nil); err != nil {
		s.logf("channels: %s: interrupt after the recall: %v", r.ID, err)
	}
}

// runMessageIn: the run's message is in its conversation now (false when that can't be read).
func (s *Service) runMessageIn(ctx context.Context, r *channel.Run) bool {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if s.call(ctx, r.MachineID, DeviceSessionItems, map[string]any{"session_id": r.SessionID, "after_seq": r.Base, "lazy_images": true}, &res) != nil {
		return false
	}
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Head || len(r.MsgIDs) == 0 && r.Probe == "" {
		return len(res.Items) > 0 // a new conversation's first message: in once it has anything
	}
	_, ok := r.Find(res.Items, nil)
	return ok
}
