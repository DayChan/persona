package devices

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/net/proxy"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Computers of kind service (docs/archive/operations/plan-hub-center.md 4, docs/operations/link.md "Passive mode"): the computer's connector
// (Persona Link in passive mode) listens on a TLS port of its own and the manager dials it — no ssh. The manager pins
// the connector's key (the SPKI SHA-256 pairing gave it), shows the credential made up at pairing, and opens
// GET /api/v1/connector/stream, which the connector joins to the resident node's attach socket: from there it is
// the node protocol, as over ssh. GET /api/v1/connector/info, every half minute, says whether the connector is there
// and how far (the choice between a computer's paths uses it). Pairing goes the other way round from link: the
// computer makes a one-time code and a link (persona://connector?addr=…&fp=…&code=…), pasted into the admin page; the
// manager trades the code for a credential it generates (POST /api/v1/connector/pair); the hub keeps it sealed in
// Postgres with its own key and hands it over when asked (devices.connector_cred, devices.connector_paired).

const (
	kindService          = "service"
	connectorPairPath    = link.ConnectorPairPath
	connectorInfoPath    = link.ConnectorInfoPath
	connectorStreamPath  = link.ConnectorStreamPath
	connectorDialTimeout = 10 * time.Second
	// connectorRevoked: the connector was paired with another hub; the old credential is gone for good. (Switched
	// back to active mode or stopping is 1001: tried again as usual.)
	connectorRevoked = link.CloseRevoked
)

// serviceProbeEvery: how often each connector's info is read; connectorRefusedWait: how long a connector that refused
// the credential is left alone (it blocks an address after 10 failures a minute). Vars: tests shorten them.
var (
	serviceProbeEvery    = 30 * time.Second
	connectorRefusedWait = 5 * time.Minute
)

// serviceState is what the hub knows of a computer's connector (under Machine.mu).
type serviceState struct {
	probed  time.Time // last info read
	ok      bool      // it answered, for this machine
	rtt     int       // ms, its round trip
	err     string    // why not
	invalid bool      // the credential is gone (4002): pair again
	wait    time.Time // rate limited (429): not before this
	version string    // the connector's build
	pairGen int       // bumped when it is paired again
	// attention: its last info said it takes the "Needs you" summary; attRev: the Rev of the summary it holds, as that
	// info said or a post since made it (attention.go).
	attention bool
	attRev    string
	// cred is the pin and credential as the hub last handed them over (devices.connector_cred), kept so the secret
	// crosses the bus once, not on every probe; forgotten when the connector refuses it or it is paired again.
	cred *connectorCred
}

// connectorCred is a service machine's pin and credential.
type connectorCred struct{ fp, token, version string }

// connectorCred is the kept credential, or the one the hub hands over. What the hub answers is kept only if the
// state is where it was when the call went out: a pairing here while the call was out (PairConnector) already set the
// new credential, which the old answer must not replace, and a 4002 meanwhile (serviceInvalidated) dropped it for good.
// It also returns the pairing generation the credential belongs to, which can be newer than the caller's snapshot when
// the computer was paired again while the call was out: what the connector says about that credential is of that
// generation.
func (m *Machine) connectorCred(ctx context.Context) (*connectorCred, int, error) {
	m.mu.Lock()
	cached, gen := m.svc.cred, m.svc.pairGen
	m.mu.Unlock()
	if cached != nil {
		return cached, gen, nil
	}
	var c devicewire.ConnectorCred
	if err := m.d.opts.Hub.Call(ctx, devicewire.MethodConnectorCred, map[string]any{"machine_id": m.cfg.ID}, &c); err != nil {
		return nil, 0, err
	}
	if c.Token == "" {
		return nil, 0, errors.New("这台电脑的连接器还没配对：在管理界面这台电脑上点「重新配对」，粘贴它的 persona://connector 链接")
	}
	cred := &connectorCred{fp: c.FP, token: c.Token, version: c.Version}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svc.invalid {
		return nil, 0, errors.New(errConnectorInvalid)
	}
	if m.svc.cred != nil { // paired again, or another fetch kept its answer first
		return m.svc.cred, m.svc.pairGen, nil
	}
	if m.svc.pairGen != gen {
		return nil, 0, errors.New("这台电脑的连接器刚重新配对，稍后再试")
	}
	m.svc.cred = cred
	return cred, gen, nil
}

