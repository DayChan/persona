package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/preview"
)

// previewPage: a button that renames the page, a field that does too as it is typed in, and a link elsewhere.
const previewPage = `<!doctype html><html><head><title>Hi</title><style>
body { margin: 0 } #go { position: absolute; left: 0; top: 0; width: 100px; height: 50px }
#in { position: absolute; left: 0; top: 100px; width: 200px; height: 40px }
#ext { position: absolute; left: 0; top: 200px; width: 200px; height: 40px; display: block }
#next { position: absolute; left: 0; top: 300px; width: 200px; height: 40px; display: block }
</style></head><body>
<button id="go" onclick="document.title='clicked'">Go</button>
<input id="in" oninput="document.title='v:'+this.value">
<a id="ext" href="https://example.com/away">elsewhere</a>
<a id="next" href="/second">second</a>
</body></html>`

// liveTab opens a preview tab of the browser on this computer, at url, and returns the web's end of its stream.
func liveTab(t *testing.T, url string) (*bufio.Reader, net.Conn) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := findBrowser(); err != nil {
		t.Skipf("no browser here: %v", err)
	}
	prof := t.TempDir()
	was := previewProfileDir
	previewProfileDir = func() (string, error) { return prof, nil }
	t.Cleanup(func() { previewProfileDir = was })
	h := &previewHost{logf: log.Printf}
	t.Cleanup(h.close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b, err := h.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := b.newTab(ctx, nil, previewViewport{w: 400, h: 400, dpr: 1}.clamp())
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := net.Pipe()
	tab.conn = mine
	go func() {
		defer h.release()
		tab.run(url)
	}()
	t.Cleanup(func() { theirs.Close() })
	return bufio.NewReader(theirs), theirs
}

type liveMsg struct {
	kind byte
	json map[string]any
	bin  []byte
}

// next reads messages until want says one is it, acknowledging pictures as the web does.
func next(t *testing.T, r *bufio.Reader, w net.Conn, what string, want func(liveMsg) bool) liveMsg {
	t.Helper()
	got := make(chan liveMsg)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			k, p, err := preview.ReadFrame(r)
			if err != nil {
				return
			}
			m := liveMsg{kind: k, bin: p}
			if k == preview.Text {
				_ = json.Unmarshal(p, &m.json)
			} else if len(p) > 0 && p[0] == previewFrameMsg {
				say(t, w, map[string]any{"t": "ack", "seq": binary.BigEndian.Uint32(p[1:])})
			}
			select {
			case got <- m:
			case <-stop:
				return
			}
			if want(m) {
				return
			}
		}
	}()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case m := <-got:
			if want(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s within 20 s", what)
		}
	}
}

func say(t *testing.T, w net.Conn, v any) {
	b, _ := json.Marshal(v)
	if err := preview.WriteFrame(w, preview.Text, b); err != nil {
		t.Errorf("write: %v", err)
	}
}

func isText(typ string, more func(map[string]any) bool) func(liveMsg) bool {
	return func(m liveMsg) bool {
		return m.kind == preview.Text && m.json["t"] == typ && (more == nil || more(m.json))
	}
}

func click(t *testing.T, w net.Conn, x, y float64) {
	say(t, w, map[string]any{"t": "mouse", "type": "move", "x": x, "y": y})
	say(t, w, map[string]any{"t": "mouse", "type": "down", "x": x, "y": y, "button": "left", "buttons": 1, "clicks": 1})
	say(t, w, map[string]any{"t": "mouse", "type": "up", "x": x, "y": y, "button": "left", "clicks": 1})
}

func TestPreviewDrivesARealBrowser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/away" {
			http.Redirect(w, r, "https://example.com/redirected", http.StatusFound)
			return
		}
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>two</p>`)
			return
		}
		fmt.Fprint(w, previewPage)
	}))
	defer srv.Close()
	home := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/"
	r, w := liveTab(t, home)

	next(t, r, w, "ready", isText("ready", nil))
	next(t, r, w, "the page", isText("page", func(m map[string]any) bool { return m["url"] == home && m["title"] == "Hi" }))
	f := next(t, r, w, "a picture", func(m liveMsg) bool { return m.kind == preview.Binary && m.bin[0] == previewFrameMsg })
	if !bytes.HasPrefix(f.bin[9:], []byte{0xff, 0xd8}) || binary.BigEndian.Uint16(f.bin[5:]) != 400 {
		t.Fatalf("picture: % x…, width %d", f.bin[:12], binary.BigEndian.Uint16(f.bin[5:]))
	}

	click(t, w, 50, 25)
	next(t, r, w, "the click", isText("page", func(m map[string]any) bool { return m["title"] == "clicked" }))

	click(t, w, 100, 120)
	say(t, w, map[string]any{"t": "key", "type": "down", "key": "a", "code": "KeyA", "key_code": 65, "text": "a"})
	say(t, w, map[string]any{"t": "key", "type": "up", "key": "a", "code": "KeyA", "key_code": 65})
	say(t, w, map[string]any{"t": "text", "text": "好"})
	next(t, r, w, "the typing", isText("page", func(m map[string]any) bool { return m["title"] == "v:a好" }))

	// a link elsewhere is not followed: the web is told, the page stays
	click(t, w, 50, 220)
	next(t, r, w, "the link elsewhere", isText("external", func(m map[string]any) bool { return m["url"] == "https://example.com/away" }))
	say(t, w, map[string]any{"t": "nav", "url": "/away"}) // a local address that redirects elsewhere
	next(t, r, w, "the redirect elsewhere", isText("external", func(m map[string]any) bool { return m["url"] == "https://example.com/redirected" }))
	say(t, w, map[string]any{"t": "nav", "url": "http://192.0.2.10:8080/"})
	next(t, r, w, "the address elsewhere", isText("external", func(m map[string]any) bool { return m["url"] == "http://192.0.2.10:8080/" }))

	say(t, w, map[string]any{"t": "shot", "id": 7})
	s := next(t, r, w, "the screenshot", func(m liveMsg) bool { return m.kind == preview.Binary && m.bin[0] == previewShotMsg })
	if binary.BigEndian.Uint32(s.bin[1:]) != 7 || !bytes.HasPrefix(s.bin[5:], []byte("\x89PNG")) {
		t.Fatalf("screenshot: % x…", s.bin[:12])
	}

	say(t, w, map[string]any{"t": "pick", "on": true})
	time.Sleep(200 * time.Millisecond)
	click(t, w, 50, 25)
	p := next(t, r, w, "the picked element", isText("picked", nil))
	if p.json["selector"] != "button#go" || p.json["tag"] != "button" || p.json["text"] != "Go" || p.json["url"] != home {
		t.Fatalf("picked: %v", p.json)
	}

	// a link on this computer is followed, and back comes back
	click(t, w, 50, 320)
	next(t, r, w, "the second page", isText("page", func(m map[string]any) bool { return m["title"] == "Second" && m["back"] == true }))
	say(t, w, map[string]any{"t": "back"})
	next(t, r, w, "back", isText("page", func(m map[string]any) bool { return m["url"] == home && m["forward"] == true }))

	say(t, w, map[string]any{"t": "size", "w": 300, "h": 500, "dpr": 2, "mobile": true})
	f = next(t, r, w, "a phone-sized picture", func(m liveMsg) bool {
		return m.kind == preview.Binary && m.bin[0] == previewFrameMsg && binary.BigEndian.Uint16(m.bin[5:]) == 300
	})
	if binary.BigEndian.Uint16(f.bin[7:]) != 500 {
		t.Fatalf("phone-sized picture is %d high", binary.BigEndian.Uint16(f.bin[7:]))
	}
}
