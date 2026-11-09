package node

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// stateFile remembers which sessions Persona started and with which launcher. It lives in the hub's Postgres:
// loaded with `state.load` when the node starts and written through with `state.put` events. A session in Sessions
// is Persona's (takesReport, close and decorate's StartedByPersona go by that): what the node only observes of the
// user's own sessions never goes in (ultracode.go keeps that in memory, #5510).
type stateFile struct {
	mu       sync.Mutex
	Sessions map[string]sessionState `json:"sessions"`
	persist  func(id string, v sessionState)
	// loading: the hub's copy has yet to come (Serve opens it before loadState runs, which closes it). Until then a
	// session missing from Sessions may still be Persona's. Nil once loaded.
	loading chan struct{}
}

type sessionState struct {
	Pane             string    `json:"pane,omitempty"`
	Launcher         string    `json:"launcher"`
	Model            string    `json:"model,omitempty"`
	ModelFingerprint string    `json:"model_fingerprint,omitempty"`
	ModelEffort      string    `json:"model_effort,omitempty"`
	Runtime          string    `json:"runtime,omitempty"` // the assistant runtime it was asked to run on (assistant_sdk.go)
	Cwd              string    `json:"cwd"`
	IsMain           bool      `json:"is_main,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	// AppMode / AppEffort: the permission mode and effort set on an assistant conversation (modes.go). AppMode
	// also retains a terminal Codex start constraint for later resumes. They
	// win over what its transcript recorded and outlive the node: an old "default" there no longer locks it in.
	AppMode   string `json:"app_mode,omitempty"`
	AppEffort string `json:"app_effort,omitempty"`
	// Tasks: the assistant conversation's background tasks and monitors running right now (node/assistant.go).
	Tasks []string `json:"tasks,omitempty"`
	// Compaction: the last compaction the user asked for (assistant_compact.go).
	Compaction *proto.Compaction `json:"compaction,omitempty"`
	// Guest: a guest conversation's tools (assistant_guest.go): every process it runs in has no built-in tools and
	// only these of Persona's. Nil is an ordinary conversation.
	Guest []string `json:"guest,omitempty"`
	// Ultracode: what was last seen of ultracode in the process Persona runs this session in (ultracode.go). Written
	// by setUltracode only, which never makes a record for a session that has none.
	Ultracode *ultracodeRecord `json:"ultracode_process,omitempty"`
}

func newState() *stateFile { return &stateFile{Sessions: map[string]sessionState{}} }

// legacyState reads the pre-Postgres ~/.persona/sessions.json, if any.
func legacyState(path string) map[string]sessionState {
	var f struct {
		Sessions map[string]sessionState `json:"sessions"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f.Sessions
}

// merge adds sessions loaded from the hub without overwriting ones touched since the node started.
func (s *stateFile) merge(m map[string]sessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range m {
		if observationOnly(v) {
			continue // not Persona's, and what it says of the user's session is not kept either (observationOnly)
		}
		if _, ok := s.Sessions[id]; !ok {
			s.Sessions[id] = v
		}
	}
}

// observationOnly: a record holding nothing but an ultracode observation, as a node of #3782 wrote for a session the
// user started. Persona's own records always have a directory and a launcher (checkStart fills both in), and the hub
// gives back a zero start time only for a record stored without one next to an ultracode observation
// (db.PutNodeSession's started_at_unknown). Such a record is dropped when loaded, observation and all: nothing would
// ever write it again (the user's sessions get no record), so a resume notice dismissed now, or a value of a process
// long gone, would come back from it with every node restart. What the node sees of the session from then on stays in
// memory, as for any session the user started.
func observationOnly(v sessionState) bool {
	return v.Ultracode != nil && v.StartedAt.IsZero() && v.Launcher == "" && v.Cwd == ""
}

// beginLoad and endLoad bracket loading the hub's copy (loading).
func (s *stateFile) beginLoad() {
	s.mu.Lock()
	if s.loading == nil {
		s.loading = make(chan struct{})
	}
	s.mu.Unlock()
}

func (s *stateFile) endLoad() {
	s.mu.Lock()
	if s.loading != nil {
		close(s.loading)
		s.loading = nil
	}
	s.mu.Unlock()
}

func (s *stateFile) isLoading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loading != nil
}

// loaded waits until the hub's copy is loaded (or failed to load: loadState gives up after 20 seconds).
func (s *stateFile) loaded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	ch := s.loading
	s.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ultracode is the stored ultracode observation of a session Persona started.
func (s *stateFile) ultracode(id string) (ultracodeRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Sessions[id]
	if !ok || v.Ultracode == nil {
		return ultracodeRecord{}, false
	}
	return *v.Ultracode, true
}

// setUltracode changes the ultracode observation of a session Persona started and nothing else of its record, under
// the lock, so a copy read before another writer's change is never written back over it. A session without a record
// is not Persona's, or not loaded yet: nothing is written for it (false).
func (s *stateFile) setUltracode(id string, rec ultracodeRecord) bool {
	s.mu.Lock()
	v, ok := s.Sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	v.Ultracode = &rec
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
	}
	return true
}

func (s *stateFile) get(id string) (sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Sessions[id]
	return v, ok
}

// panes returns the remembered pane of every session (herdr pane ids are not reused while the server lives).
func (s *stateFile) panes() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for id, v := range s.Sessions {
		if v.Pane != "" {
			out[v.Pane] = id
		}
	}
	return out
}

func (s *stateFile) setPane(id, pane string) {
	s.mu.Lock()
	v, ok := s.Sessions[id]
	if !ok || v.Pane == pane {
		s.mu.Unlock()
		return
	}
	v.Pane = pane
	s.Sessions[id] = v
	s.mu.Unlock()
	s.put(id, v)
}

func (s *stateFile) put(id string, v sessionState) {
	s.mu.Lock()
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
	}
}
