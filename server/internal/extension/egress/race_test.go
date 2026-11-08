package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// fakeHop is how one address answers a dial: never (silent: a SYN nobody answers), at once with a refusal, or
// with a connection after a delay. stubborn: the connection comes even after the dial is canceled.
type fakeHop struct {
	silent, refuse, stubborn bool
	after                    time.Duration
	// timeout: silent, and ends as a real dialer does at the request's deadline, with its own "i/o timeout"
	// rather than the context's error, from a timer of its own.
	timeout bool
}

// fakeNet plays the internet for Proxy.Dial, inside a synctest bubble: it records the order addresses are
// dialed in, how many attempts are open at once, and every connection it hands out.
type fakeNet struct {
	t     *testing.T
	plan  map[string]fakeHop
	mu    sync.Mutex
	order []string
	open  int
	peak  int
	conns []*fakeConn
}

type fakeConn struct {
	net.Conn
	addr   string
	mu     sync.Mutex
	closed bool
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (f *fakeNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.order = append(f.order, addr)
	f.open++
	f.peak = max(f.peak, f.open)
	hop, ok := f.plan[addr]
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.open--
		f.mu.Unlock()
	}()
	opErr := func(err error) error { return &net.OpError{Op: "dial", Net: network, Err: err} }
	switch {
	case !ok:
		f.t.Errorf("dialed %s, which is not in the plan", addr)
		return nil, opErr(syscall.ECONNREFUSED)
	case hop.refuse:
		return nil, opErr(syscall.ECONNREFUSED)
	case hop.silent:
		<-ctx.Done()
		return nil, opErr(ctx.Err())
	case hop.timeout:
		// A real dialer's socket deadline is its own timer, set to the context's deadline: it can fire before
		// the context's own timer has marked the context done.
		dl, ok := ctx.Deadline()
		if !ok {
			f.t.Error("timeout hop without a deadline")
			return nil, opErr(syscall.ECONNREFUSED)
		}
		timer := time.NewTimer(time.Until(dl))
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil, opErr(os.ErrDeadlineExceeded)
		case <-ctx.Done():
			return nil, opErr(ctx.Err())
		}
	}
	if hop.stubborn {
		time.Sleep(hop.after)
	} else {
		timer := time.NewTimer(hop.after)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, opErr(ctx.Err())
		case <-timer.C:
		}
	}
	near, far := net.Pipe()
	_ = far.Close()
	c := &fakeConn{Conn: near, addr: addr}
	f.mu.Lock()
	f.conns = append(f.conns, c)
	f.mu.Unlock()
	return c, nil
}

func (f *fakeNet) openNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

func (f *fakeNet) dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.order)
}

// unclosed are the connections handed out and not closed, but keep.
func (f *fakeNet) unclosed(keep net.Conn) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.conns {
		if net.Conn(c) != keep && !c.isClosed() {
			out = append(out, c.addr)
		}
	}
	return out
}

func racingProxy(f *fakeNet, ips ...string) *Proxy {
	return &Proxy{
		Resolve: func(context.Context, string) ([]net.IP, error) {
			out := make([]net.IP, len(ips))
			for i, s := range ips {
				out[i] = net.ParseIP(s)
			}
			return out, nil
		},
		Dial:     f.dial,
		Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
	}
}

