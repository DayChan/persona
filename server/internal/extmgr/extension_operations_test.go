package extmgr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/extension"
)

// waitOp waits until operation id is in one of states, and returns it.
func waitOp(t *testing.T, h *Manager, id string, states ...string) ExtensionOperation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		op := h.opByID(id)
		if op == nil {
			t.Fatalf("operation %s is gone", id)
		}
		v := h.snapshot(op)
		for _, s := range states {
			if v.State == s {
				return v
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s is %s, not %v: %+v", id, v.State, states, v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stepState(v ExtensionOperation, id string) ExtensionOpStep {
	for _, s := range v.Steps {
		if s.ID == id {
			return s
		}
	}
	return ExtensionOpStep{}
}

func opID(t *testing.T, code int, out map[string]any) string {
	t.Helper()
	if code != http.StatusAccepted {
		t.Fatalf("not 202: %d %v", code, out)
	}
	op, _ := out["operation"].(map[string]any)
	id, _ := op["id"].(string)
	if id == "" {
		t.Fatalf("no operation: %v", out)
	}
	return id
}

// TestExtensionOperations: an install from a market in steps (#3277): previewed in the background, waiting for the
// user, installed and started; a second time from the cache; a stalled download fails at its step and is retried; a
// failure after the user agreed goes on by itself when retried; an uninstall in steps.
func TestExtensionOperations(t *testing.T) {
	h, phone, reader := marketHub(t)
	mux := h.routes()
	dir := uiPackage(t)
	v1 := tarDir(t, dir)
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: map[string][]byte{"example-ui-1.0.0.tar.gz": v1}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	storage := httptest.NewTLSServer(gh)
	defer storage.Close()
	gh.storage = "https://storage.example.com"
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "storage.example.com:443" {
			addr = storage.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	h.extensions.markets.client, h.extensions.markets.githubAPI = &http.Client{Transport: tr}, srv.URL
	gh.files[extension.IndexFile] = marketIndex(t, "Example plugins", marketEntry(t, dir, "release:v1/example-ui-1.0.0.tar.gz", v1))
	_, mk := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, `{"spec": "github:example/plugins"}`)
	market := mk["id"].(string)
	preview := fmt.Sprintf(`{"market": %q, "extension": "example-ui", "async": true}`, market)

	if code, _ := call(t, mux, "GET", "/api/v1/extensions/-/operations", reader, ""); code != http.StatusForbidden {
		t.Fatalf("an answer-only device lists them: %d", code)
	}

	// Previewed in the background: it stops at the consent, with every step before it done.
	id := opCall(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, preview)
	v := waitOp(t, h, id, opNeedsConsent, opFailed)
	if v.State != opNeedsConsent || v.Extension != "example-ui" || v.Version != "1.0.0" || v.Market != market || v.PreviewID == "" {
		t.Fatalf("%+v", v)
	}
	for _, s := range []string{extension.StepDownload, extension.StepVerify, extension.StepUnpack, extension.StepCheck} {
		if st := stepState(v, s); st.State != extension.StepDone {
			t.Fatalf("%s: %+v", s, st)
		}
	}
	if d := stepState(v, extension.StepDownload); d.Bytes != int64(len(v1)) || d.Cached {
		t.Fatalf("download: %+v", d)
	}
	code, one := call(t, mux, "GET", "/api/v1/extensions/-/operations/"+id, phone, "")
	pv, _ := one["preview"].(map[string]any)
	if code != 200 || pv["preview_id"] != v.PreviewID {
		t.Fatalf("its preview: %d %v", code, one)
	}
	_, list := call(t, mux, "GET", "/api/v1/extensions/-/operations", phone, "")
	if ops, _ := list["operations"].([]any); len(ops) != 1 {
		t.Fatalf("listed: %v", list)
	}

	// Agreed to: installed and started in the background, in the same operation.
	body := fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q, "async": true}`, pv["preview_id"], pv["permissions_digest"])
	if got := opCall(t, mux, "POST", "/api/v1/extensions/-/install", phone, body); got != id {
		t.Fatalf("another operation: %s", got)
	}
	v = waitOp(t, h, id, opSucceeded, opFailed)
	if v.State != opSucceeded || stepState(v, extension.StepStart).State != extension.StepDone || stepState(v, extension.StepNodes).State != extension.StepSkipped || v.EndedAt == nil {
		t.Fatalf("%+v", v)
	}
	if h.extensions.reg.Get("example-ui") == nil {
		t.Fatal("not installed")
	}

	// Uninstalled in steps; then previewed again: the package comes from the cache.
	id = opCall(t, mux, "DELETE", "/api/v1/extensions/example-ui/-/?async=1", phone, "")
	if v = waitOp(t, h, id, opSucceeded, opFailed); v.State != opSucceeded || v.Kind != opUninstall || stepState(v, extension.StepRemove).State != extension.StepDone {
		t.Fatalf("%+v", v)
	}
	if code, _ := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+id, phone, ""); code != http.StatusNoContent || h.opByID(id) != nil {
		t.Fatalf("a finished uninstall taken off the list: %d", code)
	}
	id = opCall(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, preview)
	if v = waitOp(t, h, id, opNeedsConsent, opFailed); !stepState(v, extension.StepDownload).Cached {
		t.Fatalf("not from the cache: %+v", v)
	}
	// Turned down: canceled, its preview dropped.
	if code, _ := call(t, mux, "DELETE", "/api/v1/extensions/-/operations/"+id, phone, ""); code != http.StatusNoContent {
		t.Fatalf("cancel: %d", code)
	}
	if v = waitOp(t, h, id, opCanceled); h.extensions.reg.PreviewOf(v.PreviewID) != nil {
		t.Fatal("its preview is kept")
	}

	// A download that stalls fails at its step, and a retry downloads it again.
	defer func(was time.Duration) { extension.StallAfter = was }(extension.StallAfter)
	extension.StallAfter = 300 * time.Millisecond
	_ = os.RemoveAll(h.packageCache())
	gh.mu.Lock()
	gh.stall = true
	gh.mu.Unlock()
	id = opCall(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, preview)
	v = waitOp(t, h, id, opFailed, opNeedsConsent)
	if v.State != opFailed || v.ErrorCode != "download_stalled" || v.Step != extension.StepDownload || stepState(v, extension.StepDownload).State != extension.StepFailed || !v.Retryable || v.Error == "" {
		t.Fatalf("%+v", v)
	}
	if d := stepState(v, extension.StepDownload); d.Bytes == 0 {
		t.Fatalf("its bytes so far: %+v", d)
	}
	gh.mu.Lock()
	gh.stall = false
	gh.mu.Unlock()
	next := opCall(t, mux, "POST", "/api/v1/extensions/-/operations/"+id+"/retry", phone, "")
	if v = waitOp(t, h, next, opNeedsConsent, opFailed); v.State != opNeedsConsent {
		t.Fatalf("retried: %+v", v)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/operations/"+id+"/retry", phone, ""); code != http.StatusConflict || out["code"] != "not_retryable" || h.snapshot(h.opByID(id)).Retryable {
		t.Fatalf("the stalled one retried twice: %d %v", code, out)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/extensions/-/operations/"+next+"/retry", phone, ""); code != http.StatusConflict {
		t.Fatalf("a retry of one that did not fail: %d", code)
	}

	// Agreed to, then refused when installing: a retry goes on without asking again (the same digest).
	var refuse atomic.Bool
	refuse.Store(true)
	was := h.extensions.reg.CanEnable
	h.extensions.reg.CanEnable = func(ctx context.Context, source string, m *extension.Manifest) error {
		if refuse.Load() {
			return errors.New("not now")
		}
		if was != nil {
			return was(ctx, source, m)
		}
		return nil
	}
	_, one = call(t, mux, "GET", "/api/v1/extensions/-/operations/"+next, phone, "")
	pv = one["preview"].(map[string]any)
	code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != http.StatusBadRequest {
		t.Fatalf("refused: %d %v", code, out)
	}
	if v = waitOp(t, h, next, opFailed); v.Step != extension.StepInstall || v.ErrorCode != "install_failed" {
		t.Fatalf("%+v", v)
	}
	refuse.Store(false)
	again := opCall(t, mux, "POST", "/api/v1/extensions/-/operations/"+next+"/retry", phone, "")
	if v = waitOp(t, h, again, opSucceeded, opFailed, opNeedsConsent); v.State != opSucceeded || stepState(v, extension.StepConsent).State != extension.StepDone {
		t.Fatalf("went on by itself: %+v", v)
	}

	// A synchronous call (an older app) is an operation too, and names it.
	code, out = call(t, mux, "DELETE", "/api/v1/extensions/example-ui/-/", phone, "")
	if code != 200 || out["ok"] != true {
		t.Fatalf("uninstall: %d %v", code, out)
	}
	code, out = call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "extension": "example-ui"}`, market))
	if code != 200 || out["preview_id"] == nil || out["operation_id"] == nil {
		t.Fatalf("synchronous preview: %d %v", code, out)
	}
}

