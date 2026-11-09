package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// readinessDevicesHost waits for the real device layer to report its initial machine list.
type readinessDevicesHost struct {
	reported chan struct{}
	once     sync.Once
}

func (*readinessDevicesHost) Call(context.Context, string, any, any) error {
	return errors.New("the test hub has no node connection")
}

func (h *readinessDevicesHost) Notify(event string, _ any) error {
	if event == devicewire.EventState {
		h.once.Do(func() { close(h.reported) })
	}
	return nil
}

// The hub hands every pending ID to a manager, including computers another manager owns. Readiness must take
// names only from its own device layer, and follow a new handover immediately after confirmation.
func TestReadinessPendingDeviceHandover(t *testing.T) {
	host := &readinessDevicesHost{reported: make(chan struct{})}
	d := devices.New(devices.Options{Hub: host})
	cfg := devicewire.Config{HubID: "test-hub", Machines: []json.RawMessage{
		json.RawMessage(`{"id":"own-laptop","name":"Example Laptop","kind":"link"}`),
	}, Pending: []string{"another-manager", "own-laptop"}}
	d.Configure(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("device layer did not stop")
		}
	})
	select {
	case <-host.reported:
	case <-time.After(5 * time.Second):
		t.Fatal("device layer did not report its machines")
	}

	f := newFakeHub()
	s := withSettings(t, f, New(Options{Hub: f, Devices: d, Trust: d, Logf: testLogf(t)}))
	check := func(wantCode, wantMachine string) {
		t.Helper()
		r := getReadiness(t, s, "probe=1", "en")
		step := readinessStep(t, r, StepComputer)
		if r.Ready || r.Mode != ReadinessSetup || step.Code != wantCode || step.Machine != wantMachine || r.Probe != nil {
			t.Fatalf("readiness after handover: %+v", r)
		}
		if wantMachine != "" && (r.Machine == nil || r.Machine.ID != wantMachine || r.Machine.Name != "Example Laptop") {
			t.Fatalf("readiness lost the owned computer's identity: %+v", r.Machine)
		}
		if wantCode == proto.CodeMachinePending && (step.Action == nil || step.Action.ID != "confirm_computer") {
			t.Fatalf("pending action: %+v", step)
		}
	}
	check(proto.CodeMachinePending, "own-laptop")
	status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", "en", `{"prompt":"Hello","main":true}`)
	if status != 409 || body["code"] != proto.CodeMachinePending {
		t.Fatalf("starting before confirmation: %d %v", status, body)
	}

	// A failed trust read must not keep offering the previous confirmation target.
	cfg.PendingUnknown = true
	d.Configure(cfg)
	check(proto.CodeNoMachine, "")
	// Another manager's pending computer does not become this manager's confirmation target.
	cfg.PendingUnknown, cfg.Pending = false, []string{"another-manager"}
	d.Configure(cfg)
	check(proto.CodeNoMachine, "")

	cfg.Pending = []string{"another-manager", "own-laptop"}
	d.Configure(cfg)
	check(proto.CodeMachinePending, "own-laptop")
	// Confirmation changes the handover and the hub's filtered list. The same computer may still be offline.
	cfg.Pending = []string{"another-manager"}
	d.Configure(cfg)
	f.mu.Lock()
	f.hello.Assistant.MachineID = "own-laptop"
	f.devices = []Device{{ID: "own-laptop", Name: "Example Laptop", Kind: "link"}}
	f.mu.Unlock()
	check(codeNodeUnreachable, "own-laptop")
}
