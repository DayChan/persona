package link

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// NodeDownMarker is in the error a computer's link reports when its resident node is not running (nodeMissing,
// listen.go): the hub reads it as error_code node_service_down (hub/machine_trust.go machineErrorCode). An older link
// says it the same way, so it is kept as it is.
const NodeDownMarker = "常驻 node 没在运行"

// NodeDownSelf comes right before NodeDownMarker in nodeMissing: the link's own computer. The hub swaps it for the
// computer's name on the machine's page (devices nodeUnavailable, #4080).
const NodeDownSelf = "本机的"

// nodeMissing says the resident node is not answering at target, and how this computer runs it (docs/operations/link.md "Installing
// the node service"): the hub shows it on the machine's page.
func nodeMissing(goos, target string) error {
	how := "让 personad node --attach 一直跑"
	switch goos {
	case "windows":
		how = "persona-link install --personad <personad.exe 的路径> 装成登录时启动的计划任务（docs/operations/windows.md）"
	case "darwin":
		how = "菜单栏 App「Persona Link」里的「安装本机 node 服务」，或者 persona-link node install"
	case "linux":
		how = "persona-link node install（systemd 用户服务）"
	}
	return fmt.Errorf("%s%s（%s 连不上）：%s", NodeDownSelf, NodeDownMarker, target, how)
}

// These are settings, not measurements.
const (
	stagger     = 300 * time.Millisecond // between starting one candidate and the next (docs/operations/link.md "Route selection")
	dialTimeout = 8 * time.Second        // one candidate: TCP, TLS and the WebSocket handshake
	checkPing   = 3 * time.Second        // a ping after the network changed
	refusedWait = 5 * time.Minute        // after the hub refused the credential, unless link.json changes
	replaceWait = 30 * time.Second       // after another persona-link of this machine took over
)

// errPairedAgain: link.json holds another credential than the link's (paired again meanwhile): nothing of this link's
// is written into it.
var errPairedAgain = errors.New("paired again meanwhile")

var (
	errPin    = errors.New("TLS 公钥指纹和配对时的不一样，不是那台 hub")
	errRedial = errors.New("手动重连")
)

