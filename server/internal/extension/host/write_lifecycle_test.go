//go:build linux

package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
	"golang.org/x/sys/unix"
)

// The child acknowledges each read on an independent owned socket. No PID or
// transport is inferred from a timer or from another process's environment.
func TestWriteLifecycleChild(t *testing.T) {
	mode := os.Getenv("HOST_LIFECYCLE_CHILD")
	if mode == "" {
		return
	}
	c, err := net.DialTimeout("unix", os.Getenv("HOST_LIFECYCLE_CONTROL"), 3*time.Second)
	if err != nil {
		os.Exit(2)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	size, err := unix.FcntlInt(0, unix.F_SETPIPE_SZ, 4096)
	if err != nil {
		os.Exit(4)
	}
	fmt.Fprintf(c, "ready %d %d\n", os.Getpid(), size)
	input := bufio.NewReader(os.Stdin)
	control := bufio.NewScanner(c)
	var lastID, pageID string
	for control.Scan() {
		switch control.Text() {
		case "read":
			line, err := input.ReadBytes('\n')
			if err != nil {
				fmt.Fprintln(c, "eof")
				os.Exit(0)
			}
			var m struct{ ID, Method, Event string }
			if json.Unmarshal(line, &m) != nil {
				os.Exit(3)
			}
			lastID = m.ID
			if m.Method == "page" {
				pageID = m.ID
			}
			fmt.Fprintln(c, "read "+m.Method+m.Event)
			if m.Method == extension.MInitialize {
				result, _ := json.Marshal(extension.InitializeResult{Protocol: extension.Protocol, ID: "demo", Version: "1.0.0"})
				fmt.Fprintf(os.Stdout, "{\"id\":%q,\"result\":%s}\n", m.ID, result)
			}
		case "late":
			fmt.Fprintf(os.Stdout, "{\"id\":%q,\"result\":true}\n", pageID)
			fmt.Fprintln(c, "late sent")
		case "reply":
			fmt.Fprintf(os.Stdout, "{\"id\":%q,\"result\":true}\n", lastID)
			fmt.Fprintln(c, "reply sent")
		case "close-output":
			_ = os.Stdout.Close()
			fmt.Fprintln(c, "output closed")
		case "occupancy":
			n, err := unix.IoctlGetInt(0, unix.TIOCINQ)
			if err != nil {
				os.Exit(5)
			}
			fmt.Fprintf(c, "bytes %d\n", n)
		case "exit":
			os.Exit(0)
		}
	}
	os.Exit(0)
}

type lifecycleFixture struct {
	p             *Proc
	c             net.Conn
	br            *bufio.Reader
	pid, capacity int
}

func lifecycleStart(t *testing.T, initialize bool, configure ...func(*Spec)) *lifecycleFixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "rpc-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "control")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
	s := spec(t, "")
	s.Args = []string{"-test.run=^TestWriteLifecycleChild$"}
	s.Env = []string{"HOST_TEST_PLUGIN=", "HOST_LIFECYCLE_CHILD=blocked", "HOST_LIFECYCLE_CONTROL=" + socket}
	s.Startup = true
	s.CleanEnv = true
	if !initialize {
		s.Init = func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Config: json.RawMessage(`"` + strings.Repeat("x", 61482) + `"`)}, nil
		}
	}
	for _, configure := range configure {
		configure(&s)
	}
	readyGate := make(chan struct{})
	init := s.Init
	s.Init = func(ctx context.Context) (extension.InitializeParams, error) {
		select {
		case <-readyGate:
			return init(ctx)
		case <-ctx.Done():
			return extension.InitializeParams{}, ctx.Err()
		}
	}
	p := Start(context.Background(), s)
	// Register ownership cleanup before accepting or parsing readiness: failed
	// handshakes must not leave a Proc retrying children after the test returns.
	t.Cleanup(func() {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Error("TIMEOUT owned Proc cleanup")
		}
	})
	c, err := ln.Accept()
	if err != nil {
		p.cancel()
		t.Fatal("TIMEOUT child ready", err)
	}
	_ = c.SetDeadline(time.Now().Add(rpc.StallWindow + 25*time.Second))
	f := &lifecycleFixture{p: p, c: c, br: bufio.NewReader(c)}
	ready := f.line(t)
	if !strings.HasPrefix(ready, "ready ") {
		t.Fatal("FAIL child ready")
	}
	if _, err = fmt.Sscanf(ready, "ready %d %d", &f.pid, &f.capacity); err != nil {
		t.Fatal(err)
	}
	close(readyGate)
	t.Cleanup(func() {
		p.cancel()
		_ = c.Close()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Error("TIMEOUT owned Proc reap")
		}
	})
	if initialize {
		f.command(t, "read")
		if got := f.line(t); got != "read initialize" {
			t.Fatal("FAIL initialize read", got)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Wait(ctx); err != nil {
			t.Fatal("FAIL initialize", err)
		}
	}
	return f
}
func (f *lifecycleFixture) command(t *testing.T, s string) {
	t.Helper()
	if _, err := fmt.Fprintln(f.c, s); err != nil {
		t.Fatal(err)
	}
}
func (f *lifecycleFixture) line(t *testing.T) string {
	t.Helper()
	s, err := f.br.ReadString('\n')
	if err != nil {
		t.Fatal("TIMEOUT control", err)
	}
	return strings.TrimSpace(s)
}
func lifecycleAwait(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(rpc.StallWindow + 3*time.Second):
		// Includes a canceled caller's stalled Write, interrupted after the window.
		t.Fatal("TIMEOUT RPC completion")
		return nil
	}
}

