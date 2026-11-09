package extension

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// previewFor is how long a preview's staged package is kept.
const previewFor = 10 * time.Minute

// Preview is a staged package (or a remote plugin) and what installing it means, shown before the user agrees.
type Preview struct {
	ID          string           `json:"preview_id"`
	Manifest    *Manifest        `json:"manifest"`
	Permissions []PermissionLine `json:"permissions"`
	Digest      string           `json:"permissions_digest"`
	Warnings    []string         `json:"warnings,omitempty"`
	ExpiresAt   time.Time        `json:"expires_at"`
	// Upgrade: of the installed plugin with this id, from version From; MorePermissions: it asks for more than the
	// user granted, so the upgrade needs the digest.
	RenameFrom      string `json:"rename_from,omitempty"`
	Upgrade         bool   `json:"upgrade,omitempty"`
	From            string `json:"from,omitempty"`
	MorePermissions bool   `json:"more_permissions,omitempty"`
	// AddedPermissions are the lines of what the new version may do that the installed one may not (only what is
	// new: the added hosts, the added secrets), for the upgrade screen.
	AddedPermissions []PermissionLine `json:"added_permissions,omitempty"`
	// Market is the market it comes from (a full-rights device may install it, docs/extensions/extensions.md "Extension market").
	Market string `json:"market,omitempty"`
	// Depends are its dependencies on this hub (docs/extensions/extensions.md "Dependencies"); Bundle the previews of what installs with it,
	// before it: its missing dependencies from the markets, or a pack's members. Each has its own id and digest, and
	// installing sends them all (the user agreed to every one on one screen).
	Depends []DependencyState `json:"depends,omitempty"`
	Bundle  []*Preview        `json:"bundle,omitempty"`
	// Unresolved are the dependencies or members no registered market has (it does not install while one is missing).
	Unresolved []string `json:"unresolved,omitempty"`

	replaces              string    // source declaration pinned at preview
	previous              *Manifest // immutable consent baseline captured when the preview was staged
	source                Source
	marketSpec, marketKey string
	verifiedMarketKey     string // verified entry and package, separate from the mutable market's pin
	fingerprint           string // all staged package bytes/modes as approved at preview
	root                  string // the staged package
	staging               string
}

// MarketTrust is the market address and trusted public key pinned at preview time; it adds no protocol field.
func (pv *Preview) MarketTrust() (string, string) { return pv.marketSpec, pv.marketKey }

// previewIDPattern is what newID makes: a preview's staging directory is named so (staging/<id>).
var previewIDPattern = lazyre.New(`^[0-9a-f]{24}$`)

