//go:build unix

package managerupdate

import (
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"
)

func acquire(path string) (func(), error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return nil, errors.Join(fmt.Errorf("manager update already running: %w", e), f.Close())
	}
	return func() {
		if e := f.Close(); e != nil {
			log.Printf("manager update lock close: %v", e)
		}
	}, nil
}

func ownedIndependentFile(path string, uid int) error {
	i, e := os.Stat(path)
	if e != nil {
		return e
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot inspect file identity: %s", path)
	}
	if int(st.Uid) != uid || st.Nlink != 1 || !i.Mode().IsRegular() {
		return fmt.Errorf("file is not an independently owned regular file: %s", path)
	}
	return nil
}
