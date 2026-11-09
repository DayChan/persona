package sysmon

import (
	"bufio"
	"context"
	"os/exec"
	"time"
)

// follow runs a periodic command for as long as ctx lives and hands each output line to line. A command that exits is
// restarted at once the first time and after restartWait later. skipFirstRow (may be nil) drops, in every run, the
// first line it accepts: iostat's first row is the average since boot, not the last interval. stopped (may be nil)
// runs each time the command has exited or failed to start, so the caller can drop its last reading.
func follow(ctx context.Context, restartWait time.Duration, skipFirstRow func(string) bool, line func(string),
	stopped func(), name string, args ...string) {
	first := true
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, name, args...)
		out, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			sc := bufio.NewScanner(out)
			skipped := skipFirstRow == nil
			for sc.Scan() {
				if !skipped && skipFirstRow(sc.Text()) {
					skipped = true
					continue
				}
				line(sc.Text())
			}
			_ = cmd.Wait()
		}
		if stopped != nil {
			stopped()
		}
		if !first {
			select {
			case <-ctx.Done():
			case <-time.After(restartWait):
			}
		}
		first = false
	}
}
