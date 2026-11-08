package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Installs, upgrades and uninstalls as operations with steps (docs/protocol/protocol.md "Extension install, upgrade and
// uninstall progress", #3277): each one is kept here while it runs and for opKeep after, and every change of a step
// goes to the apps as an extension_operation event, so a page closed and opened again still shows an install in
// progress. They are only in memory: a hub that restarts forgets them (what was not installed yet is not installed).

const (
	opInstall   = "install"
	opUpgrade   = "upgrade"
	opUninstall = "uninstall"

	opRunning      = "running"
	opNeedsConsent = "needs_consent"
	opSucceeded    = "succeeded"
	opFailed       = "failed"
	opCanceled     = "canceled"

	// opKeep is how long a finished operation is still listed; opMost how many are kept at most.
	opKeep = 10 * time.Minute
	opMost = 64
	// opPushEvery is how often a download's progress is pushed at most (a step that changes is pushed at once).
	opPushEvery = time.Second
)

// The steps of each kind, in order.
var (
	installSteps   = []string{extension.StepDownload, extension.StepVerify, extension.StepUnpack, extension.StepCheck, extension.StepConsent, extension.StepTools, extension.StepInstall, extension.StepStart, extension.StepNodes}
	uninstallSteps = []string{extension.StepStop, extension.StepRemove, extension.StepNodes}
)

// ExtensionOperation is one install, upgrade or uninstall as the apps see it.
type ExtensionOperation struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Extension string            `json:"extension"`
	Name      string            `json:"name,omitempty"`
	Market    string            `json:"market,omitempty"`
	Source    string            `json:"source,omitempty"`
	Version   string            `json:"version,omitempty"`
	From      string            `json:"from,omitempty"`
	Actor     string            `json:"actor,omitempty"`
	State     string            `json:"state"`
	Step      string            `json:"step,omitempty"`
	Steps     []ExtensionOpStep `json:"steps"`
	Error     string            `json:"error,omitempty"`
	ErrorCode string            `json:"error_code,omitempty"`
	PreviewID string            `json:"preview_id,omitempty"`
	Retryable bool              `json:"retryable"`
	StartedAt time.Time         `json:"started_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	EndedAt   *time.Time        `json:"ended_at,omitempty"`
	// Next is what the user should do once it succeeded: "auth" when the extension signs in and is not signed in.
	Next string `json:"next,omitempty"`
	// Preview is the preview waiting for the user's consent (GET of one operation only, never in events).
	Preview any `json:"preview,omitempty"`
	// Warnings is what an uninstall that succeeded leaves the user to do by hand, e.g. files it could not delete.
	Warnings []string `json:"warnings,omitempty"`
}

// ExtensionOpStep is one step of an operation.
type ExtensionOpStep struct {
	ID       string      `json:"id"`
	State    string      `json:"state"`
	Bytes    int64       `json:"bytes,omitempty"`
	Total    int64       `json:"total,omitempty"`
	Cached   bool        `json:"cached,omitempty"`
	Machines *OpMachines `json:"machines,omitempty"`
	// Tools is the tools step's command-line programs, in the order they install.
	Tools     []OpTool   `json:"tools,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// OpTool is one command-line program of the tools step: pending, running, done or failed; Reused when it is the
// machine's own copy.
type OpTool struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reused bool   `json:"reused,omitempty"`
}

// OpMachines is how far the nodes step is: the computers done of those it waits for, and why one failed.
type OpMachines struct {
	Done   int               `json:"done"`
	Total  int               `json:"total"`
	Errors map[string]string `json:"errors,omitempty"`
}

type extensionOps struct {
	mu   sync.Mutex
	byID map[string]*extOp
}

// extOp is an operation and what the hub needs to go on with it; its fields are guarded by extensionOps.mu.
type extOp struct {
	v        ExtensionOperation
	lang     i18n.Lang
	pushed   time.Time
	ofMarket bool // a full-rights device may see to it (else only the owner, over loopback)
	// retry starts it again (nil: it cannot be, as an upload whose body is gone).
	retry func() (*extOp, error)
	// consented: the digests the user agreed to (the main one's and its bundle's by extension id), so a retry after
	// the consent goes on by itself when nothing it asks for changed.
	consented       string
	consentedBundle map[string]string
	// claimed is the preview one install took this operation over with (claimOpOfPreview).
	claimed string
	// cancel stops its preview while it downloads and checks (DELETE); canceling says it was asked to.
	cancel    context.CancelFunc
	canceling bool
}

