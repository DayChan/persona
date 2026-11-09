package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// kbStore is an in-memory store with the knowledge base's index, whose settings can stop being readable (the
// database down).
type kbStore struct {
	*memStore
	mu      sync.Mutex
	rows    map[string]string // "kind/id" → name
	down    bool
	deleted []string
}

func newKBStore() *kbStore { return &kbStore{memStore: &memStore{}, rows: map[string]string{}} }

func (k *kbStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	k.mu.Lock()
	down := k.down
	k.mu.Unlock()
	if down {
		return false, errors.New("database down")
	}
	return k.memStore.Setting(ctx, key, out)
}

func (k *kbStore) PutKBEntity(_ context.Context, kind, id, name, _ string, _ int, _ []string, _ string, _ time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rows[kind+"/"+id] = name
	return nil
}

func (k *kbStore) DeleteKBEntities(_ context.Context, kind string, ids []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, id := range ids {
		delete(k.rows, kind+"/"+id)
		k.deleted = append(k.deleted, kind+"/"+id)
	}
	return nil
}

// kbServiceOn is an assistant on f keeping its knowledge base in dir, with st as its database.
func kbServiceOn(t *testing.T, f *fakeHub, st Store, dir string, logf func(string, ...any)) *Service {
	t.Helper()
	if logf == nil {
		logf = t.Logf
	}
	f.mu.Lock()
	if f.hello.KB == nil {
		f.hello.KB = &KBSettings{}
	}
	f.mu.Unlock()
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: logf, KB: &KBOptions{Dir: dir}})
	t.Cleanup(s.kbClose)
	return s
}

// What the hub asks of the knowledge base: a chat's context (the inbox's AI twin), a directory's projects (the
// triage), deleting a source's chats and people with their index rows; anything else is not the knowledge base's.
func TestKBAnswersTheHub(t *testing.T) {
	st := newKBStore()
	s := kbServiceOn(t, newFakeHub(), st, t.TempDir(), nil)
	e := kbEng(t, s)
	ctx := context.Background()
	c := &kb.Chat{ID: "lark-f1", Source: "lark", SourceID: "f1", Kind: "group", Name: "发布群"}
	_ = e.Store.PutChat(c)
	_ = e.Store.PutSummary(kb.KindChat, c.ID, "在推发布")
	if _, err := e.Apply(kb.KindProject, "Persona 发布", kb.Update{Chats: []string{"lark-f1"}, Dirs: []string{"server:/code/persona"}}); err != nil {
		t.Fatal(err)
	}
	ask := func(method string, params any) any {
		t.Helper()
		raw, _ := json.Marshal(params)
		out, ok, err := s.kbRequest(ctx, method, raw)
		if !ok || err != nil {
			t.Fatalf("%s: %v %v", method, ok, err)
		}
		b, _ := json.Marshal(out)
		var v any
		_ = json.Unmarshal(b, &v)
		return v
	}
	got := fmt.Sprint(ask(assistantwire.MethodKBContext, map[string]any{"chat_id": "lark-f1"}))
	if !strings.Contains(got, "ChatName:发布群") || !strings.Contains(got, "在推发布") || !strings.Contains(got, "Name:Persona 发布") {
		t.Fatalf("context: %s", got)
	}
	if got := ask(assistantwire.MethodKBContext, map[string]any{"chat_id": "lark-nope"}); fmt.Sprint(got) != "map[context:<nil>]" {
		t.Fatalf("context of an unknown chat: %v", got)
	}
	if got := fmt.Sprint(ask(assistantwire.MethodKBProjects, map[string]any{"machine_id": "server", "cwd": "/code/persona/ios"})); got != "map[projects:[map[name:Persona 发布 slug:persona-发布]]]" {
		t.Fatalf("projects: %s", got)
	}
	if got := fmt.Sprint(ask(assistantwire.MethodKBProjects, map[string]any{"machine_id": "laptop", "cwd": "/code/persona"})); got != "map[projects:[]]" {
		t.Fatalf("another machine's: %s", got)
	}

	_ = e.Store.PutChat(&kb.Chat{ID: "example-c1", Source: "example", Name: "示例群"})
	_ = e.Store.PutPerson(&kb.Person{ID: "example-p1", Source: "example", Name: "示例用户"})
	st.rows["chats/example-c1"], st.rows["people/example-p1"] = "示例群", "示例用户"
	if got := fmt.Sprint(ask(assistantwire.MethodKBForget, map[string]any{"source": "example"})); got != "map[chats:1 items:0 people:1 posts:0]" {
		t.Fatalf("forget: %s", got)
	}
	if _, ok := e.Store.Chat("example-c1"); ok {
		t.Fatal("the chat is still there")
	}
	if _, ok := e.Store.Chat("lark-f1"); !ok {
		t.Fatal("Feishu's chat is gone")
	}
	if _, ok := st.rows["chats/example-c1"]; ok || len(st.deleted) != 2 {
		t.Fatalf("index rows: %v, deleted %v", st.rows, st.deleted)
	}

	if _, ok, _ := s.kbRequest(ctx, "kb.nope", nil); ok {
		t.Fatal("an unknown method was taken")
	}
	var re *rpc.Error
	if _, ok, err := s.kbRequest(ctx, assistantwire.MethodKBForget, json.RawMessage(`[]`)); !ok || !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
		t.Fatalf("bad params: %v %v", ok, err)
	}
}

