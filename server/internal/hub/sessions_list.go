package hub

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
	"github.com/nwverse/persona/server/internal/sessionorigin"
)

const (
	featureSessionsList       = "sessions_list"
	featureSessionsLookup     = "sessions_lookup"
	featureSessionsPaneSource = "sessions_pane_source"
)

// runtimeList is a transport cache of the manager's already-decorated rows. It
// does not derive agent state or store business data. sessionsMu orders updates
// with legacy list pushes, while the index serves readers independently.
type runtimeList struct {
	once     sync.Once
	index    sessionlist.Index
	machines map[string]bool // guarded by sessionsMu
	// unaligned: machines whose last pass had a row not encoded under an id of its own (indexSessionsLocked); guarded
	// by sessionsMu.
	unaligned map[string]bool
}

func (h *Hub) sessionListIndex() *sessionlist.Index {
	h.listCache.once.Do(func() {
		h.sessionsMu.Lock()
		defer h.sessionsMu.Unlock()
		h.listCache.machines = map[string]bool{}
		for _, m := range h.machinesNow() {
			list := h.shown(m.cfg.ID, h.decorated(m))
			h.indexSessionsLocked(m.cfg.ID, list, encodeSessions(list))
		}
	})
	return &h.listCache.index
}

// indexSessionsLocked consumes the same encoded whole rows used by the legacy
// push path. Bounded rows add the scalar origin; summaries encode once per update.
// A row whose encoded bytes the index already holds keeps its indexed payloads
// (indexedRow): usually one row of a list changed since the last update.
func (h *Hub) indexSessionsLocked(mid string, list []proto.Session, encoded []sentSession) {
	if h.listCache.machines == nil {
		return // no paginated reader yet; legacy users pay no indexing overhead
	}
	full := make(map[string]json.RawMessage, len(encoded))
	for _, row := range encoded {
		full[row.id] = row.raw
	}
	// A row's summary is read from the row and its full row from the bytes under its id: the same session only while
	// every row encoded under an id of its own. A duplicate id takes the last one's bytes and the first one's summary,
	// so after a pass that is not so, and in the pass after it, every row is computed again.
	aligned := len(encoded) == len(list) && len(full) == len(encoded)
	reuse := aligned && !h.listCache.unaligned[mid]
	if aligned {
		delete(h.listCache.unaligned, mid)
	} else {
		if h.listCache.unaligned == nil {
			h.listCache.unaligned = map[string]bool{}
		}
		h.listCache.unaligned[mid] = true
	}
	rows := make([]int, 0, len(list))
	ids := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i := range list {
		sid := list[i].SessionID
		if sid == "" || seen[sid] || len(full[sid]) == 0 {
			continue
		}
		seen[sid] = true
		rows = append(rows, i)
		ids = append(ids, runtimeListID(mid, sid))
	}
	var items []sessionlist.Item
	if reuse {
		items = h.listCache.index.Items(ids) // what each row was indexed as, to compare
	} else {
		items = make([]sessionlist.Item, len(ids))
	}
	for k, i := range rows {
		items[k] = indexedRow(mid, ids[k], &list[i], full[list[i].SessionID], items[k])
	}
	h.listCache.index.Replace(mid, items)
	h.listCache.machines[mid] = true
}

// indexedRow is row's item in the paged index, raw its encoding and indexed what the index holds under id (the zero
// Item for none). When indexed's Full is raw with the same pane_source, its Summary is what row's is too: the summary
// is cut from the fields raw encodes. Both are then kept, and the item is unchanged for Replace, rather than
// marshalled again. The scalars are always read from row.
func indexedRow(mid, id string, row *proto.Session, raw json.RawMessage, indexed sessionlist.Item) sessionlist.Item {
	project := row.RepoRoot
	if project == "" {
		project = row.WorkspaceCwd
	}
	if project == "" {
		project = row.Cwd
	}
	group := row.SpaceID
	if group == "" {
		group = row.WorkspaceID
	}
	pane := sessionorigin.Of(*row)
	item := sessionlist.Item{
		ID: id, Machine: mid, Project: project, Group: group, PaneSource: pane,
		State: row.State, BackgroundWaiting: row.BackgroundWaiting, Source: row.SourceKind, Provider: row.Agent, UpdatedAt: row.UpdatedAt,
	}
	text := [...]string{row.Title, row.Cwd, row.RepoRoot, row.SpaceName, row.SlotName, row.AgentName, row.LastText}
	if was, ok := indexed.Full.(json.RawMessage); ok && sameIndexedFull(was, raw, pane) {
		item.Summary, item.Full = indexed.Summary, indexed.Full
		if item.SearchText = indexed.SearchText; !joinedBy(item.SearchText, text[:], '\n') {
			item.SearchText = strings.Join(text[:], "\n")
		}
		return item
	}
	summary, _ := json.Marshal(runtimeSummary(*row))
	item.Summary = json.RawMessage(summary)
	// Reuse the exact legacy encoding, including compatibility keys.
	item.Full = runtimeListFields(raw, struct {
		PaneSource string `json:"pane_source"`
	}{pane})
	item.SearchText = strings.Join(text[:], "\n")
	return item
}