func newOpID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// startOp makes a new operation of kind for extension id and tells the apps.
func (h *Manager) startOp(kind, id, actor string, l i18n.Lang, ofMarket bool) *extOp {
	ps := h.extensions
	now := time.Now()
	steps := installSteps
	if kind == opUninstall {
		steps = uninstallSteps
	}
	op := &extOp{lang: l, ofMarket: ofMarket, v: ExtensionOperation{ID: newOpID(), Kind: kind, Extension: id, Actor: actor, State: opRunning,
		StartedAt: now, UpdatedAt: now}}
	for _, s := range steps {
		op.v.Steps = append(op.v.Steps, ExtensionOpStep{ID: s, State: extension.StepPending})
	}
	if p := ps.reg.Get(id); p != nil {
		op.v.Name = p.Full.Name
		if kind != opInstall {
			op.v.From = p.Record.Version
		}
		if kind == opUninstall {
			op.v.Source = p.Record.Origin
			op.v.Market = extension.FromMarket(p.Record.Origin)
		}
	}
	// A consent-waiting operation whose preview expired is canceled first, so prune can give it up even when no app
	// lists the operations (a script that only previews would otherwise grow the list without bound).
	h.expireOps()
	ps.ops.mu.Lock()
	if ps.ops.byID == nil {
		ps.ops.byID = map[string]*extOp{}
	}
	ps.ops.prune(now)
	ps.ops.byID[op.v.ID] = op
	ps.ops.mu.Unlock()
	h.pushOp(op, true)
	return op
}

// prune drops what is over for more than opKeep, and the oldest finished ones over opMost. Called with mu held.
// Running and consent-waiting operations are never dropped: their goroutine still updates them and the apps still
// ask for them, so the list can briefly hold more than opMost of them.
func (o *extensionOps) prune(now time.Time) {
	for id, op := range o.byID {
		if op.v.EndedAt != nil && now.Sub(*op.v.EndedAt) > opKeep {
			delete(o.byID, id)
		}
	}
	if len(o.byID) < opMost {
		return
	}
	ended := make([]*extOp, 0, len(o.byID))
	for _, op := range o.byID {
		if op.v.EndedAt != nil {
			ended = append(ended, op)
		}
	}
	slices.SortFunc(ended, func(a, b *extOp) int { return a.v.EndedAt.Compare(*b.v.EndedAt) })
	for _, op := range ended[:min(len(ended), len(o.byID)-opMost+1)] {
		delete(o.byID, op.v.ID)
	}
}

// update changes op under the lock and tells the apps (a download's bytes at most every opPushEvery).
func (h *Manager) update(op *extOp, f func(v *ExtensionOperation) (changed bool)) {
	ps := h.extensions
	changed := func() bool {
		ps.ops.mu.Lock()
		defer ps.ops.mu.Unlock() // a change that panics must not leave every operation locked
		op.v.UpdatedAt = time.Now()
		return f(&op.v)
	}()
	h.pushOp(op, changed)
}

func (h *Manager) pushOp(op *extOp, now bool) {
	ps := h.extensions
	ps.ops.mu.Lock()
	if !now && time.Since(op.pushed) < opPushEvery {
		ps.ops.mu.Unlock()
		return
	}
	op.pushed = time.Now()
	ps.ops.mu.Unlock()
	v := h.snapshot(op)
	// The hub passes it only to the apps that declared extension_operations, and keeps it out of the event log (a
	// download pushes once a second; an app that comes back lists the operations again): hub/bus_forward.go.
	h.broadcast(map[string]any{"type": "extension_operation", "operation": v})
}

// stepOf is step id of v.
func stepOf(v *ExtensionOperation, id string) *ExtensionOpStep {
	for i := range v.Steps {
		if v.Steps[i].ID == id {
			return &v.Steps[i]
		}
	}
	return nil
}

// setStep moves step id of v to state (a step that starts is the operation's current one).
func setStep(v *ExtensionOperation, id, state string) bool {
	s := stepOf(v, id)
	if s == nil || s.State == state {
		return false
	}
	now := time.Now()
	switch state {
	case extension.StepRunning:
		s.StartedAt, s.EndedAt = &now, nil
		v.Step = id
	case extension.StepDone, extension.StepFailed, extension.StepSkipped:
		if s.StartedAt == nil && state != extension.StepSkipped {
			s.StartedAt = &now
		}
		s.EndedAt = &now
	}
	s.State = state
	return true
}

// opStep moves op's step id to state.
func (h *Manager) opStep(op *extOp, id, state string) {
	h.update(op, func(v *ExtensionOperation) bool { return setStep(v, id, state) })
}