// connectorTransport is an HTTP/1.1 transport to a connector pinned to fp, dialing through proxyURL when set.
func connectorTransport(fp, proxyURL string) (*http.Transport, error) {
	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	base := &net.Dialer{Timeout: connectorDialTimeout, KeepAlive: 30 * time.Second}
	dial = base.DialContext
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil || u.Scheme != "socks5" && u.Scheme != "socks5h" || u.Host == "" {
			return nil, fmt.Errorf("proxy 要写成 socks5://主机:端口")
		}
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, base)
		if err != nil {
			return nil, err
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks5 dialer without context")
		}
		dial = cd.DialContext
	}
	return &http.Transport{
		Proxy: nil,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			cfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
				InsecureSkipVerify: true, // the pin below is the check: the connector's certificate is its own
				VerifyConnection: func(cs tls.ConnectionState) error {
					if len(cs.PeerCertificates) == 0 {
						return errConnectorPin
					}
					sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
					if base64.RawURLEncoding.EncodeToString(sum[:]) != fp {
						return errConnectorPin
					}
					return nil
				}}
			c := tls.Client(raw, cfg)
			if err := c.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			return c, nil
		},
		ResponseHeaderTimeout: connectorDialTimeout,
		MaxIdleConns:          1,
		IdleConnTimeout:       time.Minute,
	}, nil
}

// connectorClient makes requests to a connector over tr: within a time limit, never following a redirect.
func connectorClient(tr *http.Transport) *http.Client {
	return &http.Client{Transport: tr, Timeout: 2 * connectorDialTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var errConnectorPin = errors.New("TLS 公钥指纹和配对时的不一样：那里不是这台电脑的连接器")

// connectorError reads a connector's error answer: {"error": code, "message": …}.
func connectorError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &e)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &connectorHTTPError{status: resp.StatusCode, retry: retryAfterOf(resp), msg: "连接器那边试错太多次，暂时拒绝：稍后再试"}
	case resp.StatusCode == http.StatusUnauthorized:
		// Not tried again for a while: the connector counts failures and would block this hub's address.
		return &connectorHTTPError{status: resp.StatusCode, retry: connectorRefusedWait, msg: "连接器不认这台 hub 的凭据：重新配对"}
	case e.Error == "node_unavailable":
		return &connectorHTTPError{status: resp.StatusCode, msg: nodeUnavailable(e.Message)}
	case e.Message != "":
		return &connectorHTTPError{status: resp.StatusCode, msg: clip(oneLine(e.Message), 200)}
	}
	return &connectorHTTPError{status: resp.StatusCode, msg: "连接器回了 " + resp.Status}
}

// nodeUnavailable is what the machine's page says when the computer's connector answers that its resident node is not
// running. A connector since #4020 already says so (link.NodeDownMarker, then how that OS runs the node), so it is said
// once, and its "This machine" (the connector's own computer) becomes "this computer": on the hub's page it reads as the hub
// (#4080). An older connector's message only follows the hub's own sentence.
func nodeUnavailable(message string) string {
	message = oneLine(message)
	self := link.NodeDownSelf
	switch {
	case strings.HasPrefix(message, self+link.NodeDownMarker):
		return clip("这台电脑的"+strings.TrimPrefix(message, self), 200)
	case strings.Contains(message, link.NodeDownMarker):
		return clip(message, 200)
	case message != "":
		return "这台电脑的" + link.NodeDownMarker + "：" + clip(message, 200)
	}
	return "这台电脑的" + link.NodeDownMarker
}

type connectorHTTPError struct {
	status int
	retry  time.Duration
	msg    string
}

func (e *connectorHTTPError) Error() string { return e.msg }

func retryAfterOf(resp *http.Response) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Minute
}

