package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/update"
)

// joinHub answers POST /api/v1/bus/join like the hub: code "mi_good" once, for manager m-0a1b2c.
type joinHub struct{ trades atomic.Int32 }

func (j *joinHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != bus.JoinPath {
		http.NotFound(w, r)
		return
	}
	var req struct{ Code string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Code != "mi_good" || j.trades.Add(1) > 1 {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"bad_install_code"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"id": "m-0a1b2c", "name": "工作", "token": "jb_secret", "hub_id": "h1"})
}

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	t.Setenv("PERSONA_DATABASE_URL", "")
	t.Setenv("PERSONA_DATABASE_URL", "")
	return home
}

// The code is traded once for manager.json in the default directory; without Docker for its database and no
// --database-url nothing is traded (the code works once); a code the hub does not take says so.
func TestJoin(t *testing.T) {
	home := isolate(t)
	j := &joinHub{}
	srv := httptest.NewServer(j)
	defer srv.Close()
	ctx := context.Background()
	noDocker := func(context.Context) error { return ConfigError{errNoDocker} }
	docker := func(context.Context) error { return nil }
	var ce ConfigError
	if _, _, err := Join(ctx, JoinOptions{Hub: srv.URL, Code: "mi_good", CheckDocker: noDocker}); !errors.As(err, &ce) ||
		!strings.Contains(err.Error(), "--database-url 'postgres://USER:PASSWORD@HOST:5432/DBNAME'") || strings.Contains(err.Error(), "\n") ||
		j.trades.Load() != 0 {
		t.Fatalf("no database: %v, %d trades", err, j.trades.Load())
	}
	dir, c, err := Join(ctx, JoinOptions{Hub: srv.URL + "/", Code: " mi_good ", CheckDocker: docker})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".persona", "managers", "m-0a1b2c"); dir != want {
		t.Fatalf("dir %s, want %s", dir, want)
	}
	got, err := assistant.ReadManagerConfig(dir)
	if err != nil || *got != *c || got.Token != "jb_secret" || got.Hub != srv.URL || got.HubID != "h1" || got.DatabaseURL != "" {
		t.Fatalf("manager.json %+v %v", got, err)
	}
	if _, _, err := Join(ctx, JoinOptions{Hub: srv.URL, Code: "mi_good", CheckDocker: docker}); err == nil || !strings.Contains(err.Error(), "用过") {
		t.Fatalf("second trade: %v", err)
	}
	if _, _, err := Join(ctx, JoinOptions{Hub: "[fd00::20]:18660", Code: "mi_good", CheckDocker: docker}); err == nil {
		t.Fatal("hub without a scheme")
	}
	if _, _, err := Join(ctx, JoinOptions{Hub: srv.URL, Code: "mi_x", DatabaseURL: "mysql://x/y", CheckDocker: noDocker}); err == nil {
		t.Fatal("a database URL that is not postgres")
	}

	// Into a directory of its own with its own database; a directory that is another manager's is left alone.
	j.trades.Store(0)
	other := filepath.Join(t.TempDir(), "mgr")
	if err := assistant.WriteManagerConfig(other, assistant.ManagerConfig{ID: "m-ffffff", Hub: srv.URL, Token: "jb_other"}); err != nil {
		t.Fatal(err)
	}
	own := "postgres://me@[fd00::21]:5432/mine"
	dir2, _, err := Join(ctx, JoinOptions{Hub: srv.URL, Code: "mi_good", Dir: other, DatabaseURL: own, CheckDocker: noDocker})
	if err != nil {
		t.Fatal(err)
	}
	if dir2 == other {
		t.Fatal("overwrote another manager's directory")
	}
	if kept, _ := assistant.ReadManagerConfig(other); kept == nil || kept.ID != "m-ffffff" {
		t.Fatalf("other manager's config: %+v", kept)
	}
	if c2, _ := assistant.ReadManagerConfig(dir2); c2 == nil || c2.DatabaseURL != own {
		t.Fatalf("own database not kept: %+v", c2)
	}
}

// Without Docker (to make its own Postgres) and no --database-url, nothing is traded: join stops with a
// configuration error (exit 2), and run --join says so once and waits (a container is restarted on exit 2 too).
func TestNoDatabaseSpendsNoCode(t *testing.T) {
	isolate(t)
	t.Setenv("PATH", t.TempDir()) // no docker
	j := &joinHub{}
	srv := httptest.NewServer(j)
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "m")
	var ce ConfigError
	if err := Main(context.Background(), update.Build{}, []string{"join", srv.URL, "mi_good"}, io.Discard); !errors.As(err, &ce) ||
		!strings.Contains(err.Error(), "--database-url") {
		t.Errorf("join: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := Main(ctx, update.Build{}, []string{"run", "--dir", dir, "--join", srv.URL, "mi_good"}, &out); !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(out.String(), "--database-url") || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("run --join: %v %q", err, out.String())
	}
	if j.trades.Load() != 0 {
		t.Fatalf("%d codes traded", j.trades.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, assistant.ManagerFile)); !os.IsNotExist(err) {
		t.Fatal("manager.json written")
	}
}

