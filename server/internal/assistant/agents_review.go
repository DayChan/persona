package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The independent review of an agent session's work (#1808, docs/assistant/agent-work.md "Independent review"): the
// user asks for one from the web; the manager starts a fresh, READ-ONLY reviewer agent on the same machine and
// directory (Claude Code's plan mode — the permission mode enforces it, never the prompt), seeded only with the
// user's own asks from that session and the session's git review scope, never the agent under review's reasoning.
// The reviewer reports prioritized findings; when it finishes, the source session gets one card ("N findings · Send
// to agent / Dismiss"). The reviewer is hidden from the session lists and reached only through its card. Everything
// here is the manager's; the hub relays the two routes under /api/v1/machines/{mid}/sessions/{sid}/review.

const (
	// reviewsSettingKey is where the review book is kept (Setting / PutSetting), read and written only by the manager.
	reviewsSettingKey = "assistant.agent_reviews"
	// reviewPrefix starts the id of every review (and its card).
	reviewPrefix = "review_"
	// reviewDeadline: a running review older than this failed (its reviewer never came back) and clears review_running.
	reviewDeadline = 30 * time.Minute
	// reviewCardTTL is how long a finished review's card waits for its answer.
	reviewCardTTL = 7 * 24 * time.Hour
	// reviewBookMax caps the book; entries no longer pending are dropped oldest first.
	reviewBookMax = 50
	// reviewDiffBudget and reviewFileBudget cap the diff embedded in the reviewer's first message (characters).
	reviewDiffBudget  = 150_000
	reviewFileBudget  = 20_000
	reviewAsksBudget  = 24_000 // and the user's asks: the last this many characters, each message at most reviewAskMax
	reviewAskMax      = 4_000
	reviewAsksMax     = 30
	reviewAskTurns    = 40 // how many turns of the source session the asks are read from
	reviewStartWait   = 3 * time.Minute
	reviewGitTimeout  = 75 * time.Second
	reviewSendTimeout = 5 * time.Minute // a send to a stopped session resumes it first, which can take minutes
)

// The states of a review. running: the reviewer works. pending: it finished, its card waits for the user. failed:
// the reviewer never answered (or ran past the deadline); no card. sent: the user sent the findings to the source
// agent. dismissed: the user closed the card.
const (
	reviewStateRunning   = "running"
	reviewStatePending   = "pending"
	reviewStateFailed    = "failed"
	reviewStateSent      = "sent"
	reviewStateDismissed = "dismissed"
)

// agentReview is one review in the manager's book (reviewsSettingKey).
type agentReview struct {
	ID                string // the review's (and its card's) id, review_<hex>
	MachineID         string // the SOURCE session's machine
	SessionID         string // the source session
	ReviewerID        string
	ReviewerMachineID string
	Findings          string
	FindingsCount     *int
	Scope             string
	BaseShort         string
	StartedAt         time.Time
	EndedAt           time.Time
	State             string
	// SessionTitle and Cwd are the source session's, kept for the card (it shows after a restart too).
	SessionTitle string
	Cwd          string
}

// agentReviewsSetting is the book as it is stored.
type agentReviewsSetting struct {
	Reviews []agentReview `json:"reviews"`
}

// reviewBook is the manager's copy of the reviews, plus what only matters while it runs.
type reviewBook struct {
	mu     sync.Mutex
	loaded bool
	// reviews, newest last; pending ones carry the card out (reviewCards).
	reviews []agentReview
	// taking marks reviews whose reviewer's turn end is being taken: a second turn_ended for the same reviewer is
	// the same finish, not a second one.
	taking map[string]bool
}

// reviewT is one of this file's texts in l.
func reviewT(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-agents_review."+name, args...)
}

// reviewRefusal is one of the review routes' refusals: an HTTP status with its stable code.
type reviewRefusal struct {
	status int
	code   string
	err    error
}

func (e *reviewRefusal) Error() string { return e.err.Error() }
func (e *reviewRefusal) Unwrap() error { return e.err }

