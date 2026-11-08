package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Only file names of the selected app-server processes are retained, never contents or their other descriptors.
func openCodexRollouts(processes []codexProcess) map[int][]string {
	out, _ := scanOpenRollouts(processes)
	return out
}

// scanOpenRollouts is openCodexRollouts, and whether every process could be read: a resume (codex_resume.go) must
// not take "could not tell" for "nothing holds it". Windows reads each process's handle table (scanOpenRolloutsOS).
func scanOpenRollouts(processes []codexProcess) (map[int][]string, bool) {
	if runtime.GOOS == "windows" {
		return scanOpenRolloutsOS(processes)
	}
	out := map[int][]string{}
	var pids []string
	for _, p := range processes {
		if p.pid > 0 {
			pids = append(pids, strconv.Itoa(p.pid))
		}
	}
	if len(pids) == 0 {
		return out, true
	}
	if runtime.GOOS == "linux" {
		ok := true
		for _, pid := range pids {
			entries, err := os.ReadDir("/proc/" + pid + "/fd")
			if err != nil && !os.IsNotExist(err) { // gone since the scan is fine; unreadable is not
				ok = false
			}
			for _, e := range entries {
				path, err := os.Readlink("/proc/" + pid + "/fd/" + e.Name())
				if err == nil && rolloutIDRe.MatchString(path) {
					id, _ := strconv.Atoi(pid)
					out[id] = append(out[id], path)
				}
			}
		}
		return out, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strings.Join(pids, ","), "-Fn").Output()
	if err != nil && len(b) == 0 {
		return out, false
	}
	pid := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(line[1:])
		}
		if pid > 0 && strings.HasPrefix(line, "n") {
			path := line[1:]
			if filepath.IsAbs(path) && rolloutIDRe.MatchString(path) {
				out[pid] = append(out[pid], path)
			}
		}
	}
	return out, true
}
