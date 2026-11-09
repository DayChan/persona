package devices

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
)

// slowConnector is a connector whose info answers status, once the test lets it (release); asked is closed when the
// request is in.
type slowConnector struct {
	srv     *httptest.Server
	fp      string
	asked   chan struct{}
	release chan struct{}
}

func newSlowConnector(t *testing.T, status int) *slowConnector {
	t.Helper()
	sc := &slowConnector{asked: make(chan struct{}), release: make(chan struct{})}
	sc.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(sc.asked)
		<-sc.release
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"machine":"example-service","version":"test-connector"}`)
		} else {
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		}
	}))
	t.Cleanup(sc.srv.Close)
	sum := sha256.Sum256(sc.srv.Certificate().RawSubjectPublicKeyInfo)
	sc.fp = base64.RawURLEncoding.EncodeToString(sum[:])
	return sc
}

// serviceMachine is a service computer reached at sc, paired (pairing gen 1) with token.
func serviceMachine(t *testing.T, sc *slowConnector, token string) *Machine {
	t.Helper()
	return serviceMachineAt(t, sc.srv, sc.fp, token)
}

// serviceMachineAt is a service computer whose connector is srv, pinned to fp, paired (pairing gen 1) with token.
func serviceMachineAt(t *testing.T, srv *httptest.Server, fp, token string) *Machine {
	t.Helper()
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "example-service", Name: "Example service", Kind: kindService,
		Address: strings.TrimPrefix(srv.URL, "https://")})
	d.addEntry(m)
	m.svc = serviceState{pairGen: 1, cred: &connectorCred{fp: fp, token: token}}
	return m
}

// probeWhile reads m's connector info and does meanwhile while the connector holds the answer.
func probeWhile(t *testing.T, m *Machine, sc *slowConnector, meanwhile func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.probeService(ctx)
	}()
	<-sc.asked
	meanwhile()
	close(sc.release)
	<-done
}

// A probe that was out when the connector ended the stream with 4002 (paired with another hub) answers too late to
// count: the machine still says to pair it again (TestCenterServiceConnector lost "Pair again" that way under load).
func TestServiceProbeAfterRevokeKeepsPairAgain(t *testing.T) {
	sc := newSlowConnector(t, http.StatusOK)
	m := serviceMachine(t, sc, "jc_old")
	probeWhile(t, m, sc, func() { m.serviceInvalidated(1) })
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if !st.invalid || st.ok || st.err != errConnectorInvalid {
		t.Fatalf("the late answer overwrote the revocation: invalid=%v ok=%v err=%q", st.invalid, st.ok, st.err)
	}
	if ok, _ := m.serviceReachable(); ok {
		t.Fatal("reachable with a revoked credential")
	}
}

// A probe with the old credential that the connector refuses after the computer was paired again here does not drop
// the new credential or hold the connector off for connectorRefusedWait.
func TestServiceProbeRefusedAfterRepairKeepsNewCredential(t *testing.T) {
	sc := newSlowConnector(t, http.StatusUnauthorized)
	m := serviceMachine(t, sc, "jc_old")
	fresh := &connectorCred{fp: sc.fp, token: "jc_new"}
	probeWhile(t, m, sc, func() {
		m.mu.Lock()
		m.svc = serviceState{pairGen: m.svc.pairGen + 1, cred: fresh} // what PairConnector does
		m.mu.Unlock()
	})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.cred != fresh || !st.wait.IsZero() || st.err != "" || !st.probed.IsZero() {
		t.Fatalf("the old credential's refusal hit the new pairing: cred=%v wait=%v err=%q probed=%v", st.cred, st.wait, st.err, st.probed)
	}
	if ok, _ := m.serviceReachable(); !ok {
		t.Fatal("the new pairing is held off")
	}
}

// The same refusal without a new pairing is the connector not knowing the credential: dropped, held off, said so.
func TestServiceProbeRefusedDropsCredential(t *testing.T) {
	sc := newSlowConnector(t, http.StatusUnauthorized)
	m := serviceMachine(t, sc, "jc_old")
	probeWhile(t, m, sc, func() {})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.cred != nil || time.Until(st.wait) < connectorRefusedWait/2 || st.ok || !strings.Contains(st.err, "不认") {
		t.Fatalf("refused: cred=%v wait=%v ok=%v err=%q", st.cred, st.wait, st.ok, st.err)
	}
}

// revokingConnector is a connector that accepts one stream and ends it with 4002 "credential replaced" once the test
// lets it (revoke), as the connector does to the old credential's streams when it is paired again.
type revokingConnector struct {
	srv    *httptest.Server
	fp     string
	revoke chan struct{}
	done   chan struct{}
}

func newRevokingConnector(t *testing.T) *revokingConnector {
	t.Helper()
	rc := &revokingConnector{revoke: make(chan struct{}), done: make(chan struct{})}
	rc.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectorStreamPath {
			http.NotFound(w, r)
			return
		}
		defer close(rc.done)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-rc.revoke
		_ = conn.Close(connectorRevoked, "credential replaced")
	}))
	t.Cleanup(rc.srv.Close)
	sum := sha256.Sum256(rc.srv.Certificate().RawSubjectPublicKeyInfo)
	rc.fp = base64.RawURLEncoding.EncodeToString(sum[:])
	return rc
}

// revokedStream opens a stream to rc with m's pairing gen 1 credential, does meanwhile, has rc end it with 4002 and
// reads it to the end. It returns the stream's lastErr.
func revokedStream(t *testing.T, m *Machine, rc *revokingConnector, meanwhile func()) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := m.dialService(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer p.close()
	meanwhile()
	close(rc.revoke)
	if _, err := io.Copy(io.Discard, p.r); websocket.CloseStatus(err) != connectorRevoked {
		t.Fatalf("stream ended with %v, want close %d", err, connectorRevoked)
	}
	<-rc.done
	return p.lastErr()
}

func revokingMachine(t *testing.T, rc *revokingConnector) *Machine {
	t.Helper()
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "example-service", Name: "Example service", Kind: kindService,
		Address: strings.TrimPrefix(rc.srv.URL, "https://")})
	d.addEntry(m)
	m.svc = serviceState{pairGen: 1, cred: &connectorCred{fp: rc.fp, token: "jc_old"}}
	return m
}

// The connector ends the old credential's streams with 4002 when it is paired again, here too. A close read after
// PairConnector moved on is of the old pairing: the computer just paired is not marked "Pair again" and keeps its new
// credential.
func TestServiceStaleStreamRevokeKeepsNewPairing(t *testing.T) {
	rc := newRevokingConnector(t)
	m := revokingMachine(t, rc)
	fresh := &connectorCred{fp: rc.fp, token: "jc_new"}
	msg := revokedStream(t, m, rc, func() {
		m.mu.Lock()
		m.svc = serviceState{pairGen: m.svc.pairGen + 1, cred: fresh} // what PairConnector does
		m.mu.Unlock()
	})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.invalid || st.cred != fresh || st.err != "" {
		t.Fatalf("the old stream's 4002 hit the new pairing: invalid=%v cred=%v err=%q", st.invalid, st.cred, st.err)
	}
	if msg == errConnectorInvalid {
		t.Fatalf("the old stream reports %q", msg)
	}
	if ok, _ := m.serviceReachable(); !ok {
		t.Fatal("the new pairing is not reachable")
	}
}

// The same 4002 without a new pairing is the connector paired with another hub: pair it again here.
func TestServiceStreamRevokeMarksPairAgain(t *testing.T) {
	rc := newRevokingConnector(t)
	m := revokingMachine(t, rc)
	msg := revokedStream(t, m, rc, func() {})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if !st.invalid || st.ok || st.cred != nil || st.err != errConnectorInvalid {
		t.Fatalf("4002 not taken: invalid=%v ok=%v cred=%v err=%q", st.invalid, st.ok, st.cred, st.err)
	}
	if msg != errConnectorInvalid {
		t.Fatalf("stream lastErr %q, want %q", msg, errConnectorInvalid)
	}
	if ok, _ := m.serviceReachable(); ok {
		t.Fatal("reachable with a revoked credential")
	}
}

// credMachine is a service computer at pairing gen 1 with no kept credential; the hub holds token for it.
func credMachine(t *testing.T, token string) (*Machine, *fakeHost) {
	t.Helper()
	d := newTestDevices(t)
	fh := d.opts.Hub.(*fakeHost)
	fh.creds = map[string]devicewire.ConnectorCred{"example-service": {MachineID: "example-service", FP: "fp-old", Token: token}}
	m := newMachine(d, MachineConfig{ID: "example-service", Name: "Example service", Kind: kindService, Address: "127.0.0.1:1"})
	d.addEntry(m)
	m.svc = serviceState{pairGen: 1}
	return m, fh
}

// A credential the hub handed over for the old pairing, answered after the computer was paired again here, does not
// replace the new one: the next request would be refused with 401 and hold the new pairing off.
func TestConnectorCredFetchedBeforeRepairKeepsNewCredential(t *testing.T) {
	m, fh := credMachine(t, "jc_old")
	fresh := &connectorCred{fp: "fp-new", token: "jc_new"}
	fh.credCall = func() {
		m.mu.Lock()
		m.svc = serviceState{pairGen: m.svc.pairGen + 1, cred: fresh} // what PairConnector does
		m.mu.Unlock()
	}
	got, gen, err := m.connectorCred(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	kept := m.svc.cred
	m.mu.Unlock()
	if kept != fresh || got.token != "jc_new" || gen != 2 {
		t.Fatalf("the old pairing's credential won: kept=%v returned=%v gen=%d", kept, got, gen)
	}
}

// A fetch that was out when the connector ended a stream with 4002 does not put a credential back.
func TestConnectorCredFetchedBeforeRevokeStaysDropped(t *testing.T) {
	m, fh := credMachine(t, "jc_old")
	fh.credCall = func() { m.serviceInvalidated(1) }
	if got, _, err := m.connectorCred(context.Background()); err == nil {
		t.Fatalf("a revoked connector's credential was handed out: %v", got)
	}
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.cred != nil || !st.invalid {
		t.Fatalf("the revocation was undone: cred=%v invalid=%v", st.cred, st.invalid)
	}
}

// Without a pairing meanwhile, what the hub hands over is kept: the secret crosses the bus once.
func TestConnectorCredFetchedIsKept(t *testing.T) {
	m, fh := credMachine(t, "jc_old")
	calls := 0
	fh.credCall = func() { calls++ }
	for i := 0; i < 2; i++ {
		got, gen, err := m.connectorCred(context.Background())
		if err != nil || got.token != "jc_old" || gen != 1 {
			t.Fatalf("fetch %d: %v gen=%d %v", i, got, gen, err)
		}
	}
	m.mu.Lock()
	kept := m.svc.cred
	m.mu.Unlock()
	if kept == nil || kept.token != "jc_old" || calls != 1 {
		t.Fatalf("not kept: cred=%v hub calls=%d", kept, calls)
	}
}

// A stream dialed with the credential of a pairing made while its fetch was out belongs to that pairing: a 4002 on it
// is the new pairing revoked, so pair again.
func TestServiceStreamOfRepairDuringFetchRevokeMarksPairAgain(t *testing.T) {
	rc := newRevokingConnector(t)
	m := revokingMachine(t, rc)
	m.svc.cred = nil // fetched from the hub on dial
	fh := m.d.opts.Hub.(*fakeHost)
	fh.creds = map[string]devicewire.ConnectorCred{"example-service": {MachineID: "example-service", FP: rc.fp, Token: "jc_old"}}
	fresh := &connectorCred{fp: rc.fp, token: "jc_new"}
	fh.credCall = func() {
		m.mu.Lock()
		m.svc = serviceState{pairGen: m.svc.pairGen + 1, cred: fresh} // what PairConnector does
		m.mu.Unlock()
	}
	msg := revokedStream(t, m, rc, func() {})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if !st.invalid || st.cred != nil || st.err != errConnectorInvalid {
		t.Fatalf("the new pairing's 4002 was ignored: invalid=%v cred=%v err=%q", st.invalid, st.cred, st.err)
	}
	if msg != errConnectorInvalid {
		t.Fatalf("stream lastErr %q, want %q", msg, errConnectorInvalid)
	}
}

// repairDuringFetch makes m (reached at sc) fetch its credential from the hub, and has the computer paired again here
// while that fetch is out. It returns the new pairing's credential, which connectorCred hands over at pairing gen 2.
func repairDuringFetch(t *testing.T, m *Machine, sc *slowConnector) *connectorCred {
	t.Helper()
	m.svc.cred = nil // fetched from the hub
	fh := m.d.opts.Hub.(*fakeHost)
	fh.creds = map[string]devicewire.ConnectorCred{"example-service": {MachineID: "example-service", FP: sc.fp, Token: "jc_old"}}
	fresh := &connectorCred{fp: sc.fp, token: "jc_new"}
	fh.credCall = func() {
		m.mu.Lock()
		m.svc = serviceState{pairGen: m.svc.pairGen + 1, cred: fresh} // what PairConnector does
		m.mu.Unlock()
	}
	return fresh
}

// A probe made with the credential of a pairing made while its fetch was out speaks for that pairing: a 401 drops the
// new credential and holds the connector off.
func TestServiceProbeOfRepairDuringFetchRefusedDropsCredential(t *testing.T) {
	sc := newSlowConnector(t, http.StatusUnauthorized)
	m := serviceMachine(t, sc, "jc_old")
	repairDuringFetch(t, m, sc)
	probeWhile(t, m, sc, func() {})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.pairGen != 2 || st.cred != nil || time.Until(st.wait) < connectorRefusedWait/2 || st.ok || !strings.Contains(st.err, "不认") {
		t.Fatalf("the new pairing's refusal was ignored: gen=%d cred=%v wait=%v ok=%v err=%q", st.pairGen, st.cred, st.wait, st.ok, st.err)
	}
}

// The same probe answered: the new pairing is reachable.
func TestServiceProbeOfRepairDuringFetchAnswers(t *testing.T) {
	sc := newSlowConnector(t, http.StatusOK)
	m := serviceMachine(t, sc, "jc_old")
	fresh := repairDuringFetch(t, m, sc)
	probeWhile(t, m, sc, func() {})
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.pairGen != 2 || st.cred != fresh || !st.ok || st.err != "" || st.version != "test-connector" {
		t.Fatalf("the new pairing's answer was dropped: gen=%d cred=%v ok=%v err=%q version=%q", st.pairGen, st.cred, st.ok, st.err, st.version)
	}
}

// A stream dial made with the credential of a pairing made while its fetch was out that the connector refuses with 401
// drops the new credential and holds the connector off with the reason.
func TestServiceDialOfRepairDuringFetchRefusedDropsCredential(t *testing.T) {
	sc := newSlowConnector(t, http.StatusUnauthorized)
	m := serviceMachine(t, sc, "jc_old")
	repairDuringFetch(t, m, sc)
	close(sc.release) // answer at once
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if p, err := m.dialService(ctx); err == nil {
		p.close()
		t.Fatal("dial with a refused credential succeeded")
	}
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.pairGen != 2 || st.cred != nil || time.Until(st.wait) < connectorRefusedWait/2 || !strings.Contains(st.err, "不认") {
		t.Fatalf("the new pairing's refusal was ignored: gen=%d cred=%v wait=%v err=%q", st.pairGen, st.cred, st.wait, st.err)
	}
}

// The stream dial tells the connector the hub's heartbeat (hub.json link) in link.PingHeader, and says nothing when
// the hub sets none: the connector keeps its own then, as an older one always does.
func TestServiceDialTellsTheConnectorTheHeartbeat(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(link.PingHeader)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "example-service", Name: "Example service", Kind: kindService,
		Address: strings.TrimPrefix(srv.URL, "https://")})
	d.addEntry(m)
	m.svc = serviceState{pairGen: 1, cred: &connectorCred{fp: base64.RawURLEncoding.EncodeToString(sum[:]), token: "jc_x"}}
	for _, tc := range []struct {
		pingMS, timeoutMS int
		want              string
	}{
		{0, 0, ""},
		{30000, 90000, "30000 90000"},
		{-1, 5000, ""}, // nothing usable: as none
	} {
		d.mu.Lock()
		d.cfg.LinkPingMS, d.cfg.LinkPingTimeoutMS = tc.pingMS, tc.timeoutMS
		d.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := m.dialService(ctx)
		cancel()
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		p.close()
		if h := <-got; h != tc.want {
			t.Errorf("hub's %d / %d ms: header %q, want %q", tc.pingMS, tc.timeoutMS, h, tc.want)
		}
	}
}

// A connector whose resident node is down already says so and how its OS runs it; the machine's page says it once,
// and about that computer, not "This machine" (on the hub's page that reads as the hub, #4080).
func TestConnectorNodeUnavailableSaidOnce(t *testing.T) {
	answer := func(message string) string {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(rec, `{"error":"node_unavailable","message":`+strconv.Quote(message)+`}`)
		return connectorError(rec.Result()).Error()
	}
	now := link.NodeDownSelf + link.NodeDownMarker + "（/tmp/node-attach.sock 连不上）：persona-link node install（systemd 用户服务）"
	if got, want := answer(now), "这台电脑的"+link.NodeDownMarker+"（/tmp/node-attach.sock 连不上）：persona-link node install（systemd 用户服务）"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// An older connector's own words follow the hub's sentence, which the hub still reads as node_service_down.
	if got := answer("node down"); got != "这台电脑的"+link.NodeDownMarker+"：node down" {
		t.Fatalf("older connector: %q", got)
	}
	if got := answer("node 那边：" + link.NodeDownMarker); got != "node 那边："+link.NodeDownMarker { // already says it
		t.Fatalf("marker inside: %q", got)
	}
	if got := answer(""); got != "这台电脑的"+link.NodeDownMarker {
		t.Fatalf("no message: %q", got)
	}
}
