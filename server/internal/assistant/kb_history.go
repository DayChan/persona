package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// The history import (docs/assistant/knowledge.md "History import"): a chat connector that can read its
// conversations backwards (connectors.history_chats / history / history_anchors, read-only) hands their older
// messages to the knowledge base, which merges them into the day files (kb.Engine.WriteHistory) as archive only.
//
// It runs in its own goroutine, never inside the knowledge base's loop, and keeps everything on disk under history/
// (kb_history_files.go), so a redeploy at any time loses nothing: a restart goes on from each chat's state. It reads
// a source only when all of these hold: the knowledge base is on and writable, kb.history.enabled is true and lists
// the source, the owner approved the source's plan in the assistant chat (kb_history_resume), there is no STOP, and
// the source does not wait for the search index (kb.SearchIndexed). One source at a time, in the order kb.history
// lists them; one request in flight, ever; every wait drawn afresh (kb_history_pace.go). A call that timed out or an
// answer it does not know stops the whole import (STOP, a push) until a person looks; nothing is retried by itself.
// A stop never cancels the call in flight: it takes effect before the next one.

const (
	kbHistoryPoll        = time.Minute     // how often the loop looks for something to run
	historyCallTimeout   = 3 * time.Minute // one call (the hub bounds it at two)
	historyWaitStep      = 30 * time.Second
	historyPauseMost     = 2 * time.Hour // paused longer than this without a call going through: stop
	historyTransientWait = 5 * time.Minute
)

// kbHistoryShared is the import's part of the Service.
type kbHistoryShared struct {
	calls    sync.Mutex // one request in flight to any source, the driver's and the tools' alike
	wake     chan struct{}
	running  atomic.Pointer[string] // the source this process is importing now
	instance string
}

// historySource is how the import reaches the connectors (through the hub; a fake in tests).
type historySource interface {
	Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error)
	Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error)
	Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error)
	Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error)
	Media(ctx context.Context, id, ref string) (connector.Media, error)
}

// historyDriver is one import: its dependencies, and the state of the run it is in.
type historyDriver struct {
	data      string
	src       historySource
	eng       func(context.Context) (*kb.Engine, error) // the knowledge base, open and writable
	cfg       func(context.Context) *assistantwire.KBHistory
	connected func() bool
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	rng       *mrand.Rand
	logf      func(string, ...any)
	notify    func(ctx context.Context, source string, why historyReason)
	calls     *sync.Mutex
	instance  string
	indexed   func() bool
	running   *atomic.Pointer[string]
	timeout   time.Duration

	// The run.
	lock        *historyLock
	key         []byte
	zone        *time.Location
	source      string
	info        assistantwire.ArchiveSource
	pacer       *historyPacer
	pausedSince time.Time
	halted      error // a halt an image download met inside a page's write, acted on after it
	beatAt      time.Time
	// lastOK: the call before went through, with nothing but the interval since (no rest, pause or quiet period).
	lastOK bool
}

// historyHalt ends a run: stop (write STOP, push; a person must look) or not (the import was turned off, the
// knowledge base closed, STOP was there already: the loop looks again later).
type historyHalt struct {
	stop bool
	why  historyReason
}

func (h *historyHalt) Error() string { return h.why.String() }

// halt is a historyHalt for the reason code, with its arguments as key, value pairs (historyReason).
func halt(stop bool, code string, kv ...string) *historyHalt {
	return &historyHalt{stop: stop, why: reason(code, kv...)}
}

func haltWhy(stop bool, why historyReason) *historyHalt { return &historyHalt{stop: stop, why: why} }

// ---- the Service's side ----

func (s *Service) kbHistoryInit() {
	s.kb.history.wake = make(chan struct{}, 1)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	s.kb.history.instance = hex.EncodeToString(b)
}

// kbHistoryWake has the loop look again now (a plan was approved, STOP removed).
func (s *Service) kbHistoryWake() {
	select {
	case s.kb.history.wake <- struct{}{}:
	default:
	}
}

