package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Folders (docs/assistant/inbox.md "Folders", #2463): each item can be filed under a "/"-separated path
// ("work/alerts/engine"), every segment a folder level, so the inbox can be browsed as a tree. A step of the triage
// pass (fileFolders) asks the small-task model, in batches, where the items without a folder go: it sees the folders
// in use (to reuse them, so the tree stays stable), the user's folder rules and the moves the user made by hand. An
// item the model filed keeps its folder when new messages come in; one the user filed (FolderBy "user", even into no
// folder) is never touched again. Off until the user turns it on (setting inbox_folders): it spends model calls.

// inboxFoldersKey is where the folder settings are kept.
const inboxFoldersKey = "inbox_folders"

// Who filed an item (InboxItem.FolderBy).
const (
	FolderByModel = "model"
	FolderByUser  = "user"
)

// The shape of a folder path (normalizeFolder).
const (
	folderMaxDepth   = 6
	folderMaxSegment = 40  // runes
	folderMaxBytes   = 200 // the whole path
)

// The classifier's budget: items per model call, items per pass, folders and examples it is shown.
const (
	folderBatch    = 20
	folderPerPass  = 60
	folderShown    = 80
	folderExamples = 12
	folderWait     = 90 * time.Second
)

// InboxFolderConfig is the user's folder settings: whether the model files items, and their own folder rules.
type InboxFolderConfig struct {
	Enabled bool   `json:"enabled"`
	Rules   string `json:"rules,omitempty"`
}

// InboxFolder is one folder in use: items filed exactly there (Count) and how many of them are open.
type InboxFolder struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
	Open  int    `json:"open"`
}

var errBadFolder = i18n.ErrEnglish("inbox-errors.bad_folder")

// normalizeFolder is a folder path as it is kept: segments trimmed, inner spaces collapsed, control characters and
// empty / "." / ".." segments dropped, each segment clipped, at most folderMaxDepth levels and folderMaxBytes in all.
// "" is no folder. Case is kept: the user's names stay as typed.
func normalizeFolder(s string) string {
	var segs []string
	size := 0
	for _, seg := range strings.Split(strings.ReplaceAll(s, "\\", "/"), "/") {
		seg = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, seg)
		seg = strings.Join(strings.Fields(seg), " ")
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		if utf8.RuneCountInString(seg) > folderMaxSegment {
			seg = strings.TrimSpace(string([]rune(seg)[:folderMaxSegment]))
		}
		if len(segs) == folderMaxDepth || size+len(seg)+len(segs) > folderMaxBytes {
			break
		}
		segs = append(segs, seg)
		size += len(seg)
	}
	return strings.Join(segs, "/")
}

// loadInboxFolders reads the folder settings; false when the database could not be read.
func (s *Service) loadInboxFolders(ctx context.Context) bool {
	st := s.store()
	if st == nil {
		return true
	}
	var cfg InboxFolderConfig
	if _, err := st.Setting(ctx, inboxFoldersKey, &cfg); err != nil {
		s.logf("inbox: folders: %v", err)
		return false
	}
	b := &s.inbox
	b.mu.Lock()
	b.folderCfg = cfg
	b.mu.Unlock()
	return true
}

// setFolderConfig changes what is given (nil: as it is), keeping it before memory changes; turning folders on starts
// a pass.
func (s *Service) setFolderConfig(ctx context.Context, enabled *bool, rules *string) (InboxFolderConfig, error) {
	b := &s.inbox
	b.folderCfgMu.Lock()
	defer b.folderCfgMu.Unlock()
	b.mu.Lock()
	cfg := b.folderCfg
	b.mu.Unlock()
	was := cfg.Enabled
	if enabled != nil {
		cfg.Enabled = *enabled
	}
	if rules != nil {
		cfg.Rules = strings.TrimSpace(*rules)
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, inboxFoldersKey, cfg); err != nil {
			return cfg, err
		}
	}
	b.mu.Lock()
	b.folderCfg = cfg
	b.mu.Unlock()
	if cfg.Enabled && !was {
		s.fileFoldersSoon(ctx)
	}
	return cfg, nil
}

