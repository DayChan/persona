package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// uploadNode answers file.upload.* in memory over a real rpc pair, like a node over ssh stdio.
type uploadNode struct {
	mu      sync.Mutex
	old     bool // predates file.upload.*
	calls   []string
	offsets []int64
	data    bytes.Buffer
	aborted bool
	dests   []string // the dest of each file.upload.begin
	begins  []map[string]any
	noEcho  bool   // knows file.upload.* but not dest=dir: opens it in its temp directory and says nothing
	refuse  string // file.upload.begin fails with this rpc code
	noDest  bool   // fails dest=dir as a node from before it does: "unknown upload destination"
	// noHealthEcho: takes health_dir without saying so (a node from before other managers' health spaces)
	noHealthEcho bool
}

func (f *uploadNode) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	if f.old {
		return nil, errors.New("unknown method " + method)
	}
	var p struct {
		Name      string `json:"name"`
		Dest      string `json:"dest"`
		HealthDir string `json:"health_dir"`
		Offset    int64  `json:"offset"`
		Data      []byte `json:"data"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case "file.upload.begin":
		f.dests = append(f.dests, p.Dest)
		var all map[string]any
		_ = json.Unmarshal(params, &all)
		f.begins = append(f.begins, all)
		if f.refuse != "" {
			return nil, &rpc.Error{Code: f.refuse, Message: "refused", Data: json.RawMessage(`{"name":"a.txt"}`)}
		}
		if f.noDest && p.Dest == proto.UploadDestDir {
			return nil, errors.New(`unknown upload destination "dir"`)
		}
		if p.Dest == proto.UploadDestDir && !f.noEcho {
			return map[string]any{"upload_id": "u1", "dest": p.Dest}, nil
		}
		if p.HealthDir != "" && !f.noHealthEcho {
			return map[string]any{"upload_id": "u1", "health_dir": p.HealthDir}, nil
		}
		return map[string]any{"upload_id": "u1"}, nil
	case "file.upload.chunk":
		f.offsets = append(f.offsets, p.Offset)
		f.data.Write(p.Data)
		return map[string]any{"received": f.data.Len()}, nil
	case "file.upload.end":
		return proto.UploadedFile{Path: "/tmp/report-20261029-153012.pdf", Name: "report-20261029-153012.pdf", Size: int64(f.data.Len())}, nil
	case "file.upload.abort":
		f.aborted = true
		return map[string]any{"ok": true}, nil
	}
	return nil, errors.New("unknown method " + method)
}

func uploadHub(t *testing.T, f *uploadNode) http.Handler {
	t.Helper()
	_, _, mux := uploadHubOf(t, f)
	return mux
}

// uploadHubOf is uploadHub with its hub and its machine m1.
func uploadHubOf(t *testing.T, f *uploadNode) (*Hub, *Machine, http.Handler) {
	t.Helper()
	return uploadHubHandler(t, f.handle)
}

func uploadHubHandler(t *testing.T, handler rpc.Handler) (*Hub, *Machine, http.Handler) {
	t.Helper()
	h := newTestHub(t)
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	hubPeer := rpc.New("h", nodeIn)
	nodePeer := rpc.New("n", hubIn)
	nodePeer.OnRequest = handler
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); toNode.Close(); toHub.Close() })
	go hubPeer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	m := newMachine(h, MachineConfig{ID: "m1", Name: "box"})
	m.peer = hubPeer
	h.byID["m1"] = m
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/machines/{mid}/files", h.withMachine(h.uploadFile))
	return h, m, mux
}

// A health report goes in the health space of the machine's manager: the built-in's as ever (no health_dir: the
// node's call is what it was), another's health-<id>, which the node has to echo; a node that does not know other
// managers' spaces (no capability, or no echo) is told to update, and nothing stays.
func TestHealthUploadInTheMachinesManagersSpace(t *testing.T) {
	upload := func(f *uploadNode, owner string, info proto.NodeInfo) *httptest.ResponseRecorder {
		h, m, mux := uploadHubOf(t, f)
		h.cfg.Machines = []MachineConfig{{ID: "m1", Name: "box"}}
		if owner != "" {
			h.devices.assign = map[string]string{"m1": owner}
		}
		m.info = info
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=report.pdf&dest=health", strings.NewReader("pdf")))
		return rec
	}
	knows := proto.NodeInfo{Version: "v1", Protocol: nodeproto.Version, Capabilities: nodeproto.Announced}
	builtin := &uploadNode{}
	if rec := upload(builtin, "", proto.NodeInfo{Version: "old"}); rec.Code != 200 || len(builtin.begins) != 1 || builtin.begins[0]["health_dir"] != nil {
		t.Fatalf("the built-in's: %d %s %v", rec.Code, rec.Body, builtin.begins)
	}
	other := &uploadNode{}
	if rec := upload(other, "m-0b0b0b", knows); rec.Code != 200 || len(other.begins) != 1 || other.begins[0]["health_dir"] != "health-m-0b0b0b" {
		t.Fatalf("another manager's: %d %s %v", rec.Code, rec.Body, other.begins)
	}
	quiet := &uploadNode{noHealthEcho: true}
	if rec := upload(quiet, "m-0b0b0b", knows); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), proto.CodeNodeOutdated) || !quiet.aborted {
		t.Fatalf("a node that does not echo it: %d %s, aborted %v", rec.Code, rec.Body, quiet.aborted)
	}
	old := &uploadNode{}
	if rec := upload(old, "m-0b0b0b", proto.NodeInfo{Version: "old", Protocol: nodeproto.Version, Capabilities: []string{nodeproto.Sessions}}); rec.Code != http.StatusConflict ||
		len(old.begins) != 0 {
		t.Fatalf("a node without health_spaces: %d %s %v", rec.Code, rec.Body, old.begins)
	}
}

func TestUploadStreamsChunks(t *testing.T) {
	f := &uploadNode{}
	mux := uploadHub(t, f)
	data := bytes.Repeat([]byte{7}, proto.UploadChunkBytes*2+12345)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=report.pdf", bytes.NewReader(data)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out proto.UploadedFile
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Path != "/tmp/report-20261029-153012.pdf" || out.Size != int64(len(data)) {
		t.Fatalf("%+v", out)
	}
	want := []int64{0, proto.UploadChunkBytes, 2 * proto.UploadChunkBytes}
	if len(f.offsets) != 3 || f.offsets[0] != want[0] || f.offsets[1] != want[1] || f.offsets[2] != want[2] {
		t.Fatalf("offsets %v", f.offsets)
	}
	if !bytes.Equal(f.data.Bytes(), data) || f.aborted {
		t.Fatal("data or abort")
	}
}

func TestUploadRefused(t *testing.T) {
	// Over the limit: refused before the node hears of it.
	f := &uploadNode{}
	mux := uploadHub(t, f)
	req := httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=big.bin", strings.NewReader("x"))
	req.ContentLength = proto.MaxUploadBytes + 1
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `"code":"too_large"`) || len(f.calls) != 0 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, f.calls)
	}

	// A node from before file uploads: 409 node_outdated, not a bare 502.
	old := &uploadNode{old: true}
	rec = httptest.NewRecorder()
	uploadHub(t, old).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt", strings.NewReader("hi")))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"node_outdated"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	// No name.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files", strings.NewReader("hi")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
}

// failingBody gives n bytes, then the connection drops (the app cancelled).
type failingBody struct{ n int }

func (b *failingBody) Read(p []byte) (int, error) {
	if b.n <= 0 {
		return 0, errors.New("connection reset")
	}
	k := min(len(p), b.n)
	b.n -= k
	return k, nil
}

func TestUploadCancelledAborts(t *testing.T) {
	f := &uploadNode{}
	mux := uploadHub(t, f)
	req := httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.bin", &failingBody{n: proto.UploadChunkBytes + 10})
	req.ContentLength = 5 * proto.UploadChunkBytes
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == 200 || !f.aborted {
		t.Fatalf("%d aborted=%v calls=%v", rec.Code, f.aborted, f.calls)
	}
	for _, c := range f.calls {
		if c == "file.upload.end" {
			t.Fatal("ended a cut-short upload")
		}
	}
}

func TestUploadForTerminal(t *testing.T) {
	// dest=terminal reaches the node, which keeps the file in its Persona directory.
	f := &uploadNode{}
	mux := uploadHub(t, f)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=image.png&dest=terminal", strings.NewReader("png")))
	if rec.Code != 200 || len(f.dests) != 1 || f.dests[0] != proto.UploadDestTerminal {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, f.dests)
	}
	// dest=health (a report for the health space) reaches the node too.
	hf := &uploadNode{}
	rec = httptest.NewRecorder()
	uploadHub(t, hf).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=report.pdf&dest=health", strings.NewReader("pdf")))
	if rec.Code != 200 || len(hf.dests) != 1 || hf.dests[0] != proto.UploadDestHealth {
		t.Fatalf("health: %d %s %v", rec.Code, rec.Body, hf.dests)
	}
	// Without dest the node hears none (an old node never has to understand it).
	plain := &uploadNode{}
	rec = httptest.NewRecorder()
	uploadHub(t, plain).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt", strings.NewReader("hi")))
	if rec.Code != 200 || len(plain.dests) != 1 || plain.dests[0] != "" {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, plain.dests)
	}

	// The terminal's limit is lower than the chat's, and the answer says what it is.
	req := httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=big.mov&dest=terminal", strings.NewReader("x"))
	req.ContentLength = proto.MaxTerminalUploadBytes + 1
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `"code":"too_large"`) || len(f.dests) != 1 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, f.dests)
	}
	var body struct {
		Limit int64 `json:"limit"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Limit != proto.MaxTerminalUploadBytes {
		t.Fatalf("limit %d", body.Limit)
	}

	// An unknown destination is refused before the node hears of it.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt&dest=%2Fetc", strings.NewReader("hi")))
	if rec.Code != http.StatusBadRequest || len(f.dests) != 1 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, f.dests)
	}
}

