package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestExtensionDependencyExporterInitializationAndEvents(t *testing.T) {
	for _, activation := range []string{extension.OnStartup, extension.OnCall} {
		t.Run(activation, func(t *testing.T) {
			h, _, _ := marketHub(t)
			reg := h.extensions.reg
			dir, err := os.MkdirTemp("/tmp", "exp-")
			if err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "r.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			initialized := make(chan map[string][]extension.ExportedContribution, 4)
			events := make(chan map[string][]extension.ExportedContribution, 16)
			var connections atomic.Int32
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					connections.Add(1)
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer conn.Close()
						peer := rpc.New("exporter", conn)
						peer.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
							if method == extension.MInitialize {
								var params extension.InitializeParams
								if err := json.Unmarshal(raw, &params); err != nil {
									return nil, err
								}
								initialized <- params.ContributionsToMe
								return extension.InitializeResult{Protocol: extension.Protocol, ID: "search", Version: "1.0.0"}, nil
							}
							return map[string]any{}, nil
						}
						peer.OnEvent = func(event string, raw json.RawMessage) {
							if event != "extension.contributions_changed" {
								return
							}
							var params struct {
								Contributions map[string][]extension.ExportedContribution `json:"contributions_to_me"`
							}
							if json.Unmarshal(raw, &params) == nil {
								events <- params.Contributions
							}
						}
						_ = peer.Serve(ctx, conn)
					}()
				}
			}()
			t.Cleanup(func() {
				if hp := h.extensions.get("search"); hp != nil && hp.proc != nil {
					hp.proc.Stop()
				}
				cancel()
				_ = listener.Close()
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("fake exporter did not stop")
				}
				_ = os.RemoveAll(dir)
			})
			install := func(id, deps, extra string) {
				t.Helper()
				pkg := declarativePackage(t, fmt.Sprintf(`{"manifest_version":1,"id":%q,"name":%q,"version":"1.0.0","depends":%s,"contributes":{"providers":[{"id":%q,"name":"Example","base_url":"https://provider.example.com"}]%s}}`, id, id, deps, id+"-provider", extra))
				pv, err := reg.Preview(ctx, pkg, "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
					t.Fatal(err)
				}
			}
			install("middle", `[{"id":"base"}]`, "")
			install("reader", `[{"id":"middle"},{"id":"search"}]`, `,"search.sources":[{"id":"notes"}]`)
			manifest, err := extension.Parse([]byte(fmt.Sprintf(`{"manifest_version":1,"id":"search","name":"Search","version":"1.0.0","engines":{"extension_protocol":1},"runtime":{"command":{%q:"bin/mock"}},"activation_events":[%q],"exports":{"extension_points":[{"id":"sources","version":1}]},"contributes":{"providers":[{"id":"search-provider","name":"Example","base_url":"https://provider.example.com"}]}}`, runtime.GOOS+"-"+runtime.GOARCH, activation)))
			if err != nil {
				t.Fatal(err)
			}
			reg.RemoteManifest = func(context.Context, extension.Remote) (*extension.Manifest, error) { return manifest, nil }
			pv, err := reg.Preview(ctx, "remote:rpc="+socket, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
				t.Fatal(err)
			}
			receive := func(ch <-chan map[string][]extension.ExportedContribution, want int) map[string][]extension.ExportedContribution {
				t.Helper()
				select {
				case snapshot := <-ch:
					if len(snapshot["sources"]) != want {
						t.Fatalf("exporter received %d sources, want %d", len(snapshot["sources"]), want)
					}
					return snapshot
				case <-time.After(10 * time.Second):
					t.Fatal("exporter snapshot/event timed out")
					return nil
				}
			}
			if activation == extension.OnStartup {
				receive(initialized, 0)
				waitLong(t, "exporter ready", func() bool { hp := h.extensions.get("search"); return hp != nil && hp.proc != nil && hp.proc.Ready() })
				// Re-check after connection too: initialize already delivered this snapshot.
				h.extensions.modelChanged(ctx, extension.Change{ID: "search"})
			}
			install("base", `[]`, "")
			if activation == extension.OnCall {
				hp := h.extensions.get("search")
				if hp == nil || hp.proc == nil {
					t.Fatal("on-demand host missing")
				}
				state, _ := hp.proc.Status()
				if state != host.StateStopped || connections.Load() != 0 {
					t.Fatalf("readiness/notification woke on-demand exporter: %s, connections=%d", state, connections.Load())
				}
				for _, d := range reg.Dependencies(reg.Get("reader").Full) {
					if d.State != extension.DepOK {
						t.Fatal("stopped on-demand exporter treated as unavailable:", d)
					}
				}
				return
			}
			before := receive(events, 1)
			if err = reg.SetEnabled(ctx, "base", false, "test"); err != nil {
				t.Fatal(err)
			}
			receive(events, 0)
			if len(before["sources"]) != 1 {
				t.Fatal("previous event snapshot changed")
			}
			if err = reg.SetEnabled(ctx, "base", true, "test"); err != nil {
				t.Fatal(err)
			}
			receive(events, 1)
		})
	}
}

func TestSameContributions(t *testing.T) {
	type snapshot = map[string][]extension.ExportedContribution
	item := extension.ExportedContribution{Extension: "reader", Item: json.RawMessage(`{"id":"notes"}`)}
	for _, tt := range []struct {
		name string
		a, b snapshot
		want bool
	}{
		{"empty", nil, snapshot{}, true},
		{"identical bytes", snapshot{"sources": {item}}, snapshot{"sources": {{Extension: "reader", Item: json.RawMessage(`{"id":"notes"}`)}}}, true},
		{"added", nil, snapshot{"sources": {item}}, false},
		{"removed", snapshot{"sources": {item}}, nil, false},
		{"point changed", snapshot{"sources": {item}}, snapshot{"other": {item}}, false},
		{"owner changed", snapshot{"sources": {item}}, snapshot{"sources": {{Extension: "other", Item: item.Item}}}, false},
		{"item changed", snapshot{"sources": {item}}, snapshot{"sources": {{Extension: "reader", Item: json.RawMessage(`{"id":"other"}`)}}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameContributions(tt.a, tt.b); got != tt.want {
				t.Fatalf("sameContributions = %v, want %v", got, tt.want)
			}
		})
	}
}
