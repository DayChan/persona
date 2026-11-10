package node

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Another PermissionRequest hook's answer (#2648). Claude Code runs every PermissionRequest hook at once and goes
// with the first allow; the others are not stopped, they run until their own timeout. Persona's hook would then keep
// a card on the phone (the agent shown as waiting for you) for a call that already ran. A hook that answers tells the
// node over the socket (kind hook.decided, with the PermissionRequest input Claude Code gave it, the same one Persona's
// hook got): a card not shown yet is not shown, one shown is withdrawn.
//
// A card waits permissionGrace before it is shown, so that the other hook's word, a few milliseconds behind, keeps it
// off the phone altogether; only on a machine where some hook has said hook.decided since the node started.

// permissionGrace is how long a tool card waits for another hook's answer, once any hook here has given one.
var permissionGrace = time.Second

// decidedKeep is how long an answer no card was waiting for is kept for the next card that asks after it.
const decidedKeep = 2 * time.Minute

// One hook.decided stands for one call (#4646): it goes to the earliest card waiting for that call, or, when none
// waits, to the first that asks after it, and is used up. A later call with the same input is a new call and asks.
type decisions struct {
	mu      sync.Mutex
	seen    bool                         // some hook said hook.decided since the node started
	unused  map[string][]time.Time       // answers no card took yet, by permissionKey, oldest first
	waiting map[string][]*decisionWaiter // cards waiting, by permissionKey, in the order they asked
}

// decisionWaiter is one card watching for another hook's answer.
type decisionWaiter struct {
	cancel   context.CancelFunc
	answered bool
}

// permissionKey names one tool call: its session, its subagent, the tool and its input (decoded and encoded again,
// so a hook that passes it on re-encoded, with other spacing or escapes, names the same call).
func permissionKey(in hookInput) string {
	input := string(in.ToolInput)
	var v any
	if json.Unmarshal(in.ToolInput, &v) == nil {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if enc.Encode(v) == nil {
			input = b.String()
		}
	}
	return in.SessionID + "\x00" + in.AgentID + "\x00" + in.ToolName + "\x00" + input
}

// decide records that another hook answered one call in: the earliest card waiting for it is withdrawn, or, with
// none waiting, the answer is kept for the next card that asks.
func (d *decisions) decide(in hookInput, now time.Time) {
	key := permissionKey(in)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = true
	for _, w := range d.waiting[key] {
		if !w.answered {
			w.answered = true
			w.cancel()
			return
		}
	}
	d.forget(now)
	if d.unused == nil {
		d.unused = map[string][]time.Time{}
	}
	d.unused[key] = append(d.unused[key], now)
}

// forget drops answers kept longer than decidedKeep. d.mu is held.
func (d *decisions) forget(now time.Time) {
	for k, ats := range d.unused {
		i := 0
		for i < len(ats) && now.Sub(ats[i]) > decidedKeep {
			i++
		}
		if i == len(ats) {
			delete(d.unused, k)
		} else {
			d.unused[k] = ats[i:]
		}
	}
}

// watch returns a context that ends when another hook answers this call (or ctx ends), a function saying whether it
// did, and the function that stops watching. An answer kept for key is taken now.
func (d *decisions) watch(ctx context.Context, key string) (context.Context, func() bool, func()) {
	ctx, cancel := context.WithCancel(ctx)
	w := &decisionWaiter{cancel: cancel}
	d.mu.Lock()
	d.forget(time.Now())
	if len(d.unused[key]) > 0 {
		w.answered = true
		if d.unused[key] = d.unused[key][1:]; len(d.unused[key]) == 0 {
			delete(d.unused, key)
		}
	}
	d.waiting = addHookWaiter(d.waiting, key, w)
	answered := w.answered
	d.mu.Unlock()
	if answered {
		cancel()
	}
	return ctx, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return w.answered
		}, func() {
			d.mu.Lock()
			d.waiting = removeHookWaiter(d.waiting, key, w)
			d.mu.Unlock()
			cancel()
		}
}

// grace waits permissionGrace on a machine where hooks say hook.decided, and says whether the call was answered by
// then (ctx and answered are a watch's).
func (d *decisions) grace(ctx context.Context, answered func() bool) bool {
	d.mu.Lock()
	seen := d.seen
	d.mu.Unlock()
	if seen {
		t := time.NewTimer(permissionGrace)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	return answered()
}
