package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
)

// assistant.mode: child (or nothing) the hub starts the built-in manager; service it does not; one this version does
// not know (a later version's, a typo) starts none, so a rollback or a typo never starts a second one next to a
// service.
func TestAssistantMode(t *testing.T) {
	for _, c := range []struct {
		mode   string
		starts bool
		bad    bool
	}{{"", true, false}, {"child", true, false}, {"service", false, false}, {"Service", false, false}, {"services", false, false}} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
		if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"token": "t", "assistant": map[string]any{"mode": c.mode}})
		if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig()
		if c.bad {
			if err == nil || !strings.Contains(err.Error(), "assistant.mode") {
				t.Errorf("mode %q: %v", c.mode, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("mode %q: %v", c.mode, err)
		}
		if got := cfg.Assistant.StartsBuiltin(); got != c.starts {
			t.Errorf("mode %q: starts %v", c.mode, got)
		}
		if got := cfg.Assistant.ServiceBound(); got != (c.mode == "service") {
			t.Errorf("mode %q: service %v", c.mode, got)
		}
	}
	if (AssistantProfile{External: true}).StartsBuiltin() || (AssistantProfile{External: true, Mode: "service"}).ServiceBound() {
		t.Error("external starts it, or is a bound service")
	}
}

// In service mode the hub starts no built-in manager and makes it no token.
func TestServiceModeStartsNoBuiltinManager(t *testing.T) {
	h, _, store := managersHub(t)
	h.cfg.Assistant.Mode = AssistantModeService
	var started atomic.Bool
	old := assistantCommand
	assistantCommand = func(context.Context) (*exec.Cmd, error) {
		started.Store(true)
		return nil, errors.New("not in this test")
	}
	defer func() { assistantCommand = old }()
	before, _ := store.BusIdentityByID(context.Background(), assistantwire.AssistantID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h.superviseAssistant(ctx) // returns at once
	if ctx.Err() != nil || started.Load() {
		t.Fatalf("started one (%v), or waited (%v)", started.Load(), ctx.Err())
	}
	after, _ := store.BusIdentityByID(context.Background(), assistantwire.AssistantID)
	if (before == nil) != (after == nil) || before != nil && !before.CreatedAt.Equal(after.CreatedAt) {
		t.Fatal("made the built-in manager a token")
	}
}

// manager-mode shows and sets hub.json's assistant.mode, keeping the rest of the file.
func TestManagerMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"token":"t","assistant":{"notes":"n"},"listen":":1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	show := func() string {
		var out bytes.Buffer
		if err := managerMode(dir, nil, &out); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	if got := show(); got != "child" {
		t.Fatalf("default: %q", got)
	}
	var out bytes.Buffer
	if err := managerMode(dir, []string{"service"}, &out); err != nil || !strings.Contains(out.String(), "persona-hub switch") {
		t.Fatalf("set: %v %q", err, out.String())
	}
	if got := show(); got != "service" {
		t.Fatalf("after set: %q", got)
	}
	var raw map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "hub.json"))
	if err := json.Unmarshal(b, &raw); err != nil || raw["listen"] != ":1" || raw["token"] != "t" ||
		raw["assistant"].(map[string]any)["notes"] != "n" || raw["assistant"].(map[string]any)["mode"] != "service" {
		t.Fatalf("hub.json: %s %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "hub.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	if err := managerMode(dir, []string{"child"}, &out); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "hub.json"))
	if strings.Contains(string(b), `"mode"`) || show() != "child" {
		t.Fatalf("child: %s", b)
	}
	if err := managerMode(dir, []string{"bogus"}, &out); err == nil {
		t.Fatal("took bogus")
	}
}

// A code is traded only by the join of its kind (the built-in manager's by join --builtin, every other by the others:
// 409, unspent); the built-in manager's only while it is a bound service here (409, also when external, unspent), and
// only from this hub's machine or its local network (403, unspent); a lookup that fails refuses (503). Then it gives
// the built-in manager a new token. Its pending code does not show in the roster as a manager being installed.
func TestBuiltinManagerJoin(t *testing.T) {
	h, _, store := managersHub(t)
	mux := h.routes()
	code, _, err := h.newInstallCode(context.Background(), assistantwire.AssistantID, "助手")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := h.newInstallCode(context.Background(), "m-0a0b0c", "工作")
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Managers []managerInfo }
	_ = json.Unmarshal(securityRequest(mux, "GET", "/api/v1/managers", h.cfg.Token, "", "127.0.0.1:1", false).Body.Bytes(), &list)
	for _, m := range list.Managers {
		if m.ID == assistantwire.AssistantID && m.Installing != nil {
			t.Fatalf("the built-in manager's code shows as an install: %+v", m)
		}
	}
	builtin := `{"code":"` + code + `","builtin":true}`
	h.cfg.Assistant.Mode = AssistantModeChild
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", builtin, "127.0.0.1:1", false), 409, "builtin_not_service")
	h.cfg.Assistant.Mode, h.cfg.Assistant.External = AssistantModeService, true
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", builtin, "127.0.0.1:1", false), 409, "builtin_not_service")
	h.cfg.Assistant.External = false
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", `{"code":"`+code+`"}`, "127.0.0.1:1", false), 409, "builtin_code")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", `{"code":"`+other+`","builtin":true}`, "127.0.0.1:1", false), 409, "not_builtin_code")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", builtin, "198.51.100.1:1", true), 403, "builtin_local_only")
	// the lookup failing: refused, not traded
	real, base := h.bus.idents, h.managerStore()
	h.bus.idents = &failingLookup{managerStore: base}
	if w := securityRequest(mux, "POST", bus.JoinPath, "", builtin, "127.0.0.1:1", false); w.Code != 503 ||
		strings.Contains(w.Body.String(), "database is away") {
		t.Fatalf("lookup failing: %d %s", w.Code, w.Body.String())
	}
	// the trade failing in the store: refused with nothing of the store's error in the answer
	h.bus.idents = &failingRedeem{managerStore: base}
	if w := securityRequest(mux, "POST", bus.JoinPath, "", builtin, "127.0.0.1:1", false); w.Code != 503 ||
		strings.Contains(w.Body.String(), "user=") || strings.Contains(w.Body.String(), "database is away") {
		t.Fatalf("redeem failing: %d %s", w.Code, w.Body.String())
	}
	h.bus.idents = real
	w := securityRequest(mux, "POST", bus.JoinPath, "", builtin, "[fd00::21]:1", false)
	requireCode(t, w, 200, "")
	var got struct{ ID, Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ID != assistantwire.AssistantID || got.Token == "" {
		t.Fatalf("join: %s %v", w.Body.String(), err)
	}
	b, err := store.BusIdentityByHash(context.Background(), hashToken(got.Token))
	if err != nil || b == nil || b.ID != assistantwire.AssistantID {
		t.Fatalf("identity %+v %v", b, err)
	}
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", builtin, "127.0.0.1:1", false), 401, "bad_install_code")
	// the other code was never spent by the refusals: its own join (no builtin) from the internet over TLS trades it
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", `{"code":"`+other+`"}`, "198.51.100.1:1", true), 200, "")
}

