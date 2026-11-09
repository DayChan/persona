package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Upstream limits (docs/assistant/model-config.md "Upstream rate limits"). A relay answering "the account's quota is used up" (HTTP 429
// account_quota_exhausted) or "the account is busy" (HTTP 503 account_concurrency_full) used to be tried again item
// after item: 429 requests and as many log lines in 15 minutes. Now such an answer holds the connection (address +
// key, whichever use calls it): requests are put off without being sent until the pause is over, then one goes
// first to find out. Callers take a PausedError as "not now", leave the work queued and log nothing; the gate logs
// one line when it holds and one when it lets go.

const (
	ReasonQuota = "quota" // the account's quota is used up
	ReasonBusy  = "busy"  // the account is at its concurrency limit, or the upstream is overloaded
)

var (
	quotaPause = 30 * time.Minute // without a Retry-After
	busyFirst  = 5 * time.Second  // the first busy pause; each busy answer in a row doubles it
	busyMost   = 5 * time.Minute
	afterMost  = 24 * time.Hour // a Retry-After longer than this is taken as this
	growAfter  = 10             // successes under a cap before it is raised by one
	capLift    = 8              // a cap raised past this is lifted
	now        = time.Now
	logf       = log.Printf
)

// Pause is a connection held back: nothing is sent to it until Until (then one request goes first).
type Pause struct {
	Reason string    `json:"reason"` // quota | busy
	Until  time.Time `json:"until"`
}

// Text is the pause the way the app shows it, e.g. "quota used up, retry in 23 minutes".
func (p Pause) Text() string {
	what := "并发已满"
	if p.Reason == ReasonQuota {
		what = "额度用完"
	}
	d := p.Until.Sub(now())
	switch {
	case d <= 0:
		return what + "，稍后重试"
	case d < time.Minute:
		return fmt.Sprintf("%s，%d 秒后重试", what, int((d+time.Second-1)/time.Second))
	default:
		return fmt.Sprintf("%s，%d 分钟后重试", what, int((d+time.Minute-1)/time.Minute))
	}
}

// PausedError: the request was not sent because its connection is held (or it is the answer that held it). The
// work is put off, not failed: keep it queued and do not log it.
type PausedError struct{ Pause }

func (e *PausedError) Error() string { return e.Text() }

// IsPaused tells a put-off request from a failed one.
func IsPaused(err error) bool {
	var p *PausedError
	return errors.As(err, &p)
}

// Held is the pause on cfg's connection, nil when requests go out.
func Held(cfg Config) *Pause {
	if !cfg.Configured() {
		return nil
	}
	key, err := New(cfg).key()
	if err != nil {
		return nil
	}
	v, ok := gates.Load(gateKey(cfg.BaseURL, key))
	if !ok {
		return nil
	}
	g := v.(*gate)
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return nil
	}
	p := g.pause
	return &p
}

// gate is one connection's state; every client of the same address and key shares it.
type gate struct {
	host string // for the log

	mu       sync.Mutex
	held     bool      // from the answer that paused it to the next success
	pause    Pause     // while held
	since    time.Time // when it was held
	putOff   int       // requests not sent while held
	probing  bool      // the pause is over and one request is finding out
	strikes  int       // busy answers in a row
	cap      int       // requests in flight at most, 0 for no cap
	inflight int
	fine     int           // successes since the cap last changed
	wake     chan struct{} // closed on every change, for requests waiting under the cap
}

var gates sync.Map // gateKey → *gate

func gateKey(baseURL, key string) string {
	sum := sha256.Sum256([]byte(key))
	return strings.TrimRight(baseURL, "/") + "#" + hex.EncodeToString(sum[:8])
}

func gateFor(baseURL, key string) *gate {
	k := gateKey(baseURL, key)
	if g, ok := gates.Load(k); ok {
		return g.(*gate)
	}
	host := baseURL
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	g, _ := gates.LoadOrStore(k, &gate{host: host, wake: make(chan struct{})})
	return g.(*gate)
}

// admit lets a request go out, waits for a slot under the cap, or puts it off while the connection is held. probe:
// this request is the one finding out whether the pause is over.
func (g *gate) admit(ctx context.Context) (probe bool, err error) {
	for {
		g.mu.Lock()
		if g.held && (now().Before(g.pause.Until) || g.probing) {
			g.putOff++
			p := g.pause
			g.mu.Unlock()
			return false, &PausedError{p}
		}
		if g.cap > 0 && g.inflight >= g.cap {
			wake := g.wake
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				// Out of time waiting for a slot: nothing was sent, so it is put off like a paused one.
				return false, &PausedError{Pause{Reason: ReasonBusy, Until: now()}}
			case <-wake:
			}
			continue
		}
		probe = g.held
		g.probing = g.probing || probe
		g.inflight++
		g.mu.Unlock()
		return probe, nil
	}
}

