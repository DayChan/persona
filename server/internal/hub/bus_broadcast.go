package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A manager's messages to the apps (ws.broadcast, docs/assistant/managers.md "What the hub keeps of each manager"). The hub
// passes on only what a manager may say, about its own things, read the way the apps read it:
//
//   - its types: managerMessageTypes, and the types its own extensions declare for their api.broadcast (the
//     extensions' report); never one of the hub's own (hubMessageTypes). TestBroadcastTypes keeps both lists whole.
//     A manager from before the report carried the declared types (its report without EventsDeclared, or the old one
//     the hub kept) may send any type but the hub's: its extensions' types are not known.
//   - its own machines, sessions and conversations: every object of the message, at any depth, that names a
//     machine_id, session_id or conversation names one of the caller's (ownsRef).
//   - exact keys: the apps read exact keys and take a repeated key's last value, Go's encoding/json matches keys
//     regardless of case. A message with a key twice in one object, or with a key that differs only by case from one
//     the hub checks, is dropped, and what the apps get is the message as the hub read it (marshalled again), not the
//     bytes the manager sent.
//   - whose it is: a message from any manager but the assistant the hub starts gets top-level "manager" (its id,
//     replacing one it carried), so an app showing one manager's assistant page leaves the others' live updates out.
//     The assistant's go without it, as they always did (one it carried is taken out): no "manager" is the
//     assistant's (docs/protocol/protocol.md).

// managerMessageTypes are the types the managers send the apps (internal/assistant Service.event, internal/extmgr
// Manager.broadcast and broadcastExtension).
var managerMessageTypes = map[string]bool{
	// the assistant
	"assistant_feed": true, "assistant_schedule": true, "assistant_view": true, "assistant_view_action": true,
	"assistant_view_block": true, "agents.triage": true, "interaction_reanswered": true, "kb_selection": true, "launcher_health": true,
	"model_profile": true, "task_plan": true, "todos": true, "workspace_board": true, "workspace_board_settings": true,
	// its extensions' manager
	"agents": true, "agents.prefs": true, "inbox": true, "mail_accounts": true, "messaging": true,
	"extension_auth": true, "extension_operation": true, "extension_ui": true, "plugin_ui": true,
	"extension_upgrade": true, "plugin_upgrade": true,
}

// hubMessageTypes are the messages the hub sends the apps from its own state (cards, machines, sessions and their
// items, errors, the Live Activity, looks, the roster, its update…): no manager's message is one, not even one its
// extensions declare.
var hubMessageTypes = map[string]bool{
	"hello": true, "client_hello_ack": true, "pong": true, "notify": true, "lock_test": true, "error": true,
	"interaction": true, "interaction_resolved": true,
	"machines": true, "sessions": true, "sessions_delta": true, "sessions_list": true, "items": true, "revs": true,
	"delta": true, "live": true,
	"screen": true, "tool_call": true, "hook_event": true, "fs_changed": true, "desktops": true, "endpoints": true,
	"terminal": true, "terminal_control": true, "herdr_state": true, "herdr_focus": true, "herdr_end": true, "herdr_data": true,
	"fleet": true, "liveactivity": true, "look": true, "personas": true, "managers": true, "hub_update": true,
	"hub_restarting":      true,
	"extension_ui_stream": true, // reserved targeted transport; never a manager broadcast
}

// checkedKeys are the keys the hub reads to place a message: a key that differs from one only by case is refused, at
// any depth (ownRefs reads them at any depth).
var checkedKeys = []string{"type", "machine_id", "session_id", "conversation"}

// stampKeys are the keys the hub sets on the message itself: a key that differs from one only by case is refused at the
// top level (a "Manager" beside the hub's stamp would read as the sender's to a lenient decoder); deeper down they are
// the sender's own fields (a contact record's "Manager"), which the hub neither reads nor sets.
var stampKeys = []string{ManagerQuery}

// relayBroadcast is relayBroadcastFrom the assistant the hub starts.
func (h *Hub) relayBroadcast(message json.RawMessage) error {
	return h.relayBroadcastFrom(assistantwire.AssistantID, message)
}

