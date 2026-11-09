package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// fakeEnv is a Linux computer with a systemd user instance, unless the test changes it; nothing it does reaches the
// real one. calls records the commands run; onCommand answers them.
type fakeEnv struct {
	*env
	out       *bytes.Buffer
	calls     []string
	systemd   bool // /run/systemd/system
	linger    bool
	onCommand func(args string) (string, error)
	// trayCalls is what was done to the Windows tray; trayUp and trayAuto are whether it runs and starts at sign-in.
	trayCalls        []string
	trayUp, trayAuto bool
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	t.Setenv("PERSONA_HOME", "")
	home := t.TempDir()
	self := filepath.Join(t.TempDir(), "persona-link")
	if err := os.WriteFile(self, []byte("#!/bin/sh\n# this build\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeEnv{out: &bytes.Buffer{}, systemd: true}
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	f.env = &env{home: home, goos: "linux", user: "pi", uid: 1000, stdin: strings.NewReader(""), stdout: f.out, stderr: f.out,
		getenv:   func(string) string { return "" },
		exe:      func() (string, error) { return self, nil },
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		exists: func(p string) bool {
			switch p {
			case "/run/systemd/system":
				return f.systemd
			case "/var/lib/systemd/linger/pi":
				return f.linger
			}
			return fileExists(p)
		},
		now:   func() time.Time { return clock },
		sleep: func(d time.Duration) { clock = clock.Add(d) },
		wait:  5 * time.Second}
	f.command = func(name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		f.calls = append(f.calls, call)
		if f.onCommand != nil {
			return f.onCommand(call)
		}
		return "", nil
	}
	f.passthrough = func(name string, args ...string) error {
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		return nil
	}
	f.tray = trayControl{
		setAutostart: func(exe string, on bool) error {
			f.trayAuto = on
			if !on {
				f.trayCalls = append(f.trayCalls, "autostart off")
				return nil
			}
			f.trayCalls = append(f.trayCalls, "autostart "+exe)
			return nil
		},
		autostart:       func() bool { return f.trayAuto },
		registerLinks:   func(exe string) error { f.trayCalls = append(f.trayCalls, "links "+exe); return nil },
		unregisterLinks: func() { f.trayCalls = append(f.trayCalls, "unlinks") },
		removeData:      func() error { f.trayCalls = append(f.trayCalls, "data"); return nil },
		start:           func(exe string) error { f.trayCalls = append(f.trayCalls, "start "+exe); f.trayUp = true; return nil },
		quit: func(string, time.Duration) bool {
			f.trayCalls = append(f.trayCalls, "quit")
			f.trayUp = false
			return true
		},
		running: func(string) bool { return f.trayUp },
	}
	return f
}

// linuxPaths skips a test that looks for this computer's paths in a systemd unit or a shell line: on Windows they are
// C:\ paths, which the unit escapes and the shell quotes. The Windows install has its own tests (winservice_test.go).
func linuxPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("looks for Linux paths in a systemd unit or a shell line")
	}
}