// A schema-independent payload below the settings byte budget exercises the
// real Proc startup transport, not schema validation or a Deno sandbox.
func TestWriteLifecycleStartup(t *testing.T) {
	old, grace := ReadyWithin, StopGrace
	ReadyWithin, StopGrace = 80*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { ReadyWithin, StopGrace = old, grace })
	f := lifecycleStart(t, false)
	f.waitBytes(t, f.capacity)
	stopped := make(chan error, 1)
	go func() { f.p.Stop(); stopped <- nil }()
	select {
	case <-stopped:
		if alive(f.pid) {
			t.Fatal("FAIL Stop before reap")
		}
		t.Log("PASS real Proc startup shutdown reaped")
	case <-time.After(250 * time.Millisecond):
		t.Fatal("TIMEOUT startup Stop")
	}
}

func TestWriteLifecycleQueuedCancel(t *testing.T) {
	for _, capability := range []bool{false, true} {
		t.Run(fmt.Sprint(capability), func(t *testing.T) {
			f := lifecycleStart(t, true)
			f.p.mu.Lock()
			peer := f.p.sess.peer
			f.p.mu.Unlock()
			peer.Cancels = map[string]bool{"page": capability}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			call := make(chan error, 1)
			go func() { call <- f.p.Call(ctx, "page", struct{}{}, nil) }()
			f.command(t, "read")
			if got := f.line(t); got != "read page" {
				t.Fatal(got)
			}
			large := make(chan error, 1)
			go func() { large <- f.p.Notify("large", strings.Repeat("x", 2<<20)) }()
			f.waitBytes(t, f.capacity)
			// A queued request expires without reaching the child's full stdin pipe.
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			err := peer.Call(probeCtx, "probe", nil, nil)
			probeCancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("FAIL writer occupancy", err)
			}
			cancel()
			select {
			case err := <-call:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				t.Log("PASS real Proc cancellation returned", capability)
			case <-time.After(250 * time.Millisecond):
				t.Fatal("TIMEOUT queued cancel")
			}
			f.command(t, "read")
			if got := f.line(t); got != "read large" {
				t.Fatal("FAIL large frame", got)
			}
			if err := lifecycleAwait(t, large); err != nil {
				t.Fatal(err)
			}
			if peer.Err() != nil {
				t.Fatal("FAIL delivered withdrawal disconnected healthy stream")
			}
			if capability {
				f.command(t, "read")
				if got := f.line(t); got != "read rpc.cancel" {
					t.Fatal("FAIL queued withdrawal was not delivered", got)
				}
			}
			// The timed-out probe must never reach the child's input.
			if err := f.p.Notify("next", nil); err != nil {
				t.Fatal(err)
			}
			f.command(t, "read")
			if got := f.line(t); got != "read next" {
				t.Fatal("FAIL late unsent frame", got)
			}
		})
	}
}