// Off, the knowledge base answers the hub with nothing (and the posts with why).
func TestKBOffAnswersTheHub(t *testing.T) {
	f := newFakeHub()
	f.hello.KB = &KBSettings{Off: true} // manager.json kb.enabled false
	s := New(Options{Hub: f, Logf: testLogf(t), KB: &KBOptions{Dir: t.TempDir()}})
	ctx := context.Background()
	out, _, err := s.kbRequest(ctx, assistantwire.MethodKBContext, json.RawMessage(`{"chat_id":"lark-f1"}`))
	if err != nil || out.(assistantwire.KBContextResult).Context != nil {
		t.Fatalf("context: %v %v", out, err)
	}
	if out, _, err := s.kbRequest(ctx, assistantwire.MethodKBForget, json.RawMessage(`{"source":"x"}`)); err != nil || out.(assistantwire.KBForgotten) != (assistantwire.KBForgotten{}) {
		t.Fatalf("forget: %v %v", out, err)
	}
	if _, _, err := s.kbRequest(ctx, assistantwire.MethodKBPostsPut, json.RawMessage(`{"connector":"feed"}`)); !errors.Is(err, errKBNone) {
		t.Fatalf("posts: %v", err)
	}
}

// An agent's finished turn (kb.agent_turn) goes into the timeline of the projects of its directory: once per agent
// and half hour, only that machine's; an agent without a title is named after its directory.
func TestKBAgentTurn(t *testing.T) {
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	if _, err := e.Apply(kb.KindProject, "Persona", kb.Update{Dirs: []string{"server:/code/persona"}}); err != nil {
		t.Fatal(err)
	}
	turn := func(t assistantwire.KBAgentTurn) {
		raw, _ := json.Marshal(t)
		if ok, err := s.kbEvent(context.Background(), assistantwire.EventKBAgentTurn, raw); !ok || err != nil {
			panic(fmt.Sprint(ok, err))
		}
	}
	turn(assistantwire.KBAgentTurn{MachineID: "server", SessionID: "s1", Cwd: "/code/persona/ios", Title: "修排序", Line: "改好了，\n测试通过"})
	turn(assistantwire.KBAgentTurn{MachineID: "server", SessionID: "s1", Cwd: "/code/persona/ios", Title: "修排序", Line: "又改了"})
	turn(assistantwire.KBAgentTurn{MachineID: "laptop", SessionID: "s2", Cwd: "/code/persona", Line: "x"})
	turn(assistantwire.KBAgentTurn{MachineID: "server", SessionID: "s3", Cwd: "/code/persona", Title: "Claude Code", Line: "完成：发好了"})
	log := e.Store.Log("persona", 10)
	if len(log) != 2 || !strings.Contains(log[0], "Agent · 修排序 · server · 改好了， 测试通过") || !strings.Contains(log[1], "Agent · persona · server · 完成：发好了") {
		t.Fatalf("log: %v", log)
	}
	if ok, _ := s.kbEvent(context.Background(), assistantwire.EventKBKick, nil); !ok || len(s.kb.kick) != 1 {
		t.Fatal("a kick did not wake the loop")
	}
	if ok, _ := s.kbEvent(context.Background(), "kb.nope", nil); ok {
		t.Fatal("an unknown event was taken")
	}
}

