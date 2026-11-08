package kb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Model is the small model that compresses and tags (the hub's ai.Client).
type Model interface {
	JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error
}

// When a chat is due for compression: enough new messages, or some new ones and a quiet while.
const (
	compressBatch = 50
	compressQuiet = 10 * time.Minute
	compressInput = 24000 // characters of new messages per call
)

// After a failed compression the chat waits compressRetry, doubled with every further failure in a row up to
// compressRetryMax, so one chat the model cannot handle does not cost a call every round (2026-11-07: a chat the model
// refused was sent again every two minutes for three days).
const (
	compressRetry    = 10 * time.Minute
	compressRetryMax = 6 * time.Hour
)

var importanceRe = regexp.MustCompile(`^P[0-3]$`)

type chatDigest struct {
	Summary    string   `json:"summary"`
	OneLine    string   `json:"one_line"`
	Importance string   `json:"importance"`
	Relevance  int      `json:"relevance"`
	Tags       []string `json:"tags"`
	Projects   []struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Aliases []string `json:"aliases"`
		Update  string   `json:"update"`
		MyRole  string   `json:"my_role"`
	} `json:"projects"`
	People []struct {
		Name string `json:"name"`
		Note string `json:"note"`
	} `json:"people"`
}

// DueForCompression lists chats with news to fold into their summary.
func (e *Engine) DueForCompression(now time.Time) []*Chat {
	var out []*Chat
	for _, c := range e.Store.Chats() {
		if c.Pending == 0 || now.Before(c.CompressRetryAt) {
			continue
		}
		if c.Pending >= compressBatch || now.Sub(c.LastAt) >= compressQuiet || c.CompressedAt.IsZero() && now.Sub(c.Created) >= compressQuiet {
			out = append(out, c)
		}
	}
	// The chats that matter most first; then the busiest.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Relevance != out[j].Relevance {
			return out[i].Relevance > out[j].Relevance
		}
		return out[i].Pending > out[j].Pending
	})
	return out
}

// compressFailed records that compressing c (the copy the call started from) failed with err, and returns the error to
// log. The model declines the same messages again, so a refused batch is settled without a summary: the chat goes on
// from the messages after it. Any other failure puts the chat off (compressRetry). A paused connection or an ended run
// changes nothing: the chat stays due.
func (e *Engine) compressFailed(ctx context.Context, c *Chat, err error) error {
	if ai.IsPaused(err) || ctx.Err() != nil {
		return err
	}
	e.Store.chatMu.Lock() // read and written whole: what archiving or a history import wrote meanwhile stays
	defer e.Store.chatMu.Unlock()
	current, ok := e.Store.Chat(c.ID) // archiving may have added messages while the model was called
	if !ok {
		return err
	}
	if errors.Is(err, ai.ErrRefused) {
		current.CoveredUntil = c.LastAt.Add(time.Minute)
		if !current.LastAt.After(c.LastAt) {
			current.Pending = 0
		}
		current.CompressFailures, current.CompressRetryAt = 0, time.Time{}
		if perr := e.Store.PutChat(current); perr != nil {
			return perr
		}
		return fmt.Errorf("%w: skipped its messages up to %s", err, c.LastAt.UTC().Format(time.RFC3339))
	}
	current.CompressFailures++
	wait := compressRetryMax
	if n := current.CompressFailures - 1; n < 16 {
		wait = min(compressRetry<<n, compressRetryMax)
	}
	current.CompressRetryAt = time.Now().Add(wait)
	if perr := e.Store.PutChat(current); perr != nil {
		return perr
	}
	return fmt.Errorf("%w (failed %d time(s) in a row, next try in %s)", err, current.CompressFailures, wait)
}

// putCompressed writes what compression decided for c: the chat is read again under chatMu and only set changes it,
// so the messages, people and history mode that archiving or a history import (WriteHistory) wrote while the model
// was called stay. c gets the chat as written. A chat forgotten meanwhile is not written back (false).
func (e *Engine) putCompressed(c *Chat, set func(cur *Chat)) (bool, error) {
	e.Store.chatMu.Lock()
	defer e.Store.chatMu.Unlock()
	cur, ok := e.Store.Chat(c.ID)
	if !ok {
		return false, nil
	}
	set(cur)
	if err := e.Store.PutChat(cur); err != nil {
		return false, err
	}
	*c = *cur
	return true, nil
}

