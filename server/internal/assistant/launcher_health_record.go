package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ---- recording ----

// healthResult is one check's or start's outcome.
type healthResult struct {
	ok           bool
	agent, model string
	millis       int
	err          string
	source       string
	kind         string // the error's class when the outcome says it (a start's coded refusal); "": classifyLauncherError
	// resetsAt: a usage limit's reset as the outcome's source knew it (an agent's own record of the limit); nil: read
	// off err's words when they name a time zone.
	resetsAt *time.Time
}

// Where a result comes from (LauncherHealth.Source).
const (
	healthSourceCheck = "check" // launchers.check, in the node's login shell
	healthSourceStart = "start" // an agent's start, in a terminal tab
	// healthSourceSession: an agent's own turn with the launcher ended on the subscription's usage limit, its reset
	// known (launcherLimitTurnEnded). Nothing was run for it: no check, no repair.
	healthSourceSession = "session"
)

// terminalBlockedReason is why a launcher the terminal cannot run is blocked: no repair reaches the terminal's startup files.
const terminalBlockedReason = "not available in this computer's terminal: its shell reads other startup files than the login shell " +
	"Persona lists and checks launchers in (another shell, a container, an rc file that returns early). Define the launcher where " +
	"the terminal's shell reads it, or add it to Persona as a model provider; a start with it that works clears this"

// recordHealth keeps r as launcher's latest on machine; the apps are told when its state changes.
func (s *Service) recordHealth(ctx context.Context, machine, launcher string, r healthResult) LauncherHealth {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	key := healthKey(machine, launcher)
	rec := b.recs[key]
	if rec == nil {
		rec = &LauncherHealth{Machine: machine, Launcher: launcher}
		b.recs[key] = rec
	}
	before := rec.State
	if r.source != healthSourceStart && rec.Kind == errKindNotInTerminal && rec.unusable() {
		// The check runs in the login shell, where the launcher works; the terminal is what cannot run it. Only a
		// start says otherwise (#1642): a session already running says nothing of a new terminal tab.
		out := *rec
		b.mu.Unlock()
		return out
	}
	rec.CheckedAt, rec.Source, rec.Millis = time.Now().UTC(), r.source, r.millis
	if r.agent != "" {
		rec.Agent = r.agent
	}
	rec.ResetsAt = nil
	if r.ok {
		rec.State, rec.Kind, rec.Error, rec.Fails, rec.Repairs, rec.BlockedReason = healthOK, "", "", 0, 0, ""
		rec.Model = r.model
	} else {
		rec.Fails++
		rec.Error = clip(oneLine(r.err), 400)
		rec.Kind = r.kind
		if rec.Kind == "" {
			rec.Kind = classifyLauncherError(r.err)
		}
		switch {
		case rec.Kind == errKindNotInTerminal:
			rec.State, rec.BlockedReason = healthBlocked, terminalBlockedReason // no repair round (repairMachine takes failing ones)
		case rec.Kind == errKindUsageLimit:
			// The request got through to the subscription, whose limit is used up: nothing is broken, so no repair
			// is due (repairNow) and a block for an earlier error no longer stands. The reset clears it.
			rec.State, rec.BlockedReason, rec.ResetsAt = healthFailing, "", limitResetOf(r, rec.CheckedAt)
		case rec.State == "" || rec.State == healthOK:
			rec.State = healthFailing
		}
	}
	out := *rec
	s.healthSaveLocked(ctx)
	b.mu.Unlock()
	if before != out.State {
		s.healthChanged(ctx, out, before)
	}
	return out
}

// setHealthState moves launcher on machine to state ("": it stays; why: the blocked reason) and keeps fixed as its
// latest repair, one more round of this run of failures. Nothing changes once a check passed meanwhile, nor for a
// usage limit met meanwhile: it is neither repaired nor blocked, its reset clears it.
func (s *Service) setHealthState(ctx context.Context, machine, launcher, state, why string, fixed *LauncherFixed) {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	rec := b.recs[healthKey(machine, launcher)]
	if rec == nil || rec.State == healthOK || rec.Kind == errKindUsageLimit {
		b.mu.Unlock()
		return
	}
	before := rec.State
	if state != "" {
		rec.State = state
	}
	if state == healthBlocked {
		rec.BlockedReason = clip(oneLine(why), 600)
	}
	if fixed != nil {
		f := *fixed
		rec.Repair = &f
		rec.Repairs++
	}
	out := *rec
	s.healthSaveLocked(ctx)
	b.mu.Unlock()
	if before != out.State {
		s.healthChanged(ctx, out, before)
	}
}

