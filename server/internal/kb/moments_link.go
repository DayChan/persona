package kb

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Moments for projects (docs/assistant/knowledge.md "Moments of a project"). The archive (moments/, the crawler) is never summarized post
// by post; a project finds its posts instead: its keywords (from the project rewrite) pick candidates out of the
// posts of the last year, and the model keeps the ones really about it. Keyword hits alone are noise (165 posts say
// "codex", hardly any about the project named so). Every post judged for a project is remembered
// (projects/<slug>/moments-judged.json), so a run only asks about posts it has not seen, and new posts are picked up
// as the crawler brings them.

const (
	maxProjectMoments = 50 // kept per project, newest first
	momentCandidates  = 20 // posts put before the model per project and run
	momentsMaxAge     = 365 * 24 * time.Hour
)

// cleanKeywords keeps keywords long enough to mean something: two characters of CJK, three of anything else ("MA"
// matches half the archive).
func cleanKeywords(in []string) []string {
	var out []string
	for _, k := range in {
		k = strings.TrimSpace(k)
		if usefulKeyword(k) && len(out) < 8 {
			out = addUnique(out, k)
		}
	}
	return out
}

func usefulKeyword(k string) bool {
	r := []rune(k)
	if len(r) == 0 || genericWord[strings.ToLower(k)] {
		return false
	}
	for _, c := range r {
		if unicode.Is(unicode.Han, c) {
			return len(r) >= 2
		}
	}
	return len(r) >= 3
}

func (s *Store) judgedPath(slug string) string {
	return s.path(KindProject, slug, "moments-judged.json")
}

func (s *Store) judged(slug string) map[string]bool {
	out := map[string]bool{}
	if raw, err := os.ReadFile(s.judgedPath(slug)); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// LinkMoments looks for Moments posts for at most max projects (the most relevant live ones with keywords and posts
// not judged yet). Returns how many posts were linked.
func (e *Engine) LinkMoments(ctx context.Context, max int) int {
	if e.Model == nil {
		return 0
	}
	cutoff := time.Now().Add(-momentsMaxAge)
	var posts []*MomentPost
	for _, p := range e.Store.MomentPosts() {
		if !p.At.Before(cutoff) && strings.TrimSpace(p.Text) != "" {
			posts = append(posts, p)
		}
	}
	if len(posts) == 0 {
		return 0
	}
	var projects []*Project
	for _, p := range e.Store.Projects() {
		if p.Live() && p.Status != "ignored" && p.Status != "done" && len(p.Keywords) > 0 {
			projects = append(projects, p)
		}
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].Relevance > projects[j].Relevance })
	linked, asked := 0, 0
	for _, p := range projects {
		if asked >= max || ctx.Err() != nil {
			break
		}
		judged := e.Store.judged(p.Slug)
		cands := momentCandidatesFor(p, posts, judged)
		if len(cands) == 0 {
			continue
		}
		asked++
		n, err := e.judgeMoments(ctx, p, cands, judged)
		if err != nil {
			if ai.IsPaused(err) {
				break
			}
			e.logf("moments for %s: %v", p.Slug, err)
			continue
		}
		linked += n
	}
	return linked
}

// momentCandidatesFor ranks the posts not judged for p by how many of its keywords they have, then comments, then
// how new; at most momentCandidates.
func momentCandidatesFor(p *Project, posts []*MomentPost, judged map[string]bool) []*MomentPost {
	var terms []string
	for _, k := range p.Keywords {
		if usefulKeyword(k) {
			terms = append(terms, strings.ToLower(k))
		}
	}
	if len(terms) == 0 {
		return nil
	}
	type cand struct {
		p     *MomentPost
		score float64
	}
	var cs []cand
	now := time.Now()
	for _, m := range posts {
		if judged[m.ID] {
			continue
		}
		text := strings.ToLower(m.Text)
		hits := 0
		for _, t := range terms {
			if strings.Contains(text, t) {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		age := now.Sub(m.At).Hours() / 24
		cs = append(cs, cand{m, float64(hits)*10 + math.Log1p(float64(m.CommentCount)) - age/90})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].p.ID < cs[j].p.ID
	})
	out := make([]*MomentPost, 0, min(len(cs), momentCandidates))
	for i := 0; i < len(cs) && i < momentCandidates; i++ {
		out = append(out, cs[i].p)
	}
	return out
}

