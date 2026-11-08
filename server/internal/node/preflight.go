package node

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/osproc"
)

// Before SessionStart, Claude Code reads its settings, its skills, commands and agents (following the links among
// them) and the working directory. On a Mac, a read in a privacy-protected folder (TCC: Desktop, Documents,
// Downloads) by a program that is not allowed there is refused, or waits on a consent prompt nobody answers for a node
// started by launchd or ssh: claude stops at "Loading commands and agents", no hook fires, and on the phone the
// session never starts. The preflight (`node doctor`'s path-access check, #5944) reads the same paths the same way,
// in a child of its own (this program again, `node doctor --preflight-child`: own process group, cwd /) that is killed
// whole once a single read has taken preflightStall, and says which path is blocked and the two ways out. It never
// starts claude. The child is a Go program rather than a shell script running ls for each path: on a Mac whose
// security software inspects every exec, a hundred linked skills took 5 s that way.

const pathAccessCheck = "path-access"

// preflightStall: a read that has not returned after this long is blocked.
const preflightStall = 3 * time.Second

// preflightStartDefault is how long the child may take to say its first line (its exec, its runtime coming up):
// starting is not a read. One stall, so a whole run is still preflightMaxStalls stalls.
const preflightStartDefault = preflightStall

// preflightStart is preflightStartDefault but in tests, whose stalls are short (TestMain): under the whole server
// suite's load the test binary took longer than one of those to come up (#1110).
var preflightStart = preflightStartDefault

// preflightMaxStalls caps a whole run at this many stalls: the child's start (preflightStart) is the first, then the
// reads, ones that each return but slowly (a sleeping network disk) included; what was not read by then is counted,
// not blamed.
const preflightMaxStalls = 5

// preflightLinger is what stopping the child may add after that: SweepGroup's second and the two seconds runPreflight
// gives it to exit.
const preflightLinger = 3 * time.Second

// preflightAnswerWait is how long `node doctor` waits for the node's answer: longer than the node's longest run
// (its start, preflightMaxStalls-1 more stalls and preflightLinger), so a slow run is never taken for a node that
// does not answer.
const preflightAnswerWait = preflightStartDefault + (preflightMaxStalls-1)*preflightStall + preflightLinger + 5*time.Second

// preflightMaxDirs caps the working directories read: the most recently used.
const preflightMaxDirs = 20

// preflightItem is one path the preflight reads.
type preflightItem struct {
	kind   string // "dir": list it; "file": read a byte; "links": list it, then read through each symlink in it
	path   string
	role   string // what it is to claude, in the report
	global bool   // every claude start reads it (Claude Code's own directory), not only the starts in one directory
}

// preflightItems are the paths a claude start reads: Claude Code's directory (claudeDir), its settings and
// instructions, the skills, commands and agents there; then each working directory, the assistant's first, with its
// own .claude/skills, commands and agents. Paths that are not absolute are left out.
func preflightItems(claudeDir string, workDirs []string) []preflightItem {
	var items []preflightItem
	seen := map[string]bool{}
	add := func(kind, path, role string, global bool) {
		if path == "" || !filepath.IsAbs(path) || seen[kind+"\x00"+path] {
			return
		}
		seen[kind+"\x00"+path] = true
		items = append(items, preflightItem{kind: kind, path: filepath.Clean(path), role: role, global: global})
	}
	add("dir", claudeDir, "Claude Code 的目录", true)
	for _, f := range []string{"settings.json", "settings.local.json", "CLAUDE.md"} {
		add("file", filepath.Join(claudeDir, f), "Claude Code 的设置", true)
	}
	for _, d := range []string{"skills", "commands", "agents"} {
		add("links", filepath.Join(claudeDir, d), "Claude Code 的设置", true)
	}
	for _, w := range workDirs {
		add("dir", w, "工作目录", false)
		for _, d := range []string{"skills", "commands", "agents"} {
			add("links", filepath.Join(w, ".claude", d), "工作目录", false)
		}
	}
	return items
}