// repairWorked marks launcher's latest repair on machine as the one that fixed it (its check passed after).
func (s *Service) repairWorked(ctx context.Context, machine, launcher string, fixed LauncherFixed) {
	b := &s.health
	b.mu.Lock()
	defer b.mu.Unlock()
	s.healthLoadLocked(ctx)
	if rec := b.recs[healthKey(machine, launcher)]; rec != nil && rec.State == healthOK {
		fixed.OK = true
		rec.Repair = &fixed
		s.healthSaveLocked(ctx)
	}
}

// healthChanged logs a launcher's new state and tells the apps (an event, not a push).
func (s *Service) healthChanged(ctx context.Context, r LauncherHealth, before string) {
	if before == "" {
		before = "new"
	}
	if r.State == healthOK {
		s.logf("launcher health: %s on %s: %s → ok", r.Launcher, r.Machine, before)
	} else {
		s.logf("launcher health: %s on %s: %s → %s (%s: %s)", r.Launcher, r.Machine, before, r.State, r.Kind, clip(r.Error, 160))
	}
	s.event(ctx, map[string]any{"type": "launcher_health", "record": r})
}

// recordStartFailure: an agent did not start with launcher on machine. A dropped link says nothing about the
// launcher, nor does a start the node turned down before running it (startRefused); anything else counts, and a
// check follows at once to tell a blink from a broken launcher. Not after a start the terminal could not run
// (errKindNotInTerminal): the check runs where it works.
func (s *Service) recordStartFailure(ctx context.Context, machine, launcher string, err error) {
	if err == nil || launcher == "" || transientStartErr(err) || errors.Is(err, ErrNoDevice) || startRefused(err) {
		return
	}
	if terminalMissing(err) {
		s.recordHealth(ctx, machine, launcher, healthResult{err: err.Error(), source: healthSourceStart, kind: errKindNotInTerminal})
		return
	}
	s.recordHealth(ctx, machine, launcher, healthResult{err: err.Error(), source: healthSourceStart})
	go func() {
		ctx := context.WithoutCancel(ctx)
		if res, ok := s.probeLauncherHealth(ctx, machine, launcher); ok {
			s.recordHealth(ctx, machine, launcher, res)
		}
	}()
}

// recordStartSuccess: an agent started with launcher on machine, so the terminal there runs it: a record saying it
// does not (errKindNotInTerminal) is cleared. Any other record stays: a start that came up says nothing of the model
// route or the key a check found broken.
func (s *Service) recordStartSuccess(ctx context.Context, machine, launcher string) {
	if launcher == "" {
		launcher = "claude"
	}
	if r, ok := s.launcherHealth(ctx, machine, launcher); !ok || r.Kind != errKindNotInTerminal || r.State == healthOK {
		return
	}
	s.recordHealth(ctx, machine, launcher, healthResult{ok: true, source: healthSourceStart})
}

// limitResetOf is when the usage limit r failed on resets, as r's source knew it or as its words say (at: when it
// was recorded); nil when unknown or further off than healthLimitMax.
func limitResetOf(r healthResult, at time.Time) *time.Time {
	reset := time.Time{}
	if r.resetsAt != nil {
		reset = *r.resetsAt
	} else if t, ok := transcript.UsageLimitReset(r.err, at); ok {
		reset = t
	}
	if reset.IsZero() || reset.Sub(at) > healthLimitMax {
		return nil
	}
	reset = reset.UTC()
	return &reset
}

