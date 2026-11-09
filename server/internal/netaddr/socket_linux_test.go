//go:build linux

package netaddr

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRouteSocketCloseUnblocksReceive(t *testing.T) {
	socket, err := openRouteSocket(0)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.file.Close()
	done := make(chan error, 1)
	go func() { _, err := socket.receive(make([]byte, 4096)); done <- err }()
	socket.file.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("receive succeeded after close")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock receive")
	}
}

func TestRouteSocketTruncatedDump(t *testing.T) {
	socket, err := openRouteSocket(0)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.file.Close()
	if err := socket.file.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 24)
	binary.NativeEndian.PutUint32(request[:4], 24)
	binary.NativeEndian.PutUint16(request[4:6], 22) // RTM_GETADDR.
	binary.NativeEndian.PutUint16(request[6:8], 0x301)
	binary.NativeEndian.PutUint32(request[8:12], 1)
	if err := socket.send(request); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.receive(make([]byte, 1)); err != errDumpInterrupted {
		t.Fatalf("truncated datagram: %v", err)
	}
}

// A dump datagram larger than the buffer truncates, and List's retry receives into the larger buffer: a 1-byte first
// buffer still lists the addresses a 64 KiB one does.
func TestListRetriesATruncatedDumpWithALargerBuffer(t *testing.T) {
	if _, err := listLinux(1); !errors.Is(err, errDumpInterrupted) {
		t.Fatalf("a 1-byte buffer: %v, want an interrupted dump", err)
	}
	got, err := list(1)
	if err != nil {
		t.Fatalf("retry after a truncated dump: %v", err)
	}
	want, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(got, want, func(a, b Addr) bool { return compareIdentity(a, b) == 0 }) {
		t.Fatalf("after a truncated dump: %v, want %v", got, want)
	}
}

func TestNativeWatchCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := watchEvents(ctx)
	cancel()
	if events == nil {
		t.Fatal("native subscription unavailable")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatal("native reader did not exit")
		}
	}
}
