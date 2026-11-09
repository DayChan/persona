package assistant

import (
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/proto"
)

type readinessPendingDevices struct {
	fakeDevices
	list []devices.Summary
}

func (d *readinessPendingDevices) List() []devices.Summary { return d.list }

func TestReadinessPendingComputer(t *testing.T) {
	f := newFakeHub()
	trust := &trustStore{pending: []string{"computer"}}
	s := newServiceTrust(t, f, &memStore{}, trust)
	s.devs = &readinessPendingDevices{list: []devices.Summary{{ID: "computer", Name: "My computer", Online: true, Reachable: true}}}
	for _, lang := range []string{"en", "zh-Hans", "ja"} {
		r := getReadiness(t, s, "probe=1", lang)
		c := readinessStep(t, r, StepComputer)
		if r.Ready || r.Mode != ReadinessSetup || nextOf(r) != StepComputer || r.Probe != nil ||
			r.Machine == nil || r.Machine.ID != "computer" || r.Machine.Name != "My computer" ||
			c.State != StepTodo || c.Code != proto.CodeMachinePending || c.Machine != "computer" ||
			c.Action == nil || c.Action.ID != "confirm_computer" {
			t.Fatalf("%s: readiness = %+v", lang, r)
		}
		if c.Title == "assistant-readiness.machine_pending_title" || c.Detail == "assistant-readiness.machine_pending_detail" {
			t.Fatalf("%s: missing localized copy: %+v", lang, c)
		}
		for _, step := range r.Steps[1:] {
			if step.State != StepUnknown {
				t.Fatalf("dependent step: %+v", step)
			}
		}
		status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", lang, `{"prompt":"Hello","main":true}`)
		if status != 409 || body["code"] != proto.CodeMachinePending || body["action"] != "confirm_computer" ||
			body["delivered"] != "none" || body["machine_name"] != "My computer" {
			t.Fatalf("%s: start = %d %v", lang, status, body)
		}
	}
	for _, call := range f.calls {
		if call.Device != DeviceAll {
			t.Fatalf("called pending computer: %+v", call)
		}
	}
	// A failed trust read must not reuse a pending computer that may already have been confirmed or removed.
	trust.set(nil, errors.New("trust unavailable"))
	r := getReadiness(t, s, "", "en")
	if r.Machine != nil || readinessStep(t, r, StepComputer).Code != proto.CodeNoMachine {
		t.Fatalf("unknown trust = %+v", r)
	}
}

func TestReadinessPendingComputerSelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending []string
		own     []devices.Summary
		want    string
	}{
		{name: "stable order", pending: []string{"z", "a"}, own: []devices.Summary{{ID: "z"}, {ID: "a"}}, want: "a"},
		{name: "other manager", pending: []string{"foreign"}, own: []devices.Summary{{ID: "own"}}},
		{name: "deleted", pending: []string{"deleted"}},
		{name: "work computer", pending: []string{"work"}, own: []devices.Summary{{ID: "work", Trust: "work"}}},
		{name: "not pending", own: []devices.Summary{{ID: "own"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub()
			s := newServiceTrust(t, f, &memStore{}, &trustStore{pending: tc.pending})
			s.devs = &readinessPendingDevices{list: tc.own}
			r := getReadiness(t, s, "", "en")
			if tc.want == "" {
				if r.Machine != nil || readinessStep(t, r, StepComputer).Code != proto.CodeNoMachine {
					t.Fatalf("unexpected pending computer: %+v", r)
				}
			} else if r.Machine == nil || r.Machine.ID != tc.want || r.Machine.Name != tc.want {
				t.Fatalf("pending computer = %+v", r.Machine)
			}
		})
	}
}

func TestReadinessConfirmedComputerPrecedesPending(t *testing.T) {
	f, s, _ := readyMachine(t)
	s.trustSrc = &trustStore{pending: []string{"pending"}}
	s.devs = &readinessPendingDevices{list: []devices.Summary{{ID: "server"}, {ID: "pending"}}}
	f.devices = []Device{{ID: "server", Name: "My server", Online: false, Reachable: false}}
	r := getReadiness(t, s, "", "en")
	if r.Machine == nil || r.Machine.ID != "server" || readinessStep(t, r, StepComputer).Code != codeNodeUnreachable {
		t.Fatalf("confirmed offline computer = %+v", r)
	}
	f.devices[0].Online, f.devices[0].Reachable = true, true
	r = getReadiness(t, s, "", "en")
	if r.Machine == nil || r.Machine.ID != "server" || readinessStep(t, r, StepComputer).State != StepOK {
		t.Fatalf("confirmed online computer = %+v", r)
	}
}
