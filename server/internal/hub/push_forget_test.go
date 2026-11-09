package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// forgetStore is a database whose device row delete either hangs until the caller gives up (a locked row) or fails.
type forgetStore struct {
	Store
	entered chan struct{}
	fail    error
}

func (s *forgetStore) Devices(context.Context) ([]Device, error) { return nil, nil }

func (s *forgetStore) SetDevicePrefs(context.Context, string, string, json.RawMessage) error {
	return nil
}

func (s *forgetStore) DeleteDevice(ctx context.Context, _ string) error {
	if s.fail != nil {
		return s.fail
	}
	close(s.entered)
	<-ctx.Done()
	return ctx.Err()
}

// logLines collects what the pusher logs.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Dropping a dead token's device whose row delete hangs gives up after storeTimeout, logs it, and does not hold
// saveMu forever: another device's settings write still goes through.
func TestForgetIfCurrentStuckStoreGivesUp(t *testing.T) {
	store := &forgetStore{entered: make(chan struct{})}
	logs := &logLines{}
	p := newPusher(t.TempDir(), store, logs.logf)
	p.storeTimeout = 300 * time.Millisecond
	dead := Device{DeviceID: "dev-a", Name: "Phone A", APNSToken: "tok-a"}
	p.devices[dead.DeviceID] = dead

	done := make(chan struct{})
	go func() { p.forgetIfCurrent(dead); close(done) }()
	<-store.entered

	prefs := make(chan error, 1)
	go func() { prefs <- p.setPrefs("dev-b", NotifyPrefs{Input: false, Done: DoneOff}) }()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forgetIfCurrent did not give up after storeTimeout")
	}
	select {
	case err := <-prefs:
		if err != nil {
			t.Fatalf("setPrefs dev-b: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("setPrefs for another device blocked behind a stuck device delete")
	}
	if _, ok := p.device("dev-a"); ok {
		t.Fatal("dev-a is still in memory after its token was gone")
	}
	if !logs.has("forget device Phone A: " + context.DeadlineExceeded.Error()) {
		t.Fatalf("no log line for the timed-out delete; logged %q", logs.lines)
	}
}

// A device row delete that fails is logged, for a dead token and for a signed-out device alike: the row stays and
// the device comes back on the next start.
func TestForgetStoreErrorIsLogged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		forget func(p *Pusher, d Device)
	}{
		{"Gone", func(p *Pusher, d Device) { p.forgetIfCurrent(d) }},
		{"SignedOut", func(p *Pusher, d Device) { p.signedOut(d.Credential) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &forgetStore{fail: errors.New("row locked")}
			logs := &logLines{}
			p := newPusher(t.TempDir(), store, logs.logf)
			d := Device{DeviceID: "dev-a", Name: "Phone A", APNSToken: "tok-a", Credential: "cred-a"}
			p.devices[d.DeviceID] = d
			tc.forget(p, d)
			if _, ok := p.device("dev-a"); ok {
				t.Fatal("dev-a is still in memory")
			}
			if !logs.has("forget device Phone A: row locked") {
				t.Fatalf("no log line for the failed delete; logged %q", logs.lines)
			}
		})
	}
}

// bookkeepingOf reports which of the pusher's per-device maps still hold id.
func bookkeepingOf(p *Pusher, id string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var left []string
	if _, ok := p.failed[id]; ok {
		left = append(left, "failed")
	}
	if _, ok := p.replaced[id]; ok {
		left = append(left, "replaced")
	}
	if p.acceptedDirty[id] {
		left = append(left, "acceptedDirty")
	}
	return left
}

// Signing out a device that had a push accepted, a push fail and a renewed grant clears all of its bookkeeping, so
// the next maintenance pass writes no row for it: not its own, and not a ghost row with an empty id.
func TestSignedOutClearsPushBookkeeping(t *testing.T) {
	rows := &deviceRows{rows: map[string]db.Device{}}
	p := newPusher(t.TempDir(), rows, t.Logf)
	d := Device{DeviceID: "phone", Name: "Phone", APNSToken: "tok", Credential: "cred-a"}
	if err := p.register(d); err != nil {
		t.Fatal(err)
	}
	p.pushed(d, nil)
	p.pushed(d, errors.New("APNs refused"))
	p.mu.Lock()
	p.replaced[d.DeviceID] = "old-grant"
	p.mu.Unlock()

	p.signedOut("cred-a")
	if left := bookkeepingOf(p, d.DeviceID); len(left) > 0 {
		t.Fatalf("signed-out device still in %v", left)
	}
	if err := p.reconcileDevices(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := rows.row(""); ok {
		t.Fatal("maintenance wrote a devices row with an empty id")
	}
	if _, ok, _ := rows.row(d.DeviceID); ok {
		t.Fatal("maintenance wrote the signed-out device's row back")
	}
	for _, id := range []string{d.DeviceID, ""} {
		if _, ok := p.routeOfID(id); ok {
			t.Fatalf("routeOfID(%q) still finds a device", id)
		}
	}
}

// Maintenance skips an accepted push whose device is no longer registered instead of upserting a zero Device, and
// drops the stale mark.
func TestReconcileSkipsForgottenDirtyDevice(t *testing.T) {
	rows := &deviceRows{rows: map[string]db.Device{}}
	p := newPusher(t.TempDir(), rows, t.Logf)
	p.mu.Lock()
	p.acceptedDirty = map[string]bool{"gone": true}
	p.mu.Unlock()

	if err := p.reconcileDevices(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows.mu.Lock()
	n := len(rows.rows)
	rows.mu.Unlock()
	if n != 0 {
		t.Fatalf("maintenance wrote %d rows for a device that is not registered", n)
	}
	if left := bookkeepingOf(p, "gone"); len(left) > 0 {
		t.Fatalf("forgotten device still in %v", left)
	}
	if _, ok := p.routeOfID(""); ok {
		t.Fatal("a device with an empty id is in memory")
	}
}