// `manager join` prints where it is and what is next; run --join skips the join once manager.json is there.
func TestJoinCommandAndArgs(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(&joinHub{})
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "m")
	var out bytes.Buffer
	err := Main(context.Background(), update.Build{}, []string{"join", srv.URL, "mi_good", "--dir", dir,
		"--database-url", "postgres://me@[fd00::21]:5432/mine"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "m-0a1b2c") || !strings.Contains(out.String(), "manager install --dir") || strings.Contains(out.String(), "jb_secret") {
		t.Fatalf("output: %s", out.String())
	}
	for _, args := range [][]string{{"join", srv.URL}, {"run", "extra"}, {"run", "--join", srv.URL}, {"nope"}, nil} {
		if err := Main(context.Background(), update.Build{}, args, &out); err != errUsage {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// The service units run `manager run --dir` from the right binary, quoted; a configuration mistake (exit 2) is not
// restarted.
func TestServiceFiles(t *testing.T) {
	unit := serviceUnit("m-0a1b2c", "/opt/my bin/personad", "/home/u/.persona/managers/m-0a1b2c")
	for _, want := range []string{`ExecStart="/opt/my bin/personad" manager run --dir "/home/u/.persona/managers/m-0a1b2c"`,
		"Restart=always", "RestartPreventExitStatus=2", `Environment="PERSONA_ASSISTANT_DEPLOY=service"`, "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	plist := servicePlist("m-0a1b2c", "/opt/personad", "/Users/u/a&b", "/Users/u")
	for _, want := range []string{"<string>com.persona.manager.m-0a1b2c</string>", "<string>manager</string><string>run</string><string>--dir</string><string>/Users/u/a&amp;b</string>",
		"<key>KeepAlive</key><true/>", "/Users/u/a&amp;b/manager.log"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
}

// checkReplaced: the service file at path was replaced in one step (a new file renamed over it), not rewritten in
// place: old, opened on it before, still reads had; path is the new text, with want in it, mode 0644; no temporary
// file is left beside it.
func checkReplaced(t *testing.T, path string, old *os.File, had []byte, want string) {
	t.Helper()
	if got, err := io.ReadAll(old); err != nil || string(got) != string(had) {
		t.Fatalf("the file opened before reads %q (%v), want what it had: written in place, not replaced", got, err)
	}
	now, err := os.ReadFile(path)
	if err != nil || string(now) == string(had) || !strings.Contains(string(now), want) {
		t.Fatalf("%s now: %s %v, want %q in it", path, now, err, want)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("%s: %v %v, want mode 0644", path, st, err)
	}
	names, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.Contains(n.Name(), ".tmp-") {
			t.Fatalf("temporary file left: %s", n.Name())
		}
	}
}

// install writes the unit (or plist) in one step: installed again with another binary, the file is replaced, never
// cut short in place, so a crash mid-write cannot leave systemd or launchd a broken one. A former name that links to
// the file keeps linking to it (the file it names is replaced, not the link).
func TestInstallReplacesTheServiceFile(t *testing.T) {
	for _, c := range []struct {
		name, goos string
		linked     bool
	}{{"systemd", "linux", false}, {"launchd", "darwin", false}, {"systemd through the former name", "linux", true}} {
		t.Run(c.name, func(t *testing.T) {
			home := isolate(t)
			const id = "m-0a1b2c"
			dir := filepath.Join(home, ".persona", "managers", id)
			if err := assistant.WriteManagerConfig(dir, assistant.ManagerConfig{ID: id, Name: "work", Hub: "http://[fd00::20]:18660", Token: "t"}); err != nil {
				t.Fatal(err)
			}
			bin := "/opt/personad"
			e := &env{stdout: io.Discard, goos: c.goos, home: home, exe: func() (string, error) { return bin, nil },
				look:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
				getenv: func(string) string { return "" },
				run:    func(context.Context, string, ...string) (string, error) { return "active\n", nil }}
			(&fakeDocker{}).wire(e)
			path := func() string {
				if c.goos == "darwin" {
					return e.plistPath(id)
				}
				return e.unitPath(id)
			}
			if err := cmdInstall(context.Background(), e, nil); err != nil {
				t.Fatal(err)
			}
			file := path()
			legacy := filepath.Join(filepath.Dir(file), compat.LegacyManagerUnit(id))
			if c.linked {
				if err := os.Symlink(filepath.Base(file), legacy); err != nil {
					t.Fatal(err)
				}
				if path() != legacy {
					t.Fatalf("install writes %s, want the former name %s", path(), legacy)
				}
			}
			old, err := os.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			had, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}

			bin = "/opt/new/personad"
			if err := cmdInstall(context.Background(), e, nil); err != nil {
				t.Fatal(err)
			}
			checkReplaced(t, file, old, had, bin)
			if !c.linked {
				return
			}
			if st, err := os.Lstat(legacy); err != nil || st.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the former name is no longer a link: %v %v", st, err)
			}
			if err := e.checkCompatibleService(id); err != nil {
				t.Fatalf("the two names are two files now: %v", err)
			}
		})
	}
}

// install writes the user unit and enables and restarts it; uninstall takes it away and keeps the directory.
func TestInstallSystemd(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".persona", "managers", "m-0a1b2c")
	if err := assistant.WriteManagerConfig(dir, assistant.ManagerConfig{ID: "m-0a1b2c", Name: "工作", Hub: "http://[fd00::20]:18660", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	var out bytes.Buffer
	d := &fakeDocker{}
	e := &env{stdout: &out, goos: "linux", home: home, exe: func() (string, error) { return "/opt/personad", nil },
		look:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		getenv: func(string) string { return "" },
		run: func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return "active\n", nil
		}}
	d.wire(e)
	if err := cmdInstall(context.Background(), e, nil); err != nil { // the only manager: no --dir needed
		t.Fatal(err)
	}
	c, _ := assistant.ReadManagerConfig(dir)
	if c.DatabaseContainer != "persona-mgr-m-0a1b2c-pg" || !strings.HasPrefix(c.DatabaseURL, "postgres://persona:") ||
		!strings.Contains(c.DatabaseURL, "@127.0.0.1:25432/persona") || !d.called("docker run -d --name persona-mgr-m-0a1b2c-pg") ||
		!d.called("docker volume create persona-mgr-m-0a1b2c-data") || !strings.Contains(out.String(), "建好了它自己的 Postgres") {
		t.Fatalf("its database: %+v\n%s\n%s", c, strings.Join(d.calls, "\n"), out.String())
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "persona-manager-m-0a1b2c.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), `"/opt/personad" manager run --dir "`+dir+`"`) {
		t.Fatalf("unit %s %v", b, err)
	}
	if got := strings.Join(calls, "; "); !strings.Contains(got, "systemctl --user enable persona-manager-m-0a1b2c.service") ||
		!strings.Contains(got, "systemctl --user restart persona-manager-m-0a1b2c.service") {
		t.Fatalf("calls: %s", got)
	}
	out.Reset()
	if err := cmdStatus(context.Background(), e, nil); err != nil || !strings.Contains(out.String(), "persona-manager-m-0a1b2c.service active") ||
		!strings.Contains(out.String(), "persona-mgr-m-0a1b2c-pg") {
		t.Fatalf("status %v: %s", err, out.String())
	}
	out.Reset()
	if err := cmdUninstall(context.Background(), e, []string{"--dir", dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatal("unit still there")
	}
	if c, err := assistant.ReadManagerConfig(dir); err != nil || c.DatabaseContainer == "" {
		t.Fatalf("uninstall took the directory or the database: %+v %v", c, err)
	}
	if !d.called("docker stop persona-mgr-m-0a1b2c-pg") || d.called("docker volume rm") || !strings.Contains(out.String(), "数据卷 persona-mgr-m-0a1b2c-data 和里面的数据都留着") {
		t.Fatalf("uninstall keeps the volume: %s\n%s", strings.Join(d.calls, "\n"), out.String())
	}

	// Installed again: the same container and password, started again; nothing made.
	before, _ := assistant.ReadManagerConfig(dir)
	runs := d.count("docker run")
	if err := cmdInstall(context.Background(), e, []string{"--dir", dir}); err != nil {
		t.Fatal(err)
	}
	if after, _ := assistant.ReadManagerConfig(dir); after.DatabaseURL != before.DatabaseURL || d.count("docker run") != runs {
		t.Fatalf("installed again: %+v", after)
	}

	out.Reset()
	if err := cmdUninstall(context.Background(), e, []string{"--dir", dir, "--purge"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := assistant.ReadManagerConfig(dir); c.DatabaseURL != "" || c.DatabaseContainer != "" ||
		!d.called("docker rm -f persona-mgr-m-0a1b2c-pg") || !d.called("docker volume rm persona-mgr-m-0a1b2c-data") ||
		!strings.Contains(out.String(), "它的数据没了") {
		t.Fatalf("purge: %+v\n%s", c, out.String())
	}

	e.look = func(name string) (string, error) {
		if name == "systemctl" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + name, nil
	}
	if err := cmdInstall(context.Background(), e, []string{"--dir", dir}); err == nil || !strings.Contains(err.Error(), "manager run --dir") {
		t.Fatalf("without systemd: %v", err)
	}
}

// install on a Mac boots the agent out and bootstraps it again; a bootstrap refused right after the bootout
// (5: Input/output error) is tried again instead of leaving the manager unloaded.
func TestInstallLaunchdRetriesBootstrap(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".persona", "managers", "m-0a1b2c")
	if err := assistant.WriteManagerConfig(dir, assistant.ManagerConfig{ID: "m-0a1b2c", Name: "Work", Hub: "http://[fd00::20]:18660", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	defer func(w time.Duration) { bootstrapWait = w }(bootstrapWait)
	bootstrapWait = time.Millisecond
	var calls []string
	var out bytes.Buffer
	fails := 1
	e := &env{stdout: &out, goos: "darwin", uid: 501, home: home, exe: func() (string, error) { return "/opt/personad", nil },
		look:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		getenv: func(string) string { return "" },
		run: func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "launchctl" && args[0] == "bootstrap" && fails > 0 {
				fails--
				return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
			}
			return "", nil
		}}
	(&fakeDocker{}).wire(e)
	if err := cmdInstall(context.Background(), e, []string{"--dir", dir}); err != nil {
		t.Fatalf("%v\ncalls: %s", err, strings.Join(calls, "; "))
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.persona.manager.m-0a1b2c.plist")
	bootstraps := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "launchctl bootstrap ") {
			bootstraps++
			if c != "launchctl bootstrap gui/501 "+plist {
				t.Fatalf("bootstrap call %q", c)
			}
		}
	}
	if bootstraps != 2 || !fileExists(plist) || !strings.Contains(out.String(), "com.persona.manager.m-0a1b2c") {
		t.Fatalf("%d bootstraps, calls: %s\n%s", bootstraps, strings.Join(calls, "; "), out.String())
	}
}

// An http:// hub on the public internet is refused before the code is traded (the token would cross it in the
// clear) unless --allow-plaintext; a code the hub doesn't take is a configuration error (exit 2), so a container's
// restart doesn't loop on it.
func TestJoinPlaintextAndBadCode(t *testing.T) {
	isolate(t)
	j := &joinHub{}
	srv := httptest.NewServer(j)
	defer srv.Close()
	own := "postgres://persona:pw@[fd00::20]:5432/persona"
	public := func(context.Context, string) bool { return true }
	var ce ConfigError
	_, _, err := Join(context.Background(), JoinOptions{Hub: srv.URL, Code: "mi_good", DatabaseURL: own, PublicHost: public})
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "--allow-plaintext") || j.trades.Load() != 0 {
		t.Fatalf("public plaintext: %v (%d trades)", err, j.trades.Load())
	}
	if _, _, err := Join(context.Background(), JoinOptions{Hub: srv.URL, Code: "mi_good", DatabaseURL: own, PublicHost: public,
		AllowPlaintext: true}); err != nil {
		t.Fatalf("with --allow-plaintext: %v", err)
	}
	if _, _, err := Join(context.Background(), JoinOptions{Hub: srv.URL, Code: "mi_good", DatabaseURL: own}); !errors.As(err, &ce) {
		t.Fatalf("a used code: %v", err)
	}
}

func TestPublicHost(t *testing.T) {
	ctx := context.Background()
	// The private IPv4 ranges are built, not written: no private address in the repository's text.
	v4 := func(a, b, c, d byte) string { return netip.AddrFrom4([4]byte{a, b, c, d}).String() }
	for host, want := range map[string]bool{"203.0.113.9": false, "8.8.8.8": true, "2001:4860:4860::8888": true,
		"127.0.0.1": false, "::1": false, v4(192, 168, 1, 2): false, v4(10, 0, 0, 1): false, v4(100, 64, 1, 1): false,
		"fd00::20": false, "fe80::1": false, "localhost": false, "nas.local": false, "nas": false,
		// NAT64: as the IPv4 address in it.
		"64:ff9b::808:808": true, "64:ff9b::" + v4(10, 0, 0, 1): false, "64:ff9b::" + v4(192, 168, 1, 2): false,
		"64:ff9b:1::808:808": true, "64:ff9b:1::" + v4(100, 64, 1, 1): false, "64:ff9b::7f00:1": false,
		// The local-use /48 itself: the IPv4 address in bytes 6, 7, 9 and 10 (RFC 6052).
		"64:ff9b:1:808:8:800::": true, nat64At48(10, 0, 0, 1): false, nat64At48(192, 168, 1, 2): false,
		nat64At48(127, 0, 0, 1): false, nat64At48(8, 8, 8, 8): true,
		// A longer prefix inside it (here a /64, the IPv4 address in bytes 9 to 12): its length unknown, public.
		"64:ff9b:1:0:8:808:800:0": true, nat64At64(10, 0, 0, 1): true} {
		if got := publicHost(ctx, host); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}

// nat64At48 is IPv4 address a.b.c.d in NAT64 prefix 64:ff9b:1::/48 (RFC 6052, section 2.2).
func nat64At48(a, b, c, d byte) string {
	return netip.AddrFrom16([16]byte{0, 0x64, 0xff, 0x9b, 0, 1, a, b, 0, c, d}).String()
}

// nat64At64 is a.b.c.d in the NAT64 prefix 64:ff9b:1::/64.
func nat64At64(a, b, c, d byte) string {
	return netip.AddrFrom16([16]byte{0, 0x64, 0xff, 0x9b, 0, 1, 0, 0, 0, a, b, c, d}).String()
}

// Only the hub's own refusal of a plaintext join (403 tls_required) is a configuration error; any other 403 (a
// proxy's, say) is a plain one.
func TestJoinForbidden(t *testing.T) {
	isolate(t)
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	own := "postgres://persona:pw@[fd00::20]:5432/persona"
	var ce ConfigError
	body.Store(`{"error":"TLS is required for public requests","code":"tls_required"}`)
	_, _, err := Join(context.Background(), JoinOptions{Hub: srv.URL, Code: "mi_good", DatabaseURL: own})
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "https") {
		t.Fatalf("tls_required: %v", err)
	}
	for _, b := range []string{`{"error":"blocked by policy","code":"forbidden"}`, "<html>forbidden</html>"} {
		body.Store(b)
		_, _, err := Join(context.Background(), JoinOptions{Hub: srv.URL, Code: "mi_good", DatabaseURL: own})
		if err == nil || errors.As(err, &ce) || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "明文") {
			t.Fatalf("%s: %v", b, err)
		}
	}
}

// A code the hub refuses (used, expired) makes run --join wait instead of exiting (a container's restart would
// try it again and again): one line says what to do, and once a join by hand writes manager.json it goes on.
func TestRunJoinWaitsOnARefusedCode(t *testing.T) {
	isolate(t)
	t.Setenv("PATH", t.TempDir()) // no docker
	j := &joinHub{}
	srv := httptest.NewServer(j)
	defer srv.Close()
	old := revokedPoll
	revokedPoll = 10 * time.Millisecond
	t.Cleanup(func() { revokedPoll = old })
	dir := filepath.Join(t.TempDir(), "m")
	own := "postgres://u:p@[fd00::20]:5432/own?sslmode=disable"
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Main(context.Background(), update.Build{}, []string{"run", "--dir", dir, "--join", srv.URL, "mi_used", "--database-url", own}, out)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "manager join") {
		select {
		case err := <-done:
			t.Fatalf("run --join exited on a refused code: %v %q", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no line about the refused code: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "--dir "+dir) || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("the waiting line: %q", out.String())
	}
	// manager.json appears (a join by hand): it goes on to run the manager in it, which here stops at once on the
	// database this manager.json does not name (no Docker to make it).
	if err := assistant.WriteManagerConfig(dir, assistant.ManagerConfig{ID: "m-0a1b2c", Name: "工作", Hub: srv.URL, Token: "jb_secret"}); err != nil {
		t.Fatal(err)
	}
	var ce ConfigError
	select {
	case err := <-done:
		if !errors.As(err, &ce) {
			t.Fatalf("after manager.json: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting after manager.json was written")
	}
	if !strings.Contains(out.String(), "有了 manager.json") {
		t.Fatalf("output: %q", out.String())
	}
}

// Without --dir, a refused code is the configuration error (exit 2): there is no directory to wait on, and waiting
// for "the one manager of this computer" would run another manager's directory when there is exactly one.
func TestRunJoinWithoutDirDoesNotTakeAnotherManager(t *testing.T) {
	isolate(t)
	t.Setenv("PATH", t.TempDir()) // no docker
	j := &joinHub{}
	srv := httptest.NewServer(j)
	defer srv.Close()
	old := revokedPoll
	revokedPoll = 10 * time.Millisecond
	t.Cleanup(func() { revokedPoll = old })
	other := filepath.Join(managersRoot(), "m-0d0d0d")
	if err := assistant.WriteManagerConfig(other, assistant.ManagerConfig{ID: "m-0d0d0d", Name: "别的", Hub: srv.URL, Token: "jb_other"}); err != nil {
		t.Fatal(err)
	}
	own := "postgres://u:p@[fd00::20]:5432/own?sslmode=disable"
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Main(context.Background(), update.Build{}, []string{"run", "--join", srv.URL, "mi_used", "--database-url", own}, out)
	}()
	var ce ConfigError
	select {
	case err := <-done:
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), "安装码") {
			t.Fatalf("run --join without --dir: %v %q", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("still running: %q", out.String())
	}
	if strings.Contains(out.String(), other) || strings.Contains(out.String(), "manager.json") {
		t.Fatalf("took up the other manager: %q", out.String())
	}
}

// lockedBuffer is a bytes.Buffer safe to write from one goroutine and read from another.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// A manager whose token was refused waits for a new one in manager.json, and tries the same token again after
// revokedRetry with none.
func TestWaitNewToken(t *testing.T) {
	defer func(p time.Duration) { revokedPoll = p }(revokedPoll)
	revokedPoll = 10 * time.Millisecond
	dir := t.TempDir()
	c := assistant.ManagerConfig{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Token: "jb_old"}
	if err := assistant.WriteManagerConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	if renewed, err := waitNewToken(context.Background(), dir, &c, 50*time.Millisecond); renewed || err != nil {
		t.Fatalf("no new token: %v %v", renewed, err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		n := c
		n.Token = "jb_new"
		_ = assistant.WriteManagerConfig(dir, n)
	}()
	if renewed, err := waitNewToken(context.Background(), dir, &c, 5*time.Second); !renewed || err != nil {
		t.Fatalf("new token: %v %v", renewed, err)
	}
}

// After a refusal nothing of the manager starts again on the timer alone: every revokedRetry only the token is
// probed, and the manager comes back when the probe is no longer refused, or when manager.json gets a new token.
func TestAwaitRestartProbesTheTokenOnly(t *testing.T) {
	defer func(p, r time.Duration, f func(context.Context, *assistant.ManagerConfig) error) {
		revokedPoll, revokedRetry, probeToken = p, r, f
	}(revokedPoll, revokedRetry, probeToken)
	revokedPoll, revokedRetry = 5*time.Millisecond, 20*time.Millisecond
	dir := t.TempDir()
	c := assistant.ManagerConfig{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Token: "jb_old"}
	if err := assistant.WriteManagerConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	refused := atomic.Bool{}
	refused.Store(true)
	probeToken = func(_ context.Context, got *assistant.ManagerConfig) error {
		probes.Add(1)
		if got.Token != "jb_old" {
			t.Errorf("probed %q", got.Token)
		}
		if refused.Load() {
			return bus.ErrRevoked
		}
		return nil
	}
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- awaitRestart(context.Background(), dir, &c, &out) }()
	for deadline := time.Now().Add(10 * time.Second); probes.Load() < 3; {
		select {
		case err := <-done:
			t.Fatalf("came back while the token was still refused: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d probes in 10 s", probes.Load())
		}
	}
	refused.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not come back once the hub took the token again")
	}

	// A new token comes back at once, probing nothing.
	probeToken = func(context.Context, *assistant.ManagerConfig) error { t.Error("probed"); return bus.ErrRevoked }
	revokedRetry = time.Hour
	go func() {
		time.Sleep(20 * time.Millisecond)
		n := c
		n.Token = "jb_new"
		_ = assistant.WriteManagerConfig(dir, n)
	}()
	if err := awaitRestart(context.Background(), dir, &c, &out); err != nil {
		t.Fatal(err)
	}
}

// A probe that gets no verdict (the hub unreachable, a proxy's 401 or 503) is never taken for yes: the manager keeps
// waiting and tries again sooner; only a probe the hub took brings it back.
func TestAwaitRestartHubDown(t *testing.T) {
	defer func(p, r, a time.Duration, f func(context.Context, *assistant.ManagerConfig) error) {
		revokedPoll, revokedRetry, probeAgain, probeToken = p, r, a, f
	}(revokedPoll, revokedRetry, probeAgain, probeToken)
	revokedPoll, revokedRetry, probeAgain = 5*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	dir := t.TempDir()
	c := assistant.ManagerConfig{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Token: "jb_old"}
	if err := assistant.WriteManagerConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	var up atomic.Bool
	probeToken = func(context.Context, *assistant.ManagerConfig) error {
		// From the second probe on the waits must be probeAgain's: revokedRetry is made an hour.
		if probes.Add(1) == 1 {
			revokedRetry = time.Hour
		}
		if up.Load() {
			return nil
		}
		return errors.New("dial tcp: connection refused")
	}
	done := make(chan error, 1)
	go func() { done <- awaitRestart(context.Background(), dir, &c, io.Discard) }()
	for deadline := time.Now().Add(10 * time.Second); probes.Load() < 5; {
		select {
		case err := <-done:
			t.Fatalf("came back while the hub was down: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d probes in 10 s", probes.Load())
		}
	}
	up.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not come back once the hub took the token")
	}
}

