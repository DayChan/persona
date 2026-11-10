//go:build linux

package node

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// readProcArgv walks a procfs root and returns pid -> argv for the processes owned by uid. Other users' processes
// are skipped before their cmdline is read (one lstat each); this matches `ps -U <uid>` on macOS, and their environ
// could not be read anyway. Processes with an empty cmdline (zombies, kernel threads) are left out.
//
// The walk runs every 30 s over every process on the machine, so it allocates only what it returns: the names, not a
// DirEntry and a FileInfo each; the uid from a Stat_t of its own; every cmdline read into one buffer, not a new one
// per process grown for the long command lines of browsers and language servers.
func readProcArgv(root string, uid uint32) map[int][]string {
	out := map[int][]string{}
	d, err := os.Open(root)
	if err != nil {
		return out
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return out
	}
	var st syscall.Stat_t
	buf := make([]byte, 0, 4096)
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		path := root + "/" + name + "/cmdline"
		if syscall.Lstat(path[:len(path)-len("/cmdline")], &st) != nil || st.Uid != uid {
			continue // exited since the directory was read, or another user's
		}
		if buf, err = readInto(buf[:0], path); err != nil || len(buf) == 0 {
			continue
		}
		out[pid] = strings.Split(string(bytes.TrimRight(buf, "\x00")), "\x00")
	}
	return out
}

// readInto reads the file at path into buf, as os.ReadFile does into a new slice, and returns buf, grown if the file
// did not fit. procfs files say they are empty, so it reads until EOF.
func readInto(buf []byte, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return buf, err
	}
	defer f.Close()
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
}
