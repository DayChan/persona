package extension

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is the registry's store in memory.
type memStore struct {
	mu      sync.Mutex
	rows    map[string]Record
	secrets map[string]map[string]string
	audit   []string
}

func newMemStore() *memStore {
	return &memStore{rows: map[string]Record{}, secrets: map[string]map[string]string{}}
}

func (s *memStore) Extensions(context.Context) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}

func (s *memStore) PutExtension(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[r.StorageKey()] = r
	return nil
}

func (s *memStore) DeleteExtension(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	delete(s.secrets, id)
	return nil
}

func (s *memStore) ExtensionSecrets(_ context.Context, id string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.secrets[id] {
		out[k] = v
	}
	return out, nil
}

func (s *memStore) PutExtensionSecret(_ context.Context, id, name, sealed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[id]; !ok {
		return errors.New("no row: the foreign key")
	}
	if s.secrets[id] == nil {
		s.secrets[id] = map[string]string{}
	}
	s.secrets[id][name] = sealed
	return nil
}

func (s *memStore) DeleteExtensionSecret(_ context.Context, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets[id], name)
	return nil
}

func (s *memStore) AddExtensionAudit(_ context.Context, id, action, actor string, detail json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, id+" "+action+" "+actor+" "+string(detail))
	return nil
}

// rot13 stands for secret.key: sealed is not the plain text.
type rot13 struct{}

func (rot13) Seal(p string) (string, error) { return "sealed:" + rot(p), nil }
func (rot13) Open(s string) (string, error) {
	p, ok := strings.CutPrefix(s, "sealed:")
	if !ok {
		return "", errors.New("not sealed")
	}
	return rot(p), nil
}
func rot(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = 'a' + (c-'a'+13)%26
		case c >= 'A' && c <= 'Z':
			b[i] = 'A' + (c-'A'+13)%26
		}
	}
	return string(b)
}

var platform = runtime.GOOS + "-" + runtime.GOARCH

// writePackage writes a package directory with the example manifest changed by change and its program.
func writePackage(t *testing.T, dir string, change func(m map[string]any)) {
	t.Helper()
	m := with(t, func(m map[string]any) {
		m["runtime"] = map[string]any{"command": map[string]any{platform: "bin/notes"}}
		m["engines"] = map[string]any{"plugin_protocol": 1}
		if change != nil {
			change(m)
		}
	})
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), m, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "notes"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newTestRegistry(t *testing.T) (*Registry, *memStore, *[]Change) {
	store := newMemStore()
	r := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	var changes []Change
	r.OnChange = func(c Change) { changes = append(changes, c) }
	return r, store, &changes
}

