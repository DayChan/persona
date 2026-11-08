package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

var uploadNow = time.Date(2026, 10, 29, 15, 30, 12, 0, time.Local)

func sendFile(t *testing.T, u *uploads, name string, data []byte, chunk int) (string, error) {
	t.Helper()
	id, err := u.begin(name, int64(len(data)))
	if err != nil {
		return "", err
	}
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		if _, err := u.chunk(id, int64(off), data[off:end]); err != nil {
			return "", err
		}
	}
	path, _, err := u.end(id, uploadNow)
	return path, err
}

func TestUploadInChunks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	var u uploads
	data := bytes.Repeat([]byte("0123456789"), 1000)
	path, err := sendFile(t, &u, "report.pdf", data, 777)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "report-20261029-153012.pdf") || !filepath.IsAbs(path) {
		t.Fatalf("path %s", path)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// No part files left behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("left %v", ents)
	}

	// The same name in the same second: a short random suffix.
	again, err := sendFile(t, &u, "report.pdf", data[:10], 4)
	if err != nil {
		t.Fatal(err)
	}
	b := filepath.Base(again)
	if again == path || !strings.HasPrefix(b, "report-20261029-153012-") || !strings.HasSuffix(b, ".pdf") || len(b) != len("report-20261029-153012-abcd.pdf") {
		t.Fatalf("second: %s", again)
	}
	// An empty file is a file.
	if p, err := sendFile(t, &u, "empty.txt", nil, 1); err != nil || filepath.Base(p) != "empty-20261029-153012.txt" {
		t.Fatalf("empty: %s %v", p, err)
	}
}

func TestUploadRefusals(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	var u uploads
	_, err := u.begin("big.bin", proto.MaxUploadBytes+1)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeTooLarge {
		t.Fatalf("too large: %v", err)
	}
	if _, err := u.begin("x", -1); err == nil {
		t.Fatal("no size")
	}

	// A chunk out of order, more bytes than announced, too few at the end: refused, part file removed.
	id, _ := u.begin("a.txt", 4)
	if _, err := u.chunk(id, 2, []byte("ab")); err == nil {
		t.Fatal("offset")
	}
	if _, err := u.chunk(id, 0, []byte("ab")); err == nil {
		t.Fatal("the upload is gone after a bad chunk")
	}
	id, _ = u.begin("a.txt", 4)
	if _, err := u.chunk(id, 0, []byte("abcde")); err == nil {
		t.Fatal("overflow")
	}
	id, _ = u.begin("a.txt", 4)
	u.chunk(id, 0, []byte("ab"))
	if _, _, err := u.end(id, uploadNow); err == nil {
		t.Fatal("short")
	}
	id, _ = u.begin("a.txt", 4)
	u.chunk(id, 0, []byte("ab"))
	u.abort(id)
	if _, err := u.chunk(id, 2, []byte("cd")); err == nil {
		t.Fatal("aborted")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
}

func TestUploadNames(t *testing.T) {
	cases := map[string]string{
		"report.pdf":         "report-20261029-153012.pdf",
		"backup.tar.gz":      "backup-20261029-153012.tar.gz",
		"Makefile":           "Makefile-20261029-153012",
		".env":               ".env-20261029-153012",
		"会议纪要 v2.docx":       "会议纪要 v2-20261029-153012.docx",
		"../../etc/passwd":   ".._.._etc_passwd-20261029-153012",
		"a\\b\nc.txt":        "a_b_c-20261029-153012.txt",
		"..":                 "file-20261029-153012",
		"  ":                 "file-20261029-153012",
		"notes. final draft": "notes. final draft-20261029-153012",
	}
	for in, want := range cases {
		dir := t.TempDir()
		p, err := reserveName(dir, in, uploadNow)
		if err != nil || filepath.Dir(p) != dir || filepath.Base(p) != want {
			t.Errorf("%q -> %q %v, want %q", in, filepath.Base(p), err, want)
		}
	}
	long := strings.Repeat("长", 200) + ".txt"
	p, err := reserveName(t.TempDir(), long, uploadNow)
	if err != nil || len(filepath.Base(p)) > 255 || !strings.HasSuffix(p, ".txt") {
		t.Fatalf("long: %d %v", len(filepath.Base(p)), err)
	}
}

func TestUploadIdleRemovesPart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	synctest.Test(t, func(t *testing.T) {
		var u uploads
		id, err := u.begin("a.txt", 4)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(uploadIdle) // virtual time reaches the real idle callback
		synctest.Wait()
		if u.lookup(id) != nil {
			t.Fatal("expired upload remains in registry")
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("left %v", ents)
		}
	})
}

