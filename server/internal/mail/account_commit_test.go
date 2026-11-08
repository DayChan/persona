package mail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

var errAccountWrite = errors.New("injected account write failure")

type accountTokenTransport func(*http.Request) (*http.Response, error)

func (f accountTokenTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type accountWriteRecords struct {
	*memRecords
	replace func(context.Context, []db.MailAccountRow) error
}

func (r *accountWriteRecords) ReplaceMailAccounts(ctx context.Context, rows []db.MailAccountRow) error {
	return r.replace(ctx, rows)
}

func commitTestAccount(id string) *MailAccount {
	return &MailAccount{ID: id, Name: "Original name", Email: id + "@example.com", Provider: ProviderIMAP,
		Auth: AuthOAuth2, Status: "ok", OAuth: &OAuthCreds{ClientID: "example-client", RefreshToken: "example-refresh-" + id}}
}

// Copy secrets too: a shallow snapshot would hide an in-place token mutation.
func committedAccounts(s *Service) map[string]MailAccount {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]MailAccount)
	for id, a := range s.accts {
		cp := *a
		if a.OAuth != nil {
			creds := *a.OAuth
			cp.OAuth = &creds
		}
		out[id] = cp
	}
	return out
}

func assertAccountRestart(t *testing.T, s *Service, recs Records) {
	t.Helper()
	again, err := New(s.dir, recs)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !reflect.DeepEqual(committedAccounts(s), committedAccounts(again)) {
		t.Fatal("restarted accounts differ from committed memory")
	}
}

// All snapshot writers must capture their candidate only after the previous writer
// finishes. Wait parks the second goroutine at the guard, without requiring it to
// enter storage (which would deadlock a correctly serialized implementation).
func TestAccountCommitsSerializeSnapshots(t *testing.T) {
	for _, kind := range []string{"add", "upsert", "update", "import", "delete", "refresh"} {
		for _, failFirst := range []bool{false, true} {
			for _, failSecond := range []bool{false, true} {
				name := kind
				if failFirst {
					name += "/first-fails"
				} else {
					name += "/first-succeeds"
				}
				if failSecond {
					name += "/second-fails"
				} else {
					name += "/second-succeeds"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						s, recs := newOAuthService(t)
						defer s.Close()
						if _, err := s.upsertAccount(commitTestAccount("base")); err != nil {
							t.Fatal(err)
						}
						before := committedAccounts(s)
						started, release := make(chan struct{}), make(chan struct{})
						var calls atomic.Int32
						s.store.recs = &accountWriteRecords{memRecords: recs, replace: func(ctx context.Context, rows []db.MailAccountRow) error {
							n := calls.Add(1)
							if n == 1 {
								close(started)
								<-release
							}
							if (n == 1 && failFirst) || (n == 2 && failSecond) {
								return errAccountWrite
							}
							return recs.ReplaceMailAccounts(ctx, rows)
						}}
						first, second := make(chan error, 1), make(chan error, 1)
						mutate := func() error {
							var err error
							switch kind {
							case "add":
								_, err = s.AddAccount(addRequest("first@example.com"))
							case "upsert":
								_, err = s.upsertAccount(commitTestAccount("first"))
							case "update":
								a := commitTestAccount("replacement")
								a.Email, a.OAuth.RefreshToken = "BASE@example.com", "example-updated-refresh"
								_, err = s.upsertAccount(a)
							case "import":
								a := commitTestAccount("first")
								data, marshalErr := json.Marshal(exportedAccount{MailAccount: *a, OAuth: a.OAuth})
								if marshalErr != nil {
									panic(marshalErr)
								}
								_, err = s.ImportAccount(data)
							case "delete":
								err = s.DeleteAccount("base")
							case "refresh":
								a, _ := s.account("base")
								s.rotateRefresh(a.ID, a.OAuth, "example-rotated-refresh", s.accessEntry(a.OAuth))
							}
							return err
						}
						go func() { first <- mutate() }()
						<-started
						if !reflect.DeepEqual(committedAccounts(s), before) {
							close(release)
							<-first
							t.Fatal("first mutation was visible before persistence")
						}
						go func() { _, err := s.upsertAccount(commitTestAccount("second")); second <- err }()
						synctest.Wait()
						pending := committedAccounts(s)
						close(release)
						firstErr, secondErr := <-first, <-second
						if !reflect.DeepEqual(pending, before) {
							t.Error("uncommitted account changes were visible while storage was blocked")
						}
						if kind != "refresh" && ((!failFirst && firstErr != nil) || (failFirst && !errors.Is(firstErr, errAccountWrite))) {
							t.Fatalf("first write: %v", firstErr)
						}
						if (!failSecond && secondErr != nil) || (failSecond && !errors.Is(secondErr, errAccountWrite)) {
							t.Fatalf("second write: %v", secondErr)
						}
						if calls.Load() != 2 {
							t.Fatalf("writes = %d, want 2", calls.Load())
						}
						after := committedAccounts(s)
						if _, ok := after["second"]; ok == failSecond {
							t.Error("second account publication disagrees with its write result")
						}
						if failFirst {
							delete(after, "second")
							if !reflect.DeepEqual(after, before) {
								t.Error("failed mutation changed committed accounts")
							}
						} else {
							switch kind {
							case "delete":
								if _, ok := after["base"]; ok {
									t.Error("successful delete retained account")
								}
							case "refresh":
								if after["base"].OAuth.RefreshToken != "example-rotated-refresh" {
									t.Error("rotation was not published")
								}
							case "update":
								if after["base"].OAuth.RefreshToken != "example-updated-refresh" || after["base"].Name != before["base"].Name {
									t.Error("update lost credentials, ID or name")
								}
							default:
								want := 2
								if !failSecond {
									want++
								}
								if len(after) != want {
									t.Error("successful addition was not published")
								}
							}
						}
						assertAccountRestart(t, s, recs)
						// A later write must recover without reviving a rejected mutation.
						if _, err := s.upsertAccount(commitTestAccount("recovery")); err != nil {
							t.Fatal(err)
						}
						assertAccountRestart(t, s, recs)
						if failFirst {
							if err := mutate(); err != nil {
								t.Fatalf("retry: %v", err)
							}
							assertAccountRestart(t, s, recs)
						}
					})
				})
			}
		}
	}
}