func (f *fakeEnv) pairHere(t *testing.T) {
	t.Helper()
	err := link.Update(f.configPath(), func(c *link.Config) error {
		*c = link.Config{Hub: "0f1e2d3c-0000-4000-8000-000000000000", Machine: "pi", FP: "pin", Token: "jd_x",
			Endpoints: []link.Endpoint{{URL: "https://192.0.2.10:18662", Kind: "lan"}, {URL: "https://hub.example.com:18662", Kind: "name", Manual: true}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fakeEnv) saveStatus(t *testing.T, s link.Status, pid int) {
	t.Helper()
	if err := link.WriteStatus(f.configPath(), link.SavedStatus{Status: s, PID: pid, Version: "v1", Updated: f.now()}); err != nil {
		t.Fatal(err)
	}
}

func codeOf(err error) int {
	var c exitCode
	if errors.As(err, &c) {
		return int(c)
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestUnitFile(t *testing.T) {
	u := unitFile("/home/pi/.persona/bin/persona-link", "/home/pi/.persona/link.log")
	for _, want := range []string{
		`ExecStart="/home/pi/.persona/bin/persona-link" run --log "/home/pi/.persona/link.log"` + "\n",
		"Restart=always\n", "StandardOutput=null\n", "WantedBy=default.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "network-online") {
		t.Error("a user unit has no network-online.target to wait for")
	}
	// A home with odd characters still makes one word each, nothing systemd would expand.
	if got := systemdQuote(`/home/a b/100%/$x/"q"\`); got != `"/home/a b/100%%/$$x/\"q\"\\"` {
		t.Errorf("quoted %s", got)
	}
	if got := shellQuote("/home/a b/it's"); got != `'/home/a b/it'\''s'` {
		t.Errorf("shell quoted %s", got)
	}
	if got := shellQuote("/home/pi/.persona/bin/persona-link"); got != "/home/pi/.persona/bin/persona-link" {
		t.Errorf("shell quoted %s", got)
	}
}

func TestInstallWritesTheUnitStartsItAndWaitsForTheLink(t *testing.T) {
	linuxPaths(t)
	f := newFakeEnv(t)
	f.pairHere(t)
	// The service that was running before: install restarts it and does not take its last words for the new one's.
	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	f.saveStatus(t, link.Status{State: "connected", Node: "online"}, 41)
	f.onCommand = func(call string) (string, error) {
		if call == "systemctl --user restart persona-link.service" {
			release()
			f.saveStatus(t, link.Status{State: "stopped"}, 41)
			f.saveStatus(t, link.Status{State: "connected", Kind: "lan", Address: "https://192.0.2.10:18662", Node: "online",
				HubName: "家里", MeshHubs: 1}, 42)
		}
		return "", nil
	}
	if err := install(f.env, nil); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	unit, err := os.ReadFile(f.unitPath())
	if err != nil || !strings.Contains(string(unit), f.binPath()) {
		t.Fatalf("unit %s %v", unit, err)
	}
	if f.unitPath() != filepath.Join(f.home, ".config", "systemd", "user", "persona-link.service") {
		t.Fatal(f.unitPath())
	}
	if b, err := os.ReadFile(f.binPath()); err != nil || !strings.Contains(string(b), "this build") {
		t.Fatalf("binary not put in place: %v", err)
	}
	want := []string{"systemctl --user show-environment", "systemctl --user daemon-reload", "systemctl --user enable persona-link.service",
		"systemctl --user restart persona-link.service"}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q", f.calls)
	}
	out := f.out.String()
	for _, want := range []string{"装好了 " + f.binPath(), "装好了 systemd 用户服务", "sudo loginctl enable-linger pi",
		"连上了：「家里」，没和别的 hub 互联，内网 192.0.2.10:18662；本机 node 在线。"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Again from the installed binary: nothing to copy; with linger, no note about it.
	f.out.Reset()
	f.calls = nil
	f.linger = true
	f.exe = func() (string, error) { return f.binPath(), nil }
	if err := install(f.env, nil); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); strings.Contains(out, "装好了 "+f.binPath()) || strings.Contains(out, "linger") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestInstallSaysWhenTheLinkDoesNotComeUp(t *testing.T) {
	f := newFakeEnv(t)
	f.pairHere(t)
	f.onCommand = func(call string) (string, error) {
		if strings.HasSuffix(call, "restart persona-link.service") {
			f.saveStatus(t, link.Status{State: "waiting", Error: "内网 192.0.2.10:18662：超时；域名 hub.example.com:18662：拒绝连接"}, 50)
		}
		return "", nil
	}
	err := install(f.env, nil)
	if codeOf(err) != 1 {
		t.Fatalf("exit %v", err)
	}
	out := f.out.String()
	for _, want := range []string{"还没连上：未连接", "  · 内网 192.0.2.10:18662：超时", "  · 域名 hub.example.com:18662：拒绝连接", "服务会一直重试"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestInstallBeforePairing(t *testing.T) {
	f := newFakeEnv(t)
	if err := install(f.env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "还没配对：persona-link pair") {
		t.Fatalf("output:\n%s", f.out)
	}
	// --pair reads the link from stdin when none follows it; a bad one stops before anything is installed.
	f = newFakeEnv(t)
	f.stdin = strings.NewReader("https://example.com/not-a-pairing-link\n")
	err := install(f.env, []string{"--pair", "--machine", "pi"})
	if err == nil || !strings.Contains(err.Error(), "不是 personad hub pair 给的配对链接") || fileExists(f.unitPath()) || len(f.calls) != 0 {
		t.Fatalf("%v, calls %q", err, f.calls)
	}
}

func TestInstallWithoutSystemd(t *testing.T) {
	linuxPaths(t)
	f := newFakeEnv(t)
	f.systemd = false
	err := install(f.env, nil)
	if codeOf(err) != 1 {
		t.Fatalf("exit %v", err)
	}
	out := f.out.String()
	bin := f.binPath()
	for _, want := range []string{"系统不是由 systemd 启动的", "nohup " + bin + " run --log " + f.logPath() + " >/dev/null 2>&1 &",
		"@reboot " + bin + " run --log", "| crontab -", "群晖", "unless-stopped", "node --attach --supervised"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if fileExists(f.unitPath()) || !fileExists(bin) {
		t.Fatal("no unit without systemd; the binary is in place for the commands above")
	}
	// systemd there, but its user instance out of reach (a session from su / docker exec without linger).
	f = newFakeEnv(t)
	f.onCommand = func(call string) (string, error) {
		if strings.HasSuffix(call, "show-environment") {
			return "Failed to connect to bus: No medium found", errors.New("exit status 1")
		}
		return "", nil
	}
	if codeOf(install(f.env, nil)) != 1 || !strings.Contains(f.out.String(), "连不上你的 systemd 用户实例：Failed to connect to bus") ||
		!strings.Contains(f.out.String(), "用 ssh 直接登录这个用户再装") {
		t.Fatalf("output:\n%s", f.out)
	}
}

func TestInstallElsewhere(t *testing.T) {
	f := newFakeEnv(t)
	f.goos = "darwin"
	if err := install(f.env, nil); err == nil || !strings.Contains(err.Error(), "菜单栏 App") || !strings.Contains(err.Error(), "scripts/app/mac-link.sh install") {
		t.Fatalf("mac: %v", err)
	}
	f.goos = "freebsd" // Windows has its scheduled task (winservice_test.go)
	if err := install(f.env, nil); err == nil || !strings.Contains(err.Error(), "persona-link run") {
		t.Fatalf("freebsd: %v", err)
	}
	if fileExists(f.binPath()) || len(f.calls) != 0 {
		t.Fatal("did something")
	}
}

func TestStatus(t *testing.T) {
	f := newFakeEnv(t)
	f.onCommand = func(call string) (string, error) {
		switch {
		case strings.Contains(call, "is-active"):
			return "inactive", errors.New("exit status 3")
		case strings.Contains(call, "is-enabled"):
			return "disabled", errors.New("exit status 1")
		}
		return "", nil
	}
	// Not paired, nothing installed.
	if codeOf(status(f.env, nil)) != 1 || !strings.Contains(f.out.String(), "链路：还没配对") || !strings.Contains(f.out.String(), "后台服务：没装（persona-link install）") {
		t.Fatalf("output:\n%s", f.out)
	}

	// Paired, installed, running and connected.
	f.pairHere(t)
	_ = os.MkdirAll(filepath.Dir(f.unitPath()), 0o755)
	_ = os.WriteFile(f.unitPath(), []byte(unitFile(f.binPath(), f.logPath())), 0o644)
	f.onCommand = func(call string) (string, error) {
		switch {
		case strings.Contains(call, "is-active"):
			return "active", nil
		case strings.Contains(call, "is-enabled"):
			return "enabled", nil
		}
		return "", nil
	}
	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	since := f.now().Add(-(2*time.Hour + 3*time.Minute))
	f.saveStatus(t, link.Status{State: "connected", Machine: "pi", Kind: "lan", Address: "https://192.0.2.10:18662", Since: &since,
		Node: "online", HubName: "家里", MeshHubs: 3,
		Endpoints: []link.Endpoint{{URL: "https://192.0.2.10:18662", Kind: "lan"}}}, 4321)
	f.out.Reset()
	if err := status(f.env, nil); err != nil {
		t.Fatalf("connected: %v", err)
	}
	want := `模式：我去连 hub（主动）
链路：已连接 · 内网 192.0.2.10:18662 · 已连 2 小时 3 分钟
hub：「家里」，互联组共 3 台 hub
本机 node：在线
这台电脑：hub 上的「pi」
候选地址（按尝试顺序）：
  · 内网 https://192.0.2.10:18662（在用）
  · 域名 https://hub.example.com:18662（手动加的）
后台服务：systemd 用户服务 persona-link.service · active · enabled · linger 没开，退出登录就停：sudo loginctl enable-linger pi
进程：pid 4321 · 版本 v1
日志：` + f.logPath() + `（persona-link logs -f）
`
	if f.out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", f.out, want)
	}

	// --json: the same, for scripts.
	f.out.Reset()
	if err := status(f.env, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(f.out.Bytes(), &r); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	if !r.Running || !r.Paired || r.Machine != "pi" || r.HubName != "家里" || r.Link == nil || r.Link.State != "connected" || r.Link.MeshHubs != 3 ||
		r.Link.PID != 4321 || r.Service == nil || !r.Service.Installed || r.Service.Active != "active" || len(r.Endpoints) != 2 ||
		strings.Contains(f.out.String(), "jd_x") {
		t.Fatalf("report %s", f.out)
	}

	// Waiting: why each candidate failed, one per line.
	retry := f.now().Add(12 * time.Second)
	f.saveStatus(t, link.Status{State: "waiting", Retry: &retry, HubName: "家里", Error: "内网 192.0.2.10:18662：超时；域名 hub.example.com:18662：域名解析不了"}, 4321)
	f.out.Reset()
	if codeOf(status(f.env, nil)) != 1 {
		t.Fatal("waiting is not connected")
	}
	if out := strings.TrimPrefix(f.out.String(), "模式：我去连 hub（主动）\n"); !strings.HasPrefix(out, "链路：未连接 · 12 秒后重试\n  · 内网 192.0.2.10:18662：超时\n  · 域名 hub.example.com:18662：域名解析不了\nhub：「家里」\n") ||
		strings.Contains(out, "本机 node") || strings.Contains(out, "进程：pid") == false {
		t.Fatalf("output:\n%s", out)
	}

	// Stopped since: its last report, said to be the last.
	release()
	f.saveStatus(t, link.Status{State: "connected", Kind: "lan", Address: "https://192.0.2.10:18662", Node: "online", HubName: "家里"}, 4321)
	f.out.Reset()
	if codeOf(status(f.env, nil)) != 1 || !strings.Contains(f.out.String(), "链路：没在运行（0 秒前最后一次报告：已连接") || strings.Contains(f.out.String(), "进程：") {
		t.Fatalf("output:\n%s", f.out)
	}
}

func TestUninstall(t *testing.T) {
	f := newFakeEnv(t)
	f.pairHere(t)
	_ = os.MkdirAll(filepath.Dir(f.unitPath()), 0o755)
	_ = os.WriteFile(f.unitPath(), []byte("x"), 0o644)
	if _, _, err := f.installBinary(); err != nil {
		t.Fatal(err)
	}
	if err := uninstall(f.env, nil); err != nil {
		t.Fatal(err)
	}
	if fileExists(f.unitPath()) || !fileExists(f.configPath()) || !fileExists(f.binPath()) {
		t.Fatal("uninstall takes the service away, nothing else")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "systemctl --user disable --now persona-link.service") ||
		!strings.Contains(f.out.String(), "persona-link uninstall --purge") {
		t.Fatalf("calls %q\n%s", f.calls, f.out)
	}

	// --purge with another persona-link still running (the menu bar app's, say): refused, nothing deleted.
	release, _ := link.LockRun(f.configPath())
	if err := uninstall(f.env, []string{"--purge"}); err == nil || !fileExists(f.configPath()) {
		t.Fatalf("purged under a running persona-link: %v", err)
	}
	release()
	f.saveStatus(t, link.Status{State: "stopped"}, 1)
	_ = os.WriteFile(f.logPath(), []byte("log\n"), 0o600)
	if err := uninstall(f.env, []string{"--purge"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.configPath(), link.StatusPath(f.configPath()), f.logPath(), f.binPath()} {
		if fileExists(p) {
			t.Errorf("%s still there", p)
		}
	}
}

func TestPairReadsTheLinkFromStdin(t *testing.T) {
	f := newFakeEnv(t)
	if _, err := pair(f.env, "", ""); err == nil || !strings.Contains(err.Error(), "没有配对链接") {
		t.Fatalf("empty stdin: %v", err)
	}
	// Piped: the whole input. On a terminal: one line, without waiting for the end of input.
	f.stdin = strings.NewReader("  persona://elsewhere?token=x  \n")
	if _, err := pair(f.env, "-", ""); err == nil || !strings.Contains(err.Error(), "不是 personad hub pair 给的配对链接") {
		t.Fatalf("piped: %v", err)
	}
	f.terminal = true
	f.stdin = strings.NewReader("persona://link?url=http%3A%2F%2F192.0.2.10%3A18660&token=jp_x\nmore that never comes")
	if _, err := pair(f.env, "", ""); err == nil || !strings.Contains(err.Error(), "没开 TLS") {
		t.Fatalf("terminal: %v", err)
	}
	if !strings.Contains(f.out.String(), "粘贴") {
		t.Fatalf("no prompt: %s", f.out)
	}
}

func TestKeepLinkSavesItsStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json") // never paired: it says so and waits
	logPath := filepath.Join(t.TempDir(), "link.log")
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- keepLink(ctx, path, logPath, &out) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := link.ReadStatus(path); err == nil && st.State == "unpaired" && st.PID == os.Getpid() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no status saved")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !link.Running(path) {
		t.Fatal("not running by its lock")
	}
	if err := keepLink(context.Background(), path, "", &bytes.Buffer{}); !errors.Is(err, link.ErrRunning) {
		t.Fatalf("a second one: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st, err := link.ReadStatus(path); err != nil || st.State != "stopped" || link.Running(path) {
		t.Fatalf("after: %+v %v", st, err)
	}
	if !strings.Contains(out.String(), `"state":"unpaired"`) {
		t.Fatalf("stdout %q", out.String())
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "started") || !strings.Contains(string(b), "stopped") {
		t.Fatalf("log %q", b)
	}
}

func TestRotatingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.log")
	r, err := openRotating(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes
	for range 5 {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	// Writes 3 and 5 would have gone past 100 bytes: each moved the 2 lines before it aside.
	if len(old) != 80 || len(cur) != 40 {
		t.Fatalf("current %d bytes, old %d bytes", len(cur), len(old))
	}
	// Reopened past its size: moved aside at the next write.
	r, _ = openRotating(path, 30)
	_, _ = r.Write([]byte(line))
	r.Close()
	if cur, _ := os.ReadFile(path); len(cur) != 40 {
		t.Fatalf("current %d bytes", len(cur))
	}
}

func TestLastLines(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc\n"}, {"a\nb\nc", 2, "b\nc"}, {"a\nb\nc\n", 5, "a\nb\nc\n"}, {"a\nb\nc\n", 0, ""}, {"", 3, ""}, {"one\n", 1, "one\n"},
	} {
		if got := string(lastLines([]byte(tc.in), tc.n)); got != tc.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestTailFollowsAcrossARotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.log")
	_ = os.WriteFile(path, []byte("1\n2\n3\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	var mu syncBuffer
	done := make(chan error, 1)
	go func() { done <- tailFile(ctx, &mu, path, 2, true, 10*time.Millisecond) }()
	waitText(t, &mu, "2\n3\n")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("4\n")
	f.Close()
	waitText(t, &mu, "2\n3\n4\n")
	_ = os.Rename(path, path+".1")
	_ = os.WriteFile(path, []byte("5\n"), 0o600)
	waitText(t, &mu, "2\n3\n4\n5\n")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Lines written to the old file just before it is moved aside, after the last look, still come out, before the
// new file's.
func TestTailReadsTheOldFileToItsEndOnRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.log")
	_ = os.WriteFile(path, []byte("1\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- tailFile(ctx, &out, path, 5, true, 200*time.Millisecond) }()
	waitText(t, &out, "1\n")
	r, err := openRotating(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Write([]byte("2\n")) // to the old file
	_, _ = r.Write([]byte("3\n")) // moves it aside first
	r.Close()
	waitText(t, &out, "1\n2\n3\n")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLogsWithoutALogFile(t *testing.T) {
	f := newFakeEnv(t)
	if err := logs(f.env, nil); err != nil || !strings.Contains(f.out.String(), "还没有日志") {
		t.Fatalf("%v %s", err, f.out)
	}
	// The service installed but no log file (yet): the journal.
	_ = os.MkdirAll(filepath.Dir(f.unitPath()), 0o755)
	_ = os.WriteFile(f.unitPath(), []byte("x"), 0o644)
	if err := logs(f.env, []string{"-n", "20", "-f"}); err != nil || f.calls[len(f.calls)-1] != "journalctl --user -u persona-link.service -n 20 --no-pager -f" {
		t.Fatalf("%v %q", err, f.calls)
	}
	if err := logs(f.env, []string{"-n", "x"}); err == nil {
		t.Fatal("-n x")
	}
}

// syncBuffer is a bytes.Buffer for one writer and one reader at a time.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
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

func waitText(t *testing.T, b *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.String() != want {
		if time.Now().After(deadline) {
			t.Fatalf("got %q, want %q", b.String(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHubText(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mesh      int
		id        string
		connected bool
		want      string
	}{
		{"家里", 3, "h1", true, "「家里」，互联组共 3 台 hub"},
		{"家里", 1, "h1", true, "「家里」，没和别的 hub 互联"},
		{"家里", 0, "h1", false, "「家里」"},
		{"", 0, "h1", true, "h1（旧版 hub 不报名字）"},
		{"", 0, "h1", false, "h1"}, // not connected yet: nothing known about its version
	} {
		if got := hubText(tc.name, tc.mesh, tc.id, tc.connected); got != tc.want {
			t.Errorf("hubText(%q, %d, %q, %v) = %q, want %q", tc.name, tc.mesh, tc.id, tc.connected, got, tc.want)
		}
	}
}

func TestStatusWithoutTheUserInstance(t *testing.T) {
	r := report{Paired: true, Machine: "pi", HubName: "家里", Log: "/x/link.log",
		Service: &serviceState{Unit: unitName, Installed: true, Missing: noUserInstance + "：Failed to connect to bus: No medium found"}}
	out := r.text(time.Now(), "pi")
	want := "后台服务：装了 persona-link.service，但连不上你的 systemd 用户实例（Failed to connect to bus: No medium found）；" +
		"没开 linger 时没人登录它就不在：sudo loginctl enable-linger pi\n"
	if !strings.Contains(out, want) || !strings.HasPrefix(out, "模式：我去连 hub（主动）\n链路：没在运行\n") {
		t.Fatalf("output:\n%s", out)
	}
}

// A hub behind an HTTPS reverse proxy (no pin): status says how it is checked and shows the proxy's path.
func TestStatusBehindAReverseProxy(t *testing.T) {
	f := newFakeEnv(t)
	err := link.Update(f.configPath(), func(c *link.Config) error {
		*c = link.Config{Hub: "0f1e2d3c-0000-4000-8000-000000000000", Machine: "pi", Trust: link.TrustSystem, Token: "jd_x",
			Endpoints: []link.Endpoint{{URL: "https://hub.example.com:443/prefix", Kind: "name"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	since := f.now().Add(-5 * time.Minute)
	f.saveStatus(t, link.Status{State: "connected", Machine: "pi", Kind: "name", Address: "https://hub.example.com:443/prefix", Since: &since,
		Node: "online", Trust: link.TrustSystem}, 4321)
	_ = status(f.env, nil)
	for _, want := range []string{"链路：已连接 · 域名 hub.example.com:443/prefix · 已连 5 分钟\n",
		"校验：经反向代理 · 系统证书（按系统信任的 CA 和域名，没有钉住 hub 的公钥）\n", "  · 域名 https://hub.example.com:443/prefix（在用）\n"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("no %q in:\n%s", want, f.out)
		}
	}
	f.out.Reset()
	_ = status(f.env, []string{"--json"})
	var r report
	if err := json.Unmarshal(f.out.Bytes(), &r); err != nil || r.Trust != link.TrustSystem || !r.Paired {
		t.Fatalf("%v %s", err, f.out)
	}
}