// dialService opens a stream to the node through the computer's connector.
func (m *Machine) dialService(ctx context.Context) (*nodePipe, error) {
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.invalid {
		return nil, errors.New(errConnectorInvalid)
	}
	if time.Now().Before(st.wait) {
		if st.err != "" {
			return nil, fmt.Errorf("%s（%s 后再试）", st.err, time.Until(st.wait).Round(time.Second))
		}
		return nil, fmt.Errorf("连接器暂时拒绝，%s 后再试", time.Until(st.wait).Round(time.Second))
	}
	if m.cfg.Address == "" {
		return nil, errors.New("hub.json 里这台没有 address（连接器的 主机:端口）")
	}
	cred, gen, err := m.connectorCred(ctx)
	if err != nil {
		return nil, err
	}
	tr, err := connectorTransport(cred.fp, m.cfg.Proxy)
	if err != nil {
		return nil, err
	}
	every, timeout, set := m.d.linkPingTimes()
	header := http.Header{"Authorization": {"Bearer " + cred.token}}
	if set { // the connector pings the stream as this side does (an older one keeps its own)
		header.Set(link.PingHeader, link.PingHeaderValue(every, timeout))
	}
	dctx, cancel := context.WithTimeout(ctx, 2*connectorDialTimeout)
	conn, resp, err := websocket.Dial(dctx, "wss://"+m.cfg.Address+connectorStreamPath, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		HTTPHeader: header, CompressionMode: websocket.CompressionContextTakeover})
	cancel()
	if err != nil {
		tr.CloseIdleConnections()
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			herr := connectorError(resp)
			m.refused(gen, resp.StatusCode, herr, true)
			return nil, herr
		}
		return nil, err
	}
	conn.SetReadLimit(64 << 20)
	sctx, scancel := context.WithCancelCause(context.Background())
	nc := websocket.NetConn(sctx, conn, websocket.MessageBinary)
	go func() { // a stream whose TCP connection died quietly
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(sctx, timeout)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil && sctx.Err() == nil {
					scancel(fmt.Errorf("connector: no pong within %s: %w", timeout, err))
					_ = conn.CloseNow()
					return
				}
			}
		}
	}()
	// A 4002 on a stream of an older pairing (gen is the stream credential's) is the connector replacing that
	// credential (paired again, maybe here): it says nothing of the current one.
	r := &closeWatch{r: nc, onClose: func(err error) {
		if websocket.CloseStatus(err) == connectorRevoked {
			m.serviceInvalidated(gen)
		}
	}}
	var once sync.Once
	return &nodePipe{r: r, w: nc,
		lastErr: func() string {
			if r.err != nil && websocket.CloseStatus(r.err) == connectorRevoked && m.svcGenIs(gen) {
				return errConnectorInvalid
			}
			if c := context.Cause(sctx); c != nil && !errors.Is(c, context.Canceled) {
				return c.Error()
			}
			return ""
		},
		close: func() {
			once.Do(func() {
				scancel(nil)
				_ = nc.Close()
				tr.CloseIdleConnections()
			})
		}}, nil
}

// refused records a connector's refusal of a request made with pairing gen's credential: a 401 drops the kept
// credential, a refusal with a retry (401, 429) holds the path off that long (with its reason, withErr). Nothing when
// the state moved on while the request was out (svcMovedOnLocked): the refusal was of a credential no longer in use.
func (m *Machine) refused(gen, status int, herr error, withErr bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svcMovedOnLocked(gen) {
		return
	}
	if status == http.StatusUnauthorized {
		m.svc.cred = nil
	}
	var ce *connectorHTTPError
	if errors.As(herr, &ce) && ce.retry > 0 {
		m.svc.wait = time.Now().Add(ce.retry)
		if withErr {
			m.svc.err = herr.Error()
		}
	}
}

// svcMovedOnLocked: since a request made with pairing gen's credential went out, the connector was paired again here
// or ended a stream of pairing gen with 4002 (serviceInvalidated). What that request learned is stale: a probe that
// answered before the 4002 must not clear "Pair again", a refusal of the old credential must not hold off the new one.
// m.mu held.
func (m *Machine) svcMovedOnLocked(gen int) bool { return m.svc.invalid || m.svc.pairGen != gen }

// svcGenIs: the computer was not paired again since pairing gen.
func (m *Machine) svcGenIs(gen int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.svc.pairGen == gen
}

const errConnectorInvalid = "连接器已经配对给了别的 hub，原来的凭据失效：在管理界面重新配对这台电脑"

// closeWatch passes reads through and reports the error that ended them (a close frame's code).
type closeWatch struct {
	r       io.Reader
	onClose func(error)
	once    sync.Once
	err     error
}

func (c *closeWatch) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil {
		c.once.Do(func() {
			c.err = err
			c.onClose(err)
		})
	}
	return n, err
}

