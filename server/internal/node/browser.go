package node

// ego lite on this Mac, the agent browser the assistant drives (the built-in plugin ego, docs/plans/plan-ego-browser.md):
// what is there (browser.status), installing it from its official DMG and opening it for its first-run setup
// (browser.install, browser.open, browser.cancel), and running a script in it (browser.call). The hub asks only about
// a machine while the plugin is on.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// egoTeam is Citro Labs' Developer ID team: an app that is not signed by it is not installed.
	egoTeam = "JGQLC6YQYJ"
	egoName = "ego lite"
)

// egoDMG is the official DMG for this Mac's chip (the download link the project's README gives).
var egoDMG = func() string {
	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "x64"
	}
	return "https://cdn.ego.app/setup/macos/" + arch + "/egolite-Y7MbxKIuhzFB.dmg"
}

// egoHome is where ego keeps its things; tests point it elsewhere.
var egoHome = func() string { h, _ := os.UserHomeDir(); return h }

// egoApp is the installed app, "" when there is none.
var egoApp = func() string {
	for _, dir := range []string{"/Applications", filepath.Join(egoHome(), "Applications")} {
		p := filepath.Join(dir, egoName+".app")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// egoBin is the ego-browser CLI its first-run setup registers, "" before that.
func egoBin() string {
	p := filepath.Join(egoHome(), ".local", "bin", "ego-browser")
	if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

func egoData() string {
	return filepath.Join(egoHome(), "Library", "Application Support", "Citro Labs", egoName)
}

var egoJob jobSlot

// The last read-only probe through ego-browser, kept a minute: status is asked often, and the probe starts Node.
var egoProbe struct {
	mu   sync.Mutex
	at   time.Time
	ok   bool
	note string
}

func browserStatus(ctx context.Context) proto.Browser {
	b := proto.Browser{}
	if app := egoApp(); app != "" {
		b.Installed = true
		b.Version = plistString(ctx, filepath.Join(app, "Contents", "Info.plist"), "CFBundleShortVersionString")
	}
	b.SetUp, b.Imported, b.Analytics = egoSetUp()
	if b.Installed && b.SetUp && egoBin() != "" {
		b.Ready, b.Note = probeEgo(ctx)
	}
	if !b.Ready || b.Note == "" {
		b.Note = browserNote(b, b.Note)
	}
	b.ScreenSharing = screenSharing(ctx)
	b.Displays = screenDisplays(ctx, b.ScreenSharing)
	b.Job = egoJob.current()
	b.Checked = time.Now().UTC() // last: the hub keeps the answer with the latest (keepBrowser)
	return b
}

// egoSetUp reads ego's Local State: whether its first-run setup ran (ego.welcome_page_shown), whether that imported
// another browser's data, and Chromium's usage statistics (on while its consent file is there).
func egoSetUp() (setUp, imported bool, analytics string) {
	analytics = "unknown"
	data := egoData()
	raw, err := os.ReadFile(filepath.Join(data, "Local State"))
	if err != nil {
		return false, false, analytics
	}
	var state struct {
		Ego struct {
			Welcome  bool `json:"welcome_page_shown"`
			Imported int  `json:"onboarding_imported_browser_data"`
		} `json:"ego"`
	}
	_ = json.Unmarshal(raw, &state)
	analytics = "off"
	if _, err := os.Stat(filepath.Join(data, "Consent To Send Stats")); err == nil {
		analytics = "on"
	}
	return state.Ego.Welcome && egoBin() != "", state.Ego.Imported > 0, analytics
}

// probeEgo lists ego's task spaces through ego-browser (reads nothing of any page).
func probeEgo(ctx context.Context) (bool, string) {
	egoProbe.mu.Lock()
	defer egoProbe.mu.Unlock()
	if time.Since(egoProbe.at) < time.Minute {
		return egoProbe.ok, egoProbe.note
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, egoBin(), "nodejs", "-e", `console.log("ready " + (await listTaskSpaces()).length)`)
	out, err := cmd.CombinedOutput()
	egoProbe.at, egoProbe.ok, egoProbe.note = time.Now(), err == nil && bytes.Contains(out, []byte("ready ")), ""
	if !egoProbe.ok {
		egoProbe.note = "ego-browser 没跑通：" + oneLine(string(out), 2)
	}
	return egoProbe.ok, egoProbe.note
}

func browserNote(b proto.Browser, probe string) string {
	switch {
	case !b.Installed:
		return "这台 Mac 上没有装 ego lite"
	case !b.SetUp:
		return "ego lite 还没做完首次设置"
	case probe != "":
		return probe
	case !b.Ready:
		return "还不知道 ego-browser 能不能用"
	}
	return ""
}

func plistString(ctx context.Context, plist, key string) string {
	out, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", key, "raw", "-o", "-", plist).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---- install and open ----

// installEgo downloads the official DMG, checks the app in it (signed by Citro Labs' team, notarized), copies it into
// /Applications and checks it there again. What node downloads carries no quarantine (as with curl), so Gatekeeper's
// check is verifyEgoApp's spctl, before and after the copy.
func installEgo(ctx context.Context, step func(string)) error {
	if installOS != "darwin" {
		return errors.New("只能装在 Mac 上")
	}
	if egoApp() != "" {
		return errors.New("已经装好了")
	}
	app, done, err := fetchEgo(ctx, step)
	if err != nil {
		return err
	}
	defer done()
	step("装进「应用程序」")
	dest := filepath.Join("/Applications", egoName+".app")
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", app, dest).CombinedOutput(); err != nil {
		return fmt.Errorf("没装进「应用程序」：%s", oneLine(string(out), 2))
	}
	if err := verifyEgoApp(ctx, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	return nil
}

// fetchEgo downloads ego's DMG, mounts it read-only and checks the app in it (verifyEgoApp); done unmounts it and
// removes the download.
func fetchEgo(ctx context.Context, step func(string)) (app string, done func(), err error) {
	dir, err := os.MkdirTemp("", "persona-ego-install-")
	if err != nil {
		return "", nil, err
	}
	step("下载 ego lite")
	dmg := filepath.Join(dir, "egolite.dmg")
	if err := download(ctx, egoDMG(), dmg); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("下载安装包：%w", err)
	}
	step("校验签名和公证")
	mount := filepath.Join(dir, "mnt")
	if out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", mount, dmg).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("打不开安装包：%s", oneLine(string(out), 2))
	}
	done = func() {
		_ = exec.Command("/usr/bin/hdiutil", "detach", "-force", mount).Run()
		os.RemoveAll(dir)
	}
	app = filepath.Join(mount, egoName+".app")
	if err := verifyEgoApp(ctx, app); err != nil {
		done()
		return "", nil, err
	}
	return app, done, nil
}

// verifyEgoApp: the app's signature is whole, it is Citro Labs' team's, and Gatekeeper takes it as notarized. Not
// codesign's --strict: it fails on a Finder attribute inside the shipped app (2026-11-04, 0.6.2).
var verifyEgoApp = func(ctx context.Context, app string) error {
	if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", app).CombinedOutput(); err != nil {
		return fmt.Errorf("签名校验没通过：%s", oneLine(string(out), 2))
	}
	info, _ := exec.CommandContext(ctx, "/usr/bin/codesign", "-dv", "--verbose=2", app).CombinedOutput()
	if !strings.Contains(string(info), "TeamIdentifier="+egoTeam) {
		return errors.New("签名不是 Citro Labs 的，没有安装")
	}
	gk, _ := exec.CommandContext(ctx, "/usr/sbin/spctl", "-a", "-vv", "-t", "exec", app).CombinedOutput()
	if !strings.Contains(string(gk), "accepted") || !strings.Contains(string(gk), "Notarized") {
		return fmt.Errorf("没有通过公证检查：%s", oneLine(string(gk), 2))
	}
	return nil
}

// download fetches url to path (the DMG is a few hundred MB at most).
func download(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, 1<<30))
	return err
}

