package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Pause part-file work without replacing its storage: releasing the gate writes
// or closes the original regular file.
type gatedUploadFile struct {
	uploadFile
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	onClose bool
	closes  atomic.Int32
}

func (f *gatedUploadFile) wait() {
	f.once.Do(func() { close(f.entered) })
	<-f.release
}
func (f *gatedUploadFile) Write(p []byte) (int, error) {
	if !f.onClose {
		f.wait()
	}
	return f.uploadFile.Write(p)
}
func (f *gatedUploadFile) Close() error {
	if f.onClose {
		f.wait()
	}
	f.closes.Add(1)
	return f.uploadFile.Close()
}
func awaitUpload[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("upload operation did not settle")
		var zero T
		return zero
	}
}

type uploadRPCFixture struct {
	n    *Node
	peer *rpc.Peer
}

func newUploadRPCFixture(t *testing.T) *uploadRPCFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	n := &Node{home: dir, dir: dir, state: newState(), logger: log.New(io.Discard, "", 0)}
	client, server := net.Pipe()
	peer, nodePeer := rpc.New("h", client), rpc.New("n", server)
	nodePeer.OnRequest = n.handle
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = peer.Serve(ctx, client); done <- struct{}{} }()
	go func() { _ = nodePeer.Serve(ctx, server); done <- struct{}{} }()
	t.Cleanup(func() {
		n.ups.mu.Lock()
		ids := make([]string, 0, len(n.ups.open))
		for id := range n.ups.open {
			ids = append(ids, id)
		}
		n.ups.mu.Unlock()
		for _, id := range ids {
			n.ups.abort(id)
		}
		cancel()
		client.Close()
		server.Close()
		awaitUpload(t, done)
		awaitUpload(t, done)
	})
	return &uploadRPCFixture{n: n, peer: peer}
}
func (f *uploadRPCFixture) call(method string, p map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.peer.Call(ctx, "file.upload."+method, p, out)
}
func (f *uploadRPCFixture) begin(t *testing.T, p map[string]any) string {
	t.Helper()
	var out struct {
		ID string `json:"upload_id"`
	}
	if err := f.call("begin", p, &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}
func (f *uploadRPCFixture) gate(t *testing.T, id string, onClose bool) (*gatedUploadFile, string) {
	t.Helper()
	up := f.n.ups.lookup(id)
	up.mu.Lock()
	defer up.mu.Unlock()
	g := &gatedUploadFile{uploadFile: up.f, entered: make(chan struct{}), release: make(chan struct{}), onClose: onClose}
	up.f = g // installed before any operation is dispatched
	t.Cleanup(func() {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
	})
	return g, up.f.Name()
}
func (f *uploadRPCFixture) assertRetired(t *testing.T, id, part string) {
	t.Helper()
	f.n.ups.mu.Lock()
	listed := f.n.ups.open[id] != nil
	f.n.ups.mu.Unlock()
	if listed {
		t.Error("retired upload returned to the registry")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("part file remains: %v", err)
	}
	if err := f.call("chunk", map[string]any{"upload_id": id, "data": []byte("x")}, nil); err == nil {
		t.Error("retired upload accepted a chunk")
	}
	if err := f.call("end", map[string]any{"upload_id": id}, nil); err == nil {
		t.Error("retired upload accepted end")
	}
}
func TestUploadAbortDuringChunkRPC(t *testing.T) {
	f := newUploadRPCFixture(t)
	data := bytes.Repeat([]byte("x"), proto.UploadChunkBytes)
	id := f.begin(t, map[string]any{"name": "cancelled.txt", "size": len(data)})
	gate, part := f.gate(t, id, false)
	done := make(chan error, 1)
	go func() { done <- f.call("chunk", map[string]any{"upload_id": id, "data": data}, nil) }()
	awaitUpload(t, gate.entered)
	if f.n.ups.lookup(id) == nil {
		t.Error("busy upload is invisible to cancellation")
	}
	for _, method := range []string{"chunk", "end", "chunk", "end"} {
		if err := f.call(method, map[string]any{"upload_id": id}, nil); err == nil {
			t.Errorf("concurrent %s succeeded", method)
		}
	}
	for i := 0; i < 2; i++ {
		if err := f.call("abort", map[string]any{"upload_id": id}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if gate.closes.Load() != 0 {
		t.Error("abort closed a file still owned by a write")
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("part removed before its writer settled: %v", err)
	}
	other := f.begin(t, map[string]any{"name": "independent.txt", "size": 0})
	var out proto.UploadedFile
	if err := f.call("end", map[string]any{"upload_id": other}, &out); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err := awaitUpload(t, done); err == nil {
		t.Error("canceled chunk reported success")
	}
	f.assertRetired(t, id, part)
	if gate.closes.Load() != 1 {
		t.Errorf("closed canceled part %d times", gate.closes.Load())
	}
	retry := f.begin(t, map[string]any{"name": "cancelled.txt", "size": 0})
	if err := f.call("end", map[string]any{"upload_id": retry}, nil); err != nil {
		t.Fatalf("new upload after abort: %v", err)
	}
}
func TestUploadAbortBeforePublicationRPC(t *testing.T) {
	for _, tc := range []struct{ dest, conflict string }{
		{}, {dest: proto.UploadDestTerminal}, {dest: proto.UploadDestHealth},
		{dest: proto.UploadDestDir}, {dest: proto.UploadDestDir, conflict: proto.UploadConflictKeep},
		{dest: proto.UploadDestDir, conflict: proto.UploadConflictReplace},
	} {
		t.Run("dest="+tc.dest+"/conflict="+tc.conflict, func(t *testing.T) {
			f := newUploadRPCFixture(t)
			p := map[string]any{"name": "cancelled.txt", "size": 0, "dest": tc.dest, "conflict": tc.conflict}
			var existing string
			if tc.dest == proto.UploadDestDir {
				p["dir"] = f.n.home
			}
			if tc.conflict != "" {
				existing = filepath.Join(f.n.home, "cancelled.txt")
				if err := os.WriteFile(existing, []byte("original"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			id := f.begin(t, p)
			gate, part := f.gate(t, id, true)
			done := make(chan error, 1)
			go func() { done <- f.call("end", map[string]any{"upload_id": id}, nil) }()
			awaitUpload(t, gate.entered)
			if err := f.call("abort", map[string]any{"upload_id": id}, nil); err != nil {
				t.Fatal(err)
			}
			close(gate.release)
			if err := awaitUpload(t, done); err == nil {
				t.Error("finalization published after abort won")
			}
			f.assertRetired(t, id, part)
			if gate.closes.Load() != 1 {
				t.Errorf("closed canceled part %d times", gate.closes.Load())
			}
			entries, err := os.ReadDir(filepath.Dir(part))
			want := 0
			if existing != "" {
				want = 1
				if b, err := os.ReadFile(existing); err != nil || string(b) != "original" {
					t.Errorf("existing destination changed: %q %v", b, err)
				}
				if st, err := os.Stat(existing); err != nil || st.Mode().Perm() != 0o640 {
					t.Errorf("existing permissions changed: %v", err)
				}
			}
			if err != nil || len(entries) != want {
				t.Errorf("publication left files: %v, %v", entries, err)
			}
		})
	}
}

func TestUploadAbortAfterPublicationRPC(t *testing.T) {
	f := newUploadRPCFixture(t)
	id := f.begin(t, map[string]any{"name": "committed.txt", "size": 0})
	up := f.n.ups.lookup(id)
	up.mu.Lock()
	epoch := up.epoch
	up.mu.Unlock()
	var out proto.UploadedFile
	if err := f.call("end", map[string]any{"upload_id": id}, &out); err != nil {
		t.Fatal(err)
	}
	if err := f.call("abort", map[string]any{"upload_id": id}, nil); err != nil {
		t.Fatal(err)
	}
	f.n.ups.expire(id, up, epoch)
	up.mu.Lock()
	timer := up.idle
	up.mu.Unlock()
	if timer != nil {
		t.Error("committed upload retained a timer")
	}
	if st, err := os.Stat(out.Path); err != nil || st.Size() != 0 {
		t.Fatalf("committed upload was undone: %v", err)
	}
	if err := f.call("end", map[string]any{"upload_id": id}, nil); err == nil {
		t.Fatal("committed upload ended twice")
	}
}

func TestUploadStaleExpiryRPC(t *testing.T) {
	f := newUploadRPCFixture(t)
	id := f.begin(t, map[string]any{"name": "expires.txt", "size": 1})
	up := f.n.ups.lookup(id)
	up.mu.Lock()
	stale := up.epoch
	up.mu.Unlock()
	gate, part := f.gate(t, id, false)
	done := make(chan error, 1)
	go func() { done <- f.call("chunk", map[string]any{"upload_id": id, "data": []byte("x")}, nil) }()
	awaitUpload(t, gate.entered)
	// Model an idle callback that was scheduled before fresh work took ownership.
	f.n.ups.expire(id, up, stale)
	up.mu.Lock()
	retired, timer := up.retired, up.idle
	up.mu.Unlock()
	if retired || timer != nil {
		t.Error("stale callback canceled fresh work or active work retained an idle timer")
	}
	close(gate.release)
	if err := awaitUpload(t, done); err != nil {
		t.Fatal(err)
	}
	f.n.ups.expire(id, up, stale)
	if f.n.ups.lookup(id) != up {
		t.Fatal("stale callback retired the refreshed upload")
	}
	up.mu.Lock()
	current := up.epoch
	up.mu.Unlock()
	f.n.ups.expire(id, up, current)
	f.n.ups.expire(id, up, current)
	f.assertRetired(t, id, part)
	up.mu.Lock()
	timer = up.idle
	up.mu.Unlock()
	if timer != nil {
		t.Error("retired upload retained an idle timer")
	}
}

func TestUploadConcurrentEndRPC(t *testing.T) {
	f := newUploadRPCFixture(t)
	id := f.begin(t, map[string]any{"name": "once.txt", "size": 0})
	gate, _ := f.gate(t, id, true)
	done := make(chan error, 1)
	var out proto.UploadedFile
	go func() { done <- f.call("end", map[string]any{"upload_id": id}, &out) }()
	awaitUpload(t, gate.entered)
	for _, method := range []string{"end", "chunk", "end"} {
		if err := f.call(method, map[string]any{"upload_id": id}, nil); err == nil {
			t.Errorf("concurrent %s succeeded", method)
		}
	}
	close(gate.release)
	if err := awaitUpload(t, done); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(out.Path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one publication: %v %v", entries, err)
	}
}

type failingUploadFile struct {
	uploadFile
	short              bool
	writeErr, closeErr error
	closes             atomic.Int32
}

func (f *failingUploadFile) Write(p []byte) (int, error) {
	if f.short {
		return f.uploadFile.Write(p[:len(p)/2])
	}
	return 0, f.writeErr
}
func (f *failingUploadFile) Close() error {
	f.closes.Add(1)
	err := f.uploadFile.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}
func TestUploadFailureRetiresRPC(t *testing.T) {
	for _, kind := range []string{"write", "short write", "close", "missing part"} {
		t.Run(kind, func(t *testing.T) {
			f := newUploadRPCFixture(t)
			size := 4
			if kind == "close" || kind == "missing part" {
				size = 0
			}
			id := f.begin(t, map[string]any{"name": "failed.txt", "size": size})
			up := f.n.ups.lookup(id)
			file := &failingUploadFile{uploadFile: up.f, short: kind == "short write", writeErr: errors.New("synthetic write failure")}
			if kind == "close" {
				file.closeErr = errors.New("synthetic close failure")
			}
			up.f = file
			part := file.Name()
			method := "chunk"
			if size == 0 {
				method = "end"
			}
			if kind == "missing part" {
				if err := os.Remove(part); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.call(method, map[string]any{"upload_id": id, "data": []byte("data")}, nil); err == nil {
				t.Fatal("failed storage operation succeeded")
			}
			f.assertRetired(t, id, part)
			if file.closes.Load() != 1 {
				t.Errorf("closed part %d times", file.closes.Load())
			}
			retry := f.begin(t, map[string]any{"name": "failed.txt", "size": 0})
			if err := f.call("end", map[string]any{"upload_id": retry}, nil); err != nil {
				t.Fatalf("new upload after failure: %v", err)
			}
		})
	}
}