func TestRejectedAccountChangesPreserveHealthAndConnection(t *testing.T) {
	s := newHealthService(t)
	a, _ := s.account("acc_test")
	c, err := s.getConn(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	s.noteLogin(a.ID, errGoogleRevoked)
	before := committedAccounts(s)
	recs := s.store.recs
	s.store.recs = &accountWriteRecords{replace: func(context.Context, []db.MailAccountRow) error { return errAccountWrite }}
	for _, kind := range []string{"update", "delete"} {
		var err error
		if kind == "update" {
			updated := *a
			updated.ID, updated.Password, updated.Name = "replacement", "example-new-password", "New name"
			_, err = s.upsertAccount(&updated)
		} else {
			err = s.DeleteAccount(a.ID)
		}
		if !errors.Is(err, errAccountWrite) {
			t.Fatalf("%s: %v", kind, err)
		}
		if !reflect.DeepEqual(committedAccounts(s), before) {
			t.Fatalf("%s changed committed account", kind)
		}
		if status, reason := accountStatus(t, s); status != "error" || reason != errGoogleRevoked.Error() {
			t.Fatalf("%s cleared health", kind)
		}
		s.mu.Lock()
		cached := s.conns[a.ID]
		s.mu.Unlock()
		if cached == nil || cached.c != c {
			t.Fatalf("%s retired the cached connection", kind)
		}
		if err := c.Noop().Wait(); err != nil {
			t.Fatalf("%s closed the connection: %v", kind, err)
		}
	}
	s.store.recs = recs
	updated := *a
	updated.ID, updated.Name = "replacement", "New name"
	got, err := s.upsertAccount(&updated)
	if err != nil || got.ID != a.ID || got.Name != a.Name {
		t.Fatalf("successful repair changed identity: %v", err)
	}
	if status, reason := accountStatus(t, s); status != "ok" || reason != "" {
		t.Fatal("successful repair did not clear health")
	}
	if err := c.Noop().Wait(); err == nil {
		t.Fatal("successful repair did not close old connection")
	}
	assertAccountRestart(t, s, recs)
	c, err = s.getConn(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	s.noteLogin(a.ID, errGoogleRevoked)
	if err := s.DeleteAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Noop().Wait(); err == nil {
		t.Fatal("successful delete did not close cached connection")
	}
	s.mu.Lock()
	_, unhealthy := s.loginErr[a.ID]
	s.mu.Unlock()
	if unhealthy {
		t.Fatal("successful delete retained account health")
	}
	assertAccountRestart(t, s, recs)
}

func TestAccountVerificationDoesNotBlockCommits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, recs := newOAuthService(t)
		defer s.Close()
		started, release := make(chan struct{}), make(chan struct{})
		s.verify = func(context.Context, *MailAccount) error {
			close(started)
			<-release
			return errGoogleRevoked
		}
		done := make(chan error, 1)
		go func() { _, err := s.AddAccount(addRequest("verifying@example.com")); done <- err }()
		<-started
		if _, err := s.upsertAccount(commitTestAccount("other")); err != nil {
			t.Fatal(err)
		}
		assertAccountRestart(t, s, recs)
		close(release)
		if err := <-done; !errors.Is(err, errGoogleRevoked) {
			t.Fatalf("verification: %v", err)
		}
		if len(committedAccounts(s)) != 1 {
			t.Fatal("rejected verification published an account")
		}
	})
}

