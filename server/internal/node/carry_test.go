package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// carryTranscript is a transcript of about 8 KiB (twice bgHeadLen) whose background command started at; tag makes its
// first line differ from another one's of the same length.
func carryTranscript(at time.Time, tag byte) []byte {
	ts := at.UTC().Format(time.RFC3339)
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"type":"user","uuid":"u0","timestamp":%q,"cwd":"/work/carry","message":{"role":"user","content":"start %c"}}`+"\n", ts, tag)
	fmt.Fprintf(&b, `{"type":"assistant","uuid":"a1","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"sleep 600","run_in_background":true}}]}}`+"\n", ts)
	fmt.Fprintf(&b, `{"type":"user","uuid":"u1","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: b1. Output is being written to: /tmp/tasks/b1.output"}]}}`+"\n", ts)
	for i := 0; b.Len() < 2*bgHeadLen; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","uuid":"p%d","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"waiting %d"}]}}`+"\n", i, ts, i)
	}
	return b.Bytes()
}

// A scan carried to the next connection's Node goes on from its offset only while its path still holds the file it
// read, as long as it was and starting with the same bytes; otherwise it starts over. Either way it then has what a
// fresh scan of the file has. A carried scan of a deleted transcript, which the new Node has no quick meta for, is
// pruned.
func TestCarriedScanRevalidated(t *testing.T) {
	now := time.Now()
	src := carryTranscript(now, 'a')
	background := `{"type":"assistant","uuid":"a2","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"sleep 700","run_in_background":true}}]}}` + "\n" +
		`{"type":"user","uuid":"u2","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"Command running in background with ID: b2. Output is being written to: /tmp/tasks/b2.output"}]}}` + "\n"
	more := fmt.Sprintf(background, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	replace := func(t *testing.T, path string, b []byte) { // another file (inode) at the path
		t.Helper()
		if err := os.WriteFile(path+".new", b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name   string
		change func(t *testing.T, path string)
		kept   bool
	}{
		{"appended", func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString(more); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"renamed, another file at the path", func(t *testing.T, path string) {
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(carryTranscript(now, 'b'), more...), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"truncated", func(t *testing.T, path string) {
			if err := os.Truncate(path, int64(len(src)/2)); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"rewritten in place, same size", func(t *testing.T, path string) {
			if err := os.WriteFile(path, carryTranscript(now, 'b'), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"same bytes, another inode", func(t *testing.T, path string) { replace(t, path, append(bytes.Clone(src), more...)) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			if err := os.WriteFile(path, src, 0o600); err != nil {
				t.Fatal(err)
			}
			n1 := &Node{bgScans: map[string]*bgScan{}}
			if tasks, _, _, _ := n1.backgroundTasks(path); len(tasks) != 1 {
				t.Fatalf("first connection: %d background tasks, want 1", len(tasks))
			}
			carry := &bgCarry{}
			n1.handOverBgScans(carry)
			c.change(t, path)
			n2 := &Node{bgScans: map[string]*bgScan{}}
			n2.adoptBgScans(carry)
			sc := n2.bgScans[path]
			if sc == nil || sc.off != int64(len(src)) {
				t.Fatal("the scan was not carried")
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			st, _ := f.Stat()
			sc.mu.Lock()
			sc.checkCarried(f, st)
			kept := sc.off == int64(len(src))
			sc.mu.Unlock()
			f.Close()
			if kept != c.kept {
				t.Fatalf("kept the carried offset: %v, want %v", kept, c.kept)
			}
			got, _, _, _ := n2.backgroundTasks(path)
			want, _, _, _ := (&Node{bgScans: map[string]*bgScan{}}).backgroundTasks(path)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("background tasks after the change\n got %+v\nwant %+v", got, want)
			}
		})
	}

	t.Run("deleted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(path, src, 0o600); err != nil {
			t.Fatal(err)
		}
		n1 := &Node{bgScans: map[string]*bgScan{}, metas: map[string]quickMeta{}}
		n1.backgroundTasks(path)
		carry := &bgCarry{}
		n1.handOverBgScans(carry)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		n2 := &Node{bgScans: map[string]*bgScan{}, metas: map[string]quickMeta{}}
		n2.adoptBgScans(carry)
		n2.pruneGoneMetas(nil)
		if len(n2.bgScans) != 0 {
			t.Fatalf("a deleted transcript's carried scan stays: %d entries", len(n2.bgScans))
		}
	})
}

// A resident node's next connection goes on with the last one's background-task scans: its first use of the
// transcript's meta has the background task, and the scan is the one the last connection made. A read from the start
// of this transcript (8 KiB, bgSyncLimit 1 KiB here) runs off the list's path and initially returns pending.
func TestAttachCarriesBackgroundScans(t *testing.T) {
	home := shortHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CODEX_CONFIG_DIR", "")
	t.Setenv("HERDR_CONFIG_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", os.Getenv("PATH")) // New puts ~/.local/bin and the like in front
	defer func(v int64) { bgSyncLimit = v }(bgSyncLimit)
	bgSyncLimit = 1 << 10
	const id = "c0ffee00-0000-4000-8000-000000000001"
	dir := filepath.Join(home, ".claude", "projects", "-work-carry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, carryTranscript(time.Now(), 'a'), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var nodes []*Node
	extra := make(chan net.Conn)
	done := make(chan error, 1)
	go func() {
		done <- Attach(ctx, func() (*Node, error) {
			n, err := New("test")
			if err == nil {
				// Only the fixture's transcripts participate, regardless of agents running on this machine.
				n.cdirs.scan = func() map[int]procEnv { return nil }
				n.codices.scan = func() []codexProcess { return nil }
				n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
				mu.Lock()
				nodes = append(nodes, n)
				mu.Unlock()
			}
			return n, err
		}, extra, nil)
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Attach: %v", err)
		}
	}()
	// connect is the hub on a new connection: state.load answers with no sessions, everything else with {}.
	connect := func() (*rpc.Peer, net.Conn) {
		a, b := net.Pipe()
		hub := rpc.New("h", a)
		hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			return map[string]any{"sessions": map[string]any{}}, nil
		}
		go func() { _ = hub.Serve(ctx, a) }()
		extra <- b
		if err := hub.Call(ctx, "node.configure", map[string]any{"machine_id": "m1", "herdr_autostart": false, "botmux": false}, nil); err != nil {
			t.Fatal(err)
		}
		return hub, a
	}
	// listed checks discovery through the hub, then the background state on this connection. The shared offline list
	// can already hold the previous node's row without having read this node's meta at all.
	listed := func(hub *rpc.Peer, i int) bool {
		t.Helper()
		offlineCache.Lock() // the transcript's row is read again for every list, not taken from the last 20 s
		offlineCache.at = time.Time{}
		offlineCache.Unlock()
		if i == 1 {
			// An in-flight poll from the old connection can refill the shared cache before the new list arrives.
			mu.Lock()
			previous := nodes[0]
			mu.Unlock()
			previous.appendOffline(nil, nil)
		}
		var reply struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if err := hub.Call(ctx, "sessions.list", map[string]any{}, &reply); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(reply.Sessions, func(s proto.Session) bool { return s.SessionID == id }) {
			t.Fatalf("%s is not listed", id)
		}
		mu.Lock()
		n := nodes[i]
		mu.Unlock()
		n.mu.Lock()
		m := n.metas[path]
		n.mu.Unlock()
		if i == 1 {
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			m = n.meta(path, st) // first use must return the carried task without scheduling another scan
		}
		return !m.bgWait && len(m.bg) == 1 && strings.Contains(m.bg[0].Command, "sleep 600")
	}
	scanOf := func(i int) *bgScan {
		mu.Lock()
		n := nodes[i]
		mu.Unlock()
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.bgScans[path]
	}

	hub, conn := connect()
	if listed(hub, 0) {
		t.Fatal("first connection: the first list has the task before any read of the transcript")
	}
	waitFor(t, "the first connection's read of the transcript", func() bool { return listed(hub, 0) })
	first := scanOf(0)
	conn.Close()

	hub, conn = connect()
	defer conn.Close()
	if !listed(hub, 1) {
		t.Fatal("second connection: first use of the transcript's meta lacks the carried background task")
	}
	if sc := scanOf(1); sc != first {
		t.Fatal("second connection: not the first connection's scan")
	}
}