// paneSourceKey is what runtimeListFields puts between a row's fields and its pane_source value.
const paneSourceKey = `,"pane_source":"`

// sameIndexedFull reports whether indexed is runtimeListFields(raw, pane_source) byte for byte, without building it.
// A pane source JSON would escape is never taken as the same.
func sameIndexedFull(indexed, raw []byte, pane string) bool {
	head := raw[:len(raw)-1]
	if len(indexed) != len(head)+len(paneSourceKey)+len(pane)+2 || !plainJSONString(pane) {
		return false
	}
	tail := indexed[len(head):]
	return string(tail[len(paneSourceKey):len(tail)-2]) == pane && string(tail[len(tail)-2:]) == `"}` &&
		string(tail[:len(paneSourceKey)]) == paneSourceKey && bytes.Equal(indexed[:len(head)], head)
}

// plainJSONString: json.Marshal writes s between quotes as it is (printable ASCII that is not escaped).
func plainJSONString(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

// joinedBy reports whether s is strings.Join(parts, string(sep)), without joining them.
func joinedBy(s string, parts []string, sep byte) bool {
	for i, p := range parts {
		if i > 0 {
			if s == "" || s[0] != sep {
				return false
			}
			s = s[1:]
		}
		if !strings.HasPrefix(s, p) {
			return false
		}
		s = s[len(p):]
	}
	return s == ""
}

// The JSON tuple is unambiguous even if a runtime id contains slashes. Clients
// form the same key from machine_id/session_id when applying delta removals.
func runtimeListID(mid, sid string) string {
	if plainJSONString(mid) && plainJSONString(sid) {
		return `["` + mid + `","` + sid + `"]` // json.Marshal's bytes, in one allocation rather than four
	}
	b, _ := json.Marshal([2]string{mid, sid})
	return string(b)
}

// startErrorRow is how much of s's start_error a row keeps: a coded refusal's whole sentence (the manager's, in the user's
// language, its remedy last: #1642), else a summary's.
func startErrorRow(s proto.Session) int {
	if s.StartErrorCode != "" {
		return proto.CodedStartErrorMax
	}
	return 180
}

func rowText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// runtimeSummary deliberately excludes transcript/task arrays and chat-only
// configuration. The normal info/items routes remain the source for chat detail.
type runtimeRowSummary struct {
	proto.Session
	PaneSource       string `json:"pane_source"`
	Waiting          bool   `json:"waiting"`
	BackgroundCount  int    `json:"background_count,omitempty"`
	RunningSubagents int    `json:"running_subagents,omitempty"`
	RunningWorkflows int    `json:"running_workflows,omitempty"`
}

// runtimeListFields joins two encoded objects. Embedding proto.Session in a
// wrapper directly would promote its compatibility MarshalJSON and hide all
// sibling fields. Both objects here are produced by our own JSON encoders.
func runtimeListFields(session json.RawMessage, fields any) json.RawMessage {
	extra, _ := json.Marshal(fields)
	out := make([]byte, 0, len(session)+len(extra))
	out = append(out, session[:len(session)-1]...)
	out = append(out, ',')
	out = append(out, extra[1:]...)
	return out
}

func (s runtimeRowSummary) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(s.Session)
	if err != nil {
		return nil, err
	}
	return runtimeListFields(raw, struct {
		PaneSource       string `json:"pane_source"`
		Waiting          bool   `json:"waiting"`
		BackgroundCount  int    `json:"background_count,omitempty"`
		RunningSubagents int    `json:"running_subagents,omitempty"`
		RunningWorkflows int    `json:"running_workflows,omitempty"`
	}{s.PaneSource, s.Waiting, s.BackgroundCount, s.RunningSubagents, s.RunningWorkflows}), nil
}

