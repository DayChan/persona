package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's own settings (settings `assistant`, docs/assistant/assistant.md "Boundary with hub"): the machine its
// conversations run on, its launcher, the user's time zone, "About me", the main conversation and the ones it replaced,
// and whether tasks from the main conversation are told only there. Only the assistant reads and writes them; hub.json's
// assistant section is their default (the hub's hello). It reports what the hub's own features need of them in every
// snapshot (Profile), and serves GET / PUT /api/v1/assistant, the warm-up and the memory routes.

// settingsKey is the setting the assistant keeps them in (the hub wrote the same JSON until 2026-11-04).
const settingsKey = "assistant"

// assistantRel is this manager's assistant directory on a machine, relative to its home (node assistant.prepare):
// assistant for the built-in manager, assistant-<id> for another (identity.AssistantDirName), in the state directory. It
// is spelled with the former state directory's name (compat.StateDir), which older nodes join to HOME as it is; a
// current node resolves either spelling to the state directory it runs on (node/assistant_guest.go assistantDirOf,
// #1681), so the spelling never decides where the directory is.
func (s *Service) assistantRel() string {
	return path.Join(compat.StateDir, identity.AssistantDirName(s.id))
}

// settingsFresh is how long the settings read from the database are used before they are read again (the assistant
// writes them itself; another hub sharing the database might too).
const settingsFresh = 5 * time.Second

// Settings are the assistant's settings, as saved.
type Settings struct {
	MachineID string `json:"machine_id"`
	Launcher  string `json:"launcher"`
	// DefaultPermissionMode applies only to new non-guest conversations without an explicit mode.
	// Empty keeps the runtime's existing default; it never changes an existing conversation.
	DefaultPermissionMode string `json:"default_permission_mode,omitempty"`
	// Timezone is the phone's (IANA name): routines and the snapshot read times in it, and the hub its own.
	Timezone string `json:"timezone,omitempty"`
	// Notes, once edited in the app, replace manager.json's assistant.notes.
	Notes *string `json:"notes,omitempty"`
	// OwnerAliases are the names the user explicitly adds for kb.owner; an empty list clears them.
	OwnerAliases []string `json:"owner_aliases,omitempty"`
	// Main is the main conversation (the Persona tab opens it): one long-running conversation, the others are side ones.
	Main string `json:"main,omitempty"`
	// Former are the main conversations replaced since (newest first, POST /api/v1/assistant/main/new): their machine
	// keeps listing them, archived, however long they are quiet.
	Former []FormerMain `json:"former_main,omitempty"`
	// MainTasksQuiet: tasks the main conversation opens are told only there, not pushed, on the Live Activity or in
	// task badges (#1731). nil is on (the default); the view always says what is in effect.
	MainTasksQuiet *bool `json:"main_tasks_quiet,omitempty"`
	// StartRequestedTasks: a task_plan task the user asked for in so many words starts without the card's "Start"
	// (#2559). nil is on (the default).
	StartRequestedTasks *bool `json:"start_requested_tasks,omitempty"`
	// RecordingDays: how many days the phone's recordings stay in the knowledge base (#3235); nil is 14.
	RecordingDays *int `json:"recording_days,omitempty"`
	// DoneNeedsDelivery: a finished task closes only once its work is live and checked; stopped at a PR, merged or
	// off by default it stays open with where it is held, and its agent is told so with its prompt (#3306,
	// delivery.go). nil is off (the default).
	DoneNeedsDelivery *bool `json:"done_needs_delivery,omitempty"`
	// IdleCloseHours: an agent Persona started that has been idle this many hours with nothing going on is closed
	// (tasks_idle.go); 0 turns it off, nil is 12 (the default).
	IdleCloseHours *int `json:"idle_close_hours,omitempty"`
	// ReviewSamplePercent: the share (0–100) of the finished supervised tasks that passed the judge's own check whose
	// review still goes to the main conversation (#3128, supervise_sample.go); the rest the judge closes. nil is 100:
	// every finished task waits for task_review, as before.
	ReviewSamplePercent *int `json:"review_sample_percent,omitempty"`
	// Leaders are the side conversations the user made leaders (#3133, supervise_leader.go): a task one hands out with
	// task_plan is supervised by it instead of the main conversation. Empty (the default): only the main one supervises.
	Leaders []string `json:"leaders,omitempty"`
}

