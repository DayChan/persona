package node

// A dev server preview (docs/web/preview.md): preview.open makes a tab of the headless browser (preview_browser.go) show
// http://localhost:<port><path> and ties it to a stream (streams.Mux, like a forwarded port's) on which each message
// between the web and the node is one preview.Frame. The node sends pictures of the page (Page.startScreencast) and
// what it is showing; the web sends what the user does in it. The page's top frame stays on this computer's loopback
// addresses: a link to anywhere else is not followed in the preview, the web is told to open it in a new tab.
//
// The tab's goroutine handles everything in order (CDP events and the web's messages) and never waits on a CDP
// answer that may depend on an event it has yet to handle (a navigation waits on Fetch.requestPaused): such calls run
// on goroutines of their own and post their result back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/preview"
	"github.com/nwverse/persona/server/internal/rpc"
)

// previewTitleBinding is the function the page's title is reported through (Runtime.addBinding): the browser does not
// say when a page sets its title.
const previewTitleBinding = "__personaPreviewTitle"

// previewTitleJS runs in each new document of the top frame and reports its title whenever it changes.
const previewTitleJS = `(() => {
  if (window.top !== window) return;
  let last = null;
  const check = () => {
    const t = document.title;
    if (t === last) return;
    last = t;
    try { window.` + previewTitleBinding + `(t); } catch (_) {}
  };
  new MutationObserver(check).observe(document, { subtree: true, childList: true, characterData: true });
  document.addEventListener("DOMContentLoaded", check);
  check();
})()`

// Viewport bounds (CSS pixels) and the picture's quality.
const (
	previewMinSide   = 200
	previewMaxWidth  = 3840
	previewMaxHeight = 2400
	previewMaxDPR    = 2
	previewQuality   = 70
	// previewInFlight: pictures sent and not yet drawn by the web. The browser makes the next one when one is
	// acknowledged, so a slow link gets fewer pictures, not a backlog.
	previewInFlight = 2
	// previewShotMaxHeight: a full-page screenshot stops here (CSS pixels).
	previewShotMaxHeight = 12000
)

// Binary messages to the web: a picture of the page or a screenshot.
const (
	previewFrameMsg byte = 'f' // seq (4), CSS width (2), CSS height (2), JPEG
	previewShotMsg  byte = 's' // id (4), PNG
)

// previewIn is a message from the web (docs/web/preview.md "Messages").
type previewIn struct {
	T string `json:"t"`
	// mouse: down | up | move; key: down | up
	Type    string  `json:"type,omitempty"`
	X       float64 `json:"x,omitempty"`
	Y       float64 `json:"y,omitempty"`
	Button  string  `json:"button,omitempty"`
	Buttons int     `json:"buttons,omitempty"`
	Clicks  int     `json:"clicks,omitempty"`
	// Mods: Alt 1, Ctrl 2, Meta 4, Shift 8 (CDP's)
	Mods     int     `json:"mods,omitempty"`
	DX       float64 `json:"dx,omitempty"`
	DY       float64 `json:"dy,omitempty"`
	Key      string  `json:"key,omitempty"`
	Code     string  `json:"code,omitempty"`
	KeyCode  int     `json:"key_code,omitempty"`
	Location int     `json:"location,omitempty"`
	Repeat   bool    `json:"repeat,omitempty"`
	Text     string  `json:"text,omitempty"`
	URL      string  `json:"url,omitempty"`
	W        int     `json:"w,omitempty"`
	H        int     `json:"h,omitempty"`
	DPR      float64 `json:"dpr,omitempty"`
	Mobile   bool    `json:"mobile,omitempty"`
	Seq      uint32  `json:"seq,omitempty"`
	ID       uint32  `json:"id,omitempty"`
	Full     bool    `json:"full,omitempty"`
	On       bool    `json:"on,omitempty"`
	Accept   bool    `json:"accept,omitempty"`
	Name     string  `json:"name,omitempty"`
}

type previewViewport struct {
	w, h   int
	dpr    float64
	mobile bool
}

func (v previewViewport) clamp() previewViewport {
	v.w = min(max(v.w, previewMinSide), previewMaxWidth)
	v.h = min(max(v.h, previewMinSide), previewMaxHeight)
	if v.dpr <= 0 || math.IsNaN(v.dpr) {
		v.dpr = 1
	}
	v.dpr = math.Min(math.Max(v.dpr, 1), previewMaxDPR)
	return v
}

