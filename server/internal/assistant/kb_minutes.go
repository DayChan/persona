package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// Meeting minutes of the phone's recordings (kb/minutes.go, docs/assistant/meeting-minutes.md): asking for them, the
// push when they are written, and the to-dos the user confirms from their action items.

// codeMinutesStale: the minutes were written again since the version the app sent; it reads them again.
const codeMinutesStale = "minutes_stale"

// minutesWritten pushes the minutes the knowledge-base loop wrote for a recording's current revision: one push per
// recording and version (a stable id, so a restart does not repeat it), a tap opening the main conversation as
// reminders do, and the same text with the recording's web page to the Lark bot and the channels with pushes on.
func (s *Service) minutesWritten(ctx context.Context, r kb.Recording, m kb.Minutes) {
	l := s.lang(ctx)
	title := i18n.T(l, "assistant-minutes.ready_title")
	body := i18n.T(l, "assistant-minutes.ready_body", i18n.Args{"name": m.Title, "decisions": len(m.Decisions), "actions": len(m.Actions)})
	set := s.settings(ctx)
	n := Notification{ID: "ntf_minutes_" + r.ID + "_" + strconv.Itoa(m.Version), Kind: NotifyPush, Title: title, Body: body,
		DeviceID: set.MachineID, SessionID: set.Main}
	if err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n}); err != nil {
		s.logf("assistant: minutes push: %v", err)
	}
	if link := recordingLink(s.webBase(ctx), r.ID); link != "" {
		body += "\n" + link
	}
	// Not as the main conversation's: a channel holds back its mirror of a conversation it was just pushed for.
	s.channelsPushed("", "", title, body)
}

// recordingLink is a recording's page in the web app at web (web/src/lib/router.ts), "" when web is.
func recordingLink(web, id string) string {
	if web == "" {
		return ""
	}
	return web + "/kb/recordings/" + id
}

