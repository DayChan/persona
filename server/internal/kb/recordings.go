package kb

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Recordings (docs/assistant/knowledge.md "Recordings", docs/protocol/protocol.md "Recording sync"): the phone uploads each recording's transcript,
// keyed by its UUID, the whole state every time (revision orders them), so replays, reordering and a transcript
// completed later all land on one entry. Only text: the audio stays on the phone. A directory per recording:
// meta.json, segments.json, transcript.md (what the searches read), summary.md (the model's, once it is complete).
// Uploads journal the accepted files until all replacements commit (recording_commit.go).
// recordings/.deleted.json is the tombstones: a late upload of a deleted one is refused. Kept recordingDays.

// KindRecording is the recordings' kind and directory.
const KindRecording = "recordings"

// DefaultRecordingDays is how long recordings are kept unless the user says otherwise (assistant.recording_days).
const DefaultRecordingDays = 14

// Limits of one upload.
const (
	maxRecordingSegments = 20000
	maxRecordingText     = 4 << 20
)

// Defined in the contract package kbwire (docs/server/architecture.md "Contract packages"); the old names stay.
type RecordingSegment = kbwire.RecordingSegment

// RecordingBookmark is a mark the user set while recording: seconds from the start.
type RecordingBookmark struct {
	At    float64 `json:"at"`
	Label string  `json:"label,omitempty"`
}

// RecordingCalendar is the event of the phone's own calendar a recording belongs to: a hint for its name and the
// minutes' attendees and place (docs/assistant/meeting-minutes.md "Field contract").
type RecordingCalendar struct {
	Title     string   `json:"title"`
	Attendees []string `json:"attendees,omitempty"`
	Location  string   `json:"location,omitempty"`
}

// Recording kinds: a meeting gets minutes (minutes.go); a note (or none) only the summary.
const (
	RecordingNote    = "note"
	RecordingMeeting = "meeting"
)

// Limits of an upload's calendar hint.
const (
	maxCalendarTitle     = 200
	maxCalendarAttendees = 100
	maxCalendarName      = 100
)

// RecordingUpload is PUT /api/v1/recordings/{id}'s body.
type RecordingUpload struct {
	Revision  int64               `json:"revision"`
	StartedAt time.Time           `json:"started_at"`
	EndedAt   *time.Time          `json:"ended_at,omitempty"`
	Duration  float64             `json:"duration"`
	Language  string              `json:"language"`
	Device    string              `json:"device"`
	Title     string              `json:"title"`
	Bookmarks []RecordingBookmark `json:"bookmarks"`
	Segments  []RecordingSegment  `json:"segments"`
	Complete  bool                `json:"complete"`
	Kind      string              `json:"kind"`
	Calendar  *RecordingCalendar  `json:"calendar,omitempty"`
}

