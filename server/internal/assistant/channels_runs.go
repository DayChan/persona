package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The turns channels wait on (docs/extensions/channels.md "Following the turn"): one copy for every channel. A message from a channel
// starts a run; the assistant follows the conversation (the hub sends on its items and the text it writes, like an
// open chat in the app), shows progress where the channel can (a card it changes, "typing"), and when the turn
// ends sends its answer back through the channel's sink. The rules are channel.Run's; what reaches the chat app is
// the sink's.

// channelSink is how a channel's runs reach its chat app.
type channelSink interface {
	// answer sends a run's answer and note (into its progress message when there is one). final: the run is over
	// (its messages are marked done); quiet: there is nothing to say (a held run whose background work ended); end:
	// how a final one ended. An error: it did not go out and may when tried again (the same run, the same refs).
	answer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error
}

// channelProgress is a sink that shows a run's progress in a message it changes (the Feishu bot's card).
type channelProgress interface {
	progress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string)
	// folded: o went into a later run; its progress message says so.
	folded(ctx context.Context, o *channel.Run)
}

// channelRecall is a sink that marks a message the owner recalled: one taken out of a run that answers others too
// (channel.Run.Unfold) loses its own marks (the reaction on it, its folded progress message).
type channelRecall interface {
	recalled(ctx context.Context, m channel.Folded)
}

// channelTyping is a sink that shows "typing" while a run goes on.
type channelTyping interface {
	typing(ctx context.Context, on bool)
}

// chanLive is a run's live state, in memory only.
type chanLive struct {
	mu       chanMutex // held while its progress is being written
	steps    []string
	live     string
	dirty    bool
	updated  time.Time
	checked  time.Time // the answer was last read (while the turn was not seen to end, or the message not found)
	notFound bool      // the last read did not find the message in the conversation yet
	finished bool
	done     chan struct{}
	// checking is held by checkChannelRun: the watch loop and a turn's end may both check at once, and two checks
	// would send the same answer twice.
	checking chanMutex
	failing  time.Time // its answer first failed to go out (answerAgain)
	// unsettled: the turn was first read as over without words at its end (its answer may not be written yet)
	unsettled time.Time
	// items are the turn's thinking, words and tool calls (for the Feishu bot's agent message); cot what of them it
	// has shown, under mu.
	items []proto.Item
	cot   cotProgress
	// from is the seq of the run's own message once found (0: not yet: queued behind another turn, or not in);
	// upTo the last item noted; looked when locateRun last read the conversation.
	from, upTo int
	looked     time.Time
	// sentCard is the progress card last sent (its hash), not sent again; backoff and quietUntil hold updates back
	// after Feishu's rate limit. All under mu.
	sentCard   [32]byte
	backoff    time.Duration
	quietUntil time.Time
	// What the progress card shows besides the steps, noted as the items come (liveNote): the calls with no result
	// yet (oldest first), how many calls, the files read and changed, the subagents.
	open         []openCall
	calls        int
	read, edited map[string]bool
	subs         []subtask
	subsMore     int // subtasks dropped from subs (it keeps the latest)
}

// openCall is a tool call with no result yet.
type openCall struct {
	id, step string
	since    time.Time
}

// subtask is a Task / Agent call of the turn.
type subtask struct {
	id, desc, kind string
	done           bool
}

// Bounds of chanLive's notes: a turn of thousands of calls stays small.
const (
	liveOpenMost  = 50
	liveFilesMost = 1000
	liveSubsMost  = 50
)

