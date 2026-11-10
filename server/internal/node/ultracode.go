package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// Ultracode is independent of effort in Claude Code 2.1.284 and later. Only the
// slash command changes it: neither launch flags nor settings carry a choice.
// The record is an observation of one process, never a preference for a resume.
// It is kept in the session's record only for a session Persona started; of the
// user's own sessions it stays in memory, so they never read as Persona's (#5510).
type ultracodeRecord struct {
	Since  time.Time  `json:"since"`
	Value  *bool      `json:"value,omitempty"`
	At     time.Time  `json:"at,omitempty"`
	LostAt *time.Time `json:"lost_at,omitempty"`
	// LostDismissed also cancels a delayed history scan after dismissal or reopening.
	LostDismissed bool `json:"lost_dismissed,omitempty"`
}

type ultracodeState struct {
	persistMu sync.Mutex // serializes writes of this process observation
	ultracodeRecord
	pending       *bool
	note          string
	rejectedModel string
	applying      bool
	retryAt       time.Time
	// failures: attempts in a row that came to no verdict (a draft, a dialog, a screen that could not be read): each
	// waits longer before the next (backoff).
	failures       int
	awaitingHook   bool
	awaitingResume bool
	// hydrated: the stored observation is read in (ultracodeLocked), which waits for the session state to load.
	hydrated bool
	// dirty: observed before the state was loaded, written once it is (ultracodeStateLoaded).
	dirty bool
}

func ultraBool(v bool) *bool { return &v }

// wantsUltracode: a start asks for ultracode on. false asks for nothing a new process does not do anyway, and is
// taken as if it was left out: no agent or version check, no command.
func wantsUltracode(on *bool) bool { return on != nil && *on }

// Retries of a queued switch that could not be typed: 3 s, doubling up to 5 minutes.
const (
	ultracodeRetry    = 3 * time.Second
	ultracodeRetryMax = 5 * time.Minute
)

// backoff puts the next try of the queued switch off, longer after each in a row that came to nothing.
func (u *ultracodeState) backoff(now time.Time) {
	d := ultracodeRetry << min(u.failures, 7)
	u.retryAt = now.Add(min(d, ultracodeRetryMax))
	u.failures++
}

// Notes of a switch that waits or cannot be made. A fixed text: nothing typed at the computer is ever quoted (the
// note goes to every device), and one that does not change with each key keeps the list's key still.
const (
	ultracodeUnsupportedCode = "ultracode_unsupported"
	ultracodeDraftNote       = "电脑上的输入框里有没发出去的内容，清空后 ultracode 再切换"
	ultracodeDialogNote      = "电脑上的终端里开着一个对话框，关掉后 ultracode 再切换"
	ultracodeScreenWaitNote  = "暂时读不到终端的画面，稍后再切换 ultracode"
	ultracodeScreenlessNote  = "这个会话所在的终端不能从手机读屏或按键，没法切换 ultracode"
)

// ultracodeUnsupported refuses a switch the session or the machine cannot make, with nothing typed.
func ultracodeUnsupported(message string) error {
	return refusal(ultracodeUnsupportedCode, message, nil)
}

// ultracodeHostRefusal: the pane's host cannot read its screen back or type the command (botmux).
func (n *Node) ultracodeHostRefusal(pane string) error {
	if h, _ := n.hosts.Pane(pane); h != nil && !drivesScreen(h) {
		return ultracodeUnsupported(ultracodeScreenlessNote)
	}
	return nil
}

// ultracodeWaitNote says why a queued switch waits on the input box (checkPane's refusal), without its contents.
func ultracodeWaitNote(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		switch re.Code {
		case screen.InputDraft:
			return ultracodeDraftNote
		case screen.DialogOpen:
			return ultracodeDialogNote
		}
	}
	return ultracodeScreenWaitNote
}

var ultracodeVersion = lazyre.New(`^v?\d+\.\d+\.\d+(?:[-+].*)?$`)

func ultracodeVersionNote(version string) string {
	version = strings.TrimSpace(version)
	if !ultracodeVersion.MatchString(version) {
		return ""
	} // unknown: try the command
	if versionAtLeast(strings.TrimPrefix(strings.SplitN(version, "+", 2)[0], "v"), [3]int{2, 1, 284}) {
		return ""
	}
	return "请升级到 Claude Code 2.1.284 或更新版本，旧版不支持独立的 ultracode 开关"
}

