//go:build linux

package node

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// procfsProcs lists processes from <root>/<pid>/stat (Linux; parseStat). It runs every port sweep over every process
// on the machine, so it reads the stat files into one reused buffer through openat on the root's descriptor instead of
// os.ReadFile: a process costs its directory name and the string parseStat copies out, not an *os.File, a path and a
// buffer each.
func procfsProcs(root string) (map[int]proc, error) {
	dir, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	dirfd := int(dir.Fd())
	procs := make(map[int]proc, len(names))
	path, buf := make([]byte, 0, 32), make([]byte, 0, 1024)
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		path = append(append(path[:0], name...), "/stat\x00"...)
		if buf, err = readFileAt(dirfd, path, buf[:0]); err != nil {
			continue // the process exited since the directory was read
		}
		if p, ok := parseStat(buf); ok {
			procs[pid] = p
		}
	}
	return procs, nil
}

// readFileAt appends the file at path (relative to dirfd, NUL-terminated) to buf. It calls openat itself: the
// syscall package's wrappers copy a string path on every call.
func readFileAt(dirfd int, path, buf []byte) ([]byte, error) {
	var fd uintptr
	var errno syscall.Errno
	for {
		fd, _, errno = syscall.Syscall6(syscall.SYS_OPENAT, uintptr(dirfd), uintptr(unsafe.Pointer(&path[0])),
			syscall.O_RDONLY|syscall.O_CLOEXEC, 0, 0, 0)
		if errno != syscall.EINTR {
			break
		}
	}
	if errno != 0 {
		return buf, errno
	}
	defer syscall.Close(int(fd))
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := syscall.Read(int(fd), buf[len(buf):cap(buf)])
		switch {
		case err == syscall.EINTR:
		case err != nil:
			return buf, err
		case n == 0:
			return buf, nil
		default:
			buf = buf[:len(buf)+n]
		}
	}
}