// MarshalJSONTo shadows the promoted (*proto.Session).MarshalJSONTo, which encoding/json prefers for an addressable
// summary (a pointer, a slice element) and which would drop the fields beside the session.
func (s *runtimeRowSummary) MarshalJSONTo(enc *jsontext.Encoder) error {
	b, err := s.MarshalJSON()
	if err != nil {
		return err
	}
	return enc.WriteValue(b)
}

func runtimeSummary(s proto.Session) runtimeRowSummary {
	out := proto.Session{
		Manager: s.Manager, MachineID: s.MachineID, SessionID: s.SessionID,
		Title: rowText(s.Title, 256), Cwd: s.Cwd, WorkspaceCwd: s.WorkspaceCwd, RepoRoot: s.RepoRoot, Worktree: s.Worktree, Branch: s.Branch,
		Status: s.Status, State: s.State, UpdatedAt: s.UpdatedAt, StateSince: s.StateSince, SeenAt: s.SeenAt,
		LastText: rowText(s.LastText, 180), Activity: rowText(s.Activity, 120), ActivityAt: s.ActivityAt,
		AwaitsReply: s.AwaitsReply, BackgroundWorking: s.BackgroundWorking, BackgroundWaiting: s.BackgroundWaiting,
		Agent: s.Agent, AgentLabel: s.AgentLabel, AgentName: s.AgentName, AgentSurface: s.AgentSurface,
		SourceKind: s.SourceKind, SourceThreadID: s.SourceThreadID, SourceInstanceID: s.SourceInstanceID,
		ParentThreadID: s.ParentThreadID, ParentSessionID: s.ParentSessionID, RootSessionID: s.RootSessionID, IsSubagent: s.IsSubagent,
		Capabilities: s.Capabilities, Attached: s.Attached, CanFork: s.CanFork,
		SpaceKind: s.SpaceKind, SpaceID: s.SpaceID, SpaceName: s.SpaceName, SpaceIndex: s.SpaceIndex,
		ParentSpaceID: s.ParentSpaceID, SlotID: s.SlotID, SlotName: s.SlotName, SlotIndex: s.SlotIndex, PaneIndex: s.PaneIndex,
		WorkspaceID: s.WorkspaceID, TabID: s.TabID, PaneID: s.PaneID,
		Assistant: s.Assistant, Archived: s.Archived, ArchiveChangedAt: s.ArchiveChangedAt, Space: s.Space,
		Starting: s.Starting, StartError: rowText(s.StartError, startErrorRow(s)), StartedByPersona: s.StartedByPersona,
		StartErrorCode: s.StartErrorCode, StartErrorArgs: s.StartErrorArgs,
		ReadOnlyReason: rowText(s.ReadOnlyReason, 180), Origin: s.Origin, OriginRef: s.OriginRef, OpenedBy: s.OpenedBy,
		TurnError: rowText(s.TurnError, 180), TurnErrorKind: s.TurnErrorKind, TurnErrorCode: s.TurnErrorCode,
		TurnErrorResetsAt: s.TurnErrorResetsAt, TurnErrorResumes: s.TurnErrorResumes,
		PendingInteractions: s.PendingInteractions, Rev: s.Rev, Quiet: s.Quiet,
		Model: s.Model, ContextTokens: s.ContextTokens, ContextWindow: s.ContextWindow,
	}
	if s.Work != nil {
		out.Work = &proto.WorkState{Phase: s.Work.Phase, On: s.Work.On, Line: rowText(s.Work.Line, 180),
			Wait: rowText(s.Work.Wait, 180), Then: rowText(s.Work.Then, 180), WakeAt: s.Work.WakeAt, ResetsAt: s.Work.ResetsAt,
			Attempts: s.Work.Attempts, Max: s.Work.Max, Deferred: s.Work.Deferred, Agent: s.Work.Agent}
	}
	if s.Todo != nil {
		out.Todo = &proto.TodoRef{ID: s.Todo.ID, Title: rowText(s.Todo.Title, 256)}
	}
	if s.Plan != nil {
		cp := *s.Plan
		cp.Current = rowText(cp.Current, 180)
		out.Plan = &cp
	}
	if s.Goal != nil {
		cp := *s.Goal
		cp.Objective = rowText(cp.Objective, 180)
		cp.Reason = rowText(cp.Reason, 180)
		out.Goal = &cp
	}
	if s.Loop != nil {
		cp := *s.Loop
		cp.Prompt = ""
		cp.Reason = rowText(cp.Reason, 120)
		out.Loop = &cp
	}
	out.Host = s.Host
	row := runtimeRowSummary{Session: out, PaneSource: sessionorigin.Of(s), BackgroundCount: len(s.Background)}
	for _, sub := range s.Subagents {
		if sub.Running {
			row.RunningSubagents++
		}
	}
	for _, flow := range s.Workflows {
		if flow.Running {
			row.RunningWorkflows++
		}
	}
	row.Waiting = s.Loop != nil || row.BackgroundCount > 0 || row.RunningSubagents > 0 || row.RunningWorkflows > 0 ||
		(s.Work != nil && s.Work.Phase == "blocked" && s.Work.On != "user" &&
			(s.Work.WakeAt != nil || (s.Work.On == "external" && s.Work.Wait == "" && s.Work.Agent == nil)))
	return row
}