func momentForPrompt(m *MomentPost, note string, chars int, zone *time.Location) prompts.KBMoment {
	who := m.Author
	if m.Anonymous || who == "" {
		who = "匿名"
	}
	return prompts.KBMoment{ID: m.ID, Author: who, At: m.At.In(zone).Format("2006-01-02"), Comments: m.CommentCount,
		Text: clip(oneLine(m.Text), chars), Note: note}
}

// judgeMoments asks which candidates are about p, links those, and remembers every verdict.
func (e *Engine) judgeMoments(ctx context.Context, p *Project, cands []*MomentPost, judged map[string]bool) (int, error) {
	in := prompts.KBMomentsLinkInput{Name: p.Name, Aliases: p.Aliases, Keywords: p.Keywords, OneLine: p.OneLine, Goal: p.Goal,
		Summary: clip(e.Store.Summary(KindProject, p.Slug), 1500)}
	for _, m := range cands {
		in.Posts = append(in.Posts, momentForPrompt(m, "", 300, e.Store.Zone()))
	}
	var d struct {
		Posts []struct {
			ID      string `json:"id"`
			Related bool   `json:"related"`
			Note    string `json:"note"`
		} `json:"posts"`
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.modelFor("kb.moments").JSONMax(cctx, prompts.RenderKBMomentsLink(prompts.KBOwner{Owner: e.Owner}),
		prompts.RenderKBMomentsLinkInput(in), 3000, &d); err != nil {
		return 0, err
	}
	e.Store.applyMu.Lock()
	defer e.Store.applyMu.Unlock()
	byID := map[string]*MomentPost{}
	judged = e.Store.judged(p.Slug)
	for _, candidate := range cands {
		m, exists := e.Store.MomentPost(candidate.ID)
		if !exists {
			continue
		}
		byID[m.ID] = m
		judged[m.ID] = true // asked: an answer the model left out counts as not related
	}
	// Reread: the project may have changed while the model thought.
	cur, ok := e.Store.Project(p.Slug)
	if !ok || !cur.Live() {
		return 0, nil
	}
	var added []ProjectMoment
	for _, r := range d.Posts {
		m := byID[r.ID]
		if m == nil || !r.Related {
			continue
		}
		added = append(added, ProjectMoment{ID: m.ID, Note: oneLine(r.Note), At: m.At})
	}
	raw, _ := json.Marshal(judged)
	if err := writeFile(e.Store.judgedPath(p.Slug), raw); err != nil {
		return 0, err
	}
	if len(added) == 0 {
		return 0, nil
	}
	cur.Moments = mergeMoments(cur.Moments, added)
	// Recent ones are news: a timeline line each, and the profile is rewritten with them.
	for _, m := range added {
		if time.Since(m.At) < 14*24*time.Hour {
			post := byID[m.ID]
			_ = e.Store.AppendLog(cur.Slug, m.At, "公司圈 · "+momentForPrompt(post, "", 0, e.Store.Zone()).Author,
				firstNonEmpty(m.Note, clip(oneLine(post.Text), 60)))
			cur.Dirty = true
		}
	}
	if err := e.Store.PutProject(cur); err != nil {
		return 0, err
	}
	return len(added), nil
}

// projectMomentsForPrompt is a project's linked posts for its rewrite: the most commented first, at most n.
func (e *Engine) projectMomentsForPrompt(p *Project, n int) []prompts.KBMoment {
	type pm struct {
		m    *MomentPost
		note string
	}
	var list []pm
	for _, x := range p.Moments {
		if m, ok := e.Store.MomentPost(x.ID); ok {
			list = append(list, pm{m, x.Note})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].m.CommentCount > list[j].m.CommentCount })
	var out []prompts.KBMoment
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, momentForPrompt(list[i].m, list[i].note, 400, e.Store.Zone()))
	}
	return out
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// SeedKeywords marks the live projects without keywords that changed within recent dirty, so the next rewrites give
// them keywords (projects from before keywords existed). Returns how many.
func (e *Engine) SeedKeywords(recent time.Duration) int {
	n := 0
	for _, p := range e.Store.Projects() {
		if !p.Live() || p.Status == "ignored" || p.Status == "done" || len(p.Keywords) > 0 || p.Dirty || time.Since(p.Updated) > recent {
			continue
		}
		p.Dirty = true
		if e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false) == nil { // not PutProject: Updated stays
			n++
		}
	}
	return n
}
