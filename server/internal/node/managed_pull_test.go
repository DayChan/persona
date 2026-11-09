package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// pullHub answers the node's launchers.pull like the manager: the providers it has, by name, or its refusal.
type pullHub struct {
	mu     sync.Mutex
	asked  []map[string]any
	answer func(name string) (any, error)
}

func withPullHub(t *testing.T, n *Node, h *pullHub) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "launchers.pull" {
			return nil, errors.New("unknown method " + method)
		}
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		h.mu.Lock()
		h.asked = append(h.asked, p)
		h.mu.Unlock()
		name, _ := p["name"].(string)
		return h.answer(name)
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
}

func (h *pullHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

// A catalog entry names the provider without its secrets: personad launch fetches them once, keeps them while the
// provider is unchanged, and fetches again after it changed.
func TestCatalogProviderIsPulledOnceAndKept(t *testing.T) {
	n := &Node{bin: "/usr/local/bin/personad"}
	key := "sk-test-pulled-1"
	h := &pullHub{answer: func(name string) (any, error) {
		return map[string]any{"launcher": proto.ManagedLauncher{Name: name, BaseURL: "https://gw.example.com", Model: "m1", APIKey: key}}, nil
	}}
	withPullHub(t, n, h)
	entry := proto.ManagedLauncher{Name: "box", BaseURL: "https://gw.example.com", Model: "m1", Delivery: proto.DeliveryOnDemand, Rev: "r1"}
	n.setCatalog([]proto.ManagedLauncher{entry.Public()})
	if got := n.launcherCommand("box"); !strings.HasSuffix(got, " launch box") {
		t.Fatalf("a catalog provider does not run through personad launch: %q", got)
	}
	if l, _ := n.managedLauncher("box"); l.APIKey != "" || !l.Withheld {
		t.Fatalf("secrets before any pull: %+v", l)
	}
	before := n.executionFingerprint("box", modelExecution{})
	env := n.launcherEnv("box")
	if env["error"] != nil || !strings.Contains(strings.Join(env["env"].([]string), "\n"), "ANTHROPIC_API_KEY="+key) {
		t.Fatalf("personad launch after a pull: %v", env)
	}
	if after := n.executionFingerprint("box", modelExecution{}); after != before {
		t.Fatal("a pull changed the execution's fingerprint")
	}
	n.launcherEnv("box")
	if h.count() != 1 {
		t.Fatalf("pulled %d times", h.count())
	}
	// The same catalog again keeps what was pulled; a new rev drops it.
	n.setCatalog([]proto.ManagedLauncher{entry.Public()})
	n.launcherEnv("box")
	if h.count() != 1 {
		t.Fatal("an unchanged catalog dropped what was pulled")
	}
	entry.Rev, key = "r2", "sk-test-pulled-2"
	n.setCatalog([]proto.ManagedLauncher{entry.Public()})
	if env := n.launcherEnv("box"); !strings.Contains(strings.Join(env["env"].([]string), "\n"), "ANTHROPIC_API_KEY=sk-test-pulled-2") || h.count() != 2 {
		t.Fatalf("a changed provider was not pulled again (%d): %v", h.count(), env)
	}
	// Out of scope now: not named any more, its secrets gone.
	n.setCatalog(nil)
	if _, ok := n.managedLauncher("box"); ok {
		t.Fatal("a provider out of the catalog is still known")
	}
}

// The manager's refusal reaches personad launch as it said it; with no manager, the words say how to keep a copy.
func TestCatalogPullRefusedOrAway(t *testing.T) {
	n := &Node{bin: "/usr/local/bin/personad"}
	n.setCatalog([]proto.ManagedLauncher{{Name: "box", Model: "m1", Withheld: true, Rev: "r1"}})
	if e := n.launcherEnv("box")["error"]; e == nil || !strings.Contains(e.(string), "Keep a copy") {
		t.Fatalf("no manager: %v", e)
	}
	h := &pullHub{answer: func(string) (any, error) {
		return nil, &rpc.Error{Code: proto.CodeLauncherOutOfScope, Message: "box is not set up for mac"}
	}}
	withPullHub(t, n, h)
	if e := n.launcherEnv("box")["error"]; e == nil || !strings.Contains(e.(string), "not set up for mac") {
		t.Fatalf("refused: %v", e)
	}
	err := n.ensureManaged(context.Background(), "box")
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeLauncherOutOfScope {
		t.Fatalf("the refusal's code is lost: %v", err)
	}
}

// A provider that skips computers with their own service says what this one has; one that does not, nothing.
func TestCatalogPullReportsOwnServiceOnlyWhenAsked(t *testing.T) {
	n := &Node{bin: "/usr/local/bin/personad", home: t.TempDir()}
	ownServiceCache.Lock()
	ownServiceCache.at = map[string]time.Time{n.home + "\x00claude": time.Now()}
	ownServiceCache.st = map[string]proto.OwnServiceState{n.home + "\x00claude": {Agent: "claude", Known: true, Own: true, Via: "login"}}
	ownServiceCache.Unlock()
	t.Cleanup(func() {
		ownServiceCache.Lock()
		ownServiceCache.at, ownServiceCache.st = nil, nil
		ownServiceCache.Unlock()
	})
	h := &pullHub{answer: func(name string) (any, error) {
		return map[string]any{"launcher": proto.ManagedLauncher{Name: name, Model: "m1", APIKey: "k"}}, nil
	}}
	withPullHub(t, n, h)
	n.setCatalog([]proto.ManagedLauncher{
		{Name: "skip", Model: "m1", OwnService: proto.OwnServiceSkip, Delivery: proto.DeliveryOnDemand, Withheld: true, Rev: "1"},
		{Name: "use", Model: "m1", OwnService: proto.OwnServiceUse, Delivery: proto.DeliveryOnDemand, Withheld: true, Rev: "1"},
	})
	for _, name := range []string{"skip", "use"} {
		if err := n.ensureManaged(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if own, ok := h.asked[0]["own_service"].(map[string]any); !ok || own["own"] != true || own["via"] != "login" {
		t.Fatalf("skip's pull: %v", h.asked[0])
	}
	if _, ok := h.asked[1]["own_service"]; ok {
		t.Fatalf("use's pull reported the own service: %v", h.asked[1])
	}
}

func TestOwnLauncherCountsOnlyTheUsersOwn(t *testing.T) {
	list := []proto.Launcher{{Name: "claude"}, {Name: "codex", Agent: "codex"}, {Name: "dsh_x", Source: "dsh"}}
	if ownLauncher(list, "claude") || ownLauncher(list, "codex") {
		t.Fatal("the plain CLIs or a runtime's launcher count as a service of one's own")
	}
	list = append(list, proto.Launcher{Name: "cc_work", Source: "zshrc"}, proto.Launcher{Name: "cx", Agent: "codex", Source: "zshrc"})
	if !ownLauncher(list, "claude") || !ownLauncher(list, "codex") {
		t.Fatal("an rc-file launcher is a service of one's own")
	}
}
