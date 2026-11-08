package extmgr

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension/denohost"
	"github.com/nwverse/persona/server/internal/extension/host"
)

// The actual installed adapter drives a separately bundled guest SDK over a portable
// Node stdio facade. No Deno permissions, egress or production resource claim is made.
func pageTrustedAdapterManager(t *testing.T, timeout bool) (*Manager, *host.Proc, *pageTestHub) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal("Bun is required for trusted-adapter integration")
	}
	root := shortTempDir(t)
	program := filepath.Join(root, "guest.mjs")
	source, err := filepath.Abs("testdata/ui-page-cancel-program.tsx")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bun, "build", source, "--target=browser", "--outfile="+program).CombinedOutput(); err != nil {
		t.Fatalf("guest bundle: %v %s", err, out)
	}
	adapter, err := denohost.InstallAdapter(root)
	if err != nil {
		t.Fatal(err)
	}
	facade, err := filepath.Abs("testdata/ui-page-adapter.mjs")
	if err != nil {
		t.Fatal(err)
	}
	return pageProcessManager(t, func(spec *host.Spec) {
		spec.Args = []string{facade, adapter, program}
		if timeout {
			spec.CallTimeout = 150 * time.Millisecond
		}
	})
}

func pageHide(t *testing.T, h *Manager, session, epoch string) {
	t.Helper()
	if _, err := pageCall(t, h, pageWho, wire.UIStreamRequest{Request: "suspend", Protocol: 1, RequestID: "hide", Session: session, Epoch: epoch}); err != nil {
		t.Fatal(err)
	}
}
func pageResume(session, epoch string, version int64) wire.UIStreamRequest {
	return wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "recover", Session: session, Epoch: epoch, Version: version}
}

func TestPageDistinctAllocationMayOverlapRetiredUncooperativeCallback(t *testing.T) {
	h, process, _ := pageTrustedAdapterManager(t, false)
	pageFixture(t, h, "callback", map[string]any{"mode": "late"})
	first := pageOpenTest(t, h, pageWho)
	old := pageEvent(t, first, "no-ui", "old-write")
	oldDone := make(chan struct{}, 1)
	go func() { _, _ = pageCall(t, h, pageWho, old); oldDone <- struct{}{} }()
	pageAwait(t, func() bool { return pageStats(t, process)["callbackActive"] == 1 })
	pageHide(t, h, first.Session(), first.Epoch())
	select {
	case <-oldDone:
	case <-time.After(3 * time.Second):
		t.Fatal("old RPC remained busy")
	}
	resume := pageResume(first.Session(), first.Epoch(), pageTree(h, first.Session()).Version())
	if _, err := pageCall(t, h, pageWho, resume); pageFailureCode(err) != "expired" {
		t.Fatalf("retirement: %v", err)
	}
	pageFixture(t, h, "callback", map[string]any{"mode": "cooperative"})
	fresh := pageOpenTest(t, h, pageWho)
	if fresh.Session() == first.Session() || fresh.Epoch() == first.Epoch() {
		t.Fatal("retired identity reused")
	}
	newDone := make(chan struct{}, 1)
	go func() {
		_, _ = pageCall(t, h, pageWho, pageEvent(t, fresh, "no-ui", "distinct-write"))
		newDone <- struct{}{}
	}()
	pageAwait(t, func() bool { return pageStats(t, process)["callbackActive"] == 2 })
	if _, err := pageCall(t, h, pageWho, old); pageFailureCode(err) != "expired" {
		t.Fatal("old event crossed allocation fence")
	}
	pageFixture(t, h, "releaseCallback", nil)
	pageAwait(t, func() bool { return pageStats(t, process)["callbackActive"] == 1 })
	if bytes.Contains(pageTree(h, fresh.Session()).ViewJSON(), []byte(`"text":"99"`)) {
		t.Fatal("late retired callback changed the new allocation")
	}
	pageHide(t, h, fresh.Session(), fresh.Epoch())
	select {
	case <-newDone:
	case <-time.After(3 * time.Second):
		t.Fatal("new callback did not cooperate")
	}
	stats := pageStats(t, process)
	if stats["writes"] != 2 || stats["callbackMaxActive"] != 2 {
		t.Fatalf("distinct actions or concurrency changed: %v", stats)
	}
	t.Logf("PASS distinct allocations: writes=%d maxActive=%d; retired event rejected, no receipt re-execution", stats["writes"], stats["callbackMaxActive"])
}
