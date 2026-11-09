package devices

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func fp(v float64) *float64     { return &v }
func tp(t time.Time) *time.Time { return &t }

func gwWindow(kind string, used *float64, resets time.Time) proto.GatewayWindow {
	return proto.GatewayWindow{Kind: kind, Scope: "account", UsedRatio: used, ResetsAt: tp(resets)}
}

func gwAccount(provider, status string, schedulable bool, cooldown *time.Time, stale bool, wins ...proto.GatewayWindow) proto.GatewayAccount {
	return proto.GatewayAccount{Provider: provider, Status: status, Schedulable: schedulable, CooldownUntil: cooldown, Windows: wins, Stale: stale}
}

func localAccount(provider, name string, stale bool, wins ...proto.UsageWindow) proto.UsageAccount {
	return proto.UsageAccount{Provider: provider, Name: name, Local: true, Stale: stale, SampledAt: time.Now(), Windows: wins}
}

func poolAccount(provider, name string, stale bool, wins ...proto.UsageWindow) proto.UsageAccount {
	a := localAccount(provider, name, stale, wins...)
	a.Sources = []string{"Relay"}
	return a
}

// The builder counts gateway accounts from the inventory with their schedulable/cooldown/disabled state, takes the
// cooldown end as the recovery time, and leaves a healthy account out of the recovery.
func TestBuildCapacityFromGatewayInventory(t *testing.T) {
	now := time.Now()
	coolUntil := now.Add(20 * time.Minute)
	reset := now.Add(time.Hour)
	inv := proto.GatewayAccounts{Accounts: []proto.GatewayAccount{
		gwAccount("codex", "active", true, nil, false),
		gwAccount("codex", "cooldown", true, tp(coolUntil), false),
		gwAccount("codex", "disabled", false, nil, false),
		gwAccount("codex", "exhausted", false, nil, false, gwWindow("five_hour", fp(1), reset)),
	}}
	got := buildCapacitySnapshot(inv, nil, now)
	if len(got.Providers) != 1 {
		t.Fatalf("want one provider: %+v", got.Providers)
	}
	p := got.Providers[0]
	if p.Provider != "codex" || p.Accounts != 4 || p.Available != 1 || p.Stale {
		t.Fatalf("codex entry: %+v", p)
	}
	if p.EarliestRecovery == nil || !p.EarliestRecovery.Equal(coolUntil) {
		t.Fatalf("recovery wants the cooldown end %s, got %v", coolUntil, p.EarliestRecovery)
	}
	if p.Saturated() {
		t.Fatalf("one account is available: not saturated")
	}
}

// Every account unavailable with fresh data saturates the provider; the nearest window reset is the recovery, and
// Saturated flips back to false once that data is stale.
func TestBuildCapacitySaturationAndStaleness(t *testing.T) {
	now := time.Now()
	soon, later := now.Add(30*time.Minute), now.Add(2*time.Hour)
	inv := proto.GatewayAccounts{Accounts: []proto.GatewayAccount{
		gwAccount("claude", "exhausted", false, nil, false, gwWindow("five_hour", fp(1), later)),
		gwAccount("claude", "cooldown", true, tp(soon), false),
	}}
	fresh := buildCapacitySnapshot(inv, nil, now)
	p := fresh.Providers[0]
	if !p.Saturated() || p.Available != 0 {
		t.Fatalf("fresh: want saturated: %+v", p)
	}
	if p.EarliestRecovery == nil || !p.EarliestRecovery.Equal(soon) {
		t.Fatalf("want the soonest recovery %s, got %v", soon, p.EarliestRecovery)
	}
	for i := range inv.Accounts {
		inv.Accounts[i].Stale = true
	}
	stale := buildCapacitySnapshot(inv, nil, now)
	if stale.Providers[0].Saturated() {
		t.Fatalf("stale data must not saturate: %+v", stale.Providers[0])
	}
}

// Local windows fill in providers no gateway reported; a gateway-sourced sidebar account is not counted twice, and a
// per-model exhaustion does not take the account down while its plan window still has room.
func TestBuildCapacityFromLocalWindows(t *testing.T) {
	now := time.Now()
	reset := now.Add(90 * time.Minute)
	usage := []proto.UsageAccount{
		// gateway-sourced: the inventory already counts it
		poolAccount("codex", "pool seat", false, uw("5h", 300, 12, now.Add(2*time.Hour))),
		// local account, plan window exhausted: unavailable until its reset
		localAccount("codex", "local codex", false, uw("5h", 300, 100, reset)),
		// local claude: only a per-model window is full, the plan is fine
		localAccount("claude", "local claude", false,
			uw("5h", 300, 40, now.Add(3*time.Hour)),
			proto.UsageWindow{WindowMinutes: 300, UsedPercent: 100, Model: "fable", ResetsAt: tp(now.Add(time.Hour))}),
	}
	inv := proto.GatewayAccounts{Accounts: []proto.GatewayAccount{
		gwAccount("codex", "active", true, nil, false),
	}}
	got := buildCapacitySnapshot(inv, usage, now)
	byID := map[string]proto.ProviderCapacity{}
	for _, p := range got.Providers {
		byID[p.Provider] = p
	}
	codex := byID["codex"] // one inventory account + one local account, the pool sidebar one skipped
	if codex.Accounts != 2 || codex.Available != 1 {
		t.Fatalf("codex: %+v", codex)
	}
	if codex.EarliestRecovery == nil || !codex.EarliestRecovery.Equal(reset) {
		t.Fatalf("codex recovery: %v", codex.EarliestRecovery)
	}
	claude := byID["claude"]
	if claude.Accounts != 1 || claude.Available != 1 || claude.EarliestRecovery != nil {
		t.Fatalf("claude stays available on its plan window: %+v", claude)
	}
}

// No data means an empty, non-nil provider list (unknown, not saturated), and providers come back sorted.
func TestBuildCapacityEmptyAndSorted(t *testing.T) {
	got := buildCapacitySnapshot(proto.GatewayAccounts{}, nil, time.Now())
	if got.Providers == nil || len(got.Providers) != 0 || got.CheckedAt.IsZero() {
		t.Fatalf("empty snapshot: %+v", got)
	}
	inv := proto.GatewayAccounts{Accounts: []proto.GatewayAccount{
		gwAccount("kiro", "active", true, nil, false),
		gwAccount("claude", "active", true, nil, false),
		gwAccount("antigravity", "active", true, nil, false),
	}}
	got = buildCapacitySnapshot(inv, nil, time.Now())
	want := []string{"antigravity", "claude", "kiro"}
	for i, p := range got.Providers {
		if p.Provider != want[i] {
			t.Fatalf("order: %+v, want %v", got.Providers, want)
		}
	}
}
