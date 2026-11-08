package extmgr

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/extwire"
)

// serveBoxedExtensionForTest answers GET /probe on its HTTP side with what it may do from where it runs.
func serveBoxedExtensionForTest() {
	ln, err := net.Listen("unix", os.Getenv("PERSONA_EXTENSION_SOCKET"))
	if err != nil {
		os.Exit(1)
	}
	var mu sync.Mutex
	var init extension.HubInfo
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hub := init
		mu.Unlock()
		out := map[string]any{"home": os.Getenv("HOME"), "proxy": os.Getenv("HTTPS_PROXY"), "hub_url": os.Getenv("PERSONA_HUB_URL"),
			"init_url": hub.URL}
		get := func(u, token string) string {
			req, _ := http.NewRequest("GET", u, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			c := &http.Client{Timeout: 10 * time.Second}
			resp, err := c.Do(req)
			if err != nil {
				return "error"
			}
			resp.Body.Close()
			return fmt.Sprint(resp.StatusCode)
		}
		out["hub"] = get(hub.URL+"/api/v1/kb-search?q=x", hub.Token)
		out["denied"] = get("http://denied.example.org/", "")
		c, err := net.DialTimeout("tcp", "192.0.2.1:80", 2*time.Second)
		if c != nil {
			c.Close()
		}
		out["dial"] = err == nil
		// An agent runtime's way to the node (permissions.agent_runtimes): its socket, and the node's own path.
		if sock := os.Getenv("PERSONA_RUNTIME_SOCKET"); sock != "" {
			out["runtime"] = askLine(sock)
		}
		if p := r.URL.Query().Get("node"); p != "" {
			out["node_direct"] = askLine(p)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "boxed", Version: "1.0.0",
		Init: func(_ context.Context, ip extension.InitializeParams, _ *pserve.Hub) (map[string]any, error) {
			mu.Lock()
			init = ip.Hub
			mu.Unlock()
			return nil, nil
		}})
}

// askLine sends a line on the unix socket at path and is the line answered, or "error".
func askLine(path string) string {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return "error"
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping\n")); err != nil {
		return "error"
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "error"
	}
	return strings.TrimSpace(line)
}

// boxedPackage is a package with this test binary in it (a sandbox sees its package only).
func boxedPackage(t *testing.T) string {
	return boxedPackageWith(t, `"hub": ["kb.read"], "network": ["allowed.example.org"]`)
}