func TestInstallConfigureUpgradeRollbackUninstall(t *testing.T) {
	ctx := context.Background()
	r, store, changes := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, nil)

	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Upgrade || pv.Manifest.ID != "example-notes" || len(pv.Permissions) != 3 {
		t.Fatalf("%+v", pv)
	}
	if _, err := r.Install(ctx, pv.ID, "nope", "cli"); err == nil {
		t.Fatal("a digest that is not the preview's")
	}
	// A refused install used the preview up.
	pv, _ = r.Preview(ctx, pkg, "")
	p, err := r.Install(ctx, pv.ID, pv.Digest, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Problem != "" || p.Dir != filepath.Join(r.dir, "installed", "example-notes", "1.2.0") || !p.Record.Enabled {
		t.Fatalf("%+v", p)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, "bin", "notes")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "staging")); err == nil {
		if entries, _ := os.ReadDir(filepath.Join(r.dir, "staging")); len(entries) != 0 {
			t.Fatal("staging left behind", entries)
		}
	}
	// The original changing later changes nothing installed.
	_ = os.WriteFile(filepath.Join(pkg, "bin", "notes"), []byte("changed"), 0o755)
	if b, _ := os.ReadFile(filepath.Join(p.Dir, "bin", "notes")); string(b) != "#!/bin/sh\n" {
		t.Fatal("a copy, not a link")
	}
	if _, err := r.Preview(ctx, pkg, ""); err == nil {
		t.Fatal("installed already")
	}

	if m := r.Missing(ctx, p); len(m) != 1 || !strings.Contains(m[0], "API Key") {
		t.Fatal("needs its key and nothing else (url has a default):", m)
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url": "nope"}`), "device:x"); err == nil {
		t.Fatal("held to the schema")
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"folders": ["a"]}`), "device:x"); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(r.Config(r.Get("example-notes")), &cfg)
	if cfg["base_url"] != "https://notes.example.com" || cfg["include_shared"] != false {
		t.Fatal("defaults filled in:", cfg)
	}
	if err := r.SetSecret(ctx, "example-notes", "other", "x", "device:x"); err == nil {
		t.Fatal("an undeclared secret")
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "", "device:x"); err == nil {
		t.Fatal("an empty secret")
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "s3cret", "device:x"); err != nil {
		t.Fatal(err)
	}
	if store.secrets["example-notes"]["api_key"] == "s3cret" {
		t.Fatal("kept sealed")
	}
	if s, _ := r.Secrets(ctx, "example-notes"); s["api_key"] != "s3cret" {
		t.Fatal(s)
	}
	if m := r.Missing(ctx, r.Get("example-notes")); len(m) != 0 {
		t.Fatal(m)
	}
	for _, a := range store.audit {
		if strings.Contains(a, "s3cret") || strings.Contains(a, "\"a\"") {
			t.Fatal("the audit has no values:", a)
		}
	}

	// Upgrade: same permissions, no digest needed.
	pkg2 := t.TempDir()
	writePackage(t, pkg2, func(m map[string]any) { m["version"] = "1.3.0" })
	if _, err := r.Preview(ctx, pkg2, "other"); err == nil {
		t.Fatal("not the package of that plugin")
	}
	pv, err = r.Preview(ctx, pkg2, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Upgrade || pv.From != "1.2.0" || pv.MorePermissions {
		t.Fatalf("%+v", pv)
	}
	if p, err = r.Upgrade(ctx, "example-notes", pv.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	if p.Record.Version != "1.3.0" || p.Record.PreviousVersion != "1.2.0" || string(p.Record.Config) != `{"folders":["a"]}` {
		t.Fatalf("%+v", p.Record)
	}
	if s, _ := r.Secrets(ctx, "example-notes"); s["api_key"] != "s3cret" {
		t.Fatal("secrets stay over an upgrade")
	}

	// Upgrade asking for more: the digest is needed.
	pkg3 := t.TempDir()
	writePackage(t, pkg3, func(m map[string]any) {
		m["version"] = "1.4.0"
		m["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "hub_api": true}
	})
	pv, _ = r.Preview(ctx, pkg3, "example-notes")
	if !pv.MorePermissions {
		t.Fatal("asks for hub_api")
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, "", "cli"); err == nil {
		t.Fatal("more permissions without consent")
	}
	pv, _ = r.Preview(ctx, pkg3, "example-notes")
	if p, err = r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	if !p.Record.Granted.HubAPI {
		t.Fatal("granted")
	}
	entries, _ := os.ReadDir(filepath.Join(r.dir, "installed", "example-notes"))
	if len(entries) != 2 {
		t.Fatal("the current and the previous version only:", entries)
	}
	pv, _ = r.Preview(ctx, pkg2, "example-notes")
	if pv != nil {
		t.Fatal("not newer")
	}

	p, err = r.Rollback(ctx, "example-notes", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.Version != "1.3.0" || p.Record.Granted.HubAPI || p.Record.PreviousVersion != "" {
		t.Fatalf("%+v", p.Record)
	}
	if _, err := r.Rollback(ctx, "example-notes", "cli"); err == nil {
		t.Fatal("only one step back")
	}

	if err := os.MkdirAll(r.DataDir("example-notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.Uninstall(ctx, "example-notes", false, "cli"); err != nil {
		t.Fatal(err)
	}
	if r.Get("example-notes") != nil || len(store.rows) != 0 || len(store.secrets) != 0 {
		t.Fatal("row and secrets gone")
	}
	for _, d := range []string{filepath.Join(r.dir, "installed", "example-notes"), r.DataDir("example-notes")} {
		if _, err := os.Stat(d); err == nil {
			t.Fatal(d, "left")
		}
	}
	var kinds []string
	for _, c := range *changes {
		kinds = append(kinds, c.Kind)
	}
	if strings.Join(kinds, ",") != "installed,config,secret,upgraded,upgraded,rolled_back,removed" {
		t.Fatal(kinds)
	}
	var actions []string
	for _, a := range store.audit {
		actions = append(actions, strings.Fields(a)[1])
	}
	if strings.Join(actions, ",") != "install,config,secret_set,upgrade,upgrade,rollback,uninstall" {
		t.Fatal(actions)
	}

	// The rows come back after a restart.
	pv, _ = r.Preview(ctx, pkg, "")
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p := r2.Get("example-notes"); p == nil || p.Problem != "" || p.Manifest.Name != "Example Notes" {
		t.Fatalf("%+v", p)
	}
	r3 := NewRegistry(r.dir, store, rot13{}, 202611010000)
	_ = r3.Load(ctx)
	if p := r3.Get("example-notes"); p == nil || p.Problem != "" {
		t.Fatal("no engines.persona: runs anywhere")
	}
}

func TestPreviewRefuses(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	r.Reserve("connectors", "github")
	cases := map[string]func(m map[string]any){
		"reserved connector": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "github", "name": "x", "capabilities": []any{"status"}}}}
		},
		"no program for this platform": func(m map[string]any) {
			m["runtime"] = map[string]any{"command": map[string]any{"plan9-mips": "bin/notes"}}
		},
		"program not in the package": func(m map[string]any) { m["runtime"] = map[string]any{"command": "bin/other"} },
		"the hub's binary":           func(m map[string]any) { m["runtime"] = map[string]any{"command": "personad"} },
		"too new": func(m map[string]any) {
			m["engines"] = map[string]any{"persona": ">=202701010000", "plugin_protocol": 1}
		},
	}
	for name, change := range cases {
		pkg := t.TempDir()
		writePackage(t, pkg, change)
		if _, err := r.Preview(ctx, pkg, ""); err == nil {
			t.Errorf("%s: previewed", name)
		}
	}
	// Not executable.
	pkg := t.TempDir()
	writePackage(t, pkg, nil)
	_ = os.Chmod(filepath.Join(pkg, "bin", "notes"), 0o644)
	if _, err := r.Preview(ctx, pkg, ""); err == nil {
		t.Error("not executable")
	}
	// A link out of the package.
	pkg = t.TempDir()
	writePackage(t, pkg, nil)
	_ = os.Symlink("/etc/passwd", filepath.Join(pkg, "bin", "leak"))
	if _, err := r.Preview(ctx, pkg, ""); err == nil {
		t.Error("a link out of the package")
	}
	// Another plugin's connector.
	pkg = t.TempDir()
	writePackage(t, pkg, nil)
	pv, _ := r.Preview(ctx, pkg, "")
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	pkg = t.TempDir()
	writePackage(t, pkg, func(m map[string]any) { m["id"] = "copycat" })
	if _, err := r.Preview(ctx, pkg, ""); err == nil || !strings.Contains(err.Error(), "example-notes") {
		t.Error("its connector is example-notes':", err)
	}
	none := NewRegistry(t.TempDir(), nil, nil, 1)
	if _, err := none.Preview(ctx, pkg, ""); err == nil {
		t.Error("no database, no installs")
	}
}

func TestBuiltinAndHubJSON(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	m, err := Parse(with(t, func(m map[string]any) {
		m["id"] = "telegram"
		m["runtime"] = map[string]any{"command": "personad"}
		m["engines"] = map[string]any{"plugin_protocol": 1}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AddBuiltin(m, "/opt/persona", true); err != nil {
		t.Fatal(err)
	}
	voice := &Manifest{ManifestVersion: 1, ID: "voice", Name: "语音", Version: "0.0.0", Runtime: &Runtime{Command: Command{Path: "/usr/bin/x"}, RPC: "none", HTTP: true},
		ActivationEvents: []string{OnStartup}, Contributes: map[string]json.RawMessage{"voice": json.RawMessage(`[{"id":"voice"}]`)}, Permissions: Permissions{HubAPI: true}}
	if err := r.AddHubJSON(voice, "entry", true); err != nil {
		t.Fatal(err)
	}
	if err := r.AddHubJSON(voice, "entry", true); err == nil {
		t.Fatal("twice")
	}
	if err := r.SetEnabled(ctx, "voice", false, "cli"); err == nil {
		t.Fatal("hub.json's is read-only")
	}
	if err := r.Uninstall(ctx, "telegram", false, "cli"); err == nil {
		t.Fatal("a built-in one only switches off")
	}
	if err := r.SetEnabled(ctx, "telegram", false, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, "telegram", "api_key", "k", "cli"); err != nil {
		t.Fatal(err)
	}
	if len(store.rows) != 1 || store.rows["telegram"].Enabled {
		t.Fatal(store.rows)
	}
	r2 := NewRegistry(r.dir, store, rot13{}, 1)
	_ = r2.AddBuiltin(m, "/opt/persona", true)
	_ = r2.Load(ctx)
	if p := r2.Get("telegram"); p.Record.Enabled || p.Dir != "/opt/persona" {
		t.Fatalf("its row's switch: %+v", p.Record)
	}
	ids := []string{}
	for _, p := range r.List() {
		ids = append(ids, p.Record.ID+"/"+p.Record.Source)
	}
	if !slices.Equal(ids, []string{"telegram/builtin", "voice/hub.json"}) {
		t.Fatal(ids)
	}
}

// tarGz is a package archive of name → content (a name ending in / is a directory; "->target" a link).
func tarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		switch {
		case strings.HasSuffix(name, "/"):
			_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755})
		case strings.HasPrefix(content, "->"):
			_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: content[2:]})
		default:
			_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(content))})
			_, _ = tw.Write([]byte(content))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestStageArchives(t *testing.T) {
	ctx := context.Background()
	manifest := string(with(t, func(m map[string]any) {
		m["runtime"] = map[string]any{"command": map[string]any{platform: "bin/notes"}}
		m["engines"] = map[string]any{"plugin_protocol": 1}
	}))
	good := tarGz(t, map[string]string{"notes-1.2.0/": "", "notes-1.2.0/" + ManifestFile: manifest, "notes-1.2.0/bin/notes": "#!/bin/sh\n",
		"notes-1.2.0/bin/run": "->notes"})
	file := filepath.Join(t.TempDir(), "notes.tar.gz")
	_ = os.WriteFile(file, good, 0o644)
	root, err := Stage(ctx, Source{Kind: "file", Path: file}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "bin", "notes")); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatal("the program, executable", err)
	}

	// Made on a Mac: AppleDouble files beside the top-level directory (tar without COPYFILE_DISABLE), and Finder's
	// __MACOSX/ and .DS_Store; none of them is unpacked, and the directory is still the package's root.
	mac := tarGz(t, map[string]string{"._notes": "\x00\x05\x16\x07", "notes/": "", "notes/._" + ManifestFile: "x",
		"notes/" + ManifestFile: manifest, "notes/bin/notes": "#!/bin/sh\n", "notes/bin/._notes": "x", "notes/.DS_Store": "x",
		"__MACOSX/notes/._bin": "x"})
	file = filepath.Join(t.TempDir(), "mac.tar.gz")
	_ = os.WriteFile(file, mac, 0o644)
	root, err = Stage(ctx, Source{Kind: "file", Path: file}, t.TempDir())
	if err != nil {
		t.Fatal("a package made on a Mac:", err)
	}
	var kept []string
	_ = filepath.WalkDir(filepath.Dir(root), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(filepath.Dir(root), p)
			kept = append(kept, filepath.ToSlash(rel))
		}
		return nil
	})
	if filepath.Base(root) != "notes" || strings.Join(kept, " ") != "notes/bin/notes notes/"+ManifestFile {
		t.Fatalf("%s: %v", root, kept)
	}
	macDir := filepath.Join(t.TempDir(), "notes")
	for rel, content := range map[string]string{ManifestFile: manifest, "._" + ManifestFile: "x", ".DS_Store": "x", "__MACOSX/x": "x"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(macDir, rel)), 0o755)
		_ = os.WriteFile(filepath.Join(macDir, rel), []byte(content), 0o644)
	}
	root, err = Stage(ctx, Source{Kind: "dir", Path: macDir}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("a directory made on a Mac keeps %d entries", len(entries))
	}

	for name, files := range map[string]map[string]string{
		"escape":       {"../x": "x"},
		"absolute":     {"/etc/x": "x"},
		"link out":     {ManifestFile: manifest, "bin/x": "->../../etc/passwd"},
		"link abs":     {ManifestFile: manifest, "bin/x": "->/etc/passwd"},
		"no manifest":  {"a/x": "x", "b/y": "y"},
		"nested twice": {"a/b/" + ManifestFile: manifest},
	} {
		f := filepath.Join(t.TempDir(), "p.tar.gz")
		_ = os.WriteFile(f, tarGz(t, files), 0o644)
		if _, err := Stage(ctx, Source{Kind: "file", Path: f}, t.TempDir()); err == nil {
			t.Errorf("%s: staged", name)
		}
	}

	// zip, and an https download checked against its sha256.
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create(ManifestFile)
	_, _ = io.WriteString(w, manifest)
	w, _ = zw.Create("__MACOSX/._" + ManifestFile)
	_, _ = io.WriteString(w, "x")
	hd := &zip.FileHeader{Name: "bin/notes"}
	hd.SetMode(0o755)
	w, _ = zw.CreateHeader(hd)
	_, _ = io.WriteString(w, "#!/bin/sh\n")
	_ = zw.Close()
	sum := sha256.Sum256(zbuf.Bytes())
	old := Fetch
	defer func() { Fetch = old }()
	Fetch = func(_ context.Context, u string, w io.Writer) error {
		if u != "https://plugins.example.com/notes.zip" {
			t.Fatal(u)
		}
		_, err := w.Write(zbuf.Bytes())
		return err
	}
	src, err := ParseSource("https://plugins.example.com/notes.zip#sha256=" + hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(ctx, src, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	src.SHA256 = strings.Repeat("0", 64)
	if _, err := Stage(ctx, src, t.TempDir()); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatal("a download that does not hash to its sum:", err)
	}
	for _, bad := range []string{"https://x.example.com/p.zip", "http://x.example.com/p.zip#sha256=" + strings.Repeat("0", 64), "remote:", "remote:tcp=x", ""} {
		if _, err := ParseSource(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if s, err := ParseSource("remote:rpc=/tmp/a.sock,http=/tmp/b.sock"); err != nil || s.RPCSocket != "/tmp/a.sock" || s.HTTPSocket != "/tmp/b.sock" {
		t.Fatal(s, err)
	}
}

// A built-in that yields (Telegram moving into a package, #1704): an installed plugin of its id takes its place —
// installing tells the hub what ran before (Old, the built-in), a restart finds the installed one, uninstalling brings
// the built-in back. One that does not yield refuses the id as before.
func TestBuiltinYieldsToAnInstalledOne(t *testing.T) {
	ctx := context.Background()
	r, store, changes := newTestRegistry(t)
	builtin := func(id string) *Manifest {
		m, err := Parse(with(t, func(m map[string]any) {
			m["id"] = id
			m["runtime"] = map[string]any{"command": "personad"}
			m["engines"] = map[string]any{"plugin_protocol": 1}
		}))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if err := r.AddBuiltin(builtin("example-notes"), "/opt/persona", true); err != nil {
		t.Fatal(err)
	}
	pkg := t.TempDir()
	writePackage(t, pkg, nil) // example-notes 1.2.0
	if _, err := r.Preview(ctx, pkg, ""); err == nil || !strings.Contains(err.Error(), "已经有一个叫 example-notes 的扩展") {
		t.Fatal("a built-in that does not yield keeps its id:", err)
	}
	r.Yields("example-notes")
	pv, err := r.Preview(ctx, pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Install(ctx, pv.ID, pv.Digest, "cli")
	if err != nil {
		t.Fatal(err)
	}
	last := (*changes)[len(*changes)-1]
	if got := r.Get("example-notes"); got != p || got.Record.Source != SourceInstalled ||
		last.Kind != "installed" || last.Old == nil || last.Old.Record.Source != SourceBuiltin {
		t.Fatalf("installed in the built-in's place: %+v / %+v", got.Record, last)
	}
	// After a restart: the built-in is registered again, then its row puts the installed one in its place.
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	_ = r2.AddBuiltin(builtin("example-notes"), "/opt/persona", true)
	r2.Yields("example-notes")
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r2.Get("example-notes"); got.Record.Source != SourceInstalled || got.Record.Version != "1.2.0" {
		t.Fatalf("restarted: %+v", got.Record)
	}
	if err := r2.Uninstall(ctx, "example-notes", true, "cli"); err != nil {
		t.Fatal(err)
	}
	if got := r2.Get("example-notes"); got == nil || got.Record.Source != SourceBuiltin || got.Dir != "/opt/persona" {
		t.Fatalf("the built-in is back: %+v", got)
	}
	if len(store.rows) != 0 {
		t.Fatal("the installed row is gone:", store.rows)
	}
}

// A package with a program on computers: refused where nodes cannot run one (NodePrograms off); else every
// platform's program has to be in it, executable, whatever this hub's own platform is.
func TestPackageWithNodePrograms(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["node"] = map[string]any{"command": map[string]any{"darwin-arm64": "node/helper", "linux-amd64": "node/helper-linux"}, "machines": "computers"}
		m["configuration"].(map[string]any)["properties"].(map[string]any)["computers"] = map[string]any{"type": "array",
			"items": map[string]any{"type": "string", "format": "machine", "x-os": []any{"darwin", "linux"}}}
	})
	NodePrograms = false
	if _, err := r.Preview(ctx, "dir:"+pkg, ""); err == nil || !strings.Contains(err.Error(), "还不能在电脑上运行") {
		t.Fatalf("installed where nodes cannot run it: %v", err)
	}
	NodePrograms = true
	if _, err := r.Preview(ctx, "dir:"+pkg, ""); err == nil || !strings.Contains(err.Error(), "node/helper") {
		t.Fatalf("no program for the computers in the package: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(pkg, "node", "helper"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(pkg, "node", "helper-linux"), []byte("not a program"), 0o644)
	if _, err := r.Preview(ctx, "dir:"+pkg, ""); err == nil || !strings.Contains(err.Error(), "helper-linux 不是可执行文件") {
		t.Fatalf("one not executable: %v", err)
	}
	_ = os.Chmod(filepath.Join(pkg, "node", "helper-linux"), 0o755)
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, l := range pv.Permissions {
		keys = append(keys, l.Key)
	}
	if !slices.Contains(keys, "node_runs_code") {
		t.Fatal(keys)
	}
}

// Links that each point inside the package can still lead out of it together: "a" -> "." puts "a/b" -> ".." at the
// package's top, so "a/b/x" is a file next to the package, and every link like "c" -> "b/.." climbs one more level.
// Each link alone passes linkInside; writing through them must not leave the package.
func TestUntarRefusesWritesThroughLinkChains(t *testing.T) {
	type entry struct{ name, link, body string }
	archive := func(entries ...entry) []byte { // in this order: a map's would not do
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, e := range entries {
			if e.link != "" {
				_ = tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeSymlink, Linkname: e.link})
				continue
			}
			_ = tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(e.body))})
			_, _ = tw.Write([]byte(e.body))
		}
		_ = tw.Close()
		_ = gz.Close()
		return buf.Bytes()
	}
	for _, c := range []struct {
		name    string
		entries []entry
		outside string // relative to the package's directory
	}{
		{"one level", []entry{{name: "a", link: "."}, {name: "a/b", link: ".."}, {name: "a/b/x.txt", body: "x"}}, "../x.txt"},
		{"two levels", []entry{{name: "a", link: "."}, {name: "a/b", link: ".."}, {name: "c", link: "b/.."},
			{name: "c/y.txt", body: "y"}}, "../../y.txt"},
		{"a directory", []entry{{name: "a", link: "."}, {name: "a/b", link: ".."}, {name: "a/b/d/z.txt", body: "z"}}, "../d/z.txt"},
		{"links only", []entry{{name: "a", link: "."}, {name: "a/b", link: ".."}}, ""},
		{"a link through a link", []entry{{name: "a", link: "."}, {name: "d", link: "a/.."}}, ""},
		{"the link made last", []entry{{name: "d", link: "a/.."}, {name: "a", link: "."}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			to := filepath.Join(work, "stage", "pkg")
			file := filepath.Join(work, "p.tar.gz")
			if err := os.WriteFile(file, archive(c.entries...), 0o644); err != nil {
				t.Fatal(err)
			}
			err := untar(file, to)
			if _, serr := os.Lstat(filepath.Join(to, c.outside)); c.outside != "" && serr == nil {
				t.Fatalf("untar wrote %s outside the package (err %v)", c.outside, err)
			}
			if err == nil {
				t.Fatal("untar took a package that writes through links out of it")
			}
		})
	}
	// A link that resolves inside the package still works, written through or not.
	work := t.TempDir()
	file := filepath.Join(work, "p.tar.gz")
	_ = os.WriteFile(file, archive(entry{name: "lib/real.txt", body: "r"}, entry{name: "cur", link: "lib"},
		entry{name: "cur/more.txt", body: "m"}, entry{name: "lib/up", link: "../cur/real.txt"}, entry{name: "later", link: "gone"}), 0o644)
	to := filepath.Join(work, "pkg")
	if err := untar(file, to); err != nil {
		t.Fatalf("a link inside the package: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(to, "lib", "more.txt")); err != nil || string(b) != "m" {
		t.Fatalf("written through an inside link: %q %v", b, err)
	}
}

// A preview left when the hub restarted (no install, no discard) is removed when the next registry loads; what is not
// a preview's directory, a preview not yet expired and a preview of this registry stay (#4547).
func TestLoadRemovesPreviewsOfAnEarlierRun(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, nil)
	old, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(r.dir, "staging", "plan-"+newID())
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}

	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	mine, err := r2.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	// Still fresh: another hub on the same directory may be about to install it.
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.staging); err != nil {
		t.Fatal("a preview not yet expired is removed:", err)
	}
	r2.now = func() time.Time { return time.Now().Add(previewFor + time.Minute) }
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.staging); !os.IsNotExist(err) {
		t.Fatal("the earlier run's preview is left:", err)
	}
	if _, err := os.Stat(mine.staging); err != nil {
		t.Fatal("this registry's own preview is removed:", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a directory that is not a preview's is removed:", err)
	}
}

// An uninstall that cannot remove the extension's data fails with nothing changed: the row, the package and the data
// stay, instead of an uninstall that says it worked over data left behind.
func TestUninstallThatCannotRemoveDataChangesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root moves out of a read-only directory")
	}
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, nil)
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Install(ctx, pv.ID, pv.Digest, "cli")
	if err != nil {
		t.Fatal(err)
	}
	data := r.DataDir("example-notes")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "token"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(data)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	if err := r.Uninstall(ctx, "example-notes", false, "cli"); err == nil {
		t.Fatal("an uninstall that cannot remove the data says so")
	}
	if r.Get("example-notes") == nil || len(store.rows) != 1 {
		t.Fatal("the extension is still installed")
	}
	if _, err := os.Stat(filepath.Join(p.Dir, "bin", "notes")); err != nil {
		t.Fatal("its package is back where it was:", err)
	}
	if _, err := os.Stat(filepath.Join(data, "token")); err != nil {
		t.Fatal("its data is still there:", err)
	}

	_ = os.Chmod(parent, 0o700)
	if err := r.Uninstall(ctx, "example-notes", false, "cli"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(r.dir, "installed", "example-notes"), data} {
		if _, err := os.Stat(d); err == nil {
			t.Fatal(d, "left")
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(r.dir, "staging")); len(entries) != 0 {
		t.Fatal("staging left behind", entries)
	}
}

// installNotes installs the example package with a data file and returns its package and data directories.
func installNotes(t *testing.T, r *Registry) (pkgDir, data string) {
	t.Helper()
	ctx := context.Background()
	pkg := t.TempDir()
	writePackage(t, pkg, nil)
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Install(ctx, pv.ID, pv.Digest, "cli")
	if err != nil {
		t.Fatal(err)
	}
	data = r.DataDir("example-notes")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "token"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p.Dir, data
}

// A crash after the directories moved aside but before the row went: the next Load puts them back, the extension
// is whole again, and a later uninstall removes everything.
func TestLoadPutsBackAnUninstallInterruptedBeforeTheRow(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkgDir, data := installNotes(t, r)
	aside, err := r.moveAside(r.Get("example-notes").Record, []string{filepath.Join("installed", "example-notes"), filepath.Join("data", "example-notes")})
	if err != nil || aside == "" {
		t.Fatal(aside, err)
	}
	if !strings.Contains(filepath.Base(aside), "example-notes") {
		t.Fatal("the staging name says whose it is:", aside)
	}
	// The crash: the row is still there.
	var logs []string
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	r2.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	p := r2.Get("example-notes")
	if p == nil || p.Problem != "" {
		t.Fatalf("whole again: %+v", p)
	}
	for _, f := range []string{filepath.Join(pkgDir, "bin", "notes"), filepath.Join(data, "token")} {
		if _, err := os.Stat(f); err != nil {
			t.Fatal("put back:", err)
		}
	}
	if _, err := os.Stat(aside); err == nil {
		t.Fatal("the staging directory is gone")
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "back") {
		t.Fatal(logs)
	}
	if err := r2.Uninstall(ctx, "example-notes", false, "cli"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(r.dir, "staging")); len(entries) != 0 {
		t.Fatal("staging left behind", entries)
	}
}

// A crash after the row went but before the moved-aside directory was removed: the next Load removes it.
func TestLoadRemovesWhatAnUninstallLeftAfterTheRow(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	installNotes(t, r)
	aside, err := r.moveAside(r.Get("example-notes").Record, []string{filepath.Join("installed", "example-notes"), filepath.Join("data", "example-notes")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteExtension(ctx, "example-notes"); err != nil {
		t.Fatal(err)
	}
	var logs []string
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	r2.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if r2.Get("example-notes") != nil {
		t.Fatal("not installed")
	}
	if _, err := os.Stat(aside); err == nil {
		t.Fatal("its files are removed")
	}
	for _, d := range []string{filepath.Join(r.dir, "installed", "example-notes"), r.DataDir("example-notes")} {
		if _, err := os.Stat(d); err == nil {
			t.Fatal(d, "is not back")
		}
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "removed") {
		t.Fatal(logs)
	}
}

// A move that cannot be undone keeps the staging directory and says where; nothing is deleted.
func TestMoveBackThatFailsKeepsTheFiles(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	_, data := installNotes(t, r)
	aside, err := r.moveAside(r.Get("example-notes").Record, []string{filepath.Join("data", "example-notes")})
	if err != nil {
		t.Fatal(err)
	}
	// Something took the place back meanwhile: the data cannot go back over it.
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.moveBack(aside); err == nil {
		t.Fatal("says it could not put it back")
	}
	if _, err := os.Stat(filepath.Join(aside, "0", "token")); err != nil {
		t.Fatal("the data is kept in staging:", err)
	}
}

// An uninstall whose cleanup fails still uninstalls, and says what to delete by hand.
func TestUninstallThatCannotCleanUpIsALeftover(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes from a read-only directory")
	}
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	_, data := installNotes(t, r)
	// The data has a read-only directory in it: it moves aside, but cannot be removed there.
	locked := filepath.Join(data, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	var aside string
	t.Cleanup(func() {
		if aside != "" {
			_ = filepath.Walk(aside, func(p string, _ os.FileInfo, _ error) error { return os.Chmod(p, 0o700) })
		}
	})
	err := r.Uninstall(ctx, "example-notes", false, "cli")
	var left *LeftoverError
	if !errors.As(err, &left) || !strings.Contains(left.Error(), "已卸载") || !strings.Contains(left.Error(), left.Path) {
		t.Fatalf("a leftover, not a failure: %v", err)
	}
	aside = left.Path
	if r.Get("example-notes") != nil || len(store.rows) != 0 {
		t.Fatal("uninstalled")
	}
}

// The reviewer's case (#4466): a no-keep uninstall whose cleanup failed leaves its files in staging; the owner
// installs the same id again. The next Load must not move the old data into the new install: it removes it.
func TestReinstallNeverGetsTheDataOfAnUninstall(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	_, data := installNotes(t, r)
	row := r.Get("example-notes").Record
	aside, err := r.moveAside(row, []string{filepath.Join("installed", "example-notes"), filepath.Join("data", "example-notes")})
	if err != nil {
		t.Fatal(err)
	}
	// The row went (the uninstall happened) and its cleanup did not.
	if err := store.DeleteExtension(ctx, "example-notes"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aside, uninstallCommitted), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	delete(r.extensions, "example-notes")
	r.mu.Unlock()
	// Reinstalled: a new row of the same id, no data yet.
	r.now = func() time.Time { return time.Now().Add(time.Hour) }
	pkg := t.TempDir()
	writePackage(t, pkg, nil)
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "token")); !os.IsNotExist(err) {
		t.Fatal("the deleted data came back:", err)
	}
	if _, err := os.Stat(aside); !os.IsNotExist(err) {
		t.Fatal("the old files are removed:", err)
	}
	if p := r2.Get("example-notes"); p == nil || p.Problem != "" {
		t.Fatalf("the new install is whole: %+v", p)
	}

	// Without the committed marker (a crash just before it), the install identity still tells them apart.
	row2 := r2.Get("example-notes").Record
	stale := filepath.Join(r.dir, "staging", "uninstall-example-notes-"+newID())
	if err := os.MkdirAll(filepath.Join(stale, "0"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(stale, "0", "token"), []byte("old"), 0o600)
	b, _ := json.Marshal(map[string]any{"id": "example-notes", "version": row2.Version, "installed_at": row.InstalledAt.Unix(),
		"moves": []map[string]string{{"from": filepath.Join("data", "example-notes"), "to": "0"}}})
	_ = os.WriteFile(filepath.Join(stale, uninstallManifest), b, 0o600)
	r3 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r3.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "token")); !os.IsNotExist(err) {
		t.Fatal("another install's data was put back:", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("removed:", err)
	}
}

// moveBack only puts back an extension's own directories: a manifest naming anything else is refused.
func TestMoveBackRefusesOtherPaths(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	aside := filepath.Join(r.dir, "staging", "uninstall-x-"+newID())
	if err := os.MkdirAll(filepath.Join(aside, "0"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"installed/y", "../escape", "data", "staging/x", "installed/x/../y"} {
		b, _ := json.Marshal(map[string]any{"id": "x", "moves": []map[string]string{{"from": from, "to": "0"}}})
		_ = os.WriteFile(filepath.Join(aside, uninstallManifest), b, 0o600)
		if err := r.moveBack(aside); err == nil {
			t.Fatal(from, "is not x's own directory")
		}
	}
	if !ownDir("x", "tools/x") || !ownDir("x", "data/x") || !ownDir("x", "installed/x") || ownDir("..", "data/..") {
		t.Fatal("ownDir")
	}
}

// An uninstall moves the extension's tools directory too, and the sweep of old previews leaves uninstall-* alone.
func TestUninstallMovesToolsAndPreviewSweepSparesIt(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installNotes(t, r)
	tools := r.ToolsDir("example-notes")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	aside, err := r.moveAside(r.Get("example-notes").Record, []string{filepath.Join("tools", "example-notes")})
	if err != nil || aside == "" {
		t.Fatal(aside, err)
	}
	if _, err := os.Stat(tools); !os.IsNotExist(err) {
		t.Fatal("tools moved:", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(aside, old, old)
	r.now = func() time.Time { return time.Now().Add(previewFor + time.Hour) }
	r.dropStalePreviews()
	if _, err := os.Stat(aside); err != nil {
		t.Fatal("the preview sweep removed an uninstall's directory:", err)
	}
	if err := r.moveBack(aside); err != nil {
		t.Fatal(err)
	}
	if err := r.Uninstall(ctx, "example-notes", false, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tools); !os.IsNotExist(err) {
		t.Fatal("tools left after uninstall:", err)
	}
}

func TestRegistrySnapshotsAreIndependent(t *testing.T) {
	p := &Extension{
		Record: Record{
			ID: "alpha", Enabled: true,
			Manifest: json.RawMessage(`{"name":"original"}`), Config: json.RawMessage(`{"value":"original"}`),
			Aliases: []string{"old-alpha"}, Granted: Permissions{Network: []string{"example.invalid"}, Hub: []string{"sessions:read"}},
			DisabledContributions: []string{"tools/disabled"}, Remote: &Remote{RPCSocket: "original.sock"},
		},
		Manifest: &Manifest{}, Full: &Manifest{}, Schema: &Schema{}, Warnings: []string{"original warning"},
	}
	r := &Registry{extensions: map[string]*Extension{"zulu": {Record: Record{ID: "zulu"}}, "alpha": p}}
	before, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	all := r.Snapshots()
	if len(all) != 2 || all[0].Record.ID != "alpha" || all[1].Record.ID != "zulu" {
		t.Fatal("snapshots do not include every extension sorted by id")
	}
	copy := all[0]
	if copy == p || copy.Manifest != p.Manifest || copy.Full != p.Full || copy.Schema != p.Schema {
		t.Fatal("snapshot must clone the entry and share immutable parsed definitions")
	}
	copy.Record.Enabled = false
	copy.Record.Manifest[0] = ' '
	copy.Record.Config[0] = ' '
	copy.Record.Aliases[0] = "changed"
	copy.Record.Granted.Network[0] = "changed"
	copy.Record.Granted.Hub[0] = "changed"
	copy.Record.DisabledContributions[0] = "changed"
	copy.Record.Remote.RPCSocket = "changed.sock"
	copy.Warnings[0] = "changed"
	after, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("mutating a snapshot changed the registry entry")
	}
	fresh := r.Snapshots()[0]
	if fresh == copy || fresh.Record.Remote == copy.Record.Remote || string(fresh.Record.Config) != `{"value":"original"}` {
		t.Fatal("a later snapshot reused a previous snapshot's mutable state")
	}
	p.Record.Config[0] = '['
	p.Record.Remote.RPCSocket = "updated.sock"
	if string(fresh.Record.Config) != `{"value":"original"}` || fresh.Record.Remote.RPCSocket != "original.sock" {
		t.Fatal("registry changes reached an earlier snapshot")
	}
}
