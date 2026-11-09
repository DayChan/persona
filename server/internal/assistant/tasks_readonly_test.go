package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func readonlyTaskArgs(t *testing.T, tasks ...PlanTask) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"tasks": tasks})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readonlyTestTask(permission string) PlanTask {
	return PlanTask{Title: "Inspect source", Prompt: "Read the source and explain the login flow.", Machine: "box",
		Cwd: "/code/app", Readonly: true, Permission: permission}
}

// Avoid a real model server in a synctest bubble: these tests inspect dispatch, not generated tab names.
func readonlyTaskService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	f.ownModels = true
	return newStorelessService(t, f), f
}

func readonlyFeedPlan(t *testing.T, s *Service, conversation string) *TaskPlan {
	t.Helper()
	_, plans, err := s.feed(context.Background(), conversation, time.Time{})
	if err != nil || len(plans) != 1 {
		t.Fatalf("feed plans = %v, error = %v", plans, err)
	}
	for _, p := range plans {
		return p
	}
	return nil
}

func TestReadonlyTaskPlanLaunchPermissions(t *testing.T) {
	for _, permission := range []string{"", "default", "plan", "acceptEdits", "bypassPermissions"} {
		name := permission
		if name == "" {
			name = "omitted"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				if _, err := s.toolTaskPlan(context.Background(), "readonly-chat", readonlyTaskArgs(t, readonlyTestTask(permission))); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				p := readonlyFeedPlan(t, s, "readonly-chat")
				if p.State != planStarted || p.Tasks[0].Permission != "plan" || !p.Tasks[0].Readonly {
					t.Fatalf("saved read-only plan = %+v", p)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.starts) != 1 || f.starts[0]["permission_mode"] != "plan" {
					t.Fatalf("read-only launch = %v", f.starts)
				}
				if todo := onlyTodo(s); len(todo.Agents) != 1 {
					t.Fatalf("task did not finish dispatch: %+v", todo)
				}
			})
		})
	}
}

// Waiting cards and their edits must carry the same restriction as automatic starts.
func TestReadonlyTaskWaitingPlanEdits(t *testing.T) {
	for _, health := range []bool{false, true} {
		name := "mixed"
		if health {
			name = "health"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				conversation := "readonly-chat"
				tasks := []PlanTask{readonlyTestTask("bypassPermissions")}
				if health {
					f.hello.Assistant.MachineID, f.hello.Assistant.Machines = "box", []string{"box"}
					setSettings(t, s, func(v *Settings) { v.Main, v.MachineID = "main", "box" })
					f.setSessions("box", proto.Session{SessionID: conversation, Space: SpaceHealth})
				} else {
					edit := readonlyTestTask("acceptEdits")
					edit.Title, edit.Prompt, edit.Readonly = "Fix login", "Fix the login flow and add tests.", false
					tasks = append(tasks, edit)
				}
				if _, err := s.toolTaskPlan(context.Background(), conversation, readonlyTaskArgs(t, tasks...)); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				p := readonlyFeedPlan(t, s, conversation)
				if p.State != planOpen || p.Tasks[0].Permission != "plan" || len(f.starts) != 0 {
					t.Fatalf("waiting plan = %+v, starts = %v", p, f.starts)
				}
				p, err := s.startPlan(context.Background(), p.ID, []PlanTask{{Permission: "acceptEdits"}}, false)
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				p = readonlyFeedPlan(t, s, conversation)
				if p.State != planStarted || p.Tasks[0].Permission != "plan" {
					t.Fatalf("edited plan = %+v", p)
				}
				if len(f.starts) != len(tasks) {
					t.Fatalf("starts = %v", f.starts)
				}
				for _, start := range f.starts {
					want := "plan"
					// Dispatch order is independent of the plan's order; identify the task by its prompt.
					if strings.Contains(start["prompt"].(string), "Fix the login flow and add tests.") {
						want = "acceptEdits"
					}
					if start["permission_mode"] != want {
						t.Fatalf("start = %v, want permission %q", start, want)
					}
				}
			})
		})
	}
}

func TestReadonlyTaskLegacyStartAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "start"
		if retry {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				ctx := context.Background()
				p := &TaskPlan{ID: "plan_legacy", Conversation: "readonly-chat", State: planOpen,
					Tasks: []PlanTask{readonlyTestTask("bypassPermissions")}}
				if retry {
					todo, err := s.createTodo(ctx, p.Tasks[0].Title, p.Tasks[0].Prompt, p.Conversation, p.ID)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.failTodo(ctx, todo.ID, "Start failed"); err != nil {
						t.Fatal(err)
					}
					p.State, p.Tasks[0].TodoID = planStarted, todo.ID
				}
				if err := s.savePlan(ctx, p); err != nil {
					t.Fatal(err)
				}
				if retry {
					if _, err := s.retryTask(ctx, p.Tasks[0].TodoID); err != nil {
						t.Fatal(err)
					}
				} else if _, err := s.startPlan(ctx, p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				saved, err := s.loadPlan(ctx, p.ID)
				if err != nil || saved.Tasks[0].Permission != "plan" {
					t.Fatalf("legacy plan = %+v, error = %v", saved, err)
				}
				if len(f.starts) != 1 || f.starts[0]["permission_mode"] != "plan" || len(onlyTodo(s).Agents) != 1 {
					t.Fatalf("legacy launch = %v, todo = %+v", f.starts, onlyTodo(s))
				}
			})
		})
	}
}

func TestReadonlyTaskDispatchBoundary(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		for _, permission := range []string{"", "default", "plan", "acceptEdits", "bypassPermissions"} {
			s, f := readonlyTaskService(t)
			task := readonlyTestTask(permission)
			task.Readonly = readonly
			if _, _, err := s.startTaskAgent(context.Background(), &task, true); err != nil {
				t.Fatal(err)
			}
			want := permission
			if readonly {
				want = "plan"
			}
			got, _ := f.starts[0]["permission_mode"].(string)
			if got != want || task.Permission != want {
				t.Fatalf("readonly=%v permission=%q: launch=%v task=%+v", readonly, permission, f.starts, task)
			}
		}
	}
}

func TestReadonlyTaskUnsupportedLauncherReportsFailure(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				f.hello.Locale = string(lang)
				f.failStart = &rpc.Error{Code: "readonly_unsupported", Message: "Node cannot enforce read-only mode"}
				ctx := context.Background()
				if _, err := s.toolTaskPlan(ctx, "readonly-chat", readonlyTaskArgs(t, readonlyTestTask(""))); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				p := readonlyFeedPlan(t, s, "readonly-chat")
				if task := p.Tasks[0]; task.ErrorCode != "readonly_unsupported" || task.Permission != "plan" {
					t.Fatalf("unsupported task = %+v", task)
				}
				if todo := onlyTodo(s); todo.DoneAt == nil || len(todo.Agents) != 0 ||
					todo.StartError != i18n.T(lang, "rpc-errors.readonly_unsupported") {
					t.Fatalf("refusal was not localized or finished: %+v", todo)
				}
				if len(f.starts) != 1 || f.agents != 0 || len(f.checked) != 0 {
					t.Fatalf("refusal retried or probed the launcher: starts=%v agents=%d checks=%v", f.starts, f.agents, f.checked)
				}
				if health, ok := s.launcherHealth(ctx, "box", "claude"); ok {
					t.Fatalf("unsupported mode poisoned launcher health: %+v", health)
				}
			})
		})
	}
}

func TestReadonlyTaskPlanPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newDatabase(t), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _ := newTaskServiceOn(t, database)
	read := readonlyTestTask("bypassPermissions")
	edit := readonlyTestTask("acceptEdits")
	edit.Readonly = false
	if _, err := s.toolTaskPlan(ctx, "readonly-chat", readonlyTaskArgs(t, read, edit)); err != nil {
		t.Fatal(err)
	}
	p := readonlyFeedPlan(t, s, "readonly-chat")
	restarted, _ := newTaskServiceOn(t, database)
	saved, err := restarted.loadPlan(ctx, p.ID)
	if err != nil || saved.State != planOpen || saved.Tasks[0].Permission != "plan" || saved.Tasks[1].Permission != "acceptEdits" {
		t.Fatalf("persisted plan = %+v, error = %v", saved, err)
	}
}