// progress is the ProgressFunc that moves op's steps as a preview reports them.
func (h *Manager) progress(op *extOp) extension.ProgressFunc {
	return func(step string, ev extension.StepEvent) {
		h.update(op, func(v *ExtensionOperation) bool {
			changed := setStep(v, step, ev.State)
			if s := stepOf(v, step); s != nil && step == extension.StepDownload {
				if ev.Bytes > 0 || ev.Total > 0 {
					s.Bytes, s.Total = ev.Bytes, ev.Total
				}
				if ev.Cached {
					s.Cached, changed = true, true
				}
			}
			if s := stepOf(v, step); s != nil && step == extension.StepTools {
				if toolProgress(s, ev) {
					changed = true
				}
				if ev.State == extension.StepDone || ev.State == extension.StepSkipped {
					changed = setStep(v, extension.StepInstall, extension.StepRunning) || changed
				}
			}
			return changed
		})
	}
}

// toolProgress records a tools step event on s: a program's state, and the bytes of the one downloading.
func toolProgress(s *ExtensionOpStep, ev extension.StepEvent) bool {
	if ev.Tool == "" {
		return false
	}
	i := slices.IndexFunc(s.Tools, func(t OpTool) bool { return t.Name == ev.Tool })
	if i < 0 {
		s.Tools = append(s.Tools, OpTool{Name: ev.Tool})
		i = len(s.Tools) - 1
	}
	t := &s.Tools[i]
	changed := t.State != ev.ToolState || t.Reused != ev.Reused
	t.State, t.Reused = ev.ToolState, ev.Reused
	switch {
	case ev.ToolState == extension.StepRunning && ev.Bytes > 0:
		s.Bytes, s.Total, changed = ev.Bytes, ev.Total, true
	case ev.ToolState == extension.StepRunning:
		s.Bytes, s.Total = 0, 0
	}
	return changed
}

// opFail ends op with err, at the step it was at (the first one not done when none was running).
func (h *Manager) opFail(op *extOp, err error) {
	h.update(op, func(v *ExtensionOperation) bool {
		at := ""
		if s := stepOf(v, v.Step); s != nil && s.State == extension.StepRunning {
			at = s.ID
		} else {
			for _, s := range v.Steps {
				if s.State == extension.StepPending || s.State == extension.StepRunning {
					at = s.ID
					break
				}
			}
		}
		if at != "" {
			setStep(v, at, extension.StepFailed)
			v.Step = at
		}
		now := time.Now()
		v.State, v.EndedAt, v.PreviewID = opFailed, &now, ""
		if op.canceling {
			v.State = opCanceled
			if s := stepOf(v, at); s != nil {
				s.State = extension.StepSkipped
			}
			return true
		}
		v.Error, v.ErrorCode = i18n.Localize(err, op.lang), opErrorCode(at, v.Kind, err)
		v.Retryable = op.retry != nil
		return true
	})
}

// codedErr is a failure whose error_code is the hub's own answer's (an uninstall refused with has_dependents).
type codedErr struct{ code, msg string }

func (e codedErr) Error() string { return e.msg }

// opErrorCode is the error_code of a failure at step.
func opErrorCode(step, kind string, err error) string {
	if c, ok := errors.AsType[codedErr](err); ok && c.code != "" {
		return c.code
	}
	switch {
	case errors.Is(err, extension.ErrDownloadStalled):
		return "download_stalled"
	case errors.Is(err, extension.ErrChecksum):
		return "checksum_mismatch"
	case errors.Is(err, errRolledBack):
		return "rolled_back"
	case errors.Is(err, extension.ErrTools):
		return "tools_failed"
	case kind == opUninstall:
		return "uninstall_failed"
	}
	switch step {
	case extension.StepDownload:
		return "download_failed"
	case extension.StepVerify:
		return "checksum_mismatch"
	case extension.StepUnpack, extension.StepCheck:
		return "invalid_package"
	case extension.StepConsent, extension.StepTools, extension.StepInstall:
		return "install_failed"
	case extension.StepStart:
		return "start_failed"
	}
	return ""
}

var errRolledBack = errors.New("rolled back")

// opSucceed ends op: what it did not need to do is skipped.
func (h *Manager) opSucceed(op *extOp) {
	h.update(op, func(v *ExtensionOperation) bool {
		for i := range v.Steps {
			if v.Steps[i].State == extension.StepPending || v.Steps[i].State == extension.StepRunning {
				setStep(v, v.Steps[i].ID, extension.StepSkipped)
			}
		}
		now := time.Now()
		v.State, v.EndedAt, v.Step, v.PreviewID, v.Retryable = opSucceeded, &now, "", "", false
		return true
	})
}

