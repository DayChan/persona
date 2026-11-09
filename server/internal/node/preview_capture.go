package node

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"

	"github.com/nwverse/persona/server/internal/preview"
)

//go:embed preview_pick.js
var previewPickJS string

// screenshot sends a PNG of the page (what is in view, or the whole page up to a height) under id.
func (t *previewTab) screenshot(id uint32, full bool) {
	fail := func(err error) { t.send(map[string]any{"t": "shot_failed", "id": id, "error": err.Error()}) }
	capture := func(params map[string]any) {
		params["format"] = "png"
		t.async("Page.captureScreenshot", params, func(raw json.RawMessage, err error) {
			var r struct {
				Data string `json:"data"`
			}
			if err == nil {
				err = json.Unmarshal(raw, &r)
			}
			var png []byte
			if err == nil {
				png, err = base64.StdEncoding.DecodeString(r.Data)
			}
			if err == nil && len(png)+5 >= preview.MaxFrame {
				err = errors.New("the screenshot is too big to send")
			}
			if err != nil {
				fail(err)
				return
			}
			msg := make([]byte, 5, 5+len(png))
			msg[0] = previewShotMsg
			binary.BigEndian.PutUint32(msg[1:], id)
			t.write(preview.Binary, append(msg, png...))
		})
	}
	if !full {
		capture(map[string]any{})
		return
	}
	t.async("Page.getLayoutMetrics", nil, func(raw json.RawMessage, err error) {
		var m struct {
			Content struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"cssContentSize"`
		}
		if err != nil || json.Unmarshal(raw, &m) != nil || m.Content.Height <= 0 {
			capture(map[string]any{})
			return
		}
		capture(map[string]any{"captureBeyondViewport": true, "clip": map[string]any{
			"x": 0, "y": 0, "width": math.Max(m.Content.Width, float64(t.vp.w)), "height": math.Min(m.Content.Height, previewShotMaxHeight), "scale": 1,
		}})
	})
}

// pick turns element picking on or off: while on, the element under the pointer is outlined and a click picks it
// (Overlay.inspectNodeRequested) instead of clicking it.
func (t *previewTab) pick(on bool) {
	if on == t.picking {
		return
	}
	t.picking = on
	if on {
		t.cdp("DOM.enable", nil)
		t.cdp("Overlay.enable", nil)
		t.cdp("Overlay.setInspectMode", map[string]any{"mode": "searchForNode", "highlightConfig": map[string]any{
			"showInfo": true, "showAccessibilityInfo": false,
			"contentColor": map[string]any{"r": 57, "g": 135, "b": 229, "a": 0.25},
			"borderColor":  map[string]any{"r": 57, "g": 135, "b": 229, "a": 0.9},
			"paddingColor": map[string]any{"r": 27, "g": 175, "b": 122, "a": 0.18},
			"marginColor":  map[string]any{"r": 235, "g": 104, "b": 52, "a": 0.15},
		}})
		return
	}
	t.cdp("Overlay.setInspectMode", map[string]any{"mode": "none", "highlightConfig": map[string]any{}})
	t.cdp("Overlay.hideHighlight", nil)
}

// picked describes the element the user picked and sends it to the web.
func (t *previewTab) picked(backendNode int) {
	t.pick(false)
	t.send(map[string]any{"t": "pick", "on": false})
	t.async("DOM.resolveNode", map[string]any{"backendNodeId": backendNode}, func(raw json.RawMessage, err error) {
		var r struct {
			Object struct {
				ObjectID string `json:"objectId"`
			} `json:"object"`
		}
		if err != nil || json.Unmarshal(raw, &r) != nil || r.Object.ObjectID == "" {
			t.send(map[string]any{"t": "pick_failed"})
			return
		}
		t.async("Runtime.callFunctionOn", map[string]any{
			"objectId": r.Object.ObjectID, "functionDeclaration": previewPickJS, "returnByValue": true,
		}, func(raw json.RawMessage, err error) {
			var res struct {
				Result struct {
					Value json.RawMessage `json:"value"`
				} `json:"result"`
			}
			if err != nil || json.Unmarshal(raw, &res) != nil || len(res.Result.Value) == 0 || string(res.Result.Value) == "null" {
				t.send(map[string]any{"t": "pick_failed"})
				return
			}
			var el map[string]any
			if json.Unmarshal(res.Result.Value, &el) != nil {
				t.send(map[string]any{"t": "pick_failed"})
				return
			}
			el["t"], el["url"], el["title"] = "picked", t.url, t.title
			t.send(el)
		})
	})
}
