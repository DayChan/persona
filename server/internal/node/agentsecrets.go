package node

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent runtimes keep their credentials in their own config directories, which a launcher may move: Claude Code in
// $CLAUDE_CONFIG_DIR (else ~/.claude), Codex in $CODEX_HOME (else ~/.codex). A launcher's gateway token also sits in the
// `env` of Claude Code's settings. The file guard refuses these files in every such directory the node knows, not only
// the default ones (#1959); the rest of the directory (projects, transcripts, sessions) stays readable.

// claudeSecretNames are the files of a Claude Code config directory that hold credentials.
var claudeSecretNames = []string{".credentials.json", "settings.json", "settings.local.json"}

// codexSecretNames are the files of a Codex home that hold credentials.
var codexSecretNames = []string{"auth.json"}

// agentSecret says whether path (absolute, clean) is a credential of an agent config directory the node knows. It
// reads only what is already known (no process scan) and compares by name as written and by the directories' real
// paths, so a link to such a directory is refused as the directory is.
func (n *Node) agentSecret(path string) bool {
	dir, base := filepath.Dir(path), strings.ToLower(filepath.Base(path))
	claude, codex := n.knownAgentDirs()
	return secretIn(dir, base, claude, claudeSecretNames) || secretIn(dir, base, codex, codexSecretNames)
}

func secretIn(dir, base string, dirs, names []string) bool {
	if !slices.Contains(names, base) {
		return false
	}
	d := fold(dir)
	for _, c := range dirs {
		if fold(c) == d {
			return true
		}
		if real, err := filepath.EvalSymlinks(c); err == nil && fold(real) == d {
			return true
		}
	}
	return false
}

// knownAgentDirs lists the Claude Code config directories and Codex homes the node knows of now, expanded and
// absolute: the defaults, the session scope's, those seen in running processes and those the hub configured.
func (n *Node) knownAgentDirs() (claude, codex []string) {
	add := func(list []string, p string) []string {
		if p = proto.ExpandHome(p, n.home); p != "" && filepath.IsAbs(p) {
			return append(list, filepath.Clean(p))
		}
		return list
	}
	if n.sessionScope != nil {
		claude = add(claude, n.sessionScope.claude)
		codex = add(codex, n.sessionScope.codex)
	}
	if n.home != "" {
		claude = add(claude, claudeDirOf(n.home))
		codex = add(codex, filepath.Join(n.home, ".codex"))
	}
	if d := n.cdirs; d != nil {
		d.mu.Lock()
		for _, c := range append(slices.Clone(d.found), d.known...) {
			claude = add(claude, c)
		}
		for _, c := range d.byPID {
			claude = add(claude, c)
		}
		d.mu.Unlock()
	}
	if r := n.codices; r != nil {
		r.mu.Lock()
		for _, h := range r.config.Homes {
			codex = add(codex, h)
		}
		for _, s := range r.config.Servers {
			codex = add(codex, s.Home)
		}
		if r.config.LegacyHome != "" {
			codex = add(codex, r.config.LegacyHome)
		}
		for _, s := range r.threads {
			codex = add(codex, s.home)
		}
		r.mu.Unlock()
	}
	return claude, codex
}
