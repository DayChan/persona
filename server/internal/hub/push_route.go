package hub

import (
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The way this hub's pushes reach a device (docs/operations/push-relay.md "Which way a device is pushed", #5998): a phone whose
// relay registration failed used to send empty relay fields, and with no apns.json its background pushes went nowhere
// without a word anywhere.
const (
	pushDirect = "direct" // the hub's own APNs key (apns.json)
	pushRelay  = "relay"  // the relay the phone registered with
	pushNone   = "none"
)

// Why a device's pushes do not go, or did not the last time.
const (
	pushNoToken     = "no_token"     // the device has no APNs token: notifications not allowed, or APNs refused it
	pushRelayFailed = "relay_failed" // the phone's relay registration failed (Detail: its words)
	pushNoRelay     = "no_relay"     // no apns.json, and the phone names no relay (a build without one, or an older app)
	pushFailed      = "push_failed"  // the way is there, but the last push did not go (Detail: APNs' or the relay's words)
)

// relayErrorMax bounds what a phone may say about its relay registration.
const relayErrorMax = 300

// pushRoute is a device's way, as the devices API, Settings › Devices and `hub doctor` show it.
type pushRoute struct {
	Route    string     `json:"route"`
	Reason   string     `json:"reason,omitempty"`
	Detail   string     `json:"detail,omitempty"`
	FailedAt *time.Time `json:"failed_at,omitempty"`
}

// pushFailure is the last push to a device that did not go. Kept in memory: the next push says again.
type pushFailure struct {
	text string
	at   time.Time
}

// routeOf is d's way now.
func (p *Pusher) routeOf(d Device) pushRoute {
	var r pushRoute
	switch {
	case d.APNSToken == "":
		return pushRoute{Route: pushNone, Reason: pushNoToken}
	case p.direct != nil:
		r.Route = pushDirect
	case p.relayed(d):
		r.Route = pushRelay
	case d.RelayError != "":
		return pushRoute{Route: pushNone, Reason: pushRelayFailed, Detail: d.RelayError}
	case d.RelayURL != "" && d.RelayGrant != "":
		return pushRoute{Route: pushNone, Reason: pushRelayFailed, Detail: "no push key"}
	default:
		return pushRoute{Route: pushNone, Reason: pushNoRelay}
	}
	p.mu.Lock()
	f, failed := p.failed[d.DeviceID]
	p.mu.Unlock()
	if failed {
		at := f.at
		r.Reason, r.Detail, r.FailedAt = pushFailed, f.text, &at
	}
	return r
}

// routeOfID is the way of the device with this id; ok false when the hub has no such device.
func (p *Pusher) routeOfID(id string) (pushRoute, bool) {
	d, ok := p.device(id)
	if !ok {
		return pushRoute{}, false
	}
	return p.routeOf(d), true
}

// pushed notes how a push to d's own token went: err nil, it went.
func (p *Pusher) pushed(d Device, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.failed[d.DeviceID] = pushFailure{text: clipRelayError(err.Error()), at: p.now().UTC()}
		return
	}
	if p.acceptedLocked(d) {
		delete(p.failed, d.DeviceID)
	}
}

// pushedAs is pushed for a push of pushType. A Live Activity push goes to the activity's own token, not d's, so its
// answer says nothing about d's way: it neither sets nor clears push_failed. Accepted, it still shows the app is
// installed.
func (p *Pusher) pushedAs(d Device, pushType string, err error) {
	if pushType != "liveactivity" {
		p.pushed(d, err)
		return
	}
	if err == nil {
		p.mu.Lock()
		p.acceptedLocked(d)
		p.mu.Unlock()
	}
}

// acceptedLocked notes that APNs or the relay took a push for d: d's LastPushAt, saved by maintenance. It is false,
// and notes nothing, when the device registered another token or relay since. p.mu is held.
func (p *Pusher) acceptedLocked(d Device) bool {
	cur, ok := p.devices[d.DeviceID]
	if !ok || cur.APNSToken != d.APNSToken || cur.RelayURL != d.RelayURL {
		return false
	}
	cur.LastPushAt = p.now().UTC()
	p.devices[d.DeviceID] = cur
	if p.acceptedDirty == nil {
		p.acceptedDirty = map[string]bool{}
	}
	p.acceptedDirty[d.DeviceID] = true
	return true
}

// settingsOnly: a row only notification settings made (an Android phone, a browser, the simulator), which never
// registered for APNs and is notified over the WebSocket. It has no push way to show (#1911); an iPhone that
// registered without a token has a kind and still says why.
func settingsOnly(d Device) bool { return d.APNSToken == "" && d.Kind == "" }

// routesByCredential is, per device token, the way of the device it registered last (Settings › Devices).
// Settings-only rows are skipped before picking the newest, so one can never hide a registered device.
func (p *Pusher) routesByCredential() map[string]pushRoute {
	out := map[string]pushRoute{}
	if p == nil {
		return out
	}
	newest := map[string]Device{}
	for _, d := range p.list() {
		if d.Credential == "" || settingsOnly(d) {
			continue
		}
		if cur, ok := newest[d.Credential]; !ok || d.UpdatedAt.After(cur.UpdatedAt) {
			newest[d.Credential] = d
		}
	}
	for cred, d := range newest {
		out[cred] = p.routeOf(d)
	}
	return out
}

// list is every device, newest registration first.
func (p *Pusher) list() []Device {
	p.mu.Lock()
	out := make([]Device, 0, len(p.devices))
	for _, d := range p.devices {
		out = append(out, d)
	}
	p.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// pushDevice is one device in GET /api/v1/devices/push.
type pushDevice struct {
	DeviceID  string    `json:"device_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updated_at"`
	Push      pushRoute `json:"push"`
}

// pushRoutes: GET /api/v1/devices/push, every device the hub pushes to and its way (`hub doctor`).
func (h *Hub) pushRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/devices/push", func(w http.ResponseWriter, r *http.Request) {
		devices := []pushDevice{}
		direct := false
		if h.push != nil {
			direct = h.push.direct != nil
			for _, d := range h.push.list() {
				if settingsOnly(d) {
					continue // nothing registered
				}
				devices = append(devices, pushDevice{DeviceID: d.DeviceID, Name: d.Name, Kind: d.Kind, UpdatedAt: d.UpdatedAt, Push: h.push.routeOf(d)})
			}
		}
		writeJSON(w, map[string]any{"direct": direct, "devices": devices})
	})
}

// clipRelayError keeps what a phone or a failed push says to one line of relayErrorMax bytes.
func clipRelayError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= relayErrorMax {
		return s
	}
	cut := relayErrorMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
