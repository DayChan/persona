package hub

import (
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
)

// Taking a card's push back once the card is over (#4349): answered on the computer, the web or the watch, withdrawn
// by the agent or expired. A suspended app never gets `interaction_resolved`, so its approval and question pushes
// stayed on the lock screen until it was opened (#3513 is the app's side). The hub sends the devices the card was
// pushed to a silent background push naming the cards that are over, and the app removes their notifications.
//
// iOS gives background pushes a small budget: cards over close together go in one push, a device gets at most one a
// minute, and only devices that said they understand it (`features: ["withdraw"]` at registration) get any.

// featureWithdraw is what a device registers with when it removes the notifications a withdrawal push names.
const featureWithdraw = "withdraw"

const (
	// withdrawKept: how long the hub remembers where a card's push went. A card still pending after this is old
	// news on the lock screen, and the app clears it when it next lists the cards.
	withdrawKept = 30 * time.Minute
	// withdrawGather: cards over within this of the first go in one push ("always allow" settles several at once).
	withdrawGather = 5 * time.Second
	// withdrawEvery: at most one withdrawal push per device this often; what comes between waits for the next.
	withdrawEvery = time.Minute
	// withdrawMost bounds the ids in one push (APNs takes 4 KB); the rest go with the next.
	withdrawMost = 40
	// withdrawIDBytes bounds the encoded ids in one push too: runtime ids may be 128 characters, and forty of those
	// are more than a sealed push carries (relayMaxPlain, under APNs' 4 KB). The rest leaves room for aps, hub_id and iat.
	withdrawIDBytes = relayMaxPlain - 256
)

type withdrawals struct {
	mu sync.Mutex
	// pushed: per card id, the devices its push went to and when.
	pushed map[string]cardPush
	// queue: per device id, the cards over that it has not been told about yet.
	queue map[string]*withdrawQueue
	// gather and every are withdrawGather and withdrawEvery; tests shorten them.
	gather, every time.Duration
}

type cardPush struct {
	devices map[string]bool
	at      time.Time
}

type withdrawQueue struct {
	ids   []string
	timer *time.Timer
	last  time.Time // when the last withdrawal push went
}

func newWithdrawals() withdrawals {
	return withdrawals{pushed: map[string]cardPush{}, queue: map[string]*withdrawQueue{}, gather: withdrawGather, every: withdrawEvery}
}

// sendCard pushes a card (sendLocalizedQuiet) and notes which devices that understand withdrawals it went to.
func (p *Pusher) sendCard(manager string, build localized, id string, wants func(NotifyPrefs) bool, rule quietRule) {
	if p == nil {
		return
	}
	p.sendFrom(manager, build, id, wants, rule, func(d Device) {
		if hasFeature(d, featureWithdraw) {
			p.cardPushedTo(id, d.DeviceID)
		}
	})
}

func (p *Pusher) cardPushedTo(id, device string) {
	w := &p.withdrawals
	now := p.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for old, c := range w.pushed {
		if now.Sub(c.at) > withdrawKept {
			delete(w.pushed, old)
		}
	}
	c, ok := w.pushed[id]
	if !ok {
		c = cardPush{devices: map[string]bool{}}
	}
	c.devices[device] = true
	c.at = now
	w.pushed[id] = c
}

// withdraw: the card is over. The devices its push went to are told, unless it went to none (a muted session, a
// quiet task, "Needs you" off) or so long ago that the hub forgot.
func (p *Pusher) withdraw(id string) {
	if p == nil {
		return
	}
	w := &p.withdrawals
	now := p.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.pushed[id]
	delete(w.pushed, id)
	if !ok || now.Sub(c.at) > withdrawKept {
		return
	}
	for device := range c.devices {
		q := w.queue[device]
		if q == nil {
			q = &withdrawQueue{}
			w.queue[device] = q
		}
		q.ids = append(q.ids, id)
		p.scheduleWithdrawLocked(device, q, now)
	}
}

// scheduleWithdrawLocked sends q after the gathering wait, or once the device's minute is up if that is later.
func (p *Pusher) scheduleWithdrawLocked(device string, q *withdrawQueue, now time.Time) {
	if q.timer != nil || len(q.ids) == 0 {
		return
	}
	w := &p.withdrawals
	wait := w.gather
	if next := q.last.Add(w.every).Sub(now); next > wait {
		wait = next
	}
	q.timer = time.AfterFunc(wait, func() { p.flushWithdraw(device) })
}

func (p *Pusher) flushWithdraw(device string) {
	w := &p.withdrawals
	w.mu.Lock()
	q := w.queue[device]
	if q == nil {
		w.mu.Unlock()
		return
	}
	q.timer = nil
	ids := q.ids
	if n := withdrawBatch(ids); n < len(ids) {
		ids, q.ids = ids[:n:n], ids[n:]
	} else {
		q.ids = nil
	}
	q.last = p.now()
	p.scheduleWithdrawLocked(device, q, q.last) // what did not fit
	w.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	p.sendAs(map[string]any{"aps": map[string]any{"content-available": 1}, "resolved_interaction_ids": ids}, "", "background", "5",
		func(d Device) bool { return d.DeviceID == device && hasFeature(d, featureWithdraw) })
}

// withdrawBatch is how many of ids, from the first, go in one push: at most withdrawMost, and only as many as fit
// withdrawIDBytes encoded. Always at least one, so the queue moves.
func withdrawBatch(ids []string) int {
	size := 0
	for i, id := range ids {
		b, _ := json.Marshal(id)
		size += len(b) + 1 // and its comma
		if i == withdrawMost || i > 0 && size > withdrawIDBytes {
			return i
		}
	}
	return len(ids)
}

func hasFeature(d Device, feature string) bool { return slices.Contains(d.Features, feature) }

// knownFeatures are the features a device may register with; anything else is dropped.
var knownFeatures = map[string]bool{featureWithdraw: true, compat.Feature: true}

func cleanFeatures(in []string) []string {
	var out []string
	for _, f := range in {
		if knownFeatures[f] && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}