// launcherLimitTurnEnded: a turn of machine's session sid ended. When it ended on the subscription's usage limit,
// with a reset still ahead, the launcher the session names counts as usage-limited on machine until then (source
// healthSourceSession): an agent started with it there would be refused at once too. Nothing runs for it, no check
// (it would spend a request to be refused) and no repair. The assistant's conversations count as well: the main
// chat runs the machine's launcher under the same sign-in, so its limit is the launcher's.
func (s *Service) launcherLimitTurnEnded(ctx context.Context, machine, sid string) {
	// Once the turn stayed still, as a task's end is judged (settleDelay): right after a write the node's list may not
	// have read the transcript yet (its meta is read again at most every few seconds), and says no error meanwhile.
	time.Sleep(s.settleDelay(ctx))
	// As the node lists it, when the fleet is here: the fields below are the node's, and the decorated copy
	// session.get answers with costs a pass over the machine's whole list.
	sess, ok := s.nodeSession(machine, sid)
	if !s.fleetOn() {
		sess, ok = s.session(ctx, machine, sid)
	}
	if !ok || sess.Launcher == "" {
		return
	}
	lim := limitOfTurn(sess)
	reset, ok := usageLimitReset(lim)
	if now := time.Now(); !ok || !reset.After(now) || reset.Sub(now) > healthLimitMax {
		return
	}
	if r, found := s.launcherHealth(ctx, machine, sess.Launcher); found {
		if at, limited := r.limitReset(); limited && !at.Before(reset) {
			return // known already (each turn a background task wakes meets it again)
		}
	}
	s.recordHealth(ctx, machine, sess.Launcher, healthResult{err: lim.TurnError, source: healthSourceSession, kind: errKindUsageLimit, resetsAt: &reset})
}

// limitOfTurn is sess's latest turn error decorated with what it says of a usage limit (decorateLimit): the node's
// word, else Claude Code's last words when they are the error. Whatever sess's state now, unlike the lists'
// turnError: a background task wakes an agent its limit stopped again and again, and it counts as working meanwhile.
func limitOfTurn(sess proto.Session) proto.Session {
	out := proto.Session{TurnErrorResetsAt: sess.TurnErrorResetsAt, UpdatedAt: sess.UpdatedAt}
	out.TurnError, out.TurnErrorKind = latestTurnError(sess)
	decorateLimit(&out)
	return out
}

// terminalMissing: err is the node's refusal of a start whose new terminal tab could not run the launcher or its claude
// (launcher_missing / claude_missing with where "terminal", node/shellmiss.go, #1642).
func terminalMissing(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || (re.Code != proto.CodeLauncherMissing && re.Code != proto.CodeClaudeMissing) {
		return false
	}
	var data struct {
		Where string `json:"where"`
	}
	return json.Unmarshal(re.Data, &data) == nil && data.Where == "terminal"
}

// startRequestErr are the node's answers to a session.start it would not run, whatever the launcher (node/control.go
// checkStart and start, node/model_config.go check): a directory that is not there, a bad name, mode, effort or model.
var startRequestErr = lazyre.New(`^directory .* does not exist$|^invalid launcher |^invalid session id |^invalid model$|^unknown permission mode |^unknown effort level |^unsupported agent type |^permission mode and effort are only supported for Claude starts|^普通 Agent 的启动只支持|^新建 Codex 的思考强度由其启动器管理`)

// startRefusalCodes are the node's (and the hub's) coded refusals of the request itself (node/control.go,
// node/codex_start.go, node/openelsewhere.go…). Codes about the machine's state (engine_missing, runtime_away,
// not_running, orca_not_registered) are not here: the launcher cannot run there now, and a task may go elsewhere.
var startRefusalCodes = []string{"codex_needs_prompt", "codex_resume", "codex_read_only", "orca_resume", "open_elsewhere", "runtime_session",
	"identity_conflict", "ultracode_unsupported", proto.CodeNodeOutdated}

// startRefused: the start failed on the request, not the launcher: the node checked it and said no before running
// anything (one of startRefusalCodes, or of startRequestErr).
func startRefused(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	if re.Code != "" {
		return slices.Contains(startRefusalCodes, re.Code)
	}
	return startRequestErr.MatchString(re.Message)
}
