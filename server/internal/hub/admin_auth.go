package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The administrator's password and browser sessions (docs/operations/admin.md "Admin password and sessions").

// adminStore is what the admin UI keeps in Postgres (internal/db/admin.go); the hub's Store stays as it is.
type adminStore interface {
	AdminPasswordHash(ctx context.Context) (string, error)
	SetAdminPassword(ctx context.Context, hash string, ifUnset bool) (bool, error)
	SetAdminPasswordSignOut(ctx context.Context, hash, keep string) error
	CreateAdminSession(ctx context.Context, s db.AdminSession) error
	AdminSessionByHash(ctx context.Context, hash string) (*db.AdminSession, error)
	DeleteAdminSession(ctx context.Context, hash string) error
}

func (h *Hub) adminDB() (adminStore, bool) {
	s, ok := h.store.(adminStore)
	return s, ok && h.store != nil
}

// Argon2id at the low end of OWASP's recommendation (19 MiB, 2 passes): a sign-in costs one of these, and at most
// adminHashSlots run at once, so a burst of sign-ins cannot take more memory than that.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16

	adminPasswordMin   = 8
	adminPasswordBytes = 256
)

var adminHashSlots = make(chan struct{}, 2)

func argonKey(password string, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	adminHashSlots <- struct{}{}
	defer func() { <-adminHashSlots }()
	return argon2.IDKey([]byte(password), salt, t, m, p, n)
}

// hashAdminPassword is a PHC string: $argon2id$v=19$m=…,t=…,p=…$<salt>$<key>.
func hashAdminPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argonKey(password, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// checkAdminPassword compares in constant time; the parameters come from the stored hash, so stronger ones later
// keep old hashes working.
func checkAdminPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<20 || t > 16 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argonKey(password, salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// adminPasswordProblem is why a new password is refused, nil when it is fine.
func adminPasswordProblem(password string) error {
	switch {
	case utf8.RuneCountInString(password) < adminPasswordMin:
		return i18n.Err("hub-admin_auth.password_too_short", i18n.Args{"min": adminPasswordMin})
	case len(password) > adminPasswordBytes:
		return i18n.Err("hub-admin_auth.password_too_long", i18n.Args{"max": adminPasswordBytes})
	case strings.TrimSpace(password) == "":
		return i18n.Err("hub-admin_auth.password_blank")
	}
	return nil
}

// --- sessions ---------------------------------------------------------------------------------------------------

const adminCookiePath = "/api/v1/admin/"

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// adminSession is the signed-in session of this request; without one it answers 401 login_required.
func (h *Hub) adminSession(w http.ResponseWriter, r *http.Request) (*db.AdminSession, bool) {
	s, err := h.currentAdminSession(r)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return nil, false
	}
	if s == nil {
		writeErrCode(w, http.StatusUnauthorized, "login_required", errors.New("sign in to the admin UI"))
		return nil, false
	}
	return s, true
}

func (h *Hub) currentAdminSession(r *http.Request) (*db.AdminSession, error) {
	c, err := compatibleAdminCookie(r)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, nil
	}
	store, ok := h.adminDB()
	if !ok {
		return nil, errors.New("no database")
	}
	return store.AdminSessionByHash(r.Context(), sha256Hex(c.Value))
}

func (h *Hub) startAdminSession(w http.ResponseWriter, r *http.Request, store adminStore) (*db.AdminSession, error) {
	value := randomToken()
	hours := h.adminSessionHours()
	s := db.AdminSession{
		IDHash: sha256Hex(value), CSRF: randomToken(), ExpiresAt: h.adminNow().Add(time.Duration(hours) * time.Hour),
		Source: maskedSource(r), UserAgent: truncateRunes(r.UserAgent(), 200),
	}
	if err := store.CreateAdminSession(r.Context(), s); err != nil {
		return nil, err
	}
	for _, name := range compat.AdminCookieNames(r.TLS != nil) {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: value, Path: adminCookiePath, MaxAge: hours * 3600,
			HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
		})
	}
	return &s, nil
}

func clearAdminCookie(w http.ResponseWriter, r *http.Request) {
	for _, name := range compat.AdminCookieNames(r.TLS != nil) {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: adminCookiePath, MaxAge: -1,
			HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	}
}

