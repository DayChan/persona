package assistant

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// A todo a task opens (task_plan, agent_start) used to be titled with its prompt's first line, which opens with what
// the agent is told rather than what the work is: "ultrathink ultracode, the owner's feedback of 11-05 19:47, cleared to start: …" (in Chinese)
// (#2775, docs/assistant/todos.md "To-do titles"). The todo now gets that line cleaned up at once (cleanTodoTitle), so nothing
// waits; the small-task model then names it from the prompt in the background, and its title replaces the cleaned
// one only while the todo still has it: a title the user gave meanwhile stays.

const maxTodoTitle = 20 // characters, in Chinese; todoTitleMax for the user's language

// todoTitleMax is how many characters a todo title in the user's language may have.
func todoTitleMax(locale string) int {
	if l := i18n.Lang(locale); i18n.Valid(l) && l != i18n.ZhHans && l != i18n.Ja {
		return 40
	}
	return maxTodoTitle
}

var (
	// Words that tune the agent's thinking, at the start of a prompt (Claude Code's ultrathink and the like).
	todoTitleKeyword = lazyre.New(`(?i)^/?(?:ultrathink|ultracode|ultraplan|megathink|think\s+(?:very\s+)?hard(?:er)?|think\s+deeply|think\s+harder)\b[\s,，、:：;；.。!！-]*`)
	// Where the request comes from and when: "the owner's feedback of 11-05 19:47," / "the message of 11/05 19:47:" (in Chinese). A date needs a time
	// or what came then after it, so a version number ("fix the crash in 3.2") is not taken for one.
	todoTitleStamp = lazyre.New(`^[^\s，,:：]{0,8}\s*\d{1,4}[-/.月]\d{1,2}日?(?:\s*\d{1,2}:\d{2}(?::\d{2})?\s*(?:的)?(?:反馈|消息|要求|需求|留言)?|\s*(?:的)?(?:反馈|消息|要求|需求|留言))[\s,，、:：;；]*`)
	// A note on who hands the work over, in brackets: "secretary hands out work (the owner allowed it…):" or "[hand out work]" (in Chinese, full-width brackets).
	todoTitleBracket = lazyre.New(`^[^\s（(【\[]{0,8}\s*(?:（[^）]*）|\([^)]*\)|【[^】]*】|\[[^\]]*\])[\s,，、:：;；]*`)
	// Permission the agent is given, which says nothing about the work.
	todoTitleGrant = lazyre.New(`^(?:已(?:经)?(?:获)?授权|授权)(?:过)?(?:，|,)?(?:直接|可以|你)?(?:开工|动手|开始|执行|做)?[\s,，、:：;；!！。]*`)
	// The bare go-ahead before a colon: "just do it:", "start now:" (in Chinese).
	todoTitleGo = lazyre.New(`^(?:直接|就|马上|立刻)(?:做|开工|动手|开始|执行|改)\s*[:：]\s*`)
)

// cleanTodoTitle is s (a task's prompt, or a title made from it) on one line without what it opens with for the
// agent (keywords, where and when it was asked, who handed it over, the go-ahead), cut to max characters; "" when
// nothing is left.
func cleanTodoTitle(s string, max int) string {
	s = strings.TrimSpace(contextBlock.ReplaceAllString(s, " "))
	s = strings.Join(strings.Fields(cleanTitleNoise(s)), " ")
	for {
		before := s
		for _, re := range []*lazyre.Regexp{todoTitleKeyword, todoTitleStamp, todoTitleBracket, todoTitleGrant, todoTitleGo} {
			if loc := re.FindStringIndex(s); loc != nil && loc[1] > 0 {
				s = strings.TrimSpace(s[loc[1]:])
			}
		}
		if s == before {
			break
		}
	}
	s = strings.Trim(s, "，。,.：:；;、 ")
	return strings.Trim(cutTitle(s, max), "，。,.：:；;、 ")
}