// A database the user gave is used as it is: no docker, and uninstall leaves it alone.
func TestGivenDatabaseIsLeftAlone(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, "m")
	given := "postgres://me:pw@[fd00::21]:5432/mine"
	if err := assistant.WriteManagerConfig(dir, assistant.ManagerConfig{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Token: "t", DatabaseURL: given}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	d := &fakeDocker{}
	e := &env{stdout: &out, goos: "plan9", home: home, look: func(string) (string, error) { return "", os.ErrNotExist }}
	d.wire(e)
	c, _ := assistant.ReadManagerConfig(dir)
	if made, err := e.ownDatabase(context.Background(), dir, c); err != nil || made || len(d.calls) != 0 {
		t.Fatalf("given: %v %v %v", made, err, d.calls)
	}
	if err := cmdUninstall(context.Background(), e, []string{"--dir", dir, "--purge"}); err != nil || len(d.calls) != 0 ||
		!strings.Contains(out.String(), "--database-url 给的，没动它") {
		t.Fatalf("uninstall: %v %v %s", err, d.calls, out.String())
	}
	if c, _ := assistant.ReadManagerConfig(dir); c.DatabaseURL != given {
		t.Fatalf("its URL changed: %+v", c)
	}
}

// Without Docker, and none given: a configuration error (exit 2) saying what to do.
func TestOwnDatabaseNeedsDocker(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, "m")
	_ = assistant.WriteManagerConfig(dir, assistant.ManagerConfig{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Token: "t"})
	c, _ := assistant.ReadManagerConfig(dir)
	d := &fakeDocker{}
	e := &env{stdout: io.Discard, look: func(string) (string, error) { return "", os.ErrNotExist }}
	d.wire(e)
	var ce ConfigError
	if _, err := e.ownDatabase(context.Background(), dir, c); !errors.As(err, &ce) || !strings.Contains(err.Error(), "--database-url") {
		t.Fatalf("no docker: %v", err)
	}
	d.denied = true
	e.look = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	if _, err := e.ownDatabase(context.Background(), dir, c); !errors.As(err, &ce) || !strings.Contains(err.Error(), "usermod") {
		t.Fatalf("denied: %v", err)
	}
}

