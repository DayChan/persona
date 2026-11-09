package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
)

type pairStore struct {
	tokenStore
	pairs map[string]db.PairToken
}

func newPairStore() *pairStore {
	return &pairStore{tokenStore: tokenStore{rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}, pairs: map[string]db.PairToken{}}
}

func (s *pairStore) CreatePairToken(_ context.Context, hash string, expires time.Time, machine string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairs[hash] = db.PairToken{Hash: hash, CreatedAt: time.Now(), ExpiresAt: expires, MachineID: machine}
	return nil
}

func (s *pairStore) PairTokenByHash(_ context.Context, hash string) (*db.PairToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pairs[hash]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

func (s *pairStore) RedeemPairToken(_ context.Context, hash string, device db.DeviceToken, tokenHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pairs[hash]
	if !ok || p.UsedAt != nil || !time.Now().Before(p.ExpiresAt) {
		return false, nil
	}
	now := time.Now()
	p.UsedAt, p.DeviceID = &now, device.ID
	s.pairs[hash], s.rows[tokenHash] = p, device
	return true, nil
}

func (s *pairStore) RevokeDeviceToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, device := range s.rows {
		if device.ID == id && !s.rev[id] {
			s.rev[id] = true
			now := time.Now()
			for hash, p := range s.pairs {
				if p.DeviceID == id {
					p.ExpiresAt = now
					s.pairs[hash] = p
				}
			}
			return true, nil
		}
	}
	return false, nil
}

// RevokeMachineCredentials is db.DB's: the machine's live credentials revoked, its unused codes expired.
func (s *pairStore) RevokeMachineCredentials(_ context.Context, machine string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for hash, p := range s.pairs {
		if p.MachineID == machine && p.UsedAt == nil && now.Before(p.ExpiresAt) {
			p.ExpiresAt = now
			s.pairs[hash] = p
		}
	}
	var ids []string
	for _, device := range s.rows {
		if device.MachineID == machine && device.Role == roleLink && !s.rev[device.ID] {
			s.rev[device.ID] = true
			ids = append(ids, device.ID)
		}
	}
	return ids, nil
}

func newPairCode(t *testing.T, h *Hub) pairCode {
	t.Helper()
	w := securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", h.cfg.Token, "", "127.0.0.1:1", false)
	requireCode(t, w, 200, "")
	var p pairCode
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPairTokenLifecycle(t *testing.T) {
	h := newTestHub(t)
	s := newPairStore()
	h.store = s
	h.cfg.PublicURL = "http://example.com:18660"
	p := newPairCode(t, h)
	if !strings.HasPrefix(p.Token, "jp_") || len(p.Token) != 67 || p.Token == h.cfg.Token {
		t.Fatal("bad pairing credential")
	}
	if left := time.Until(p.ExpiresAt); left > pairLifetime || left < pairLifetime-time.Second {
		t.Fatal("bad expiry")
	}
	if len(s.pairs) != 1 || s.pairs[hashToken(p.Token)].Hash != hashToken(p.Token) {
		t.Fatal("pair credential not persisted by hash")
	}
	u, err := url.Parse(p.PairURL)
	if err != nil || u.Query().Get("token") != p.Token || u.Query().Get("url") != h.cfg.PublicURL {
		t.Fatal("bad pairing URL")
	}
	for _, key := range []string{"hub", "fp", "tls", "alt"} {
		if u.Query().Has(key) {
			t.Fatalf("empty %s must be omitted", key)
		}
	}
	if who, ok := h.authenticate(context.Background(), p.Token); !ok || who.ID == "" || who.PairHash == "" {
		t.Fatal("pair token impersonated master")
	}
	// A new hub instance uses the persisted credential, including over public TLS.
	restarted := newTestHub(t)
	restarted.store = s
	mux := restarted.routes()
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "198.51.100.1:1", true), 200, "")
	requireCode(t, securityRequest(mux, "POST", "/api/v1/devices", p.Token, `{"device_id":"phone","name":"小王的手机"}`, "127.0.0.1:1", false), 200, "")
	// Old iOS connects WS before exchanging credentials, and may still use the token during grace.
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws?token="+p.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	code, device := call(t, mux, "POST", "/api/v1/auth/tokens", p.Token, `{"name":"小王的手机","kind":"phone"}`)
	if code != 200 || device["role"] != roleFull {
		t.Fatalf("exchange %d %v", code, device)
	}
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), 200, "")
	requireCode(t, securityRequest(mux, "POST", "/api/v1/auth/tokens", p.Token, `{}`, "127.0.0.1:1", false), 401, "")
	requireCode(t, securityRequest(mux, "POST", "/api/v1/auth/pair-codes", p.Token, "", "127.0.0.1:1", false), 403, "")
	s.mu.Lock()
	pt := s.pairs[hashToken(p.Token)]
	if pt.DeviceID != device["id"] || pt.UsedAt == nil {
		t.Fatal("redemption not recorded")
	}
	past := time.Now().Add(-pairGrace - time.Second)
	pt.UsedAt = &past
	s.pairs[pt.Hash] = pt
	s.mu.Unlock()
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), 401, "")
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", device["token"].(string), "", "198.51.100.1:1", true), 200, "")
}

