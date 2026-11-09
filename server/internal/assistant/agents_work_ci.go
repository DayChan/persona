package assistant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

var ciPRURL = lazyre.New(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)\b`)
var ciPRNumber = lazyre.New(`(?i)\b(?:pr|pull request)\s*#?([1-9][0-9]*)\b`)
var ciBareNumber = lazyre.New(`(?:^|[^A-Za-z0-9_/#])#([1-9][0-9]*)\b`)
var ciArtifactNumber = lazyre.New(`^#?[1-9][0-9]*$`)
var ciArtifactRepo = lazyre.New(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)$`)
var ciWaitSignal = lazyre.New(`(?i)\b(ci|checks?|pipelines?|workflows?)\b|持续集成|检查|チェック|ワークフロー|パイプライン`)                                                                                 // i18n-ignore: match model output
var ciExplicitSignal = lazyre.New(`(?i)\b(ci|pipelines?|workflows?)\b|持续集成|ワークフロー|パイプライン`)                                                                                             // i18n-ignore: match model output
var ciExternalSignal = lazyre.New(`(?i)\bexternal\b|外部`)                                                                                                                               // i18n-ignore: match model output
var ciPRWaitSignal = lazyre.New(`(?i)\b(pr|pull request)\b|拉取请求|合并请求|プルリクエスト`)                                                                                                         // i18n-ignore: match model output
var ciBuildSignal = lazyre.New(`(?i)\b(builds?|tests?)\b|构建|测试|ビルド|テスト`)                                                                                                               // i18n-ignore: match model output
var ciOtherWait = lazyre.New(`(?i)\b(review|reviews|approval|deployment|deploy|feedback|manual|customer|client|user)\b|审核|审查|评审|部署|上线|反馈|手动|手工|客户|用户|レビュー|承認|デプロイ|フィードバック|手動|顧客|ユーザー`) // i18n-ignore: match model output

// ciWaitIntent is shared by triage and watch registration so unrelated waits
// never require a second transcript read.
func ciWaitIntent(w *proto.WorkState) bool {
	wait := w.Wait + " " + w.Line
	if ciOtherWait.MatchString(wait) || ciExternalSignal.MatchString(wait) && !ciExplicitSignal.MatchString(wait) {
		return false
	}
	return ciWaitSignal.MatchString(wait) || ciPRWaitSignal.MatchString(wait) && ciBuildSignal.MatchString(wait)
}

// startCIWatch records only an unambiguous PR. A number without a repository
// can be checked only against the session directory's current branch.
func startCIWatch(c proto.Session, e *db.SessionWork, now time.Time, ask, reply string) {
	w := &e.Work
	if e.WakeAt == nil || w.On != proto.WaitExternal || w.ResetsAt != nil {
		return
	}
	if !ciWaitIntent(w) {
		return
	}
	texts := []string{w.Wait, w.Line, c.LastText, ask, reply}
	for _, a := range w.Artifacts {
		if a.Kind == "pr" || a.Kind == "url" {
			ref := strings.TrimSpace(a.Ref)
			if a.Kind == "pr" && ciArtifactNumber.MatchString(ref) {
				ref = "PR #" + strings.TrimPrefix(ref, "#")
			} else if match := ciArtifactRepo.FindStringSubmatch(ref); a.Kind == "pr" && len(match) == 3 {
				ref = "https://github.com/" + match[1] + "/pull/" + match[2]
			}
			texts = append(texts, ref)
		}
	}
	var repo string
	var number int
	for _, text := range texts {
		for _, match := range ciPRURL.FindAllStringSubmatch(text, -1) {
			n, _ := strconv.Atoi(match[2])
			if n == 0 || number != 0 && number != n || repo != "" && !strings.EqualFold(repo, match[1]) {
				return
			}
			repo, number = match[1], n
		}
		for _, pattern := range []*lazyre.Regexp{ciPRNumber, ciBareNumber} {
			for _, match := range pattern.FindAllStringSubmatch(text, -1) {
				n, _ := strconv.Atoi(match[1])
				if n == 0 || number != 0 && number != n {
					return
				}
				number = n
			}
		}
	}
	if number == 0 || repo == "" && c.Cwd == "" {
		return
	}
	w.CI = &proto.WorkCI{Repo: repo, Number: number, FallbackAt: *e.WakeAt}
	at := now.Add(time.Minute).UTC()
	if at.After(*e.WakeAt) {
		at = *e.WakeAt
	}
	e.WakeAt, w.WakeAt = &at, &at
}

// startCIPoll keeps slow providers from blocking unrelated work, while limiting
// concurrent requests and preventing overlapping polls for one session.
func (s *Service) startCIPoll(ctx context.Context, key [2]string, now time.Time) {
	s.work.mu.Lock()
	s.work.init()
	if s.work.ciPolling[key] || len(s.work.ciPolling) >= 3 {
		s.work.mu.Unlock()
		return
	}
	s.work.ciPolling[key] = true
	s.work.mu.Unlock()
	go func() {
		defer func() {
			s.work.mu.Lock()
			delete(s.work.ciPolling, key)
			s.work.mu.Unlock()
		}()
		s.workWake(ctx, s.newWorkView(ctx), key, now)
	}()
}

type ciResult struct {
	pending     bool
	sourceError bool // no source produced a usable result
	failures    []string
	count       int
	fingerprint string
}

// ciChecks first asks the active, authorized connector. The current-branch
// node RPC is safe for a number only after it returns the requested PR; a full
// page of 100 checks is refused because the RPC cannot prove it was complete.
func (s *Service) ciChecks(ctx context.Context, e db.SessionWork, c proto.Session) (ciResult, time.Time, bool) {
	ci := e.Work.CI
	// Connector access follows the same trust boundary as session tools. A
	// guest must not acquire the owner's credentials through a background watch.
	_, guest, err := s.guestOf(ctx, e.MachineID, e.SessionID)
	if err != nil || guest || s.isWorkMachine(e.MachineID) {
		return ciResult{}, time.Time{}, false
	}
	checks, hasChecks := s.ext.(interface {
		PRChecks(context.Context, string, int) (github.PRCheckSnapshot, time.Time, bool, error)
	})
	retryable := false
	fromConnector := func(repo string) (ciResult, time.Time, bool) {
		if !hasChecks || repo == "" {
			return ciResult{}, time.Time{}, false
		}
		result, reset, available, err := checks.PRChecks(ctx, repo, ci.Number)
		if available && !reset.IsZero() {
			return ciResult{}, reset, true
		}
		if err != nil {
			text := strings.ToLower(err.Error())
			if !strings.Contains(text, " 401 ") && !strings.Contains(text, " 403 ") && !strings.Contains(text, " 404 ") {
				retryable = true
			}
			return ciResult{}, time.Time{}, false
		}
		if !available {
			return ciResult{}, time.Time{}, false
		}
		return ciResult{pending: result.Pending, failures: result.Failures, count: result.Count, fingerprint: result.Fingerprint}, time.Time{}, true
	}
	if result, reset, ok := fromConnector(ci.Repo); ok {
		return result, reset, true
	}
	if c.Cwd == "" {
		if retryable {
			return ciResult{sourceError: true}, time.Time{}, true
		}
		return ciResult{}, time.Time{}, false
	}
	var result struct {
		PR *struct {
			Number         int                            `json:"number"`
			URL            string                         `json:"url"`
			HeadOID        string                         `json:"head_oid"`
			ChecksComplete bool                           `json:"checks_complete"`
			Checks         []struct{ Name, State string } `json:"checks"`
		} `json:"pr"`
	}
	if err := s.call(ctx, e.MachineID, "git.pr", map[string]any{"cwd": c.Cwd}, &result); err != nil {
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "rate limit") || strings.Contains(text, "429") {
			return ciResult{}, time.Now().Add(time.Hour), true
		}
		var re *rpc.Error
		unavailable := errors.As(err, &re) && (re.Code == proto.CodeNoGh || re.Code == proto.CodeNotSignedIn || re.Code == "method_not_found")
		unavailable = unavailable || strings.Contains(text, "unknown method") || strings.Contains(text, "method_not_found") || strings.Contains(text, "unknown json field")
		if retryable || !unavailable {
			return ciResult{sourceError: true}, time.Time{}, true
		}
		return ciResult{}, time.Time{}, false
	}
	if result.PR == nil || result.PR.Number != ci.Number {
		if retryable {
			return ciResult{sourceError: true}, time.Time{}, true
		}
		return ciResult{}, time.Time{}, false
	}
	match := ciPRURL.FindStringSubmatch(result.PR.URL)
	if len(match) != 3 || ci.Repo != "" && !strings.EqualFold(ci.Repo, match[1]) {
		if retryable {
			return ciResult{sourceError: true}, time.Time{}, true
		}
		return ciResult{}, time.Time{}, false
	}
	if ci.Repo == "" {
		if result, reset, ok := fromConnector(match[1]); ok {
			return result, reset, true
		}
	}
	if !result.PR.ChecksComplete || result.PR.HeadOID == "" || len(result.PR.Checks) >= 100 {
		if retryable {
			return ciResult{sourceError: true}, time.Time{}, true
		}
		return ciResult{}, time.Time{}, false
	}
	out := ciResult{count: len(result.PR.Checks)}
	var identities []string
	for _, check := range result.PR.Checks {
		identities = append(identities, fmt.Sprintf("%q:%q", check.Name, check.State))
		if check.Name == "" {
			out.pending = true
			continue
		}
		switch check.State {
		case "pass", "skipped":
		case "fail":
			out.failures = append(out.failures, check.Name)
		default:
			out.pending = true
		}
	}
	sort.Strings(out.failures)
	sort.Strings(identities)
	fingerprint := sha256.Sum256([]byte(result.PR.HeadOID + "\x00" + strings.Join(identities, "\x00")))
	out.fingerprint = fmt.Sprintf("%x", fingerprint)
	return out, time.Time{}, true
}

// pollCI returns a localized result only when every check completed. handled
// means the next action was persisted (or the watch was cancelled).
func (s *Service) pollCI(ctx context.Context, e db.SessionWork, c proto.Session, now time.Time) (string, bool) {
	if e.Work.CI == nil {
		return "", false
	}
	ci := *e.Work.CI
	pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	result, reset, available := s.ciChecks(pollCtx, e, c)
	cancel()
	key := [2]string{e.MachineID, e.SessionID}
	current, ok := s.workEntry(key)
	if !ok || current.WakeAt == nil || current.Work.CI == nil || !current.TurnAt.Equal(e.TurnAt) || *current.Work.CI != ci {
		return "", true
	}
	if _, why := s.workGuard(s.newWorkView(ctx), current, false); why != "" {
		if why == "offline" {
			at := now.Add(time.Minute).UTC()
			current.WakeAt, current.Work.WakeAt = &at, &at
		} else {
			current.WakeAt, current.Work.WakeAt, current.Work.CI = nil, nil, nil
		}
		s.replaceCI(e, current)
		return "", true
	}
	if !s.triageSwitches(ctx).Wake {
		current.WakeAt, current.Work.WakeAt, current.Work.CI = nil, nil, nil
		s.replaceCI(e, current)
		return "", true
	}
	_, guest, err := s.guestOf(ctx, e.MachineID, e.SessionID)
	if err != nil || guest || s.isWorkMachine(e.MachineID) {
		available = false
	}
	if !available {
		current.Work.CI = nil
		at := ci.FallbackAt
		if at.Before(now) {
			at = now
		}
		current.WakeAt, current.Work.WakeAt = &at, &at
		s.replaceCI(e, current)
		return "", true
	}
	// A provider error is not a pending check. A permanently unusable source
	// must not hold the agent forever; actual checks and rate limits are uncapped.
	if result.sourceError {
		ci.Observed, ci.ObservedAt = "", time.Time{}
		ci.SourceErrors = min(max(ci.SourceErrors, 0), 3) + 1
		if ci.SourceErrors >= 3 {
			current.Work.CI = nil
			at := ci.FallbackAt
			if at.Before(now) {
				at = now.UTC()
			}
			current.WakeAt, current.Work.WakeAt = &at, &at
		} else {
			current.Work.CI = &ci
			at := now.Add(time.Minute * time.Duration(1<<(ci.SourceErrors-1))).UTC()
			current.WakeAt, current.Work.WakeAt = &at, &at
		}
		s.replaceCI(e, current)
		return "", true
	}
	ci.SourceErrors = 0
	// An empty set does not establish that the PR has a CI source. Give checks
	// time to register, then use the ordinary scheduled wake.
	if reset.IsZero() && result.count == 0 && !now.Before(ci.FallbackAt) {
		current.Work.CI = nil
		at := now.UTC()
		current.WakeAt, current.Work.WakeAt = &at, &at
		s.replaceCI(e, current)
		return "", true
	}
	if !reset.IsZero() || result.pending || result.count == 0 || result.fingerprint == "" {
		ci.Observed, ci.ObservedAt = "", time.Time{}
		ci.Polls = min(ci.Polls+1, 15) // cap the interval, not the lifetime
		current.Work.CI = &ci
		delay := time.Minute * time.Duration(1<<min(ci.Polls/5, 3))
		at := now.Add(delay).UTC()
		if reset.After(at) {
			at = reset.Add(time.Minute).UTC()
		}
		current.WakeAt, current.Work.WakeAt = &at, &at
		s.replaceCI(e, current)
		return "", true
	}
	// GitHub can register workflows after a fast check has already finished.
	// Require the same complete set on the same head after an observation period.
	if ci.Observed != result.fingerprint || ci.ObservedAt.IsZero() || now.Before(ci.ObservedAt.Add(2*time.Minute)) {
		if ci.Observed != result.fingerprint || ci.ObservedAt.IsZero() {
			ci.Observed, ci.ObservedAt = result.fingerprint, now.UTC()
		}
		at := ci.ObservedAt.Add(2 * time.Minute).UTC()
		current.Work.CI = &ci
		current.WakeAt, current.Work.WakeAt = &at, &at
		s.replaceCI(e, current)
		return "", true
	}
	if len(result.failures) == 0 {
		return workT(s.lang(ctx), "ci_passed"), false
	}
	return workT(s.lang(ctx), "ci_failed", i18n.Args{"checks": ciCheckNames(result.failures)}), false
}

// ciClosedTodo checks durable retirements and returns a proof of the exact
// closed-todo versions that this turn was allowed to follow. Only a watch's own
// persisted proof may be reused; a newly finished todo invalidates it.
func (s *Service) ciClosedTodo(ctx context.Context, machine, sid, existingProof string, prior proto.WorkState, turn time.Time) (bool, string, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, "", err
	}
	key := [2]string{machine, sid}
	s.work.mu.Lock()
	started := s.work.started[key]
	s.work.mu.Unlock()
	s.todos.mu.RLock()
	if _, open := s.todos.activeBySession[key]; open {
		s.todos.mu.RUnlock()
		return false, "", nil
	}
	var identities []string
	var latest time.Time
	for _, todo := range s.todos.todos {
		if todo.DoneAt == nil {
			continue
		}
		for _, agent := range todo.Agents {
			if agent.MachineID == machine && agent.SessionID == sid && agent.ReleasedAt == nil && agent.DismissedAt == nil {
				reopened := ""
				if todo.ReopenedAt != nil {
					reopened = todo.ReopenedAt.UTC().Format(time.RFC3339Nano)
				}
				identities = append(identities, fmt.Sprintf("%q:%s:%s", todo.ID, todo.DoneAt.UTC().Format(time.RFC3339Nano), reopened))
				if todo.DoneAt.After(latest) {
					latest = *todo.DoneAt
				}
				break
			}
		}
	}
	s.todos.mu.RUnlock()
	if len(identities) == 0 {
		return false, "", nil
	}
	sort.Strings(identities)
	hash := sha256.Sum256([]byte(strings.Join(identities, "\x00")))
	proof := fmt.Sprintf("%x", hash)
	if existingProof == proof || started.After(latest) {
		return false, proof, nil
	}
	// RetiredAt and todo.DoneAt both come from the manager; the two turn
	// timestamps both come from the node. Never compare across these clocks.
	if prior.RetiredAt != nil && !prior.RetiredAt.Before(latest) && prior.RetiredTurnAt != nil &&
		!turn.IsZero() && turn.After(*prior.RetiredTurnAt) && !sameTurn(proto.Session{UpdatedAt: turn}, *prior.RetiredTurnAt) {
		return false, proof, nil
	}
	return true, "", nil
}

// ciCheckNames are untrusted external data. Quote and escape each bounded name
// so it cannot add lines or close a markup delimiter in the wake message.
func ciCheckNames(names []string) string {
	out := make([]string, 0, min(len(names), 5))
	for _, name := range names {
		if len(out) == 5 {
			break
		}
		out = append(out, strconv.Quote(html.EscapeString(clip(oneLine(name), 40))))
	}
	if len(names) > len(out) {
		out = append(out, "…")
	}
	return strings.Join(out, ", ")
}

// sameCIWatch verifies both the turn and the specific scheduled watch.
func sameCIWatch(a, b db.SessionWork) bool {
	return a.TurnAt.Equal(b.TurnAt) && a.WakeAt != nil && b.WakeAt != nil && a.WakeAt.Equal(*b.WakeAt) &&
		a.Work.CI != nil && b.Work.CI != nil && *a.Work.CI == *b.Work.CI
}

// replaceCI applies a poll only while its original watch is still current.
// A user action or a new turn must win even if it arrived during the network call.
func (s *Service) replaceCI(before, after db.SessionWork) bool {
	key := [2]string{before.MachineID, before.SessionID}
	s.work.mu.Lock()
	current := s.work.entries[key]
	if current == nil || !sameCIWatch(before, *current) {
		s.work.mu.Unlock()
		return false
	}
	after.UpdatedAt = time.Now().UTC()
	*current = after
	s.work.mu.Unlock()
	s.saveWork(after)
	s.publishSoon()
	return true
}

// prepareCI persists the exact wake before any send. A restart can retry it
// with the same client message id through the hub’s once-only guard.
func (s *Service) prepareCI(ctx context.Context, e db.SessionWork, text string) bool {
	key := [2]string{e.MachineID, e.SessionID}
	unlock := s.lockWorkWrite(key)
	defer unlock()
	s.work.mu.Lock()
	current := s.work.entries[key]
	if current == nil || !sameCIWatch(e, *current) || current.Work.CI.Message != "" {
		s.work.mu.Unlock()
		return false
	}
	saved := *current
	ci := *current.Work.CI
	ci.Message = text
	saved.Work.CI = &ci
	saved.Wakes = e.Wakes
	saved.UpdatedAt = time.Now().UTC()
	s.work.mu.Unlock()
	if st := s.workDB(); st != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := st.PutSessionWork(writeCtx, saved)
		cancel()
		if err != nil {
			s.logf("work: save CI wake: %v", err)
			return false
		}
	}
	s.work.mu.Lock()
	current = s.work.entries[key]
	if current == nil || !sameCIWatch(e, *current) || current.Work.CI.Message != "" {
		s.work.mu.Unlock()
		return false
	}
	*current = saved
	s.work.mu.Unlock()
	s.publishSoon()
	return true
}

// deliverCI uses the persisted outbox and the hub’s durable message-id guard.
// An uncertain delivery is never repeated by the hub.
func (s *Service) deliverCI(ctx context.Context, e db.SessionWork, now time.Time) {
	if e.Work.CI == nil || e.Work.CI.Message == "" {
		return
	}
	if _, why := s.workGuard(s.newWorkView(ctx), e, false); why != "" || !s.triageSwitches(ctx).Wake {
		return
	}
	_, guest, err := s.guestOf(ctx, e.MachineID, e.SessionID)
	if err != nil || guest || s.isWorkMachine(e.MachineID) {
		return
	}
	if closed, _, err := s.ciClosedTodo(ctx, e.MachineID, e.SessionID, e.Work.CI.TodoProof, proto.WorkState{}, time.Time{}); err != nil || closed {
		return
	}
	sum := sha256.Sum256([]byte(e.MachineID + "\x00" + e.SessionID + "\x00" + e.TurnAt.UTC().Format(time.RFC3339Nano)))
	id := fmt.Sprintf("ci-%x", sum)
	key := [2]string{e.MachineID, e.SessionID}
	s.work.mu.Lock()
	current := s.work.entries[key]
	if current == nil || current.WakeAt == nil || current.Work.CI == nil || !current.TurnAt.Equal(e.TurnAt) || current.Work.CI.Message != e.Work.CI.Message {
		s.work.mu.Unlock()
		return
	}
	s.work.sent[key] = time.Now()
	s.work.mu.Unlock()
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = s.call(sendCtx, e.MachineID, DeviceSessionSendOnce, map[string]any{"session_id": e.SessionID, "text": e.Work.CI.Message, "client_msg_id": id}, nil)
	cancel()
	s.work.mu.Lock()
	current = s.work.entries[key]
	if current == nil || current.WakeAt == nil || current.Work.CI == nil || !current.TurnAt.Equal(e.TurnAt) || current.Work.CI.Message != e.Work.CI.Message {
		s.work.mu.Unlock()
		return
	}
	if err != nil {
		at := now.Add(2 * time.Minute).UTC()
		current.WakeAt, current.Work.WakeAt = &at, &at
	} else {
		current.WakeAt, current.Work.WakeAt, current.Work.CI = nil, nil, nil
	}
	saved := *current
	s.work.mu.Unlock()
	s.saveWork(saved)
	s.publishSoon()
	if err != nil {
		s.logf("work %s/%s: CI wake: %v", e.MachineID, short(e.SessionID), err)
		return
	}
	s.workEvent(e.MachineID, e.SessionID, "woke", map[string]any{"attempt": e.Wakes, "after": now.Sub(e.TurnAt).Seconds()})
}