// liveNote notes a new item for the progress card; now is when it was seen (for an item with no time).
func (p *chanLive) liveNote(l i18n.Lang, it proto.Item, now time.Time) {
	switch it.Kind {
	case proto.KindToolUse:
		p.calls++
		since := it.TS
		if since.IsZero() {
			since = now
		}
		if it.ToolUseID != "" && !slices.ContainsFunc(p.open, func(o openCall) bool { return o.id == it.ToolUseID }) {
			p.open = append(p.open, openCall{id: it.ToolUseID, step: botStep(l, it), since: since})
			if len(p.open) > liveOpenMost {
				p.open = p.open[len(p.open)-liveOpenMost:]
			}
		}
		var in struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
			Description  string `json:"description"`
			SubagentType string `json:"subagent_type"`
		}
		_ = json.Unmarshal(it.ToolInput, &in)
		file := in.FilePath
		if file == "" {
			file = in.NotebookPath
		}
		switch it.ToolName {
		case "Read":
			p.read = noteFile(p.read, file)
		case "Edit", "MultiEdit", "Write", "NotebookEdit":
			p.edited = noteFile(p.edited, file)
		case "Task", "Agent":
			p.subs = append(p.subs, subtask{id: it.ToolUseID, desc: clip(oneLine(in.Description), 60), kind: clip(oneLine(in.SubagentType), 30)})
			if len(p.subs) > liveSubsMost {
				p.subsMore += len(p.subs) - liveSubsMost
				p.subs = p.subs[len(p.subs)-liveSubsMost:]
			}
		}
	case proto.KindToolResult:
		p.open = slices.DeleteFunc(p.open, func(o openCall) bool { return o.id == it.ToolUseID })
		for i := range p.subs {
			if p.subs[i].id == it.ToolUseID {
				p.subs[i].done = true
			}
		}
	}
}

func noteFile(m map[string]bool, file string) map[string]bool {
	if file == "" || len(m) >= liveFilesMost {
		return m
	}
	if m == nil {
		m = map[string]bool{}
	}
	m[file] = true
	return m
}

// chanMutex is a mutex the watch loop can try without blocking.
type chanMutex chan struct{}

func newChanMutex() chanMutex { return make(chanMutex, 1) }
func (m chanMutex) Lock()     { m <- struct{}{} }
func (m chanMutex) Unlock()   { <-m }
func (m chanMutex) TryLock() bool {
	select {
	case m <- struct{}{}:
		return true
	default:
		return false
	}
}

// claimChannelMessage: the first time channel id's message is seen; an error while the messages seen can't be read.
func (s *Service) claimChannelMessage(id, msg string) (bool, error) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelStateLocked(context.Background())
	if !c.stLoaded {
		return false, errChannelsUnread
	}
	seen := c.st.Seen[id]
	if msg == "" || slices.Contains(seen, msg) {
		return false, nil
	}
	seen = append(seen, msg)
	if len(seen) > channelSeenKeep {
		seen = seen[len(seen)-channelSeenKeep:]
	}
	c.st.Seen[id] = seen
	s.saveChannelStateLocked()
	return true, nil
}

var errChannelsUnread = errors.New("the channels' state could not be read")

// sinkFor is how channel id's runs reach it: nil when it has gone (a plugin uninstalled), an error when that can't be
// told now (the hub could not be asked).
func (s *Service) sinkFor(ctx context.Context, id string) (channelSink, error) {
	if id == channel.Feishu {
		return feishuSink{s}, nil
	}
	_, ok, err := s.pluginChannelNow(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return extensionSink{s: s, id: id}, nil
}

// channelRuns are channel id's runs now (copies; "" is every channel's).
func (s *Service) channelRuns(id string) []channel.Run {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelStateLocked(context.Background())
	var out []channel.Run
	for _, r := range c.st.Runs {
		if id == "" || r.Channel == id {
			out = append(out, *r)
		}
	}
	return out
}

// startChannelRun records a run and watches it. An earlier run of the same channel on the same conversation that has
// not answered yet is folded into this one: one answer covers both messages.
func (s *Service) startChannelRun(r *channel.Run) {
	s.startChannelRunForBinding(r, nil)
}

// startChannelRunForBinding records the run only if the binding that sent its message is still current.
// A nil generation is for callers whose channel does not use the plugin binding lifecycle.
func (s *Service) startChannelRunForBinding(r *channel.Run, generation *uint64) {
	if r.MachineID == "" || r.SessionID == "" {
		s.logf("channels: %s: no conversation to follow", r.ID)
		return
	}
	c := &s.chans
	c.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	if generation != nil && c.generation[r.Channel] != *generation {
		c.mu.Unlock()
		return
	}
	var folded []*channel.Run
	keep := c.st.Runs[:0]
	for _, o := range c.st.Runs {
		// One answer covers both only when both go to the same place: a topic's reply never turns up in the private chat
		// (or another topic), nor the other way round.
		if o.Channel == r.Channel && o.MachineID == r.MachineID && o.SessionID == r.SessionID && !c.answering[o.ID] && !o.Recalled &&
			o.Ext["chat"] == r.Ext["chat"] && o.Ext["thread"] == r.Ext["thread"] {
			folded = append(folded, o)
			r.Fold(o)
			continue
		}
		keep = append(keep, o)
	}
	c.st.Runs = append(keep, r)
	gone := map[string]*chanLive{}
	for _, o := range folded {
		if p := c.live[o.ID]; p != nil {
			gone[o.ID] = p
			delete(c.live, o.ID)
		}
	}
	s.saveChannelStateLocked()
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sink, _ := s.sinkFor(ctx, r.Channel)
	for _, o := range folded {
		ended := true
		if p := gone[o.ID]; p != nil {
			p.mu.Lock()
			ended = !p.finished // finishChannelRun may have answered it just now: then its message holds the answer
			if ended {
				p.finished = true
				close(p.done)
			}
			p.mu.Unlock()
		}
		if fs, ok := sink.(channelProgress); ok && ended {
			fs.folded(ctx, o)
		}
	}
	s.noteChannelConversation(r.Channel, r.MachineID, r.SessionID)
	s.channelsChanged()
	if ts, ok := sink.(channelTyping); ok {
		ts.typing(ctx, true)
	}
	go s.watchChannelRun(r)
}

func (s *Service) channelRunByID(id string) (*channel.Run, *chanLive) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.st.Runs {
		if r.ID == id {
			return r, c.live[id]
		}
	}
	return nil, nil
}