// dest=dir hands the folder, cwd and conflict policy to the node and takes up to the bigger cap; the node's refusals
// keep their codes; a node that predates dest=dir is node_outdated, never a file quietly in its temp directory.
func TestUploadIntoFolder(t *testing.T) {
	f := &uploadNode{}
	rec := httptest.NewRecorder()
	uploadHub(t, f).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt&dest=dir&dir=~/proj&cwd=/w&conflict=keep", strings.NewReader("hi")))
	if rec.Code != 200 || len(f.begins) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b := f.begins[0]; b["dest"] != "dir" || b["dir"] != "~/proj" || b["cwd"] != "/w" || b["conflict"] != "keep" {
		t.Fatalf("begin %v", b)
	}

	// No folder: refused before the node hears of it.
	f = &uploadNode{}
	rec = httptest.NewRecorder()
	uploadHub(t, f).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt&dest=dir", strings.NewReader("hi")))
	if rec.Code != http.StatusBadRequest || len(f.calls) != 0 {
		t.Fatalf("%d %v", rec.Code, f.calls)
	}

	// The folder's cap is above the chat's.
	f = &uploadNode{}
	req := httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.bin&dest=dir&dir=~", strings.NewReader("x"))
	req.ContentLength = proto.MaxUploadBytes + 1
	rec = httptest.NewRecorder()
	uploadHub(t, f).ServeHTTP(rec, req)
	if rec.Code == http.StatusRequestEntityTooLarge || len(f.begins) != 1 {
		t.Fatalf("over the chat's cap: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.bin&dest=dir&dir=~", strings.NewReader("x"))
	req.ContentLength = proto.MaxDirUploadBytes + 1
	rec = httptest.NewRecorder()
	uploadHub(t, &uploadNode{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the folder's cap: %d", rec.Code)
	}

	// The name is taken / the folder is read-only: 409 with the node's code and details.
	for _, code := range []string{proto.CodeExists, proto.CodeReadOnly} {
		rec = httptest.NewRecorder()
		uploadHub(t, &uploadNode{refuse: code}).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt&dest=dir&dir=~", strings.NewReader("hi")))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"`+code+`"`) || !strings.Contains(rec.Body.String(), `"name":"a.txt"`) {
			t.Fatalf("%s: %d %s", code, rec.Code, rec.Body)
		}
	}

	// Nodes from before dest=dir: one that says so, and one that ignores dest (its upload is aborted, nothing sent).
	for _, old := range []*uploadNode{{noDest: true}, {noEcho: true}} {
		rec = httptest.NewRecorder()
		uploadHub(t, old).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=a.txt&dest=dir&dir=~", strings.NewReader("hi")))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"node_outdated"`) || old.data.Len() != 0 {
			t.Fatalf("old node: %d %s %v", rec.Code, rec.Body, old.calls)
		}
		if old.noEcho && !old.aborted {
			t.Fatal("the ignored upload was not aborted")
		}
	}
}

