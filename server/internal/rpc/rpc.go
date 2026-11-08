// Package rpc is a tiny bidirectional JSON-lines RPC used between hub and node over SSH stdio.
//
// Each line is one of:
//
//	{"id":"h1","method":"m","params":{}}      request
//	{"id":"h1","result":{}} / {"id":"h1","error":{"message":"…"}}   response
//	{"event":"e","params":{}}                 one-way event
//	{"event":"rpc.cancel","params":{"id":"h1"}}   the caller no longer waits for request h1 (Peer.Cancels)
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type EventHandler func(event string, params json.RawMessage)

type message struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Event  string          `json:"event,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Message string `json:"message"`
	// Code and Data are set by handlers that return an *Error themselves, for callers that act on the kind of
	// failure (a refused send) rather than show the message.
	Code string          `json:"code,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// WireError is its code and data, for the extension SDK (sdk/go/ext) to send as they are.
func (e *Error) WireError() (string, json.RawMessage) { return e.Code, e.Data }

var ErrClosed = errors.New("rpc: connection closed")

var errFrameBudget = errors.New("rpc: runtime frame budget exceeded")

// ErrDropped is what Call answers when the connection closed after the request went out: the other side may have done
// it. It is ErrClosed too (errors.Is), with the same words (a relay further on passes them as they are).
var ErrDropped error = droppedError{}

type droppedError struct{}

func (droppedError) Error() string        { return ErrClosed.Error() }
func (droppedError) Is(target error) bool { return target == ErrClosed }

// Limits are optional transport budgets for untrusted runtime peers. Zero preserves
// existing process/node behavior; configure them before Serve or any concurrent call.
type Limits struct{ FrameBytes, ConcurrentRequests, PendingCalls int }

type Peer struct {
	Limits Limits
	// Interrupt is optional, explicit ownership of a transport interrupt. It must
	// promptly unblock active Write and return, without waiting for writer admission.
	// Configure it before concurrent use. Close invokes it once, outside Peer locks.
	// An arbitrary io.Closer does not satisfy this contract. Without it, legacy
	// active writes cannot be bounded: queued calls still return on their ctx, and
	// a canceler returns at once unless the withdrawal queue is full (then it waits
	// for room or Close).
	Interrupt func()
	// Progress goes with Interrupt: an opaque token that changes on observed
	// transport progress (ConsumedProgress). An owned Write is stalled only when
	// it stays active for StallWindow without this token changing; only then is
	// the transport interrupted, for a waiting withdrawal or a caller that
	// canceled during the Write. Counting only what Write returns is not enough:
	// a Linux socket wakes a blocked writer only after its peer drains a large
	// part of the send buffer. Without Progress, stalls are not observable and
	// such a Write is never interrupted for them. Tokens must be measured during
	// each synchronous invocation; slow/expired sampling is unavailable, not a
	// stall. Callbacks are not preempted. Configure before concurrent use.
	Progress func() int64
	prefix   string
	w        io.Writer
	// Opted-in withdrawals waiting for the writer, under mu: one worker drains
	// them in order, ahead of later ordinary frames (cancelBestEffort).
	controlQueue [][]byte
	controlDone  chan struct{} // non-nil while withdrawals are queued or being written
	controlSpace chan struct{} // closed when the full queue shrinks
	// The Write in progress on an owned transport, under mu (observeStall): when
	// it began and the transport's progress count then.
	activeSeq   uint64
	activeOn    bool
	activeSince time.Time
	activeBase  int64
	// wsem is a one-slot semaphore around w, not a mutex: a write can block for good (SSH stdin to a frozen node,
	// a stalled relay WebSocket), and whoever waits behind it must still return on its ctx or on Close.
	wsem    chan struct{}
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[string]chan message
	closed  chan struct{}
	once    sync.Once
	err     error

	OnRequest Handler
	OnEvent   EventHandler
	// Cancels: the methods whose calls tell the other side when their ctx ends first (cancelEvent), which cancels
	// that request's ctx. None by default: extensions and connectors speak this protocol too and need not know the
	// event, and most calls are meant to finish on the other side anyway. A peer that does not know the event ignores
	// it, as before: its handler runs on.
	Cancels map[string]bool

	running map[string]context.CancelFunc // requests being handled, by id: what a cancelEvent ends
	// Only bounded peers couple accepted work to closure; legacy peers retain their cancellation semantics.
	runtimeCancel context.CancelFunc
}