// tabItem is one thing for the tab's goroutine: a CDP event, a message from the web, a result to apply, or the end.
type tabItem struct {
	method string
	params json.RawMessage
	in     *previewIn
	fn     func()
	gone   string // why the tab ends: the web left, the browser or the tab went away
}

type previewTab struct {
	n       *Node
	b       *previewBrowser
	target  string
	session string
	frame   string // the main frame's id
	conn    io.ReadWriteCloser
	ua      string // the browser's own user agent (a phone-sized preview changes it)

	wmu sync.Mutex // writes to conn

	qmu  sync.Mutex
	q    []tabItem
	wake chan struct{}

	vp       previewViewport
	seq      uint32
	unacked  int
	heldAck  []int // screencast frames not acknowledged to the browser yet
	url      string
	title    string
	loading  bool
	picking  bool
	lastPage string
}

func (t *previewTab) post(it tabItem) {
	t.qmu.Lock()
	t.q = append(t.q, it)
	t.qmu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *previewTab) take() []tabItem {
	t.qmu.Lock()
	q := t.q
	t.q = nil
	t.qmu.Unlock()
	return q
}

func (t *previewTab) write(kind byte, p []byte) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = preview.WriteFrame(t.conn, kind, p)
}

func (t *previewTab) send(v any) {
	b, err := json.Marshal(v)
	if err == nil {
		t.write(preview.Text, b)
	}
}

// cdp sends a command to the tab and does not wait for the answer (input, acknowledgements: in order, nothing to
// read back).
func (t *previewTab) cdp(method string, params any) {
	_ = t.b.cdp.sendNoReply(t.session, method, params)
}

// async runs a command whose answer matters on a goroutine of its own and hands the answer to then, on the tab's
// goroutine.
func (t *previewTab) async(method string, params any, then func(json.RawMessage, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var out json.RawMessage
		err := t.b.cdp.call(ctx, t.session, method, params, &out)
		if then != nil {
			t.post(tabItem{fn: func() { then(out, err) }})
		}
	}()
}