// Compress folds a chat's new messages into its summary and tags, then evolves the projects and people it mentions.
func (e *Engine) Compress(ctx context.Context, c *Chat) error {
	current, ok := e.Store.Chat(c.ID)
	if !ok {
		return nil
	}
	c = current
	if e.Model == nil {
		return nil
	}
	from := c.CoveredUntil
	if from.IsZero() {
		from = c.Created.AddDate(0, 0, -30)
		// A Moments post is archived when it first concerns me, often long after it was written: the 30 days before
		// that held none of it, so 20 of 21 posts were settled without a model call (2026-11-01). The whole post,
		// back to a year before it was archived.
		if c.Kind == "moment" {
			from = c.Created.AddDate(-1, 0, 0)
		}
	}
	news := e.Store.Since(c.ID, from, compressInput)
	if strings.TrimSpace(news) == "" {
		_, err := e.putCompressed(c, func(cur *Chat) { cur.Pending = 0 })
		return err
	}
	in := prompts.RenderKBCompressInput(prompts.KBCompressInput{Name: c.Name, Kind: c.Kind, Automated: c.Kind == "mail" && !HasPeople(c),
		Pinned: c.Pinned, People: e.pinnedPeople(c.People), Projects: e.knownProjects(), Tags: e.TopTags(40), Summary: e.Store.Summary(KindChat, c.ID), News: news})

	var d chatDigest
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.modelFor("kb.compress").JSONMax(cctx, prompts.RenderKBCompress(prompts.KBOwner{Owner: e.Owner}), in, 4000, &d); err != nil {
		return e.compressFailed(ctx, c, err)
	}
	if _, ok := e.Store.Chat(c.ID); !ok { // forgotten while the model wrote (ForgetSource): not written back
		return nil
	}
	started := *c // the chat as the model read it; c gets what compression decides, and only that is written back
	now := time.Now().UTC()
	if strings.TrimSpace(d.Summary) != "" {
		if err := e.Store.PutSummary(KindChat, c.ID, "# "+c.Name+"\n\n"+d.Summary); err != nil {
			return err
		}
	}
	c.OneLine = oneLine(d.OneLine)
	applyTags(&c.Tagging, d.Importance, d.Relevance, d.Tags, now)
	if HasPeople(c) {
		for _, p := range d.Projects {
			slug := e.evolveProject(p.Name, p.Kind, p.Aliases, p.MyRole, c, p.Update, now)
			c.Projects = addUnique(c.Projects, slug)
		}
	}
	for _, p := range d.People {
		e.notePersonNote(c, p.Name, p.Note)
	}
	written, err := e.putCompressed(c, func(cur *Chat) {
		cur.Tagging, cur.OneLine, cur.TagRev = c.Tagging, c.OneLine, tagRev
		for _, slug := range c.Projects {
			cur.Projects = addUnique(cur.Projects, slug)
		}
		// Messages archived while the model wrote are after what it covered: they stay pending.
		if !cur.LastAt.After(started.LastAt) {
			cur.Pending = 0
		}
		cur.CompressedAt = now
		cur.CoveredUntil = started.LastAt.Add(time.Minute)
		cur.CompressFailures, cur.CompressRetryAt = 0, time.Time{}
	})
	if err != nil || !written {
		return err
	}
	e.index(KindChat, c.ID, c.Name, c.Tagging, c.OneLine, c.LastAt)
	// A DM is mostly about the other person: their profile follows it.
	if c.Kind == "dm" || c.Kind == "mail" {
		for _, pid := range c.People {
			if p, ok := e.Store.Person(pid); ok && !p.Locked {
				p.Importance, p.Relevance = c.Importance, c.Relevance
				p.Tags = c.Tags
				_ = e.Store.PutPerson(p)
			}
		}
	}
	for _, pid := range c.People {
		e.WritePersonSummary(pid)
	}
	return nil
}