// cancelEvent is sent for a request the caller no longer waits for: an approval the hub asks for on the phone stays
// pending otherwise, and allowing it later runs a tool call nobody is waiting on (a stale message sent, #2597).
const cancelEvent = "rpc.cancel"

// connKey carries the connection's context in a handler's ctx (ConnContext).
type connKey struct{}

// ConnContext is the context of the connection a handler's request came in on: it ends with the connection, not with
// the request. A handler's own ctx ends when the handler returns (and when the caller cancels it), so anything meant
// to outlive the request (a watch that keeps pushing after its reply) must hang off this one (#3139: a transcript
// watch that took the request's ctx ended with the reply, and no session's items were pushed any more). A ctx that
// did not come from Serve is its own connection.
func ConnContext(ctx context.Context) context.Context {
	if c, ok := ctx.Value(connKey{}).(context.Context); ok {
		return c
	}
	return ctx
}

// New creates a peer. prefix distinguishes request ids by side ("h" for hub, "n" for node).
func New(prefix string, w io.Writer) *Peer {
	return &Peer{prefix: prefix, w: w, pending: map[string]chan message{}, running: map[string]context.CancelFunc{}, closed: make(chan struct{}), wsem: make(chan struct{}, 1)}
}

// Serve reads messages until r fails; it returns the read error.
func (p *Peer) Serve(ctx context.Context, r io.Reader) error {
	// Actual transport EOF ends the connection's accepted work, including pending
	// approvals. Logical Close alone still does not interrupt a legacy reader.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if p.Limits.ConcurrentRequests > 0 {
		p.mu.Lock()
		p.runtimeCancel = cancel
		select {
		case <-p.closed:
			cancel()
		default:
		}
		p.mu.Unlock()
	}
	sc := bufio.NewScanner(r)
	maximum := 64 * 1024 * 1024
	if p.Limits.FrameBytes > 0 {
		maximum = p.Limits.FrameBytes
	}
	sc.Buffer(make([]byte, min(64*1024, maximum)), maximum)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch {
		case m.Method != "":
			// Registered before the handler starts: a cancel right behind the request finds it.
			hctx, cancel := context.WithCancel(context.WithValue(ctx, connKey{}, ctx))
			p.mu.Lock()
			if p.Limits.ConcurrentRequests > 0 && (len(p.running) >= p.Limits.ConcurrentRequests || p.running[m.ID] != nil || m.ID == "") {
				p.mu.Unlock()
				cancel()
				err := errors.New("rpc: runtime request budget exceeded")
				p.Close(err)
				return err
			}
			p.running[m.ID] = cancel
			p.mu.Unlock()
			go p.handle(hctx, m)
		case m.Event == cancelEvent:
			var c struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(m.Params, &c)
			p.mu.Lock()
			cancel := p.running[c.ID]
			p.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		case m.Event != "":
			if p.OnEvent != nil {
				p.OnEvent(m.Event, m.Params)
			}
		case m.ID != "":
			p.mu.Lock()
			ch := p.pending[m.ID]
			delete(p.pending, m.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	p.Close(err)
	return err
}

func (p *Peer) handle(ctx context.Context, m message) {
	defer func() {
		p.mu.Lock()
		cancel := p.running[m.ID]
		delete(p.running, m.ID)
		p.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	var res any
	var err error
	if p.OnRequest == nil {
		err = fmt.Errorf("no handler for %s", m.Method)
	} else {
		res, err = p.OnRequest(ctx, m.Method, m.Params)
	}
	out := message{ID: m.ID}
	var coded *Error
	if errors.As(err, &coded) {
		out.Error = coded
	} else if err != nil {
		out.Error = &Error{Message: err.Error()}
	} else {
		b, merr := json.Marshal(res)
		if merr != nil {
			out.Error = &Error{Message: merr.Error()}
		} else {
			out.Result = b
		}
	}
	if err := p.write(out); errors.Is(err, errFrameBudget) {
		// The request must get an error, not a permanently unresolved promise.
		// If even the error cannot fit, disconnect so all pending calls fail.
		if err := p.write(message{ID: m.ID, Error: &Error{Message: errFrameBudget.Error()}}); err != nil {
			p.Close(err)
		}
	}
}

func (p *Peer) write(m message) error {
	return p.writeContext(context.Background(), m)
}

func (p *Peer) writeContext(ctx context.Context, m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if p.Limits.FrameBytes > 0 && len(b) > p.Limits.FrameBytes {
		return errFrameBudget
	}
	// A retained withdrawal has priority over subsequent ordinary frames. Check
	// again after admission: a withdrawal can arrive behind the previous frame
	// while this writer was already waiting. The control worker bypasses this gate.
	for {
		p.mu.Lock()
		control := p.controlDone
		p.mu.Unlock()
		if control != nil {
			select {
			case <-control:
			case <-ctx.Done():
				return ctx.Err()
			case <-p.closed:
				if err := ctx.Err(); err != nil {
					return err
				}
				return ErrClosed
			}
		}
		select {
		case p.wsem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closed:
			if err := ctx.Err(); err != nil {
				return err
			}
			return ErrClosed
		}
		p.mu.Lock()
		control = p.controlDone
		p.mu.Unlock()
		if control != nil {
			<-p.wsem
			continue
		}
		defer func() { <-p.wsem }()
		return p.writeAdmitted(ctx, b)
	}
}

// writeAdmitted owns wsem until Write and its cancellation callback both finish.
// A late callback must never interrupt the next frame on this stream.
func (p *Peer) writeAdmitted(ctx context.Context, b []byte) error {
	// Admission may race closure or cancellation; an unsent request must remain
	// unsent, and its context identity wins over a resulting transport closure.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-p.closed:
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrClosed
	default:
	}
	track := p.Interrupt != nil
	if track {
		var base int64
		if p.Progress != nil {
			base = p.Progress()
		}
		p.mu.Lock()
		p.activeSeq++
		p.activeOn, p.activeSince, p.activeBase = true, time.Now(), base
		p.mu.Unlock()
	}
	// A caller canceling during the Write does not close a healthy stream: the
	// frame may already have reached the remote side. Its Write is watched and
	// interrupted only if it stops making progress (watchWrite).
	var stop func() bool
	var watched, written chan struct{}
	if track && p.Progress != nil && ctx.Done() != nil {
		watched, written = make(chan struct{}), make(chan struct{})
		stop = context.AfterFunc(ctx, func() {
			defer close(watched)
			p.watchWrite(written)
		})
	}
	n, err := p.w.Write(b)
	if track {
		p.mu.Lock()
		p.activeOn = false
		p.mu.Unlock()
	}
	if stop != nil {
		close(written)
		if !stop() {
			<-watched
		}
	}
	if n != len(b) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		// Even full length plus error is uncertain. Never retry or append another
		// message to a prefix left by a failed write, including a nil-error short.
		p.Close(err)
		if n > 0 {
			err = errors.Join(err, ErrDropped)
		}
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		return err
	}
	// Completion concurrent with cancellation does not establish non-execution.
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), ErrDropped)
	}
	return nil
}

