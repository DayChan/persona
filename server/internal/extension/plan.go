package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/i18n"
)

// PreviewRef is the consent for one package in an install plan. It is internal to the registry; existing HTTP
// preview_id/permissions_digest/bundle fields stay unchanged.
type PreviewRef struct{ PreviewID, Digest string }

// PlanStore commits the records as one batch; a plain error means nothing was committed, and a lost
// confirmation must return CommitUnknownError.
// The registry prepares the packages; recovering from a failure never deletes user data or rewrites sealed
// secrets.
type PlanStore interface {
	PutExtensionPlan(context.Context, []Record) error
}

// CommitUnknownError means the answer to COMMIT was lost; it does not mean the database rolled back.
type CommitUnknownError struct{ Err error }

func (e *CommitUnknownError) Error() string { return "提交结果未知：" + e.Err.Error() }
func (e *CommitUnknownError) Unwrap() error { return e.Err }

func (r *Registry) beginOperation() (func(), error) {
	r.operationMu.Lock()
	if r.planProblem != nil {
		r.operationMu.Unlock()
		return nil, r.planProblem
	}
	return sync.OnceFunc(r.operationMu.Unlock), nil
}

// PlanError identifies the failure boundary without claiming that a committed program's external effects can roll back.
type PlanError struct {
	Stage string
	Err   error
}

func (e *PlanError) Error() string {
	if e.Stage == "校验" {
		return e.Err.Error()
	}
	return "扩展计划（" + e.Stage + "）：" + e.Err.Error()
}
func (e *PlanError) Unwrap() error { return e.Err }

