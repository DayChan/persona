package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// The single-center mesh end to end (docs/archive/operations/plan-hub-center.md): hubs in this process on 127.0.0.1 over TLS, nodes in
// the process, apps on WebSockets, a connector played by a TLS server here.

func init() {
	devices.ShortenTimings(200*time.Millisecond, 300*time.Millisecond, 2*time.Second) // the manager's
	revocationResendEvery = 200 * time.Millisecond
	credsHandoverRetryEvery = 200 * time.Millisecond
}

// joinHubs has a use b's code: a (and its group) joins b's group.
func joinHubs(t *testing.T, a, b *testHub) {
	t.Helper()
	pairHubs(t, a, b)
}

func (mh *testHub) waitCenter(t *testing.T, center string) {
	t.Helper()
	eventually(t, 20*time.Second, mh.h.cfg.Name+" takes "+shortID(center)+" for the center", func() bool {
		return mh.h.centerID() == center
	})
}

func (mh *testHub) waitReachable(t *testing.T, target string) {
	t.Helper()
	eventually(t, 20*time.Second, mh.h.cfg.Name+" sees "+shortID(target)+" reachable", func() bool {
		return mh.h.meshView().reachable(target)
	})
}

// Joining makes one center; an app on either hub sees and works every computer, those of the member through it.
func TestCenterMemberForwardsEverything(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	lap := newMeshNode("uid-lap", "lap", "s2")
	box.resident, lap.resident = true, true
	center := startTestHub(t, hubB, "Office", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	member := startTestHub(t, hubA, "家里", []MachineConfig{{ID: "lap", Name: "Laptop", Kind: "link"}}, []*meshNode{lap})
	// A device paired with the member before it joined keeps working afterwards.
	oldTok, _ := member.deviceToken(t, roleFull)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	if member.h.isCenter() || !center.h.isCenter() {
		t.Fatal("two centers")
	}
	tok, _ := center.deviceToken(t, roleFull)

	// Both machines on the center, the member's through it; the member drives none.
	eventually(t, 15*time.Second, "both machines online on the center", func() bool {
		ms := center.machines(t, tok)
		return len(ms) == 2 && ms["box"].Online && ms["lap"].Online
	})
	ms := center.machines(t, tok)
	if r := ms["lap"].Route; r == nil || r.Via != viaHub || ms["lap"].HubID != hubA {
		t.Fatalf("lap's route %+v hub %s", r, ms["lap"].HubID)
	}
	if r := ms["box"].Route; r == nil || r.Via != "ssh" || ms["box"].HubID != hubB {
		t.Fatalf("box's route %+v hub %s", r, ms["box"].HubID)
	}
	eventually(t, 5*time.Second, "the member drives none of its computers itself", func() bool {
		return !member.h.machineByID("lap").isOnline()
	})

	// Through the member: the same list, with the credential the member issued (handed to the center).
	eventually(t, 10*time.Second, "the member's credential works through the member", func() bool {
		code, _ := member.do(t, "GET", "/api/v1/machines", oldTok, "")
		return code == 200
	})
	if mm := member.machines(t, oldTok); len(mm) != 2 || !mm["lap"].Online || !mm["box"].Online {
		t.Fatalf("through the member: %+v", mm)
	}
	// ...and on the center directly.
	if code, b := center.do(t, "GET", "/api/v1/machines", oldTok, ""); code != 200 {
		t.Fatalf("the member's credential on the center: %d %s", code, b)
	}

	// Hello on the member's WebSocket is the center's: its hub_id, and the center marked.
	a := dialApp(t, member, oldTok)
	hello, _ := a.wait(t, "hello", 0, typeIs("hello"))
	if hello["hub_id"] != hubB {
		t.Fatalf("hello from %v", hello["hub_id"])
	}
	hubs := hello["mesh"].(map[string]any)["hubs"].([]any)
	centers := 0
	for _, x := range hubs {
		if x.(map[string]any)["center"] == true {
			centers++
			if x.(map[string]any)["hub_id"] != hubB {
				t.Fatalf("center %v", x)
			}
		}
	}
	if len(hubs) != 2 || centers != 1 {
		t.Fatalf("mesh.hubs %v", hubs)
	}

	// Subscribe to the member's computer through the member: backlog, then live items.
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "lap", "session_id": "s2"})
	a.wait(t, "lap's backlog", 0, hasItem("hi from s2"))
	lap.addItem("s2", "live from lap")
	a.wait(t, "a live item", 0, hasItem("live from lap"))

	// Send a message and read a directory, through the member.
	if code, b := member.do(t, "POST", "/api/v1/sessions/lap/s2/messages", oldTok, `{"text":"hello lap"}`); code != 200 {
		t.Fatalf("send: %d %s", code, b)
	}
	eventually(t, 5*time.Second, "the message reached lap", func() bool {
		return strings.Contains(strings.Join(lap.sentTexts(), "|"), "hello lap")
	})
	if code, b := member.do(t, "GET", "/api/v1/machines/lap/fs?path=/home/user", oldTok, ""); code != 200 || !strings.Contains(string(b), "notes.txt") {
		t.Fatalf("fs: %d %s", code, b)
	}

	// An interaction on the member's computer: the app hears it through the member and answers it there.
	ixDone := make(chan proto.Resolution, 1)
	go func() {
		var res proto.Resolution
		_ = lap.current().p.Call(context.Background(), "interaction.create", map[string]any{"interaction": proto.Interaction{
			ID: "ix-lap", SessionID: "s2", Kind: proto.IxPermission, ToolName: "Bash", Summary: "ls",
			CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}}, &res)
		ixDone <- res
	}()
	a.wait(t, "the interaction", 0, func(m map[string]any) bool {
		ix, _ := m["interaction"].(map[string]any)
		return m["type"] == "interaction" && ix != nil && ix["id"] == "ix-lap" && ix["machine_id"] == "lap"
	})
	if code, b := member.do(t, "POST", "/api/v1/interactions/ix-lap/resolve", oldTok, `{"allow":true}`); code != 200 {
		t.Fatalf("resolve: %d %s", code, b)
	}
	eventually(t, 20*time.Second, "the answer reaches lap's node", func() bool {
		select {
		case res := <-ixDone:
			if res.Allow == nil || !*res.Allow {
				t.Fatalf("resolution %+v", res)
			}
			return true
		default:
			return false
		}
	})

	// The terminal of the member's computer, driven through the member.
	a.send(t, map[string]any{"type": "terminal_subscribe", "machine_id": "lap", "session_id": "s2", "control": map[string]any{"cols": 80, "rows": 24}})
	a.wait(t, "terminal control", 0, typeIs("terminal_control", "machine_id", "lap", "state", "active"))

	// Unknown credentials are refused through the member too.
	if code, _ := member.do(t, "GET", "/api/v1/machines", "jd_nope", ""); code != 401 {
		t.Fatalf("unknown credential through the member: %d", code)
	}

	// The admin view: roles.
	sum := member.h.meshSummary()
	if sum["role"] != "member" || sum["center"].(map[string]any)["hub_id"] != hubB {
		t.Fatalf("member summary %v", sum)
	}
	if center.h.meshSummary()["role"] != "center" {
		t.Fatal("the center's summary")
	}
}