// folders are the folders the listed items are filed in (connectors' only when all), most used first.
func (b *inboxBook) folders(all bool) []InboxFolder {
	cutoff := time.Now().Add(-inboxKeepDone)
	by := map[string]*InboxFolder{}
	for _, it := range b.items {
		if it.Folder == "" || it.DoneAt != nil && it.DoneAt.Before(cutoff) || !all && it.Connector != nil {
			continue
		}
		f := by[it.Folder]
		if f == nil {
			f = &InboxFolder{Path: it.Folder}
			by[it.Folder] = f
		}
		f.Count++
		if it.open() {
			f.Open++
		}
	}
	out := make([]InboxFolder, 0, len(by))
	for _, f := range by {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// unfiled says the model may be asked where the item goes: no folder, not the user's, not asked about what it holds now.
func (it *InboxItem) unfiled() bool {
	return it.Folder == "" && it.FolderBy == "" && it.FolderOf != it.Last
}

// fileFoldersSoon starts a folder pass in the background when folders are on and one is not running already.
func (s *Service) fileFoldersSoon(ctx context.Context) {
	b := &s.inbox
	b.mu.Lock()
	on := b.folderCfg.Enabled
	b.mu.Unlock()
	if !on || !b.foldering.TryLock() {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer b.foldering.Unlock()
		s.fileFolders(ctx)
	}()
}

// fileFolders asks the model where the listed items without a folder go, newest first and open ones before done
// ones, folderBatch at a time, at most folderPerPass a pass. It stops at the first call that fails (the next pass
// asks again). The caller holds b.foldering.
func (s *Service) fileFolders(ctx context.Context) {
	b := &s.inbox
	b.mu.Lock()
	if !b.folderCfg.Enabled {
		b.mu.Unlock()
		return
	}
	cutoff := time.Now().Add(-inboxKeepDone)
	var todo []*InboxItem
	for _, it := range b.items {
		if it.unfiled() && (it.DoneAt == nil || !it.DoneAt.Before(cutoff)) {
			todo = append(todo, it)
		}
	}
	b.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool {
		if todo[i].open() != todo[j].open() {
			return todo[i].open()
		}
		return todo[i].UpdatedAt.After(todo[j].UpdatedAt)
	})
	if len(todo) > folderPerPass {
		todo = todo[:folderPerPass]
	}
	for len(todo) > 0 && ctx.Err() == nil {
		n := min(folderBatch, len(todo))
		if err := s.fileBatch(ctx, todo[:n]); err != nil {
			s.logf("inbox: folders: %v", err)
			return
		}
		todo = todo[n:]
	}
}

// fileBatch asks the model about one batch and files what it answered; an item it gave no usable folder is marked
// as asked (FolderOf) so the same content is not asked about again.
func (s *Service) fileBatch(ctx context.Context, batch []*InboxItem) error {
	b := &s.inbox
	b.mu.Lock()
	cfg := b.folderCfg
	in := prompts.InboxFoldersInput{Folders: b.folderLines(), Examples: b.folderExamples()}
	asked := make(map[string]string, len(batch))
	for _, it := range batch {
		if !it.unfiled() || b.items[it.ID] != it {
			continue
		}
		asked[it.ID] = it.Last
		in.Items = append(in.Items, prompts.InboxFolderItem{ID: it.ID, Kind: it.Kind, Source: b.sourceName(it.Source), Title: oneLine(it.Title),
			Sender: oneLine(it.Sender), Summary: oneLine(it.Summary), Preview: clip(oneLine(it.Preview), 300)})
	}
	b.mu.Unlock()
	if len(in.Items) == 0 {
		return nil
	}
	hello := s.hello(ctx)
	system := prompts.RenderInboxFolders(prompts.InboxFolders{Owner: hello.Owner, FolderRules: cfg.Rules,
		Rules: s.inboxConfig(ctx).Rules, Language: answerLanguage(hello.Locale), MaxDepth: 4})
	var out struct {
		Folders map[string]string `json:"folders"`
	}
	cctx, cancel := context.WithTimeout(ctx, folderWait)
	err := s.inboxModelJSON(cctx, purposeTriage, system, prompts.RenderInboxFoldersInput(in), 0, &out)
	cancel()
	if err != nil {
		return err
	}
	b.mu.Lock()
	var ws []*inboxWrite
	for _, it := range batch {
		last, ok := asked[it.ID]
		if !ok || b.items[it.ID] != it || !it.unfiled() || it.Last != last {
			continue // gone, filed by the user, or new messages came in meanwhile (the next pass asks again)
		}
		w := beginWrite(it)
		if f := normalizeFolder(out.Folders[it.ID]); f != "" {
			it.Folder, it.FolderBy = f, FolderByModel
		}
		it.FolderOf = last
		b.settle(w)
		ws = append(ws, w)
	}
	b.mu.Unlock()
	s.saveWrites(ctx, ws, "its folder")
	return nil
}

// folderLines are the folders in use as the model is shown them, most used first. Under b.mu.
func (b *inboxBook) folderLines() []prompts.InboxFolderUse {
	all := b.folders(true)
	if len(all) > folderShown {
		all = all[:folderShown]
	}
	out := make([]prompts.InboxFolderUse, len(all))
	for i, f := range all {
		out[i] = prompts.InboxFolderUse{Path: f.Path, Count: f.Count}
	}
	return out
}

// folderExamples are the items the user filed by hand, newest first: the model follows their habits. Under b.mu.
func (b *inboxBook) folderExamples() []prompts.InboxFolderExample {
	var mine []*InboxItem
	for _, it := range b.items {
		if it.FolderBy == FolderByUser && it.Folder != "" {
			mine = append(mine, it)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].UpdatedAt.After(mine[j].UpdatedAt) })
	if len(mine) > folderExamples {
		mine = mine[:folderExamples]
	}
	out := make([]prompts.InboxFolderExample, len(mine))
	for i, it := range mine {
		out[i] = prompts.InboxFolderExample{Title: clip(oneLine(it.Title), 60), Sender: oneLine(it.Sender), Folder: it.Folder}
	}
	return out
}