const (
	cancelFrameMost = 1024 // independent of zero-value legacy frame budgets
	// cancelQueueMost caps queued withdrawals. A canceler finding it full waits
	// for room (or closure) instead of dropping one; a drained queue never fills.
	cancelQueueMost = 4096
	// Progress is checked every controlStallPoll while a Write is watched;
	// ProgressWriter reports it in pieces of at most progressPiece bytes.
	controlStallPoll = 250 * time.Millisecond
	progressPiece    = 4096
)

// StallWindow: an owned Write has stalled once it stays active this long without
// observable transport progress. ConsumedProgress observes unread bytes in pipes
// and release of complete send buffers on unix sockets (4 KiB at most when using
// WriteCounted). Reading at least one such buffer per window avoids retirement;
// partial reads that do not release a buffer are invisible. A longer pause, even
// while the reader is busy, is indistinguishable from a stall.
const StallWindow = 5 * time.Second

// progressUnavailable is ConsumedProgress's fail-safe after a failed descriptor
// query: unavailable measurement must not retire an otherwise healthy stream.
const progressUnavailable int64 = -1 << 63

// ErrStalled is why an owned transport was retired: its active Write had no
// observable progress for the stall window while a withdrawal or a canceled
// caller waited on it.
var ErrStalled = errors.New("rpc: transport write made no progress")