// FormerMain is a main conversation that was replaced, and until when it was the main one.
type FormerMain struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Until     time.Time `json:"until"`
}

// settingsBook keeps the settings read last (with no database: the only copy).
type settingsBook struct {
	change sync.Mutex // one change at a time
	mu     sync.Mutex
	cached *Settings
	at     time.Time
	gen    uint64 // counts the changes saved: a read begun before one does not replace what it saved
}

// savedSettings are the settings as saved (zero when never written); false when they could not be read.
func (s *Service) savedSettings(ctx context.Context) (Settings, bool) {
	b := &s.prefs
	st := s.store()
	b.mu.Lock()
	if b.cached != nil && (st == nil || time.Since(b.at) < settingsFresh) {
		v := *b.cached
		b.mu.Unlock()
		return v, true
	}
	gen := b.gen
	b.mu.Unlock()
	var v Settings
	if st != nil {
		if _, err := st.Setting(ctx, settingsKey, &v); err != nil {
			s.logf("assistant: settings: %v", err)
			return Settings{}, false
		}
	}
	b.mu.Lock()
	if b.gen == gen { // no change was saved meanwhile (else what this read is older than what is cached)
		b.cached, b.at = &v, time.Now()
	} else if b.cached != nil {
		v = *b.cached
	}
	b.mu.Unlock()
	return v, true
}

// updateSettings changes the saved settings with fn (which may refuse) and saves them.
func (s *Service) updateSettings(ctx context.Context, fn func(*Settings) error) (Settings, error) {
	b := &s.prefs
	b.change.Lock()
	defer b.change.Unlock()
	b.mu.Lock()
	b.at = time.Time{} // read them again
	b.mu.Unlock()
	v, ok := s.savedSettings(ctx)
	if !ok {
		return Settings{}, errors.New("the assistant's settings could not be read")
	}
	if err := fn(&v); err != nil {
		return Settings{}, err
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, settingsKey, v); err != nil {
			return Settings{}, err
		}
	}
	b.mu.Lock()
	b.cached, b.at = &v, time.Now()
	b.gen++
	b.mu.Unlock()
	return v, nil
}

// settings are the settings in effect, what the rest of the package reads (the main conversation, the machine, the time
// zone; isMain and mainTasksQuiet for short): the machine one the hub has (else the hub's default), the launcher the
// assistant's own (else hub.json's, else claude; Model configuration › Main chat goes over it: launcherNow). A read that fails once
// they were read serves those (settingsLastGood): the zero value would say there is no main conversation, which
// would let it be archived and title it by its first message for that moment.
func (s *Service) settings(ctx context.Context) Settings {
	v, _ := s.settingsLastGood(ctx)
	return s.effective(ctx, v)
}

func (s *Service) effective(ctx context.Context, v Settings) Settings {
	def := s.hello(ctx).Assistant
	if v.MachineID == "" || !slices.Contains(def.Machines, v.MachineID) {
		v.MachineID = def.MachineID
	}
	if s.isWorkMachine(v.MachineID) {
		v.MachineID = def.MachineID
		if s.isWorkMachine(v.MachineID) {
			v.MachineID = ""
		}
	}
	if v.Launcher == "" {
		v.Launcher = def.Launcher
	}
	if v.Launcher == "" {
		v.Launcher = "claude"
	}
	return v
}

// notes is what the assistant knows about the user: edited in the app, else manager.json's assistant.notes.
func (s *Service) notes(ctx context.Context) string {
	if n := s.settings(ctx).Notes; n != nil {
		return strings.TrimSpace(*n)
	}
	return strings.TrimSpace(s.hello(ctx).OwnerNotes)
}

