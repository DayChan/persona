package node

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestReadOnlyCodexTerminalStart(t *testing.T) {
	for _, tc := range []struct {
		name, launcher, mode string
		argv                 []string
	}{
		{"plain", "codex", "plan", []string{"codex"}},
		{"alias", "code-alias", "plan", []string{"codex", "--model", "sample-model"}},
		{"writable", "code-alias", "acceptEdits", []string{"codex", "--full-auto", "-m", "sample-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCodexLaunchers(t, map[string][]string{tc.launcher: tc.argv})
			n, _, _, _ := codexStartHerdr(t, 0)
			res, err := request(t, n, "session.start", map[string]any{
				"cwd": "~/proj", "launcher": tc.launcher, "permission_mode": tc.mode, "prompt": "Inspect the project",
			})
			if err != nil {
				t.Fatal(err)
			}
			session := decodeSessions(t, []any{res["session"]})[0]
			if session.SessionID != startThread || session.Agent != "codex" {
				t.Fatalf("start did not produce the Codex session: %+v", session)
			}
			want := slices.Clone(tc.argv)
			if tc.mode == "plan" {
				want = append(want, "--sandbox", "read-only", "--ask-for-approval", "never")
			}
			if !slices.Equal(ran, want) {
				t.Fatalf("started with %q, want %q", ran, want)
			}
			wantMode := ""
			if tc.mode == "plan" {
				wantMode = "plan"
			}
			if st, _ := n.state.get(startThread); st.AppMode != wantMode {
				t.Fatalf("saved mode = %q, want %q", st.AppMode, wantMode)
			}
		})
	}
}

func TestReadOnlyCodexArgumentsRejectConflicts(t *testing.T) {
	n, _, _ := startNode(t)
	for _, args := range [][]string{
		{"--full-auto"}, {"--dangerously-bypass-approvals-and-sandbox"}, {"--yolo"}, {"--approve-for-me"},
		{"--sandbox", "workspace-write"}, {"--sandbox=workspace-write"},
		{"-s", "workspace-write"}, {"-sworkspace-write"}, {"-s=workspace-write"},
		{"--ask-for-approval", "never"}, {"--ask-for-approval=never"},
		{"-a", "never"}, {"-anever"}, {"-a=never"},
	} {
		t.Run(args[0], func(t *testing.T) {
			argv := append([]string{"codex"}, args...)
			original := slices.Clone(argv)
			if _, err := n.codexReadonlyArgs("codex", argv, "plan"); err == nil {
				t.Fatalf("read-only start accepted conflicting arguments: %q", argv)
			} else if code, data := refusalOf(t, err); code != "readonly_unsupported" || data["delivered"] != deliveredNone {
				t.Fatalf("conflict: %v (%v)", err, data)
			}
			if !slices.Equal(argv, original) {
				t.Fatalf("validation changed launcher arguments: %q", argv)
			}
			for _, mode := range []string{"", "default", "acceptEdits", "bypassPermissions"} {
				got, err := n.codexReadonlyArgs("codex", argv, mode)
				if err != nil || !slices.Equal(got, original) {
					t.Fatalf("mode %q changed launcher arguments: %q (%v)", mode, got, err)
				}
			}
		})
	}
}

func TestReadOnlyTerminalStartRefusesUnsupportedLaunchers(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"code-alias": {"codex", "--full-auto"}})
	n, hh, oh := startNode(t)
	n.setManaged([]proto.ManagedLauncher{{Name: "managed-codex", Agent: "codex", Args: []string{"--full-auto"}}})
	for _, tc := range []struct{ launcher, agent string }{
		{"code-alias", ""}, {"managed-codex", ""}, {"wrapper", "codex"}, {traexAgent, ""},
	} {
		t.Run(tc.launcher, func(t *testing.T) {
			_, err := n.start(context.Background(), t.TempDir(), tc.launcher, "Inspect the project", "", "", "plan", "", "", "", tc.agent)
			if code, data := refusalOf(t, err); code != "readonly_unsupported" || data["delivered"] != deliveredNone {
				t.Fatalf("start: %v (%v)", err, data)
			}
		})
	}
	if hh.asked()+oh.asked() != 0 {
		t.Fatalf("a refused start reached an engine: herdr %d, Orca %d", hh.asked(), oh.asked())
	}
}

