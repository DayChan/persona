// Package link is persona-link, the small program that keeps a computer reachable from the hub when the hub cannot
// reach it (a laptop on whatever network it is on), and the protocol it speaks with the hub. persona-link finds the
// hub, tells it which machine it is, keeps the link up and hands each stream the hub opens on it to the computer's
// resident node (`personad node --attach`), byte for byte. It does nothing else: what a node does stays in personad
// (docs/operations/link.md).
package link

import (
	"encoding/json"
	"errors"
	"fmt"
	appidentity "github.com/nwverse/persona/server/internal/identity"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The link is a WebSocket on the hub's TLS port (or under the path of the HTTPS reverse proxy in front of it); each node
// connection the hub opens on it is another WebSocket.
const (
	Path       = "/api/v1/link"
	StreamPath = "/api/v1/link/stream"
)

// Close codes on the link besides the standard ones (1001: the hub is stopping).
const (
	CloseReplaced websocket.StatusCode = 4001 // a newer link of the same machine took over
	CloseRevoked  websocket.StatusCode = 4002 // its credential was revoked
	CloseRefused  websocket.StatusCode = 4003 // the hello did not match the credential
)

// Both ends ping every PingEvery and drop the link after PingTimeout without a pong, so a dead link is noticed
// within about PingEvery+PingTimeout. These are settings, not measurements: a hub may set others (hub.json link: on a
// slow link a ping waits behind the message being written), and then tells Persona Link (endpoints' ping_ms and
// ping_timeout_ms, the connector stream's PingHeader).
const (
	PingEvery   = 10 * time.Second
	PingTimeout = 5 * time.Second
)

// A heartbeat a hub sets is brought into [MinPing, MaxPing] at both ends (PingTimes). MinPing is below hub.json's
// whole seconds for the tests' short heartbeats; MaxPing still notices a dead link within twenty minutes.
const (
	MinPing = 50 * time.Millisecond
	MaxPing = 10 * time.Minute
)

// PingTimes is the heartbeat a hub set, in range; ok is false unless both are set (more than 0): the caller keeps its
// own then.
func PingTimes(every, timeout time.Duration) (time.Duration, time.Duration, bool) {
	if every <= 0 || timeout <= 0 {
		return 0, 0, false
	}
	return min(max(every, MinPing), MaxPing), min(max(timeout, MinPing), MaxPing), true
}

// PingTimesMS is PingTimes of milliseconds, as the wire has them (no overflow however large, either way: a negative
// one stays not set).
func PingTimesMS(every, timeout int64) (time.Duration, time.Duration, bool) {
	ms := func(n int64) time.Duration {
		return time.Duration(min(max(n, 0), MaxPing.Milliseconds())) * time.Millisecond
	}
	return PingTimes(ms(every), ms(timeout))
}

// Message is one JSON message on the link.
type Message struct {
	Type string `json:"type"` // hello | endpoints | node | open | open_failed | rename | renamed | attention
	// hello (persona-link → hub, the first message): which machine, which build, and the hub URL it went to.
	Machine string `json:"machine,omitempty"`
	Version string `json:"version,omitempty"`
	Address string `json:"address,omitempty"`
	// hello: what this persona-link takes besides the node's streams (FeatureAttention). Older ones send none, and
	// older managers ignore it.
	Features []string `json:"features,omitempty"`
	// attention (hub → persona-link): the "Needs you" summary, to links whose hello listed FeatureAttention.
	Attention *Attention `json:"attention,omitempty"`
	// endpoints (hub → persona-link): where else to look for the hub; and, from hubs that know (hub mesh, #293),
	// what the hub is called and how many hubs its mesh has, itself included. Older hubs send neither.
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	HubName   string     `json:"hub_name,omitempty"`
	MeshHubs  int        `json:"mesh_hubs,omitempty"`
	// endpoints, from hubs whose hub.json sets the heartbeat (link): how often to ping and how long to wait for a
	// pong, in milliseconds; Persona Link keeps this link's to them (PingTimes). Without them it keeps its own.
	PingMS        int `json:"ping_ms,omitempty"`
	PingTimeoutMS int `json:"ping_timeout_ms,omitempty"`
	// endpoints: this computer waits for the user to confirm it (a signed-in device added it, docs/assistant/onboarding.md
	// 4.2). Every endpoints message says how it is now, so a confirmed one (and an older hub) leaves it out.
	Pending bool `json:"pending,omitempty"`
	// endpoints: what the hub calls this computer (its hub.json name; hubs that rename computers, docs/operations/link.md
	// "The computer's name"). rename (persona-link → hub) asks the hub to call it this instead (empty: its id again);
	// renamed (hub → persona-link) answers the rename of the same ID with the name it has now, or Error and Code.
	MachineName string `json:"machine_name,omitempty"`
	// renamed: why the hub did not take the name, as a code (RenameCode*).
	Code string `json:"code,omitempty"`
	// node (hub → persona-link): whether the hub got through to the resident node.
	Online bool `json:"online,omitempty"`
	// open (hub → persona-link) asks for stream ID; open_failed answers that it could not be opened. rename / renamed:
	// which rename.
	ID string `json:"id,omitempty"`
	// open_failed, node, renamed: why not.
	Error string `json:"error,omitempty"`
}

// Endpoint is a hub URL on its TLS port, or a reverse proxy's https URL with the path the hub sits under (no trailing
// slash: the API paths are joined to it), and what kind of address it is.
type Endpoint struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // lan | ipv6 | name | ipv4
	// Manual: added on this computer (persona-link address add); kept when the hub sends its own list.
	Manual bool `json:"manual,omitempty"`
}

