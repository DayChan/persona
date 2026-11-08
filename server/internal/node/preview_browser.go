package node

// The browser a preview runs in (docs/web/preview.md "The browser"): Chrome, Chromium, Edge or Brave on this computer,
// headless, with a profile of its own. One browser per node, started for the first preview and ended a while after
// the last one closes; each preview is a tab of it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// previewBrowserEnv names a browser to use instead of looking for one.
const previewBrowserEnv = "PERSONA_CHROME"

// previewIdle is how long the browser stays after its last tab closed (a reopened preview starts at once).
var previewIdle = 10 * time.Minute

// previewTabsMax: previews open at once on one computer.
const previewTabsMax = 6

// errNoBrowser: this computer has no browser to preview with.
var errNoBrowser = errors.New("no Chrome, Chromium, Edge or Brave on this computer")

type browserBin struct {
	name string // as people know it: "Google Chrome", "Chromium"…
	path string
}

// browserCandidates are the places a browser is looked for, in order of preference.
var browserCandidates = func() []browserBin {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		var out []browserBin
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			for _, b := range []browserBin{
				{"Google Chrome", "Google Chrome.app/Contents/MacOS/Google Chrome"},
				{"Chromium", "Chromium.app/Contents/MacOS/Chromium"},
				{"Microsoft Edge", "Microsoft Edge.app/Contents/MacOS/Microsoft Edge"},
				{"Brave", "Brave Browser.app/Contents/MacOS/Brave Browser"},
				{"Chrome for Testing", "Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"},
				{"Google Chrome Canary", "Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary"},
			} {
				out = append(out, browserBin{b.name, filepath.Join(dir, b.path)})
			}
		}
		return append(out, playwrightBrowsers(filepath.Join(home, "Library", "Caches", "ms-playwright"))...)
	case "linux":
		var out []browserBin
		for _, b := range []browserBin{
			{"Google Chrome", "google-chrome-stable"}, {"Google Chrome", "google-chrome"}, {"Chromium", "chromium"},
			{"Chromium", "chromium-browser"}, {"Microsoft Edge", "microsoft-edge"}, {"Brave", "brave-browser"},
		} {
			if p, err := exec.LookPath(b.path); err == nil {
				out = append(out, browserBin{b.name, p})
			}
		}
		return append(out, playwrightBrowsers(filepath.Join(home, ".cache", "ms-playwright"))...)
	}
	return nil
}

// playwrightBrowsers are the Chromium builds Playwright downloaded (a developer's computer often has one), newest
// first.
func playwrightBrowsers(cache string) []browserBin {
	dirs, _ := filepath.Glob(filepath.Join(cache, "chromium-*"))
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	var out []browserBin
	for _, d := range dirs {
		for _, rel := range []string{
			"chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			"chrome-mac/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			"chrome-mac/Chromium.app/Contents/MacOS/Chromium",
			"chrome-linux64/chrome", "chrome-linux/chrome",
		} {
			out = append(out, browserBin{"Chromium", filepath.Join(d, rel)})
		}
	}
	return out
}

// findBrowser is the browser previews run in: PERSONA_CHROME when set, else the first candidate that is there.
func findBrowser() (browserBin, error) {
	if p := strings.TrimSpace(os.Getenv(previewBrowserEnv)); p != "" {
		if executable(p) {
			return browserBin{strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)), p}, nil
		}
		return browserBin{}, fmt.Errorf("%s=%s is not a program", previewBrowserEnv, p)
	}
	for _, b := range browserCandidates() {
		if executable(b.path) {
			return b, nil
		}
	}
	return browserBin{}, errNoBrowser
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// previewProfileDir is the browser's own profile: a sign-in to the dev app is still there next time. It holds those
// sign-ins, so it is a private cache folder of this user (privateCacheDir): never a fixed name in the shared temp
// folder, where another local user could make it first and read them.
var previewProfileDir = func() (string, error) {
	return privateCacheDir("preview-browser")
}

