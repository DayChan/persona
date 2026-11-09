package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset" // register GBK etc. charset decoders
)

const (
	// networkTimeout bounds every single network operation (dial, command).
	networkTimeout = 20 * time.Second
	// idleTimeout closes a cached IMAP connection that has been unused this long. It is longer than the mail rounds
	// (the inbox's every 3 minutes, the knowledge base's every 6), so a round reuses the last one's login instead of
	// a fresh TCP + TLS + LOGIN per account; RFC 3501 servers keep an idle session at least 30 minutes.
	idleTimeout = 10 * time.Minute
	// sweepInterval is how often idle connections are checked.
	sweepInterval = 30 * time.Second
)

// Service is the mail module. One Service per hub process.
type Service struct {
	dir   string
	store *store

	// accountMu serializes candidate capture, persistence and publication. Network
	// verification happens before taking it; readers see only committed accounts.
	accountMu accountCommitMutex
	mu        sync.Mutex
	accts     map[string]*MailAccount // by ID, as loaded from disk
	conns     map[string]*conn        // by account ID
	// loginErr is why an account's last login was refused, by ID (account_health.go); in memory only.
	loginErr map[string]string
	opMu     map[string]accountLock // per-account IMAP serialization (imap.go)

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// now is swappable in tests.
	now func() time.Time
	// imapDial and sendMail are swappable in tests; nil means the real network.
	imapDial func(a *MailAccount) (*imapclient.Client, error)
	// googleClientID is the iOS OAuth client used for "Sign in with Google" (hub.json google.client_id).
	googleClientID string
	// Outlook sign-in (microsoft.go): hub.json's default client ID and redirect, and the one the user entered
	// (loaded from the settings once).
	msDefaultClientID, msRedirect string
	msClientID                    string
	msClientLoaded                bool
	usedStates                    map[string]time.Time    // OAuth states already finished, against replays
	googleProxy                   *url.URL                // hub.json google.proxy for Gmail traffic (proxy.go), nil = direct
	googleHTTP                    *http.Client            // talks to Google's OAuth and Gmail API endpoints
	access                        map[string]*accessCache // access tokens, by refresh token
	// verify replaces the IMAP + SMTP login check of new accounts in tests.
	verify func(ctx context.Context, a *MailAccount) error
	// scan bounds one page of a local header search, fetch a page's summary fetch; tests shrink them.
	scan  localScan
	fetch pageFetch
	// probe is when a cached connection is checked before it is reused; tests shrink it.
	probe connProbe
	// refreshAccess replaces the Google token endpoint in tests.
	refreshAccess func(*OAuthCreds) (string, error)
	// sendMail delivers a rendered message over SMTP; nil uses the real network.
	sendMail func(ctx context.Context, a *MailAccount, from string, to []string, raw []byte) error
}

// conn is a cached, logged-in IMAP client.
type conn struct {
	c        *imapclient.Client
	lastUsed time.Time
}

// accountCommitMutex has a usable zero value, including for Services without a
// store. A channel lets concurrency tests wait for blocked writers deterministically.
type accountCommitMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *accountCommitMutex) Lock() {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
	m.ch <- struct{}{}
}

func (m *accountCommitMutex) Unlock() { <-m.ch }

// New creates a Service. Accounts live in recs; dataDir holds the encryption key (secret.key).
func New(dataDir string, recs Records) (*Service, error) {
	return NewWithKey(dataDir, filepath.Join(dataDir, "secret.key"), recs)
}

// NewWithKey is New with the encryption key at keyPath (a manager's own secret.key, not its data directory's).
func NewWithKey(dataDir, keyPath string, recs Records) (*Service, error) {
	st, err := openStore(dataDir, keyPath, recs)
	if err != nil {
		return nil, err
	}
	s := &Service{
		dir:        dataDir,
		store:      st,
		accts:      make(map[string]*MailAccount),
		conns:      make(map[string]*conn),
		loginErr:   make(map[string]string),
		usedStates: make(map[string]time.Time),
		googleHTTP: httpClient,
		access:     make(map[string]*accessCache),
		stop:       make(chan struct{}),
		now:        time.Now,
		scan:       defaultLocalScan,
		fetch:      defaultPageFetch,
		probe:      defaultConnProbe,
	}
	accounts, err := st.load()
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		s.accts[a.ID] = a
	}
	s.wg.Add(1)
	go s.sweep()
	return s, nil
}