// The center cannot be reached: a member answers 503 center_unreachable and does not take over.
func TestCenterUnreachable(t *testing.T) {
	center := startTestHub(t, hubB, "Office", nil, nil)
	member := startTestHub(t, hubA, "家里", nil, nil)
	tok, _ := member.deviceToken(t, roleFull)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.stop()
	eventually(t, 10*time.Second, "the member sees the center gone", func() bool { return !member.h.centerReachable() })
	code, b := member.do(t, "GET", "/api/v1/machines", tok, "")
	if code != 503 || !strings.Contains(string(b), "center_unreachable") || !strings.Contains(string(b), "Office") {
		t.Fatalf("with the center gone: %d %s", code, b)
	}
	time.Sleep(time.Second)
	if member.h.isCenter() {
		t.Fatal("the member made itself the center")
	}
	// Its own routes still answer.
	if code, _ := member.do(t, "GET", "/api/v1/health", "", ""); code != 200 {
		t.Fatalf("health: %d", code)
	}
}

// The headers a hub opens a node stream with mean nothing from outside: a request that carries them is still an app's,
// and without a credential a member answers it 401 like any other (docs/server/mesh.md "Security boundary").
func TestCenterMemberDropsNodeStreamHeaders(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	lap := newMeshNode("uid-lap", "lap", "s2")
	box.resident, lap.resident = true, true
	center := startTestHub(t, hubB, "Office", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	member := startTestHub(t, hubA, "家里", []MachineConfig{{ID: "lap", Name: "Laptop", Kind: "link"}}, []*meshNode{lap})
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	tok, _ := center.deviceToken(t, roleFull)
	// The way to both computers is there: a refusal below is not a route still settling.
	eventually(t, 15*time.Second, "both machines online on the center", func() bool {
		ms := center.machines(t, tok)
		return ms["box"].Online && ms["lap"].Online
	})
	for _, target := range []string{hubA + "/lap", hubB + "/box"} {
		req, err := http.NewRequest(http.MethodGet, member.srv.URL+"/api/v1/anything", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", nodeUpgrade)
		req.Header.Set(hdrPeerNode, target)
		req.Header.Set(hdrPeerFrom, hubB)
		resp, err := insecureClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no credential, node stream headers for %s: %d, want 401", target, resp.StatusCode)
		}
	}
}

// A member from before the headers were dropped still passes them on, naming the center as the opener: the center knows
// it opened no such stream.
func TestCenterRefusesNodeStreamNamingItAsOpener(t *testing.T) {
	center := startTestHub(t, hubB, "Office", nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set(hdrPeerTTL, "8")
	req.Header.Set(hdrPeerNode, hubA+"/lap")
	req.Header.Set(hdrPeerFrom, hubB)
	rec := httptest.NewRecorder()
	center.h.servePeerHTTP(rec, req)
	if rec.Code != http.StatusLoopDetected {
		t.Fatalf("got %d, want 508", rec.Code)
	}
}

// Set as center: the old center becomes a member; while it is gone that needs confirming, and when it is back it
// hears the larger term and stays a member.
func TestCenterSetAndOldCenterReturns(t *testing.T) {
	bStore := newMeshStore()
	bDir := t.TempDir()
	b := startTestHubWith(t, hubB, "Office", bStore, bDir, nil, nil)
	a := startTestHub(t, hubA, "家里", nil, nil)
	joinHubs(t, a, b)
	a.waitCenter(t, hubB)
	b.waitCenter(t, hubB)
	b.waitReachable(t, hubA)
	term := b.h.group().Term

	// Handed over while both are there.
	if _, err := b.h.setCenter(context.Background(), hubA, false); err != nil {
		t.Fatal(err)
	}
	a.waitCenter(t, hubA)
	b.waitCenter(t, hubA)
	if g := a.h.group(); g.Term != term+1 {
		t.Fatalf("term %d, want %d", g.Term, term+1)
	}

	// Back to B, then B goes away: making A the center again needs confirming.
	a.waitReachable(t, hubB)
	if _, err := a.h.setCenter(context.Background(), hubB, false); err != nil {
		t.Fatal(err)
	}
	a.waitCenter(t, hubB)
	b.waitCenter(t, hubB)
	b.stop()
	eventually(t, 20*time.Second, "A sees B gone", func() bool { return !a.h.centerReachable() })
	if _, err := a.h.setCenter(context.Background(), hubA, false); err == nil {
		t.Fatal("took over from an unreachable center without confirming")
	}
	if _, err := a.h.setCenter(context.Background(), hubA, true); err != nil {
		t.Fatal(err)
	}
	if !a.h.isCenter() {
		t.Fatal("A is not the center")
	}
	aTerm := a.h.group().Term

	// B comes back with its old term: it hears A's and becomes a member.
	b2 := startTestHubWith(t, hubB, "Office", bStore, bDir, nil, nil)
	b2.waitCenter(t, hubA)
	eventually(t, 20*time.Second, "after B came back: A has hubA, B has aTerm", func() bool {
		return a.h.centerID() == hubA && b2.h.group().Term == aTerm
	})
}

// A credential revoked on the center stays revoked when the center moves to a hub that still had a copy.
func TestCenterRevocationSurvivesCenterMove(t *testing.T) {
	center := startTestHub(t, hubB, "Office", nil, nil)
	member := startTestHub(t, hubA, "家里", nil, nil)
	tok, id := member.deviceToken(t, roleFull) // paired with the member before it joined
	admin, _ := center.deviceToken(t, roleFull)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	center.waitReachable(t, hubA)
	eventually(t, 20*time.Second, "the credential handed over", func() bool {
		st, _ := center.do(t, http.MethodGet, "/api/v1/machines", tok, "")
		return st == http.StatusOK
	})
	if st, b := center.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+id, admin, ""); st != http.StatusOK {
		t.Fatalf("revoke: %d %s", st, b)
	}
	eventually(t, 20*time.Second, "the member's copy revoked", func() bool {
		dt, err := member.store.DeviceTokenByHash(context.Background(), hashToken(tok))
		return err == nil && dt == nil
	})
	center.waitReachable(t, hubA)
	if _, err := center.h.setCenter(context.Background(), hubA, false); err != nil {
		t.Fatal(err)
	}
	member.waitCenter(t, hubA)
	center.waitCenter(t, hubA)
	eventually(t, 20*time.Second, "a revoked credential on the new center: 401", func() bool {
		st, _ := member.do(t, http.MethodGet, "/api/v1/machines", tok, "")
		return st == http.StatusUnauthorized
	})
}

// A member that was away when a credential was revoked hears of it when it is back.
func TestCenterRevocationReachesHubThatWasAway(t *testing.T) {
	aStore, aDir := newMeshStore(), t.TempDir()
	center := startTestHub(t, hubB, "Office", nil, nil)
	member := startTestHubWith(t, hubA, "家里", aStore, aDir, nil, nil)
	tok, id := member.deviceToken(t, roleFull)
	admin, _ := center.deviceToken(t, roleFull)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	center.waitReachable(t, hubA)
	eventually(t, 20*time.Second, "the credential handed over", func() bool {
		st, _ := center.do(t, http.MethodGet, "/api/v1/machines", tok, "")
		return st == http.StatusOK
	})
	member.stop()
	eventually(t, 20*time.Second, "the center sees the member gone", func() bool { return !center.h.meshView().reachable(hubA) })
	if st, b := center.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+id, admin, ""); st != http.StatusOK {
		t.Fatalf("revoke: %d %s", st, b)
	}
	if dt, _ := aStore.DeviceTokenByHash(context.Background(), hashToken(tok)); dt == nil {
		t.Fatal("the member heard while it was away")
	}
	startTestHubWith(t, hubA, "家里", aStore, aDir, nil, nil)
	eventually(t, 20*time.Second, "the member's copy revoked", func() bool {
		dt, err := aStore.DeviceTokenByHash(context.Background(), hashToken(tok))
		return err == nil && dt == nil
	})
}

