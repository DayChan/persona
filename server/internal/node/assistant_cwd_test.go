package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The tests of #2368: Claude Code writes the shell's directory after a Bash `cd` into every transcript line's cwd
// (and a hook's), so an assistant conversation that `cd`s into a skill's folder had its latest cwd there, and the
// hub took it for an agent. An assistant or health conversation is where it started; an agent is where it is now.

// driftLines is a Claude transcript that starts in start and goes on in later after a `cd`, as Claude Code writes
// it, padded past the offline scan's smallest transcript.
func driftLines(start, later string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2026-11-09T01:00:00Z","cwd":` + q(start) + `,"message":{"role":"user","content":"look at the skill"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-09T01:00:02Z","cwd":` + q(start) + `,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cd ` + later + `"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","timestamp":"2026-11-09T01:00:03Z","cwd":` + q(later) + `,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":""}]}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"u2","timestamp":"2026-11-09T01:00:05Z","cwd":` + q(later) + `,"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		strings.Repeat(" ", 4096),
	}, "\n") + "\n"
}

// writeDrift writes driftLines(start, later) as session id under the projects directory Claude Code keeps for start.
func writeDrift(t *testing.T, home, id, start, later string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(start, "-"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(driftLines(start, later)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func quickMetaOf(t *testing.T, path string) quickMeta {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return readQuickMeta(path, st)
}

// The exact drift: a transcript whose first lines have the assistant (or health) directory as cwd and whose later
// lines a folder the model `cd`d into, inside or outside it. The quick meta keeps the start directory; an agent keeps
// its latest directory, a subfolder or the assistant's own folders alike.
func TestQuickMetaKeepsAnAssistantConversationWhereItStarted(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	home := t.TempDir()
	assistant := filepath.Join(home, ".persona", "assistant")
	health := filepath.Join(home, ".persona", "health")
	skill := filepath.Join(assistant, ".claude", "skills", "x")
	for i, c := range []struct{ name, start, later, want string }{
		{"assistant into a skill's folder", assistant, skill, assistant},
		{"health into its reports", health, filepath.Join(health, "reports"), health},
		{"assistant out to the data directory", assistant, filepath.Join(home, ".persona", "data"), assistant},
		{"health out to /tmp", health, "/tmp/x", health},
		{"another manager's assistant", filepath.Join(home, ".persona", "assistant-m-0b0b0b"), "/tmp/x", filepath.Join(home, ".persona", "assistant-m-0b0b0b")},
		{"an agent into a subfolder", "/work/app", "/work/app/web", "/work/app/web"},
		{"an agent into the assistant's skills", "/work/app", skill, skill},
		// The other way round: an agent started in the home directory whose `cd` landed exactly in an assistant or
		// health directory is listed where it started, not taken for one of their conversations.
		{"an agent into the assistant directory", home, assistant, home},
		{"an agent into the health directory", home, health, home},
	} {
		p := writeDrift(t, home, "aaaaaaaa-0000-0000-0000-00000000000"+string(rune('1'+i)), c.start, c.later)
		m := quickMetaOf(t, p)
		if m.cwd != c.want || m.start != c.start {
			t.Errorf("%s: cwd %q start %q, want cwd %q start %q", c.name, m.cwd, m.start, c.want, c.start)
		}
	}
	// A Codex rollout says its directory once, in session_meta: a later turn's directory does not move it.
	rollout := filepath.Join(home, "rollout-2026-11-09T01-00-00-0e000000-0000-4000-8000-0000000000a1.jsonl")
	body := `{"timestamp":"2026-11-09T01:00:00Z","type":"session_meta","payload":{"cwd":"/work/app"}}` + "\n" +
		`{"timestamp":"2026-11-09T01:00:01Z","type":"turn_context","payload":{"cwd":"/work/app/web","model":"gpt"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := quickMetaOf(t, rollout); m.cwd != "/work/app" {
		t.Errorf("a Codex rollout's cwd: %q", m.cwd)
	}
}

// A first prompt bigger than the head read (an inline image) hides the start directory: the node's own assistant or
// health project says it. Another project keeps the latest cwd.
func TestMetaWithoutAHeadGoesByTheAssistantProject(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	write := func(project, id, start, later string) string {
		dir := filepath.Join(n.home, ".claude", "projects", project)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		image := strings.Repeat("A", 300<<10)
		body := `{"type":"user","uuid":"u1","timestamp":"2026-11-09T01:00:00Z","cwd":` + q(start) + `,"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + image + `"}},{"type":"text","text":"what is this"}]}}` + "\n" +
			`{"type":"user","uuid":"u2","parentUuid":"u1","timestamp":"2026-11-09T01:00:03Z","cwd":` + q(later) + `,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":""}]}}` + "\n" +
			`{"type":"assistant","uuid":"a2","parentUuid":"u2","timestamp":"2026-11-09T01:00:05Z","cwd":` + q(later) + `,"message":{"role":"assistant","content":[{"type":"text","text":"a cat"}]}}` + "\n"
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	meta := func(p string) quickMeta {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return n.meta(p, st)
	}
	assistant, health := identity.AssistantDir(n.home), identity.HealthDir(n.home)
	own := n.assistantProject()
	p := write(own, "cccccccc-0000-0000-0000-000000000001", assistant, filepath.Join(assistant, ".claude", "skills", "x"))
	if m := meta(p); m.start != "" || m.cwd != assistant {
		t.Errorf("the assistant's project: start %q cwd %q, want no start and %q", m.start, m.cwd, assistant)
	}
	p = write(strings.TrimSuffix(own, "-assistant")+"-health", "cccccccc-0000-0000-0000-000000000002", health, filepath.Join(health, "reports"))
	if m := meta(p); m.cwd != health {
		t.Errorf("the health project: cwd %q, want %q", m.cwd, health)
	}
	p = write("-src-app", "cccccccc-0000-0000-0000-000000000003", "/src/app", "/src/app/web")
	if m := meta(p); m.cwd != "/src/app/web" {
		t.Errorf("an agent's project: cwd %q", m.cwd)
	}
	// The names a symlinked home or the former state directory give the project are not mapped back: lossy.
	if d := n.assistantDirOfProject("-data-home-me--persona-assistant"); d != "" {
		t.Errorf("another home's assistant project mapped to %q", d)
	}
	if d := n.assistantDirOfProject(own + "-bin"); d != "" {
		t.Errorf("a project next to the assistant's mapped to %q", d)
	}
}