func quietOn(v *bool) bool { return v == nil || *v }

// mainTasksQuiet: a task opened from the main conversation now is quiet (main_tasks_quiet, on by default).
func (s *Service) mainTasksQuiet(ctx context.Context) bool {
	return quietOn(s.settings(ctx).MainTasksQuiet)
}

// startRequestedTasks: task_plan starts the tasks the user asked for without waiting for "Start" (start_requested_tasks,
// on by default).
func (s *Service) startRequestedTasks(ctx context.Context) bool {
	return quietOn(s.settings(ctx).StartRequestedTasks)
}

// Bounds of recording_days.
const (
	minRecordingDays = 1
	maxRecordingDays = 365
)

// recordingDays is how many days recordings stay in the knowledge base (recording_days, 14 by default).
func (s *Service) recordingDays(ctx context.Context) int {
	if d := s.settings(ctx).RecordingDays; d != nil && *d >= minRecordingDays && *d <= maxRecordingDays {
		return *d
	}
	return kb.DefaultRecordingDays
}

// settingsLastGood is savedSettings, except that a transient read failure serves the settings read last when there
// are any. Callers whose default would be destructive use it: the recording retention falling back to 14 days for one
// locked-database round would make the next KB prune delete recordings the user kept for longer (#4008). Callers for
// which a stale value is risky (kbZone: moving the archive into a possibly old time zone) keep using savedSettings.
func (s *Service) settingsLastGood(ctx context.Context) (Settings, bool) {
	if v, ok := s.savedSettings(ctx); ok {
		return v, true
	}
	s.prefs.mu.Lock()
	cached := s.prefs.cached // a save during the failed read may already have put newer settings here
	var v Settings
	if cached != nil {
		v = *cached
	}
	s.prefs.mu.Unlock()
	return v, cached != nil
}

// recordingDaysNow is recordingDays for the knowledge base's engine, which has no context of its own. A transient
// settings read failure keeps the value read last (settingsLastGood); only settings never successfully read fall back
// to the default, so one locked-database round can never shorten the retention and prune recordings early.
func (s *Service) recordingDaysNow() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, ok := s.settingsLastGood(ctx)
	if !ok || v.RecordingDays == nil || *v.RecordingDays < minRecordingDays || *v.RecordingDays > maxRecordingDays {
		return kb.DefaultRecordingDays
	}
	return *v.RecordingDays
}

// launcherNow is the launcher new conversations start with: Model configuration › Main chat's, else the assistant's own.
func (s *Service) launcherNow(ctx context.Context) string {
	if rec, err := s.modelRecord(ctx, "assistant"); err == nil && rec.Binding.Provider != "" {
		return strings.TrimPrefix(rec.Binding.Provider, "launcher.")
	}
	return s.settings(ctx).Launcher
}

// profile is what the hub's features need of the settings (Snapshot.Profile); tasks are the scheduled tasks and todos
// the open todos' agents as reported (either nil: unreadable, and Keep is left out, so the nodes keep what they list).
// nil when the settings could not be read.
func (s *Service) profile(ctx context.Context, tasks []TaskView, todos *TodoReport) *Profile {
	saved, ok := s.savedSettings(ctx)
	if !ok {
		return nil
	}
	v := s.effective(ctx, saved)
	p := &Profile{MachineID: v.MachineID, Main: v.Main, Launcher: v.Launcher, Timezone: v.Timezone}
	if tasks != nil && todos != nil {
		p.Keep = keptSessions(v, tasks, todos.Links)
	}
	return p
}

