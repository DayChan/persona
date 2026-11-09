package devices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Subscription rate-limit windows across machines (#2432, docs/server/usage-windows.md). Every node reads its agents'
// local files and answers usage.windows; the manager, which connects the machines (docs/server/machines.md "Who connects the computers"), asks
// the ones that are online (at most every usageHubFresh), remembers each machine's last answer, and merges them into
// one list per account: the same account on two computers shows once, with the newest numbers. Nothing here is
// stored: a restarted manager asks again, and an offline machine's last answer is only kept in memory until it is
// older than usageHubMaxAge. Moved from the hub as it was; the hub relays the route (devicewire.UsageRoutes).

const (
	usageHubFresh      = 30 * time.Second // the merged answer stands this long for every app reading it
	usageHubAsk        = 8 * time.Second  // per machine
	usageHubStaleAfter = 30 * time.Minute // a sample older than this is shown as stale
	usageHubMaxAge     = 3 * 24 * time.Hour
)

// usageSource is a machine as the aggregator sees it.
type usageSource struct {
	id     string
	online bool
	ask    func(ctx context.Context) (proto.UsageSnapshot, error)
}

type usageAnswer struct {
	accounts []proto.UsageAccount
	at       time.Time // when the node answered (the manager's clock)
}

type usageWindowsHub struct {
	mu        sync.Mutex
	byMachine map[string]usageAnswer
	merged    []proto.UsageAccount
	at        time.Time
	loading   chan struct{} // closed when the poll in flight is done
	// gateways gives the samples of the account-pool gateways (usagegateways.go, #2595); nil: none.
	gateways func(now time.Time) []proto.UsageAccount
}

// invalidate drops the merged list so that the next read builds it again (a gateway answered or was edited).
func (u *usageWindowsHub) invalidate() {
	u.mu.Lock()
	u.at = time.Time{}
	u.mu.Unlock()
}

// usageSources are the machines whose node may be asked: the off switch in hub.json is honoured here as well as by
// the node, so a machine that is off is not even called.
func (d *Devices) usageSources() []usageSource {
	var out []usageSource
	for _, m := range d.machinesNow() {
		if !m.usageWindows() {
			continue
		}
		m.mu.Lock()
		online := m.online
		m.mu.Unlock()
		out = append(out, usageSource{id: m.cfg.ID, online: online, ask: func(ctx context.Context) (proto.UsageSnapshot, error) {
			var snap proto.UsageSnapshot
			err := m.call(ctx, "usage.windows", map[string]any{}, &snap)
			return snap, err
		}})
	}
	return out
}

// poll asks the online machines (in parallel) and returns the merged accounts. Concurrent callers share one poll.
func (u *usageWindowsHub) poll(ctx context.Context, sources []usageSource, now time.Time) []proto.UsageAccount {
	u.mu.Lock()
	if u.merged != nil && !u.at.IsZero() && now.Sub(u.at) < usageHubFresh {
		out := u.merged
		u.mu.Unlock()
		return out
	}
	if wait := u.loading; wait != nil { // someone is asking already: take their answer
		u.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
		}
		u.mu.Lock()
		out := u.merged
		u.mu.Unlock()
		return out
	}
	done := make(chan struct{})
	u.loading = done
	u.mu.Unlock()

	answers := make([]*usageAnswer, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		if !s.online {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageHubAsk)
			defer cancel()
			snap, err := s.ask(c)
			if err != nil { // an older node does not know usage.windows; a failed call keeps the last answer
				return
			}
			if !snap.Enabled {
				answers[i] = &usageAnswer{} // the node is off: forget what it said
				return
			}
			answers[i] = &usageAnswer{accounts: snap.Accounts, at: now}
		}()
	}
	wg.Wait()

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.byMachine == nil {
		u.byMachine = map[string]usageAnswer{}
	}
	known := map[string]bool{}
	for i, s := range sources {
		known[s.id] = true
		if a := answers[i]; a != nil {
			if a.at.IsZero() {
				delete(u.byMachine, s.id)
			} else {
				u.byMachine[s.id] = *a
			}
		}
	}
	var all []proto.UsageAccount
	for id, a := range u.byMachine {
		if !known[id] || now.Sub(a.at) > usageHubMaxAge { // a machine taken out of hub.json, or one gone for days
			delete(u.byMachine, id)
			continue
		}
		for n, acct := range a.accounts { // a node answers its accounts in a fixed order (sorted by where it read them)
			acct.Local, acct.Sources, acct.Problem, acct.Ref = true, nil, "", "" // what a node says of where it came from is not taken
			acct.Origin = id + "/" + strconv.Itoa(n)
			all = append(all, acct)
		}
	}
	if u.gateways != nil {
		all = append(all, u.gateways(now)...)
	}
	u.merged, u.at = mergeUsageAccounts(all, now), now
	u.loading = nil
	close(done)
	return u.merged
}