// automatedSender matches mail addresses of notifications and newsletters (only the inbox is read, so a reply of
// the owner's rarely shows up: the sender is what tells).
var automatedSender = regexp.MustCompile(`(?i)(^|[-_.@])(no[-_]?reply|do[-_]?not[-_]?reply|notifications?|notify|news(letter)?|marketing|mailer|mailer-daemon|postmaster|bounces?|info|support|service|billing|account|alerts?|updates?|hello|team|receipts?|orders?|invoices?|payments?|contact|sales|promo(tions)?|offers?|rewards?|digest|submissions?|feedback|welcome)([-_.@]|$)`)

// automatedLocal: a local part ending in one of those words ("applesupport", "teamnotify").
var automatedLocal = regexp.MustCompile(`(?i)(support|service|notify|notifications?|alerts?|news|updates?|receipts?|billing)$`)

// bulkDomain: the subdomains bulk mail goes out from (email.apple.com, em-s.dropbox.com, txn.dropbox.com, mg.…).
var bulkDomain = regexp.MustCompile(`(?i)^(e|em|em-[a-z]+|email|emails|emessaging|message|messages|mail|mailer|mailing|news|newsletter|txn|transactional|notify|notifications|mg|send|sendgrid|bounce|marketing)[.]`)

// newsletterHost: platforms every sender on is a newsletter.
var newsletterHost = regexp.MustCompile(`(?i)(^|[.])(substack\.com|beehiiv\.com|mailchimp(app)?\.com|convertkit\.com|buttondown\.email|ghost\.io|medium\.com)$`)

// automatedAddress reports whether a mail address looks like a system or bulk sender rather than a person.
func automatedAddress(addr string) bool {
	if automatedSender.MatchString(addr) {
		return true
	}
	local, domain, ok := strings.Cut(addr, "@")
	return ok && (automatedLocal.MatchString(local) || bulkDomain.MatchString(domain) || newsletterHost.MatchString(domain))
}

// HasPeople reports whether real people talk in a chat: chats with bots and automated mail (notifications,
// newsletters) do not make projects.
func HasPeople(c *Chat) bool {
	switch c.Kind {
	case "bot":
		return false
	case "mail":
		return c.Mine || !automatedAddress(c.SourceID[strings.Index(c.SourceID, "-")+1:])
	}
	return true
}

// IgnoreNoiseProjects marks projects that only come from chats without people as ignored (the AI once made a
// project of every app named in store emails). Hand-tagged ones stay. Returns how many changed.
func (e *Engine) IgnoreNoiseProjects() int {
	n := 0
	for _, p := range e.Store.Projects() {
		if p.Locked || p.Status == "ignored" || !p.Live() || len(p.Chats) == 0 {
			continue
		}
		people := false
		for _, cid := range p.Chats {
			if c, ok := e.Store.Chat(cid); !ok || HasPeople(c) {
				people = true
				break
			}
		}
		if !people {
			p.Status = "ignored"
			p.Dirty = false
			if e.Store.PutProject(p) == nil {
				e.index(KindProject, p.Slug, p.Name, p.Tagging, p.OneLine, p.Updated)
				n++
			}
		}
	}
	return n
}

// tagRev is the version of the tagging rules; chats tagged under an older one can be tagged again (RetagMail).
// 1: notifications (receipts, renewals, promotions, invitations) are P3 and at most 20 unless they need me to act.
const tagRev = 1

// RetagMail queues mail the owner never replied to, tagged under older rules, for one more compression, so the rules
// reach what is already there (receipts and promotions had come out at relevance 60–75, some from addresses that
// passed for people). Hand-tagged ones stay. Returns how many were queued.
func (e *Engine) RetagMail() int {
	n := 0
	for _, c := range e.Store.Chats() {
		if c.Kind != "mail" || c.Mine || c.Locked || c.TagRev >= tagRev || c.CompressedAt.IsZero() {
			continue
		}
		written, err := e.putCompressed(c, func(cur *Chat) {
			cur.CoveredUntil = time.Time{} // read its messages again; the old summary goes in as context
			cur.Pending = max(cur.Pending, 1)
		})
		if written && err == nil {
			n++
		}
	}
	return n
}

