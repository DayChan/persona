package egress

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// silentListener is a real loopback listener whose accept queue is full: the kernel drops every further SYN, so a
// dial to it never connects (a host that does not answer). Linux only.
func silentListener(t *testing.T, ip string, port int) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	addr := &syscall.SockaddrInet4{Port: port}
	copy(addr.Addr[:], net.ParseIP(ip).To4())
	if err := syscall.Bind(fd, addr); err != nil {
		t.Skipf("bind %s:%d: %v", ip, port, err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	target := net.JoinHostPort(ip, strconv.Itoa(port))
	// Connections nobody accepts fill the queue; once one hangs, the listener is silent.
	for range 4 {
		c, err := net.DialTimeout("tcp", target, 200*time.Millisecond)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = c.Close() })
	}
	t.Skipf("%s never stopped answering; this kernel does not drop SYNs on a full accept queue", target)
}

// With real dialers every attempt ends with its own "i/o timeout" when the request's deadline passes, at the same
// moment as the deadline itself; the dial must still report the deadline (errors.Is), whichever it sees first.
func TestRaceRealSocketsDeadlineIsContextError(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.2"), net.ParseIP("127.0.0.3")}
	for _, ip := range ips {
		silentListener(t, ip.String(), port)
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	attempt := func(ctx context.Context, target string) (net.Conn, error) { return d.DialContext(ctx, "tcp", target) }
	for i := range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		began := time.Now()
		c, err := dialRace(ctx, ips, strconv.Itoa(port), attempt)
		took := time.Since(began)
		cancel()
		if c != nil {
			c.Close()
			t.Fatalf("round %d: a silent listener connected", i)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("round %d after %s: %v is not the deadline", i, took, err)
		}
		if took > 2*time.Second {
			t.Fatalf("round %d took %s", i, took)
		}
	}
}
