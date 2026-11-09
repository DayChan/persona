package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The independent review of a supervised task's change (#2173, docs/assistant/unified-entry.md "Independent review").
// With manager.json agents.review set, a supervised task the judge says is done does not wait for review at once: a
// fresh reviewer agent (its own launcher and model) first reviews the change in a disposable copy of it
// (worktree.snapshot on the agent's computer), seeded only with the task's ask, its done_when and the change from
// git.review, never the working agent's transcript. Blocking findings go back to the agent as the next rework round
// (the supervisor's rework path and maxReworkRounds); no blocking findings, or no round left, and the task waits for
// review as before, the review attached to its ledger (AutoReview) and to the supervisor's message.
//
// The run is a sub-state of the ledger, not a review state of its own: the task stays "" (or rework) while it runs, so
// the apps, which know the review states, show it going on. Its states:
//
//	running ─┬─ clean     no blocking findings: pending_review
//	         ├─ blocking  blocking findings: rework round, or pending_review once the rounds are used
//	         ├─ failed    no copy, no reviewer, no answer, past its deadline: pending_review as without a review
//	         └─ cancelled the agent worked again, or the supervisor decided, meanwhile: nothing follows from it
//
// Every transition is a compare-and-set on the ledger (the run's id and running), so a turn end, the tick after a hub
// restart and a cancellation decide it once. The reviewer is linked to the todo (source "review", via the run's id)
// released from the start: it is never one of the task's working agents. It is hidden from the session lists and kept
// quiet (no pushes), and once the run ends it is closed and its copy removed (retried by the tick until both went).

// The states of an AutoReview.
const (
	autoRunning   = "running"
	autoClean     = "clean"
	autoBlocking  = "blocking"
	autoFailed    = "failed"
	autoCancelled = "cancelled"
)

// sourceReview is the todo_agents source of a reviewer.
const sourceReview = "review"

// reviewerBy is TaskReview.By for a rework the reviewer's findings started.
const reviewerBy = "reviewer"

