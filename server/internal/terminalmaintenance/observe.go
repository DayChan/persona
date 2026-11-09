package terminalmaintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Process records a bounded OS observation, not a product-issued instance ID or
// a session binding. A matching PID may have been reused within clock resolution.
type Process struct {
	PID      int    `json:"pid"`
	UID      int    `json:"uid"`
	Identity string `json:"observation_identity"`
}

func readProcess(ctx context.Context, pid int) (Process, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return Process{}, errors.New("process observation is unsupported on this platform")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// A successful complete table distinguishes absence from unreadable details.
	// Do not read arguments, environment, screens, task content or credentials.
	command := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,uid=,lstart=,comm=")
	command.Env = append(command.Environ(), "LC_ALL=C", "TZ=UTC")
	data, err := command.Output()
	if err != nil {
		return Process{}, errors.New("process table unavailable; departure is unknown")
	}
	return parseProcessTable(string(data), pid)
}

func parseProcessTable(data string, pid int) (Process, error) {
	if strings.TrimSpace(data) == "" || pid <= 0 {
		return Process{}, errors.New("incomplete process table")
	}
	out := Process{PID: pid, UID: -1}
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return Process{}, errors.New("incomplete process table row")
		}
		rowPID, err := strconv.Atoi(fields[0])
		if err != nil || rowPID <= 0 {
			return Process{}, errors.New("invalid process table PID")
		}
		uid, err := strconv.Atoi(fields[1])
		if err != nil || uid < 0 {
			return Process{}, errors.New("invalid process table owner")
		}
		if rowPID != pid {
			continue
		}
		if out.Identity != "" {
			return Process{}, errors.New("duplicate process identity")
		}
		h := sha256.Sum256([]byte(strings.Join(fields[1:], " ")))
		out.UID, out.Identity = uid, hex.EncodeToString(h[:])
	}
	return out, nil
}

type Observation struct {
	ObservedAt            time.Time `json:"observed_at"`
	State                 string    `json:"state"`
	ProcessAbsent         bool      `json:"process_absent"`
	GracefulExitConfirmed bool      `json:"graceful_exit_confirmed"`
	BackgroundComplete    bool      `json:"background_complete"`
	MachineIdle           bool      `json:"machine_idle"`
	Reason                string    `json:"reason"`
}

// Observe waits at most the supplied deadline, observes only, and never sends a
// command or signal. Absence cannot distinguish native exit, crash or detach.
func Observe(ctx context.Context, p Prepared, wait time.Duration) (Observation, error) {
	if wait < 0 || wait > 30*time.Second {
		return Observation{}, errors.New("observation wait must be between zero and 30 seconds")
	}
	if err := p.Validate(); err != nil {
		return Observation{}, err
	}
	return observeWait(ctx, p, wait, readProcess)
}

func observeWait(ctx context.Context, p Prepared, wait time.Duration, read func(context.Context, int) (Process, error)) (Observation, error) {
	if wait == 0 {
		return observeOnce(ctx, p, read)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		var readErr error
		out, err := observeOnce(ctx, p, func(ctx context.Context, pid int) (Process, error) {
			current, err := read(ctx, pid)
			readErr = err
			return current, err
		})
		// A deadline that interrupts the census (the real reader's ps child is
		// killed) is the wait running out, not an unreadable process table.
		if readErr != nil && ctx.Err() != nil {
			out.State = "timeout"
			return out, fmt.Errorf("process departure not observed: %w", ctx.Err())
		}
		if err != nil || out.ProcessAbsent {
			return out, err
		}
		select {
		case <-ctx.Done():
			out.State = "timeout"
			return out, fmt.Errorf("process departure not observed: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func observeOnce(ctx context.Context, p Prepared, read func(context.Context, int) (Process, error)) (Observation, error) {
	out := Observation{ObservedAt: time.Now().UTC(), State: "unknown", Reason: "process observation does not establish native exit, persistence, background disposition or idle"}
	current, err := read(ctx, p.Process.PID)
	if err != nil {
		return out, err
	}
	if current.PID != p.Process.PID {
		return out, errors.New("process observer returned a conflicting PID")
	}
	if current.Identity == "" && current.UID == -1 {
		out.State, out.ProcessAbsent = "absent", true
		if err := p.Validate(); err != nil {
			out.State = "recovery_material_changed"
			return out, err
		}
		return out, nil
	}
	if current != p.Process {
		return out, errors.New("process identity changed; no graceful exit or recovery conclusion is possible")
	}
	out.State = "present"
	return out, nil
}
