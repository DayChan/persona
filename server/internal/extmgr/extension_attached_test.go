package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/i18n"
)

// notesProgram is a generic extension running on its own on a unix socket, as the id and version it is started with.
type notesProgram struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startNotes(t *testing.T, sock, version string) *notesProgram {
	t.Helper()
	return startProgram(t, sock, "example-notes", version)
}

func startProgram(t *testing.T, sock, id, version string) *notesProgram {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	n := &notesProgram{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.mu.Lock()
			n.conns = append(n.conns, c)
			n.mu.Unlock()
			go func() {
				defer c.Close()
				manifest := json.RawMessage(fmt.Sprintf(`{"manifest_version": 1, "id": %q, "version": %q}`, id, version))
				_ = serve.Serve(context.Background(), c, c, serve.Program{ID: id, Version: version, Manifest: manifest})
			}()
		}
	}()
	t.Cleanup(n.stop)
	return n
}

func (n *notesProgram) stop() {
	n.ln.Close()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.conns {
		c.Close()
	}
	n.conns = nil
}

func shortHostTimings(t *testing.T) {
	first, retry, grace := host.RestartFirst, host.RemoteRetry, host.OfflineGrace
	host.RestartFirst, host.RemoteRetry, host.OfflineGrace = 50*time.Millisecond, 50*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { host.RestartFirst, host.RemoteRetry, host.OfflineGrace = first, retry, grace })
}

