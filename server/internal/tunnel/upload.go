package tunnel

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

// The upload port takes a new personad for the node, what the hub's "Redeploy" sends over ssh otherwise (devices/deploy.go):
// one JSON line {"size","sha256"} of the binary, the binary gzipped, then the node answers one JSON line
// {"ok"} or {"error"}. On ok the node has replaced its own executable and exits, and its supervisor (launchd,
// systemd: scripts/deploy/node-launchd.sh) starts the new one. An error can come before the binary has all arrived
// (another upload is going, a bad header), so the hub reads the answer while it sends.

// maxBinary bounds what an upload may write.
const maxBinary = 512 << 20

type uploadHeader struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type uploadReply struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// ServeUpload accepts uploads on ln until it closes, installing each over exe. done runs after a successful one
// (the reply already sent): the node should exit so the new binary runs.
func ServeUpload(ln net.Listener, exe string, done func()) { serveUpload(ln, exe, done, uploadIdle) }

func serveUpload(ln net.Listener, exe string, done func(), idle time.Duration) {
	var mu sync.Mutex // one at a time: two would race on the same file
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			// No total deadline (tens of MB over a slow relay take a while), but one that moves with every read: a hub
			// gone mid-upload (redeployed, restarted) must not hold the upload for long, the next one is turned away.
			if !mu.TryLock() {
				refuse(c, errors.New("another upload is in progress"))
				return
			}
			if err := install(bufio.NewReader(idleReader{c, idle}), exe); err != nil {
				mu.Unlock() // before the linger: a retry is not turned away for it
				refuse(c, err)
				return
			}
			defer mu.Unlock()
			_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
			writeReply(c, nil)
			if done != nil {
				if cw, ok := c.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
				time.Sleep(time.Second) // let the reply leave before the process does
				done()
			}
		}()
	}
}

// uploadIdle: how long an upload may go without a byte arriving.
const uploadIdle = 60 * time.Second

// refuseLinger: how long a refused upload is still read before the connection closes.
const refuseLinger = 5 * time.Second

// refuse answers err, then reads and drops what the hub is still sending for a while: closing with data unread answers
// with a reset, which can destroy the answer before the hub reads it (the hub then sees only a broken pipe).
func refuse(c net.Conn, err error) {
	_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	writeReply(c, err)
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = c.SetReadDeadline(time.Now().Add(refuseLinger))
	_, _ = io.Copy(io.Discard, c)
}

// idleReader moves the connection's read deadline before every read.
type idleReader struct {
	c    net.Conn
	idle time.Duration
}

func (r idleReader) Read(p []byte) (int, error) {
	_ = r.c.SetReadDeadline(time.Now().Add(r.idle))
	return r.c.Read(p)
}

func writeReply(c net.Conn, err error) {
	reply := uploadReply{OK: err == nil}
	if err != nil {
		reply.Error = err.Error()
	}
	b, _ := json.Marshal(reply)
	_, _ = c.Write(append(b, '\n'))
}

func install(r *bufio.Reader, exe string) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	var h uploadHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return fmt.Errorf("header: %w", err)
	}
	if h.Size <= 0 || h.Size > maxBinary {
		return fmt.Errorf("size %d out of range", h.Size)
	}
	want, err := hex.DecodeString(h.SHA256)
	if err != nil || len(want) != sha256.Size {
		return errors.New("bad sha256")
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	zr.Multistream(false) // stop at the end of the binary: the connection stays open for the answer
	tmp := exe + ".new"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // a no-op once renamed
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(zr, h.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("receive: %w", err)
	case n != h.Size:
		return fmt.Errorf("got %d bytes, want %d", n, h.Size)
	case !bytes.Equal(sum.Sum(nil), want):
		return errors.New("sha256 mismatch")
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	// The hub sends the kind of program the node runs (devices/deploy.go nodeBinary); a personad, which may run a hub
	// or a manager too, never becomes persona-node all the same.
	if err := update.CheckReplace(exe, tmp); err != nil {
		return err
	}
	// It must run here (right OS and architecture, not truncated) before it replaces the working one.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, tmp, "version").CombinedOutput(); err != nil {
		return fmt.Errorf("the new binary does not run here: %v %s", err, strings.TrimSpace(string(out)))
	}
	return os.Rename(tmp, exe)
}

// Upload sends the binary at path over c (a connection to UploadPort) and waits for the node's answer.
func Upload(ctx context.Context, c net.Conn, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	// The answer is read from the start: a node that turns the upload away answers at once and stops reading, and the
	// rest of the send fails with a broken pipe or a reset that says nothing of why.
	answers := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err == nil {
			_ = c.SetWriteDeadline(time.Now()) // an answer before the binary is all sent is a refusal: stop sending
		}
		answers <- answer{line, err}
	}()
	defer func() { _ = c.SetReadDeadline(time.Now()) }() // ends that read when no answer came
	if err := send(c, uploadHeader{Size: size, SHA256: hex.EncodeToString(sum.Sum(nil))}, f); err != nil {
		select {
		case a := <-answers:
			if a.err == nil {
				return parseReply(a.line)
			}
		case <-time.After(answerWait):
		}
		return err
	}
	a := <-answers
	if a.err != nil {
		return fmt.Errorf("no answer from the node: %w", a.err)
	}
	return parseReply(a.line)
}

// answerWait: how long a failed send still waits for the node's answer, which says why.
const answerWait = 5 * time.Second

type answer struct {
	line []byte
	err  error
}

// send writes the header line and the gzipped binary.
func send(c net.Conn, h uploadHeader, f io.Reader) error {
	head, _ := json.Marshal(h)
	w := bufio.NewWriter(c)
	if _, err := w.Write(append(head, '\n')); err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if _, err := io.Copy(zw, f); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

func parseReply(line []byte) error {
	var reply uploadReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("answer: %w", err)
	}
	if !reply.OK {
		return errors.New(reply.Error)
	}
	return nil
}

// Executable is the file a running personad was started from, symlinks resolved: what an upload replaces.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