// SetGoogleClientID enables "Sign in with Google" for Gmail with an iOS OAuth client ID.
func (s *Service) SetGoogleClientID(id string) { s.googleClientID = strings.TrimSpace(id) }

// Close closes every cached connection and stops the idle sweeper.
func (s *Service) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
	s.mu.Lock()
	conns := s.conns
	s.conns = make(map[string]*conn)
	s.mu.Unlock()
	for _, c := range conns {
		c.c.Close()
	}
	return nil
}

// sweep closes IMAP connections that have been idle for too long.
func (s *Service) sweep() {
	defer s.wg.Done()
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		s.sweepOnce()
	}
}

// sweepOnce closes every idle connection.
func (s *Service) sweepOnce() {
	now := s.now()
	s.mu.Lock()
	var stale []*conn
	for id, c := range s.conns {
		if now.Sub(c.lastUsed) > idleTimeout {
			stale = append(stale, c)
			delete(s.conns, id)
		}
	}
	s.mu.Unlock()
	for _, c := range stale {
		c.c.Close()
	}
}

// ListAccounts returns every configured account without secrets.
func (s *Service) ListAccounts() ([]MailAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MailAccount, 0, len(s.accts))
	for _, a := range s.accts {
		out = append(out, *s.withHealthLocked(a))
	}
	sortAccounts(out)
	return out, nil
}

func sortAccounts(a []MailAccount) {
	// Stable by email so the app shows a predictable order.
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && strings.ToLower(a[j].Email) < strings.ToLower(a[j-1].Email); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// account returns a copy of the account with the given ID.
func (s *Service) account(id string) (*MailAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accts[id]
	if !ok {
		return nil, &UnknownAccountError{Account: id}
	}
	return a.clone(), nil
}

func (a *MailAccount) clone() *MailAccount {
	cp := *a
	if a.OAuth != nil {
		creds := *a.OAuth
		cp.OAuth = &creds
	}
	return &cp
}

// public returns a copy without secrets.
func (a *MailAccount) public() *MailAccount {
	cp := *a
	cp.Password, cp.OAuth = "", nil
	return &cp
}

// ErrAccountExists: the address already has an account (HTTP 409). Changing its servers or password means deleting
// it and adding it again, and the message says so.
var ErrAccountExists = errors.New("已经添加过了")

func accountExists(email string) error {
	return fmt.Errorf("%s %w；要换服务器或授权码，先删掉原来的账号再添加", email, ErrAccountExists)
}

// hasAddress reports whether an account with this address is already there.
func (s *Service) hasAddress(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, email) {
			return true
		}
	}
	return false
}

// AddAccount validates the account (by testing the connections) and saves it.
func (s *Service) AddAccount(req AddAccountRequest) (*MailAccount, error) {
	if req.Auth == AuthOAuth2 {
		return nil, errors.New("invalid auth \"oauth2\": use Sign in with Google")
	}
	a, err := buildAccount(req)
	if err != nil {
		return nil, err
	}
	// A duplicate is refused before the connection test, which takes seconds and up to 40 on a slow server, twice for
	// Feishu's two server sets (#1766).
	if s.hasAddress(a.Email) {
		return nil, accountExists(a.Email)
	}
	if a.Provider == ProviderFeishu {
		err = s.pickFeishuServers(context.Background(), a)
	} else {
		err = s.testAccount(context.Background(), a)
	}
	if err != nil {
		return nil, err
	}

	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, a.Email) { // added by another request while this one was testing
			s.mu.Unlock()
			return nil, accountExists(a.Email)
		}
	}
	s.mu.Unlock()

	if err := s.commitAccount(a.ID, a, false, nil); err != nil {
		return nil, err
	}
	return a.public(), nil
}

