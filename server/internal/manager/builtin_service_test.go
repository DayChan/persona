package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/update"
)

// The names the hub's setup prints are the ones install --builtin uses.
func TestBuiltinServiceNames(t *testing.T) {
	if unitName(assistantwire.AssistantID) != managerconf.BuiltinService+".service" || launchdLabel(assistantwire.AssistantID) != managerconf.BuiltinLabel {
		t.Fatalf("%s %s", unitName(assistantwire.AssistantID), launchdLabel(assistantwire.AssistantID))
	}
	unit := builtinUnit("/home/someone/.persona/bin/personad", "/home/someone/.persona", false)
	for _, line := range []string{`ExecStart="/home/someone/.persona/bin/personad" assistant`, `Environment="PERSONA_ASSISTANT_DEPLOY=service"`,
		`Environment="PERSONA_HOME=/home/someone/.persona"`, "Restart=always", "RestartPreventExitStatus=2", "WantedBy=default.target"} {
		if !strings.Contains(unit, line+"\n") {
			t.Errorf("unit has no %q:\n%s", line, unit)
		}
	}
	plist := builtinPlist("/Users/someone/.persona/bin/personad", "/Users/someone/.persona", "/Users/someone")
	for _, part := range []string{"<string>" + managerconf.BuiltinLabel + "</string>",
		"<string>/Users/someone/.persona/bin/personad</string><string>assistant</string>",
		"<key>PERSONA_ASSISTANT_DEPLOY</key><string>service</string>", "<key>PERSONA_HOME</key><string>/Users/someone/.persona</string>",
		"<string>/Users/someone/.persona/managers/assistant/manager.log</string>"} {
		if !strings.Contains(plist, part) {
			t.Errorf("plist has no %q:\n%s", part, plist)
		}
	}
}

