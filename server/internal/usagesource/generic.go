package usagesource

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The generic source is Persona's own shape, for a gateway that is not a relay: GET <url> with
// "Authorization: Bearer <key>" answers
//
//	{"accounts": [{
//	  "id": "acct-1",              // optional: the account's identity at the gateway, stable
//	  "provider": "codex",         // "codex" and "claude" merge with the same accounts seen on the computers
//	  "name": "Team seat 3", "plan": "Pro",
//	  "sampled_at": "2026-11-05T09:08:06Z",   // when the numbers were read; missing: now
//	  "windows": [{"label": "5h", "window_minutes": 300, "used_percent": 22.0,
//	               "resets_at": "2026-11-05T13:08:06Z", "model": ""}]
//	}]}
//
// It is the body of Persona's own GET /api/v1/usage/windows: a hub can feed another. window_minutes may be left out when
// the label ("5h", "7d", "45m") says it.
type genericAccount struct {
	ID        string          `json:"id"`
	Provider  string          `json:"provider"`
	Name      string          `json:"name"`
	Plan      string          `json:"plan"`
	SampledAt *time.Time      `json:"sampled_at"`
	Stale     bool            `json:"stale"`
	Windows   []genericWindow `json:"windows"`
}

type genericWindow struct {
	Label         string     `json:"label"`
	WindowMinutes int        `json:"window_minutes"`
	UsedPercent   *float64   `json:"used_percent"`
	ResetsAt      *time.Time `json:"resets_at"`
	Model         string     `json:"model"`
}

func fetchGeneric(ctx context.Context, c *client, now time.Time) (Result, error) {
	var out struct {
		Accounts []genericAccount `json:"accounts"`
	}
	if err := c.do(ctx, "GET", "", nil, &out); err != nil {
		return Result{}, err
	}
	res := Result{Seen: Seen{Providers: map[string]int{}, Listed: distinctAccounts(out.Accounts)}}
	seen := map[string]bool{} // ids already taken: a repeated page or a gateway bug lists an account twice
	for i, a := range out.Accounts {
		if i >= AccountsMost {
			break
		}
		a.ID = strings.TrimSpace(a.ID)
		if a.ID != "" {
			if seen[a.ID] { // the first listing wins, as in the relay source
				continue
			}
			seen[a.ID] = true
		}
		var list []proto.UsageWindow
		checked := map[int]time.Time{}
		for j, w := range a.Windows {
			if j >= WindowsMost || w.UsedPercent == nil {
				continue
			}
			minutes := w.WindowMinutes
			if m, ok := KindMinutes(w.Label); ok {
				minutes = m
			}
			used, ok := clampPercent(*w.UsedPercent) // already 0-100: kept as sent, only clamped
			if !ok {
				continue
			}
			if minutes <= 0 {
				continue
			}
			list = addWindow(list, checked, proto.UsageWindow{Label: proto.WindowLabel(minutes), WindowMinutes: minutes, UsedPercent: used,
				ResetsAt: utc(w.ResetsAt), Model: clip(strings.TrimSpace(w.Model), 40)}, time.Time{})
		}
		provider := clip(strings.ToLower(strings.TrimSpace(a.Provider)), 32)
		if len(list) == 0 || provider == "" {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].WindowMinutes != list[j].WindowMinutes {
				return list[i].WindowMinutes < list[j].WindowMinutes
			}
			return list[i].Model < list[j].Model
		})
		sampled := now
		if a.SampledAt != nil && !a.SampledAt.IsZero() {
			sampled = notAfter(a.SampledAt.UTC(), now)
		}
		name := clip(strings.TrimSpace(a.Name), 80)
		if name == "" {
			name = provider
		}
		acct := proto.UsageAccount{Provider: provider, Name: name, Plan: clip(strings.TrimSpace(a.Plan), 40), Windows: list,
			SampledAt: sampled, Stale: a.Stale, Ref: a.ID}
		res.Accounts = append(res.Accounts, acct)
		res.Seen.Accounts++
		res.Seen.Windows += len(list)
		res.Seen.Providers[provider]++
	}
	return res, nil
}

// distinctAccounts counts the accounts a generic body lists, an id listed twice once. Accounts without an id cannot
// be told apart, so each counts.
func distinctAccounts(list []genericAccount) int {
	ids := map[string]bool{}
	n := 0
	for _, a := range list {
		id := strings.TrimSpace(a.ID)
		if id == "" {
			n++
			continue
		}
		if !ids[id] {
			ids[id] = true
			n++
		}
	}
	return n
}
