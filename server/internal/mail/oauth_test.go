package mail

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

const testClientID = "1234-abcd.apps.googleusercontent.com"

func newOAuthService(t *testing.T) (*Service, *memRecords) {
	t.Helper()
	recs := newMemRecords()
	s, err := New(t.TempDir(), recs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetGoogleClientID(testClientID)
	s.verify = func(context.Context, *MailAccount) error { return nil }
	return s, recs
}

func fakeIDToken(email string) string {
	enc := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{"email": email, "email_verified": true})
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(claims) + "." + enc([]byte("sig"))
}

// fakeGoogle serves the token endpoint: it checks PKCE against the challenge from start.
func fakeGoogle(t *testing.T, challenge *string, refresh string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != *challenge || r.Form.Get("code") != "the-code" ||
				r.Form.Get("redirect_uri") != "com.googleusercontent.apps.1234-abcd:/oauth2redirect" || r.Form.Has("client_secret") {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-1", "refresh_token": refresh, "expires_in": 3599,
				"scope": "openid https://mail.google.com/ https://www.googleapis.com/auth/userinfo.email", "id_token": fakeIDToken("Me@Gmail.com"),
			})
		case "refresh_token":
			if r.Form.Get("refresh_token") != refresh {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-2", "expires_in": 3599})
		}
	}))
	t.Cleanup(srv.Close)
	old := googleTokenURL
	googleTokenURL = srv.URL
	t.Cleanup(func() { googleTokenURL = old })
}

func startFlow(t *testing.T, s *Service) (state, challenge string) {
	t.Helper()
	st, err := s.StartGoogleOAuth("me@gmail.com")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	u, err := url.Parse(st.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if st.CallbackScheme != "com.googleusercontent.apps.1234-abcd" ||
		q.Get("redirect_uri") != st.CallbackScheme+":/oauth2redirect" ||
		q.Get("client_id") != testClientID || q.Get("code_challenge_method") != "S256" ||
		q.Get("access_type") != "offline" || !strings.Contains(q.Get("scope"), "https://mail.google.com/") ||
		q.Get("state") != st.State || q.Get("login_hint") != "me@gmail.com" {
		t.Fatalf("auth url = %s, scheme = %s", st.AuthURL, st.CallbackScheme)
	}
	return st.State, q.Get("code_challenge")
}

func TestGoogleOAuthNotConfigured(t *testing.T) {
	s, _ := newOAuthService(t)
	s.SetGoogleClientID("")
	if _, err := s.StartGoogleOAuth(""); err == nil || statusFor(err) != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
}

// #2442: the apps learn before the tap whether Google sign-in can start, and start on a hub without a client says, in
// the client's language, who sets it up (google.client_id) and what works meanwhile (an app password).
func TestGoogleOAuthConfig(t *testing.T) {
	s, _ := newOAuthService(t)
	code, body := doJSON(t, s, "GET", "/api/v1/mail/oauth/google/config", "")
	if code != 200 || string(body["configured"]) != "true" {
		t.Fatalf("configured hub: %d %v", code, body)
	}

	s.SetGoogleClientID("  ")
	code, body = doJSON(t, s, "GET", "/api/v1/mail/oauth/google/config", "")
	if code != 200 || string(body["configured"]) != "false" {
		t.Fatalf("hub without a client: %d %v", code, body)
	}
	// Android apps that do not read google/config fall back to the app-password form when the start error contains
	// "not configured", so every language's text keeps that phrase.
	for _, c := range []struct {
		accept string
		ctx    i18n.Lang
		want   string
	}{
		{accept: "zh-Hans", want: "应用专用密码"},
		{accept: "ja", want: "アプリパスワード"},
		{accept: "en", want: "app password"},
		{want: "app password"},
		{accept: "en", ctx: i18n.Ja, want: "アプリパスワード"}, // the signed-in user's language wins
	} {
		r := httptest.NewRequest("POST", "/api/v1/mail/oauth/google/start", nil)
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if c.ctx != "" {
			r = r.WithContext(i18n.With(r.Context(), c.ctx))
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var got struct{ Error, Code string }
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != http.StatusBadRequest || got.Code != "google_not_configured" ||
			!strings.Contains(got.Error, "not configured") || !strings.Contains(got.Error, "google.client_id") ||
			!strings.Contains(got.Error, c.want) {
			t.Fatalf("start without a client, %q/%q: %d %s", c.accept, c.ctx, w.Code, w.Body)
		}
	}
}

func TestGoogleOAuthSignIn(t *testing.T) {
	s, recs := newOAuthService(t)
	var challenge string
	fakeGoogle(t, &challenge, "rt-secret")
	state, ch := startFlow(t, s)
	challenge = ch

	a, err := s.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state, Code: "the-code"})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if a.Email != "me@gmail.com" || a.Auth != AuthOAuth2 || a.Provider != ProviderGmail || a.IMAPHost != "imap.gmail.com" || a.OAuth != nil {
		t.Fatalf("account = %+v", a)
	}
	// The state is single use.
	if _, err := s.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state, Code: "the-code"}); err == nil {
		t.Fatal("state reused")
	}
	if strings.Contains(recs.dump(), "rt-secret") {
		t.Fatal("refresh token stored in plain text")
	}

	// Reload from storage: the credentials decrypt, and the cached access token is used first.
	s2, err := New(s.dir, recs)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer s2.Close()
	got, err := s2.account(a.ID)
	if err != nil || got.OAuth == nil || got.OAuth.RefreshToken != "rt-secret" || got.OAuth.ClientID != testClientID {
		t.Fatalf("reloaded = %+v, %v", got, err)
	}
	if tok, err := s.oauthToken(context.Background(), got); err != nil || tok != "at-1" {
		t.Fatalf("cached token = %q, %v", tok, err)
	}
	if tok, err := s2.oauthToken(context.Background(), got); err != nil || tok != "at-2" {
		t.Fatalf("refreshed token = %q, %v", tok, err)
	}
}

