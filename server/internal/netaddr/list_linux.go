//go:build linux

package netaddr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// List returns a complete snapshot of local interface addresses using
// RTM_GETADDR, including full IFA_FLAGS, scope and available lifetimes. It keeps
// down, virtual and non-public addresses for diagnostics and caller LAN policy.
// Interrupted dumps are retried; failures never return a partial snapshot.
func List() ([]Addr, error) { return list(receiveBuffer) }

// list makes up to 3 attempts, the first receiving into a buffer of size bytes and each retry into
// retryReceiveBuffer, so a datagram truncated by the first fits the next.
func list(size int) ([]Addr, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var out []Addr
		out, err = listLinux(size)
		if !errors.Is(err, errDumpInterrupted) && !errors.Is(err, syscall.ENOBUFS) {
			return out, err
		}
		size = retryReceiveBuffer
	}
	return nil, err
}

func listLinux(size int) ([]Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("netaddr: interfaces: %w", err)
	}
	byIndex := make(map[int]net.Interface, len(interfaces))
	virtual := make(map[int]bool, len(interfaces))
	for _, iface := range interfaces {
		byIndex[iface.Index] = iface
		virtual[iface.Index] = virtualInterface(iface.Name, filepath.EvalSymlinks)
	}
	socket, err := openRouteSocket(0)
	if err != nil {
		return nil, fmt.Errorf("netaddr: open address dump: %w", err)
	}
	defer socket.file.Close()
	// This is an I/O deadline, not a measured discovery latency.
	if err := socket.file.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	request := make([]byte, 24)
	binary.NativeEndian.PutUint32(request[:4], uint32(len(request)))
	binary.NativeEndian.PutUint16(request[4:6], syscall.RTM_GETADDR)
	binary.NativeEndian.PutUint16(request[6:8], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	binary.NativeEndian.PutUint32(request[8:12], 1)
	if err := socket.send(request); err != nil {
		return nil, fmt.Errorf("netaddr: request addresses: %w", err)
	}
	var out []Addr
	buf := make([]byte, size)
	for {
		data, err := socket.receive(buf)
		if err != nil {
			return nil, fmt.Errorf("netaddr: receive addresses: %w", err)
		}
		messages, err := parseMessages(data, binary.NativeEndian)
		if err != nil {
			return nil, err
		}
		for _, m := range messages {
			if m.seq != 1 {
				return nil, errors.New("netaddr: unexpected dump sequence")
			}
			if err := netlinkStatus(m, binary.NativeEndian); err != nil {
				return nil, err
			}
			switch m.kind {
			case nlDone:
				slices.SortFunc(out, compareIdentity)
				return out, nil
			case rtmNewAddr:
				a, err := parseAddr(m.data, binary.NativeEndian)
				if err != nil {
					return nil, err
				}
				if !a.IP.IsValid() {
					continue
				}
				iface, ok := byIndex[a.InterfaceIndex]
				// Interface creation raced the address dump. Retry both snapshots together.
				if !ok {
					return nil, errDumpInterrupted
				}
				a.Interface = iface.Name
				a.InterfaceFlags = iface.Flags
				a.Virtual = virtual[a.InterfaceIndex]
				out = append(out, a)
			}
		}
	}
}
