package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestGitPullRouteAndPermissions(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	hubSide, nodeSide := net.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubSide), rpc.New("n", nodeSide)
	t.Cleanup(func() { hubSide.Close(); nodeSide.Close() })
	var calls atomic.Int32
	var failed atomic.Bool
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		calls.Add(1)
		var body map[string]any
		_ = json.Unmarshal(params, &body)
		if method != "git.pull" || body["cwd"] != "/work/sample" {
			return nil, fmt.Errorf("转发参数不正确：%s, %s", method, params)
		}
		if failed.Load() {
			return nil, &rpc.Error{Code: "uncommitted_changes", Message: "the working tree has uncommitted changes: commit or stash them first"}
		}
		return map[string]any{"status": map[string]any{"is_repo": true, "behind": 0}, "updated": true}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = hubPeer.Serve(ctx, hubSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()
	m := newMachine(h, MachineConfig{ID: "mac", Name: "Sample computer"})
	m.peer = hubPeer
	h.byID["mac"] = m
	handler := h.routes()
	path := "/api/v1/machines/mac/git/pull"
	code, result := call(t, handler, "POST", path, h.cfg.Token, `{"cwd":"/work/sample"}`)
	if code != http.StatusOK || result["updated"] != true || calls.Load() != 1 {
		t.Fatalf("拉取路由失败：%d, %v, calls=%d", code, result, calls.Load())
	}
	code, _ = call(t, handler, "POST", path, h.cfg.Token, `not-json`)
	if code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatalf("无效 JSON 不应到达 node：%d, calls=%d", code, calls.Load())
	}
	_, watch := call(t, handler, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Watch","kind":"watch"}`)
	code, _ = call(t, handler, "POST", path, watch["token"].(string), `{"cwd":"/work/sample"}`)
	if code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("answer 角色不应执行拉取：%d, calls=%d", code, calls.Load())
	}
	// a refusal the node coded (#1888) is a 409 in the client's language, with the node's words as the detail
	failed.Store(true)
	for lang, want := range map[string]string{"en": "uncommitted changes", "ja": "コミットしていない変更", "zh-Hans": "未提交的改动"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"cwd":"/work/sample"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Accept-Language", lang)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != http.StatusConflict || out["code"] != "uncommitted_changes" || !strings.Contains(fmt.Sprint(out["error"]), want) ||
			!strings.Contains(fmt.Sprint(out["detail"]), "commit or stash them first") {
			t.Fatalf("%s: %d, %v", lang, w.Code, out)
		}
	}
}
