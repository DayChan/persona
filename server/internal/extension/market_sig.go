package extension

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/i18n"
	"io"
	"net/url"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"
)

const (
	SigSigned     = "signed"
	SigUnsigned   = "unsigned"
	SigInvalid    = "invalid"
	SigKeyChanged = "key_changed" // Read old database states only; no trust-on-first-use policy.
)

type MarketSignature struct {
	State string `json:"state"`
	KeyID string `json:"key_id,omitempty"`
}

func (s MarketSignature) Bad() bool { return s.State == SigInvalid || s.State == SigKeyChanged }

// A catalog read fails in one of two ways, told apart with errors.Is and never by the message:
//   - ErrCatalogUnavailable: the catalog could not be read (network, timeout, an HTTP status other than 200, a
//     body over the limit). Nothing was learned about the catalog: the last verified one stays usable.
//   - ErrCatalogUnverified: a catalog was read and does not verify (not an envelope where a signature is required,
//     malformed, a bad signature, another signer). An official market whose catalog does not verify is quarantined.
var (
	ErrCatalogUnavailable = errors.New("market catalog unavailable")
	ErrCatalogUnverified  = errors.New("market catalog failed verification")
)

// catalogError keeps the reason's own text (and localization) and adds which kind of failure it is.
type catalogError struct{ kind, err error }

func (e *catalogError) Error() string   { return e.err.Error() }
func (e *catalogError) Unwrap() []error { return []error{e.kind, e.err} }

func catalogFailure(kind, err error) error {
	if err == nil || errors.Is(err, kind) {
		return err
	}
	return &catalogError{kind: kind, err: err}
}

func ParseSigningKey(s string) (ed25519.PublicKey, error) {
	b64, ok := strings.CutPrefix(strings.TrimSpace(s), "ed25519:")
	if !ok {
		return nil, i18n.Err("extension-market_security.public_key")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, i18n.Err("extension-market_security.public_key")
	}
	return ed25519.PublicKey(raw), nil
}

func KeyID(k ed25519.PublicKey) string {
	sum := sha256.Sum256(k)
	return hex.EncodeToString(sum[:8])
}

type catalogSignature struct {
	Alg   string `json:"alg"`
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"`
}

// IsOfficialMarket includes direct HTTPS catalog aliases, not just repository specs.
// The request location never determines which signing key to trust.
func IsOfficialMarket(spec string) bool {
	if Official.Is(spec) {
		return true
	}
	want, err := ParseMarket(Official.Spec)
	if err != nil || want.Kind != "github" {
		return false
	}
	got, err := ParseMarket(spec)
	if err != nil {
		return false
	}
	if got.Kind == "git" {
		u, err := url.Parse(got.URL)
		if err == nil && (strings.EqualFold(u.Hostname(), "github.com") || strings.EqualFold(u.Hostname(), "ssh.github.com")) {
			return strings.EqualFold(strings.TrimSuffix(strings.Trim(path.Clean(u.Path), "/"), ".git"), want.Owner+"/"+want.Repo)
		}
		return false
	}
	if got.Kind != "https" {
		return false
	}
	u, err := url.Parse(got.URL)
	if err != nil || u.Scheme != "https" || u.Port() != "" && u.Port() != "443" {
		return false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	ownerRepo := func(i int) bool {
		return len(p) > i+1 && strings.EqualFold(p[i], want.Owner) && strings.EqualFold(p[i+1], want.Repo)
	}
	switch strings.ToLower(u.Hostname()) {
	case "raw.githubusercontent.com", "raw.github.com":
		return len(p) >= 4 && ownerRepo(0) && p[len(p)-1] == IndexFile
	case "api.github.com":
		return len(p) == 5 && p[0] == "repos" && ownerRepo(1) && p[3] == "contents" && p[4] == IndexFile
	case "github.com":
		return len(p) >= 5 && ownerRepo(0) && (p[2] == "blob" || p[2] == "raw") && p[len(p)-1] == IndexFile
	}
	return false
}

// strictJSON rejects ambiguous objects and invalid UTF-8 before encoding/json can
// silently replace bytes. Signed payloads are validated too, without rewriting them.
func strictJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return i18n.Err("extension-market_security.catalog")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func() error
	value = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := k.(string)
					if !ok {
						return i18n.Err("extension-market_security.catalog")
					}
					// Generic maps are case sensitive, including configuration properties.
					if seen[name] {
						return i18n.Err("extension-market_security.catalog")
					}
					seen[name] = true
					if err := value(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := value(); err != nil {
						return err
					}
				}
			default:
				return i18n.Err("extension-market_security.catalog")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return i18n.Err("extension-market_security.catalog")
	}
	// encoding/json replaces unpaired escaped surrogates. Reject them explicitly.
	for i := 0; i+1 < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return i18n.Err("extension-market_security.catalog")
		}
		b, err := hex.DecodeString(string(raw[i+2 : i+6]))
		if err != nil {
			return err
		}
		n := uint16(b[0])<<8 | uint16(b[1])
		i += 5
		if n >= 0xdc00 && n <= 0xdfff {
			return i18n.Err("extension-market_security.catalog")
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+7 >= len(raw) || string(raw[i+1:i+3]) != "\\u" {
				return i18n.Err("extension-market_security.catalog")
			}
			b, err = hex.DecodeString(string(raw[i+3 : i+7]))
			if err != nil {
				return err
			}
			n = uint16(b[0])<<8 | uint16(b[1])
			if n < 0xdc00 || n > 0xdfff {
				return i18n.Err("extension-market_security.catalog")
			}
			i += 6
		}
	}
	return nil
}