// opCall makes a call that answers 202 with an operation, and is its id.
func opCall(t *testing.T, h http.Handler, method, path, token, body string) string {
	t.Helper()
	code, out := call(t, h, method, path, token, body)
	return opID(t, code, out)
}

// TestMarketClientGitHubProxy: market requests to GitHub's hosts go through hub.json's github.proxy, others as the
// environment says (#3277: a package from GitHub stalled for minutes without it).
func TestMarketClientGitHubProxy(t *testing.T) {
	h, _, _ := marketHub(t)
	h.cfg.GitHub = &github.Config{Egress: connector.Egress{Proxy: "http://127.0.0.1:3128"}}
	h.extensions.markets.client = nil
	tr := h.marketClient().Transport.(*http.Transport)
	check := func(env string) {
		t.Helper()
		prev := envProxy
		t.Cleanup(func() { envProxy = prev })
		envProxy = func(*http.Request) (*url.URL, error) {
			if env == "" {
				return nil, nil
			}
			return &url.URL{Scheme: "http", Host: env}, nil
		}
		for host, want := range map[string]string{"api.github.com": "127.0.0.1:3128", "objects.githubusercontent.com": "127.0.0.1:3128",
			"ghcr.io": "127.0.0.1:3128", "pkg-containers.githubusercontent.com": "127.0.0.1:3128",
			"github.com": "127.0.0.1:3128", "example.com": env, "registry-mirror.example.com": env, "notgithub.com": env, "notghcr.io": env} {
			req, _ := http.NewRequest("GET", "https://"+host+"/x", nil)
			u, err := tr.Proxy(req)
			got := ""
			if u != nil {
				got = u.Host
			}
			if err != nil || got != want {
				t.Errorf("env proxy %q, %s: %q %v, want %q", env, host, got, err, want)
			}
		}
	}
	check("")               // no proxy in the environment: only GitHub's hosts go through github.proxy
	check("127.0.0.1:7897") // one in the environment: the others take it, GitHub's still github.proxy
}

