package herdr

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// WorktreesDir is the directory herdr puts Git worktree checkouts under (`<dir>/<repo>/<branch-slug>`): the
// `[worktrees] directory` of the config file herdr reads (configPath), else ~/.herdr/worktrees. A relative value is
// taken from home. home is the user's home directory.
func WorktreesDir(home string) string {
	dir := ""
	if b, err := os.ReadFile(configPath(home)); err == nil {
		dir = worktreesDirIn(string(b))
	}
	if dir == "" {
		dir = "~/.herdr/worktrees"
	}
	return expandHome(home, dir)
}

// configPath is the config file herdr reads (its config_path): $HERDR_CONFIG_PATH, else config.toml in its config
// directory, $XDG_CONFIG_HOME/herdr as for SocketPath, else %APPDATA%\herdr on Windows and home's .config/herdr
// otherwise.
func configPath(home string) string {
	if p := os.Getenv("HERDR_CONFIG_PATH"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "herdr", "config.toml")
	}
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			return filepath.Join(app, "herdr", "config.toml")
		}
	}
	return filepath.Join(home, ".config", "herdr", "config.toml")
}

func expandHome(home, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return filepath.Join(home, p)
	}
	return filepath.Clean(p)
}

// worktreesDirIn reads `directory` out of the `[worktrees]` table of a config file ("" when it is not set). Only
// what this one setting needs of TOML: a basic or literal string on a line of its own in that table.
func worktreesDirIn(config string) string {
	inTable := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "["):
			head, _, _ := strings.Cut(line, "#")
			inTable = strings.TrimSpace(head) == "[worktrees]"
			continue
		}
		if !inTable {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "directory" {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) < 2 {
			return ""
		}
		q := val[0]
		if q != '"' && q != '\'' {
			return ""
		}
		var out strings.Builder
		closed := false
		for i := 1; i < len(val) && !closed; i++ {
			switch c := val[i]; {
			case c == q:
				closed = true
			case c == '\\' && q == '"' && i+1 < len(val) && (val[i+1] == '\\' || val[i+1] == '"'):
				i++
				out.WriteByte(val[i])
			default:
				out.WriteByte(c)
			}
		}
		if !closed {
			return ""
		}
		return out.String()
	}
	return ""
}

// WorktreeSlug is how herdr turns a branch name into the directory name of its checkout: lower case, every run of
// characters other than ASCII letters and digits one "-", none at either end ("Feat/A_b.c" is "feat-a-b-c").
func WorktreeSlug(branch string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(branch) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// WorktreeDirName is the folder of a branch's checkout in a worktrees directory: its slug, as the dialog names it, plus
// a short hash of the whole name (four bytes, eight hex digits) when the slug drops non-ASCII letters or digits or is
// empty. Otherwise "修复登录" would be "" (the checkouts directory itself) and "fix/登录", "fix/注册" both "fix"
// (#5775). Branches that differ only in case share a folder; that is a pre-existing constraint of WorktreeSlug.
func WorktreeDirName(branch string) string {
	slug := WorktreeSlug(branch)
	if slug != "" && !hasNonASCIIWord(branch) {
		return slug
	}
	sum := sha256.Sum256([]byte(branch))
	if slug == "" {
		slug = "worktree"
	}
	return slug + "-" + hex.EncodeToString(sum[:4])
}

// hasNonASCIIWord: s has a letter or digit the slug drops.
func hasNonASCIIWord(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return true
		}
	}
	return false
}