// ParseCatalog unwraps the only signed format. Raw is the complete persisted
// document; Payload is exactly the decoded UTF-8 string, never trimmed or marshaled.
// Every failure is ErrCatalogUnverified.
func ParseCatalog(raw []byte, trusted string) (*Fetched, error) {
	f, err := parseCatalog(raw, trusted)
	if err != nil {
		return nil, catalogFailure(ErrCatalogUnverified, err)
	}
	return f, nil
}

func parseCatalog(raw []byte, trusted string) (*Fetched, error) {
	if len(raw) > IndexMost {
		return nil, i18n.Err("extension-market_security.catalog")
	}
	if err := strictJSON(raw); err != nil {
		return nil, i18n.Err("extension-market_security.catalog")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, i18n.Err("extension-market_security.catalog")
	}
	payload := raw
	var signatures []catalogSignature
	_, hasPayload := fields["payload"]
	_, hasSignatures := fields["signatures"]
	if hasPayload || hasSignatures {
		if !hasPayload || !hasSignatures || len(fields) != 2 {
			return nil, i18n.Err("extension-market_security.catalog")
		}
		var text string
		if string(fields["payload"]) == "null" || json.Unmarshal(fields["payload"], &text) != nil {
			return nil, i18n.Err("extension-market_security.catalog")
		}
		payload = []byte(text)
		if err := strictJSON(payload); err != nil {
			return nil, i18n.Err("extension-market_security.catalog")
		}
		if err := catalogFields(fields["signatures"], reflect.TypeFor[[]catalogSignature]()); err != nil {
			return nil, err
		}
		if json.Unmarshal(fields["signatures"], &signatures) != nil || len(signatures) == 0 || len(signatures) > 16 {
			return nil, i18n.Err("extension-market_security.catalog")
		}
		for _, s := range signatures {
			b, err := base64.StdEncoding.DecodeString(s.Sig)
			if s.Alg != "ed25519" || len(s.KeyID) != 16 || err != nil || len(b) != ed25519.SignatureSize {
				return nil, i18n.Err("extension-market_security.catalog")
			}
		}
	}
	if trusted != "" {
		key, err := ParseSigningKey(trusted)
		if err != nil {
			return nil, err
		}
		valid := false
		for _, s := range signatures {
			b, _ := base64.StdEncoding.DecodeString(s.Sig)
			if s.KeyID == KeyID(key) && ed25519.Verify(key, payload, b) {
				valid = true
			}
		}
		if !valid {
			return nil, i18n.Err("extension-market_security.signature")
		}
	}
	if err := catalogFields(payload, reflect.TypeFor[MarketIndex]()); err != nil {
		return nil, err
	}
	ix, warnings, err := ParseIndex(payload)
	if err != nil {
		return nil, err
	}
	if trusted != "" {
		for i := range ix.Extensions {
			e := &ix.Extensions[i]
			e.verifiedKey, e.verifiedEntry = trusted, entryFingerprint(e)
		}
	}
	return &Fetched{Index: ix, Warnings: warnings, Raw: append([]byte(nil), raw...), Payload: payload}, nil
}
