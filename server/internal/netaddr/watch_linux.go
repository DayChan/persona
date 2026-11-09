//go:build linux

package netaddr

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
)

func watchEvents(ctx context.Context) <-chan struct{} {
	// Group masks, not RTNLGRP ordinal numbers. Link events cover UP/down changes.
	const groups = 1 | 0x10 | 0x100 // RTMGRP_LINK | IPV4_IFADDR | IPV6_IFADDR
	socket, err := openRouteSocket(groups)
	if err != nil {
		return nil
	}
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		defer socket.file.Close()
		stop := context.AfterFunc(ctx, func() { socket.file.Close() })
		defer stop()
		buf := make([]byte, receiveBuffer)
		for ctx.Err() == nil {
			data, err := socket.receive(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				notify(out)
				if errors.Is(err, syscall.ENOBUFS) || errors.Is(err, errDumpInterrupted) {
					continue
				}
				return // Periodic snapshots continue even if the subscription fails.
			}
			if addressChange(data, binary.NativeEndian) {
				notify(out)
			}
		}
	}()
	return out
}