// The shape of #1181: four IPv4 addresses first, IPv4 never answering here, then one IPv6 that does. The
// request has a 15-second budget; the IPv6 address must be reached long before it runs out.
func TestRuntimeDialSilentFirstAddresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeNet{t: t, plan: map[string]fakeHop{
			"192.0.2.1:443": {silent: true}, "192.0.2.2:443": {silent: true}, "192.0.2.3:443": {silent: true},
			"192.0.2.4:443": {silent: true}, "[2001:db8::1]:443": {after: 200 * time.Millisecond},
		}}
		p := racingProxy(f, "192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "2001:db8::1")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		began := time.Now()
		c, err := p.runtimeDial(ctx, "news.example.com", "443", false)
		if err != nil {
			t.Fatalf("after %s: %v", time.Since(began), err)
		}
		defer c.Close()
		if took := time.Since(began); took != raceAttemptDelay+200*time.Millisecond {
			t.Fatalf("connected after %s", took)
		}
		if got := c.(*fakeConn).addr; got != "[2001:db8::1]:443" {
			t.Fatalf("won by %s", got)
		}
		if got := f.dialed(); !slices.Equal(got, []string{"192.0.2.1:443", "[2001:db8::1]:443"}) {
			t.Fatalf("dialed %v", got)
		}
		synctest.Wait()
		if n := f.openNow(); n != 0 {
			t.Fatalf("%d attempts still open after the race", n)
		}
	})
}

// Families alternate from the first address's family, each in the resolver's order; a refusal starts the next
// address at once; when all fail, the error has every failure.
func TestRuntimeDialInterleavesFamilies(t *testing.T) {
	for _, tc := range []struct {
		ips, want []string
	}{
		{[]string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "2001:db8::1", "2001:db8::2"},
			[]string{"192.0.2.1:443", "[2001:db8::1]:443", "192.0.2.2:443", "[2001:db8::2]:443", "192.0.2.3:443"}},
		{[]string{"2001:db8::1", "192.0.2.1", "192.0.2.2"},
			[]string{"[2001:db8::1]:443", "192.0.2.1:443", "192.0.2.2:443"}},
		{[]string{"192.0.2.1", "::ffff:192.0.2.2", "2001:db8::1"},
			[]string{"192.0.2.1:443", "[2001:db8::1]:443", "192.0.2.2:443"}},
	} {
		synctest.Test(t, func(t *testing.T) {
			plan := map[string]fakeHop{}
			for _, a := range tc.want {
				plan[a] = fakeHop{refuse: true}
			}
			f := &fakeNet{t: t, plan: plan}
			began := time.Now()
			_, err := racingProxy(f, tc.ips...).runtimeDial(context.Background(), "news.example.com", "443", false)
			if err == nil || !errors.Is(err, syscall.ECONNREFUSED) || !strings.Contains(err.Error(), "addresses failed") {
				t.Fatalf("error %v", err)
			}
			if took := time.Since(began); took != 0 {
				t.Fatalf("refusals took %s; a failure must start the next address at once", took)
			}
			if got := f.dialed(); !slices.Equal(got, tc.want) {
				t.Fatalf("dialed %v, want %v", got, tc.want)
			}
		})
	}
}

// Nothing answers: the dial ends at the request's deadline, with at most raceInFlight attempts open at once
// and none left open afterwards (synctest.Test also fails on a goroutine left behind).
func TestRuntimeDialAllSilentEndsAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plan := map[string]fakeHop{}
		var ips []string
		for _, ip := range []string{"192.0.2.1", "2001:db8::1", "192.0.2.2", "2001:db8::2", "192.0.2.3", "2001:db8::3"} {
			ips = append(ips, ip)
			plan[net.JoinHostPort(ip, "443")] = fakeHop{silent: true}
		}
		f := &fakeNet{t: t, plan: plan}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		began := time.Now()
		c, err := racingProxy(f, ips...).runtimeDial(ctx, "news.example.com", "443", false)
		if c != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("conn %v, error %v", c, err)
		}
		if took := time.Since(began); took != 15*time.Second {
			t.Fatalf("gave up after %s", took)
		}
		if f.peak > raceInFlight {
			t.Fatalf("%d attempts open at once", f.peak)
		}
		// Four at once; from 10 s on the oldest makes room, for the last two addresses and then for the first
		// two again (each address gets one more chance at the back of the line).
		want := []string{"192.0.2.1:443", "[2001:db8::1]:443", "192.0.2.2:443", "[2001:db8::2]:443", "192.0.2.3:443",
			"[2001:db8::3]:443", "192.0.2.1:443", "[2001:db8::1]:443"}
		if got := f.dialed(); !slices.Equal(got, want) {
			t.Fatalf("dialed %v, want %v", got, want)
		}
		synctest.Wait()
		if n := f.openNow(); n != 0 {
			t.Fatalf("%d attempts still open", n)
		}
	})
}