func TestGoogleOAuthSignInAgainKeepsAccount(t *testing.T) {
	s, _ := newOAuthService(t)
	var challenge string
	fakeGoogle(t, &challenge, "rt-1")
	state, ch := startFlow(t, s)
	challenge = ch
	first, err := s.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state, Code: "the-code"})
	if err != nil {
		t.Fatal(err)
	}
	state, challenge = startFlow(t, s)
	second, err := s.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state, Code: "the-code"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("signing in again created a new account: %s vs %s", first.ID, second.ID)
	}
	if accts, _ := s.ListAccounts(); len(accts) != 1 {
		t.Fatalf("accounts = %d", len(accts))
	}
}

func TestGoogleOAuthErrors(t *testing.T) {
	s, _ := newOAuthService(t)
	var challenge string
	fakeGoogle(t, &challenge, "rt")
	if _, err := s.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: "nope", Code: "x"}); err == nil || statusFor(err) != http.StatusBadRequest {
		t.Fatalf("unknown state: %v", err)
	}
	state, _ := startFlow(t, s)
	if _, err := s.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state, Error: "access_denied"}); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied: %v", err)
	}
	// A revoked refresh token asks for a new sign-in.
	_, err := s.accessToken(context.Background(), "", &OAuthCreds{ClientID: testClientID, RefreshToken: "revoked"})
	if err == nil || !strings.Contains(err.Error(), "sign in with Google again") {
		t.Fatalf("revoked: %v", err)
	}
	// OAuth accounts cannot be added through the password endpoint.
	if _, err := s.AddAccount(AddAccountRequest{Email: "me@gmail.com", Auth: AuthOAuth2}); err == nil || statusFor(err) != http.StatusBadRequest {
		t.Fatalf("add oauth2: %v", err)
	}
}

