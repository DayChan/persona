// Package relay is the push relay (personad relay, docs/operations/push-relay.md): it holds the app's APNs key and pushes for
// hubs that have none. Phones register their device token and get a grant; a hub pushes with the grant and a payload
// sealed to the phone's key (pushcrypto), which the relay cannot read. The relay writes the aps envelope itself, so a
// grant cannot be used to show arbitrary text. Live Activity updates are the exception: iOS reads their content-state
// directly, so they pass in the clear (a decision recorded in the doc).
package relay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/pushcrypto"
	"github.com/nwverse/persona/server/internal/relayproto"
)

// Config is ~/.persona/relay.json.
type Config struct {
	Listen     string      `json:"listen"`
	APNs       apns.Config `json:"apns"`
	Topics     []string    `json:"topics"`      // bundle ids the relay pushes for
	SecretPath string      `json:"secret_path"` // seals grants; created on first run
	// ProxySecret: behind the site's Worker (site/worker.mjs), which sends it as X-Relay-Proxy-Secret. Set, a request
	// without it is refused (nobody can go around the Worker) and X-Forwarded-For names the client for rate limits.
	ProxySecret string `json:"proxy_secret,omitempty"`
	// APNsBase replaces Apple's host: only for the end-to-end script (scripts/test/relay_sim_e2e.sh).
	APNsBase string `json:"apns_base,omitempty"`
}

// Sender is what the relay needs from APNs (apns.Client; a fake in tests).
type Sender interface {
	Send(ctx context.Context, r apns.Request) (apns.Result, error)
}

const (
	maxBody    = 8 << 10
	grantAAD   = compat.GrantAAD
	pushPerMin = 120
	pushBurst  = 30
	regPerMin  = 20
	regBurst   = 10
	// Fallback is what the phone shows when it cannot open the payload (no key yet, timeout, garbage, too old).
	FallbackTitle = "Persona"
	FallbackBody  = "有新消息"
)

// Server serves the relay API.
type Server struct {
	apns        Sender
	aead        cipher.AEAD
	topics      map[string]bool
	proxySecret string
	logf        func(string, ...any)
	now         func() time.Time

	pushLimit, regLimit *limiter
}

// New builds a server around a sender and the grant secret (32 bytes).
func New(cfg Config, sender Sender, secret []byte, logf func(string, ...any)) (*Server, error) {
	if len(secret) != 32 {
		return nil, errors.New("relay secret must be 32 bytes")
	}
	if len(cfg.Topics) == 0 {
		return nil, errors.New("relay.json: topics is empty")
	}
	block, _ := aes.NewCipher(secret)
	aead, _ := cipher.NewGCM(block)
	s := &Server{apns: sender, aead: aead, topics: map[string]bool{}, proxySecret: cfg.ProxySecret, logf: logf, now: time.Now,
		pushLimit: newLimiter(pushPerMin, pushBurst), regLimit: newLimiter(regPerMin, regBurst)}
	for _, t := range cfg.Topics {
		if strings.TrimSpace(t) == "" {
			return nil, errors.New("relay.json: topics has an empty entry")
		}
		s.topics[t] = true
	}
	return s, nil
}

// grant is what a grant seals: where to push.
type grant struct {
	Token string `json:"t"`
	Env   string `json:"e"`
	Topic string `json:"p"`
	At    int64  `json:"i"`
}

func (s *Server) seal(g grant) string {
	plain, _ := json.Marshal(g)
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(grantAAD)))
}

func (s *Server) open(tok string) (grant, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	n := s.aead.NonceSize()
	if err != nil || len(raw) < n+s.aead.Overhead() {
		return grant{}, errors.New("bad grant")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(grantAAD))
	if err != nil {
		plain, err = s.aead.Open(nil, raw[:n], raw[n:], []byte("persona relay grant v1"))
	}
	if err != nil {
		return grant{}, errors.New("bad grant")
	}
	var g grant
	if err := json.Unmarshal(plain, &g); err != nil {
		return grant{}, errors.New("bad grant")
	}
	return g, nil
}

var hexToken = lazyre.New(`^[0-9a-fA-F]{32,400}$`)
var hexCollapse = lazyre.New(`^[0-9a-f]{1,64}$`)

