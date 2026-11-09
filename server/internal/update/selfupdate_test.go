package update

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func fileFor(name string, bin []byte) (File, []byte) {
	gz := gzipped(bin)
	outer, inner := sha256.Sum256(gz), sha256.Sum256(bin)
	return File{Name: name, Size: int64(len(gz)), SHA256: hex.EncodeToString(outer[:]), BinarySHA256: hex.EncodeToString(inner[:])}, gz
}

// A download cut off halfway is tried again; what finally lands matches the signed manifest.
func TestDownloadRetriesACutDownload(t *testing.T) {
	bin := bytes.Repeat([]byte("personad "), 4096)
	f, gz := fileFor("personad-linux-amd64.gz", bin)
	var tries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		if tries.Add(1) == 1 {
			w.Header().Set("Content-Length", "999999")
			w.Write(gz[:len(gz)/2]) // then the connection drops
			if hj, ok := w.(http.Hijacker); ok {
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return
		}
		w.Write(gz)
	}))
	defer srv.Close()
	dir := t.TempDir()
	got, err := Download(context.Background(), srv.Client(), srv.URL, f, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); !bytes.Equal(b, bin) || tries.Load() < 2 {
		t.Fatalf("tries %d, %d bytes", tries.Load(), len(b))
	}
	if st, _ := os.Stat(got); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("not executable")
	}
}

// A file that is not the one signed is deleted at once and not tried again.
func TestDownloadRefusesAnotherFile(t *testing.T) {
	f, _ := fileFor("personad-linux-amd64.gz", []byte("the real one"))
	_, other := fileFor("personad-linux-amd64.gz", []byte("something else entirely, same name"))
	f.Size = int64(len(other))
	var tries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		tries.Add(1)
		w.Write(other)
	}))
	defer srv.Close()
	dir := t.TempDir()
	if _, err := Download(context.Background(), srv.Client(), srv.URL, f, dir); !errors.Is(err, errMismatch) {
		t.Fatalf("got %v", err)
	}
	if tries.Load() != 1 {
		t.Fatalf("a mismatch was fetched %d times", tries.Load())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// An archive that matches the manifest's size and sha256 but does not unpack to its binary_sha256 (or does not
// unpack at all) is the release's, broken: it is deleted too, so nothing of it stays in dir.
func TestDownloadRemovesAnArchiveThatDoesNotUnpack(t *testing.T) {
	for _, broken := range []string{"binary digest", "not gzip"} {
		t.Run(broken, func(t *testing.T) {
			f, body := fileFor("personad-linux-amd64.gz", []byte("the real one"))
			if broken == "binary digest" {
				f.BinarySHA256 = hex.EncodeToString(make([]byte, sha256.Size))
			} else {
				body = []byte("not a gzip stream")
				sum := sha256.Sum256(body)
				f.Size, f.SHA256 = int64(len(body)), hex.EncodeToString(sum[:])
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
			defer srv.Close()
			dir := t.TempDir()
			if _, err := Download(context.Background(), srv.Client(), srv.URL, f, dir); !errors.Is(err, errMismatch) {
				t.Fatalf("got %v", err)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
				t.Fatalf("left behind: %v", left)
			}
		})
	}
}

func TestFailedPromotionRemovesStagingFile(t *testing.T) {
	f, gz := fileFor("program.gz", []byte("verified binary"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gz)
	}))
	defer srv.Close()

	for _, stage := range []string{"download", "unpack"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "input.gz")
			if err := os.WriteFile(input, gz, 0o600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(dir, "artifact")
			if err := os.Mkdir(dest, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dest, "keep.txt")
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}

			var err error
			if stage == "download" {
				err = fetchFile(context.Background(), srv.Client(), srv.URL, dest, f)
			} else {
				err = unpack(input, dest, f.BinarySHA256)
			}
			var renameErr *os.LinkError
			if !errors.As(err, &renameErr) || renameErr.Op != "rename" {
				t.Fatalf("promotion error = %v, want a rename failure", err)
			}
			if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
				t.Errorf("staging file remains: %v", err)
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
				t.Errorf("destination changed: %q, %v", got, err)
			}
			if got, err := os.ReadFile(input); err != nil || !bytes.Equal(got, gz) {
				t.Errorf("input archive changed: %d bytes, %v", len(got), err)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	f, _ := fileFor("personad-linux-amd64.gz", []byte("x"))
	m := Manifest{Version: "v2", Build: 202611021700, Protocol: 1, Files: map[string]File{"linux-amd64": f}}
	data, _ := json.Marshal(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dl/latest.json" {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	r, err := Check(context.Background(), srv.Client(), srv.URL+"/dl")
	if err != nil || r.Manifest.Build != 202611021700 || r.Manifest.Files["linux-amd64"].SHA256 != f.SHA256 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := Check(context.Background(), srv.Client(), srv.URL+"/nowhere"); err == nil {
		t.Fatal("no manifest was taken for one")
	}
}

func TestSwapAndRestore(t *testing.T) {
	dir := t.TempDir()
	target, next := filepath.Join(dir, "personad"), filepath.Join(dir, "new")
	os.WriteFile(target, []byte("old"), 0o755)
	os.WriteFile(next, []byte("new"), 0o755)
	prev, err := Swap(target, next)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("target %q", b)
	}
	if b, _ := os.ReadFile(prev); string(b) != "old" {
		t.Fatalf("prev %q", b)
	}
	if err := Restore(target, prev); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("restored %q", b)
	}
}