// The archive is put into the user's zone before anything reads it, once: the assistant restarting moves nothing
// again. While its settings cannot be read nothing is moved (into the machine's zone and back) and nothing archived.
func TestKBZoneAtStart(t *testing.T) {
	dir := t.TempDir()
	old, _ := kb.NewStore(dir)
	if _, err := old.Rezone(time.UTC); err != nil { // written in UTC (the hub's own zone, before)
		t.Fatal(err)
	}
	c := &kb.Chat{ID: "lark-f1", Source: "lark", SourceID: "f1", Name: "发布群"}
	_ = old.PutChat(c)
	_ = old.AppendMessages(c, []kb.Line{{At: time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC), Sender: "张三", Text: "晚上好"}})
	if _, err := (&kb.Engine{Store: old}).Apply(kb.KindProject, "Persona", kb.Update{Dirs: []string{"server:/code/persona"}}); err != nil {
		t.Fatal(err)
	}
	zoneOf := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "chats", "lark-f1", "messages", ".zone"))
		return strings.TrimSpace(string(b))
	}

	var logMu sync.Mutex
	var logs []string
	logf := func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(f, a...))
	}
	moved := func() int {
		logMu.Lock()
		defer logMu.Unlock()
		n := 0
		for _, l := range logs {
			if strings.Contains(l, "chat(s) / timeline(s) moved into") {
				n++
			}
		}
		return n
	}
	f := newFakeHub()
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) { return assistantwire.Archives{}, nil })
	st := newKBStore()
	s := kbServiceOn(t, f, st, dir, logf)
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Shanghai" })

	// The database down: nothing moves, the round waits (the connectors are not even asked).
	st.mu.Lock()
	st.down = true
	st.mu.Unlock()
	s.prefs.mu.Lock()
	s.prefs.at = time.Time{} // read the settings again
	s.prefs.mu.Unlock()
	if _, err := s.kbGet(context.Background()); !errors.Is(err, errKBNotOpen) {
		t.Fatalf("opened without the user's zone: %v", err)
	}
	if _, err := runTool(s, "c1", "kb_update", map[string]any{"kind": "projects", "id": "发布", "log": "写早了"}); err == nil {
		t.Fatal("a write went in before the zone was known")
	}
	if _, err := zoneTestTurn(s, "s1"); err != nil {
		t.Fatal(err)
	}
	if zoneOf() != "UTC" || moved() != 0 {
		t.Fatalf("moved without the user's zone: %q, %d move(s)", zoneOf(), moved())
	}
	st.mu.Lock()
	st.down = false
	st.mu.Unlock()

	e := kbEng(t, s)
	if zoneOf() != "Asia/Shanghai" || moved() != 1 {
		t.Fatalf("not moved into the user's zone: %q, %d move(s)", zoneOf(), moved())
	}
	if days := e.Store.Days("lark-f1"); len(days) != 1 || days[0] != "2026-11-04" {
		t.Fatalf("days %v", days)
	}
	if _, ok := e.Store.Project("发布"); ok {
		t.Fatal("the refused write went in")
	}
	// The agent's turn that came before it opened went in once it did.
	if log := e.Store.Log("persona", 10); len(log) != 1 || !strings.Contains(log[0], "Agent · 修排序 · server") {
		t.Fatalf("the waiting turn: %v", log)
	}

	// Another process (here another service) cannot open it while this one has the data directory.
	again := kbServiceOn(t, f, st, dir, logf)
	if _, err := again.kbGet(context.Background()); !errors.Is(err, errKBNotOpen) {
		t.Fatalf("two have the data directory: %v", err)
	}
	// A restart: the lock goes with the old one; the same zone, nothing moves.
	s.kbClose()
	kbEng(t, again)
	if moved() != 1 {
		t.Fatalf("moved again at a restart: %v", logs)
	}
}

// zoneTestTurn hands s an agent's finished turn in the project directory of TestKBZoneAtStart.
func zoneTestTurn(s *Service, session string) (bool, error) {
	raw, _ := json.Marshal(assistantwire.KBAgentTurn{MachineID: "server", SessionID: session, Cwd: "/code/persona", Title: "修排序", Line: "改好了"})
	return s.kbEvent(context.Background(), assistantwire.EventKBAgentTurn, raw)
}

// The post archive's index is written after posts came in, not on every round.
func TestKBPostsIndex(t *testing.T) {
	s := newKBService(t, newFakeHub())
	if _, err := s.kbPutPosts(context.Background(), assistantwire.KBPosts{Connector: "feed", Sources: []string{"feed"}, Posts: []connector.PostPut{
		{Post: connector.Post{ID: "p1", Text: "第一帖", At: time.Now()}}, {Post: connector.Post{ID: "p2", Text: "第二帖", At: time.Now()}}}}); err != nil {
		t.Fatal(err)
	}
	e := kbEng(t, s)
	idx := filepath.Join(e.Store.Dir, "moments", "index.jsonl")
	s.kbPostsUpkeep(e, false)
	if b, _ := os.ReadFile(idx); strings.Count(string(b), "\n") != 2 {
		t.Fatalf("index:\n%s", b)
	}
	_ = os.Remove(idx)
	s.kbPostsUpkeep(e, false)
	if _, err := os.Stat(idx); err == nil {
		t.Fatal("the index was written again with nothing new")
	}
}