func (s *Service) minutesRoutes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	// Make (or make again) a recording's minutes at the knowledge base's next round, which this wakes.
	s.handle("POST /api/v1/recordings/{id}/minutes", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		switch err := e.AskMinutes(r.PathValue("id")); {
		case errors.Is(err, kb.ErrNotFound):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-minutes.no_recording"))
			return
		case errors.Is(err, kb.ErrRecordingIncomplete):
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-minutes.incomplete"))
			return
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		s.kbWake()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"state": kb.MinutesPending})
	})
	// The action items the user confirmed become to-dos.
	s.handle("POST /api/v1/recordings/{id}/minutes/todos", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-minutes.todo_forbidden"))
			return
		}
		e := engine(w, r)
		if e == nil {
			return
		}
		var req struct {
			Version int      `json:"version"`
			Actions []string `json:"actions"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if len(req.Actions) == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-minutes.no_actions"))
			return
		}
		todos, m, err := s.minutesTodos(r.Context(), e, r.PathValue("id"), req.Version, req.Actions)
		var unknown unknownActionError
		switch {
		case errors.Is(err, kb.ErrMinutesStale):
			apierr.WriteCode(w, http.StatusConflict, codeMinutesStale, i18n.Err("assistant-minutes.stale"))
			return
		case errors.As(err, &unknown):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-minutes.unknown_action", i18n.Args{"id": string(unknown)}))
			return
		case errors.Is(err, kb.ErrNotFound):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-minutes.no_minutes"))
			return
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todos": todos, "minutes": m})
	})
}

// unknownActionError names an action id the minutes don't have.
type unknownActionError string

func (u unknownActionError) Error() string { return "no action item " + string(u) }

// minutesTodos makes a to-do of each action of version of recording id's minutes (one that has one already keeps it)
// and records them in the minutes. A to-do's id comes from the recording, the version and the action, so a request
// retried after its answer was lost makes none twice.
func (s *Service) minutesTodos(ctx context.Context, e *kb.Engine, id string, version int, actions []string) ([]Todo, *kb.Minutes, error) {
	s.minutesMu.Lock()
	defer s.minutesMu.Unlock()
	rid, _ := kb.RecordingID(id)
	m, err := e.Store.ReadMinutes(rid)
	if err != nil {
		return nil, nil, err
	}
	if m.Version != version {
		return nil, nil, kb.ErrMinutesStale
	}
	rec, ok := e.Store.Recording(rid)
	if !ok {
		return nil, nil, kb.ErrNotFound
	}
	byID := map[string]kb.MinutesAction{}
	for _, a := range m.Actions {
		byID[a.ID] = a
	}
	for _, a := range actions {
		if _, ok := byID[a]; !ok {
			return nil, nil, unknownActionError(a)
		}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, nil, err
	}
	var out []Todo
	made, byTask := map[string]string{}, map[string]string{}
	note := s.minutesTodoNotes(ctx, rec, m)
	for _, aid := range dedupe(actions) {
		a := byID[aid]
		tid := a.TodoID
		if tid == "" {
			tid = minutesTodoID(rid, version, aid)
		}
		if t, ok := s.todoNow(tid); ok {
			out, made[aid], byTask[kb.MinutesTaskKey(a)] = append(out, t), tid, tid
			continue
		}
		if a.TodoID != "" { // made before and deleted since: the user's choice stands
			continue
		}
		t, err := s.insertTodo(ctx, minutesTodoRow(tid, a, note(a)))
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" { // made a moment ago by another manager on the same store
			t, err = Todo{ID: tid, Title: minutesTodoTitle(a)}, nil
		}
		if err != nil {
			// Those made so far are recorded all the same: a retry after the user deleted one must not make it again.
			if _, serr := e.SetMinutesTodos(rid, version, made, byTask); serr != nil {
				s.logf("assistant: minutes to-dos of %s: %v", rid, serr)
			}
			return out, nil, err
		}
		out, made[aid], byTask[kb.MinutesTaskKey(a)] = append(out, t), tid, tid
	}
	// Minutes written again meanwhile get the to-dos by task, so confirming the same task there makes none twice.
	m, err = e.SetMinutesTodos(rid, version, made, byTask)
	if out == nil {
		out = []Todo{}
	}
	return out, m, err
}

// minutesTodoID is the to-do an action of a version of a recording's minutes becomes.
func minutesTodoID(recording string, version int, action string) string {
	h := sha256.Sum256([]byte(recording + "\x00" + strconv.Itoa(version) + "\x00" + action))
	return "todo_mtg_" + hex.EncodeToString(h[:8])
}

func minutesTodoTitle(a kb.MinutesAction) string {
	if a.Who != "" {
		return a.Who + ": " + a.What
	}
	return a.What
}

// minutesTodoNotes is what a to-do made of an action of minutes m of recording rec says: which meeting, when it was
// said, the deadline as said, the recording's page. The zone, language and web address are read once a request.
func (s *Service) minutesTodoNotes(ctx context.Context, rec *kb.Recording, m *kb.Minutes) func(kb.MinutesAction) string {
	loc, l, link := s.location(ctx), s.lang(ctx), recordingLink(s.webBase(ctx), rec.ID)
	return func(a kb.MinutesAction) string {
		clock := rec.StartedAt.Add(time.Duration(a.At * float64(time.Second))).In(loc).Format("15:04:05")
		notes := i18n.T(l, "assistant-minutes.todo_notes", i18n.Args{"meeting": m.Title, "when": m.When, "clock": clock})
		if a.By != "" {
			notes += "\n" + i18n.T(l, "assistant-minutes.todo_by", i18n.Args{"by": a.By})
		}
		if link != "" {
			notes += "\n" + link
		}
		return notes
	}
}

// minutesTodoRow is the plain to-do (no conversation, no task) action a becomes: due on its date when it has one.
func minutesTodoRow(id string, a kb.MinutesAction, notes string) db.TodoRow {
	now := time.Now().UTC()
	row := db.TodoRow{ID: id, Title: minutesTodoTitle(a), Notes: strings.TrimSpace(notes), CreatedAt: now, UpdatedAt: now}
	if d, err := time.Parse("2006-01-02", a.DueDate); err == nil {
		db.TodoEdit{SetDue: true, DueDate: &d}.Apply(&row, now)
	}
	return row
}

// dedupe is list without repeats, in order.
func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
