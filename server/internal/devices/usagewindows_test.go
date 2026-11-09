package devices

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func uw(label string, minutes int, used float64, resets time.Time) proto.UsageWindow {
	return proto.UsageWindow{Label: label, WindowMinutes: minutes, UsedPercent: used, ResetsAt: &resets}
}

func TestMergeUsageAccountsDedupesTheSameAccountAcrossMachines(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h5, d7 := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	other5, other7 := now.Add(4*time.Hour), now.Add(5*24*time.Hour)
	in := []proto.UsageAccount{
		// machine A saw the account a while ago, machine B saw it just now
		{Provider: "codex", Name: "Codex Plus", Plan: "plus", SampledAt: now.Add(-2 * time.Hour), Windows: []proto.UsageWindow{uw("5h", 300, 10, h5), uw("7d", 10080, 20, d7)}},
		{Provider: "codex", Name: "Codex Plus", Plan: "plus", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("5h", 300, 30, h5), uw("7d", 10080, 25, d7)}},
		// another account: other reset times
		{Provider: "codex", Name: "Codex Plus", Plan: "plus", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("5h", 300, 5, other5), uw("7d", 10080, 90, other7)}},
		// the same reset second under another provider is not the same account
		{Provider: "claude", Name: "Claude", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 1, d7)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 3 {
		t.Fatalf("accounts: %+v", got)
	}
	// the most used first
	if got[0].Windows[1].UsedPercent != 90 {
		t.Errorf("first = %+v", got[0])
	}
	var merged *proto.UsageAccount
	for i := range got {
		if got[i].Provider == "codex" && got[i].Windows[0].UsedPercent == 30 {
			merged = &got[i]
		}
	}
	if merged == nil || merged.Windows[1].UsedPercent != 25 || !merged.SampledAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("the newest sample must win: %+v", got)
	}
	// two accounts with one name are numbered (which one gets the 2 follows their ids, not their numbers)
	names := map[string]bool{}
	for _, a := range got {
		names[a.Name] = true
	}
	if !names["Codex Plus"] || !names["Codex Plus 2"] || !names["Claude"] || len(names) != 3 {
		t.Errorf("names: %v", names)
	}
	for _, a := range got {
		if a.ID == "" || a.Stale {
			t.Errorf("account %+v", a)
		}
	}
}

func TestMergeUsageAccountsDropsResetStaleAndOld(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "reset", SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 99, now.Add(-time.Second))}},
		{Provider: "codex", Name: "old", SampledAt: now.Add(-usageHubMaxAge - time.Hour), Windows: []proto.UsageWindow{uw("7d", 10080, 1, now.Add(time.Hour))}},
		{Provider: "codex", Name: "stale", SampledAt: now.Add(-usageHubStaleAfter - time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 80, now.Add(time.Hour))}},
		{Provider: "claude", Name: "fresh", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 5, now.Add(time.Hour))}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 || got[0].Name != "fresh" || got[1].Name != "stale" || !got[1].Stale {
		t.Fatalf("got %+v", got)
	}
	if got := mergeUsageAccounts(nil, now); got == nil || len(got) != 0 {
		t.Errorf("nothing merges to [] not nil: %#v", got)
	}
}

func TestMergeUsageAccountsWindowRolledOverOnOneMachine(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	oldWeek, week := now.Add(time.Hour), now.Add(7*24*time.Hour)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "A", SampledAt: now.Add(-3 * time.Hour), Windows: []proto.UsageWindow{uw("7d", 10080, 95, oldWeek)}},
		{Provider: "codex", Name: "A", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 2, week)}},
	}
	// different reset times: two accounts as far as the data says, both live
	if got := mergeUsageAccounts(in, now); len(got) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestUsagePollAsksOnlineMachinesAndRemembersTheirLastAnswer(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var asked atomic.Int32
	win := []proto.UsageWindow{uw("7d", 10080, 12, now.Add(time.Hour))}
	var fail atomic.Bool
	on := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		asked.Add(1)
		if fail.Load() {
			return proto.UsageSnapshot{}, errors.New("unknown method")
		}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageAccount{{Provider: "codex", Name: "Codex", SampledAt: now, Windows: win}}}, nil
	}}
	off := usageSource{id: "m2", online: false, ask: func(context.Context) (proto.UsageSnapshot, error) {
		t.Error("an offline machine is not asked")
		return proto.UsageSnapshot{}, nil
	}}
	var u usageWindowsHub
	got := u.poll(context.Background(), []usageSource{on, off}, now)
	if len(got) != 1 || asked.Load() != 1 {
		t.Fatalf("got %+v asked %d", got, asked.Load())
	}
	// within the fresh time: no second call
	u.poll(context.Background(), []usageSource{on, off}, now.Add(time.Second))
	if asked.Load() != 1 {
		t.Errorf("asked again within %v", usageHubFresh)
	}
	// later, the machine fails (an older node): its last answer stays
	fail.Store(true)
	got = u.poll(context.Background(), []usageSource{on, off}, now.Add(usageHubFresh+time.Second))
	if len(got) != 1 {
		t.Errorf("last answer lost: %+v", got)
	}
	// a machine taken out of hub.json is forgotten
	got = u.poll(context.Background(), nil, now.Add(2*usageHubFresh+2*time.Second))
	if len(got) != 0 {
		t.Errorf("removed machine kept: %+v", got)
	}
}