// setInboxFolder is the user's move: folder files the item there ("" in no folder), nil hands it back to the model.
// A failed write puts the item back as it was.
func (s *Service) setInboxFolder(ctx context.Context, id string, folder *string) (InboxItem, int, error) {
	b := &s.inbox
	var f string
	if folder != nil {
		f = normalizeFolder(*folder)
		if f == "" && strings.TrimSpace(*folder) != "" {
			return InboxItem{}, http.StatusBadRequest, errBadFolder
		}
	}
	b.mu.Lock()
	it, ok := b.items[id]
	if !ok {
		b.mu.Unlock()
		return InboxItem{}, http.StatusNotFound, errNoInboxItem
	}
	w := beginWrite(it)
	if folder != nil {
		it.Folder, it.FolderBy = f, FolderByUser
	} else {
		it.Folder, it.FolderBy, it.FolderOf = "", "", ""
	}
	b.settle(w)
	b.mu.Unlock()
	if failed := s.saveWrites(ctx, []*inboxWrite{w}, "its folder"); failed[id] {
		return InboxItem{}, http.StatusInternalServerError, errInboxNotPersisted
	}
	if folder == nil {
		s.fileFoldersSoon(ctx)
	}
	b.mu.Lock()
	out := it.listed()
	b.mu.Unlock()
	return out, http.StatusOK, nil
}

// ---- the assistant's tools: inbox_folders, inbox_move (prompts/mcp/inbox, #3099) ----

// folderToolItems is how many items inbox_folders lists at most.
const folderToolItems = 50