// A project's page lists the agents working in its directories (not the assistant's conversations, not offline ones).
func TestKBRouteProjectAgents(t *testing.T) {
	f := newFakeHub()
	f.sessions["server"] = []proto.Session{{SessionID: "a1", Cwd: "/code/persona/ios", Status: proto.StatusIdle},
		{SessionID: "a2", Cwd: "/code/persona", Status: proto.StatusOffline}, {SessionID: "c1", Cwd: "/code/persona", Assistant: true},
		{SessionID: "a3", Cwd: "/elsewhere", Status: proto.StatusWorking}}
	s := newKBService(t, f)
	if _, err := kbEng(t, s).Apply(kb.KindProject, "Persona", kb.Update{Dirs: []string{"server:/code/persona"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kb/projects/persona", nil))
	var out struct {
		Agents []proto.Session `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || len(out.Agents) != 1 || out.Agents[0].SessionID != "a1" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Off: 503, as before; not open yet (no word from the hub): 503 kb_unavailable.
	off := New(Options{Hub: f, Logf: testLogf(t)})
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kb/projects", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), assistantwire.CodeKBDisabled) {
		t.Fatalf("off: %d %s", rec.Code, rec.Body)
	}
	early := New(Options{Hub: newFakeHub(), Logf: testLogf(t), KB: &KBOptions{Dir: t.TempDir()}})
	rec = httptest.NewRecorder()
	early.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kb/projects", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), assistantwire.CodeKBUnavailable) {
		t.Fatalf("not open yet: %d %s", rec.Code, rec.Body)
	}
}

// How the knowledge base is kept is the hub's manager.json kb section (its hello), wherever the assistant runs: the text
// retention and the user's other notes; a change opens it again with the new one; off, it is off.
func TestKBKeptAsTheHubSays(t *testing.T) {
	f := newFakeHub()
	f.hello.KB = &KBSettings{TextDays: 30, AttachmentDays: 7, ExtraDirs: []string{"/notes"}}
	s := newKBService(t, f)
	e := kbEng(t, s)
	if e.TextDays != 30 || e.AttachmentDays != 7 || len(e.ExtraDirs) != 1 || e.ExtraDirs[0] != "/notes" {
		t.Fatalf("engine %+v", e)
	}
	if d := s.kbExtraDirs(context.Background()); len(d) != 1 {
		t.Fatalf("CLAUDE.md's dirs %v", d)
	}
	f.mu.Lock()
	f.hello.KB = &KBSettings{TextDays: 90}
	f.mu.Unlock()
	if e2 := kbEng(t, s); e2.TextDays != 90 || e2.Store != e.Store {
		t.Fatalf("after the hub's change %+v", e2)
	}
	// Attachment days per source: the engine goes by them, and a change opens it again.
	f.mu.Lock()
	f.hello.KB = &KBSettings{TextDays: 90, AttachmentDaysBySource: map[string]int{"chat": 3650}}
	f.mu.Unlock()
	if e3 := kbEng(t, s); e3.AttachmentDaysFor("chat") != 3650 || e3.AttachmentDaysFor("other") != 30 || e3.Store != e.Store {
		t.Fatalf("attachment days by source %+v", e3)
	}
	if !sameKBSettings(KBSettings{}, KBSettings{AttachmentDaysBySource: map[string]int{}}) ||
		sameKBSettings(KBSettings{AttachmentDaysBySource: map[string]int{"a": 1}}, KBSettings{AttachmentDaysBySource: map[string]int{"a": 2}}) {
		t.Fatal("sameKBSettings and attachment days by source")
	}
	f.mu.Lock()
	f.hello.KB = &KBSettings{Off: true}
	f.mu.Unlock()
	if _, err := s.kbGet(context.Background()); !errors.Is(err, errKBOff) {
		t.Fatalf("off at the hub: %v", err)
	}
}

// A panic inside one part of a knowledge-base round is recovered and logged with its value and stack, not dropped
// silently; a cancelled round does not run the part at all.
func TestKBRunLogsPanic(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), logf)
	s.kbRun(context.Background(), func() { panic("malformed item") })
	mu.Lock()
	got := strings.Join(lines, "\n")
	mu.Unlock()
	if !strings.Contains(got, "kb: panic: malformed item") || !strings.Contains(got, "TestKBRunLogsPanic") {
		t.Fatalf("panic not logged with its value and stack: %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	s.kbRun(ctx, func() { ran = true })
	if ran {
		t.Fatal("kbRun ran f on a cancelled context")
	}
}
