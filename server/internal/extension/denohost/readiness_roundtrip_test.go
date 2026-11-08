package denohost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
)

// Actual pinned Deno and installed adapter with a separately bundled official SDK.
// The local synthetic broker does not contact any site. This is not kernel isolation acceptance.
func TestDenoOfficialSDKReadinessAndCancellationRoundtrip(t *testing.T) {
	engine := testEngine(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bundle := filepath.Join(root, "guest.mjs")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "build", "testdata/readiness.tsx", "--target=browser", "--outfile="+bundle)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest build: %v %s", err, out)
	}
	adapter, err := InstallAdapter(root)
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__persona_runtime_fetch" {
			t.Errorf("unexpected broker route: %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"topic-1"}`))
	}))
	defer broker.Close()
	proc := host.Start(ctx, host.Spec{ID: "example-readiness", Command: engine.Path, Args: Args(bundle, adapter, broker.URL), Dir: root, CleanEnv: true, RPC: true, Startup: true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: 1}, nil
		},
	})
	defer proc.Stop()
	if err := proc.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	result, ok := proc.Result()
	if !ok || result.UIPageCancellation != 1 {
		t.Fatal("canonical initialize lost cancellation capability")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.ValidateJSON("InitializeResult", raw); err != nil {
		t.Fatal(err)
	}
	var canonical wire.InitializeResult
	if err := json.Unmarshal(raw, &canonical); err != nil || canonical.UIPageCancellation != 1 {
		t.Fatal("generated Go roundtrip lost capability")
	}
	canonicalRaw, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip extension.InitializeResult
	if err := json.Unmarshal(canonicalRaw, &roundtrip); err != nil || roundtrip.UIPageCancellation != 1 {
		t.Fatal("host roundtrip lost capability")
	}
	invalid := bytes.Replace(raw, []byte(`"ui_page_cancellation":1`), []byte(`"ui_page_cancellation":2`), 1)
	if err := wire.ValidateJSON("InitializeResult", invalid); err == nil {
		t.Fatal("unsupported cancellation capability accepted")
	}
	for _, transport := range []string{"snapshot", "stream"} {
		t.Run(transport, func(t *testing.T) {
			params := map[string]any{"session": transport, "transport": transport, "params": map[string]any{"contribution": "hot", "locale": "en", "ui_version": 1}}
			var first wire.UIPageSnapshot
			started := time.Now()
			if err := proc.Call(ctx, "ui.page.mount", params, &first); err != nil {
				t.Fatal(err)
			}
			if transport == "snapshot" {
				if first.View.Title != "topic-1" || time.Since(started) < 180*time.Millisecond {
					t.Fatal("cold finite fetch did not finish")
				}
			} else {
				if first.View.Title != "loading" || time.Since(started) >= 180*time.Millisecond {
					t.Fatal("native mount no longer immediate")
				}
				// Suspend cancels only this UI's fetch through the existing adapter signal.
				if err := proc.Call(ctx, "ui.page.suspend", map[string]any{"session": transport}, nil); err != nil {
					t.Fatal(err)
				}
				return
			}
			event := map[string]any{"session": transport, "ticket": "host-issued", "interaction": map[string]any{"generation": first.Generation, "version": first.Version, "eventId": "write-once", "key": "write", "actionId": first.View.Sections[0].Items[1].Action.ID, "values": map[string]string{}}}
			eventCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- proc.Call(eventCtx, "ui.page.event", event, nil) }()
			var stats struct{ Callbacks, Canceled, CallbackActive int }
			deadline := time.Now().Add(time.Second)
			for stats.CallbackActive != 1 && time.Now().Before(deadline) {
				if err := proc.Call(ctx, "test.stats", nil, &stats); err != nil {
					t.Fatal(err)
				}
			}
			if stats.CallbackActive != 1 {
				stop()
				t.Fatal("callback did not start before deadline")
			}
			stop()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled call succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("canceled RPC did not detach")
			}
			deadline = time.Now().Add(time.Second)
			for stats.Canceled != 1 && time.Now().Before(deadline) {
				if err := proc.Call(ctx, "test.stats", nil, &stats); err != nil {
					t.Fatal(err)
				}
			}
			if stats.Canceled != 1 || stats.CallbackActive != 0 || stats.Callbacks != 1 {
				t.Fatalf("callback signal cancellation: %+v", stats)
			}
			var resumed wire.UIPageSnapshot
			if err := proc.Call(ctx, "ui.page.resume", map[string]any{"session": transport}, &resumed); err != nil {
				t.Fatal(err)
			}
			if resumed.View.Title != "topic-1" {
				t.Fatal("recovery lost finite data")
			}
			var replay struct{ Status string }
			if err := proc.Call(ctx, "ui.page.event", event, &replay); err != nil || replay.Status != "ambiguous" {
				t.Fatalf("canceled callback replay: %v %+v", err, replay)
			}
			if err := proc.Call(ctx, "test.stats", nil, &stats); err != nil || stats.Callbacks != 1 {
				t.Fatal("receipt re-executed callback")
			}
		})
	}
	t.Logf("PASS actual Deno canonical capability, cold topic-1, immediate native mount and scoped callback cancellation; broker reads=%d", reads.Load())
}
