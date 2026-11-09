package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

// The workspace board (docs/web/workspace-board.md) is the manager's: its cards bind a computer's code directory, and
// only the manager reaches the computers (devices.go). Its routes are assistantwire's boardRoutes, relayed by the hub
// (503 assistant_offline while the manager is away); its events (workspace_board, workspace_board_settings) go to
// the apps as they are (ws.broadcast).

// A separate store capability keeps older/test stores explicit: board writes never fall back to memory.
type workspaceBoardStore interface {
	WorkspaceBoard(context.Context) (workspaceboard.State, error)
	SaveWorkspaceBoard(context.Context, int64, workspaceboard.State) (workspaceboard.State, error)
	ListWorkspaceSnapshots(context.Context) ([]db.WorkspaceSnapshotRow, error)
	SaveWorkspaceSnapshot(context.Context, string, json.RawMessage) error
}

// boardBook serializes workspace discovery so a slow earlier request cannot overwrite a later snapshot.
type boardBook struct {
	discoverMu sync.Mutex
}

func (s *Service) workspaceBoardRoutes() {
	s.handle("GET /api/v1/workspace-board", s.getWorkspaceBoard)
	s.handle("POST /api/v1/workspace-board", s.mutateWorkspaceBoard)
	s.handle("GET /api/v1/workspaces", s.getWorkspaces)
	s.workspaceBoardSettingsRoutes()
}

func (s *Service) boardStore(w http.ResponseWriter) workspaceBoardStore {
	st, ok := s.store().(workspaceBoardStore)
	if !ok || st == nil {
		apierr.WriteCode(w, 503, "not_supported", i18n.Err("hub-workspace_board.err_no_store"))
		return nil
	}
	return st
}

// boardMachine is a computer as the board needs it: its routable id and whether its node is connected.
type boardMachine struct {
	ID     string
	Online bool
}

// boardMachines are the computers the devices carry.
func (s *Service) boardMachines() []boardMachine {
	if s.devs == nil {
		return nil
	}
	var out []boardMachine
	for _, d := range s.devs.List() {
		out = append(out, boardMachine{ID: d.ID, Online: d.Online})
	}
	return out
}

// boardMachineOf is the computer id names (an id another one absorbed: that one); false when there is none.
func (s *Service) boardMachineOf(id string) (boardMachine, bool) {
	if s.devs == nil || id == "" || !s.devs.Has(id) {
		return boardMachine{}, false
	}
	canon := s.devs.Canonical(id)
	for _, m := range s.boardMachines() {
		if m.ID == canon {
			return m, true
		}
	}
	return boardMachine{}, false
}

func (s *Service) boardMachineOnline(id string) bool {
	m, ok := s.boardMachineOf(id)
	return ok && m.Online
}

func (s *Service) effectiveBoardMachine(id string) string {
	if m, ok := s.boardMachineOf(id); ok {
		return m.ID
	}
	return id
}

// Fleet aliases can be absorbed after a card was bound. Project current routing
// identities without rewriting durable card bindings or merging their workflow state.
func (s *Service) presentBoard(state workspaceboard.State) workspaceboard.State {
	state.Cards = append([]workspaceboard.Card{}, state.Cards...)
	for i := range state.Cards {
		c := &state.Cards[i]
		machine := s.effectiveBoardMachine(c.MachineID)
		if machine != c.MachineID {
			ws := qualifyWorkspace(machine, proto.Workspace{ID: c.WorkspaceID, Source: c.Source, SourceID: c.SourceID, Cwd: c.Cwd})
			c.MachineID, c.WorkspaceID = machine, ws.ID
		}
	}
	return state
}

func (s *Service) writeBoard(w http.ResponseWriter, r *http.Request, state workspaceboard.State) {
	apierr.WriteJSON(w, struct {
		workspaceboard.State
		CanWrite bool `json:"can_write"`
	}{s.presentBoard(state), fullRole(r)})
}

func (s *Service) getWorkspaceBoard(w http.ResponseWriter, r *http.Request) {
	store := s.boardStore(w)
	if store == nil {
		return
	}
	state, err := store.WorkspaceBoard(r.Context())
	if err != nil {
		apierr.Write(w, 503, err)
		return
	}
	s.writeBoard(w, r, state)
}

