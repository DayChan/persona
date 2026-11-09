package herdrcore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Terminal is one terminal of terminal.list / terminal.get (the fields Persona reads).
type Terminal struct {
	// Address is the terminal's id on its core ("t7", "t1b"; "machine/t7" for another machine's): never reused on
	// that core, it resets only when the core's state is wiped.
	Address   string            `json:"address"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	State     string            `json:"state"` // "live" | "finished"
	Pid       int               `json:"pid"`
	CreatedMs int64             `json:"created_ms"`
	Cols      int               `json:"cols"`
	Rows      int               `json:"rows"`
	Title     string            `json:"title"`
	Turn      int64             `json:"turn"`
	// Status is the program's own OSC 7501 report; nil when it never said, or after it exited.
	Status     *Status     `json:"status"`
	Foreground *Foreground `json:"foreground"`
}

// Status is an OSC 7501 record: the program's words, untranslated.
type Status struct {
	App   string `json:"app"`
	State string `json:"state"` // idle | working | done | blocked | error
	Kind  string `json:"kind"`  // permission | question | auth (blocked only)
}

// Foreground is what runs in a terminal right now.
type Foreground struct {
	Pid  int      `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

// Remote: the terminal belongs to another machine connected to this core ("machine/t7").
func (t Terminal) Remote() bool { return strings.Contains(t.Address, "/") }

// List is terminal.list: the live terminals, this machine's and connected machines'.
func (c *Client) List(ctx context.Context) ([]Terminal, error) {
	var out struct {
		Terminals []Terminal `json:"terminals"`
	}
	err := c.Call(ctx, listTimeout, "terminal.list", nil, &out)
	return out.Terminals, err
}

// Get is terminal.get.
func (c *Client) Get(ctx context.Context, terminal string) (*Terminal, error) {
	var t Terminal
	if err := c.Call(ctx, callTimeout, "terminal.get", map[string]any{"terminal": terminal}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Read is terminal.read: the screen plus lines of history above it, as plain text (herdr-core has no colored read).
func (c *Client) Read(ctx context.Context, terminal string, lines int) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	err := c.Call(ctx, callTimeout, "terminal.read", map[string]any{"terminal": terminal, "lines": max(lines, 0)}, &out)
	return out.Text, err
}

// Send is terminal.send: pastes text and presses the submit key, once nobody has typed at the terminal for ~3 s. It
// refuses (Error kind "blocked") a program that reports a dialog.
func (c *Client) Send(ctx context.Context, terminal, text string) error {
	return c.Call(ctx, SendTimeout, "terminal.send", map[string]any{"terminal": terminal, "text": text}, nil)
}

// Keys is terminal.keys: named keys ("enter", "esc", "shift+tab", "ctrl+u", "up", "a"…), encoded for the program's
// keyboard modes.
func (c *Client) Keys(ctx context.Context, terminal string, keys ...string) error {
	return c.Call(ctx, keysTimeout, "terminal.keys", map[string]any{"terminal": terminal, "keys": keys}, nil)
}

// Kill is terminal.kill: ends the terminal's program (hangup, then terminate, then kill) and returns the terminal as
// it is after. Ended this way the terminal is finished even with keep set (keep restarts a shell only after a loss);
// one already finished is returned as it is.
func (c *Client) Kill(ctx context.Context, terminal string) (*Terminal, error) {
	var t Terminal
	if err := c.Call(ctx, killTimeout, "terminal.kill", map[string]any{"terminal": terminal}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Version is the core's version from hello, "" when it does not answer.
func (c *Client) Version(ctx context.Context) string {
	var out struct {
		Version string `json:"version"`
	}
	if c.Call(ctx, callTimeout, "hello", map[string]any{}, &out) != nil {
		return ""
	}
	return out.Version
}

// RunOpts is what terminal.run starts (the fields Persona sets).
type RunOpts struct {
	// Argv is the program and its arguments; none: the machine's own shell, as the Herdr app opens one.
	Argv   []string          `json:"argv,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	// Keep replaces a program that is lost (killed by a signal, its holder gone) with a shell; an exit or a kill is final.
	Keep bool `json:"keep,omitempty"`
	// NoParent: the terminal belongs to no other terminal (it would end with its parent's program).
	NoParent bool `json:"no_parent,omitempty"`
}

// Run is terminal.run without waiting: it starts the program in a new terminal and returns the terminal.
func (c *Client) Run(ctx context.Context, o RunOpts) (*Terminal, error) {
	var t Terminal
	if err := c.Call(ctx, runTimeout, "terminal.run", o, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Label updates terminal labels without replacing labels owned by other callers.
func (c *Client) Label(ctx context.Context, terminal string, labels map[string]string) error {
	return c.Call(ctx, callTimeout, "terminal.label", map[string]any{"terminal": terminal, "labels": labels}, nil)
}

// Doc is doc.get: an app's JSON document on the core (null when never written).
func (c *Client) Doc(ctx context.Context, name string, out any) error {
	raw, _, err := c.DocRaw(ctx, name)
	if err != nil || raw == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// DocRaw is doc.get as it comes: the document (nil when never written or deleted) and its version, which DocSet
// takes.
func (c *Client) DocRaw(ctx context.Context, name string) (json.RawMessage, uint64, error) {
	var res struct {
		Value   json.RawMessage `json:"value"`
		Version uint64          `json:"version"`
	}
	if err := c.Call(ctx, listTimeout, "doc.get", map[string]any{"name": name}, &res); err != nil {
		return nil, 0, err
	}
	if len(res.Value) == 0 || string(res.Value) == "null" {
		return nil, res.Version, nil
	}
	return res.Value, res.Version, nil
}

// DocSet is doc.set: writes the document while it is still at version (Error kind "conflict" when someone wrote it
// since: read it again and redo the change). The core emits a doc event, which the Herdr app follows.
func (c *Client) DocSet(ctx context.Context, name string, value any, version uint64) error {
	return c.Call(ctx, callTimeout, "doc.set", map[string]any{"name": name, "value": value, "version": version}, nil)
}

// Event is one change events.subscribe pushes: its type ("created", "status", "removed", "doc"…), its terminal
// ("" for doc events), or Type "missed" when the core dropped events and sent a fresh snapshot instead.
type Event struct {
	Type     string `json:"type"`
	Terminal string `json:"terminal"`
	// Data: a status event's new status (nil once the program cleared it).
	Data struct {
		Status *Status `json:"status"`
	} `json:"data"`
}

// Subscribe keeps one events.subscribe stream open (a plain connection that then carries only notifications), calling
// started once the core accepted it, and fn for each change, until ctx ends or the stream breaks or goes silent (the
// core sends a heartbeat every 15 s); it returns why it stopped.
func (c *Client) Subscribe(ctx context.Context, started func(), fn func(Event)) error {
	err := c.subscribe(ctx, started, fn)
	if ctx.Err() == nil {
		c.note("events.subscribe", err)
	}
	return err
}

func (c *Client) subscribe(ctx context.Context, started func(), fn func(Event)) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(callTimeout))
	id := c.seq.Add(1)
	if err := writeRequest(conn, id, "events.subscribe", map[string]any{}); err != nil {
		return fmt.Errorf("herdr-core events.subscribe: %w", err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	for live := false; ; {
		line, err := readLine(r, maxLine)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("herdr-core events: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(heartbeatGap))
		var msg response
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if !live {
			if msg.Method != "" {
				continue
			}
			if err := msg.decode(nil); err != nil {
				return err
			}
			_ = conn.SetWriteDeadline(time.Time{})
			live = true
			started()
			continue
		}
		switch msg.Method {
		case "event":
			var ev Event
			if json.Unmarshal(msg.Params, &ev) == nil {
				fn(ev)
			}
		case "missed":
			fn(Event{Type: "missed"})
		}
	}
}