func ultracodeCommand(on bool) string {
	if on {
		return "/effort ultracode on"
	}
	return "/effort ultracode off"
}

// ultracodeOutcome only reads the verdict after the command, never an old line
// above it. A nil value is unknown, including refusals, not an assumed off.
func ultracodeOutcome(shown string, on bool) (value *bool, note string, modelRejected bool) {
	flat := strings.Join(strings.Fields(screen.Plain(shown)), " ")
	i := strings.LastIndex(flat, ultracodeCommand(on))
	if i < 0 {
		return nil, "", false
	}
	flat = flat[i+len(ultracodeCommand(on)):]
	switch {
	case strings.Contains(flat, "Ultracode needs dynamic workflows enabled"):
		return nil, "需要先在 Claude Code 设置中开启 dynamic workflows（工作流）", false
	case strings.Contains(flat, "Ultracode isn't available on "):
		model := strings.SplitN(strings.SplitN(flat, "Ultracode isn't available on ", 2)[1], ". ", 2)[0]
		return nil, "模型 " + strings.TrimSpace(model) + " 不支持 ultracode，请切换模型后重试", true
	case strings.Contains(flat, "CLAUDE_CODE_EFFORT_LEVEL=") && strings.Contains(flat, "overrides"):
		if err := effortOutcome(flat, "ultracode"); err != nil {
			return nil, err.Error(), false
		}
	case strings.Contains(flat, "Invalid argument"), strings.Contains(flat, "Usage: /effort"), strings.Contains(flat, "Usage: /ultracode"):
		return nil, "Claude Code 未接受 ultracode 开关，请确认版本为 2.1.284 或更新版本", false
	}
	if strings.Contains(flat, "Ultracode on (this session only):") {
		return ultraBool(true), "", false
	}
	if strings.Contains(flat, "Ultracode off.") {
		return ultraBool(false), "", false
	}
	return nil, "", false
}

func (n *Node) ultracodeMeta(id string) quickMeta {
	var m quickMeta
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			m = n.meta(p, st)
		}
	}
	m.model = n.shownModel(id, m.model)
	return m
}