// failedOp puts a failed operation whose retry counts its calls and answers with next (or err) into h's list.
func failedOp(h *Manager, calls *atomic.Int32, next func() *extOp, err error) *extOp {
	now := time.Now()
	op := &extOp{ofMarket: true, v: ExtensionOperation{ID: newOpID(), Kind: opInstall, Extension: "example-ui", State: opFailed,
		StartedAt: now, UpdatedAt: now, EndedAt: &now, Retryable: true}}
	op.retry = func() (*extOp, error) {
		calls.Add(1)
		if err != nil {
			return nil, err
		}
		return next(), nil
	}
	ps := h.extensions
	ps.ops.mu.Lock()
	if ps.ops.byID == nil {
		ps.ops.byID = map[string]*extOp{}
	}
	ps.ops.byID[op.v.ID] = op
	ps.ops.mu.Unlock()
	return op
}

// TestExtensionOpRetryOnce: a failed operation is retried once; a second POST (a double tap, a second app) is 409
// not_retryable and starts nothing, the old one says it is not retryable; a retry that could not start can be tried
// again; of two at the same time exactly one starts.
func TestExtensionOpRetryOnce(t *testing.T) {
	h, phone, _ := marketHub(t)
	mux := h.routes()
	started := func() *extOp { return h.startOp(opInstall, "example-ui", "", "", true) }
	path := func(op *extOp) string { return "/api/v1/extensions/-/operations/" + op.v.ID + "/retry" }

	var calls atomic.Int32
	op := failedOp(h, &calls, started, nil)
	opCall(t, mux, "POST", path(op), phone, "")
	code, out := call(t, mux, "POST", path(op), phone, "")
	if code != http.StatusConflict || out["code"] != "not_retryable" {
		t.Fatalf("a second retry: %d %v", code, out)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("retried %d times", n)
	}
	if v := h.snapshot(op); v.Retryable || v.State != opFailed {
		t.Fatalf("the old one: %+v", v)
	}

	// A retry that could not start gives it back: it can be tried again.
	calls.Store(0)
	op = failedOp(h, &calls, nil, errors.New("not now"))
	if code, out := call(t, mux, "POST", path(op), phone, ""); code != http.StatusBadRequest || out["code"] != "retry_failed" {
		t.Fatalf("a retry that fails: %d %v", code, out)
	}
	if v := h.snapshot(op); !v.Retryable {
		t.Fatalf("not retryable after a retry that did not start: %+v", v)
	}
	if code, _ := call(t, mux, "POST", path(op), phone, ""); code != http.StatusBadRequest || calls.Load() != 2 {
		t.Fatalf("tried again: %d, %d calls", code, calls.Load())
	}

	// Two at the same time: one 202, one 409.
	calls.Store(0)
	op = failedOp(h, &calls, started, nil)
	codes := make(chan int, 2)
	for range 2 {
		go func() {
			code, _ := call(t, mux, "POST", path(op), phone, "")
			codes <- code
		}()
	}
	got := map[int]int{}
	for range 2 {
		got[<-codes]++
	}
	if got[http.StatusAccepted] != 1 || got[http.StatusConflict] != 1 || calls.Load() != 1 {
		t.Fatalf("two at once: %v, %d calls", got, calls.Load())
	}
}
