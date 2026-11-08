package denohost

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The installed guest SDK and trusted transport are two different distributions.
// A fresh SDK copy alone cannot repair a stale embedded PageBridge.
func TestTrustedAdapterMatchesOfficialSDK(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal("Bun is required to check the generated trusted adapter")
	}
	output := filepath.Join(t.TempDir(), "adapter.mjs")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "build", "adapter.ts", "--target=browser", "--minify", "--outfile", output)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate trusted adapter: %v %s", err, raw)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := InstallAdapter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, embedded) {
		t.Fatal("trusted adapter is stale: run scripts/gen/deno_adapter.sh with the portable runtime CI's Bun version")
	}
	t.Log("PASS generated trusted adapter matches the official SDK and installed bytes")
}