func refuseReview(status int, code string, err error) error {
	return &reviewRefusal{status: status, code: code, err: err}
}

// ---- the book ----

// loadReviews reads the book back (once; a store that cannot be read leaves it empty, so a review can still start:
// the next mutation that saves rewrites it). Running reviews past their deadline failed while the manager was away;
// pending ones ride the snapshot again from reviewCards (a restart must not lose a finished review's card).
func (s *Service) loadReviews(ctx context.Context) bool {
	s.reviews.mu.Lock()
	loaded := s.reviews.loaded
	s.reviews.mu.Unlock()
	if loaded {
		return true
	}
	var saved agentReviewsSetting
	if st := s.store(); st != nil {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := st.Setting(lctx, reviewsSettingKey, &saved)
		cancel()
		if err != nil {
			s.logf("review: load: %v (starting empty)", err)
		}
	}
	now := time.Now()
	s.reviews.mu.Lock()
	if s.reviews.loaded { // loaded meanwhile by another caller
		s.reviews.mu.Unlock()
		return true
	}
	s.reviews.reviews = saved.Reviews
	changed := false
	for i := range s.reviews.reviews {
		if e := &s.reviews.reviews[i]; e.State == reviewStateRunning && now.After(e.StartedAt.Add(reviewDeadline)) {
			e.State, e.EndedAt, changed = reviewStateFailed, now, true
		}
	}
	s.reviews.reviews = trimBook(s.reviews.reviews)
	s.reviews.loaded = true
	s.reviews.mu.Unlock()
	if changed {
		s.saveReviews()
	}
	return true
}

// saveReviews persists the book (best effort).
func (s *Service) saveReviews() {
	s.reviews.mu.Lock()
	saved := agentReviewsSetting{Reviews: append([]agentReview(nil), s.reviews.reviews...)}
	s.reviews.mu.Unlock()
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, reviewsSettingKey, saved); err != nil {
		s.logf("review: save: %v", err)
	}
}

// reviewPut stores e in the book (under its id), trims it and persists.
func (s *Service) reviewPut(e agentReview) {
	s.reviews.mu.Lock()
	if i := reviewIndexLocked(s.reviews.reviews, e.ID); i >= 0 {
		s.reviews.reviews[i] = e
	} else {
		s.reviews.reviews = append(s.reviews.reviews, e)
	}
	s.reviews.reviews = trimBook(s.reviews.reviews)
	s.reviews.mu.Unlock()
	s.saveReviews()
}

// reviewIndexLocked is a review's place in the book (-1 when none). The caller holds the lock.
func reviewIndexLocked(book []agentReview, id string) int {
	for i := range book {
		if book[i].ID == id {
			return i
		}
	}
	return -1
}

