//go:build !windows

package node

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// progName is a command line's program as the scans compare it (isClaudeCmd, agentOfCmd): its base name.
func progName(argv0 string) string { return filepath.Base(argv0) }

// scanOrcaProcsOS and scanClaudeProcessesOS are Windows' scans; macOS and Linux use ps and /proc.
func scanOrcaProcsOS() (map[string][]orcaProc, error) { return nil, errors.New("no process scan here") }

func scanClaudeProcessesOS() map[int]procEnv { return map[int]procEnv{} }

// scanCodexProcessesOS and scanOpenRolloutsOS are Windows' (codex_procs_windows.go); macOS and Linux use ps, lsof and
// /proc.
func scanCodexProcessesOS() ([]codexProcess, bool) { return nil, false }

func scanOpenRolloutsOS([]codexProcess) (map[int][]string, bool) { return nil, false }

// codexStartProgram: a launcher off Windows is run as it is (codex_procs_windows.go).
func codexStartProgram(path string, _ bool, env []string) (string, []string, error) {
	return path, env, nil
}

// loginShellProgram is the interactive shell loginShell runs scripts in: zsh, so ~/.zshrc functions and PATH are
// there, else bash. A user whose login shell is bash and who has no ~/.zshrc gets bash even where zsh is installed
// (a Linux server with zsh around but everything in ~/.bashrc), so ~/.bashrc's launchers and PATH are there.
func loginShellProgram() string {
	if _, err := exec.LookPath("zsh"); err != nil {
		return "bash"
	}
	if userLoginShell() == "bash" {
		if home, err := os.UserHomeDir(); err == nil {
			if _, err := os.Stat(filepath.Join(home, ".zshrc")); errors.Is(err, os.ErrNotExist) {
				return "bash"
			}
		}
	}
	return "zsh"
}

// userLoginShell is the base name of the user's login shell: $SHELL, else the user's /etc/passwd entry (a node under
// systemd has no $SHELL); "" when neither says.
func userLoginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return filepath.Base(sh)
	}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	return passwdShell(string(b), os.Getuid())
}
