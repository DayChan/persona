package update

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func compatArtifact(t *testing.T, name string) (File, []byte) {
	t.Helper()
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	if _, err := z.Write([]byte("test binary")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := describe(path)
	if err != nil {
		t.Fatal(err)
	}
	return f, gz.Bytes()
}

func TestCompatDownloadAliasesBothDirections(t *testing.T) {
	for _, name := range []string{"personad-linux-amd64.gz", "jarvisd-linux-amd64.gz", "personad-wechat-linux-amd64.gz", "jarvisd-wechat-linux-amd64.gz"} {
		t.Run(name, func(t *testing.T) {
			f, data := compatArtifact(t, name)
			alias := artifactNames(name)[1]
			var calls []string
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path != "/"+alias {
					http.NotFound(w, r)
					return
				}
				w.Write(data)
			}))
			defer s.Close()
			out, err := Download(context.Background(), s.Client(), s.URL, f, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(out)
			if string(b) != "test binary" || filepath.Base(out) != strings.TrimSuffix(name, ".gz") {
				t.Fatalf("wrong output: %s %q", out, b)
			}
			want := "GET /" + name + "\nGET /" + alias
			if strings.Join(calls, "\n") != want {
				t.Fatalf("requests: %v", calls)
			}
		})
	}
}

func TestCompatDownloadMismatchDoesNotFallbackOrInstall(t *testing.T) {
	for _, damage := range []string{"compressed digest", "binary digest", "fallback digest"} {
		t.Run(damage, func(t *testing.T) {
			f, data := compatArtifact(t, "personad-linux-amd64.gz")
			if damage == "binary digest" {
				f.BinarySHA256 = strings.Repeat("0", 64)
			}
			var calls []string
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if damage == "fallback digest" && r.URL.Path == "/"+f.Name {
					http.NotFound(w, r)
					return
				}
				if damage != "binary digest" {
					data = append(data, 'x')
				}
				w.Write(data)
			}))
			defer s.Close()
			dir := t.TempDir()
			if _, err := Download(context.Background(), s.Client(), s.URL, f, dir); !errors.Is(err, errMismatch) {
				t.Fatalf("wrong error %v", err)
			}
			want := 1
			if damage == "fallback digest" {
				want = 2
			}
			if len(calls) != want {
				t.Fatalf("retried/fell back after bad digest: %v", calls)
			}
			if file, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(file) > 0 {
				t.Fatalf("partial files: %v", file)
			}
			if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(f.Name, ".gz"))); !os.IsNotExist(err) {
				t.Fatal("bad binary installed")
			}
		})
	}
}

func TestCompatMissingArtifactsOnlyOneAttemptPerName(t *testing.T) {
	f, _ := compatArtifact(t, "personad-linux-amd64.gz")
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.NotFound(w, r) }))
	defer s.Close()
	if _, err := Download(context.Background(), s.Client(), s.URL, f, t.TempDir()); !errors.Is(err, errNotFound) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("404 retries: %d", calls)
	}
}

func TestCompatManifestProgramKeysBothDirections(t *testing.T) {
	for _, name := range []string{"personad-wechat", legacyWechat} {
		m := Manifest{Version: "v1", Build: 1, Files: map[string]File{"linux-amd64": {Name: "jarvisd-linux-amd64.gz"}}, Programs: map[string]map[string]File{name: {"linux-amd64": {Name: name + "-linux-amd64.gz"}}}}
		b, _ := json.Marshal(m)
		got, err := ParseManifest(b)
		if err != nil || got.Programs["personad-wechat"]["linux-amd64"].Name != name+"-linux-amd64.gz" || got.Programs[legacyWechat]["linux-amd64"].Name != name+"-linux-amd64.gz" {
			t.Fatalf("%+v %v", got, err)
		}
	}
}

func TestCompatBuildManifestKeepsDaemonAliasesButOmitsRetiredPrograms(t *testing.T) {
	for _, name := range []string{"jarvisd", "personad"} {
		dir := t.TempDir()
		for _, program := range []string{name, name + "-wechat"} {
			_, data := compatArtifact(t, program+"-linux-amd64.gz")
			if err := os.WriteFile(filepath.Join(dir, program+"-linux-amd64.gz"), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		m, err := BuildManifest(dir, Manifest{Version: "v1", Build: 1})
		if err != nil || len(m.Files) != 1 || m.Files["linux-amd64"].Name != name+"-linux-amd64.gz" {
			t.Fatalf("%+v %v", m, err)
		}
		// Historical artifacts remain readable, but a new release does not republish the retired program.
		b, _ := json.Marshal(m)
		var baseline Manifest
		if err := json.Unmarshal(b, &baseline); err != nil {
			t.Fatal(err)
		}
		if len(baseline.Programs) != 0 {
			t.Fatal("new manifest republished a retired bundled program")
		}
	}
}

func TestCompatArtifactPathTraversalRejectedBeforeIO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	if _, err := Download(context.Background(), http.DefaultClient, "https://example.com", File{Name: "../outside.gz"}, dir); err == nil {
		t.Fatal("invalid artifact accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("invalid name had filesystem side effects")
	}
}
