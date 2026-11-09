package assistant

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Launcher health (docs/server/launcher-health.md): every launcher on every machine is checked now and then with the same
// one-word request "Test availability" sends (launchers.check); a failed start counts too. A launcher whose latest result is a
// failure is not used for a new agent while another machine has a healthy one; known errors are repaired (the node
// updates its CLI, else an agent with the configured skill), and what stays broken is blocked for the user to see.
// Nothing is pushed.

const launchersHealthKey = "launchers.health"

// The launcher's states.
const (
	healthOK        = "ok"
	healthFailing   = "failing"   // failed, not repaired yet
	healthRepairing = "repairing" // a repair agent is on it
	healthBlocked   = "blocked"   // repairs did not help (or there is none): the user's to fix
)

// The classes of a launcher's error (classifyLauncherError).
const (
	// errKindOnboarding: Claude Code's first-run setup is unfinished (hasCompletedOnboarding is missing or false).
	// Only a person at the machine can finish it, so it never enters auto repair; periodic and on-demand probes
	// clear the record once setup is done.
	errKindOnboarding  = "onboarding"
	errKindModelRoute  = "model_route"
	errKindAuth        = "auth"
	errKindCLIOutdated = "cli_outdated"
	errKindTimeout     = "timeout"
	errKindOther       = "other"
	// errKindUsageLimit: the subscription's usage limit is used up (LauncherHealth.ResetsAt, when known). Unusable until
	// it resets, then checked again; never repaired: nothing on the machine is broken.
	errKindUsageLimit = "usage_limit"
	// errKindNotInTerminal: a start's new terminal tab could not run the launcher, or its claude (node/shellmiss.go,
	// #1642): the terminal reads other startup files than the login shell the launchers are listed and checked in, so
	// a check passing says nothing of it, and no repair the manager has reaches it. Blocked at once; a start with it
	// that works clears it (or it goes stale).
	errKindNotInTerminal = "not_in_terminal"
)

var (
	// healthTick is how often the loop looks at the machines; a launcher is checked again after the interval.
	healthTick = 5 * time.Minute
	// healthOnlineRecheck: a machine that comes online gets its launchers checked unless checked this recently (the
	// hub's restarts would otherwise check everything each time).
	healthOnlineRecheck = 10 * time.Minute
	// healthStale: a failure older than this no longer keeps a launcher from being used (a usage limit with a known
	// reset: until the reset instead).
	healthStale = 6 * time.Hour
	// healthLimitMax: a usage limit said to reset further ahead than this was misread (a weekly limit resets within 7
	// days): the record counts as one without a reset.
	healthLimitMax = 8 * 24 * time.Hour
	// healthMaxRepairs: repair rounds for one run of failures before the launcher is blocked.
	healthMaxRepairs = 2
	// repairSettle and repairDeadline: a repair agent that stopped is rechecked once it has run this long; one that
	// has not stopped by the deadline is rechecked anyway.
	repairSettle   = 2 * time.Minute
	repairDeadline = 45 * time.Minute
	// healthCheckTimeout bounds one check (the node's own is 90 seconds).
	healthCheckTimeout = 100 * time.Second
	// healthRecheckThrottle: how long a failing launcher keeps the on-demand probe's answer instead of probing again,
	// so a burst of starts does not turn into a burst of probes.
	healthRecheckThrottle = 30 * time.Second
)

const defaultHealthInterval = 30 * time.Minute

// defaultRepairPermission: the repair agent runs ssh and edits CLI settings; under the default mode it would stop on
// an approval card for each step (2026-11-05 user decision).
const defaultRepairPermission = "bypassPermissions"

