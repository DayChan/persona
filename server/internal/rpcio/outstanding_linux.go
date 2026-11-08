//go:build linux

// Package rpcio supplies transport-owner I/O observations, separate from the
// standard-library-only RPC framing contract.
package rpcio

import (
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

// OutstandingQuery measures outstanding data on a pipe or a unix stream socket:
// FIONREAD on the write end of a pipe counts unread bytes; SIOCOUTQ on a unix
// socket counts queue memory, released only when a complete send buffer is read.
// It is not a byte count. Owners must bound writes to 4 KiB for buffer release
// to remain observable even while a blocked Write has not returned. TCP is not
// supported: acknowledgement is not consumption by the receiving application.
// Other descriptors and writers without a descriptor have no progress signal.
// Control never switches a pollable file back to blocking mode, unlike Fd.
func OutstandingQuery(w io.Writer) func() (int64, bool) {
	sc, ok := w.(syscall.Conn)
	if !ok {
		return nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	var request uint
	if err := raw.Control(func(fd uintptr) {
		var st unix.Stat_t
		if unix.Fstat(int(fd), &st) != nil {
			return
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFIFO:
			request = unix.TIOCINQ // FIONREAD
		case unix.S_IFSOCK:
			kind, kindErr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
			domain, domainErr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_DOMAIN)
			if kindErr == nil && domainErr == nil && kind == unix.SOCK_STREAM && domain == unix.AF_UNIX {
				request = unix.SIOCOUTQ
			}
		}
	}); err != nil || request == 0 {
		return nil
	}
	query := func() (int64, bool) {
		var n int
		var ioctlErr error
		if err := raw.Control(func(fd uintptr) { n, ioctlErr = unix.IoctlGetInt(int(fd), request) }); err != nil || ioctlErr != nil {
			return 0, false
		}
		return int64(n), true
	}
	if _, ok := query(); !ok {
		return nil
	}
	return query
}