// joinCounter is builtinHub counting the trades it was asked for.
func joinCounter(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	joins := builtinJoins()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		joins.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

type fakeServices struct {
	calls  []string
	active bool
}

func (f *fakeServices) env(t *testing.T, goos string) (*env, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	home, _ := os.UserHomeDir()
	return &env{stdout: &out, goos: goos, home: home, uid: 501, exe: func() (string, error) { return "/opt/personad", nil },
		look:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		getenv: func(string) string { return "" },
		run: func(_ context.Context, name string, args ...string) (string, error) {
			call := name + " " + strings.Join(args, " ")
			f.calls = append(f.calls, call)
			if (strings.Contains(call, "is-active") || strings.HasPrefix(call, "launchctl print")) && !f.active {
				return "inactive\n", errors.New("exit status 3")
			}
			return "", nil
		}}, &out
}

func (f *fakeServices) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// install --builtin: not bound and no code, nothing is installed (exit 2). With --join and a code it binds, writes the
// unit and starts it. Again with a code while the hub takes its token: the binding stays (the code is not traded) and
// the running service is left alone. When the hub no longer takes it, the code binds it again and the service
// restarts on the new binding.
func TestInstallBuiltinSystemd(t *testing.T) {
	home := isolate(t)
	data := filepath.Join(home, ".persona")
	srv, trades := joinCounter(t)
	defer func(f func(context.Context, *assistant.ManagerConfig) error) { probeToken = f }(probeToken)
	taken := true
	probeToken = func(_ context.Context, c *assistant.ManagerConfig) error {
		if c.Hub != srv.URL || c.Token != "jb_builtin" {
			t.Errorf("probed %+v", c)
		}
		if taken {
			return nil
		}
		return bus.ErrRevoked
	}
	f := &fakeServices{}
	e, out := f.env(t, "linux")
	unit := filepath.Join(home, ".config", "systemd", "user", "persona-manager-assistant.service")

	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin"}); !errors.As(err, new(ConfigError)) || fileExists(unit) {
		t.Fatalf("not bound: %v, unit there: %v", err, fileExists(unit))
	}
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--join", srv.URL}); !errors.As(err, new(ConfigError)) ||
		!strings.Contains(err.Error(), managerconf.BuiltinCodeEnv) || trades() != 0 {
		t.Fatalf("no code: %v", err)
	}

	e.getenv = func(k string) string {
		if k == managerconf.BuiltinCodeEnv {
			return "mi_builtin"
		}
		return ""
	}
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--join", srv.URL}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, err := managerconf.ReadBinding(data); err != nil || b.Token != "jb_builtin" || b.Hub != srv.URL || trades() != 1 {
		t.Fatalf("binding %+v %v, trades %d", b, err, trades())
	}
	text, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(text), `ExecStart="/opt/personad" assistant`) || !strings.Contains(string(text), `PERSONA_HOME=`+data+`"`) {
		t.Fatalf("unit %s %v", text, err)
	}
	for _, c := range []string{"systemctl --user daemon-reload", "systemctl --user enable persona-manager-assistant.service", "systemctl --user restart persona-manager-assistant.service"} {
		if !f.called(c) {
			t.Fatalf("no %q in %v", c, f.calls)
		}
	}

	// Again: the hub takes its token, the service runs: nothing traded, nothing restarted.
	f.calls, f.active = nil, true
	out.Reset()
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--join", srv.URL}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if trades() != 1 || f.called("systemctl --user restart") || !strings.Contains(out.String(), "Binding: kept") {
		t.Fatalf("trades %d, calls %v\n%s", trades(), f.calls, out)
	}

	// The hub refuses its token now (bound again elsewhere, a hub back in child mode): the code binds it again.
	taken = false
	f.calls = nil
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--join", srv.URL}); err != nil {
		t.Fatal(err)
	}
	if trades() != 2 || !f.called("systemctl --user restart persona-manager-assistant.service") {
		t.Fatalf("trades %d, calls %v", trades(), f.calls)
	}

	// status, then uninstall: the unit goes, the binding stays.
	out.Reset()
	if err := cmdStatus(context.Background(), e, nil); err != nil || !strings.Contains(out.String(), "Built-in manager (assistant)") ||
		!strings.Contains(out.String(), "Binding: hub "+srv.URL) {
		t.Fatalf("status %v\n%s", err, out)
	}
	if err := Main(context.Background(), update.Build{}, []string{"uninstall", "--builtin", "extra"}, out); !errors.Is(err, errUsage) {
		t.Fatalf("uninstall with more: %v", err)
	}
	if err := cmdUninstallBuiltin(context.Background(), e, []string{"--builtin"}); err != nil || fileExists(unit) ||
		!f.called("systemctl --user disable --now persona-manager-assistant.service") {
		t.Fatalf("uninstall %v %v", err, f.calls)
	}
	if _, err := managerconf.ReadBinding(data); err != nil {
		t.Fatalf("binding after uninstall: %v", err)
	}
}

// What stops the install stops it before the code is spent: no systemd user instance, another system.
func TestInstallBuiltinChecksFirst(t *testing.T) {
	isolate(t)
	srv, trades := joinCounter(t)
	for _, c := range []struct {
		goos      string
		noSystemd bool
	}{{"linux", true}, {"windows", false}} {
		f := &fakeServices{}
		e, _ := f.env(t, c.goos)
		if c.noSystemd {
			e.look = func(string) (string, error) { return "", os.ErrNotExist }
		}
		j := builtinJoin{hub: srv.URL, code: "mi_builtin", always: true}
		if err := e.installBuiltin(context.Background(), dataDir(), j); err == nil || trades() != 0 {
			t.Fatalf("%s: %v, trades %d", c.goos, err, trades())
		}
	}
}

