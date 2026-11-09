package db

import (
	"context"
	"testing"
	"time"
)

func TestTodoReopenGeneration(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	id := "reopen-generation"
	if err := d.CreateTodo(ctx, TodoRow{ID: id, Title: "Task", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	closeTodo, openTodo := true, false
	edit := func(done *bool, want int64) *TodoRow {
		t.Helper()
		row, err := d.UpdateTodo(ctx, id, nil, nil, done, at)
		if err != nil || row.ReopenGeneration != want {
			t.Fatalf("UpdateTodo: row=%+v err=%v, want generation %d", row, err, want)
		}
		return row
	}
	edit(nil, 0)
	edit(&openTodo, 0)
	edit(&closeTodo, 0)
	row := edit(&openTodo, 1)
	stored, err := d.GetTodo(ctx, id)
	if row.ReopenedAt == nil || !row.ReopenedAt.Equal(at) || err != nil || stored.ReopenedAt == nil || !stored.ReopenedAt.Equal(at.Truncate(time.Microsecond)) || stored.ReopenGeneration != row.ReopenGeneration {
		t.Fatalf("database precision: returned=%+v stored=%+v err=%v", row, stored, err)
	}
	edit(nil, 1)
	edit(&openTodo, 1)
	// Simulate an older binary's writes, using the same timestamp for both cycles.
	if _, err := d.pool.Exec(ctx, `UPDATE todos SET done_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE todos SET done_at = NULL, reopened_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatal(err)
	}
	row, err = d.GetTodo(ctx, id)
	if err != nil || row.ReopenGeneration != 2 {
		t.Fatalf("legacy reopen: %+v %v", row, err)
	}
	if _, err := d.FailTodo(ctx, id, "failed", at); err != nil {
		t.Fatal(err)
	}
	row, ok, err := d.RetryTodo(ctx, id, at.Add(-time.Hour))
	if err != nil || !ok || row.ReopenGeneration != 3 || row.ReopenedAt == nil || !row.ReopenedAt.Equal(at.Add(-time.Hour).Truncate(time.Microsecond)) {
		t.Fatalf("retry after clock rollback: %+v %v %v", row, ok, err)
	}
	edit(&closeTodo, 3)
	edit(&openTodo, 4)
	if _, err := d.pool.Exec(ctx, `UPDATE todos SET done_at = $2, reopen_generation = 9223372036854775807 WHERE id = $1`, id, at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateTodo(ctx, id, nil, nil, &openTodo, at); err == nil {
		t.Fatal("generation overflow reopened a todo")
	}
	row, err = d.GetTodo(ctx, id)
	if err != nil || row.DoneAt == nil || row.ReopenGeneration != 1<<63-1 {
		t.Fatalf("overflow changed todo: %+v %v", row, err)
	}
}

func TestTodos(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// 1. CreateTodo and GetTodo
	t1 := TodoRow{
		ID:        "t1",
		Title:     "天气 App 发 1.3 版",
		Notes:     "发布说明",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := d.CreateTodo(ctx, t1); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	got, err := d.GetTodo(ctx, "t1")
	if err != nil {
		t.Fatalf("GetTodo: %v", err)
	}
	if got.Title != t1.Title || got.Notes != t1.Notes || got.DoneAt != nil {
		t.Fatalf("unexpected got: %+v", got)
	}

	// 2. ListTodos
	t2 := TodoRow{
		ID:        "t2",
		Title:     "照片同步",
		Notes:     "HEIC 支持",
		CreatedAt: now.Add(time.Minute),
		UpdatedAt: now.Add(time.Minute),
	}
	if err := d.CreateTodo(ctx, t2); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	list, err := d.ListTodos(ctx)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(list) != 2 || list[0].ID != "t2" || list[1].ID != "t1" {
		t.Fatalf("unexpected list order: %+v", list)
	}

	// 3. UpdateTodo (PATCH semantics)
	newTitle := "天气 App 发 1.3.1 版"
	upTime := now.Add(2 * time.Minute)
	up, err := d.UpdateTodo(ctx, "t1", &newTitle, nil, nil, upTime)
	if err != nil {
		t.Fatalf("UpdateTodo title: %v", err)
	}
	if up.Title != newTitle || up.Notes != t1.Notes || up.DoneAt != nil {
		t.Fatalf("unexpected updated todo: %+v", up)
	}

	// Mark done
	doneTrue := true
	doneTime := now.Add(3 * time.Minute)
	up, err = d.UpdateTodo(ctx, "t1", nil, nil, &doneTrue, doneTime)
	if err != nil {
		t.Fatalf("UpdateTodo done: %v", err)
	}
	if up.DoneAt == nil || !up.DoneAt.Equal(doneTime) {
		t.Fatalf("expected DoneAt to be set, got %+v", up)
	}

	// Reopen
	doneFalse := false
	reopenTime := now.Add(4 * time.Minute)
	up, err = d.UpdateTodo(ctx, "t1", nil, nil, &doneFalse, reopenTime)
	if err != nil {
		t.Fatalf("UpdateTodo reopen: %v", err)
	}
	if up.DoneAt != nil {
		t.Fatalf("expected DoneAt to be nil, got %+v", up)
	}

	// FailTodo: an agent that did not start ends the todo with why (#1077); reopening clears it.
	failTime := now.Add(4*time.Minute + 30*time.Second)
	failed, err := d.FailTodo(ctx, "t1", "rpc: connection closed", failTime)
	if err != nil {
		t.Fatalf("FailTodo: %v", err)
	}
	if failed.DoneAt == nil || !failed.DoneAt.Equal(failTime) || failed.StartError != "rpc: connection closed" {
		t.Fatalf("failed todo = %+v", failed)
	}
	if got, _ := d.GetTodo(ctx, "t1"); got.StartError != "rpc: connection closed" || got.DoneAt == nil {
		t.Fatalf("stored failed todo = %+v", got)
	}
	if _, err := d.FailTodo(ctx, "nope", "x", failTime); err != ErrTodoNotFound {
		t.Fatalf("FailTodo on a missing todo: %v", err)
	}
	// RetryTodo (#1396): a failed start reopens to start again, once; a todo that is not failed is left alone.
	retryTime := now.Add(4*time.Minute + 40*time.Second)
	retried, ok, err := d.RetryTodo(ctx, "t1", retryTime)
	if err != nil || !ok {
		t.Fatalf("RetryTodo: %v %v", ok, err)
	}
	if retried.DoneAt != nil || retried.StartError != "" || retried.ReopenedAt == nil || !retried.ReopenedAt.Equal(retryTime) {
		t.Fatalf("retried todo = %+v", retried)
	}
	if again, ok, err := d.RetryTodo(ctx, "t1", retryTime.Add(time.Second)); err != nil || ok || again != nil {
		t.Fatalf("a second RetryTodo reopened it again: %+v %v %v", again, ok, err)
	}
	if _, _, err := d.RetryTodo(ctx, "nope", retryTime); err != ErrTodoNotFound {
		t.Fatalf("RetryTodo on a missing todo: %v", err)
	}
	if _, err := d.FailTodo(ctx, "t1", "rpc: connection closed", failTime); err != nil { // failed again
		t.Fatalf("FailTodo after the retry: %v", err)
	}
	up, err = d.UpdateTodo(ctx, "t1", nil, nil, &doneFalse, reopenTime)
	if err != nil {
		t.Fatalf("UpdateTodo reopen after failure: %v", err)
	}
	if up.DoneAt != nil || up.StartError != "" {
		t.Fatalf("reopened todo kept its failure: %+v", up)
	}

	// AutoDoneTodo (#1092): the hub closes an open task with why, unless the user reopened it after the judged work
	// began; the user's own done / reopen clears the mark, and reopening is remembered.
	if up.ReopenedAt == nil || !up.ReopenedAt.Equal(reopenTime) {
		t.Fatalf("reopen not remembered: %+v", up)
	}
	autoTime := reopenTime.Add(time.Minute)
	if row, ok, err := d.AutoDoneTodo(ctx, "t1", "已合并 PR", autoTime, reopenTime.Add(-time.Second)); err != nil || ok || row != nil {
		t.Fatalf("auto done over work from before the reopen: %+v %v %v", row, ok, err)
	}
	row, ok, err := d.AutoDoneTodo(ctx, "t1", "已合并 PR", autoTime, reopenTime.Add(time.Second))
	if err != nil || !ok || row.DoneAt == nil || !row.DoneAt.Equal(autoTime) || row.DoneBy != DoneByAuto || row.DoneReason != "已合并 PR" {
		t.Fatalf("auto done = %+v %v %v", row, ok, err)
	}
	if row, ok, err := d.AutoDoneTodo(ctx, "t1", "again", autoTime.Add(time.Minute), autoTime); err != nil || ok || row != nil {
		t.Fatalf("auto done twice: %+v %v %v", row, ok, err)
	}
	if _, _, err := d.AutoDoneTodo(ctx, "nope", "x", autoTime, autoTime); err != ErrTodoNotFound {
		t.Fatalf("AutoDoneTodo on a missing todo: %v", err)
	}
	reopen2 := autoTime.Add(time.Minute)
	up, err = d.UpdateTodo(ctx, "t1", nil, nil, &doneFalse, reopen2)
	if err != nil || up.DoneAt != nil || up.DoneBy != "" || up.DoneReason != "" || up.ReopenedAt == nil || !up.ReopenedAt.Equal(reopen2) {
		t.Fatalf("reopen after auto done = %+v %v", up, err)
	}
	if got, _ := d.GetTodo(ctx, "t1"); got.DoneBy != "" || got.ReopenedAt == nil {
		t.Fatalf("stored reopened todo = %+v", got)
	}

	// 4. LinkTodoAgent and transfer
	l1Time := now.Add(5 * time.Minute)
	_, err = d.LinkTodoAgent(ctx, "t1", "macbook", "s1", "测试 Agent", "/code/weather", "claude", "", l1Time)
	if err != nil {
		t.Fatalf("LinkTodoAgent: %v", err)
	}

	agents, err := d.ListAllTodoAgents(ctx)
	if err != nil {
		t.Fatalf("ListAllTodoAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].TodoID != "t1" || agents[0].ReleasedAt != nil {
		t.Fatalf("unexpected agents: %+v", agents)
	}

	// Move same agent to t2: t1's link should become released
	l2Time := now.Add(6 * time.Minute)
	released, err := d.LinkTodoAgent(ctx, "t2", "macbook", "s1", "测试 Agent", "/code/weather", "claude", "", l2Time)
	if err != nil {
		t.Fatalf("LinkTodoAgent move: %v", err)
	}
	if len(released) != 1 || released[0] != "t1" {
		t.Fatalf("expected released [t1], got %v", released)
	}

	agents, err = d.ListAllTodoAgents(ctx)
	if err != nil {
		t.Fatalf("ListAllTodoAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agent rows, got %d", len(agents))
	}
	var t1Link, t2Link *TodoAgentRow
	for i := range agents {
		if agents[i].TodoID == "t1" {
			t1Link = &agents[i]
		} else if agents[i].TodoID == "t2" {
			t2Link = &agents[i]
		}
	}
	if t1Link == nil || t1Link.ReleasedAt == nil {
		t.Fatalf("expected t1Link to be released, got %+v", t1Link)
	}
	if t2Link == nil || t2Link.ReleasedAt != nil {
		t.Fatalf("expected t2Link to be active, got %+v", t2Link)
	}

	// Re-link to t1: revives t1 link (released_at becomes nil), releases t2
	l3Time := now.Add(7 * time.Minute)
	released, err = d.LinkTodoAgent(ctx, "t1", "macbook", "s1", "测试 Agent", "/code/weather", "claude", "", l3Time)
	if err != nil {
		t.Fatalf("LinkTodoAgent revive: %v", err)
	}
	if len(released) != 1 || released[0] != "t2" {
		t.Fatalf("expected released [t2], got %v", released)
	}
	agents, err = d.ListAllTodoAgents(ctx)
	if err != nil {
		t.Fatalf("ListAllTodoAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected still 2 agent rows, got %d", len(agents))
	}

	// Cannot link to completed todo
	_, err = d.UpdateTodo(ctx, "t2", nil, nil, &doneTrue, now.Add(8*time.Minute))
	if err != nil {
		t.Fatalf("UpdateTodo done: %v", err)
	}
	_, err = d.LinkTodoAgent(ctx, "t2", "macbook", "s2", "Agent 2", "/code", "claude", "", now.Add(9*time.Minute))
	if err != ErrTodoCompleted {
		t.Fatalf("expected ErrTodoCompleted, got %v", err)
	}

	// UpdateTodoAgentSnapshot
	if err := d.UpdateTodoAgentSnapshot(ctx, "macbook", "s1", "更新后的 Agent 标题", "some start error"); err != nil {
		t.Fatalf("UpdateTodoAgentSnapshot: %v", err)
	}
	agents, _ = d.ListAllTodoAgents(ctx)
	for _, a := range agents {
		if a.TodoID == "t1" && a.SessionID == "s1" {
			if a.Title != "更新后的 Agent 标题" || a.StartError != "some start error" {
				t.Fatalf("snapshot not updated: %+v", a)
			}
		}
	}

	// UnlinkTodoAgent marks dismissed_at and released_at (soft unlink #762)
	if err := d.UnlinkTodoAgent(ctx, "t1", "macbook", "s1"); err != nil {
		t.Fatalf("UnlinkTodoAgent: %v", err)
	}
	agents, _ = d.ListAllTodoAgents(ctx)
	for _, a := range agents {
		if a.TodoID == "t1" && a.SessionID == "s1" {
			if a.DismissedAt == nil || a.ReleasedAt == nil {
				t.Fatalf("expected t1 s1 to have dismissed_at and released_at set, got %+v", a)
			}
		}
	}

	// DeleteTodo cascades to links
	_, _ = d.LinkTodoAgent(ctx, "t1", "macbook", "s3", "A3", "/cwd", "claude", "", now.Add(10*time.Minute))
	if err := d.DeleteTodo(ctx, "t1"); err != nil {
		t.Fatalf("DeleteTodo: %v", err)
	}
	if _, err := d.GetTodo(ctx, "t1"); err != ErrTodoNotFound {
		t.Fatalf("expected ErrTodoNotFound, got %v", err)
	}
	agents, _ = d.ListAllTodoAgents(ctx)
	for _, a := range agents {
		if a.TodoID == "t1" {
			t.Fatalf("expected links for t1 to be deleted via cascade")
		}
	}
}

func TestTodoSpaces(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Millisecond)

	_ = d.CreateTodo(ctx, TodoRow{ID: "td1", Title: "Todo 1", CreatedAt: now, UpdatedAt: now})
	_ = d.CreateTodo(ctx, TodoRow{ID: "td2", Title: "Todo 2", CreatedAt: now, UpdatedAt: now})

	// 1. Link td1 to space "proj1"
	oldID, err := d.LinkTodoSpace(ctx, "td1", "macbook", "proj1", now)
	if err != nil {
		t.Fatalf("LinkTodoSpace: %v", err)
	}
	if oldID != "" {
		t.Fatalf("expected empty oldID, got %q", oldID)
	}
	spaces, err := d.ListAllTodoSpaces(ctx)
	if err != nil || len(spaces) != 1 || spaces[0].TodoID != "td1" || spaces[0].Space != "proj1" {
		t.Fatalf("unexpected spaces: %+v, err: %v", spaces, err)
	}

	// Add space agent to td1 and manual agent to td1
	_, _ = d.LinkTodoAgentWithSource(ctx, "td1", "macbook", "s_space", "S1", "/cwd", "claude", "", "space", "macbook/proj1", now)
	_, _ = d.LinkTodoAgentWithSource(ctx, "td1", "macbook", "s_manual", "S2", "/cwd", "claude", "", "manual", "", now)

	// 2. Transfer "proj1" to td2
	oldID, err = d.LinkTodoSpace(ctx, "td2", "macbook", "proj1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("LinkTodoSpace transfer: %v", err)
	}
	if oldID != "td1" {
		t.Fatalf("expected oldID td1, got %q", oldID)
	}
	// td1's space agent should be deleted, manual agent remains
	agents, _ := d.ListAllTodoAgents(ctx)
	for _, a := range agents {
		if a.TodoID == "td1" && a.SessionID == "s_space" {
			t.Fatalf("expected td1 s_space to be deleted on transfer")
		}
		if a.TodoID == "td1" && a.SessionID == "s_manual" {
			// OK
		}
	}

	// 3. Rename space via hub
	voided, err := d.RenameTodoSpace(ctx, "macbook", "proj1", "proj_renamed")
	if err != nil || voided {
		t.Fatalf("RenameTodoSpace: voided=%v, err=%v", voided, err)
	}
	spaces, _ = d.ListAllTodoSpaces(ctx)
	if len(spaces) != 1 || spaces[0].Space != "proj_renamed" {
		t.Fatalf("expected renamed space, got %+v", spaces)
	}

	// 4. Unlink space
	if err := d.UnlinkTodoSpace(ctx, "td2", "macbook", "proj_renamed"); err != nil {
		t.Fatalf("UnlinkTodoSpace: %v", err)
	}
	spaces, _ = d.ListAllTodoSpaces(ctx)
	if len(spaces) != 0 {
		t.Fatalf("expected 0 spaces, got %+v", spaces)
	}
}

func TestAutoLinkTodoAgentDoesNotOverwriteDismissed(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Millisecond)

	_ = d.CreateTodo(ctx, TodoRow{ID: "t_race", Title: "Race Todo", CreatedAt: now, UpdatedAt: now})

	// 1. User unlinks first (e.g. before async auto-link arrives in DB) -> creates dismissed row
	if err := d.UnlinkTodoAgent(ctx, "t_race", "macbook", "s_fast"); err != nil {
		t.Fatalf("UnlinkTodoAgent: %v", err)
	}

	agents, err := d.ListAllTodoAgents(ctx)
	if err != nil || len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %+v (err: %v)", agents, err)
	}
	if agents[0].DismissedAt == nil || agents[0].ReleasedAt == nil {
		t.Fatalf("expected dismissed and released timestamps, got %+v", agents[0])
	}
	originalDismissedAt := *agents[0].DismissedAt

	// 2. Later, async auto-link arrives -> AutoLinkTodoAgent with ON CONFLICT DO NOTHING
	inserted, _, err := d.AutoLinkTodoAgent(ctx, "t_race", "macbook", "s_fast", "Title", "/cwd", "claude", "", "space", "macbook/work", now)
	if err != nil {
		t.Fatalf("AutoLinkTodoAgent: %v", err)
	}
	if inserted {
		t.Fatalf("expected inserted=false because row already existed as dismissed")
	}

	// 3. Verify row was NOT overwritten into active
	agents, err = d.ListAllTodoAgents(ctx)
	if err != nil || len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %+v", agents)
	}
	if agents[0].DismissedAt == nil || !agents[0].DismissedAt.Equal(originalDismissedAt) {
		t.Fatalf("dismissed_at was altered or wiped: %+v", agents[0])
	}
	if agents[0].ReleasedAt == nil {
		t.Fatalf("released_at was cleared: %+v", agents[0])
	}
}

func TestRenameTodoSpaceConflictVoidsOldLink(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Millisecond)

	_ = d.CreateTodo(ctx, TodoRow{ID: "t1", Title: "Todo 1", CreatedAt: now, UpdatedAt: now})
	_ = d.CreateTodo(ctx, TodoRow{ID: "t2", Title: "Todo 2", CreatedAt: now, UpdatedAt: now})

	// Link t1 to space "alpha", t2 to space "beta"
	_, _ = d.LinkTodoSpace(ctx, "t1", "macbook", "alpha", now)
	_, _ = d.LinkTodoSpace(ctx, "t2", "macbook", "beta", now)

	// Add space-sourced agent to t1 for "alpha"
	_, _ = d.LinkTodoAgentWithSource(ctx, "t1", "macbook", "s_alpha", "Alpha Agent", "/cwd", "claude", "", "space", "macbook/alpha", now)

	// Rename "alpha" to "beta", which conflicts with t2's existing link on "beta"
	voided, err := d.RenameTodoSpace(ctx, "macbook", "alpha", "beta")
	if err != nil {
		t.Fatalf("RenameTodoSpace: %v", err)
	}
	if !voided {
		t.Fatalf("expected voided=true due to unique constraint conflict on beta")
	}

	// Verify "alpha" is deleted from todo_spaces and t1's space agent is deleted
	spaces, _ := d.ListAllTodoSpaces(ctx)
	for _, sp := range spaces {
		if sp.Space == "alpha" {
			t.Fatalf("expected alpha space to be removed, got %+v", spaces)
		}
	}
	agents, _ := d.ListAllTodoAgents(ctx)
	for _, a := range agents {
		if a.TodoID == "t1" && a.SessionID == "s_alpha" {
			t.Fatalf("expected s_alpha to be removed after voiding, got %+v", a)
		}
	}
}

// An agent linked to a todo the user already finished stays linked (#2954): that is what a task's start does when the
// user taps "Done" while the agent is still starting, so the task keeps the agent and the close loop retires it.
// Linking one to a done todo by hand stays refused.
func TestLinkTodoAgentOfDoneTask(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Millisecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "td1", Title: "Todo 1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := d.UpdateTodo(ctx, "td1", nil, nil, &done, now); err != nil {
		t.Fatalf("UpdateTodo done: %v", err)
	}
	if _, err := d.LinkTodoAgent(ctx, "td1", "macbook", "s1", "Agent 1", "/cwd", "claude", "", now); err != ErrTodoCompleted {
		t.Fatalf("expected ErrTodoCompleted linking by hand, got %v", err)
	}
	if _, err := d.LinkTodoAgentOfDoneTask(ctx, "td1", "macbook", "s1", "Agent 1", "/cwd", "claude", "", "manual", "", now); err != nil {
		t.Fatalf("LinkTodoAgentOfDoneTask: %v", err)
	}
	agents, err := d.ListAllTodoAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].TodoID != "td1" || agents[0].SessionID != "s1" || agents[0].ReleasedAt != nil {
		t.Fatalf("the done todo's agent: %+v", agents)
	}
	if row, err := d.GetTodo(ctx, "td1"); err != nil || row.DoneAt == nil || row.StartError != "" {
		t.Fatalf("the done todo changed: %+v %v", row, err)
	}
	if _, err := d.LinkTodoAgentOfDoneTask(ctx, "nope", "macbook", "s2", "Agent 2", "/cwd", "claude", "", "manual", "", now); err != ErrTodoNotFound {
		t.Fatalf("expected ErrTodoNotFound for a missing todo, got %v", err)
	}
}