// With raceInFlight silent attempts open, the oldest makes room after raceReclaimAfter, so an address late in
// the list is still reached.
func TestRuntimeDialReclaimsSlotForLaterAddress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plan := map[string]fakeHop{"192.0.2.6:443": {after: 100 * time.Millisecond}}
		ips := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6"}
		for _, ip := range ips[:5] {
			plan[ip+":443"] = fakeHop{silent: true}
		}
		f := &fakeNet{t: t, plan: plan}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		began := time.Now()
		c, err := racingProxy(f, ips...).runtimeDial(ctx, "news.example.com", "443", false)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if took, want := time.Since(began), raceReclaimAfter+raceAttemptDelay+100*time.Millisecond; took != want {
			t.Fatalf("connected after %s, want %s", took, want)
		}
		if f.peak != raceInFlight {
			t.Fatalf("peak %d attempts open, want %d", f.peak, raceInFlight)
		}
		synctest.Wait()
		if n := f.openNow(); n != 0 {
			t.Fatalf("%d attempts still open", n)
		}
	})
}

// Canceling the request mid-race (revocation, a settings change, the guest gone) stops every attempt.
func TestRuntimeDialCancelStopsEveryAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plan := map[string]fakeHop{}
		ips := []string{"192.0.2.1", "2001:db8::1", "192.0.2.2", "2001:db8::2"}
		for _, ip := range ips {
			plan[net.JoinHostPort(ip, "443")] = fakeHop{silent: true}
		}
		f := &fakeNet{t: t, plan: plan}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(600*time.Millisecond, cancel)
		c, err := racingProxy(f, ips...).runtimeDial(ctx, "news.example.com", "443", false)
		if c != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("conn %v, error %v", c, err)
		}
		if n := len(f.dialed()); n != 3 {
			t.Fatalf("%d attempts started in 600ms, want 3", n)
		}
		synctest.Wait()
		if n := f.openNow(); n != 0 {
			t.Fatalf("%d attempts still open after cancel", n)
		}
	})
}

// Only the winner stays open: a loser that connects anyway, even after its cancellation, is closed.
func TestRuntimeDialClosesLosers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeNet{t: t, plan: map[string]fakeHop{
			"192.0.2.1:443":     {stubborn: true, after: 400 * time.Millisecond},
			"[2001:db8::1]:443": {after: 50 * time.Millisecond},
			"192.0.2.2:443":     {after: 50 * time.Millisecond}, // starts at 500ms: never, the race is over
		}}
		p := racingProxy(f, "192.0.2.1", "192.0.2.2", "2001:db8::1")
		c, err := p.runtimeDial(context.Background(), "news.example.com", "443", false)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if got := c.(*fakeConn).addr; got != "[2001:db8::1]:443" {
			t.Fatalf("won by %s", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := f.unclosed(c); len(got) != 0 {
			t.Fatalf("losers left open: %v", got)
		}
		if len(f.conns) != 2 {
			t.Fatalf("%d connections made, want the winner and the stubborn loser", len(f.conns))
		}
		if slices.Contains(f.dialed(), "192.0.2.2:443") {
			t.Fatal("an address was dialed after the race was won")
		}
	})
}

