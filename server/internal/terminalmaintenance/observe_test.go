package terminalmaintenance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDepartureNeverBecomesGracefulExitOrIdle(t *testing.T) {
	r, proc := fixture(t, "claude")
	p := prepare(t, r, proc)
	for _, test := range []struct {
		name       string
		current    Process
		err        error
		wantError  bool
		wantAbsent bool
	}{
		{"still-present", proc, nil, false, false},
		{"gone", Process{PID: r.PID, UID: -1}, nil, false, true},
		{"reused-pid", Process{PID: r.PID, UID: proc.UID, Identity: "another-birth"}, nil, true, false},
		{"changed-owner", Process{PID: r.PID, UID: proc.UID + 1, Identity: proc.Identity}, nil, true, false},
		{"wrong-pid", Process{PID: r.PID + 1, UID: -1}, nil, true, false},
		{"unreadable", Process{}, errors.New("unreadable"), true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := observeOnce(context.Background(), p, func(context.Context, int) (Process, error) { return test.current, test.err })
			if (err != nil) != test.wantError || out.ProcessAbsent != test.wantAbsent {
				t.Fatalf("observation=%+v err=%v", out, err)
			}
			if out.MachineIdle || out.BackgroundComplete || out.GracefulExitConfirmed {
				t.Fatal("departure manufactured idle")
			}
		})
	}
}

func TestObserveTimeoutPreservesFailureWithoutAction(t *testing.T) {
	r, proc := fixture(t, "claude")
	p := prepare(t, r, proc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := observeWait(ctx, p, time.Second, func(context.Context, int) (Process, error) { return proc, nil })
	if !errors.Is(err, context.Canceled) || out.State != "timeout" || out.ProcessAbsent {
		t.Fatalf("%+v %v", out, err)
	}
	for _, wait := range []time.Duration{-time.Second, 31 * time.Second} {
		if _, err := Observe(context.Background(), p, wait); err == nil {
			t.Fatal("accepted unbounded observation")
		}
	}
}

func TestObserveDeadlineDuringRead(t *testing.T) {
	r, proc := fixture(t, "claude")
	p := prepare(t, r, proc)
	unavailable := errors.New("process table unavailable; departure is unknown")
	// The first census sees the process; the next one is still running when the
	// wait deadline lands, so the real reader would see its ps child killed.
	calls := 0
	out, err := observeWait(context.Background(), p, time.Second, func(ctx context.Context, _ int) (Process, error) {
		if calls++; calls == 1 {
			return proc, nil
		}
		<-ctx.Done()
		return Process{}, unavailable
	})
	if calls != 2 || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, unavailable) ||
		out.State != "timeout" || out.ProcessAbsent {
		t.Fatalf("calls=%d observation=%+v err=%v", calls, out, err)
	}
	if out.MachineIdle || out.BackgroundComplete || out.GracefulExitConfirmed {
		t.Fatal("timeout manufactured idle")
	}

	// While the wait is live, an unreadable table is still an unknown departure.
	out, err = observeWait(context.Background(), p, time.Second, func(context.Context, int) (Process, error) {
		return Process{}, unavailable
	})
	if !errors.Is(err, unavailable) || errors.Is(err, context.DeadlineExceeded) || out.State != "unknown" || out.ProcessAbsent {
		t.Fatalf("observation=%+v err=%v", out, err)
	}

	// A census that completes as the deadline lands keeps its own conclusion.
	late := func(current Process) func(context.Context, int) (Process, error) {
		return func(ctx context.Context, _ int) (Process, error) {
			<-ctx.Done()
			return current, nil
		}
	}
	out, err = observeWait(context.Background(), p, 50*time.Millisecond, late(Process{PID: r.PID, UID: proc.UID, Identity: "another-birth"}))
	if err == nil || errors.Is(err, context.DeadlineExceeded) || out.State != "unknown" {
		t.Fatalf("reused PID hidden by timeout: %+v %v", out, err)
	}
	write(t, r.Executable, "changed runtime bytes")
	out, err = observeWait(context.Background(), p, 50*time.Millisecond, late(Process{PID: r.PID, UID: -1}))
	if err == nil || errors.Is(err, context.DeadlineExceeded) || out.State != "recovery_material_changed" || !out.ProcessAbsent {
		t.Fatalf("changed material hidden by timeout: %+v %v", out, err)
	}
}

func TestProcessTableRejectsPartialDuplicateAndChangedIdentity(t *testing.T) {
	line := "42 1000 Sun Nov 8 12:00:00 2026 /opt/runtime\n"
	first, err := parseProcessTable(line, 42)
	if err != nil {
		t.Fatal(err)
	}
	other, err := parseProcessTable(strings.ReplaceAll(line, "12:00:00", "12:00:01"), 42)
	if err != nil || first == other {
		t.Fatal("birth observation lost")
	}
	for _, data := range []string{"", "42", line + line, line + "partial"} {
		if _, err := parseProcessTable(data, 42); err == nil {
			t.Fatal("accepted partial census")
		}
	}
	absent, err := parseProcessTable(line, 43)
	if err != nil || absent.Identity != "" || absent.UID != -1 {
		t.Fatalf("%+v %v", absent, err)
	}
}

func TestRealReadOnlyProcessObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := readProcess(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != os.Getpid() || p.UID != os.Getuid() || p.Identity == "" {
		t.Fatalf("%+v", p)
	}
}

func TestCLIHelpAndUnsupportedActionsHaveNoExecutionPath(t *testing.T) {
	for _, action := range []string{"--help", "prepare", "observe", "recovery-recipe"} {
		args := []string{action}
		if action != "--help" {
			args = append(args, "--help")
		}
		var out bytes.Buffer
		if err := Main(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"exit"}, {"resume"}, {"start"}, {"prepare", "unexpected"}, {"observe", "--wait", "1h"}} {
		var out bytes.Buffer
		if err := Main(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