// Handler is the relay's HTTP API under /relay/v1.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "apns": s.apns != nil})
	})
	mux.HandleFunc("POST /relay/v1/register", s.register)
	mux.HandleFunc("POST /relay/v1/push", s.push)
	if s.proxySecret == "" {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Relay-Proxy-Secret")), []byte(s.proxySecret)) != 1 {
			writeErr(w, http.StatusForbidden, "not through the proxy")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.regLimit.allow("ip:"+s.clientIP(r), s.now()) {
		writeErr(w, http.StatusTooManyRequests, "too many registrations")
		return
	}
	var body relayproto.RegisterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	switch {
	case !hexToken.MatchString(body.APNSToken):
		writeErr(w, http.StatusBadRequest, "apns_token must be hex")
	case body.Env != "production" && body.Env != "sandbox":
		writeErr(w, http.StatusBadRequest, "env must be production or sandbox")
	case !s.topics[body.Topic]:
		writeErr(w, http.StatusForbidden, "topic not served by this relay")
	default:
		g := grant{Token: strings.ToLower(body.APNSToken), Env: body.Env, Topic: body.Topic, At: s.now().Unix()}
		s.logf("register %s (%s, %s)", short(g.Token), g.Env, g.Topic)
		writeJSON(w, 200, map[string]any{"grant": s.seal(g)})
	}
}

// PushRequest is what a hub posts (hub/push_send.go builds it).
type PushRequest = relayproto.PushRequest

// liveActivityKeys are the aps keys a Live Activity push may carry (hub/liveactivity.go liveActivityPayload).
var liveActivityKeys = map[string]bool{
	"timestamp": true, "event": true, "content-state": true, "stale-date": true, "dismissal-date": true,
	"relevance-score": true, "attributes-type": true, "attributes": true, "alert": true,
}