// upsertAccount saves a verified account; an existing account with the same address keeps its ID
// and name but takes the new credentials (signing in with Google again repairs a revoked token).
func (s *Service) upsertAccount(a *MailAccount) (*MailAccount, error) {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	a = a.clone()
	s.mu.Lock()
	var prev *MailAccount
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, a.Email) {
			prev = existing
			break
		}
	}
	if prev != nil {
		a.ID, a.Name = prev.ID, prev.Name
	}
	s.mu.Unlock()

	if err := s.commitAccount(a.ID, a, true, nil); err != nil {
		return nil, err
	}
	return a.public(), nil
}

// DeleteAccount removes an account and closes its connection.
func (s *Service) DeleteAccount(id string) error {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	if _, ok := s.accts[id]; !ok {
		s.mu.Unlock()
		return &UnknownAccountError{Account: id}
	}
	s.mu.Unlock()
	return s.commitAccount(id, nil, true, nil)
}

// commitAccount persists a detached candidate, then publishes it and retires
// health/connections together when credentials changed or the account was removed.
// A nil account removes id; publish updates associated caches under s.mu.
// The caller holds accountMu, never across network verification.
func (s *Service) commitAccount(id string, a *MailAccount, retire bool, publish func()) error {
	s.mu.Lock()
	accounts := s.snapshotLocked()
	s.mu.Unlock()
	candidate := make([]*MailAccount, 0, len(accounts)+1)
	for _, existing := range accounts {
		if existing.ID != id {
			candidate = append(candidate, existing)
		}
	}
	if a != nil {
		a = a.clone()
		candidate = append(candidate, a)
	}
	if err := s.store.save(candidate); err != nil {
		return err
	}
	s.mu.Lock()
	if a == nil {
		delete(s.accts, id)
	} else {
		s.accts[id] = a
	}
	var c *conn
	if retire {
		delete(s.loginErr, id)
		c = s.conns[id]
		delete(s.conns, id)
	}
	if publish != nil {
		publish()
	}
	s.mu.Unlock()
	if c != nil {
		c.c.Close()
	}
	return nil
}

// snapshotLocked copies accounts and their credentials. The caller holds s.mu.
func (s *Service) snapshotLocked() []*MailAccount {
	out := make([]*MailAccount, 0, len(s.accts))
	for _, a := range s.accts {
		out = append(out, a.clone())
	}
	return out
}

// TestAccount connects to IMAP and authenticates, and checks SMTP is reachable.
func (s *Service) TestAccount(id string) error {
	a, err := s.account(id)
	if err != nil {
		return err
	}
	return s.testAccount(context.Background(), a)
}

func (s *Service) testAccount(ctx context.Context, a *MailAccount) error {
	if s.verify != nil {
		return s.verify(ctx, a)
	}
	ctx, cancel := context.WithTimeout(ctx, networkTimeout*2)
	defer cancel()

	c, err := s.dialIMAP(ctx, a)
	if err != nil {
		if _, ok := errors.AsType[userError](err); ok {
			return err // already says what to fix; no protocol prefix in front of it
		}
		return fmt.Errorf("imap: %w", err)
	}
	c.Close()

	if a.Auth == AuthOAuth2 && a.Provider == ProviderGmail {
		// Sent through the Gmail API, not SMTP (gmailAPIBase).
		return s.gmailAPICheck(ctx, a)
	}
	if err := s.checkSMTP(ctx, a); err != nil {
		if _, ok := errors.AsType[userError](err); ok {
			return err
		}
		return fmt.Errorf("smtp: %w", err)
	}
	return nil
}

