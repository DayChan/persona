// Package herdrcore talks to herdr-core (`hc`), the terminal runtime behind the Herdr desktop app: JSON-RPC 2.0, one
// message per line on its unix socket. It is a different program from classic herdr (internal/herdr): its own socket,
// methods (terminal.*), and environment (HC_TERMINAL_ID). See docs/plans/plan-orca-support.md "herdr-core host".
//
// herdr-core being absent (not installed, not running) is normal: calls fail fast with ErrNoSocket and log at most
// one line per episode.
package herdrcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Client is one herdr-core server, reached at Socket.
type Client struct {
	// Socket is herdr-core's API socket (SocketPath); empty: herdr-core is not there.
	Socket string
	// Log gets one line when calls start failing and one when they work again; a socket that was never there is not
	// logged. nil: silent.
	Log func(format string, args ...any)

	seq   atomic.Int64
	trips episode
}

// New is a client of the herdr-core server this user's hc would talk to.
func New() *Client { return &Client{Socket: SocketPath()} }

// SocketPath is where herdr-core listens, by hc's own rules (paths.rs): $HC_SOCKET, else `socket` in
// $XDG_CONFIG_HOME/herdr-core/config.toml, else <session>.sock in the user's runtime directory
// ($XDG_RUNTIME_DIR/herdr-core, else /run/user/<uid>/herdr-core when that exists), else in the state directory
// ($XDG_STATE_HOME/herdr-core/<session>, ~/.local/state by default — macOS). The session is $HC_SESSION, "default".
func SocketPath() string {
	if p := os.Getenv("HC_SOCKET"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	xdg := func(v, fallback string) string {
		if d := os.Getenv(v); d != "" {
			return d
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, fallback)
	}
	if cfg := xdg("XDG_CONFIG_HOME", ".config"); cfg != "" {
		if p := configSocket(filepath.Join(cfg, "herdr-core", "config.toml")); p != "" {
			return p
		}
	}
	session := strings.TrimSpace(os.Getenv("HC_SESSION"))
	if session == "" {
		session = "default"
	}
	if strings.ContainsAny(session, `/\:*?"<>|`) || session == "." || session == ".." {
		return ""
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		if d := "/run/user/" + strconv.Itoa(os.Getuid()); isDir(d) {
			run = d
		}
	}
	if run != "" {
		return filepath.Join(run, "herdr-core", session+".sock")
	}
	state := xdg("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if state == "" {
		return ""
	}
	return filepath.Join(state, "herdr-core", session, session+".sock")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// configSocket reads the top-level `socket = "…"` of hc's config.toml ("" when there is none). Only that one key is
// read, so this is a line scan, not a TOML parser: a key inside a [table] does not count.
func configSocket(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return ""
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "socket" {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 && !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, "'") {
			v = strings.TrimSpace(v[:i])
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				return v[1 : 1+end]
			}
		}
		return ""
	}
	return ""
}

// Error is herdr-core's JSON-RPC error; Kind is its stable data.kind ("not_found", "blocked", "unknown_method"…).
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Kind    string `json:"-"`
}

func (e *Error) Error() string {
	if e.Kind != "" && e.Kind != e.Message {
		return "herdr-core: " + e.Kind + ": " + e.Message
	}
	return "herdr-core: " + e.Message
}

// IsKind: err is herdr-core's error of that kind.
func IsKind(err error, kind string) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}

// ErrNoSocket: herdr-core's socket is not there or refuses (not installed, not running).
var ErrNoSocket = errors.New("herdr-core is not running")