// noTemps fails when a .new temp file is left in dir.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, "*.new"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// A swap whose rename fails (target.prev is a directory that is not empty) removes its temp file and leaves target as
// it was.
func TestSwapLeavesNoTempWhenItFails(t *testing.T) {
	dir := t.TempDir()
	target, next := filepath.Join(dir, "personad"), filepath.Join(dir, "next")
	os.WriteFile(target, []byte("old"), 0o755)
	os.WriteFile(next, []byte("new"), 0o755)
	if err := os.MkdirAll(filepath.Join(target+".prev", "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Swap(target, next); err == nil {
		t.Fatal("swap over an occupied .prev succeeded")
	}
	noTemps(t, dir)
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("target %q", b)
	}
}

// A marker whose rename fails (the path is a directory that is not empty) removes its temp file.
func TestSaveLeavesNoTempWhenItFails(t *testing.T) {
	dir := t.TempDir()
	path := PendingPath(dir)
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Pending{ToVersion: "v2"}); err == nil {
		t.Fatal("save over a directory succeeded")
	}
	noTemps(t, dir)
	if err := Save(RolledBackPath(dir), RolledBack{ToVersion: "v2"}); err != nil {
		t.Fatal(err)
	}
	var back RolledBack
	if ok, err := Load(RolledBackPath(dir), &back); !ok || err != nil || back.ToVersion != "v2" {
		t.Fatalf("%v %v %+v", ok, err, back)
	}
	noTemps(t, dir)
}

// The watchdog: the new build confirming ends it; one that never does is put back and restarted; the new version
// rolling back by itself (the marker gone) ends it too.
func TestWatch(t *testing.T) {
	confirmed := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": confirmed.Load()}})
	}))
	defer srv.Close()
	run := func(t *testing.T, before func(dir, pending string)) (restored, restarted bool, dir string) {
		dir = t.TempDir()
		pending := PendingPath(dir)
		p := Pending{ToBuild: 202611021700, ToVersion: "v2", FromVersion: "v1", HealthURL: srv.URL}
		Save(pending, p)
		if before != nil {
			before(dir, pending)
		}
		err := Watch(context.Background(), srv.Client(), p, pending, time.Now().Add(300*time.Millisecond), 50*time.Millisecond,
			func() error { restored = true; return nil }, func() error { restarted = true; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	confirmed.Store(true)
	if restored, restarted, _ := run(t, nil); restored || restarted {
		t.Fatal("a confirmed update was rolled back")
	}
	confirmed.Store(false)
	restored, restarted, dir := run(t, nil)
	if !restored || !restarted {
		t.Fatal("a new version that never confirmed was left running")
	}
	var rb RolledBack
	if ok, _ := Load(RolledBackPath(dir), &rb); !ok || rb.ToBuild != 202611021700 {
		t.Fatal("why it went back is not written down")
	}
	if _, err := os.Stat(PendingPath(dir)); !os.IsNotExist(err) {
		t.Fatal("the pending marker stayed")
	}
	if left, _ := filepath.Glob(PendingPath(dir) + "*"); len(left) != 0 {
		t.Fatalf("left %v behind", left)
	}
	if restored, restarted, _ := run(t, func(dir, pending string) { os.Remove(pending) }); restored || restarted {
		t.Fatal("rolled back again what the new version already rolled back")
	}
}

// A build deployed straight from master while the watchdog waits is left alone.
func TestWatchLeavesADirectDeploy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"build": 202611021800, "update": map[string]bool{"confirmed": true}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
	Save(PendingPath(dir), p)
	restored := false
	if err := Watch(context.Background(), srv.Client(), p, PendingPath(dir), time.Now().Add(200*time.Millisecond), 50*time.Millisecond,
		func() error { restored = true; return nil }, func() error { return nil }); err != nil || restored {
		t.Fatalf("undid a direct deploy: %v %v", err, restored)
	}
}

// The new version confirming while the last health check is still out is left alone: that check's answer (computed
// just before the confirm, or a timeout under load) is older than the marker being gone.
func TestWatchLeavesAnUpdateConfirmedAtTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		answer  func(w http.ResponseWriter, r *http.Request)
	}{
		{"answered", 5 * time.Second, func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": false}})
		}},
		{"timed out", 50 * time.Millisecond, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pending := PendingPath(dir)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				os.Remove(pending) // the new version confirms (hub/selfupdate.go confirm)
				tc.answer(w, r)
			}))
			defer srv.Close()
			p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, ToVersion: "v2", FromVersion: "v1", HealthURL: srv.URL}
			if err := Save(pending, p); err != nil {
				t.Fatal(err)
			}
			client := srv.Client()
			client.Timeout = tc.timeout
			restored, restarted := false, false
			err := Watch(context.Background(), client, p, pending, time.Now(), 50*time.Millisecond,
				func() error { restored = true; return nil }, func() error { restarted = true; return nil })
			if err != nil || restored || restarted {
				t.Fatalf("went back on a confirmed update: %v, restored %v, restarted %v", err, restored, restarted)
			}
			if _, err := os.Stat(RolledBackPath(dir)); !os.IsNotExist(err) {
				t.Fatal("wrote down a rollback that did not happen")
			}
		})
	}
}

