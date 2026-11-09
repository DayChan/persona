package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// capacityRig is a manager with two reachable machines and a fleet fake whose capacity snapshot a test controls.
func capacityRig(t *testing.T) (*Service, *fleetDevs) {
	t.Helper()
	f := newFakeHub()
	f.devices = []Device{
		{ID: "m1", Name: "m1", Home: "/home/me", Online: true, Reachable: true},
		{ID: "m2", Name: "m2", Home: "/home/me", Online: true, Reachable: true},
	}
	d := newFleetDevs()
	d.add("m1", "m1")
	d.add("m2", "m2")
	return New(Options{Hub: f, Logf: t.Logf, Devices: d}), d
}

func saturated(provider string, recovery time.Time, stale bool) proto.CapacitySnapshot {
	return proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
		Provider: provider, Accounts: 2, Available: 0, EarliestRecovery: &recovery, Stale: stale,
	}}}
}

// capacityNote only speaks on fresh data that proves the launcher's provider is saturated.
func TestCapacityNote(t *testing.T) {
	s, d := capacityRig(t)
	ctx := context.Background()
	s.recordHealth(ctx, "m1", "claude", healthResult{agent: "claude", err: "401 unauthorized", source: healthSourceCheck})

	if got := s.capacityNote(ctx, "claude"); got != "" {
		t.Fatalf("no pool data yet: %q", got)
	}

	recovery := time.Now().Add(30 * time.Minute)
	d.capacity = saturated("claude", recovery, false)
	got := s.capacityNote(ctx, "claude")
	if !strings.Contains(got, "no schedulable claude account") || !strings.Contains(got, "recovers in") {
		t.Fatalf("saturated note: %q", got)
	}

	// a pool with room says nothing
	d.capacity.Providers[0].Available = 1
	if got := s.capacityNote(ctx, "claude"); got != "" {
		t.Fatalf("available pool must stay quiet: %q", got)
	}

	// stale saturation is "unknown", not "full"
	d.capacity = saturated("claude", recovery, true)
	if got := s.capacityNote(ctx, "claude"); got != "" {
		t.Fatalf("stale data must stay quiet: %q", got)
	}

	// another provider's pool, or an unknown launcher, says nothing
	d.capacity = saturated("codex", recovery, false)
	if got := s.capacityNote(ctx, "claude"); got != "" {
		t.Fatalf("a different provider's pool: %q", got)
	}
	if got := s.capacityNote(ctx, ""); got != "" {
		t.Fatalf("unknown provider: %q", got)
	}
}

// The provider comes from the launcher's health record (and later a managed launcher's agent).
func TestLauncherProviderResolution(t *testing.T) {
	s, _ := capacityRig(t)
	ctx := context.Background()
	if got := s.launcherProvider(ctx, "m1", "claude"); got != "" {
		t.Fatalf("nothing known yet: %q", got)
	}
	s.recordHealth(ctx, "m1", "claude", healthResult{agent: "claude", ok: true, source: healthSourceCheck})
	if got := s.launcherProvider(ctx, "m1", "claude"); got != "claude" {
		t.Fatalf("health record: %q", got)
	}
}

// A failed launcher is rerouted to the healthy machine, and the detour line carries the saturated-pool fact with
// the nearest recovery time.
func TestHealthDetourNoteOnReroute(t *testing.T) {
	s, d := capacityRig(t)
	ctx := context.Background()
	s.recordHealth(ctx, "m1", "claude", healthResult{agent: "claude", err: "401 unauthorized", source: healthSourceCheck})
	s.recordHealth(ctx, "m2", "claude", healthResult{agent: "claude", ok: true, source: healthSourceCheck})
	d.capacity = saturated("claude", time.Now().Add(30*time.Minute), false)

	alt, note, moved := s.healthDetour(ctx, Device{ID: "m1"}, "claude")
	if !moved || alt.ID != "m2" {
		t.Fatalf("want the healthy m2: %s %v", alt.ID, moved)
	}
	if !strings.Contains(note, "runs on m2 instead") || !strings.Contains(note, "no schedulable claude account") {
		t.Fatalf("detour note: %q", note)
	}
}

// With no healthy alternative the start stays put and is warned about both the failed check and the saturated pool.
func TestHealthDetourNoteWhenStaying(t *testing.T) {
	s, d := capacityRig(t)
	ctx := context.Background()
	s.recordHealth(ctx, "m1", "claude", healthResult{agent: "claude", err: "401 unauthorized", source: healthSourceCheck})
	s.recordHealth(ctx, "m2", "claude", healthResult{agent: "claude", err: "401 unauthorized", source: healthSourceCheck})
	d.capacity = saturated("claude", time.Now().Add(time.Hour), false)

	alt, note, moved := s.healthDetour(ctx, Device{ID: "m1"}, "claude")
	if moved || alt.ID != "m1" {
		t.Fatalf("want to stay on m1: %s %v", alt.ID, moved)
	}
	if !strings.Contains(note, "started there anyway") || !strings.Contains(note, "no schedulable claude account") {
		t.Fatalf("stay note: %q", note)
	}

	// No pool data: the original wording, untouched.
	d.capacity = proto.CapacitySnapshot{}
	_, note, _ = s.healthDetour(ctx, Device{ID: "m1"}, "claude")
	if strings.Contains(note, "pool") {
		t.Fatalf("note without pool data: %q", note)
	}
}

// A healthy launcher is never detoured, pool or no pool.
func TestHealthDetourOffWhenHealthy(t *testing.T) {
	s, d := capacityRig(t)
	ctx := context.Background()
	d.capacity = saturated("claude", time.Now().Add(time.Hour), false)
	m, note, moved := s.healthDetour(ctx, Device{ID: "m1"}, "claude")
	if moved || m.ID != "m1" || note != "" {
		t.Fatalf("healthy launcher: %q %v", note, moved)
	}
}

// poolSuffix only ever adds a leading separator.
func TestPoolSuffix(t *testing.T) {
	if got := poolSuffix(""); got != "" {
		t.Fatalf("empty note: %q", got)
	}
	if got := poolSuffix("note"); got != "; note" {
		t.Fatalf("suffix: %q", got)
	}
}