// join --builtin --install binds and installs; the code is always traded, and a service on the old binding restarts.
func TestJoinBuiltinInstall(t *testing.T) {
	home := isolate(t)
	srv, trades := joinCounter(t)
	f := &fakeServices{active: true}
	e, out := f.env(t, "darwin")
	if err := cmdJoin(context.Background(), e, []string{"--builtin", srv.URL, "mi_builtin", "--install"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", managerconf.BuiltinLabel+".plist")
	if trades() != 1 || !fileExists(plist) || !f.called("launchctl bootstrap gui/501 "+plist) {
		t.Fatalf("trades %d, calls %v", trades(), f.calls)
	}
	// The same again: traded again (join always trades), the agent unchanged and loaded, so kickstarted.
	f.calls = nil
	if err := cmdJoin(context.Background(), e, []string{"--builtin", srv.URL, "mi_builtin", "--install"}); err != nil {
		t.Fatal(err)
	}
	if trades() != 2 || !f.called("launchctl kickstart -k gui/501/"+managerconf.BuiltinLabel) || f.called("launchctl bootstrap") {
		t.Fatalf("trades %d, calls %v", trades(), f.calls)
	}
}

// newBinaryEnv runs RunBuiltin's watch fast in a test: bound, running from a file of its own (bin) that answers
// `version -v`, the manager a stand-in (main), restart counted (exec fails with execErr, else as though it started
// again). It returns bin; write puts a new file there as an update does (renamed over it).
func newBinaryEnv(t *testing.T, execErr error) (bin string, write func(script string, mode os.FileMode), restarts *atomic.Int64) {
	t.Helper()
	home := isolate(t)
	if err := managerconf.WriteBinding(filepath.Join(home, ".persona"), managerconf.Binding{Hub: "http://127.0.0.1:1", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	type saved struct {
		check, idle, most time.Duration
		restart           func([]string) error
		running           func() (string, os.FileInfo, bool)
		main              func(context.Context, update.Build, io.Reader, assistant.MainHooks) error
	}
	old := saved{binaryCheck, idleCheck, idleMost, restart, runningBinary, runAssistant}
	t.Cleanup(func() {
		binaryCheck, idleCheck, idleMost, restart, runningBinary, runAssistant = old.check, old.idle, old.most, old.restart, old.running, old.main
	})
	binaryCheck, idleCheck = 5*time.Millisecond, 5*time.Millisecond
	bin = filepath.Join(home, "personad")
	n := 0
	write = func(script string, mode os.FileMode) {
		n++
		tmp := fmt.Sprintf("%s.new%d", bin, n)
		if err := os.WriteFile(tmp, []byte(script), mode); err != nil {
			t.Error(err)
		}
		if err := os.Rename(tmp, bin); err != nil {
			t.Error(err)
		}
	}
	write(goodBinary, 0o755)
	runningBinary = func() (string, os.FileInfo, bool) {
		fi, err := os.Stat(bin)
		return bin, fi, err == nil
	}
	restarts = &atomic.Int64{}
	restart = func([]string) error {
		restarts.Add(1)
		if execErr != nil {
			return execErr
		}
		cancelAfterRestart() // as though this process were another now: the test's RunBuiltin ends
		return nil
	}
	return bin, write, restarts
}

// cancelAfterRestart ends the RunBuiltin of a test whose restart "worked" (set by runBuiltin).
var cancelAfterRestart = func() {}

const goodBinary = "#!/bin/sh\necho personad test\n"

// runBuiltin runs RunBuiltin until done is closed (or a restart worked), with main standing in for the manager:
// it is given the idle the manager would give, and runs until its context ends. It returns the output, how many
// times the manager ran, and RunBuiltin's error.
func runBuiltin(t *testing.T, idle assistant.Idle, during func(run int), done <-chan struct{}) (string, int, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAfterRestart = cancel
	t.Cleanup(func() { cancelAfterRestart = func() {} })
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	// A test that goes wrong ends here rather than at go test's timeout.
	guard := time.AfterFunc(20*time.Second, func() {
		t.Errorf("RunBuiltin did not end")
		cancel()
	})
	defer guard.Stop()
	var runs atomic.Int64
	runAssistant = func(run context.Context, _ update.Build, _ io.Reader, h assistant.MainHooks) error {
		if run.Err() != nil { // after a restart that "worked": this process would be another one by now
			return nil
		}
		n := int(runs.Add(1))
		if h.Ready != nil {
			h.Ready(idle)
		}
		if during != nil {
			go during(n)
		}
		<-run.Done()
		return nil
	}
	var out syncBuffer
	err := RunBuiltin(ctx, update.Build{}, &out)
	return out.String(), int(runs.Load()), err
}

// syncBuffer is a strings.Builder for writers on several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// (a) A new binary that runs: the manager starts again on it only once it is idle, saying what it waits for.
func TestBuiltinNewBinaryWaitsForIdle(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, nil)
	var busy atomic.Bool
	busy.Store(true)
	idle := func(context.Context) (bool, string) {
		if busy.Load() {
			return false, "its conversation c1 is on a turn"
		}
		return true, ""
	}
	out, _, err := runBuiltin(t, idle, func(run int) {
		write(goodBinary+"# new\n", 0o755)
		time.Sleep(150 * time.Millisecond)
		if restarts.Load() != 0 {
			t.Error("started again while busy")
		}
		busy.Store(false)
	}, nil)
	if err != nil || restarts.Load() != 1 || !strings.Contains(out, "waiting to start again on the new binary: its conversation c1 is on a turn") ||
		!strings.Contains(out, "starting again on the new") {
		t.Fatalf("err %v, restarts %d\n%s", err, restarts.Load(), out)
	}
}

// (a) Busy for longer than idleMost: it starts again anyway, and says so.
func TestBuiltinNewBinaryIdleBound(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, nil)
	idleMost = 50 * time.Millisecond
	out, _, err := runBuiltin(t, func(context.Context) (bool, string) { return false, "answering 1 request(s) of the hub's" },
		func(int) { write(goodBinary+"# new\n", 0o755) }, nil)
	if err != nil || restarts.Load() != 1 || !strings.Contains(out, "still not idle after 50ms (answering 1 request(s) of the hub's): starting again on the new binary anyway") {
		t.Fatalf("err %v, restarts %d\n%s", err, restarts.Load(), out)
	}
}

// (b), (c) A new file that does not run (not executable, or `version -v` failing) is checked once and not started
// on: the manager goes on with its code until another file is there; one that runs then is.
func TestBuiltinNewBinaryMustRun(t *testing.T) {
	bin, write, restarts := newBinaryEnv(t, nil)
	counter := bin + ".checks"
	failing := "#!/bin/sh\necho x >> '" + counter + "'\nexit 1\n"
	checks := func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
	done, started := make(chan struct{}), make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, _, err = runBuiltin(t, func(context.Context) (bool, string) { return true, "" }, func(run int) {
			if run == 1 {
				close(started)
			}
		}, nil)
	}()
	<-started // watching the file there at start
	write(goodBinary+"# not executable\n", 0o644)
	time.Sleep(100 * time.Millisecond)
	write(failing, 0o755)
	waitFor(t, "the failing binary checked", func() bool { return checks() >= 1 })
	time.Sleep(150 * time.Millisecond) // many more looks: still checked once
	if n := checks(); n != 1 || restarts.Load() != 0 {
		t.Fatalf("checked %d times, restarts %d", n, restarts.Load())
	}
	write(goodBinary+"# good\n", 0o755)
	<-done
	if err != nil || restarts.Load() != 1 || strings.Count(out, "does not run") != 2 || !strings.Contains(out, "is not an executable file") {
		t.Fatalf("err %v, restarts %d\n%s", err, restarts.Load(), out)
	}
}

// (b), (c) An exec that fails leaves the manager running the code it has (it runs again in this process), and that
// file is not tried again; another one is.
func TestBuiltinExecFails(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, errors.New("no exec here"))
	release := make(chan struct{})
	out, runs, err := runBuiltin(t, func(context.Context) (bool, string) { return true, "" }, func(run int) {
		switch run {
		case 1:
			write(goodBinary+"# new\n", 0o755)
		case 2: // the old code runs again: the same file is not tried again
			time.Sleep(150 * time.Millisecond)
			if restarts.Load() != 1 {
				t.Errorf("restarts %d", restarts.Load())
			}
			write(goodBinary+"# newer\n", 0o755)
		case 3:
			close(release)
		}
	}, release)
	if err != nil || runs != 3 || restarts.Load() != 2 || strings.Count(out, "could not start again on the new") != 2 ||
		!strings.Contains(out, "going on with this one until it is replaced again") {
		t.Fatalf("err %v, runs %d, restarts %d\n%s", err, runs, restarts.Load(), out)
	}
}

