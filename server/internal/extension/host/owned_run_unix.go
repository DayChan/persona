//go:build linux || darwin

package host

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// ownRun is the sole Wait and group-signal owner. observeOwnedLeader observes
// the original child without reaping: even a zombie pins that identity until
// this owner calls cmd.Wait. No pidfd or fresh numeric PID assumption is used
// to justify signaling a process group.
func (ch *child) ownRun(dispose func()) {
	pid := ch.cmd.Process.Pid
	grace := StopGrace
	interval := max(time.Millisecond, min(grace/10, 50*time.Millisecond))
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var termAt, killAt time.Time
	leaderEnded, termSent, forceSent := false, false, false
	for {
		ended, err := observeOwnedLeader(pid)
		if err != nil {
			// ECHILD or an observer error is not proof that the identity is still
			// ours. Retain ownership, report the error, and send no signal.
			ch.ownershipError(err)
		} else {
			if ended && !leaderEnded {
				leaderEnded = true
				close(ch.leaderDone)
			}
			if termAt.IsZero() {
				select {
				case <-ch.stopRequest:
					termAt = time.Now()
					if ch.stdin != nil && !leaderEnded {
						termAt = termAt.Add(grace)
					}
				default:
					if leaderEnded {
						termAt = time.Now()
					}
				}
			}
			if !termAt.IsZero() && !termSent && !time.Now().Before(termAt) {
				if err := unix.Kill(-pid, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
					ch.ownershipError(err)
				}
				termSent = true
				killAt = time.Now().Add(grace)
			}
			if leaderEnded && termSent {
				// The leader can no longer perform graceful shutdown. Sweep its
				// members now, while the unreaped leader still pins the group.
				killAt = time.Now()
			}
			if termSent && !forceSent && !time.Now().Before(killAt) {
				if err := unix.Kill(-pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
					ch.ownershipError(err)
				} else {
					forceSent = true
				}
			}
			if leaderEnded && forceSent {
				break
			}
		}
		<-tick.C
	}
	ch.waitErr = ch.cmd.Wait() // exactly one owner, only after the final group signal
	close(ch.reaped)
	// After reap there are no real signals, ever. ESRCH is a conservative
	// completion condition: surviving members, zombies, a reused PGID, EPERM or
	// another observer error all keep admission/disposal owned. We do not scan
	// unrelated processes or treat leader/log-pipe EOF as whole-group completion.
	ch.finishRun(dispose, func() (bool, error) { return ownedGroupAbsent(pid) }, interval)
}

// finishRun is observation-only. An occupied/reused numeric group may delay
// completion, but cannot authorize any signal or early disposal.
func (ch *child) finishRun(dispose func(), absent func() (bool, error), interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		gone, err := absent()
		if err != nil {
			ch.ownershipError(err)
		}
		if gone && err == nil {
			break
		}
		<-tick.C
	}
	if dispose != nil {
		dispose()
	}
	close(ch.exited)
}

func ownedGroupAbsent(pid int) (bool, error) {
	err := unix.Kill(-pid, 0) // existence query, not delivery of a signal
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	return false, err
}