func TestPairTokenRevocationEndsGrace(t *testing.T) {
	h := newTestHub(t)
	s := newPairStore()
	h.store = s
	p := newPairCode(t, h)
	unrelated := newPairCode(t, h)
	mux := h.routes()
	code, device := call(t, mux, "POST", "/api/v1/auth/tokens", p.Token, `{"kind":"phone"}`)
	if code != 200 {
		t.Fatalf("exchange %d %v", code, device)
	}
	pt, err := s.PairTokenByHash(context.Background(), hashToken(p.Token))
	if err != nil || pt == nil || pt.UsedAt == nil || !time.Now().Before(pt.UsedAt.Add(pairGrace)) {
		t.Fatalf("expected redeemed token within grace: %+v %v", pt, err)
	}
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), 200, "")
	requireCode(t, securityRequest(mux, "DELETE", "/api/v1/auth/tokens/"+device["id"].(string), h.cfg.Token, "", "127.0.0.1:1", false), 200, "")

	// Revocation persists across hub restarts, without waiting for the redemption grace to elapse.
	restarted := newTestHub(t)
	restarted.store = s
	for _, hub := range []*Hub{h, restarted} {
		for _, path := range []string{"/api/v1/machines", "/api/v1/ws", "/api/v1/auth/tokens", "/api/v1/auth/pair-codes"} {
			method := "GET"
			if strings.HasPrefix(path, "/api/v1/auth/") {
				method = "POST"
			}
			requireCode(t, securityRequest(hub.routes(), method, path, p.Token, `{}`, "127.0.0.1:1", false), 401, "")
		}
		requireCode(t, securityRequest(hub.routes(), "GET", "/api/v1/machines", unrelated.Token, "", "127.0.0.1:1", false), 200, "")
	}
}

func TestPairTokenExpiredAndOneUse(t *testing.T) {
	h := newTestHub(t)
	s := newPairStore()
	h.store = s
	p := newPairCode(t, h)
	s.mu.Lock()
	pt := s.pairs[hashToken(p.Token)]
	pt.ExpiresAt = time.Now().Add(-time.Second)
	s.pairs[pt.Hash] = pt
	s.mu.Unlock()
	for _, path := range []string{"/api/v1/machines", "/api/v1/ws", "/api/v1/auth/tokens"} {
		method := "GET"
		if strings.HasSuffix(path, "tokens") {
			method = "POST"
		}
		requireCode(t, securityRequest(h.routes(), method, path, p.Token, `{}`, "127.0.0.1:1", false), 401, "")
	}
	p = newPairCode(t, h)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	mux := h.routes()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- securityRequest(mux, "POST", "/api/v1/auth/tokens", p.Token, `{}`, "127.0.0.1:1", false).Code
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 401 {
			t.Fatalf("status %d", status)
		}
	}
	if success != 1 || len(s.rows) != 1 {
		t.Fatalf("issued %d credentials", success)
	}
}