// keptSessions are the conversations their machines list however long they have been quiet: the main conversation,
// the main ones it replaced, scheduled tasks' own, and the agents of open todos. Otherwise a busy machine's 80 newest
// transcripts crowd them out of the list and the Persona tab has no main chat (2026-11-01: the main one, quiet for two
// days, was gone), a weekly task's conversation drops out in between, with the app's link to it and the check that its
// last run still goes, and an open task whose agent finished and exited shows "Starting Agent" for ever: its todo
// names a session the list no longer has (#4510).
func keptSessions(v Settings, tasks []TaskView, links []TodoLink) []SessionRef {
	out := []SessionRef{}
	add := func(machine, id string) {
		if machine != "" && id != "" && !slices.Contains(out, SessionRef{MachineID: machine, SessionID: id}) {
			out = append(out, SessionRef{MachineID: machine, SessionID: id})
		}
	}
	add(v.MachineID, v.Main)
	for _, f := range v.Former { // archived, still to be found
		add(f.MachineID, f.SessionID)
	}
	for _, t := range tasks {
		add(t.RunMachineID, t.RunSessionID)
	}
	for _, l := range links {
		add(l.MachineID, l.SessionID)
	}
	return out
}

// settingsChanged: the hub learns them (the snapshot's profile) and the next message reads the time in the new zone.
func (s *Service) settingsChanged(ctx context.Context) {
	s.ProfileChanged(ctx)
	s.publish(ctx)
}

// ---- GET / PUT /api/v1/assistant, the warm-up, the memory ----

// assistantCwd is this manager's assistant directory on d, or "" while d has not reported its home.
func (s *Service) assistantCwd(d Device) string {
	return stateDirOn(d.Home, d.DataDir, identity.AssistantDirName(s.id))
}

// stateDirOn is the directory name in a machine's state directory: its data directory when it reported one (a custom
// PERSONA_HOME), else ~/.persona; "" while it has not reported its home.
func stateDirOn(home, dataDir, name string) string {
	if home == "" {
		return ""
	}
	if dataDir != "" {
		return path.Join(dataDir, name)
	}
	return path.Join(home, ".persona", name)
}

type assistantView struct {
	Settings
	Manager            string             `json:"manager"`
	PermissionDefaults permissionDefaults `json:"permission_defaults"`
	Cwd                string             `json:"cwd"`
	Conversations      []proto.Session    `json:"conversations"`
	NotesEffective     string             `json:"notes_effective"`  // what the assistant is told about the user
	MainTasksQuiet     bool               `json:"main_tasks_quiet"` // in effect (the setting's own is left out while unset)
	// StartRequestedTasks is in effect, like MainTasksQuiet.
	StartRequestedTasks bool `json:"start_requested_tasks"`
	// RecordingDays and DoneNeedsDelivery are in effect, like MainTasksQuiet.
	RecordingDays     int  `json:"recording_days"`
	DoneNeedsDelivery bool `json:"done_needs_delivery"`
	// IdleCloseHours is in effect (0: off).
	IdleCloseHours int `json:"idle_close_hours"`
	// ReviewSamplePercent is in effect (100: every finished task waits for task_review).
	ReviewSamplePercent int `json:"review_sample_percent"`
}

// view is GET /api/v1/assistant; withArchived lists archived conversations too (flagged `archived`). It lists the
// assistant's own conversations; space "health" lists the health space's instead (docs/assistant/health.md), with that
// space's directory as Cwd.
func (s *Service) view(ctx context.Context, withArchived bool, space ...string) assistantView {
	sp := ""
	if len(space) > 0 {
		sp = space[0]
	}
	v := s.settings(ctx)
	v.Launcher = s.launcherNow(ctx)
	out := assistantView{Settings: v, Manager: s.managerIdentity(), PermissionDefaults: s.permissionDefaults(), Conversations: []proto.Session{}, NotesEffective: s.notes(ctx), MainTasksQuiet: quietOn(v.MainTasksQuiet),
		StartRequestedTasks: quietOn(v.StartRequestedTasks), RecordingDays: s.recordingDays(ctx),
		DoneNeedsDelivery: v.DoneNeedsDelivery != nil && *v.DoneNeedsDelivery, IdleCloseHours: int(s.idleCloseAfter(ctx) / time.Hour),
		ReviewSamplePercent: s.reviewSamplePercent(ctx)}
	if d, ok := s.device(ctx, v.MachineID); ok {
		out.Cwd = s.spaceCwd(d, sp)
		var list []proto.Session
		if s.fleetOn() {
			// Select the headless conversations before copying, decorating or serializing the inventory: coding
			// sessions can outnumber them by thousands.
			if m, found := s.fleetMachine(d.ID); found {
				list = s.decorateInventory(m.ID, s.assistantSessions(m), false)
			}
		} else {
			list, _ = s.sessions(ctx, d.ID)
		}
		for _, c := range list {
			if c.Assistant && spaceOf(c) == sp && (withArchived || !c.Archived) {
				out.Conversations = append(out.Conversations, c)
			}
		}
	}
	return out
}

