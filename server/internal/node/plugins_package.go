package node

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
)

// hubPluginNames are the hub's own in its plugins directory: never a node's, whatever is in them (#1933).
var hubPluginNames = []string{"installed", "data", "staging", "markets", "node-packages"}

func (ph *pluginHost) versionDir(id, version string) string {
	return filepath.Join(ph.dir, id, version)
}
func (ph *pluginHost) dataDir(id string) string { return filepath.Join(ph.dir, id, "data") }

// own makes id's directory, marked as this node's (pluginOwnMark): the only kind sync removes.
func (ph *pluginHost) own(id string) error {
	at := filepath.Join(ph.dir, id)
	if st, err := os.Lstat(at); err == nil && (!st.IsDir() || !ph.owns(id)) {
		return errors.New("extension slot is not owned by this node")
	}
	if err := os.MkdirAll(at, 0o700); err != nil {
		return err
	}
	mark := filepath.Join(at, pluginOwnMark)
	if _, err := os.Stat(mark); err == nil {
		return nil
	}
	return os.WriteFile(mark, []byte("plugin "+id+": a program the hub runs on this computer (node/plugins.go)\n"), 0o600)
}

// owns says whether id's directory is one this node made (not the hub's, nor anything else that is there).
func (ph *pluginHost) owns(id string) bool {
	if slices.Contains(hubPluginNames, id) {
		return false
	}
	base, err := os.Lstat(filepath.Join(ph.dir, id))
	if err != nil || !base.IsDir() {
		return false
	}
	st, err := os.Lstat(filepath.Join(ph.dir, id, pluginOwnMark))
	return err == nil && st.Mode().IsRegular()
}

// has: the package of that version is here, and is the one with that digest.
func (ph *pluginHost) has(p proto.PluginNodeProgram) bool {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if ph.checkIdentity(p.ID, p.StorageKey(), p.Revision) != nil {
		return false
	}
	if checkProgram(p) != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(ph.versionDir(p.StorageKey(), p.Version), ".persona-digest"))
	return err == nil && strings.TrimSpace(string(b)) == p.Digest
}

func (ph *pluginHost) begin(p proto.PluginNodePutBegin) (string, error) {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if err := ph.checkIdentity(p.ID, p.StorageKey(), p.Revision); err != nil {
		return "", err
	}
	if err := checkProgram(p.PluginNodeProgram); err != nil {
		return "", err
	}
	if p.Digest == "" || p.Size <= 0 || p.Size > extension.PackageMost {
		return "", fmt.Errorf("plugin %s: a package of %d bytes with a digest", p.ID, p.Size)
	}
	if err := os.MkdirAll(filepath.Join(ph.dir, ".uploads"), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Join(ph.dir, ".uploads"), p.ID+"-*.tgz")
	if err != nil {
		return "", err
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	ph.mu.Lock()
	defer ph.mu.Unlock()
	ph.dropStaleLocked()
	ph.uploads[id] = &pluginUpload{prog: p.PluginNodeProgram, size: p.Size, file: f, sum: sha256.New(), at: ph.now()}
	return id, nil
}

// dropStaleLocked forgets uploads nobody went on with (a hub that went away halfway).
func (ph *pluginHost) dropStaleLocked() {
	for id, u := range ph.uploads {
		if ph.now().Sub(u.at) > pluginUploadTTL {
			u.file.Close()
			_ = os.Remove(u.file.Name())
			delete(ph.uploads, id)
		}
	}
}

func (ph *pluginHost) chunk(p proto.PluginNodePutChunk) error {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	u := ph.uploads[p.UploadID]
	switch {
	case u == nil:
		return errors.New("no such upload")
	case p.Offset != u.offset:
		return fmt.Errorf("chunk at %d, expected %d", p.Offset, u.offset)
	case u.offset+int64(len(p.Data)) > u.size:
		return errors.New("more than the package's size")
	}
	if _, err := u.file.Write(p.Data); err != nil {
		return err
	}
	u.sum.Write(p.Data)
	u.offset += int64(len(p.Data))
	u.at = ph.now()
	return nil
}

func (ph *pluginHost) commit(uploadID string) error {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	ph.mu.Lock()
	u := ph.uploads[uploadID]
	delete(ph.uploads, uploadID)
	ph.mu.Unlock()
	if u == nil {
		return errors.New("no such upload")
	}
	defer os.Remove(u.file.Name())
	u.file.Close()
	if err := ph.checkIdentity(u.prog.ID, u.prog.StorageKey(), u.prog.Revision); err != nil {
		return err
	}
	if u.offset != u.size {
		return fmt.Errorf("plugin %s: %d of %d bytes", u.prog.ID, u.offset, u.size)
	}
	if got := hex.EncodeToString(u.sum.Sum(nil)); got != u.prog.Digest {
		return fmt.Errorf("plugin %s: the package's sha256 is %s, not %s", u.prog.ID, got, u.prog.Digest)
	}
	if err := ph.own(u.prog.StorageKey()); err != nil {
		return err
	}
	dir := ph.versionDir(u.prog.StorageKey(), u.prog.Version)
	tmp := dir + ".unpacking"
	_ = os.RemoveAll(tmp)
	if err := extension.UnpackNodePackage(u.file.Name(), tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("plugin %s: %w", u.prog.ID, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".persona-digest"), []byte(u.prog.Digest+"\n"), 0o644); err != nil {
		return err
	}
	ph.mu.Lock()
	r := ph.running[u.prog.ID]
	busy := r != nil && r.spec.Version == u.prog.Version
	ph.mu.Unlock()
	if busy { // the same version with other bytes (a package rebuilt): stop it before its files change under it
		ph.stop(u.prog.ID)
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}
