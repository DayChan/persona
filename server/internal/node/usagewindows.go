package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Subscription rate-limit windows of the agents on this machine (#2432, docs/server/usage-windows.md): how much of an
// account's 5-hour and weekly allowance is used. Only local files are read, never an account API and never a
// credential:
//
//   - Codex writes the limits into every rollout's token_count event (rate_limits.primary / secondary with
//     used_percent, window_minutes and resets_at). The collector reads the tail of the newest rollouts of each Codex
//     home. Moshi also asks ChatGPT's usage endpoint for them; that needs the login token, so it is not done here.
//   - Claude Code hands the limits to a status line command (the rate_limits field of its JSON input) and keeps them
//     nowhere else; the API that has them needs the OAuth token, so it is skipped too. The way in is opt-in: a
//     status line wrapper drops the JSON into <node dir>/usage/claude*.json, which is read here.
//
// The node polls every usageWindowsEvery, keeps the last sample of every source (a source that goes quiet keeps its
// number for three days), and answers usage.windows from that. hub.json machines[].usage_windows false turns it off.

const (
	usageWindowsEvery  = 5 * time.Minute
	usageWindowsFirst  = 20 * time.Second // the hub's node.configure (the off switch) comes first
	usageWindowsMaxAge = 3 * 24 * time.Hour
	usageWindowsTail   = 512 << 10 // how much of a rollout's end is searched for the last token_count
	usageWindowsScan   = 12        // rollouts looked into per Codex home
)

type usageWindowsState struct {
	off atomic.Bool // the hub said usage_windows false

	mu       sync.Mutex
	at       time.Time
	accounts map[string]proto.UsageAccount // by source ("codex:<home>", "claude:<file>")
}

// setUsageWindows is the hub's word (node.configure usage_windows): whether the node reads the windows at all. Off
// forgets what was read.
func (n *Node) setUsageWindows(on bool) {
	n.usageWin.off.Store(!on)
	if !on {
		n.usageWin.mu.Lock()
		n.usageWin.at, n.usageWin.accounts = time.Time{}, nil
		n.usageWin.mu.Unlock()
	}
}

// runUsageWindows polls on a schedule so that an answer is never a cold scan for the hub.
func (n *Node) runUsageWindows(done <-chan struct{}) {
	t := time.NewTimer(usageWindowsFirst)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		n.usageSnapshot(true)
		t.Reset(usageWindowsEvery)
	}
}

// usageSnapshot is usage.windows: the last poll, or a fresh one when there is none yet or it is older than the
// schedule (force: poll anyway).
func (n *Node) usageSnapshot(force bool) proto.UsageSnapshot {
	if n.usageWin.off.Load() {
		return proto.UsageSnapshot{Enabled: false, Accounts: []proto.UsageAccount{}}
	}
	st := &n.usageWin
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	if force || st.accounts == nil || now.Sub(st.at) >= usageWindowsEvery {
		st.accounts = mergeUsage(st.accounts, n.collectUsageWindows(now), now)
		st.at = now
	}
	keys := make([]string, 0, len(st.accounts))
	for k := range st.accounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := proto.UsageSnapshot{Enabled: true, CollectedAt: st.at, Accounts: []proto.UsageAccount{}}
	for _, k := range keys {
		out.Accounts = append(out.Accounts, st.accounts[k])
	}
	return out
}

// mergeUsage lays this poll over the kept samples: a source read now replaces its old sample, one that was not
// found keeps the last until it is older than usageWindowsMaxAge.
func mergeUsage(kept, found map[string]proto.UsageAccount, now time.Time) map[string]proto.UsageAccount {
	out := map[string]proto.UsageAccount{}
	for k, a := range kept {
		if now.Sub(a.SampledAt) <= usageWindowsMaxAge {
			out[k] = a
		}
	}
	for k, a := range found {
		out[k] = a
	}
	return out
}

// collectUsageWindows reads every source once.
func (n *Node) collectUsageWindows(now time.Time) map[string]proto.UsageAccount {
	out := map[string]proto.UsageAccount{}
	for _, home := range n.usageCodexHomes() {
		if a, ok := codexUsageAccount(home, now); ok {
			out["codex:"+home] = a
		}
	}
	files, _ := filepath.Glob(filepath.Join(n.dir, "usage", "claude*.json"))
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 || now.Sub(st.ModTime()) > usageWindowsMaxAge {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if a, ok := parseClaudeStatusLimits(data, st.ModTime(), now); ok {
			out["claude:"+filepath.Base(p)] = a
		}
	}
	return out
}

// usageCodexHomes are the Codex homes whose rollouts are read: the default one and the registered ones.
func (n *Node) usageCodexHomes() []string {
	homes := []string{n.codexHome()}
	if r := n.codices; r != nil {
		r.mu.Lock()
		for _, h := range r.config.Homes {
			if h != "" && !containsString(homes, h) {
				homes = append(homes, h)
			}
		}
		r.mu.Unlock()
	}
	return homes
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// codexUsageAccount is the account a Codex home is signed in to, as its newest rollout last saw it.
func codexUsageAccount(home string, now time.Time) (proto.UsageAccount, bool) {
	for i, r := range rollouts(home) {
		if i >= usageWindowsScan || now.Sub(r.mtime) > usageWindowsMaxAge {
			break
		}
		s, ok := lastCodexSample(r.path)
		if !ok || now.Sub(s.at) > usageWindowsMaxAge {
			continue
		}
		wins := liveWindows(s.windows, now)
		if len(wins) == 0 {
			continue
		}
		return proto.UsageAccount{Provider: "codex", Name: codexAccountName(s.plan), Plan: s.plan, Windows: wins, SampledAt: s.at}, true
	}
	return proto.UsageAccount{}, false
}

func codexAccountName(plan string) string {
	if plan == "" {
		return "Codex"
	}
	return "Codex " + strings.ToUpper(plan[:1]) + plan[1:]
}

// liveWindows drops the windows that have already reset (their number says nothing about the new one) and orders
// the rest shortest first.
func liveWindows(in []proto.UsageWindow, now time.Time) []proto.UsageWindow {
	var out []proto.UsageWindow
	for _, w := range in {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			continue
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WindowMinutes < out[j].WindowMinutes })
	return out
}