// opPreviewed is op with its preview made: it waits for the user's consent, or goes on when it was retried after
// the user agreed to just this (the same digests). go on installs in the background.
func (h *Manager) opPreviewed(op *extOp, pv *extension.Preview, goOn func(refs []bundleRef)) {
	auto := false
	var refs []bundleRef
	h.update(op, func(v *ExtensionOperation) bool {
		m := pv.Manifest
		v.Extension, v.Name, v.Version = m.ID, m.Name, m.Version
		if pv.Upgrade {
			v.Kind, v.From = opUpgrade, pv.From
		}
		if pv.Market != "" {
			v.Market, v.Source = pv.Market, extension.MarketOrigin(pv.Market, m.ID)
		}
		setStep(v, extension.StepCheck, extension.StepDone)
		auto = op.consented != "" && op.consented == pv.Digest && len(pv.Unresolved) == 0
		for _, b := range pv.Bundle {
			if op.consentedBundle[b.Manifest.ID] != b.Digest {
				auto = false
			}
			refs = append(refs, bundleRef{PreviewID: b.ID, Digest: b.Digest})
		}
		v.PreviewID = pv.ID
		setStep(v, extension.StepConsent, extension.StepRunning)
		if !auto {
			v.State = opNeedsConsent
		}
		return true
	})
	if auto && goOn != nil {
		go goOn(refs)
	}
}

// claimOpOfPreview takes the operation waiting for consent with preview previewID for one install, nil when none
// is. It is marked running at once, under the lock, so a second install of the same preview (a double click, the
// web and the CLI together) cannot go on with it: that one is an installing_already error while the claimed one runs, and
// never reaches the registry, so it can neither end the install nor take the preview from it. installing pushes the
// change.
func (h *Manager) claimOpOfPreview(previewID string) (*extOp, error) {
	if previewID == "" {
		return nil, nil
	}
	ps := h.extensions
	ps.ops.mu.Lock()
	defer ps.ops.mu.Unlock()
	for _, op := range ps.ops.byID {
		switch {
		case op.v.PreviewID == previewID && op.v.State == opNeedsConsent:
			op.v.State, op.v.PreviewID, op.v.UpdatedAt = opRunning, "", time.Now()
			op.claimed = previewID
			return op, nil
		case op.claimed == previewID && op.v.State == opRunning:
			return nil, i18n.Err("hub-extension_operations.installing_already")
		}
	}
	return nil, nil
}

// claimedByMarket says whether an install of a market's extension took over the operation that was waiting for consent
// with preview previewID (claimOpOfPreview): the preview was that market's, also once the install has used it up. It
// is known only while that operation is kept.
func (h *Manager) claimedByMarket(previewID string) bool {
	if previewID == "" {
		return false
	}
	ps := h.extensions
	ps.ops.mu.Lock()
	defer ps.ops.mu.Unlock()
	for _, op := range ps.ops.byID {
		if op.claimed == previewID && op.ofMarket {
			return true
		}
	}
	return false
}

// opForInstall is the operation an install of preview pv goes on with: the one that made the preview, or a new one
// whose first steps are done already (a preview made before operations, or by the admin UI). It fails while another
// install goes on with the preview's operation.
func (h *Manager) opForInstall(pv *extension.Preview, previewID, actor string, l i18n.Lang) (*extOp, error) {
	if op, err := h.claimOpOfPreview(previewID); op != nil || err != nil {
		return op, err
	}
	kind, id := opInstall, ""
	if pv != nil {
		id = pv.Manifest.ID
		if pv.Upgrade {
			kind = opUpgrade
		}
	}
	// A preview that a market's install used up was that market's: a phone may follow and clear this operation too.
	op := h.startOp(kind, id, actor, l, (pv != nil && pv.Market != "") || (pv == nil && h.claimedByMarket(previewID)))
	h.update(op, func(v *ExtensionOperation) bool {
		for _, s := range []string{extension.StepDownload, extension.StepVerify, extension.StepUnpack, extension.StepCheck} {
			setStep(v, s, extension.StepSkipped)
		}
		if pv != nil {
			v.Name, v.Version = pv.Manifest.Name, pv.Manifest.Version
			if pv.Market != "" {
				v.Market, v.Source = pv.Market, extension.MarketOrigin(pv.Market, pv.Manifest.ID)
			}
		}
		return true
	})
	return op, nil
}