// serviceInvalidated: the connector ended a stream opened with pairing gen's credential with 4002: it was paired with
// another hub. The path is not tried again until it is paired again here. Nothing when the state moved on since
// (svcMovedOnLocked): the connector also ends old streams with 4002 when it is paired again here, and that close can be
// read after PairConnector handed over the new credential.
func (m *Machine) serviceInvalidated(gen int) {
	m.mu.Lock()
	if m.svcMovedOnLocked(gen) {
		m.mu.Unlock()
		return
	}
	m.svc.invalid, m.svc.ok, m.svc.err, m.svc.cred = true, false, errConnectorInvalid, nil
	m.mu.Unlock()
	m.d.logf("[%s] connector: credential revoked (paired with another hub)", m.cfg.ID)
	m.d.changed()
}

// serviceReachable: the connector answered its last info (or was not asked yet), and its round trip.
func (m *Machine) serviceReachable() (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.svc
	if st.invalid || time.Now().Before(st.wait) || !st.probed.IsZero() && !st.ok {
		return false, 0
	}
	return true, st.rtt
}

func (m *Machine) serviceWhy() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svc.err != "" {
		return m.svc.err
	}
	if time.Now().Before(m.svc.wait) {
		return "连接器暂时拒绝，稍后再试"
	}
	return ""
}

// probeService reads the connector's info: there, the computer pairing named, and how far.
func (m *Machine) probeService(ctx context.Context) {
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if st.invalid || time.Now().Before(st.wait) || m.cfg.Address == "" {
		return
	}
	gen := st.pairGen // the probed credential's pairing; connectorCred can hand over a newer one
	set := func(ok bool, rtt int, err string, version string) {
		m.mu.Lock()
		if m.svcMovedOnLocked(gen) {
			m.mu.Unlock()
			return
		}
		was := m.svc.ok
		m.svc.probed, m.svc.ok, m.svc.rtt, m.svc.err = time.Now(), ok, rtt, err
		if version != "" {
			m.svc.version = version
		}
		m.mu.Unlock()
		if was != ok {
			m.d.changed() // the hub's mesh record says whether it answers
			m.wake()
			if ok {
				m.d.logf("[%s] connector answers (%d ms)", m.cfg.ID, rtt)
			} else {
				m.d.logf("[%s] connector: %s", m.cfg.ID, err)
			}
		}
	}
	cred, credGen, err := m.connectorCred(ctx)
	if err != nil {
		set(false, 0, err.Error(), "")
		return
	}
	gen = credGen
	tr, err := connectorTransport(cred.fp, m.cfg.Proxy)
	if err != nil {
		set(false, 0, err.Error(), "")
		return
	}
	defer tr.CloseIdleConnections()
	client := connectorClient(tr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+m.cfg.Address+connectorInfoPath, nil)
	if err != nil {
		set(false, 0, "连接器的地址不对："+err.Error(), "")
		return
	}
	req.Header.Set("Authorization", "Bearer "+cred.token)
	req.Header.Set(link.MachineNameHeader, url.QueryEscape(m.name())) // what the connector shows
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		set(false, 0, "连不上连接器："+err.Error(), "")
		return
	}
	defer resp.Body.Close()
	rtt := max(1, int(time.Since(start).Milliseconds()))
	if resp.StatusCode != http.StatusOK {
		herr := connectorError(resp)
		m.refused(gen, resp.StatusCode, herr, false)
		set(false, 0, herr.Error(), "")
		return
	}
	var info link.ConnectorInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&info); err != nil {
		set(false, 0, "连接器的回答看不懂："+err.Error(), "")
		return
	}
	if info.Machine != "" && info.Machine != m.cfg.ID {
		set(false, 0, fmt.Sprintf("那里的连接器配对的是 %q，不是这台", clip(oneLine(info.Machine), 64)), info.Version)
		return
	}
	set(true, rtt, "", info.Version)
	m.mu.Lock()
	if !m.svcMovedOnLocked(gen) {
		m.svc.attention, m.svc.attRev = slices.Contains(info.Features, link.FeatureAttention), info.AttentionRev
	}
	m.mu.Unlock()
	m.wakeAttention() // posted by the machine's own sender: a connector slow to take it holds up no probe
}

