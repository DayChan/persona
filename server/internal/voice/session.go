package voice

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// One voice conversation: the app's WebSocket on one side (docs/extensions/extensions.md "Voice"), a GPT-Live session on the other.
// The upstream session can drop mid-conversation: the app's socket stays open, shows "Reconnecting" and the
// conversation goes on in a new session told what was said so far.

// stableAfter is how long an upstream session holds before a drop of it no longer counts toward maxReconnects.
var stableAfter = time.Minute

const (
	maxReconnects = 3  // in a row: a session that held stableAfter starts the count over
	historyLines  = 40 // what a reconnected session is told was said
	recentLines   = 6  // what a handed-over task is sent with of the conversation
	startTimeout  = 25 * time.Second
	// What was sent with an event id and not acked this long is let go (a service that does not ack after all).
	unackedMost = 5 * time.Minute
	// A delegation the model makes while it also calls a tool for it (live dialect with tools) is only the tool's
	// envelope; one with no tool call this soon is work for the main conversation.
	toolCallWait = 2 * time.Second
	repliedMost  = 8 // the model's replies that were over a later word of them is checked against
)

type conversation struct {
	pl     *Plugin
	app    *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan outFrame // to the app, in order (one writer)
	// role is the caller's (X-Persona-Role: full, extension; "" from a hub that does not say, taken as full) and lang
	// the language the hub answers it in ("" when not said: the hub's own).
	role string
	lang i18n.Lang
	runs *tracker
	// version is the voice wire the app speaks: 1, or 2 when its start says so (docs/assistant/voice.md "Protocol changes"). target is
	// the conversation a protocol 2 call is bound to, fixed before ready; prep its binding, taken while the upstream
	// connects; resume the runs the app handed back (a call picked up again).
	version int
	target  *boundTarget
	prep    chan prepared
	resume  []resumeRun
	resumed bool // the app picked the call up again: its first session is told what was said
	wake    chan struct{}

	mu         sync.Mutex
	closed     bool
	p          *Provider
	w          wire
	model      string // the provider's model id
	modelID    string // <provider>/<model>, what the app knows
	voice      string
	up         upLink
	gen        int // which upstream session: a reconnect makes a new one
	ready      bool
	started    bool // the app got its ready
	outRate    int
	reconnects int
	// toolless: its sessions declare no function tools, as its provider's service refused them (Provider.Tools unset,
	// #1420). Set once, never cleared; read without mu (toolSet).
	toolless atomic.Bool

	userSeq, asstSeq int
	user, asst       *line
	history          []heardLine // in the order it was said (place)
	sawTranscript    bool        // the service sends the model's speech as transcript deltas (text deltas are then the same words)
	dropAudio        bool        // the app interrupted: drop the rest of the answer
	lastAudio        time.Time
	delegations      map[string]int         // open delegation id -> the session (gen) it belongs to
	pending          map[string]*time.Timer // delegations waiting for their tool call
	tasks            int
	gate             speechGate
	unacked          map[string]*sentWords // what deliver sent with an event id, until the service acks or refuses it
	events           int                   // event ids given
	snap             *prompts.VoiceTarget  // what the voice model is told of the bound conversation
	snapWords        string                // what of the snapshot the session took in came from agents (agentWords)
	open             []openTask            // the runs not over yet (a new session is told what it waits for)
	pace             pacing                // what the insertion gate goes by (gate.go)
	queue            []*sentWords          // what waits for the insertion gate
	userAt           time.Time             // the user's open caption last grew
	connectedAt      time.Time             // the upstream session came up
	// askCur is the card the tracker asks by voice (asks.go), askTold whether the model was told it: from then on what
	// it hands over is the card's answer. askGen is the upstream session (gen) it was last told it in.
	askCur  string
	askTold bool
	askGen  int
	// stopAt is when "Stop:" began to interrupt the bound conversation (zero: it did not, or that failed): a card
	// that goes from then on was closed by it (asks.go).
	stopAt time.Time

	barge barge // the user's voice over the model's speech (gate.go, protocol 2)
	// own is the user's own voice, clear of the model's speech, for the user-speech gate (gate.go).
	own ownVoice
	// cutAt: the energy rule cut the model off then (dropAudio); its reply to the user comes once they are quiet.
	cutAt time.Time
	// signals: the service says when the model's replies start, end and are cut off (the measured one's
	// response.event): a cut-in is its to tell (evInterrupted), not the energy rule's. replied are its last replies
	// that were over (completed) before such a word of theirs came.
	signals bool
	replied []string
	// reply is the model's reply under way (evReply's id) and replyVoiced whether audio of it came; silent are its last
	// replies that were over without any: replies that only called a tool, whose user caption the measured service
	// holds until that call's result (#5790).
	reply       string
	replyVoiced bool
	silent      []string
	// sus is the call's pausing of its upstream while its conversation works (suspend.go, protocol 2).
	sus suspension
	// log is what the call's log counts, and its id (calllog.go).
	log callLog
	// asked is the protocol the app's start asked for (the log: an extension's call gets 1 whatever it asks).
	asked int
	// tail are the model's lines after the user's last line in the app, oldest first (protocol 2): a user's caption
	// that comes after the model began to answer it goes before them (placeUser, #5673).
	tail []shownLine
	// turnsEnd: the service says when the model's turn is over (evTurnEnd, or evReply, whose replies end with one): a
	// line of the model's ends by that, not by a late caption of the user's (#5673).
	turnsEnd bool
	// owed is the answer the model owes the user, and modelAt when it last spoke, wrote a caption, handed something
	// over or called a function (nudge.go).
	owed    owedAnswer
	modelAt time.Time
}