// (c) The file there at start is never started on, whatever it is (one that does not answer is not checked again
// and again); nor is one written over the same bytes the way it was.
func TestBuiltinBinaryAtStartNotTried(t *testing.T) {
	bin, write, restarts := newBinaryEnv(t, nil)
	counter := bin + ".checks"
	write("#!/bin/sh\necho x >> '"+counter+"'\nexit 1\n", 0o755)
	release := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() { close(release) })
	out, runs, err := runBuiltin(t, func(context.Context) (bool, string) { return true, "" }, nil, release)
	if b, _ := os.ReadFile(counter); err != nil || runs != 1 || restarts.Load() != 0 || len(b) != 0 || strings.Contains(out, "replaced") {
		t.Fatalf("err %v, runs %d, restarts %d, checks %q\n%s", err, runs, restarts.Load(), b, out)
	}
}

// Stopped while it waits for idle: no restart.
func TestBuiltinStopsWhileWaitingForIdle(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, nil)
	release := make(chan struct{})
	out, _, err := runBuiltin(t, func(context.Context) (bool, string) { return false, "busy" }, func(int) {
		write(goodBinary+"# new\n", 0o755)
		time.Sleep(100 * time.Millisecond)
		close(release)
	}, release)
	if err != nil || restarts.Load() != 0 || !strings.Contains(out, "waiting to start again") {
		t.Fatalf("err %v, restarts %d\n%s", err, restarts.Load(), out)
	}
}