// preflightChildArg, as `node doctor`'s first argument, makes it the preflight's child (preflightChild).
const preflightChildArg = "--preflight-child"

// preflightHello is the child's first line: a program that is not one (an older binary at the same path) says nothing.
const preflightHello = "preflight 1"

// preflightChild reads each "<d|f|l>:<path>" of args and says on out what happened, a line each, tab-separated:
// "start <id>" before a read (so the read that hangs is the last one started), then "ok <id>", "none <id>" (not there)
// or "fail <id> <error>"; "link <id> <path> <target>" for a symlink found in an "l" directory, whose read follows as
// <id> (the directory's id, a dot and a number). Every line is written at once: the parent takes silence for a hang.
func preflightChild(args []string, out io.Writer) {
	fmt.Fprintln(out, preflightHello)
	for i, a := range args {
		kind, path, ok := strings.Cut(a, ":")
		if !ok {
			continue
		}
		id := strconv.Itoa(i + 1)
		if kind == "l" {
			childLinks(out, id, path)
		} else {
			childRead(out, id, path)
		}
	}
}

// childRead reads path the way claude does: a directory's first entry, a file's first byte. Whether it is there is
// not asked first: macOS refuses stat in a protected folder too.
func childRead(out io.Writer, id, path string) {
	fmt.Fprintf(out, "start\t%s\n", id)
	childSay(out, id, readOne(path))
}

func readOne(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		_, err = f.Readdirnames(1)
	} else {
		_, err = f.Read(make([]byte, 1))
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// childLinks lists dir and reads through each symlink in it.
func childLinks(out io.Writer, id, dir string) {
	fmt.Fprintf(out, "start\t%s\n", id)
	ents, err := os.ReadDir(dir)
	if childSay(out, id, err); err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		n++
		sub, link := id+"."+strconv.Itoa(n), filepath.Join(dir, e.Name())
		target, _ := os.Readlink(link)
		fmt.Fprintf(out, "link\t%s\t%s\t%s\n", sub, oneField(link), oneField(target))
		childRead(out, sub, link)
	}
}

func childSay(out io.Writer, id string, err error) {
	switch {
	case err == nil:
		fmt.Fprintf(out, "ok\t%s\n", id)
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		fmt.Fprintf(out, "none\t%s\n", id)
	default:
		fmt.Fprintf(out, "fail\t%s\t%s\n", id, oneField(err.Error()))
	}
}

// oneField keeps s on its line and in its field.
func oneField(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}

// preflightResult is what reading one path came to.
type preflightResult struct {
	item   int    // the item it was read for (for a link: the directory the link is in)
	path   string // the item's path, or the link's
	target string // a link's target as written
	link   bool
	state  string // ok | missing | denied | error | hung
	msg    string
}

// preflightOutcome is a whole run: the results in the order read, how many items were never reached (a read before
// them hung, or the run took too long), and an error when the child could not run or did not do it.
type preflightOutcome struct {
	results   []preflightResult
	unchecked int
	err       error
}

// runPreflight reads items in a child process (env.child) and kills its whole process group as soon as one read has
// taken env.stall (or the child has said nothing env.start, one stall if unset, after it was started, or the run has
// taken that start and preflightMaxStalls-1 more stalls, or ctx ends).
func runPreflight(ctx context.Context, items []preflightItem, env preflightEnv) preflightOutcome {
	if len(items) == 0 {
		return preflightOutcome{}
	}
	if len(env.child) == 0 {
		return preflightOutcome{err: errors.New("no program to read with")}
	}
	stall, start := env.stall, cmp.Or(env.start, env.stall)
	args := slices.Clone(env.child[1:])
	for _, it := range items {
		args = append(args, it.kind[:1]+":"+it.path)
	}
	cmd := exec.Command(env.child[0], args...)
	cmd.Dir = "/" // not inside a folder the reads are about
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "LC_ALL=C"}, env.childEnv...)
	cmd.SysProcAttr = osproc.GroupAttr()
	out := &progressBuffer{moved: make(chan struct{}, 1)}
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return preflightOutcome{err: err}
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait() // a read that fails is in the output; the exit status says nothing more
		close(done)
	}()
	idle := time.NewTimer(start) // until its first line: starting is not a read
	defer idle.Stop()
	all := time.NewTimer(start + (preflightMaxStalls-1)*stall)
	defer all.Stop()
	finished, hung := false, false