func TestPairCodesRequireLoopbackMasterOrAFullDevice(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	h.cfg.Security.TrustedCIDRs = []string{"192.0.2.0/24"}
	_, watch := call(t, h.routes(), "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"kind":"watch"}`)
	pair := newPairCode(t, h).Token
	before := len(h.store.(*pairStore).pairs)
	for _, tc := range []struct {
		remote, token, body string
		tls                 bool
	}{
		// The master token only over loopback.
		{"192.0.2.1:1", h.cfg.Token, "", false},
		{"198.51.100.1:1", h.cfg.Token, "", true},
		// Not the watch's answer-only token, nor a pairing code making another one.
		{"127.0.0.1:1", watch["token"].(string), "", false},
		{"198.51.100.1:1", pair, "", true},
	} {
		requireCode(t, securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", tc.token, tc.body, tc.remote, tc.tls), 403, "")
	}
	if len(h.store.(*pairStore).pairs) != before {
		t.Fatal("forbidden request created pairing code")
	}
	// IPv6 loopback is accepted too.
	requireCode(t, securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", h.cfg.Token, "", "[::1]:1", false), 200, "")
}

// A signed-in device with full access pairs another one from anywhere it can reach the hub (#2731): an app code
// with its QR code, and with {"web"} the link that signs a browser in.
func TestPairCodesFromADevice(t *testing.T) {
	h := newTestHub(t)
	s := newPairStore()
	h.store = s
	h.cfg.PublicURL = "https://hub.example.com"
	_, phone := call(t, h.routes(), "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"我的手机","kind":"phone"}`)
	tok := phone["token"].(string)

	w := securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", tok, `{"web":"https://hub.example.com/settings"}`, "198.51.100.1:1", true)
	requireCode(t, w, 200, "")
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("a pairing code must not be cached")
	}
	var reply struct {
		pairCode
		QR     qrImage `json:"qr"`
		WebURL string  `json:"web_url"`
		WebQR  qrImage `json:"web_qr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply.Token, "jp_") || s.pairs[hashToken(reply.Token)].Hash == "" {
		t.Fatal("no one-time pairing code made")
	}
	if !strings.HasPrefix(reply.PairURL, "persona://pair?") || reply.QR.Size == 0 || len(reply.QR.Rows) != reply.QR.Size {
		t.Fatalf("app pairing link / QR: %q %d", reply.PairURL, reply.QR.Size)
	}
	if reply.WebURL != "https://hub.example.com/settings#token="+reply.Token || len(reply.WebQR.Rows) == 0 {
		t.Fatalf("web sign-in link %q", reply.WebURL)
	}
	// The code pairs a new device once, like one from `personad hub pair`.
	code, device := call(t, h.routes(), "POST", "/api/v1/auth/tokens", reply.Token, `{"name":"另一台电脑","kind":"web"}`)
	if code != 200 || !strings.HasPrefix(device["token"].(string), "jd_") {
		t.Fatalf("exchange %d %v", code, device)
	}
	requireCode(t, securityRequest(h.routes(), "POST", "/api/v1/auth/tokens", reply.Token, `{}`, "198.51.100.1:1", true), 401, "")

	// No body: just the app code.
	w = securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", tok, "", "198.51.100.1:1", true)
	requireCode(t, w, 200, "")
	if strings.Contains(w.Body.String(), "web_url") {
		t.Fatal("web sign-in link without being asked")
	}

	pairs := len(s.pairs)
	// A device makes no code for a computer already confirmed (machines_add_test.go has the ones it may make); web
	// must be a page address for the token's #.
	h.cfg.Machines = append(h.cfg.Machines, MachineConfig{ID: "laptop", Kind: kindLink})
	requireCode(t, securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", tok, `{"machine":"laptop"}`, "198.51.100.1:1", true), 409, "not_pending")
	for _, web := range []string{"javascript:alert(1)", "/settings", "https://hub.example.com/#x", "https://me:pw@hub.example.com/"} {
		body, _ := json.Marshal(map[string]string{"web": web})
		requireCode(t, securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", tok, string(body), "198.51.100.1:1", true), 400, "")
	}
	if len(s.pairs) != pairs {
		t.Fatal("refused request created pairing code")
	}
}

func TestPairContextExpires(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	ctx, cancel := h.pairContext(context.Background(), principal{PairHash: "unknown", PairExpires: time.Now().Add(20 * time.Millisecond)})
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("pair WS context did not expire")
	}
}

func TestPairGraceDoesNotExtendExpiry(t *testing.T) {
	now := time.Now()
	used := now.Add(-pairGrace)
	p := &db.PairToken{ExpiresAt: now.Add(time.Hour), UsedAt: &used}
	if !pairExpires(p).Equal(now) {
		t.Fatal("redemption grace ignored")
	}
	p.ExpiresAt = now.Add(-time.Second)
	if !pairExpires(p).Equal(p.ExpiresAt) {
		t.Fatal("grace extended original expiry")
	}
}

func TestPairRepeatedRedemptionIsRateLimited(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	h.cfg.Security.AuthFailures = 1
	p := newPairCode(t, h)
	mux := h.routes()
	requireCode(t, securityRequest(mux, "POST", "/api/v1/auth/tokens", p.Token, `{}`, "198.51.100.1:1", true), 200, "")
	requireCode(t, securityRequest(mux, "POST", "/api/v1/auth/tokens", p.Token, `{}`, "198.51.100.1:1", true), 401, "")
	requireCode(t, securityRequest(mux, "POST", "/api/v1/auth/tokens", p.Token, `{}`, "198.51.100.1:1", true), 429, "rate_limited")
}

// Exercise the real migration and transaction through the HTTP flow, using only the lane's test database.
func TestPairTokenDatabaseFlow(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := db.Open(ctx, dsn, db.HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := newTestHub(t)
	h.store = store
	p := newPairCode(t, h)
	if row, err := store.PairTokenByHash(ctx, hashToken(p.Token)); err != nil || row == nil || row.UsedAt != nil {
		t.Fatalf("pair row %v %v", row, err)
	}
	restarted := newTestHub(t)
	restarted.store = store
	mux := restarted.routes()
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), 200, "")
	code, device := call(t, mux, "POST", "/api/v1/auth/tokens", p.Token, `{"name":"小王的手机","kind":"phone"}`)
	if code != 200 {
		t.Fatalf("exchange %d %v", code, device)
	}
	requireCode(t, securityRequest(mux, "POST", "/api/v1/auth/tokens", p.Token, `{}`, "127.0.0.1:1", false), 401, "")
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), 200, "")
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", device["token"].(string), "", "198.51.100.1:1", true), 200, "")
	if ok, err := store.RevokeDeviceToken(ctx, device["id"].(string)); err != nil || !ok {
		t.Fatalf("revoke %v %v", ok, err)
	}
	requireCode(t, securityRequest(mux, "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), 401, "")
}