type outFrame struct {
	text  []byte
	audio []byte
	close *websocket.StatusCode
	done  bool // the conversation is over: stop after what was queued before
}

func (pl *Plugin) converse(ctx context.Context, app *websocket.Conn, role string, lang i18n.Lang) {
	ctx, cancel := context.WithCancel(ctx)
	c := &conversation{pl: pl, app: app, ctx: ctx, cancel: cancel, out: make(chan outFrame, 2048), role: role, lang: lang,
		delegations: map[string]int{}, pending: map[string]*time.Timer{}, unacked: map[string]*sentWords{}, version: 1,
		wake: make(chan struct{}, 1)}
	c.log.id, c.log.opened = pl.calls.Add(1), time.Now()
	c.logf("conversation opened (role %q)", role)
	pl.active.Add(1)
	defer pl.active.Add(-1)
	writerDone, loopsDone := make(chan struct{}), make(chan struct{}, 2)
	go c.writeApp(writerDone)
	c.runs = newTracker(c, pl.link.listen())
	go func() { c.runs.loop(ctx); loopsDone <- struct{}{} }()
	go func() { c.speaker(); loopsDone <- struct{}{} }()
	defer func() {
		c.shutdown()
		// What is queued for the app (a fatal error, its close) goes out before the socket is dropped.
		select {
		case c.out <- outFrame{done: true}:
			select {
			case <-writerDone:
			case <-time.After(3 * time.Second):
			}
		case <-writerDone:
		case <-time.After(3 * time.Second):
		}
		cancel()
		<-writerDone
		_ = app.CloseNow()
		<-loopsDone // the conversation is over once its runs and what it says are (GET /health counts it till then)
		<-loopsDone
	}()
	if err := c.begin(); err != nil {
		c.logf("start failed after %v: %s", logDur(time.Since(c.log.opened)), failure(err))
		c.ending("start failed: " + firstNonEmpty(errCode(err), "no code"))
		var ce *codedError
		switch {
		case errors.Is(err, errNoUpstream):
			c.failCode(codeNoUpstream, "还没配实时语音服务：在 设置 › 扩展 › 实时语音 里填服务地址和 Key", true)
		case errors.As(err, &ce):
			c.failCode(ce.code, ce.msg, true)
		default:
			c.failCode(upstreamCode(err), "语音会话没能建立："+err.Error(), true)
		}
		return
	}
	c.readApp()
}

