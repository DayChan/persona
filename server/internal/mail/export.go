package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Moving an account to another manager (docs/extensions/mail.md, "Moving an account"): the other manager has its own
// secret.key, so the account goes as a whole with its secrets in plaintext (over the bus only) and is sealed again on
// arrival.

// ErrNoAccount: there is no account with that ID.
var ErrNoAccount = errors.New("no such mail account")

// exportedAccount is an account with its secrets, as ExportAccount gives it and ImportAccount takes it. The outer
// Password and OAuth win over MailAccount's (which never marshal).
type exportedAccount struct {
	MailAccount
	Password string      `json:"password,omitempty"`
	OAuth    *OAuthCreds `json:"oauth,omitempty"`
}

// ExportAccount gives the whole account (ID, name, servers, credentials) as JSON for ImportAccount on another
// manager. The result holds the secrets in plaintext: never log or store it.
func (s *Service) ExportAccount(id string) ([]byte, error) {
	s.mu.Lock()
	a, ok := s.accts[id]
	var cp MailAccount
	if ok {
		cp = *a.clone()
	}
	s.mu.Unlock()
	if !ok {
		return nil, ErrNoAccount
	}
	e := exportedAccount{MailAccount: cp, Password: cp.Password, OAuth: cp.OAuth}
	e.MailAccount.Error = "" // the old manager's view of its health, not the account's
	return json.Marshal(e)
}

// ImportAccount takes an account ExportAccount gave, keeping its ID, and saves it with its secrets sealed by this
// Service's key. It does not test the connections (the account was working where it came from); an address or an ID
// that is here already is refused with ErrAccountExists. The result has no secrets.
func (s *Service) ImportAccount(data []byte) (*MailAccount, error) {
	var e exportedAccount
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, errors.New("mail: the account to import is not valid JSON") // not err: it could quote the secrets
	}
	a := e.MailAccount
	a.Password, a.OAuth, a.Error = e.Password, e.OAuth, ""
	a.ID, a.Email = strings.TrimSpace(a.ID), strings.TrimSpace(a.Email)
	if a.ID == "" || a.Email == "" {
		return nil, errors.New("mail: the account to import needs an id and an email")
	}
	if a.Auth == AuthOAuth2 && a.OAuth == nil {
		return nil, errors.New("mail: the account to import signs in with OAuth but has no credentials")
	}
	if a.Status == "" {
		a.Status = "ok"
	}

	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	if _, ok := s.accts[a.ID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("account %s %w", a.ID, ErrAccountExists)
	}
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, a.Email) {
			s.mu.Unlock()
			return nil, accountExists(a.Email)
		}
	}
	s.mu.Unlock()

	if err := s.commitAccount(a.ID, &a, false, nil); err != nil {
		return nil, err
	}
	return a.public(), nil
}
