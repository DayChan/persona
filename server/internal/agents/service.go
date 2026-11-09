package agents

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// Error is a refusal with the HTTP status the hub should answer.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func fail(status int, format string, args ...any) error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, args...)}
}

// HTTPStatus maps an error from the service to a status: its own refusals (and a plugin source's, mapped to them)
// keep theirs, anything else is a bad gateway.
func HTTPStatus(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusBadGateway
}

// FollowUps are when to tell the apps to re-pull a session after a send: the bridge re-polls the agent at these
// delays itself (FOLLOW_UP_DELAYS_MS), and there is no streaming.
var FollowUps = []time.Duration{4 * time.Second, 15 * time.Second, 45 * time.Second}

// Service is the agents layer the hub serves: sources, a cache, the outbox, and change notifications.
type Service struct {
	srcMu     sync.RWMutex
	sources   map[string]Source
	order     []string
	polls     map[string]*pollMarks // source → what the poll saw of it; a new one each time the source is added
	cache     *cache
	listIndex sessionlist.Index
	out       *outbox
	prefs     *prefsStore
	onChange  func(ids []string) // sessions whose content changed: the apps re-pull them
	logf      func(string, ...any)

	pollMu sync.Mutex // one poll at a time; guards every pollMarks' seen
}

// pollMarks is what the poll saw of one source's sessions at its last list: id → rankTimeMs, unreadCount. seen is
// nil until the source first answers: that list is its baseline, since the apps were told when it was added.
type pollMarks struct{ seen map[string][2]int64 }

func NewService(kv KV, onChange func(ids []string), logf func(string, ...any), sources ...Source) *Service {
	s := &Service{sources: map[string]Source{}, polls: map[string]*pollMarks{}, cache: newCache(20 * time.Second), out: &outbox{kv: kv}, prefs: &prefsStore{kv: kv},
		onChange: onChange, logf: logf}
	s.cache.onSessions = s.indexSessions
	if s.onChange == nil {
		s.onChange = func([]string) {}
	}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	for _, src := range sources {
		s.sources[src.Name()] = src
		s.order = append(s.order, src.Name())
		s.polls[src.Name()] = &pollMarks{}
	}
	return s
}

// Add registers a source while the hub runs (a plugin's, docs/extensions/extensions.md "Personal agent sources"); false when one by that
// name is there already.
func (s *Service) Add(src Source) bool {
	s.srcMu.Lock()
	defer s.srcMu.Unlock()
	if _, ok := s.sources[src.Name()]; ok {
		return false
	}
	s.sources[src.Name()] = src
	s.order = append(s.order, src.Name())
	s.polls[src.Name()] = &pollMarks{}
	return true
}

// Get is the source registered under name, nil when none.
func (s *Service) Get(name string) Source {
	s.srcMu.RLock()
	defer s.srcMu.RUnlock()
	return s.sources[name]
}

// Remove drops a source (its plugin turned off or gone) with what is cached of it, and tells the apps.
func (s *Service) Remove(name string) {
	s.srcMu.Lock()
	if _, ok := s.sources[name]; !ok {
		s.srcMu.Unlock()
		return
	}
	delete(s.sources, name)
	delete(s.polls, name)
	s.order = slices.DeleteFunc(s.order, func(n string) bool { return n == name })
	s.srcMu.Unlock()
	s.cache.forget(name)
	s.onChange([]string{})
}

// many says whether there is more than one source: alone, a source's failure is the answer, as it always was.
func (s *Service) many() bool {
	s.srcMu.RLock()
	defer s.srcMu.RUnlock()
	return len(s.order) > 1
}

// has says whether source is registered now.
func (s *Service) has(source string) bool {
	s.srcMu.RLock()
	defer s.srcMu.RUnlock()
	_, ok := s.sources[source]
	return ok
}

// all is the sources in order, as they are now.
func (s *Service) all() []Source {
	s.srcMu.RLock()
	defer s.srcMu.RUnlock()
	out := make([]Source, len(s.order))
	for i, n := range s.order {
		out[i] = s.sources[n]
	}
	return out
}

func (s *Service) source(id string) (Source, string, error) {
	name, key, err := SplitID(id)
	if err != nil {
		return nil, "", &Error{Status: http.StatusBadRequest, Msg: err.Error()}
	}
	s.srcMu.RLock()
	src := s.sources[name]
	s.srcMu.RUnlock()
	if src == nil {
		return nil, "", fail(http.StatusNotFound, "agents: unknown source %q", name)
	}
	return src, key, nil
}

func (s *Service) Statuses(ctx context.Context) []Status {
	out := []Status{}
	for _, src := range s.all() {
		st, _ := src.Status(ctx)
		if st.Providers == nil {
			st.Providers = []AgentProvider{}
		}
		if st.Accounts == nil {
			st.Accounts = []AgentAccount{}
		}
		out = append(out, st)
	}
	return out
}

func (s *Service) sessionsOf(ctx context.Context, src Source, fresh bool) ([]Session, error) {
	name := src.Name()
	if !fresh {
		if list, ok := s.cache.getSessions(name); ok {
			return list, nil
		}
	}
	gen := s.cache.generation(name)
	list, err := src.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	s.cache.putSessions(name, gen, list)
	return list, nil
}

