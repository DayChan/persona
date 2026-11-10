package voice

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

// credential is the provider's Bearer credential: Key, the first line of KeyFile, or the first line KeyCommand
// prints (run for every conversation, so a credential renewed elsewhere is picked up). None is allowed.
//
// KeyCommand runs in a session of its own: at its 20 s limit the whole group is killed, not just sh. A process it
// leaves running that still holds its output (an agent it starts, cmd & echo token) is not waited for past
// keyCommandWaitDelay once the command has exited; without it Output read the pipe until that process ended, and the
// call start waited with it.
func (p *Provider) credential(ctx context.Context) (string, error) {
	switch {
	case p.Key != "":
		return p.Key, nil
	case p.KeyFile != "":
		raw, err := os.ReadFile(expandHome(p.KeyFile))
		if err != nil {
			return "", fmt.Errorf("%s: key_file: %w", p.ID, err)
		}
		return firstLine(raw), nil
	case p.KeyCommand != "":
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "sh", "-c", p.KeyCommand)
		cmd.Stderr = &stderr
		cmd.SysProcAttr = osproc.SessionGroupAttr()
		cmd.Cancel = func() error { return osproc.StopGroup(cmd.Process.Pid, true) }
		cmd.WaitDelay = keyCommandWaitDelay
		out, err := cmd.Output()
		// ErrWaitDelay: the command exited 0 and only a process it left held the pipe; what it printed counts.
		if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
			return "", fmt.Errorf("%s: key_command: %v %s", p.ID, err, strings.TrimSpace(stderr.String()))
		}
		if key := firstLine(out); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("%s: key_command printed nothing", p.ID)
	}
	return "", nil
}

// keyCommandWaitDelay is how long credential keeps reading KeyCommand's output after the command has exited.
const keyCommandWaitDelay = time.Second

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(line)
}

// httpClient reaches the provider: directly, or through its SSH machine. GET /models, GET /voices and the session's
// WebSocket use it. One client per SSH machine: its lists reuse one `ssh -W` connection, closed after 90 s idle (a
// client per call left every list's ssh process open for good). An upgraded WebSocket is taken out of the pool.
func (p *Provider) httpClient() *http.Client {
	if p.SSH == "" {
		return directClient
	}
	if c, ok := sshClients.Load(p.SSH); ok {
		return c.(*http.Client)
	}
	c, _ := sshClients.LoadOrStore(p.SSH, &http.Client{Transport: &http.Transport{
		DialContext:         sshDialer(p.SSH),
		TLSHandshakeTimeout: 15 * time.Second,
		IdleConnTimeout:     90 * time.Second,
	}})
	return c.(*http.Client)
}

// sshClients is, per SSH machine, the client that reaches providers through it.
var sshClients sync.Map

// directClient reaches providers without an SSH machine: HTTPS through dialTLS; plain HTTP, and HTTPS through a proxy
// set in the environment, as Go's default transport does. One shared pool.
var directClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	DialTLSContext:        dialTLS,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	ExpectContinueTimeout: time.Second,
}}

// How long one address of an upstream gets for TCP and the TLS handshake before the next is tried, and the last one
// (so a slow host with a single address is not cut short). Variables so tests can shorten them.
var (
	tlsAddrBudget = 4 * time.Second
	tlsLastBudget = 15 * time.Second
)

// lookupUpstream resolves an upstream's host to the addresses to dial (ip:port), in the resolver's order. A variable
// so tests can point a name at local listeners.
var lookupUpstream = func(ctx context.Context, host, port string) ([]string, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	addrs := make([]string, len(ips))
	for i, ip := range ips {
		addrs[i] = net.JoinHostPort(ip.Unmap().String(), port)
	}
	return addrs, nil
}

// upstreamRoots verifies upstream certificates: nil is the system's pool. Tests trust their own server.
var upstreamRoots *x509.CertPool

// lastGood is, per host:port, the address whose handshake last completed: tried first next time.
var lastGood sync.Map

