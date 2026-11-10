package proto

import (
	"fmt"
	"time"
)

// Subscription rate-limit windows (#2432, docs/server/usage-windows.md): how much of an account's 5-hour and weekly allowance
// is used. A node reads them from the agents' own local files (Codex's rollouts, a Claude Code status line drop) and
// answers usage.windows; the hub merges the machines' answers into one list per account. Nothing here carries a
// credential: the account has no id the agent's vendor knows, only the windows it was seen with.

// UsageWindow is one rate-limit window of one account.
type UsageWindow struct {
	// Label is the window's length as people say it: "5h", "7d" (see WindowLabel).
	Label         string  `json:"label"`
	WindowMinutes int     `json:"window_minutes"`
	UsedPercent   float64 `json:"used_percent"`
	// ResetsAt is when the window starts over; nil when the source did not say.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Model names the one model this window measures, for an allowance a provider keeps per model next to the plan's
	// own (#2595); empty for the plan's window. Such a window never identifies an account.
	Model string `json:"model,omitempty"`
}

// UsageAccount is one subscription account as one source saw it (a node's answer) or as the hub merged it.
type UsageAccount struct {
	// ID is set by the hub: a short digest of the provider and the windows' identity, stable while the windows are.
	ID       string `json:"id,omitempty"`
	Provider string `json:"provider"`       // codex | claude
	Name     string `json:"name"`           // "Codex Pro", "Claude"
	Plan     string `json:"plan,omitempty"` // as the source calls it ("pro")
	// Windows are ordered shortest first.
	Windows []UsageWindow `json:"windows"`
	// SampledAt is when the source wrote the newest of these numbers (not when we read them).
	SampledAt time.Time `json:"sampled_at"`
	// Stale: the sample is older than the collector's schedule allows (its source has not been written to for a
	// while, or the machine that had it is offline).
	Stale bool `json:"stale,omitempty"`
	// StaleAt is when the sample turns stale if nothing newer comes: the sample time plus its source's threshold (#1905).
	// A client that keeps this answer while refreshes fail judges stale itself, Stale || now >= StaleAt, so old numbers
	// fade however long ago it last heard from the hub, and no client hard-codes the thresholds.
	StaleAt *time.Time `json:"stale_at,omitempty"`

	// Where the numbers came from (#2595), set by the hub. Local: an agent's own files on one of the machines
	// (docs/server/usage-windows.md); Sources: the names of the account-pool gateways that reported it
	// (docs/server/usage-windows.md "Account pool gateways"). An account both saw lists both.
	Local   bool     `json:"local,omitempty"`
	Sources []string `json:"sources,omitempty"`
	// Machines are the names of the computers whose agents' files reported it (#1907), set with Local; clients name
	// those instead of a word like "this computer", which reads as the device in hand.
	Machines []string `json:"machines,omitempty"`
	// Problem: "error" when the gateway could not read the account's numbers just now (it kept the stored ones). When
	// the gateway's own last read failed and this is its last answer, why: "rejected" (it turned the key away, 401 /
	// 403), "unreachable" (it could not be asked: no connection, a timeout) or "failed" (it answered, but not with
	// usage: 404, 5xx, a bad answer). Never carries the text; clients show nothing for a value they don't know.
	Problem string `json:"problem,omitempty"`
	// Ref is the account's identity at its gateway ("<source id>:<account id>"), when the source has one. Hub-internal:
	// it is never sent out. Two samples with refs from one source but different accounts are never merged.
	Ref string `json:"-"`
	// Origin is where this sample was read (#1904): "<machine id>/<n>" for the n-th account of a machine's answer, the
	// gateway's Ref, or "<source id>#<n>" for a gateway account without one. Hub-internal, never sent out: it only tells
	// apart, in a stable order, accounts that nothing else does.
	Origin string `json:"-"`
}

// UsageSnapshot is what a node answers to usage.windows.
type UsageSnapshot struct {
	// Enabled false: hub.json machines[].usage_windows is false for this machine; nothing is read.
	Enabled     bool           `json:"enabled"`
	CollectedAt time.Time      `json:"collected_at"`
	Accounts    []UsageAccount `json:"accounts"`
}

// WindowLabel names a window by its length: 300 → "5h", 10080 → "7d", 45 → "45m"; 0 or less → "".
func WindowLabel(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}