// MeetingRef is the knowledge base's calendar item (meetings or events) a recording overlaps.
type MeetingRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Minutes states (meta.json's minutes_state; "" when the recording is not a meeting).
const (
	MinutesPending = "pending"
	MinutesReady   = "ready"
	MinutesFailed  = "failed"
)

// Recording is a recording's meta.json.
type Recording struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`            // the user's title, else the model's, else the start time
	Title     string              `json:"title,omitempty"` // the user's
	Revision  int64               `json:"revision"`
	StartedAt time.Time           `json:"started_at"`
	EndedAt   *time.Time          `json:"ended_at,omitempty"`
	Duration  float64             `json:"duration"`
	Language  string              `json:"language,omitempty"`
	Device    string              `json:"device"`
	Bookmarks []RecordingBookmark `json:"bookmarks,omitempty"`
	Complete  bool                `json:"complete"`
	Segments  int                 `json:"segments"`
	Chars     int                 `json:"chars"`
	Projects  []string            `json:"projects,omitempty"`
	OneLine   string              `json:"one_line,omitempty"`
	AITitle   string              `json:"ai_title,omitempty"`
	// SummarizedRev is the revision summary.md was written from (0: none yet).
	SummarizedRev int64     `json:"summarized_rev,omitempty"`
	Updated       time.Time `json:"updated"`
	// Meeting minutes (minutes.go). Kind and Calendar are the phone's, replaced by every upload; the rest is the hub's.
	Kind     string             `json:"kind,omitempty"`
	Calendar *RecordingCalendar `json:"calendar,omitempty"`
	// Meeting is the overlapping calendar item, matched once per revision (MatchedRev); Items links it ("events/<id>").
	Meeting      *MeetingRef `json:"meeting,omitempty"`
	Items        []string    `json:"items,omitempty"`
	MatchedRev   int64       `json:"matched_rev,omitempty"`
	MinutesState string      `json:"minutes_state,omitempty"`
	MinutesAsked bool        `json:"minutes_asked,omitempty"` // the user asked for minutes: a meeting whatever its kind
	// MinutesRev is the revision minutes.json was made from (0: none, or asked to be made again); MinutesVersion counts
	// the minutes written; MinutesFails the unusable answers for MinutesFailRev (failed after minutesTries).
	MinutesRev     int64 `json:"minutes_rev,omitempty"`
	MinutesVersion int   `json:"minutes_version,omitempty"`
	MinutesFails   int   `json:"minutes_fails,omitempty"`
	MinutesFailRev int64 `json:"minutes_fail_rev,omitempty"`
	// MinutesAsks counts the user's requests: minutes made from before the latest one don't settle it.
	MinutesAsks int `json:"minutes_asks,omitempty"`
	Tagging
}

// RecordingState is a recording as the phone reconciles against it (GET /api/v1/recordings).
type RecordingState struct {
	ID           string    `json:"id"`
	Revision     int64     `json:"revision"`
	Complete     bool      `json:"complete"`
	StartedAt    time.Time `json:"started_at"`
	MinutesState string    `json:"minutes_state,omitempty"`
}

// What PutRecording did.
const (
	RecordingStored  = "stored"
	RecordingStale   = "stale"
	RecordingExpired = "expired"
)

var (
	// ErrRecordingDeleted: the recording was deleted (a tombstone); the phone must not upload it again.
	ErrRecordingDeleted = errors.New("this recording was deleted")
	// ErrBadRecording wraps what is wrong with an upload.
	ErrBadRecording = errors.New("invalid recording")
	uuidRe          = lazyre.New(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// RecordingID is id as stored (lowercase), false when it is not a UUID.
func RecordingID(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	return id, uuidRe.MatchString(id)
}

func badRecording(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRecording, fmt.Sprintf(f, a...))
}

// recordingDays is days, or the default when unset.
func recordingDays(days int) int {
	if days <= 0 {
		return DefaultRecordingDays
	}
	return days
}

func (s *Store) Recording(id string) (*Recording, bool) {
	var r Recording
	raw, err := s.recordingRead(id, "meta.json")
	if err != nil || raw == nil || json.Unmarshal(raw, &r) != nil {
		return nil, false
	}
	r.persisted = true
	return &r, true
}

// Recordings lists them all (the tombstones file is not one).
func (s *Store) Recordings() []*Recording {
	var out []*Recording
	for _, id := range s.IDs(KindRecording) {
		if strings.HasPrefix(id, ".") {
			continue
		}
		if r, ok := s.Recording(id); ok {
			out = append(out, r)
		}
	}
	return out
}

// RecordingSegments reads a recording's segments.
func (s *Store) RecordingSegments(id string) []RecordingSegment {
	var segs []RecordingSegment
	raw, err := s.recordingRead(id, "segments.json")
	if err == nil {
		_ = json.Unmarshal(raw, &segs)
	}
	return segs
}

func (s *Store) tombstonesPath() string { return filepath.Join(s.Dir, KindRecording, ".deleted.json") }

func (s *Store) tombstones() map[string]time.Time {
	m := map[string]time.Time{}
	_, _ = readJSON(s.tombstonesPath(), &m)
	return m
}

// recordingName is what a recording is called in lists: the user's title, the phone calendar's, the matched calendar
// item's, the model's, else when it started.
func (s *Store) recordingName(r *Recording) string {
	switch {
	case r.Title != "":
		return r.Title
	case r.Calendar != nil && r.Calendar.Title != "":
		return r.Calendar.Title
	case r.Meeting != nil && r.Meeting.Name != "":
		return r.Meeting.Name
	case r.AITitle != "":
		return r.AITitle
	}
	return "录音 " + r.StartedAt.In(s.Zone()).Format("2006-01-02 15:04")
}

// clean checks an upload and puts its segments in order (empty ones dropped).
func (u *RecordingUpload) clean() error {
	if u.Revision <= 0 {
		return badRecording("revision must be > 0")
	}
	if u.StartedAt.IsZero() {
		return badRecording("started_at is required")
	}
	if u.Device != "phone" && u.Device != "watch" {
		return badRecording("device must be phone or watch")
	}
	if len(u.Segments) > maxRecordingSegments {
		return badRecording("at most %d segments", maxRecordingSegments)
	}
	segs := u.Segments[:0]
	total := 0
	for _, sg := range u.Segments {
		sg.Text = strings.TrimSpace(sg.Text)
		if sg.Text == "" {
			continue
		}
		total += len(sg.Text)
		segs = append(segs, sg)
	}
	if total > maxRecordingText {
		return badRecording("the transcript is over %d bytes", maxRecordingText)
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	u.Segments = segs
	sort.SliceStable(u.Bookmarks, func(i, j int) bool { return u.Bookmarks[i].At < u.Bookmarks[j].At })
	u.Title = oneLine(u.Title)
	u.Language = strings.TrimSpace(u.Language)
	switch u.Kind = strings.ToLower(strings.TrimSpace(u.Kind)); u.Kind {
	case "", RecordingNote:
		u.Kind = RecordingNote
	case RecordingMeeting:
	default:
		return badRecording("kind must be meeting or note")
	}
	return u.cleanCalendar()
}

// cleanCalendar trims the calendar hint, dropping it when it says nothing.
func (u *RecordingUpload) cleanCalendar() error {
	c := u.Calendar
	if c == nil {
		return nil
	}
	c.Title, c.Location = strings.Join(strings.Fields(c.Title), " "), strings.Join(strings.Fields(c.Location), " ")
	if len([]rune(c.Title)) > maxCalendarTitle || len([]rune(c.Location)) > maxCalendarTitle {
		return badRecording("calendar title and location are at most %d characters", maxCalendarTitle)
	}
	if len(c.Attendees) > maxCalendarAttendees {
		return badRecording("at most %d calendar attendees", maxCalendarAttendees)
	}
	names := c.Attendees[:0]
	for _, n := range c.Attendees {
		n = strings.Join(strings.Fields(n), " ")
		if len([]rune(n)) > maxCalendarName {
			return badRecording("a calendar attendee is at most %d characters", maxCalendarName)
		}
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	c.Attendees = names
	if c.Title == "" && c.Location == "" && len(c.Attendees) == 0 {
		u.Calendar = nil
	}
	return nil
}

// PutRecording stores an upload (the whole state of recording id): RecordingStale when a newer revision is there,
// RecordingExpired when it started before the retention, ErrRecordingDeleted after a delete.
func (e *Engine) PutRecording(rawID string, u RecordingUpload, now time.Time) (string, error) {
	if e.Store.ReadOnly() {
		return "", ErrReadOnly
	}
	id, ok := RecordingID(rawID)
	if !ok {
		return "", badRecording("id must be a UUID")
	}
	if err := u.clean(); err != nil {
		return "", err
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if _, gone := st.tombstones()[id]; gone {
		return "", ErrRecordingDeleted
	}
	if u.StartedAt.Before(now.UTC().AddDate(0, 0, -recordingDays(e.RecordingDays()))) {
		return RecordingExpired, nil
	}
	r, exists := st.Recording(id)
	if exists && r.Revision > u.Revision {
		return RecordingStale, nil
	}
	if !exists {
		r = &Recording{ID: id}
	}
	r.Revision, r.StartedAt, r.EndedAt, r.Duration = u.Revision, u.StartedAt.UTC(), u.EndedAt, u.Duration
	if r.EndedAt != nil {
		t := r.EndedAt.UTC()
		r.EndedAt = &t
	}
	r.Language, r.Device, r.Title, r.Bookmarks, r.Complete = u.Language, u.Device, u.Title, u.Bookmarks, u.Complete
	r.Kind, r.Calendar = u.Kind, u.Calendar
	r.planMinutes()
	r.Segments, r.Chars = len(u.Segments), 0
	for _, sg := range u.Segments {
		r.Chars += len([]rune(sg.Text))
	}
	r.Name = st.recordingName(r)
	r.Updated = now.UTC()
	if err := st.commitRecording(r, u.Segments); err != nil {
		return "", err
	}
	st.notify(KindRecording, id, "metadata")
	st.notify(KindRecording, id, "messages")
	e.index(KindRecording, id, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	return RecordingStored, nil
}

func nonNilSegments(s []RecordingSegment) []RecordingSegment {
	if s == nil {
		return []RecordingSegment{}
	}
	return s
}

// renderTranscript is transcript.md: a line per segment (and bookmark) at its clock time in the user's zone.
func (s *Store) renderTranscript(r *Recording, segs []RecordingSegment) string {
	zone := s.Zone()
	at := func(sec float64) string {
		return r.StartedAt.Add(time.Duration(sec * float64(time.Second))).In(zone).Format("15:04:05")
	}
	type line struct {
		sec  float64
		text string
	}
	var lines []line
	for _, sg := range segs {
		lines = append(lines, line{sg.Start, "- " + at(sg.Start) + " " + strings.Join(strings.Fields(sg.Text), " ")})
	}
	for _, b := range r.Bookmarks {
		lines = append(lines, line{b.At, "- " + at(b.At) + " [书签] " + oneLine(b.Label)})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].sec < lines[j].sec })
	var b strings.Builder
	b.WriteString("# 录音 · ")
	if t := cmp.Or(r.Title, r.AITitle); t != "" {
		b.WriteString(t + " · ")
	}
	fmt.Fprintf(&b, "%s · %s\n\n", r.StartedAt.In(zone).Format("2006-01-02 15:04"), zone)
	for _, l := range lines {
		b.WriteString(strings.TrimRight(l.text, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// DeleteRecording removes recording id (files and index) and leaves a tombstone; false when there was none.
func (e *Engine) DeleteRecording(rawID string, now time.Time) (bool, error) {
	if e.Store.ReadOnly() {
		return false, ErrReadOnly
	}
	id, ok := RecordingID(rawID)
	if !ok {
		return false, badRecording("id must be a UUID")
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	t := st.tombstones()
	if _, ok := t[id]; !ok {
		t[id] = now.UTC()
		if err := writeJSON(st.tombstonesPath(), t); err != nil {
			return false, err
		}
	}
	r, existed := st.Recording(id)
	if !existed {
		return false, nil
	}
	st.recFilesMu.Lock()
	err := os.RemoveAll(st.path(KindRecording, id))
	st.recFilesMu.Unlock()
	if err != nil {
		return false, err
	}
	e.unlinkRecording(id, r.Projects)
	e.unindex(KindRecording, []string{id})
	st.notify(KindRecording, id, "metadata")
	return true, nil
}

// unlinkRecording takes a gone recording off its projects.
func (e *Engine) unlinkRecording(id string, projects []string) {
	for _, slug := range projects {
		if p, ok := e.Store.Project(slug); ok {
			p.Items = removeStrings(p.Items, []string{KindRecording + "/" + id})
			_ = e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false)
		}
	}
}

// RecordingStates is every recording kept and every tombstone, for the phone to reconcile.
func (e *Engine) RecordingStates() (kept []RecordingState, deleted []string) {
	kept, deleted = []RecordingState{}, []string{}
	for _, r := range e.Store.Recordings() {
		kept = append(kept, RecordingState{ID: r.ID, Revision: r.Revision, Complete: r.Complete, StartedAt: r.StartedAt, MinutesState: r.MinutesState})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].StartedAt.After(kept[j].StartedAt) })
	for id := range e.Store.tombstones() {
		deleted = append(deleted, id)
	}
	sort.Strings(deleted)
	return kept, deleted
}

// PruneRecordings removes the recordings that started before the retention (files and index), and the tombstones
// older than it; how many recordings went.
func (e *Engine) PruneRecordings(now time.Time) int {
	if e.Store.ReadOnly() {
		return 0
	}
	cut := now.UTC().AddDate(0, 0, -recordingDays(e.RecordingDays()))
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	var gone []string
	for _, r := range st.Recordings() {
		if !r.StartedAt.Before(cut) {
			continue
		}
		st.recFilesMu.Lock()
		err := os.RemoveAll(st.path(KindRecording, r.ID))
		st.recFilesMu.Unlock()
		if err == nil {
			e.unlinkRecording(r.ID, r.Projects)
			st.notify(KindRecording, r.ID, "metadata")
			gone = append(gone, r.ID)
		}
	}
	e.unindex(KindRecording, gone)
	t := st.tombstones()
	n := len(t)
	for id, at := range t {
		if at.Before(cut) {
			delete(t, id)
		}
	}
	if len(t) != n {
		_ = writeJSON(st.tombstonesPath(), t)
	}
	return len(gone)
}

// RecordingDays is the retention in days (Engine.RecordingDaysFunc, the assistant's setting), the default without one.
func (e *Engine) RecordingDays() int {
	if e.RecordingDaysFunc != nil {
		return recordingDays(e.RecordingDaysFunc())
	}
	return DefaultRecordingDays
}

func (e *Engine) unindex(kind string, ids []string) {
	if e.Unindex != nil && len(ids) > 0 {
		e.Unindex(kind, ids)
	}
}

// recordingSummary is the model's answer for a recording (kb/recording).
type recordingSummary struct {
	Title      string     `json:"title"`
	Summary    string     `json:"summary"`
	OneLine    string     `json:"one_line"`
	Importance string     `json:"importance"`
	Relevance  modelScore `json:"relevance"`
	Tags       []string   `json:"tags"`
	Projects   []struct {
		Name   string `json:"name"`
		Update string `json:"update"`
	} `json:"projects"`
}

// recordingPromptChars bounds the transcript the model reads (the start and the end kept).
const recordingPromptChars = 24000

// SummarizeRecordings writes the summary and tags of up to max complete recordings whose latest revision has none
// yet, newest first; how many it did.
func (e *Engine) SummarizeRecordings(ctx context.Context, max int) int {
	if e.Model == nil && e.ModelFor == nil {
		return 0
	}
	var todo []*Recording
	for _, r := range e.Store.Recordings() {
		if r.Complete && r.SummarizedRev < r.Revision && r.Chars > 0 {
			todo = append(todo, r)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].StartedAt.After(todo[j].StartedAt) })
	n := 0
	for _, r := range todo {
		if n >= max || ctx.Err() != nil {
			break
		}
		if err := e.summarizeRecording(ctx, r); err != nil {
			// No model, the model paused or failing: the others would fail alike; they stay due for the next round.
			e.logf("recording %s: %v", r.ID, err)
			break
		}
		n++
	}
	return n
}

func (e *Engine) summarizeRecording(ctx context.Context, r *Recording) error {
	files, err := e.Store.recordingSnapshot(r.ID)
	if err != nil {
		return err
	}
	var snapshot Recording
	if files["meta.json"] == nil {
		return nil
	}
	if err := json.Unmarshal(files["meta.json"], &snapshot); err != nil {
		return err
	}
	r = &snapshot
	if !r.Complete || r.Chars == 0 || r.SummarizedRev >= r.Revision {
		return nil
	}
	var segs []RecordingSegment
	if err := json.Unmarshal(files["segments.json"], &segs); err != nil {
		return err
	}
	zone := e.Store.Zone()
	in := prompts.KBRecordingInput{Pinned: r.Pinned, Title: r.Title, Device: r.Device, Language: r.Language,
		Start: r.StartedAt.In(zone), Minutes: int(r.Duration / 60), Projects: e.knownProjects(), Tags: e.TopTags(40),
		Transcript: clipMiddle(e.Store.renderTranscript(r, segs), recordingPromptChars)}
	var out recordingSummary
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err = e.modelFor("kb.compress").JSONMax(cctx, prompts.RenderKBRecording(prompts.KBOwner{Owner: e.Owner}), prompts.RenderKBRecordingInput(in), 4000, &out)
	cancel()
	if err != nil {
		return err
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	cur, ok := st.Recording(r.ID)
	if !ok {
		return nil // deleted meanwhile
	}
	now := time.Now().UTC()
	if s := strings.TrimSpace(out.Summary); s != "" && !cur.fieldLocked("summary") {
		if err := st.putSummary(KindRecording, cur.ID, s+"\n", false); err != nil {
			return err
		}
	}
	applyTags(&cur.Tagging, out.Importance, int(out.Relevance), out.Tags, now)
	cur.OneLine = oneLine(out.OneLine)
	if t := oneLine(out.Title); t != "" {
		cur.AITitle = t
	}
	cur.Name = st.recordingName(cur)
	// It summarized r's revision: a newer upload meanwhile is summarized again.
	cur.SummarizedRev = r.Revision
	for _, p := range out.Projects {
		pr := e.FindProject(p.Name)
		if pr == nil || !pr.Live() {
			continue
		}
		cur.Projects = addUnique(cur.Projects, pr.Slug)
		pr.Items = addUnique(pr.Items, KindRecording+"/"+cur.ID)
		if u := oneLine(p.Update); u != "" {
			_ = st.AppendLog(pr.Slug, now, "录音 · "+cur.Name, u)
			pr.Dirty = true
		}
		_ = st.putEntity(KindProject, pr.Slug, pr, &pr.Tagging, false)
	}
	if err := st.putEntity(KindRecording, cur.ID, cur, &cur.Tagging, false); err != nil {
		return err
	}
	e.index(KindRecording, cur.ID, cur.Name, cur.Tagging, cur.OneLine, cur.StartedAt)
	return nil
}

// clipMiddle keeps the first and last of s within n runes, marking the cut.
func clipMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	head := n * 2 / 3
	return string(r[:head]) + "\n…（中间省略）…\n" + string(r[len(r)-(n-head):])
}