func (s *Service) mutateWorkspaceBoard(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, 403, i18n.Err("hub-workspace_board.err_readonly"))
		return
	}
	store := s.boardStore(w)
	if store == nil {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &fields) != nil || fields["revision"] == nil || strings.TrimSpace(string(fields["revision"])) == "null" {
		writeBodyErr(w, err, i18n.Err("hub-workspace_board.err_bad_request"))
		return
	}
	var op workspaceboard.Mutation
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&op); err != nil {
		apierr.Write(w, 400, err)
		return
	}
	state, err := store.WorkspaceBoard(r.Context())
	if err != nil {
		apierr.Write(w, 503, err)
		return
	}
	if state.Revision != op.Revision {
		apierr.WriteCode(w, 409, "conflict", db.ErrWorkspaceBoardConflict)
		return
	}
	switch op.Action {
	case "add_card":
		if err := s.resolveBoardCard(r.Context(), &op); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		for _, card := range state.Cards {
			if s.effectiveBoardMachine(card.MachineID) == op.MachineID && workspaceDirectory(card.Cwd) == workspaceDirectory(op.Cwd) {
				apierr.Write(w, 400, i18n.Err("hub-workspace_board.err_already_bound"))
				return
			}
		}
		op.ID = "card_" + randomHex(12)
	case "add_column":
		op.ID = "col_" + randomHex(8)
	}
	next, err := workspaceboard.Apply(state, op)
	if err != nil {
		apierr.Write(w, 400, err)
		return
	}
	saved, err := store.SaveWorkspaceBoard(r.Context(), op.Revision, next)
	if errors.Is(err, db.ErrWorkspaceBoardConflict) {
		apierr.WriteCode(w, 409, "conflict", err)
		return
	}
	if err != nil {
		apierr.Write(w, 503, err)
		return
	}
	s.event(r.Context(), map[string]any{"type": "workspace_board", "revision": saved.Revision})
	s.writeBoard(w, r, saved)
}

// Only the node can resolve a directory on its filesystem; the HTTP caller cannot forge discovery metadata.
func (s *Service) resolveBoardCard(ctx context.Context, op *workspaceboard.Mutation) error {
	m, ok := s.boardMachineOf(op.MachineID)
	if !ok {
		return i18n.Err("hub-workspace_board.err_unknown_machine")
	}
	if !s.boardMachineOnline(m.ID) {
		return i18n.Err("hub-workspace_board.err_offline")
	}
	op.MachineID = m.ID
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var selected *proto.Workspace
	if op.WorkspaceID != "" {
		sessions, _ := s.boardSessions(m.ID)
		for _, session := range sessions {
			candidate := workspaceForSession(m.ID, session)
			if candidate.Cwd != "" && candidate.ID == op.WorkspaceID {
				selected = &candidate
				break
			}
		}
		if selected == nil {
			var found proto.WorkspacesResponse
			if err := s.nodeCall(ctx, m.ID, "workspaces.list", map[string]any{}, &found); err != nil {
				return i18n.Err("hub-workspace_board.err_list_failed", i18n.Args{"error": err.Error()})
			}
			for _, item := range found.Workspaces {
				candidate := qualifyWorkspace(m.ID, item)
				if candidate.ID == op.WorkspaceID {
					selected = &candidate
					break
				}
			}
		}
		if selected == nil {
			return i18n.Err("hub-workspace_board.err_changed")
		}
		if selected.Cwd == "" {
			return i18n.Err("hub-workspace_board.err_no_directory")
		}
		op.Cwd = selected.Cwd
	}

	if strings.TrimSpace(op.Cwd) == "" {
		return i18n.Err("hub-workspace_board.err_pick_directory")
	}
	var resolved proto.Workspace
	if err := s.nodeCall(ctx, m.ID, "workspace.resolve", map[string]any{"path": op.Cwd}, &resolved); err != nil {
		return i18n.Err("hub-workspace_board.err_resolve_failed", i18n.Args{"error": err.Error()})
	}
	if resolved.Cwd == "" || !resolved.Available {
		return i18n.Err("hub-workspace_board.err_resolve_empty")
	}
	if selected != nil {
		op.Source, op.SourceID, op.Branch = selected.Source, selected.SourceID, selected.Branch
		op.WorkspaceID = selected.ID
	} else {
		resolved = qualifyWorkspace(m.ID, resolved)
		op.Source, op.SourceID, op.Branch = resolved.Source, resolved.SourceID, resolved.Branch
		op.WorkspaceID = resolved.ID
	}
	op.Cwd = resolved.Cwd
	if op.Title == nil || strings.TrimSpace(*op.Title) == "" {
		title := resolved.Title
		if selected != nil {
			title = selected.Title
		}
		op.Title = &title
	}
	return nil
}

func workspaceDirectory(p string) string {
	if canonical, err := workspaceboard.CanonicalCwd(p); err == nil {
		return canonical
	}
	return p
}