wait:
	for {
		select {
		case <-done:
			finished = true
			break wait
		case <-out.moved:
			idle.Reset(stall)
		case <-idle.C:
			hung = true
			break wait
		case <-all.C: // still going, only slowly: no single read is to blame
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	if finished {
		o := parsePreflight(out.String(), items, true, false)
		if !strings.HasPrefix(out.String(), preflightHello+"\n") {
			o.err = errors.New(filepath.Base(env.child[0]) + " 不会做这项检查（版本旧？）")
		}
		return o
	}
	// What was said before the kill: nothing written while it dies counts.
	said := out.String()
	pid := cmd.Process.Pid
	_ = osproc.StopGroup(pid, true)
	osproc.SweepGroup(pid, time.Second) // with the wait below: preflightLinger
	select {
	case <-done:
	case <-time.After(preflightLinger - time.Second): // a read stuck in the kernel can outlive SIGKILL for a while: not waited for
	}
	return parsePreflight(said, items, false, hung)
}

// progressBuffer keeps what the child writes and signals each write.
type progressBuffer struct {
	mu    sync.Mutex
	b     bytes.Buffer
	moved chan struct{}
}

func (p *progressBuffer) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.b.Write(b)
	p.mu.Unlock()
	select {
	case p.moved <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (p *progressBuffer) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b.String()
}

// parsePreflight turns the child's lines into results. When it was stopped for a read that hung, the read it last
// started is that one.
func parsePreflight(out string, items []preflightItem, finished, hung bool) preflightOutcome {
	var o preflightOutcome
	byID := map[string]int{} // id -> index in o.results
	itemOf := func(id string) (int, bool) {
		top, _, _ := strings.Cut(id, ".")
		i, err := strconv.Atoi(top)
		return i - 1, err == nil && i >= 1 && i <= len(items)
	}
	inflight, started := "", 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		id := f[1]
		item, ok := itemOf(id)
		if !ok {
			continue
		}
		switch f[0] {
		case "link":
			if len(f) >= 4 {
				byID[id] = len(o.results)
				o.results = append(o.results, preflightResult{item: item, path: f[2], target: f[3], link: true})
			}
		case "start":
			if _, ok := byID[id]; !ok {
				byID[id] = len(o.results)
				o.results = append(o.results, preflightResult{item: item, path: items[item].path})
			}
			if !strings.Contains(id, ".") {
				started = item + 1
			}
			inflight = id
		case "ok", "fail", "none":
			i, ok := byID[id]
			if !ok {
				continue
			}
			r := &o.results[i]
			r.state = "ok"
			if f[0] == "none" {
				r.state = "missing"
			}
			if f[0] == "fail" {
				r.msg = strings.TrimSpace(strings.Join(f[2:], " "))
				r.state = failState(r.msg)
			}
			if inflight == id {
				inflight = ""
			}
		}
	}
	if !finished {
		if i, ok := byID[inflight]; ok && inflight != "" && hung {
			o.results[i].state = "hung"
		}
		o.unchecked = len(items) - started
	}
	// Whatever was started but never said how it went (the child killed between lines) is left out.
	o.results = slices.DeleteFunc(o.results, func(r preflightResult) bool { return r.state == "" })
	return o
}

// failReason is the end of an error that names the path first ("open /a/b: operation not permitted"): the error.
func failReason(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return strings.TrimSpace(msg[i+2:])
	}
	return msg
}