// dialTLS opens a TLS connection to addr (host:port) address by address: the first whose TLS handshake completes in
// time wins (docs/lessons/voice.md "An address that accepts TCP but never answers TLS"). Go's dialer falls back only
// when TCP fails, and orders a ULA IPv6 address after IPv4: when the IPv4 address takes the connection but never
// answers TLS, a working IPv6 address was never tried. ALPN offers only http/1.1, which the WebSocket upgrade needs.
func dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs := []string{addr}
	if _, err := netip.ParseAddr(host); err != nil {
		if addrs, err = lookupUpstream(ctx, host, port); err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("%s: no addresses", host)
		}
		if last, ok := lastGood.Load(addr); ok {
			addrs = moveFirst(addrs, last.(string))
		}
	}
	conf := &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}, RootCAs: upstreamRoots}
	failed := make([]string, 0, len(addrs))
	for i, a := range addrs {
		budget := tlsAddrBudget
		if i == len(addrs)-1 {
			budget = tlsLastBudget
		}
		conn, err := handshake(ctx, network, a, conf, budget)
		if err == nil {
			lastGood.Store(addr, a)
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("TLS to %s: %w", addr, ctx.Err())
		}
		failed = append(failed, a+": "+err.Error())
	}
	lastGood.Delete(addr)
	return nil, fmt.Errorf("TLS to %s failed on every address: %s", addr, strings.Join(failed, "; "))
}

// handshake connects to addr and completes TLS within budget.
func handshake(ctx context.Context, network, addr string, conf *tls.Config, budget time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	raw, err := (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("no connection within %s", budget)
		}
		var op *net.OpError
		if errors.As(err, &op) {
			err = op.Err
		}
		return nil, err
	}
	conn := tls.Client(raw, conf)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("no TLS handshake within %s", budget)
		}
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}
	return conn, nil
}

// moveFirst returns addrs with a moved to the front, if it is there.
func moveFirst(addrs []string, a string) []string {
	i := slices.Index(addrs, a)
	if i <= 0 {
		return addrs
	}
	out := append([]string{a}, addrs[:i]...)
	return append(out, addrs[i+1:]...)
}

// sshDialer opens a connection to addr from another machine: `ssh -W addr target`, its stdio as the connection.
func sshDialer(target string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		// Not tied to ctx: the connection outlives the dial. A failed ssh shows on the first read, with its stderr
		// (for TLS the server only talks after the client, so the dial cannot wait for a first byte).
		cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15",
			"-W", addr, target)
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		stderr := &tailBuffer{}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("ssh %s: %w", target, err)
		}
		return &cmdConn{cmd: cmd, in: in, out: bufio.NewReader(out), stderr: stderr, addr: addr}, nil
	}
}

// cmdConn is a net.Conn over a command's stdin and stdout.
type cmdConn struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *tailBuffer
	addr   string
	once   sync.Once
}

func (c *cmdConn) Read(b []byte) (int, error) {
	n, err := c.out.Read(b)
	if err == io.EOF && n == 0 {
		if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
			return 0, fmt.Errorf("ssh: %s", msg)
		}
	}
	return n, err
}

func (c *cmdConn) Write(b []byte) (int, error) { return c.in.Write(b) }

func (c *cmdConn) Close() error {
	c.once.Do(func() {
		_ = c.in.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
	})
	return nil
}

func (c *cmdConn) LocalAddr() net.Addr              { return sshAddr("ssh") }
func (c *cmdConn) RemoteAddr() net.Addr             { return sshAddr(c.addr) }
func (c *cmdConn) SetDeadline(time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(time.Time) error { return nil }

type sshAddr string

func (a sshAddr) Network() string { return "ssh" }
func (a sshAddr) String() string  { return string(a) }

// tailBuffer keeps the last 2 KB written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

var errNoProvider = errors.New("no such provider")

// errNoUpstream: no upstream is set up yet (Config.Setup says what is missing).
var errNoUpstream = errors.New("no realtime voice service is set up")
