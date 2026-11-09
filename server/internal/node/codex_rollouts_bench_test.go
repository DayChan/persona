package node

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkRolloutsOlder: one 5 s refresh of a home with 5,000 rollouts in day directories before the window (listed
// once a minute, each still checked on every refresh) and a few recent ones.
func BenchmarkRolloutsOlder(b *testing.B) {
	home := b.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	for i := range 5000 {
		day := now.AddDate(0, 0, -(20 + i%300))
		writeRollout(b, home, fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i), "/w", day, day)
	}
	for i := range 5 {
		writeRollout(b, home, fmt.Sprintf("%08x-1111-4000-8000-%012x", i, i), "/w", now, now)
	}
	expireRollouts(codex, true)
	if got := len(rollouts(codex)); got != 5 {
		b.Fatalf("rollouts in the window: %d, want 5", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		expireRollouts(codex, false)
		rollouts(codex)
	}
}