func (h *Hub) adminSessionHours() int {
	h.adminInit()
	h.admin.mu.Lock()
	defer h.admin.mu.Unlock()
	return h.admin.file.sessionLifetime()
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// --- sign-in limits ---------------------------------------------------------------------------------------------

// adminRetry is how long this source must wait before trying a password or setup code again (0: go ahead). The same
// thresholds as the app's tokens (security.auth_*), but its own table, and LAN and loopback sources count too: a
// password is far easier to guess than a token, and loopback may be a reverse proxy relaying the internet (the hub's
// own owner, locked out for a while, still has personad hub admin on the machine). Sources outside the LAN also share
// one budget of ten times that, against many addresses guessing together.
func (h *Hub) adminRetry(r *http.Request) time.Duration {
	now := h.adminNow()
	if d := h.admin.failures.retry(authSourceKey(r), now, h.cfg.Security); d > 0 {
		return d
	}
	if !h.adminLANSource(r) {
		return h.admin.publicFailures.retry("public", now, h.publicAdminLimits())
	}
	return 0
}

func (h *Hub) adminFailed(r *http.Request) {
	now := h.adminNow()
	h.admin.failures.fail(authSourceKey(r), now, h.cfg.Security)
	if !h.adminLANSource(r) {
		h.admin.publicFailures.fail("public", now, h.publicAdminLimits())
	}
}

func (h *Hub) adminLANSource(r *http.Request) bool {
	return h.addrInScope(adminScope{access: accessLAN, trusted: h.adminScope().trusted}, remoteIP(r))
}

func (h *Hub) publicAdminLimits() SecurityConfig {
	s := h.cfg.Security
	s.AuthFailures = s.failures() * 10
	return s
}

func (h *Hub) adminRateLimited(w http.ResponseWriter, r *http.Request) bool {
	retry := h.adminRetry(r)
	if retry <= 0 {
		return false
	}
	retryAfter(w, retry)
	seconds := max(1, int((retry+time.Second-1)/time.Second))
	writeErrorBody(w, http.StatusTooManyRequests, map[string]any{
		"error": "too many wrong passwords", "code": "rate_limited", "retry_after": seconds,
	})
	return true
}

// --- routes -----------------------------------------------------------------------------------------------------

type adminStateView struct {
	PasswordSet bool `json:"password_set"`
	LoggedIn    bool `json:"logged_in"`
	CanSetup    bool `json:"can_setup"`
	// SetupCodeRequired: first-run setup from loopback needs the one-time code (admin_setupcode.go).
	SetupCodeRequired bool       `json:"setup_code_required"`
	CSRF              string     `json:"csrf,omitempty"`
	HubName           string     `json:"hub_name"`
	Version           string     `json:"version"`
	SessionExpiresAt  *time.Time `json:"session_expires_at,omitempty"`
}

func (h *Hub) adminStateView(r *http.Request, store adminStore, s *db.AdminSession) (adminStateView, error) {
	hash, err := store.AdminPasswordHash(r.Context())
	if err != nil {
		return adminStateView{}, err
	}
	v := adminStateView{PasswordSet: hash != "", CanSetup: hash == "" && h.adminCanSetup(r), HubName: h.hubName(), Version: h.version}
	v.SetupCodeRequired = v.CanSetup && h.setupCodeRequired(r)
	if s != nil && hash != "" {
		v.LoggedIn, v.CSRF = true, s.CSRF
		at := s.ExpiresAt
		v.SessionExpiresAt = &at
	}
	return v, nil
}

func (h *Hub) adminAuthRoutes(open, closed *http.ServeMux) {
	withStore := func(fn func(w http.ResponseWriter, r *http.Request, store adminStore)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			store, ok := h.adminDB()
			if !ok {
				writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
				return
			}
			fn(w, r, store)
		}
	}
	open.HandleFunc("GET /api/v1/admin/state", withStore(func(w http.ResponseWriter, r *http.Request, store adminStore) {
		s, err := h.currentAdminSession(r)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		v, err := h.adminStateView(r, store, s)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Someone is at the setup page from loopback and the startup code has run out: log a new one to read.
		if v.SetupCodeRequired && !h.setupCodeLive() {
			h.logSetupCode(h.newSetupCode())
		}
		writeJSON(w, v)
	}))
	open.HandleFunc("POST /api/v1/admin/setup", withStore(func(w http.ResponseWriter, r *http.Request, store adminStore) {
		var in struct {
			Password  string `json:"password"`
			SetupCode string `json:"setup_code"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		if !h.adminCanSetup(r) {
			writeErrCode(w, http.StatusForbidden, "setup_local_only",
				errors.New("set the first password from the hub's own network, or run personad hub admin reset-password on the hub's machine"))
			return
		}
		if h.setupCodeRequired(r) {
			if h.adminRateLimited(w, r) {
				return
			}
			if strings.TrimSpace(in.SetupCode) == "" {
				writeErrCode(w, http.StatusForbidden, "setup_code_required",
					errors.New("the first password needs the setup code: the admin link personad hub setup printed, the hub log, or personad hub admin setup-code"))
				return
			}
			if problem := adminPasswordProblem(in.Password); problem != nil { // before spending the code
				writeErrCode(w, http.StatusBadRequest, "weak_password", problem)
				return
			}
			if !h.useSetupCode(in.SetupCode) {
				h.adminFailed(r)
				h.logf("admin: wrong or expired setup code from %s", maskedSource(r))
				writeErrCode(w, http.StatusUnauthorized, "bad_setup_code",
					errors.New("wrong or expired setup code: personad hub admin setup-code prints a new one"))
				return
			}
		}
		if problem := adminPasswordProblem(in.Password); problem != nil {
			writeErrCode(w, http.StatusBadRequest, "weak_password", problem)
			return
		}
		hash, err := hashAdminPassword(in.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		set, err := store.SetAdminPassword(r.Context(), hash, true)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !set {
			writeErrCode(w, http.StatusConflict, "already_set", errors.New("the administrator password is already set"))
			return
		}
		h.logf("admin: administrator password set (source %s)", maskedSource(r))
		h.adminSignedIn(w, r, store)
	}))
	open.HandleFunc("POST /api/v1/admin/login", withStore(func(w http.ResponseWriter, r *http.Request, store adminStore) {
		if h.adminRateLimited(w, r) {
			return
		}
		var in struct {
			Password string `json:"password"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		hash, err := store.AdminPasswordHash(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if hash == "" {
			writeErrCode(w, http.StatusConflict, "not_set", errors.New("no administrator password yet: set one first"))
			return
		}
		if len(in.Password) > adminPasswordBytes || !checkAdminPassword(hash, in.Password) {
			h.adminFailed(r)
			h.logf("admin: wrong password from %s", maskedSource(r))
			writeErrCode(w, http.StatusUnauthorized, "bad_password", errors.New("wrong password"))
			return
		}
		h.adminSignedIn(w, r, store)
	}))
	open.HandleFunc("POST /api/v1/admin/logout", withStore(func(w http.ResponseWriter, r *http.Request, store adminStore) {
		// The browser forgets the cookie either way; when the row could not be deleted, say so,
		// because a copy of the cookie still signs in until the session expires.
		c, err := compatibleAdminCookie(r)
		if err == nil && c.Value != "" {
			if err := store.DeleteAdminSession(r.Context(), sha256Hex(c.Value)); err != nil {
				h.logf("admin: sign-out from %s could not end the session: %v", maskedSource(r), err)
				clearAdminCookie(w, r)
				writeErrCode(w, http.StatusInternalServerError, "logout_failed", errors.New("signed out here, but the session could not be ended on the hub: it may still be valid"))
				return
			}
		}
		clearAdminCookie(w, r)
		writeJSON(w, map[string]any{"ok": true})
	}))
	h.setupCodeRoute(open)
	// `personad hub admin access` on this machine: read admin.access again after writing it (admin_cli.go).
	open.HandleFunc("POST /api/v1/admin/reload", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !remoteIP(r).IsLoopback() || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(h.cfg.Token)) != 1 {
			writeErrCode(w, http.StatusForbidden, "forbidden", errors.New("reload needs the master token over loopback"))
			return
		}
		if err := h.adminReload(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "access": h.adminScope().access})
	})
	closed.HandleFunc("POST /api/v1/admin/password", withStore(func(w http.ResponseWriter, r *http.Request, store adminStore) {
		if h.adminRateLimited(w, r) {
			return
		}
		var in struct {
			Current string `json:"current"`
			New     string `json:"new"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		hash, err := store.AdminPasswordHash(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if len(in.Current) > adminPasswordBytes || !checkAdminPassword(hash, in.Current) {
			h.adminFailed(r)
			writeErrCode(w, http.StatusUnauthorized, "bad_password", errors.New("the current password is wrong"))
			return
		}
		if problem := adminPasswordProblem(in.New); problem != nil {
			writeErrCode(w, http.StatusBadRequest, "weak_password", problem)
			return
		}
		next, err := hashAdminPassword(in.New)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		c, cookieErr := compatibleAdminCookie(r)
		if cookieErr != nil || c == nil {
			writeErr(w, http.StatusUnauthorized, i18n.Err("hub-compatibility.admin_session_missing"))
			return
		}
		if err := store.SetAdminPasswordSignOut(r.Context(), next, sha256Hex(c.Value)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.logf("admin: administrator password changed; other sessions signed out")
		writeJSON(w, map[string]any{"ok": true})
	}))
}

func (h *Hub) adminSignedIn(w http.ResponseWriter, r *http.Request, store adminStore) {
	s, err := h.startAdminSession(w, r, store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	v, err := h.adminStateView(r, store, s)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, v)
}
