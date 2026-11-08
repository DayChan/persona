package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- duplicates ----

// duplicateHints are the tasks going on and the agents at work that look like the same work as task t: a shared
// issue / PR number, or the same machine and directory with a similar title, or a very similar title anywhere.
func duplicateHints(t PlanTask, todos []Todo, sessions []proto.Session) []string {
	refs := issueRefs(t.Title + " " + t.Prompt)
	var out []string
	seen := map[string]bool{}
	add := func(key, line string) {
		if !seen[key] && len(out) < 4 {
			seen[key] = true
			out = append(out, line)
		}
	}
	for _, o := range todos {
		if o.DoneAt != nil {
			continue
		}
		sameDir := false
		for _, a := range o.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil && a.MachineID == t.Machine && dirsOverlap(a.Cwd, t.Cwd) {
				sameDir = true
			}
		}
		if shared := sharedRef(refs, issueRefs(o.Title+" "+o.Notes)); shared != "" {
			add("todo:"+o.ID, fmt.Sprintf("任务「%s」（%s）也提到 %s", o.Title, o.ID, shared))
		} else if sim := titleSimilarity(t.Title, o.Title); sim >= 0.7 || sameDir && sim >= 0.4 {
			add("todo:"+o.ID, fmt.Sprintf("任务「%s」（%s）标题相近%s", o.Title, o.ID, map[bool]string{true: "，在同一个目录", false: ""}[sameDir]))
		}
	}
	for _, c := range sessions {
		if c.Assistant || c.State != proto.StateWorking && c.State != proto.StateNeedsYou {
			continue
		}
		title := cmp.Or(c.Title, c.LastText)
		where := fmt.Sprintf("%s 上的 Agent「%s」（session_id=%s）", c.MachineID, clip(oneLine(title), 40), short(c.SessionID))
		sameDir := c.MachineID == t.Machine && dirsOverlap(c.Cwd, t.Cwd)
		if shared := sharedRef(refs, issueRefs(c.Title+" "+c.LastText)); shared != "" {
			add("agent:"+c.MachineID+"/"+c.SessionID, where+"也在做 "+shared)
		} else if sim := titleSimilarity(t.Title, c.Title); sim >= 0.7 || sameDir && sim >= 0.4 {
			add("agent:"+c.MachineID+"/"+c.SessionID, where+"在做相近的事")
		}
	}
	return out
}

// issueRefs are the issue / PR numbers s names: #123, issues/123, pull/123 (three digits or more: not a list's #1).
func issueRefs(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		var rest string
		switch {
		case s[i] == '#':
			rest = s[i+1:]
		case strings.HasPrefix(s[i:], "issues/"):
			rest = s[i+len("issues/"):]
		case strings.HasPrefix(s[i:], "pull/"):
			rest = s[i+len("pull/"):]
		default:
			continue
		}
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n >= 3 && n <= 7 {
			ref := "#" + rest[:n]
			if !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	return out
}

func sharedRef(a, b []string) string {
	for _, x := range a {
		if slices.Contains(b, x) {
			return x
		}
	}
	return ""
}

// dirsOverlap: one directory is the other or inside it.
func dirsOverlap(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// titleSimilarity is how alike two short titles are: the Dice coefficient of their character bigrams (spaces and
// punctuation dropped, letters folded), 0 to 1.
func titleSimilarity(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	common := 0
	left := map[string]int{}
	for _, g := range gb {
		left[g]++
	}
	for _, g := range ga {
		if left[g] > 0 {
			left[g]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(ga)+len(gb))
}

func bigrams(s string) []string {
	var r []rune
	for _, c := range strings.ToLower(s) {
		if c == ' ' || c == '\t' || utf8.RuneLen(c) == 1 && !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') ||
			strings.ContainsRune("，。、：；！？「」（）《》【】·…—", c) {
			continue
		}
		r = append(r, c)
	}
	out := make([]string, 0, len(r))
	for i := 0; i+1 < len(r); i++ {
		out = append(out, string(r[i:i+2]))
	}
	return out
}

// planDuplicates is what the task_plan tool adds to its answer about the plan's tasks that look like work already
// going on (duplicateHints); "" when none does. A hint, not a refusal: the plan goes on as ever.
func (s *Service) planDuplicates(ctx context.Context, tasks []PlanTask) string {
	var open []Todo
	if s.ensureTodosLoaded(ctx) == nil {
		own := map[string]bool{} // a todo a task runs on is that task, not a copy of it
		for _, t := range tasks {
			if t.ReuseTodo != "" {
				own[t.ReuseTodo] = true
			}
		}
		s.todos.mu.RLock()
		for _, t := range s.todos.todos {
			if t.DoneAt == nil && !own[t.ID] {
				open = append(open, copyTodoLocked(t))
			}
		}
		s.todos.mu.RUnlock()
	}
	var sessions []proto.Session
	for _, d := range s.devices(ctx) {
		if !d.Reachable {
			continue
		}
		list, err := s.sessions(ctx, d.ID)
		if err != nil {
			continue
		}
		for _, c := range list {
			if c.MachineID == "" {
				c.MachineID = d.ID
			}
			sessions = append(sessions, c)
		}
	}
	var b strings.Builder
	for _, t := range tasks {
		hints := duplicateHints(t, open, sessions)
		if len(hints) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n- 「%s」可能和这些重复：%s", t.Title, strings.Join(hints, "；"))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nPossible duplicates (work already going on): look at them (agent_read, the todo) before this goes on; if it is the " +
		"same work, cancel the new task (task_review cancelled, or dismiss the plan) or send what is new to the agent already on it." + b.String()
}