func applyTags(t *Tagging, importance string, relevance int, tags []string, now time.Time) {
	if !t.Locked {
		if importanceRe.MatchString(importance) {
			t.Importance = importance
		}
		if relevance >= 0 && relevance <= 100 {
			t.Relevance = relevance
		}
	}
	var clean []string
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.TrimPrefix(tag, "#"))
		if tag != "" && len([]rune(tag)) <= 16 {
			clean = addUnique(clean, tag)
		}
	}
	if len(clean) > 0 {
		t.Tags = clean
	}
	t.TaggedAt = now
}

// --- projects ---

var slugRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// Slug is the directory name of a project name ("Persona 2.0 Launch" → "persona-2-0-launch"; letters of any script are kept).
func Slug(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func normName(s string) string { return strings.ReplaceAll(Slug(s), "-", "") }

// FindProject matches a name against project names and aliases; a merged project's name leads to the one it went into.
// When several live projects match, a name (or slug) beats an alias, and among equals the most recently updated wins
// (then the smaller slug), so the answer does not depend on directory order.
//
// A name in the form known_projects.md shows a project in (「名字（别名、…）：一句话」, which models copy whole) that
// matches nothing is looked up again without its one line and trailing bracket groups (#5824).
func (e *Engine) FindProject(name string) *Project {
	if p := e.findProject(name); p != nil {
		return p
	}
	for _, base := range displayBases(name) {
		if tooGenericAlias(normName(base)) { // 「无（某某群）」 is not the project named 无
			break
		}
		if p := e.findProject(base); p != nil {
			return p
		}
	}
	return nil
}

// trailingGroupRe is one bracket group at the end of a name: 「（…）」 or 「(...)」.
var trailingGroupRe = regexp.MustCompile(`\s*[（(][^（()）]*[)）]\s*$`)

// displayBases are the shorter names a project name in the shown form may stand for, longest first: without the
// one line after 「：」, then without one trailing bracket group at a time. 2026-11-06 a knowledge base got projects
// named 「X（a、b）」 and 「X（a、b）（c、d…）」; dedupe then merged the real X into them.
func displayBases(name string) []string {
	var out []string
	s := strings.TrimSpace(name)
	if i := strings.Index(s, "："); i > 0 {
		s = strings.TrimSpace(s[:i])
		out = append(out, s)
	}
	for {
		t := strings.TrimSpace(trailingGroupRe.ReplaceAllString(s, ""))
		if t == s || t == "" {
			return out
		}
		s = t
		out = append(out, s)
	}
}

// shownName is the name a new project gets from a model's: without the one line and without trailing bracket groups
// that list aliases (「、」 inside, or more than one group). A single group without 「、」 can be part of a real name
// (「分身（内置版）」) and stays.
func shownName(name string) string {
	s := strings.TrimSpace(name)
	if i := strings.Index(s, "："); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	for {
		loc := trailingGroupRe.FindStringIndex(s)
		if loc == nil {
			return s
		}
		group, rest := s[loc[0]:], strings.TrimSpace(s[:loc[0]])
		if rest == "" || tooGenericAlias(normName(rest)) || (!strings.Contains(group, "、") && !trailingGroupRe.MatchString(rest)) {
			return s
		}
		s = rest
	}
}

// isShownFormOf says whether name is base in the shown form: base followed by bracket groups or a one line.
func isShownFormOf(name, base string) bool {
	n := normName(base)
	for _, b := range displayBases(name) {
		if normName(b) == n {
			return true
		}
	}
	return false
}

func (e *Engine) findProject(name string) *Project {
	n := normName(name)
	if n == "" {
		return nil
	}
	var byName, byAlias, merged *Project
	for _, p := range e.Store.Projects() {
		exact := normName(p.Name) == n || p.Slug == Slug(name)
		if !exact && !hasAlias(p, n) {
			continue
		}
		switch {
		case !p.Live():
			merged = preferProject(merged, p)
		case exact:
			byName = preferProject(byName, p)
		default:
			byAlias = preferProject(byAlias, p)
		}
	}
	if byName != nil {
		return byName
	}
	if byAlias != nil {
		return byAlias
	}
	if merged != nil && merged.MergedInto != "" {
		if p, ok := e.Store.Project(merged.MergedInto); ok && p.Live() {
			return p
		}
	}
	return nil
}

func hasAlias(p *Project, n string) bool {
	for _, a := range p.Aliases {
		if normName(a) == n {
			return true
		}
	}
	return false
}

// preferProject is the one of two matches to keep: the more recently updated, then the smaller slug.
func preferProject(cur, p *Project) *Project {
	if cur == nil || p.Updated.After(cur.Updated) || (p.Updated.Equal(cur.Updated) && p.Slug < cur.Slug) {
		return p
	}
	return cur
}

// maxProjectAliases bounds the aliases a project collects on its own (compression, dedupe): past it new ones are
// dropped. Aliases added by hand (kb_update) are not bounded. Before the bound a week of compression left active
// projects with 90-145 aliases each, among them the names of other active projects (#2546).
const maxProjectAliases = 12

// takenProjectNames are the normalized names, slugs and aliases of the live projects other than skip: an alias that
// is one of them belongs to another project and would make mentions of it land here.
func (e *Engine) takenProjectNames(skip ...string) map[string]bool {
	taken := map[string]bool{}
	for _, p := range e.Store.Projects() {
		if !p.Live() || slices.Contains(skip, p.Slug) {
			continue
		}
		for _, n := range append([]string{p.Name, p.Slug}, p.Aliases...) {
			if n = normName(n); n != "" {
				taken[n] = true
			}
		}
	}
	return taken
}

// tooGenericAlias: too short to tell projects apart (a one-character name, a two-letter abbreviation) or a word
// every project has.
func tooGenericAlias(n string) bool {
	r := []rune(n)
	if len(r) < 2 || genericWord[n] {
		return true
	}
	return len(r) <= 2 && len(n) == len(r) // ASCII only: "ss", "ai"
}

// addProjectAliases adds the aliases worth keeping to p: not p's own name, not too generic, not taken by another
// live project, and while p has fewer than maxProjectAliases.
func addProjectAliases(p *Project, aliases []string, taken map[string]bool) {
	own := normName(p.Name)
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		n := normName(a)
		if n == "" || n == own || tooGenericAlias(n) || taken[n] || hasAlias(p, n) {
			continue
		}
		if len(p.Aliases) >= maxProjectAliases {
			return
		}
		p.Aliases = append(p.Aliases, a)
	}
}