// boxedPackageWith is boxedPackage with these permissions (the inside of the manifest's "permissions").
func boxedPackageWith(t *testing.T, permissions string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "self"), b, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "boxed"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=boxed exec \"$(dirname \"$0\")/self\" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "boxed", "name": "Boxed", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/boxed"}, "http": true, "resources": {"memory_mb": 256}},
		"activation_events": ["on_startup"], "contributes": {"api": [{"id": "x", "prefix": "boxedx"}]},
		"permissions": {%s}}`, runtime.GOOS+"-"+runtime.GOARCH, permissions)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// An extension installed from a directory runs in a sandbox by default: the hub's API through the proxy under
// egress.HubHost, nothing else but its permissions.network; the owner can switch it off and back to the default, and
// the choice goes with the extension.
func TestExtensionSandbox(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the sandbox for real is Linux's here")
	}
	if sup := sandbox.Probe(); !sup.Available() {
		t.Skip("no sandbox here: " + sup.Why)
	}
	h, store := extensionTestHub(t)
	h.extensions.sandboxes.noDefault = false
	mux := h.routes()
	// The hub's own listener, which egress.HubHost stands for: it asks the manager whose an extension's token is and
	// whether it opens the route (extensions.authorize), as the hub does.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		a := h.authorize(extwire.Authorize{Hash: hashToken(tok), Pattern: "GET /api/v1/kb-search", Method: r.Method, Path: r.URL.Path})
		switch {
		case a.Extension == "":
			w.WriteHeader(http.StatusUnauthorized)
		case !a.OK:
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	h.cfg.Listen = strings.TrimPrefix(srv.URL, "http://")
	master := h.cfg.Token

	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+boxedPackage(t)+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	code, d := call(t, mux, "POST", "/api/v1/extensions/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install: %d %v", code, d)
	}
	sb, _ := d["sandbox"].(map[string]any)
	if sb["enabled"] != true || sb["default"] != true || sb["active"] != true || sb["chosen"] != false {
		t.Fatalf("a directory's extension is sandboxed by default: %v", d["sandbox"])
	}
	probe := func() map[string]any {
		var out map[string]any
		waitLong(t, "the extension answering", func() bool {
			hp := h.extensions.get("boxed")
			if hp == nil || hp.proc == nil || !hp.proc.Ready() {
				return false
			}
			var code int
			code, out = call(t, mux, "GET", "/api/v1/extensions/boxed/probe", master, "")
			return code == 200 && out["hub"] != nil
		})
		return out
	}
	got := probe()
	data := h.extensions.reg.DataDir("boxed")
	if got["hub_url"] != "http://"+egress.HubHost || got["init_url"] != "http://"+egress.HubHost {
		t.Fatalf("the hub's API through the proxy: %v", got)
	}
	if hub, _ := got["hub"].(string); hub == "error" || hub == "502" || hub == "401" || hub == "403" || hub == "407" {
		t.Fatalf("the hub's API from the sandbox (its token, through the proxy): %v", got)
	}
	if got["denied"] != "403" || got["dial"] != false || got["home"] != data ||
		got["proxy"] != "http://127.0.0.1:3128" {
		t.Fatalf("inside the sandbox: %v", got)
	}
	if !strings.HasPrefix(h.extensions.get("boxed").socket, filepath.Join(h.extensions.root, "run", "boxed")) {
		t.Fatalf("its HTTP socket is in its run directory: %s", h.extensions.get("boxed").socket)
	}
	if st := h.extensions.sandboxes.proxyIfStarted().Stats("boxed"); st.Allowed < 1 || st.Denied < 1 {
		t.Fatalf("proxy counts %+v", st)
	}

	// Off: it runs as before sandboxes, outside and not pointed at the proxy. The switch is a request: the program it
	// restarts outlives it.
	code, d = callCanceled(t, mux, "PUT", "/api/v1/extensions/boxed/-/sandbox", master, `{"enabled": false}`)
	if sb, _ := d["sandbox"].(map[string]any); code != 200 || sb["enabled"] != false || sb["chosen"] != true {
		t.Fatalf("off: %d %v", code, d)
	}
	got = probe()
	if got["proxy"] != "" || got["hub_url"] == "http://"+egress.HubHost || got["home"] == data {
		t.Fatalf("outside the sandbox: %v", got)
	}
	// Only the hub's machine (or the admin UI) changes it.
	req := httptest.NewRequest("PUT", "/api/v1/extensions/boxed/-/sandbox", strings.NewReader(`{"enabled": true}`))
	req.RemoteAddr = "192.0.2.50:4000"
	req.Header.Set("Authorization", "Bearer "+master)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("from elsewhere: %d %s", w.Code, w.Body)
	}
	// Back to the default: on again.
	code, d = call(t, mux, "PUT", "/api/v1/extensions/boxed/-/sandbox", master, `{"enabled": null}`)
	if sb, _ := d["sandbox"].(map[string]any); code != 200 || sb["enabled"] != true || sb["chosen"] != false {
		t.Fatalf("default: %d %v", code, d)
	}
	if got = probe(); got["proxy"] != "http://127.0.0.1:3128" {
		t.Fatalf("back in the sandbox: %v", got)
	}

	// Removed: the choice goes too.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/boxed/-/sandbox", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := call(t, mux, "DELETE", "/api/v1/extensions/boxed/-/", master, ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	var s sandboxSettings
	if _, err := store.Setting(context.Background(), extensionSandboxKey, &s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.On["boxed"]; ok {
		t.Fatalf("an uninstalled extension's choice stays: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(h.extensions.root, "run", "boxed")); !os.IsNotExist(err) {
		t.Fatalf("an uninstalled extension's run directory stays: %v", err)
	}
}

// An agent runtime in a sandbox (permissions.agent_runtimes, #5413) reaches this machine's node through its own
// socket, which leads to the node's runtime.sock and nowhere else: the node's own path stays out of sight, a node
// that made its socket anew is still reached, and an extension without the permission has no such socket.
func TestExtensionSandboxAgentRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the sandbox for real is Linux's here")
	}
	if sup := sandbox.Probe(); !sup.Available() {
		t.Skip("no sandbox here: " + sup.Why)
	}
	// The node: answers each line with "node <n>", n its listener's (so a socket made anew shows).
	nodeSock := filepath.Join(t.TempDir(), "runtime.sock")
	listen := func(n int) net.Listener {
		_ = os.Remove(nodeSock)
		ln, err := net.Listen("unix", nodeSock)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					if _, err := bufio.NewReader(c).ReadString('\n'); err == nil {
						fmt.Fprintf(c, "node %d\n", n)
					}
				}()
			}
		}()
		return ln
	}
	ln := listen(1)
	old := nodeRuntimeSocket
	nodeRuntimeSocket = func() string { return nodeSock }
	t.Cleanup(func() { nodeRuntimeSocket = old })

	h, _ := extensionTestHub(t)
	h.extensions.sandboxes.noDefault = false
	mux := h.routes()
	master := h.cfg.Token
	install := func(perms string) {
		t.Helper()
		code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+boxedPackageWith(t, perms)+`"}`)
		if code != 200 {
			t.Fatalf("preview: %d %v", code, pv)
		}
		code, d := call(t, mux, "POST", "/api/v1/extensions/-/install", master,
			fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
		if code != 200 {
			t.Fatalf("install: %d %v", code, d)
		}
	}
	probe := func() map[string]any {
		t.Helper()
		var out map[string]any
		waitLong(t, "the extension answering", func() bool {
			hp := h.extensions.get("boxed")
			if hp == nil || hp.proc == nil || !hp.proc.Ready() {
				return false
			}
			var code int
			code, out = call(t, mux, "GET", "/api/v1/extensions/boxed/probe?node="+nodeSock, master, "")
			return code == 200 && out["dial"] != nil
		})
		return out
	}

	install(`"agent_runtimes": true`)
	got := probe()
	sock := filepath.Join(h.extensions.root, "run", "boxed", "runtime.sock")
	if got["runtime"] != "node 1" {
		t.Fatalf("the node through its socket: %v", got)
	}
	if got["node_direct"] != "error" {
		t.Fatalf("the node's own path is seen from the sandbox: %v", got)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("its socket in its run directory, this user's only: %v %v", fi, err)
	}
	// The node made its socket anew (it restarted): the next connection reaches the new one.
	_ = ln.Close()
	ln = listen(2)
	defer ln.Close()
	if got := probe(); got["runtime"] != "node 2" {
		t.Fatalf("after the node restarted: %v", got)
	}

	// Without the permission: no socket, nothing set.
	if code, out := call(t, mux, "DELETE", "/api/v1/extensions/boxed/-/", master, ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	install(`"network": ["allowed.example.org"]`)
	if got := probe(); got["runtime"] != nil {
		t.Fatalf("a runtime socket without the permission: %v", got)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("its socket without the permission: %v", err)
	}
}

