//go:build codexipclive && windows

package node

import (
	"context"
	"testing"
	"time"
)

// A read-only look at the real Codex App bus on this Windows computer: `go test -tags codexipclive -run
// TestCodexIPCLive ./internal/node/` with the Codex App running. It connects to \\.\pipe\codex-ipc, says initialize
// and asks which window owns a thread id made up here (no conversation has it): the App's answer is "no owner". Nothing
// is sent to any conversation and no link is opened.
func TestCodexIPCLive(t *testing.T) {
	if !codexPipeUp(codexDesktopPipe) {
		t.Skip("the Codex App's bus is not up in this session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := dialCodexDesktop(ctx, "")
	if err != nil {
		t.Fatalf("initialize on the App's bus: %v", err)
	}
	defer c.close()
	f, err := c.owner(newUUID())
	if err == nil {
		t.Fatalf("a made-up thread has an owner: %+v", f)
	}
	t.Logf("initialize: ok; owner of an unknown thread: %v", err)
}
