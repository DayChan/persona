package extension

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestRegistryScopeAndSubscriberSnapshots(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	for _, tc := range []struct {
		id      string
		grant   Permissions
		enabled bool
		problem string
	}{
		{"read", Permissions{Hub: []string{ScopeKBRead}}, true, ""},
		{"write", Permissions{Hub: []string{ScopeKBWrite}}, true, ""},
		{"none", Permissions{}, true, ""},
		{"legacy", Permissions{HubAPI: true}, true, ""},
		{"disabled", Permissions{Hub: []string{ScopeKBRead}}, false, ""},
		{"broken", Permissions{Hub: []string{ScopeKBRead}}, true, "incompatible"},
	} {
		m := &Manifest{ID: tc.id, Version: "1.0.0", Permissions: Permissions{Hub: []string{ScopeKBRead, ScopeKBWrite}},
			Contributes: map[string]json.RawMessage{"events": json.RawMessage(`[{"id":"example","subscribe":["kb.changed","agent.finished"]}]`)}}
		if err := r.AddBuiltin(m, t.TempDir(), tc.enabled); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.extensions[tc.id].Record.Granted = tc.grant
		r.extensions[tc.id].Problem = tc.problem
		r.mu.Unlock()
	}
	for _, id := range []string{"missing", "none", "write", "disabled", "broken"} {
		if r.HubScopeAllowed(id, ScopeKBRead) {
			t.Fatalf("read scope granted to %s", id)
		}
	}
	if !r.HubScopeAllowed("read", ScopeKBRead) || r.HubScopeAllowed("read", ScopeKBWrite) || !r.HubScopeAllowed("legacy", ScopeKBWrite) {
		t.Fatal("scope isolation or legacy full grant")
	}
	if ids := r.EventSubscribers("kb.changed", ScopeKBRead); !reflect.DeepEqual(ids, []string{"legacy", "read"}) {
		t.Fatalf("KB subscribers: %v", ids)
	}
	if ids := r.EventSubscribers("agent.finished", ""); !reflect.DeepEqual(ids, []string{"legacy", "none", "read", "write"}) {
		t.Fatalf("ordinary subscribers: %v", ids)
	}
	if ids := r.EventSubscribers("unknown", ""); len(ids) != 0 {
		t.Fatalf("unknown topic subscribers: %v", ids)
	}
	ids := r.EventSubscribers("kb.changed", ScopeKBRead)
	ids[0] = "changed snapshot"
	if ids := r.EventSubscribers("kb.changed", ScopeKBRead); ids[0] != "legacy" {
		t.Fatal("caller changed the registry's subscriber snapshot")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 500 {
			if err := r.SetEnabled(context.Background(), "read", i%2 == 0, "test"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			r.HubScopeAllowed("read", ScopeKBRead)
			for _, id := range r.EventSubscribers("kb.changed", ScopeKBRead) {
				if id != "legacy" && id != "read" {
					t.Errorf("unexpected subscriber: %s", id)
				}
			}
		}
	}()
	wg.Wait()
}

func TestRegistrySnapshotKeepsProcessState(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	m := &Manifest{ID: "example", Version: "1.0.0", Permissions: Permissions{Hub: []string{ScopeKBRead}},
		Contributes: map[string]json.RawMessage{"events": json.RawMessage(`[{"id":"example","subscribe":["kb.changed"]}]`)}}
	if err := r.AddBuiltin(m, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	snapshot := r.Snapshot("example")
	if snapshot == nil || r.Snapshot("missing") != nil {
		t.Fatal("snapshot lookup")
	}
	if err := r.SetContribution(context.Background(), "example", "events", "example", false, "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled(context.Background(), "example", false, "test"); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Record.Enabled || len(snapshot.Manifest.Topics()) != 1 || len(snapshot.Record.DisabledContributions) != 0 {
		t.Fatalf("the old process's snapshot changed: %+v", snapshot)
	}
	current := r.Snapshot("example")
	if current.Record.Enabled || len(current.Manifest.Topics()) != 0 || len(current.Record.DisabledContributions) != 1 {
		t.Fatalf("the current state was not updated: %+v", current)
	}
	snapshot.Record.Granted.Hub[0] = ScopeKBWrite
	if r.Snapshot("example").Record.Granted.Hub[0] != ScopeKBRead {
		t.Fatal("the snapshot shares the mutable permission slice")
	}
}

// A token of every scope has every scope but agents.permission_mode, which the install screen still lists.
func TestEveryScopeLeavesOutThePermissionMode(t *testing.T) {
	every := EveryScope()
	if slices.Contains(every, ScopeAgentsPermissionMode) || !slices.Contains(HubScopes, ScopeAgentsPermissionMode) ||
		len(every) != len(HubScopes)-1 || InEveryScope(ScopeAgentsPermissionMode) || !InEveryScope(ScopeAgentsControl) {
		t.Fatalf("every scope: %v", every)
	}
	every[0] = "x"
	if HubScopes[0] == "x" {
		t.Fatal("EveryScope hands out HubScopes itself")
	}
}

// The legacy full-hub grant covers agents.control but not agents.permission_mode, which only a named grant gives.
func TestFullHubGrantLeavesOutThePermissionMode(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	for id, grant := range map[string]Permissions{
		"legacy": {HubAPI: true},
		"named":  {Hub: []string{ScopeAgentsPermissionMode}},
	} {
		m := &Manifest{ID: id, Version: "1.0.0", Permissions: grant,
			Contributes: map[string]json.RawMessage{"events": json.RawMessage(`[{"id":"example","subscribe":["agent.finished"]}]`)}}
		if err := r.AddBuiltin(m, t.TempDir(), true); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.extensions[id].Record.Granted = grant
		r.mu.Unlock()
	}
	if r.HubScopeAllowed("legacy", ScopeAgentsPermissionMode) || !r.HubScopeAllowed("legacy", ScopeAgentsControl) ||
		!r.HubScopeAllowed("named", ScopeAgentsPermissionMode) {
		t.Fatal("the full-hub grant and the permission mode")
	}
	if ids := r.EventSubscribers("agent.finished", ScopeAgentsPermissionMode); !reflect.DeepEqual(ids, []string{"named"}) {
		t.Fatalf("permission mode subscribers: %v", ids)
	}
	if ids := r.EventSubscribers("agent.finished", ScopeAgentsControl); !reflect.DeepEqual(ids, []string{"legacy"}) {
		t.Fatalf("agents.control subscribers: %v", ids)
	}
}

// An installed hub_api extension whose upgrade names agents.permission_mode asks the user again, and the upgrade lists it
// as added.
func TestUpgradeAddingThePermissionModeNeedsConsent(t *testing.T) {
	modeTitle, _ := ScopeTitleIn(i18n.Default, ScopeAgentsPermissionMode)
	installed := func(x *Manifest) *Extension {
		return &Extension{Record: Record{Granted: x.Permissions, GrantedDigest: Digest(x)}, Manifest: x, Full: x}
	}
	legacy := &Manifest{ID: "example", Version: "1.0.0", Permissions: Permissions{HubAPI: true}}
	for _, c := range []struct {
		what string
		from *Manifest
		to   Permissions
		more bool
	}{
		{"hub_api to hub_api", legacy, Permissions{HubAPI: true}, false},
		{"hub_api to a scope it already had", legacy, Permissions{HubAPI: true, Hub: []string{ScopeAgentsControl}}, false},
		{"hub_api to the permission mode", legacy, Permissions{HubAPI: true, Hub: []string{ScopeAgentsPermissionMode}}, true},
		{"hub_api to the permission mode alone", legacy, Permissions{Hub: []string{ScopeAgentsPermissionMode}}, true},
		{"the permission mode kept", &Manifest{ID: "example", Version: "1.0.0",
			Permissions: Permissions{Hub: []string{ScopeAgentsPermissionMode}}}, Permissions{Hub: []string{ScopeAgentsPermissionMode}}, false},
	} {
		to := &Manifest{ID: "example", Version: "1.1.0", Permissions: c.to}
		if got := MorePermissions(installed(c.from), to); got != c.more {
			t.Errorf("%s: MorePermissions = %v", c.what, got)
		}
		listed := slices.ContainsFunc(Added(c.from, to), func(l PermissionLine) bool {
			return l.Key == "hub" && strings.Contains(l.Detail, modeTitle)
		})
		if listed != c.more {
			t.Errorf("%s: the permission mode listed as added = %v: %+v", c.what, listed, Added(c.from, to))
		}
	}
}