// browserArgs are the switches the browser runs with: headless, CDP over the pipe, its own profile, nothing that
// phones home or asks the keychain.
func browserArgs(profile string) []string {
	args := []string{
		"--headless=new", "--remote-debugging-pipe", "--user-data-dir=" + profile,
		"--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-component-update",
		"--disable-default-apps", "--disable-sync", "--disable-extensions", "--metrics-recording-only", "--mute-audio",
		"--password-store=basic", "--use-mock-keychain", "--force-color-profile=srgb",
		"--disable-features=Translate,MediaRouter,OptimizationHints,DialMediaRouteProvider",
		"--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows",
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 { // Chrome's sandbox refuses root (a container)
		args = append(args, "--no-sandbox")
	}
	return append(args, "about:blank")
}

// previewBrowser is a running browser.
type previewBrowser struct {
	name    string // "Google Chrome 131"
	cdp     *cdpConn
	stop    func() // ends the process and its children
	exited  chan struct{}
	stderr  *tailBuffer
	mu      sync.Mutex
	pages   map[string]*previewTab // by CDP session
	targets map[string]*previewTab // by target id
}

// previewHost is the node's browser and its tabs.
type previewHost struct {
	mu       sync.Mutex
	b        *previewBrowser
	starting chan struct{} // closed when the start under way ends
	tabs     int
	idle     *time.Timer
	logf     func(string, ...any)
}

func (n *Node) previewHost() *previewHost {
	n.previewOnce.Do(func() { n.preview = &previewHost{logf: n.logger.Printf} })
	return n.preview
}

// acquire is the running browser, started when there is none, for one more tab; release gives it back.
func (h *previewHost) acquire(ctx context.Context) (*previewBrowser, error) {
	for {
		h.mu.Lock()
		if h.tabs >= previewTabsMax {
			h.mu.Unlock()
			return nil, fmt.Errorf("%d previews are open on this computer already", previewTabsMax)
		}
		if b := h.b; b != nil {
			select {
			case <-b.exited:
				h.b = nil
			default:
				h.tabs++
				if h.idle != nil {
					h.idle.Stop()
					h.idle = nil
				}
				h.mu.Unlock()
				return b, nil
			}
		}
		if wait := h.starting; wait != nil {
			h.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		h.starting = done
		h.mu.Unlock()
		b, err := startPreviewBrowser(ctx)
		h.mu.Lock()
		h.starting = nil
		close(done)
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		h.b = b
		h.mu.Unlock()
		h.logf("preview: started %s", b.name)
	}
}

func (h *previewHost) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tabs > 0 {
		h.tabs--
	}
	if h.tabs > 0 || h.b == nil {
		return
	}
	b := h.b
	if h.idle != nil {
		h.idle.Stop()
	}
	h.idle = time.AfterFunc(previewIdle, func() {
		h.mu.Lock()
		if h.tabs > 0 || h.b != b {
			h.mu.Unlock()
			return
		}
		h.b, h.idle = nil, nil
		h.mu.Unlock()
		b.stop()
		h.logf("preview: browser closed after %s without previews", previewIdle)
	})
}

// close ends the browser now (the node is stopping).
func (h *previewHost) close() {
	h.mu.Lock()
	b := h.b
	h.b = nil
	if h.idle != nil {
		h.idle.Stop()
		h.idle = nil
	}
	h.mu.Unlock()
	if b != nil {
		b.stop()
	}
}

// startPreviewBrowser starts the browser and connects to it.
func startPreviewBrowser(ctx context.Context) (*previewBrowser, error) {
	bin, err := findBrowser()
	if err != nil {
		return nil, err
	}
	profile, err := previewProfileDir()
	if err != nil {
		return nil, fmt.Errorf("the preview browser's profile folder: %w", err)
	}
	args := browserArgs(profile)
	b, v, err := connectBrowser(ctx, bin, args)
	if err != nil && strings.Contains(err.Error(), "No usable sandbox") && !slices.Contains(args, "--no-sandbox") {
		// Linux without unprivileged user namespaces for this build (Ubuntu's AppArmor rule lets only the packaged
		// Chrome have them, not a Chromium Playwright downloaded): as Playwright does there, without the sandbox
		b, v, err = connectBrowser(ctx, bin, append([]string{"--no-sandbox"}, args...))
	}
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, ver, ok := strings.Cut(v.Product, "/"); ok {
		major, _, _ := strings.Cut(ver, ".")
		b.name = bin.name + " " + major
	}
	_ = b.cdp.call(sctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil)
	_ = b.cdp.call(sctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "deny"}, nil)
	// the tab the browser opened with: previews make their own
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if b.cdp.call(sctx, "", "Target.getTargets", nil, &targets) == nil {
		for _, t := range targets.TargetInfos {
			if t.Type == "page" {
				_ = b.cdp.call(sctx, "", "Target.closeTarget", map[string]any{"targetId": t.TargetID}, nil)
			}
		}
	}
	go func() {
		select {
		case <-b.cdp.done:
		case <-b.exited:
		}
		b.cdp.close()
		b.mu.Lock()
		pages := make([]*previewTab, 0, len(b.pages))
		for _, t := range b.pages {
			pages = append(pages, t)
		}
		b.mu.Unlock()
		for _, t := range pages {
			t.post(tabItem{gone: "browser_exited"})
		}
	}()
	return b, nil
}

// connectBrowser starts the browser with args and waits for it to answer: what it says it is.
func connectBrowser(ctx context.Context, bin browserBin, args []string) (*previewBrowser, browserVersion, error) {
	b := &previewBrowser{name: bin.name, exited: make(chan struct{}), stderr: &tailBuffer{},
		pages: map[string]*previewTab{}, targets: map[string]*previewTab{}}
	r, w, stop, err := launchBrowser(bin.path, args, b.stderr, b.exited)
	if err != nil {
		return nil, browserVersion{}, fmt.Errorf("could not start %s: %w", bin.name, err)
	}
	b.stop = stop
	b.cdp = newCDPConn(r, w, b.event)
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var v browserVersion
	if err := b.cdp.call(sctx, "", "Browser.getVersion", nil, &v); err != nil {
		stop()
		if tail := strings.TrimSpace(b.stderr.String()); tail != "" {
			return nil, v, fmt.Errorf("%s did not answer: %v: %s", bin.name, err, lastLines(tail, 4))
		}
		return nil, v, fmt.Errorf("%s did not answer: %v", bin.name, err)
	}
	return b, v, nil
}

type browserVersion struct {
	Product string `json:"product"`
}

// event routes a CDP event: a page's to its tab, a target's to the tab it concerns.
func (b *previewBrowser) event(session, method string, params json.RawMessage) {
	b.mu.Lock()
	t := b.pages[session]
	if session == "" {
		var p struct {
			TargetID   string `json:"targetId"`
			TargetInfo struct {
				TargetID string `json:"targetId"`
				OpenerID string `json:"openerId"`
			} `json:"targetInfo"`
		}
		_ = json.Unmarshal(params, &p)
		switch {
		case p.TargetID != "":
			t = b.targets[p.TargetID]
		case p.TargetInfo.OpenerID != "" && method == "Target.targetCreated":
			t = b.targets[p.TargetInfo.OpenerID]
		default:
			t = b.targets[p.TargetInfo.TargetID]
		}
	}
	b.mu.Unlock()
	if t != nil {
		t.post(tabItem{method: method, params: params})
	}
}