func TestReadOnlyCodexResume(t *testing.T) {
	for _, tc := range []struct {
		name, mode, savedMode string
		send                  bool
		argv                  []string
	}{
		{"history", "plan", "", false, []string{"codex", "-m", "sample-model"}},
		{"saved", "", "plan", false, []string{"codex", "-m", "sample-model"}},
		{"message", "", "plan", true, []string{"codex", "-m", "sample-model"}},
		{"writable", "", "", false, []string{"codex", "--full-auto", "-m", "sample-model"}},
		{"clear", "acceptEdits", "plan", false, []string{"codex", "--full-auto", "-m", "sample-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCodexLaunchers(t, map[string][]string{"code-alias": tc.argv})
			n, host, cwd := codexHistoryNode(t)
			listSessions(t, n)
			if tc.savedMode != "" {
				n.state.put(startThread, sessionState{Launcher: "code-alias", Cwd: cwd, AppMode: tc.savedMode})
			}
			host.reset()
			got := resumeFile(host, cwd)
			if tc.send {
				if _, err := request(t, n, "session.send", map[string]any{"session_id": startThread, "text": "Inspect the project"}); err != nil {
					t.Fatal(err)
				}
			} else {
				res, err := request(t, n, "session.start", map[string]any{
					"resume_session_id": startThread, "launcher": "code-alias", "permission_mode": tc.mode, "prompt": "Inspect the project",
				})
				if err != nil {
					t.Fatal(err)
				}
				if session, _ := res["session"].(map[string]any); session == nil || session["session_id"] != startThread || session["pane_id"] != "w1:p2" {
					t.Fatalf("resume did not return the terminal session: %v", res)
				}
			}
			if agent, ok := n.agent(startThread); !ok || agent.PaneID != "w1:p2" {
				t.Fatalf("resume did not start the terminal agent: %+v", agent)
			}
			want := slices.Clone(tc.argv)
			wantMode := ""
			if tc.mode == "plan" || tc.mode == "" && tc.savedMode == "plan" {
				want = append(want, "--sandbox", "read-only", "--ask-for-approval", "never")
				wantMode = "plan"
			}
			want = append(want, "resume", startThread)
			if file := got(); !slices.Equal(file.Argv, want) || file.Prompt != "Inspect the project" {
				t.Fatalf("resumed with %q and %q, want %q", file.Argv, file.Prompt, want)
			}
			if st, _ := n.state.get(startThread); st.AppMode != wantMode {
				t.Fatalf("resumed mode = %q, want %q", st.AppMode, wantMode)
			}
		})
	}
}

func TestReadOnlyCodexResumeRefusesConflictingLauncherBeforeOpeningATab(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"code-alias": {"codex", "--full-auto"}})
	n, host, cwd := codexHistoryNode(t)
	listSessions(t, n)
	host.reset()
	_, err := n.start(context.Background(), cwd, "code-alias", "Inspect the project", startThread, "", "plan", "", "", "", "")
	if code, data := refusalOf(t, err); code != "readonly_unsupported" || data["delivered"] != deliveredNone {
		t.Fatalf("resume: %v (%v)", err, data)
	}
	if got := host.trace("workspace.list", "tab.create", "pane.send_input"); len(got) != 0 {
		t.Fatalf("refused resume reached the terminal: %v", got)
	}
}

func TestReadOnlyCodexLiveResumeCannotChangePermissions(t *testing.T) {
	n, host, cwd := codexHistoryNode(t)
	listSessions(t, n)
	resumeFile(host, cwd)
	if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); err != nil {
		t.Fatal(err)
	}
	host.reset()
	for _, resume := range []func() error{
		func() error {
			_, err := n.start(context.Background(), cwd, "codex", "Inspect the project", startThread, "", "plan", "", "", "", "")
			return err
		},
		func() error {
			// The thread can become live after start's initial check, before the resume lock is acquired.
			_, err := n.resumeCodexSession(context.Background(), startThread, "codex", "Inspect the project", "plan")
			return err
		},
	} {
		if code, data := refusalOf(t, resume()); code != "readonly_unsupported" || data["delivered"] != deliveredNone {
			t.Fatalf("live resume: code %q, data %v", code, data)
		}
	}
	if got := host.trace("tab.create", "pane.send_input"); len(got) != 0 {
		t.Fatalf("live resume sent a new command: %v", got)
	}
}