// installing marks op as agreed to (digest and its bundle's) and installing.
func (h *Manager) installing(op *extOp, pv *extension.Preview, digest string) {
	h.update(op, func(v *ExtensionOperation) bool {
		op.consented = digest
		op.consentedBundle = map[string]string{}
		if pv != nil {
			if op.consented == "" {
				op.consented = pv.Digest // an upgrade asking for nothing more needs no digest
			}
			for _, b := range pv.Bundle {
				op.consentedBundle[b.Manifest.ID] = b.Digest
			}
		}
		v.State, v.PreviewID = opRunning, ""
		setStep(v, extension.StepConsent, extension.StepDone)
		setStep(v, extension.StepTools, extension.StepRunning) // the registry says done or skipped, then install runs
		return true
	})
}

// settle is what comes after an install or upgrade: it waits for the extension to come up, then for its program to
// reach the computers it runs on, and ends op.
func (h *Manager) settle(ctx context.Context, op *extOp, p *extension.Extension, upgrade bool) {
	ps := h.extensions
	id := p.Record.ID
	h.opStep(op, extension.StepInstall, extension.StepDone)
	h.opStep(op, extension.StepStart, extension.StepRunning)
	if err := h.waitStarted(ctx, id, upgrade, op); err != nil {
		h.opFail(op, err)
		return
	}
	h.opStep(op, extension.StepStart, extension.StepDone)
	if cur := ps.reg.Snapshot(id); cur != nil && cur.Manifest.NodeProgram() != nil {
		h.syncNodesFor(ctx, op, cur)
	}
	if h.needsSignIn(ctx, id) {
		h.update(op, func(v *ExtensionOperation) bool { v.Next = "auth"; return true })
	}
	h.opSucceed(op)
}

// waitStarted waits until extension id runs (or needs nothing to: no program, started by its first call, switched
// off, waiting for its settings). An upgrade that does not come up is rolled back by watchUpgrade: that is the
// failure then.
func (h *Manager) waitStarted(ctx context.Context, id string, upgrade bool, op *extOp) error {
	ps := h.extensions
	hp := ps.get(id)
	if hp == nil || hp.proc == nil {
		return nil
	}
	wait := host.ReadyWithin + 5*time.Second
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	name := op.v.Name
	for {
		state, why := hp.proc.Status()
		switch state {
		case host.StateRunning, host.StateStopped:
			return nil
		case host.StateError:
			if upgrade {
				return localizedKind(errRolledBack, "hub-extension_operations.rolled_back", i18n.Args{"name": name, "to": op.v.Version, "from": op.v.From, "why": why})
			}
			return i18n.Err("hub-extension_operations.start_failed", i18n.Args{"name": name, "why": why})
		}
		select {
		case <-ctx.Done():
			return i18n.Err("hub-extension_operations.start_timeout", i18n.Args{"name": name, "seconds": int(wait / time.Second)})
		case <-hp.proc.Changes():
		}
	}
}

// syncNodesFor sends p's program to (or takes it off) every online computer it is for, and waits for each.
func (h *Manager) syncNodesFor(ctx context.Context, op *extOp, p *extension.Extension) {
	ps := h.extensions
	id := p.Record.ID
	var machines []*Machine
	for _, m := range h.machinesNow() {
		if m.isOnline() {
			machines = append(machines, m)
		}
	}
	h.update(op, func(v *ExtensionOperation) bool {
		setStep(v, extension.StepNodes, extension.StepRunning)
		stepOf(v, extension.StepNodes).Machines = &OpMachines{Total: len(machines)}
		return true
	})
	var wg sync.WaitGroup
	for _, m := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.syncExtensionNodes(ctx, m) // after the one the change started (one at a time per machine)
			why := ""
			for _, st := range ps.nodes.views(id) {
				if st.MachineID == m.cfg.ID && st.Error != "" {
					why = st.Error
				}
			}
			h.update(op, func(v *ExtensionOperation) bool {
				ms := stepOf(v, extension.StepNodes).Machines
				ms.Done++
				if why != "" {
					if ms.Errors == nil {
						ms.Errors = map[string]string{}
					}
					ms.Errors[m.cfg.ID] = why
				}
				return true
			})
		}()
	}
	wg.Wait()
	h.opStep(op, extension.StepNodes, extension.StepDone)
}