// probeServices reads every service machine's connector info now and then, whatever this hub's role (a member says
// in its record whether its connectors answer).
func (d *Devices) probeServices(ctx context.Context) {
	for {
		for _, m := range d.cfgMachines() {
			if m.cfg.Kind == kindService && m.dialNode == nil {
				pctx, cancel := context.WithTimeout(ctx, 2*connectorDialTimeout)
				m.probeService(pctx)
				cancel()
			}
		}
		if !sleepCtx(ctx, serviceProbeEvery) {
			return
		}
	}
}

func validHostPort(a string) bool {
	if strings.Contains(a, "/") {
		return false
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil || host == "" {
		return false
	}
	return link.ValidPort(port)
}

// PairConnector trades a connector's one-time code for a credential the manager makes up, pinned to the link's key:
// the address that worked (the one given, else the link's candidates in order), and the connector's build. The hub
// keeps the credential sealed for machine id (devices.connector_paired).
func (d *Devices) PairConnector(ctx context.Context, p devicewire.PairConnector) (string, string, error) {
	addrs, fp, code, err := link.ParseConnectorLink(p.Link)
	if err != nil {
		return "", "", err
	}
	if address := strings.TrimSpace(p.Address); address != "" {
		if !validHostPort(address) {
			return "", "", errors.New("地址写成 主机:端口（IPv6 带方括号）")
		}
		addrs = []string{address}
	}
	if len(addrs) == 0 {
		return "", "", errors.New("配对链接里没有地址：填上那台电脑的 主机:端口")
	}
	tr, err := connectorTransport(fp, p.Proxy)
	if err != nil {
		return "", "", err
	}
	defer tr.CloseIdleConnections()
	client := connectorClient(tr)
	cfg := d.config()
	token := "jc_" + randomHex(32)
	body, _ := json.Marshal(map[string]string{"code": code, "token": token, "machine": p.MachineID, "hub_id": cfg.HubID, "hub_name": cfg.HubName,
		"machine_name": cmp.Or(entryNames(cfg)[p.MachineID], p.MachineID)})
	var errs []string
	for _, a := range addrs {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+a+connectorPairPath, bytes.NewReader(body))
		if err != nil {
			errs = append(errs, a+"：地址不对："+err.Error())
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(err, errConnectorPin) {
				return "", "", errConnectorPin
			}
			errs = append(errs, a+"："+err.Error())
			continue
		}
		if resp.StatusCode != http.StatusOK {
			herr := connectorError(resp)
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return "", "", errors.New("配对码不对、用过了或过期了：在那台电脑上重新生成")
			}
			return "", "", herr
		}
		var out struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&out)
		resp.Body.Close()
		version := clip(oneLine(out.Version), 80)
		if err := d.opts.Hub.Call(ctx, devicewire.MethodConnectorPaired, devicewire.ConnectorCred{MachineID: p.MachineID, FP: fp,
			Token: token, Version: version}, nil); err != nil {
			return "", "", err
		}
		if m := d.cfgMachine(p.MachineID); m != nil {
			m.mu.Lock()
			m.svc = serviceState{version: out.Version, pairGen: m.svc.pairGen + 1, cred: &connectorCred{fp: fp, token: token, version: version}}
			m.mu.Unlock()
			m.wake()
		}
		d.logf("[%s] connector paired at %s", p.MachineID, a)
		return a, out.Version, nil
	}
	return "", "", errors.New("连不上那台电脑的连接器：" + strings.Join(errs, "；"))
}

// linkPingTimes are the heartbeats of the streams the manager keeps and of Persona Link (link.PingEvery and
// PingTimeout, or the hub's: hub.json link). set: the hub's, which Persona Link is told (endpoints, link.PingHeader).
func (d *Devices) linkPingTimes() (every, timeout time.Duration, set bool) {
	return linkPing(d.config())
}

func linkPing(cfg devicewire.Config) (time.Duration, time.Duration, bool) {
	if every, timeout, ok := link.PingTimesMS(int64(cfg.LinkPingMS), int64(cfg.LinkPingTimeoutMS)); ok {
		return every, timeout, true
	}
	return link.PingEvery, link.PingTimeout, false
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func clip(s string, n int) string { return textfmt.Clip(s, n) }

func oneLine(s string) string { return textfmt.OneLine(s) }