// ManifestsEqual compares whole manifests; the order of object keys inside a RawMessage does not matter.
func ManifestsEqual(a, b *Manifest) bool {
	canonical := func(m *Manifest) ([]byte, error) {
		raw, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err = decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	left, err := canonical(a)
	if err != nil {
		return false
	}
	right, err := canonical(b)
	return err == nil && bytes.Equal(left, right)
}

var errPlanDigest = errors.New("权限摘要对不上：装的不是你看过的那一份")

type plannedRecord struct {
	preview *Preview
	old     *Extension
	row     Record
	next    *Extension
}
type packageMove struct {
	preview        *Preview
	target, backup string
	moved          bool
}

// planTools installs the command-line programs of the plan's packages (StepTools) before anything is committed: a
// tool that does not install leaves the extension as it was. It answers the manifests it installed tools for.
func (r *Registry) planTools(ctx context.Context, id, previewID, digest string, refs []PreviewRef, complete bool) ([]plannedRecord, error) {
	r.mu.Lock()
	plans, err := r.validatePlan(id, previewID, digest, refs, complete)
	r.mu.Unlock()
	if err != nil {
		return nil, nil // commitPlan says why
	}
	var done []plannedRecord
	any := false
	for _, plan := range plans {
		if m := plan.preview.Manifest; plan.row.Remote == nil && len(m.Tools) > 0 {
			any = true
			if err := r.installToolsAt(ctx, m, plan.row.StorageKey()); err != nil {
				r.dropPlanTools(append(done, plan))
				return nil, &PlanError{"安装命令行", err}
			}
			done = append(done, plan)
		}
	}
	if !any {
		reportState(ctx, StepTools, StepSkipped)
	}
	return done, nil
}

// dropPlanTools undoes planTools for a plan that did not commit: what the versions that stay do not use goes.
func (r *Registry) dropPlanTools(plans []plannedRecord) {
	unlock, err := r.beginOperation()
	if err != nil {
		return
	} // Indeterminate commits retain every potentially committed tool.
	defer unlock()
	for _, plan := range plans {
		slot := plan.row.StorageKey()
		var keep []string
		// A rename that failed has no new public name yet. Resolve the stable slot instead.
		if cur := r.Snapshot(slot); cur != nil && cur.Record.Source == SourceInstalled {
			keep = []string{cur.Record.Version, cur.Record.PreviousVersion}
		}
		if len(keep) == 0 {
			_ = os.RemoveAll(r.toolsDir(slot))
			continue
		}
		r.pruneTools(slot, keep...)
	}
}

// InstallPlan/UpgradePlan execute the complete confirmation, not a sequence of independently activated installs.
func (r *Registry) InstallPlan(ctx context.Context, previewID, digest string, refs []PreviewRef, actor string) (*Extension, error) {
	return r.applyPlan(ctx, "", previewID, digest, refs, actor, true)
}
func (r *Registry) UpgradePlan(ctx context.Context, id, previewID, digest string, refs []PreviewRef, actor string) (*Extension, error) {
	return r.applyPlan(ctx, id, previewID, digest, refs, actor, true)
}

func (r *Registry) applyPlan(ctx context.Context, id, previewID, digest string, refs []PreviewRef, actor string, complete bool) (*Extension, error) {
	tooled, err := r.planTools(ctx, id, previewID, digest, refs, complete)
	if err != nil {
		return nil, err
	}
	plans, main, err := r.commitPlan(ctx, id, previewID, digest, refs, complete)
	if err != nil {
		r.dropPlanTools(tooled)
		return nil, err
	}
	// Nothing runs, is pruned, or records a successful install before all database records have committed.
	for _, plan := range plans {
		kind, action := "installed", "install"
		detail := map[string]any{"version": plan.row.Version, "origin": plan.row.Origin, "permissions": plan.row.Granted}
		if plan.preview.Upgrade {
			kind, action = "upgraded", "upgrade"
			if plan.preview.RenameFrom != "" {
				kind = "renamed"
			}
			detail = map[string]any{"from": plan.row.PreviousVersion, "to": plan.row.Version, "permissions": plan.row.Granted}
			if unlock, err := r.beginOperation(); err == nil {
				if current := r.Snapshot(plan.row.ID); current != nil && current.Record.Version == plan.row.Version {
					r.prune(plan.row.StorageKey(), plan.row.Version, plan.row.PreviousVersion)
				}
				unlock()
			}
		}
		r.dropStaging(plan.preview)
		r.audit(ctx, plan.row.ID, action, actor, detail)
		r.changed(Change{ID: plan.row.ID, Kind: kind, Old: plan.old})
	}
	return main, nil
}

func (r *Registry) commitPlan(ctx context.Context, id, previewID, digest string, refs []PreviewRef, complete bool) (plans []plannedRecord, main *Extension, err error) {
	unlock, err := r.beginOperation()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	if r.store == nil {
		return nil, nil, &PlanError{"校验", errors.New("没有数据库：装不了扩展")}
	}
	r.expire()
	r.mu.Lock()
	plans, err = r.validatePlan(id, previewID, digest, refs, complete)
	r.mu.Unlock()
	if err != nil {
		return nil, nil, &PlanError{"校验", err}
	}
	for _, plan := range plans {
		if pv := plan.preview; pv.source.Kind == "remote" {
			if r.RemoteManifest == nil {
				return nil, nil, &PlanError{"校验", errors.New("无法复查远端扩展清单")}
			}
			current, e := r.RemoteManifest(ctx, Remote{RPCSocket: pv.source.RPCSocket, HTTPSocket: pv.source.HTTPSocket})
			if e != nil {
				return nil, nil, &PlanError{"校验", e}
			}
			if !ManifestsEqual(pv.Manifest, current) {
				return nil, nil, &PlanError{"校验", errors.New("预览后远端清单变了，请重新预览")}
			}
		}
		if plan.row.Enabled {
			if err = r.canEnable(ctx, SourceInstalled, plan.preview.Manifest); err != nil {
				return nil, nil, &PlanError{"校验", err}
			}
		}
	}
	if r.PlanGuard != nil {
		previews := make([]*Preview, 0, len(plans))
		for _, plan := range plans {
			previews = append(previews, plan.preview)
		}
		release, e := r.PlanGuard(ctx, previews)
		if e != nil {
			return nil, nil, &PlanError{"校验", e}
		}
		if release != nil {
			defer release()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Recheck previews after callbacks; callbacks may read the registry, but no operation can update installed rows.
	plans, err = r.validatePlan(id, previewID, digest, refs, complete)
	if err != nil {
		return nil, nil, &PlanError{"校验", err}
	}
	backupRoot := filepath.Join(r.dir, "staging", "plan-"+newID())
	if err = os.MkdirAll(backupRoot, 0o700); err != nil {
		return nil, nil, &PlanError{"准备", err}
	}
	var moves []packageMove
	var createdParents []string
	restore := func(cause error) ([]plannedRecord, *Extension, error) {
		var failures []error
		for i := len(moves) - 1; i >= 0; i-- {
			move := moves[i]
			if move.moved {
				if e := os.MkdirAll(filepath.Dir(move.preview.root), 0o755); e != nil {
					failures = append(failures, e)
				} else if e = os.Rename(move.target, move.preview.root); e != nil {
					failures = append(failures, e)
				}
			}
			if move.backup != "" {
				if e := os.Rename(move.backup, move.target); e != nil {
					failures = append(failures, e)
				}
			}
		}
		for i := len(createdParents) - 1; i >= 0; i-- {
			if e := os.Remove(createdParents[i]); e != nil && !os.IsNotExist(e) {
				failures = append(failures, e)
			}
		}
		if len(failures) > 0 {
			r.planProblem = &PlanError{"恢复未完成，已保留包和备份，请重新加载 hub 后核对", errors.Join(append([]error{cause}, failures...)...)}
			return nil, nil, r.planProblem
		}
		_ = os.RemoveAll(backupRoot)
		return nil, nil, &PlanError{"提交失败，原安装已保留，可重试", cause}
	}
	for _, plan := range plans {
		pv := plan.preview
		if plan.row.Remote != nil {
			continue
		}
		target := r.installedDir(plan.row.StorageKey(), plan.row.Version)
		move := packageMove{preview: pv, target: target}
		for _, parent := range []string{filepath.Join(r.dir, "installed"), filepath.Dir(target)} {
			if _, e := os.Lstat(parent); os.IsNotExist(e) {
				createdParents = append(createdParents, parent)
			} else if e != nil {
				return restore(e)
			}
		}
		if e := os.MkdirAll(filepath.Dir(target), 0o755); e != nil {
			return restore(e)
		}
		if _, e := os.Lstat(target); e == nil {
			move.backup = filepath.Join(backupRoot, plan.row.ID)
			if e = os.Rename(target, move.backup); e != nil {
				return restore(e)
			}
		} else if !os.IsNotExist(e) {
			return restore(e)
		}
		moves = append(moves, move)
		if e := os.Rename(pv.root, target); e != nil {
			return restore(e)
		}
		moves[len(moves)-1].moved = true
	}
	rows := make([]Record, 0, len(plans))
	for _, plan := range plans {
		rows = append(rows, plan.row)
	}
	if store, ok := r.store.(PlanStore); ok {
		err = store.PutExtensionPlan(ctx, rows)
	} else if len(rows) == 1 {
		err = r.store.PutExtension(ctx, rows[0])
	} else {
		err = errors.New("数据库没有整份计划事务能力")
	}
	if err != nil {
		var unknown *CommitUnknownError
		if errors.As(err, &unknown) {
			r.planProblem = &PlanError{"提交确认丢失，未激活；已保留包和备份，请重新加载 hub 后核对", err}
			return nil, nil, r.planProblem
		}
		return restore(err)
	}
	for i := range plans {
		plan := &plans[i]
		plan.next = r.fromRow(plan.row)
		if r.yielding(r.extensions[plan.row.ID]) {
			r.yielded[plan.row.ID] = r.extensions[plan.row.ID]
		}
		if plan.old != nil && plan.old.Record.ID != plan.row.ID {
			delete(r.extensions, plan.old.Record.ID)
		}
		r.extensions[plan.row.ID] = plan.next
		delete(r.previews, plan.preview.ID)
		if plan.preview.ID == previewID {
			main = plan.next
		}
	}
	_ = os.RemoveAll(backupRoot)
	return plans, main, nil
}

// validatePlan is called with r.mu held. It validates the complete candidate graph and namespace before any write.
func (r *Registry) validatePlan(id, previewID, digest string, refs []PreviewRef, complete bool) ([]plannedRecord, error) {
	main := r.previews[previewID]
	if main == nil || !r.now().Before(main.ExpiresAt) {
		return nil, errors.New("预览过期了，重新预览一次")
	}
	if main.Upgrade != (id != "") || id != "" && main.UpgradeID() != r.canonicalID(id) {
		return nil, errors.New("安装/升级预览和请求对不上")
	}
	if len(refs) != len(main.Bundle) {
		return nil, fmt.Errorf("要一起确认 %d 个扩展，重新预览", len(main.Bundle))
	}
	complete = complete || main.Manifest.IsPack() || len(main.Bundle) > 0
	all := make([]*Preview, 0, len(refs)+1)
	for i, ref := range refs {
		expected := main.Bundle[i]
		pv := r.previews[ref.PreviewID]
		if pv == nil || !r.now().Before(pv.ExpiresAt) {
			return nil, errors.New("一起装的预览过期了，重新预览一次")
		}
		if pv != expected || ref.Digest != pv.Digest {
			return nil, fmt.Errorf("一起装的 %s 和预览对不上", expected.Manifest.ID)
		}
		all = append(all, pv)
	}
	all = append(all, main)
	combined := make(map[string]*Extension, len(r.extensions)+len(all))
	for key, p := range r.extensions {
		combined[key] = p
	}
	seen := map[string]bool{}
	var plans []plannedRecord
	var manifests []*Manifest
	for _, pv := range all {
		m := pv.Manifest
		if seen[m.ID] {
			return nil, fmt.Errorf("计划里重复出现扩展 %s", m.ID)
		}
		seen[m.ID] = true
		if len(pv.Unresolved) > 0 {
			return nil, errors.New("依赖尚未满足：" + strings.Join(pv.Unresolved, "、"))
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if Digest(m) != pv.Digest {
			return nil, errPlanDigest
		}
		if _, err := CheckEngines(m, r.build); err != nil {
			return nil, err
		}
		if err := CheckTriageRules(m); err != nil {
			return nil, err
		}
		if pv.source.Kind != "remote" {
			fingerprint, err := packageFingerprint(pv.root)
			if err != nil {
				return nil, err
			}
			if fingerprint != pv.fingerprint {
				return nil, errors.New("预览后的包内容变了，请重新预览")
			}
			raw, err := os.ReadFile(ManifestPath(pv.root))
			if err != nil {
				return nil, err
			}
			staged, err := Parse(raw)
			if err != nil {
				return nil, err
			}
			if !ManifestsEqual(m, staged) {
				return nil, errors.New("预览后的包清单变了，请重新预览")
			}
			// Preview already validated this exact signed digest outside r.mu.
			// Recheck bounded integrity here, never wait for a validator while
			// holding the registry lock needed by neighboring extensions.
			if err = r.checkPackageFiles(context.Background(), m, pv.root, false); err != nil {
				return nil, err
			}
		}
		old := snapshotExtension(r.extensions[pv.UpgradeID()])
		if pv.RenameFrom != "" {
			if pv.replaces != pv.RenameFrom || old == nil || FromMarket(old.Record.Origin) != pv.Market || pv.Market == "" || pv.source.Kind != "market" {
				return nil, ErrIdentity
			}
			if occupied := r.extensions[r.canonicalID(m.ID)]; occupied != nil && occupied.Record.StorageKey() != old.Record.StorageKey() {
				return nil, ErrIdentity
			}
			delete(combined, old.Record.ID)
		}
		requested := pv.Digest
		if pv == main {
			requested = digest
		}
		row := Record{ID: m.ID, Source: SourceInstalled, Origin: pv.source.String(), Version: m.Version, Granted: m.Permissions, GrantedDigest: pv.Digest, Enabled: true, Config: json.RawMessage(`{}`), InstalledAt: r.now(), UpdatedAt: r.now()}
		if pv.Upgrade {
			if old == nil || old.Record.Source != SourceInstalled || old.Record.Version != pv.From {
				return nil, errors.New("扩展在预览之后变了，重新预览一次")
			}
			if old.Record.AttachedPackage() {
				return nil, i18n.Err("extension-status.attached_detach_first", i18n.Args{"id": old.Record.ID})
			}
			if pv.RenameFrom != "" && (requested == "" || pv.Digest == "" || requested != pv.Digest) {
				return nil, errPlanDigest
			}
			if (requested != "" || MorePermissions(old, m) || pv != main || pv.RenameFrom != "") && requested != pv.Digest {
				return nil, errPlanDigest
			}
			row = snapshotExtension(old).Record
			row.ExpectedID, row.ExpectedVersion = old.Record.ID, old.Record.Version
			if pv.RenameFrom != "" {
				row.StorageID = old.Record.StorageKey()
				row.PreviousID = old.Record.ID
				row.ID = m.ID
				if !slices.Contains(row.Aliases, old.Record.ID) {
					row.Aliases = append(row.Aliases, old.Record.ID)
				}
				row.Aliases = slices.DeleteFunc(row.Aliases, func(id string) bool { return id == m.ID })
			} else if row.StorageID != "" {
				row.PreviousID = old.Record.ID
			}
			row.DisabledContributions = m.StartsOff(old.Full, row.DisabledContributions)
			row.PreviousVersion, row.Version, row.UpdatedAt = row.Version, m.Version, r.now()
			// The replaced version's provenance goes with it, for a rollback to restore.
			row.PreviousOrigin, row.PreviousMarketKey = old.Record.Origin, old.Record.MarketKey
			row.Origin = pv.source.String()
			row.Granted, row.GrantedDigest = m.Permissions, pv.Digest
		} else {
			if pv.replaces != "" && r.extensions[pv.replaces] != nil {
				return nil, ErrIdentity
			}
			if occupied := r.extensions[r.canonicalID(m.ID)]; occupied != nil && occupied.Record.ID != m.ID {
				return nil, ErrIdentity
			}
			if requested != pv.Digest {
				return nil, errPlanDigest
			}
			if old != nil && !r.yielding(old) {
				return nil, fmt.Errorf("已经有扩展 %s", m.ID)
			}
			row.DisabledContributions = m.StartsOff(nil, nil)
			row.NewInstallation = !r.yielding(old)
		}
		// Overwrite on every install/upgrade, including a return to unsigned code.
		row.MarketKey = pv.verifiedMarketKey
		row.Manifest, _ = json.Marshal(m)
		if pv.source.Kind == "remote" {
			row.Remote = &Remote{RPCSocket: pv.source.RPCSocket, HTTPSocket: pv.source.HTTPSocket}
		} else {
			row.Remote = nil
		}
		if !json.Valid(row.Config) {
			return nil, errors.New("已保存配置不是有效的 JSON")
		}
		candidate := &Extension{Record: row, Full: m, Manifest: m.Without(row.DisabledContributions)}
		var err error
		candidate.Schema, err = ParseSchema(m.Configuration)
		if err != nil {
			return nil, err
		}
		if err = candidate.Schema.CheckSaved(row.Config); err != nil {
			return nil, fmt.Errorf("扩展 %s 的已保存配置不符合新清单：%w", m.ID, err)
		}
		combined[m.ID] = candidate
		plans = append(plans, plannedRecord{preview: pv, old: old, row: row})
		manifests = append(manifests, m)
	}
	if len(all) > 1 {
		if _, ok := r.store.(PlanStore); !ok {
			return nil, errors.New("数据库没有整份计划事务能力")
		}
	}
	baseline := maps.Clone(r.extensions)
	for _, plan := range plans {
		if plan.preview.RenameFrom != "" {
			delete(baseline, plan.preview.RenameFrom)
		}
	}
	if err := checkCandidateDependencyGraph(baseline, manifests...); err != nil {
		return nil, err
	}
	for _, plan := range plans {
		m := plan.preview.Manifest
		for point, ids := range m.Contributions() {
			for _, cid := range ids {
				if owner := r.reserved[point][cid]; owner != "" {
					return nil, fmt.Errorf("%s 的 %s 已由 %s 使用", point, cid, owner)
				}
				for other, p := range combined {
					if other != m.ID && slices.Contains(p.Full.Items(point), cid) {
						return nil, fmt.Errorf("%s 的 %s 和扩展 %s 冲突", point, cid, other)
					}
				}
			}
		}
	}
	if complete {
		seen := map[string]bool{}
		var check func(string) error
		check = func(id string) error {
			if seen[id] {
				return nil
			}
			seen[id] = true
			p := combined[id]
			if p == nil {
				return fmt.Errorf("先装它要的扩展 %s", id)
			}
			deps := slices.Clone(p.Full.Depends)
			for _, member := range p.Full.ExtensionPack {
				deps = append(deps, Dependency{ID: member.ID, Version: member.Version})
			}
			for _, dep := range deps {
				child := combined[dep.ID]
				if child == nil {
					return fmt.Errorf("先装扩展 %s 要的扩展 %s", id, dep.ID)
				}
				if child.Record.Source == SourceInstalled && !dep.Allows(child.Record.Version) {
					return fmt.Errorf("扩展 %s 要 %s %s，候选版本是 %s", id, dep.ID, dep.Version, child.Record.Version)
				}
				if err := check(dep.ID); err != nil {
					return err
				}
			}
			return nil
		}
		if err := check(main.Manifest.ID); err != nil {
			return nil, err
		}
	}
	return plans, nil
}
