package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type originReadBarrierStore struct {
	*db.DB
	once          sync.Once
	read, proceed chan struct{}
}

func (st *originReadBarrierStore) ReadOrigins(ctx context.Context) (json.RawMessage, int64, bool, error) {
	raw, rev, found, err := st.DB.ReadOrigins(ctx)
	st.once.Do(func() { close(st.read); <-st.proceed })
	return raw, rev, found, err
}

func TestOriginsPostgresInterleavedWriters(t *testing.T) {
	st := scheduleStore(t)
	if err := st.PutSetting(t.Context(), originsKey, map[string]*convOrigin{}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	barrier := &originReadBarrierStore{DB: st, read: make(chan struct{}), proceed: make(chan struct{})}
	first, second := newServiceOn(t, f, barrier), newServiceOn(t, f, st)
	result := make(chan error, 1)
	go func() {
		result <- first.recordRetryOrigin(t.Context(), "server", "first-session", OriginTask, "first-task")
	}()
	awaitBusSignal(t, barrier.read)
	second.originSet(t.Context(), "server", "second-session", OriginInbox, "second-item")
	close(barrier.proceed)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	var got map[string]*convOrigin
	if _, err := st.Setting(t.Context(), originsKey, &got); err != nil {
		t.Fatal(err)
	}
	if got["first-session"] == nil || got["second-session"] == nil {
		t.Fatalf("interleaved writes lost an origin: %+v", got)
	}
}

type originRefreshFaultStore struct {
	*db.DB
	fail bool
}

func (st *originRefreshFaultStore) ReadOrigins(ctx context.Context) (json.RawMessage, int64, bool, error) {
	if st.fail {
		return nil, 0, false, errors.New("origin refresh unavailable")
	}
	return st.DB.ReadOrigins(ctx)
}

func TestOriginsPostgresAuthorizationMissRefreshes(t *testing.T) {
	st := scheduleStore(t)
	if err := st.PutSetting(t.Context(), originsKey, map[string]*convOrigin{}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	f.hello.Assistant.Machines = []string{"server", "other"}
	fault := &originRefreshFaultStore{DB: st}
	first, second, discovery := newServiceOn(t, f, fault), newServiceOn(t, f, st), newServiceOn(t, f, st)
	setSettings(t, first, func(v *Settings) { v.MachineID = "other" })
	for _, s := range []*Service{first, discovery} {
		s.origins.mu.Lock()
		loaded := s.originsLocked(t.Context())
		s.origins.mu.Unlock()
		if !loaded {
			t.Fatal("manager could not load its initial origins")
		}
	}
	second.originSet(t.Context(), "server", "new-session", OriginTask, "task")
	fault.fail = true
	if first.assistantCaller(t.Context(), "server", "new-session") || first.assistantDiscovery(t.Context(), "server", "") {
		t.Fatal("failed refresh granted authorization")
	}
	fault.fail = false
	if !first.assistantCaller(t.Context(), "server", "new-session") {
		t.Fatal("authorization miss did not refresh the other manager's origin")
	}
	if !discovery.assistantDiscovery(t.Context(), "server", "") {
		t.Fatal("discovery miss did not refresh the other manager's origin")
	}
	if first.assistantCaller(t.Context(), "wrong-machine", "new-session") {
		t.Fatal("origin authorized another machine")
	}
}

func TestOriginsPostgresBackfillCannotOverwriteStart(t *testing.T) {
	st := scheduleStore(t)
	if err := st.PutSetting(t.Context(), originsKey, map[string]*convOrigin{}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	barrier := &originReadBarrierStore{DB: st, read: make(chan struct{}), proceed: make(chan struct{})}
	first, second := newServiceOn(t, f, barrier), newServiceOn(t, f, st)
	result := make(chan error, 1)
	go func() {
		_, err := first.backfillOrigins(t.Context(), "server", []proto.Session{{SessionID: "same-session", MachineID: "server", Assistant: true, Title: "⏰ example"}}, false)
		result <- err
	}()
	awaitBusSignal(t, barrier.read)
	second.originSet(t.Context(), "server", "same-session", OriginUser, "")
	close(barrier.proceed)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	var got map[string]*convOrigin
	if _, err := st.Setting(t.Context(), originsKey, &got); err != nil {
		t.Fatal(err)
	}
	if o := got["same-session"]; o == nil || o.By != originByStart || o.Origin != OriginUser {
		t.Fatalf("backfill overwrote authoritative start: %+v", o)
	}
}

type originFailCASStore struct{ *db.DB }

func (st *originFailCASStore) CompareOrigins(context.Context, int64, json.RawMessage) (bool, error) {
	return false, errors.New("origin CAS unavailable")
}

func TestOriginsPostgresCASFailureDoesNotGrant(t *testing.T) {
	st := scheduleStore(t)
	if err := st.PutSetting(t.Context(), originsKey, map[string]*convOrigin{}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	f.hello.Assistant.Machines = []string{"server", "other"}
	s := newServiceOn(t, f, &originFailCASStore{DB: st})
	setSettings(t, s, func(v *Settings) { v.MachineID = "other" })
	if err := s.recordRetryOrigin(t.Context(), "server", "retry-session", OriginTask, "task"); err == nil {
		t.Fatal("CAS failure was ignored")
	}
	if s.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("failed CAS granted authorization")
	}
	var got map[string]*convOrigin
	if _, err := st.Setting(t.Context(), originsKey, &got); err != nil {
		t.Fatal(err)
	}
	if got["retry-session"] != nil {
		t.Fatal("failed CAS changed durable origins")
	}
}

// TestOriginsPostgresManagersMerge uses two independently cached managers on one disposable database.
func TestOriginsPostgresManagersMerge(t *testing.T) {
	st := scheduleStore(t)
	if err := st.PutSetting(t.Context(), originsKey, map[string]*convOrigin{}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	first, second := newServiceOn(t, f, st), newServiceOn(t, f, st)
	for _, s := range []*Service{first, second} {
		s.origins.mu.Lock()
		ok := s.originsLocked(t.Context())
		s.origins.mu.Unlock()
		if !ok {
			t.Fatal("load origins")
		}
	}
	first.originSet(t.Context(), "server", "first-session", OriginTask, "first-task")
	second.originSet(t.Context(), "server", "second-session", OriginInbox, "second-item")
	var got map[string]*convOrigin
	if _, err := st.Setting(t.Context(), originsKey, &got); err != nil {
		t.Fatal(err)
	}
	if got["first-session"] == nil || got["second-session"] == nil {
		t.Fatalf("a manager overwrote the other's origin: %+v", got)
	}
}

type originContextStore struct{ *memStore }

func (st *originContextStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return st.memStore.Setting(ctx, key, out)
}

func (st *originContextStore) PutSetting(ctx context.Context, key string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return st.memStore.PutSetting(ctx, key, value)
}

func TestOriginStartPersistsAfterRequestCancellation(t *testing.T) {
	st := &originContextStore{memStore: &memStore{}}
	f := readyHub()
	s := newServiceOn(t, f, st)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the node already accepted the conversation before the client disconnected
	s.originSet(ctx, "server", "accepted-session", OriginTask, "task")
	var saved map[string]*convOrigin
	if _, err := st.Setting(t.Context(), originsKey, &saved); err != nil {
		t.Fatal(err)
	}
	if o := saved["accepted-session"]; o == nil || o.By != originByStart || o.MachineID != "server" {
		t.Fatalf("accepted start lost its origin: %+v", o)
	}
}

// originOf is a conversation's origin and ref in the last snapshot the hub got.
func originOf(f *fakeHub, id string) (string, string) {
	for _, v := range f.lastSnapshot().Sessions {
		if v.SessionID == id {
			return v.Origin, v.OriginRef
		}
	}
	return "", ""
}

func titleIn(f *fakeHub, id string) string {
	for _, v := range f.lastSnapshot().Sessions {
		if v.SessionID == id {
			return v.Title
		}
	}
	return ""
}

// Every way the hub opens a conversation notes where it comes from (#3264): the app's own and a channel's /new are
// the user's, the rest are the hub's.
func TestOriginNotedAtStart(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Timezone: "Asia/Shanghai"}
	s := newTestService(t, f)

	app, err := s.Start(ctx, StartParams{Text: "帮我看看明天的会"})
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := originOf(f, app.SessionID); o != OriginUser || app.Origin != OriginUser {
		t.Fatalf("the app's conversation: %q / %q", o, app.Origin)
	}
	ch, err := s.Start(ctx, StartParams{Text: "/new 你好", Origin: OriginChannel, OriginRef: "wechat"})
	if err != nil {
		t.Fatal(err)
	}
	if o, ref := originOf(f, ch.SessionID); o != OriginChannel || ref != "wechat" {
		t.Fatalf("a channel's /new: %q %q", o, ref)
	}
	main, err := s.Start(ctx, StartParams{Text: "早", Main: true, Origin: OriginChannel, OriginRef: "feishu"})
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := originOf(f, main.SessionID); o != OriginUser {
		t.Fatalf("the main conversation is the user's whatever opened it: %q", o)
	}

	// conversation_new: spawned, of the conversation that asked.
	res := s.Tool(ctx, ToolCall{MachineID: "server", SessionID: app.SessionID, Name: "conversation_new", Arguments: json.RawMessage(`{"text":"去查一下"}`)})
	if res.IsError {
		t.Fatalf("conversation_new: %+v", res)
	}
	spawned := f.lastSend(t)
	var spawnedID string
	for _, v := range f.lastSnapshot().Sessions {
		if v.Origin == OriginSpawned {
			spawnedID = v.SessionID
			if v.OriginRef != app.SessionID {
				t.Fatalf("spawned of %q", v.OriginRef)
			}
		}
	}
	if spawnedID == "" || !strings.Contains(spawned["text"].(string), "去查一下") {
		t.Fatalf("no spawned conversation: %+v", f.lastSnapshot().Sessions)
	}

	// A scheduled task: task, of the task, titled after it at once.
	setTasks(s, Task{ID: "r", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatDaily, Time: "09:00", Enabled: true})
	run, err := runNow(t, s, "r")
	if err != nil {
		t.Fatal(err)
	}
	if o, ref := originOf(f, run.SessionID); o != OriginTask || ref != "r" {
		t.Fatalf("a task's conversation: %q %q", o, ref)
	}
	if got := titleIn(f, run.SessionID); !strings.HasSuffix(got, " | 定时 | 早报") {
		t.Fatalf("a task's title: %q", got)
	}
}

// A plugin's request opens an extension's conversation; on lark.agent's model it is a Feishu /agent. An app's
// request is the user's whatever header it sends.
func TestOriginOfARequest(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server"}
	s := newTestService(t, f)
	post := func(role, ext, purpose string) string {
		t.Helper()
		body := `{"prompt":"看看这个群在聊什么"`
		if purpose != "" {
			body += `,"purpose":"` + purpose + `"`
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(body+"}"))
		if role != "" {
			r.Header.Set(RoleHeader, role)
		}
		if ext != "" {
			r.Header.Set(ExtensionHeader, ext)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var out struct {
			Session proto.Session `json:"session"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Session.SessionID
	}
	for _, c := range []struct{ role, ext, purpose, origin, ref string }{
		{"", "", "", OriginUser, ""},
		{"full", "larkext", "", OriginUser, ""},
		{"extension", "larkext", "lark.agent", OriginLarkAgent, "larkext"},
		{"extension", "github", "", OriginExtension, "github"},
	} {
		id := post(c.role, c.ext, c.purpose)
		if o, ref := originOf(f, id); o != c.origin || ref != c.ref {
			t.Errorf("%+v: %q %q", c, o, ref)
		}
	}
}

// The AI twin's conversation is the inbox's, of its item, and named after the item from the start instead of its
// prompt's first line (the sidebar's "This is an item from the inbox; as…").
func TestInboxAgentOriginAndTitle(t *testing.T) {
	h := agentHub(t)
	it := actionItem("lark:f1:11", time.Now())
	it.Title = "张三问 1013 合了吗"
	it.Agent = &InboxAgent{State: AgentQueued, Task: "查 1013 是否合入"}
	h.inbox.items[it.ID] = it
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
	id := it.Agent.SessionID
	if o, ref := originOf(h.f, id); o != OriginInbox || ref != it.ID {
		t.Fatalf("origin %q %q", o, ref)
	}
	if got := titleIn(h.f, id); !strings.HasSuffix(got, " | 收件箱 | 张三问 1013 合了吗") {
		t.Fatalf("title %q", got)
	}
}

func TestGuessOrigin(t *testing.T) {
	relay := prompts.RenderRelay(prompts.Relay{FromID: "abcd-1234", FromTitle: "1105 | 研究 | 竞品", Tool: "conversation_new", Hop: 1, MaxHops: 3, Text: "去查"})
	ev := originEvidence{inbox: map[string]*InboxItem{"in": {ID: "mail:1"}}, tasks: map[string]string{"run": "morning"}}
	for _, c := range []struct {
		id, title, by, origin, ref string
	}{
		{"in", "随便什么", "", OriginInbox, "mail:1"},
		{"run", "随便什么", "", OriginTask, "morning"},
		{"x", "1101 | 定时 | 早报", titleByTask, OriginTask, ""},
		{"x", "1101 | 收件箱 | 张三", titleByInbox, OriginInbox, ""},
		{"x", "这是收件箱里的一件事，你作为我的 AI 分身先处理它…", "", OriginInbox, ""},
		{"x", "⏰ 早报（定时任务）", "", OriginTask, ""},
		{"x", relay, titleAuto, OriginSpawned, "abcd-1234"},
		{"x", `<persona-context kind="instructions" title="给 Agent 的说明">` + "\n我在飞书「群」里用 /agent 叫你。", titleAuto, OriginLarkAgent, ""},
		{"x", "帮我看看明天的会", titleAuto, OriginUser, ""},
		{"x", "", "", OriginUser, ""},
	} {
		if o, ref := guessOrigin(c.id, c.title, c.by, ev); o != c.origin || ref != c.ref {
			t.Errorf("guessOrigin(%q, %q, %q) = %q %q", c.id, c.title, c.by, o, ref)
		}
	}
}

// The backfill fills what has no origin yet from what the hub still knows, never touches one the start noted, and
// can run again: nothing changes the second time, and a redo never takes one back to the user's.
func TestBackfillOrigins(t *testing.T) {
	ctx := context.Background()
	h := agentHub(t)
	f := h.f
	setSettings(t, h.Service, func(v *Settings) { v.MachineID, v.Main = "server", "main" })
	dir := "/home/me/.persona/assistant"
	it := actionItem("mail:1", time.Now())
	it.Title, it.Agent = "李四的周报", &InboxAgent{State: AgentDrafted, SessionID: "old-inbox"}
	h.inbox.items[it.ID] = it
	setTasks(h.Service, Task{ID: "r", Title: "早报", Repeat: repeatDaily, Time: "09:00", Enabled: true, RunSessionID: "old-task"})

	started, err := h.Start(ctx, StartParams{Text: "x", Origin: OriginExtension, OriginRef: "github"})
	if err != nil {
		t.Fatal(err)
	}
	relay := prompts.RenderRelay(prompts.Relay{FromID: "main", FromTitle: "主对话", Tool: "conversation_new", Hop: 1, MaxHops: 3, Text: "去查"})
	h.titleNew("server", "old-spawned", time.Now())
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: relay})
	list := []proto.Session{
		{SessionID: "main", Cwd: dir, Title: "早"},
		{SessionID: "old-inbox", Cwd: dir, Title: "这是收件箱里的一件事，你作为我的 AI 分身先处理它…"},
		{SessionID: "old-inbox2", Cwd: dir, Title: "这是收件箱里的一件事，你作为我的 AI 分身先处理它…"},
		{SessionID: "old-task", Cwd: dir, Title: "⏰ 早报"},
		{SessionID: "old-spawned", Cwd: dir, Title: "去查"},
		{SessionID: "old-user", Cwd: dir, Title: "帮我写周报"},
		{SessionID: started.SessionID, Cwd: dir, Title: "x"},
		{SessionID: "agent", Cwd: "/home/me/code/app", PaneID: "w1:p1", Title: "这是收件箱里的一件事"}, // not an assistant conversation
	}
	res, err := h.backfillOrigins(ctx, "server", list, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"main": {OriginUser, ""}, "old-inbox": {OriginInbox, "mail:1"}, "old-inbox2": {OriginInbox, ""}, "old-task": {OriginTask, "r"},
		"old-spawned": {OriginSpawned, "main"}, "old-user": {OriginUser, ""}, started.SessionID: {OriginExtension, "github"},
	}
	for id, w := range want {
		if o, ref := originOf(f, id); o != w[0] || ref != w[1] {
			t.Errorf("%s: %q %q, want %v", id, o, ref, w)
		}
	}
	if o, _ := originOf(f, "agent"); o != "" {
		t.Errorf("an agent got an origin: %q", o)
	}
	if res.Checked != 6 || res.Filled[OriginInbox] != 2 || res.Filled[OriginUser] != 2 {
		t.Errorf("result %+v", res)
	}
	if got := titleIn(f, "old-inbox"); !strings.HasSuffix(got, " | 收件箱 | 李四的周报") {
		t.Errorf("the AI 分身's old conversation is named after its item: %q", got)
	}

	again, err := h.backfillOrigins(ctx, "server", list, false)
	if err != nil || again.Checked != 0 || len(again.Filled) != 0 {
		t.Fatalf("a second run: %+v %v", again, err)
	}
	// A redo over the hub's decorated list (titles over the first messages, the item forgotten) keeps what it found.
	delete(h.inbox.items, it.ID)
	f.setItems()
	decorated := []proto.Session{{SessionID: "old-spawned", Cwd: dir, Assistant: true, Title: "1105 | 研究 | 竞品"},
		{SessionID: "old-inbox", Cwd: dir, Assistant: true, Title: "1105 | 收件箱 | 李四的周报"}}
	if _, err := h.backfillOrigins(ctx, "server", decorated, true); err != nil {
		t.Fatal(err)
	}
	if o, _ := originOf(f, "old-spawned"); o != OriginSpawned {
		t.Errorf("redo took it back: %q", o)
	}
	if o, _ := originOf(f, "old-inbox"); o != OriginInbox {
		t.Errorf("redo: %q", o)
	}
}

// The route runs the backfill on the assistant's machine's list, for a full device only.
func TestBackfillRoute(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server"}
	f.sessions["server"] = []proto.Session{{SessionID: "old", Cwd: "/home/me/.persona/assistant", Title: "⏰ 早报"}}
	s := newTestService(t, f)
	call := func(role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/origins/backfill", strings.NewReader(`{}`))
		if role != "" {
			r.Header.Set(RoleHeader, role)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := call("extension"); w.Code != http.StatusForbidden {
		t.Fatalf("an extension ran it: %d", w.Code)
	}
	w := call("")
	var res BackfillResult
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != http.StatusOK || res.Filled[OriginTask] != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if o, _ := originOf(f, "old"); o != OriginTask {
		t.Fatalf("origin %q", o)
	}
}

// The lists the manager decorates for the apps carry the origin of the assistant's conversations (fleet_sessions.go),
// and a new origin is a change of the machine's list.
func TestFleetListsOrigins(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.d.add("server", "服务器")
	ft.d.setSessions("server", []proto.Session{
		{SessionID: "conv-0001", Cwd: "/home/me/.persona/assistant", Title: "这是收件箱里的一件事", Status: proto.StatusDone},
		{SessionID: "agent-0001", Cwd: "/home/me/code/app", PaneID: "w1:p1", Title: "修登录", Status: proto.StatusDone},
	})
	view := SessionView{MachineID: "server", SessionID: "conv-0001", Origin: OriginInbox, OriginRef: "mail:1"}
	s.fleetTakeSnapshot(Snapshot{Sessions: []SessionView{view}})
	if c := listed(t, s, "server", "conv-0001"); c.Origin != OriginInbox || c.OriginRef != "mail:1" {
		t.Fatalf("the conversation: %q %q", c.Origin, c.OriginRef)
	}
	if c := listed(t, s, "server", "agent-0001"); c.Origin != "" {
		t.Fatalf("an agent has no origin: %q", c.Origin)
	}
	old := map[string]SessionView{"conv-0001": {MachineID: "server", SessionID: "conv-0001"}}
	if got := changedMachines(old, map[string]SessionView{"conv-0001": view}); len(got) != 1 || got[0] != "server" {
		t.Fatalf("changed: %v", got)
	}
}

// On Postgres (the test database): the backfill's origins are saved, read back by an assistant started afresh (a
// restart), and running it again there changes nothing.
func TestBackfillOriginsOnPostgres(t *testing.T) {
	store := scheduleStore(t)
	ctx := context.Background()
	if err := store.PutSetting(ctx, originsKey, nil); err != nil {
		t.Fatal(err)
	}
	dir := "/home/me/.persona/assistant"
	list := []proto.Session{
		{SessionID: "pg-inbox", Cwd: dir, Title: "这是收件箱里的一件事，你作为我的 AI 分身先处理它…"},
		{SessionID: "pg-task", Cwd: dir, Title: "⏰ 早报（定时任务）"},
		{SessionID: "pg-user", Cwd: dir, Title: "帮我写周报"},
	}
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server"}
	s := newServiceOn(t, f, store)
	res, err := s.backfillOrigins(ctx, "server", list, false)
	if err != nil || res.Checked != 3 || res.Filled[OriginInbox] != 1 || res.Filled[OriginTask] != 1 || res.Filled[OriginUser] != 1 {
		t.Fatalf("first run: %+v %v", res, err)
	}

	var saved map[string]*convOrigin
	if _, err := store.Setting(ctx, originsKey, &saved); err != nil || saved["pg-inbox"] == nil || saved["pg-inbox"].Origin != OriginInbox || saved["pg-inbox"].By != originByBackfill {
		t.Fatalf("saved: %+v %v", saved, err)
	}

	f2 := newFakeHub().withMachine()
	f2.settings = &Settings{MachineID: "server"}
	again := newServiceOn(t, f2, store)
	res, err = again.backfillOrigins(ctx, "server", list, false)
	if err != nil || res.Checked != 0 || len(res.Filled) != 0 {
		t.Fatalf("after a restart: %+v %v", res, err)
	}
	res, err = again.backfillOrigins(ctx, "server", list, true) // a redo finds the same
	if err != nil || len(res.Filled) != 0 {
		t.Fatalf("a redo: %+v %v", res, err)
	}
	again.publish(ctx)
	if o, _ := originOf(f2, "pg-task"); o != OriginTask {
		t.Fatalf("read back: %q", o)
	}
}