// A center that took over without hearing of a revocation (the old one went away before it got out) takes the
// credential until the old one is back; then it dies there, and its live connection with it (docs/server/mesh.md
// "Not done / known limits").
func TestCenterRevocationCatchesUpWhenOldCenterReturns(t *testing.T) {
	bStore, bDir := newMeshStore(), t.TempDir()
	center := startTestHubWith(t, hubB, "Office", bStore, bDir, nil, nil)
	member := startTestHub(t, hubA, "家里", nil, nil)
	tok, id := member.deviceToken(t, roleFull)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	center.waitReachable(t, hubA)
	eventually(t, 20*time.Second, "the credential handed over", func() bool {
		st, _ := center.do(t, http.MethodGet, "/api/v1/machines", tok, "")
		return st == http.StatusOK
	})
	center.stop()
	if ok, err := bStore.RevokeDeviceToken(context.Background(), id); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	eventually(t, 20*time.Second, "the member sees the center gone", func() bool { return !member.h.centerReachable() })
	if _, err := member.h.setCenter(context.Background(), hubA, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := member.do(t, http.MethodGet, "/api/v1/machines", tok, ""); st != http.StatusOK {
		t.Fatalf("the credential on a center that never heard: %d, want 200", st)
	}
	conn := dialApp(t, member, tok)
	back := startTestHubWith(t, hubB, "Office", bStore, bDir, nil, nil)
	back.waitCenter(t, hubA)
	eventually(t, 20*time.Second, "the revocation on the new center", func() bool {
		st, _ := member.do(t, http.MethodGet, "/api/v1/machines", tok, "")
		return st == http.StatusUnauthorized
	})
	eventually(t, 20*time.Second, "the revoked credential's connection closes", func() bool {
		select {
		case <-conn.done:
			return true
		default:
			return false
		}
	})
}

// A message that did not take is sent again without waiting for a hub to go away and come back.
func TestCenterRevocationResent(t *testing.T) {
	center := startTestHub(t, hubB, "Office", nil, nil)
	member := startTestHub(t, hubA, "家里", nil, nil)
	tok, id := member.deviceToken(t, roleFull)
	admin, _ := center.deviceToken(t, roleFull)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	center.waitReachable(t, hubA)
	eventually(t, 20*time.Second, "the credential handed over", func() bool {
		st, _ := center.do(t, http.MethodGet, "/api/v1/machines", tok, "")
		return st == http.StatusOK
	})
	member.store.mu.Lock()
	member.store.applyFails = 2
	member.store.mu.Unlock()
	if st, b := center.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+id, admin, ""); st != http.StatusOK {
		t.Fatalf("revoke: %d %s", st, b)
	}
	eventually(t, 20*time.Second, "the member's copy revoked", func() bool {
		dt, err := member.store.DeviceTokenByHash(context.Background(), hashToken(tok))
		return err == nil && dt == nil
	})
}

