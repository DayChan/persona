package node

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"

	"github.com/nwverse/persona/server/internal/preview"
)

type previewHistory struct {
	CurrentIndex int `json:"currentIndex"`
	Entries      []struct {
		ID  int    `json:"id"`
		URL string `json:"url"`
	} `json:"entries"`
}

// event handles a CDP event of the tab; true when the tab is gone.
func (t *previewTab) event(method string, params json.RawMessage) bool {
	switch method {
	case "Page.screencastFrame":
		var f struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
			Metadata  struct {
				DeviceWidth  float64 `json:"deviceWidth"`
				DeviceHeight float64 `json:"deviceHeight"`
			} `json:"metadata"`
		}
		if json.Unmarshal(params, &f) != nil {
			return false
		}
		jpeg, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			t.cdp("Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID})
			return false
		}
		t.seq++
		msg := make([]byte, 9, 9+len(jpeg))
		msg[0] = previewFrameMsg
		binary.BigEndian.PutUint32(msg[1:], t.seq)
		binary.BigEndian.PutUint16(msg[5:], uint16(math.Round(f.Metadata.DeviceWidth)))
		binary.BigEndian.PutUint16(msg[7:], uint16(math.Round(f.Metadata.DeviceHeight)))
		t.write(preview.Binary, append(msg, jpeg...))
		t.unacked++
		if t.unacked < previewInFlight {
			t.cdp("Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID})
		} else {
			t.heldAck = append(t.heldAck, f.SessionID)
		}
	case "Fetch.requestPaused":
		var r struct {
			RequestID    string `json:"requestId"`
			FrameID      string `json:"frameId"`
			ResourceType string `json:"resourceType"`
			Request      struct {
				URL string `json:"url"`
			} `json:"request"`
		}
		if json.Unmarshal(params, &r) != nil {
			return false
		}
		if r.ResourceType == "Document" && (r.FrameID == t.frame || r.FrameID == "") && !allowedTop(r.Request.URL) {
			// A 204 answer leaves the page where it was (an error page would replace it); the web opens the link.
			t.cdp("Fetch.fulfillRequest", map[string]any{"requestId": r.RequestID, "responseCode": 204, "responseHeaders": []any{}})
			t.send(map[string]any{"t": "external", "url": r.Request.URL})
			return false
		}
		t.cdp("Fetch.continueRequest", map[string]any{"requestId": r.RequestID})
	case "Page.frameNavigated":
		var f struct {
			Frame struct {
				ID             string `json:"id"`
				ParentID       string `json:"parentId"`
				URL            string `json:"url"`
				URLFragment    string `json:"urlFragment"`
				UnreachableURL string `json:"unreachableUrl"`
			} `json:"frame"`
		}
		if json.Unmarshal(params, &f) == nil && f.Frame.ParentID == "" {
			t.frame = f.Frame.ID
			t.url = f.Frame.URL + f.Frame.URLFragment
			if f.Frame.UnreachableURL != "" {
				t.url = f.Frame.UnreachableURL
			}
			t.pushPage(true)
		}
	case "Page.navigatedWithinDocument":
		var f struct {
			FrameID string `json:"frameId"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(params, &f) == nil && f.FrameID == t.frame {
			t.url = f.URL
			t.pushPage(true)
		}
	case "Page.frameStartedLoading", "Page.frameStoppedLoading":
		var f struct {
			FrameID string `json:"frameId"`
		}
		if json.Unmarshal(params, &f) == nil && f.FrameID == t.frame {
			t.loading = method == "Page.frameStartedLoading"
			t.pushPage(!t.loading)
		}
	case "Runtime.bindingCalled":
		var c struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		if json.Unmarshal(params, &c) == nil && c.Name == previewTitleBinding && c.Payload != t.title {
			if r := []rune(c.Payload); len(r) > 300 { // the page can call the function with anything
				c.Payload = string(r[:300])
			}
			t.title = c.Payload
			t.pushPage(false)
		}
	case "Target.targetCreated": // a window the page opened: shown here when it is this computer's, else in a new tab
		var i struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				OpenerID string `json:"openerId"`
				Type     string `json:"type"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &i) == nil && i.TargetInfo.OpenerID == t.target && i.TargetInfo.Type == "page" {
			_ = t.b.cdp.sendNoReply("", "Target.closeTarget", map[string]any{"targetId": i.TargetInfo.TargetID})
		}
	case "Page.windowOpen":
		var w struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(params, &w) == nil && w.URL != "" {
			t.navigate(w.URL)
		}
	case "Page.javascriptDialogOpening":
		var d struct {
			Message       string `json:"message"`
			Type          string `json:"type"`
			DefaultPrompt string `json:"defaultPrompt"`
		}
		if json.Unmarshal(params, &d) == nil {
			t.send(map[string]any{"t": "dialog", "kind": d.Type, "message": d.Message, "default": d.DefaultPrompt})
		}
	case "Page.javascriptDialogClosed":
		t.send(map[string]any{"t": "dialog_closed"})
	case "Page.fileChooserOpened":
		t.send(map[string]any{"t": "notice", "code": "file_chooser"})
	case "Overlay.inspectNodeRequested":
		var r struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		if json.Unmarshal(params, &r) == nil && r.BackendNodeID != 0 {
			t.picked(r.BackendNodeID)
		}
	case "Inspector.targetCrashed":
		t.send(map[string]any{"t": "crashed"})
	case "Target.targetDestroyed", "Target.detachedFromTarget":
		t.send(map[string]any{"t": "closed", "code": "tab_closed"})
		return true
	}
	return false
}

// pushPage tells the web what the tab shows; with history, whether it can go back or forward too.
func (t *previewTab) pushPage(history bool) {
	send := func(back, forward bool) {
		msg := map[string]any{"t": "page", "url": t.url, "title": t.title, "loading": t.loading, "back": back, "forward": forward}
		b, _ := json.Marshal(msg)
		if string(b) == t.lastPage {
			return
		}
		t.lastPage = string(b)
		t.write(preview.Text, b)
	}
	var last struct {
		Back, Forward bool
	}
	_ = json.Unmarshal([]byte(t.lastPage), &last)
	if !history {
		send(last.Back, last.Forward)
		return
	}
	t.async("Page.getNavigationHistory", nil, func(raw json.RawMessage, err error) {
		var h previewHistory
		if err != nil || json.Unmarshal(raw, &h) != nil {
			// between two documents the page cannot say ("Not attached to an active page"): it is asked again
			// once the new one has loaded
			send(last.Back, last.Forward)
			return
		}
		// the about:blank the tab opened at is not somewhere to go back to
		first := 0
		if len(h.Entries) > 0 && h.Entries[0].URL == "about:blank" {
			first = 1
		}
		send(h.CurrentIndex > first, h.CurrentIndex < len(h.Entries)-1)
	})
}