// LauncherHealth is one launcher on one machine as last checked.
type LauncherHealth struct {
	Machine   string    `json:"machine"`
	Launcher  string    `json:"launcher"`
	Agent     string    `json:"agent,omitempty"` // claude, codex… (as the check ran it)
	State     string    `json:"state"`
	Kind      string    `json:"kind,omitempty"` // the error's class; "" when it works
	Error     string    `json:"error,omitempty"`
	Model     string    `json:"model,omitempty"` // which model answered
	Millis    int       `json:"millis,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"`          // check | start (an agent's start failed) | session (an agent's turn met a usage limit)
	Fails     int       `json:"fails,omitempty"` // failures in a row
	// Repairs are this run of failures' repair rounds; BlockedReason says why it was given up on.
	Repairs       int            `json:"repairs,omitempty"`
	Repair        *LauncherFixed `json:"repair,omitempty"`
	BlockedReason string         `json:"blocked_reason,omitempty"`
	// ResetsAt: Kind is errKindUsageLimit and this is when the limit resets (read off the check's words when they name a
	// time zone, or an agent's own record of the limit): the launcher counts as unusable until then, and no longer after.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// LauncherFixed is the latest repair of a launcher.
type LauncherFixed struct {
	Action    string    `json:"action"` // upgrade_cli | agent
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Output    string    `json:"output,omitempty"`
	Machine   string    `json:"machine,omitempty"` // the repair agent's
	SessionID string    `json:"session_id,omitempty"`
	TodoID    string    `json:"todo_id,omitempty"`
}

// machineRepair is a repair agent at work on one machine's launchers.
type machineRepair struct {
	Machine      string    `json:"machine"` // the broken one
	Launchers    []string  `json:"launchers"`
	AgentMachine string    `json:"agent_machine"`
	SessionID    string    `json:"session_id"`
	TodoID       string    `json:"todo_id,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

// healthStore is what launchersHealthKey holds.
type healthStore struct {
	Records []LauncherHealth `json:"records"`
	Repairs []machineRepair  `json:"repairs,omitempty"`
	// Counted are the app's failed starts already counted (launcher_health_starts.go), kept across a restart while the
	// node may still list them.
	Counted []countedStart `json:"counted_starts,omitempty"`
}

type healthBook struct {
	mu       sync.Mutex
	loaded   bool
	recs     map[string]*LauncherHealth // by healthKey
	repairs  map[string]*machineRepair  // by broken machine
	online   map[string]bool            // reachable at the last look
	checking map[string]bool            // machines being checked now
	// The app's starts answered at once (launcher_health_starts.go): those seen starting, by machine and session, and
	// the failed ones already counted, when (kept with the records: healthStore.Counted).
	starting  map[[2]string]startWatch
	counted   map[[2]string]time.Time
	recheck   map[string]*healthRecheck // on-demand probes in flight, by healthKey
	recheckAt map[string]time.Time      // last on-demand probe, by healthKey
}

// healthRecheck is an on-demand launcher probe one start is running and concurrent starts wait for.
type healthRecheck struct {
	done    chan struct{}
	res     healthResult
	got     bool
	waiters int // starts that joined this probe instead of starting their own
}

func healthKey(machine, launcher string) string { return machine + "/" + launcher }

// healthConfig is manager.json agents.launcher_health with its defaults.
type healthConfig struct {
	on       bool
	interval time.Duration
	repair   assistantwire.LauncherRepairConfig
}

func (s *Service) healthConfig(ctx context.Context) healthConfig {
	c := s.hello(ctx).Agents.LauncherHealth
	out := healthConfig{on: c.Enabled != nil && *c.Enabled, interval: defaultHealthInterval, repair: c.Repair}
	if out.repair.Permission == "" {
		out.repair.Permission = defaultRepairPermission
	}
	if c.IntervalMinutes > 0 {
		out.interval = time.Duration(c.IntervalMinutes) * time.Minute
	}
	return out
}

// ---- what is stored ----

// healthLoadLocked reads the records once (b.mu held).
func (s *Service) healthLoadLocked(ctx context.Context) {
	b := &s.health
	if b.loaded {
		return
	}
	b.recs, b.repairs = map[string]*LauncherHealth{}, map[string]*machineRepair{}
	if b.online == nil {
		b.online, b.checking = map[string]bool{}, map[string]bool{}
	}
	if b.recheck == nil {
		b.recheck, b.recheckAt = map[string]*healthRecheck{}, map[string]time.Time{}
	}
	var stored healthStore
	if st := s.store(); st != nil {
		// Bounded: b.mu is held, and every start waits for it (#5467).
		rctx, cancel := context.WithTimeout(ctx, historyTimeout)
		_, err := st.Setting(rctx, launchersHealthKey, &stored)
		cancel()
		if err != nil {
			s.logf("launcher health: read: %v", err)
			return // read again next time
		}
	}
	for i := range stored.Records {
		r := stored.Records[i]
		b.recs[healthKey(r.Machine, r.Launcher)] = &r
	}
	for i := range stored.Repairs {
		r := stored.Repairs[i]
		b.repairs[r.Machine] = &r
	}
	for _, c := range stored.Counted {
		if b.counted == nil {
			b.counted = map[[2]string]time.Time{}
		}
		if key := [2]string{c.Machine, c.Session}; b.counted[key].IsZero() {
			b.counted[key] = c.At
		}
	}
	b.loaded = true
}

// healthSaveLocked writes the records (b.mu held); no database keeps them in memory only.
func (s *Service) healthSaveLocked(ctx context.Context) {
	st := s.store()
	b := &s.health
	if st == nil || !b.loaded {
		return // not over what could not be read
	}
	out := healthStore{Records: make([]LauncherHealth, 0, len(b.recs))}
	for _, r := range b.recs {
		out.Records = append(out.Records, *r)
	}
	sort.Slice(out.Records, func(i, j int) bool {
		return healthKey(out.Records[i].Machine, out.Records[i].Launcher) < healthKey(out.Records[j].Machine, out.Records[j].Launcher)
	})
	for _, r := range b.repairs {
		out.Repairs = append(out.Repairs, *r)
	}
	sort.Slice(out.Repairs, func(i, j int) bool { return out.Repairs[i].Machine < out.Repairs[j].Machine })
	for key, at := range b.counted {
		if time.Since(at) <= startFollowFor {
			out.Counted = append(out.Counted, countedStart{Machine: key[0], Session: key[1], At: at})
		}
	}
	sort.Slice(out.Counted, func(i, j int) bool {
		return out.Counted[i].Machine+"/"+out.Counted[i].Session < out.Counted[j].Machine+"/"+out.Counted[j].Session
	})
	// Bounded: b.mu is held, and every start waits for it; the records stay in memory either way (#5467).
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), historyTimeout)
	defer cancel()
	if err := st.PutSetting(wctx, launchersHealthKey, out); err != nil {
		s.logf("launcher health: save: %v", err)
	}
}

// healthRecords are the records, by machine and launcher; state and machine filter them when set.
func (s *Service) healthRecords(ctx context.Context, state, machine string) []LauncherHealth {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	out := []LauncherHealth{}
	for _, r := range b.recs {
		if (state == "" || r.State == state) && (machine == "" || r.Machine == machine) {
			out = append(out, *r)
		}
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return healthKey(out[i].Machine, out[i].Launcher) < healthKey(out[j].Machine, out[j].Launcher)
	})
	return out
}

// launcherHealth is launcher's record on machine.
func (s *Service) launcherHealth(ctx context.Context, machine, launcher string) (LauncherHealth, bool) {
	b := &s.health
	b.mu.Lock()
	defer b.mu.Unlock()
	s.healthLoadLocked(ctx)
	if r := b.recs[healthKey(machine, launcher)]; r != nil {
		return *r, true
	}
	return LauncherHealth{}, false
}

// unusable: the record says the launcher does not work (its latest result, failed within healthStale). A usage limit
// whose reset is known holds until that reset, however far off, and no longer.
func (r LauncherHealth) unusable() bool {
	if r.State == "" || r.State == healthOK {
		return false
	}
	if reset, ok := r.limitReset(); ok {
		return time.Now().Before(reset)
	}
	return time.Since(r.CheckedAt) < healthStale
}

// limitReset is when the usage limit r records resets: r failed on the subscription's usage limit, and its reset is
// known and not past healthLimitMax from when it was recorded.
func (r LauncherHealth) limitReset() (time.Time, bool) {
	if r.Kind != errKindUsageLimit || r.ResetsAt == nil || r.State == healthOK || r.ResetsAt.Sub(r.CheckedAt) > healthLimitMax {
		return time.Time{}, false
	}
	return *r.ResetsAt, true
}

// resetNote is how agents_list's lines and a detour's reason say when r's usage limit resets ("" without one):
// machine-readable, and how far off.
func (r LauncherHealth) resetNote() string {
	reset, ok := r.limitReset()
	if !ok {
		return ""
	}
	return fmt.Sprintf("resets_at=%s, %s", reset.UTC().Format(time.RFC3339), until(reset))
}

// launcherUnusable is launcher's record on machine when it does not work.
func (s *Service) launcherUnusable(ctx context.Context, machine, launcher string) (LauncherHealth, bool) {
	r, ok := s.launcherHealth(ctx, machine, launcher)
	return r, ok && r.unusable()
}

// ---- classifying ----

var launcherErrorKinds = []struct {
	kind string
	re   *lazyre.Regexp
}{
	// First: its markers are specific to the node's onboarding messages (node/claudeonboarding.go). Both the Chinese
	// and the English message carry hasCompletedOnboarding, so no Chinese literal is needed here (i18n baseline).
	{errKindOnboarding, lazyre.New(`hasCompletedOnboarding|first-run setup`)},
	// Then the limit: its words name no other cause, and its line may carry a prefix ("Error: …"). Claude Code's own
	// line ("You've hit your session limit · resets 4:20pm (…)", an older "Claude AI usage limit reached|…") and
	// Codex's (its message, its error kinds in a rollout and the app-server). A gateway's 429, "rate limit" or "quota"
	// is not a subscription's limit and stays in its class.
	{errKindUsageLimit, lazyre.New(`(?i)\byou(?:'|’)ve hit your [^·\n.]{0,40}\blimit\b|usage limit reached|usage_limit_exceeded|usageLimitExceeded`)},
	{errKindModelRoute, lazyre.New(`(?i)bare claude model name|cannot be routed|no account match|model[^.\n]{0,60}(not found|not supported|not available|does not exist|unknown)|unknown model|invalid model`)},
	{errKindAuth, lazyre.New(`(?i)\b40[13]\b|invalid (or missing )?(api[ _-]?key|x-api-key|token|bearer)|authentication_error|unauthori[sz]ed|not logged in|please run /login|token (has )?expired|permission_error|forbidden|requires \w+ auth|no \w+ auth token|auth login`)},
	{errKindCLIOutdated, lazyre.New(`(?i)unknown option|unrecognized option|unexpected argument|please (update|upgrade)|(update|upgrade) (claude|codex|your)|version[^.\n]{0,40}(too old|no longer supported|not supported|outdated)|minimum (required )?version|requires (a )?newer`)},
	{errKindTimeout, lazyre.New(`(?i)context deadline exceeded|deadline exceeded|timed? ?out|秒内没有回答`)},
}

// classifyLauncherError names the class of a launcher's error, the first that matches.
func classifyLauncherError(text string) string {
	for _, k := range launcherErrorKinds {
		if k.re.MatchString(text) {
			return k.kind
		}
	}
	return errKindOther
}

// repairNow: a failure the repairs know right away; the others wait for a second one in a row. Never a usage limit,
// however many in a row: nothing on the machine is broken, it waits for the reset. The same for an unfinished
// first-run setup: only a person at the machine can finish it.
func (r LauncherHealth) repairNow() bool {
	switch r.Kind {
	case errKindUsageLimit, errKindOnboarding:
		return false
	case errKindModelRoute, errKindAuth, errKindCLIOutdated:
		return true
	}
	return r.Fails >= 2
}

// ---- the loop and the routes ----

// healthLoop checks the launchers every healthTick until ctx ends (Run).
func (s *Service) healthLoop(ctx context.Context) {
	t := time.NewTicker(healthTick)
	defer t.Stop()
	for {
		if !s.hello(ctx).Assistant.Standby { // a member hub's machines are its center's to check
			s.healthRound(ctx, false)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) launcherHealthRoutes() {
	s.handle("GET /api/v1/launchers/health", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		apierr.WriteJSON(w, map[string]any{"records": s.healthRecords(r.Context(), q.Get("state"), q.Get("machine"))})
	})
	// A check now: of one launcher on one machine, all of a machine's, or every reachable machine's.
	s.handle("POST /api/v1/launchers/health/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		q := r.URL.Query()
		machine, launcher := q.Get("machine"), q.Get("launcher")
		cfg := s.healthConfig(ctx)
		cfg.interval = 0 // everything is due
		var out []LauncherHealth
		for _, d := range s.devices(ctx) {
			if (machine != "" && d.ID != machine) || !d.Reachable {
				continue
			}
			out = append(out, s.checkMachine(ctx, d, cfg, false, launcher)...)
		}
		if out == nil {
			out = []LauncherHealth{}
		}
		apierr.WriteJSON(w, map[string]any{"records": out})
	})
}
