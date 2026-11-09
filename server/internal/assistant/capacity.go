package assistant

import (
	"context"
	"fmt"

	"github.com/nwverse/persona/server/internal/proto"
)

// Account-pool capacity in launcher selection (docs/server/usage-windows.md "Account-pool gateways",
// docs/server/launcher-health.md "Capacity-aware notes"). The manager already collects, read-only, how many
// subscription accounts of each upstream provider are schedulable and when the nearest one recovers.
//
// The first cut deliberately does not reorder machines: one launcher maps to one upstream provider, so every
// machine offering it sees the same provider-level capacity (and two different providers are not equivalent — the
// model configuration must not be silently changed, docs/assistant/model-config.md). What capacity adds here is the
// "why and when" on the detour path (healthDetour, the one chokepoint the agent_start tool and task plans share):
// when a launcher is rerouted or started anyway despite a failed check, a saturated pool is named, with the nearest
// recovery time. Stale or missing data says nothing and changes nothing.

// capacitySnapshot is the current view; zero when there is no fleet or no pool data.
func (s *Service) capacitySnapshot(ctx context.Context) proto.CapacitySnapshot {
	if s.devs == nil {
		return proto.CapacitySnapshot{}
	}
	return s.devs.CapacitySnapshot(ctx)
}

// launcherProvider maps a launcher on a machine to the upstream provider id its pooled accounts are listed under
// (proto.ProviderCapacity.Provider, the "claude" / "codex" vocabulary). The health record already names the agent
// the check ran there, and a Persona-managed launcher names its agent too; "" when unknown.
func (s *Service) launcherProvider(ctx context.Context, machine, launcher string) string {
	if r, ok := s.launcherHealth(ctx, machine, launcher); ok && r.Agent != "" {
		return r.Agent
	}
	for _, l := range s.storedLaunchers(ctx) {
		if l.Name == launcher && l.Agent != "" {
			return l.Agent
		}
	}
	return ""
}

// capacityNote is a sentence about the provider's pool when it is freshly known to be saturated: "" with no data,
// stale data, or any schedulable account. The recovery time is relative (schedule.go's until).
func (s *Service) capacityNote(ctx context.Context, provider string) string {
	if provider == "" {
		return ""
	}
	p, ok := s.capacitySnapshot(ctx).Provider(provider)
	if !ok || !p.Saturated() {
		return ""
	}
	if p.EarliestRecovery != nil {
		return fmt.Sprintf("no schedulable %s account in the pool right now; the nearest one recovers %s", provider, until(*p.EarliestRecovery))
	}
	return fmt.Sprintf("no schedulable %s account in the pool right now", provider)
}

// poolSuffix renders a saturated-pool note as a detour line's sentence suffix ("; …" or "").
func poolSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "; " + note
}