// stallClock follows the Write in progress across observations.
type stallClock struct {
	on       bool
	seq      uint64
	progress int64
	since    time.Time
}

// progressObservation bounds when the owner could have measured its token.
// A callback is synchronous and must measure during its invocation; it is not
// preempted or retried. Slow sampling/lock admission suppresses retirement.
type progressObservation struct {
	value             int64
	started, finished time.Time
}

func (p *Peer) sampleProgress() progressObservation {
	s := progressObservation{started: time.Now()}
	s.value = p.Progress()
	s.finished = time.Now()
	return s
}

func (s progressObservation) freshAt(now time.Time) bool {
	return s.value != progressUnavailable && now.Sub(s.started) <= controlStallPoll
}

func (c *stallClock) unavailable(seq uint64, now time.Time) {
	*c = stallClock{on: true, seq: seq, progress: progressUnavailable, since: now}
}

// observeStall reports a candidate only from a fresh observation. The unchanged
// interval ends at invocation start, not return: callback latency cannot prove
// zero progress. Changed tokens start a new window at return, never backdated to
// invocation start. Every candidate is sampled again before atomic retirement.
func (p *Peer) observeStall(c *stallClock) bool {
	p.mu.Lock()
	on, seq, since, base := p.activeOn, p.activeSeq, p.activeSince, p.activeBase
	p.mu.Unlock()
	if !on {
		c.on = false
		return false
	}
	sample := p.sampleProgress()
	// Progress may call into the owner or block. Its sampled frame must still
	// be active when it returns; a control watcher can span several frames.
	p.mu.Lock()
	current, now := p.activeOn && p.activeSeq == seq, time.Now()
	p.mu.Unlock()
	if !current {
		c.on = false
		return false
	}
	if !sample.freshAt(now) {
		c.unavailable(seq, now)
		return false
	}
	progress := sample.value
	if !c.on || c.seq != seq || c.progress != progress {
		if (c.on && c.seq == seq) || progress != base {
			since = sample.finished
		}
		*c = stallClock{on: true, seq: seq, progress: progress, since: since}
	}
	return sample.started.Sub(c.since) >= StallWindow
}

// watchWrite interrupts the owned transport if the Write in progress stalls,
// until done closes. Without Progress nothing is observable and it only waits.
func (p *Peer) watchWrite(done <-chan struct{}) {
	if p.Interrupt == nil || p.Progress == nil {
		return
	}
	var clock stallClock
	p.observeStall(&clock)
	tick := time.NewTicker(controlStallPoll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-p.closed:
			return
		case <-tick.C:
			if p.observeStall(&clock) && p.retireStalled(&clock) {
				return
			}
		}
	}
}

// WriteCounted writes b to w in pieces of at most 4 KiB, adding each piece's
// accepted bytes to accepted as it goes: a transport owner's progress signal
// inside one large frame (Peer.Progress). A short or failed piece ends the Write
// with the total accepted.
func WriteCounted(w io.Writer, b []byte, accepted *atomic.Int64) (int, error) {
	total := 0
	for len(b) > 0 {
		piece := b[:min(len(b), progressPiece)]
		n, err := w.Write(piece)
		if n < 0 || n > len(piece) {
			// An invalid count says nothing about what went out: never unsent.
			return total, errors.Join(io.ErrShortWrite, ErrDropped, fmt.Errorf("rpc: invalid write count %d", n))
		}
		total += n
		accepted.Add(int64(n))
		if err != nil {
			return total, err
		}
		if n < len(piece) {
			return total, io.ErrShortWrite
		}
		b = b[len(piece):]
	}
	return total, nil
}

// ConsumedProgress combines an owner's accepted-byte counter and outstanding-data
// query into Peer.Progress. It returns an opaque token, not a consumed-byte count:
// a unix socket's queue memory cannot be subtracted from accepted bytes. The
// token changes when accepted bytes increase or outstanding data decreases; the
// latter observes reading while Write is blocked. The transport owner supplies
// the descriptor query, or nil where consumption cannot be measured. Query
// failures suppress stall retirement until measurement recovers.
func ConsumedProgress(accepted func() int64, unread func() (int64, bool)) func() int64 {
	if unread == nil {
		return nil
	}
	var mu sync.Mutex
	var initialized bool
	var lastAccepted, lastUnread, token int64
	return func() int64 {
		mu.Lock()
		defer mu.Unlock()
		n, ok := unread()
		if !ok {
			return progressUnavailable
		}
		written := accepted()
		if !initialized || written != lastAccepted || n < lastUnread {
			token++
		}
		initialized, lastAccepted, lastUnread = true, written, n
		return token
	}
}