// relayBroadcastFrom sends message, manager from's, to every app (see the top of the file for what is dropped).
func (h *Hub) relayBroadcastFrom(from string, message json.RawMessage) error {
	if err := exactJSON(message); err != nil {
		return fmt.Errorf("dropped: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(message))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return errors.New("a message is a JSON object with a type")
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		return errors.New("a message is a JSON object with a type")
	}
	if !h.managerMaySend(from, typ) {
		return fmt.Errorf("%q is not a manager's message: dropped", clip(typ, 64))
	}
	if err := h.ownRefs(from, m); err != nil {
		return fmt.Errorf("%q: %w: dropped", clip(typ, 64), err)
	}
	if from == assistantwire.AssistantID {
		delete(m, ManagerQuery) // the hub says whose a message is, not the message
	} else {
		m[ManagerQuery] = from
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return err
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if typ == extensionOperationEvent {
		h.toWanting(extensionOperationsFeature, out)
		return nil
	}
	h.broadcastRaw(typ, out)
	return nil
}

// managerMaySend: typ is a type of manager from's messages: one of the managers' own, or one its running extensions
// declare (an api contribution's events), and never one of the hub's. An older manager's report does not say what
// its extensions declare (EventsDeclared): from it any type but the hub's goes.
func (h *Hub) managerMaySend(from, typ string) bool {
	if hubMessageTypes[typ] {
		return false
	}
	if managerMessageTypes[typ] {
		return true
	}
	if r := h.extReportOf(from); r != nil {
		if !r.EventsDeclared {
			return true
		}
		for _, a := range r.APIs {
			if slices.Contains(a.Events, typ) {
				return true
			}
		}
	}
	return false
}

// ownRefs: every object in v, at any depth, names only manager from's machines, sessions and conversations
// (machine_id, session_id, conversation: strings, or null).
func (h *Hub) ownRefs(from string, v any) error {
	switch v := v.(type) {
	case map[string]any:
		ref := func(k string) (string, error) {
			switch s := v[k].(type) {
			case nil:
				return "", nil
			case string:
				return s, nil
			}
			return "", fmt.Errorf("%s is not a string", k)
		}
		mid, err := ref("machine_id")
		if err != nil {
			return err
		}
		sid, err := ref("session_id")
		if err != nil {
			return err
		}
		conv, err := ref("conversation")
		if err != nil {
			return err
		}
		if !h.ownsRef(from, mid, sid) || !h.ownsRef(from, "", conv) {
			return errors.New("about another manager's machine, session or conversation")
		}
		for _, x := range v {
			if err := h.ownRefs(from, x); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range v {
			if err := h.ownRefs(from, x); err != nil {
				return err
			}
		}
	}
	return nil
}

// exactJSON refuses a JSON object the apps could read otherwise than the hub: one value and nothing after it, no key
// twice in an object, no key that differs only by case from one the hub checks (checkedKeys) at any depth, or from one
// it sets (stampKeys) at the top level.
func exactJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		object, wantKey bool
		keys            map[string]bool
	}
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}
	values := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(stack) == 0 {
			if values++; values > 1 {
				return errors.New("more than one JSON value")
			}
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:n-1]
				valueDone()
				continue
			}
			k, _ := tok.(string)
			top := stack[n-1]
			if top.keys[k] {
				return fmt.Errorf("key %q twice in an object", clip(k, 64))
			}
			checked := checkedKeys
			if n == 1 {
				checked = append(slices.Clip(checked), stampKeys...)
			}
			for _, c := range checked {
				if k != c && strings.EqualFold(k, c) {
					return fmt.Errorf("key %q is %q in another case", clip(k, 64), c)
				}
			}
			top.keys[k], top.wantKey = true, false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, wantKey: true, keys: map[string]bool{}})
		case json.Delim('['):
			stack = append(stack, &frame{})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
	if values == 0 || len(stack) > 0 {
		return errors.New("not one whole JSON value")
	}
	return nil
}
