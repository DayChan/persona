//go:build unix

package node

import (
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var errStdioNotInterruptible = errors.New("node: exclusive stdio requires pollable pipes or sockets")

// openOwnedStdio is exclusively for ServeStdio's process-owned descriptors.
// dup does NOT isolate file status flags: O_NONBLOCK also changes the original
// open descriptions (and any inherited aliases). The launcher must exclusively
// hand these endpoints to this node. No caller may use them after this succeeds.
// Ownership transfers both original files as well as their duplicates. All I/O
// uses fresh Go files registered with the runtime poller; terminal interruption
// closes originals too, otherwise an open stdout alias would prevent remote EOF.
// NewFile alone on inherited blocking stdout does not register pollable I/O.
// Regular files and poller rejection fail before ownership transfer or Serve.
func openOwnedStdio(input, output *os.File) (*ownedRPCTransport, error) {
	originals := []*os.File{input, output}
	var files []*os.File
	var flags []int
	var descriptors []int
	committed := false
	defer func() {
		if !committed {
			for i, fd := range descriptors {
				// No I/O has started; restore any shared flags on failed setup.
				_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags[i])
			}
			for _, f := range files {
				_ = f.Close()
			}
		}
	}()
	for _, original := range originals {
		info, err := original.Stat()
		if err != nil {
			return nil, err
		}
		if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
			return nil, errStdioNotInterruptible
		}
		raw, err := original.SyscallConn()
		if err != nil {
			return nil, err
		}
		var fd, flag int
		var flagErr error
		// Unlike File.Fd, SyscallConn never switches an already pollable
		// original to blocking mode, including during failed preflight.
		if err := raw.Control(func(value uintptr) {
			fd = int(value)
			flag, flagErr = unix.FcntlInt(value, unix.F_GETFL, 0)
		}); err != nil {
			return nil, err
		}
		if flagErr != nil {
			return nil, flagErr
		}
		// Capture both open descriptions before changing either: stdin and
		// stdout may themselves be aliases of a single socket description.
		flags = append(flags, flag)
		descriptors = append(descriptors, fd)
	}
	for i, fd := range descriptors {
		dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		if err := unix.SetNonblock(dup, true); err != nil {
			_ = unix.Close(dup)
			return nil, err
		}
		f := os.NewFile(uintptr(dup), originals[i].Name())
		files = append(files, f)
		// A pipe/socket Stat is not a pollability capability. Verify Go's
		// poll descriptor; on unsupported endpoints this is ErrNoDeadline.
		if err := f.SetDeadline(time.Time{}); err != nil {
			return nil, errors.Join(errStdioNotInterruptible, err)
		}
	}
	var once sync.Once
	transport := &ownedRPCTransport{Reader: files[0], Writer: files[1]}
	transport.interrupt = func() {
		once.Do(func() {
			// Pollable Close evicts active Read/Write before waiting for refs.
			_ = files[0].Close()
			_ = files[1].Close()
			// Exclusive ownership means no I/O uses these original Go files.
			// Close every transferred endpoint so the hub can actually see EOF,
			// not just a logical Peer.Close with a hidden stdout alias still open.
			// Borrowed Serve files never enter this ownership path.
			_ = input.Close()
			_ = output.Close()
		})
	}
	committed = true
	return transport, nil
}