// PruneProjectAliases takes off each live project the aliases the rules above would not let it collect, for the
// knowledge bases that collected them before the rules: another live project's name or slug (a name beats an alias
// in FindProject, so the alias never finds this project and only tells the models the two are one), its own name, a
// repeat, one too generic, and those past the first maxProjectAliases. The names of projects merged into it, directly
// or through another merged project, stay and do not count: their mentions should find it. An alias two live projects
// share stays on both (whose it is cannot be told), and so do aliases set by hand (locked). Returns how many went.
func (e *Engine) PruneProjectAliases() int {
	projects := e.Store.Projects()
	bySlug := map[string]*Project{}
	for _, p := range projects {
		bySlug[p.Slug] = p
	}
	owners := map[string][]string{}  // a live project's normalized name or slug → the projects going by it
	mergedIn := map[[2]string]bool{} // {live slug, normalized name of a project merged into it}
	for _, p := range projects {
		if p.Live() {
			for _, n := range []string{normName(p.Name), normName(p.Slug)} {
				if n != "" && !slices.Contains(owners[n], p.Slug) {
					owners[n] = append(owners[n], p.Slug)
				}
			}
			continue
		}
		into := bySlug[p.MergedInto]
		for range len(projects) { // a chain ends at a live project; the bound only guards a cycle
			if into == nil || into.Live() {
				break
			}
			into = bySlug[into.MergedInto]
		}
		if into != nil && into.Live() {
			mergedIn[[2]string{into.Slug, normName(p.Name)}] = true
		}
	}
	removed := 0
	for _, p := range projects {
		if !p.Live() || len(p.Aliases) == 0 || p.fieldLocked("aliases") {
			continue
		}
		own, seen, counted := normName(p.Name), map[string]bool{}, 0
		var kept []string
		for _, a := range p.Aliases {
			n := normName(a)
			other := slices.ContainsFunc(owners[n], func(s string) bool { return s != p.Slug })
			if n == "" || n == own || seen[n] || other {
				continue
			}
			seen[n] = true
			if !mergedIn[[2]string{p.Slug, n}] {
				if tooGenericAlias(n) || counted >= maxProjectAliases {
					continue
				}
				counted++
			}
			kept = append(kept, a)
		}
		if len(kept) == len(p.Aliases) {
			continue
		}
		n := len(p.Aliases) - len(kept)
		p.Aliases = kept
		// Not PutProject: tidying its aliases is not news, so Updated (recency in lists and in FindProject) stays.
		if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false); err != nil {
			e.logf("prune aliases of %s: %v", p.Slug, err)
			continue
		}
		removed += n
	}
	return removed
}

