package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The mock hub's readiness scenarios (scripts/mock/readiness_mock.py, docs/operations/testing.md "Readiness scenarios") are pinned to what
// this code answers: each scenario is set up here on a fake hub, and its readiness in the three languages and its
// coded start refusal are kept in testdata/readiness_scenarios/<name>.json (UPDATE_GOLDEN=1 writes them, checked_at
// blanked). scripts/mock/test_readiness_mock.py checks the mock against the same files, so a change to the readiness
// fails here first and then there until the mock follows. The computer is a sample user's MacBook (mbp).

type readinessScenario struct {
	name string
	// setup changes the fake hub that has mbp online with Claude Code 2.1.320 and no main conversation yet.
	setup func(f *fakeHub)
	// refusal is what the node answers assistant.send with (nil: it starts, or no machine is there to ask).
	refusal *rpc.Error
	start   bool // the scenario's start is refused: keep its body
}

const scenarioMachine, scenarioName = "mbp", "小林的 MacBook"

// scenarioMain is the main conversation m1 whose only turn ended with text (an answer, or Claude Code's error).
func scenarioMain(f *fakeHub, text string) {
	f.settings.Main = "m1"
	f.setSessions(scenarioMachine, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: text, UpdatedAt: time.Now()})
}

func startRefusal(code, launcher string) *rpc.Error {
	cmd := launcher
	if code == proto.CodeClaudeMissing {
		cmd = "claude"
	}
	data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": launcher})
	return &rpc.Error{Code: code, Data: data,
		Message: launcher + " 没有启动起来（1.2s 后退出），消息没有送达：zsh:1: command not found: " + cmd}
}

var readinessScenarios = []readinessScenario{
	{name: "zero", setup: func(f *fakeHub) { *f = *newFakeHub(); f.settings = &Settings{} }, start: true},
	// A pending computer is absent from devices.list but remains in the manager's own device layer.
	{name: "pending", setup: func(f *fakeHub) { *f = *newFakeHub(); f.settings = &Settings{} }, start: true},
	{name: "offline", setup: func(f *fakeHub) { f.devices[0].Online, f.devices[0].Reachable = false, false }},
	{name: "no_claude", setup: func(f *fakeHub) { f.doctor[scenarioMachine] = "读不到 claude 的版本" },
		refusal: startRefusal(proto.CodeClaudeMissing, "claude"), start: true},
	{name: "not_logged_in", setup: func(f *fakeHub) { scenarioMain(f, "Not logged in · Please run /login") }},
	{name: "bad_key", setup: func(f *fakeHub) { scenarioMain(f, "Invalid API key · Please run /login") }},
	{name: "full", setup: func(f *fakeHub) { scenarioMain(f, "你好，我是 Persona。") }},
	{name: "unverified", setup: func(f *fakeHub) {}},
	{name: "launcher_missing", setup: func(f *fakeHub) {
		f.hello.Assistant.Launcher = "cc_glm"
		f.zshrc = map[string][]proto.Launcher{scenarioMachine: {}}
	}, refusal: startRefusal(proto.CodeLauncherMissing, "cc_glm"), start: true},
	{name: "model_route", setup: func(f *fakeHub) {
		scenarioMain(f, "There's an issue with the selected model (deepseek-v9). It may not exist or you may not have access to it. Run /model to pick a different model.")
	}},
	{name: "rate_limited", setup: func(f *fakeHub) { scenarioMain(f, "API Error: 429 Too Many Requests") }},
	{name: "node_outdated", setup: func(f *fakeHub) { f.devices[0].Version, f.devices[0].ModelConfiguration = "0.9.0", false }},
}

func TestReadinessScenariosGolden(t *testing.T) {
	for _, sc := range readinessScenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newFakeHub()
			f.sessions[scenarioMachine] = nil
			f.hello.Assistant.MachineID = scenarioMachine
			f.devices = []Device{{ID: scenarioMachine, Name: scenarioName, Kind: "link", Online: true, Reachable: true,
				Version: "1.0.0", ModelConfiguration: true}}
			f.doctor = map[string]string{scenarioMachine: "2.1.320，支持 --plugin-dir"}
			f.settings = &Settings{}
			sc.setup(f)
			s := newServiceOn(t, f, &memStore{})
			if sc.name == "pending" {
				s.trustSrc = &trustStore{pending: []string{scenarioMachine}}
				s.devs = &readinessPendingDevices{list: []devices.Summary{{ID: scenarioMachine, Name: scenarioName, Online: true, Reachable: true}}}
			}
			out := map[string]any{}
			if sc.start {
				f.failAssistant = sc.refusal
				for _, lang := range []string{"", "en"} {
					status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", lang, `{"prompt":"你好","main":true}`)
					out["start"+map[string]string{"": "", "en": ".en"}[lang]] = map[string]any{"status": status, "body": body}
				}
				f.failAssistant = nil
			}
			for _, lang := range []string{"zh-Hans", "en", "ja"} {
				res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: "GET", Path: "/api/v1/assistant/readiness",
					Header: map[string][]string{"Accept-Language": {lang}}}, s.lang)
				if err != nil || res.Status != 200 {
					t.Fatalf("readiness: %v %d %s", err, res.Status, res.Body)
				}
				var r map[string]any
				if err := json.Unmarshal(res.Body, &r); err != nil {
					t.Fatal(err)
				}
				r["checked_at"] = ""
				out[lang] = r
			}
			got, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "readiness_scenarios", sc.name+".json")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (UPDATE_GOLDEN=1 writes it)", err)
			}
			if string(append(got, '\n')) != string(want) {
				t.Errorf("%s differs (UPDATE_GOLDEN=1 rewrites it; then make scripts/mock/readiness_mock.py follow)\n--- got ---\n%s", path, got)
			}
		})
	}
}
