package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The Git panel's writes and pull request (#6121) reach the node as the method they name with what was sent; a
// watch's token reaches none of them; a refusal comes back with its code and the files it lists.
func TestGitWriteRoutes(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	hubSide, nodeSide := net.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubSide), rpc.New("n", nodeSide)
	t.Cleanup(func() { hubSide.Close(); nodeSide.Close() })
	var mu sync.Mutex
	got := map[string]map[string]any{}
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var body map[string]any
		_ = json.Unmarshal(params, &body)
		mu.Lock()
		got[method] = body
		mu.Unlock()
		if method == "git.switch" && body["branch"] == "busy" {
			return nil, &rpc.Error{Code: "git_conflict", Message: "uncommitted changes", Data: json.RawMessage(`{"files":["app.txt"]}`)}
		}
		return map[string]any{"ok": method}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = hubPeer.Serve(ctx, hubSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()
	m := newMachine(h, MachineConfig{ID: "mac", Name: "Sample computer"})
	m.peer = hubPeer
	h.byID["mac"] = m
	handler := h.routes()
	_, watch := call(t, handler, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Watch","kind":"watch"}`)
	base := "/api/v1/machines/mac/git/"
	for _, c := range []struct{ verb, path, body, method, key string }{
		{"GET", "branches?cwd=/work/web", "", "git.branches", "cwd"},
		{"GET", "pr?cwd=/work/web", "", "git.pr", "cwd"},
		{"POST", "stage", `{"cwd":"/work/web","paths":["a.txt"],"unstage":true}`, "git.stage", "unstage"},
		{"POST", "commit", `{"cwd":"/work/web","message":"Fix"}`, "git.commit", "message"},
		{"POST", "push", `{"cwd":"/work/web","set_upstream":true}`, "git.push", "set_upstream"},
		{"POST", "switch", `{"cwd":"/work/web","branch":"main"}`, "git.switch", "branch"},
		{"POST", "stash", `{"cwd":"/work/web","op":"pop","index":1,"oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "git.stash", "oid"},
		{"POST", "pr", `{"cwd":"/work/web","title":"Fix","base":"main"}`, "git.pr_create", "title"},
		{"POST", "pr/merge", `{"cwd":"/work/web","number":7,"method":"squash","auto":true}`, "git.pr_merge", "auto"},
	} {
		code, res := call(t, handler, c.verb, base+c.path, h.cfg.Token, c.body)
		mu.Lock()
		params := got[c.method]
		mu.Unlock()
		if code != http.StatusOK || res["ok"] != c.method || params == nil || params[c.key] == nil || params["cwd"] != "/work/web" {
			t.Fatalf("%s %s: %d %v, node got %v", c.verb, c.path, code, res, params)
		}
		if c.method == "git.stash" && params["oid"] != strings.Repeat("a", 40) {
			t.Fatalf("stash identity lost at HTTP/RPC boundary: %v", params)
		}
		if code, _ := call(t, handler, c.verb, base+c.path, watch["token"].(string), c.body); code != http.StatusForbidden {
			t.Fatalf("a watch's token %s %s: %d", c.verb, c.path, code)
		}
	}
	code, res := call(t, handler, "POST", base+"switch", h.cfg.Token, `{"cwd":"/work/web","branch":"busy"}`)
	files, _ := res["files"].([]any)
	if code != http.StatusConflict || res["code"] != "git_conflict" || len(files) != 1 || files[0] != "app.txt" {
		t.Fatalf("refusal: %d %v", code, res)
	}
}
