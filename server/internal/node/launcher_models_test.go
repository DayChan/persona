package node

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Claude Code's initialize answer, as a 2.1 CLI with a gateway gives it: the /model menu's aliases resolved with the
// launcher's settings, then the gateway's models.
const claudeInitAnswer = `{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"commands":[],"models":[` +
	`{"value":"default","resolvedModel":"claude-opus-5-5[1m]","displayName":"Default (recommended)","description":"Use the default model","supportsEffort":true,"supportedEffortLevels":["low","medium","high","max"]},` +
	`{"value":"haiku","resolvedModel":"example-small","displayName":"example-small","description":"Custom Haiku model"},` +
	`{"value":"gateway/model-a","resolvedModel":"gateway/model-a","displayName":"model-a","description":"From gateway","supportsEffort":true,"supportedEffortLevels":["low","high","ultra"]},` +
	`{"value":"","displayName":"nameless"}]}}}`

func TestClaudeModelListReadsInitialize(t *testing.T) {
	var sent strings.Builder
	out := "zsh: a banner from ~/.zshrc\n" + `{"type":"system","subtype":"status"}` + "\n" + claudeInitAnswer + "\n"
	got, err := claudeModelList(&sent, strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.String(), `"subtype":"initialize"`) {
		t.Fatalf("asked %q", sent.String())
	}
	want := []proto.AgentModel{
		{ID: "default", Name: "Default (recommended)", Description: "Use the default model", Resolved: "claude-opus-5-5[1m]", Default: true, Efforts: []string{"low", "medium", "high", "max"}},
		{ID: "haiku", Name: "example-small", Description: "Custom Haiku model", Resolved: "example-small"},
		// "ultra" is no effort a Claude start takes
		{ID: "gateway/model-a", Name: "model-a", Description: "From gateway", Efforts: []string{"low", "high"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name || got[i].Resolved != want[i].Resolved ||
			got[i].Default != want[i].Default || !slices.Equal(got[i].Efforts, want[i].Efforts) {
			t.Fatalf("model %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestClaudeModelListErrors(t *testing.T) {
	for name, out := range map[string]string{
		"refused":   `{"type":"control_response","response":{"subtype":"error","request_id":"persona-models","error":"Not logged in"}}`,
		"old CLI":   `{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"commands":[]}}}`,
		"no answer": `zsh: command not found: claude`,
	} {
		if _, err := claudeModelList(io.Discard, strings.NewReader(out+"\n")); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// fakeModelServer answers like codex app-server: initialize, then model/list in two pages, with a notification and a
// request of its own in between.
func fakeModelServer(t *testing.T, in io.Reader, out io.WriteCloser) {
	t.Helper()
	defer out.Close()
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var req struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		reply := func(result string) {
			io.WriteString(out, `{"jsonrpc":"2.0","method":"thread/status","params":{}}`+"\n")
			io.WriteString(out, `{"jsonrpc":"2.0","id":99,"method":"account/refresh","params":{}}`+"\n")
			b, _ := json.Marshal(*req.ID)
			io.WriteString(out, `{"jsonrpc":"2.0","id":`+string(b)+`,"result":`+result+"}\n")
		}
		switch {
		case req.Method == "initialize":
			reply(`{"userAgent":"codex"}`)
		case req.Method == "model/list" && req.Params["cursor"] == nil:
			reply(`{"data":[{"id":"preset-a","model":"model-a","displayName":"Model A","description":"default one","isDefault":true,"hidden":false,` +
				`"defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"},{"reasoningEffort":"high"}]},` +
				`{"id":"old","model":"old","displayName":"Old","hidden":true}],"nextCursor":"p2"}`)
		case req.Method == "model/list" && req.Params["cursor"] == "p2":
			reply(`{"data":[{"id":"gateway/model-b","model":"gateway/model-b","displayName":"","hidden":false}],"nextCursor":null}`)
		default:
			io.WriteString(out, `{"jsonrpc":"2.0","id":1,"error":{"message":"unexpected"}}`+"\n")
		}
	}
}

func TestCodexModelListPagesAndHides(t *testing.T) {
	toServer, fromClient := io.Pipe()
	fromServer, toClient := io.Pipe()
	go fakeModelServer(t, toServer, toClient)
	got, err := codexModelList(fromClient, fromServer)
	fromClient.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	a, b := got[0], got[1]
	if a.ID != "model-a" || a.Name != "Model A" || !a.Default || a.DefaultEffort != "medium" || !slices.Equal(a.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("first %+v", a)
	}
	if b.ID != "gateway/model-b" || b.Name != "gateway/model-b" || b.Default {
		t.Fatalf("second %+v", b)
	}
}

// fakeCLIHome is a home whose ~/.zshrc defines launchers running a fake claude (on PATH) that writes its arguments
// to args.txt, answers initialize with the model its launcher set and then waits, as a real one waits for a message.
func fakeCLIHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	claude := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$HOME/args.txt"
for a in "$@"; do case "$a" in /*settings*) cat "$a" > "$HOME/settings.txt" ;; esac; done
read line
echo "a line that is no JSON"
echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default","resolvedModel":"'"$LAUNCHER_MODEL"'","displayName":"Default"}]}}}'
sleep 60
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(claude), 0o700); err != nil {
		t.Fatal(err)
	}
	zshrc := `mine() { LAUNCHER_MODEL=model-of-mine command claude "$@" }
fixed() { LAUNCHER_MODEL=fixed-model command claude --model fixed-model "$@" }
`
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("ZDOTDIR", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	launcherCache.Lock()
	oldAt, oldList, oldSettings := launcherCache.at, launcherCache.list, launcherCache.settings
	launcherCache.at, launcherCache.list, launcherCache.settings = time.Now(), []proto.Launcher{{Name: "mine"}, {Name: "fixed"}}, map[string]string{}
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list, launcherCache.settings = oldAt, oldList, oldSettings
		launcherCache.Unlock()
	})
	return home
}

// The list comes from the CLI run as the launcher runs it (its environment), with hooks off, and the CLI is not
// waited for once it has answered.
func TestLauncherModelsRunsTheLauncher(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home = home
	started := time.Now()
	res, err := n.launcherModels(context.Background(), "mine", "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 20*time.Second {
		t.Fatalf("waited %v for the CLI to exit", took)
	}
	if res.Agent != "claude" || res.Locked != "" || len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine" || !res.Models[0].Default {
		t.Fatalf("got %+v", res)
	}
	args, _ := os.ReadFile(filepath.Join(home, "args.txt"))
	for _, want := range []string{"-p", "--input-format", "stream-json", "--settings"} {
		if !slices.Contains(strings.Split(string(args), "\n"), want) {
			t.Fatalf("claude got %q", args)
		}
	}
	settings, _ := os.ReadFile(filepath.Join(home, "settings.txt"))
	if !strings.Contains(string(settings), `"disableAllHooks":true`) {
		t.Fatalf("settings %q", settings)
	}

	// Kept: the second ask does not run the CLI again.
	os.Remove(filepath.Join(home, "args.txt"))
	if _, err := n.launcherModels(context.Background(), "mine", "claude", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "args.txt")); err == nil {
		t.Fatal("asked the CLI again")
	}
}

// A list past its TTL is answered at once, and the CLI is asked again in the background for the next ask (#1501): the
// picker does not wait seconds for the login shell and the CLI every ten minutes.
func TestLauncherModelsAnswersAnExpiredListAtOnce(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home = home
	if res, err := n.launcherModels(context.Background(), "mine", "claude", true); err != nil || len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine" {
		t.Fatalf("first ask: %+v %v", res, err)
	}
	// The launcher now answers slowly with another model, and the kept list has expired.
	zshrc := "mine() { sleep 2; LAUNCHER_MODEL=model-of-mine-2 command claude \"$@\" }\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	launcherModelsCache.Lock()
	for k := range launcherModelsCache.at {
		if strings.HasPrefix(k, home+"\x00") {
			launcherModelsCache.at[k] = time.Now().Add(-launcherModelsTTL - time.Minute)
		}
	}
	launcherModelsCache.Unlock()

	started := time.Now()
	res, err := n.launcherModels(context.Background(), "mine", "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("an expired list waited %v for the CLI", took)
	}
	if len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine" {
		t.Fatalf("expired ask: %+v", res)
	}
	// The background ask replaces it; asks meanwhile join it and keep answering the kept list.
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err = n.launcherModels(context.Background(), "mine", "claude", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Models) == 1 && res.Models[0].Resolved == "model-of-mine-2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background ask never replaced the list: %+v", res)
		}
		time.Sleep(50 * time.Millisecond) // sleep-ok: the poll interval of a wait bounded by the deadline above
	}

	// Too old to show: the ask waits for the CLI, as it does with no list.
	launcherModelsCache.Lock()
	for k := range launcherModelsCache.at {
		if strings.HasPrefix(k, home+"\x00") {
			launcherModelsCache.at[k] = time.Now().Add(-launcherModelsStale - time.Minute)
		}
	}
	launcherModelsCache.Unlock()
	zshrc = "mine() { LAUNCHER_MODEL=model-of-mine-3 command claude \"$@\" }\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := n.launcherModels(context.Background(), "mine", "claude", false); err != nil || len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine-3" {
		t.Fatalf("a list past launcherModelsStale was answered: %+v %v", res, err)
	}
}

// A launcher that passes its own --model would start with it whatever is picked: the list says so.
func TestLauncherModelsLockedByFixedModel(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home = home
	res, err := n.launcherModels(context.Background(), "fixed", "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Locked == "" || len(res.Models) != 1 {
		t.Fatalf("got %+v", res)
	}
}

// Asking a launcher for its models runs it, so a name this machine does not offer as a launcher is not run, unless the
// hub says its hub.json names it.
func TestLauncherModelsOnlyRunsKnownLaunchers(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	// A node directory of its own: the managed launcher's merged settings would otherwise land in the source tree.
	n.home, n.dir = home, filepath.Join(home, ".persona")
	marker := filepath.Join(home, "ran")
	f, err := os.OpenFile(filepath.Join(home, ".zshrc"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("notalauncher() { echo ran >> " + marker + " }\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	ask := func(name string, configured bool) error {
		params, _ := json.Marshal(map[string]any{"name": name, "refresh": true, "configured": configured})
		_, err := n.handle(context.Background(), "launchers.models", params)
		return err
	}
	for _, name := range []string{"notalauncher", "true"} {
		if err := ask(name, false); err == nil {
			t.Errorf("asked %s, not a launcher, for its models", name)
		}
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ran what is not a launcher: %s", b)
	}
	if err := ask("mine", false); err != nil {
		t.Fatalf("a launcher of this machine: %v", err)
	}
	_ = ask("notalauncher", true) // the hub vouches for it: asked, whatever it answers
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("a launcher in hub.json was not asked: %v", err)
	}
	_ = os.Remove(marker)
	params, _ := json.Marshal(map[string]any{"name": "notalauncher", "agent_type": "claude", "refresh": true}) // an old hub says it with this
	_, _ = n.handle(context.Background(), "launchers.models", params)
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("a launcher with its type from hub.json was not asked: %v", err)
	}
	n.setManaged([]proto.ManagedLauncher{{Name: "managed"}})
	for _, name := range []string{"claude", "traex", "managed"} {
		if err := ask(name, false); err != nil && strings.Contains(err.Error(), "unknown launcher") {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

// A runtime's launcher is a launcher too, though no shell lists it.
func TestLauncherModelsKnowsRuntimeLaunchers(t *testing.T) {
	n, _ := runtimeNode(t)
	dialRuntime(t, n, dshHello())
	var name string
	for _, l := range n.runtimes.launchers() {
		name = l.Name
	}
	if name == "" {
		t.Fatal("no runtime launcher")
	}
	params, _ := json.Marshal(map[string]any{"name": name})
	if _, err := n.handle(context.Background(), "launchers.models", params); err != nil && strings.Contains(err.Error(), "unknown launcher") {
		t.Fatalf("%s refused: %v", name, err)
	}
}

func TestLauncherModelsOtherAgents(t *testing.T) {
	n, _ := testNode(t)
	res, err := n.launcherModels(context.Background(), "traex", "", false)
	if err != nil || res.Agent != traexAgent || res.Models == nil || len(res.Models) != 0 {
		t.Fatalf("got %+v %v", res, err)
	}
	if _, err := n.launcherModels(context.Background(), "bad name;", "", false); err == nil {
		t.Fatal("ran an invalid launcher name")
	}
}

// The probe, the gateway (quota) and the --model check run the launcher too: the same rule as its models. A word this
// machine does not offer as a launcher is not run, unless the hub's hub.json names it (#1185).
func TestLauncherProbesOnlyRunKnownLaunchers(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home, n.dir = home, filepath.Join(home, ".persona")
	marker := filepath.Join(home, "ran")
	f, err := os.OpenFile(filepath.Join(home, ".zshrc"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("notalauncher() { echo ran >> " + marker + " }\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	probeCache.Lock()
	delete(probeCache.at, "notalauncher")
	probeCache.Unlock()
	ask := func(method, name string, configured bool) error {
		params, _ := json.Marshal(map[string]any{"name": name, "model": "claude-opus-5-5", "configured": configured})
		_, err := n.handle(context.Background(), method, params)
		return err
	}
	methods := []string{"launchers.probe", "launchers.http", "launchers.model.check"}
	for _, method := range methods {
		for _, name := range []string{"notalauncher", "true"} {
			if err := ask(method, name, false); err == nil || !strings.Contains(err.Error(), "unknown launcher") {
				t.Errorf("%s %s, not a launcher: %v", method, name, err)
			}
		}
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ran what is not a launcher: %s", b)
	}
	for _, method := range methods {
		if err := ask(method, "mine", false); err != nil && strings.Contains(err.Error(), "unknown launcher") {
			t.Errorf("%s of a launcher of this machine refused: %v", method, err)
		}
	}
	_ = ask("launchers.probe", "notalauncher", true) // the hub vouches for it: asked, whatever it answers
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("a launcher in hub.json was not probed: %v", err)
	}
}
