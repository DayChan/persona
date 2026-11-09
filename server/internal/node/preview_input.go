package node

import (
	"bufio"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/preview"
)

func (t *previewTab) readWeb() {
	br := bufio.NewReader(t.conn)
	for {
		kind, p, err := preview.ReadFrame(br)
		if err != nil {
			t.post(tabItem{gone: "web"})
			return
		}
		if kind != preview.Text {
			continue
		}
		var in previewIn
		if json.Unmarshal(p, &in) == nil && in.T != "" {
			t.post(tabItem{in: &in})
		}
	}
}

// allowedTop says whether the top frame may go to u: this computer's loopback addresses over http(s), or nothing at
// all (about:blank).
func allowedTop(u string) bool {
	if u == "about:blank" {
		return true
	}
	pu, err := url.Parse(u)
	return err == nil && (pu.Scheme == "http" || pu.Scheme == "https") && loopbackHost(pu.Hostname())
}

// resolve is an address typed in the panel as a URL: a path on the page's server, a host:port without a scheme, or a
// whole URL.
func (t *previewTab) resolve(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "/") {
		if cur, err := url.Parse(t.url); err == nil && cur.Host != "" {
			return cur.Scheme + "://" + cur.Host + s
		}
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return s
}

func (t *previewTab) navigate(u string) {
	if !allowedTop(u) {
		t.send(map[string]any{"t": "external", "url": u})
		return
	}
	t.async("Page.navigate", map[string]any{"url": u}, func(raw json.RawMessage, err error) {
		var r struct {
			ErrorText string `json:"errorText"`
		}
		_ = json.Unmarshal(raw, &r)
		if err != nil {
			r.ErrorText = err.Error()
		}
		if r.ErrorText != "" && r.ErrorText != "net::ERR_ABORTED" {
			t.send(map[string]any{"t": "failed", "url": u, "error": r.ErrorText})
		}
	})
}

// fromWeb does what the user did in the panel.
func (t *previewTab) fromWeb(in *previewIn) {
	switch in.T {
	case "mouse":
		typ := map[string]string{"down": "mousePressed", "up": "mouseReleased", "move": "mouseMoved"}[in.Type]
		if typ == "" {
			return
		}
		button := in.Button
		if button == "" {
			button = "none"
		}
		t.cdp("Input.dispatchMouseEvent", map[string]any{
			"type": typ, "x": in.X, "y": in.Y, "button": button, "buttons": in.Buttons, "clickCount": in.Clicks, "modifiers": in.Mods,
		})
	case "wheel":
		t.cdp("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": in.X, "y": in.Y, "deltaX": in.DX, "deltaY": in.DY, "modifiers": in.Mods})
	case "key":
		ev := map[string]any{
			"key": in.Key, "code": in.Code, "modifiers": in.Mods, "windowsVirtualKeyCode": in.KeyCode, "nativeVirtualKeyCode": in.KeyCode,
			"autoRepeat": in.Repeat, "location": in.Location, "isKeypad": in.Location == 3,
		}
		switch in.Type {
		case "down":
			ev["type"] = "rawKeyDown"
			if in.Text != "" {
				ev["type"], ev["text"], ev["unmodifiedText"] = "keyDown", in.Text, in.Text
			}
		case "up":
			ev["type"] = "keyUp"
		default:
			return
		}
		t.cdp("Input.dispatchKeyEvent", ev)
	case "text":
		if in.Text != "" {
			t.cdp("Input.insertText", map[string]any{"text": in.Text})
		}
	case "cmd":
		t.command(in.Name)
	case "ack":
		if t.unacked > 0 {
			t.unacked--
		}
		if len(t.heldAck) > 0 {
			t.cdp("Page.screencastFrameAck", map[string]any{"sessionId": t.heldAck[0]})
			t.heldAck = t.heldAck[1:]
		}
	case "nav":
		t.navigate(t.resolve(in.URL))
	case "reload":
		t.cdp("Page.reload", map[string]any{"ignoreCache": true})
	case "back", "forward":
		t.async("Page.getNavigationHistory", nil, func(raw json.RawMessage, err error) {
			var h previewHistory
			if err != nil || json.Unmarshal(raw, &h) != nil {
				return
			}
			i := h.CurrentIndex - 1
			if in.T == "forward" {
				i = h.CurrentIndex + 1
			}
			if i >= 0 && i < len(h.Entries) {
				t.cdp("Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID})
			}
		})
	case "size":
		vp := previewViewport{w: in.W, h: in.H, dpr: in.DPR, mobile: in.Mobile}.clamp()
		if vp != t.vp {
			ua := vp.mobile != t.vp.mobile
			t.vp = vp
			t.applyViewport(true)
			if ua { // the page asks who it is talking to as it loads
				t.cdp("Page.reload", nil)
			}
		}
	case "shot":
		t.screenshot(in.ID, in.Full)
	case "pick":
		t.pick(in.On)
	case "dialog":
		t.cdp("Page.handleJavaScriptDialog", map[string]any{"accept": in.Accept, "promptText": in.Text})
	}
}

// command is an editing command the panel's keyboard shortcut stands for (⌘A / Ctrl+A…): the page's own, since the
// browser here has no menu to take them from.
func (t *previewTab) command(name string) {
	switch name {
	case "selectAll", "undo", "redo", "delete":
		t.async("Runtime.evaluate", map[string]any{"expression": "document.execCommand(" + strconv.Quote(name) + ")"}, nil)
	case "copy", "cut":
		const sel = `(() => { const a = document.activeElement;
  if (a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA") && a.selectionStart !== a.selectionEnd)
    return a.value.slice(a.selectionStart, a.selectionEnd);
  return String(getSelection()); })()`
		t.async("Runtime.evaluate", map[string]any{"expression": sel, "returnByValue": true}, func(raw json.RawMessage, err error) {
			var r struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			if err == nil && json.Unmarshal(raw, &r) == nil && r.Result.Value != "" {
				t.send(map[string]any{"t": "copied", "text": r.Result.Value})
				if name == "cut" {
					t.async("Runtime.evaluate", map[string]any{"expression": `document.execCommand("delete")`}, nil)
				}
			}
		})
	}
}