func (n *Node) bgUltracode(path string) (*bool, time.Time) {
	n.mu.Lock()
	sc := n.bgScans[path]
	n.mu.Unlock()
	if sc == nil {
		return nil, time.Time{}
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.pending {
		return nil, time.Time{}
	}
	return sc.meta.Ultracode, sc.meta.UltracodeAt
}

// ultracodeLocked is a session's ultracode state, with its stored process observation read in once the session state
// is loaded: read before, a session not loaded yet would have none, and an empty boundary would let an old process's
// evidence count for good. Caller holds n.mu.
func (n *Node) ultracodeLocked(id string) *ultracodeState {
	if n.modes == nil {
		n.modes = map[string]sessionModes{}
	}
	m := n.modes[id]
	if m.Ultracode == nil {
		m.Ultracode = &ultracodeState{}
		n.modes[id] = m
	}
	u := m.Ultracode
	if !u.hydrated && (n.state == nil || !n.state.isLoading()) {
		u.hydrated = true
		var rec ultracodeRecord
		stored := false
		if n.state != nil {
			rec, stored = n.state.ultracode(id)
		}
		// What this node saw before the state loaded stands when it has a process boundary of its own (a start or a
		// SessionStart since), and is written now; evidence without one gives way to the stored observation, which
		// knows the process it belongs to.
		if stored && (u.Since.IsZero() || rec.Since.After(u.Since)) {
			u.ultracodeRecord = rec
		} else if u.ultracodeRecord != (ultracodeRecord{}) {
			u.dirty = true
		}
	}
	return u
}

// ultracodeStateLoaded ends loading the session state (stateFile.loading): every session's stored observation is read
// in, and what was seen meanwhile of Persona's own sessions is written.
func (n *Node) ultracodeStateLoaded() {
	n.state.endLoad()
	var dirty []string
	n.mu.Lock()
	for id, m := range n.modes {
		if m.Ultracode != nil && n.ultracodeLocked(id).dirty {
			dirty = append(dirty, id)
		}
	}
	n.mu.Unlock()
	for _, id := range dirty {
		n.persistUltracode(id)
	}
}

// ultracodeStateReloaded: the session state came on a later try (reloadState) after the first one failed and ended the
// loading, so every session's ultracode state was read in against an empty state (no stored observation). It is read
// in again, by the same rule as at a first load (ultracodeLocked): a stored observation wins over what was seen
// meanwhile without a process boundary of its own, which otherwise the next write would put over it (its Since,
// LostAt and LostDismissed lost). What stands is written.
func (n *Node) ultracodeStateReloaded() {
	n.mu.Lock()
	for _, m := range n.modes {
		if m.Ultracode != nil {
			m.Ultracode.hydrated = false
		}
	}
	n.mu.Unlock()
	n.ultracodeStateLoaded()
}

func (u *ultracodeState) observe(m quickMeta) {
	if m.ultracode != nil && !m.ultracodeAt.Before(u.Since) && m.ultracodeAt.After(u.At) {
		u.Value, u.At = m.ultracode, m.ultracodeAt
		u.note, u.rejectedModel = "", ""
		if *u.Value {
			u.LostAt = nil
			u.LostDismissed = true
		}
	}
	if u.rejectedModel != "" && m.model != "" && m.model != u.rejectedModel {
		u.note, u.rejectedModel = "", ""
	}
}

// persistUltracode writes the session's process observation into its record, the ultracode_process field alone, and
// only for a session Persona started (one with a record): the user's own sessions get no record, which would make
// them read as Persona's (takesReport would drop their global hooks' reports). Until the state is loaded it waits.
func (n *Node) persistUltracode(id string) {
	if n.state == nil {
		return
	}
	n.mu.Lock()
	u := n.ultracodeLocked(id)
	n.mu.Unlock()
	u.persistMu.Lock()
	defer u.persistMu.Unlock()
	n.mu.Lock()
	if !u.hydrated {
		u.dirty = true
		n.mu.Unlock()
		return
	}
	record := u.ultracodeRecord
	u.dirty = false
	n.mu.Unlock()
	n.state.setUltracode(id, record)
}

// versionNote is the upgrade note for the version the transcript last recorded, if this process recorded it. A line
// from before the process boundary says nothing of the CLI running now: a newer one resuming an old session has not
// written yet. Unknown: try the command, its verdict decides. Caller holds n.mu.
func (u *ultracodeState) versionNote(m quickMeta) string {
	if m.versionAt.Before(u.Since) || m.versionAt.IsZero() && !u.Since.IsZero() {
		return ""
	}
	return ultracodeVersionNote(m.version)
}

// beginUltracodeProcess invalidates every older observation. The hook for a
// process started here completes its boundary without dropping its start choice.
func (n *Node) beginUltracodeProcess(id string, resume, own bool, now time.Time) {
	m := n.ultracodeMeta(id)
	n.mu.Lock()
	u := n.ultracodeLocked(id)
	u.observe(m)
	if !own && u.awaitingHook && resume == u.awaitingResume && time.Since(u.Since) < n.startWait() {
		u.awaitingHook = false
		// Keep the launch boundary: the hook may reach node after a slash verdict.
		n.mu.Unlock()
		return
	}
	lost := u.LostAt
	previousSince, previousAt := u.Since, u.At
	if u.Value != nil && *u.Value {
		t := now.UTC()
		lost = &t
	}
	u.ultracodeRecord = ultracodeRecord{Since: now.UTC(), LostAt: lost}
	if resume {
		u.Value, u.At = ultraBool(false), now.UTC()
	}
	u.pending, u.note, u.rejectedModel = nil, "", ""
	u.applying = false
	u.failures, u.retryAt = 0, time.Time{}
	u.awaitingHook, u.awaitingResume = own, resume
	n.mu.Unlock()
	n.persistUltracode(id)
	if resume && m.bgWait && lost == nil {
		n.recoverUltracodeLost(id, previousSince, previousAt, now.UTC())
	}
}

func (n *Node) ultracodeHook(req sockRequest, in hookInput) {
	if req.Resume || in.AgentID != "" || in.HookEventName != "SessionStart" ||
		(in.Source != "startup" && in.Source != "resume") || !validID(in.SessionID) {
		return
	}
	n.rememberTranscript(in)
	if n.isAssistantSession(in.SessionID) {
		return
	}
	n.beginUltracodeProcess(in.SessionID, in.Source == "resume", false, time.Now())
}

// ultracodeRow fills a Claude row's ultracode fields and runs a queued switch once its turn is over. Only a running
// row reads its transcript: an offline one shows what was last seen of its process, and no version of the past. pass
// is the list's (codexListPass; nil: each lookup made again).
func (n *Node) ultracodeRow(s *proto.Session, pass *codexListPass) {
	if s.Agent != "claude" || n.isAssistantSessionIn(s.SessionID, pass) {
		return
	}
	running := s.PaneID != "" && s.Status != proto.StatusOffline
	var m quickMeta
	if running {
		m = n.ultracodeMeta(s.SessionID)
	}
	if s.Model != "" {
		m.model = s.Model
	}
	n.mu.Lock()
	u := n.ultracodeLocked(s.SessionID)
	if !u.hydrated { // the state is still loading: which process the evidence belongs to is not known yet
		s.UltracodePending = u.pending != nil
		n.mu.Unlock()
		return
	}
	before := u.ultracodeRecord
	if running {
		u.observe(m)
	}
	changed := before != u.ultracodeRecord
	s.Ultracode, s.UltracodeNote, s.UltracodePending, s.UltracodeLostAt = u.Value, u.note, u.pending != nil, u.LostAt
	if running {
		if note := u.versionNote(m); note != "" {
			s.UltracodeNote = note
		}
	}
	run := running && u.pending != nil && !u.applying && !s.Starting &&
		(s.Status == proto.StatusIdle || s.Status == proto.StatusDone) && time.Now().After(u.retryAt)
	if run {
		u.applying = true
	}
	n.mu.Unlock()
	if changed {
		n.persistUltracode(s.SessionID)
	}
	if run {
		go n.retryUltracode(s.SessionID, s.PaneID)
	}
}

// retryUltracode is the list's try of a queued switch (ultracodeRow, which marked it applying). It takes its turn at
// the input as a message does (inputTurn), and decides again once it has it: a message that went first may have
// typed the switch already, or put the next try off (backoff), which the list's look from before does not know.
func (n *Node) retryUltracode(id, pane string) {
	// Pushed only when the list changed: a switch still waiting (backoff) is not news every few seconds.
	defer func() { n.publish(n.sessionList(context.Background(), false)) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	settled := func() {
		n.mu.Lock()
		n.ultracodeLocked(id).applying = false
		n.mu.Unlock()
	}
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		settled()
		return
	}
	defer unlock()
	n.mu.Lock()
	u := n.ultracodeLocked(id)
	due := u.pending != nil && !time.Now().Before(u.retryAt)
	n.mu.Unlock()
	if !due {
		settled()
		return
	}
	_ = n.applyPendingUltracode(ctx, id, pane)
}

func writeUltracodeKey(w *keyWriter, s *proto.Session) {
	if s.Ultracode == nil && s.UltracodeNote == "" && !s.UltracodePending && s.UltracodeLostAt == nil {
		return
	}
	w.b.WriteString("ultracode:")
	switch {
	case s.Ultracode == nil:
		w.str("unknown", '/')
	case *s.Ultracode:
		w.str("on", '/')
	default:
		w.str("off", '/')
	}
	w.quote(s.UltracodeNote, '/')
	if s.UltracodePending {
		w.b.WriteByte('p')
	}
	if s.UltracodeLostAt != nil {
		w.int(s.UltracodeLostAt.UnixNano(), '/')
	}
	w.b.WriteByte('\n')
}

// setUltracode queues a busy agent's request; a resting agent takes it now. A request that has to wait (a turn at
// work, a draft or a dialog in the way) is accepted and shows as ultracode_pending, with the reason in its note.
func (n *Node) setUltracode(ctx context.Context, id string, on *bool, dismiss bool) error {
	if n.isAssistantSession(id) {
		return ultracodeUnsupported("主对话不支持 ultracode，请在 Agent 会话中使用")
	}
	if on == nil && !dismiss {
		return fmt.Errorf("ultracode or dismiss_lost is required")
	}
	// After a node restart, not before the stored observation is read in: one taken before would give way to it
	// (ultracodeLocked), and a dismissal or a verdict answered as done would be undone by the record it was meant to
	// change. Not in line for the input yet, so the messages behind do not wait for the load.
	if err := n.state.loaded(ctx); err != nil {
		return fmt.Errorf("这台电脑上的 node 刚启动，会话状态还没载入，稍后再试: %w", err)
	}
	if on == nil {
		n.mu.Lock()
		u := n.ultracodeLocked(id)
		u.LostAt = nil
		u.LostDismissed = true
		n.mu.Unlock()
		n.persistUltracode(id)
		n.notifySessions()
		return nil
	}
	// In line with the messages: one sent before the switch goes in before it.
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return n.noPaneForModes(id)
	}
	if a, ok := n.agent(id); !ok || a.Agent != "claude" {
		return ultracodeUnsupported("只有 Claude Code Agent 支持 ultracode")
	}
	if r := n.ultracodeHostRefusal(pane); r != nil {
		return r
	}
	m := n.ultracodeMeta(id)
	n.mu.Lock()
	u := n.ultracodeLocked(id)
	u.observe(m)
	if note := u.versionNote(m); note != "" {
		n.mu.Unlock()
		return ultracodeUnsupported(note)
	}
	if u.rejectedModel != "" && *on {
		note := u.note
		n.mu.Unlock()
		return fmt.Errorf("%s", note)
	}
	if u.pending != nil && *u.pending != *on && u.Value != nil && *u.Value == *on && !u.At.Before(u.Since) {
		// Asked back to what this process already has while the other value still waits: the queued switch is
		// taken back, nothing is typed.
		u.pending, u.note = nil, ""
		u.failures, u.retryAt = 0, time.Time{}
		n.mu.Unlock()
		n.notifySessions()
		return nil
	}
	u.pending, u.note = ultraBool(*on), ""
	u.failures, u.retryAt = 0, time.Time{}
	if dismiss {
		u.LostDismissed = true
		u.LostAt = nil
	}
	n.mu.Unlock()
	if dismiss {
		n.persistUltracode(id)
	}
	_, err = n.applyUltracode(ctx, id, pane)
	n.notifySessions()
	return err
}

