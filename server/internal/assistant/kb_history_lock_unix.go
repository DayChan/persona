//go:build !windows

package assistant

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes f's exclusive lock without waiting: false when another holds it.
func tryLockFile(f *os.File) (bool, error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