// callCanceled is call with the request's context canceled once it is answered, as a real server does.
func callCanceled(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	cancel()
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// What decides the default: where it came from, and whether the hub trusts that market's signing key; the extensions
// installed before sandboxes keep running without one.
func TestExtensionSandboxDefault(t *testing.T) {
	h, store := extensionTestHub(t)
	ps := h.extensions
	ps.sandboxes.noDefault = false
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key := "ed25519:" + base64.StdEncoding.EncodeToString(pub)
	ms := h.marketsState()
	ms.mu.Lock()
	ms.loaded = true
	ms.byID = map[string]*marketState{
		"mine":    {rec: extension.MarketRecord{ID: "mine", SigningKey: key}},
		"nokey":   {rec: extension.MarketRecord{ID: "nokey"}},
		"another": {rec: extension.MarketRecord{ID: "another", SigningKey: "ed25519:" + base64.StdEncoding.EncodeToString(make([]byte, 32))}},
	}
	ms.mu.Unlock()
	ext := func(id, source, origin string) *extension.Extension {
		return &extension.Extension{Record: extension.Record{ID: id, Source: source, Origin: origin},
			Manifest: &extension.Manifest{ID: id, Runtime: &extension.Runtime{}}}
	}
	ctx := context.Background()
	for _, c := range []struct {
		p       *extension.Extension
		applies bool
		on      bool
	}{
		{ext("a", extension.SourceInstalled, "dir:/x"), true, true},
		{ext("b", extension.SourceInstalled, "file:/x.tar.gz"), true, true},
		{ext("c", extension.SourceInstalled, "market:mine/c"), true, true}, // not trusted yet
		{ext("d", extension.SourceInstalled, "market:nokey/d"), true, true},
		{ext("e", extension.SourceBuiltin, ""), false, false},
		{ext("f", extension.SourceHubJSON, ""), false, false},
	} {
		st := ps.sandboxOf(ctx, c.p)
		if st.applies != c.applies || st.Enabled != c.on || st.Default != c.on {
			t.Errorf("%s (%s %s): %+v", c.p.Record.ID, c.p.Record.Source, c.p.Record.Origin, st)
		}
	}
	h.cfg.TrustedExtensionMarkets = []string{key}
	if st := ps.sandboxOf(ctx, ext("c", extension.SourceInstalled, "market:mine/c")); !st.Enabled || st.Trusted {
		t.Errorf("a legacy nonofficial pin must not grant a sandbox exemption (key): %+v", st)
	}
	h.cfg.TrustedExtensionMarkets = []string{keyIDOf(key)}
	if st := ps.sandboxOf(ctx, ext("c", extension.SourceInstalled, "market:mine/c")); !st.Enabled || st.Trusted {
		t.Errorf("a legacy nonofficial pin must not grant a sandbox exemption (key id): %+v", st)
	}
	if st := ps.sandboxOf(ctx, ext("x", extension.SourceInstalled, "market:another/x")); !st.Enabled {
		t.Errorf("a market signed by a key nobody trusts: %+v", st)
	}
	for _, id := range officialMarketKeys {
		if len(id) != 16 {
			t.Errorf("official key id %q", id)
		}
	}
	remote := ext("r", extension.SourceInstalled, "remote:rpc=/x.sock")
	remote.Record.Remote = &extension.Remote{RPCSocket: "/x.sock"}
	if st := ps.sandboxOf(ctx, remote); st.applies {
		t.Errorf("an extension running elsewhere: %+v", st)
	}

	// The first start with sandboxes: what is installed is recorded off, once.
	ps.sandboxes.noDefault = true
	mux := h.routes()
	master := h.cfg.Token
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+boxedPackage(t)+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
	ps.sandboxes.noDefault = false
	ps.sandboxes.settings = sandboxSettings{}
	if err := store.PutSetting(ctx, extensionSandboxKey, sandboxSettings{}); err != nil {
		t.Fatal(err)
	}
	ps.loadSandboxes(ctx)
	if on, ok := ps.sandboxes.settings.On["boxed"]; !ok || on || !ps.sandboxes.settings.Seeded {
		t.Errorf("installed before sandboxes: %+v", ps.sandboxes.settings)
	}
	var saved sandboxSettings
	_, _ = store.Setting(ctx, extensionSandboxKey, &saved)
	if !saved.Seeded || saved.On["boxed"] {
		t.Errorf("not saved: %+v", saved)
	}
	// Seeded once: an extension installed after it follows the default.
	ps.loadSandboxes(ctx)
	if st := ps.sandboxOf(ctx, ext("later", extension.SourceInstalled, "dir:/x")); !st.Enabled {
		t.Errorf("installed after: %+v", st)
	}
}

// With a sandbox on and none on the machine, it runs as it is but pointed at the proxy, and says why.
func TestExtensionSandboxMissing(t *testing.T) {
	old := sandboxProbe
	sandboxProbe = func() sandbox.Support {
		return sandbox.Support{OS: "linux", Why: "机器上没有 bwrap（bubblewrap）"}
	}
	t.Cleanup(func() { sandboxProbe = old })
	h, _ := extensionTestHub(t)
	ps := h.extensions
	ps.sandboxes.noDefault = false
	p := &extension.Extension{Record: extension.Record{ID: "notes", Source: extension.SourceInstalled, Origin: "dir:/x",
		Granted: extension.Permissions{Network: []string{"api.example.com"}}},
		Manifest: &extension.Manifest{ID: "notes", Runtime: &extension.Runtime{}}}
	p.Full = p.Manifest
	st := ps.sandboxOf(context.Background(), p)
	if !st.Enabled || st.Active || !strings.Contains(st.Why, "bwrap") {
		t.Fatalf("%+v", st)
	}
	s := host.Spec{StartEnv: func() []string { return []string{"PERSONA_HUB_URL=http://127.0.0.1:1"} }}
	if err := ps.confine(context.Background(), p, &hubExtension{}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Wrap != nil {
		t.Fatal("wrapped without a sandbox")
	}
	env := strings.Join(s.StartEnv(), " ")
	if !strings.Contains(env, "HTTPS_PROXY=http://ext:") || !strings.Contains(env, "PERSONA_HUB_URL=http://127.0.0.1:1") {
		t.Fatalf("env %s", env)
	}
	if v := ps.view(context.Background(), p); v.Sandbox == nil || !strings.Contains(strings.Join(v.Warnings, " "), "bwrap") {
		t.Fatalf("view %+v", v)
	}
}

// Choices set and forgotten from several goroutines at once: each saves a copy made under the lock (go test -race),
// and the choices saved last are the ones in memory.
func TestExtensionSandboxConcurrentChoices(t *testing.T) {
	h, store := extensionTestHub(t)
	ps := h.extensions
	ctx := context.Background()
	on := true
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 20 {
				id := fmt.Sprintf("x%d-%d", i, j)
				p := &extension.Extension{Record: extension.Record{ID: id, Source: extension.SourceInstalled, Origin: "dir:/x"},
					Manifest: &extension.Manifest{ID: id, Runtime: &extension.Runtime{}}}
				// Not in the registry: setSandbox's restart forgets the choice again (forgetSandbox).
				if err := ps.setSandbox(ctx, p, &on, "test"); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	var saved sandboxSettings
	if _, err := store.Setting(ctx, extensionSandboxKey, &saved); err != nil {
		t.Fatal(err)
	}
	ps.sandboxes.mu.Lock()
	want, _ := json.Marshal(ps.sandboxes.settings)
	ps.sandboxes.mu.Unlock()
	if got, _ := json.Marshal(saved); string(got) != string(want) {
		t.Fatalf("saved %s\nwant %s", got, want)
	}
}