// trimBook caps the book: entries no longer pending go oldest first, pending ones stay (their cards are the user's
// to answer). The caller holds the lock.
func trimBook(book []agentReview) []agentReview {
	for len(book) > reviewBookMax {
		dropped := false
		for i, e := range book {
			if e.State != reviewStatePending {
				book = append(book[:i], book[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped { // every remaining entry pending: the book stays as it is
			return book
		}
	}
	return book
}

// reviewCards are the finished reviews' cards out (the snapshot's interactions besides ask_user's and the work's),
// oldest first.
func (s *Service) reviewCards() []proto.Interaction {
	s.reviews.mu.Lock()
	out := make([]proto.Interaction, 0, 4)
	for _, e := range s.reviews.reviews {
		if e.State == reviewStatePending {
			out = append(out, reviewCardOf(e, s.lang(context.Background())))
		}
	}
	s.reviews.mu.Unlock()
	sortByCreated(out)
	return out
}

// reviewCardOf is the pending card of a finished review e, in l: its summary says the finding count.
func reviewCardOf(e agentReview, l i18n.Lang) proto.Interaction {
	ix := proto.Interaction{
		ID: e.ID, MachineID: e.MachineID, SessionID: e.SessionID, SessionTitle: e.SessionTitle, Cwd: e.Cwd,
		Kind: proto.IxReview, ToolName: assistantwire.ReviewCardTool,
		CreatedAt: e.EndedAt.UTC(), ExpiresAt: e.EndedAt.Add(reviewCardTTL).UTC(),
		Findings: e.Findings, FindingsCount: e.FindingsCount, ReviewSession: e.ReviewerID,
	}
	if e.FindingsCount != nil {
		ix.Summary = reviewT(l, "card_title", i18n.Args{"n": *e.FindingsCount})
	} else {
		ix.Summary = reviewT(l, "card_title_no_count")
	}
	return ix
}

// sortByCreated orders interactions oldest first.
func sortByCreated(ix []proto.Interaction) {
	slices.SortStableFunc(ix, func(a, b proto.Interaction) int { return a.CreatedAt.Compare(b.CreatedAt) })
}

// reviewers are the reviewer session ids in the book (running, pending or finished): hidden from the apps' session
// lists — a reviewer is reached only through its card (publishList).
func (s *Service) reviewers() map[string]bool {
	s.reviews.mu.Lock()
	defer s.reviews.mu.Unlock()
	out := make(map[string]bool, len(s.reviews.reviews))
	for _, e := range s.reviews.reviews {
		out[e.ReviewerID] = true
	}
	return out
}

// ---- the routes ----

// agentReviewRoutes are the two routes of agentRoutes() (agents_routes.go): starting a review, and its light state
// for the session menu. Both are relayed by the hub under /api/v1/machines/{mid}/ (assistantRoutesRelayed).
func (s *Service) agentReviewRoutes() {
	s.handle("POST /api/v1/machines/{mid}/sessions/{sid}/review", s.startAgentReview)
	s.handle("GET /api/v1/machines/{mid}/sessions/{sid}/review", s.agentReviewState)
}

// startAgentReview is POST …/review: start an independent read-only reviewer of machine {mid}'s session {sid}.
func (s *Service) startAgentReview(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot start a review"))
		return
	}
	m, ok := s.fleetMachine(r.PathValue("mid"))
	if !ok {
		apierr.Write(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
		return
	}
	ctx := r.Context()
	if !s.loadReviews(ctx) {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("the reviews could not be read yet"))
		return
	}
	s.reviewCheckDeadlines(ctx)
	c, ok := s.session(ctx, m.ID, r.PathValue("sid"))
	if !ok {
		apierr.WriteCode(w, http.StatusNotFound, "no_session", i18n.Err("assistant-agents_review.no_session"))
		return
	}
	if c.Starting || c.Status == proto.StatusWorking || c.Status == proto.StatusBlocked {
		apierr.WriteCode(w, http.StatusConflict, "session_busy", i18n.Err("assistant-agents_review.session_busy"))
		return
	}
	// Read-only is enforced by the permission mode (the node's --permission-mode plan), so it rests on the launcher:
	// an agent whose launcher cannot start one (Codex, the runtimes) has no independent review.
	if c.Agent != "" && c.Agent != "claude" {
		apierr.WriteCode(w, http.StatusConflict, "read_only_unavailable",
			i18n.Err("assistant-agents_review.read_only_unavailable", i18n.Args{"agent": agentLabel(c)}))
		return
	}
	if s.reviewRunningOn(m.ID, c.SessionID) {
		apierr.WriteCode(w, http.StatusConflict, "review_running", i18n.Err("assistant-agents_review.review_running"))
		return
	}
	rev, scope, baseShort, err := s.reviewGitScope(ctx, m.ID, reviewCwd(c))
	if err != nil {
		var refusal *reviewRefusal
		if errors.As(err, &refusal) {
			apierr.WriteCode(w, refusal.status, refusal.code, refusal.err)
			return
		}
		apierr.Call(w, err) // the node could not be reached, or answered something it should not
		return
	}
	if len(rev.Files) == 0 && !rev.Truncated {
		apierr.WriteCode(w, http.StatusConflict, "no_change", i18n.Err("assistant-agents_review.no_change"))
		return
	}
	diff, omitted := reviewDiff(*rev)
	asks := s.reviewAsks(ctx, m.ID, c.SessionID) // a read failure is not fatal: the diff alone carries the review
	prompt := prompts.RenderAgentReview(prompts.AgentReview{
		Language: answerLanguage(s.hello(ctx).Locale), Cwd: reviewCwd(c), Scope: scope, Asks: asks, Diff: diff, Omitted: omitted})
	params := map[string]any{
		"cwd": reviewCwd(c), "launcher": c.Launcher, "permission_mode": "plan", "prompt": prompt,
		"tab_name": "review-" + short(c.SessionID), "async": true,
	}
	sctx, cancel := context.WithTimeout(ctx, reviewStartWait)
	defer cancel()
	var res struct {
		Session proto.Session `json:"session"`
	}
	if err := s.call(sctx, m.ID, DeviceSessionStart, params, &res); err != nil {
		s.logf("review %s/%s: start: %v", m.ID, short(c.SessionID), err)
		apierr.WriteCode(w, http.StatusConflict, "not_supported",
			i18n.Err("assistant-agents_review.not_supported", i18n.Args{"error": err.Error()}))
		return
	}
	now := time.Now().UTC()
	e := agentReview{ID: reviewPrefix + randomHex(6), MachineID: m.ID, SessionID: c.SessionID,
		ReviewerID: res.Session.SessionID, ReviewerMachineID: m.ID, Scope: scope, BaseShort: baseShort,
		StartedAt: now, State: reviewStateRunning, SessionTitle: agentLabel(c), Cwd: c.Cwd}
	s.reviewReplacePending(e) // a new review replaces the pending card an earlier one left
	s.publishSoon()
	apierr.WriteJSON(w, map[string]any{"running": true, "reviewer_session_id": e.ReviewerID, "started_at": now.Format(time.RFC3339)})
}

// agentReviewState is GET …/review: the light state the session menu shows.
func (s *Service) agentReviewState(w http.ResponseWriter, r *http.Request) {
	m, ok := s.fleetMachine(r.PathValue("mid"))
	if !ok {
		apierr.Write(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
		return
	}
	ctx := r.Context()
	if !s.loadReviews(ctx) {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("the reviews could not be read yet"))
		return
	}
	s.reviewCheckDeadlines(ctx)
	out := map[string]any{"running": false, "started_at": "", "pending": nil}
	sid := r.PathValue("sid")
	s.reviews.mu.Lock()
	for _, e := range s.reviews.reviews {
		if e.MachineID != m.ID || e.SessionID != sid {
			continue
		}
		if e.State == reviewStateRunning {
			out["running"], out["started_at"] = true, e.StartedAt.Format(time.RFC3339)
			continue
		}
		if e.State == reviewStatePending {
			if out["started_at"] == "" {
				out["started_at"] = e.StartedAt.Format(time.RFC3339)
			}
			out["pending"] = reviewCardOf(e, s.lang(ctx))
		}
	}
	s.reviews.mu.Unlock()
	apierr.WriteJSON(w, out)
}

// reviewRunningOn: a review of machine/session is running.
func (s *Service) reviewRunningOn(machine, sid string) bool {
	s.reviews.mu.Lock()
	defer s.reviews.mu.Unlock()
	for _, e := range s.reviews.reviews {
		if e.MachineID == machine && e.SessionID == sid && e.State == reviewStateRunning {
			return true
		}
	}
	return false
}

// reviewReplacePending stores e (under its id) and takes the pending cards an earlier review of the same source
// session still has out of the book: the new one replaces them (the hub withdraws a card that left the snapshot).
func (s *Service) reviewReplacePending(e agentReview) {
	var replaced []string
	s.reviews.mu.Lock()
	kept := s.reviews.reviews[:0]
	for _, old := range s.reviews.reviews {
		if old.ID != e.ID && old.State == reviewStatePending && old.MachineID == e.MachineID && old.SessionID == e.SessionID {
			replaced = append(replaced, old.ID)
			continue
		}
		kept = append(kept, old)
	}
	s.reviews.reviews = kept
	if i := reviewIndexLocked(s.reviews.reviews, e.ID); i >= 0 {
		s.reviews.reviews[i] = e
	} else {
		s.reviews.reviews = append(s.reviews.reviews, e)
	}
	s.reviews.reviews = trimBook(s.reviews.reviews)
	s.reviews.mu.Unlock()
	s.saveReviews()
	for _, id := range replaced {
		s.recordEnd(id, "cancelled", nil) // the card never goes up again: nothing else ends it in the history
	}
	if len(replaced) > 0 {
		s.publishSoon()
	}
}

// reviewCwd is where a session's repository is: the node-verified workspace directory when it has one.
func reviewCwd(c proto.Session) string {
	if c.WorkspaceCwd != "" {
		return c.WorkspaceCwd
	}
	return c.Cwd
}

// ---- the seed ----

// reviewGitScope is the change to review: the branch scope, falling back to uncommitted when the branch has no base
// to compare with. Refused with code no_change when the directory is not a Git repository; scope is the one line
// that names what the diff is compared with, baseShort the base's short hash ("" when none).
func (s *Service) reviewGitScope(ctx context.Context, machine, cwd string) (rev *proto.GitReview, scope, baseShort string, err error) {
	qctx, cancel := context.WithTimeout(ctx, reviewGitTimeout)
	defer cancel()
	var out proto.GitReview
	call := func(scope string) error {
		out = proto.GitReview{}
		return s.call(qctx, machine, "git.review", proto.GitReviewRequest{Cwd: cwd, Scope: scope}, &out)
	}
	if err := call(proto.ReviewBranch); err != nil {
		var re *rpc.Error
		if !errors.As(err, &re) {
			return nil, "", "", err
		}
		switch re.Code {
		case proto.CodeNotGit:
			return nil, "", "", refuseReview(http.StatusConflict, "no_change", i18n.Err("assistant-agents_review.no_change_not_git"))
		case proto.CodeNoDefaultBranch, proto.CodeNoMergeBase:
			if err := call(proto.ReviewUncommitted); err != nil {
				var fallback *rpc.Error
				if errors.As(err, &fallback) && fallback.Code == proto.CodeNotGit {
					return nil, "", "", refuseReview(http.StatusConflict, "no_change", i18n.Err("assistant-agents_review.no_change_not_git"))
				}
				return nil, "", "", err
			}
		default:
			return nil, "", "", err
		}
	}
	return &out, reviewScopeLine(out), out.Base.Short, nil
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
// for a page. The string is the reason ("" when its hunks may be shown), in the reviewer's prompt's language.
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

// reviewAsks are the user's own messages to the source session, oldest first: the last reviewAsksMax, each clipped
// to reviewAskMax and together to reviewAsksBudget (the oldest go first, the most recent stay whole). A read
// failure is not fatal — the review runs with the diff alone.
func (s *Service) reviewAsks(ctx context.Context, machine, sid string) []string {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(qctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": reviewAskTurns, "lazy_images": true}, &res); err != nil {
		s.logf("review %s/%s: the asks could not be read: %v", machine, short(sid), err)
		return nil
	}
	var asks []string
	for _, it := range res.Items {
		if it.Kind == proto.KindText && it.Role == "user" && strings.TrimSpace(it.Text) != "" {
			asks = append(asks, clip(strings.TrimSpace(it.Text), reviewAskMax))
		}
	}
	if len(asks) > reviewAsksMax {
		asks = asks[len(asks)-reviewAsksMax:]
	}
	total := 0
	for i := len(asks) - 1; i >= 0; i-- { // the most recent whole, the oldest dropped when the budget runs out
		total += utf8.RuneCountInString(asks[i])
		if total > reviewAsksBudget {
			return asks[i+1:]
		}
	}
	return asks
}

// ---- the findings marker ----

// reviewMarker reads the reviewer's machine-readable last line, <review-findings count="N"/>: the quotes around the
// number are optional and the whitespace is flexible; a malformed one is not a marker. lazyre: personad starts with
// none of its reviews' work compiled (lazyre's own guard).
var reviewMarker = lazyre.New(`<review-findings\s+count\s*=\s*"?([0-9]+)"?\s*/>`)

// parseReviewFindings strips every marker from the reviewer's reply and returns its finding count: the LAST marker
// wins, nil when there is none (the card then says the review finished, without a count).
func parseReviewFindings(text string) (string, *int) {
	found := reviewMarker.FindAllStringSubmatchIndex(text, -1)
	if found == nil {
		return text, nil
	}
	var count *int
	if v, err := strconv.Atoi(text[found[len(found)-1][2]:found[len(found)-1][3]]); err == nil {
		count = &v
	}
	return strings.TrimSpace(reviewMarker.ReplaceAllString(text, "")), count
}

// ---- watching the reviewer ----

// reviewTurnEnded: a session on machine finished a turn; when it is a running review's reviewer, the review is done.
// Called from TurnEnded, on its own (bus.go).
func (s *Service) reviewTurnEnded(ctx context.Context, machine, sid string) {
	if !s.loadReviews(ctx) {
		return
	}
	s.reviews.mu.Lock()
	var mine *agentReview
	for i := range s.reviews.reviews {
		if e := &s.reviews.reviews[i]; e.State == reviewStateRunning && e.ReviewerMachineID == machine && e.ReviewerID == sid {
			mine = e
			break
		}
	}
	if mine == nil {
		s.reviews.mu.Unlock()
		return
	}
	if s.reviews.taking == nil {
		s.reviews.taking = map[string]bool{}
	}
	if s.reviews.taking[mine.ID] { // its second turn end: the same finish
		s.reviews.mu.Unlock()
		return
	}
	s.reviews.taking[mine.ID] = true
	e := *mine
	s.reviews.mu.Unlock()
	defer func() {
		s.reviews.mu.Lock()
		delete(s.reviews.taking, e.ID)
		s.reviews.mu.Unlock()
	}()
	s.finishReview(ctx, e)
}

// finishReview reads the reviewer's last reply and puts its findings on a card for the source session. An empty
// reply fails the review (no card): the reviewer never said anything.
func (s *Service) finishReview(ctx context.Context, e agentReview) {
	reply := s.reviewReply(ctx, e.ReviewerMachineID, e.ReviewerID)
	if strings.TrimSpace(reply) == "" {
		s.logf("review %s/%s: the reviewer said nothing: failed", e.MachineID, short(e.SessionID))
		e.State, e.EndedAt = reviewStateFailed, time.Now().UTC()
		s.reviewPut(e)
		return
	}
	findings, count := parseReviewFindings(reply)
	e.Findings, e.FindingsCount, e.State, e.EndedAt = findings, count, reviewStatePending, time.Now().UTC()
	if c, ok := s.session(ctx, e.MachineID, e.SessionID); ok {
		e.SessionTitle, e.Cwd = agentLabel(c), c.Cwd
	}
	s.reviewReplacePending(e)
	s.publishSoon()
	s.logf("review %s/%s: finished (%s)", e.MachineID, short(e.SessionID), reviewCountWord(e.FindingsCount))
}

// reviewCountWord is a review's count for the log.
func reviewCountWord(count *int) string {
	if count == nil {
		return "no count"
	}
	return fmt.Sprintf("%d findings", *count)
}

// reviewReply is the reviewer's last assistant text, "" when there is none.
func (s *Service) reviewReply(ctx context.Context, machine, sid string) string {
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

// reviewCheckDeadlines fails running reviews past their deadline (a reviewer that never came back), so
// review_running clears; the stuck reviewer is interrupted, best effort.
func (s *Service) reviewCheckDeadlines(ctx context.Context) {
	now := time.Now()
	var stuck []agentReview
	s.reviews.mu.Lock()
	for i := range s.reviews.reviews {
		if e := &s.reviews.reviews[i]; e.State == reviewStateRunning && now.After(e.StartedAt.Add(reviewDeadline)) {
			e.State, e.EndedAt = reviewStateFailed, now.UTC()
			stuck = append(stuck, *e)
		}
	}
	s.reviews.mu.Unlock()
	if len(stuck) == 0 {
		return
	}
	s.saveReviews()
	s.publishSoon()
	for _, e := range stuck {
		s.logf("review %s/%s: past its deadline: failed", e.MachineID, short(e.SessionID))
		ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		if err := s.call(ictx, e.ReviewerMachineID, "session.interrupt", map[string]any{"session_id": e.ReviewerID}, nil); err != nil {
			s.logf("review %s/%s: interrupt: %v", e.MachineID, short(e.ReviewerID), err)
		}
		cancel()
	}
}

// reviewTick is the deadline sweep of the work loop (agents_work_tick.go): reviews are the user's explicit asks,
// so they run whatever the triage switches say.
func (s *Service) reviewTick(now time.Time) {
	if !s.loadReviews(context.Background()) {
		return
	}
	s.reviewCheckDeadlines(context.Background())
}

// ---- the card's answers ----

// reviewAnswer is manager.action for one of the review cards (false: id is none of them). {action:"send"} sends
// the findings to the source agent and closes the card; {dismiss:true} (or a cancel) closes it; anything else is a
// bad request. A send that fails returns its error and the card stays, so the user can answer it again.
func (s *Service) reviewAnswer(ctx context.Context, id string, r proto.Resolution) (bool, error) {
	if !strings.HasPrefix(id, reviewPrefix) {
		return false, nil
	}
	if r.Cancel && !r.IsQuestionCancel() {
		return true, errInvalidQuestionCancel // the card is not cancelled with an answer in it
	}
	if !s.loadReviews(ctx) {
		return true, errors.New("the reviews could not be read yet")
	}
	s.reviews.mu.Lock()
	var mine *agentReview
	for i := range s.reviews.reviews {
		if e := &s.reviews.reviews[i]; e.ID == id && e.State == reviewStatePending {
			mine = e
			break
		}
	}
	if mine == nil {
		s.reviews.mu.Unlock()
		return true, errGone(id)
	}
	e := *mine
	if s.reviews.taking == nil {
		s.reviews.taking = map[string]bool{}
	}
	if s.reviews.taking[e.ID] { // this callback's answer is already on its way to the agent
		s.reviews.mu.Unlock()
		return true, nil
	}
	s.reviews.taking[e.ID] = true
	s.reviews.mu.Unlock()
	defer func() {
		s.reviews.mu.Lock()
		delete(s.reviews.taking, e.ID)
		s.reviews.mu.Unlock()
	}()
	switch {
	case r.Cancel || r.Dismiss:
		e.State = reviewStateDismissed
		s.reviewPut(e)
		s.publishSoon()
		return true, nil // the card's end in the history is the queue's (the answer went through it)
	case r.Action != "send":
		return true, badRequest(fmt.Errorf("a review card takes action \"send\" or dismiss, not %q", r.Action))
	}
	text := prompts.RenderAgentReviewFindings(prompts.AgentReviewFindings{Findings: e.Findings, Count: e.FindingsCount})
	// The source agent may be stopped: its node resumes it first, which can take minutes.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reviewSendTimeout)
	defer cancel()
	if err := s.call(sctx, e.MachineID, DeviceSessionSend, sendParams(e.SessionID, text, false), nil); err != nil {
		s.logf("review %s/%s: the findings did not go: %v", e.MachineID, short(e.SessionID), err)
		return true, err // the card stays for the user to answer again
	}
	e.State = reviewStateSent
	s.reviewPut(e)
	s.publishSoon()
	return true, nil
}
