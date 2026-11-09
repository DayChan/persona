package node

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

func readonlyStartUnsupported() error {
	return refusal("readonly_unsupported", i18n.T(i18n.Default, "rpc-errors.readonly_unsupported"), nil)
}

// codexReadonlyArgs constrains a terminal Codex without changing other modes or the launcher's own arguments.
func (n *Node) codexReadonlyArgs(launcher string, argv []string, mode string) ([]string, error) {
	if mode != "plan" {
		return argv, nil
	}
	if l, ok := n.managedLauncher(launcher); ok {
		// Managed arguments are prepended later by `personad launch`; checking its wrapper argv alone misses them.
		if codexReadonlyConflict(l.Args) {
			return nil, readonlyStartUnsupported()
		}
	}
	if codexReadonlyConflict(argv) {
		return nil, readonlyStartUnsupported()
	}
	// Codex 0.167 no longer accepts the thread API's legacy "untrusted" CLI policy.
	// "never" disallows approval-based escalation out of the read-only sandbox.
	return append(slices.Clone(argv), "--sandbox", "read-only", "--ask-for-approval", "never"), nil
}

func codexReadonlyConflict(argv []string) bool {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		flag, _, hasValue := strings.Cut(arg, "=")
		switch flag {
		case "--full-auto", "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--approve-for-me":
			return true // These presets grant writes or bypass the sandbox, conflicting with read-only execution.
		case "--sandbox", "--ask-for-approval":
			return true // Do not guess precedence or replace a launcher's explicit permission choice.
		case "--":
			return true // Subsequent injected options would be interpreted as prompt text, not permission flags.
		case "-c", "--config", "-m", "--model", "-p", "--profile", "-i", "--image", "-C", "--cd",
			"--remote", "--remote-auth-token-env", "--local-provider", "--enable", "--disable", "--add-dir":
			if !hasValue {
				i++ // An option's value is not another permission flag.
			}
		default:
			if strings.HasPrefix(arg, "-s") || strings.HasPrefix(arg, "-a") {
				return true // Short permission flags also accept attached values, including -s=value and -avalue.
			}
		}
	}
	return false
}