// runExts are some of a run's channel values, read under the channels' lock.
func (s *Service) runExts(r *channel.Run, keys ...string) map[string]string {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = r.Ext[k]
	}
	return out
}

// setRunExt sets one of a run's channel values and keeps it.
func (s *Service) setRunExt(r *channel.Run, key, value string) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Ext == nil {
		r.Ext = map[string]string{}
	}
	r.Ext[key] = value
	s.saveChannelStateLocked()
}

// sweepChannelRuns makes sure every run has its watcher (one lost to a restart).
func (s *Service) sweepChannelRuns() {
	c := &s.chans
	c.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	var lost []*channel.Run
	for _, r := range c.st.Runs {
		if c.live[r.ID] == nil {
			lost = append(lost, r)
		}
	}
	c.mu.Unlock()
	if len(lost) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, r := range lost {
		if s.sinkReady(ctx, r.Channel) {
			go s.watchChannelRun(r)
		}
	}
}

// sinkReady: channel id can be answered now (the Feishu bot is connected and paired; a plugin's is there).
func (s *Service) sinkReady(ctx context.Context, id string) bool {
	if id == channel.Feishu {
		st, err := s.feishuStatusKept(ctx)
		return err == nil && st.Ready
	}
	_, ok := s.pluginChannel(ctx, id)
	return ok
}

// forgetChannelTurns drops what channel id waits on (unbound).
func (s *Service) forgetChannelTurns(id string) error {
	c := &s.chans
	c.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	if !c.stLoaded {
		c.mu.Unlock()
		return errChannelsUnread
	}
	if c.generation == nil {
		c.generation = map[string]uint64{}
	}
	c.generation[id]++
	delete(c.lastEnded, id)
	c.list = nil
	c.st.Runs = slices.DeleteFunc(c.st.Runs, func(r *channel.Run) bool { return r.Channel == id })
	c.st.Asks = slices.DeleteFunc(c.st.Asks, func(a *channel.Ask) bool { return a.Channel == id })
	err := s.saveChannelStateLocked()
	c.mu.Unlock()
	s.channelsChanged()
	return err
}

// channelWaiting is how many answers and questions channel id waits on.
func (s *Service) channelWaiting(id string) channel.Waiting {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelStateLocked(context.Background())
	var w channel.Waiting
	for _, r := range c.st.Runs {
		if r.Channel == id {
			w.Answers++
		}
	}
	for _, a := range c.st.Asks {
		if a.Channel == id {
			w.Asks++
		}
	}
	return w
}

// errCannotSend: the channel has nowhere to send to yet (WeChat before the owner's first message); errNoChannel: the
// channel is gone (a plugin uninstalled).
var (
	errCannotSend = errors.New("cannot send yet")
	errNoChannel  = errors.New("no channel")
)
