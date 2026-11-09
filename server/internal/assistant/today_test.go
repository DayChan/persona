package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTodayCardListsPendingReviewBeforeAgentState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     string
		turnError string
	}{
		{"followup", proto.StateIdle, ""},
		{"unread", proto.StateUnread, ""},
		{"working", proto.StateWorking, ""},
		{"needs_you", proto.StateNeedsYou, ""},
		{"interrupted", proto.StateIdle, "request failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", State: tc.state, TurnError: tc.turnError})
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.State = reviewPending
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.appendFeed(ctx, "conv1", feedReceipt, todo.ID, receiptBody{Title: todo.Title, State: "done"}); err != nil {
				t.Fatal(err)
			}
			it, made, err := s.ensureToday(ctx, "conv1")
			if err != nil || !made {
				t.Fatalf("today made=%v err=%v", made, err)
			}
			var body todayBody
			if err := json.Unmarshal(it.Body, &body); err != nil {
				t.Fatal(err)
			}
			if want := []todayTask{{ID: todo.ID, Title: todo.Title}}; !reflect.DeepEqual(body.PendingReview, want) {
				t.Fatalf("pending review = %+v, want %+v", body.PendingReview, want)
			}
			if len(body.Waiting) != 0 || body.Working != 0 || len(body.Done) != 0 {
				t.Fatalf("pending review also counted in another group: %+v", body)
			}
		})
	}
}

func TestTodayCardPendingReviewScopeAndOrder(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	now := time.Now()
	var sessions []proto.Session
	var wantReview, wantWaiting []todayTask
	for i, tc := range []struct {
		title       string
		origin      string
		review      string
		agentState  string
		closed      bool
		wantReview  bool
		wantWaiting bool
	}{
		{"Older review", "conv1", reviewPending, "", false, true, false},
		{"Newer review", "conv1", reviewPending, "", false, true, false},
		{"Other conversation", "conv2", reviewPending, "", false, false, false},
		{"Manually closed", "conv1", reviewPending, "", true, false, false},
		{"Accepted", "conv1", reviewAccepted, "", true, false, false},
		{"Not supervised", "conv1", "", proto.StateUnread, false, false, false},
		{"Ordinary question", "conv1", "", proto.StateNeedsYou, false, false, true},
		{"Ordinary work", "conv1", "", proto.StateWorking, false, false, false},
		{"Awaiting a decision", "conv1", reviewNeedsUser, proto.StateNeedsYou, false, false, true},
	} {
		todo, err := s.createTodo(ctx, tc.title, "", tc.origin, "")
		if err != nil {
			t.Fatal(err)
		}
		if tc.review != "" {
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.Supervisor, v.State = tc.origin, tc.review
				return true
			}); err != nil {
				t.Fatal(err)
			}
		}
		// Review waits are not limited to the overnight window used for completed tasks.
		s.todos.mu.Lock()
		s.todos.todos[todo.ID].CreatedAt = now.Add(-48*time.Hour + time.Duration(i)*time.Minute)
		if tc.closed {
			s.todos.todos[todo.ID].DoneAt = &now
		}
		s.todos.mu.Unlock()
		if tc.agentState != "" {
			if err := s.linkTodoAgent(ctx, todo.ID, "box", todo.ID, tc.title, "/code/app", "claude"); err != nil {
				t.Fatal(err)
			}
			sessions = append(sessions, proto.Session{MachineID: "box", SessionID: todo.ID, State: tc.agentState})
		}
		if tc.wantReview {
			wantReview = append(wantReview, todayTask{ID: todo.ID, Title: tc.title})
		}
		if tc.wantWaiting {
			wantWaiting = append(wantWaiting, todayTask{ID: todo.ID, Title: tc.title})
		}
	}
	f.setSessions("box", sessions...)
	it, _, err := s.ensureToday(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	var body todayBody
	if err := json.Unmarshal(it.Body, &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body.PendingReview, wantReview) {
		t.Fatalf("pending review = %+v, want open tasks of this conversation, oldest first: %+v", body.PendingReview, wantReview)
	}
	if !reflect.DeepEqual(body.Waiting, wantWaiting) || body.Working != 1 {
		t.Fatalf("ordinary waiting / working tasks changed: %+v", body)
	}
}

func TestTodayCardOmitsEmptyPendingReview(t *testing.T) {
	s, _ := newTaskService(t)
	it, _, err := s.ensureToday(context.Background(), "conv1")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(it.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["pending_review"]; ok {
		t.Fatalf("empty pending_review must be omitted: %s", it.Body)
	}
}

// dayStart is the first instant of the user's day, also where the clocks jump forward at midnight: there time.Date
// answers 23:00 the evening before, and the Today card was filed under yesterday's date.
func TestDayStart(t *testing.T) {
	for _, tc := range []struct {
		name, zone, now, start string
	}{
		{"santiago spring forward at midnight", "America/Santiago", "2024-09-08 12:00", "2024-09-08T01:00:00-03:00"},
		{"santiago just after the jump", "America/Santiago", "2024-09-08 01:00", "2024-09-08T01:00:00-03:00"},
		{"santiago the evening before", "America/Santiago", "2024-09-07 23:59", "2024-09-07T00:00:00-04:00"},
		{"havana spring forward at midnight", "America/Havana", "2024-03-10 08:30", "2024-03-10T01:00:00-04:00"},
		{"asuncion spring forward at midnight", "America/Asuncion", "2023-10-01 20:00", "2023-10-01T01:00:00-03:00"},
		{"santiago normal day", "America/Santiago", "2024-07-01 09:00", "2024-07-01T00:00:00-04:00"},
		{"santiago fall back, the day before", "America/Santiago", "2024-04-06 22:00", "2024-04-06T00:00:00-03:00"},
		{"santiago fall back, the day after", "America/Santiago", "2024-04-07 09:00", "2024-04-07T00:00:00-04:00"},
		{"new york spring forward at 02:00", "America/New_York", "2024-03-10 12:00", "2024-03-10T00:00:00-05:00"},
		{"utc", "UTC", "2024-09-08 12:00", "2024-09-08T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Skipf("no zone data for %s: %v", tc.zone, err)
			}
			now, err := time.ParseInLocation("2006-01-02 15:04", tc.now, loc)
			if err != nil {
				t.Fatal(err)
			}
			want, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			got := dayStart(now)
			date := now.Format("2006-01-02")
			if !got.Equal(want) || got.Location() != loc {
				t.Fatalf("dayStart(%s) = %s, want %s in %s", now, got, want.In(loc), loc)
			}
			if got.Format("2006-01-02") != date || got.Add(-time.Nanosecond).Format("2006-01-02") == date {
				t.Fatalf("dayStart(%s) = %s is not the first instant of %s", now, got, date)
			}
		})
	}
}

