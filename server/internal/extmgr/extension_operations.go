package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Installs, upgrades and uninstalls as operations with steps (docs/protocol/protocol.md "Extension install, upgrade and
// uninstall progress", #3277): each one is kept here while it runs and for opKeep after, and every change of a step
// goes to the apps as an extension_operation event, so a page closed and opened again still shows an install in
// progress. They are only in this manager's memory: a manager that restarts forgets them (what was not installed yet
// is not installed) and sends no final event, so the apps let go of one it no longer lists or answers 404 for. A hub
// restart forgets them only when it restarts this manager too (the hub's own child); in service mode they survive it.

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
