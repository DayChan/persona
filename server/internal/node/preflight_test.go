//go:build !windows

package node

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// preflightHome is a home with Claude Code's directory (settings, a skill linked from elsewhere, a link to nothing) and
// a working directory with its own linked command.
func preflightHome(t *testing.T) (home, claude, work string) {
	t.Helper()
	home = t.TempDir()
	claude = mkdir(t, filepath.Join(home, ".claude"))
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := mkdir(t, filepath.Join(home, "dotfiles", "skills", "review"))
	mkdir(t, filepath.Join(claude, "skills"))
	symlink(t, shared, filepath.Join(claude, "skills", "review"))
	symlink(t, filepath.Join(home, "gone"), filepath.Join(claude, "skills", "old"))
	work = mkdir(t, filepath.Join(home, "code", "app"))
	cmds := mkdir(t, filepath.Join(work, ".claude", "commands"))
	if err := os.WriteFile(filepath.Join(home, "dotfiles", "ship.md"), []byte("ship"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(home, "dotfiles", "ship.md"), filepath.Join(cmds, "ship.md"))
	return home, claude, work
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// testPreflightEnv reads with a short stall; the child (the test binary) gets the tests' long start (TestMain).
func testPreflightEnv(home string) preflightEnv {
	env := preflightEnv{goos: "linux", home: home, via: "", exe: "/x/personad", stall: 400 * time.Millisecond,
		start: preflightStart}
	env.child, env.childEnv = preflightChildCmd("")
	return env
}

func TestPreflightAllReadable(t *testing.T) {
	home, claude, work := preflightHome(t)
	items := preflightItems(claude, []string{work, filepath.Join(home, "removed-project")})
	c := pathPreflight(context.Background(), items, testPreflightEnv(home))
	if c.Status != DoctorOK || c.Name != pathAccessCheck {
		t.Fatalf("%+v", c)
	}
	// Read: the directory, settings.json, skills/ and the skill through its link, the working directory, its commands/
	// and the command through its link. A removed working directory and absent settings files are no news; a link to
	// nothing is said, and is not a problem.
	if !strings.Contains(c.Detail, "7 个路径都能读") || !strings.Contains(c.Detail, "1 个链接指向的地方不存在") {
		t.Errorf("detail %q", c.Detail)
	}
	if len(c.Items) != 1 || !strings.Contains(c.Items[0], filepath.Join(claude, "skills", "old")+" → "+filepath.Join(home, "gone")) {
		t.Errorf("items %q", c.Items)
	}
}

// A read that never returns (a FIFO with no writer stands in for a folder macOS holds on a consent prompt) is cut
// after the stall: the child's whole process group goes, the blocked path is named, the paths after it are counted.
func TestPreflightKillsAHungRead(t *testing.T) {
	home, claude, work := preflightHome(t)
	fifo := filepath.Join(claude, "settings.json")
	os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	items := preflightItems(claude, []string{work})
	env := testPreflightEnv(home)
	startedAt := filepath.Join(t.TempDir(), "started")
	env.childEnv = append(env.childEnv, "NODE_TEST_PREFLIGHT_STARTED="+startedAt)
	c := pathPreflight(context.Background(), items, env)
	// Timed from the child's start, not from its exec: how long that takes is the machine's load (#1110).
	if b, err := os.ReadFile(startedAt); err != nil {
		t.Errorf("the child did not start: %v", err)
	} else if started, err := time.Parse(time.RFC3339Nano, string(b)); err != nil {
		t.Errorf("started %q: %v", b, err)
	} else if took := time.Since(started); took > env.stall+3*time.Second {
		t.Errorf("took %s", took)
	}
	if c.Status != DoctorFail { // every claude reads its settings
		t.Errorf("status %s", c.Status)
	}
	if !strings.Contains(c.Detail, "读 "+fifo+" 超过 400ms 没有返回") || !strings.Contains(c.Detail, "每个 Claude") {
		t.Errorf("detail %q", c.Detail)
	}
	if !slices.ContainsFunc(c.Items, func(s string) bool { return strings.HasPrefix(s, "卡住") && strings.Contains(s, fifo) }) ||
		!slices.ContainsFunc(c.Items, func(s string) bool { return strings.HasPrefix(s, "还有") }) {
		t.Errorf("items %q", c.Items)
	}
	if c.Hint == "" || strings.Contains(c.Hint, "完全磁盘访问") { // not a Mac: no Full Disk Access to give
		t.Errorf("hint %q", c.Hint)
	}
	// Nobody is left reading the FIFO: opening it to write without a reader fails with ENXIO.
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if errors.Is(err, syscall.ENXIO) {
			break
		}
		if err == nil {
			f.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hung reader outlived the preflight: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Starting the child is not a read (#1110): under the whole server suite's load the test binary can take longer than
// one of the tests' short stalls to say its first line. The start has a budget of its own, and the reads' stalls and
// the run's cap count past it: a child slower to start than a whole run of stalls still reads every path.
func TestPreflightGivesTheChildTimeToStart(t *testing.T) {
	home, claude, work := preflightHome(t)
	env := testPreflightEnv(home)
	items := preflightItems(claude, []string{work})
	childEnv := env.childEnv
	env.childEnv = append(slices.Clone(childEnv), "NODE_TEST_PREFLIGHT_SLOW="+((preflightMaxStalls+1)*env.stall).String())
	c := pathPreflight(context.Background(), items, env)
	if c.Status != DoctorOK || !strings.Contains(c.Detail, "7 个路径都能读") {
		t.Errorf("%+v", c)
	}
	// A child that never says its first line is stopped rather than waited for (it would sleep a minute): nothing
	// read, nothing blamed.
	env.childEnv = append(slices.Clone(childEnv), "NODE_TEST_PREFLIGHT_SLOW=1m")
	env.start = 200 * time.Millisecond
	start := time.Now()
	c = pathPreflight(context.Background(), items, env)
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("waited %s for a child that did not start", took)
	}
	if c.Status != DoctorWarn || !strings.Contains(c.Detail, "读了 0 个路径") ||
		!slices.ContainsFunc(c.Items, func(s string) bool { return strings.Contains(s, "读得太久") }) {
		t.Errorf("%+v", c)
	}
}

// A link whose target hangs is named with its target, and only warns when it is a working directory's.
func TestPreflightHungLinkInAWorkingDirectory(t *testing.T) {
	home, claude, work := preflightHome(t)
	fifo := filepath.Join(home, "dotfiles", "slow.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, ".claude", "commands", "slow.md")
	symlink(t, fifo, link)
	c := pathPreflight(context.Background(), preflightItems(claude, []string{work}), testPreflightEnv(home))
	if c.Status != DoctorWarn {
		t.Errorf("status %s: %+v", c.Status, c)
	}
	if !strings.Contains(c.Detail, link+" → "+fifo) || !strings.Contains(c.Detail, "在这里启动的 Claude") {
		t.Errorf("detail %q", c.Detail)
	}
}

func TestPreflightDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	home, claude, work := preflightHome(t)
	locked := mkdir(t, filepath.Join(home, "locked"))
	symlink(t, locked, filepath.Join(claude, "skills", "locked"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	c := pathPreflight(context.Background(), preflightItems(claude, []string{work}), testPreflightEnv(home))
	if c.Status != DoctorFail {
		t.Errorf("status %s", c.Status)
	}
	if !slices.ContainsFunc(c.Items, func(s string) bool {
		return strings.HasPrefix(s, "被拒绝") && strings.Contains(s, locked) && strings.HasSuffix(s, "permission denied")
	}) {
		t.Errorf("items %q", c.Items)
	}
}

// The words for a Mac: which program gets Full Disk Access depends on what started the node, and which folder to
// move out of on where the blocked path is.
func TestPreflightCheckWordsOnAMac(t *testing.T) {
	home := "/Users/me"
	items := []preflightItem{
		{kind: "file", path: home + "/.claude/settings.json", role: "Claude Code 的设置", global: true},
		{kind: "links", path: home + "/.claude/skills", role: "Claude Code 的设置", global: true},
		{kind: "dir", path: home + "/Documents/app", role: "工作目录"},
	}
	link := preflightOutcome{results: []preflightResult{
		{item: 0, path: items[0].path, state: "ok"},
		{item: 1, path: items[1].path, state: "ok"},
		{item: 1, path: items[1].path + "/review", target: "../../Desktop/skills/review", link: true, state: "hung"},
	}, unchecked: 1}
	for via, who := range map[string]string{
		"launchd": "/Users/me/.persona/bin/personad（常驻 node，由 launchd 启动）",
		"ssh":     "sshd-keygen-wrapper",
		"":        "运行 node 的终端或 App",
	} {
		env := preflightEnv{goos: "darwin", home: home, via: via, exe: "/Users/me/.persona/bin/personad", stall: preflightStall}
		c := preflightCheck(link, items, env)
		if c.Status != DoctorFail || !strings.Contains(c.Hint, "完全磁盘访问权限") || !strings.Contains(c.Hint, who) || !strings.Contains(c.Hint, "「桌面」文件夹") {
			t.Errorf("%q: %+v", via, c)
		}
	}
	// A working directory macOS refuses: a warning, and the folder it is in.
	denied := preflightOutcome{results: []preflightResult{{item: 2, path: items[2].path, state: "denied",
		msg: "open /Users/me/Documents/app: operation not permitted"}}}
	c := preflightCheck(denied, items, preflightEnv{goos: "darwin", home: home, via: "launchd", exe: "/x/personad"})
	if c.Status != DoctorWarn || !strings.Contains(c.Hint, "「文稿」文件夹") || !strings.Contains(c.Detail, "（operation not permitted）") {
		t.Errorf("%+v", c)
	}
	// Plain Unix permissions are not macOS privacy: no Full Disk Access.
	denied.results[0].msg = "open /Users/me/Documents/app: permission denied"
	if c := preflightCheck(denied, items, preflightEnv{goos: "darwin", home: home}); strings.Contains(c.Hint, "完全磁盘访问") {
		t.Errorf("hint %q", c.Hint)
	}
	// The child did not run at all.
	if c := preflightCheck(preflightOutcome{err: errors.New("no /bin/sh")}, items, preflightEnv{}); c.Status != DoctorWarn {
		t.Errorf("%+v", c)
	}
}

// On a Mac a hang outside the protected folders is not blamed on macOS privacy: a network disk or FUSE mount that
// stopped answering hangs the same way, and moving out of Desktop or Documents would not help.
func TestPreflightHangOutsideProtectedFolders(t *testing.T) {
	home := "/Users/me"
	items := []preflightItem{
		{kind: "dir", path: "/Volumes/share/project", role: "工作目录"},
		{kind: "dir", path: home + "/code/app", role: "工作目录"},
		{kind: "dir", path: home + "/Documents/app", role: "工作目录"},
	}
	env := preflightEnv{goos: "darwin", home: home, via: "launchd", exe: "/x/personad", stall: preflightStall}
	hung := func(i int) preflightOutcome {
		return preflightOutcome{results: []preflightResult{{item: i, path: items[i].path, state: "hung"}}, unchecked: len(items) - 1 - i}
	}
	// A mounted volume: the mount first; macOS asks before reading one too, so Full Disk Access is named, the
	// protected folders are not.
	c := preflightCheck(hung(0), items, env)
	if !strings.Contains(c.Hint, "网络盘") || !strings.Contains(c.Hint, "完全磁盘访问权限") || !strings.Contains(c.Hint, "/x/personad") ||
		strings.Contains(c.Hint, "挪出") || strings.Contains(c.Hint, "隐私保护挡住") {
		t.Errorf("volume: %q", c.Hint)
	}
	// Anywhere else outside the protected folders: the mount or the permissions, no Full Disk Access.
	if c := preflightCheck(hung(1), items, env); !strings.Contains(c.Hint, "网络盘") || strings.Contains(c.Hint, "完全磁盘访问") {
		t.Errorf("home: %q", c.Hint)
	}
	// In Documents: macOS privacy, and the folder named.
	if c := preflightCheck(hung(2), items, env); !strings.Contains(c.Hint, "隐私保护挡住") || !strings.Contains(c.Hint, "「文稿」文件夹") {
		t.Errorf("documents: %q", c.Hint)
	}
	// The hint follows the problem macOS privacy is behind, even when another one comes first.
	both := preflightOutcome{results: []preflightResult{
		{item: 1, path: items[1].path, state: "hung"},
		{item: 2, path: items[2].path, state: "denied", msg: "open " + items[2].path + ": operation not permitted"},
	}}
	if c := preflightCheck(both, items, env); !strings.Contains(c.Hint, "挡住了"+items[2].path) || !strings.Contains(c.Hint, "「文稿」文件夹") ||
		!strings.Contains(c.Detail, home+"/code/app") {
		t.Errorf("both: %+v", c)
	}
}

func TestBlockedPath(t *testing.T) {
	for _, tc := range []struct {
		r    preflightResult
		want string
	}{
		{preflightResult{path: "/a/b"}, "/a/b"},
		{preflightResult{path: "/h/.claude/skills/x", target: "/h/Desktop/x/", link: true}, "/h/Desktop/x"},
		{preflightResult{path: "/h/.claude/skills/x", target: "../../Desktop/x", link: true}, "/h/Desktop/x"},
	} {
		if got := blockedPath(tc.r); got != tc.want {
			t.Errorf("%+v: %q", tc.r, got)
		}
	}
}

func TestParsePreflight(t *testing.T) {
	items := []preflightItem{{kind: "dir", path: "/a"}, {kind: "links", path: "/b"}, {kind: "dir", path: "/c"}, {kind: "dir", path: "/d"}}
	out := "preflight 1\nstart\t1\nnone\t1\nstart\t2\nok\t2\nlink\t2.1\t/b/x\t/elsewhere/x\nstart\t2.1\nok\t2.1\n" +
		"link\t2.2\t/b/y\t/slow/y\nstart\t2.2\n"
	o := parsePreflight(out, items, false, true)
	states := map[string]string{}
	for _, r := range o.results {
		states[r.path] = r.state
	}
	want := map[string]string{"/a": "missing", "/b": "ok", "/b/x": "ok", "/b/y": "hung"}
	if len(states) != len(want) || o.unchecked != 2 {
		t.Fatalf("%+v", o)
	}
	for p, s := range want {
		if states[p] != s {
			t.Errorf("%s: %s, want %s", p, states[p], s)
		}
	}
	// Finished: nothing hung, nothing unchecked; lines that do not parse are skipped.
	o = parsePreflight("start\t1\nok\t1\ngarbage\nstart\t9\nok\t9\n", items[:1], true, false)
	if len(o.results) != 1 || o.results[0].state != "ok" || o.unchecked != 0 {
		t.Errorf("%+v", o)
	}
}

func TestLaunchedVia(t *testing.T) {
	// The node's agent under today's label or the one a Mac set up before the rename keeps.
	for _, label := range []string{NodeLaunchdLabel, legacyNodeLaunchdLabel} {
		for want, env := range map[string]map[string]string{
			"ssh":     {"SSH_CONNECTION": "192.0.2.2 50000 192.0.2.1 22", "XPC_SERVICE_NAME": label},
			"launchd": {"XPC_SERVICE_NAME": label},
			"":        {"XPC_SERVICE_NAME": "application.com.apple.Terminal.123"},
		} {
			if got := launchedVia(func(k string) string { return env[k] }); got != want {
				t.Errorf("%v: %q, want %q", env, got, want)
			}
		}
	}
	if got := launchedVia(func(k string) string { return map[string]string{"XPC_SERVICE_NAME": "0"}[k] }); got != "" {
		t.Errorf("a terminal: %q", got)
	}
}

// `personad node doctor` asks the running node to read with its own permissions (the node knows its sessions'
// directories); with no node it reads here and says so.
func TestDoctorAsksTheNodeForThePreflight(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	// The node's child has the tests' long start (TestMain), so the wait outlasts that too.
	c, why := askNodePreflight(context.Background(), e.opts.NodeSock, preflightStart+preflightAnswerWait)
	if why != "" || c.Name != pathAccessCheck || c.Status != DoctorOK || c.Items == nil {
		t.Fatalf("%q %+v", why, c)
	}
	if strings.Contains(c.Detail, "没有回答") {
		t.Errorf("detail %q", c.Detail)
	}
	none := preflightFromNode(filepath.Join(e.n.home, "none.sock"), e.n.home, e.opts.ClaudeConfigDir)(context.Background())
	if none.Name != pathAccessCheck || !strings.Contains(none.Detail, "没有 node 回答这项检查（没在运行") {
		t.Errorf("%+v", none)
	}
}

// Why the node gave no answer is said as it was: an older node, one that answers nothing in time (not "not
// running"), one in standby.
func TestAskNodePreflightSaysWhyNot(t *testing.T) {
	serve := func(t *testing.T, answer func(net.Conn)) string {
		sock := filepath.Join(newDoctorEnv(t).n.home, "x.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go answer(c)
			}
		}()
		return sock
	}
	reply := func(line string) func(net.Conn) {
		return func(c net.Conn) {
			defer c.Close()
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			_, _ = c.Write([]byte(line + "\n"))
		}
	}
	for _, tc := range []struct {
		name   string
		answer func(net.Conn)
		want   string
	}{
		{"old", reply(`{"error":"unknown kind"}`), "版本旧"},
		{"standby", reply(`{"away":true}`), "hub 没连上它"},
		{"slow", func(c net.Conn) { time.Sleep(2 * time.Second); c.Close() }, "300ms 内没有回答"},
	} {
		_, why := askNodePreflight(context.Background(), serve(t, tc.answer), 300*time.Millisecond)
		if !strings.Contains(why, tc.want) {
			t.Errorf("%s: %q", tc.name, why)
		}
	}
}

// The command line waits for the node longer than the node's longest run, so a slow run is not taken for no node.
func TestPreflightAnswerWaitOutlastsTheRun(t *testing.T) {
	if run := preflightStartDefault + (preflightMaxStalls-1)*preflightStall + preflightLinger; preflightAnswerWait <= run {
		t.Errorf("waits %s for a run of up to %s", preflightAnswerWait, run)
	}
}

// The doctor puts the check after the others, and runs it once even with --fix (which checks again).
func TestDoctorRunsThePreflightOnce(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	os.Remove(filepath.Join(e.opts.Dir, "mcp.json"))
	runs := 0
	e.opts.Preflight = func(context.Context) DoctorCheck {
		runs++
		return DoctorCheck{Name: pathAccessCheck, Status: DoctorFail, Detail: "blocked"}
	}
	e.opts.Fix = true
	rep := RunDoctor(context.Background(), e.opts)
	if runs != 1 || rep.OK || rep.Checks[len(rep.Checks)-1].Name != pathAccessCheck || rep.Checks[len(rep.Checks)-1].Items == nil {
		t.Errorf("runs %d ok %v checks %+v", runs, rep.OK, rep.Checks)
	}
	if !slices.Equal(rep.Fixed, []string{"mcp.json"}) {
		t.Errorf("fixed %v", rep.Fixed)
	}
}

// The node's working directories: the assistant's first, then by last use, without reading the disk.
func TestNodeWorkDirs(t *testing.T) {
	e := newDoctorEnv(t)
	now := time.Now()
	e.n.metas = map[string]quickMeta{
		"/p/a/1.jsonl": {cwd: "/w/a", mtime: now.Add(-time.Hour)},
		"/p/a/2.jsonl": {cwd: "/w/a", mtime: now},
		"/p/b/1.jsonl": {cwd: "/w/b", mtime: now.Add(-time.Minute)},
	}
	got := e.n.workDirs(3)
	if len(got) != 3 || got[1] != "/w/a" || got[2] != "/w/b" || !strings.HasSuffix(got[0], "assistant") {
		t.Errorf("%q", got)
	}
	if got := e.n.workDirs(2); len(got) != 2 {
		t.Errorf("cap: %q", got)
	}
}

// The child mode, as `personad node doctor --preflight-child` runs it: the hello line, then a line per step.
func TestPreflightChildMode(t *testing.T) {
	home, claude, _ := preflightHome(t)
	var out strings.Builder
	failed, err := DoctorCommand(context.Background(), []string{preflightChildArg, "d:" + claude, "f:" + filepath.Join(claude, "nope.json"),
		"l:" + filepath.Join(claude, "skills")}, &out)
	if failed || err != nil {
		t.Fatal(failed, err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{preflightHello, "start\t1", "ok\t1", "start\t2", "none\t2", "start\t3", "ok\t3",
		"link\t3.1\t" + filepath.Join(claude, "skills", "old") + "\t" + filepath.Join(home, "gone"), "start\t3.1", "none\t3.1",
		"link\t3.2\t" + filepath.Join(claude, "skills", "review") + "\t" + filepath.Join(home, "dotfiles", "skills", "review"), "start\t3.2", "ok\t3.2"}
	if !slices.Equal(lines, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A program at the child's path that is not one (an older personad there) says nothing: that is not "all readable".
func TestPreflightChildThatSaysNothing(t *testing.T) {
	home, claude, _ := preflightHome(t)
	env := testPreflightEnv(home)
	env.child, env.childEnv = []string{"/bin/sh", "-c", "exit 2", "old-personad"}, nil
	c := pathPreflight(context.Background(), preflightItems(claude, nil), env)
	if c.Status != DoctorWarn || !strings.Contains(c.Detail, "没法检查") {
		t.Errorf("%+v", c)
	}
}

// Stopped because the whole run took too long (not one read): nothing is blamed, what was not read is counted.
func TestPreflightCutShortBlamesNoRead(t *testing.T) {
	items := []preflightItem{{kind: "dir", path: "/a"}, {kind: "dir", path: "/b"}, {kind: "dir", path: "/c"}}
	o := parsePreflight("preflight 1\nstart\t1\nok\t1\nstart\t2\n", items, false, false)
	if len(o.results) != 1 || o.unchecked != 1 {
		t.Fatalf("%+v", o)
	}
	c := preflightCheck(o, items, preflightEnv{goos: "darwin", stall: preflightStall})
	if c.Status != DoctorWarn || !slices.ContainsFunc(c.Items, func(s string) bool { return strings.Contains(s, "读得太久") }) {
		t.Errorf("%+v", c)
	}
}