func qualifyWorkspace(machine string, w proto.Workspace) proto.Workspace {
	w.Cwd = workspaceDirectory(w.Cwd)
	if w.Source == "" {
		w.Source = "workspace"
	}
	if w.SourceID == "" {
		w.SourceID = w.ID
	}
	if w.SourceID == "" {
		w.SourceID = w.Cwd
	}
	sum := sha256.Sum256([]byte(machine + "\x00" + w.Source + "\x00" + w.SourceID))
	w.ID, w.MachineID = "ws_"+hex.EncodeToString(sum[:12]), machine
	return w
}

func sessionBoardDirectory(s proto.Session) string {
	if s.WorkspaceCwd != "" {
		return workspaceDirectory(s.WorkspaceCwd)
	}
	return workspaceDirectory(s.Cwd)
}

// boardSessions are, of a machine's sessions, the first to name each directory (WorkspaceCwd, else Cwd), in list order,
// and how many its node listed. The board reads a directory's first session only: a later one in the same directory
// is either taken as that first one was or skipped, since sessionBoardDirectory canonicalizes a path to one it leaves
// as it is. So a machine listing thousands of sessions in a few directories copies a few.
func (s *Service) boardSessions(machine string) ([]proto.Session, int) {
	named := map[string]bool{}
	return s.nodeSessionsWhere(machine, func(sess *proto.Session) bool {
		dir := cmp.Or(sess.WorkspaceCwd, sess.Cwd)
		if dir == "" || named[dir] {
			return false // none (sessionBoardDirectory ""), or one already read
		}
		named[dir] = true
		return true
	})
}

func workspaceForSession(machine string, s proto.Session) proto.Workspace {
	title := s.SpaceName
	if title == "" {
		title = s.Cwd
	}
	cwd := sessionBoardDirectory(s)
	return qualifyWorkspace(machine, proto.Workspace{Source: "session-directory", SourceID: cwd, Title: title, Cwd: cwd})
}

type workspaceDiscoveryError struct {
	MachineID string `json:"machine_id"`
	Error     string `json:"error"`
}