// Config is ~/.persona/link.json (0600), written by `persona-link pair`. The fields at the top are active mode's (this
// computer dials the hub); Passive is passive mode's (the hub dials this computer), kept apart so either mode can be
// switched back to without pairing again.
type Config struct {
	Hub     string `json:"hub"`     // the hub's id
	Machine string `json:"machine"` // this computer in the hub's hub.json
	// HubName is what the hub calls itself, as it last said (endpoints); empty until a hub that says it.
	HubName string `json:"hub_name,omitempty"`
	// MachineName is what the hub calls this computer, as it last said (endpoints, renamed); empty until a hub that
	// says it.
	MachineName string `json:"machine_name,omitempty"`
	// FP pins the hub: SHA-256 of its TLS SubjectPublicKeyInfo, base64url (the hub's certificate is self-signed).
	// With FP set every candidate is checked against it and nothing else.
	FP string `json:"fp"`
	// Trust is TrustSystem when the pairing link had no FP: the hub has no TLS of its own and sits behind an HTTPS
	// reverse proxy, whose certificate is checked against the system's CAs and the host name (docs/operations/link.md "Hub behind a
	// reverse proxy"). Ignored while FP is set.
	Trust     string     `json:"trust,omitempty"`
	Token     string     `json:"token"`
	Endpoints []Endpoint `json:"endpoints"`
	// Mode is ModeActive (also when empty: files from before passive mode) or ModePassive.
	Mode    string   `json:"mode,omitempty"`
	Passive *Passive `json:"passive,omitempty"`
}

// The two modes (docs/operations/link.md "Passive mode").
const (
	ModeActive  = "active"  // persona-link dials the hub's /api/v1/link
	ModePassive = "passive" // the hub dials the TLS port persona-link listens on
)

// TrustSystem: the hub is checked with the system's CAs and its host name, not a pin (Config.Trust).
const TrustSystem = "system"

// SystemTrust: c checks the hub with the system's CAs (no pin).
func (c *Config) SystemTrust() bool { return c != nil && c.FP == "" && c.Trust == TrustSystem }

// CurrentMode is the mode c is in.
func (c *Config) CurrentMode() string {
	if c != nil && c.Mode == ModePassive {
		return ModePassive
	}
	return ModeActive
}