func TestXOAUTH2(t *testing.T) {
	if got := string(xoauth2Payload("me@gmail.com", "tok")); got != "user=me@gmail.com\x01auth=Bearer tok\x01\x01" {
		t.Fatalf("payload = %q", got)
	}
	s, _ := newOAuthService(t)
	s.refreshAccess = func(*OAuthCreds) (string, error) { return "tok", nil }
	c, err := s.smtpAuth(context.Background(), &MailAccount{Auth: AuthOAuth2, Username: "me@gmail.com", OAuth: &OAuthCreds{RefreshToken: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	mech, ir, _ := c.Start()
	if mech != "XOAUTH2" || string(ir) != string(xoauth2Payload("me@gmail.com", "tok")) {
		t.Fatalf("mech %s ir %q", mech, ir)
	}
}

func TestGoogleOAuthHTTP(t *testing.T) {
	s, _ := newOAuthService(t)
	code, body := doJSON(t, s, "POST", "/api/v1/mail/oauth/google/start", `{"login_hint":"me@gmail.com"}`)
	if code != 200 || !strings.Contains(string(body["auth_url"]), "accounts.google.com") || len(body["callback_scheme"]) == 0 {
		t.Fatalf("start = %d %v", code, body)
	}
	code, _ = doJSON(t, s, "POST", "/api/v1/mail/oauth/google/start", "")
	if code != 200 {
		t.Fatalf("start without body = %d", code)
	}
	code, body = doJSON(t, s, "POST", "/api/v1/mail/oauth/google/finish", `{"state":"x","code":"y"}`)
	if code != 400 || len(body["error"]) == 0 {
		t.Fatalf("finish = %d %v", code, body)
	}
}

func TestGoogleOAuthSurvivesRestart(t *testing.T) {
	s, recs := newOAuthService(t)
	var challenge string
	fakeGoogle(t, &challenge, "rt")
	state, ch := startFlow(t, s)
	challenge = ch

	// The hub restarts (redeploy) while the user is on Google's pages.
	s2, err := New(s.dir, recs)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.SetGoogleClientID(testClientID)
	s2.verify = s.verify
	if _, err := s2.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state, Code: "the-code"}); err != nil {
		t.Fatalf("finish after restart: %v", err)
	}
	// A forged or tampered state is rejected.
	if _, err := s2.FinishGoogleOAuth(context.Background(), OAuthFinishRequest{State: state[:len(state)-2] + "AA", Code: "the-code"}); err == nil {
		t.Fatal("tampered state accepted")
	}
}

func TestGmailAPISendAndCheck(t *testing.T) {
	s, _ := newOAuthService(t)
	s.refreshAccess = func(*OAuthCreds) (string, error) { return "tok", nil }
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"bad token"}}`))
			return
		}
		switch r.URL.Path {
		case "/gmail/v1/users/me/profile":
			w.Write([]byte(`{"emailAddress":"Me@gmail.com"}`))
		case "/gmail/v1/users/me/messages/send":
			var body struct{ Raw string }
			json.NewDecoder(r.Body).Decode(&body)
			b, _ := base64.RawURLEncoding.DecodeString(body.Raw)
			sent = string(b)
			w.Write([]byte(`{"id":"1"}`))
		}
	}))
	defer srv.Close()
	old := gmailAPIBase
	gmailAPIBase = srv.URL
	defer func() { gmailAPIBase = old }()

	a := &MailAccount{ID: "acc_g", Email: "me@gmail.com", Username: "me@gmail.com", Provider: ProviderGmail, Auth: AuthOAuth2,
		OAuth: &OAuthCreds{ClientID: testClientID, RefreshToken: "r"}}
	if err := s.gmailAPICheck(context.Background(), a); err != nil {
		t.Fatalf("check: %v", err)
	}
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()
	err := s.Send(context.Background(), SendRequest{AccountID: a.ID, To: []Address{{Email: "you@example.com"}},
		Bcc: []Address{{Name: "Hidden", Email: "bcc@example.com"}}, Subject: "hi", Text: "hello"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.HasPrefix(sent, "Bcc: \"Hidden\" <bcc@example.com>\r\n") || !strings.Contains(sent, "you@example.com") {
		t.Fatalf("raw = %q", sent)
	}

	a.Email = "other@gmail.com"
	if err := s.gmailAPICheck(context.Background(), a); err == nil {
		t.Fatal("profile of another address accepted")
	}
}