// failState reads an error the child gave.
func failState(msg string) string {
	switch msg := strings.ToLower(msg); {
	case strings.Contains(msg, "no such file"), strings.Contains(msg, "not a directory"):
		return "missing"
	case strings.Contains(msg, "operation not permitted"), strings.Contains(msg, "permission denied"):
		return "denied"
	}
	return "error"
}

// tccRefused says whether msg is macOS privacy (EPERM) rather than file permissions (EACCES).
func tccRefused(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "operation not permitted")
}

// launchedVia says what started this process, for macOS privacy (TCC) wording: TCC holds a read to account against
// the "responsible" program, which is sshd-keygen-wrapper for anything started over ssh, the program itself for a
// LaunchAgent's (launchd sets XPC_SERVICE_NAME to the agent's label), and the terminal or app otherwise.
// "ssh", "launchd" or "".
func launchedVia(getenv func(string) string) string {
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" {
		return "ssh"
	}
	if x := getenv("XPC_SERVICE_NAME"); x != "" && x != "0" && !strings.HasPrefix(x, "application.") {
		return "launchd"
	}
	return ""
}

// fullDiskAccessFor is, in the user's words, the program to give Full Disk Access to.
func fullDiskAccessFor(via, exe string) string {
	switch via {
	case "launchd":
		if exe == "" {
			exe = "personad"
		}
		return exe + "（常驻 node，由 launchd 启动）"
	case "ssh":
		return "sshd-keygen-wrapper（node 经 ssh 启动）"
	}
	return "运行 node 的终端或 App"
}

// protectedFolderOf names the protected folder (Desktop, Documents, Downloads) path is in, "" when none.
func protectedFolderOf(path, home string) string {
	rel, err := filepath.Rel(home, path)
	if home == "" || err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	top, _, _ := strings.Cut(rel, string(filepath.Separator))
	return protectedDirs[top]
}

// preflightEnv is how the preflight runs and what its words depend on.
type preflightEnv struct {
	goos, home string
	via, exe   string // launchedVia, os.Executable
	// stall: a read taking this long is blocked; start: how long the child has to say its first line (its exec, its
	// runtime coming up).
	stall, start time.Duration
	// child is the program and first arguments that run preflightChild (the items follow); childEnv adds to its
	// environment.
	child, childEnv []string
}

// preflightChildCmd is how the preflight's child is started: this program (exe) in child mode. Tests start the test
// binary instead (TestMain in hostorca_test.go).
var preflightChildCmd = func(exe string) (args, env []string) {
	return []string{exe, "node", "doctor", preflightChildArg}, nil
}

func thisPreflightEnv(home string) preflightEnv {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	env := preflightEnv{goos: runtime.GOOS, home: home, via: launchedVia(os.Getenv), exe: exe, stall: preflightStall,
		start: preflightStart}
	env.child, env.childEnv = preflightChildCmd(exe)
	return env
}

// pathPreflight runs the preflight on items and reports it as the path-access check.
func pathPreflight(ctx context.Context, items []preflightItem, env preflightEnv) DoctorCheck {
	if env.goos == "windows" {
		return DoctorCheck{Name: pathAccessCheck, Status: DoctorOK, Detail: "Windows 上不检查", Items: []string{}}
	}
	return preflightCheck(runPreflight(ctx, items, env), items, env)
}

