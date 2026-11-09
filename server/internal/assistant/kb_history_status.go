package assistant

import (
	"path/filepath"
	"sync"
	"time"
)

// ---- the log and the status ----

func (d *historyDriver) log(l historyLogLine) {
	if l.At.IsZero() {
		l.At = d.now()
	}
	if l.Source == "" {
		l.Source = d.source
	}
	l.historyReason = l.historyReason.logged()
	if err := appendHistoryLog(d.data, d.zone, l); err != nil {
		d.logf("kb history: log: %v", err)
	}
}

// historySourceStatus is a source's line of history/status.json.
type historySourceStatus struct {
	State     string          `json:"state,omitempty"` // running, resting, quiet, paused, stopped, done, idle, unavailable
	Since     time.Time       `json:"since,omitzero"`
	Until     time.Time       `json:"until,omitzero"` // the end of a rest, quiet period or pause
	Calls     int             `json:"calls,omitempty"`
	LastCall  *historyLogLine `json:"last_call,omitempty"`
	LastSlow  *historyEvent   `json:"last_slow,omitempty"`
	LastPause *historyEvent   `json:"last_pause,omitempty"`
	LastStop  *historyEvent   `json:"last_stop,omitempty"`
	// SlowUntil: kb_history_resume with slow: the slowest pace until then. SlowPending: asked for, and the two hours
	// start with the next call (the first after the resume).
	SlowUntil   time.Time `json:"slow_until,omitzero"`
	SlowPending bool      `json:"slow_pending,omitempty"`
	// Pace is the pacer's state, so a restart goes on at the same pace (kb_history_pace.go).
	Pace *historyPaceState `json:"pace,omitempty"`
}

type historyEvent struct {
	At time.Time `json:"at"`
	historyReason
}

// paceState is the pacer's state with the pause the driver counts, for status.json.
func (d *historyDriver) paceState() *historyPaceState {
	if d.pacer == nil {
		return nil
	}
	ps := d.pacer.snapshot()
	ps.PausedSince = d.pausedSince
	return &ps
}

func historyStatusPath(data string) string { return filepath.Join(historyDir(data), "status.json") }

var historyStatusMu sync.Mutex

func readHistoryStatus(data string) map[string]historySourceStatus {
	out := map[string]historySourceStatus{}
	_, _ = readJSONFile(historyStatusPath(data), &out)
	return out
}

func updateHistoryStatus(data, source string, f func(*historySourceStatus)) {
	historyStatusMu.Lock()
	defer historyStatusMu.Unlock()
	all := readHistoryStatus(data)
	st := all[source]
	f(&st)
	all[source] = st
	_ = writeJSONFile(historyStatusPath(data), all)
}

func (d *historyDriver) status(source string) historySourceStatus {
	return readHistoryStatus(d.data)[source]
}

func (d *historyDriver) setStatus(source string, f func(*historySourceStatus)) {
	if source == "" {
		return
	}
	updateHistoryStatus(d.data, source, f)
}

func orText(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
