package sysmon

import (
	"context"
	"os/exec"
	"slices"
	"testing"
	"time"
)

// follow hands each line over in order, calls stopped whenever the command exits or cannot start (so the collector
// drops its last reading instead of reporting it as current), restarts it, and returns soon after its context ends.
func TestFollowClearsOnExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	print12 := []string{"-c", `printf "1\n2\n"`}
	for _, tc := range []struct {
		name string
		skip func(string) bool
		cmd  string
		args []string
		want []string
	}{
		{"exits", nil, "sh", print12, []string{"1", "2", "stopped", "1", "2", "stopped"}},
		{"first row skipped in every run", func(l string) bool { return l == "1" }, "sh", print12,
			[]string{"2", "stopped", "2", "stopped"}},
		{"cannot start", nil, "persona-sysmon-no-such-command", nil, []string{"stopped", "stopped"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := make(chan string)
			send := func(s string) {
				select {
				case events <- s:
				case <-ctx.Done():
				}
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				follow(ctx, 10*time.Millisecond, tc.skip, send, func() { send("stopped") }, tc.cmd, tc.args...)
			}()
			var got []string
			timeout := time.After(10 * time.Second)
			for len(got) < len(tc.want) {
				select {
				case e := <-events:
					got = append(got, e)
				case <-timeout:
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("follow outlived its context")
			}
		})
	}
}