// Status is what persona-link reports: Run hands one to Options.Status every time something changes, and
// cmd/persona-link prints each as a JSON line for the menu bar app.
type Status struct {
	// State: unpaired | connecting | connected | waiting | refused; passive mode: unpaired | listening (paired, no
	// stream from the hub open) | connected (one is) | waiting (cannot listen).
	State string `json:"state"`
	// Mode: active (this computer dials the hub) | passive (the hub dials it).
	Mode    string `json:"mode,omitempty"`
	Machine string `json:"machine,omitempty"`
	// HubName: what the hub calls itself, once a hub that says it has (link.json keeps it). MeshHubs, while
	// connected: how many hubs its mesh has, itself included. Both missing with older hubs.
	HubName  string `json:"hub_name,omitempty"`
	MeshHubs int    `json:"mesh_hubs,omitempty"`
	// MachineName: what the hub calls this computer, once a hub that says it has (link.json keeps it); missing with
	// older hubs, which show Machine (its id) instead.
	MachineName string `json:"machine_name,omitempty"`
	// Rename: how the last rename asked of this persona-link went (RequestRename waits for it by its ID).
	Rename *RenameResult `json:"rename,omitempty"`
	// Pending, while connected: the hub waits for the user to confirm this computer ("It's mine" in the app);
	// missing once confirmed, and with older hubs.
	Pending bool `json:"pending,omitempty"`
	// connected: the hub URL in use, its kind (lan | ipv6 | name | ipv4) and since when.
	Address string     `json:"address,omitempty"`
	Kind    string     `json:"kind,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	// Trust: "system" when the hub is checked with the system's CAs and host name (it sits behind an HTTPS reverse
	// proxy, Config.Trust); empty when it is pinned.
	Trust string `json:"trust,omitempty"`
	// connected: whether the hub got through to the resident node (online | offline | unknown), and why not.
	Node      string `json:"node,omitempty"`
	NodeError string `json:"node_error,omitempty"`
	// waiting, refused: why the last attempt or link failed, and when the next attempt is.
	Error string     `json:"error,omitempty"`
	Retry *time.Time `json:"retry,omitempty"`
	// The candidates, best first; never the credential.
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	// Attention is the hub's latest "Needs you" summary (attention.go), as it sent it: active mode while this link is
	// up, passive mode while the paired hub keeps coming in. Missing before the first one, and with older hubs.
	Attention *Attention `json:"attention,omitempty"`
	// Passive mode: where it listens; this computer's addresses the hub may use (best first, host:port); when the hub
	// last came in and from where; until when a pairing code from `pair --listen` is good. Connected: Address is the
	// hub's address and Since when its stream opened.
	Listen      string     `json:"listen,omitempty"`
	Addrs       []string   `json:"addrs,omitempty"`
	LastSeen    *time.Time `json:"last_seen,omitempty"`
	From        string     `json:"from,omitempty"`
	CodeExpires *time.Time `json:"code_expires,omitempty"`
}

// Options are what Run needs from its caller.
type Options struct {
	Path    string // link.json
	Target  string // the resident node's socket
	Version string
	Status  func(Status)
	// Check: look at the link now (the network changed). Redial: drop it and connect again.
	Check, Redial <-chan struct{}
	Logf          func(string, ...any)
	// PingEvery, PingTimeout replace the defaults (tests). A hub that says its own heartbeat (endpoints, PingHeader) is
	// followed on that link or stream instead.
	PingEvery, PingTimeout time.Duration
	// limiter replaces passive mode's (tests).
	limiter *authLimiter
}

// httpError is a refused handshake: 401 / 403 mean the credential is no good, 429 that the hub wants a pause.
type httpError struct {
	status int
	retry  time.Duration
}

func (e *httpError) Error() string { return "HTTP " + strconv.Itoa(e.status) }

func handshakeError(resp *http.Response, err error) error {
	if resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
		return err
	}
	he := &httpError{status: resp.StatusCode}
	if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
		he.retry = time.Duration(s) * time.Second
	}
	return he
}

// backoff: exponential from 1 s to a minute, each wait randomized down to half so links that broke together do not
// come back together.
type backoff struct{ n int }

func (b *backoff) next() time.Duration {
	d := min(time.Second<<b.n, time.Minute)
	if d < time.Minute {
		b.n++
	}
	return d/2 + rand.N(d/2+1)
}

// reporter keeps the status and hands every change to Options.Status, saying which mode it is about.
type reporter struct {
	mu   sync.Mutex
	st   Status
	out  func(Status)
	mode string
}

// switchTo starts reporting about mode.
func (r *reporter) switchTo(mode string) {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

func (r *reporter) set(change func(*Status)) {
	r.mu.Lock()
	change(&r.st)
	r.st.Mode = r.mode
	st := r.st
	r.mu.Unlock()
	if r.out != nil {
		r.out(st)
	}
}

// Run keeps this computer reachable from the hub until ctx ends: in active mode by keeping the link to the hub up,
// in passive mode by listening for it (listen.go). It follows link.json: switched to the other mode (persona-link
// mode, pair, pair --listen), it changes over within a second or two.
func Run(ctx context.Context, o Options) error {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.PingEvery <= 0 || o.PingTimeout <= 0 {
		o.PingEvery, o.PingTimeout = PingEvery, PingTimeout
	}
	rep := &reporter{out: o.Status}
	for ctx.Err() == nil {
		mode := currentMode(o.Path)
		mctx, cancel := context.WithCancel(ctx)
		go watchMode(mctx, o.Path, mode, cancel)
		if mode == ModePassive {
			runPassive(mctx, o, rep)
		} else {
			runActive(mctx, o, rep)
		}
		cancel()
		if ctx.Err() == nil {
			o.Logf("mode: %s → %s", mode, currentMode(o.Path))
		}
	}
	return nil
}

func currentMode(path string) string {
	c, err := Read(path)
	if err != nil {
		return ModeActive
	}
	return c.CurrentMode()
}

// modeCheck is how often Run looks whether link.json switched modes (a setting).
const modeCheck = time.Second

// watchMode ends the mode being run (cancel) once link.json says another.
func watchMode(ctx context.Context, path, mode string, cancel context.CancelFunc) {
	stamp := func() time.Time {
		if fi, err := os.Stat(path); err == nil {
			return fi.ModTime()
		}
		return time.Time{}
	}
	was := stamp()
	if currentMode(path) != mode {
		cancel()
		return
	}
	t := time.NewTicker(modeCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := stamp()
			if now.Equal(was) {
				continue
			}
			was = now
			if currentMode(path) != mode {
				cancel()
				return
			}
		}
	}
}

// runActive keeps the link to the hub up until ctx ends.
func runActive(ctx context.Context, o Options, rep *reporter) {
	rep.switchTo(ModeActive)
	var b backoff
	for ctx.Err() == nil {
		c, err := Load(o.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				rep.set(func(s *Status) { *s = Status{State: "unpaired"} })
			} else {
				rep.set(func(s *Status) { *s = Status{State: "waiting", Error: err.Error()} })
			}
			o.wait(ctx, time.Hour)
			continue
		}
		rep.set(func(s *Status) {
			*s = Status{State: "connecting", Machine: c.Machine, HubName: c.HubName, MachineName: c.MachineName, Trust: trustOf(c),
				Endpoints: ranked(c.Endpoints)}
		})
		l, err := dial(ctx, c)
		var end error
		state := "waiting"
		if err != nil {
			end = err
		} else {
			start := time.Now()
			end = l.serve(ctx, c, o, rep)
			if time.Since(start) > time.Minute {
				b = backoff{}
			}
		}
		if ctx.Err() != nil {
			return
		}
		wait := b.next()
		var he *httpError
		switch {
		case errors.Is(end, errRedial):
			b, wait = backoff{}, 0
		case errors.As(end, &he) && (he.status == http.StatusUnauthorized || he.status == http.StatusForbidden):
			state, wait = "refused", refusedWait
			end = errors.New("hub 不认这台的凭据（可能撤销了）：重新配对")
		case errors.As(end, &he) && he.status == http.StatusTooManyRequests:
			wait = max(wait, he.retry)
		case websocket.CloseStatus(end) == CloseReplaced:
			wait = replaceWait
			end = errors.New("这台电脑在别处又连了一条链路（同一个凭据开了两个 Persona Link？）")
		case websocket.CloseStatus(end) == CloseRevoked:
			state, wait = "refused", refusedWait
			end = errors.New("凭据被撤销了：重新配对")
		}
		retry := time.Now().Add(wait)
		o.Logf("link: %v; next attempt in %s", end, wait.Round(time.Second))
		rep.set(func(s *Status) {
			*s = Status{State: state, Machine: c.Machine, HubName: c.HubName, MachineName: c.MachineName, Error: summary(end), Retry: &retry,
				Trust: trustOf(c), Endpoints: ranked(c.Endpoints)}
		})
		o.wait(ctx, wait)
	}
}

// wait sleeps up to d, less if the network changed, a reconnect was asked for or link.json changed (paired again,
// an address added).
func (o Options) wait(ctx context.Context, d time.Duration) {
	stamp := func() time.Time {
		if fi, err := os.Stat(o.Path); err == nil {
			return fi.ModTime()
		}
		return time.Time{}
	}
	was := stamp()
	timer := time.NewTimer(d)
	defer timer.Stop()
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-o.Check:
			return
		case <-o.Redial:
			return
		case <-poll.C:
			if !stamp().Equal(was) {
				return
			}
		}
	}
}