// An extension installed from remote: is attached: its view says whether it is online and since when, why it is not
// (also while it is still starting, within the grace), and the version the program says it is when that was not
// approved (never another extension's); none of that while nothing needs it. Any other extension has no attached.
func TestAttachedExtensionView(t *testing.T) {
	shortHostTimings(t)
	h, _, _ := marketHub(t)
	h.opts.Timezone = func() string { return "Asia/Tokyo" }
	reg := h.extensions.reg
	ctx := t.Context()
	dir, err := os.MkdirTemp("/tmp", "att-") // a unix socket path stays short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "notes.sock")
	notes := startNotes(t, sock, "1.0.0")
	manifest, err := extension.Parse([]byte(fmt.Sprintf(`{"manifest_version":1,"id":"example-notes","name":"Example notes","version":"1.0.0","engines":{"extension_protocol":1},"runtime":{"command":{%q:"bin/notes"}},"activation_events":["on_startup"],"contributes":{"providers":[{"id":"notes-provider","name":"Example","base_url":"https://provider.example.com"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)))
	if err != nil {
		t.Fatal(err)
	}
	reg.RemoteManifest = func(context.Context, extension.Remote) (*extension.Manifest, error) { return manifest, nil }
	pv, err := reg.Preview(ctx, "remote:rpc="+sock, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	view := func() ExtensionView { return h.extensions.view(ctx, reg.Get("example-notes")) }
	waitStatus := func(want string) ExtensionView {
		t.Helper()
		var v ExtensionView
		waitLong(t, "status "+want, func() bool { v = view(); return v.Status == want })
		return v
	}

	v := waitStatus(host.StateRunning)
	if a := v.Attached; a == nil || !a.Online || a.Since.IsZero() || a.Error != "" || a.Version != "" {
		t.Fatalf("online: %+v", a)
	}
	if loc := v.Attached.Since.Location().String(); loc != "Asia/Tokyo" {
		t.Fatalf("since in %s, not the user's zone", loc)
	}
	if hp := h.extensions.get("example-notes"); !hp.proc.Spec().Attached {
		t.Fatal("the host is not told it is attached")
	}

	notes.stop()
	v = waitStatus(host.StateStarting)
	if a := v.Attached; a == nil || a.Online || a.Error == "" || v.Error != "" {
		t.Fatalf("within the grace: %+v (error %q)", a, v.Error)
	}
	v = waitStatus(host.StateError)
	if a := v.Attached; a == nil || a.Online || !strings.Contains(v.Error, sock) {
		t.Fatalf("offline: %+v (error %q)", a, v.Error)
	}

	notes = startNotes(t, sock, "1.1.0")
	waitLong(t, "the version it says", func() bool { v = view(); return v.Attached != nil && v.Attached.Version == "1.1.0" })
	if v.Status != host.StateError || v.Version != "1.0.0" || !strings.Contains(v.Error, "1.1.0") ||
		!strings.Contains(v.Error, "personad extension upgrade example-notes") {
		t.Fatalf("another version: %s %q (%+v)", v.Status, v.Error, v.Attached)
	}
	if summary, detail := i18n.ExtensionFailureText(i18n.En, v.Error, "extension-status.failed"); detail != "" ||
		!strings.Contains(summary, "approved 1.0.0") || !strings.Contains(summary, "personad extension upgrade example-notes") {
		t.Fatalf("clients are told %q (%q), not how to approve it", summary, detail)
	}

	notes.stop()
	notes = startNotes(t, sock, "1.0.0")
	v = waitStatus(host.StateRunning)
	if a := v.Attached; a == nil || !a.Online || a.Version != "" {
		t.Fatalf("back: %+v", a)
	}

	notes.stop()
	startProgram(t, sock, "example-other", "1.1.0") // another extension's program on its socket
	waitLong(t, "refused as another program", func() bool {
		v = view()
		return v.Status == host.StateError && strings.Contains(v.Error, "example-other")
	})
	if a := v.Attached; a == nil || a.Online || a.Version != "" || strings.Contains(v.Error, "personad extension upgrade") {
		t.Fatalf("another program: %q (%+v)", v.Error, a)
	}

	later, err := extension.Parse([]byte(fmt.Sprintf(`{"manifest_version":1,"id":"example-later","name":"Example later","version":"1.0.0","engines":{"extension_protocol":1},"runtime":{"command":{%q:"bin/later"}},"activation_events":["on_call"],"contributes":{"providers":[{"id":"later-provider","name":"Example","base_url":"https://provider.example.com"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)))
	if err != nil {
		t.Fatal(err)
	}
	reg.RemoteManifest = func(context.Context, extension.Remote) (*extension.Manifest, error) { return later, nil }
	if pv, err = reg.Preview(ctx, "remote:rpc="+filepath.Join(dir, "later.sock"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	var lv ExtensionView
	waitLong(t, "the on-call one stopped", func() bool {
		lv = h.extensions.view(ctx, reg.Get("example-later"))
		return lv.Status == host.StateStopped
	})
	if a := lv.Attached; a == nil || a.Online || !a.Since.IsZero() || a.Error != "" || a.Version != "" {
		t.Fatalf("nothing needs it: %+v", a)
	}

	pkg := declarativePackage(t, `{"manifest_version":1,"id":"plain","name":"Plain","version":"1.0.0","contributes":{"providers":[{"id":"plain-provider","name":"Example","base_url":"https://provider.example.com"}]}}`)
	if pv, err = reg.Preview(ctx, pkg, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if v := h.extensions.view(ctx, reg.Get("plain")); v.Attached != nil {
		t.Fatalf("not attached: %+v", v.Attached)
	}
}

// An attached extension never upgrades itself from a market, even with a market origin: the manager has no program of
// it to replace.
func TestAttachedNeverUpgradesItself(t *testing.T) {
	p := &extension.Extension{Record: extension.Record{ID: "example-notes", Source: extension.SourceInstalled, Origin: "market:example/example-notes", Version: "1.0.0"}}
	if got := upgradesFrom(p); got != "example" {
		t.Fatalf("from its market: %q", got)
	}
	p.Record.Remote = &extension.Remote{RPCSocket: "/tmp/notes.sock"}
	if got := upgradesFrom(p); got != "" {
		t.Fatalf("attached: %q", got)
	}
	if _, _, _, ok := (&extensionUpgrades{}).candidateIn(p, "en"); ok {
		t.Fatal("an attached extension has an upgrade")
	}
	var v ExtensionView
	(&extensionUpgrades{}).decorate(t.Context(), &v, p)
	if v.AutoUpgrade != nil || v.Upgrade != nil {
		t.Fatalf("decorated: %+v", v)
	}
}

func TestAttachedLine(t *testing.T) {
	since := time.Date(2025, 1, 2, 10, 42, 0, 0, time.FixedZone("", 9*3600)) // printed in its own zone, not this machine's
	for _, tt := range []struct {
		a         *ExtensionAttached
		withError bool
		want      string
	}{
		{nil, true, ""},
		{&ExtensionAttached{Online: true, Since: since}, true, "；独立运行，在线（自 01-02 10:42:00）"},
		{&ExtensionAttached{Since: since, Error: "not answering"}, true, "；独立运行，离线（自 01-02 10:42:00）：not answering"},
		{&ExtensionAttached{Since: since, Error: "not answering"}, false, "；独立运行，离线（自 01-02 10:42:00）"},
		{&ExtensionAttached{Version: "1.1.0"}, false, "；独立运行，离线；程序自报版本 1.1.0"},
	} {
		if got := attachedLine(tt.a, tt.withError); got != tt.want {
			t.Errorf("attachedLine(%+v, %v) = %q, want %q", tt.a, tt.withError, got, tt.want)
		}
	}
}
