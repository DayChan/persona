package hub

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
)

// When a "finished" push is worth sending (docs/server/server.md "Push policy"). Borrowed from Ovis (github.com/devswha/ovis,
// server/push.ts, MIT): a push is for someone away from the computer, so
//   - wait a while after the turn ends; if the agent resumes, or the user looks at it (herdr idle, or opened in the
//     app), nothing is sent;
//   - only turns that took a while are worth it ("long"); a quick answer is read where it was asked;
//   - background tasks and subagents still running mean the work is not done: hold, and push once when the turn that
//     follows their reports ends.
// Each device picks what it wants (NotifyPrefs); the timings are hub.json "notify".

// NotifyConfig is hub.json "notify". Zero values fall back to the defaults.
type NotifyConfig struct {
	// DoneDelaySeconds: how long a finished agent must stay finished (and unseen) before the push. Default 60.
	DoneDelaySeconds int `json:"done_delay_seconds,omitempty"`
	// LongTurnSeconds: turns at least this long count as "long" (NotifyPrefs.Done = "long"). Default 60.
	LongTurnSeconds int `json:"long_turn_seconds,omitempty"`
	// BackgroundHoldMinutes: give up on a turn whose background work has not finished after this long. Default 120.
	BackgroundHoldMinutes int `json:"background_hold_minutes,omitempty"`
	// LiveMetricsSeconds: the least time between two Live Activity pushes to a device when only the numbers it shows
	// (TPM, today's tokens, load…) changed. Default 60; state changes are not held back (docs/apple/live-activity.md).
	LiveMetricsSeconds int `json:"live_metrics_seconds,omitempty"`
}

func (c NotifyConfig) doneDelay() time.Duration { return secondsOr(c.DoneDelaySeconds, 60) }
func (c NotifyConfig) longTurn() time.Duration  { return secondsOr(c.LongTurnSeconds, 60) }
func (c NotifyConfig) liveMetrics() time.Duration {
	return secondsOr(c.LiveMetricsSeconds, 60)
}
func (c NotifyConfig) backgroundHold() time.Duration {
	if c.BackgroundHoldMinutes > 0 {
		return time.Duration(c.BackgroundHoldMinutes) * time.Minute
	}
	return 120 * time.Minute
}

func secondsOr(n, def int) time.Duration {
	if n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Duration(def) * time.Second
}

// NotifyPrefs is what one device wants pushed, set in the app (Settings › Notifications) and stored per device.
type NotifyPrefs struct {
	// Input: questions, plans, permissions and terminal prompts (anything that needs you).
	Input bool `json:"input"`
	// Done: finished turns. "off", "long" (turns of at least LongTurnSeconds) or "always".
	Done string `json:"done"`
	// LiveActivity: keep the Fleet activity on the lock screen / Dynamic Island (liveactivity.go).
	LiveActivity bool `json:"live_activity"`
	// LockDetails: the activity may show agent names on the lock screen; off, it shows only counts.
	LockDetails bool `json:"lock_details"`
	// Island: what the compact Dynamic Island shows on each side.
	Island IslandPrefs `json:"island"`
	// Translate: the app's Settings › Translation, so the hub can translate new mail before it is opened (assistant/inbox_translate.go).
	Translate TranslatePrefs `json:"translate"`
	// MuteWhileDesktop: "Phone Muted" (WeChat's) — no alert pushes to this device while a desktop app (the Mac app) is
	// connected; it shows them there (desktops.go).
	MuteWhileDesktop bool `json:"mute_while_desktop"`
	// Look: the persona this device's Live Activity draws (Settings › Look), echoed in its pushes (FleetState.Look): the
	// widget extension cannot read the app's settings.
	Look *FleetLook `json:"look,omitempty"`
	// Quiet: this device's quiet hours (quiethours.go): no "Agent finished", and "Needs you" without sound, inside the window.
	Quiet QuietHours `json:"quiet"`
	// MutedManagers: the managers (their ids, docs/assistant/managers.md) whose pushes this device does not want (Settings ›
	// Managers › one › Notifications). A push a manager asks for (notify.request, its extensions' pushes, a computer's
	// `personad notify`) skips this device; the hub's own (pairing, a computer joining, updates) and the Live Activity
	// are no manager's and still come.
	MutedManagers []string `json:"muted_managers"`
}

// maxMutedManagers bounds what a device may store: far more managers than anyone runs. A write keeps only the
// roster's managers (rosterManagers), so a removed manager's id does not take a place a new mute needs.
const maxMutedManagers = 64