func TestReadOnlyCodexLiveResumeWithoutLocalRollout(t *testing.T) {
	n, hh, oh := startNode(t)
	cwd := t.TempDir()
	n.mu.Lock()
	n.agents[startThread] = host.Agent{Agent: "codex", Cwd: cwd, PaneID: "w1:p1", AgentStatus: proto.StatusIdle}
	n.mu.Unlock()
	if path := n.codexPath(startThread); path != "" {
		t.Fatalf("fixture unexpectedly has a local rollout: %q", path)
	}
	_, err := n.start(context.Background(), cwd, "codex", "Inspect the project", startThread, "", "plan", "", "", "", "")
	if code, data := refusalOf(t, err); code != "readonly_unsupported" || data["delivered"] != deliveredNone {
		t.Fatalf("live resume: %v (%v)", err, data)
	}
	if hh.asked()+oh.asked() != 0 {
		t.Fatal("a refused live resume created a workspace")
	}
}

func TestReadOnlyCodexArgumentsKeepOptionValues(t *testing.T) {
	n, _, _ := startNode(t)
	argv := []string{"codex", "--model", "-sample", "--config", "model_reasoning_effort=low"}
	want := append(slices.Clone(argv), "--sandbox", "read-only", "--ask-for-approval", "never")
	got, err := n.codexReadonlyArgs("codex", argv, "plan")
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("a model value was treated as a permission flag: %q (%v)", got, err)
	}
	n.setManaged([]proto.ManagedLauncher{{Name: "managed-codex", Agent: "codex", Args: []string{"--model", "-sample"}}})
	wrapper := []string{n.bin, "launch", "managed-codex"}
	want = append(slices.Clone(wrapper), "--sandbox", "read-only", "--ask-for-approval", "never")
	got, err = n.codexReadonlyArgs("managed-codex", wrapper, "plan")
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("safe managed arguments were refused: %q (%v)", got, err)
	}
}

func TestReadOnlyRuntimeStartRefusesEveryProvider(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		return map[string]any{"session": map[string]any{"session_id": "paseo-readonly", "status": "idle", "cwd": "/w/app"}}, nil
	}
	for _, launcher := range []string{"paseo-claude", "paseo-codex", "claude", ""} {
		t.Run(launcher, func(t *testing.T) {
			_, err := nodeCall(t, n, "session.start", map[string]any{
				"cwd": "/w/app", "launcher": launcher, "permission_mode": "plan", "prompt": "Inspect the project",
			})
			if rpcCode(err) != "readonly_unsupported" {
				t.Fatalf("start: %v", err)
			}
		})
	}
	// Paseo's takeover path does not apply modes, even for a named Claude provider.
	if _, err := nodeCall(t, n, "session.start", map[string]any{
		"cwd": "/w/app", "launcher": "paseo-claude", "permission_mode": "plan", "resume_session_id": sdkSession,
	}); rpcCode(err) != "readonly_unsupported" {
		t.Fatalf("takeover: %v", err)
	}
	if got := host.got("session.start"); len(got) != 0 {
		t.Fatalf("refused starts reached the runtime: %v", got)
	}
	for _, tc := range []struct{ launcher, mode string }{
		{"paseo-claude", "default"}, {"paseo-codex", "acceptEdits"}, {"claude", ""},
	} {
		if _, err := nodeCall(t, n, "session.start", map[string]any{
			"cwd": "/w/app", "launcher": tc.launcher, "permission_mode": tc.mode, "prompt": "Inspect the project",
		}); err != nil {
			t.Fatalf("%s / %s: %v", tc.launcher, tc.mode, err)
		}
	}
	got := host.got("session.start")
	if len(got) != 3 {
		t.Fatalf("allowed starts: %v", got)
	}
	var start map[string]any
	if err := json.Unmarshal(got[0], &start); err != nil || start["permission_mode"] != "default" {
		t.Fatalf("Claude mode: %s (%v)", got[0], err)
	}
}

func TestReadOnlyRuntimeStartRefusesUnverifiedRuntimes(t *testing.T) {
	for _, agent := range []string{"dsh", "pi", "agy", "claude"} {
		t.Run(agent, func(t *testing.T) {
			n, _ := runtimeNode(t)
			hello := dshHello()
			hello["launchers"] = []map[string]any{{"name": "sample", "agent": agent}}
			host := dialRuntime(t, n, hello)
			_, err := nodeCall(t, n, "session.start", map[string]any{
				"cwd": "/w/app", "launcher": "sample", "permission_mode": "plan", "prompt": "Inspect the project",
			})
			if rpcCode(err) != "readonly_unsupported" || len(host.got("session.start")) != 0 {
				t.Fatalf("start: %v, requests %v", err, host.got("session.start"))
			}
		})
	}
}