// What a hub takes from another: only an app's credential. One it does not know stays revoked, so a copy handed over
// later does not bring it back.
func TestCredsRevokedTakesOnlyAppCredentials(t *testing.T) {
	center := startTestHub(t, hubB, "Office", nil, nil)
	member := startTestHub(t, hubA, "家里", nil, nil)
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	center.waitCenter(t, hubB)
	ctx := context.Background()
	hash := func(s string) string { return hashToken(s) }
	linkDT := db.DeviceToken{ID: "dev_link", Name: "link", Kind: "link", Role: roleLink, MachineID: "mac"}
	if err := center.store.CreateDeviceToken(ctx, linkDT, hash("link")); err != nil {
		t.Fatal(err)
	}
	send := func(from string, list ...db.ExportedToken) error {
		raw, _ := json.Marshal(map[string]any{"tokens": list})
		_, err := center.h.meshLocalCall(ctx, from, "creds.revoked", raw)
		return err
	}
	gone := db.ExportedToken{DeviceToken: db.DeviceToken{ID: "dev_gone", Name: "old", Kind: "phone", Role: roleFull}, Hash: hash("gone")}
	asLink := db.ExportedToken{DeviceToken: linkDT, Hash: hash("link")}
	asFull := asLink
	asFull.Role = roleFull // a hub's word for the role does not reach a credential that is not an app's
	if err := send(hubA, asLink, asFull, gone); err != nil {
		t.Fatal(err)
	}
	if dt, _ := center.store.DeviceTokenByHash(ctx, hash("link")); dt == nil {
		t.Fatal("a persona-link credential was revoked")
	}
	if n, err := center.store.ImportDeviceTokens(ctx, []db.ExportedToken{gone}); err != nil || n != 0 {
		t.Fatalf("handing over a revoked credential: %d %v", n, err)
	}
	if dt, _ := center.store.DeviceTokenByHash(ctx, hash("gone")); dt != nil {
		t.Fatal("a copy handed over brought a revoked credential back")
	}
}