// knownProjects are the projects the models may name: not done or ignored.
func (e *Engine) knownProjects() []prompts.KnownProject {
	var out []prompts.KnownProject
	for _, p := range e.Store.Projects() {
		if p.Status == "done" || p.Status == "ignored" || !p.Live() {
			continue
		}
		// With its one line: names alone made the model start a second project for the same work under the name
		// of the group it was reading (2026-11-01, two work groups about the same project).
		out = append(out, prompts.KnownProject{Name: p.Name, Aliases: p.Aliases, OneLine: p.OneLine, Pinned: p.Pinned})
	}
	return out
}

// evolveProject merges a mention into its project (creating it), links the chat and its people, and logs the update.
func (e *Engine) evolveProject(name, kind string, aliases []string, myRole string, c *Chat, update string, now time.Time) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	p := e.FindProject(name)
	if p == nil {
		for _, a := range aliases {
			if p = e.FindProject(a); p != nil {
				break
			}
		}
	}
	if p != nil && p.Status == "ignored" {
		return ""
	}
	if p == nil {
		name = shownName(name) // not 「X（a、b）」 as a new project's name (#5824)
		slug := Slug(name)
		if slug == "" {
			return ""
		}
		p = &Project{Slug: slug, Name: name, Kind: "short", Status: "active", Created: now}
	}
	if kind == "short" || kind == "long" {
		if p.Kind == "" || p.Kind == "short" {
			p.Kind = kind // long wins: a mission does not become a feature
		}
	}
	addProjectAliases(p, aliases, e.takenProjectNames(p.Slug))
	if myRole != "" {
		p.MyRole = oneLine(myRole)
	}
	p.Chats = addUnique(p.Chats, c.ID)
	p.People = addUnique(p.People, c.People...)
	if strings.TrimSpace(update) != "" {
		_ = e.Store.AppendLog(p.Slug, now, c.Name, update)
		p.Dirty = true
		if p.Status == "done" {
			p.Status = "active"
		}
	}
	_ = e.Store.PutProject(p)
	for _, pid := range c.People {
		if per, ok := e.Store.Person(pid); ok {
			n := len(per.Projects)
			if per.Projects = addUnique(per.Projects, p.Slug); len(per.Projects) != n {
				_ = e.Store.PutPerson(per)
			}
		}
	}
	return p.Slug
}