func (h *Hub) pagedSessions(w http.ResponseWriter, r *http.Request) {
	opts, err := sessionlist.Parse(r.URL.Query())
	if err != nil {
		sessionListErr(w, err)
		return
	}
	if values, lookup := r.URL.Query()["session_id"]; lookup {
		if len(values) != 1 || values[0] == "" || opts.Machine == "" || opts.Since != "" || opts.Cursor != "" {
			sessionListErr(w, errors.New("exact session lookup requires one nonempty session_id and machine_id, without since or cursor"))
			return
		}
		opts.ID = runtimeListID(opts.Machine, values[0])
		// Match the legacy machine-specific ownership check before initializing
		// the index; missing/foreign machines must not weaken token boundaries.
		if em := extensionManager(r); em != "" && h.machineOwner(opts.Machine) != em {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
	}
	index := h.sessionListIndex()
	// Machine disappearance does not always carry a final sessions event.
	h.pruneSessionListMachines()
	// An extension's token: its manager's machines only (ownership.go), as if it had asked for those.
	own, limited := h.extensionMachines(r)
	if limited {
		if opts.Machine != "" && !slices.Contains(own, opts.Machine) {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		if opts.Machine == "" && len(own) == 1 {
			opts.Machine = own[0]
		}
		limited = opts.Machine == ""
	}
	if limited && opts.PaneSource != "" {
		// The legacy ownership projection recomputes totals outside the frozen view.
		// Require one authorized machine for this stricter origin contract.
		sessionListErr(w, errors.New("pane_source requires machine_id for a manager-scoped aggregate"))
		return
	}
	if counts := r.URL.Query().Get("counts"); counts == "1" || counts == "true" {
		var counts sessionlist.Counts
		var err error
		if limited {
			counts, err = ownCounts(index, opts, own)
		} else {
			counts, err = index.Counts(opts)
		}
		if err != nil {
			sessionListErr(w, err)
			return
		}
		writeJSON(w, counts)
		return
	}
	page, err := index.Query(opts)
	if err != nil {
		sessionListErr(w, err)
		return
	}
	if limited {
		if page, err = ownPage(index, opts, own, page); err != nil {
			sessionListErr(w, err)
			return
		}
	}
	writeJSON(w, page)
}

// extensionMachines are the machines of the manager whose extension's token r carries, and whether that limits what
// it is shown: not for any other credential, nor while every machine is that manager's (one manager: as before).
func (h *Hub) extensionMachines(r *http.Request) (own []string, limited bool) {
	em := extensionManager(r)
	if em == "" {
		return nil, false
	}
	for _, m := range h.machinesNow() {
		if h.machineOwner(m.cfg.ID) == em {
			own = append(own, m.cfg.ID)
		} else {
			limited = true
		}
	}
	return own, limited
}

// ownCounts are the counts of opts over the machines own only (each machine's, added up).
func ownCounts(index *sessionlist.Index, opts sessionlist.Options, own []string) (sessionlist.Counts, error) {
	out := sessionlist.Counts{Machine: map[string]int{}, Project: map[string]int{}, Group: map[string]int{}, State: map[string]int{}}
	for _, mid := range own {
		o := opts
		o.Machine, o.Cursor = mid, "" // current membership (a cursor's frozen view is of every machine)
		c, err := index.Counts(o)
		if err != nil {
			return sessionlist.Counts{}, err
		}
		out.Total += c.Total
		out.Revision = c.Revision
		for dst, src := range map[*map[string]int]map[string]int{&out.Machine: c.Machine, &out.Project: c.Project, &out.Group: c.Group, &out.State: c.State} {
			for k, n := range src {
				(*dst)[k] += n
			}
		}
	}
	return out, nil
}

// ownPage is page without the sessions and removals of machines not in own; its total is own's.
func ownPage(index *sessionlist.Index, opts sessionlist.Options, own []string, page sessionlist.Page) (sessionlist.Page, error) {
	keep := func(mid string) bool { return slices.Contains(own, mid) }
	page.Sessions = slices.DeleteFunc(page.Sessions, func(v any) bool {
		raw, _ := json.Marshal(v)
		var s struct {
			MachineID string `json:"machine_id"`
		}
		return json.Unmarshal(raw, &s) != nil || !keep(s.MachineID)
	})
	page.Remove = slices.DeleteFunc(page.Remove, func(id string) bool {
		var k [2]string
		return json.Unmarshal([]byte(id), &k) != nil || !keep(k[0])
	})
	counts, err := ownCounts(index, sessionlist.Options{Since: opts.Since, State: opts.State, Project: opts.Project, Group: opts.Group,
		Source: opts.Source, Provider: opts.Provider, Q: opts.Q}, own)
	if err != nil {
		return sessionlist.Page{}, err
	}
	page.Total = counts.Total
	return page, nil
}

// pruneSessionListMachines scans only machine ids, never session rows. Called
// before machine broadcasts as well as requests, so removals wake list clients.
func (h *Hub) pruneSessionListMachines() bool {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if h.listCache.machines == nil {
		return false
	}
	present := map[string]bool{}
	for _, m := range h.machinesNow() {
		present[m.cfg.ID] = true
	}
	changed := false
	for mid := range h.listCache.machines {
		if !present[mid] {
			h.listCache.index.RemoveSource(mid)
			delete(h.listCache.machines, mid)
			delete(h.listCache.unaligned, mid)
			changed = true
		}
	}
	return changed
}

// reindexSessions refreshes m's paged rows after a change that sends no legacy
// list (sessions.revs): the "revs" broadcast updates whole-list apps, but paged
// readers only ever see what the index holds.
func (h *Hub) reindexSessions(m *Machine) {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if !h.listCache.machines[m.cfg.ID] {
		return // no paginated reader yet, or the machine was pruned
	}
	before := h.listCache.index.Revision()
	shown := h.shown(m.cfg.ID, h.decorated(m))
	h.indexSessionsLocked(m.cfg.ID, shown, encodeSessions(shown))
	revision := h.listCache.index.Revision()
	if revision == before {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.listMode {
			c.sendJSON(map[string]any{"type": "sessions_list", "revision": revision})
		}
	}
}

func (h *Hub) broadcastSessionListNotice() {
	revision := h.listCache.index.Revision()
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.listMode {
			c.sendJSON(map[string]any{"type": "sessions_list", "revision": revision})
		}
	}
}

func sessionListErr(w http.ResponseWriter, err error) {
	code, status := "invalid_query", http.StatusBadRequest
	var typed *sessionlist.Error
	if errors.As(err, &typed) {
		code = typed.Code
		if code == "cursor_expired" || code == "revision_expired" {
			status = http.StatusConflict
		}
	}
	writeErrCode(w, status, code, i18n.Err("session-list."+code))
}

// A paginated subscriber gets no whole list, including on reconnect. The HTTP
// page/revision is authoritative; this event only wakes a delta/count request.
func (h *Hub) sessionListNotice(c *client) {
	c.sendJSON(map[string]any{"type": "sessions_list", "revision": h.sessionListIndex().Revision()})
}