// cutTitle is s cut to max characters, never inside a joined emoji (👨‍👩‍👧‍👦 is kept whole or dropped) or before a
// combining mark.
func cutTitle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	n := max
	for n > 0 && (r[n] == '\u200d' || r[n-1] == '\u200d' || r[n] == '\ufe0f' || unicode.Is(unicode.Mn, r[n]) ||
		(r[n] >= 0x1f3fb && r[n] <= 0x1f3ff)) {
		n--
	}
	return strings.TrimSpace(string(r[:n]))
}

// taskTitle is the title a todo for a task with this prompt starts with: given, the assistant's own (task_plan),
// stays when it is a real title; one that is the prompt's opening, too long or opens with what cleanTodoTitle takes
// off is cleaned. retitle: the model should name it (it was not the assistant's good title).
func (s *Service) taskTitle(ctx context.Context, given, prompt string) (title string, retitle bool) {
	max := todoTitleMax(s.hello(ctx).Locale)
	given = strings.TrimSpace(given)
	if given != "" && !promptLikeTitle(given, max) {
		return given, false
	}
	if title = cleanTodoTitle(given, max); title == "" || given == "" {
		title = cleanTodoTitle(prompt, max)
	}
	if title == "" {
		title = cutRunes(oneLine(given+" "+prompt), max)
	}
	return title, true
}

// promptLikeTitle: title reads as a prompt rather than a name for the work: longer than max, or it opens with what
// cleanTodoTitle takes off (a keyword, a time stamp, a bracketed note, the go-ahead).
func promptLikeTitle(title string, max int) bool {
	title = strings.TrimSpace(title)
	return title != "" && (utf8.RuneCountInString(title) > max || hasPromptOpening(title))
}

// hasPromptOpening: title opens with something cleanTodoTitle takes off.
func hasPromptOpening(title string) bool {
	return cleanTodoTitle(title, 1<<20) != strings.Trim(oneLine(title), "，。,.：:；;、 ")
}

// openedFromPrompt: title is how notes starts (a todo titled with its prompt's first line, as before #2775).
func openedFromPrompt(title, notes string) bool {
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(title), "…"))
	return t != "" && strings.HasPrefix(oneLine(notes), oneLine(t))
}

// nameTodoLater has the model name a task's todo, off the caller's path; from is the title it was made with.
func (s *Service) nameTodoLater(ctx context.Context, id, from, prompt string) {
	s.todos.titling.Add(1)
	go func() {
		defer s.todos.titling.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		to := s.todoTitleFromModel(ctx, prompt)
		if to == "" || to == from {
			return
		}
		if _, ok, err := s.retitleTodo(ctx, id, from, to); err != nil {
			s.logf("todo %s: title: %v", id, err)
		} else if ok {
			s.logf("todo %s: titled %q", id, to)
		}
	}()
}

// todoTitleFromModel is the small-task model's title for a task with this prompt; "" when there is no model, it
// fails or says nothing usable (the todo keeps the title it has).
func (s *Service) todoTitleFromModel(ctx context.Context, prompt string) string {
	prompt = strings.TrimSpace(contextBlock.ReplaceAllString(prompt, ""))
	if prompt == "" {
		return ""
	}
	locale := s.hello(ctx).Locale
	max := todoTitleMax(locale)
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Reasoning models think first: a small max_tokens comes back empty (agents_start.go's tab names).
	text, err := s.purposeModel("todo.title").Complete(cctx,
		prompts.RenderTodoTitle(prompts.TodoTitle{Language: answerLanguage(locale), Max: max}),
		prompts.RenderTodoTitleInput(cutRunes(prompt, 1500)), 200)
	if err != nil {
		if !ai.IsPaused(err) {
			s.logf("todo title: %v", err)
		}
		return ""
	}
	line := ""
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	line = strings.Trim(line, "「」『』《》\"'“”`* ")
	return cleanTodoTitle(line, max)
}

// retitleTodo gives todo id the title to while its title is still from; false (no error) when it is not any more or
// the todo is gone.
func (s *Service) retitleTodo(ctx context.Context, id, from, to string) (Todo, bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false, err
	}
	still := func(t *Todo) bool { return t.Title == from }
	s.todos.writing.Lock() // memory takes the rows in the order the store committed them (todoBook.writing)
	now := time.Now().UTC()
	var row db.TodoRow
	if st := s.todoDB(); st != nil {
		r, ok, err := st.RetitleTodo(ctx, id, from, to, now)
		if err != nil || !ok {
			s.todos.writing.Unlock()
			return Todo{}, false, err
		}
		row = *r
	} else {
		cur, ok := s.todoNow(id)
		if !ok || cur.Title != from {
			s.todos.writing.Unlock()
			return Todo{}, false, nil
		}
		row = db.TodoRow{ID: cur.ID, Title: to, Notes: cur.Notes, DoneAt: cur.DoneAt, CreatedAt: cur.CreatedAt, UpdatedAt: now,
			Origin: cur.Origin, PlanID: cur.PlanID, StartError: cur.StartError, DoneBy: cur.DoneBy, DoneReason: cur.DoneReason,
			ReopenedAt: cur.ReopenedAt, ReopenGeneration: cur.ReopenGeneration, Quiet: cur.Quiet}
		cur.keepPlan(&row)
	}
	t, doneKeys, ok := s.setTodoRow(id, row, still)
	s.todos.writing.Unlock()
	if ok {
		s.todoRowSet(ctx, doneKeys)
	}
	return t, ok, nil
}