// fakeDocker answers docker like a machine with no containers yet: run makes one, rm removes it.
type fakeDocker struct {
	calls    []string
	envs     []string
	exists   bool
	denied   bool
	down     bool // docker info fails: not running
	noVolume bool // docker volume inspect fails
}

func (d *fakeDocker) wire(e *env) {
	e.docker = d.run
	e.freePort = func() (int, error) { return 25432, nil }
	e.ping = func(context.Context, string) error { return nil }
	e.dbWait = time.Second
}

func (d *fakeDocker) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	d.calls = append(d.calls, call)
	d.envs = append(d.envs, env...)
	switch {
	case strings.HasPrefix(call, "docker info") && d.down:
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
	case strings.HasPrefix(call, "docker volume inspect") && d.noVolume:
		return []byte("no such volume"), errors.New("exit status 1")
	case strings.HasPrefix(call, "docker inspect -f {{.State.Status}}") && !d.exists:
		return []byte("No such object"), errors.New("exit status 1")
	case strings.HasPrefix(call, "docker info") && d.denied:
		return []byte("permission denied while trying to connect to the Docker daemon socket"), errors.New("exit status 1")
	case strings.HasPrefix(call, "docker ps -a") && d.exists:
		return []byte(args[len(args)-3][len("name=^/"):len(args[len(args)-3])-1] + "\n"), nil
	case strings.HasPrefix(call, "docker run"):
		d.exists = true
	case strings.HasPrefix(call, "docker rm"):
		d.exists = false
	case strings.HasPrefix(call, "docker port"):
		return []byte("127.0.0.1:25432\n"), nil
	case strings.HasPrefix(call, "docker inspect -f {{.State.Status}}"):
		return []byte("running\n"), nil
	}
	return nil, nil
}

