package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Gmail "Sign in with Google": the app opens Google's consent page in the system browser
// (ASWebAuthenticationSession), Google redirects to the reversed client ID scheme of an
// iOS OAuth client, and the hub exchanges the code (PKCE, no client secret) for a refresh
// token. IMAP and SMTP then authenticate with SASL XOAUTH2. See docs/extensions/mail.md.

const (
	AuthOAuth2 = "oauth2"

	googleScopes    = "https://mail.google.com/ openid email"
	oauthPendingTTL = 15 * time.Minute
)

var (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	httpClient     = &http.Client{Timeout: networkTimeout}
)

// OAuthCreds is the long-lived credential of an OAuth account, stored encrypted.
type OAuthCreds struct {
	// Provider is whose sign-in it is: "" Google, "microsoft" (microsoft.go).
	Provider     string `json:"provider,omitempty"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

// OAuthStart is the POST /api/v1/mail/oauth/google/start response.
type OAuthStart struct {
	AuthURL        string `json:"auth_url"`
	CallbackScheme string `json:"callback_scheme"`
	State          string `json:"state"`
}

// OAuthFinishRequest is the POST /api/v1/mail/oauth/google/finish body: the callback URL's query.
type OAuthFinishRequest struct {
	State            string `json:"state"`
	Code             string `json:"code"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// oauthState is what the state parameter carries, sealed with the store key: the flow survives a
// hub restart (or a redeploy) while the user is on Google's pages.
type oauthState struct {
	Verifier string `json:"v"`
	Created  int64  `json:"t"`
	ClientID string `json:"c,omitempty"` // Microsoft: the client the flow started with (the user may change it meanwhile)
}

func (s *Service) sealState(st oauthState) (string, error) {
	plain, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	enc, err := s.store.encrypt(plain)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Service) openState(state string) (*oauthState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return nil, err
	}
	plain, err := s.store.decrypt(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		return nil, err
	}
	var st oauthState
	if err := json.Unmarshal(plain, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// accessCache holds one refresh token's current access token.
type accessCache struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

// googleRedirectURI is the redirect of an iOS OAuth client: its reversed client ID as the scheme.
func googleRedirectURI(clientID string) (scheme, uri string) {
	parts := strings.Split(clientID, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	scheme = strings.Join(parts, ".")
	return scheme, scheme + ":/oauth2redirect"
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// GoogleConfig says whether "Sign in with Google" can start on this hub (GET /mail/oauth/google/config). There is no
// shared client: each hub has its own iOS OAuth client (google.client_id), so the apps ask before offering the button
// and add Gmail with an app password on a hub without one (#2442).
type GoogleConfig struct {
	Configured bool `json:"configured"`
}

func (s *Service) GoogleConfig() GoogleConfig {
	return GoogleConfig{Configured: s.googleClientID != ""}
}

// errGoogleNotConfigured says who sets Google sign-in up and what works meanwhile, in the client's language (English
// without one). Every language's text keeps "not configured": Android apps that do not read google/config match that
// phrase to fall back to the app-password form.
var errGoogleNotConfigured = i18n.ErrEnglish("mail-oauth.google_not_configured")

// googleNotConfiguredText is errGoogleNotConfigured in r's language: the signed-in user's, else Accept-Language.
func googleNotConfiguredText(r *http.Request) string {
	l := i18n.From(r.Context())
	if l == "" {
		l = i18n.FromRequest(r)
	}
	return i18n.Localize(errGoogleNotConfigured, l)
}

// StartGoogleOAuth begins a "Sign in with Google" flow. loginHint preselects an account.
func (s *Service) StartGoogleOAuth(loginHint string) (*OAuthStart, error) {
	if s.googleClientID == "" {
		return nil, errGoogleNotConfigured
	}
	verifier := randomURLSafe(48)
	state, err := s.sealState(oauthState{Verifier: verifier, Created: s.now().Unix()})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	scheme, redirect := googleRedirectURI(s.googleClientID)
	q := url.Values{
		"client_id":             {s.googleClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {googleScopes},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		// offline + consent: always hand out a refresh token, also when re-authorizing.
		"access_type": {"offline"},
		"prompt":      {"select_account consent"},
	}
	if hint := normalizeEmail(loginHint); hint != "" {
		q.Set("login_hint", hint)
	}

	return &OAuthStart{AuthURL: googleAuthURL + "?" + q.Encode(), CallbackScheme: scheme, State: state}, nil
}

// FinishGoogleOAuth exchanges the authorization code, verifies IMAP and SMTP access and saves
// the account. Signing in again with an existing Gmail account replaces its credentials.
func (s *Service) FinishGoogleOAuth(ctx context.Context, req OAuthFinishRequest) (*MailAccount, error) {
	// Each state is good for one attempt (remembered in memory; Google's codes are single use anyway).
	st, err := s.claimState(req.State)
	if err != nil {
		return nil, err
	}
	if req.Error != "" {
		return nil, fmt.Errorf("Google sign-in failed: %s", req.Error)
	}
	if req.Code == "" {
		return nil, errors.New("code is required")
	}

	_, redirect := googleRedirectURI(s.googleClientID)
	tr, err := s.googleToken(ctx, url.Values{
		"client_id":     {s.googleClientID},
		"code":          {req.Code},
		"code_verifier": {st.Verifier},
		"redirect_uri":  {redirect},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return nil, err
	}
	if tr.RefreshToken == "" {
		return nil, errors.New("Google returned no refresh token, sign in again")
	}
	if !strings.Contains(" "+tr.Scope+" ", " https://mail.google.com/ ") {
		return nil, errors.New("Gmail access was not granted: tick the mail permission on Google's consent page")
	}
	email, err := idTokenEmail(tr.IDToken)
	if err != nil {
		return nil, err
	}

	a, err := buildAccount(AddAccountRequest{Email: email, Provider: ProviderGmail, Auth: AuthOAuth2})
	if err != nil {
		return nil, err
	}
	a.OAuth = &OAuthCreds{ClientID: s.googleClientID, RefreshToken: tr.RefreshToken}
	s.storeAccess(a.OAuth, tr.AccessToken, tr.ExpiresIn)
	if err := s.testAccount(ctx, a); err != nil {
		return nil, err
	}
	return s.upsertAccount(a)
}

// tokenResponse is the subset of Google's token endpoint reply we use.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (s *Service) googleToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.googleHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oauth: parse token response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		if tr.Error == "invalid_grant" && form.Get("grant_type") == "refresh_token" {
			return nil, errGoogleRevoked
		}
		msg := tr.Error
		if tr.ErrorDesc != "" {
			msg += ": " + tr.ErrorDesc
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("oauth: token endpoint returned HTTP %d: %s", resp.StatusCode, msg)
	}
	return &tr, nil
}

// idTokenEmail reads the verified email from an ID token. The token came straight from Google's
// token endpoint over TLS, so its signature does not need checking (OpenID Connect Core 3.1.3.7).
func idTokenEmail(idToken string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", errors.New("oauth: Google returned no ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("oauth: decode ID token: %w", err)
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("oauth: parse ID token: %w", err)
	}
	if claims.Email == "" || !claims.EmailVerified {
		return "", errors.New("oauth: Google account has no verified email")
	}
	return normalizeEmail(claims.Email), nil
}

func (s *Service) accessEntry(creds *OAuthCreds) *accessCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accessEntryLocked(creds)
}

func (s *Service) accessEntryLocked(creds *OAuthCreds) *accessCache {
	c := s.access[creds.RefreshToken]
	if c == nil {
		c = &accessCache{}
		s.access[creds.RefreshToken] = c
	}
	return c
}

func (s *Service) storeAccess(creds *OAuthCreds, token string, expiresIn int) {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	c := s.accessEntry(creds)
	c.mu.Lock()
	c.token, c.expires = token, s.now().Add(time.Duration(expiresIn)*time.Second)
	c.mu.Unlock()
}

// accessToken returns a valid access token, refreshing it when it expires within a minute.
func (s *Service) accessToken(ctx context.Context, id string, creds *OAuthCreds) (string, error) {
	if creds == nil || creds.RefreshToken == "" {
		return "", errors.New("mail: oauth2 account without credentials")
	}
	if s.refreshAccess != nil {
		return s.refreshAccess(creds)
	}
	// Operations own detached credentials. A read queued before a rotation uses
	// the latest committed token and its cache when it finally authenticates.
	s.mu.Lock()
	if a := s.accts[id]; a != nil && a.OAuth != nil {
		creds = a.clone().OAuth
	}
	c := s.accessEntryLocked(creds)
	s.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && s.now().Add(time.Minute).Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{
		"client_id":     {creds.ClientID},
		"refresh_token": {creds.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	var tr *tokenResponse
	var err error
	if creds.Provider == oauthMicrosoft {
		form.Set("scope", microsoftScopes)
		tr, err = s.microsoftToken(ctx, form)
	} else {
		tr, err = s.googleToken(ctx, form)
	}
	if err != nil {
		return "", err
	}
	// Microsoft hands out a new refresh token with every refresh (the old one lapses): keep it.
	if tr.RefreshToken != "" && tr.RefreshToken != creds.RefreshToken {
		s.rotateRefresh(id, creds, tr.RefreshToken, c)
	}
	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	c.token, c.expires = tr.AccessToken, s.now().Add(time.Duration(expiresIn)*time.Second)
	return c.token, nil
}

// rotateRefresh replaces an account's refresh token (and the key of its access cache) and saves the accounts.
func (s *Service) rotateRefresh(id string, creds *OAuthCreds, refresh string, c *accessCache) {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	var updated *MailAccount
	if a := s.accts[id]; a != nil && a.OAuth != nil && *a.OAuth == *creds {
		updated = a.clone()
		updated.OAuth.RefreshToken = refresh
	}
	s.mu.Unlock()
	publish := func() {
		delete(s.access, creds.RefreshToken)
		creds.RefreshToken = refresh
		s.access[refresh] = c
	}
	// Verification can rotate credentials before the account has been committed.
	// A stale refresh after deletion/re-authorization must not recreate that account.
	if updated != nil {
		if err := s.commitAccount(updated.ID, updated, false, publish); err != nil {
			logf("mail: save the rotated refresh token: %v", err)
			return
		}
		return
	}
	s.mu.Lock()
	publish()
	s.mu.Unlock()
}

// xoauth2Payload is the SASL XOAUTH2 initial response: "user=<email>^Aauth=Bearer <token>^A^A".
func xoauth2Payload(user, token string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01")
}

// xoauth2Client implements SASL XOAUTH2 for IMAP AUTHENTICATE and SMTP AUTH.
type xoauth2Client struct {
	user, token string
	done        bool
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	return "XOAUTH2", xoauth2Payload(c.user, c.token), nil
}

// Next answers the server's error challenge (a JSON status) with an empty response, which makes
// the server finish with a normal NO / 535 carrying the reason.
func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if c.done {
		return nil, errors.New("mail: XOAUTH2 server sent too many challenges")
	}
	c.done = true
	return []byte{}, nil
}

func newXOAUTH2Client(user, token string) sasl.Client {
	return &xoauth2Client{user: user, token: token}
}

// gmailAPIBase is the Gmail API. OAuth Gmail accounts send through it rather than SMTP: some hub machines cannot
// reach smtp.gmail.com on 465 or 587, while HTTPS to Google works, and https://mail.google.com/ covers both.
var gmailAPIBase = "https://gmail.googleapis.com"

func (s *Service) gmailAPI(ctx context.Context, a *MailAccount, method, path string, body any, out any) error {
	token, err := s.oauthToken(ctx, a)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, gmailAPIBase+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.googleHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("gmail api: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("gmail api: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return fmt.Errorf("gmail api: HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// gmailAPICheck stands in for the SMTP check of OAuth Gmail accounts: the token works for the Gmail API
// and belongs to the account's address.
func (s *Service) gmailAPICheck(ctx context.Context, a *MailAccount) error {
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := s.gmailAPI(ctx, a, http.MethodGet, "/gmail/v1/users/me/profile", nil, &p); err != nil {
		return err
	}
	if !strings.EqualFold(p.EmailAddress, a.Email) {
		return fmt.Errorf("gmail api: token belongs to %s, not %s", p.EmailAddress, a.Email)
	}
	return nil
}

// gmailAPISend sends a rendered message. Gmail delivers to the Bcc header and strips it, and files the message
// in Sent itself.
func (s *Service) gmailAPISend(ctx context.Context, a *MailAccount, bcc []Address, raw []byte) error {
	if len(bcc) > 0 {
		list := make([]string, 0, len(bcc))
		for _, b := range bcc {
			list = append(list, (&netmail.Address{Name: b.Name, Address: strings.TrimSpace(b.Email)}).String())
		}
		raw = append([]byte("Bcc: "+strings.Join(list, ", ")+"\r\n"), raw...)
	}
	body := map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}
	return s.gmailAPI(ctx, a, http.MethodPost, "/gmail/v1/users/me/messages/send", body, nil)
}