// writeApp is the only writer of the app's socket.
func (c *conversation) writeApp(done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-c.ctx.Done():
			return
		case f := <-c.out:
			var err error
			switch {
			case f.done:
				return
			case f.close != nil:
				_ = c.app.Close(*f.close, "voice session ended")
				return
			case f.audio != nil:
				err = c.app.Write(c.ctx, websocket.MessageBinary, f.audio)
			default:
				err = c.app.Write(c.ctx, websocket.MessageText, f.text)
			}
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

// sendApp queues a frame for the app (mu may be held).
func (c *conversation) sendApp(v any) {
	b, _ := json.Marshal(v)
	select {
	case c.out <- outFrame{text: b}:
	case <-c.ctx.Done():
	}
}

func (c *conversation) fail(msg string, fatal bool) { c.failCode("", msg, fatal) }

// failCode is fail with the error's code (docs/assistant/voice.md "Extension → app"), none when code is "".
func (c *conversation) failCode(code, msg string, fatal bool) {
	f := map[string]any{"type": "error", "message": msg, "fatal": fatal}
	if code != "" {
		f["code"] = code
	}
	c.sendApp(f)
	if fatal {
		c.logf("fatal error %s: the call ends", firstNonEmpty(code, "(no code)"))
		c.ending("fatal error " + firstNonEmpty(code, "(no code)"))
	} else {
		c.logf("told the app: error %s", firstNonEmpty(code, "(no code)"))
	}
	if fatal {
		code := websocket.StatusInternalError
		select {
		case c.out <- outFrame{close: &code}:
		case <-c.ctx.Done():
		}
	}
}

// begin reads the app's start and opens the upstream session.
func (c *conversation) begin() error {
	rctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	typ, b, err := c.app.Read(rctx)
	cancel()
	if err != nil {
		return err
	}
	var start startFrame
	if typ != websocket.MessageText || json.Unmarshal(b, &start) != nil || start.Type != "start" {
		return errors.New("the first frame must be start")
	}
	if err := c.negotiate(&start); err != nil {
		return err
	}
	p, model, err := c.pl.cat.resolve(c.ctx, start.Model)
	gone := errors.Is(err, errNoProvider)
	if gone {
		// The app's model is of an upstream no longer configured (its address cleared, hub.json changed): the default
		// one, rather than no conversation.
		p, model, err = c.pl.cat.resolve(c.ctx, "")
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.p, c.w, c.model, c.modelID = p, newWire(p.Dialect), model, p.ID+"/"+model
	c.gate.echoes = start.EchoCancellation != nil && !*start.EchoCancellation
	if voicePattern.MatchString(start.Voice) && !gone { // a voice of the other upstream's may not exist here
		c.voice = start.Voice
	}
	c.mu.Unlock()
	c.logStart(&start, p)
	if err := c.connect(false); err != nil {
		return err
	}
	if gone {
		c.logf("model %q is gone: %s instead", start.Model, c.modelID)
		c.failCode(codeModelUnavailable, "选的模型 "+start.Model+" 已经不在了，这次用默认模型 "+c.modelID, false)
	}
	return nil
}

// negotiate reads what the app's start asks for: the protocol, the conversation to bind (protocol 2: its binding starts
// now, while the upstream connects), what it hands back of a call it picks up again. Another extension calling has
// protocol 1 only (docs/assistant/voice.md "Permissions"): the main conversation, never a target of its choosing.
func (c *conversation) negotiate(s *startFrame) error {
	c.asked = s.Protocol
	full := c.role == "" || c.role == roleFull
	if s.Target != nil && !full {
		return &codedError{code: codeTargetForbidden, msg: "别的扩展调来的语音只能交给 Persona 主对话，不能绑定对话"}
	}
	if s.Protocol < 2 || !full {
		return nil // protocol 1: its frames, the main conversation
	}
	if s.Target == nil || s.Target.MachineID == "" || s.Target.SessionID == "" {
		return errors.New("protocol 2 starts with the conversation to bind (target)")
	}
	c.version = 2
	machine, session, recent := s.Target.MachineID, s.Target.SessionID, appRecent(c.language(), s.Recent)
	c.prep = make(chan prepared, 1)
	go func() { c.prep <- c.prepare(c.ctx, machine, session, recent) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range historyOf(s.History) {
		c.history = append(c.history, heardLine{VoiceLine: l})
	}
	if s.Resume != nil {
		for _, r := range s.Resume.Runs {
			if r.ID == "" || !openState(r.State) || len(c.resume) == resumeMost {
				continue
			}
			c.resume = append(c.resume, r)
			c.open = append(c.open, openTask{id: r.ID, task: clipRunes(r.Task, taskMost), state: r.State})
		}
	}
	c.resumed = len(c.history) > 0 || len(c.resume) > 0
	return nil
}

// bind starts reading the bound conversation for a new upstream session, and returns what waits for it: the binding
// negotiate started (the first session), or a new snapshot of it (a new session after a drop or a pause: the last one
// is told again when it cannot be read). It is read while the upstream's socket connects (about 0.4 s on the measured
// service, docs/lessons/voice.md "Instructions size"), not after. online: its machine is connected, as far as the hub
// said.
func (c *conversation) bind(ctx context.Context) func() (online bool, err error) {
	if c.version < 2 {
		return func() (bool, error) { return true, nil }
	}
	if c.prep != nil {
		return func() (bool, error) {
			var p prepared
			select {
			case p = <-c.prep:
			case <-ctx.Done():
				return false, ctx.Err()
			}
			c.prep = nil
			if p.err != nil {
				return false, p.err
			}
			c.mu.Lock()
			c.target, c.snap = p.target, p.snap
			c.mu.Unlock()
			online := "online"
			if !p.online {
				online = "offline"
			}
			c.logf("bound: %s %s/%s, its machine %s", p.target.Kind, p.target.MachineID, p.target.SessionID, online)
			return p.online, nil
		}
	}
	snaps := make(chan *prompts.VoiceTarget, 1)
	go func() { snaps <- c.refreshSnapshot(ctx, c.target) }()
	return func() (bool, error) {
		select {
		case snap := <-snaps:
			if snap != nil {
				c.mu.Lock()
				c.snap = snap
				c.mu.Unlock()
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
		return true, nil
	}
}

// openTask is a run not over yet, as a new upstream session is told (instructions' Waiting), and its run frame's state.
type openTask struct{ id, task, state string }

// waiting are the tasks of the runs not over yet, oldest first (mu held).
func (c *conversation) waiting() []string {
	var out []string
	for _, o := range c.open {
		out = append(out, o.task)
	}
	return out
}

// setOpen records run id's task as not over yet, in state (task "": over).
func (c *conversation) setOpen(id, task, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open = slices.DeleteFunc(c.open, func(o openTask) bool { return o.id == id })
	if task != "" {
		c.open = append(c.open, openTask{id: id, task: task, state: state})
	}
}

// roleFull is X-Persona-Role of a full device (and of the hub's own token).
const roleFull = "full"

// language is what the voice turn's prompts are written in: the caller's, else the hub's, else Chinese.
func (c *conversation) language() i18n.Lang {
	if c.lang != "" {
		return c.lang
	}
	if l := c.pl.link.language(); l != "" {
		return l
	}
	return i18n.ZhHans
}

// heard is when the call was last not quiet: the model's speech played out, the user's voice, a caption or a line of
// theirs (a heartbeat goes after a long quiet, docs/assistant/voice.md "Announcements").
func (c *conversation) heard() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return latest(c.pace.playEnd, c.pace.voice, c.gate.user, c.userAt)
}

func (c *conversation) shutdown() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	up, w := c.up, c.w
	lines := callLines{user: c.userSeq, model: c.asstSeq, pauses: c.sus.pauses}
	for id, t := range c.pending {
		t.Stop()
		delete(c.pending, id)
	}
	c.mu.Unlock()
	c.logf("conversation closed %s", c.summary(lines))
	if up != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = writeJSON(ctx, up, w.close())
		cancel()
		_ = up.Close(websocket.StatusNormalClosure, "bye")
	}
}

func writeJSON(ctx context.Context, c interface {
	Write(context.Context, websocket.MessageType, []byte) error
}, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