func (f *lifecycleFixture) waitBytes(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.command(t, "occupancy")
		n, err := strconv.Atoi(strings.TrimPrefix(f.line(t), "bytes "))
		if err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
	}
	t.Fatal("TIMEOUT pipe occupancy handshake")
}
func (f *lifecycleFixture) fill(t *testing.T) {
	t.Helper()
	empty, _ := json.Marshal(map[string]any{"event": "fill", "params": ""})
	if err := f.p.Notify("fill", strings.Repeat("x", f.capacity-len(empty)-1)); err != nil {
		t.Fatal(err)
	}
	f.waitBytes(t, f.capacity)
}
func TestWriteLifecycleActiveCancel(t *testing.T) {
	for _, delayedDisposal := range []bool{false, true} {
		t.Run(fmt.Sprint(delayedDisposal), func(t *testing.T) {
			testWriteLifecycleActiveCancel(t, delayedDisposal)
		})
	}
}

func testWriteLifecycleActiveCancel(t *testing.T, delayedDisposal bool) {
	grace := StopGrace
	StopGrace = 20 * time.Millisecond
	t.Cleanup(func() { StopGrace = grace })
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	observer := &terminalObserver{}
	var preparations atomic.Int32
	var disposed atomic.Bool
	f := lifecycleStart(t, true, func(s *Spec) {
		s.Observe = observer
		s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
			preparations.Add(1)
			return command, args, nil, nil, func() {
				close(entered)
				if delayedDisposal {
					select {
					case <-release:
					case <-time.After(3 * time.Second):
					}
				}
				disposed.Store(true)
			}, nil
		}
	})
	// Release before the fixture's cleanup waits, including on a failed assertion.
	t.Cleanup(unblock)
	f.p.mu.Lock()
	peer := f.p.sess.peer
	f.p.mu.Unlock()
	peer.Cancels = map[string]bool{"page": true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := make(chan error, 1)
	go func() { call <- f.p.Call(ctx, "page", nil, nil) }()
	f.command(t, "read")
	if got := f.line(t); got != "read page" {
		t.Fatal(got)
	}
	f.fill(t)
	cancel()
	select {
	case err := <-call:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		t.Log("PASS real Proc caller released during admitted cancel write")
		// A withdrawal Write whose child consumes nothing retires the owned
		// transport after rpc.StallWindow.
		select {
		case <-peer.Done():
		case <-time.After(rpc.StallWindow + 5*time.Second):
			t.Fatal("TIMEOUT owned cancel transport interruption")
		}
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		stopErr := f.p.StopContext(ctx)
		if delayedDisposal && !errors.Is(stopErr, ErrTeardownTimeout) {
			t.Fatal("FAIL held disposal did not report teardown failure", stopErr)
		}
		if stopErr != nil {
			var failure *teardownFailure
			if !errors.Is(stopErr, ErrTeardownTimeout) || !errors.As(stopErr, &failure) || failure.cause != nil {
				t.Fatal("FAIL unexpected teardown error", stopErr)
			}
			f.p.mu.Lock()
			retained := f.p.teardownErr
			f.p.mu.Unlock()
			if !errors.Is(retained, ErrTeardownTimeout) {
				t.Fatal("FAIL teardown failure not retained")
			}
			assertTeardownLocalized(t, stopErr)
			t.Log("PASS bounded teardown failure remains distinct from actual completion")
		}
		if delayedDisposal {
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("TIMEOUT actual reap/disposal entry")
			}
			gone, groupErr := ownedGroupAbsent(f.pid)
			if alive(f.pid) || !gone || groupErr != nil || disposed.Load() {
				t.Fatal("FAIL disposal gate did not follow actual reap and group absence", groupErr)
			}
			select {
			case <-f.p.Done():
				t.Fatal("FAIL held disposal reported ownership complete")
			default:
			}
			if observer.active.Load() != 1 || observer.starts.Load() != 1 || observer.stops.Load()+observer.exits.Load() != 0 || preparations.Load() != 1 {
				t.Fatal("FAIL teardown failure released active run or admitted replacement")
			}
			if state, _ := f.p.Status(); state != StateError {
				t.Fatal("FAIL teardown failure reported stopped")
			}
		}
		unblock()
		select {
		case <-f.p.Done():
		case <-ctx.Done():
			t.Fatal("TIMEOUT actual admitted cancel teardown completion")
		}
		gone, groupErr := ownedGroupAbsent(f.pid)
		if alive(f.pid) || !gone || groupErr != nil || !disposed.Load() {
			t.Fatal("FAIL admitted cancel completion before group cleanup, reap and disposal", groupErr)
		}
		if observer.active.Load() != 0 || observer.starts.Load() != 1 || observer.stops.Load()+observer.exits.Load() != 1 || preparations.Load() != 1 {
			t.Fatal("FAIL admitted cancel terminal event or replacement")
		}
		if stopErr != nil && !errors.Is(f.p.StopContext(ctx), ErrTeardownTimeout) {
			t.Fatal("FAIL late completion erased teardown failure")
		}
		t.Log("PASS admitted cancel independently completed owned group cleanup, reap and disposal")
	case <-time.After(250 * time.Millisecond):
		t.Fatal("TIMEOUT active cancel")
	}
}
func TestWriteLifecycleInlinePing(t *testing.T) {
	// Fill returns before the first tick; admission is observed below.
	old, grace := PingEvery, StopGrace
	PingEvery, StopGrace = 200*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { PingEvery, StopGrace = old, grace })
	f := lifecycleStart(t, true)
	f.fill(t)
	// Read-only semaphore observation establishes actual admission, not elapsed
	// ticker time. No other write runs after fill returned.
	f.p.mu.Lock()
	peer := f.p.sess.peer
	f.p.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for reflect.ValueOf(peer).Elem().FieldByName("wsem").Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("TIMEOUT inline ping admission")
		}
		f.command(t, "occupancy")
		_ = f.line(t)
	}
	stopped := make(chan error, 1)
	go func() { f.p.Stop(); stopped <- nil }()
	select {
	case <-stopped:
		if alive(f.pid) {
			t.Fatal("FAIL inline ping Stop before reap")
		}
		t.Log("PASS real Proc inline ping shutdown reaped")
	case <-time.After(250 * time.Millisecond):
		t.Fatal("TIMEOUT inline ping Stop")
	}
}

