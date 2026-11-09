package denohost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
)

// The separately bundled guest opts in; the installed trusted adapter supplies raw metadata.
func TestDenoExactInitializationMetadataRoundtrip(t *testing.T) {
	engine := testEngine(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bundle := filepath.Join(root, "guest.mjs")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "build", "testdata/exact-init.ts", "--target=browser", "--outfile="+bundle)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest build: %v %s", err, out)
	}
	adapter, err := InstallAdapter(root)
	if err != nil {
		t.Fatal(err)
	}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("initialization metadata unexpectedly needed a broker request")
		http.NotFound(w, r)
	}))
	defer broker.Close()
	config := `{"large":9223372036854775807,"exponent":1e0,"n":"invalid","n":2,"nil":null}`
	proc := host.Start(ctx, host.Spec{ID: "example-exact-init", Command: engine.Path,
		Args: Args(bundle, adapter, broker.URL), Dir: root, CleanEnv: true, RPC: true, Startup: true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: 1, Config: json.RawMessage(config)}, nil
		},
	})
	defer proc.Stop()
	if err := proc.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	var got struct {
		RawConfig      string  `json:"raw_config"`
		ExactLarge     string  `json:"exact_large"`
		ExactExponent  string  `json:"exact_exponent"`
		ExactNull      string  `json:"exact_null"`
		ParsedExponent float64 `json:"parsed_exponent"`
		ParsedN        float64 `json:"parsed_n"`
		SameReceiver   bool    `json:"same_receiver"`
		MetadataFrozen bool    `json:"metadata_frozen"`
	}
	if err := proc.Call(ctx, "fixture.status", nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.RawConfig != config || got.ExactLarge != "9223372036854775807" || got.ExactExponent != "1e0" || got.ExactNull != "null" {
		t.Fatal("actual Deno initialization lost lexical config or duplicate order")
	}
	if got.ParsedExponent != 1 || got.ParsedN != 2 || !got.SameReceiver || !got.MetadataFrozen {
		t.Fatal("exact initialization changed parsed Init, receiver or readonly metadata")
	}
	t.Log("PASS actual Deno/installed adapter preserves exact initialization metadata across the guest SDK bundle boundary")
}