// One computer over two paths (the center's own and through a member): listed once, the direct path used; when it
// breaks the other takes over at once; when it is back the center returns to it once, and then stays.
func TestCenterOneComputerTwoPaths(t *testing.T) {
	mac := newMeshNode("uid-mac", "mac", "s1")
	mac.resident = true
	center := startTestHub(t, hubB, "Office", []MachineConfig{{ID: "mac", Name: "Mac", Kind: "ssh"}}, []*meshNode{mac})
	member := startTestHub(t, hubA, "家里", []MachineConfig{{ID: "macbook", Name: "MacBook", Kind: "link"}}, []*meshNode{mac})
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	tok, _ := center.deviceToken(t, roleFull)

	// Both paths learned (the member's path is tried once to know where it goes), then one machine, direct.
	eventually(t, 20*time.Second, "one machine with two paths, on the direct one", func() bool {
		ms := center.machines(t, tok)
		m, ok := ms["mac"]
		return len(ms) == 1 && ok && m.Online && len(m.Paths) == 2 && m.Route != nil && m.Route.Via == "ssh"
	})
	settle := func(what string) int {
		var n int
		eventually(t, 15*time.Second, what, func() bool {
			mac.mu.Lock()
			before := mac.connects
			mac.mu.Unlock()
			time.Sleep(1500 * time.Millisecond)
			mac.mu.Lock()
			n = mac.connects
			mac.mu.Unlock()
			return n == before
		})
		return n
	}
	settle("the connections settle")

	// The direct path breaks: the member's takes over.
	mac.mu.Lock()
	mac.refuse[center.h] = true
	mac.mu.Unlock()
	if c := mac.current(); c != nil {
		_ = c.c.Close()
	}
	eventually(t, 15*time.Second, "through the member", func() bool {
		m := center.machines(t, tok)["mac"]
		return m.Online && m.Route != nil && m.Route.Via == viaHub && m.HubID == hubA
	})
	// Items still flow through the new path.
	a := dialApp(t, center, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "mac", "session_id": "s1"})
	a.wait(t, "backlog through the member", 0, hasItem("hi from s1"))

	// The direct path comes back: after path_switch_seconds the center moves back to it, once.
	mac.mu.Lock()
	mac.refuse[center.h] = false
	mac.mu.Unlock()
	eventually(t, 20*time.Second, "back on the direct path", func() bool {
		m := center.machines(t, tok)["mac"]
		return m.Online && m.Route != nil && m.Route.Via == "ssh"
	})
	n := settle("stays on the direct path")
	time.Sleep(3 * time.Second)
	mac.mu.Lock()
	after := mac.connects
	mac.mu.Unlock()
	if after != n {
		t.Fatalf("flapping: %d connects in 3 s after settling", after-n)
	}
	mac.addItem("s1", "after the switch back")
	a.wait(t, "a live item after the switch back", 0, hasItem("after the switch back"))
}

