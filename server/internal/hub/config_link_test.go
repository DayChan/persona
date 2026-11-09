package hub

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// hub.json's link section is read as written; a first run does not write one.
func TestLoadConfigReadsTheLinkHeartbeat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".persona", "hub.json")
	if b, _ := os.ReadFile(path); strings.Contains(string(b), `"link"`) {
		t.Fatalf("a first run wrote a link section: %s", b)
	}
	body := `{"listen":":1","token":"abc","machines":[],"link":{"ping_seconds":30,"ping_timeout_seconds":90}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Link != (LinkConfig{PingSeconds: 30, PingTimeoutSeconds: 90}) {
		t.Fatalf("link %+v", cfg.Link)
	}
}

// The heartbeat: hub.json's when it sets one (in range, the default for the half it leaves out), the protocol's
// otherwise; the manager is told only one that is set, so a hub without the section says nothing new on the wire.
func TestLinkHeartbeatFromHubJSON(t *testing.T) {
	for _, tc := range []struct {
		cfg            LinkConfig
		every, timeout time.Duration
		set            bool
	}{
		{LinkConfig{}, link.PingEvery, link.PingTimeout, false},
		{LinkConfig{PingSeconds: -1, PingTimeoutSeconds: -5}, link.PingEvery, link.PingTimeout, false},
		{LinkConfig{PingSeconds: 30, PingTimeoutSeconds: 90}, 30 * time.Second, 90 * time.Second, true},
		{LinkConfig{PingTimeoutSeconds: 60}, link.PingEvery, time.Minute, true},
		{LinkConfig{PingSeconds: 20}, 20 * time.Second, link.PingTimeout, true},
		{LinkConfig{PingSeconds: 1, PingTimeoutSeconds: 601}, time.Second, link.MaxPing, true},
		{LinkConfig{PingSeconds: math.MaxInt, PingTimeoutSeconds: math.MaxInt}, link.MaxPing, link.MaxPing, true},
	} {
		h := newTestHub(t)
		h.cfg.Link = tc.cfg
		every, timeout, set := h.linkPingTimes()
		if every != tc.every || timeout != tc.timeout || set != tc.set {
			t.Errorf("%+v: %s, %s, %v; want %s, %s, %v", tc.cfg, every, timeout, set, tc.every, tc.timeout, tc.set)
		}
		dc := h.devicesConfig()
		want := [2]int{}
		if tc.set {
			want = [2]int{int(tc.every.Milliseconds()), int(tc.timeout.Milliseconds())}
		}
		if got := [2]int{dc.LinkPingMS, dc.LinkPingTimeoutMS}; got != want {
			t.Errorf("%+v: the manager is told %v, want %v", tc.cfg, got, want)
		}
	}
	// A test's own heartbeat comes first.
	h := newTestHub(t)
	h.cfg.Link = LinkConfig{PingSeconds: 30, PingTimeoutSeconds: 90}
	h.linkPing = [2]time.Duration{200 * time.Millisecond, 2 * time.Second}
	if every, timeout, set := h.linkPingTimes(); every != 200*time.Millisecond || timeout != 2*time.Second || !set {
		t.Fatalf("with the test's: %s, %s, %v", every, timeout, set)
	}
}

// The web app's address Persona Link opens: public_url when it is https, its sub-path kept (a query dropped); nothing for
// a plain http one (the LAN address the apps reach the hub at) or none.
func TestDevicesConfigWebURL(t *testing.T) {
	for public, want := range map[string]string{
		"https://hub.example.com":            "https://hub.example.com",
		" https://hub.example.com/persona/ ": "https://hub.example.com/persona",
		"https://hub.example.com:8443/a%2Fb": "https://hub.example.com:8443/a%2Fb",
		"http://192.0.2.10:18660":            "",
		"https://user@hub.example.com":       "",
		"https://hub.example.com/?x=1":       "https://hub.example.com",
		"":                                   "",
	} {
		h := newTestHub(t)
		h.cfg.PublicURL = public
		if got := h.devicesConfig().WebURL; got != want {
			t.Errorf("public_url %q: web_url %q, want %q", public, got, want)
		}
	}
}

// hub.json's heartbeat end to end: the manager tells Persona Link (endpoints, right after the hello), and the hub keeps
// it on the link itself, so a link that stops answering is dropped after it.
func TestLinkHeartbeatReachesPersonaLink(t *testing.T) {
	h, m, srv := newLinkHub(t)
	h.cfg.Link = LinkConfig{PingSeconds: 1, PingTimeoutSeconds: 1}
	h.sendLinkEndpoints()
	eventually(t, 5*time.Second, "the manager has the heartbeat", func() bool {
		h.devices.mu.Lock()
		defer h.devices.mu.Unlock()
		return strings.Contains(string(h.devices.sent[devicesOwner]), `"link_ping_ms":1000`)
	})
	token, _ := linkToken(t, h, "laptop")
	c, _, err := dialLink(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello(t, c, "laptop")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg link.Message
	for msg.Type != "endpoints" {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("no endpoints: %v", err)
		}
		msg = link.Message{}
		_ = json.Unmarshal(b, &msg)
	}
	if msg.PingMS != 1000 || msg.PingTimeoutMS != 1000 {
		t.Fatalf("endpoints %+v", msg)
	}
	// And never read again: pongs are only sent while reading.
	eventually(t, 3*time.Second, "link up", func() bool { return linkVersion(m) != "" })
	eventually(t, 5*time.Second, "dead link dropped", func() bool { return linkVersion(m) == "" })
	eventually(t, 3*time.Second, "the machine says why", func() bool { // and what the ping ran into
		return strings.Contains(m.snapshot().Error, "no pong within 1s: ")
	})
}
