package node

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// cacheEnv points the user cache folder (or, with cache false, none at all) and the temp folder at fresh folders,
// and forgets the per-process temp folders made so far.
func cacheEnv(t *testing.T, cache bool) (cacheDir, tmp string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the user cache folder is LocalAppData on Windows")
	}
	tmp = t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if cache {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		var err error
		if cacheDir, err = os.UserCacheDir(); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Setenv("HOME", "")
		t.Setenv("XDG_CACHE_HOME", "")
		if _, err := os.UserCacheDir(); err == nil {
			t.Skip("this system has a user cache folder without HOME")
		}
	}
	reset := func() {
		processTempMu.Lock()
		clear(processTempDirs)
		processTempMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
	return cacheDir, tmp
}

func TestPrivateCacheDirIsTheUsersOwn(t *testing.T) {
	cache, _ := cacheEnv(t, true)
	dir, err := privateCacheDir("things")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(cache, "persona", "things") {
		t.Fatalf("dir %s, want under %s", dir, cache)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v (%v), want 0700", st.Mode(), err)
	}
}

// A folder others can write to, or a symlink to one, is refused rather than used.
func TestPrivateCacheDirRefusesAnOpenFolder(t *testing.T) {
	cache, _ := cacheEnv(t, true)
	open := filepath.Join(cache, "persona", "things")
	if err := os.MkdirAll(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if dir, err := privateCacheDir("things"); err == nil {
		t.Fatalf("used %s, which others can write to", dir)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(cache, "persona", "linked")); err != nil {
		t.Fatal(err)
	}
	if dir, err := privateCacheDir("linked"); err == nil {
		t.Fatalf("used %s through a symlink", dir)
	}
}

// Without a user cache folder the files go to a folder of this process's own in the temp folder, not to a fixed name
// another user could have made first.
func TestPrivateCacheDirFallbackIsThisProcesss(t *testing.T) {
	_, tmp := cacheEnv(t, false)
	planted := filepath.Join(tmp, "persona-things")
	if err := os.Mkdir(planted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(planted, 0o777); err != nil {
		t.Fatal(err)
	}
	dir, err := privateCacheDir("things")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != tmp || dir == planted || !strings.HasPrefix(filepath.Base(dir), "persona-things-") {
		t.Fatalf("fallback dir %s, want a fresh one in %s", dir, tmp)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v (%v), want 0700", st.Mode(), err)
	}
	if again, err := privateCacheDir("things"); err != nil || again != dir {
		t.Fatalf("second call %s (%v), want the same %s", again, err, dir)
	}
}

func orcaTestImages() []proto.Image {
	return []proto.Image{{Mime: "image/png", Data: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png bytes"))}}
}

// Images for Orca go to the user's cache, not the shared temp folder, and the ones Orca has long read are removed.
func TestOrcaImagesInTheUserCache(t *testing.T) {
	cache, tmp := cacheEnv(t, true)
	dir := filepath.Join(cache, "persona", "orca-images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "old.png"), filepath.Join(dir, "fresh.png")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-orcaImageKeep - time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	paths, err := orcaImages(orcaTestImages())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Dir(paths[0]) != dir || strings.HasPrefix(paths[0], tmp) {
		t.Fatalf("paths %v, want in %s", paths, dir)
	}
	if b, err := os.ReadFile(paths[0]); err != nil || string(b) != "png bytes" {
		t.Fatalf("read %q (%v)", b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("an image older than a day is still there (%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a recent image was removed: %v", err)
	}
}

// convertFixture is a document to convert with a fake LibreOffice, which counts its runs in calls.
func convertFixture(t *testing.T) (n *Node, req proto.FileConvertRequest, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = fakeSoffice(t, dir)
	doc := filepath.Join(dir, "old.doc")
	writeFile(t, doc, "old word")
	return &Node{home: dir}, proto.FileConvertRequest{Path: doc, To: "pdf"}, calls
}

// blobFixture is a repository with one committed PDF, and the request for its version at HEAD.
func blobFixture(t *testing.T) (*Node, proto.GitBlobRequest) {
	t.Helper()
	gitEnv(t)
	repo := t.TempDir()
	runPullGit(t, repo, "init", "--initial-branch=main", repo)
	writeFile(t, filepath.Join(repo, "spec.pdf"), "%PDF-1.4 committed")
	runPullGit(t, repo, "add", ".")
	runPullGit(t, repo, "commit", "-m", "one")
	return &Node{home: repo, state: newState()}, proto.GitBlobRequest{Cwd: repo, Path: "spec.pdf", Rev: "HEAD"}
}

// Without a user cache folder, converted copies and old versions go to folders of this process's own, never to the
// fixed persona/convert and persona/blobs in the shared temp folder, which another local user could have made first.
func TestFileCachesWithoutUserCacheAreThisProcesss(t *testing.T) {
	_, tmp := cacheEnv(t, false)
	shared := filepath.Join(tmp, "persona")
	if err := os.MkdirAll(filepath.Join(shared, "convert"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{shared, filepath.Join(shared, "convert")} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	theirs := t.TempDir()
	if err := os.Symlink(theirs, filepath.Join(shared, "blobs")); err != nil {
		t.Fatal(err)
	}

	n, req, _ := convertFixture(t)
	pdf, err := n.convertFile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	bn, breq := blobFixture(t)
	blob, err := bn.gitBlob(context.Background(), breq)
	if err != nil || blob.Missing {
		t.Fatalf("blob %+v, %v", blob, err)
	}
	for name, path := range map[string]string{"convert": pdf.Path, "blobs": blob.Path} {
		if dir, err := privateCacheDir(name); err != nil || filepath.Dir(path) != dir {
			t.Fatalf("%s at %s, want in this process's %s (%v)", name, path, dir, err)
		}
	}
	for _, dir := range []string{filepath.Join(shared, "convert"), theirs} {
		if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
			t.Fatalf("wrote into %s: %v (%v)", dir, ents, err)
		}
	}
}

// With a user cache folder the caches stay at <user cache>/persona/convert and .../blobs, and a symlink planted there
// is refused rather than written through.
func TestFileCachesInTheUserCacheRefuseASymlink(t *testing.T) {
	cache, _ := cacheEnv(t, true)
	n, req, calls := convertFixture(t)
	pdf, err := n.convertFile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	bn, breq := blobFixture(t)
	blob, err := bn.gitBlob(context.Background(), breq)
	if err != nil {
		t.Fatal(err)
	}
	theirs := t.TempDir()
	for name, path := range map[string]string{"convert": pdf.Path, "blobs": blob.Path} {
		dir := filepath.Join(cache, "persona", name)
		if filepath.Dir(path) != dir {
			t.Fatalf("%s at %s, want in %s", name, path, dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(theirs, dir); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := n.convertFile(context.Background(), req); err == nil {
		t.Fatalf("converted into %s through a symlink", got.Path)
	}
	if got, err := bn.gitBlob(context.Background(), breq); err == nil {
		t.Fatalf("wrote a version to %s through a symlink", got.Path)
	}
	if ents, err := os.ReadDir(theirs); err != nil || len(ents) != 0 {
		t.Fatalf("wrote into the symlink's target: %v (%v)", ents, err)
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("LibreOffice ran %q, want once", b)
	}
}

// Opening a cached copy again leaves the revision a download in progress is pinned to alone (usedCached).
func TestFileCacheHitsKeepADownloadGoing(t *testing.T) {
	cacheEnv(t, true)
	n, req, _ := convertFixture(t)
	bn, breq := blobFixture(t)
	open := func() map[string]string {
		t.Helper()
		pdf, err := n.convertFile(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		blob, err := bn.gitBlob(context.Background(), breq)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"convert": pdf.Path, "blobs": blob.Path}
	}
	paths := open()
	revs := map[string]string{}
	for name, path := range paths {
		head, err := n.readFileChunk(proto.FileChunkRequest{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		revs[name] = head.Revision
	}
	open() // another client opens both while the first one reads them
	for name, path := range paths {
		if _, err := n.readFileChunk(proto.FileChunkRequest{Path: path, Length: 4, Revision: revs[name]}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The preview browser's profile keeps the dev app's sign-ins: without a user cache folder it is this process's own
// folder, never the fixed persona/preview-browser in the shared temp folder that another local user could make first.
func TestPreviewProfileWithoutUserCacheIsThisProcesss(t *testing.T) {
	_, tmp := cacheEnv(t, false)
	shared := filepath.Join(tmp, "persona", "preview-browser")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(shared), shared} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := previewProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == shared || filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), "persona-preview-browser-") {
		t.Fatalf("profile at %s, want a folder of this process's own in %s", dir, tmp)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("profile folder %v (%v), want 0700", st, err)
	}
}

// With a user cache folder the profile stays at <user cache>/persona/preview-browser (sign-ins kept from before), and
// a symlink planted there stops the browser from starting rather than writing the profile through it.
func TestPreviewProfileInTheUserCacheRefusesASymlink(t *testing.T) {
	cache, _ := cacheEnv(t, true)
	want := filepath.Join(cache, "persona", "preview-browser")
	if dir, err := previewProfileDir(); err != nil || dir != want {
		t.Fatalf("profile at %s (%v), want %s", dir, err, want)
	}
	if err := os.RemoveAll(want); err != nil {
		t.Fatal(err)
	}
	theirs := t.TempDir()
	if err := os.Symlink(theirs, want); err != nil {
		t.Fatal(err)
	}
	if dir, err := previewProfileDir(); err == nil {
		t.Fatalf("profile at %s through a symlink", dir)
	}
	bin := filepath.Join(t.TempDir(), "my-chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch \"$0.ran\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(previewBrowserEnv, bin)
	if _, err := startPreviewBrowser(context.Background()); err == nil || !strings.Contains(err.Error(), "profile folder") {
		t.Fatalf("started with the profile behind a symlink: %v", err)
	}
	if _, err := os.Stat(bin + ".ran"); err == nil {
		t.Fatal("ran the browser with the profile behind a symlink")
	}
	if ents, err := os.ReadDir(theirs); err != nil || len(ents) != 0 {
		t.Fatalf("wrote into the symlink's target: %v (%v)", ents, err)
	}
}
