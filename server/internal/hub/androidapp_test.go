package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAndroidAppRoutes(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	token := "jp_android-test"
	if err := h.store.CreatePairToken(context.Background(), hashToken(token), time.Now().Add(time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	handler := h.routes()
	request := func(path, credential, byteRange string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/app/android/"+path, nil)
		r.RemoteAddr = net.JoinHostPort(netip.AddrFrom4([4]byte{127, 0, 0, 1}).String(), "1")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		r.Header.Set("Range", byteRange)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"latest", "apk"} {
		if w := request(path, "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
		w := request(path, token, "")
		if w.Code != http.StatusNotFound || errorResponse(t, w)["code"] != "not_found" {
			t.Fatalf("missing %s: %d %s", path, w.Code, w.Body)
		}
	}
	apk := []byte("0123456789")
	sum := sha256.Sum256(apk)
	digest := hex.EncodeToString(sum[:])
	manifest, err := json.Marshal(map[string]any{
		"version_code": 42, "version_name": "1.2", "sha256": digest,
		"size": len(apk), "min_sdk": 26, "notes": "更新说明",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.cfg.dir, "apps", "android")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"latest.json": manifest, digest + ".apk": apk} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if w := request("latest", token, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "apk?sha256="+digest) {
		t.Fatalf("latest: %d %s", w.Code, w.Body)
	}

	// A cached manifest must keep downloading its own APK after a newer publication.
	newer := bytes.Replace(manifest, []byte(digest), []byte(strings.Repeat("a", 64)), 1)
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if w := request("apk?sha256="+digest, token, ""); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), apk) {
		t.Fatalf("pinned download after publication: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"../latest.json", strings.Repeat("g", 64), "abc"} {
		if w := request("apk?sha256="+url.QueryEscape(bad), token, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid digest: %d", w.Code)
		}
	}
	if w := request("apk?sha256="+strings.Repeat("b", 64), token, ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing pinned APK: %d", w.Code)
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	w := request("apk?token="+url.QueryEscape(token), "", "bytes=2-5")
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w.Header().Get("Content-Type") != "application/vnd.android.package-archive" {
		t.Fatalf("APK content type: %v", w.Header())
	}
}

func TestAndroidPairDownloadLink(t *testing.T) {
	p := pairCode{URL: "http://example.com", Token: "jp_example+&", ExpiresAt: time.Now().Add(time.Minute)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(p)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	cfg := &Config{Listen: ":" + port, PublicURL: p.URL + "/", Token: "master-example"}
	var out bytes.Buffer
	if err := cfg.printPair(context.Background(), false, &out, true); err != nil {
		t.Fatal(err)
	}
	want := p.URL + "/api/v1/app/android/apk?token=" + url.QueryEscape(p.Token)
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), p.pairURL()) || strings.Contains(out.String(), cfg.Token) {
		t.Fatal("Android link must share the temporary pairing credential")
	}
	out.Reset()
	if err := cfg.printPair(context.Background(), true, &out, true); err == nil || out.Len() != 0 {
		t.Fatal("Android legacy pairing must not expose the master credential")
	}
}
