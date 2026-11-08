package hub

import (
	"context"
	"encoding/json"
	"time"
)

// A planned stop (systemctl restart, a deploy, an update: SIGTERM) that the apps should not notice
// (docs/operations/hub-restart.md): before anything is torn down, every app hears hub_restarting — it reconnects at once and
// keeps its screen as it is — and the machines as they are now are handed to the next hub (warm.go). What the stop
// itself causes afterwards (nodes disconnecting, their questions ending) is not broadcast (stopping). The event log
// is handed over last, once nothing more can be broadcast (saveEventLog).

// restartBackIn is what hub_restarting tells the apps to expect: how long a restart takes, roughly (they show nothing
// for a while longer than that).
const restartBackIn = 3 * time.Second

// beginStop runs once, when the stop is asked for, before the hub's context ends.
func (h *Hub) beginStop() {
	if h.stopping.Swap(true) {
		return
	}
	b, _ := json.Marshal(map[string]any{"type": "hub_restarting", "back_in_ms": restartBackIn.Milliseconds()})
	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		c.queue(b)
		clients = append(clients, c)
	}
	h.mu.Unlock()
	// Let it reach them before the close frame does (stopWebSockets), briefly: a suspended phone never reads it.
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		pending := false
		for _, c := range clients {
			if len(c.out) > 0 {
				pending = true
				break
			}
		}
		if !pending {
			break
		}
	}
	// The machines as the managers report them, for the next hub process (devices_warm.go); each manager hands its
	// own to its next process apart (internal/devices warm.go).
	h.saveDevicesWarm(context.Background())
	h.saveMachineEntries(context.Background()) // and what each one has, each machine last seen as of now (machine_entries.go)
}

// rememberLiveText keeps a session's message being generated (session.delta: the whole text so far, "" once it is in
// the transcript), for clients that subscribe in the middle of it: one reconnecting after a restart, whose node
// sends the text again when it connects (node: resumeAssistants), sees the answer go on where it was.
func (h *Hub) rememberLiveText(machineID, sessionID, text string) {
	key := [2]string{machineID, sessionID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if text == "" {
		delete(h.deltas, key)
		return
	}
	if h.deltas == nil {
		h.deltas = map[[2]string]string{}
	}
	h.deltas[key] = text
}

// sendLiveText follows a subscription's backlog with the session's live text, if a message is being generated.
func (h *Hub) sendLiveText(c *client, machineID, sessionID string) {
	h.mu.Lock()
	text := h.deltas[[2]string{machineID, sessionID}]
	h.mu.Unlock()
	if text != "" {
		c.sendJSON(deltaPush{Type: "delta", MachineID: machineID, SessionID: sessionID, Text: text})
	}
}

func (h *Hub) forgetLiveText(machineID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.deltas {
		if k[0] == machineID {
			delete(h.deltas, k)
		}
	}
}