// opsView is the operations to list (newest first), with an expired preview's one canceled.
func (h *Manager) opsView() []ExtensionOperation {
	ps := h.extensions
	h.expireOps()
	ps.ops.mu.Lock()
	ps.ops.prune(time.Now())
	ops := slices.Collect(maps.Values(ps.ops.byID))
	ps.ops.mu.Unlock()
	out := []ExtensionOperation{}
	for _, op := range ops {
		out = append(out, h.snapshot(op))
	}
	slices.SortFunc(out, func(a, b ExtensionOperation) int { return b.StartedAt.Compare(a.StartedAt) })
	return out
}

// expireOps cancels the operations whose preview is gone (expired, or installed some other way).
func (h *Manager) expireOps() {
	ps := h.extensions
	ps.ops.mu.Lock()
	var gone []*extOp
	for _, op := range ps.ops.byID {
		if op.v.State == opNeedsConsent && ps.reg.PreviewOf(op.v.PreviewID) == nil {
			gone = append(gone, op)
		}
	}
	ps.ops.mu.Unlock()
	for _, op := range gone {
		h.cancelOp(op)
	}
}

func (h *Manager) cancelOp(op *extOp) {
	h.update(op, func(v *ExtensionOperation) bool {
		if v.State != opNeedsConsent {
			return false
		}
		setStep(v, extension.StepConsent, extension.StepSkipped)
		now := time.Now()
		v.State, v.EndedAt, v.PreviewID, v.Step = opCanceled, &now, "", ""
		return true
	})
}

func (h *Manager) opByID(id string) *extOp {
	ps := h.extensions
	ps.ops.mu.Lock()
	defer ps.ops.mu.Unlock()
	return ps.ops.byID[id]
}

// withOperation is v's JSON with "operation_id" added (the synchronous answers name their operation too).
func withOperation(v any, op *extOp) any {
	if op == nil {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return v
	}
	m["operation_id"], _ = json.Marshal(op.v.ID) // set before the operation is shared, never changed
	return m
}

// snapshot is a copy of op as it is now.
func (h *Manager) snapshot(op *extOp) ExtensionOperation {
	h.extensions.ops.mu.Lock()
	defer h.extensions.ops.mu.Unlock()
	v := op.v
	v.Steps = slices.Clone(v.Steps)
	v.Warnings = slices.Clone(v.Warnings)
	for i, s := range v.Steps {
		v.Steps[i].Tools = slices.Clone(s.Tools) // toolProgress writes its elements in place
		if s.Machines != nil {
			ms := *s.Machines
			ms.Errors = maps.Clone(ms.Errors)
			v.Steps[i].Machines = &ms
		}
	}
	return v
}

// writeOp answers 202 with op.
func (h *Manager) writeOp(w http.ResponseWriter, op *extOp) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	raw, err := json.Marshal(map[string]any{"operation": h.snapshot(op)})
	if err == nil {
		_, _ = w.Write(i18n.ExtensionClientJSON(i18n.OfWriter(w), raw))
	}
}