// codexLimitSample is one token_count event's rate_limits.
type codexLimitSample struct {
	at      time.Time
	plan    string
	windows []proto.UsageWindow
}

// lastCodexSample is the newest token_count rate_limits in the tail of a rollout.
func lastCodexSample(path string) (codexLimitSample, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexLimitSample{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return codexLimitSample{}, false
	}
	off := int64(0)
	if st.Size() > usageWindowsTail {
		off = st.Size() - usageWindowsTail
	}
	buf := make([]byte, st.Size()-off)
	got, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return codexLimitSample{}, false
	}
	buf = buf[:got]
	if off > 0 { // the first line of the tail is cut
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return codexLimitSample{}, false
		}
		buf = buf[i+1:]
	}
	for end := len(buf); end > 0; {
		start := bytes.LastIndexByte(buf[:end], '\n') + 1
		if s, ok := parseCodexRateLimits(buf[start:end]); ok {
			return s, true
		}
		end = start - 1
	}
	return codexLimitSample{}, false
}

type codexLimitWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes *int     `json:"window_minutes"`
	ResetsAt      *int64   `json:"resets_at"`
}

// parseCodexRateLimits reads one rollout line; ok only for a token_count event of Codex's own limit that carries at
// least one window with a length.
func parseCodexRateLimits(line []byte) (codexLimitSample, bool) {
	if !bytes.Contains(line, []byte(`"rate_limits"`)) {
		return codexLimitSample{}, false
	}
	var l struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type       string `json:"type"`
			RateLimits *struct {
				LimitID   *string           `json:"limit_id"`
				PlanType  *string           `json:"plan_type"`
				Primary   *codexLimitWindow `json:"primary"`
				Secondary *codexLimitWindow `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Payload.Type != "token_count" || l.Payload.RateLimits == nil {
		return codexLimitSample{}, false
	}
	rl := l.Payload.RateLimits
	if rl.LimitID != nil && *rl.LimitID != "" && *rl.LimitID != "codex" { // a model's own limit ("premium")
		return codexLimitSample{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		return codexLimitSample{}, false
	}
	s := codexLimitSample{at: at}
	if rl.PlanType != nil {
		s.plan = strings.ToLower(strings.TrimSpace(*rl.PlanType))
	}
	for _, w := range []*codexLimitWindow{rl.Primary, rl.Secondary} {
		if w == nil || w.UsedPercent == nil || w.WindowMinutes == nil || *w.WindowMinutes <= 0 {
			continue
		}
		win := proto.UsageWindow{Label: proto.WindowLabel(*w.WindowMinutes), WindowMinutes: *w.WindowMinutes, UsedPercent: clampPercent(*w.UsedPercent)}
		if w.ResetsAt != nil && *w.ResetsAt > 0 {
			t := time.Unix(*w.ResetsAt, 0).UTC()
			win.ResetsAt = &t
		}
		s.windows = append(s.windows, win)
	}
	return s, len(s.windows) > 0
}

func clampPercent(p float64) float64 {
	if math.IsNaN(p) || p < 0 {
		return 0
	}
	return math.Min(p, 100)
}

// claudeStatusWindows are the rate_limits keys of Claude Code's status line input and the length of each window.
var claudeStatusWindows = []struct {
	key     string
	minutes int
}{{"five_hour", 5 * 60}, {"seven_day", 7 * 24 * 60}}

// parseClaudeStatusLimits reads what a status line wrapper dropped: Claude Code's status line JSON (or just its
// rate_limits field) with five_hour / seven_day {used_percentage, resets_at}. mod is when it was written. The field
// is described in Claude Code's changelog ("rate_limits … 5-hour and 7-day windows with used_percentage and
// resets_at"); resets_at is read as epoch seconds (milliseconds and RFC 3339 are accepted too).
func parseClaudeStatusLimits(data []byte, mod, now time.Time) (proto.UsageAccount, bool) {
	var in struct {
		RateLimits map[string]json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(data, &in) != nil {
		return proto.UsageAccount{}, false
	}
	var wins []proto.UsageWindow
	for _, def := range claudeStatusWindows {
		raw, ok := in.RateLimits[def.key]
		if !ok {
			continue
		}
		var w struct {
			UsedPercentage *float64        `json:"used_percentage"`
			ResetsAt       json.RawMessage `json:"resets_at"`
		}
		if json.Unmarshal(raw, &w) != nil || w.UsedPercentage == nil {
			continue
		}
		win := proto.UsageWindow{Label: proto.WindowLabel(def.minutes), WindowMinutes: def.minutes, UsedPercent: clampPercent(*w.UsedPercentage)}
		if t, ok := transcript.ParseResetsAt(w.ResetsAt); ok {
			win.ResetsAt = &t
		}
		wins = append(wins, win)
	}
	wins = liveWindows(wins, now)
	if len(wins) == 0 {
		return proto.UsageAccount{}, false
	}
	return proto.UsageAccount{Provider: "claude", Name: "Claude", Windows: wins, SampledAt: mod.UTC()}, true
}