// dropStalePreviews removes the staging directories of previews an earlier run of the hub made and never installed
// or discarded: previews live in memory only, so after a restart nothing else would remove them. Only those older
// than previewFor go (expired wherever they were made), so a preview being staged now, or one of another hub still
// running on the same directory, stays. Other names in staging/ (plan-*, …) are left alone. Called with r.mu held.
func (r *Registry) dropStalePreviews() {
	root := filepath.Join(r.dir, "staging")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || !previewIDPattern.MatchString(e.Name()) || r.previews[e.Name()] != nil {
			continue
		}
		if fi, err := e.Info(); err == nil && r.now().Sub(fi.ModTime()) > previewFor {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// newID makes preview ids (and suffixes such as plan-<id>). A bare id directly under staging/ is a preview's
// directory and is removed on Load once expired (dropStalePreviews): other staging directories need a prefix.
func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Preview stages a package (or asks a remote plugin) and checks it for installing (upgrade "" ) or for upgrading
// the installed plugin upgrade.
func (r *Registry) Preview(ctx context.Context, source, upgrade string) (*Preview, error) {
	if r.store == nil {
		return nil, i18n.Err("extension-status.install_no_database")
	}
	src, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	return r.preview(ctx, src, upgrade, func(dir string) (string, error) {
		if src.Kind == "https" {
			reportState(ctx, StepDownload, StepRunning)
		} else {
			reportState(ctx, StepDownload, StepSkipped)
			reportState(ctx, StepVerify, StepSkipped)
		}
		return Stage(ctx, src, dir)
	})
}

// PreviewMarket is Preview for a plugin of a market: its package for this hub's platform, downloaded and checked
// against the index's sha256, must be the plugin the index says (id, version, permissions).
func (r *Registry) PreviewMarket(ctx context.Context, mk *Market, marketID string, e *MarketEntry, upgrade string, candidates ...*Manifest) (*Preview, error) {
	if r.store == nil {
		return nil, i18n.Err("extension-status.install_no_database")
	}
	if e == nil || e.Parsed == nil {
		return nil, ErrIdentity
	}
	if err := ValidateReplacements([]MarketEntry{*e}); err != nil {
		return nil, err
	}
	if upgrade == "" && e.Replaces != "" && r.SnapshotExact(e.Replaces) != nil {
		return nil, ErrIdentity // A successor cannot silently create a second installation.
	}
	if upgrade != "" {
		upgrade = r.CanonicalID(upgrade)
	}
	renameFrom := ""
	if upgrade != "" && upgrade != e.Parsed.ID {
		old := r.SnapshotExact(upgrade)
		if e.Replaces != upgrade || old == nil || FromMarket(old.Record.Origin) != marketID || marketID == "" {
			return nil, ErrIdentity
		}
		if target := r.Get(e.Parsed.ID); target != nil && target.Record.StorageKey() != old.Record.StorageKey() {
			return nil, ErrIdentity
		}
		renameFrom = upgrade
	}
	verifiedKey := ""
	if IsOfficialMarket(mk.Spec.String()) {
		if e.verifiedKey == "" || e.verifiedKey != Official.SigningKey || e.verifiedKey != mk.TrustedKey || e.verifiedEntry != entryFingerprint(e) {
			return nil, i18n.Err("extension-market_security.signature")
		}
		verifiedKey = e.verifiedKey
	}
	pkg := e.PackageFor(Platform())
	if pkg == nil {
		return nil, fmt.Errorf("%s 没有这台 hub（%s）能用的包", e.Parsed.ID, Platform())
	}
	src := Source{Kind: "market", Path: marketID + "/" + e.Parsed.ID}
	pv, err := r.previewIdentity(ctx, src, upgrade, renameFrom, func(dir string) (string, error) {
		file, name, err := mk.Download(ctx, pkg, dir)
		if err != nil {
			return "", err
		}
		out := filepath.Join(dir, "pkg")
		reportState(ctx, StepUnpack, StepRunning)
		if err := unpackAs(file, name, out); err != nil {
			return "", err
		}
		_ = os.Remove(file)
		return packageRoot(out)
	}, candidates...)
	if err != nil {
		return nil, err
	}
	if m := pv.Manifest; !ManifestsEqual(m, e.Parsed) {
		if pv, _ := r.take(pv.ID); pv != nil {
			r.dropStaging(pv)
		}
		return nil, fmt.Errorf("下载的包（%s %s）的完整清单和市场索引（%s %s）不一致", m.ID, m.Version, e.Parsed.ID, e.Parsed.Version)
	}
	pv.Market, pv.marketSpec, pv.marketKey = marketID, mk.Spec.String(), mk.TrustedKey
	pv.verifiedMarketKey = verifiedKey
	pv.RenameFrom, pv.replaces = renameFrom, e.Replaces
	return pv, nil
}

// MorePermissions says whether m asks for more than the installed cur was granted (its upgrade needs the user's
// confirmation).
func MorePermissions(cur *Extension, m *Manifest) bool {
	was := cur.Full
	return !cur.Record.Granted.Covers(m.Permissions) || (m.Runtime != nil && was.Runtime == nil) || NodeAdds(was, m) ||
		len(NewTools(was, m)) > 0 ||
		slices.ContainsFunc(m.Topics(), func(t string) bool { return !slices.Contains(was.Topics(), t) }) ||
		slices.ContainsFunc(m.Surfaces(), func(t string) bool { return !slices.Contains(was.Surfaces(), t) }) ||
		len(newChannels(was, m)) > 0 || len(newSendsAsUser(was, m)) > 0 ||
		slices.ContainsFunc(m.ContributesTo(), func(t string) bool { return !slices.Contains(was.ContributesTo(), t) })
}

// Discard drops a preview that will not be installed.
func (r *Registry) Discard(previewID string) {
	if pv, _ := r.take(previewID); pv != nil {
		r.dropStaging(pv)
	}
}

// PreviewMarketOf is the market a preview's package comes from ("" when it does not, or the preview is gone).
func (r *Registry) PreviewMarketOf(previewID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pv := r.previews[previewID]; pv != nil {
		return pv.Market
	}
	return ""
}

// PreviewUpload is Preview for a package uploaded in the browser (the admin UI): body is the .tar.gz / .zip named
// name, written into the preview's staging directory (at most PackageMost bytes) and unpacked there.
func (r *Registry) PreviewUpload(ctx context.Context, name string, body io.Reader, upgrade string) (*Preview, error) {
	if r.store == nil {
		return nil, i18n.Err("extension-status.install_no_database")
	}
	name, err := UploadName(name)
	if err != nil {
		return nil, err
	}
	return r.preview(ctx, Source{Kind: "upload", Path: name}, upgrade, func(dir string) (string, error) {
		reportState(ctx, StepDownload, StepSkipped)
		reportState(ctx, StepVerify, StepSkipped)
		reportState(ctx, StepUnpack, StepRunning)
		return StageUpload(body, name, dir)
	})
}

// preview checks what stage put into a fresh staging directory (or what a remote plugin says of itself).
func (r *Registry) preview(ctx context.Context, src Source, upgrade string, stage func(dir string) (string, error), candidates ...*Manifest) (*Preview, error) {
	return r.previewIdentity(ctx, src, upgrade, "", stage, candidates...)
}

func (r *Registry) previewIdentity(ctx context.Context, src Source, upgrade, renameFrom string, stage func(dir string) (string, error), candidates ...*Manifest) (*Preview, error) {
	upgrade = r.CanonicalID(upgrade)
	r.expire()
	pv := &Preview{ID: newID(), source: src, ExpiresAt: r.now().Add(previewFor)}
	var m *Manifest
	var err error
	if src.Kind == "remote" {
		for _, step := range []string{StepDownload, StepVerify, StepUnpack} {
			reportState(ctx, step, StepSkipped)
		}
		reportState(ctx, StepCheck, StepRunning)
		if r.RemoteManifest == nil {
			return nil, errors.New("this hub cannot reach remote plugins")
		}
		if m, err = r.RemoteManifest(ctx, Remote{RPCSocket: src.RPCSocket, HTTPSocket: src.HTTPSocket}); err != nil {
			return nil, fmt.Errorf("问不到远端扩展的清单：%w", err)
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if err := CheckTriageRules(m); err != nil {
			return nil, err
		}
	} else {
		pv.staging = filepath.Join(r.dir, "staging", pv.ID)
		root, err := stage(pv.staging)
		if err != nil {
			_ = os.RemoveAll(pv.staging)
			return nil, err
		}
		pv.root = root
		reportState(ctx, StepUnpack, StepDone)
		reportState(ctx, StepCheck, StepRunning)
		data, err := os.ReadFile(ManifestPath(root))
		if err == nil {
			m, err = Parse(data)
		}
		if err == nil {
			err = r.checkPackageContext(ctx, m, root)
		}
		if err == nil {
			err = CheckTriageRules(m)
		}
		if err != nil {
			_ = os.RemoveAll(pv.staging)
			return nil, err
		}
	}
	pv.Manifest, pv.Digest, pv.Permissions = m, Digest(m), Explain(m)
	pv.Warnings = m.Lint()
	pv.Depends = r.DependenciesContext(ctx, m)
	fail := func(err error) (*Preview, error) {
		if pv.staging != "" {
			_ = os.RemoveAll(pv.staging)
		}
		return nil, err
	}
	w, err := CheckEngines(m, r.build)
	if err != nil {
		return fail(err)
	}
	if w != "" {
		pv.Warnings = append(pv.Warnings, w)
	}
	if src.Kind != "remote" {
		pv.fingerprint, err = packageFingerprint(pv.root)
		if err != nil {
			return fail(err)
		}
	}
	var joint []*Manifest
	if len(candidates) == 0 && r.PlanCandidates != nil {
		joint = r.PlanCandidates(ctx, m, FromMarket(src.String()))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.extensions[m.ID]
	if renameFrom != "" {
		if src.Kind != "market" || renameFrom != upgrade {
			return fail(ErrIdentity)
		}
		cur = r.extensions[renameFrom]
		if cur == nil || FromMarket(cur.Record.Origin) != FromMarket(src.String()) {
			return fail(ErrIdentity)
		}
		if occupied := r.extensions[r.canonicalID(m.ID)]; occupied != nil && occupied.Record.StorageKey() != cur.Record.StorageKey() {
			return fail(ErrIdentity)
		}
		pv.RenameFrom = renameFrom
	}
	switch {
	case upgrade == "" && cur != nil && !r.yielding(cur):
		return fail(fmt.Errorf("已经有一个叫 %s 的扩展（%s）：要换新版本用升级", m.ID, cur.Record.Source))
	case upgrade != "" && upgrade != m.ID && renameFrom == "":
		return fail(fmt.Errorf("这个包是 %s，不是 %s", m.ID, upgrade))
	case upgrade != "" && (cur == nil || cur.Record.Source != SourceInstalled):
		return fail(i18n.Err("extension-status.upgrade_not_installed", i18n.Args{"id": upgrade}))
	case upgrade != "" && !NewerVersion(m.Version, cur.Record.Version):
		return fail(fmt.Errorf("%s 不比已装的 %s 新", m.Version, cur.Record.Version))
	}
	if renameFrom != "" {
		delete(r.extensions, renameFrom)
		defer func() { r.extensions[renameFrom] = cur }()
	}
	if len(candidates) == 0 && (r.conflicts(m) != nil || r.checkDependencyGraph(m) != nil) {
		candidates = joint
	}
	candidates = previewCandidates(m, candidates)
	if err := r.conflictsCandidates(m, candidates); err != nil {
		return fail(err)
	}
	if err := r.checkDependencyGraph(candidates...); err != nil {
		return fail(err)
	}
	if upgrade != "" {
		pv.Upgrade, pv.From = true, cur.Record.Version
		pv.MorePermissions = MorePermissions(cur, m)
		pv.previous = cur.Full
		pv.AddedPermissions = Added(cur.Full, m)
	}
	r.previews[pv.ID] = pv
	return pv, nil // the check is done when the hub has resolved its bundle too (it says so)
}

func (r *Registry) checkPackageContext(ctx context.Context, m *Manifest, root string) error {
	return r.checkPackageFiles(ctx, m, root, true)
}
func (r *Registry) checkPackageFiles(ctx context.Context, m *Manifest, root string, syntax bool) error {
	for _, a := range m.AssistantParts() {
		for _, sk := range a.Skills {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(sk), "SKILL.md")); err != nil {
				return fmt.Errorf("assistant %s: %s/SKILL.md 不在包里", a.ID, sk)
			}
		}
		if a.Instructions != "" {
			if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(a.Instructions))); err != nil || !fi.Mode().IsRegular() {
				return fmt.Errorf("assistant %s: %s 不在包里", a.ID, a.Instructions)
			}
		}
		if a.Gate != "" {
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.Gate)))
			if err != nil {
				return fmt.Errorf("assistant %s: %s 不在包里", a.ID, a.Gate)
			}
			g, err := ParseGate(raw)
			if err != nil {
				return fmt.Errorf("assistant %s: %s: %v", a.ID, a.Gate, err)
			}
			// The script is in one of its skills (the skills directory on the assistant's machine is where they go).
			if !slices.ContainsFunc(a.Skills, func(sk string) bool { return strings.HasPrefix(g.Script, path.Base(sk)+"/") }) {
				return fmt.Errorf("assistant %s: the gate's script %s is not in its skills", a.ID, g.Script)
			}
		}
	}
	for _, l := range m.Looks() {
		if err := CheckLookFiles(l, root); err != nil {
			return err
		}
	}
	if m.Node != nil {
		if !NodePrograms {
			return errors.New("这个版本的 hub 还不能在电脑上运行扩展的程序（node 段），先升级 hub")
		}
		// Every computer's program is in the package: the hub sends each machine the one for its platform.
		for _, platform := range slices.Sorted(maps.Keys(m.Node.Command.ByPlatform)) {
			if err := checkProgram(root, "node.command", m.Node.Command.ByPlatform[platform]); err != nil {
				return err
			}
		}
	}
	if m.Runtime == nil {
		return nil
	}
	if m.Runtime.IsDeno() {
		if !syntax {
			_, err := CheckSourceBundleIntegrity(root, m.Runtime.Source)
			return err
		}
		_, err := CheckSourceBundleContext(ctx, root, m.Runtime.Source)
		return err
	}
	cmd, ok := m.Runtime.Command.For(r.platform)
	if !ok {
		return fmt.Errorf("这个扩展没有 %s 的程序", r.platform)
	}
	if IsBuiltinCommand(cmd) {
		return errors.New("runtime.command: only a built-in plugin runs the hub's own binary")
	}
	return checkProgram(root, "runtime.command", cmd)
}