// ProgressWriter is an owned transport's writer with a progress count: pass it to
// New and supply its Accepted counter plus the owner's outstanding-data query
// to ConsumedProgress for Peer.Progress.
type ProgressWriter struct {
	W        io.Writer
	accepted atomic.Int64
}

func (w *ProgressWriter) Write(b []byte) (int, error) { return WriteCounted(w.W, b, &w.accepted) }

// Accepted is how many bytes W has accepted so far.
func (w *ProgressWriter) Accepted() int64 { return w.accepted.Load() }

// cancelBestEffort queues a withdrawal and returns; it never drops one on a
// healthy stream. A single worker writes queued withdrawals in order, ahead of
// later ordinary frames, so concurrent cancellations and a slow but progressing
// writer only delay them. Only an owned transport with a consumption signal
// (Interrupt and Progress) is retired only when the Write has no observable
// progress for StallWindow (observeStall): retirement ends the actual
// session, so the remote reads EOF and its pending work ends too. A borrowed
// stream is never retired here, since logical closure alone would leave its
// reader executing requests it could no longer answer; behind a stuck legacy
// writer its withdrawal waits, as the synchronous write before #1204 did.
// Cancellation is not an execution acknowledgement.
func (p *Peer) cancelBestEffort(id string) {
	var b []byte
	if len(id) <= cancelFrameMost {
		params, _ := json.Marshal(map[string]string{"id": id})
		b, _ = json.Marshal(message{Event: cancelEvent, Params: params})
		b = append(b, '\n')
	}
	if b == nil || len(b) > cancelFrameMost || (p.Limits.FrameBytes > 0 && len(b) > p.Limits.FrameBytes) {
		// A configuration that cannot carry any withdrawal, not a transient state.
		p.Close(errFrameBudget)
		return
	}
	for {
		p.mu.Lock()
		select {
		case <-p.closed:
			p.mu.Unlock()
			return
		default:
		}
		if len(p.controlQueue) < cancelQueueMost {
			p.controlQueue = append(p.controlQueue, b)
			if p.controlDone == nil {
				p.controlDone = make(chan struct{})
				go p.drainControl()
			}
			p.mu.Unlock()
			return
		}
		if p.controlSpace == nil {
			p.controlSpace = make(chan struct{})
		}
		space := p.controlSpace
		p.mu.Unlock()
		select {
		case <-space:
		case <-p.closed:
			return
		}
	}
}

// drainControl is the one withdrawal worker: it exists only while the queue is
// non-empty, and ordinary writers wait for controlDone.
func (p *Peer) drainControl() {
	for {
		if p.admitControl() {
			p.writeControl()
			<-p.wsem
		}
		p.mu.Lock()
		closed := false
		select {
		case <-p.closed:
			closed = true
		default:
		}
		if closed || len(p.controlQueue) == 0 {
			p.controlQueue = nil
			close(p.controlDone)
			p.controlDone = nil
			p.wakeControlSpace()
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}
}

// wakeControlSpace is called under mu.
func (p *Peer) wakeControlSpace() {
	if p.controlSpace != nil {
		close(p.controlSpace)
		p.controlSpace = nil
	}
}

// admitControl waits for the writer. On an owned transport with a progress
// signal it retires the transport if the Write in progress stalls; otherwise
// (borrowed, or no progress signal) it waits for the writer.
func (p *Peer) admitControl() bool {
	watch := p.Interrupt != nil && p.Progress != nil
	var clock stallClock
	var tick <-chan time.Time
	if watch {
		p.observeStall(&clock)
		ticker := time.NewTicker(controlStallPoll)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case p.wsem <- struct{}{}:
			return true
		case <-p.closed:
			return false
		case <-tick:
			if p.observeStall(&clock) && p.retireStalled(&clock) {
				return false
			}
		}
	}
}

