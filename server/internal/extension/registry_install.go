package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Install installs a previewed package, digest being the one the user was shown. It is on at once, so CanEnable is
// asked first (a refusal keeps the preview).
func (r *Registry) Install(ctx context.Context, previewID, digest, actor string) (*Extension, error) {
	p, err := r.applyPlan(ctx, "", previewID, digest, nil, actor, false)
	if errors.Is(err, errPlanDigest) {
		r.Discard(previewID)
	} // legacy single-package consent consumes the refused preview
	return p, err
}

func (r *Registry) dropStaging(pv *Preview) {
	if pv.staging != "" {
		_ = os.RemoveAll(pv.staging)
	}
}

// Upgrade installs a previewed newer version of id. The digest is needed when it asks for more than was granted. A
// plugin that is on stays on, so CanEnable is asked about the new version first (a refusal keeps the preview).
func (r *Registry) Upgrade(ctx context.Context, id, previewID, digest, actor string) (*Extension, error) {
	p, err := r.applyPlan(ctx, id, previewID, digest, nil, actor, false)
	if errors.Is(err, errPlanDigest) {
		r.Discard(previewID)
	}
	return p, err
}

// prune deletes the package directories of id other than keep.
func (r *Registry) prune(id string, keep ...string) {
	entries, _ := os.ReadDir(filepath.Join(r.dir, "installed", id))
	for _, e := range entries {
		if !slices.Contains(keep, e.Name()) {
			_ = os.RemoveAll(filepath.Join(r.dir, "installed", id, e.Name()))
		}
	}
	r.pruneTools(id, keep...)
}

// Rollback goes back to the version installed before the last upgrade.
func (r *Registry) Rollback(ctx context.Context, id, actor string) (*Extension, error) {
	unlock, err := r.beginOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	old := r.Snapshot(id)
	if old == nil || old.Record.Source != SourceInstalled {
		return nil, fmt.Errorf("没有装过扩展 %s", id)
	}
	id = old.Record.ID
	if old.Record.AttachedPackage() {
		return nil, i18n.Err("extension-status.attached_detach_first", i18n.Args{"id": id})
	}
	prev := old.Record.PreviousVersion
	if prev == "" || old.Record.Remote != nil {
		return nil, errors.New("没有可以回到的上一个版本")
	}
	data, err := os.ReadFile(ManifestPath(r.installedDir(old.Record.StorageKey(), prev)))
	if err != nil {
		return nil, fmt.Errorf("上一个版本 %s 的包不在了", prev)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	previousID := old.Record.PreviousID
	if previousID == "" {
		previousID = old.Record.ID
	}
	if m.ID != previousID || m.Version != prev {
		return nil, ErrIdentity
	}
	var p *Extension
	err = func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		cur := r.extensions[id]
		if cur == nil || cur.Record.Version != old.Record.Version || cur.Record.PreviousVersion != prev {
			return errors.New("扩展在回滚之前变了，请重试")
		}
		if previousID != id {
			if occupied := r.extensions[r.canonicalID(previousID)]; occupied != nil && occupied.Record.StorageKey() != cur.Record.StorageKey() {
				return ErrIdentity
			}
			delete(r.extensions, id)
			defer func() {
				if p == nil {
					r.extensions[id] = cur
				}
			}()
		}
		if err := r.conflicts(m); err != nil {
			return err
		}
		if err := r.checkDependencyGraph(m); err != nil {
			return err
		}
		row := snapshotExtension(cur).Record
		row.ExpectedID, row.ExpectedVersion = cur.Record.ID, cur.Record.Version
		row.ID, row.PreviousID = previousID, ""
		if previousID != id && !slices.Contains(row.Aliases, id) {
			row.Aliases = append(row.Aliases, id)
		}
		row.Aliases = slices.DeleteFunc(row.Aliases, func(alias string) bool { return alias == row.ID })
		row.Version, row.PreviousVersion, row.UpdatedAt = prev, "", r.now()
		// The old version's own provenance, never the current one's: a verified key goes back only with a snapshot.
		row.MarketKey = ""
		if row.PreviousOrigin != "" {
			row.Origin, row.MarketKey = row.PreviousOrigin, row.PreviousMarketKey
		}
		row.PreviousOrigin, row.PreviousMarketKey = "", ""
		row.Manifest, _ = json.Marshal(m)
		row.Granted, row.GrantedDigest = m.Permissions, Digest(m)
		if err := r.store.PutExtension(ctx, row); err != nil {
			var unknown *CommitUnknownError
			if errors.As(err, &unknown) {
				r.planProblem = err
			}
			return err
		}
		p = r.fromRow(row)
		delete(r.extensions, id)
		r.extensions[row.ID] = p
		r.prune(row.StorageKey(), prev)
		return nil
	}()
	if err != nil {
		return nil, err
	}
	unlock()
	r.audit(ctx, id, "rollback", actor, map[string]any{"from": old.Record.Version, "to": prev})
	r.changed(Change{ID: p.Record.ID, Kind: "rolled_back", Old: old})
	return p, nil
}