// POST .../today reads the conversation's feed twice (is today's card there; what finished since yesterday 18:00) and
// looks only at the items: it loads none of the plans its plan cards name, and the card is the same as before.
func TestTodayCardReadsNoPlans(t *testing.T) {
	st := &countingPlans{DB: todoTestDB(t)}
	// planReads are the plan reads that reached the database, one plan at a time or in one batch.
	planReads := func() int64 { return st.one.Load() + st.batch.Load() }
	resetReads := func() {
		st.one.Store(0)
		st.batch.Store(0)
	}
	e := newTodoEnv(t, st)
	ctx := context.Background()
	loc := e.s.location(ctx)
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	// task_plans and assistant_feed are not truncated between tests, so the names are fresh.
	conversation := "today_" + randomHex(6)
	planCard := func(i int, at time.Time) {
		t.Helper()
		p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: conversation, State: planOpen, CreatedAt: at, UpdatedAt: at,
			Tasks: []PlanTask{{Title: fmt.Sprintf("Task %d", i), Prompt: "Fix the crash and add a test", Machine: "box", Cwd: "/code/app"}}}
		if err := e.s.savePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.appendFeedAt(ctx, conversation, feedPlan, p.ID, map[string]any{"plan": p}, at); err != nil {
			t.Fatal(err)
		}
	}
	const todayPlans = 20
	planCard(0, day.Add(-4*time.Hour)) // yesterday 20:00: only the second read has it
	if _, err := e.s.appendFeedAt(ctx, conversation, feedReceipt, "todo_night",
		receiptBody{Title: "Overnight fix", State: "done"}, day.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= todayPlans; i++ {
		planCard(i, now)
	}

	post := func() (FeedItem, bool) {
		t.Helper()
		var res struct {
			Item FeedItem `json:"item"`
			New  bool     `json:"new"`
		}
		if err := json.Unmarshal(e.must("POST", "/api/v1/assistant/conversations/"+conversation+"/today", "").Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res.Item, res.New
	}
	resetReads()
	it, made := post()
	t.Logf("plan reads with %d plan cards today and 1 overnight: %d on the first POST", todayPlans, planReads())
	if n := planReads(); n != 0 {
		t.Fatalf("the first POST read %d plans, want none", n)
	}
	if !made || it.Kind != feedToday || it.Ref != day.Format("2006-01-02") || !it.At.Equal(day) {
		t.Fatalf("today = %+v new=%v", it, made)
	}
	var body todayBody
	if err := json.Unmarshal(it.Body, &body); err != nil {
		t.Fatal(err)
	}
	want := todayBody{Date: it.Ref, Done: []todayTask{{ID: "todo_night", Title: "Overnight fix"}}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %+v, want %+v", body, want)
	}

	resetReads()
	again, made := post()
	t.Logf("plan reads on the second POST: %d", planReads())
	if n := planReads(); n != 0 {
		t.Fatalf("the second POST read %d plans, want none", n)
	}
	if made || again.ID != it.ID || !bytes.Equal(again.Body, it.Body) {
		t.Fatalf("second POST: %+v new=%v, want the first card %+v", again, made, it)
	}
}

// Concurrent opens of a chat (two devices or tabs, the Lark workbench) make one "Today" card for the day: the first
// caller makes it and the others get that card, not one each.
func TestTodayCardConcurrentOpensMakeOne(t *testing.T) {
	s, f := newTaskService(t)
	// The connectors answer late, so each caller spends a while building the card after looking for one: without the
	// lock every caller would find none and append its own.
	f.slowSnapshots = 20 * time.Millisecond
	ctx := context.Background()
	const callers = 8
	type result struct {
		it   FeedItem
		made bool
		err  error
	}
	results := make([]result, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			it, made, err := s.ensureToday(ctx, "conv1")
			results[i] = result{it, made, err}
		}()
	}
	close(start)
	wg.Wait()

	made := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("caller %d: %v", i, r.err)
		}
		if r.made {
			made++
		}
	}
	items, err := s.feedItems(ctx, "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	cards := 0
	for _, it := range items {
		if it.Kind == feedToday {
			cards++
		}
	}
	if cards != 1 {
		t.Fatalf("%d Today cards for one day, want 1", cards)
	}
	if made != 1 {
		t.Fatalf("%d callers made the card, want 1", made)
	}
	for i, r := range results {
		if r.it.ID != results[0].it.ID {
			t.Fatalf("caller %d got card %s, caller 0 got %s", i, r.it.ID, results[0].it.ID)
		}
	}
	if n := s.tasks.todayLocks.len(); n != 0 {
		t.Fatalf("%d conversation locks kept after the opens, want none", n)
	}
}