// Every address is checked before any is dialed: one refused answer refuses the name, in both runtime modes,
// and the process path skips an internal address without dialing it while it races the rest.
func TestRaceNeverDialsRefusedAddress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeNet{t: t, plan: map[string]fakeHop{}}
		ctx := context.Background()
		p := racingProxy(f, "192.0.2.1", "2001:db8::1", "127.0.0.1")
		if _, err := p.runtimeDial(ctx, "news.example.com", "443", false); !errors.Is(err, errInternal) {
			t.Fatalf("loopback answer: %v", err)
		}
		p = racingProxy(f, "192.0.2.1", "2001:db8::1", net.IPv4(169, 254, 169, 254).String())
		if _, err := p.runtimeDial(ctx, "news.example.com", "443", true); !errors.Is(err, errNotForSettings) {
			t.Fatalf("metadata answer: %v", err)
		}
		if _, err := p.dialConfigured(ctx, "news.example.com", "443"); !errors.Is(err, errNotForSettings) {
			t.Fatalf("metadata answer (process path): %v", err)
		}
		if got := f.dialed(); len(got) != 0 {
			t.Fatalf("dialed %v", got)
		}
		f.plan = map[string]fakeHop{"192.0.2.1:443": {silent: true}, "[2001:db8::1]:443": {after: 10 * time.Millisecond}}
		p = racingProxy(f, "127.0.0.1", "192.0.2.1", "2001:db8::1")
		c, err := p.dialOut(ctx, "news.example.com", "443", Rule{Hosts: []string{"news.example.com"}})
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if got := f.dialed(); !slices.Equal(got, []string{"192.0.2.1:443", "[2001:db8::1]:443"}) {
			t.Fatalf("dialed %v", got)
		}
		synctest.Wait()
	})
}

// The process path through settings (dialConfigured) races its checked addresses too.
func TestConfiguredDialSilentFirstAddress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeNet{t: t, plan: map[string]fakeHop{"192.0.2.1:443": {silent: true}, "[2001:db8::1]:443": {after: 10 * time.Millisecond}}}
		p := racingProxy(f, "192.0.2.1", "2001:db8::1")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		began := time.Now()
		c, err := p.dialConfigured(ctx, "lan.example.com", "443")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if took := time.Since(began); took != raceAttemptDelay+10*time.Millisecond {
			t.Fatalf("connected after %s", took)
		}
		synctest.Wait()
	})
}

// Through the hub's upstream proxy each attempt is a CONNECT to one checked address (never the name); a
// CONNECT the proxy never answers is raced like a silent SYN and its connection to the proxy closed.
func TestRuntimeDialRacesThroughUpstreamProxy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var targets []string
		var proxyConns []*fakeConn
		p := &Proxy{
			Resolve: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("2001:db8::1")}, nil
			},
			Upstream: func(*url.URL) (*url.URL, error) { return url.Parse("http://proxy.example.com:3128") },
			Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
				if addr != "proxy.example.com:3128" {
					t.Errorf("dialed %s, not the proxy", addr)
				}
				near, far := net.Pipe()
				go func() {
					defer far.Close()
					r, err := http.ReadRequest(bufio.NewReader(far))
					if err != nil {
						return
					}
					mu.Lock()
					targets = append(targets, r.Host)
					mu.Unlock()
					if r.Host == "192.0.2.1:443" {
						_, _ = io.Copy(io.Discard, far) // never answers; ends when the broker closes
						return
					}
					time.Sleep(20 * time.Millisecond)
					_, _ = io.WriteString(far, "HTTP/1.1 200 Connection established\r\n\r\n")
					_, _ = io.Copy(io.Discard, far)
				}()
				c := &fakeConn{Conn: near, addr: addr}
				mu.Lock()
				proxyConns = append(proxyConns, c)
				mu.Unlock()
				return c, nil
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		began := time.Now()
		c, err := p.runtimeDial(ctx, "news.example.com", "443", false)
		if err != nil {
			t.Fatal(err)
		}
		if took := time.Since(began); took != raceAttemptDelay+20*time.Millisecond {
			t.Fatalf("tunnel after %s", took)
		}
		synctest.Wait()
		mu.Lock()
		if !slices.Equal(targets, []string{"192.0.2.1:443", "[2001:db8::1]:443"}) {
			t.Fatalf("CONNECT targets %v", targets)
		}
		if len(proxyConns) != 2 || !proxyConns[0].isClosed() || proxyConns[1].isClosed() {
			t.Fatal("the unanswered CONNECT was not closed, or the tunnel was")
		}
		mu.Unlock()
		c.Close()
	})
}