// rosterManagers is ids (trimmed) without those the roster does not have, from one read of the roster bounded like a
// device row write; when the roster can't answer (its database not answering) every id stays.
func (h *Hub) rosterManagers(ctx context.Context, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && len(id) <= 64 {
			out = append(out, id)
		}
	}
	known := map[string]bool{assistantwire.AssistantID: true}
	if s := h.managerStore(); s != nil && len(out) > 0 {
		timeout := deviceStoreTimeout
		if h.push != nil && h.push.storeTimeout > 0 {
			timeout = h.push.storeTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		idents, err := h.busIdentities(ctx, s)
		if err != nil {
			return out
		}
		for _, b := range idents {
			if b.Role == bus.RoleManager {
				known[b.ID] = true
			}
		}
	}
	// connected now: as rosterHas, a manager on the bus is the roster's even before the read sees it
	return slices.DeleteFunc(out, func(id string) bool { return !known[id] && h.busConnOf(id) == nil })
}

// mutes says whether this device turned off the pushes of manager ("": the hub's own, never muted).
func (p NotifyPrefs) mutes(manager string) bool {
	return manager != "" && slices.Contains(p.MutedManagers, manager)
}

// TranslatePrefs mirrors Settings › Translation on a device.
type TranslatePrefs struct {
	Enabled bool   `json:"enabled"`
	Target  string `json:"target,omitempty"` // BCP 47, one of assistantwire.TranslateLanguages
	Mail    bool   `json:"mail"`
}

// IslandPrefs picks the two sides of the compact Dynamic Island (docs/apple/live-activity.md). "Needs you" always takes the
// left side when there is any, whatever is chosen.
type IslandPrefs struct {
	Preset  string `json:"preset"`            // a: status counts on both sides | b: status + TPM | c: status + today's tokens | custom
	Left    string `json:"left,omitempty"`    // custom: an island slot
	Right   string `json:"right,omitempty"`   // custom: an island slot
	Machine string `json:"machine,omitempty"` // whose load "load" shows; empty: the busiest
}

// Island slots: what one side can show.
const (
	SlotStatus    = "status"    // left: the most urgent count (needs you > working > finished); right: the next one
	SlotTPM       = "tpm"       // output tokens per minute, every machine
	SlotTokens    = "tokens"    // today's tokens
	SlotRequests  = "requests"  // today's model requests
	SlotAssistant = "assistant" // whether the assistant (chat tab) is answering
	SlotLoad      = "load"      // a machine's load
	SlotTimer     = "timer"     // how long the longest running turn has been going
	SlotNone      = "none"
)

var islandSlots = map[string]bool{SlotStatus: true, SlotTPM: true, SlotTokens: true, SlotRequests: true,
	SlotAssistant: true, SlotLoad: true, SlotTimer: true, SlotNone: true}

// sides resolves the preset.
func (p IslandPrefs) sides() (left, right string) {
	switch p.Preset {
	case "b":
		return SlotStatus, SlotTPM
	case "c":
		return SlotStatus, SlotTokens
	case "custom":
		left, right = p.Left, p.Right
		if !islandSlots[left] {
			left = SlotStatus
		}
		if !islandSlots[right] {
			right = SlotStatus
		}
		return left, right
	}
	return SlotStatus, SlotStatus
}

const (
	DoneOff    = "off"
	DoneLong   = "long"
	DoneAlways = "always"
)

func defaultPrefs() NotifyPrefs {
	return NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, Island: IslandPrefs{Preset: "a"}, MutedManagers: []string{}}
}

// devicePrefs decodes what is stored for a device; anything missing or unknown is the default.
func devicePrefs(raw json.RawMessage) NotifyPrefs {
	p := defaultPrefs()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	switch p.Done {
	case DoneOff, DoneLong, DoneAlways:
	default:
		p.Done = DoneLong
	}
	switch p.Island.Preset {
	case "a", "b", "c", "custom":
	default:
		p.Island.Preset = "a"
	}
	if p.Quiet.validate() != nil { // stored before a check, or by hand: off
		p.Quiet = QuietHours{}
	}
	muted := make([]string, 0, len(p.MutedManagers))
	for _, id := range p.MutedManagers {
		if id = strings.TrimSpace(id); id != "" && len(id) <= 64 && !slices.Contains(muted, id) && len(muted) < maxMutedManagers {
			muted = append(muted, id)
		}
	}
	p.MutedManagers = muted // never null: the apps read a list, and its presence says the hub has the switch
	if l := p.Look; l != nil && (l.Design == "" || len(l.Design) > 32 || len(l.Package) > 160) {
		p.Look = nil // every push carries it, inside the activity's 4 KB
	}
	return p
}

// prefsView is a device's prefs plus the hub's timings, so the app can say what "long" means.
func (h *Hub) prefsView(p NotifyPrefs) any {
	return struct {
		NotifyPrefs
		LongTurnSeconds  int `json:"long_turn_seconds"`
		DoneDelaySeconds int `json:"done_delay_seconds"`
	}{p, int(h.cfg.Notify.longTurn().Seconds()), int(h.cfg.Notify.doneDelay().Seconds())}
}

// wantsDone: does a device with these prefs get the push for a turn that took d?
func (p NotifyPrefs) wantsDone(d, long time.Duration) bool {
	switch p.Done {
	case DoneAlways:
		return true
	case DoneLong:
		return d >= long
	}
	return false
}