// pickFeishuServers tests a Feishu account against every set in feishuServers at
// once and keeps the first that passes, so a hub in mainland China ends up on
// feishu.cn and one overseas on larkoffice.com without asking the user which. The
// slower tests are left to finish on their own; each is bounded by testAccount's
// timeout.
func (s *Service) pickFeishuServers(ctx context.Context, a *MailAccount) error {
	type result struct {
		p   *preset
		err error
	}
	results := make(chan result, len(feishuServers))
	for _, p := range feishuServers {
		c := *a
		c.IMAPHost, c.IMAPPort, c.SMTPHost, c.SMTPPort, c.SMTPSecurity = p.IMAPHost, p.IMAPPort, p.SMTPHost, p.SMTPPort, p.SMTPSecurity
		go func() { results <- result{p, s.testAccount(ctx, &c)} }()
	}
	errs := make([]error, 0, len(feishuServers))
	var answered error
	for range feishuServers {
		r := <-results
		if r.err == nil {
			a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort, a.SMTPSecurity = r.p.IMAPHost, r.p.IMAPPort, r.p.SMTPHost, r.p.SMTPPort, r.p.SMTPSecurity
			return nil
		}
		err := fmt.Errorf("%s: %w", r.p.IMAPHost, r.err)
		errs = append(errs, err)
		if answered == nil && !unreachable(r.err) {
			answered = err
		}
	}
	// A wrong password is reported by whichever set was reachable; the other set
	// timing out or refusing the connection must not hide it.
	if answered != nil {
		return answered
	}
	return errors.Join(errs...)
}

// unreachable reports whether err means the server could not be reached at all
// (timeout, refused, DNS), as opposed to an answer such as a rejected login.
func unreachable(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne)
}

// buildAccount normalizes a request, applying the domain preset for any field
// the request leaves empty.
func buildAccount(req AddAccountRequest) (*MailAccount, error) {
	email := normalizeEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.New("email is required")
	}
	a := &MailAccount{
		ID:           newAccountID(),
		Name:         strings.TrimSpace(req.Name),
		Email:        email,
		Username:     strings.TrimSpace(req.Username),
		IMAPHost:     strings.TrimSpace(req.IMAPHost),
		IMAPPort:     req.IMAPPort,
		IMAPSecurity: strings.ToLower(strings.TrimSpace(req.IMAPSecurity)),
		SMTPHost:     strings.TrimSpace(req.SMTPHost),
		SMTPPort:     req.SMTPPort,
		SMTPSecurity: strings.ToLower(strings.TrimSpace(req.SMTPSecurity)),
		Password:     req.Password,
		Status:       "ok",
	}
	if a.Name == "" {
		a.Name = email
	}
	if a.Username == "" {
		a.Username = email
	}

	preset := presetFor(email)
	// Providers with fixed servers (Feishu) win over the email domain, which is the company's own.
	if p := providerPresets[req.Provider]; p != nil {
		preset = p
		a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort, a.SMTPSecurity = "", 0, "", 0, ""
	}
	if preset != nil {
		if a.Provider == "" {
			a.Provider = preset.Provider
		}
		if a.IMAPHost == "" {
			a.IMAPHost = preset.IMAPHost
		}
		if a.IMAPPort == 0 {
			a.IMAPPort = preset.IMAPPort
		}
		if a.SMTPHost == "" {
			a.SMTPHost = preset.SMTPHost
		}
		if a.SMTPPort == 0 {
			a.SMTPPort = preset.SMTPPort
		}
		if a.SMTPSecurity == "" {
			a.SMTPSecurity = preset.SMTPSecurity
		}
	}
	if a.Provider == "" {
		a.Provider = req.Provider
	}
	if a.Provider == "" {
		a.Provider = ProviderIMAP
	}
	if a.Provider != ProviderGmail && a.Provider != ProviderIMAP && a.Provider != ProviderFeishu && a.Provider != ProviderOutlook {
		return nil, fmt.Errorf("invalid provider %q", a.Provider)
	}

	switch a.Auth = req.Auth; a.Auth {
	case "", AuthPassword:
		a.Auth = AuthPassword
		if a.Password == "" {
			return nil, errors.New("password is required")
		}
	case AuthOAuth2:
		if a.Provider != ProviderGmail && a.Provider != ProviderOutlook {
			return nil, errors.New("invalid auth \"oauth2\": only Gmail and Outlook sign in with their accounts")
		}
	default:
		return nil, fmt.Errorf("invalid auth %q", a.Auth)
	}

	if a.IMAPHost == "" || a.IMAPPort == 0 {
		return nil, fmt.Errorf("unknown mail provider for %s: provide imap_host and imap_port", email)
	}
	if a.SMTPHost == "" || a.SMTPPort == 0 {
		return nil, fmt.Errorf("unknown mail provider for %s: provide smtp_host and smtp_port", email)
	}
	if a.SMTPSecurity == "" {
		switch a.SMTPPort {
		case 465:
			a.SMTPSecurity = "tls"
		case 587, 25:
			a.SMTPSecurity = "starttls"
		default:
			a.SMTPSecurity = "tls"
		}
	}
	return a, nil
}

