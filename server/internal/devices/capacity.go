package devices

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Capacity for the scheduler (analysis direction "account-pool capacity in Agent scheduling",
// docs/server/usage-windows.md "Account-pool gateways"). The manager already holds, in memory, everything this
// needs: the gateways' account list (schedulable, cooldown, status) and the merged subscription windows the
// computers and gateways reported. Nothing here asks a gateway or a machine, and no credential or gateway-side
// account id leaves the manager: the view is keyed by upstream provider only.

// capacityAccount is one account's scheduling-relevant state.
type capacityAccount struct {
	available bool
	stale     bool
	// recovery is when the account may take work again (a cooldown end or a window reset); nil when it is available
	// or no time is known.
	recovery *time.Time
}

// buildCapacitySnapshot derives the scheduler view from the gateway inventory and the merged sidebar windows.
// Gateway accounts are counted from the inventory, which is authoritative for them; a sidebar account a gateway
// saw (Sources non-empty) is therefore skipped, so the same account is never counted twice. Accounts only the
// computers saw (Local, no gateway source) fill the rest. Pure: the clock is passed in and there is no I/O.
func buildCapacitySnapshot(inv proto.GatewayAccounts, usage []proto.UsageAccount, now time.Time) proto.CapacitySnapshot {
	byProvider := map[string][]capacityAccount{}
	add := func(provider string, a capacityAccount) {
		if provider == "" {
			return
		}
		byProvider[provider] = append(byProvider[provider], a)
	}

	for _, g := range inv.Accounts {
		a := capacityAccount{stale: g.Stale}
		cooling := g.CooldownUntil != nil && g.CooldownUntil.After(now)
		// Schedulable is the gateway's own verdict; a disabled account is switched off on purpose and never takes
		// work, and a cooldown still in the future is not over.
		a.available = g.Schedulable && g.Status != "disabled" && !cooling
		if !a.available {
			switch {
			case cooling:
				t := *g.CooldownUntil
				a.recovery = &t
			default:
				// Exhausted, quarantined, refresh_required…: the soonest window reset is the only time signal.
				a.recovery = earliestFutureGatewayReset(g.Windows, now)
			}
		}
		add(g.Provider, a)
	}

	for _, u := range usage {
		if len(u.Sources) > 0 { // a gateway reports this one: counted from the inventory
			continue
		}
		a := capacityAccount{stale: u.Stale, available: true}
		if reset := exhaustedPlanReset(u, now); reset != nil {
			a.available = false
			a.recovery = reset
		}
		add(u.Provider, a)
	}

	ids := make([]string, 0, len(byProvider))
	for id := range byProvider {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := proto.CapacitySnapshot{CheckedAt: now.UTC(), Providers: make([]proto.ProviderCapacity, 0, len(ids))}
	for _, id := range ids {
		accounts := byProvider[id]
		p := proto.ProviderCapacity{Provider: id, Accounts: len(accounts), Stale: true}
		for _, a := range accounts {
			if a.available {
				p.Available++
			}
			if !a.stale {
				p.Stale = false
			}
			if a.recovery != nil && (p.EarliestRecovery == nil || a.recovery.Before(*p.EarliestRecovery)) {
				t := *a.recovery
				p.EarliestRecovery = &t
			}
		}
		out.Providers = append(out.Providers, p)
	}
	return out
}

// earliestFutureGatewayReset is the soonest not-yet-passed reset among an account's windows, whatever their kind.
func earliestFutureGatewayReset(windows []proto.GatewayWindow, now time.Time) *time.Time {
	var out *time.Time
	for _, w := range windows {
		if w.ResetsAt == nil || !w.ResetsAt.After(now) {
			continue
		}
		if out == nil || w.ResetsAt.Before(*out) {
			t := *w.ResetsAt
			out = &t
		}
	}
	return out
}

// exhaustedPlanReset is the soonest reset of a plan window (not a per-model one) that is fully used. A per-model
// allowance running out does not exhaust the account, matching mostUsed in usagewindows.go. nil when the account
// still has room or no reset time is known.
func exhaustedPlanReset(a proto.UsageAccount, now time.Time) *time.Time {
	var out *time.Time
	for _, w := range a.Windows {
		if w.Model != "" || w.UsedPercent < 100 || w.ResetsAt == nil || !w.ResetsAt.After(now) {
			continue
		}
		if out == nil || w.ResetsAt.Before(*out) {
			t := *w.ResetsAt
			out = &t
		}
	}
	return out
}

// CapacitySnapshot is the scheduler's account-pool view, built from the gateways' inventory and the merged usage
// windows. The windows come through usageWin.poll's own short, single-flight cache, so this does not hammer the
// machines; it never causes an upstream gateway read (the background poll in usagegateways.go owns those).
func (d *Devices) CapacitySnapshot(ctx context.Context) proto.CapacitySnapshot {
	now := time.Now()
	inventory := d.usageGW.gatewayAccounts()
	accounts := d.usageWin.poll(ctx, d.usageSources(), now)
	return buildCapacitySnapshot(inventory, accounts, now)
}
