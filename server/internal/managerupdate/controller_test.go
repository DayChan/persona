package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

func fixture(t *testing.T) (*Controller, Plan) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "manager")
	bin := filepath.Join(dir, "bin", "personad")
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.persona.manager.m-test.plist")
	exe := filepath.Join(home, "candidate")
	for _, p := range []string{filepath.Dir(bin), filepath.Dir(plist)} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	put := func(path, body string) {
		t.Helper()
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	put(bin, "old image")
	put(plist, "original plist with custom environment")
	put(exe, "new image")
	put(filepath.Join(dir, "manager.json"), `{"id":"m-test","database_url":"postgres://localhost/example","token":"example"}`)
	now := time.Now().UTC()
	hash := func(path string) string {
		h, e := hashFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return h
	}
	p := Plan{Schema: 1, RunID: "test-run", ManagerID: "m-test", Dir: dir, Binary: bin, Unit: "com.persona.manager.m-test", PlistPath: plist, UID: 123, PreviousSHA256: hash(bin), ConfigSHA256: hash(filepath.Join(dir, "manager.json")), PlistSHA256: hash(plist), Source: strings.Repeat("a", 40), Version: "release-example", BinarySHA256: hash(exe), Size: 9, ActivationEnabled: true, BootstrapAuthorized: true, BootstrapWindow: now.Add(time.Minute), IdleVerifiedAt: now.Add(-time.Second), IdleValidUntil: now.Add(30 * time.Second), IdleSnapshotSHA256: strings.Repeat("b", 64)}
	c := &Controller{GOOS: "darwin", UID: 123, Home: home, Exe: exe, Build: update.Build{Channel: update.Release, Number: 202611080101, Version: p.Version, Commit: p.Source}, Now: func() time.Time { return now }, ReleaseCheck: func(Plan, update.Build, string) error { return nil }}
	c.Command = func(ctx context.Context, cmd string, args ...string) ([]byte, error) {
		switch cmd {
		case "/usr/bin/plutil":
			return json.Marshal(map[string]any{"Label": p.Unit, "ProgramArguments": []string{p.Binary, "manager", "run", "--dir", p.Dir}, "EnvironmentVariables": map[string]string{"CUSTOM": "retained"}})
		case "/bin/launchctl":
			if args[0] == "print" {
				return []byte("path = " + p.PlistPath + "\nprogram = " + p.Binary + "\npid = 777\narguments = {\n" + p.Binary + "\nmanager\nrun\n--dir\n" + p.Dir + "\n}\n"), nil
			}
			return nil, nil
		case "/bin/ps":
			return []byte("123 " + p.Binary), nil
		}
		return nil, errors.New("unexpected command")
	}
	return c, p
}
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if e := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		if i.IsDir() {
			m[rel] = "directory"
		} else {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			m[rel] = string(b)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return m
}
func TestCheckIsReadOnlyAndScopeStrict(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*Controller, *Plan)
	}{
		{"valid", func(*Controller, *Plan) {}},
		{"wrong-manager", func(c *Controller, p *Plan) { p.ManagerID = "m-other" }},
		{"wrong-config-hash", func(c *Controller, p *Plan) { p.ConfigSHA256 = strings.Repeat("0", 64) }},
		{"wrong-binary-hash", func(c *Controller, p *Plan) { p.PreviousSHA256 = strings.Repeat("0", 64) }},
		{"shared-exe", func(c *Controller, p *Plan) { p.Binary = c.Exe }},
		{"wrong-uid", func(c *Controller, p *Plan) { c.UID++ }},
		{"wrong-unit", func(c *Controller, p *Plan) { p.Unit = "another.service" }},
		{"wrong-platform", func(c *Controller, p *Plan) { c.GOOS = "linux" }},
		{"dirty-release", func(c *Controller, p *Plan) { c.ReleaseCheck = validateRelease; c.Build.Channel = update.Dev }},
		{"wrong-loaded-unit", func(c *Controller, p *Plan) {
			old := c.Command
			c.Command = func(ctx context.Context, n string, a ...string) ([]byte, error) {
				if n == "/bin/launchctl" {
					return []byte("path = /other\nprogram = /shared\npid = 777"), nil
				}
				return old(ctx, n, a...)
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, p := fixture(t)
			tt.modify(c, &p)
			before := tree(t, c.Home)
			e := c.Check(context.Background(), p)
			if (e == nil) != (tt.name == "valid") {
				t.Fatalf("check error=%v", e)
			}
			if !reflect.DeepEqual(before, tree(t, c.Home)) {
				t.Fatal("check wrote files")
			}
		})
	}
}
func TestStartFrozenOrExpiredHasZeroWrites(t *testing.T) {
	for _, field := range []string{"freeze", "disabled", "unauthorized", "expired-idle", "long-idle", "expired-window"} {
		t.Run(field, func(t *testing.T) {
			c, p := fixture(t)
			switch field {
			case "freeze":
				p.Freeze = true
			case "disabled":
				p.ActivationEnabled = false
			case "unauthorized":
				p.BootstrapAuthorized = false
			case "expired-idle":
				p.IdleValidUntil = c.Now().Add(-time.Second)
			case "long-idle":
				p.IdleValidUntil = c.Now().Add(2 * time.Minute)
			case "expired-window":
				p.BootstrapWindow = c.Now().Add(-time.Second)
			}
			before := tree(t, c.Home)
			if e := c.Start(context.Background(), p); e == nil {
				t.Fatal("expected refusal")
			}
			if !reflect.DeepEqual(before, tree(t, c.Home)) {
				t.Fatal("refused start wrote state")
			}
		})
	}
}
func prepare(t *testing.T, p Plan) {
	t.Helper()
	if e := os.MkdirAll(txDir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(filepath.Join(txDir(p), "receipt.json"), receipt(p, "checked", nil)); e != nil {
		t.Fatal(e)
	}
}
func TestStartKeepsOriginalServiceAndRejectsDuplicate(t *testing.T) {
	c, p := fixture(t)
	original, _ := os.ReadFile(p.PlistPath)
	config, _ := os.ReadFile(filepath.Join(p.Dir, "manager.json"))
	var commands []string
	old := c.Command
	c.Command = func(ctx context.Context, n string, a ...string) ([]byte, error) {
		commands = append(commands, n+" "+strings.Join(a, " "))
		return old(ctx, n, a...)
	}
	if e := c.Start(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if e := c.Start(context.Background(), p); e == nil {
		t.Fatal("duplicate accepted")
	}
	after, _ := os.ReadFile(p.PlistPath)
	afterConfig, _ := os.ReadFile(filepath.Join(p.Dir, "manager.json"))
	if string(original) != string(after) || string(config) != string(afterConfig) {
		t.Fatal("service or config modified")
	}
	for _, s := range commands {
		if strings.Contains(s, "kickstart") || strings.Contains(s, "install") {
			t.Fatal("start touched original service")
		}
	}
}
func TestRunInterruptedActivationAlwaysRollsBack(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	if e := copyExclusive(p.Binary, filepath.Join(txDir(p), "previous")); e != nil {
		t.Fatal(e)
	}
	if e := replaceImage(c.Exe, p.Binary); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(activePath(p), p); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(filepath.Join(txDir(p), "receipt.json"), receipt(p, "applied", nil)); e != nil {
		t.Fatal(e)
	}
	p.Freeze = true
	p.IdleValidUntil = c.Now().Add(-time.Second)
	r, e := c.Run(context.Background(), p)
	if e != nil || r.Phase != "rollback" {
		t.Fatalf("%+v %v", r, e)
	}
	h, _ := hashFile(p.Binary)
	if h != p.PreviousSHA256 {
		t.Fatal("old image not restored")
	}
	again, e := c.Run(context.Background(), p)
	if e != nil || again.Phase != "rollback" {
		t.Fatal("terminal rollback was retried")
	}
}
func TestRunCancellationRollsBack(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	old := c.Command
	c.Command = func(ctx context.Context, n string, a ...string) ([]byte, error) {
		if n == "/bin/launchctl" && a[0] == "kickstart" {
			cancel()
		}
		return old(ctx, n, a...)
	}
	r, e := c.Run(ctx, p)
	if e != nil || r.Phase != "rollback" {
		t.Fatalf("%+v %v", r, e)
	}
	h, _ := hashFile(p.Binary)
	if h != p.PreviousSHA256 {
		t.Fatal("rollback lost original")
	}
}
func TestRunConfirmedRequiresLiveReady(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	old := c.Command
	c.Command = func(ctx context.Context, n string, a ...string) ([]byte, error) {
		if n == "/bin/launchctl" && a[0] == "kickstart" {
			r := Ready{ManagerID: p.ManagerID, RunID: p.RunID, PID: 777, Exe: p.Binary, Source: p.Source, Version: p.Version, NativeSHA256: p.BinarySHA256, ConfigSHA256: p.ConfigSHA256, Connected: true, StoresOpened: true, At: c.Now()}
			if e := writeJSON(filepath.Join(txDir(p), "ready.json"), r); e != nil {
				return nil, e
			}
		}
		return old(ctx, n, a...)
	}
	confirmDone := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			r, e := readReceipt(p)
			if e == nil && r.Ready != nil {
				confirmDone <- c.Acknowledge(context.Background(), p, "confirm")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		confirmDone <- errors.New("readiness not reached")
	}()
	r, e := c.Run(context.Background(), p)
	if ackErr := <-confirmDone; ackErr != nil {
		t.Fatal(ackErr)
	}
	if e != nil || r.Phase != "confirmed" {
		t.Fatalf("%+v %v", r, e)
	}
	if r.PID != 777 {
		t.Fatal("missing live PID")
	}
	r, e = c.Run(context.Background(), p)
	if e != nil || r.Phase != "confirmed" {
		t.Fatal("confirmed attempt was not idempotent")
	}
}
func TestConcurrentUpdateLock(t *testing.T) {
	c, p := fixture(t)
	unlock, e := acquire(filepath.Join(p.Dir, ".manager-update.lock"))
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if e = c.Start(context.Background(), p); e == nil {
		t.Fatal("concurrent start accepted")
	}
}
func TestUnknownPlanFieldsAndTrailingDataRejected(t *testing.T) {
	for _, body := range []string{`{"schema":1,"unknown":true}`, `{"schema":1} {"schema":1}`} {
		path := filepath.Join(t.TempDir(), "plan.json")
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := ReadPlan(path); e == nil {
			t.Fatal("invalid plan accepted")
		}
	}
}

func TestCrashAfterActiveBeforeAppliedRestoresWithoutWindow(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	if e := copyExclusive(p.Binary, filepath.Join(txDir(p), "previous")); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(activePath(p), p); e != nil {
		t.Fatal(e)
	}
	c.Now = func() time.Time { return p.BootstrapWindow.Add(time.Hour) }
	r, e := c.Run(context.Background(), p)
	if e != nil || r.Phase != "rollback" {
		t.Fatalf("%+v %v", r, e)
	}
	h, _ := hashFile(p.Binary)
	if h != p.PreviousSHA256 {
		t.Fatal("original image changed")
	}
}
func TestExpiredStartJobTerminatesWithErrorReceipt(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	c.Now = func() time.Time { return p.BootstrapWindow.Add(time.Hour) }
	r, e := c.Run(context.Background(), p)
	if e != nil || r.Phase != "checked" || r.Error == "" {
		t.Fatalf("%+v %v", r, e)
	}
	r, e = c.Run(context.Background(), p)
	if e != nil || r.Error == "" {
		t.Fatal("failed job retried")
	}
}
func TestRollbackWillNotOverwriteExternalImage(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	if e := copyExclusive(p.Binary, filepath.Join(txDir(p), "previous")); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(activePath(p), p); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(filepath.Join(txDir(p), "receipt.json"), receipt(p, "applied", nil)); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p.Binary, []byte("external version"), 0700); e != nil {
		t.Fatal(e)
	}
	_, e := c.Run(context.Background(), p)
	if e == nil || !strings.Contains(e.Error(), "unexpected external image") {
		t.Fatalf("%v", e)
	}
	b, _ := os.ReadFile(p.Binary)
	if string(b) != "external version" {
		t.Fatal("external image overwritten")
	}
}
func TestConfirmRejectedWithoutReady(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	if e := writeJSON(filepath.Join(txDir(p), "receipt.json"), receipt(p, "applied", nil)); e != nil {
		t.Fatal(e)
	}
	if e := c.Acknowledge(context.Background(), p, "confirm"); e == nil {
		t.Fatal("unready activation confirmed")
	}
}
func TestSymlinkTargetRejected(t *testing.T) {
	c, p := fixture(t)
	old := p.Binary + ".real"
	if e := os.Rename(p.Binary, old); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(old, p.Binary); e != nil {
		t.Fatal(e)
	}
	if e := c.Check(context.Background(), p); e == nil {
		t.Fatal("symlink executable accepted")
	}
}

func TestOwnedFileRejectsSharedHardlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personad")
	if e := os.WriteFile(path, []byte("example"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := ownedIndependentFile(path, os.Getuid()); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(path, path+".shared"); e != nil {
		t.Fatal(e)
	}
	if e := ownedIndependentFile(path, os.Getuid()); e == nil {
		t.Fatal("shared hardlink accepted")
	}
}
func TestRunReadonlyHealthCannotConfirmWrongPID(t *testing.T) {
	c, p := fixture(t)
	prepare(t, p)
	r := Ready{ManagerID: p.ManagerID, RunID: p.RunID, PID: 999, Exe: p.Binary, Source: p.Source, Version: p.Version, NativeSHA256: p.BinarySHA256, ConfigSHA256: p.ConfigSHA256, Connected: true, StoresOpened: true, At: c.Now()}
	if e := writeJSON(filepath.Join(txDir(p), "ready.json"), r); e != nil {
		t.Fatal(e)
	}
	if _, e := c.liveReady(context.Background(), p); e == nil {
		t.Fatal("marker with wrong live PID accepted")
	}
}