// ultracodeBusy: the agent is at work on a turn of its own, or waits on the user. Only the foreground turn counts (the
// list's status, which claudeStatus corrects): a dynamic workflow that runs on after the turn stopped leaves the input
// box to the user, so the switch goes in then too, and a message is never held back for it.
func (n *Node) ultracodeBusy(id string) bool {
	a, ok := n.agent(id)
	return ok && (a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked)
}

// applyPendingUltracode is applyUltracode for callers that only want how it ended.
func (n *Node) applyPendingUltracode(ctx context.Context, id, pane string) error {
	_, err := n.applyUltracode(ctx, id, pane)
	return err
}

// applyUltracode types the queued switch while the agent rests and reads its verdict back. It needs the input lock,
// or a start that has yet to let sends in. A switch that has to wait (the agent at work, a draft or a dialog in the
// box, a screen that cannot be read now) stays queued and is tried again later, each time later (backoff): no error.
// An error ends the request (pending cleared, the note says why). typed: a command went to the terminal, and may
// still sit in its input box when the error says it is not known to have gone in.
func (n *Node) applyUltracode(ctx context.Context, id, pane string) (typed bool, err error) {
	n.mu.Lock()
	u := n.ultracodeLocked(id)
	wanted := u.pending
	generation := u.Since
	u.applying = wanted != nil
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		if u.Since.Equal(generation) {
			u.applying = false
		}
		n.mu.Unlock()
	}()
	if wanted == nil {
		return false, nil
	}
	// wait keeps the request for a later try; end drops it with why.
	wait := func(note string) {
		n.mu.Lock()
		if u.Since.Equal(generation) {
			u.note = note
			u.backoff(time.Now())
		}
		n.mu.Unlock()
	}
	end := func(note string) {
		n.mu.Lock()
		if u.Since.Equal(generation) {
			u.pending, u.note = nil, note
			u.backoff(time.Now())
		}
		n.mu.Unlock()
	}
	if r := n.ultracodeHostRefusal(pane); r != nil {
		end(ultracodeScreenlessNote)
		return false, r
	}
	if n.ultracodeBusy(id) {
		n.mu.Lock()
		if u.Since.Equal(generation) {
			u.retryAt = time.Now().Add(ultracodeRetry) // waiting for a turn is no failure: no backoff
		}
		n.mu.Unlock()
		return false, nil
	}
	if err := n.checkPane(ctx, pane, "claude", ""); err != nil {
		wait(ultracodeWaitNote(err))
		return false, nil
	}
	m := n.ultracodeMeta(id)
	n.mu.Lock()
	note := u.versionNote(m)
	n.mu.Unlock()
	if note != "" {
		end(note)
		return false, fmt.Errorf("%s", note)
	}
	before, err := n.readPane(ctx, pane, "visible", 60)
	if err != nil {
		if errors.Is(err, host.ErrUnsupported) {
			end(ultracodeScreenlessNote)
			return false, ultracodeUnsupported(ultracodeScreenlessNote)
		}
		wait(ultracodeScreenWaitNote)
		return false, nil
	}
	n.mu.Lock()
	sameProcess := u.Since.Equal(generation)
	n.mu.Unlock()
	if !sameProcess {
		return false, fmt.Errorf("Claude Code 已恢复，请重新设置 ultracode")
	}
	cmd := ultracodeCommand(*wanted)
	started := time.Now().UTC()
	if err := n.prompt(ctx, pane, cmd); err != nil {
		end(err.Error())
		if r := deliveryRefusal(err); r != nil {
			return false, r
		}
		return true, uncertain(err)
	}
	n.typing.typed(pane, cmd)
	var value *bool
	var rejected bool
	for range 15 {
		select {
		case <-ctx.Done():
			note = "未能确认 ultracode 状态，请在终端里确认"
		case <-time.After(200 * time.Millisecond):
		}
		if ctx.Err() != nil {
			break
		}
		shown, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil || shown == before {
			continue
		}
		value, note, rejected = ultracodeOutcome(shown, *wanted)
		if value != nil || note != "" {
			break
		}
	}
	if value == nil && note == "" {
		note = "未能确认 ultracode 状态，请在终端里确认"
	}
	if value != nil && *value != *wanted {
		note = "ultracode 的实际状态与请求不一致，请在终端里确认"
	}
	n.mu.Lock()
	if !u.Since.Equal(generation) {
		n.mu.Unlock()
		return true, fmt.Errorf("Claude Code 已恢复，未采用旧进程的 ultracode 结果")
	}
	u.pending, u.note = nil, note
	u.failures, u.retryAt = 0, time.Time{}
	if value != nil {
		u.Value, u.At = value, started
		if *value {
			u.LostDismissed = true
			u.LostAt = nil
		}
	}
	if rejected {
		u.rejectedModel = m.model
		if u.rejectedModel == "" {
			u.rejectedModel = "unknown"
		}
	}
	n.mu.Unlock()
	n.persistUltracode(id)
	if note != "" {
		return true, fmt.Errorf("%s", note)
	}
	return true, nil
}