// openEgo brings ego lite up on the Mac's screen: its first-run setup is answered there (or over its live screen).
func openEgo(ctx context.Context, step func(string)) error {
	app := egoApp()
	if app == "" {
		return errors.New("这台 Mac 上没有装 ego lite")
	}
	step("在那台 Mac 上打开 ego lite")
	if out, err := exec.CommandContext(ctx, "/usr/bin/open", app).CombinedOutput(); err != nil {
		return fmt.Errorf("打不开 ego lite：%s", oneLine(string(out), 2))
	}
	return nil
}

// ---- running a script ----

// browserMost: the most of a script's output the model gets back.
const browserMost = 60000

// browserCall runs the assistant's script in ego (browser.call): in the conversation's own task space (its name is
// the hub's session label, so tabs and snapshot refs carry from one call to the next), with `task` and `page` (its
// tab p1) at hand; look takes the page's snapshot instead. A screenshot of the page comes back beside the output.
func browserCall(ctx context.Context, params json.RawMessage) (*proto.ComputerResult, error) {
	var p struct {
		Session    string `json:"session"`
		Script     string `json:"script"`
		Look       bool   `json:"look"`
		Full       bool   `json:"full"`
		Screenshot bool   `json:"screenshot"`
		TimeoutMS  int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	bin := egoBin()
	if bin == "" {
		return nil, errors.New("ego-browser 不在：先装 ego lite 并做完首次设置")
	}
	if p.Session == "" || (!p.Look && strings.TrimSpace(p.Script) == "") {
		return nil, errors.New("browser.call: need a session and a script")
	}
	dir, err := os.MkdirTemp("", "persona-browser-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	shot := filepath.Join(dir, "page.png")
	wrapped := browserScript(p.Session, p.Script, p.Look, p.Full, p.Screenshot || p.Look, shot)
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 2*time.Minute {
		timeout = time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "nodejs")
	cmd.Stdin = strings.NewReader(wrapped) // not in the process list, no length limit
	cmd.Dir = dir
	out := &keptOutput{headMost: browserHeadMost, tailMost: browserTailMost}
	cmd.Stdout, cmd.Stderr = out, out // the same writer: exec calls its Write from one goroutine at a time
	runErr := cmd.Run()
	text, result := splitBrowserOutput(out.String())
	if result == nil {
		if cctx.Err() != nil {
			return nil, fmt.Errorf("超时（%s）", timeout)
		}
		if runErr != nil {
			return nil, fmt.Errorf("ego-browser：%s", oneLine(text, 4))
		}
		return nil, errors.New("ego-browser 没有给出结果")
	}
	if text != "" {
		result["console"] = clipOutput(text, browserMost/4)
	}
	raw, _ := json.Marshal(result)
	res := &proto.ComputerResult{Output: json.RawMessage(clipJSON(raw))}
	res.Image = screenshotImage(shot)
	return res, nil
}

// What of a script's output is kept while it runs: the start of what it printed (the model gets a quarter of
// browserMost of it), and the end, where its result line is (a page's snapshot in it is cut to browserMost later).
// The rest is dropped as it comes, so a script or a page that prints without end does not grow the node (#3980).
const (
	browserHeadMost = 64 << 10
	browserTailMost = 16 << 20
)

// keptOutput keeps the first headMost bytes written to it and the last tailMost; what is between is counted, not
// kept.
type keptOutput struct {
	headMost, tailMost int
	head, tail         []byte
	dropped            int64
}

func (k *keptOutput) Write(p []byte) (int, error) {
	n := len(p)
	if room := k.headMost - len(k.head); room > 0 {
		take := min(room, len(p))
		k.head = append(k.head, p[:take]...)
		p = p[take:]
	}
	if len(k.tail)+len(p) > 2*k.tailMost { // slide now and then, not on every write
		cut := max(0, len(k.tail)+len(p)-k.tailMost)
		if cut >= len(k.tail) {
			k.dropped += int64(len(k.tail))
			cut -= len(k.tail)
			k.tail = k.tail[:0]
			k.dropped += int64(cut)
			p = p[cut:]
		} else {
			k.dropped += int64(cut)
			k.tail = k.tail[:copy(k.tail, k.tail[cut:])]
		}
	}
	if need := len(k.tail) + len(p); need > cap(k.tail) { // doubling, up to what the tail ever holds
		grown := make([]byte, len(k.tail), min(max(need, 2*cap(k.tail), 4<<10), 2*k.tailMost))
		copy(grown, k.tail)
		k.tail = grown
	}
	k.tail = append(k.tail, p...)
	return n, nil
}

// String is what was kept, with a line saying how much was left out between the start and the end.
func (k *keptOutput) String() string {
	tail, dropped := k.tail, k.dropped
	if len(tail) > k.tailMost {
		dropped += int64(len(tail) - k.tailMost)
		tail = tail[len(tail)-k.tailMost:]
	}
	gap := ""
	if dropped > 0 {
		gap = fmt.Sprintf("\n… (%d bytes left out)\n", dropped)
	}
	var b strings.Builder
	b.Grow(len(k.head) + len(gap) + len(tail))
	b.Write(k.head)
	b.WriteString(gap)
	b.Write(tail)
	return b.String()
}

// browserMark starts the line the wrapped script prints its result on.
const browserMark = "@@persona-browser@@"

// browserScript wraps the assistant's script: the task space and its first tab, the script as the body of an async
// function, and one last line with what it returned (or threw), the page's address and title, and the snapshot when
// it is a look.
func browserScript(session, script string, look, full, screenshot bool, shot string) string {
	var b strings.Builder
	b.WriteString("const task = await taskSpace(" + jsString(session) + ");\n")
	b.WriteString("const page = task.page(\"p1\");\n")
	b.WriteString("const __r = {};\n")
	b.WriteString("try {\n")
	if look {
		scope := `"only_within_viewport"`
		if full {
			scope = `"full_page"`
		}
		b.WriteString("  __r.snapshot = await page.snapshot({ scope: " + scope + " });\n")
	} else {
		b.WriteString("  __r.value = await (async () => {\n" + script + "\n  })() ?? null;\n")
	}
	b.WriteString("} catch (e) { __r.error = String(e && e.message || e); __r.code = e && e.code; }\n")
	b.WriteString("try { __r.url = await page.url(); __r.title = await page.title(); } catch (e) {}\n")
	if screenshot {
		b.WriteString("try { await page.screenshot({ path: " + jsString(shot) + " }); } catch (e) {}\n")
	}
	b.WriteString("console.log(" + jsString(browserMark) + " + JSON.stringify(__r));\n")
	return b.String()
}

// splitBrowserOutput: what the script printed, and its result line.
func splitBrowserOutput(out string) (string, map[string]any) {
	i := strings.LastIndex(out, browserMark)
	if i < 0 {
		return strings.TrimSpace(out), nil
	}
	line, _, _ := strings.Cut(out[i+len(browserMark):], "\n")
	var r map[string]any
	if json.Unmarshal([]byte(line), &r) != nil {
		return strings.TrimSpace(out), nil
	}
	return strings.TrimSpace(out[:i]), r
}

func clipOutput(s string, most int) string {
	if len(s) <= most {
		return s
	}
	cut := most
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d more bytes)", len(s)-cut)
}

// clipJSON keeps a result the model can read: a long snapshot is cut (as text), the rest as it is.
func clipJSON(raw []byte) []byte {
	if len(raw) <= browserMost {
		return raw
	}
	var r map[string]any
	if json.Unmarshal(raw, &r) == nil {
		if s, ok := r["snapshot"].(string); ok {
			r["snapshot"] = clipOutput(s, browserMost-2000)
		}
		if out, err := json.Marshal(r); err == nil && len(out) <= browserMost+200 {
			return out
		}
	}
	out, _ := json.Marshal(clipOutput(string(raw), browserMost)) // still JSON: the text as a string
	return out
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
