package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// Records is where accounts are persisted: Postgres (internal/db) in production, memory in tests.
// The mail package encrypts secrets before they reach it, with a key that never leaves dataDir.
type Records interface {
	MailAccounts(ctx context.Context) ([]db.MailAccountRow, error)
	ReplaceMailAccounts(ctx context.Context, rows []db.MailAccountRow) error
}

// store keeps accounts in Records with their secrets encrypted: with the manager's secret.key, and while a rotation
// keeps them, its old keys for reading (secretkey.NewWithFallback).
type store struct {
	recs Records
	box  *secretkey.Box
	mu   sync.Mutex
}

// accountFile is the legacy on-disk JSON shape (mail_accounts.json), imported once into Records.
type accountFile struct {
	Accounts []accountRecord `json:"accounts"`
}

type accountRecord struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	Provider     string `json:"provider"`
	Auth         string `json:"auth"`
	Username     string `json:"username"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPSecurity string `json:"imap_security,omitempty"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPSecurity string `json:"smtp_security"`
	Status       string `json:"status,omitempty"`

	// PasswordEnc holds the IMAP/SMTP password, encrypted.
	PasswordEnc string `json:"password_enc,omitempty"`
	// OAuthEnc holds the OAuthCreds JSON of a Sign in with Google account, encrypted.
	OAuthEnc string `json:"oauth_enc,omitempty"`
}

// openStore loads (or creates) the key at keyPath and imports a legacy dataDir/mail_accounts.json into recs.
func openStore(dataDir, keyPath string, recs Records) (*store, error) {
	if dataDir == "" {
		return nil, errors.New("mail: data dir is empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mail: create data dir: %w", err)
	}
	box := secretkey.NewWithFallback(keyPath)
	if _, err := box.Key(); err != nil { // read or made now: a key that does not read stops the service at its start
		return nil, fmt.Errorf("mail: secret key: %w", err)
	}
	st := &store{recs: recs, box: box}
	if err := st.importLegacy(filepath.Join(dataDir, "mail_accounts.json")); err != nil {
		return nil, err
	}
	return st, nil
}

// importLegacy moves accounts from the old JSON file into Records (only when Records is still empty),
// then renames the file so it is not imported twice.
func (s *store) importLegacy(path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mail: read legacy accounts: %w", err)
	}
	existing, err := s.recs.MailAccounts(context.Background())
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		var f accountFile
		if err := json.Unmarshal(b, &f); err != nil {
			return fmt.Errorf("mail: parse legacy accounts: %w", err)
		}
		rows := make([]db.MailAccountRow, 0, len(f.Accounts))
		for _, r := range f.Accounts {
			row, err := r.row()
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
		if err := s.recs.ReplaceMailAccounts(context.Background(), rows); err != nil {
			return fmt.Errorf("mail: import legacy accounts: %w", err)
		}
	}
	return os.Rename(path, path+".migrated")
}

// row converts a record (secrets already encrypted) to its database shape.
func (r accountRecord) row() (db.MailAccountRow, error) {
	pw, oauth := r.PasswordEnc, r.OAuthEnc
	r.PasswordEnc, r.OAuthEnc = "", ""
	data, err := json.Marshal(r)
	if err != nil {
		return db.MailAccountRow{}, err
	}
	return db.MailAccountRow{ID: r.ID, Email: r.Email, Data: data, PasswordEnc: pw, OAuthEnc: oauth}, nil
}

func (s *store) encrypt(plain []byte) (string, error) { return s.box.Seal(string(plain)) }

func (s *store) decrypt(enc string) ([]byte, error) {
	if enc == "" {
		return nil, nil
	}
	plain, err := s.box.Open(enc)
	if err != nil {
		return nil, fmt.Errorf("mail: open secret: %w", err)
	}
	return []byte(plain), nil
}

