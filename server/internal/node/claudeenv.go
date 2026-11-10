package node

import (
	"strings"
	"sync"
	"time"
)

// paneEnvOfPID reads the engine pane variables (enginePaneEnvs) from a process's environment: what tells a claude in
// a terminal of Persona's engines from one in a terminal of the user's own. Per platform (claudeenv_*.go); nil where
// it cannot be read, and then every cli claude counts as outside (a claude whose engine lists it is in seen already,
// so the cost is only a claude in a pane its engine has not reported). Cached per pid and start: a pid's environment
// does not change, and the list is rebuilt every second.
var paneEnvOfPID func(pid int) map[string]string = cachedPaneEnv

var paneEnvCache = struct {
	sync.Mutex
	m map[int]paneEnvEntry
}{m: map[int]paneEnvEntry{}}

type paneEnvEntry struct {
	start string // procStartMark when read ("" where the system does not tell)
	env   map[string]string
	at    time.Time
}

// paneEnvKeep: how long an entry is used without looking at the process again where no start time tells a reused pid
// apart (macOS, Windows): long enough for the list's every-second rebuild, short enough for a pid Windows reuses.
const paneEnvKeep = 30 * time.Second

func cachedPaneEnv(pid int) map[string]string {
	start := procStartMark(pid)
	paneEnvCache.Lock()
	e, ok := paneEnvCache.m[pid]
	paneEnvCache.Unlock()
	if ok && e.start == start && (start != "" || time.Since(e.at) < paneEnvKeep) {
		return e.env
	}
	env := readPaneEnv(pid)
	paneEnvCache.Lock()
	if len(paneEnvCache.m) > 512 {
		paneEnvCache.m = map[int]paneEnvEntry{}
	}
	paneEnvCache.m[pid] = paneEnvEntry{start: start, env: env, at: time.Now()}
	paneEnvCache.Unlock()
	return env
}

// pickedEnv takes the pane variables (paneEnvKeys: the engines', and tmux's) out of an environment listing (NAME=value,
// NUL- or space-separated: /proc/<pid>/environ, ps -E).
func pickedEnv(listing string) map[string]string {
	out := map[string]string{}
	for _, name := range paneEnvKeys {
		re, ok := paneEnvRes.Load(name)
		if !ok {
			re, _ = paneEnvRes.LoadOrStore(name, paneEnvRe(name))
		}
		if m := re.(interface{ FindStringSubmatch(string) []string }).FindStringSubmatch(listing); m != nil {
			out[name] = m[1]
		}
	}
	return out
}

// attachImages appends the saved pictures' paths to text in the tag the apps show as thumbnails.
func attachImages(text string, paths []string) string {
	if len(paths) == 0 {
		return text
	}
	return strings.TrimSpace(text) + "\n\n<attached_images>\n" + strings.Join(paths, "\n") + "\n</attached_images>"
}