// mergeUsageAccounts makes one account of the samples that are the same account, drops what has run out and orders
// the result for the sidebar.
//
// Two samples are the same account when they are of one provider and share a window: the same length that resets
// at the same second (a window's reset time is the account's, wherever it was read: on a machine or at a gateway).
// A per-model window never counts for this, and two accounts of one gateway (different refs) are never one. Their
// windows are then taken per length (and model) from the newest sample, so a machine that was idle since the 5-hour
// window rolled over does not bring the old one back. Windows that have reset by now are dropped (their number is
// the last period's) and an account with none left is not listed. Older than usageHubMaxAge is dropped; older than
// usageHubStaleAfter (a gateway's: usageGatewayStaleAfter, or what it reported itself) is marked stale. The account
// is named by a gateway when one knows it (a name somebody gave it), and says where it was seen (Local, Sources).
// The most used account comes first, stale ones last; two accounts with one name are numbered by their id.
func mergeUsageAccounts(in []proto.UsageAccount, now time.Time) []proto.UsageAccount {
	var live []proto.UsageAccount
	for _, a := range in {
		if now.Sub(a.SampledAt) > usageHubMaxAge {
			continue
		}
		var wins []proto.UsageWindow
		for _, w := range a.Windows {
			if w.ResetsAt != nil && !w.ResetsAt.After(now) {
				continue
			}
			wins = append(wins, w)
		}
		if len(wins) == 0 {
			continue
		}
		a.Windows = wins
		live = append(live, a)
	}

	// union-find over the samples
	parent := make([]int, len(live))
	for i := range parent {
		parent[i] = i
	}
	var find func(i int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	type key struct {
		provider string
		minutes  int
		resets   int64
	}
	seen := map[key]int{}
	for i, a := range live {
		for _, w := range a.Windows {
			if w.ResetsAt == nil || w.Model != "" {
				continue
			}
			k := key{a.Provider, w.WindowMinutes, w.ResetsAt.Unix()}
			j, ok := seen[k]
			if !ok {
				seen[k] = i
				continue
			}
			if sameSourceOtherAccount(live[i].Ref, live[j].Ref) {
				continue
			}
			parent[find(i)] = find(j)
		}
	}
	groups := map[int][]proto.UsageAccount{}
	var order []int
	for i, a := range live {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], a)
	}

	out := []proto.UsageAccount{} // never nil: nil means "not asked yet" to poll
	for _, r := range order {
		members := groups[r]
		sort.SliceStable(members, func(i, j int) bool { return members[i].SampledAt.After(members[j].SampledAt) })
		newest := members[0]
		acct := proto.UsageAccount{Provider: newest.Provider, Name: newest.Name, Plan: newest.Plan, SampledAt: newest.SampledAt, Problem: newest.Problem}
		// A gateway's name is one somebody gave the account; the machines only know "Codex Pro".
		naming := -1
		for i, m := range members {
			if len(m.Sources) > 0 && !m.Local {
				naming = i
				break
			}
		}
		if naming >= 0 {
			acct.Name, acct.Plan = members[naming].Name, members[naming].Plan
		}
		type slot struct {
			minutes int
			model   string
		}
		bySlot := map[slot]proto.UsageWindow{}
		var sources []string
		for _, m := range members {
			if acct.Plan == "" && m.Plan != "" { // the newest sample that knows the plan names the account
				acct.Plan = m.Plan
				if naming < 0 {
					acct.Name = m.Name
				}
			}
			acct.Local = acct.Local || m.Local
			sources = append(sources, m.Sources...)
			for _, w := range m.Windows {
				k := slot{w.WindowMinutes, w.Model}
				if _, ok := bySlot[k]; !ok {
					bySlot[k] = w
				}
			}
		}
		for _, w := range bySlot {
			acct.Windows = append(acct.Windows, w)
		}
		sort.Slice(acct.Windows, func(i, j int) bool {
			a, b := acct.Windows[i], acct.Windows[j]
			if a.WindowMinutes != b.WindowMinutes {
				return a.WindowMinutes < b.WindowMinutes
			}
			return a.Model < b.Model
		})
		acct.Sources = uniqueSorted(sources)
		acct.Ref, acct.Origin = firstOf(members, func(m proto.UsageAccount) string { return m.Ref }), firstOf(members, func(m proto.UsageAccount) string { return m.Origin })
		acct.ID = usageAccountID(acct)
		if len(newest.Sources) > 0 && !newest.Local {
			acct.Stale = newest.Stale || now.Sub(acct.SampledAt) > usageGatewayStaleAfter
		} else {
			acct.Stale = now.Sub(acct.SampledAt) > usageHubStaleAfter
		}
		out = append(out, acct)
	}

	// Whatever still shares an id (two accounts with only per-model windows that reset together) is numbered in the
	// order of where it was read, so that every id is a row of its own in the clients' lists (#1904).
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Origin < out[j].Origin
	})
	ids := map[string]int{}
	for i := range out {
		ids[out[i].ID]++
		if n := ids[out[i].ID]; n > 1 {
			out[i].ID = fmt.Sprintf("%s-%d", out[i].ID, n)
		}
	}
	// two accounts with one name: "Codex Pro", "Codex Pro 2", numbered by id so that it does not change with the numbers
	count := map[string]int{}
	for i := range out {
		count[out[i].Name]++
		if n := count[out[i].Name]; n > 1 {
			out[i].Name = fmt.Sprintf("%s %d", out[i].Name, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stale != out[j].Stale {
			return !out[i].Stale
		}
		if a, b := mostUsed(out[i]), mostUsed(out[j]); a != b {
			return a > b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// sameSourceOtherAccount: both refs name an account of one gateway ("<source id>:<account id>") and they are not the
// same account. A gateway knows its accounts apart, whatever their reset times look like.
func sameSourceOtherAccount(a, b string) bool {
	sa, _, okA := strings.Cut(a, ":")
	sb, _, okB := strings.Cut(b, ":")
	return okA && okB && sa == sb && a != b
}

// mostUsed is the used percentage of the account's fullest plan window: a per-model allowance that ran out is not the
// account running out, so model windows only count when the account has no plan window (as the clients' fullest).
func mostUsed(a proto.UsageAccount) float64 {
	var plan, model float64
	hasPlan := false
	for _, w := range a.Windows {
		if w.Model == "" {
			plan, hasPlan = max(plan, w.UsedPercent), true
		} else {
			model = max(model, w.UsedPercent)
		}
	}
	if hasPlan {
		return plan
	}
	return model
}

// firstOf is the smallest non-empty value of field among the samples of one account ("" when none has one).
func firstOf(members []proto.UsageAccount, field func(proto.UsageAccount) string) string {
	first := ""
	for _, m := range members {
		if v := field(m); v != "" && (first == "" || v < first) {
			first = v
		}
	}
	return first
}

// usageAccountID is stable while the account's longest window is: it names the window by length and reset time, which
// only change when that window rolls over (a week), not with every number. That tells apart accounts with a reset time
// (two sharing one are one account), but not two accounts of one gateway, which are never one, nor accounts without a
// reset time: the gateway's account id (its Ref) and, without a reset time, where it was read (its Origin) go in too
// (#1904). Both stay in the hash: neither a gateway's account id nor a machine's is sent out.
func usageAccountID(a proto.UsageAccount) string {
	w := a.Windows[len(a.Windows)-1]
	for i := len(a.Windows) - 1; i >= 0; i-- { // the longest window of the plan itself, not of one model
		if a.Windows[i].Model == "" {
			w = a.Windows[i]
			break
		}
	}
	var resets int64
	if w.ResetsAt != nil {
		resets = w.ResetsAt.Unix()
	}
	key := fmt.Sprintf("%s|%d|%d", a.Provider, w.WindowMinutes, resets)
	switch {
	case a.Ref != "":
		key += "|" + a.Ref
	case w.ResetsAt == nil:
		key += "|" + a.Origin
	}
	sum := sha256.Sum256([]byte(key))
	return a.Provider + "-" + hex.EncodeToString(sum[:4])
}

// usageWindowRoutes: GET /api/v1/usage/windows, the accounts' windows for the sidebar's usage bars. Read-only, from
// local files on the machines (docs/server/usage-windows.md); an empty list when no machine has any.
// (#2432: its own block so that merges with the other route blocks stay additive.)
func (d *Devices) usageWindowRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/usage/windows", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		accounts := d.usageWin.poll(r.Context(), d.usageSources(), now)
		if accounts == nil {
			accounts = []proto.UsageAccount{}
		}
		writeJSON(w, map[string]any{"accounts": accounts, "checked_at": now.UTC()})
	})
}