func TestConcurrentAccountAddRechecksAddressAfterCommit(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failFirst], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, recs := newOAuthService(t)
				defer s.Close()
				started, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				s.store.recs = &accountWriteRecords{memRecords: recs, replace: func(ctx context.Context, rows []db.MailAccountRow) error {
					if calls.Add(1) == 1 {
						close(started)
						<-release
						if failFirst {
							return errAccountWrite
						}
					}
					return recs.ReplaceMailAccounts(ctx, rows)
				}}
				first, second := make(chan error, 1), make(chan error, 1)
				go func() { _, err := s.AddAccount(addRequest("same@example.com")); first <- err }()
				<-started
				go func() { _, err := s.AddAccount(addRequest("SAME@example.com")); second <- err }()
				synctest.Wait()
				close(release)
				if err := <-first; errors.Is(err, errAccountWrite) != failFirst {
					t.Fatalf("first add: %v", err)
				}
				err := <-second
				if failFirst && err != nil {
					t.Fatalf("second add: %v", err)
				}
				if !failFirst && !errors.Is(err, ErrAccountExists) {
					t.Fatalf("duplicate: %v", err)
				}
				if len(committedAccounts(s)) != 1 {
					t.Fatal("duplicate address or failed addition survived")
				}
				assertAccountRestart(t, s, recs)
			})
		})
	}
}

