//go:build !windows

package node

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/osproc"
)

// privateSocket: a Unix control socket and its directory belong to this user, no one else may use them, and neither
// is a symlink (codexSocketPath has already followed the one alias Codex makes).
func privateSocket(path string) error {
	for _, p := range []string{filepath.Dir(path), path} {
		st, err := os.Lstat(p)
		if err != nil {
			return errors.New("Unix 入口不可访问")
		}
		if !osproc.OwnedByMe(st) || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
			return errors.New("Unix 入口或目录不属于当前用户，或权限过宽")
		}
		if p == path && st.Mode()&os.ModeSocket == 0 {
			return errors.New("Unix 入口不是 socket")
		}
	}
	return nil
}
