package extension

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Installing manifest.tools (docs/extensions/extensions.md "Command-line programs (tools)"). The layout, under the registry's directory:
//
//	tools/<id>/<name>-<spec hash>/pkg/…        what was downloaded and unpacked
//	tools/<id>/<name>-<spec hash>/bin/<name>   the program (a link into pkg)
//	tools/<id>/machine-<name>/bin/<name>       a link to the machine's own copy, when that is used
//	tools/<id>/versions/<version>.json         which of those a version of the extension runs with
//
// A spec hash is the same install, so an upgrade that keeps a tool does not download it again, and a rollback finds
// the previous version's. prune and Uninstall clean up what no kept version uses.

// ErrTools: a command-line program could not be installed.
var ErrTools = errors.New("tools failed")

// Tool limits: an unpacked tool (Node.js is about 200 MB in 6000 files) and how long a check may take.
const (
	toolMostBytes = 1 << 30
	toolMostFiles = 100000
	toolCheckWait = 10 * time.Second
)

// ResolvedTool is how a version of an extension runs one of its tools.
type ResolvedTool struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
	// Reused: the machine's own copy (Path), not one the hub installed.
	Reused bool `json:"reused,omitempty"`
	// Bin is the directory put on PATH (it holds <name>).
	Bin string `json:"bin"`
	// Path is the program.
	Path string `json:"path"`
}

// ToolsDir is where an extension's tools are.
func (r *Registry) ToolsDir(id string) string { return r.toolsDir(r.storageKey(id)) }

func (r *Registry) toolsDir(id string) string { return filepath.Join(r.dir, "tools", id) }

func (r *Registry) toolsVersionFile(id, version string) string {
	return filepath.Join(r.toolsDir(id), "versions", version+".json")
}

// Tools are the tools a version of id runs with (nil when it has none, or they are not installed).
func (r *Registry) Tools(id, version string) []ResolvedTool {
	return r.toolsAt(r.storageKey(id), version)
}