func TestWriteLifecycleConfiguredTimeout(t *testing.T) {
	grace := StopGrace
	StopGrace = 20 * time.Millisecond
	t.Cleanup(func() { StopGrace = grace })
	for _, caller := range []bool{false, true} {
		t.Run(fmt.Sprint(caller), func(t *testing.T) {
			f := lifecycleStart(t, true, func(s *Spec) { s.CallTimeout = time.Second })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if caller {
				ctx, cancel = context.WithTimeout(context.Background(), 700*time.Millisecond)
				defer cancel()
			}
			result := make(chan error, 1)
			go func() { result <- f.p.Call(ctx, "action", strings.Repeat("x", 2<<20), nil) }()
			f.waitBytes(t, f.capacity)
			err := lifecycleAwait(t, result)
			if !errors.Is(err, rpc.ErrDropped) {
				t.Fatal("FAIL partial request lost ambiguity", err)
			}
			var re *rpc.Error
			if caller {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("FAIL caller deadline identity", err)
				}
				if errors.As(err, &re) && re.Code == extension.CodeTimeout {
					t.Fatal("FAIL caller expiry became configured timeout")
				}
			} else {
				if !errors.As(err, &re) || re.Code != extension.CodeTimeout {
					t.Fatal("FAIL configured timeout code", err)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("FAIL configured timeout discarded underlying cause", err)
				}
				for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
					want := i18n.T(lang, "extension-status.call_timeout", i18n.Args{"name": f.p.spec.Name, "method": "action", "duration": f.p.spec.callTimeout().String()})
					if got := i18n.Localize(err, lang); got != want {
						t.Fatalf("FAIL configured timeout localization %s: %q, want %q", lang, got, want)
					}
				}
				if err.Error() != i18n.Localize(err, i18n.En) {
					t.Fatal("FAIL ordinary error text differs from English summary")
				}
			}
			t.Log("PASS real Proc active write timeout identity", caller)
		})
	}
}