// NodePrograms says whether this hub runs plugins' programs on computers (docs/plans/plan-extensions.md "7"): off, a package
// with a node program does not install, rather than install and never run there (a test turns it off).
var NodePrograms = true

// checkProgram holds a program a manifest names (at: which field) to being an executable file inside the package.
func checkProgram(root, at, cmd string) error {
	p := filepath.Join(root, filepath.FromSlash(cmd))
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("%s %s 不在包里", at, cmd)
	}
	rootResolved, _ := filepath.EvalSymlinks(root)
	if rel, err := filepath.Rel(rootResolved, resolved); err != nil || !inside(filepath.ToSlash(rel)) {
		return fmt.Errorf("%s %s 跳出了包", at, cmd)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s %s 不是可执行文件", at, cmd)
	}
	return nil
}

// conflicts says which contribution of m another plugin or the hub has already. Called with r.mu held.
func (r *Registry) conflicts(m *Manifest) error {
	return r.conflictsCandidates(m, nil)
}

func (r *Registry) conflictsCandidates(m *Manifest, candidates []*Manifest) error {
	owners := make(map[string]*Manifest, len(r.extensions)+len(candidates))
	for id, other := range r.extensions {
		owners[id] = other.Full
	}
	for _, candidate := range candidates {
		owners[candidate.ID] = candidate
	}
	var errs []error
	for point, ids := range m.Contributions() {
		for _, id := range ids {
			if who, ok := r.reserved[point][id]; ok {
				errs = append(errs, fmt.Errorf("%s 里的 %q 已经是 %s 的", point, id, who))
			}
			for otherID, other := range owners {
				if otherID != m.ID && slices.Contains(other.Items(point), id) {
					errs = append(errs, fmt.Errorf("%s 里的 %q 已经是扩展 %s 的", point, id, otherID))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// The manifest actually read from the package overrides the planned one with the same ID; the market's description
// must not mask what was downloaded.
func previewCandidates(m *Manifest, candidates []*Manifest) []*Manifest {
	out := make([]*Manifest, 0, len(candidates)+1)
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID != m.ID {
			out = append(out, candidate)
		}
	}
	return append(out, m)
}

// expire drops previews past their time.
func (r *Registry) expire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, pv := range r.previews {
		if r.now().After(pv.ExpiresAt) {
			if pv.staging != "" {
				_ = os.RemoveAll(pv.staging)
			}
			delete(r.previews, id)
		}
	}
}

// take is the preview previewID, removed from the waiting ones.
// canEnable asks CanEnable, when the hub set it.
func (r *Registry) canEnable(ctx context.Context, source string, m *Manifest) error {
	if r.CanEnable == nil {
		return nil
	}
	return r.CanEnable(ctx, source, m)
}

// peek is a preview, left in place (a refusal before installing does not use it up).
func (r *Registry) peek(previewID string) *Preview {
	r.expire()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.previews[previewID]
}

func (r *Registry) take(previewID string) (*Preview, error) {
	r.expire()
	r.mu.Lock()
	defer r.mu.Unlock()
	pv := r.previews[previewID]
	if pv == nil {
		return nil, errors.New("预览过期了，重新预览一次")
	}
	delete(r.previews, previewID)
	return pv, nil
}
