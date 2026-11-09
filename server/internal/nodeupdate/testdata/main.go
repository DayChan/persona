// The fixture exercises the real updater in a supervised process without any
// platform service manager, network service, or production socket.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/nodeupdate"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

var version = "next"

func main() {
	ctx := context.Background()
	if len(os.Args) > 1 && os.Args[1] == "fixture-hold" {
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "node-update-check" {
		_ = json.NewEncoder(os.Stdout).Encode(nodeupdate.Check{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH})
		return
	}
	if len(os.Args) == 5 && os.Args[1] == "node-update-watch" && os.Args[4] == "--managed-ready" {
		if err := nodeupdate.WatchManaged(ctx, os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "node-update-watch" {
		ready := os.NewFile(3, "ready")
		if err := nodeupdate.Watch(ctx, os.Args[2], os.Args[3], ready); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if os.Getenv("FIXTURE_GATE_VERSION") == version {
		gate := os.Getenv("FIXTURE_BOOT_GATE")
		_ = os.WriteFile(gate+".loaded", []byte(version), 0600)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(9)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if os.Getenv("FIXTURE_CRASH") == version {
		os.Exit(7)
	}
	restart := make(chan struct{})
	// `--supervised` among the arguments stands for a supervisor other than systemd or launchd, as the real node's
	// `node --attach --supervised` does.
	m := nodeupdate.New(version, func() { close(restart) }, nodeupdate.Options{Supervised: slices.Contains(os.Args[1:], "--supervised")})
	defer m.Close()
	if m.RestartRequired() {
		mustExec(m.Executable())
	}
	cwd, _ := os.Getwd()
	proof, _ := json.Marshal(map[string]any{"version": version, "pid": os.Getpid(), "args": os.Args, "cwd": cwd, "home": os.Getenv("HOME"), "root": os.Getenv("PERSONA_HOME"), "sentinel": os.Getenv("FIXTURE_SENTINEL")})
	path := filepath.Join(os.Getenv("PERSONA_HOME"), "fixture.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		panic(err)
	}
	_ = os.WriteFile(os.Getenv("FIXTURE_PROOF"), proof, 0600)
	go func() { <-restart; _ = ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			break
		}
		peer := rpc.New("fixture", c)
		peer.OnRequest = func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
			switch method {
			case "begin":
				var p proto.NodeUpdateBeginParams
				_ = json.Unmarshal(raw, &p)
				return m.Begin(p)
			case "chunk":
				var p proto.NodeUpdateChunkParams
				_ = json.Unmarshal(raw, &p)
				return m.Chunk(p)
			case "commit":
				var p proto.NodeUpdateCommitParams
				_ = json.Unmarshal(raw, &p)
				result, err := m.Commit(ctx, p.ID, nil)
				// The fixture's peer writes the result; restart is separately requested by
				// the test after it receives that reply, just as the node writer does.
				return result, err
			case "response-written":
				var p proto.NodeUpdateCommitParams
				_ = json.Unmarshal(raw, &p)
				m.ResponseWritten(p.ID)
				return true, nil
			case "confirm":
				var p proto.NodeUpdateConfirmParams
				_ = json.Unmarshal(raw, &p)
				return true, m.Confirm(p.Version)
			}
			return nil, fmt.Errorf("unknown method")
		}
		go func() { _ = peer.Serve(ctx, c); _ = c.Close() }()
	}
	mustExec(m.Executable())
}
func mustExec(path string) {
	if err := syscall.Exec(path, os.Args, os.Environ()); err != nil {
		panic(err)
	}
}
