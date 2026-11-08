package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// PaneProcesses are the agent processes in front whose ORCA_PANE_KEY is the pane, claude first.
func (h *orcaHost) PaneProcesses(ctx context.Context, pane host.PaneRef) ([]int, error) {
	procs, err := h.agentProcs(orcaProcsMaxAge)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, p := range procs[pane.ID] {
		pids = append(pids, p.pid)
	}
	return pids, nil
}

// agentProcs is a scan of the agent processes no older than maxAge.
func (h *orcaHost) agentProcs(maxAge time.Duration) (map[string][]orcaProc, error) {
	h.mu.Lock()
	if h.procsAt.IsZero() || time.Since(h.procsAt) > maxAge {
		h.mu.Unlock()
		procs, err := h.scan()
		h.mu.Lock()
		h.procs, h.procsErr, h.procsAt = procs, err, time.Now()
	}
	defer h.mu.Unlock()
	return h.procs, h.procsErr
}

// orcaProc is an agent process in front in one of Orca's terminals.
type orcaProc struct {
	pid   int
	agent string // claude | codex
	cwd   string
	start time.Time // when it started (to the second; zero when unknown)
}

var (
	orcaPaneRe       = paneEnvRe(orcaPaneEnv)
	orcaLegacyPaneRe = paneEnvRe(orcaLegacyPaneEnv)
	// A process with a pane of a host nested in Orca's terminal (herdr, herdr-core) belongs to that host: it inherited
	// the Orca terminal's ORCA_PANE_KEY, but Orca's terminal shows the nested host, not it. Other nested ptys (tmux,
	// zellij, screen, script…) are told by their owner instead (ptyOwner), not by a variable: Orca's own Claude agent
	// teams set TMUX in a real pane.
	nestedPaneRes = []*regexp.Regexp{paneEnvRe(herdrPaneEnv), paneEnvRe(herdrCorePaneEnv)}
)

// orcaPaneOf is the Orca pane key in a process's environment listing ("" when none, or when the process runs in a
// host nested inside Orca's terminal): ORCA_PANE_KEY, else ORCA_AGENT_PANE. Nothing else is taken from it (it also
// holds Orca's hook token).
func orcaPaneOf(env string) string {
	for _, re := range nestedPaneRes {
		if re.MatchString(env) {
			return ""
		}
	}
	if m := orcaPaneRe.FindStringSubmatch(env); m != nil {
		return m[1]
	}
	if m := orcaLegacyPaneRe.FindStringSubmatch(env); m != nil {
		return m[1]
	}
	return ""
}

// agentOfCmd: the command line runs Claude Code or Codex.
func agentOfCmd(argv []string) string {
	if isClaudeCmd(argv) {
		return "claude"
	}
	if len(argv) == 0 {
		return ""
	}
	switch progName(argv[0]) {
	case "codex":
		return "codex"
	case "node", "bun":
		if len(argv) > 1 && (progName(argv[1]) == "codex" || strings.Contains(filepath.ToSlash(argv[1]), "/codex/")) {
			return "codex"
		}
	}
	return ""
}

// scanOrcaProcs lists this user's claude and codex processes in front in Orca's terminals, by pane key: not stopped,
// in their terminal's foreground process group (a suspended or background agent is not what the terminal shows), not
// inside a nested herdr / herdr-core. Claude first, lowest pid first within an agent (a wrapper before the program it
// starts). Linux reads /proc; macOS asks ps twice: the command lines of all, then the environments of the agents only;
// Windows reads the process table (scanOrcaProcsOS, which has its own idea of in front).
func scanOrcaProcs() (map[string][]orcaProc, error) {
	switch runtime.GOOS {
	case "linux":
		return scanOrcaProcsLinux()
	case "windows":
		return scanOrcaProcsOS()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-ww", "-o", "pid=,stat=,etime=,args=", "-U", strconv.Itoa(os.Getuid())).Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	cands := parseOrcaPSArgs(string(b), time.Now())
	if len(cands) == 0 {
		return map[string][]orcaProc{}, nil
	}
	// Every process (not only the user's: /usr/bin/login sits between Orca's terminal host and the shell), for the
	// owner of each candidate's pty (ptyOwner).
	t, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,tty=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps -A: %w", err)
	}
	tree := parsePSTree(string(t))
	owners := map[int]int{}
	pids := make([]string, 0, 2*len(cands))
	for pid := range cands {
		pids = append(pids, strconv.Itoa(pid))
		if q := ptyOwner(tree, pid); q > 0 {
			owners[pid] = q
			pids = append(pids, strconv.Itoa(q))
		}
	}
	slices.Sort(pids)
	pids = slices.Compact(pids)
	b, err = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "pid=,command=", "-p", strings.Join(pids, ",")).Output()
	if err != nil && len(b) == 0 { // ps exits 1 when one of the pids is gone; the others are still listed
		return nil, fmt.Errorf("ps -E: %w", err)
	}
	return parseOrcaPSEnv(string(b), cands, owners), nil
}

// psNode is a process in `ps -A -o pid=,ppid=,tty=`: its parent and its controlling terminal ("??" for none).
type psNode struct {
	ppid int
	tty  string
}

func parsePSTree(s string) map[int]psNode {
	out := map[int]psNode{}
	for line := range strings.SplitSeq(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			out[pid] = psNode{ppid: ppid, tty: f[2]}
		}
	}
	return out
}