// Sessions lists sessions of every source (or one), optionally of one provider, ordered as the sources rank them,
// less the ones the user removed (hidden.go). Of all of them, one that fails is left out (its status says why) so
// it does not take the others' away; asked for by name, its error is the answer.
func (s *Service) Sessions(ctx context.Context, source, provider string) ([]Session, error) {
	list, err := s.listed(ctx, source, provider)
	if err != nil {
		return nil, err
	}
	return withoutHidden(list, s.hiddenNow(ctx)), nil
}

// allSessions is every source's list, removed ones included.
func (s *Service) allSessions(ctx context.Context) ([]Session, error) { return s.listed(ctx, "", "") }

func (s *Service) listed(ctx context.Context, source, provider string) ([]Session, error) {
	out := []Session{}
	for _, src := range s.all() {
		if source != "" && src.Name() != source {
			continue
		}
		list, err := s.sessionsOf(ctx, src, false)
		if err != nil && source == "" && s.many() {
			s.logf("agents: %s: sessions: %v", src.Name(), err)
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			if provider == "" || x.Provider == provider {
				out = append(out, x)
			}
		}
	}
	return out, nil
}

func (s *Service) Search(ctx context.Context, source, q string) ([]SearchHit, error) {
	out := []SearchHit{}
	for _, src := range s.all() {
		if source != "" && src.Name() != source {
			continue
		}
		hits, err := src.Search(ctx, q)
		if err != nil && source == "" && s.many() {
			s.logf("agents: %s: search: %v", src.Name(), err)
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, hits...)
	}
	return out, nil
}

// Session finds one session by id, re-listing once if the cached list does not have it.
func (s *Service) Session(ctx context.Context, id string) (Session, error) {
	src, _, err := s.source(id)
	if err != nil {
		return Session{}, err
	}
	for _, fresh := range []bool{false, true} {
		list, err := s.sessionsOf(ctx, src, fresh)
		if err != nil {
			return Session{}, err
		}
		for _, x := range list {
			if x.ID == id {
				return x, nil
			}
		}
	}
	return Session{}, fail(http.StatusNotFound, "agents: no session %s", id)
}

func (s *Service) Messages(ctx context.Context, id, cursor string, limit int) (*MessagePage, error) {
	src, key, err := s.source(id)
	if err != nil {
		return nil, err
	}
	if cursor == "" {
		if p, ok := s.cache.getLatest(id, limit); ok {
			return p, nil
		}
	}
	gen := s.cache.generation(src.Name())
	p, err := src.Messages(ctx, key, cursor, limit)
	if err != nil {
		return nil, err
	}
	if p.Session != nil && !p.Session.HasAvatar {
		if list, ok := s.cache.getSessions(src.Name()); ok {
			for _, x := range list {
				if x.ID == id {
					cp := withAvatar(*p.Session, x.AvatarURL)
					p.Session = &cp
					break
				}
			}
		}
	}
	if cursor == "" {
		s.cache.putLatest(src.Name(), gen, id, limit, p)
	}
	return p, nil
}

// Avatar decodes a session's avatar (404 when it has none).
func (s *Service) Avatar(ctx context.Context, id string) (*Avatar, error) {
	x, err := s.Session(ctx, id)
	if err != nil {
		return nil, err
	}
	if x.AvatarURL == "" {
		return nil, fail(http.StatusNotFound, "agents: session %s has no avatar", id)
	}
	a, err := decodeAvatar(ctx, x.AvatarURL)
	if err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	return a, nil
}

// SendResult is the outcome of a send: its outbox row, and whether it was already sent (nothing new went out).
type SendResult struct {
	Entry     OutboxEntry     `json:"outbox"`
	Duplicate bool            `json:"duplicate"`
	Upstream  json.RawMessage `json:"upstream,omitempty"`
}