// (b) A new binary whose `version -v` hangs does not run: killed after versionWait, not started on, not checked again.
func TestBuiltinNewBinaryHangs(t *testing.T) {
	bin, write, restarts := newBinaryEnv(t, nil)
	defer func(w time.Duration) { versionWait = w }(versionWait)
	versionWait = 100 * time.Millisecond
	counter := bin + ".checks"
	release := make(chan struct{})
	out, _, err := runBuiltin(t, func(context.Context) (bool, string) { return true, "" }, func(int) {
		write("#!/bin/sh\necho x >> '"+counter+"'\nsleep 30\n", 0o755)
		waitFor(t, "the hanging binary checked", func() bool { b, _ := os.ReadFile(counter); return len(b) > 0 })
		time.Sleep(400 * time.Millisecond) // past versionWait, and many more looks
		close(release)
	}, release)
	b, _ := os.ReadFile(counter)
	if err != nil || restarts.Load() != 0 || strings.Count(string(b), "x") != 1 || !strings.Contains(out, "does not run") {
		t.Fatalf("err %v, restarts %d, checks %q\n%s", err, restarts.Load(), b, out)
	}
}

// (a) An Idle that does not answer (its database stuck) counts as busy; idleMost still holds.
func TestBuiltinIdleThatHangs(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, nil)
	idleMost = 100 * time.Millisecond
	stuck := make(chan struct{})
	defer close(stuck)
	out, _, err := runBuiltin(t, func(context.Context) (bool, string) { <-stuck; return true, "" },
		func(int) { write(goodBinary+"# new\n", 0o755) }, nil)
	if err != nil || restarts.Load() != 1 || !strings.Contains(out, "does not answer") || !strings.Contains(out, "anyway") {
		t.Fatalf("err %v, restarts %d\n%s", err, restarts.Load(), out)
	}
}