// A restore that fails leaves the marker where it was, and no rollback is written down.
func TestWatchKeepsTheMarkerWhenRestoreFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": false}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
	Save(PendingPath(dir), p)
	restarted := false
	err := Watch(context.Background(), srv.Client(), p, PendingPath(dir), time.Now(), 50*time.Millisecond,
		func() error { return errors.New("disk full") }, func() error { restarted = true; return nil })
	if err == nil || restarted {
		t.Fatalf("a failed restore: %v, restarted %v", err, restarted)
	}
	var back Pending
	if ok, _ := Load(PendingPath(dir), &back); !ok || back.ToBuild != p.ToBuild {
		t.Fatal("the pending marker is gone after a failed restore")
	}
	if _, err := os.Stat(RolledBackPath(dir)); !os.IsNotExist(err) {
		t.Fatal("wrote down a rollback that did not happen")
	}
}

// An update directory the watchdog cannot rename in (read-only, wrong owner) does not stop it going back: it restores
// and restarts without the claim, as before claims (a confirm cannot remove the marker there either).
func TestWatchGoesBackWhenTheMarkerCannotBeClaimed(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": false}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
	if err := Save(PendingPath(dir), p); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	restored, restarted := false, false
	err := Watch(context.Background(), srv.Client(), p, PendingPath(dir), time.Now(), 50*time.Millisecond,
		func() error { restored = true; return nil }, func() error { restarted = true; return nil })
	if err != nil || !restored || !restarted {
		t.Fatalf("did not go back: %v, restored %v, restarted %v", err, restored, restarted)
	}
}

// A claim a watchdog died with goes back to the pending marker; a marker that is there already is left as it is.
func TestUnclaim(t *testing.T) {
	dir := t.TempDir()
	pending := PendingPath(dir)
	if err := Unclaim(pending); err != nil {
		t.Fatalf("no marker and no claim: %v", err)
	}
	Save(pending+".watch", Pending{ToBuild: 202611021700})
	if err := Unclaim(pending); err != nil {
		t.Fatal(err)
	}
	var back Pending
	if ok, _ := Load(pending, &back); !ok || back.ToBuild != 202611021700 {
		t.Fatal("the claimed marker did not come back")
	}
	if _, err := os.Stat(pending + ".watch"); !os.IsNotExist(err) {
		t.Fatal("the claim stayed")
	}
	Save(pending+".watch", Pending{ToBuild: 202611021600})
	if err := Unclaim(pending); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Load(pending, &back); !ok || back.ToBuild != 202611021700 {
		t.Fatalf("a marker that was there was replaced: %+v", back)
	}
}

// A hub that starts while the watchdog goes back puts the claim back (Unclaim): the rollback the watchdog writes down
// ends that update too, so the new build does not confirm an update whose old binary is back in place.
func TestWatchEndsAnUpdateUnclaimedWhileItWentBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": false}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
	Save(PendingPath(dir), p)
	err := Watch(context.Background(), srv.Client(), p, PendingPath(dir), time.Now(), 50*time.Millisecond,
		func() error { return Unclaim(PendingPath(dir)) }, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(PendingPath(dir) + "*"); len(left) != 0 {
		t.Fatalf("left %v behind", left)
	}
	if _, err := os.Stat(RolledBackPath(dir)); err != nil {
		t.Fatal("the rollback is not written down")
	}
}