var uuidRe = lazyre.New(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func NewClientID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// TextLen counts like JavaScript's String.length (UTF-16 units), which is what the Lark gateway's 20000 limit measures.
func TextLen(s string) int { return len(utf16.Encode([]rune(s))) }

func preview(s string) string {
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// Send posts text into a session as the user. clientID (a UUID; generated when empty) makes it idempotent: a
// clientId the outbox already saw sent is answered from the outbox without calling the source, and one that failed
// or never got an answer is retried with the same clientId, which the Lark gateway itself deduplicates.
func (s *Service) Send(ctx context.Context, id, clientID, text, origin string) (*SendResult, error) {
	src, key, err := s.source(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, fail(http.StatusBadRequest, "agents: text is empty")
	}
	if n := TextLen(text); n > AgentMaxText {
		return nil, fail(http.StatusRequestEntityTooLarge, "agents: text is %d characters, the limit is %d", n, AgentMaxText)
	}
	if clientID == "" {
		clientID = NewClientID()
	} else if !uuidRe.MatchString(clientID) {
		return nil, fail(http.StatusBadRequest, "agents: clientId must be a UUID")
	}
	clientID = strings.ToLower(clientID)
	prev, seen := s.out.get(ctx, clientID)
	if seen && prev.SessionID != id {
		return nil, fail(http.StatusConflict, "agents: clientId %s was used for another session", clientID)
	}
	if seen && (prev.Status == OutSent || prev.Status == OutDeleted) {
		return &SendResult{Entry: prev, Duplicate: true}, nil
	}
	sess, err := s.Session(ctx, id)
	if err != nil {
		return nil, err
	}
	if !sess.CanSend {
		return nil, fail(http.StatusConflict, "agents: session %s cannot send now (canSend=false)", id)
	}
	now := time.Now().UTC()
	e := prev
	if !seen {
		e = OutboxEntry{ClientID: clientID, SessionID: id, Preview: preview(text), Length: TextLen(text), Origin: origin, CreatedAt: now}
	}
	e.Status, e.Error, e.UpdatedAt = OutSending, "", now
	e.Attempts++
	if err := s.out.put(ctx, e); err != nil {
		s.logf("agents: outbox: %v", err)
	}
	up, err := src.Send(ctx, key, clientID, text)
	e.UpdatedAt = time.Now().UTC()
	if err != nil {
		e.Status, e.Error = OutFailed, err.Error()
	} else {
		e.Status = OutSent
	}
	if perr := s.out.put(ctx, e); perr != nil {
		s.logf("agents: outbox: %v", perr)
	}
	if err != nil {
		return nil, err
	}
	s.Updated(src.Name(), []string{key})
	s.followUp(src.Name(), key)
	return &SendResult{Entry: e, Upstream: up}, nil
}

func (s *Service) followUp(source, key string) {
	for _, d := range FollowUps {
		time.AfterFunc(d, func() { s.Updated(source, []string{key}) })
	}
}

func (s *Service) DeleteMessage(ctx context.Context, id, clientID string) error {
	src, key, err := s.source(id)
	if err != nil {
		return err
	}
	if err := src.DeleteMessage(ctx, key, clientID); err != nil {
		return err
	}
	if e, ok := s.out.get(ctx, strings.ToLower(clientID)); ok {
		e.Status, e.UpdatedAt = OutDeleted, time.Now().UTC()
		_ = s.out.put(ctx, e)
	}
	s.Updated(src.Name(), []string{key})
	return nil
}

func (s *Service) MarkRead(ctx context.Context, id string) error {
	src, key, err := s.source(id)
	if err != nil {
		return err
	}
	if err := src.MarkRead(ctx, key); err != nil {
		return err
	}
	s.Updated(src.Name(), []string{key})
	return nil
}

func (s *Service) Outbox(ctx context.Context, id string, limit int) []OutboxEntry {
	return s.out.list(ctx, id, limit)
}

// Updated is a source reporting that these sessions changed (agent-bridge.updated, a send, the poll): drop
// their cache and tell the apps. No keys means "something changed": only the list is dropped.
func (s *Service) Updated(source string, keys []string) {
	if !s.has(source) {
		return // gone (its plugin stopped) since: a send's follow-up has nothing left to tell
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, source+":"+k)
	}
	s.cache.invalidate(source, ids)
	s.onChange(ids)
}

// Poll is the fallback when realtime events are missing: every interval it re-lists each source and reports the
// sessions whose rankTimeMs or unreadCount moved. A source's first list (at start, or once it is added) only records
// its baseline.
func (s *Service) Poll(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.PollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) PollOnce(ctx context.Context) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	srcs, marks := s.pollTargets()
	for i, src := range srcs {
		n := src.Name()
		gen := s.cache.generation(n)
		list, err := src.Sessions(ctx)
		if err != nil {
			continue // the next tick tries again; the routes report errors themselves
		}
		changed := s.mark(n, marks[i], list)
		s.cache.putSessions(n, gen, list)
		if len(changed) > 0 {
			s.Updated(n, changed)
		}
	}
}

// pollTargets is the sources in order with what the poll saw of each.
func (s *Service) pollTargets() ([]Source, []*pollMarks) {
	s.srcMu.RLock()
	defer s.srcMu.RUnlock()
	srcs, marks := make([]Source, len(s.order)), make([]*pollMarks, len(s.order))
	for i, n := range s.order {
		srcs[i], marks[i] = s.sources[n], s.polls[n]
	}
	return srcs, marks
}

// mark records list as what the poll saw of source and answers the keys of its sessions that are new or moved since
// the last list: none on its first (the baseline), nor when the source was removed while it answered (added again,
// it starts over). Callers hold pollMu.
func (s *Service) mark(source string, pm *pollMarks, list []Session) []string {
	s.srcMu.RLock()
	current := s.polls[source] == pm
	s.srcMu.RUnlock()
	if !current {
		return nil
	}
	seen := make(map[string][2]int64, len(list))
	var changed []string
	for _, x := range list {
		m := [2]int64{x.RankTimeMs, int64(x.UnreadCount)}
		if old, ok := pm.seen[x.ID]; pm.seen != nil && (!ok || old != m) {
			changed = append(changed, x.SessionKey)
		}
		seen[x.ID] = m
	}
	pm.seen = seen
	return changed
}