// toolInboxFolders lists the folders in use (every source's) and, given a folder (that folder and below) or unfiled
// (items in no folder), the items there, open first and then newest first, with the ids inbox_move takes.
func (s *Service) toolInboxFolders(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Folder  string `json:"folder"`
		Unfiled bool   `json:"unfiled"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	f := normalizeFolder(a.Folder)
	if f == "" && strings.TrimSpace(a.Folder) != "" {
		return "", fmt.Errorf("folder %q has no name between the slashes", a.Folder)
	}
	if err := s.inboxReady(ctx); err != nil {
		return "", err
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	var out strings.Builder
	if b.folderCfg.Enabled {
		out.WriteString("Automatic filing is on: the triage model files new items that have no folder.\n")
	} else {
		out.WriteString("Automatic filing is off: only moves by the user (or inbox_move) file items.\n")
	}
	folders := b.folders(true)
	if len(folders) == 0 {
		out.WriteString("No folders in use.\n")
	} else {
		out.WriteString("Folders in use (items, open):\n")
		for _, x := range folders {
			fmt.Fprintf(&out, "- %s (%d, %d open)\n", x.Path, x.Count, x.Open)
		}
	}
	if f == "" && !a.Unfiled {
		return out.String(), nil
	}
	cutoff := time.Now().Add(-inboxKeepDone)
	var items []*InboxItem
	for _, it := range b.items {
		if it.DoneAt != nil && it.DoneAt.Before(cutoff) {
			continue
		}
		if f != "" && (it.Folder == f || strings.HasPrefix(it.Folder, f+"/")) || f == "" && it.Folder == "" {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].open() != items[j].open() {
			return items[i].open()
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	where := "in no folder"
	if f != "" {
		where = "in " + f + " and below"
	}
	fmt.Fprintf(&out, "\n%s %s", countItems(len(items)), where)
	if len(items) > folderToolItems {
		fmt.Fprintf(&out, " (the %d most recent open ones first)", folderToolItems)
		items = items[:folderToolItems]
	}
	out.WriteString(":\n")
	for _, it := range items {
		fmt.Fprintf(&out, "- id=%s · %s · %s", it.ID, b.sourceName(it.Source), clip(oneLine(it.Title), 80))
		if it.Sender != "" {
			fmt.Fprintf(&out, " · from %s", oneLine(it.Sender))
		}
		if it.Folder != "" && it.Folder != f {
			fmt.Fprintf(&out, " · folder %s", it.Folder)
		}
		if !it.open() {
			out.WriteString(" · done")
		}
		out.WriteString("\n")
	}
	return out.String(), nil
}

// toolInboxMove files the items given into folder ("" out of every folder), as the user's move: the triage model
// leaves them alone afterwards. Every id is checked before anything moves.
func (s *Service) toolInboxMove(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Items  []string `json:"items"`
		Folder *string  `json:"folder"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	if len(a.Items) == 0 || a.Folder == nil {
		return "", errors.New("items (ids from inbox_folders) and folder are required; folder \"\" takes the items out of every folder")
	}
	f := normalizeFolder(*a.Folder)
	if f == "" && strings.TrimSpace(*a.Folder) != "" {
		return "", fmt.Errorf("folder %q has no name between the slashes; nothing was moved", *a.Folder)
	}
	if err := s.inboxReady(ctx); err != nil {
		return "", err
	}
	b := &s.inbox
	ids, unknown := []string{}, []string{}
	seen := map[string]bool{}
	b.mu.Lock()
	for _, id := range a.Items {
		id = strings.TrimSpace(id)
		if seen[id] {
			continue
		}
		seen[id] = true
		if b.items[id] == nil {
			unknown = append(unknown, id)
		} else {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	if len(unknown) > 0 {
		return "", fmt.Errorf("no inbox item %s (ids come from inbox_folders); nothing was moved", strings.Join(unknown, ", "))
	}
	var failed []string
	for _, id := range ids {
		if _, _, err := s.setInboxFolder(ctx, id, &f); err != nil {
			failed = append(failed, id+" ("+err.Error()+")")
		}
	}
	moved := len(ids) - len(failed)
	where := "into " + f
	if f == "" {
		where = "out of every folder"
	}
	if len(failed) > 0 {
		return "", fmt.Errorf("moved %d of %d items %s; not moved: %s", moved, len(ids), where, strings.Join(failed, "; "))
	}
	return fmt.Sprintf("Moved %s %s. They stay where they are put: the triage model no longer files them.", countItems(moved), where), nil
}

// countItems is "1 item", "2 items".
func countItems(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// folderState is GET /api/v1/inbox/folders' answer.
func (s *Service) folderState(all bool) map[string]any {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{"enabled": b.folderCfg.Enabled, "rules": b.folderCfg.Rules, "folders": b.folders(all)}
}

func (s *Service) inboxFolderRoutes() {
	s.handle("GET /api/v1/inbox/folders", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		apierr.WriteJSON(w, s.folderState(r.URL.Query().Get("sources") == "all"))
	})
	// {"enabled"?, "rules"?}: what is given changes.
	s.handle("PUT /api/v1/inbox/folders", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Enabled *bool   `json:"enabled"`
			Rules   *string `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if _, err := s.setFolderConfig(r.Context(), in.Enabled, in.Rules); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, s.folderState(r.URL.Query().Get("sources") == "all"))
	})
	// {"folder": "a/b"} files the item there by hand, {"folder": ""} in no folder (the model leaves it alone too),
	// {"folder": null} hands it back to the model.
	s.handle("PUT /api/v1/inbox/{id}/folder", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Folder *string `json:"folder"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		it, code, err := s.setInboxFolder(r.Context(), r.PathValue("id"), in.Folder)
		if err != nil {
			apierr.Write(w, code, err)
			return
		}
		apierr.WriteJSON(w, it)
	})
}