func (s *Service) profileRoutes() {
	s.handle("GET /api/v1/assistant", func(w http.ResponseWriter, r *http.Request) {
		archived := r.URL.Query().Get("archived")
		space := r.URL.Query().Get("space")
		if !validSpace(space) {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("space %q: health or none", space))
			return
		}
		apierr.WriteJSON(w, s.view(r.Context(), archived == "1" || archived == "true", space))
	})
	s.handle("PUT /api/v1/assistant", s.putSettings)
	// warm: the app opened the chat tab; get a process ready so the first message skips claude's startup, and say what
	// the main conversation still needs.
	s.handle("POST /api/v1/assistant/warm", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Launcher string `json:"launcher"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		go s.warm(in.Launcher)
		// What the main conversation still needs, for the first screen's card (docs/assistant/onboarding.md 5.1); no probe, and
		// bounded so that opening the tab stays quick.
		out := map[string]any{"ok": true}
		if rd := s.readinessQuick(r.Context(), readinessWaitWarm); rd != nil {
			out["readiness"] = rd
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("GET /api/v1/assistant/memory", func(w http.ResponseWriter, r *http.Request) {
		var res json.RawMessage
		if err := s.memory(r.Context(), map[string]any{"op": "list", "space": r.URL.Query().Get("space")}, &res); err != nil {
			apierr.Call(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res)
	})
	s.handle("PUT /api/v1/assistant/memory/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change memory"))
			return
		}
		var in struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.memory(r.Context(), map[string]any{"op": "write", "name": r.PathValue("name"), "content": in.Content, "space": r.URL.Query().Get("space")}, nil); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	s.handle("DELETE /api/v1/assistant/memory/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change memory"))
			return
		}
		if err := s.memory(r.Context(), map[string]any{"op": "delete", "name": r.PathValue("name"), "space": r.URL.Query().Get("space")}, nil); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// warm gets a process ready on the assistant's machine (node assistant.warm) for the launcher ("" is the one new
// conversations start with).
func (s *Service) warm(launcher string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	machine := s.settings(ctx).MachineID
	if launcher == "" {
		launcher = s.launcherNow(ctx)
	}
	if _, ok := s.device(ctx, machine); !ok {
		return
	}
	cwd, err := s.prepare(ctx, machine)
	if err != nil {
		return
	}
	params := map[string]any{"cwd": cwd, "launcher": launcher}
	if err := s.applyModel(ctx, "assistant", params); err != nil {
		s.logf("warm model: %v", err)
		return
	}
	if err := s.call(ctx, machine, DeviceAssistantWarm, params, nil); err != nil {
		s.logf("warm assistant: %v", err)
	}
}

// memory runs one memory operation (node/memory.go) on the assistant's machine.
func (s *Service) memory(ctx context.Context, op map[string]any, out any) error {
	d, ok := s.device(ctx, s.settings(ctx).MachineID)
	if !ok {
		return errNoAssistantMachine
	}
	space, _ := op["space"].(string)
	delete(op, "space")
	if !validSpace(space) {
		return badRequest(fmt.Errorf("space %q: health or none", space))
	}
	cwd := s.spaceCwd(d, space) // the health space keeps a memory of its own (docs/assistant/health.md)
	if cwd == "" {
		return errors.New("assistant machine is offline")
	}
	op["cwd"] = cwd
	return s.call(ctx, d.ID, DeviceAssistantMemory, op, out)
}