// Uninstall removes an installed plugin: its row and secrets, its packages and (unless keepData) its data.
func (r *Registry) Uninstall(ctx context.Context, id string, keepData bool, actor string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	old := r.Get(id)
	if old == nil {
		return i18n.Err("extension-status.extension_missing", i18n.Args{"id": id})
	}
	if old.Record.Source == SourceDev {
		unlock()
		return r.RemoveDev(id)
	}
	if old.Record.Source == SourceBuiltin {
		return i18n.Err("extension-status.builtin_disable_only", i18n.Args{"id": id})
	}
	if old.Record.Source != SourceInstalled {
		return i18n.Err("extension-status.config_disable_only", i18n.Args{"id": id})
	}
	id = old.Record.ID
	var trash string
	err = func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		cur := r.extensions[id]
		if cur == nil || cur.Record.Source != SourceInstalled {
			return fmt.Errorf("扩展 %s 在卸载之前变了，请重试", id)
		}
		old = snapshotExtension(cur)
		slot := old.Record.StorageKey()
		dirs := []string{filepath.Join("installed", slot), filepath.Join("tools", slot)}
		if !keepData {
			dirs = append(dirs, filepath.Join("data", slot))
		}
		// Its directories move aside before the row goes: one that cannot move fails the uninstall with nothing
		// changed, rather than leaving data behind an uninstall that said it worked. Load finishes or undoes a move
		// a crash interrupted (recoverUninstalls).
		aside, err := r.moveAside(old.Record, dirs)
		if err != nil {
			return err
		}
		if store, ok := r.store.(StorageRetentionStore); ok {
			err = store.DeleteExtensionRecordRetainingData(ctx, old.Record, keepData)
		} else if store, ok := r.store.(IdentityDeleteStore); ok {
			err = store.DeleteExtensionRecord(ctx, old.Record)
		} else {
			err = r.store.DeleteExtension(ctx, slot)
		}
		if err != nil {
			var unknown *CommitUnknownError
			if errors.As(err, &unknown) {
				r.planProblem = err
				return err
			} // Leave aside for committed-row reconciliation on restart.
			if rerr := r.moveBack(aside); rerr != nil {
				return putBackFailed(err, aside, rerr)
			}
			return err
		}
		// The row is gone: from here these files are only ever removed, never put back (a reinstall of the same id
		// must not get the data the owner asked to delete). The identity in uninstall.json covers a crash before this.
		if aside != "" {
			_ = os.WriteFile(filepath.Join(aside, uninstallCommitted), nil, 0o600)
		}
		trash = aside
		delete(r.extensions, id)
		back := r.yielded[id]
		if back != nil {
			delete(r.yielded, id)
			r.extensions[id] = back
		}
		return nil
	}()
	if err != nil {
		return err
	}
	// Still inside the operation: a Load meanwhile would find the moved-aside directory too.
	var left error
	if trash != "" {
		if err := os.RemoveAll(trash); err != nil {
			left = &LeftoverError{ID: id, Path: trash, Err: err}
		}
	}
	unlock()
	r.audit(ctx, id, "uninstall", actor, map[string]any{"version": old.Record.Version, "keep_data": keepData})
	r.changed(Change{ID: id, Kind: "removed", Old: old})
	return left
}

// LeftoverError is an uninstall that happened (the row is gone) whose files could not all be removed: the caller
// reports the extension uninstalled, with Path to delete by hand.
type LeftoverError struct {
	ID, Path string
	Err      error
}

func (e *LeftoverError) Error() string {
	return i18n.T(i18n.Default, "extension-uninstall.leftover", i18n.Args{"id": e.ID, "path": e.Path, "err": e.Err.Error()})
}

func (e *LeftoverError) Unwrap() error { return e.Err }

// putBackFailed is err (what failed the uninstall) when putting the moved directories back failed too (rerr): the
// files stay in aside, named so they can be found.
func putBackFailed(err error, aside string, rerr error) error {
	return fmt.Errorf("%w: %w", err, i18n.Err("extension-uninstall.put_back_failed", i18n.Args{"path": aside, "err": rerr.Error()}))
}

// uninstallManifest is what a moved-aside directory holds besides the directories: whose they are and where each
// came from (relative to the registry's directory), so Load can put them back or finish removing them.
const uninstallManifest = "uninstall.json"

// uninstallCommitted, in a moved-aside directory, says the extension's row was deleted: its files are to be removed.
const uninstallCommitted = "committed"