// preflightCheck says what a run found: each blocked path, and what to do about it.
func preflightCheck(o preflightOutcome, items []preflightItem, env preflightEnv) DoctorCheck {
	c := DoctorCheck{Name: pathAccessCheck, Items: []string{}}
	if o.err != nil {
		c.Status, c.Detail = DoctorWarn, "没法检查："+shorten(o.err.Error(), 80)
		return c
	}
	var problems []preflightResult
	read, dangling := 0, 0
	for _, r := range o.results {
		switch {
		case r.state == "ok":
			read++
		case r.state == "missing" && r.link:
			dangling++
			c.Items = append(c.Items, "链接指向的地方不存在："+r.path+" → "+r.target)
		case r.state == "missing": // a working directory since removed, a settings file the user never made
		default:
			problems = append(problems, r)
		}
	}
	if len(problems) == 0 {
		c.Status, c.Detail = DoctorOK, fmt.Sprintf("Claude 启动时读的 %d 个路径都能读", read)
		if dangling > 0 {
			c.Detail += fmt.Sprintf("；%d 个链接指向的地方不存在（Claude 会跳过）", dangling)
		}
		if o.unchecked > 0 {
			c.Status, c.Detail = DoctorWarn, fmt.Sprintf("读了 %d 个路径，都能读，但没读完", read)
			c.Items = append(c.Items, unchecked(o))
			c.Hint = "读得太慢：再检查一次；一直这样的话，看看工作目录或者链接是不是在网络盘上"
		}
		return c
	}
	c.Status = DoctorWarn
	tcc := -1      // the first problem macOS privacy is behind, an index in problems
	onVolume := "" // the first hung path on a mounted volume
	var problemItems []string
	for i, r := range problems {
		it := items[r.item]
		if it.global {
			c.Status = DoctorFail
		}
		shown := r.path
		if r.link {
			shown += " → " + r.target
		}
		shown += "（" + it.role + "）"
		mac := env.goos == "darwin"
		switch r.state {
		case "hung":
			problemItems = append(problemItems, fmt.Sprintf("卡住（%s 没有返回）：%s", env.stall, shown))
			// A hang in a protected folder is macOS holding the read; anywhere else it is as likely a mount that
			// stopped answering (a network disk, FUSE).
			if mac && tcc < 0 && protectedFolderOf(blockedPath(r), env.home) != "" {
				tcc = i
			}
			if mac && onVolume == "" && strings.HasPrefix(blockedPath(r), "/Volumes/") {
				onVolume = blockedPath(r)
			}
		case "denied":
			problemItems = append(problemItems, "被拒绝："+shown+"："+failReason(r.msg))
			if mac && tcc < 0 && tccRefused(r.msg) {
				tcc = i
			}
		default:
			problemItems = append(problemItems, "读不了："+shown+"："+shorten(failReason(r.msg), 80))
		}
	}
	c.Items = append(problemItems, c.Items...)
	if o.unchecked > 0 {
		c.Items = append(c.Items, unchecked(o))
	}
	first := problems[0]
	where := first.path
	if first.link {
		where = first.path + " → " + first.target
	}
	who := "在这里启动的 Claude"
	if items[first.item].global {
		who = "每个 Claude"
	}
	if first.state == "hung" {
		c.Detail = fmt.Sprintf("读 %s 超过 %s 没有返回：%s会停在 Loading commands and agents，等不到 SessionStart", where, env.stall, who)
	} else {
		c.Detail = fmt.Sprintf("读不了 %s（%s）：%s启动时会失败或卡住", where, shorten(failReason(first.msg), 60), who)
	}
	if len(problems) > 1 {
		c.Detail += fmt.Sprintf("；共 %d 处", len(problems))
	}
	if tcc < 0 {
		c.Hint = "检查这个路径的权限，或者它所在的挂载（网络盘、FUSE）是否还有响应；或者把目录挪到别处"
		if onVolume != "" { // macOS asks before a program reads a network or removable volume, too
			c.Hint += "。macOS 读外接盘、网络盘也要授权：挂载正常的话，在「系统设置 › 隐私与安全性 › 完全磁盘访问权限」里加上并打开 " +
				fullDiskAccessFor(env.via, env.exe)
		}
		return c
	}
	folder := "受保护的文件夹（桌面、文稿、下载）"
	if name := protectedFolderOf(blockedPath(problems[tcc]), env.home); name != "" {
		folder = "「" + name + "」文件夹"
	}
	it := "它"
	if tcc > 0 { // not the path the detail names
		it = blockedPath(problems[tcc])
	}
	c.Hint = "macOS 的隐私保护挡住了" + it + "。两条出路：在「系统设置 › 隐私与安全性 › 完全磁盘访问权限」里加上并打开 " +
		fullDiskAccessFor(env.via, env.exe) + "；或者把它挪出" + folder
	return c
}