// ptyOwner is the process that owns pid's pty: the first ancestor on another terminal (or none). For an agent in an
// Orca pane that is Orca's terminal host; for one inside tmux, zellij, screen, script or any other pty a program in
// the pane made, it is that program, which inherited the pane's ORCA_PANE_KEY (#5463). 0 when the chain is broken.
func ptyOwner(tree map[int]psNode, pid int) int {
	tty := tree[pid].tty
	if tty == "" || tty == "??" {
		return 0
	}
	for cur, i := pid, 0; i < 64; i++ {
		p := tree[cur].ppid
		node, ok := tree[p]
		if !ok || p == cur {
			return 0
		}
		if node.tty != tty {
			return p
		}
		cur = p
	}
	return 0
}

// parseOrcaPSArgs reads `ps -o pid=,stat=,etime=,args=`: the claude / codex processes in front (stat holds "+", the
// foreground process group, and does not start with T, stopped), with their start times.
func parseOrcaPSArgs(s string, now time.Time) map[int]orcaProc {
	out := map[int]orcaProc{}
	for line := range strings.SplitSeq(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		stat := f[1]
		if !strings.Contains(stat, "+") || strings.HasPrefix(stat, "T") {
			continue
		}
		agent := agentOfCmd(f[3:])
		if agent == "" {
			continue
		}
		p := orcaProc{pid: pid, agent: agent}
		if d, ok := parseEtime(f[2]); ok {
			p.start = now.Add(-d).Truncate(time.Second)
		}
		out[pid] = p
	}
	return out
}

// parseEtime reads ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second, true
}

// parseOrcaPSEnv reads `ps -E -o pid=,command=` of the candidates and their pty owners (the command line followed by
// the environment): each candidate's Orca pane. owners (candidate → ptyOwner; nil: not checked) drops a candidate
// whose pty is not the pane's own: its owner carries the same pane key (a nested pty), its owner's environment cannot
// be read (ps -E shows none for Apple's own binaries, /usr/bin/script and screen among them: unknown is not "no
// key"), or it has no known owner.
func parseOrcaPSEnv(s string, cands map[int]orcaProc, owners map[int]int) map[string][]orcaProc {
	panes, readable := map[int]string{}, map[int]bool{}
	for line := range strings.SplitSeq(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			panes[pid] = orcaPaneOf(line)
			readable[pid] = strings.Contains(line, " HOME=") // every login environment has it
		}
	}
	out := map[string][]orcaProc{}
	for pid, p := range cands {
		pane := panes[pid]
		if pane == "" {
			continue
		}
		if owners != nil {
			if q := owners[pid]; q == 0 || !readable[q] || panes[q] == pane {
				continue
			}
		}
		out[pane] = append(out[pane], p)
	}
	sortOrcaProcs(out)
	return out
}

// scanOrcaProcsLinux is scanOrcaProcs from /proc: a process is in front when its process group is its terminal's
// foreground group (stat's pgrp = tpgid).
func scanOrcaProcsLinux() (map[string][]orcaProc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := map[string][]orcaProc{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name()
		cmd, err := os.ReadFile(dir + "/cmdline")
		if err != nil {
			continue
		}
		agent := agentOfCmd(strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00"))
		if agent == "" {
			continue
		}
		stat, err := os.ReadFile(dir + "/stat")
		if err != nil || !procInFront(string(stat)) {
			continue
		}
		env, _ := os.ReadFile(dir + "/environ")
		if pane := orcaPaneOf(string(env)); pane != "" {
			// A nested pty (tmux, zellij, script…) a program in the pane made: its owner carries the pane key too.
			q := ptyOwnerLinux(pid)
			if q == 0 {
				continue
			}
			// Unreadable (a setuid / setgid owner such as screen is not dumpable) is not "no key": not taken.
			if qenv, err := os.ReadFile("/proc/" + strconv.Itoa(q) + "/environ"); err != nil || len(qenv) == 0 ||
				orcaPaneOf(string(qenv)) == pane {
				continue
			}
			p := orcaProc{pid: pid, agent: agent}
			p.cwd, _ = os.Readlink(dir + "/cwd")
			if st, err := os.Stat(dir); err == nil {
				p.start = st.ModTime()
			}
			out[pane] = append(out[pane], p)
		}
	}
	sortOrcaProcs(out)
	return out, nil
}

// statPPIDTTY reads /proc/<pid>/stat's ppid and tty_nr.
func statPPIDTTY(pid int) (ppid int, tty string, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, "", false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, "", false
	}
	f := strings.Fields(s[i+1:]) // state ppid pgrp session tty_nr …
	if len(f) < 5 {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(f[1])
	return ppid, f[4], err == nil
}

// ptyOwnerLinux is ptyOwner from /proc: the first ancestor of pid on another terminal, 0 when the chain is broken.
func ptyOwnerLinux(pid int) int {
	_, tty, ok := statPPIDTTY(pid)
	if !ok || tty == "0" {
		return 0
	}
	for cur, i := pid, 0; i < 64; i++ {
		p, _, ok := statPPIDTTY(cur)
		if !ok || p <= 0 || p == cur {
			return 0
		}
		_, t, ok := statPPIDTTY(p)
		if !ok {
			return 0
		}
		if t != tty {
			return p
		}
		cur = p
	}
	return 0
}

// procInFront reads /proc/<pid>/stat: not stopped, and its process group is the terminal's foreground group.
func procInFront(stat string) bool {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return false
	}
	f := strings.Fields(stat[i+1:]) // state ppid pgrp session tty_nr tpgid …
	if len(f) < 6 || f[0] == "T" || f[0] == "t" {
		return false
	}
	return f[2] == f[5]
}

func sortOrcaProcs(m map[string][]orcaProc) {
	for _, ps := range m {
		slices.SortFunc(ps, func(a, b orcaProc) int {
			if (a.agent == "claude") != (b.agent == "claude") {
				if a.agent == "claude" {
					return -1
				}
				return 1
			}
			return a.pid - b.pid
		})
	}
}