// failingRedeem is a store whose trade fails the way pgx does, naming the database.
type failingRedeem struct{ managerStore }

func (failingRedeem) RedeemManagerInstallCode(context.Context, string, string) (*db.ManagerInstallCode, string, error) {
	return nil, "", errors.New("failed to connect to `user=persona database=persona`: the database is away")
}

// failingLookup is a store whose code lookup fails.
type failingLookup struct{ managerStore }

func (failingLookup) ManagerInstallCodeByHash(context.Context, string) (*db.ManagerInstallCode, error) {
	return nil, errors.New("the database is away")
}

// assistant-token would replace the bound service's token: refused in service mode.
func TestAssistantTokenRefusedInServiceMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte(`{"token":"t","assistant":{"mode":"service"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := AssistantToken(context.Background(), &out); err == nil || !strings.Contains(err.Error(), "manager-code") {
		t.Fatalf("service mode: %v", err)
	}
}

// manager-code prints a code only in service mode.
func TestManagerCodeNeedsServiceMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ManagerCode(context.Background(), &out); err == nil || !strings.Contains(err.Error(), "manager-mode service") {
		t.Fatalf("child mode: %v", err)
	}
}

// The CLI runs without a user's profile: its expiry names UTC and never depends on the host's zone.
func TestManagerCodeOutputUsesExplicitUTC(t *testing.T) {
	until := time.Date(2026, time.November, 8, 0, 5, 0, 0, time.FixedZone("input", 8*60*60))
	var out bytes.Buffer
	if err := writeManagerCode(&out, "example-code", until, ":18660"); err != nil {
		t.Fatal(err)
	}
	want := "example-code\n\nGood until 2026-11-07 16:05 UTC, once. On this machine (where the built-in manager's service runs), with " +
		"the code in the environment, where other users of the machine cannot read it:\n" +
		"  PERSONA_MANAGER_CODE=<the code> persona-manager join --builtin http://127.0.0.1:18660 --install\n"
	if got := out.String(); got != want {
		t.Fatalf("manager-code output = %q; want %q", got, want)
	}
}

func TestManagerCodeOutputReturnsWriteFailure(t *testing.T) {
	want := errors.New("output unavailable")
	if err := writeManagerCode(failedManagerCodeWriter{want}, "example-code", time.Time{}, ":18660"); !errors.Is(err, want) {
		t.Fatalf("write error = %v; want %v", err, want)
	}
}

type failedManagerCodeWriter struct{ err error }

func (w failedManagerCodeWriter) Write([]byte) (int, error) { return 0, w.err }

// MintBuiltinCode (a container's supervisor): the built-in manager's code from the hub's database, only in service
// mode; VoidBuiltinCode voids it while it is still good.
func TestMintAndVoidBuiltinCode(t *testing.T) {
	_, _, store := managersHub(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	t.Setenv("PERSONA_DATABASE_URL", os.Getenv("PERSONA_TEST_DATABASE_URL"))
	write := func(mode string) {
		if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte(`{"token":"t","assistant":{"mode":"`+mode+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	write("child")
	if _, err := MintBuiltinCode(ctx); err == nil {
		t.Fatal("a code in child mode")
	}
	write("service")
	code, err := MintBuiltinCode(ctx)
	if err != nil || code == "" {
		t.Fatalf("code %q, err %v", code, err)
	}
	if c, err := store.ManagerInstallCodeByHash(ctx, hashToken(code)); err != nil || c == nil || c.ManagerID != assistantwire.AssistantID || !c.ExpiresAt.After(time.Now()) {
		t.Fatalf("the code in the database: %+v %v", c, err)
	}
	if err := VoidBuiltinCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	if voided, err := store.ExpireManagerInstallCode(ctx, hashToken(code)); err != nil || voided {
		t.Fatalf("still good after VoidBuiltinCode: %v %v", voided, err)
	}
}