// (b) Checked again right before the exec: a file that stopped running while it waited for idle is not started on;
// the manager runs again on its old code, and that file is not tried again. One put back while it waited (the very
// file it started with, renamed back) is not started on either.
func TestBuiltinCheckedAgainBeforeExec(t *testing.T) {
	for _, c := range []struct {
		name string
		then func(bin string, write func(string, os.FileMode))
		want string
	}{
		{"stopped running", func(bin string, write func(string, os.FileMode)) {
			write("#!/bin/sh\nexit 3\n", 0o755)
		}, "does not run any more"},
		{"put back", func(bin string, _ func(string, os.FileMode)) {
			if err := os.Rename(bin+".start", bin); err != nil {
				t.Error(err)
			}
		}, "put back"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, write, restarts := newBinaryEnv(t, nil)
			if err := os.Link(bin, bin+".start"); err != nil { // keep the start file to put it back
				t.Fatal(err)
			}
			var asked atomic.Bool
			release := make(chan struct{})
			// Idle is asked only once the new file passed its check: the file changes then, before the answer.
			out, runs, err := runBuiltin(t, func(context.Context) (bool, string) {
				if asked.CompareAndSwap(false, true) {
					c.then(bin, write)
				}
				return true, ""
			}, func(run int) {
				switch run {
				case 1:
					write(goodBinary+"# new\n", 0o755)
				case 2:
					time.Sleep(100 * time.Millisecond) // not tried again
					close(release)
				}
			}, release)
			if err != nil || runs != 2 || restarts.Load() != 0 || !strings.Contains(out, c.want) {
				t.Fatalf("err %v, runs %d, restarts %d\n%s", err, runs, restarts.Load(), out)
			}
		})
	}
}

// (a) An answer of Idle that came too late is not gone by: it is asked anew (and is busy now).
func TestBuiltinLateIdleAnswerDropped(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, nil)
	idleCheck = 20 * time.Millisecond
	var calls atomic.Int64
	release := make(chan struct{})
	time.AfterFunc(400*time.Millisecond, func() { close(release) })
	out, _, err := runBuiltin(t, func(context.Context) (bool, string) {
		if calls.Add(1) == 1 {
			time.Sleep(60 * time.Millisecond) // past idleCheck: an old "idle" by the time it comes
			return true, ""
		}
		return false, "busy now"
	}, func(int) { write(goodBinary+"# new\n", 0o755) }, release)
	if err != nil || restarts.Load() != 0 || calls.Load() < 2 || !strings.Contains(out, "does not answer") || !strings.Contains(out, "busy now") {
		t.Fatalf("err %v, restarts %d, calls %d\n%s", err, restarts.Load(), calls.Load(), out)
	}
}

// After a refused token, it starts again in place only on a file that runs: one that does not (replaced meanwhile)
// leaves it running again on the code it has.
func TestBuiltinRevokedStartsAgainOnlyOnAFileThatRuns(t *testing.T) {
	_, write, restarts := newBinaryEnv(t, nil)
	binaryCheck = time.Hour // the watch out of the way: the revoke's path is the one under test
	defer func(p, r time.Duration, f func(context.Context, *assistant.ManagerConfig) error) {
		revokedPoll, revokedRetry, probeToken = p, r, f
	}(revokedPoll, revokedRetry, probeToken)
	revokedPoll, revokedRetry = 5*time.Millisecond, 10*time.Millisecond
	probeToken = func(context.Context, *assistant.ManagerConfig) error { return nil } // taken again
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := time.AfterFunc(20*time.Second, func() {
		t.Errorf("RunBuiltin did not end")
		cancel()
	})
	defer guard.Stop()
	var runs atomic.Int64
	runAssistant = func(run context.Context, _ update.Build, _ io.Reader, _ assistant.MainHooks) error {
		if runs.Add(1) == 1 {
			write("#!/bin/sh\nexit 1\n", 0o755)
			return bus.ErrRevoked
		}
		cancel()
		return nil
	}
	var out syncBuffer
	if err := RunBuiltin(ctx, update.Build{}, &out); err != nil || runs.Load() != 2 || restarts.Load() != 0 ||
		!strings.Contains(out.String(), "running it again here, on the code it has") {
		t.Fatalf("err %v, runs %d, restarts %d\n%s", err, runs.Load(), restarts.Load(), out.String())
	}
}