// load reads every account. A missing file is not an error.
func (s *store) load() ([]*MailAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *store) loadLocked() ([]*MailAccount, error) {
	rows, err := s.recs.MailAccounts(context.Background())
	if err != nil {
		return nil, fmt.Errorf("mail: load accounts: %w", err)
	}
	var f accountFile
	for _, row := range rows {
		var r accountRecord
		if err := json.Unmarshal(row.Data, &r); err != nil {
			return nil, fmt.Errorf("mail: parse account %s: %w", row.ID, err)
		}
		r.ID, r.PasswordEnc, r.OAuthEnc = row.ID, row.PasswordEnc, row.OAuthEnc
		f.Accounts = append(f.Accounts, r)
	}
	accounts := make([]*MailAccount, 0, len(f.Accounts))
	for _, r := range f.Accounts {
		a := r.account()
		if r.PasswordEnc != "" {
			plain, err := s.decrypt(r.PasswordEnc)
			if err != nil {
				return nil, fmt.Errorf("mail: decrypt password for %s: %w", r.Email, err)
			}
			a.Password = string(plain)
		}
		if r.OAuthEnc != "" && a.Auth == AuthOAuth2 {
			plain, err := s.decrypt(r.OAuthEnc)
			if err != nil {
				return nil, fmt.Errorf("mail: decrypt oauth for %s: %w", r.Email, err)
			}
			a.OAuth = &OAuthCreds{}
			if err := json.Unmarshal(plain, a.OAuth); err != nil {
				return nil, fmt.Errorf("mail: parse oauth for %s: %w", r.Email, err)
			}
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func (r accountRecord) account() *MailAccount {
	a := &MailAccount{
		ID:           r.ID,
		Name:         r.Name,
		Email:        r.Email,
		Provider:     r.Provider,
		Auth:         r.Auth,
		Username:     r.Username,
		IMAPHost:     r.IMAPHost,
		IMAPPort:     r.IMAPPort,
		IMAPSecurity: r.IMAPSecurity,
		SMTPHost:     r.SMTPHost,
		SMTPPort:     r.SMTPPort,
		SMTPSecurity: r.SMTPSecurity,
		Status:       r.Status,
	}
	if a.Status == "" {
		a.Status = "ok"
	}
	return a
}

// save replaces every account atomically in Records, encrypting the stored
// secrets again with a fresh nonce. The caller owns the detached snapshot.
func (s *store) save(accounts []*MailAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := accountFile{Accounts: make([]accountRecord, 0, len(accounts))}
	for _, a := range accounts {
		r := accountRecord{
			ID:           a.ID,
			Name:         a.Name,
			Email:        a.Email,
			Provider:     a.Provider,
			Auth:         a.Auth,
			Username:     a.Username,
			IMAPHost:     a.IMAPHost,
			IMAPPort:     a.IMAPPort,
			IMAPSecurity: a.IMAPSecurity,
			SMTPHost:     a.SMTPHost,
			SMTPPort:     a.SMTPPort,
			SMTPSecurity: a.SMTPSecurity,
			Status:       a.Status,
		}
		if a.Password != "" {
			enc, err := s.encrypt([]byte(a.Password))
			if err != nil {
				return fmt.Errorf("mail: encrypt password: %w", err)
			}
			r.PasswordEnc = enc
		}
		if a.OAuth != nil {
			plain, err := json.Marshal(a.OAuth)
			if err != nil {
				return err
			}
			enc, err := s.encrypt(plain)
			if err != nil {
				return fmt.Errorf("mail: encrypt oauth: %w", err)
			}
			r.OAuthEnc = enc
		}
		f.Accounts = append(f.Accounts, r)
	}

	rows := make([]db.MailAccountRow, 0, len(f.Accounts))
	for _, r := range f.Accounts {
		row, err := r.row()
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if err := s.recs.ReplaceMailAccounts(context.Background(), rows); err != nil {
		return fmt.Errorf("mail: save accounts: %w", err)
	}
	return nil
}

// normalizeEmail lowercases the address and trims spaces.
func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
