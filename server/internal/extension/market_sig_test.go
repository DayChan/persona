package extension

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// testKey signs the payload string, returning the complete catalog document.
func testKey(t *testing.T) (string, func([]byte) []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return "ed25519:" + base64.StdEncoding.EncodeToString(pub), func(payload []byte) []byte {
		b, _ := json.Marshal(map[string]any{"payload": string(payload), "signatures": []catalogSignature{{Alg: "ed25519", KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))}}})
		return b
	}
}

func TestCatalogSignatureExactPayload(t *testing.T) {
	key, sign := testKey(t)
	payload := []byte(" \n{\"market_version\":1,\"name\":\"Example 日本語\",\"extensions\":[]}\t\n")
	raw := sign(payload)
	f, err := ParseCatalog(raw, key)
	if err != nil || !bytes.Equal(f.Payload, payload) || !bytes.Equal(f.Raw, raw) {
		t.Fatalf("exact payload: %v", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(formatted.Bytes(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(bytes.Replace(raw, []byte("Example"), []byte("Changed"), 1), key); err == nil {
		t.Fatal("tampered payload accepted")
	}
	other, _ := testKey(t)
	if _, err := ParseCatalog(raw, other); err == nil {
		t.Fatal("wrong pin accepted")
	}
	if _, err := ParseCatalog(payload, key); err == nil {
		t.Fatal("official unsigned catalog accepted")
	}
	if _, err := ParseCatalog(payload, ""); err != nil {
		t.Fatal("unsigned private catalog refused")
	}
	// Unofficial signatures are metadata, never TOFU, even if the hint is absent.
	if _, err := ParseCatalog(raw, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRejectsAmbiguousDocuments(t *testing.T) {
	key, sign := testKey(t)
	valid := []byte(`{"market_version":1,"name":"Example","extensions":[]}`)
	for _, raw := range [][]byte{
		[]byte(`{"payload":"{}"}`),
		[]byte(`{"signatures":[]}`),
		[]byte(`{"payload":null,"signatures":[]}`),
		[]byte(`{"payload":"{}","payload":"{}","signatures":[]}`),
		[]byte(`{"market_version":1,"Market_Version":1,"name":"Example"}`),
		[]byte(`{"market_version":1,"name":"\ud800"}`),
		[]byte(`{"market_version":1,"name":"\udfff"}`),
		append([]byte(`{"market_version":1,"name":"`), append([]byte{0xff}, []byte(`"}`)...)...),
		sign([]byte(`{"market_version":1,"name":"First","name":"Second"}`)),
		[]byte(strings.Replace(string(sign(valid)), `"ed25519"`, `"rsa"`, 1)),
		[]byte(strings.Replace(string(sign(valid)), `"alg":"ed25519"`, `"alg":"ed25519","alg":"ed25519"`, 1)),
		append(sign(valid), []byte(`{}`)...),
	} {
		for _, pin := range []string{"", key} {
			if _, err := ParseCatalog(raw, pin); err == nil {
				t.Fatalf("ambiguous document accepted: %q", raw)
			}
		}
	}
	if _, err := ParseCatalog([]byte(`{"market_version":1,"name":"😀","extensions":[]}`), ""); err != nil {
		t.Fatal(err)
	}
}

func TestOfficialCatalogAliases(t *testing.T) {
	for _, spec := range []string{
		"github:nwverse/persona-extensions@branch",
		"git:https://github.com/nwverse/persona-extensions.git#branch",
		"git:https://github.com:443/nwverse/persona-extensions.git",
		"https://github.com/nwverse/persona-extensions/blob/main/persona-extensions.json",
		"https://raw.githubusercontent.com/nwverse/persona-extensions/branch/persona-extensions.json",
		"https://api.github.com/repos/nwverse/persona-extensions/contents/persona-extensions.json?ref=branch",
	} {
		if !IsOfficialMarket(spec) {
			t.Errorf("official alias not recognized: %s", spec)
		}
	}
	for _, spec := range []string{"github:other/persona-extensions", "https://raw.githubusercontent.com/other/persona-extensions/main/persona-extensions.json", "https://example.com/persona-extensions.json"} {
		if IsOfficialMarket(spec) {
			t.Errorf("unofficial alias recognized: %s", spec)
		}
	}
}