func TestRefreshRotationOwnsCommittedCredentials(t *testing.T) {
	s, recs := newOAuthService(t)
	input := commitTestAccount("base")
	if _, err := s.upsertAccount(input); err != nil {
		t.Fatal(err)
	}
	// Verification owns its candidate even if the provider returned credentials
	// identical to an existing account's; a later rejection must leave that row alone.
	pending := commitTestAccount("pending")
	*pending.OAuth = *input.OAuth
	s.rotateRefresh(pending.ID, pending.OAuth, "example-pending-refresh", s.accessEntry(pending.OAuth))
	if got := committedAccounts(s)["base"]; got.OAuth.RefreshToken != input.OAuth.RefreshToken {
		t.Fatal("uncommitted verification rotated another account's credentials")
	}
	assertAccountRestart(t, s, recs)
	// Mutating the caller's object, an account copy or an inbox snapshot cannot
	// change committed credentials or another operation's encryption input.
	input.OAuth.RefreshToken = "example-caller-change"
	a, _ := s.account("base")
	s.mu.Lock()
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	snapshot[0].OAuth.RefreshToken = "example-snapshot-change"
	if a.OAuth.RefreshToken != commitTestAccount("base").OAuth.RefreshToken {
		t.Fatal("caller changed committed credentials")
	}
	s.noteLogin("base", errGoogleRevoked)
	c := s.accessEntry(a.OAuth)
	queued, _ := s.account(a.ID)
	s.store.recs = &accountWriteRecords{memRecords: recs, replace: func(context.Context, []db.MailAccountRow) error { return errAccountWrite }}
	s.rotateRefresh(a.ID, a.OAuth, "example-next-refresh", c)
	assertAccountRestart(t, s, recs)
	if s.accessEntry(a.OAuth) != c {
		t.Fatal("failed rotation moved access cache")
	}
	s.store.recs = recs
	s.rotateRefresh(a.ID, a.OAuth, "example-next-refresh", c)
	assertAccountRestart(t, s, recs)
	if a.OAuth.RefreshToken != "example-next-refresh" || s.accessEntry(a.OAuth) != c {
		t.Fatal("successful rotation lost access cache")
	}
	if accounts, _ := s.ListAccounts(); len(accounts) != 1 || accounts[0].Status != "error" || accounts[0].Error != errGoogleRevoked.Error() {
		t.Fatal("refresh rotation cleared account health")
	}
	c.mu.Lock()
	c.token, c.expires = "example-cached-access", s.now().Add(time.Hour)
	c.mu.Unlock()
	s.googleHTTP = &http.Client{Transport: accountTokenTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected token endpoint request")
	})}
	if token, err := s.oauthToken(context.Background(), queued); err != nil || token != "example-cached-access" {
		t.Fatalf("queued account lost rotated access cache: %v", err)
	}

	stale, _ := s.account("base")
	replacement := commitTestAccount("new-id")
	replacement.Email = stale.Email
	if _, err := s.upsertAccount(replacement); err != nil {
		t.Fatal(err)
	}
	before := committedAccounts(s)
	var writes atomic.Int32
	s.store.recs = &accountWriteRecords{memRecords: recs, replace: func(context.Context, []db.MailAccountRow) error { writes.Add(1); return errAccountWrite }}
	s.rotateRefresh(stale.ID, stale.OAuth, "example-stale-refresh", c)
	if !reflect.DeepEqual(committedAccounts(s), before) || writes.Load() != 0 {
		t.Fatal("stale refresh overwrote reauthorization")
	}
	s.store.recs = recs
	if err := s.DeleteAccount("base"); err != nil {
		t.Fatal(err)
	}
	s.rotateRefresh("base", replacement.OAuth, "example-deleted-refresh", c)
	assertAccountRestart(t, s, recs)
	if len(committedAccounts(s)) != 0 {
		t.Fatal("stale refresh revived deleted account")
	}

	// A token rotated during verification belongs to the uncommitted candidate.
	unsaved := commitTestAccount("unsaved")
	s.rotateRefresh(unsaved.ID, unsaved.OAuth, "example-verification-refresh", c)
	if rows, _ := recs.MailAccounts(context.Background()); len(rows) != 0 {
		t.Fatal("verification persisted an account")
	}
	if _, err := s.upsertAccount(unsaved); err != nil {
		t.Fatal(err)
	}
	assertAccountRestart(t, s, recs)
}

func TestConcurrentAccountRepairsKeepCommittedCredentials(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "second-fails", true: "first-fails"}[failFirst], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, recs := newOAuthService(t)
				defer s.Close()
				if _, err := s.upsertAccount(commitTestAccount("base")); err != nil {
					t.Fatal(err)
				}
				started, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				s.store.recs = &accountWriteRecords{memRecords: recs, replace: func(ctx context.Context, rows []db.MailAccountRow) error {
					n := calls.Add(1)
					if n == 1 {
						close(started)
						<-release
					}
					if (n == 1 && failFirst) || (n == 2 && !failFirst) {
						return errAccountWrite
					}
					return recs.ReplaceMailAccounts(ctx, rows)
				}}
				repair := func(token string) error {
					a := commitTestAccount("replacement")
					a.Email, a.OAuth.RefreshToken = "BASE@example.com", token
					_, err := s.upsertAccount(a)
					return err
				}
				first, second := make(chan error, 1), make(chan error, 1)
				go func() { first <- repair("example-first-refresh") }()
				<-started
				if a, _ := s.account("base"); a.OAuth.RefreshToken != commitTestAccount("base").OAuth.RefreshToken {
					close(release)
					<-first
					t.Fatal("repair was published before persistence")
				}
				go func() { second <- repair("example-second-refresh") }()
				synctest.Wait()
				close(release)
				firstErr, secondErr := <-first, <-second
				if (failFirst && (!errors.Is(firstErr, errAccountWrite) || secondErr != nil)) ||
					(!failFirst && (firstErr != nil || !errors.Is(secondErr, errAccountWrite))) {
					t.Fatalf("repairs: %v, %v", firstErr, secondErr)
				}
				want := "example-first-refresh"
				if failFirst {
					want = "example-second-refresh"
				}
				a, err := s.account("base")
				if err != nil || a.OAuth.RefreshToken != want || a.Name != "Original name" || len(committedAccounts(s)) != 1 {
					t.Fatal("rejected repair replaced committed credentials or identity")
				}
				assertAccountRestart(t, s, recs)
			})
		})
	}
}