func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	g, err := s.open(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !s.topics[g.Topic] {
		writeErr(w, http.StatusForbidden, "topic not served by this relay")
		return
	}
	if !s.pushLimit.allow("dev:"+g.Token, s.now()) {
		writeErr(w, http.StatusTooManyRequests, "too many pushes for this device")
		return
	}
	var req PushRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	out, status, msg := s.envelope(g, req)
	if msg != "" {
		writeErr(w, status, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	sent := s.now()
	res, err := s.apns.Send(ctx, out)
	if err != nil {
		s.logf("push %s %s: %v", req.Type, short(out.Token), err)
		writeErr(w, http.StatusBadGateway, "apns: "+err.Error())
		return
	}
	s.logf("push %s %s: %d %s in %d ms (%d bytes)", req.Type, short(out.Token), res.Status, res.Reason,
		s.now().Sub(sent).Milliseconds(), len(out.Body))
	switch {
	case res.Status == 200:
		writeJSON(w, 200, map[string]any{"ok": true})
	case res.Gone():
		writeJSON(w, http.StatusGone, map[string]any{"reason": res.Reason})
	default:
		writeJSON(w, apnsFailStatus(res.Status), map[string]any{"error": fmt.Sprintf("apns %d", res.Status), "reason": res.Reason})
	}
}

// apnsFailStatus is the relay's status for an APNs answer that is neither 200 nor a gone token. APNs' trouble that
// may pass (429, 5xx) is 502, which the hub tries again; any other answer is a rejection that sending again will not
// change (BadCollapseId, InvalidProviderToken, PayloadTooLarge, ...), so it is 422, which the hub does not try again.
// Never 401: the hub reads that as a stale grant to renew.
func apnsFailStatus(status int) int {
	if status == http.StatusTooManyRequests || status >= 500 {
		return http.StatusBadGateway
	}
	return http.StatusUnprocessableEntity
}

// envelope turns a hub's request into the APNs request: the relay writes the aps itself.
func (s *Server) envelope(g grant, req PushRequest) (apns.Request, int, string) {
	out := apns.Request{Token: g.Token, Env: g.Env, Topic: g.Topic, PushType: req.Type, Priority: req.Priority}
	if out.Priority != "5" {
		out.Priority = "10"
	}
	if req.CollapseID != "" {
		if !hexCollapse.MatchString(req.CollapseID) {
			return out, 400, "collapse_id must be lowercase hex"
		}
		out.CollapseID = req.CollapseID
	}
	var payload map[string]any
	switch req.Type {
	case "alert", "background":
		raw, err := base64.StdEncoding.DecodeString(req.Sealed)
		if err != nil || len(raw) < pushcrypto.MinSealed || len(req.Sealed) > relayproto.MaxSealed {
			return out, 400, fmt.Sprintf("sealed must be a base64 HPKE payload of at most %d characters", relayproto.MaxSealed)
		}
		if req.Type == "alert" {
			payload = map[string]any{"aps": map[string]any{
				"alert":           map[string]any{"title": FallbackTitle, "body": FallbackBody},
				"mutable-content": 1, "sound": "default",
			}, "e": req.Sealed}
		} else {
			out.Priority = "5" // APNs rejects background pushes at 10
			payload = map[string]any{"aps": map[string]any{"content-available": 1}, "e": req.Sealed}
		}
	case "liveactivity":
		if !hexToken.MatchString(req.Token) {
			return out, 400, "token must be hex"
		}
		if req.APS["event"] == nil || req.APS["content-state"] == nil {
			return out, 400, "aps needs event and content-state"
		}
		for k := range req.APS {
			if !liveActivityKeys[k] {
				return out, 400, "aps key not allowed: " + k
			}
		}
		// Only a push-to-start carries an alert (hub/liveactivity.go startPayload); updates and ends show none.
		if _, ok := req.APS["alert"]; ok && req.APS["event"] != "start" {
			return out, 400, "aps alert is only for event start"
		}
		out.Token = strings.ToLower(req.Token)
		payload = map[string]any{"aps": req.APS}
	default:
		return out, 400, "type must be alert, background or liveactivity"
	}
	out.Body, _ = json.Marshal(payload)
	if len(out.Body) > 4096 {
		return out, 413, "payload over 4 KB"
	}
	return out, 0, ""
}

func (s *Server) clientIP(r *http.Request) string {
	// Only behind the Worker (it proved it with the secret): the first X-Forwarded-For entry is the client it saw.
	if s.proxySecret != "" {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// short names a device token in logs without writing it out.
func short(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:4])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// limiter is a token bucket per key.
type limiter struct {
	mu      sync.Mutex
	perMin  float64
	burst   float64
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(perMin, burst int) *limiter {
	return &limiter{perMin: float64(perMin), burst: float64(burst), buckets: map[string]*bucket{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) > 50000 { // forget the full ones
			for k, v := range l.buckets {
				if now.Sub(v.at) > time.Duration(l.burst/l.perMin*float64(time.Minute)) {
					delete(l.buckets, k)
				}
			}
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Minutes()*l.perMin)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Run is `personad relay`: reads the config, loads the key and the secret, serves until ctx ends.
func Run(ctx context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("relay config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("relay config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:18670"
	}
	client, err := apns.New(cfg.APNs)
	if err != nil {
		return fmt.Errorf("relay apns key: %w", err)
	}
	client.Base = cfg.APNsBase
	secret, err := loadSecret(expand(cfg.SecretPath, filepath.Join(filepath.Dir(path), "relay.secret")))
	if err != nil {
		return err
	}
	srv, err := New(cfg, client, secret, log.Printf)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("relay listen: %w", err)
	}
	hs := newHTTPServer(srv.Handler())
	log.Printf("relay on %s (key %s, topics %v)", cfg.Listen, cfg.APNs.KeyID, cfg.Topics)
	return serve(ctx, ln, hs)
}

// Connection limits of the relay's listener, which may face the internet directly (proxy_secret unset). A request body
// is at most maxBody, so readTimeout (headers and body) cuts off a client that stalls its body; idleTimeout closes
// keep-alive connections that go quiet. There is no WriteTimeout: the answers are small JSON, and push bounds its APNs
// send to 15 s itself. Variables so tests can shrink them.
var (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 2 * time.Minute
)

// newHTTPServer is the relay's http.Server around h, with the connection limits above.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, IdleTimeout: idleTimeout}
}

// shutdownGrace is how long serve lets in-flight requests (an APNs send can take up to 15 s) finish after ctx ends.
var shutdownGrace = 5 * time.Second

// serve runs hs on ln until ctx ends, then drains in-flight requests for up to shutdownGrace. It returns only after
// the drain, because Serve itself returns ErrServerClosed as soon as Shutdown starts and the caller exits on return.
func serve(ctx context.Context, ln net.Listener, hs *http.Server) error {
	done := make(chan error, 1)
	go func() { done <- hs.Serve(ln) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	sh, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	err := hs.Shutdown(sh)
	if err != nil {
		_ = hs.Close()
	}
	<-done
	if err != nil {
		return fmt.Errorf("relay shutdown: %w", err)
	}
	return nil
}

func expand(p, def string) string {
	if p == "" {
		return def
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}

// loadSecret reads the grant secret, creating it (0600) the first time.
func loadSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		_, _ = rand.Read(b)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, fmt.Errorf("relay secret: %w", err)
		}
		log.Printf("relay: created %s; losing it makes every phone register again", path)
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("relay secret: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("relay secret %s: want 32 bytes, got %d", path, len(b))
	}
	return b, nil
}
