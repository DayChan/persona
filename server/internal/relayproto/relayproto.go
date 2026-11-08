// Package relayproto is what a hub and the push relay (internal/relay, docs/operations/push-relay.md) say to each other, so the
// hub does not link the relay's server.
package relayproto

// PushRequest is what a hub posts to /relay/v1/push.
type PushRequest struct {
	Type       string         `json:"type"` // alert | background | liveactivity
	Priority   string         `json:"priority,omitempty"`
	CollapseID string         `json:"collapse_id,omitempty"`
	Sealed     string         `json:"sealed,omitempty"` // alert / background: base64 HPKE payload (pushcrypto)
	Token      string         `json:"token,omitempty"`  // liveactivity: the activity's or the push-to-start token
	APS        map[string]any `json:"aps,omitempty"`    // liveactivity
}

// MaxSealed is the longest base64 sealed payload the relay takes: with its envelope (about 130 bytes) the push stays
// under APNs' 4 KB. The hub shortens a push's texts until it fits (hub/push_transport.go sealFor).
const MaxSealed = 3800

// RegisterRequest is what /relay/v1/register takes (the phone at first, the hub again when a grant stops working);
// the answer is {"grant": "..."}.
type RegisterRequest struct {
	APNSToken string `json:"apns_token"`
	Env       string `json:"env"`
	Topic     string `json:"topic"`
}