// kbHistoryLoop runs the import while ctx lasts, looking every minute (at once when woken).
func (s *Service) kbHistoryLoop(ctx context.Context) {
	if s.kb.dir == "" {
		return
	}
	t := time.NewTicker(kbHistoryPoll)
	defer t.Stop()
	for {
		s.kbRun(ctx, func() { s.kbHistoryRound(ctx) })
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kb.history.wake:
		}
	}
}

// kbHistoryRound runs what is runnable now, if anything: only while connected, not a mesh member's standby, the
// knowledge base writable and kb.history enabled.
func (s *Service) kbHistoryRound(ctx context.Context) {
	if !s.kbConnected() || s.hello(ctx).Assistant.Standby || !s.kbWritable(ctx) {
		return
	}
	if set := s.kbSettings(ctx); set == nil || !set.History.On() {
		return
	}
	e, err := s.kbGet(ctx)
	if err != nil || e.Store.ReadOnly() {
		return
	}
	d := s.historyDriver(e.Store.Dir)
	if err := d.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.logf("kb history: %v", err)
	}
}

// historyDriver is an import of the knowledge base in data, reaching the connectors through the hub.
func (s *Service) historyDriver(data string) *historyDriver {
	return &historyDriver{
		data: data,
		src:  busHistory{s: s},
		eng: func(ctx context.Context) (*kb.Engine, error) {
			if !s.kbWritable(ctx) {
				return nil, errors.New("the knowledge base is not writable")
			}
			e, err := s.kbGet(ctx)
			if err == nil && e.Store.ReadOnly() {
				err = kb.ErrReadOnly
			}
			return e, err
		},
		cfg: func(ctx context.Context) *assistantwire.KBHistory {
			if set := s.kbSettings(ctx); set != nil && !set.Off && !set.ReadOnly {
				return set.History
			}
			return nil
		},
		connected: s.kbConnected,
		now:       time.Now,
		sleep:     sleepCtx,
		rng:       mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64())),
		logf:      s.logf,
		notify: func(ctx context.Context, source string, why historyReason) {
			l := s.lang(ctx)
			title, body := "assistant-kb_history.stopped_title", "assistant-kb_history.stopped_body"
			if why.Code == "plan_changed" {
				title, body = "assistant-kb_history.plan_changed_title", "assistant-kb_history.plan_changed_body"
			}
			s.push(ctx, Notification{Subtitle: "Persona", Title: i18n.T(l, title),
				Body: i18n.T(l, body, i18n.Args{"source": source, "reason": historyReasonText(l, why), "count": why.Args["count"]})})
		},
		calls:    &s.kb.history.calls,
		instance: s.kb.history.instance,
		indexed:  func() bool { return historySearchIndexed },
		running:  &s.kb.history.running,
		timeout:  historyCallTimeout,
	}
}

// historySearchIndexed is kb.SearchIndexed (a var for the tests).
var historySearchIndexed = kb.SearchIndexed

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// busHistory reaches the connectors through the hub (connectors.*).
type busHistory struct{ s *Service }

func (b busHistory) Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error) {
	var list assistantwire.Archives
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchives, map[string]any{}, &list)
	return list.Archives, err
}

func (b busHistory) Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error) {
	var out assistantwire.HistoryChatsResult
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistoryChats, map[string]any{"id": id}, &out)
	return out.Chats, err
}

func (b busHistory) Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	var out connectorwire.HistoryPage
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistory, assistantwire.HistoryParams{ID: id, Query: q}, &out)
	return out, err
}

func (b busHistory) Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	var out connectorwire.HistoryAnchors
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistoryAnchors, assistantwire.HistoryAnchorsParams{ID: id, Query: q}, &out)
	return out, err
}

func (b busHistory) Media(ctx context.Context, id, ref string) (connector.Media, error) {
	var out struct {
		Media connector.Media `json:"media"`
	}
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchiveMedia, map[string]any{"id": id, "ref": ref}, &out)
	return out.Media, err
}