func TestUsagePollForgetsAMachineThatTurnedItOff(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	enabled := true
	src := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		if !enabled {
			return proto.UsageSnapshot{Enabled: false}, nil
		}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageAccount{{Provider: "claude", Name: "Claude", SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 1, now.Add(time.Hour))}}}}, nil
	}}
	var u usageWindowsHub
	if got := u.poll(context.Background(), []usageSource{src}, now); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	enabled = false
	if got := u.poll(context.Background(), []usageSource{src}, now.Add(usageHubFresh+time.Second)); len(got) != 0 {
		t.Errorf("off machine still shown: %+v", got)
	}
}

func TestMachineUsageWindowsSwitch(t *testing.T) {
	off := false
	if !(&Machine{}).usageWindows() || (&Machine{cfg: MachineConfig{UsageWindows: &off}}).usageWindows() {
		t.Error("usage_windows: unset is on, false is off")
	}
}

// #3794: a model's own allowance running out does not make the account the fullest; the plan windows decide the order.
func TestMergeUsageAccountsOrdersByThePlanWindows(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fable := uw("7d", 10080, 100, now.Add(3*24*time.Hour))
	fable.Model = "fable"
	modelOnly := uw("7d", 10080, 70, now.Add(6*24*time.Hour))
	modelOnly.Model = "argon"
	in := []proto.UsageAccount{
		{Provider: "claude", Name: "A", SampledAt: now, Ref: "gw:a", Windows: []proto.UsageWindow{uw("5h", 300, 10, now.Add(time.Hour)), fable}},
		{Provider: "claude", Name: "B", SampledAt: now, Ref: "gw:b", Windows: []proto.UsageWindow{uw("5h", 300, 60, now.Add(2*time.Hour))}},
		// no plan window at all: its model window is what there is
		{Provider: "gemini", Name: "C", SampledAt: now, Ref: "gw:c", Windows: []proto.UsageWindow{modelOnly}},
	}
	got := mergeUsageAccounts(in, now)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if len(names) != 3 || names[0] != "C" || names[1] != "B" || names[2] != "A" {
		t.Fatalf("order = %v, want [C B A]", names)
	}
}

// An account read on computers names them (#1907): a phone or another computer cannot tell "this computer" apart from
// the device in hand. The same account on two computers names both; a node's own say is not taken, a rename is.
func TestUsagePollNamesTheComputersThatReadAnAccount(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	machine := func(id, name string, used float64) usageSource {
		return usageSource{id: id, name: name, online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageAccount{{Provider: "codex", Name: "Codex Pro", SampledAt: now,
				Machines: []string{"what the node says"}, Windows: []proto.UsageWindow{uw("7d", 10080, used, week)}}}}, nil
		}}
	}
	var u usageWindowsHub
	u.gateways = func(time.Time) []proto.UsageAccount {
		return []proto.UsageAccount{{Provider: "claude", Name: "seat", SampledAt: now, Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 1, week)}}}
	}
	got := u.poll(context.Background(), []usageSource{machine("m2", "Studio", 10), machine("m1", "", 20)}, now)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, a := range got {
		switch a.Provider {
		case "codex": // one account on two computers; one without a name goes by its id
			if !a.Local || !slices.Equal(a.Machines, []string{"Studio", "m1"}) {
				t.Errorf("codex: %+v", a)
			}
		case "claude":
			if a.Local || a.Machines != nil {
				t.Errorf("a gateway-only account names no computer: %+v", a)
			}
		}
	}
	got = u.poll(context.Background(), []usageSource{machine("m2", "Mac Studio", 10), machine("m1", "", 20)}, now.Add(usageHubFresh+time.Second))
	for _, a := range got {
		if a.Provider == "codex" && !slices.Equal(a.Machines, []string{"Mac Studio", "m1"}) {
			t.Errorf("renamed: %+v", a)
		}
	}
}