func (s *Service) getWorkspaces(w http.ResponseWriter, r *http.Request) {
	l := s.requestLang(r)
	store := s.boardStore(w)
	if store == nil {
		return
	}
	// Serialize discovery so a slow earlier request cannot overwrite a later snapshot in this hub.
	s.board.discoverMu.Lock()
	defer s.board.discoverMu.Unlock()
	cached, err := store.ListWorkspaceSnapshots(r.Context())
	if err != nil {
		apierr.Write(w, 503, err)
		return
	}
	byMachine := map[string]db.WorkspaceSnapshotRow{}
	for _, row := range cached {
		machine := s.effectiveBoardMachine(row.MachineID)
		if previous, ok := byMachine[machine]; ok && !row.UpdatedAt.After(previous.UpdatedAt) {
			continue
		}
		var entries []proto.Workspace
		if json.Unmarshal(row.Data, &entries) == nil {
			for i := range entries {
				entries[i] = qualifyWorkspace(machine, entries[i])
			}
			row.Data, _ = json.Marshal(entries)
		}
		byMachine[machine] = row
	}
	state, err := store.WorkspaceBoard(r.Context())
	if err != nil {
		apierr.Write(w, 503, err)
		return
	}
	state = s.presentBoard(state)
	type discovered struct {
		machine      boardMachine
		workspaces   []proto.Workspace
		err          error
		at           time.Time
		directories  map[string]bool
		sourceErrors []proto.WorkspaceSourceError
	}
	machines := s.boardMachines()
	results := make(chan discovered, len(machines))
	for _, m := range machines {
		go func(m boardMachine) {
			v := discovered{machine: m, at: time.Now().UTC()}
			if !s.boardMachineOnline(m.ID) {
				results <- v
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			var response proto.WorkspacesResponse
			v.err = s.nodeCall(ctx, m.ID, "workspaces.list", map[string]any{}, &response)
			v.sourceErrors = response.Errors
			if v.err == nil {
				v.workspaces = make([]proto.Workspace, 0, len(response.Workspaces))
				for _, item := range response.Workspaces {
					item = qualifyWorkspace(m.ID, item)
					item.Online = true
					item.LastSeen = v.at.Format(time.RFC3339Nano)
					v.workspaces = append(v.workspaces, item)
				}
			}
			var paths []string
			for _, c := range state.Cards {
				if c.MachineID == m.ID {
					paths = append(paths, c.Cwd)
				}
			}
			v.directories = map[string]bool{}
			for start := 0; start < len(paths); start += 200 {
				end := min(start+200, len(paths))
				var stats struct {
					Stats []struct {
						Path string `json:"path"`
						Kind string `json:"kind"`
					} `json:"stats"`
				}
				if err := s.nodeCall(ctx, m.ID, "fs.stat", map[string]any{"paths": paths[start:end]}, &stats); err != nil {
					break
				}
				for _, st := range stats.Stats {
					v.directories[st.Path] = st.Kind == "dir"
				}
			}
			results <- v
		}(m)
	}
	out := []proto.Workspace{}
	errs := []workspaceDiscoveryError{}
	known := map[string]bool{}
	for range machines {
		result := <-results
		m := result.machine
		online := s.boardMachineOnline(m.ID)
		entries := result.workspaces
		if entries == nil {
			if old, ok := byMachine[m.ID]; ok {
				if err := json.Unmarshal(old.Data, &entries); err != nil {
					errs = append(errs, workspaceDiscoveryError{m.ID, i18n.T(l, "hub-workspace_board.err_snapshot_read")})
				}
			}
		}
		if len(result.sourceErrors) > 0 {
			failed := map[string]bool{}
			present := map[string]bool{}
			for _, item := range entries {
				present[item.ID] = true
			}
			for _, failure := range result.sourceErrors {
				failed[failure.Source] = true
				errs = append(errs, workspaceDiscoveryError{m.ID, failure.Source + ": " + failure.Error})
			}
			if old, ok := byMachine[m.ID]; ok {
				var previous []proto.Workspace
				if json.Unmarshal(old.Data, &previous) == nil {
					for _, item := range previous {
						if failed[item.Source] && !present[item.ID] {
							item.Available = false
							entries = append(entries, item)
						}
					}
				}
			}
		}
		for i := range entries {
			entries[i] = qualifyWorkspace(m.ID, entries[i])
			entries[i].Online = online
			if result.err != nil || !online {
				entries[i].Available = false
			}
		}
		if result.err != nil {
			errs = append(errs, workspaceDiscoveryError{m.ID, i18n.Localize(result.err, l)})
		}
		sessions, listed := s.boardSessions(m.ID)
		containsDirectory := func(cwd string) bool {
			for _, item := range entries {
				if workspaceDirectory(item.Cwd) == workspaceDirectory(cwd) {
					return true
				}
			}
			return false
		}
		for _, session := range sessions {
			if sessionBoardDirectory(session) == "" || containsDirectory(sessionBoardDirectory(session)) {
				continue
			}
			item := workspaceForSession(m.ID, session)
			item.Online, item.Available = online, online
			if online {
				item.LastSeen = result.at.Format(time.RFC3339Nano)
			}
			entries = append(entries, item)
		}
		for _, card := range state.Cards {
			if card.MachineID != m.ID || containsDirectory(card.Cwd) {
				continue
			}
			entries = append(entries, proto.Workspace{ID: card.WorkspaceID, MachineID: card.MachineID, Source: card.Source, SourceID: card.SourceID, Title: card.Title, Cwd: card.Cwd, Branch: card.Branch, Online: online})
		}
		for i := range entries {
			for _, card := range state.Cards {
				if card.MachineID == m.ID && workspaceDirectory(card.Cwd) == entries[i].Cwd {
					entries[i].Available = online && result.directories[card.Cwd]
					if entries[i].Available {
						entries[i].LastSeen = result.at.Format(time.RFC3339Nano)
					}
					break
				}
			}
		}
		// Persist the merged directory catalog, not only spaces that still have a host terminal.
		if online && (result.err == nil || listed > 0) {
			raw, _ := json.Marshal(entries)
			if err := store.SaveWorkspaceSnapshot(r.Context(), m.ID, raw); err != nil {
				errs = append(errs, workspaceDiscoveryError{m.ID, i18n.T(l, "hub-workspace_board.err_snapshot_save")})
			}
		}
		for _, item := range entries {
			if !known[item.ID] {
				known[item.ID] = true
				out = append(out, item)
			}
		}
	}
	for _, card := range state.Cards {
		found := false
		for _, item := range out {
			if item.MachineID == card.MachineID && workspaceDirectory(item.Cwd) == card.Cwd {
				found = true
				break
			}
		}
		if !found {
			out = append(out, proto.Workspace{ID: card.WorkspaceID, MachineID: card.MachineID, Source: card.Source, SourceID: card.SourceID, Title: card.Title, Cwd: card.Cwd, Branch: card.Branch})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MachineID != out[j].MachineID {
			return out[i].MachineID < out[j].MachineID
		}
		return out[i].ID < out[j].ID
	})
	apierr.WriteJSON(w, map[string]any{"workspaces": out, "errors": errs})
}

// requestLang is the language the request asked for (the hub's relay passes Accept-Language on), else the user's.
func (s *Service) requestLang(r *http.Request) i18n.Lang {
	if l := i18n.From(r.Context()); l != "" {
		return l
	}
	return i18n.FromRequest(r).Or(s.lang(r.Context()))
}