// previewOpenRPC is preview.open: a tab showing a port of this computer, on the stream id.
func (n *Node) previewOpenRPC(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		ID     string  `json:"id"`
		Port   int     `json:"port"`
		Path   string  `json:"path"`
		W      int     `json:"w"`
		H      int     `json:"h"`
		DPR    float64 `json:"dpr"`
		Mobile bool    `json:"mobile"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" || p.Port <= 0 || p.Port > 65535 {
		return nil, errors.New("preview.open: need id and port")
	}
	if p.Path == "" || p.Path[0] != '/' {
		p.Path = "/" + p.Path
	}
	start, err := url.Parse("http://localhost:" + strconv.Itoa(p.Port) + p.Path)
	if err != nil || start.Host != "localhost:"+strconv.Itoa(p.Port) {
		return nil, fmt.Errorf("preview.open: bad path %q", p.Path)
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	c, err := dialPort(dctx, p.Port, "")
	cancel()
	if err != nil {
		return nil, &rpc.Error{Code: "port_gone", Message: fmt.Sprintf("nothing is listening on port %d", p.Port)}
	}
	c.Close()
	host := n.previewHost()
	b, err := host.acquire(ctx)
	if err != nil {
		if errors.Is(err, errNoBrowser) {
			return nil, &rpc.Error{Code: "no_browser", Message: err.Error()}
		}
		return nil, &rpc.Error{Code: "browser_failed", Message: err.Error()}
	}
	vp := previewViewport{w: p.W, h: p.H, dpr: p.DPR, mobile: p.Mobile}.clamp()
	t, err := b.newTab(ctx, n, vp)
	if err != nil {
		host.release()
		return nil, &rpc.Error{Code: "browser_failed", Message: err.Error()}
	}
	mine, theirs := net.Pipe()
	if err := n.streams.Open(p.ID); err != nil {
		mine.Close()
		theirs.Close()
		t.close()
		host.release()
		return nil, err
	}
	n.streams.Start(p.ID, theirs)
	t.conn = mine
	go func() {
		defer host.release()
		t.run(start.String())
	}()
	return map[string]any{"browser": b.name}, nil
}

// newTab opens a page in the browser and sets it up; it shows nothing until run navigates it.
func (b *previewBrowser) newTab(ctx context.Context, n *Node, vp previewViewport) (*previewTab, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := b.cdp.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return nil, fmt.Errorf("could not open a tab: %w", err)
	}
	t := &previewTab{n: n, b: b, target: created.TargetID, wake: make(chan struct{}, 1), vp: vp}
	b.mu.Lock()
	b.targets[t.target] = t
	b.mu.Unlock()
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.cdp.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.target, "flatten": true}, &attached); err != nil {
		t.close()
		return nil, fmt.Errorf("could not attach to the tab: %w", err)
	}
	t.session = attached.SessionID
	b.mu.Lock()
	b.pages[t.session] = t
	b.mu.Unlock()
	var version struct {
		UserAgent string `json:"userAgent"`
	}
	_ = b.cdp.call(ctx, "", "Browser.getVersion", nil, &version)
	t.ua = strings.Replace(version.UserAgent, "HeadlessChrome", "Chrome", 1)
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	steps := []struct {
		method string
		params any
		out    any
	}{
		{"Page.enable", nil, nil},
		{"Inspector.enable", nil, nil},
		{"Page.getFrameTree", nil, &tree},
		{"Runtime.enable", nil, nil},
		{"Runtime.addBinding", map[string]any{"name": previewTitleBinding}, nil},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": previewTitleJS}, nil},
		{"Page.setInterceptFileChooserDialog", map[string]any{"enabled": true}, nil},
		// The top frame's navigations are checked (top-level only; the page's own requests go as they would).
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "resourceType": "Document", "requestStage": "Request"}}}, nil},
	}
	for _, s := range steps {
		if err := b.cdp.call(ctx, t.session, s.method, s.params, s.out); err != nil {
			t.close()
			return nil, fmt.Errorf("could not set up the tab (%s): %w", s.method, err)
		}
	}
	t.frame = tree.FrameTree.Frame.ID
	t.applyViewport(false)
	return t, nil
}

// applyViewport sizes the page and (re)starts its pictures at that size.
func (t *previewTab) applyViewport(restart bool) {
	v := t.vp
	t.cdp("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": v.w, "height": v.h, "deviceScaleFactor": v.dpr, "mobile": v.mobile, "screenWidth": v.w, "screenHeight": v.h,
	})
	t.cdp("Emulation.setTouchEmulationEnabled", map[string]any{"enabled": v.mobile, "maxTouchPoints": 5})
	t.cdp("Emulation.setEmitTouchEventsForMouse", map[string]any{"enabled": v.mobile, "configuration": "mobile"})
	ua := t.ua
	if v.mobile && ua != "" {
		ua = mobileUA(ua)
	}
	if ua != "" {
		t.cdp("Emulation.setUserAgentOverride", map[string]any{"userAgent": ua})
	}
	if restart {
		t.cdp("Page.stopScreencast", nil)
		t.unacked, t.heldAck = 0, nil
	}
	t.cdp("Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": previewQuality, "maxWidth": int(float64(v.w) * v.dpr), "maxHeight": int(float64(v.h) * v.dpr), "everyNthFrame": 1,
	})
}

// mobileUA is a desktop Chrome user agent as a phone's (what a dev server's "is this a phone" check looks for).
func mobileUA(ua string) string {
	i, j := strings.Index(ua, "("), strings.Index(ua, ")")
	if i < 0 || j < i {
		return ua
	}
	ua = ua[:i] + "(Linux; Android 10; K)" + ua[j+1:]
	if k := strings.Index(ua, " Safari/"); k >= 0 && !strings.Contains(ua, "Mobile") {
		ua = ua[:k] + " Mobile" + ua[k:]
	}
	return ua
}

// close closes the tab in the browser.
func (t *previewTab) close() {
	t.b.mu.Lock()
	delete(t.b.targets, t.target)
	delete(t.b.pages, t.session)
	t.b.mu.Unlock()
	if t.target != "" {
		_ = t.b.cdp.sendNoReply("", "Target.closeTarget", map[string]any{"targetId": t.target})
	}
}

// run serves the tab until the web leaves or the tab goes away.
func (t *previewTab) run(start string) {
	defer t.conn.Close()
	defer t.close()
	go t.readWeb()
	t.send(map[string]any{"t": "ready", "browser": t.b.name, "w": t.vp.w, "h": t.vp.h})
	t.navigate(start)
	for range t.wake {
		for _, it := range t.take() {
			switch {
			case it.gone != "":
				if it.gone != "web" {
					t.send(map[string]any{"t": "closed", "code": it.gone})
				}
				return
			case it.fn != nil:
				it.fn()
			case it.in != nil:
				t.fromWeb(it.in)
			default:
				if t.event(it.method, it.params) {
					return
				}
			}
		}
	}
}
