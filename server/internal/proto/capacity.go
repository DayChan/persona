package proto

import "time"

// Capacity-aware scheduling: a read-only view of how many subscription accounts of each upstream provider can take
// work now and when the nearest unavailable one recovers. It is derived from data the manager already collects for
// the sidebar (UsageAccount) and for the account-pool gateways' account list (GatewayAccount,
// docs/server/usage-windows.md): building it never asks a gateway or a machine, and it carries no credential or
// gateway-side account identity.

// CapacitySnapshot is the scheduler's capacity view at one moment. Providers with no known account are absent; an
// empty Providers list means "no capacity data", not "everything is saturated".
type CapacitySnapshot struct {
	// CheckedAt is when the snapshot was built (the manager's clock).
	CheckedAt time.Time `json:"checked_at"`
	// Providers is one entry per upstream provider id ("codex", "claude", …), ordered by id for stable output.
	Providers []ProviderCapacity `json:"providers"`
}

// ProviderCapacity merges every account of one provider known across the gateways and the computers.
type ProviderCapacity struct {
	// Provider is the upstream provider id, the same vocabulary UsageAccount.Provider and
	// GatewayAccount.Provider use.
	Provider string `json:"provider"`
	// Accounts is how many accounts of this provider are known (gateway accounts plus accounts only the computers
	// have seen); zero means the provider is not known here.
	Accounts int `json:"accounts"`
	// Available is how many of them can take work now: gateway accounts that are schedulable, not disabled and not
	// inside a cooldown, and local accounts whose plan windows are not exhausted.
	Available int `json:"available"`
	// EarliestRecovery is when the nearest unavailable account becomes usable again: the soonest cooldown end or
	// window reset. nil when every account is available or no recovery time is known.
	EarliestRecovery *time.Time `json:"earliest_recovery,omitempty"`
	// Stale: every account behind this entry is older than the collector's freshness limit. Partially fresh data
	// stays false, and a stale saturated view must not drive a scheduling decision by itself.
	Stale bool `json:"stale"`
}

// Saturated reports the provider as known but with no account that can take work. Stale data never saturates: a
// scheduler is expected to treat it as "unknown" rather than "full".
func (p ProviderCapacity) Saturated() bool {
	return p.Accounts > 0 && p.Available == 0 && !p.Stale
}

// Provider returns the provider's entry, if known.
func (s CapacitySnapshot) Provider(id string) (ProviderCapacity, bool) {
	for _, p := range s.Providers {
		if p.Provider == id {
			return p, true
		}
	}
	return ProviderCapacity{}, false
}
