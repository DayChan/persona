package node

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// launchers.zshrc (#2455, docs/integrations/launchers.md "Import from ~/.zshrc"): the claude aliases of this machine's ~/.zshrc as the
// hub imports them into providers: `alias x="K=V K=V … claude [args]"`, quoted either way or not at all. Read from
// the file, not a shell: nothing of the user's runs. Their env holds keys: it goes to the hub through this RPC only,
// never to a log or an error.

// zshrcAlias is one claude alias: the variables it sets for claude, the words after claude, and the command.
type zshrcAlias struct {
	Alias   string            `json:"alias"`
	Env     map[string]string `json:"env"`
	Args    []string          `json:"args"`
	Command string            `json:"command"`
}

// zshrcPath is the ~/.zshrc zsh reads: $ZDOTDIR's first.
func (n *Node) zshrcPath() string {
	if d := os.Getenv("ZDOTDIR"); d != "" {
		return filepath.Join(d, ".zshrc")
	}
	return filepath.Join(n.home, ".zshrc")
}

// zshrcLaunchers answers launchers.zshrc: none when there is no ~/.zshrc.
func (n *Node) zshrcLaunchers() (map[string]any, error) {
	b, err := os.ReadFile(n.zshrcPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("~/.zshrc could not be read")
	}
	return map[string]any{"aliases": parseZshrcAliases(string(b))}, nil
}

// parseZshrcAliases is the claude aliases of a ~/.zshrc, in the order they are defined (one defined again is where
// its last definition is, as the shell keeps the last). Lines are split as the shell would (shellwords.go); one it
// does not follow is skipped.
func parseZshrcAliases(src string) []zshrcAlias {
	out := []zshrcAlias{}
	for line := range strings.Lines(src) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "alias ") {
			continue
		}
		segs, ok := shellSegments(line)
		if !ok || len(segs) == 0 {
			continue
		}
		for _, w := range segs[0].words[1:] { // `alias a=b; …`: the rest is another command
			name, value, ok := strings.Cut(w, "=")
			if !ok || !nameRe.MatchString(name) {
				continue // a flag (-g) or a lookup (`alias x`)
			}
			a, ok := claudeAlias(value)
			if !ok {
				continue
			}
			a.Alias = name
			out = append(slices.DeleteFunc(out, func(o zshrcAlias) bool { return o.Alias == name }), a)
		}
	}
	return out
}

// claudeAlias is an alias's value when one of its commands is claude (by path or not): the variables set in front of
// it (and by an `export` before it in the alias), and its arguments.
func claudeAlias(value string) (zshrcAlias, bool) {
	segs, ok := shellSegments(value)
	if !ok {
		return zshrcAlias{}, false
	}
	exported := map[string]string{}
	for _, seg := range segs {
		cmd := seg.words
		if len(cmd) > 0 && cmd[0] == "export" {
			for _, w := range cmd[1:] {
				if k, v, ok := strings.Cut(w, "="); ok && assignRe.MatchString(w) {
					exported[k] = v
				}
			}
			continue
		}
		env := maps.Clone(exported)
		i := commandIndex(cmd, env) // launchers.go
		if i < 0 || filepath.Base(cmd[i]) != "claude" {
			continue
		}
		return zshrcAlias{Env: env, Args: slices.Clone(cmd[i+1:]), Command: "claude"}, true
	}
	return zshrcAlias{}, false
}