// callUpload runs a whole terminal upload through the RPC the hub speaks.
func callUpload(t *testing.T, n *Node, dest, name string, data []byte) (proto.UploadedFile, error) {
	t.Helper()
	call := func(method string, p map[string]any) (any, error) {
		raw, _ := json.Marshal(p)
		return n.uploadCall(method, raw)
	}
	res, err := call("file.upload.begin", map[string]any{"name": name, "size": len(data), "dest": dest})
	if err != nil {
		return proto.UploadedFile{}, err
	}
	id := res.(map[string]any)["upload_id"]
	if _, err := call("file.upload.chunk", map[string]any{"upload_id": id, "offset": 0, "data": data}); err != nil {
		return proto.UploadedFile{}, err
	}
	out, err := call("file.upload.end", map[string]any{"upload_id": id})
	if err != nil {
		return proto.UploadedFile{}, err
	}
	return out.(proto.UploadedFile), nil
}

func TestTerminalUploadLandsInPersonaDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	home := t.TempDir()
	n := &Node{dir: home, logger: log.New(io.Discard, "", 0)}
	data := []byte("\x89PNG pretend")
	out, err := callUpload(t, n, proto.UploadDestTerminal, "Screen Shot 1.png", data)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := terminalUploadDir(home, time.Now())
	if !filepath.IsAbs(out.Path) || filepath.Dir(out.Path) != wantDir {
		t.Fatalf("path %s, want it in %s", out.Path, wantDir)
	}
	if b := filepath.Base(out.Path); !regexp.MustCompile(`^[0-9a-f]{16}-Screen Shot 1\.png$`).MatchString(b) {
		t.Fatalf("name %q", b)
	}
	got, _ := os.ReadFile(out.Path)
	if !bytes.Equal(got, data) || out.Size != int64(len(data)) {
		t.Fatal("content differs")
	}
	if st, _ := os.Stat(out.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", st.Mode())
	}
	if st, _ := os.Stat(wantDir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode())
	}
	// Nothing in the temp directory, and no part file beside the result.
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir has %v", ents)
	}
	if ents, _ := os.ReadDir(wantDir); len(ents) != 1 {
		t.Fatalf("dir has %v", ents)
	}
	// The same name twice is two files.
	again, err := callUpload(t, n, proto.UploadDestTerminal, "Screen Shot 1.png", data)
	if err != nil || again.Path == out.Path {
		t.Fatalf("again: %v %v", again.Path, err)
	}
	// Without a dest the old behaviour: the temp directory.
	plain, err := callUpload(t, n, "", "a.txt", []byte("x"))
	if err != nil || filepath.Dir(plain.Path) != tmp {
		t.Fatalf("plain: %v %v", plain.Path, err)
	}
}

func TestTerminalUploadCannotLeaveItsDirectory(t *testing.T) {
	home := t.TempDir()
	n := &Node{dir: home, logger: log.New(io.Discard, "", 0)}
	root := terminalUploadRoot(home)
	for _, name := range []string{"../../../etc/passwd", "..", "/etc/cron.d/x", `..\..\x`, "a/../../b.png", "\x00evil", "%2e%2e%2fx"} {
		out, err := callUpload(t, n, proto.UploadDestTerminal, name, []byte("x"))
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if filepath.Dir(out.Path) != terminalUploadDir(home, time.Now()) || !strings.HasPrefix(out.Path, root+string(filepath.Separator)) {
			t.Errorf("%q escaped to %s", name, out.Path)
		}
	}
	// Nothing was written next to the upload root's parents.
	ents, _ := os.ReadDir(home)
	if len(ents) != 1 || ents[0].Name() != "uploads" {
		t.Fatalf("home has %v", ents)
	}
	if _, err := callUpload(t, n, "elsewhere", "a.png", []byte("x")); err == nil {
		t.Fatal("an unknown destination is refused")
	}
}