// The command lines the hub's setup runs are the ones install --builtin parses, through Main's dispatch.
func TestBuiltinSetupCommandLines(t *testing.T) {
	args := managerconf.BuiltinInstallArgs("http://127.0.0.1:1")
	if args[0] != "manager" || args[1] != "install" || !isBuiltin(args[2:]) {
		t.Fatalf("%v", args)
	}
	a, err := parseInstallBuiltin(args[2:], func(k string) string {
		if k == managerconf.BuiltinCodeEnv {
			return " mi_code\n"
		}
		return ""
	}, io.Discard)
	if err != nil || a.check || a.join.hub != "http://127.0.0.1:1" || a.join.code != "mi_code" || !a.codeTaken {
		t.Fatalf("%+v %v", a, err)
	}
	check := managerconf.BuiltinCheckArgs()
	if !isBuiltin(check[2:]) {
		t.Fatalf("%v", check)
	}
	if a, err := parseInstallBuiltin(check[2:], func(string) string { return "mi_code" }, io.Discard); err != nil || !a.check || a.join.code != "" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := parseInstallBuiltin([]string{"--builtin", "--check", "--join", "http://x"}, os.Getenv, io.Discard); !errors.Is(err, errUsage) {
		t.Fatalf("--check with --join: %v", err)
	}
}

// --check says whether the service can be installed, changing nothing; the code is read once and not handed on.
func TestInstallBuiltinCheck(t *testing.T) {
	home := isolate(t)
	f := &fakeServices{}
	e, out := f.env(t, "linux")
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--check"}); err != nil || !strings.Contains(out.String(), "can be installed") {
		t.Fatalf("%v\n%s", err, out)
	}
	if fileExists(filepath.Join(home, ".config", "systemd", "user", "persona-manager-assistant.service")) || f.called("systemctl --user restart") {
		t.Fatalf("--check changed things: %v", f.calls)
	}
	e.goos = "windows"
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--check"}); err == nil {
		t.Fatal("--check on windows")
	}
	// The code in the environment: gone once read, so systemctl and launchctl never get it.
	t.Setenv(managerconf.BuiltinCodeEnv, "mi_unknown")
	e.goos, e.getenv = "linux", os.Getenv
	srv, _ := joinCounter(t)
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin", "--join", srv.URL}); !errors.As(err, new(ConfigError)) {
		t.Fatalf("unknown code: %v", err)
	}
	if v, ok := os.LookupEnv(managerconf.BuiltinCodeEnv); ok {
		t.Fatalf("still in the environment: %q", v)
	}
}

// join --builtin without a code takes it from the environment.
func TestJoinBuiltinCodeFromEnvironment(t *testing.T) {
	home := isolate(t)
	srv, trades := joinCounter(t)
	f := &fakeServices{}
	e, out := f.env(t, "linux")
	e.getenv = func(k string) string {
		if k == managerconf.BuiltinCodeEnv {
			return "mi_builtin"
		}
		return ""
	}
	if err := cmdJoin(context.Background(), e, []string{"--builtin", srv.URL}); err != nil || trades() != 1 {
		t.Fatalf("%v, trades %d\n%s", err, trades(), out)
	}
	if b, err := managerconf.ReadBinding(filepath.Join(home, ".persona")); err != nil || b.Token != "jb_builtin" {
		t.Fatalf("binding %+v %v", b, err)
	}
	if err := cmdJoin(context.Background(), e, []string{srv.URL}); !errors.Is(err, errUsage) {
		t.Fatalf("a manager's join without a code: %v", err)
	}
}