// blockedPath is where r's read went: a link's target (made absolute), the item's path otherwise.
func blockedPath(r preflightResult) string {
	if !r.link {
		return r.path
	}
	if filepath.IsAbs(r.target) {
		return filepath.Clean(r.target)
	}
	return filepath.Join(filepath.Dir(r.path), r.target)
}

// unchecked says how many items were never read, and why.
func unchecked(o preflightOutcome) string {
	why := "前面的读卡住了"
	if !slices.ContainsFunc(o.results, func(r preflightResult) bool { return r.state == "hung" }) {
		why = "读得太久，停下了"
	}
	return fmt.Sprintf("还有 %d 个路径没检查（%s）", o.unchecked, why)
}

// workDirs are the working directories of this node's sessions, most recently used first, at most `most` of them: the
// assistant's first (what the node starts claude in most), then the ones sessions ran in.
func (n *Node) workDirs(most int) []string {
	used := n.dirsByUse()
	dirs := make([]string, 0, len(used))
	for p := range used {
		if !identity.IsAssistantDir(p) {
			dirs = append(dirs, p)
		}
	}
	slices.SortFunc(dirs, func(a, b string) int {
		if c := used[b].Compare(used[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	out := append([]string{identity.AssistantDir(n.home)}, dirs...)
	if len(out) > most {
		out = out[:most]
	}
	return out
}

// preflight is the path-access check run in this node's own process: macOS holds the reads to the program that runs
// the node, as it does the claude the node starts.
func (n *Node) preflight(ctx context.Context) DoctorCheck {
	return pathPreflight(ctx, preflightItems(claudeDirOf(n.home), n.workDirs(preflightMaxDirs)), thisPreflightEnv(n.home))
}

// preflightFromNode is `personad node doctor`'s path-access check: asked of the node listening on sock, which reads
// with its own permissions and knows its sessions' working directories. Without one (none running, one in standby,
// an older one) it runs here, on Claude Code's own directory only, and says so: on a Mac the result is then this
// program's (a terminal's, ssh's), which need not be the node's.
func preflightFromNode(sock, home, claudeDir string) func(context.Context) DoctorCheck {
	return func(ctx context.Context) DoctorCheck {
		c, why := askNodePreflight(ctx, sock, preflightAnswerWait)
		if why == "" {
			return c
		}
		c = pathPreflight(ctx, preflightItems(claudeDir, []string{identity.AssistantDir(home)}), thisPreflightEnv(home))
		c.Detail += "；" + why + "，只查了 Claude Code 的目录，没查各会话的工作目录"
		if runtime.GOOS == "darwin" {
			c.Detail += "；macOS 按运行 doctor 的程序的权限算，可能和 node 的不一样"
		}
		return c
	}
}

// askNodePreflight asks the node on sock for its path-access check, waiting at most wait. why says, when it got none,
// why not, in the report's words.
func askNodePreflight(ctx context.Context, sock string, wait time.Duration) (c DoctorCheck, why string) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	line, err := nodeclient.DialAt(ctx, sock, sockRequest{Kind: "preflight"})
	switch {
	case errors.Is(err, nodeclient.ErrNoNode): // nothing listens, or a resident node in standby (no hub connected)
		return c, "没有 node 回答这项检查（没在运行，或者 hub 没连上它）"
	case err != nil && (ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded)):
		return c, fmt.Sprintf("node %s 内没有回答这项检查", wait)
	case err != nil:
		return c, "node 没有回答这项检查（" + shorten(err.Error(), 60) + "）"
	}
	if json.Unmarshal(line, &c) != nil || c.Name != pathAccessCheck {
		return DoctorCheck{}, "node 的版本旧，不会这项检查" // {"error":"unknown kind"}
	}
	if c.Items == nil {
		c.Items = []string{}
	}
	return c, ""
}
