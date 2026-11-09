package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestWidgetRenderCannotRestoreRetiredCache(t *testing.T) {
	root := shortTempDir(t)
	h := New(Options{Dir: root, Config: &Config{dir: root}})
	h.extensions = newExtensions(root, nil, func(string, ...any) {})
	const id = "example-widget"
	manifest := &extension.Manifest{ManifestVersion: 1, ID: id, Name: "Example widget", Version: "1.0.0",
		Contributes: map[string]json.RawMessage{"ui": json.RawMessage(`[{"id":"today","title":"Today","placement":"widget","families":["small"]}]`)}}
	if err := h.extensions.reg.AddHubJSON(manifest, nil, true); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "widget.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	entered, release, served := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var renders atomic.Int32
	go func() {
		defer close(served)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		peer := rpc.New("widget", conn)
		peer.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case extension.MInitialize:
				return extension.InitializeResult{Protocol: extension.Protocol, ID: id, Version: "1.0.0"}, nil
			case "ui.render":
				n := renders.Add(1)
				if n == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return json.RawMessage(fmt.Sprintf(`{"sections":[{"items":[{"type":"text","text":"Render %d"}]}]}`, n)), nil
			default:
				return map[string]any{}, nil
			}
		}
		_ = peer.Serve(ctx, conn)
	}()
	process := host.Start(ctx, host.Spec{ID: id, RPC: true, RPCSocket: socket, Attached: true, OnCall: true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: extension.Protocol}, nil
		}})
	h.extensions.byID[id] = &hubExtension{id: id, proc: process}
	t.Cleanup(func() {
		cancel()
		process.Stop()
		_ = listener.Close()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("widget RPC fixture did not stop")
		}
	})
	var in uiRequest
	in.Context.Size, in.Context.Locale, in.Context.TZ = "small", "en", "UTC"
	result := make(chan error, 1)
	go func() {
		_, err := h.widgetCall(ctx, id, "today", in)
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("widget render did not start:", ctx.Err())
	}
	// Retire while the render is in flight, without stopping RPC: cancellation must not mask the cache fence.
	h.widgetsDropExtension(id)
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, errNoExtension) {
			t.Fatalf("retired render returned %v, want errNoExtension", err)
		}
	case <-ctx.Done():
		t.Fatal("widget render did not finish:", ctx.Err())
	}
	w := &h.extensions.widgets
	w.mu.Lock()
	kept := len(w.views)
	w.mu.Unlock()
	if kept != 0 {
		t.Fatalf("retired render restored %d cached views", kept)
	}
	for range 2 {
		answer, err := h.widgetCall(ctx, id, "today", in)
		if err != nil {
			t.Fatal(err)
		}
		if len(answer.View.Sections) != 1 || len(answer.View.Sections[0].Items) != 1 || answer.View.Sections[0].Items[0].Text != "Render 2" {
			t.Fatalf("fresh render/cache returned %+v", answer.View)
		}
	}
	if got := renders.Load(); got != 2 {
		t.Fatalf("rendered %d times, want one retired render and one cached fresh render", got)
	}
}