type projectDigest struct {
	Summary    string   `json:"summary"`
	OneLine    string   `json:"one_line"`
	Goal       string   `json:"goal"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Importance string   `json:"importance"`
	Relevance  int      `json:"relevance"`
	Tags       []string `json:"tags"`
	Keywords   []string `json:"keywords"`
}

// RefreshProjects rewrites the summaries of projects with news, at most max per run.
func (e *Engine) RefreshProjects(ctx context.Context, max int) int {
	if e.Model == nil {
		return 0
	}
	var dirty []*Project
	for _, p := range e.Store.Projects() {
		if p.Dirty && p.Status != "ignored" && p.Live() {
			dirty = append(dirty, p)
		}
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].Relevance > dirty[j].Relevance })
	n := 0
	for _, p := range dirty {
		if n >= max || ctx.Err() != nil {
			break
		}
		if err := e.refreshProject(ctx, p); err != nil {
			if ai.IsPaused(err) { // the model's connection is held: the rest wait with it (still dirty)
				break
			}
			e.logf("project %s: %v", p.Slug, err)
			continue
		}
		n++
	}
	return n
}

func (e *Engine) refreshProject(ctx context.Context, p *Project) error {
	current, ok := e.Store.Project(p.Slug)
	if !ok {
		return nil
	}
	p = current
	in := e.projectInput(p)
	var d projectDigest
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.modelFor("kb.project").JSONMax(cctx, prompts.RenderKBProject(prompts.KBOwner{Owner: e.Owner}), prompts.RenderKBProjectInput(in), 8000, &d); err != nil {
		return err
	}
	return e.commitProject(p, in, d)
}

func (e *Engine) projectInput(p *Project) prompts.KBProjectInput {
	in := prompts.KBProjectInput{Pinned: p.Pinned, Name: p.Name, Kind: p.Kind, Aliases: p.Aliases, Tags: e.TopTags(40),
		Summary: e.Store.Summary(KindProject, p.Slug), Log: e.Store.Log(p.Slug, 40)}
	for i, cid := range p.Chats {
		if i >= 8 {
			break
		}
		sum := e.Store.Summary(KindChat, cid)
		if r := []rune(sum); len(r) > 1500 {
			sum = string(r[:1500]) + "…"
		}
		in.Chats = append(in.Chats, prompts.KBProjectChat{ID: cid, Summary: sum})
	}
	in.Moments = e.projectMomentsForPrompt(p, 5)
	return in
}

// --- people ---

func (e *Engine) notePersonNote(c *Chat, name, note string) {
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	if name == "" || note == "" {
		return
	}
	for _, pid := range c.People {
		if p, ok := e.Store.Person(pid); ok && p.Name == name {
			p.Note = oneLine(note)
			_ = e.Store.PutPerson(p)
			e.index(KindPerson, p.ID, p.Name, p.Tagging, p.Note, p.LastSeen)
			return
		}
	}
}

// WritePersonSummary renders people/<id>/summary.md from what is known (no model call): who, where, which projects,
// and the DM's context.
func (e *Engine) WritePersonSummary(pid string) {
	p, ok := e.Store.Person(pid)
	if !ok {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", p.Name)
	if p.Pinned != "" {
		fmt.Fprintf(&b, "## 已确认的事实\n%s\n\n", p.Pinned)
	}
	if p.Note != "" {
		fmt.Fprintf(&b, "%s\n\n", p.Note)
	}
	if p.Importance != "" {
		fmt.Fprintf(&b, "重要级 %s · 与我相关性 %d · %s\n\n", p.Importance, p.Relevance, strings.Join(p.Tags, " "))
	}
	if len(p.Projects) > 0 {
		b.WriteString("## 项目\n")
		for _, slug := range p.Projects {
			if pr, ok := e.Store.Project(slug); ok {
				fmt.Fprintf(&b, "- %s（projects/%s）%s\n", pr.Name, pr.Slug, prefixed(" — ", pr.OneLine))
			}
		}
		b.WriteString("\n")
	}
	if len(p.Chats) > 0 {
		b.WriteString("## 会话\n")
		for _, cid := range p.Chats {
			if c, ok := e.Store.Chat(cid); ok {
				fmt.Fprintf(&b, "- %s（chats/%s，%s）%s\n", c.Name, c.ID, c.Kind, prefixed(" — ", c.OneLine))
			}
		}
		b.WriteString("\n")
	}
	for _, cid := range p.Chats {
		if c, ok := e.Store.Chat(cid); ok && (c.Kind == "dm" || c.Kind == "mail") {
			if sum := strings.TrimSpace(e.Store.Summary(KindChat, cid)); sum != "" {
				fmt.Fprintf(&b, "## 和我的私聊\n%s\n", strings.TrimPrefix(sum, "# "+c.Name+"\n\n"))
			}
		}
	}
	_ = e.Store.PutSummary(KindPerson, pid, b.String())
}

func prefixed(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// TopTags returns the n most used tags across the knowledge base.
func (e *Engine) TopTags(n int) []string {
	count := map[string]int{}
	add := func(t []string) {
		for _, x := range t {
			count[x]++
		}
	}
	for _, c := range e.Store.Chats() {
		add(c.Tags)
	}
	for _, p := range e.Store.Projects() {
		add(p.Tags)
	}
	tags := make([]string, 0, len(count))
	for t := range count {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		if count[tags[i]] != count[tags[j]] {
			return count[tags[i]] > count[tags[j]]
		}
		return tags[i] < tags[j]
	})
	if len(tags) > n {
		tags = tags[:n]
	}
	return tags
}