// checkUltracodeStart refuses a start that asks for ultracode where it cannot be had, before anything starts. Asking
// for it off is asking for nothing (wantsUltracode).
func (n *Node) checkUltracodeStart(ctx context.Context, on *bool, agent string) error {
	if !wantsUltracode(on) {
		return nil
	}
	if agent != "claude" {
		return ultracodeUnsupported("只有 Claude Code Agent 支持 ultracode")
	}
	if h := n.defaultHost(); h != nil && !drivesScreen(h) {
		return ultracodeUnsupported(ultracodeScreenlessNote)
	}
	if note := ultracodeVersionNote(n.claudeV.get(ctx)); note != "" {
		return ultracodeUnsupported(note)
	}
	return nil
}

// startUltracode turns ultracode on in a session just started, before its first prompt. It is the best it can do and
// never fails the start: Claude is up, and the first prompt goes in either way. A refusal or a verdict not read back
// is in ultracode_note; a switch that has to wait (a dialog the start came up with) stays queued. unsure: the command
// went to the terminal and is not known to have gone in, so it may still sit in the input box (ultracodeBoxClear).
func (n *Node) startUltracode(ctx context.Context, id, pane string, on *bool) (unsure bool) {
	if !wantsUltracode(on) {
		return false
	}
	n.mu.Lock()
	u := n.ultracodeLocked(id)
	u.pending, u.applying = ultraBool(true), true
	u.failures, u.retryAt = 0, time.Time{}
	n.mu.Unlock()
	typed, err := n.applyUltracode(ctx, id, pane)
	if err != nil {
		log.Printf("start %s: ultracode: %v", id, err)
	}
	return typed && err != nil
}

// ultracodeBoxClear checks Claude's input box before anything is typed after a switch whose command may still sit in
// it (applyUltracode's typed with an error): a draft or a dialog refuses as for any send, and so does a screen that
// cannot be read (readBox), which checkPane would take for an empty box.
func (n *Node) ultracodeBoxClear(ctx context.Context, pane string) error {
	return n.checkPaneFor(ctx, pane, "claude", sendOpts{readBox: true})
}