// install --builtin on a Mac: bootstrapped when not loaded (tried again when bootstrap fails right after the
// bootout), left alone when loaded and unchanged.
func TestInstallBuiltinLaunchd(t *testing.T) {
	home := isolate(t)
	data := filepath.Join(home, ".persona")
	if err := managerconf.WriteBinding(data, managerconf.Binding{Hub: "http://127.0.0.1:1", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	defer func(w time.Duration) { bootstrapWait = w }(bootstrapWait)
	bootstrapWait = time.Millisecond
	f := &fakeServices{}
	e, out := f.env(t, "darwin")
	run := e.run
	fails := 2
	e.run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "launchctl" && args[0] == "bootstrap" && fails > 0 {
			fails--
			f.calls = append(f.calls, "launchctl bootstrap (refused)")
			return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
		}
		return run(ctx, name, args...)
	}
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", managerconf.BuiltinLabel+".plist")
	if !fileExists(plist) || !f.called("launchctl bootstrap gui/501 "+plist) || fails != 0 || !strings.Contains(out.String(), "started") {
		t.Fatalf("calls %v\n%s", f.calls, out)
	}
	f.calls, f.active = nil, true
	out.Reset()
	if err := cmdInstallBuiltin(context.Background(), e, []string{"--builtin"}); err != nil {
		t.Fatal(err)
	}
	if f.called("launchctl bootstrap") || f.called("launchctl kickstart") || f.called("launchctl bootout") || !strings.Contains(out.String(), "left as it was") {
		t.Fatalf("calls %v\n%s", f.calls, out)
	}
}

// The built-in manager's unit (or agent) is replaced in one step when its text changes, never cut short in place, and
// left as it is (the same file) when it does not.
func TestBuiltinServiceReplacesTheFile(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			home := isolate(t)
			data := filepath.Join(home, ".persona")
			f := &fakeServices{}
			e, _ := f.env(t, goos)
			path := e.unitPath(assistantwire.AssistantID)
			if goos == "darwin" {
				path = e.plistPath(assistantwire.AssistantID)
			}
			if err := e.builtinService(context.Background(), data, "/opt/personad", false); err != nil {
				t.Fatal(err)
			}
			old, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			had, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.builtinService(context.Background(), data, "/opt/new/persona-manager", false); err != nil {
				t.Fatal(err)
			}
			checkReplaced(t, path, old, had, "/opt/new/persona-manager")

			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.builtinService(context.Background(), data, "/opt/new/persona-manager", false); err != nil {
				t.Fatal(err)
			}
			if after, err := os.Stat(path); err != nil || !os.SameFile(before, after) {
				t.Fatalf("unchanged text written again: %v", err)
			}
		})
	}
}

// BindBuiltin (a container's supervisor): a binding the hub takes stays and no code is asked for; not bound, or
// refused, a code is asked for and traded; a code that cannot be had is the error.
func TestBindBuiltin(t *testing.T) {
	home := isolate(t)
	data := filepath.Join(home, ".persona")
	srv, trades := joinCounter(t)
	defer func(f func(context.Context, *assistant.ManagerConfig) error) { probeToken = f }(probeToken)
	taken := true
	probeToken = func(context.Context, *assistant.ManagerConfig) error {
		if taken {
			return nil
		}
		return bus.ErrRevoked
	}
	asked := 0
	code := func(context.Context) (string, error) { asked++; return "mi_builtin", nil }
	if traded, err := BindBuiltin(context.Background(), data, srv.URL, code); err != nil || !traded || asked != 1 || trades() != 1 {
		t.Fatalf("not bound: traded %v, err %v, asked %d, trades %d", traded, err, asked, trades())
	}
	if traded, err := BindBuiltin(context.Background(), data, srv.URL, code); err != nil || traded || asked != 1 || trades() != 1 {
		t.Fatalf("bound: traded %v, err %v, asked %d", traded, err, asked)
	}
	taken = false
	if traded, err := BindBuiltin(context.Background(), data, srv.URL, code); err != nil || !traded || asked != 2 || trades() != 2 {
		t.Fatalf("refused: traded %v, err %v, asked %d", traded, err, asked)
	}
	none := errors.New("no database")
	if _, err := BindBuiltin(context.Background(), data, srv.URL, func(context.Context) (string, error) { return "", none }); !errors.Is(err, none) {
		t.Fatalf("no code: %v", err)
	}
}
