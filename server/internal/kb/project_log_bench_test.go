package kb

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// benchLogLines is the size of the timeline the project-log benchmarks read and append to (~1.3 MB).
const benchLogLines = 20000

// bigLog is a timeline of n lines in zone, as AppendLog writes them.
func bigLog(zone *time.Location, n int) []byte {
	var b strings.Builder
	b.WriteString(logHeader(zone))
	at := time.Date(2024, 1, 1, 9, 0, 0, 0, zone)
	for i := range n {
		fmt.Fprintf(&b, "- %s · lark · line %d: moved to Thursday, 发布改到周四\n",
			at.Add(time.Duration(i)*time.Hour).Format("2006-01-02 15:04"), i)
	}
	return []byte(b.String())
}

// BenchmarkProjectLog: appending a line to, and reading the latest lines of, a project's 20k-line timeline.
func BenchmarkProjectLog(b *testing.B) {
	setup := func(b *testing.B) (*Store, func()) {
		b.Helper()
		st, err := NewStore(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		if _, err := st.Rezone(time.UTC); err != nil {
			b.Fatal(err)
		}
		raw := bigLog(time.UTC, benchLogLines)
		reset := func() {
			if err := os.WriteFile(st.Path(KindProject, "p", "log.md"), raw, 0o600); err != nil {
				b.Fatal(err)
			}
		}
		if err := os.MkdirAll(st.Path(KindProject, "p"), 0o700); err != nil {
			b.Fatal(err)
		}
		reset()
		return st, reset
	}
	b.Run("Append", func(b *testing.B) {
		st, reset := setup(b)
		at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			if i%1000 == 999 { // keep the timeline near its size: the old cost grew with it
				b.StopTimer()
				reset()
				b.StartTimer()
			}
			if err := st.AppendLog("p", at, "lark", "a new line"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Log30", func(b *testing.B) {
		st, _ := setup(b)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if got := st.Log("p", 30); len(got) != 30 {
				b.Fatalf("%d lines", len(got))
			}
		}
	})
}