func TestTerminalUploadSizeCap(t *testing.T) {
	home := t.TempDir()
	n := &Node{dir: home, logger: log.New(io.Discard, "", 0)}
	raw, _ := json.Marshal(map[string]any{"name": "big.bin", "size": proto.MaxTerminalUploadBytes + 1, "dest": proto.UploadDestTerminal})
	_, err := n.uploadCall("file.upload.begin", raw)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeTooLarge {
		t.Fatalf("over the cap: %v", err)
	}
	// Exactly the cap is fine to begin (and abort); a part file never outlives a refused begin.
	raw, _ = json.Marshal(map[string]any{"name": "ok.bin", "size": proto.MaxTerminalUploadBytes, "dest": proto.UploadDestTerminal})
	res, err := n.uploadCall("file.upload.begin", raw)
	if err != nil {
		t.Fatal(err)
	}
	n.ups.abort(res.(map[string]any)["upload_id"].(string))
	if ents, _ := os.ReadDir(terminalUploadDir(home, time.Now())); len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
}

func TestSweepTerminalUploads(t *testing.T) {
	home := t.TempDir()
	root := terminalUploadRoot(home)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	for _, d := range []string{"2026-10-01", "2026-11-02", "2026-11-03", "2026-11-10", "not-a-date"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(root, d, "x.png"), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(root, "2026-01-01"), []byte("a file, not a day"), 0o600)
	if got := sweepTerminalUploads(home, now); got != 2 {
		t.Fatalf("removed %d", got)
	}
	var left []string
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	// 11-03 ended 7 days before the 10th at noon is not yet over (its last hour is inside the week); 11-02 is.
	want := []string{"2026-01-01", "2026-11-03", "2026-11-10", "not-a-date"}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Fatalf("left %v, want %v", left, want)
	}
	// No uploads yet at all.
	if sweepTerminalUploads(t.TempDir(), now) != 0 {
		t.Fatal("nothing to sweep")
	}
}

// A report for the health space (dest=health) lands in the health directory's reports/ under its own name plus the
// time, and stays there: no part file beside it, nothing in the temp directory.
func TestHealthUploadLandsInReports(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	home := t.TempDir()
	n := &Node{dir: t.TempDir(), home: home, logger: log.New(io.Discard, "", 0)}
	data := []byte("%PDF-1.7 pretend")
	out, err := callUpload(t, n, proto.UploadDestHealth, "2026 体检报告.pdf", data)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(identity.HealthDir(home), "reports")
	if filepath.Dir(out.Path) != wantDir {
		t.Fatalf("path %s, want it in %s", out.Path, wantDir)
	}
	if b := filepath.Base(out.Path); !regexp.MustCompile(`^2026 体检报告-\d{8}-\d{6}\.pdf$`).MatchString(b) {
		t.Fatalf("name %q", b)
	}
	if got, _ := os.ReadFile(out.Path); !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if st, _ := os.Stat(wantDir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode())
	}
	if ents, _ := os.ReadDir(wantDir); len(ents) != 1 {
		t.Fatalf("dir has %v", ents)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir has %v", ents)
	}
	// The main conversation is denied the directory the report is in.
	denied := false
	for _, r := range n.healthDenyRules(identity.AssistantDir(home)) {
		denied = denied || strings.HasPrefix(r, "Read(/"+identity.HealthDir(home)+"/")
	}
	if !denied {
		t.Fatalf("main conversation's deny rules miss %s: %v", wantDir, n.healthDenyRules(identity.AssistantDir(home)))
	}
}