// done records how a request that went out ended (answered: the upstream said 200) and returns the error for the
// caller: an answer that holds the connection becomes a PausedError.
func (g *gate) done(probe, answered bool, err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	defer g.changed()
	if probe {
		g.probing = false
	}
	if answered {
		switch {
		case g.held && probe:
			logf("ai: %s answers again after %s; %d request(s) were put off", g.host, now().Sub(g.since).Round(time.Second), g.putOff)
			g.held, g.putOff, g.strikes = false, 0, 0
		case !g.held:
			g.strikes = 0
			if g.cap > 0 {
				if g.fine++; g.fine >= growAfter {
					g.cap, g.fine = g.cap+1, 0
					if g.cap > capLift {
						g.cap = 0
					}
				}
			}
		}
		return err
	}
	reason, after, detail := classify(err)
	if reason == "" {
		return err
	}
	// A request sent before the hold that comes back with the same answer changes nothing: only the one that holds
	// the connection and the probes count.
	counts := !g.held || probe
	pause := after
	if reason == ReasonQuota && pause <= 0 {
		pause = quotaPause
	}
	if reason == ReasonBusy {
		if counts {
			g.strikes++
			if g.cap == 0 {
				g.cap = max(1, (g.inflight+1)/2) // the requests in flight when it said busy, this one included
			} else {
				g.cap = max(1, g.cap/2)
			}
			g.fine = 0
		}
		pause = max(pause, min(busyFirst<<min(max(g.strikes-1, 0), 16), busyMost))
	}
	until := now().Add(pause)
	if !g.held {
		g.held, g.since, g.putOff = true, now(), 0
		g.pause = Pause{Reason: reason, Until: until}
		what := "is busy"
		if reason == ReasonQuota {
			what = "is out of quota"
		}
		logf("ai: %s %s (%s); requests are put off until %s", g.host, what, detail, until.Format("15:04:05"))
	} else {
		if until.After(g.pause.Until) {
			g.pause.Until = until
		}
		if reason == ReasonQuota {
			g.pause.Reason = ReasonQuota
		}
	}
	return &PausedError{g.pause}
}

func (g *gate) changed() {
	close(g.wake)
	g.wake = make(chan struct{})
}

// httpError is an answer other than 200.
type httpError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func newHTTPError(resp *http.Response, raw []byte) *httpError {
	return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(raw[:min(len(raw), 300)])),
		retryAfter: retryAfter(resp.Header.Get("Retry-After"))}
}

func (e *httpError) Error() string { return fmt.Sprintf("ai: HTTP %d: %s", e.status, e.body) }

// Transient: the same request tried again may get an answer: the call ran out of time, the connection failed, or an
// upstream answered 5xx or 408 (often a gateway in front of the model). A request turned down (other 4xx), a model
// that isn't set up, a pause (IsPaused: wait for the gate, don't hurry it) and a reply's own problems are not.
func Transient(err error) bool {
	if err == nil || IsPaused(err) {
		return false
	}
	var h *httpError
	if errors.As(err, &h) {
		return h.status >= 500 || h.status == http.StatusRequestTimeout
	}
	var u *url.Error // the request got no answer at all (a deadline, a refused or reset connection)
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &u)
}

// retryAfter reads Retry-After: seconds or an HTTP date.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now())
	}
	return min(max(d, 0), afterMost)
}

var quotaWords = lazyre.New(`quota|usage limit|exhaust|insufficient|credit|billing`)

// classify: whether err is an answer that holds the connection, why, for how long the upstream asked, and what it
// said (for the log line).
func classify(err error) (reason string, after time.Duration, detail string) {
	var h *httpError
	if !errors.As(err, &h) {
		return "", 0, ""
	}
	body := strings.ToLower(h.body)
	switch {
	case h.status != http.StatusTooManyRequests && h.status != http.StatusServiceUnavailable && h.status != 529:
		return "", 0, ""
	case strings.Contains(body, "concurrency"):
		reason = ReasonBusy
	case h.status == http.StatusTooManyRequests && quotaWords.MatchString(body):
		reason = ReasonQuota
	default: // a plain rate limit, 503 or 529 (overloaded)
		reason = ReasonBusy
	}
	detail = fmt.Sprintf("HTTP %d", h.status)
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(h.body), &e) == nil && (e.Error.Code != "" || e.Error.Message != "") {
		code := e.Error.Code
		if code == "" {
			code = e.Error.Type
		}
		detail += " " + strings.TrimSpace(code+": "+e.Error.Message)
	} else if h.body != "" {
		detail += " " + h.body[:min(len(h.body), 120)]
	}
	return reason, h.retryAfter, detail
}