// End to end through the source runtime's fetch broker, on real sockets: the IPv4 answer never connects, the
// IPv6 one leads to a local server, and the read succeeds well inside the guest's budget.
func TestRuntimeFetchSkipsSilentAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	var mu sync.Mutex
	var dialed []string
	p := &Proxy{
		Resolve: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("2001:db8::1")}, nil
		},
		Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			if addr == "192.0.2.1:80" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(srv.URL, "http://"))
		},
	}
	p.Register("a", Rule{Hosts: []string{"news.example.com"}})
	raw, _ := json.Marshal(map[string]any{"url": "http://news.example.com/", "method": "GET", "headers": map[string]string{}, "body": ""})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, "POST", RuntimeFetchPath, bytes.NewReader(raw))
	w := httptest.NewRecorder()
	began := time.Now()
	p.handler("a").ServeHTTP(w, r)
	took := time.Since(began)
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("after %s: %d %q", took, w.Code, w.Body.String())
	}
	if took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(dialed, []string{"192.0.2.1:80", "[2001:db8::1]:80"}) {
		t.Fatalf("dialed %v", dialed)
	}
}

// The only address that answers is slow (12 s) and four others never answer: making room for waiting addresses
// must not take its only chance away. Dialed one by one it connects at 12 s; within a 60-second budget it must
// still connect.
func TestRuntimeDialReclaimKeepsSlowOnlyAddress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plan := map[string]fakeHop{"192.0.2.1:443": {after: 12 * time.Second}}
		ips := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"}
		for _, ip := range ips[1:] {
			plan[ip+":443"] = fakeHop{silent: true}
		}
		f := &fakeNet{t: t, plan: plan}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		began := time.Now()
		c, err := racingProxy(f, ips...).runtimeDial(ctx, "news.example.com", "443", false)
		if err != nil {
			t.Fatalf("after %s: %v", time.Since(began), err)
		}
		defer c.Close()
		if got := c.(*fakeConn).addr; got != "192.0.2.1:443" {
			t.Fatalf("won by %s", got)
		}
		// Canceled at 10 s to make room, it is tried again at 10.25 s and answers 12 s later.
		if took, want := time.Since(began), raceReclaimAfter+raceAttemptDelay+12*time.Second; took != want {
			t.Fatalf("connected after %s, want %s", took, want)
		}
		if f.peak > raceInFlight {
			t.Fatalf("%d attempts open at once", f.peak)
		}
		synctest.Wait()
		if n := f.openNow(); n != 0 {
			t.Fatalf("%d attempts still open", n)
		}
	})
}

// Attempts that end with their own timeout at the request's deadline race the deadline itself; whichever the dial
// sees first, it reports the deadline, keeping each address's cause.
func TestRuntimeDialDeadlineWinsOverAttemptTimeouts(t *testing.T) {
	for range 50 {
		synctest.Test(t, func(t *testing.T) {
			plan := map[string]fakeHop{}
			ips := []string{"192.0.2.1", "2001:db8::1", "192.0.2.2"}
			for _, ip := range ips {
				plan[net.JoinHostPort(ip, "443")] = fakeHop{timeout: true}
			}
			f := &fakeNet{t: t, plan: plan}
			ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
			defer cancel()
			_, err := racingProxy(f, ips...).runtimeDial(ctx, "news.example.com", "443", false)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%v is not the deadline", err)
			}
			synctest.Wait()
		})
	}
}