func TestWriteLifecycleStartupDeadline(t *testing.T) {
	ready, grace, restart := ReadyWithin, StopGrace, RestartFirst
	ReadyWithin, StopGrace, RestartFirst = 80*time.Millisecond, 20*time.Millisecond, time.Hour
	t.Cleanup(func() { ReadyWithin, StopGrace, RestartFirst = ready, grace, restart })
	f := lifecycleStart(t, false)
	f.waitBytes(t, f.capacity)
	// The expired initialization Write is interrupted once it has stalled.
	ctx, cancel := context.WithTimeout(context.Background(), rpc.StallWindow+3*time.Second)
	defer cancel()
	if err := f.p.Wait(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("FAIL initialization deadline did not terminate run", err)
	}
	err := f.p.StopContext(ctx)
	if err != nil && !errors.Is(err, ErrTeardownTimeout) {
		t.Fatal(err)
	}
	if errors.Is(err, ErrTeardownTimeout) {
		// Reporting is independent of actual completion. Under scheduler load a
		// 60 ms budget may expire first; it must never be mistaken for reap.
		f.p.mu.Lock()
		retained := f.p.teardownErr
		f.p.mu.Unlock()
		if !errors.Is(retained, ErrTeardownTimeout) {
			t.Fatal("FAIL teardown failure not retained")
		}
		t.Log("PASS startup expiry reported bounded failure, waiting for actual ownership completion")
	}
	select {
	case <-f.p.Done():
	case <-ctx.Done():
		t.Fatal("TIMEOUT actual initialization teardown completion")
	}
	if alive(f.pid) {
		t.Fatal("FAIL initialization completion before actual reap")
	}
	t.Log("PASS real Proc initialization expiry independently reaped owned child")
}

