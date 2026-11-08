//go:build linux

package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestProcShortChild(t *testing.T) {
	if os.Getenv("HOST_SHORT_CHILD") == "" {
		return
	}
	c, err := net.DialTimeout("unix", os.Getenv("HOST_SHORT_CONTROL"), 3*time.Second)
	if err != nil {
		os.Exit(2)
	}
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(c, "ready %d\n", os.Getpid())
	input := bufio.NewReader(os.Stdin)
	line, err := input.ReadBytes('\n')
	if err != nil {
		os.Exit(3)
	}
	var init struct{ ID, Method string }
	if json.Unmarshal(line, &init) != nil || init.Method != extension.MInitialize {
		os.Exit(4)
	}
	result, _ := json.Marshal(extension.InitializeResult{Protocol: extension.Protocol, ID: "demo", Version: "1.0.0"})
	fmt.Fprintf(os.Stdout, "{\"id\":%q,\"result\":%s}\n", init.ID, result)
	control := bufio.NewScanner(c)
	for control.Scan() {
		if control.Text() != "read" {
			os.Exit(5)
		}
		frame, readErr := input.ReadBytes('\n')
		status := "valid"
		if readErr != nil {
			status = "truncated"
		} else if !json.Valid(frame) {
			status = "invalid"
		}
		fmt.Fprintln(c, status)
	}
	os.Exit(0)
}

type procShortWriter struct {
	w       io.Writer
	partial bool
	err     error
	writes  atomic.Int32
}

func (w *procShortWriter) Write(b []byte) (int, error) {
	if w.writes.Add(1) == 1 {
		if w.partial {
			b = b[:3]
		}
		n, err := w.w.Write(b)
		if err != nil {
			return n, err
		}
		return n, w.err
	}
	return w.w.Write(b)
}

func TestProcShortWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial bool
		err     error
	}{
		{"short-nil", true, nil}, {"partial-error", true, io.ErrUnexpectedEOF}, {"full-error", false, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "short-host-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			ln, err := net.Listen("unix", filepath.Join(dir, "control"))
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
			s := spec(t, "")
			s.Startup = true
			s.CleanEnv = true
			s.Args = []string{"-test.run=^TestProcShortChild$"}
			s.Env = []string{"HOST_TEST_PLUGIN=", "HOST_SHORT_CHILD=1", "HOST_SHORT_CONTROL=" + filepath.Join(dir, "control")}
			p := Start(context.Background(), s)
			t.Cleanup(func() {
				p.cancel()
				stopped := make(chan struct{})
				go func() { p.Stop(); close(stopped) }()
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					t.Error("TIMEOUT owned short-write child reap")
				}
			})
			c, err := ln.Accept()
			if err != nil {
				t.Fatal("TIMEOUT child readiness", err)
			}
			t.Cleanup(func() { c.Close() })
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			br := bufio.NewReader(c)
			ready, err := br.ReadString('\n')
			var childPID int
			if err != nil {
				t.Fatal("TIMEOUT ready handshake", err)
			}
			if _, err := fmt.Sscanf(ready, "ready %d", &childPID); err != nil || childPID <= 0 {
				t.Fatal("FAIL child identity")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := p.Wait(ctx); err != nil {
				t.Fatal("FAIL real Proc initialize", err)
			}
			p.mu.Lock()
			peer := p.sess.peer
			p.mu.Unlock()
			// No outgoing call, handler or ping remains active after initialization in
			// this fixture. Inject only at the writer boundary, retaining the real owned
			// stdin and child. Do not add a production transport setter just for tests.
			field := reflect.ValueOf(peer).Elem().FieldByName("w")
			slot := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
			writer := &procShortWriter{w: slot.Interface().(io.Writer), partial: tc.partial, err: tc.err}
			slot.Set(reflect.ValueOf(writer))
			first := peer.Notify("short", nil)
			wantErr := tc.err
			if wantErr == nil {
				wantErr = io.ErrShortWrite
			}
			if !errors.Is(first, wantErr) || !errors.Is(first, rpc.ErrDropped) {
				t.Fatal("FAIL failed frame identity/ambiguity", first)
			}
			if err := peer.Notify("next", nil); !errors.Is(err, rpc.ErrClosed) {
				t.Fatal("FAIL retired stream accepted next frame", err)
			}
			if writer.writes.Load() != 1 {
				t.Fatal("FAIL automatic retry or appended frame")
			}
			if _, err := fmt.Fprintln(c, "read"); err != nil {
				t.Fatal("FAIL child read handshake", err)
			}
			status, err := br.ReadString('\n')
			if err != nil {
				t.Fatal("TIMEOUT child frame outcome", err)
			}
			want := "valid"
			if tc.partial {
				want = "truncated"
			}
			if status != want+"\n" {
				t.Fatal("FAIL actual child frame outcome", status)
			}
			t.Log("PASS actual Proc retires failed frame before a following message", tc.name)
		})
	}
}