const (
	// dialTimeout bounds connecting: the socket is local, so anything slower means a wedged server.
	dialTimeout = time.Second
	// callTimeout bounds a quick call (read, keys, get).
	callTimeout = 5 * time.Second
	// listTimeout bounds terminal.list and doc.get, taken with every refresh of the node's sessions: a wedged core must
	// not hold the refresh of the other hosts for long.
	listTimeout = 2 * time.Second
	// SendTimeout bounds terminal.send: it waits for ~3 s without typing at the computer, then a few seconds to see
	// the program start working.
	SendTimeout = 20 * time.Second
	// keysTimeout bounds terminal.keys (the same wait for quiet). Keys herdr-core still holds when it ends are pressed
	// later anyway (it does not drop a call whose caller left): kept short so that window is small.
	keysTimeout = 8 * time.Second
	// killTimeout bounds terminal.kill: the core signals hangup, terminate, then kill, waiting 0.5 + 0.5 + 2 s for the
	// program to go before it answers.
	killTimeout = 10 * time.Second
	// runTimeout bounds terminal.run without waiting: it answers once the program is spawned.
	runTimeout = 10 * time.Second
	maxLine    = 64 << 20
	// heartbeatGap: events.subscribe sends a heartbeat every 15 s; a stream silent for this long is dead.
	heartbeatGap = 45 * time.Second
)

// episode throttles logging of transport failures: not one line per poll.
type episode struct {
	mu       sync.Mutex
	failing  bool
	since    time.Time
	failures int
	seen     bool // it answered once: from then on, its socket going away is worth a line
}

func (c *Client) note(method string, err error) {
	if c.Log == nil {
		return
	}
	e := &c.trips
	e.mu.Lock()
	defer e.mu.Unlock()
	var he *Error
	if err == nil || errors.As(err, &he) { // an answer, even an error one, means it is reachable
		if e.failing {
			c.Log("herdr-core: reachable again after %s (%d failed calls)", time.Since(e.since).Round(time.Second), e.failures)
		}
		e.failing, e.failures, e.seen = false, 0, true
		return
	}
	if errors.Is(err, ErrNoSocket) && !e.seen {
		return // never seen running: herdr-core is just not used on this machine
	}
	e.failures++
	if !e.failing { // one line when an episode starts, one when it ends
		e.failing, e.since = true, time.Now()
		c.Log("herdr-core: %s failed: %v", method, err)
	}
}

// dial connects within dialTimeout (and ctx). A socket file that is not there fails without a dial.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	if c.Socket == "" {
		return nil, ErrNoSocket
	}
	if _, err := os.Stat(c.Socket); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dctx, "unix", c.Socket)
	if err != nil {
		// Only a socket nobody listens on means not running; a core too busy to accept in time still runs.
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
		}
		return nil, fmt.Errorf("herdr-core: %w", err)
	}
	return conn, nil
}

// Call runs one method within timeout and decodes its result into out (nil: ignored).
func (c *Client) Call(ctx context.Context, timeout time.Duration, method string, params, out any) error {
	err := c.callOnce(ctx, timeout, method, params, out)
	if ctx.Err() == nil { // a caller giving up is not herdr-core failing
		c.note(method, err)
	}
	return err
}

func (c *Client) callOnce(ctx context.Context, timeout time.Duration, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	id := c.seq.Add(1)
	if err := writeRequest(conn, id, method, params); err != nil {
		return fmt.Errorf("herdr-core %s: %w", method, err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		line, err := readLine(r, maxLine)
		if err != nil {
			return fmt.Errorf("herdr-core %s: %w", method, err)
		}
		var env response
		if err := json.Unmarshal(line, &env); err != nil {
			return fmt.Errorf("herdr-core %s: decode: %v", method, err)
		}
		if env.ID == nil || string(env.ID) != strconv.FormatInt(id, 10) {
			continue // a notification
		}
		return env.decode(out)
	}
}

func writeRequest(w io.Writer, id int64, method string, params any) error {
	if params == nil {
		params = struct{}{}
	}
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	_, err = w.Write(append(req, '\n'))
	return err
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Kind string `json:"kind"`
		} `json:"data"`
	} `json:"error"`
}

func (r response) decode(out any) error {
	if r.Error != nil {
		return &Error{Code: r.Error.Code, Message: r.Error.Message, Kind: r.Error.Data.Kind}
	}
	if out == nil || len(r.Result) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// readLine reads one newline-terminated line of at most max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, fmt.Errorf("line over %d bytes", max)
		}
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case len(buf) > 0 && errors.Is(err, io.EOF):
			return buf, nil
		default:
			return nil, err
		}
	}
}