// writeControl writes queued withdrawals while the worker holds the writer, with
// one watcher for their Writes; a failed or partial one closes the stream before
// anything else is written.
func (p *Peer) writeControl() {
	if p.Interrupt != nil && p.Progress != nil {
		done, watched := make(chan struct{}), make(chan struct{})
		go func() { defer close(watched); p.watchWrite(done) }()
		defer func() { close(done); <-watched }()
	}
	for {
		p.mu.Lock()
		if len(p.controlQueue) == 0 {
			p.mu.Unlock()
			return
		}
		b := p.controlQueue[0]
		p.controlQueue[0] = nil
		p.controlQueue = p.controlQueue[1:]
		p.wakeControlSpace()
		p.mu.Unlock()
		if err := p.writeAdmitted(context.Background(), b); err != nil {
			p.Close(err)
			return
		}
	}
}

// Call sends a request and decodes the result into out (if non-nil).
func (p *Peer) Call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := fmt.Sprintf("%s%d", p.prefix, p.nextID.Add(1))
	ch := make(chan message, 1)
	p.mu.Lock()
	if p.Limits.PendingCalls > 0 && len(p.pending) >= p.Limits.PendingCalls {
		p.mu.Unlock()
		return errors.New("rpc: runtime pending-call budget exceeded")
	}
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := p.writeContext(ctx, message{ID: id, Method: method, Params: raw}); err != nil {
		// A legacy writer may finish the entire frame after cancellation. Still
		// send its opted-in cancel in that case; never send for an unsent request
		// or on the retired stream of a partial/failed write.
		if ctx.Err() != nil && errors.Is(err, ErrDropped) && p.Cancels[method] {
			p.cancelBestEffort(id)
		}
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		if p.Cancels[method] {
			p.cancelBestEffort(id)
		}
		return ctx.Err()
	case <-p.closed:
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), ErrDropped)
		}
		return ErrDropped
	}
}

// Notify sends a one-way event.
func (p *Peer) Notify(event string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return p.write(message{Event: event, Params: raw})
}

// retireStalled resamples a candidate outside mu: a still-active frame may have
// progressed since observeStall. Only a fresh, unchanged token can be committed.
// Generation validation and terminal publication still share mu with frame
// start/completion; callbacks run after mu and Once are released.
func (p *Peer) retireStalled(c *stallClock) bool {
	p.mu.Lock()
	current := c.on && c.progress != progressUnavailable && p.activeOn && p.activeSeq == c.seq
	p.mu.Unlock()
	if !current || time.Since(c.since) < StallWindow {
		return false
	}
	sample := p.sampleProgress()
	p.mu.Lock()
	if !p.activeOn || p.activeSeq != c.seq {
		c.on = false
		p.mu.Unlock()
		return false
	}
	now := time.Now()
	if !sample.freshAt(now) {
		c.unavailable(c.seq, now)
		p.mu.Unlock()
		return false
	}
	if sample.value != c.progress {
		c.progress, c.since = sample.value, sample.finished
		p.mu.Unlock()
		return false
	}
	if sample.started.Sub(c.since) < StallWindow {
		p.mu.Unlock()
		return false
	}
	closedHere, cancel := p.closeLocked(ErrStalled)
	p.mu.Unlock()
	p.finishClose(closedHere, cancel)
	return closedHere
}

func (p *Peer) Close(err error) {
	p.mu.Lock()
	closedHere, cancel := p.closeLocked(err)
	p.mu.Unlock()
	p.finishClose(closedHere, cancel)
}

// closeLocked publishes closure under mu; it never invokes owner callbacks.
func (p *Peer) closeLocked(err error) (bool, context.CancelFunc) {
	closedHere := false
	p.once.Do(func() {
		closedHere = true
		p.err = err
		close(p.closed)
	})
	return closedHere, p.runtimeCancel
}

func (p *Peer) finishClose(closedHere bool, cancel context.CancelFunc) {
	if !closedHere {
		return
	}
	if cancel != nil {
		cancel()
	}
	// A transport hook may call Close again. sync.Once and mu must both be
	// released before invoking it; only the winner owns the interrupt.
	if p.Interrupt != nil {
		p.Interrupt()
	}
}

func (p *Peer) Done() <-chan struct{} { return p.closed }

// Err is why the peer was closed (what Close was given); nil while it is open.
func (p *Peer) Err() error {
	select {
	case <-p.closed:
		return p.err
	default:
		return nil
	}
}