type asideMoves struct {
	ID string `json:"id"`
	// Version and InstalledAt (Unix seconds: the database keeps less than nanoseconds) say which install moved: only
	// a row of the same install gets the files back.
	Version     string `json:"version"`
	InstalledAt int64  `json:"installed_at"`
	Moves       []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"moves"`
}

// moveAside renames each existing dir (relative to r.dir) into one fresh staging/uninstall-<id>-<rand> directory
// (the same file system, so a rename), after writing its manifest there. It returns that directory ("" when no dir
// existed). On an error what moved is put back; when that fails too the directory is kept and named in the error.
func (r *Registry) moveAside(row Record, dirs []string) (string, error) {
	id := row.StorageKey()
	plan := asideMoves{ID: id, Version: row.Version, InstalledAt: row.InstalledAt.Unix()}
	for i, d := range dirs {
		if _, err := os.Lstat(filepath.Join(r.dir, d)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		plan.Moves = append(plan.Moves, struct {
			From string `json:"from"`
			To   string `json:"to"`
		}{d, strconv.Itoa(i)})
	}
	if len(plan.Moves) == 0 {
		return "", nil
	}
	aside := filepath.Join(r.dir, "staging", "uninstall-"+id+"-"+newID())
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return "", err
	}
	b, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(aside, uninstallManifest), b, 0o600); err != nil {
		_ = os.RemoveAll(aside) // nothing moved yet
		return "", err
	}
	for _, m := range plan.Moves {
		from := filepath.Join(r.dir, m.From)
		if err := os.Rename(from, filepath.Join(aside, m.To)); err != nil {
			moved := fmt.Errorf("%w: %w", i18n.Err("extension-uninstall.move_failed", i18n.Args{"path": from}), err)
			if rerr := r.moveBack(aside); rerr != nil {
				return "", putBackFailed(moved, aside, rerr)
			}
			return "", moved
		}
	}
	return aside, nil
}

// moveBack puts what moveAside moved into aside back where it came from, then removes aside. It keeps aside (and
// says so) when anything could not go back, so nothing is deleted that is not where it belongs.
func (r *Registry) moveBack(aside string) error {
	if aside == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(aside, uninstallManifest))
	if err != nil {
		return err
	}
	var plan asideMoves
	if err := json.Unmarshal(b, &plan); err != nil {
		return err
	}
	var errs []error
	for i := len(plan.Moves) - 1; i >= 0; i-- {
		m := plan.Moves[i]
		to, from := filepath.Join(aside, m.To), filepath.Join(r.dir, m.From)
		if !ownDir(plan.ID, m.From) || m.To == "" || strings.ContainsAny(m.To, `/\.`) {
			errs = append(errs, i18n.Err("extension-uninstall.bad_path", i18n.Args{"path": m.From}))
			continue
		}
		if _, err := os.Lstat(to); errors.Is(err, os.ErrNotExist) {
			continue // never moved
		}
		if _, err := os.Lstat(from); err == nil {
			errs = append(errs, i18n.Err("extension-uninstall.taken", i18n.Args{"path": from}))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(from), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(to, from); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return os.RemoveAll(aside)
}

// ownDir says whether rel is one of the directories an uninstall of id moves: installed/<id>, data/<id>, tools/<id>.
func ownDir(id, rel string) bool {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return false
	}
	for _, d := range []string{"installed", "data", "tools"} {
		if filepath.Clean(rel) == filepath.Join(d, id) {
			return true
		}
	}
	return false
}

// recoverUninstalls finishes the uninstalls a crash interrupted (staging/uninstall-*). The directories go back only
// when the uninstall had not deleted the row (no committed marker) and the row there now is the same install
// (version, installed-at); otherwise, a reinstall of the same id included, they are removed. Failures go to Logf
// and keep the directory for the next start.
func (r *Registry) recoverUninstalls(rows []Record) {
	root := filepath.Join(r.dir, "staging")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "uninstall-") {
			continue
		}
		aside := filepath.Join(root, e.Name())
		var plan asideMoves
		b, err := os.ReadFile(filepath.Join(aside, uninstallManifest))
		if err == nil {
			err = json.Unmarshal(b, &plan)
		}
		if err != nil || plan.ID == "" {
			r.logf("extensions: %s: an interrupted uninstall without its record (%v); left for you to check", aside, err)
			continue
		}
		_, cerr := os.Lstat(filepath.Join(aside, uninstallCommitted))
		committed := cerr == nil
		same := slices.ContainsFunc(rows, func(row Record) bool {
			return row.StorageKey() == plan.ID && row.Source == SourceInstalled && row.Version == plan.Version && row.InstalledAt.Unix() == plan.InstalledAt
		})
		if !committed && same {
			if err := r.moveBack(aside); err != nil {
				r.logf("extensions: %s: putting back the files of an interrupted uninstall: %v (they are in %s)", plan.ID, err, aside)
			} else {
				r.logf("extensions: %s: an uninstall was interrupted before it happened; its files are back", plan.ID)
			}
			continue
		}
		if err := os.RemoveAll(aside); err != nil {
			r.logf("extensions: %s: removing the files of an uninstalled extension: %v; delete %s by hand", plan.ID, err, aside)
		} else {
			r.logf("extensions: %s: removed the files an interrupted uninstall left", plan.ID)
		}
	}
}