// Two hub.json entries of the center that reach the same computer are one machine.
func TestCenterTwoEntriesOneComputer(t *testing.T) {
	mac := newMeshNode("uid-mac", "mac", "s1")
	mac.resident = true
	center := startTestHub(t, hubB, "Office", []MachineConfig{{ID: "mac", Name: "Mac", Kind: "link"}, {ID: "mac-ssh", Name: "Mac (ssh)", Kind: "ssh"}},
		[]*meshNode{mac, mac})
	tok, _ := center.deviceToken(t, roleFull)
	eventually(t, 20*time.Second, "one machine", func() bool {
		ms := center.machines(t, tok)
		return len(ms) == 1 && ms["mac"].Online && len(ms["mac"].Paths) == 2
	})
	if m := center.h.machineByID("mac-ssh"); m == nil || m.cfg.ID != "mac" {
		t.Fatalf("the second entry's id leads to %v", m)
	}
}

// fakeConnector plays a computer's connector in passive mode (docs/operations/link.md "Passive mode"): TLS on its own key, a
// one-time code, the hub's credential (its hash only), and the stream joined to the node.
type fakeConnector struct {
	t      *testing.T
	srv    *http.Server
	ln     net.Listener
	fp     string
	node   *meshNode
	nodeOf *Hub
	code   string

	mu      sync.Mutex
	hash    string // the credential's hash
	machine string
	hubID   string
	streams []*websocket.Conn
	infos   atomic.Int32
}

func newFakeConnector(t *testing.T, node *meshNode) *fakeConnector {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "connector.persona.invalid"},
		DNSNames: []string{"connector.persona.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	fc := &fakeConnector{t: t, node: node, fp: spkiPin(leaf), code: "code-" + randomHex(4)}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	fc.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+link.ConnectorPairPath, fc.pair)
	mux.HandleFunc("GET "+link.ConnectorInfoPath, fc.info)
	mux.HandleFunc("GET "+link.ConnectorStreamPath, fc.stream)
	fc.srv = &http.Server{Handler: mux}
	go func() { _ = fc.srv.Serve(ln) }()
	t.Cleanup(func() { _ = fc.srv.Close() })
	return fc
}