func (d *fakeDocker) called(prefix string) bool { return d.count(prefix) > 0 }

func (d *fakeDocker) count(prefix string) int {
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Joined again after a revoke, into its own directory: its database is there, so no Docker is needed and the code is
// traded; a container given its database through PERSONA_MANAGER_DATABASE_URL (`run --join`, never on a command line)
// keeps it; another database given into a directory whose container install made says the container is left unused.
func TestJoinAgainAndDatabaseFromTheEnvironment(t *testing.T) {
	home := isolate(t)
	t.Setenv("PATH", t.TempDir()) // no docker
	srv := httptest.NewServer(&joinHub{})
	defer srv.Close()
	dir := filepath.Join(home, ".persona", "managers", "m-0a1b2c")
	own := assistant.ManagerConfig{ID: "m-0a1b2c", Hub: srv.URL, Token: "jb_old",
		DatabaseURL: "postgres://persona:pw@127.0.0.1:25432/persona?sslmode=disable", DatabaseContainer: "persona-mgr-m-0a1b2c-pg"}
	if err := assistant.WriteManagerConfig(dir, own); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Main(context.Background(), update.Build{}, []string{"join", srv.URL, "mi_good", "--dir", dir}, &out); err != nil {
		t.Fatalf("join again without docker: %v", err)
	}
	if c, _ := assistant.ReadManagerConfig(dir); c.Token != "jb_secret" || c.DatabaseURL != own.DatabaseURL || c.DatabaseContainer != own.DatabaseContainer {
		t.Fatalf("joined again: %+v", c)
	}

	// The container's way: run --join with the URL in the environment (the hub is a fake; the run stops at the bus).
	srv2 := httptest.NewServer(&joinHub{})
	defer srv2.Close()
	dir2 := filepath.Join(t.TempDir(), "m")
	e := newEnv(io.Discard)
	e.getenv = func(k string) string {
		if k == databaseEnv {
			return "postgres://persona:secret@persona-mgr-m-0a1b2c-pg:5432/persona?sslmode=disable"
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cmdRun(ctx, e, update.Build{}, []string{"--dir", dir2, "--join", srv2.URL, "mi_good"})
	if c, err := assistant.ReadManagerConfig(dir2); err != nil || c.DatabaseURL != "postgres://persona:secret@persona-mgr-m-0a1b2c-pg:5432/persona?sslmode=disable" || c.DatabaseContainer != "" {
		t.Fatalf("from the environment: %+v %v", c, err)
	}

	// Another database given into the first directory: its container is not used any more, and it says so.
	srv3 := httptest.NewServer(&joinHub{})
	defer srv3.Close()
	out.Reset()
	if err := Main(context.Background(), update.Build{}, []string{"join", srv3.URL, "mi_good", "--dir", dir,
		"--database-url", "postgres://me@[fd00::21]:5432/mine"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "docker rm -f persona-mgr-m-0a1b2c-pg && docker volume rm persona-mgr-m-0a1b2c-data") {
		t.Fatalf("no word about the old container: %s", out.String())
	}
}

// When its container and volume are both gone, the run says the database it makes is a new, empty one; status tells
// docker not running apart from a container that is gone.
func TestLostDatabaseAndDockerDown(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, "m")
	c := assistant.ManagerConfig{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Token: "t",
		DatabaseURL: "postgres://persona:pw@127.0.0.1:25432/persona?sslmode=disable", DatabaseContainer: "persona-mgr-m-0a1b2c-pg"}
	_ = assistant.WriteManagerConfig(dir, c)
	var out bytes.Buffer
	d := &fakeDocker{}
	e := &env{stdout: &out, goos: "linux", home: home, look: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		getenv: func(string) string { return "" }}
	d.wire(e)
	d.noVolume = true
	if made, err := e.ownDatabase(context.Background(), dir, &c); err != nil || !made || !strings.Contains(out.String(), "建了一个新的空库") {
		t.Fatalf("lost: %v %v %s", made, err, out.String())
	}
	d.down = true
	if got := e.containerState(context.Background(), c.DatabaseContainer); got != "Docker 没在运行，看不了" {
		t.Fatalf("docker down: %q", got)
	}
	d.down, d.exists = false, false
	if got := e.containerState(context.Background(), c.DatabaseContainer); got != "容器不在了" {
		t.Fatalf("gone: %q", got)
	}
}

// --dir with a manager.json that is a link to nothing (a volume not mounted yet): refused before the code is traded,
// which a write after it would have spent for nothing.
func TestJoinRefusesADanglingManagerJSONFirst(t *testing.T) {
	isolate(t)
	j := &joinHub{}
	srv := httptest.NewServer(j)
	defer srv.Close()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "not-mounted", "manager.json"), filepath.Join(dir, "manager.json")); err != nil {
		t.Fatal(err)
	}
	var ce ConfigError
	_, _, err := Join(context.Background(), JoinOptions{Hub: srv.URL, Code: "mi_good", Dir: dir,
		CheckDocker: func(context.Context) error { return nil }})
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "link to nothing") || j.trades.Load() != 0 {
		t.Fatalf("%v, %d trades", err, j.trades.Load())
	}
}