func TestWriteLifecycleEOFAndLateResponse(t *testing.T) {
	grace, restart := StopGrace, RestartFirst
	StopGrace, RestartFirst = 20*time.Millisecond, time.Hour
	t.Cleanup(func() { StopGrace, RestartFirst = grace, restart })
	f := lifecycleStart(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- f.p.Call(ctx, "page", nil, nil) }()
	f.command(t, "read")
	if f.line(t) != "read page" {
		t.Fatal("FAIL dispatched page")
	}
	cancel()
	if err := lifecycleAwait(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.command(t, "late")
	if f.line(t) != "late sent" {
		t.Fatal("FAIL late response handshake")
	}
	var out bool
	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	go func() { result <- f.p.Call(nextCtx, "next", nil, &out) }()
	f.command(t, "read")
	if f.line(t) != "read next" {
		t.Fatal("FAIL late cancel frame")
	}
	f.command(t, "reply")
	if f.line(t) != "reply sent" {
		t.Fatal("FAIL reply handshake")
	}
	if err := lifecycleAwait(t, result); err != nil || !out {
		t.Fatal("FAIL late response contaminated next call", err)
	}
	f.p.mu.Lock()
	peer := f.p.sess.peer
	f.p.mu.Unlock()
	f.command(t, "close-output")
	if f.line(t) != "output closed" {
		t.Fatal("FAIL EOF handshake")
	}
	select {
	case <-peer.Done():
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT EOF peer closure")
	}
	if !errors.Is(peer.Err(), io.EOF) {
		t.Fatal("FAIL EOF reason", peer.Err())
	}
	if err := f.p.StopContext(nextCtx); err != nil {
		t.Fatal(err)
	}
	if alive(f.pid) {
		t.Fatal("FAIL EOF retained owned child")
	}
	t.Log("PASS real Proc late response ignored and EOF independently reaped")
}

func TestWriteLifecycleOldEpochCannotKillReplacement(t *testing.T) {
	grace := StopGrace
	StopGrace = 20 * time.Millisecond
	t.Cleanup(func() { StopGrace = grace })
	s := spec(t, "rpc")
	s.Startup = true
	p := Start(context.Background(), s)
	t.Cleanup(p.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	first := p.Generation()
	p.mu.Lock()
	oldSession := p.sess
	oldPeer, oldKill := oldSession.peer, p.kill
	p.mu.Unlock()
	oldPeer.Close(io.EOF)
	for {
		changed := p.Changes()
		if p.Generation() != 0 && p.Generation() != first {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("TIMEOUT replacement initialization")
		}
	}
	replacement := p.Generation()
	replacementPID := pid(t, p)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Go(func() { oldPeer.Close(io.EOF); oldKill(); p.strike(oldSession, "late timeout") })
	}
	done := make(chan error, 1)
	go func() { wg.Wait(); done <- nil }()
	_ = lifecycleAwait(t, done)
	if p.Generation() != replacement || pid(t, p) != replacementPID || !alive(replacementPID) {
		t.Fatal("FAIL stale cleanup touched replacement")
	}
	t.Log("PASS old run close/kill races cannot target replacement epoch")
}

func TestWriteLifecycleOnlyOwnedChildStopped(t *testing.T) {
	grace := StopGrace
	StopGrace = 20 * time.Millisecond
	t.Cleanup(func() { StopGrace = grace })
	first, neighbor := lifecycleStart(t, true), lifecycleStart(t, true)
	if first.pid == neighbor.pid {
		t.Fatal("FAIL child identity handshake")
	}
	generation := neighbor.p.Generation()
	active := make(chan error, 1)
	go func() { active <- first.p.Notify("large", strings.Repeat("x", 2<<20)) }()
	first.waitBytes(t, first.capacity)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := first.p.StopContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleAwait(t, active); err == nil {
		t.Fatal("FAIL active write not interrupted")
	}
	if alive(first.pid) || !alive(neighbor.pid) || neighbor.p.Generation() != generation {
		t.Fatal("FAIL stop escaped owned run")
	}
	next := make(chan error, 1)
	go func() { next <- neighbor.p.Call(ctx, "next", nil, nil) }()
	neighbor.command(t, "read")
	if neighbor.line(t) != "read next" {
		t.Fatal("FAIL neighbor dispatch")
	}
	neighbor.command(t, "reply")
	if neighbor.line(t) != "reply sent" {
		t.Fatal("FAIL neighbor reply")
	}
	if err := lifecycleAwait(t, next); err != nil {
		t.Fatal("FAIL healthy neighbor affected", err)
	}
	t.Log("PASS active writer shutdown terminated only its owned child")
}

func TestWriteLifecycleFailedDisposalRetainsRun(t *testing.T) {
	grace := StopGrace
	StopGrace = 20 * time.Millisecond
	t.Cleanup(func() { StopGrace = grace })
	entered, release := make(chan struct{}), make(chan struct{})
	socket := filepath.Join(t.TempDir(), "http.sock")
	var starts atomic.Int32
	f := lifecycleStart(t, true, func(s *Spec) {
		s.HTTP, s.Socket = true, socket
		s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
			starts.Add(1)
			if err := os.WriteFile(socket, []byte("owned socket marker"), 0o600); err != nil {
				return "", nil, nil, nil, nil, err
			}
			return command, args, nil, nil, func() { close(entered); <-release }, nil
		}
	})
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRun)
	f.command(t, "exit")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("TIMEOUT actual Wait/disposal entry")
	}
	// This is an actual owned child and one real cmd.Wait, followed by a stuck
	// disposal hook. It models retained ownership, not a kernel refusing SIGKILL.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.p.StopContext(ctx); !errors.Is(err, ErrTeardownTimeout) {
		t.Fatal("FAIL bounded teardown report", err)
	}
	select {
	case <-f.p.Done():
		t.Fatal("FAIL unresolved ownership reported complete")
	default:
	}
	if starts.Load() != 1 {
		t.Fatal("FAIL replacement admitted before disposal")
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatal("FAIL owned socket removed before disposal", err)
	}
	if state, _ := f.p.Status(); state != StateError {
		t.Fatal("FAIL teardown reported stopped")
	}
	legacyStopped := make(chan struct{})
	go func() { f.p.Stop(); close(legacyStopped) }()
	select {
	case <-legacyStopped:
		t.Fatal("FAIL legacy Stop released deferred ownership before completion")
	case <-time.After(50 * time.Millisecond):
	}
	releaseRun()
	select {
	case <-f.p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("TIMEOUT late disposal completion")
	}
	select {
	case <-legacyStopped:
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT legacy Stop after actual disposal")
	}
	if starts.Load() != 1 {
		t.Fatal("FAIL replacement after terminal teardown failure")
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("FAIL socket retained after late disposal", err)
	}
	t.Log("PASS failed bounded teardown retained owned run until actual disposal")
}