// AutoReview is one independent review run, as the ledger keeps it and the apps get it.
type AutoReview struct {
	ID    string `json:"id"` // rv_<hex>: the reviewer's todo_agents via
	State string `json:"state"`
	// Work names the stretch of work it reviews (superviseJudged's: when it began), Round the ledger's round then,
	// Judged the judge's reason (for the supervisor's message once it is done).
	Work   string `json:"work"`
	Round  int    `json:"round"`
	Judged string `json:"judged,omitempty"`
	// MachineID and Cwd are the working agent's computer and directory; SessionID the reviewer there, Worktree its copy.
	MachineID string     `json:"machine_id"`
	Cwd       string     `json:"cwd"`
	SessionID string     `json:"session_id,omitempty"`
	Worktree  string     `json:"worktree,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// What it found: the report as the reviewer wrote it (the count line taken out), how many findings, how many of
	// them blocking (P0, P1); Error why it failed or was cancelled.
	Findings string `json:"findings,omitempty"`
	Count    int    `json:"count"`
	Blocking int    `json:"blocking"`
	Error    string `json:"error,omitempty"`
}

// Why a run failed or was cancelled, and what the supervisor hears when its rework did not reach the agent: model
// input (the supervisor's message, prompts/assistant/supervise.md), Chinese like it, and not shown to people as is.
const (
	whyNoDiff        = "读不到这次的改动："                 // i18n-ignore: the supervisor's prompt
	whyNoChange      = "没有可评审的代码改动"                // i18n-ignore: the supervisor's prompt
	whyNoCopy        = "没能为评审建一份改动的副本："            // i18n-ignore: the supervisor's prompt
	whyNoReviewerErr = "评审员没开起来："                  // i18n-ignore: the supervisor's prompt
	whyNoMarker      = "评审员没按格式给出结论"               // i18n-ignore: the supervisor's prompt
	whyDecided       = "督办已经给出了结论"                 // i18n-ignore: the supervisor's prompt
	whyReworkUnsent  = "独立评审发现了阻断问题，但返工没送到 Agent：" // i18n-ignore: the supervisor's prompt
	whySilent        = "评审员什么都没说"                  // i18n-ignore: the supervisor's prompt
	whyAgentWorks    = "Agent 又开始工作了"              // i18n-ignore: the supervisor's prompt
	whyTodoDone      = "任务已经结束"                    // i18n-ignore: the supervisor's prompt
	whyTimeout       = "评审超时"                      // i18n-ignore: the supervisor's prompt
	whyNoReviewer    = "评审员没开起来"                   // i18n-ignore: the supervisor's prompt
)

// Limits of a run.
var (
	// autoReviewStartWait: a run with no reviewer after this long never got one (the hub restarted while starting it).
	autoReviewStartWait = 10 * time.Minute
	// autoReviewCleanFor: an ended run's reviewer and copy are taken away for this long after it ended.
	autoReviewCleanFor = 24 * time.Hour
)

// Budgets of the reviewer's first message (characters).
const (
	reviewDiffBudget = 150_000
	reviewFileBudget = 20_000
	reviewAskBudget  = 6_000
	reviewGitTimeout = 75 * time.Second
	reviewFindingMax = 12_000
)

// autoReviewBook is what only matters while the manager runs: the ended runs already cleaned up.
type autoReviewBook struct {
	mu      sync.Mutex
	cleaned map[string]bool // run id|reviewer|copy
}

func (s *Service) reviewConfig(ctx context.Context) assistantwire.ReviewConfig {
	return s.hello(ctx).Agents.Review
}

// ---- starting ----

// autoReviewBegin: supervised task t's agents stopped after work (work: when it began) and the judge said done, for
// reason. True when the review takes it from here (it started now, or one already ran or runs for this work): the task
// does not wait for review yet. False: no review (not configured, no agent directory): it waits for review as before.
func (s *Service) autoReviewBegin(ctx context.Context, t Todo, work time.Time, reason string) bool {
	if !s.reviewConfig(ctx).On() || t.Supervision == nil {
		return false
	}
	key := work.UTC().Format(time.RFC3339Nano)
	if a := t.Supervision.AutoReview; a != nil && (a.Work == key || a.State == autoRunning) {
		return true
	}
	agents := s.taskAgents(ctx, t)
	if len(agents) == 0 {
		return false
	}
	c := pickTaskAgent(agents)
	cwd := cmp.Or(c.WorkspaceCwd, c.Cwd)
	if cwd == "" {
		return false
	}
	if prev := t.Supervision.AutoReview; prev != nil {
		s.cleanAutoReview(ctx, *prev) // the run it replaces on the ledger: its leftovers go first
	}
	run := AutoReview{ID: "rv_" + randomHex(6), State: autoRunning, Work: key, Judged: clip(oneLine(reason), 300),
		MachineID: c.MachineID, Cwd: cwd, StartedAt: time.Now().UTC()}
	began, taken := false, false
	_, _, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		if a := v.AutoReview; a != nil && (a.Work == key || a.State == autoRunning) {
			taken = true
			return false
		}
		if st := v.state(t); st != "" && st != reviewRework {
			return false
		}
		run.Round = v.Round
		v.AutoReview = &run
		began = true
		return true
	})
	if err != nil {
		s.logf("task %s: review: %v", t.ID, err)
		return false
	}
	if !began {
		return taken
	}
	s.logf("task %s: review %s begins on %s", t.ID, run.ID, run.MachineID)
	go s.runAutoReview(context.WithoutCancel(ctx), t.ID, run)
	return true
}

// runAutoReview gets run's change, its copy and its reviewer; what fails on the way fails the run.
func (s *Service) runAutoReview(ctx context.Context, todoID string, run AutoReview) {
	t, ok := s.todoNow(todoID)
	if !ok {
		return
	}
	rev, scope, err := s.reviewChange(ctx, run.MachineID, run.Cwd)
	if err != nil {
		s.endAutoReview(ctx, todoID, run.ID, reviewEnd{failed: whyNoDiff + err.Error()})
		return
	}
	if len(rev.Files) == 0 && !rev.Truncated {
		s.endAutoReview(ctx, todoID, run.ID, reviewEnd{failed: whyNoChange})
		return
	}
	diff, omitted := reviewDiff(*rev)

	var snap proto.WorktreeSnapshot
	qctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err = s.call(qctx, run.MachineID, "worktree.snapshot", map[string]any{"cwd": run.Cwd, "name_hint": t.Title}, &snap)
	cancel()
	if err != nil || snap.Path == "" {
		s.endAutoReview(ctx, todoID, run.ID, reviewEnd{failed: whyNoCopy + errText(err)})
		return
	}
	if !s.updateAutoReview(ctx, todoID, run.ID, func(a *AutoReview) { a.Worktree = snap.Path }) {
		run.Worktree = snap.Path
		s.cleanAutoReview(ctx, run) // ended meanwhile
		return
	}
	run.Worktree = snap.Path

	cfg := s.reviewConfig(ctx)
	prompt := prompts.RenderAgentReview(prompts.AgentReview{Language: answerLanguage(s.hello(ctx).Locale), Title: t.Title,
		Ask: clipMiddle(strings.TrimSpace(t.Notes), reviewAskBudget), DoneWhen: t.Supervision.DoneWhen, Scope: scope, Diff: diff,
		Omitted: omitted})
	params := map[string]any{"cwd": snap.Path, "launcher": cfg.Launcher, "prompt": prompt,
		"permission_mode": cfg.PermissionMode(), "tab_name": "review-" + short(todoID)}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	if cfg.Effort != "" {
		params["effort"] = cfg.Effort
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	sctx, cancel := context.WithTimeout(ctx, taskStartTimeout)
	err = s.call(sctx, run.MachineID, DeviceSessionStart, params, &res)
	cancel()
	if err == nil && (res.Session.SessionID == "" || res.Session.StartError != "") {
		err = errors.New(cmp.Or(res.Session.StartError, "no session"))
	}
	if err != nil {
		s.endAutoReview(ctx, todoID, run.ID, reviewEnd{failed: whyNoReviewerErr + err.Error()})
		return
	}
	sid := res.Session.SessionID
	if !s.updateAutoReview(ctx, todoID, run.ID, func(a *AutoReview) { a.SessionID = sid }) {
		run.SessionID = sid
		s.cleanAutoReview(ctx, run)
		return
	}
	if err := s.linkReviewer(ctx, todoID, run.MachineID, sid, snap.Path, run.ID); err != nil {
		s.logf("task %s: review %s: link %s: %v", todoID, run.ID, short(sid), err)
	}
	s.publishSoon() // the reviewer leaves the session lists
	s.logf("task %s: review %s: reviewer %s on %s", todoID, run.ID, short(sid), run.MachineID)
}

func errText(err error) string {
	if err == nil {
		return "the node made none"
	}
	return err.Error()
}

// updateAutoReview changes run id of todo id while it runs; false when it no longer does (ended, replaced, no ledger).
func (s *Service) updateAutoReview(ctx context.Context, todoID, id string, change func(*AutoReview)) bool {
	ok := false
	_, _, err := s.updateSupervision(ctx, todoID, func(v *Supervision) bool {
		if a := v.AutoReview; a != nil && a.ID == id && a.State == autoRunning {
			change(a)
			ok = true
		}
		return ok
	})
	if err != nil {
		s.logf("task %s: review %s: %v", todoID, id, err)
		return false
	}
	return ok
}

// linkReviewer records the reviewer on the todo (sourceReview, released: see above).
func (s *Service) linkReviewer(ctx context.Context, todoID, mid, sid, cwd, via string) error {
	now := time.Now().UTC()
	title := "review"
	if st := s.supervisionDB(); st != nil && s.todoDB() != nil {
		if err := st.LinkTodoReviewer(ctx, todoID, mid, sid, title, cwd, via, now); err != nil {
			return err
		}
	}
	s.todos.mu.Lock()
	if t := s.todos.todos[todoID]; t != nil {
		found := false
		for _, a := range t.Agents {
			found = found || a.MachineID == mid && a.SessionID == sid
		}
		if !found {
			t.Agents = append(t.Agents, TodoAgent{MachineID: mid, SessionID: sid, LinkedAt: now, ReleasedAt: &now, Title: title, Cwd: cwd,
				Source: sourceReview, Via: via})
		}
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return nil
}

// ---- the change ----

// reviewChange is the change at cwd: the branch against the default branch, uncommitted when there is no base to
// compare with; and the one line naming what it is compared with.
func (s *Service) reviewChange(ctx context.Context, machine, cwd string) (*proto.GitReview, string, error) {
	qctx, cancel := context.WithTimeout(ctx, reviewGitTimeout)
	defer cancel()
	var out proto.GitReview
	call := func(scope string) error {
		out = proto.GitReview{}
		return s.call(qctx, machine, "git.review", proto.GitReviewRequest{Cwd: cwd, Scope: scope}, &out)
	}
	if err := call(proto.ReviewBranch); err != nil {
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != proto.CodeNoDefaultBranch && re.Code != proto.CodeNoMergeBase {
			return nil, "", err
		}
		if err := call(proto.ReviewUncommitted); err != nil {
			return nil, "", err
		}
	}
	return &out, reviewScopeLine(out), nil
}

// reviewScopeLine names what the review's diff is compared with. It goes into the reviewer's prompt (agents/review),
// model input like the other agents/ prompts, so it is Chinese and not localized.
func reviewScopeLine(r proto.GitReview) string {
	switch {
	case r.Base.Empty:
		return "空仓库（首次提交前，所有文件都是新增）" // i18n-ignore: the reviewer's prompt, as agents/review.md
	case r.Scope == proto.ReviewUncommitted:
		return "未提交的改动（工作区相对 HEAD）" // i18n-ignore: the reviewer's prompt, as agents/review.md
	case r.Base.Short != "" && strings.TrimSpace(r.Base.Subject) != "":
		return fmt.Sprintf("分支相对默认分支的改动（自 %s「%s」起，含未提交）", r.Base.Short, oneLine(strings.TrimSpace(r.Base.Subject))) // i18n-ignore: the reviewer's prompt, as agents/review.md
	default:
		return "分支相对默认分支的改动（含未提交）" // i18n-ignore: the reviewer's prompt, as agents/review.md
	}
}

// reviewDiff renders the review's files as unified-diff-style blocks: files in path order, each line with its
// prefix character, within the budgets. A file that is binary, refused (secrets), or over its share is one line
// instead and spends none of the total budget; once that runs out, the files after it are one lines too, and
// omitted counts them.
func reviewDiff(r proto.GitReview) (out string, omitted int) {
	var b strings.Builder
	spent := 0
	for _, f := range r.Files {
		if reason, one := fileWithoutHunks(f); one {
			fmt.Fprintf(&b, "%s — +%d/−%d 行，diff 略（%s）\n", reviewFilePath(f), f.Added, f.Deleted, reason) // i18n-ignore: the reviewer's prompt, as agents/review.md
			continue
		}
		block := reviewFileBlock(f)
		if len(block) > reviewFileBudget {
			fmt.Fprintf(&b, "%s — +%d/−%d 行，diff 略（太大）\n", reviewFilePath(f), f.Added, f.Deleted) // i18n-ignore: the reviewer's prompt, as agents/review.md
			continue
		}
		if spent+len(block) > reviewDiffBudget {
			omitted++
			fmt.Fprintf(&b, "%s — +%d/−%d 行，diff 略（超总量）\n", reviewFilePath(f), f.Added, f.Deleted) // i18n-ignore: the reviewer's prompt, as agents/review.md
			continue
		}
		spent += len(block)
		b.WriteString(block)
	}
	return b.String(), omitted
}

// fileWithoutHunks: the file's diff is not shown at all — binary, refused by the file guard (secrets), or too big
// for a page. The string is the reason, in the reviewer's prompt's language.
func fileWithoutHunks(f proto.GitReviewFile) (reason string, one bool) {
	switch {
	case f.Refused:
		return "含敏感信息，未展示", true // i18n-ignore: the reviewer's prompt, as agents/review.md
	case f.Binary:
		return "二进制文件", true // i18n-ignore: the reviewer's prompt, as agents/review.md
	case f.Big:
		return "太大", true // i18n-ignore: the reviewer's prompt, as agents/review.md
	}
	return "", false
}

// reviewFilePath names a file (a rename names both sides).
func reviewFilePath(f proto.GitReviewFile) string {
	if f.OldPath != "" && f.OldPath != f.Path {
		return f.OldPath + " → " + f.Path
	}
	return f.Path
}

// reviewFileBlock is one file's hunks as a unified-diff-style block.
func reviewFileBlock(f proto.GitReviewFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", reviewFilePath(f), f.Path)
	switch f.Status {
	case "added":
		b.WriteString("new file\n")
	case "deleted":
		b.WriteString("deleted file\n")
	case "renamed", "copied":
		fmt.Fprintf(&b, "rename from %s\nrename to %s\n", f.OldPath, f.Path)
	}
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", reviewFilePath(f), f.Path)
	for _, h := range f.Hunks {
		old, new := 0, 0
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				new++
			case strings.HasPrefix(l, "-"):
				old++
			default:
				old, new = old+1, new+1
			}
		}
		if h.Header != "" {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@ %s\n", h.OldStart, old, h.NewStart, new, h.Header)
		} else {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, old, h.NewStart, new)
		}
		for _, l := range h.Lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// ---- the findings ----

// reviewMarker is the reviewer's machine-readable last line, <review-findings count="N" blocking="M"/>: quotes
// optional, whitespace flexible. lazyre: personad starts with none of it compiled.
var reviewMarker = lazyre.New(`<review-findings\s+count\s*=\s*"?([0-9]+)"?\s+blocking\s*=\s*"?([0-9]+)"?\s*/>`)

// parseReviewFindings takes every marker out of the reviewer's reply; the last one's counts (ok false: none).
func parseReviewFindings(text string) (findings string, count, blocking int, ok bool) {
	found := reviewMarker.FindAllStringSubmatch(text, -1)
	findings = strings.TrimSpace(reviewMarker.ReplaceAllString(text, ""))
	if found == nil {
		return findings, 0, 0, false
	}
	last := found[len(found)-1]
	count, _ = strconv.Atoi(last[1])
	blocking, _ = strconv.Atoi(last[2])
	return findings, max(count, blocking), blocking, true
}

// ---- ending ----

// reviewEnd is how a run ends: the reviewer's reply, or why it failed or was cancelled.
type reviewEnd struct {
	reply             string
	failed, cancelled string
}

// endAutoReview ends run id of todo id as e says, once (see above), and does what follows: the rework goes to the
// agent, or the task waits for review and the supervisor hears it. The reviewer and its copy are taken away after.
func (s *Service) endAutoReview(ctx context.Context, todoID, id string, e reviewEnd) {
	cur, ok := s.todoNow(todoID)
	if !ok {
		return
	}
	var ended AutoReview
	rework, pending := false, false
	v, changed, err := s.updateSupervision(ctx, todoID, func(v *Supervision) bool {
		a := v.AutoReview
		if a == nil || a.ID != id || a.State != autoRunning {
			return false
		}
		now := time.Now().UTC()
		a.EndedAt = &now
		switch {
		case e.cancelled != "":
			a.State, a.Error = autoCancelled, e.cancelled
		case e.failed != "":
			a.State, a.Error = autoFailed, e.failed
		default:
			findings, count, blocking, marked := parseReviewFindings(e.reply)
			a.Findings, a.Count, a.Blocking = clipMiddle(findings, reviewFindingMax), count, blocking
			a.State = autoClean
			if blocking > 0 {
				a.State = autoBlocking
			}
			if !marked {
				a.State, a.Error = autoFailed, whyNoMarker
			}
		}
		if st := v.state(cur); a.State != autoCancelled && (st != "" && st != reviewRework || v.Round != a.Round || cur.DoneAt != nil) {
			a.State, a.Error = autoCancelled, cmp.Or(a.Error, whyDecided) // the supervisor decided meanwhile
		}
		switch {
		case a.State == autoCancelled:
		case a.State == autoBlocking && v.Round < maxReworkRounds:
			v.Round++
			v.State = reviewRework
			v.Reviews = append(v.Reviews, TaskReview{At: now, Verdict: reviewRework, By: reviewerBy, Round: v.Round,
				Note: clip(a.Findings, 2000)})
			if len(v.Reviews) > 20 {
				v.Reviews = v.Reviews[len(v.Reviews)-20:]
			}
			rework = true
		default:
			v.State = reviewPending
			pending = true
		}
		ended = *a
		return true
	})
	if err != nil {
		s.logf("task %s: review %s: end: %v", todoID, id, err)
		return
	}
	if !changed {
		return
	}
	s.logf("task %s: review %s: %s (%d findings, %d blocking) %s", todoID, id, ended.State, ended.Count, ended.Blocking, ended.Error)
	switch {
	case rework:
		text := prompts.RenderSuperviseReviewRework(prompts.SuperviseReviewRework{Round: v.Round, Max: maxReworkRounds,
			Count: ended.Count, Blocking: ended.Blocking, Findings: ended.Findings})
		if t, ok := s.todoNow(todoID); !ok {
			break
		} else if _, err := s.sendToTask(ctx, t, text); err != nil {
			// The agent did not get it: the supervisor hears the task stopped short, with why.
			s.logf("task %s: review rework: %v", todoID, err)
			s.signal(superviseSignal{Todo: todoID, Kind: sigIdle, Key: "review@" + id,
				Detail: whyReworkUnsent + err.Error()})
		}
	case pending:
		s.logf("task %s: waits for review (reviewed: %s)", todoID, ended.State)
		s.signal(superviseSignal{Todo: todoID, Kind: sigReview, Key: reviewKey(&v), Detail: ended.Judged})
	}
	s.cleanAutoReview(ctx, ended)
}

// autoReviewTurnEnded: a session on machine finished a turn; when it is a running review's reviewer, its answer ends
// the review. Called from TurnEnded, on its own.
func (s *Service) autoReviewTurnEnded(ctx context.Context, machine, sid string) {
	todoID, run, ok := s.runOfReviewer(ctx, machine, sid)
	if !ok {
		return
	}
	reply := s.reviewerReply(ctx, machine, sid)
	if strings.TrimSpace(reply) == "" {
		s.endAutoReview(ctx, todoID, run.ID, reviewEnd{failed: whySilent})
		return
	}
	s.endAutoReview(ctx, todoID, run.ID, reviewEnd{reply: reply})
}

// runOfReviewer is the running review whose reviewer is machine's sid (every turn end asks: read in place).
func (s *Service) runOfReviewer(ctx context.Context, machine, sid string) (string, AutoReview, bool) {
	if sid == "" || s.ensureTodosLoaded(ctx) != nil {
		return "", AutoReview{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for id, t := range s.todos.todos {
		if v := t.Supervision; v != nil && v.AutoReview != nil {
			if a := *v.AutoReview; a.State == autoRunning && a.MachineID == machine && a.SessionID == sid {
				return id, a, true
			}
		}
	}
	return "", AutoReview{}, false
}

// reviewerReply is the reviewer's last assistant text, "" when there is none.
func (s *Service) reviewerReply(ctx context.Context, machine, sid string) string {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(qctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 1, "lazy_images": true}, &res); err != nil {
		s.logf("review: the reviewer %s/%s could not be read: %v", machine, short(sid), err)
		return ""
	}
	for i := len(res.Items) - 1; i >= 0; i-- {
		if it := res.Items[i]; it.Kind == proto.KindText && it.Role == "assistant" && strings.TrimSpace(it.Text) != "" {
			return strings.TrimSpace(it.Text)
		}
	}
	return ""
}

// autoReviewWorking: one of supervised task t's agents started a turn: a review running on its last work is moot.
func (s *Service) autoReviewWorking(ctx context.Context, t Todo) {
	if v := t.Supervision; v != nil && v.AutoReview != nil && v.AutoReview.State == autoRunning {
		s.endAutoReview(ctx, t.ID, v.AutoReview.ID, reviewEnd{cancelled: whyAgentWorks})
	}
}

// ---- the tick ----

// autoReviewTick follows the runs (superviseWatch): one past its deadline or that never got a reviewer fails, one whose
// reviewer stopped while nobody heard (a hub restart) ends with its answer, one on a task closed meanwhile is
// cancelled; ended ones get their reviewer and copy taken away.
func (s *Service) autoReviewTick(ctx context.Context, now time.Time) {
	cfg := s.reviewConfig(ctx)
	for _, t := range s.todosOf(ctx, "*") {
		v := t.Supervision
		if v == nil || v.AutoReview == nil {
			continue
		}
		a := *v.AutoReview
		if a.State != autoRunning {
			if a.EndedAt != nil && now.Sub(*a.EndedAt) < autoReviewCleanFor {
				s.cleanAutoReview(ctx, a)
			}
			continue
		}
		switch {
		case t.DoneAt != nil:
			s.endAutoReview(ctx, t.ID, a.ID, reviewEnd{cancelled: whyTodoDone})
		case now.Sub(a.StartedAt) > cfg.Timeout():
			s.endAutoReview(ctx, t.ID, a.ID, reviewEnd{failed: whyTimeout})
		case a.SessionID == "" && now.Sub(a.StartedAt) > autoReviewStartWait:
			s.endAutoReview(ctx, t.ID, a.ID, reviewEnd{failed: whyNoReviewer})
		case a.SessionID != "":
			c, ok := s.session(ctx, a.MachineID, a.SessionID)
			if !ok || c.Starting || c.Status == proto.StatusWorking {
				continue
			}
			if reply := s.reviewerReply(ctx, a.MachineID, a.SessionID); reply != "" {
				s.endAutoReview(ctx, t.ID, a.ID, reviewEnd{reply: reply})
			}
		}
	}
}

// cleanAutoReview closes an ended run's reviewer and removes its copy, once per manager run for what it knows of them
// (both are idempotent on the node: a later try after a restart finds them gone; a reviewer a start in flight gets
// later is a new thing to clean). A copy the node will not remove yet (its reviewer still closing) is tried again at
// the next tick.
func (s *Service) cleanAutoReview(ctx context.Context, a AutoReview) {
	if a.State == autoRunning {
		return
	}
	key := a.ID + "|" + a.SessionID + "|" + a.Worktree
	b := &s.autoRuns
	b.mu.Lock()
	done := b.cleaned[key]
	b.mu.Unlock()
	if done {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if a.SessionID != "" {
		if err := s.call(cctx, a.MachineID, "session.close", map[string]any{"session_id": a.SessionID}, nil); err != nil {
			s.logf("review %s: close %s: %v", a.ID, short(a.SessionID), err)
		}
	}
	if a.Worktree != "" {
		err := s.call(cctx, a.MachineID, "worktree.remove", map[string]any{"path": a.Worktree, "force": true}, nil)
		var re *rpc.Error
		if err != nil && !(errors.As(err, &re) && re.Code == "not_our_worktree") { // not_our_worktree: gone already
			s.logf("review %s: remove its copy: %v", a.ID, err)
			return
		}
	}
	b.mu.Lock()
	if b.cleaned == nil {
		b.cleaned = map[string]bool{}
	}
	b.cleaned[key] = true
	b.mu.Unlock()
}

// ---- hiding the reviewers ----

// reviewerSessions are the reviewers linked to the todos (machine, session): hidden from the session lists, quiet.
func (s *Service) reviewerSessions() map[[2]string]bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	return s.reviewerSessionsLocked()
}

// reviewerSessionsLocked is reviewerSessions with s.todos.mu held.
func (s *Service) reviewerSessionsLocked() map[[2]string]bool {
	var out map[[2]string]bool
	for _, t := range s.todos.todos {
		for _, a := range t.Agents {
			if a.Source == sourceReview {
				if out == nil {
					out = map[[2]string]bool{}
				}
				out[[2]string{a.MachineID, a.SessionID}] = true
			}
		}
	}
	return out
}

// isReviewer: machine's sid is a review's reviewer.
func (s *Service) isReviewer(machine, sid string) bool {
	return s.reviewerSessions()[[2]string{machine, sid}]
}