func (fc *fakeConnector) addr() string { return fc.ln.Addr().String() }

func (fc *fakeConnector) link() string {
	q := url.Values{"addr": {fc.addr()}, "fp": {fc.fp}, "code": {fc.code}}
	return "persona://connector?" + q.Encode()
}

func (fc *fakeConnector) authorized(r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.hash != "" && hashToken(tok) == fc.hash
}

func (fc *fakeConnector) pair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code, Token, Machine string
		HubID                string `json:"hub_id"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req) != nil || req.Token == "" || req.Machine == "" {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"bad_request","message":"fields"}`)
		return
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if req.Code != fc.code {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"bad_code"}`)
		return
	}
	fc.code = "" // used
	fc.hash, fc.machine, fc.hubID = hashToken(req.Token), req.Machine, req.HubID
	_, _ = io.WriteString(w, `{"version":"test-connector"}`)
}

func (fc *fakeConnector) info(w http.ResponseWriter, r *http.Request) {
	fc.infos.Add(1)
	if !fc.authorized(r) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		return
	}
	fc.mu.Lock()
	machine := fc.machine
	fc.mu.Unlock()
	writeJSON(w, map[string]any{"machine": machine, "version": "test-connector"})
}

func (fc *fakeConnector) stream(w http.ResponseWriter, r *http.Request) {
	if !fc.authorized(r) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		return
	}
	nc, err := fc.node.dialer(fc.nodeOf)(r.Context())
	if err != nil {
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":"node_unavailable","message":"node down"}`)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		nc.Close()
		return
	}
	fc.mu.Lock()
	fc.streams = append(fc.streams, conn)
	fc.mu.Unlock()
	ws := websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ws, nc); done <- struct{}{} }()
	go func() { _, _ = io.Copy(nc, ws); done <- struct{}{} }()
	<-done
	nc.Close()
	_ = conn.CloseNow()
}

// repair has the connector paired with another hub: its streams end with 4002, the old credential is gone.
func (fc *fakeConnector) repair() {
	fc.mu.Lock()
	fc.hash = "someone-else"
	streams := fc.streams
	fc.streams = nil
	fc.mu.Unlock()
	for _, c := range streams {
		_ = c.Close(link.CloseRevoked, "paired with another hub")
	}
}