// TodoRetitle is one todo POST /api/v1/todos/retitle renamed (or would rename).
type TodoRetitle struct {
	ID       string `json:"id"`
	OldTitle string `json:"old_title"`
	NewTitle string `json:"new_title"`
}

// retitleable: a todo's title is still the one a task gave it before #2775, its prompt's first line cut: longer
// than max characters and how its notes (the prompt) start. A todo keeps no record of who titled it, so a title that only
// looks like a prompt ("[urgent] fix login", "11/05 feedback: fix login", in Chinese) is taken as the user's and left.
func retitleable(title, notes string, max int) bool {
	return utf8.RuneCountInString(strings.TrimSuffix(strings.TrimSpace(title), "…")) > max && openedFromPrompt(title, notes)
}

// todoRetitleRoute: POST /api/v1/todos/retitle (?dry_run=1 lists only) renames the todos tasks opened before #2775
// with their prompt's first line: a todo from a conversation (origin set) whose title is retitleable. Any other
// title is taken as the assistant's or the user's and left.
func (s *Service) todoRetitleRoute() {
	s.handle("POST /api/v1/todos/retitle", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		dry := r.URL.Query().Get("dry_run")
		dryRun := dry != "" && dry != "0" && dry != "false"
		ctx := r.Context()
		max := todoTitleMax(s.hello(ctx).Locale)

		var todo []Todo
		s.todos.mu.RLock()
		for _, t := range s.todos.todos {
			if t.Origin != "" && retitleable(t.Title, t.Notes, max) {
				todo = append(todo, copyTodoLocked(t))
			}
		}
		s.todos.mu.RUnlock()
		sort.Slice(todo, func(i, j int) bool { return todo[i].CreatedAt.After(todo[j].CreatedAt) })

		// The model, a few at a time; the cleaned title when it has none.
		names := make([]string, len(todo))
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i, t := range todo {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				if names[i] = s.todoTitleFromModel(ctx, t.Notes); names[i] == "" {
					names[i] = cleanTodoTitle(t.Notes, max)
				}
			})
		}
		wg.Wait()

		out := make([]TodoRetitle, 0, len(todo))
		for i, t := range todo {
			if names[i] == "" || names[i] == t.Title {
				continue
			}
			if !dryRun {
				_, ok, err := s.retitleTodo(ctx, t.ID, t.Title, names[i])
				if err != nil {
					apierr.Write(w, http.StatusInternalServerError, err)
					return
				}
				if !ok { // renamed or deleted meanwhile
					continue
				}
			}
			out = append(out, TodoRetitle{ID: t.ID, OldTitle: t.Title, NewTitle: names[i]})
		}
		apierr.WriteJSON(w, map[string]any{"dry_run": dryRun, "todos": out})
	})
}