// The caller stops waiting for the chunk, but the node still owns its write.
// Deferred cleanup must send abort with a live context and must never send end.
func TestUploadCancelledDuringChunkAborts(t *testing.T) {
	f := &uploadNode{}
	entered, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	aborted := make(chan error, 1)
	_, _, mux := uploadHubHandler(t, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "file.upload.chunk" {
			close(entered)
			<-release
			defer close(settled)
		}
		out, err := f.handle(ctx, method, params)
		if method == "file.upload.abort" {
			aborted <- ctx.Err()
		}
		return out, err
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/api/v1/machines/m1/files?name=cancelled.bin", bytes.NewReader([]byte("data"))).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { mux.ServeHTTP(rec, req); close(done) }()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("canceled upload did not settle")
		}
	}
	wait(entered)
	cancel()
	select {
	case err := <-aborted:
		if err != nil {
			t.Fatalf("abort inherited cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no abort while chunk was outstanding")
	}
	wait(done)
	if rec.Code == http.StatusOK {
		t.Fatal("canceled HTTP upload succeeded")
	}
	close(release)
	wait(settled)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.aborted {
		t.Fatal("canceled upload was not aborted")
	}
	for _, method := range f.calls {
		if method == "file.upload.end" {
			t.Fatal("canceled upload was finalized")
		}
	}
}