// dialIMAP opens and authenticates a fresh IMAP connection.
func (s *Service) dialIMAP(ctx context.Context, a *MailAccount) (*imapclient.Client, error) {
	if s.imapDial != nil {
		return s.imapDial(a)
	}
	token, err := s.oauthToken(ctx, a)
	if err != nil {
		return nil, err
	}
	type result struct {
		c   *imapclient.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := dialAndLogin(a, token, s.proxyFor(a))
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.err
	}
}

func dialAndLogin(a *MailAccount, token string, proxy *url.URL) (*imapclient.Client, error) {
	addr := serverAddr(a.IMAPHost, a.IMAPPort)
	opts := &imapclient.Options{
		TLSConfig:   &tls.Config{ServerName: a.IMAPHost},
		WordDecoder: mailWordDecoder(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	var (
		c    *imapclient.Client
		conn net.Conn
		err  error
	)
	switch imapSecurity(a) {
	case "tls":
		if conn, err = dialTLS(ctx, proxy, addr, a.IMAPHost); err == nil {
			c = imapclient.New(conn, opts)
		}
	case "starttls":
		if conn, err = dialTCP(ctx, proxy, addr); err == nil {
			c, err = imapclient.NewStartTLS(conn, opts)
		}
	default:
		if conn, err = dialTCP(ctx, proxy, addr); err == nil {
			c = imapclient.New(conn, opts)
		}
	}
	if err != nil {
		return nil, err
	}
	// NetEase (163/126) rejects logins from clients that do not identify
	// themselves, so send ID before authenticating. Servers without the ID
	// extension answer BAD; that is harmless here.
	if c.Caps().Has(imap.CapID) {
		sendClientID(c)
	}
	if err := loginIMAP(c, a, token); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// sendClientID announces the client to the server (RFC 2971), best effort.
func sendClientID(c *imapclient.Client) {
	data := &imap.IDData{Name: "Persona", Version: "1.0", Vendor: "Persona"}
	cmd := c.ID(data)
	_, _ = cmd.Wait()
}

func imapSecurity(a *MailAccount) string {
	if a.IMAPSecurity != "" {
		return a.IMAPSecurity
	}
	switch a.IMAPPort {
	case 993:
		return "tls"
	case 143:
		return "starttls"
	default:
		return "none"
	}
}

// connProbe is when getConn checks a cached connection before handing it out: one unused for longer than after
// must answer a NOOP within wait, or it is dropped and dialed again. A NAT or firewall that silently forgot an idle
// flow would otherwise leave the next command hanging for the whole networkTimeout.
type connProbe struct {
	after, wait time.Duration
}

var defaultConnProbe = connProbe{after: time.Minute, wait: 5 * time.Second}

// probeLimits is s.probe with the defaults filled in (a zero Service has none).
func (s *Service) probeLimits() connProbe {
	p := s.probe
	if p.after <= 0 {
		p.after = defaultConnProbe.after
	}
	if p.wait <= 0 {
		p.wait = defaultConnProbe.wait
	}
	return p
}

// alive reports whether c answers a NOOP within wait. A connection that doesn't is closed, which also ends the NOOP.
func alive(ctx context.Context, c *imapclient.Client, wait time.Duration) bool {
	done := make(chan error, 1)
	go func() { done <- c.Noop().Wait() }()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case err := <-done:
		return err == nil
	case <-t.C:
	case <-ctx.Done():
	}
	c.Close()
	return false
}

// getConn returns a logged-in IMAP client for the account, dialing if needed. The caller holds the account's lock
// (withConnAt), so nothing else uses the connection while it is probed.
func (s *Service) getConn(ctx context.Context, a *MailAccount) (*imapclient.Client, error) {
	s.mu.Lock()
	cached := s.conns[a.ID]
	if cached != nil {
		if idle := s.now().Sub(cached.lastUsed); idle <= idleTimeout {
			cached.lastUsed = s.now()
			s.mu.Unlock()
			p := s.probeLimits()
			if idle <= p.after || alive(ctx, cached.c, p.wait) {
				return cached.c, nil
			}
			logf("mail: %s: the connection idle for %v did not answer a NOOP; reconnecting", a.ID, idle.Round(time.Second))
			if err := ctx.Err(); err != nil {
				s.dropConn(a.ID)
				return nil, err
			}
			s.mu.Lock()
		}
	}
	stale := s.conns[a.ID]
	delete(s.conns, a.ID)
	s.mu.Unlock()
	if stale != nil {
		stale.c.Close()
	}

	type result struct {
		c   *imapclient.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := s.dialIMAP(context.Background(), a)
		ch <- result{c, err}
	}()
	var c *imapclient.Client
	select {
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		c = r.c
	}

	s.mu.Lock()
	// A concurrent caller may have installed a connection meanwhile; prefer it
	// and drop ours so we never leak.
	if existing, ok := s.conns[a.ID]; ok {
		s.mu.Unlock()
		c.Close()
		return existing.c, nil
	}
	s.conns[a.ID] = &conn{c: c, lastUsed: s.now()}
	s.mu.Unlock()
	return c, nil
}

// dropConn removes the cached connection for an account, closing it.
func (s *Service) dropConn(id string) {
	s.mu.Lock()
	c := s.conns[id]
	delete(s.conns, id)
	s.mu.Unlock()
	if c != nil {
		c.c.Close()
	}
}

// withConn runs fn on the cached connection; a transport-level error closes the
// connection and fn is retried once on a fresh one.
func (s *Service) withConn(ctx context.Context, a *MailAccount, fn func(*imapclient.Client) error) error {
	return s.withConnAt(ctx, a, func(c *imapclient.Client, _ time.Time) error { return fn(c) })
}

// withConnAt is withConn handing fn the operation's deadline: networkTimeout after the account was free (connecting
// included), or the context's if that is sooner, and the same one when it retries. Work done in steps (a page of a
// slow mailbox) stops in time with it.
func (s *Service) withConnAt(ctx context.Context, a *MailAccount, fn func(c *imapclient.Client, deadline time.Time) error) error {
	// IMAP is stateful (SELECT), so a cached connection can only serve one
	// command sequence at a time. The wait for the turn is the caller's ctx's:
	// a caller that gave up leaves the queue (#2344).
	mu := s.accountLock(a.ID)
	if err := mu.acquire(ctx); err != nil {
		return err
	}
	defer mu.release()

	// The operation's own time starts once the account is free, on purpose: a
	// slow mailbox ahead in the queue must not eat it.
	deadline := time.Now().Add(networkTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	run := func() error {
		c, err := s.getConn(ctx, a)
		s.noteLogin(a.ID, err)
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- fn(c, deadline) }()
		select {
		case err := <-done:
			return err
		case <-time.After(time.Until(deadline)):
			s.dropConn(a.ID)
			return errors.New("imap: operation timed out")
		case <-ctx.Done():
			s.dropConn(a.ID)
			return ctx.Err()
		}
	}

	err := run()
	if err == nil || !isConnError(err) {
		return err
	}
	s.dropConn(a.ID)
	return run()
}

// isConnError reports whether err means the connection is unusable, so a
// reconnect is worth trying.
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	var ie *imap.Error
	if errors.As(err, &ie) {
		// A tagged NO/BAD is a protocol answer, not a broken connection.
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

var netDialer = net.Dialer{Timeout: networkTimeout}

// logf is a thin indirection so tests can silence output.
var logf = func(format string, args ...any) { log.Printf(format, args...) }