func (r *Registry) toolsAt(storageID, version string) []ResolvedTool {
	b, err := os.ReadFile(r.toolsVersionFile(storageID, version))
	if err != nil {
		return nil
	}
	var out []ResolvedTool
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// ToolPath is PATH with the bin directories of the tools of a version of id first, in the manifest's order of
// requirement; path unchanged when it has none.
func (r *Registry) ToolPath(id, version, path string) string {
	tools := r.Tools(id, version)
	if len(tools) == 0 {
		return path
	}
	var dirs []string
	for i := len(tools) - 1; i >= 0; i-- { // the last installed (the one that needs the others) first
		if !slices.Contains(dirs, tools[i].Bin) {
			dirs = append(dirs, tools[i].Bin)
		}
	}
	if path != "" {
		dirs = append(dirs, path)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// installTools installs the tools of version m.Version of extension m.ID, reporting StepTools on ctx. Nothing of a
// failed install is left for that version (the tool directories it made may stay: they are complete or absent).
func (r *Registry) installTools(ctx context.Context, m *Manifest) error {
	return r.installToolsAt(ctx, m, r.storageKey(m.ID))
}

func (r *Registry) installToolsAt(ctx context.Context, m *Manifest, storageID string) error {
	if len(m.Tools) == 0 {
		reportState(ctx, StepTools, StepSkipped)
		return nil
	}
	reportState(ctx, StepTools, StepRunning)
	for _, t := range m.ToolOrder() {
		report(ctx, StepTools, StepEvent{State: StepRunning, Tool: t.Name, ToolState: StepPending})
	}
	platform := r.platform
	if platform == "" {
		platform = Platform()
	}
	root := r.toolsDir(storageID)
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		return err
	}
	var done []ResolvedTool
	for _, t := range m.ToolOrder() {
		report(ctx, StepTools, StepEvent{State: StepRunning, Tool: t.Name, ToolState: StepRunning})
		got, err := r.installTool(ctx, root, platform, t, done)
		if err != nil {
			report(ctx, StepTools, StepEvent{State: StepFailed, Tool: t.Name, ToolState: StepFailed})
			return errOfKind(ErrTools, "装不上命令行 %s：%v", t.Name, err)
		}
		done = append(done, got)
		report(ctx, StepTools, StepEvent{State: StepRunning, Tool: t.Name, ToolState: StepDone, Reused: got.Reused})
	}
	b, _ := json.MarshalIndent(done, "", "  ")
	if err := writeAtomic(r.toolsVersionFile(storageID, m.Version), b); err != nil {
		return err
	}
	reportState(ctx, StepTools, StepDone)
	return nil
}

// LookTool finds the machine's own copy of a tool (tests replace it): a program on PATH, or "".
var LookTool = func(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func (r *Registry) installTool(ctx context.Context, root, platform string, t ToolSpec, before []ResolvedTool) (ResolvedTool, error) {
	got := ResolvedTool{Name: t.Name, Title: t.Title, Version: t.Version}
	env := toolEnv(before)
	if t.Check != nil {
		if p := r.machineTool(ctx, t, env, root); p != "" {
			bin := filepath.Join(root, "machine-"+t.Name, "bin")
			if err := relink(filepath.Join(bin, t.Name), p); err != nil {
				return got, err
			}
			got.Reused, got.Bin, got.Path = true, bin, p
			return got, nil
		}
	}
	if t.NPM == nil {
		if _, ok := t.For(platform); !ok {
			return got, fmt.Errorf("没有给这台机器（%s）的安装包，机器上也没有能用的 %s", platform, t.Name)
		}
	}
	dir := filepath.Join(root, t.Name+"-"+t.specHash(platform))
	got.Bin, got.Path = filepath.Join(dir, "bin"), filepath.Join(dir, "bin", t.Name)
	if _, err := os.Stat(filepath.Join(dir, ".done")); err == nil {
		return got, nil // installed before, by this version or the one before
	}
	tmp, err := os.MkdirTemp(root, ".part-"+t.Name+"-")
	if err != nil {
		return got, err
	}
	defer os.RemoveAll(tmp)
	if err := fetchTool(ctx, t, platform, tmp); err != nil {
		return got, err
	}
	if t.Check != nil {
		if _, err := runCheck(ctx, filepath.Join(tmp, "bin", t.Name), t.Check, toolEnv(append(slices.Clone(before), ResolvedTool{Bin: filepath.Join(tmp, "bin")}))); err != nil {
			return got, fmt.Errorf("装好后跑不起来：%v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, ".done"), nil, 0o644); err != nil {
		return got, err
	}
	_ = os.RemoveAll(dir) // an incomplete one, without .done
	if err := os.Rename(tmp, dir); err != nil {
		return got, err
	}
	// The links in bin point into pkg relatively, so they hold after the rename.
	return got, nil
}

// machineTool is the machine's own copy of t when it will do: on the hub's PATH (with the tools before it), outside
// the home directory (a sandboxed extension cannot see that), and new enough.
func (r *Registry) machineTool(ctx context.Context, t ToolSpec, env []string, root string) string {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	p := LookTool(t.Name, path)
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		if within(p, home) {
			return ""
		}
	}
	hubDir := filepath.Dir(filepath.Dir(root))
	if h, err := filepath.EvalSymlinks(hubDir); err == nil {
		hubDir = h
	}
	if within(p, hubDir) { // one of the hub's own (another extension's tools)
		return ""
	}
	v, err := runCheck(ctx, p, t.Check, env)
	if err != nil {
		return ""
	}
	if t.Check.MinVersion != "" && compareLoose(v, t.Check.MinVersion) < 0 {
		return ""
	}
	return p
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// runCheck runs the check and answers the first x.y.z it prints.
func runCheck(ctx context.Context, bin string, c *ToolCheck, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, toolCheckWait)
	defer cancel()
	args := c.Args
	if len(args) == 0 {
		args = []string{"--version"}
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &out, &out
	cmd.Dir = os.TempDir()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s：%v", filepath.Base(bin), strings.Join(args, " "), err)
	}
	m := semverInText.FindString(out.String())
	if m == "" {
		return "", fmt.Errorf("%s %s 没有写版本号", filepath.Base(bin), strings.Join(args, " "))
	}
	return m, nil
}

// compareLoose compares two x.y.z.
func compareLoose(a, b string) int {
	pa, pb := semverInText.FindStringSubmatch(a), semverInText.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return strings.Compare(a, b)
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// toolEnv is the hub's environment with the bin directories of tools first on PATH.
func toolEnv(tools []ResolvedTool) []string {
	path := os.Getenv("PATH")
	for _, t := range tools {
		path = t.Bin + string(os.PathListSeparator) + path
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") })
	return append(env, "PATH="+path)
}

// fetchTool downloads, checks and unpacks t into dir: dir/pkg is what was unpacked, dir/bin/<name> the program.
func fetchTool(ctx context.Context, t ToolSpec, platform, dir string) error {
	var urls []string
	var check func(h hash.Hash) bool
	var newHash func() hash.Hash
	if n := t.NPM; n != nil {
		algo, sum, err := ParseIntegrity(n.Integrity)
		if err != nil {
			return err
		}
		urls = []string{n.TarballURL()}
		newHash = sha512.New
		if algo == "sha256" {
			newHash = sha256.New
		}
		check = func(h hash.Hash) bool { return bytes.Equal(h.Sum(nil), sum) }
	} else {
		a, _ := t.For(platform)
		urls = append([]string{a.URL}, a.Mirrors...)
		newHash = sha256.New
		check = func(h hash.Hash) bool { return fmt.Sprintf("%x", h.Sum(nil)) == a.SHA256 }
	}
	file := filepath.Join(dir, "download")
	var errs []error
	ok := false
	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := downloadTool(ctx, t.Name, u, file, newHash, check)
		if err == nil {
			ok = true
			break
		}
		host := u
		if pu, e := url.Parse(u); e == nil {
			host = pu.Host
		}
		errs = append(errs, fmt.Errorf("%s：%w", host, err))
	}
	if !ok {
		if len(errs) == 1 {
			return errs[0]
		}
		return errors.Join(errs...)
	}
	pkg := filepath.Join(dir, "pkg")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	b := &budget{maxBytes: toolMostBytes, maxFiles: toolMostFiles}
	var target string
	switch {
	case t.NPM != nil:
		if err := untarIn(file, pkg, b); err != nil {
			return err
		}
		root, err := npmRoot(pkg)
		if err != nil {
			return err
		}
		rel, err := npmBin(root, t.NPM.BinName(t.Name))
		if err != nil {
			return err
		}
		target = filepath.Join(root, rel)
	default:
		a, _ := t.For(platform)
		name := a.URL
		if pu, err := url.Parse(a.URL); err == nil {
			name = pu.Path
		}
		if isArchiveName(a.URL) {
			var err error
			if strings.HasSuffix(strings.ToLower(name), ".zip") {
				err = unzipIn(file, pkg, b)
			} else {
				err = untarIn(file, pkg, b)
			}
			if err != nil {
				return err
			}
			target = filepath.Join(pkg, filepath.FromSlash(a.Path))
		} else {
			if err := os.MkdirAll(pkg, 0o755); err != nil {
				return err
			}
			target = filepath.Join(pkg, t.Name)
			if err := os.Rename(file, target); err != nil {
				return err
			}
		}
	}
	_ = os.Remove(file)
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("包里没有 %s", strings.TrimPrefix(target, dir+string(filepath.Separator)))
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		realDir = dir
	}
	if !within(real, realDir) {
		return errors.New("程序指向了包外面")
	}
	if st, err := os.Stat(real); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%s 不是一个文件", filepath.Base(target))
	}
	if err := os.Chmod(real, 0o755); err != nil {
		return err
	}
	rel, err := filepath.Rel(bin, target)
	if err != nil {
		return err
	}
	return os.Symlink(rel, filepath.Join(bin, t.Name))
}

// downloadTool fetches one URL into file, reporting the tools step's bytes, and checks its digest.
func downloadTool(ctx context.Context, name, u, file string, newHash func() hash.Hash, check func(hash.Hash) bool) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := newHash()
	dctx, pw, stop := watchTool(ctx, name, io.MultiWriter(f, h))
	err = Fetch(dctx, u, pw)
	stalled := stop()
	f.Close()
	if stalled {
		return errOfKind(ErrDownloadStalled, "下载卡住了：%d 秒没有收到数据", int(StallAfter/time.Second))
	}
	if err != nil {
		return err
	}
	if !check(h) {
		return errOfKind(ErrChecksum, "下载的文件和清单写的校验值对不上")
	}
	return nil
}

// toolWriter counts a tool's download into the tools step, and cancels it when nothing comes for StallAfter.
type toolWriter struct {
	ctx    context.Context
	name   string
	w      io.Writer
	n      int64
	last   time.Time
	timer  *time.Timer
	mu     sync.Mutex
	stalls bool
}

func watchTool(ctx context.Context, name string, w io.Writer) (context.Context, *toolWriter, func() bool) {
	ctx, cancel := context.WithCancel(ctx)
	tw := &toolWriter{ctx: ctx, name: name, w: w}
	tw.timer = time.AfterFunc(StallAfter, func() {
		tw.mu.Lock()
		tw.stalls = true
		tw.mu.Unlock()
		cancel()
	})
	return ctx, tw, func() bool {
		tw.timer.Stop()
		cancel()
		tw.mu.Lock()
		defer tw.mu.Unlock()
		return tw.stalls
	}
}

func (tw *toolWriter) Write(b []byte) (int, error) {
	n, err := tw.w.Write(b)
	tw.timer.Reset(StallAfter)
	tw.n += int64(n)
	if now := time.Now(); now.Sub(tw.last) >= 200*time.Millisecond {
		tw.last = now
		report(tw.ctx, StepTools, StepEvent{State: StepRunning, Tool: tw.name, ToolState: StepRunning, Bytes: tw.n})
	}
	return n, err
}

// npmRoot is the package directory of an unpacked npm tarball: its only top-level directory ("package").
func npmRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 1 {
		if _, err := os.Stat(filepath.Join(dir, dirs[0], "package.json")); err == nil {
			return filepath.Join(dir, dirs[0]), nil
		}
	}
	return "", errors.New("npm 包里没有 package.json")
}

// npmBin is the path of package.json's bin named name, relative to the package.
func npmBin(root, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", err
	}
	var pj struct {
		Name string          `json:"name"`
		Bin  json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		return "", fmt.Errorf("package.json：%v", err)
	}
	var one string
	var many map[string]string
	var rel string
	switch {
	case json.Unmarshal(pj.Bin, &one) == nil && one != "":
		base := pj.Name
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if base == name {
			rel = one
		}
	case json.Unmarshal(pj.Bin, &many) == nil:
		rel = many[name]
	}
	if rel == "" {
		return "", fmt.Errorf("package.json 的 bin 里没有 %s", name)
	}
	rel = filepath.Clean(filepath.FromSlash(rel))
	if !inside(filepath.ToSlash(rel)) {
		return "", fmt.Errorf("package.json 的 bin %s 跳出了包", name)
	}
	return rel, nil
}

// relink makes link point at target, replacing what was there.
func relink(link, target string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// pruneTools keeps the tools of id's versions in keep (and what they use); everything else of id's goes.
func (r *Registry) pruneTools(id string, keep ...string) {
	root := r.toolsDir(id)
	entries, err := os.ReadDir(filepath.Join(root, "versions"))
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, e := range entries {
		v := strings.TrimSuffix(e.Name(), ".json")
		if !slices.Contains(keep, v) {
			_ = os.Remove(filepath.Join(root, "versions", e.Name()))
			continue
		}
		for _, t := range r.toolsAt(id, v) {
			used[filepath.Base(filepath.Dir(t.Bin))] = true
		}
	}
	dirs, _ := os.ReadDir(root)
	for _, d := range dirs {
		if d.Name() == "versions" || used[d.Name()] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, d.Name()))
	}
}
