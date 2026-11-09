package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
)

// personad plugin on the hub's machine: install (showing what it may do), list, configure, a secret from stdin,
// uninstall; all over loopback with hub.json's token.
func TestExtensionCommand(t *testing.T) {
	h, store := extensionTestHub(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h.routes()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PERSONA_LISTEN", "")
	b, _ := json.Marshal(map[string]any{"listen": ln.Addr().String(), "token": h.cfg.Token})
	if err := os.WriteFile(filepath.Join(home, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := ExtensionCommand(context.Background(), args, strings.NewReader(stdin), &out, &errOut); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	pkg := notesPackage(t, "1.0.0")

	var out bytes.Buffer
	if err := ExtensionCommand(context.Background(), []string{"install", pkg}, strings.NewReader("n\n"), &out, &out); err == nil {
		t.Fatal("said no")
	}
	if !strings.Contains(out.String(), "在 hub 机器上运行程序") || !strings.Contains(out.String(), "notes.example.com") {
		t.Fatal("what it may do is shown first:\n" + out.String())
	}
	if got := run("y\n", "install", pkg); !strings.Contains(got, "needs_setup") || !strings.Contains(got, "API Key") {
		t.Fatal(got)
	}
	run("", "config", "example-notes", `folders=["a","b"]`)
	run("s3cret\n", "secret", "example-notes", "api_key")
	if store.secrets["example-notes"]["api_key"] == "" {
		t.Fatal("not kept")
	}
	waitLong(t, "its connector", func() bool {
		c := h.connectors.Get("examplenotes")
		return c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})
	if got := run("", "list"); !strings.Contains(got, "example-notes") || !strings.Contains(got, "running") {
		t.Fatal(got)
	}
	if got := run("", "show", "example-notes"); strings.Contains(got, "s3cret") || !strings.Contains(got, `"set": true`) {
		t.Fatal(got)
	}
	run("", "disable", "example-notes")
	run("", "enable", "example-notes")
	run("", "uninstall", "example-notes")
	if got := run("", "list"); strings.Contains(got, "example-notes") || !strings.Contains(got, cuaExtensionID) {
		t.Fatal("gone; the built-in ones stay:", got)
	}
}

// personad extension market token <id>: the token from stdin, the market read again with it; --clear takes it away.
func TestExtensionMarketTokenCommand(t *testing.T) {
	h, _, _ := marketHub(t)
	gh := &fakeGitHub{files: map[string][]byte{extension.IndexFile: marketIndex(t, "Private")}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: h.routes()}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PERSONA_LISTEN", "")
	b, _ := json.Marshal(map[string]any{"listen": ln.Addr().String(), "token": h.cfg.Token})
	if err := os.WriteFile(filepath.Join(home, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := ExtensionCommand(context.Background(), args, strings.NewReader(stdin), &out, &out)
		return out.String(), err
	}
	if out, err := run("", "market", "add", "github:example/plugins"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	id := extension.MarketID(extension.MarketSpec{Kind: "github", Owner: "example", Repo: "plugins"})
	gh.mu.Lock()
	gh.token = "ghp_secret"
	gh.mu.Unlock()
	if out, err := run("ghp_secret\n", "market", "token", id); err != nil || !strings.Contains(out, "已设 token") || strings.Contains(out, "ghp_secret") {
		t.Fatalf("set: %v %q", err, out)
	}
	if _, err := run("\n", "market", "token", id); err == nil {
		t.Fatal("an empty token was taken for --clear")
	}
	gh.mu.Lock()
	gh.token = ""
	gh.mu.Unlock()
	if out, err := run("", "market", "token", id, "--clear"); err != nil || !strings.Contains(out, "已去掉 token") {
		t.Fatalf("clear: %v %q", err, out)
	}
}

// fakeExtensionHub is a hub.json pointing at a server that counts the calls with the fixture's token and answers
// them with status and {}.
func fakeExtensionHub(t *testing.T, status int) *atomic.Int32 {
	t.Helper()
	const token = "extension-cli-args-token"
	requests := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token { // unrelated loopback probes
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PERSONA_LISTEN", "")
	b, err := json.Marshal(map[string]string{"listen": strings.TrimPrefix(srv.URL, "http://"), "token": token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return requests
}

// A word a command does not take is refused before anything reaches the hub: a mistyped --keep-data would otherwise
// delete the extension's data, and a second id would be silently ignored.
func TestExtensionCommandRefusesUnknownArguments(t *testing.T) {
	for _, tt := range []struct {
		args  []string
		stdin string
		want  string // in the error: the offending word
	}{
		{args: []string{"uninstall", "example-extension", "--keep-dta"}, want: "--keep-dta"},
		{args: []string{"uninstall", "example-extension", "other", "--keep-data"}, want: `"other"`},
		{args: []string{"disable", "example-extension", "--cascde"}, want: "--cascde"},
		{args: []string{"enable", "example-extension", "--keep-data"}, want: "--keep-data"},
		{args: []string{"list", "--json"}, want: "--json"},
		{args: []string{"list", "example-extension"}, want: `"example-extension"`},
		{args: []string{"show", "example-extension", "other"}, want: `"other"`},
		{args: []string{"install", "./notes", "-y"}, want: `"-y"`},
		{args: []string{"install", "./notes", "--yse"}, want: "--yse"},
		{args: []string{"upgrade", "example-extension", "./notes", "--force"}, want: "--force"},
		{args: []string{"secret", "example-extension", "api_key", "--delet"}, stdin: "s3cret\n", want: "--delet"},
		{args: []string{"secret", "example-extension", "api_key", "extra"}, stdin: "s3cret\n", want: `"extra"`},
		{args: []string{"secret", "example-extension", "api_key"}, stdin: "", want: "--delete"},
		{args: []string{"sandbox", "example-extension", "on", "extra"}, want: `"extra"`},
		{args: []string{"auth", "example-extension", "login", "extra"}, want: `"extra"`},
		{args: []string{"auto-upgrade", "on", "example-extension", "extra"}, want: `"extra"`},
		{args: []string{"rollback", "example-extension", "--yes"}, want: "--yes"},
		{args: []string{"attach", "example-extension"}, want: "attach <id> remote:"},
		{args: []string{"attach", "example-extension", "remote:rpc=/p.sock", "--force"}, want: "--force"},
		{args: []string{"detach", "example-extension", "other"}, want: `"other"`},
		{args: []string{"upgrades", "--all"}, want: "--all"},
		{args: []string{"search", "notes", "--json"}, want: "--json"},
		{args: []string{"config", "example-extension", "a=1", "--force"}, want: "--force"},
		{args: []string{"contributions", "example-extension", "extra"}, want: `"extra"`},
		{args: []string{"contribution", "example-extension", "connector/notes", "on", "extra"}, want: `"extra"`},
		{args: []string{"dev", "./notes", "extra"}, want: `"extra"`},
		{args: []string{"market", "add", "github:example/plugins", "--tokn"}, stdin: "ghp_x\n", want: "--tokn"},
		{args: []string{"market", "add", "github:example/plugins", "--token-from="}, want: "--token-from="},
		{args: []string{"market", "add", "github:example/plugins", "--token", "--token-from=other"}, stdin: "ghp_x\n", want: "--token-from="},
		{args: []string{"market", "add", "github:example/plugins", "github:example/more"}, want: `"github:example/more"`},
		{args: []string{"market", "token", "example", "--clr"}, stdin: "ghp_x\n", want: "--clr"},
		{args: []string{"market", "list", "--all"}, want: "--all"},
		{args: []string{"market", "set-spec", "example", "github:example/moved", "extra"}, want: `"extra"`},
		{args: []string{"market", "remove", "example", "--force"}, want: "--force"},
		{args: []string{"market", "refresh", "example", "other"}, want: `"other"`},
		{args: []string{"market", "bogus"}, want: `"market bogus"`},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			requests := fakeExtensionHub(t, http.StatusConflict) // a call that gets through fails, but is counted
			var stdout, stderr bytes.Buffer
			err := ExtensionCommand(context.Background(), tt.args, strings.NewReader(tt.stdin), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one naming %s", err, tt.want)
			}
			if got := requests.Load(); got != 0 {
				t.Errorf("sent %d requests to the hub, want none", got)
			}
		})
	}
}

// The documented options still reach the hub.
func TestExtensionCommandAcceptsDocumentedArguments(t *testing.T) {
	for _, tt := range []struct {
		args  []string
		stdin string
	}{
		{args: []string{"enable", "example-extension", "--cascade"}},
		{args: []string{"secret", "example-extension", "api_key", "--delete"}},
		{args: []string{"secret", "example-extension", "api_key"}, stdin: "s3cret\n"},
		{args: []string{"auth", "example-extension"}},
		{args: []string{"attach", "example-extension", "remote:rpc=/p.sock"}},
		{args: []string{"detach", "example-extension"}},
		{args: []string{"auto-upgrade", "on", "example-extension"}},
		{args: []string{"search", "two", "words"}},
		{args: []string{"market", "add", "github:example/plugins", "--token-from=other"}},
		{args: []string{"market", "add", "github:example/plugins", "--token"}, stdin: "ghp_x\n"},
		{args: []string{"market", "token", "example", "--clear"}},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			requests := fakeExtensionHub(t, http.StatusOK)
			var stdout, stderr bytes.Buffer
			if err := ExtensionCommand(context.Background(), tt.args, strings.NewReader(tt.stdin), &stdout, &stderr); err != nil {
				t.Fatalf("error = %v", err)
			}
			if requests.Load() == 0 {
				t.Error("nothing reached the hub")
			}
		})
	}
}

// A mistyped command is named as such even on a machine without hub.json.
func TestExtensionCommandUnknownWithoutHubConfig(t *testing.T) {
	t.Setenv("PERSONA_HOME", t.TempDir())
	for _, args := range [][]string{{"bogus"}, {"market", "bogus"}} {
		var stdout, stderr bytes.Buffer
		err := ExtensionCommand(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "unknown extension command") {
			t.Errorf("%v: error = %v, want unknown extension command", args, err)
		}
	}
}

func TestExtensionCommandUninstallWarnings(t *testing.T) {
	for _, tt := range []struct {
		name       string
		response   string
		status     int
		flags      []string
		query      string
		wantStderr string
		wantError  string
	}{
		{
			name:       "cleanup warnings preserve success",
			response:   `{"ok":true,"warnings":["data directory cleanup failed","package directory cleanup failed"]}`,
			flags:      []string{"--keep-data", "--cascade", "--members"},
			query:      "cascade=1&keep_data=1&members=1",
			wantStderr: "警告：data directory cleanup failed\n警告：package directory cleanup failed\n",
		},
		{name: "legacy response", response: `{"ok":true}`},
		{name: "null warnings", response: `{"ok":true,"warnings":null}`},
		{name: "empty warnings", response: `{"ok":true,"warnings":[]}`},
		{
			name:      "API error remains an error",
			response:  `{"error":"extension is required","warnings":["not a successful uninstall"]}`,
			status:    http.StatusConflict,
			wantError: "extension is required（HTTP 409）",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const token = "extension-cli-test-token"
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Ignore unrelated loopback probes without the fixture's token.
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				requests.Add(1)
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/extensions/example-extension/-/" || r.URL.RawQuery != tt.query {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			home := t.TempDir()
			t.Setenv("PERSONA_HOME", home)
			t.Setenv("PERSONA_LISTEN", "")
			b, err := json.Marshal(map[string]string{"listen": strings.TrimPrefix(srv.URL, "http://"), "token": token})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "hub.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			args := append([]string{"uninstall", "example-extension"}, tt.flags...)
			err = ExtensionCommand(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				if stdout.String() != "" {
					t.Errorf("failed uninstall printed success: %q", stdout.String())
				}
			} else {
				if err != nil {
					t.Fatalf("successful uninstall returned an error: %v", err)
				}
				if got, want := stdout.String(), "卸载了 example-extension\n"; got != want {
					t.Errorf("stdout = %q, want %q", got, want)
				}
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", got, tt.wantStderr)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("sent %d uninstall requests, want exactly one", got)
			}
		})
	}
}