// A drifted assistant (or health) conversation at rest (after Stop, or once its process was let go) is listed in its
// directory, its workspace that directory, in no repository the folder it `cd`d into is in. Filters by directory see
// that directory too: a blocked pattern for the skill's folder, a session scope the folder is outside of.
func TestOfflineAssistantConversationIsListedWhereItStarted(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _, home := herdrNode(t)
	assistant, health := identity.AssistantDir(home), identity.HealthDir(home)
	skill := filepath.Join(assistant, ".claude", "skills", "x")
	reports := filepath.Join(health, "reports")
	for _, d := range []string{filepath.Join(skill, ".git"), reports} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const mine, healthID, agent = "dddddddd-0000-0000-0000-000000000001", "dddddddd-0000-0000-0000-000000000002", "dddddddd-0000-0000-0000-000000000003"
	writeDrift(t, home, mine, assistant, skill)
	writeDrift(t, home, healthID, health, reports)
	work := filepath.Join(home, "work", "app")
	if err := os.MkdirAll(filepath.Join(work, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeDrift(t, home, agent, work, filepath.Join(work, "web"))
	intoDirs := map[string]string{"dddddddd-0000-0000-0000-000000000005": assistant, "dddddddd-0000-0000-0000-000000000006": health}
	for id, d := range intoDirs {
		writeDrift(t, home, id, home, d)
	}
	rows := func() map[string]proto.Session {
		t.Helper()
		offlineCache.Lock()
		offlineCache.at = time.Time{}
		offlineCache.Unlock()
		out := map[string]proto.Session{}
		for _, s := range n.sessionList(context.Background(), false) {
			out[s.SessionID] = s
		}
		return out
	}
	real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	got := rows()
	if s := got[mine]; s.Cwd != assistant || s.WorkspaceCwd != real(assistant) || s.RepoRoot != "" || s.Worktree != "" {
		t.Errorf("the assistant's conversation: cwd %q workspace %q repo %q worktree %q", s.Cwd, s.WorkspaceCwd, s.RepoRoot, s.Worktree)
	}
	if s := got[healthID]; s.Cwd != health || s.WorkspaceCwd != real(health) {
		t.Errorf("the health conversation: cwd %q workspace %q", s.Cwd, s.WorkspaceCwd)
	}
	if s := got[agent]; s.Cwd != filepath.Join(work, "web") {
		t.Errorf("an agent keeps its latest directory: %q", s.Cwd)
	}
	// Agents whose `cd` landed in the assistant or health directory itself: at rest they are where they started,
	// never in those directories, where the hub would take them for the assistant's conversations.
	for id, d := range intoDirs {
		if s := got[id]; s.Cwd != home || identity.IsAssistantDir(s.Cwd) {
			t.Errorf("an agent that cd'd into %s is listed in %q, want %q", d, s.Cwd, home)
		}
	}
	// The directories offered for new agents and checked by the doctor are not the folders a conversation `cd`d into.
	for _, d := range n.recentDirs() {
		if strings.HasPrefix(d.Path, assistant) || strings.HasPrefix(d.Path, health) {
			t.Errorf("recent directories offer %q", d.Path)
		}
	}
	if dirs := n.workDirs(20); slices.Contains(dirs, skill) || slices.Contains(dirs, reports) {
		t.Errorf("work directories: %v", dirs)
	}

	// A blocked pattern that matches the skill's folder, not the assistant directory: still listed.
	n.cdirs = newClaudeDirs()
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.cdirs.blocked = []string{"skills"}
	if _, ok := rows()[mine]; !ok {
		t.Error("a blocked pattern for the folder it cd'd into hid the assistant's conversation")
	}
	n.cdirs = nil

	// A node scoped to its state directory: a conversation that `cd`d out of it is still its own.
	const out = "dddddddd-0000-0000-0000-000000000004"
	writeDrift(t, home, out, assistant, filepath.Join(home, "elsewhere"))
	n.sessionScope = &nodeSessionScope{dir: filepath.Join(home, ".persona"), claude: filepath.Join(home, ".claude"), codex: filepath.Join(home, ".persona", "codex")}
	got = rows()
	if s, ok := got[out]; !ok || s.Cwd != assistant {
		t.Errorf("scoped: the conversation that cd'd out is %+v (listed %v)", s.Cwd, ok)
	}
	if _, ok := got[agent]; ok {
		t.Error("scoped: an agent outside the scope is listed")
	}
}

// The node's start record wins over the transcript: the unresolved path a running conversation and its resumes use
// (a symlinked home), or the directory it was started in under the former state directory. A pane's row keeps its
// pane's directory.
func TestStartRecordPlacesARowAtRest(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	n.state = newState()
	realHome := filepath.Join(n.home, "real")
	link := filepath.Join(n.home, "link")
	if err := os.MkdirAll(filepath.Join(realHome, ".persona", "assistant"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realHome, link); err != nil {
		t.Fatal(err)
	}
	resolved, unresolved := filepath.Join(realHome, ".persona", "assistant"), filepath.Join(link, ".persona", "assistant")
	former := filepath.Join(n.home, compat.StateDir, "assistant")
	const linked, moved = "eeeeeeee-0000-0000-0000-000000000001", "eeeeeeee-0000-0000-0000-000000000002"
	writeDrift(t, n.home, linked, resolved, filepath.Join(resolved, ".claude", "skills", "x"))
	writeDrift(t, n.home, moved, filepath.Join(n.home, ".persona", "assistant"), "/tmp/x")
	n.state.Sessions[linked] = sessionState{Cwd: unresolved, Launcher: "claude", StartedAt: time.Now()}
	n.state.Sessions[moved] = sessionState{Cwd: former, Launcher: "claude", StartedAt: time.Now()}
	got := map[string]string{}
	for _, s := range n.scanOffline() {
		got[s.SessionID] = s.Cwd
	}
	if got[linked] != unresolved || got[moved] != former {
		t.Errorf("listed in %q and %q, want the records' %q and %q", got[linked], got[moved], unresolved, former)
	}
	s := proto.Session{SessionID: linked, Cwd: filepath.Join(resolved, ".claude", "skills", "x")}
	n.decorate(&s)
	if s.Cwd != unresolved {
		t.Errorf("a pane-less row decorated: %q", s.Cwd)
	}
	s = proto.Session{SessionID: linked, Cwd: "/work/x", PaneID: "w1:p1"}
	n.decorate(&s)
	if s.Cwd != "/work/x" {
		t.Errorf("a pane's row decorated: %q", s.Cwd)
	}
	n.state.Sessions["eeeeeeee-0000-0000-0000-000000000003"] = sessionState{Cwd: "/work/app", Launcher: "claude", StartedAt: time.Now()}
	s = proto.Session{SessionID: "eeeeeeee-0000-0000-0000-000000000003", Cwd: "/work/app/web"}
	n.decorate(&s)
	if s.Cwd != "/work/app/web" {
		t.Errorf("an agent's record moved its row: %q", s.Cwd)
	}
}

// Stop kills the process: the conversation is listed from its transcript at once, and it stays in the assistant
// directory it ran in, not the folder it `cd`d into. session.info says the same, running and at rest; an agent's
// says its latest directory.
func TestStoppedAssistantConversationStaysPut(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _, home := herdrNode(t)
	assistant := identity.AssistantDir(home)
	skill := filepath.Join(assistant, ".claude", "skills", "x")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeDrift(t, home, sidA, assistant, skill)
	n.mu.Lock()
	n.paths[sidA] = path
	n.procs[sidA] = &assistantProc{id: sidA, cwd: assistant, agent: "claude", exited: make(chan struct{})}
	n.mu.Unlock()
	row := func() proto.Session {
		t.Helper()
		offlineCache.Lock()
		offlineCache.at = time.Time{}
		offlineCache.Unlock()
		for _, s := range n.sessionList(context.Background(), false) {
			if s.SessionID == sidA {
				return s
			}
		}
		t.Fatal("the conversation is not listed")
		return proto.Session{}
	}
	info := func(id string) string {
		t.Helper()
		m, err := n.sessionInfo(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return m["cwd"].(string)
	}
	if s := row(); s.Cwd != assistant || s.Status == proto.StatusOffline {
		t.Fatalf("running: %q %s", s.Cwd, s.Status)
	}
	if c := info(sidA); c != assistant {
		t.Errorf("session.info running: %q", c)
	}
	n.mu.Lock()
	delete(n.procs, sidA) // as interrupt leaves it
	n.mu.Unlock()
	if s := row(); s.Cwd != assistant || s.Status != proto.StatusOffline {
		t.Fatalf("stopped: %q %s", s.Cwd, s.Status)
	}
	if c := info(sidA); c != assistant {
		t.Errorf("session.info at rest: %q", c)
	}
	work := filepath.Join(home, "work", "app")
	writeDrift(t, home, sidB, work, filepath.Join(work, "web"))
	if c := info(sidB); c != filepath.Join(work, "web") {
		t.Errorf("session.info of an agent: %q", c)
	}
}

// A hook of a running headless conversation gives the card its conversation's directory, wherever the model's `cd`
// left the hook: a health conversation's card from health/reports or /tmp is the health space's. A hook inside a
// health space keeps that over an assistant directory (a mix-up only hides more); a session with no process keeps
// the hook's.
func TestHookCardsArePlacedInTheConversationsDirectory(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	assistant, health := identity.AssistantDir(n.home), identity.HealthDir(n.home)
	const healthSession, mainSession, agentSession = "11111111-0000-4000-8000-000000000001", "11111111-0000-4000-8000-000000000002", "11111111-0000-4000-8000-000000000003"
	n.procs[healthSession] = &assistantProc{id: healthSession, agent: "claude", cwd: health, exited: make(chan struct{})}
	n.procs[mainSession] = &assistantProc{id: mainSession, agent: "claude", cwd: assistant, exited: make(chan struct{})}
	question, _ := json.Marshal(map[string]any{"questions": []map[string]any{{"question": "Which one?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}})
	for i, c := range []struct{ name, session, hookCwd, want, title string }{
		{"health, from its reports", healthSession, filepath.Join(health, "reports"), health, "health"},
		{"health, from /tmp", healthSession, "/tmp/x", health, "health"},
		{"main, from inside health", mainSession, filepath.Join(health, "x"), filepath.Join(health, "x"), "x"},
		{"main, from a skill's folder", mainSession, filepath.Join(assistant, ".claude", "skills", "x"), assistant, "assistant"},
		{"no process", agentSession, "/work/app/web", "/work/app/web", "web"},
	} {
		raw, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", ToolName: "AskUserQuestion", SessionID: c.session,
			Cwd: c.hookCwd, ToolUseID: "toolu_" + string(rune('a'+i)), ToolInput: question})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1"})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		cards := fh.interactions()
		if len(cards) != i+1 {
			t.Fatalf("%s: %d cards", c.name, len(cards))
		}
		if ix := cards[i]; ix.Cwd != c.want || ix.SessionTitle != c.title {
			t.Errorf("%s: card in %q titled %q, want %q titled %q", c.name, ix.Cwd, ix.SessionTitle, c.want, c.title)
		}
	}
}

// A Codex assistant conversation's approval card is the conversation's: in its directory and titled after it, though
// the command runs in a subfolder. The gate still goes by where the command runs: a write from inside the health space
// is confirmed on the phone though the conversation is the assistant's.
func TestCodexApprovalCardIsPlacedInTheConversationsDirectory(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = olCfg(proto.LarkRule{Method: "POST", Path: "/conversations/7123/messages", Action: "allow"}), time.Now().Add(time.Hour)
	n.mu.Unlock()
	assistant, health := identity.AssistantDir(n.home), identity.HealthDir(n.home)
	skill := filepath.Join(assistant, ".claude", "skills", "x")
	p, c, f := newFakeCodex(t, n, assistant)
	p.id = testThread
	c.threadID, c.turnID = testThread, "turn-1"
	n.noteModes(testThread, "default", "")
	res := f.request(1, "item/commandExecution/requestApproval", map[string]any{"kind": "command", "threadId": testThread,
		"turnId": "turn-1", "itemId": "call_a", "command": "/bin/zsh -lc 'rm -rf build'", "cwd": skill})
	if res["decision"] != "accept" {
		t.Fatalf("decision %v", res)
	}
	cards := fh.interactions()
	if len(cards) != 1 || cards[0].Cwd != assistant || cards[0].SessionTitle != "assistant" {
		t.Fatalf("the card: %+v", cards)
	}
	// A write the rules allow, from a folder of the health space: confirmed (the gate had the request's directory),
	// its card in that folder (a mix-up only hides more).
	res = f.request(2, "item/commandExecution/requestApproval", map[string]any{"kind": "command", "threadId": testThread,
		"turnId": "turn-1", "itemId": "call_b", "command": `ex POST /conversations/7123/messages '{"text":"hi"}'`, "cwd": filepath.Join(health, "reports")})
	cards = fh.interactions()
	if res["decision"] != "accept" || len(cards) != 2 || cards[1].Cwd != filepath.Join(health, "reports") || len(cards[1].AlwaysKeys) != 0 {
		t.Fatalf("a write from inside the health space: %v, cards %+v", res, cards)
	}
	// The same write from the assistant's own folder goes by the rule, unasked.
	res = f.request(3, "item/commandExecution/requestApproval", map[string]any{"kind": "command", "threadId": testThread,
		"turnId": "turn-1", "itemId": "call_c", "command": `ex POST /conversations/7123/messages '{"text":"hi"}'`, "cwd": skill})
	if res["decision"] != "accept" || len(fh.interactions()) != 2 {
		t.Fatalf("a write the rule allows: %v, cards %d", res, len(fh.interactions()))
	}
}

// A runtime's card or row in a folder of the health space is the health space's; the permission rules a process is
// started with still take only the space's own directory (they assume it is that directory).
func TestHealthChecksCountItsSubfolders(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	home := t.TempDir()
	health := filepath.Join(home, ".persona", "health")
	reports := filepath.Join(health, "reports")
	if !runtimeHealth(&runtimeConn{}, proto.Interaction{SessionID: "r1", Cwd: reports}) {
		t.Error("a runtime card from the health space's reports")
	}
	h := &runtimeConn{sessions: []proto.Session{{SessionID: "r1", Cwd: reports}, {SessionID: "r2", Cwd: filepath.Join(home, ".persona", "assistant", "health")}}}
	if !runtimeHealth(h, proto.Interaction{SessionID: "r1", Cwd: "/tmp/x"}) {
		t.Error("a runtime row in the health space's reports")
	}
	if runtimeHealth(h, proto.Interaction{SessionID: "r2", Cwd: "/tmp/x"}) {
		t.Error("a folder named health elsewhere is not the space")
	}
	if healthReportRules(reports) != nil || healthReportRules(health) == nil {
		t.Error("the report rules take the health directory itself only")
	}
}