// ErrUnpaired: link.json has no credential for active mode (never paired, or only for passive mode). It is an
// fs.ErrNotExist, as a missing file is.
var ErrUnpaired = fmt.Errorf("not paired for this mode: %w", os.ErrNotExist)

// DefaultPath is where the credential lives.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(appidentity.DataDir(home), "link.json")
}

// AttachSocket is the resident node's socket (internal/node/attach.go), the one place streams go.
func AttachSocket() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(appidentity.SocketDir(home), "node-attach.sock")
}

// Load reads active mode's credential; the error wraps fs.ErrNotExist before pairing (ErrUnpaired when the file only
// has passive mode's).
func Load(path string) (*Config, error) {
	c, err := Read(path)
	if err != nil {
		return nil, err
	}
	if c.Token == "" && c.FP == "" && c.Machine == "" {
		return nil, ErrUnpaired
	}
	if c.Token == "" || (c.FP == "" && c.Trust != TrustSystem) || c.Machine == "" {
		return nil, fmt.Errorf("%s is incomplete: pair again (persona-link pair)", path)
	}
	return c, nil
}

// Read reads link.json as it is, whichever mode it is in and paired or not; the error wraps fs.ErrNotExist when there
// is none.
func Read(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Update changes the file under a lock: the running link (new endpoints from the hub) and `persona-link address`
// may write it at the same time.
func Update(path string, change func(*Config) error) error {
	if filepath.Dir(path) == appidentity.HomeDir() {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if _, err := appidentity.PrepareHome(home); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockFile(lock, true); err != nil {
		return err
	}
	var c Config
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := change(&c); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// kindOf says what kind of address a hub URL is: the link tries lan first (docs/operations/link.md "Route choice").
func kindOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "name"
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return "name"
	}
	switch ip = ip.Unmap(); {
	case ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback():
		return "lan"
	case ip.Is6():
		return "ipv6"
	default:
		return "ipv4"
	}
}

func rank(kind string) int {
	switch kind {
	case "lan":
		return 0
	case "ipv6":
		return 1
	case "name":
		return 2
	}
	return 3
}

// ranked orders candidates for a race: LAN first, then public IPv6, names, IPv4; manual ones before the hub's of the
// same kind; duplicates dropped.
func ranked(eps []Endpoint) []Endpoint {
	out := make([]Endpoint, 0, len(eps))
	seen := map[string]bool{}
	for _, e := range eps {
		if !seen[e.URL] {
			seen[e.URL] = true
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b Endpoint) int {
		if d := rank(a.Kind) - rank(b.Kind); d != 0 {
			return d
		}
		switch {
		case a.Manual && !b.Manual:
			return -1
		case b.Manual && !a.Manual:
			return 1
		}
		return 0
	})
	return out
}

// normalize turns what a person types into a candidate: an https URL, or a host / host:port on the hub's TLS port
// (taken from the candidates there are).
func normalize(raw string, have []Endpoint) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		host, port := raw, ""
		if h, p, err := net.SplitHostPort(raw); err == nil {
			host, port = h, p
		}
		if port == "" {
			for _, e := range have {
				if u, err := url.Parse(e.URL); err == nil && u.Port() != "" {
					port = u.Port()
					break
				}
			}
		}
		if port == "" {
			return Endpoint{}, fmt.Errorf("%q 没有端口：写成 https://地址:端口", raw)
		}
		raw = "https://" + net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Endpoint{}, fmt.Errorf("%q 不是 https 地址：链路只走 hub 的 TLS 端口", raw)
	}
	if n, err := strconv.Atoi(u.Port()); u.Port() != "" && (err != nil || n < 1 || n > 65535) {
		return Endpoint{}, fmt.Errorf("%q 的端口不对", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return Endpoint{URL: u.String(), Kind: kindOf(u.String()), Manual: true}, nil
}