// operationRoutes list, show, retry and cancel the operations. Who may is who may start them: a full-rights device
// for a market's extension, the owner over loopback for everything.
func (h *Manager) operationRoutes(api *http.ServeMux) {
	may := func(r *http.Request, op *extOp) bool {
		return h.fromThisMachine(r) || (principalOf(r.Context()).Role == roleFull && (op == nil || op.ofMarket))
	}
	gate := func(f func(w http.ResponseWriter, r *http.Request, op *extOp)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if h.extensions == nil || h.extensions.reg == nil {
				writeExtensionResponseError(w, r, http.StatusServiceUnavailable, "no_plugins", errors.New("plugins are not set up"))
				return
			}
			var op *extOp
			if id := r.PathValue("op"); id != "" {
				if op = h.opByID(id); op == nil {
					writeExtensionResponseError(w, r, http.StatusNotFound, "not_found", i18n.Err("hub-extension_operations.not_found"))
					return
				}
			}
			if !may(r, op) {
				writeExtensionResponseError(w, r, http.StatusForbidden, "this_machine_only", i18n.Err("hub-extension_routes.market_only"))
				return
			}
			f(w, r, op)
		}
	}
	api.HandleFunc("GET /api/v1/extensions/-/operations", gate(func(w http.ResponseWriter, r *http.Request, _ *extOp) {
		writeExtensionJSON(w, r, map[string]any{"operations": h.opsView()})
	}))
	api.HandleFunc("GET /api/v1/extensions/-/operations/{op}", gate(func(w http.ResponseWriter, r *http.Request, op *extOp) {
		h.expireOps()
		ps := h.extensions
		v := h.snapshot(op)
		if v.State == opNeedsConsent {
			if pv := ps.reg.PreviewOf(v.PreviewID); pv != nil {
				v.Preview = pv.In(h.requestLang(r))
			}
		}
		writeExtensionJSON(w, r, v)
	}))
	api.HandleFunc("POST /api/v1/extensions/-/operations/{op}/retry", gate(func(w http.ResponseWriter, r *http.Request, op *extOp) {
		ps := h.extensions
		// Taken under the lock, so a failed operation is retried once: a second POST (a double tap, a second app)
		// finds it gone and starts nothing; the new operation carries on.
		ps.ops.mu.Lock()
		state, retry := op.v.State, op.retry
		if state == opFailed && retry != nil {
			op.retry, op.v.Retryable = nil, false
		}
		ps.ops.mu.Unlock()
		switch {
		case state != opFailed:
			writeExtensionResponseError(w, r, http.StatusConflict, "not_failed", i18n.Err("hub-extension_operations.not_failed"))
			return
		case retry == nil:
			writeExtensionResponseError(w, r, http.StatusConflict, "not_retryable", i18n.Err("hub-extension_operations.not_retryable"))
			return
		}
		h.pushOp(op, true) // the apps hide its Retry
		next, err := retry()
		if err != nil {
			// It did not start: give the retry back, so the user can try again.
			ps.ops.mu.Lock()
			op.retry, op.v.Retryable = retry, true
			ps.ops.mu.Unlock()
			h.pushOp(op, true)
			writeExtensionResponseError(w, r, http.StatusBadRequest, "retry_failed", err)
			return
		}
		h.writeOp(w, next)
	}))
	api.HandleFunc("DELETE /api/v1/extensions/-/operations/{op}", gate(func(w http.ResponseWriter, r *http.Request, op *extOp) {
		ps := h.extensions
		ps.ops.mu.Lock()
		state, previewID, cancel := op.v.State, op.v.PreviewID, op.cancel
		consent := stepOf(&op.v, extension.StepConsent) // an uninstall has none
		previewing := consent != nil && consent.State == extension.StepPending
		if state == opRunning && previewing && cancel != nil {
			op.canceling = true
		}
		ps.ops.mu.Unlock()
		switch {
		case state == opRunning && previewing && cancel != nil:
			cancel() // the preview fails with it, and the operation is canceled
		case state == opRunning:
			writeExtensionResponseError(w, r, http.StatusConflict, "cannot_cancel", i18n.Err("hub-extension_operations.cannot_cancel"))
			return
		case state == opNeedsConsent:
			ps.reg.Discard(previewID)
			h.cancelOp(op)
		default:
			ps.ops.mu.Lock()
			delete(ps.ops.byID, op.v.ID)
			ps.ops.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// previewJob is a preview to run as an operation: what it is of, and how (again, for a retry).
type previewJob struct {
	kind, id string // opInstall or opUpgrade; the extension (as far as it is known before)
	ofMarket bool
	actor    string
	lang     i18n.Lang
	run      func(ctx context.Context) (*extension.Preview, error)
}

// launch runs job as a new operation: in the background when async (its answer is nil, nil), else before it returns.
// from is the failed operation it retries: what the user agreed to there carries over.
func (h *Manager) launch(ctx context.Context, job previewJob, from *extOp, async bool) (*extOp, *extension.Preview, error) {
	op := h.startOp(job.kind, job.id, job.actor, job.lang, job.ofMarket)
	h.extensions.ops.mu.Lock()
	op.retry = func() (*extOp, error) {
		next, _, _ := h.launch(i18n.With(context.Background(), job.lang), job, op, true)
		return next, nil
	}
	if from != nil {
		op.consented, op.consentedBundle = from.consented, from.consentedBundle
	}
	h.extensions.ops.mu.Unlock()
	if async {
		ctx = context.WithoutCancel(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	h.extensions.ops.mu.Lock()
	op.cancel = cancel
	h.extensions.ops.mu.Unlock()
	do := func() (*extension.Preview, error) {
		defer cancel()
		pv, err := job.run(extension.WithProgress(ctx, h.progress(op)))
		if err != nil {
			h.opFail(op, err)
			return nil, err
		}
		h.opPreviewed(op, pv, func(refs []bundleRef) {
			upgrade := ""
			if pv.Upgrade {
				upgrade = pv.Manifest.ID
				if pv.RenameFrom != "" {
					upgrade = pv.RenameFrom
				}
			}
			_, _ = h.installOp(i18n.With(context.Background(), job.lang), op, upgrade, pv.ID, pv.Digest, refs, job.actor)
		})
		return pv, nil
	}
	if async {
		go func() { _, _ = do() }()
		return op, nil, nil
	}
	pv, err := do()
	return op, pv, err
}

// installOp installs (upgrade "") or upgrades (upgrade: its id) the previewed previewID as op, as the install and
// upgrade calls do; what comes after (start, the computers) goes on in the background.
func (h *Manager) installOp(ctx context.Context, op *extOp, upgrade, previewID, digest string, refs []bundleRef, actor string) (*extension.Extension, error) {
	reg := h.extensions.reg
	pv := reg.PreviewOf(previewID)
	if pv != nil && pv.RenameFrom != "" {
		if upgrade != "" && upgrade != pv.Manifest.ID && reg.CanonicalID(upgrade) != pv.RenameFrom {
			return nil, i18n.Err("extension-identity.invalid")
		}
		upgrade = pv.RenameFrom
	}
	h.installing(op, pv, digest)
	p, err := h.installExtensionPlan(extension.WithProgress(ctx, h.progress(op)), upgrade, previewID, digest, refs, actor) // members and it together
	if err != nil {
		h.opFail(op, err)
		return nil, err
	}
	go h.settle(context.WithoutCancel(ctx), op, p, upgrade != "")
	return p, nil
}

// uninstallOp is an uninstall as an operation (async: in the background, the answer 202 with it). The answer of a
// synchronous one is the one it always was.
func (h *Manager) uninstallOp(w http.ResponseWriter, r *http.Request, id string, o uninstallOptions, async bool) {
	ps := h.extensions
	p := ps.reg.Get(id)
	if p == nil {
		h.uninstallPack(w, r, id, o) // its 404
		return
	}
	op := h.startOp(opUninstall, id, opActor(r, o.actor), h.requestLang(r), extension.FromMarket(p.Record.Origin) != "")
	run := func(r *http.Request) *opRecorder {
		h.opStep(op, extension.StepStop, extension.StepRunning)
		rec := &opRecorder{header: http.Header{}, status: http.StatusOK}
		h.uninstallPack(rec, r, id, o)
		if rec.status >= 300 {
			var e struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.body, &e)
			h.opFail(op, codedErr{code: e.Code, msg: e.Error})
			return rec
		}
		h.opStep(op, extension.StepStop, extension.StepDone)
		h.opStep(op, extension.StepRemove, extension.StepDone)
		if p.Manifest.NodeProgram() != nil {
			h.syncNodesFor(context.WithoutCancel(r.Context()), op, p)
		}
		// The answer's warnings (files left to delete by hand) go on the operation, as the synchronous answer has them.
		var out struct {
			Warnings []string `json:"warnings"`
		}
		_ = json.Unmarshal(rec.body, &out)
		if len(out.Warnings) > 0 {
			h.update(op, func(v *ExtensionOperation) bool {
				v.Warnings = out.Warnings
				return false // opSucceed pushes it
			})
		}
		h.opSucceed(op)
		return rec
	}
	ps.ops.mu.Lock()
	op.retry = func() (*extOp, error) {
		bg := r.Clone(context.WithoutCancel(r.Context()))
		rec := &opRecorder{header: http.Header{}, status: http.StatusOK}
		h.uninstallOp(rec, bg, id, o, true)
		var out struct {
			Operation ExtensionOperation `json:"operation"`
		}
		_ = json.Unmarshal(rec.body, &out)
		if next := h.opByID(out.Operation.ID); next != nil {
			return next, nil
		}
		return nil, i18n.Err("hub-extension_operations.not_found")
	}
	ps.ops.mu.Unlock()
	if async {
		bg := r.Clone(context.WithoutCancel(r.Context()))
		go run(bg)
		h.writeOp(w, op)
		return
	}
	rec := run(r)
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body)
}

// opRecorder keeps an answer the hub writes, to read it (and pass it on) after.
type opRecorder struct {
	header http.Header
	status int
	body   []byte
	wrote  bool
}

func (o *opRecorder) Header() http.Header { return o.header }
func (o *opRecorder) WriteHeader(code int) {
	if !o.wrote {
		o.status, o.wrote = code, true
	}
}
func (o *opRecorder) Write(b []byte) (int, error) {
	o.wrote = true
	o.body = append(o.body, b...)
	return len(b), nil
}

// opActor is who an operation is shown as started by: the device's name, or def (the CLI, the hub).
func opActor(r *http.Request, def string) string {
	if p := principalOf(r.Context()); p.Name != "" {
		return p.Name
	}
	return actorOr(r, def)
}
