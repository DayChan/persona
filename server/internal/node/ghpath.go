package node

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Finding the GitHub CLI. Another program can be named gh too: some Linux hosts have an unrelated gh in
// /usr/local/bin, which New puts ahead of most of PATH. And a service's PATH usually misses Homebrew on Linux. So
// every gh on PATH, then in Homebrew's Linux prefixes, is asked for its version, and the first that answers as the
// GitHub CLI is the one run.

// ghLookPath is the GitHub CLI's path; tests point it at a script.
var ghLookPath = lookGitHubCLI

// ghFallbackDirs are where gh is often installed but PATH may not reach; tests point it nowhere.
var ghFallbackDirs = func() []string {
	dirs := []string{"/home/linuxbrew/.linuxbrew/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".linuxbrew", "bin"))
	}
	return dirs
}

// errNotGitHubCLI: there is a gh, but no gh found is the GitHub CLI.
var errNotGitHubCLI = errors.New("the gh on this machine is not the GitHub CLI")

// ghVersionTimeout bounds one candidate's `gh --version`.
const ghVersionTimeout = 5 * time.Second

// ghAnswers remembers each candidate's answer while its file is unchanged, so a lookup after the first is a few stats.
var ghAnswers = struct {
	sync.Mutex
	m map[string]ghAnswer
}{m: map[string]ghAnswer{}}

type ghAnswer struct {
	mod  time.Time
	size int64
	cli  bool
}

// lookGitHubCLI is the first gh on PATH, then in ghFallbackDirs, that is the GitHub CLI. Each directory is checked
// with exec.LookPath, so the executable bit (Unix) and PATHEXT (Windows: gh.exe) count as they do for a plain lookup;
// relative PATH entries are skipped, as exec.LookPath refuses them. exec.ErrNotFound when there is no gh at all;
// errNotGitHubCLI when every gh found is another program.
func lookGitHubCLI() (string, error) {
	dirs := append(filepath.SplitList(os.Getenv("PATH")), ghFallbackDirs()...)
	tried := map[string]bool{}
	other := false
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		p, err := exec.LookPath(filepath.Join(d, "gh"))
		if err != nil || tried[p] {
			continue
		}
		tried[p] = true
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if isGitHubCLI(p, st) {
			return p, nil
		}
		other = true
	}
	if other {
		return "", errNotGitHubCLI
	}
	return "", exec.ErrNotFound
}

// isGitHubCLI says whether the program at p is the GitHub CLI: its `--version` starts with "gh version ".
func isGitHubCLI(p string, st os.FileInfo) bool {
	ghAnswers.Lock()
	a, ok := ghAnswers.m[p]
	ghAnswers.Unlock()
	if ok && a.mod.Equal(st.ModTime()) && a.size == st.Size() {
		return a.cli
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "--version")
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	cli := bytes.HasPrefix(out, []byte("gh version "))
	// Only an answer from a program that ran is kept: a timeout or a failed start (fork failing for a moment) says
	// nothing about the program, so it is asked again next time.
	ran := err == nil || errors.As(err, new(*exec.ExitError))
	if ctx.Err() == nil && (cli || ran) {
		ghAnswers.Lock()
		ghAnswers.m[p] = ghAnswer{mod: st.ModTime(), size: st.Size(), cli: cli}
		ghAnswers.Unlock()
	}
	return cli
}

// ghMissing is the message for a lookup that found no GitHub CLI.
func ghMissing(err error) string {
	if errors.Is(err, errNotGitHubCLI) {
		return err.Error()
	}
	return "gh is not installed on this machine"
}