// A computer of kind service: paired from its link, reached through its connector, refused with a wrong credential,
// and marked for pairing again when the connector moved to another hub.
func TestCenterServiceConnector(t *testing.T) {
	dev := newMeshNode("uid-dev", "example-workstation", "s9")
	dev.resident = true
	fc := newFakeConnector(t, dev)
	center := startTestHub(t, hubB, "Office", []MachineConfig{{ID: "example-workstation", Name: "example-workstation", Kind: kindService, Address: fc.addr()}}, nil)
	fc.nodeOf = center.h
	tok, _ := center.deviceToken(t, roleFull)
	// Not paired yet: it says so.
	eventually(t, 10*time.Second, "example-workstation says it is not paired", func() bool {
		return strings.Contains(center.machines(t, tok)["example-workstation"].Error, "没配对")
	})
	// A wrong code is refused.
	bad := strings.Replace(fc.link(), fc.code, "wrong", 1)
	if _, _, err := center.h.pairConnector(context.Background(), bad, "example-workstation", "", ""); err == nil || !strings.Contains(err.Error(), "配对码") {
		t.Fatalf("a wrong code: %v", err)
	}
	// A link pinned to another key is refused before anything is sent.
	otherPin := strings.Replace(fc.link(), url.QueryEscape(fc.fp), "AAAA", 1)
	if _, _, err := center.h.pairConnector(context.Background(), otherPin, "example-workstation", "", ""); err == nil {
		t.Fatal("paired with a connector of another key")
	}
	addr, version, err := center.h.pairConnector(context.Background(), fc.link(), "example-workstation", "", "")
	if err != nil || addr != fc.addr() || version != "test-connector" {
		t.Fatalf("pair: %q %q %v", addr, version, err)
	}
	if fc.hubID != hubB || fc.machine != "example-workstation" {
		t.Fatalf("the connector got hub %q machine %q", fc.hubID, fc.machine)
	}
	cs := center.store
	row, _ := cs.ConnectorCredential(context.Background(), "example-workstation")
	if row == nil || row.FP != fc.fp || strings.HasPrefix(row.Token, "jc_") {
		t.Fatalf("stored credential %+v (must be sealed)", row)
	}
	eventually(t, 15*time.Second, "example-workstation online through its connector", func() bool {
		m := center.machines(t, tok)["example-workstation"]
		return m.Online && m.Route != nil && m.Route.Via == kindService
	})
	a := dialApp(t, center, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "example-workstation", "session_id": "s9"})
	a.wait(t, "example-workstation's backlog", 0, hasItem("hi from s9"))

	// The connector is paired with another hub: 4002, and the hub stops trying until it is paired again.
	fc.repair()
	// The manager shows a machine whose stream just ended as online for its reconnect grace (5 s, devices/warm.go)
	// before it says why: wait well past that.
	eventually(t, 20*time.Second, "example-workstation needs pairing again", func() bool {
		m := center.machines(t, tok)["example-workstation"]
		return !m.Online && strings.Contains(m.Error, "重新配对")
	})
	before := fc.infos.Load()
	time.Sleep(time.Second)
	if fc.infos.Load() != before {
		t.Fatal("the hub kept asking a connector whose credential is gone")
	}

	// A wrong credential (a stale one in the database): refused, said so. (Paired again first: the manager tries
	// it again; then what the hub keeps is not what the connector knows.)
	fc.mu.Lock()
	fc.code = "code-again"
	fc.mu.Unlock()
	if _, _, err := center.h.pairConnector(context.Background(), fc.link(), "example-workstation", "", ""); err != nil {
		t.Fatalf("pair again: %v", err)
	}
	eventually(t, 15*time.Second, "example-workstation online again", func() bool {
		return center.machines(t, tok)["example-workstation"].Online
	})
	row, _ = cs.ConnectorCredential(context.Background(), "example-workstation")
	sealed, _ := center.h.sealKey("jc_wrong")
	row.Token = sealed
	_ = cs.SaveConnectorCredential(context.Background(), *row)
	fc.mu.Lock()
	fc.hash = "lost" // and the connector forgot the one the manager holds: it asks the hub again, and that one is wrong
	streams := fc.streams
	fc.streams = nil
	fc.mu.Unlock()
	for _, c := range streams { // the connector restarts: the manager connects again, with what the hub keeps now
		_ = c.Close(websocket.StatusGoingAway, "restarting")
	}
	// The path failed three times in a row by now (not paired, paired elsewhere, the restart), so the manager waits
	// 8 s before it tries again (devices/paths_choose.go pathEnded) and only then hears the 401: wait well past that.
	eventually(t, 30*time.Second, "a wrong credential is refused", func() bool {
		return strings.Contains(center.machines(t, tok)["example-workstation"].Error, "不认")
	})
}

// A computer its member cannot open: the center says why. The member's manager opens the node before the member
// answers the center's upgrade, and a failure comes back with its reason.
func TestCenterLearnsWhyAMemberCannotOpenIt(t *testing.T) {
	lap := newMeshNode("uid-lap", "lap", "s2")
	center := startTestHub(t, hubB, "Office", nil, nil)
	member := startTestHub(t, hubA, "家里", []MachineConfig{{ID: "lap", Name: "Laptop", Kind: "ssh"}}, []*meshNode{lap})
	lap.mu.Lock()
	lap.refuse[member.h], lap.refusal = true, errors.New("ssh: 主机拒绝了连接")
	lap.mu.Unlock()
	joinHubs(t, member, center)
	member.waitCenter(t, hubB)
	tok, _ := center.deviceToken(t, roleFull)
	eventually(t, 20*time.Second, "the center says why", func() bool {
		m, ok := center.machines(t, tok)["lap"]
		return ok && !m.Online && strings.Contains(m.Error, "主机拒绝了连接")
	})
	lap.mu.Lock()
	lap.refuse[member.h] = false
	lap.mu.Unlock()
	eventually(t, 30*time.Second, "through the member once it can", func() bool { return center.machines(t, tok)["lap"].Online })
}
