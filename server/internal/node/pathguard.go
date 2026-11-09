package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// What a client may look at by a path a conversation names (fs.stat, file.read, fs.list, downloads): anything the
// user can read, except where looking hangs the machine (iCloud Drive on a locked Mac) or hands out a secret (ssh
// keys, the hub's token). A path is checked one component at a time as its links are followed, before each is
// looked at, so a link elsewhere that points in is refused without touching what it points to.

var (
	errICloud    = errors.New("iCloud Drive files are not readable remotely")
	errOffLimits = errors.New("this path holds secrets and is not readable remotely")
)

// maxLinks bounds the links one path may go through (a loop of links ends here).
const maxLinks = 40

// secretDirs are folders under the home that hold credentials, refused whole.
var secretDirs = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".password-store",
	"Library/Keychains", ".local/share/keyrings",
	".config/gcloud", ".config/gh",
}

// secretFiles are files under the home that are credentials.
var secretFiles = []string{
	".netrc", ".git-credentials", ".npmrc", ".pypirc", ".pgpass",
	".claude/.credentials.json", ".codex/auth.json", ".claude.json", ".claude-custom-oauth.json",
}

// keyNames are private keys by their usual names, wherever they are.
var keyNames = map[string]bool{"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true, "id_ecdsa_sk": true, "id_ed25519_sk": true}

// hubKeyExt matches key files in the hub's folder, including backup suffixes. The list of the data folder's secrets is
// shared with the agent sandbox (identity/secrets.go).
var hubKeyExt = lazyre.New(identity.DataKeyPattern)

// procSecrets are what Linux's /proc holds besides the processes' own folders that hands out a secret: the kernel's
// memory, its log, its symbols' addresses, the keyrings' entries and the page tables (#4172).
var procSecrets = []string{"kcore", "kmem", "kmsg", "kallsyms", "keys", "key-users", "kpagecount", "kpageflags", "kpagecgroup"}

// sysSecretDirs are folders in Linux's /sys refused whole: firmware variables, kernel debugging, tracing and security
// policy.
var sysSecretDirs = []string{"firmware", "kernel/debug", "kernel/security", "kernel/tracing"}

// fold compares paths as macOS's default file system does: case-insensitively.
func fold(p string) string { return strings.ToLower(filepath.ToSlash(p)) }

// inICloud: iCloud Drive hangs on a locked Mac; never touch it.
func inICloud(path string) bool { return strings.Contains(fold(path), "/library/mobile documents") }

// offLimits says why a path (absolute, clean, links already followed up to it) is not to be looked at, or nil.
func (n *Node) offLimits(path string) error {
	if inICloud(path) {
		return errICloud
	}
	p := fold(path)
	if keyNames[filepath.Base(p)] || pseudoFSSecret(p) {
		return errOffLimits
	}
	// The hub's, the node's and the managers' own folders: the credentials in them and the keys beside them (secret.key,
	// handles.key, hub.key, the APNs .p8), wherever PERSONA_HOME puts the folder.
	if slices.ContainsFunc(n.inDataDirs(p), dataSecret) {
		return errOffLimits
	}
	// Agent credentials in every Claude Code config directory and Codex home the node knows (agentsecrets.go).
	if n.agentSecret(path) {
		return errOffLimits
	}

	if n.home == "" {
		return nil
	}
	rel, ok := homeRel(p, n.home)
	if !ok {
		return nil
	}
	for _, d := range secretDirs {
		if d = strings.ToLower(d); rel == d || strings.HasPrefix(rel, d+"/") {
			return errOffLimits
		}
	}
	for _, f := range secretFiles {
		if rel == strings.ToLower(f) {
			return errOffLimits
		}
	}
	return nil
}

// dataSecret says whether rel, a folded path in a data folder, is one of its credentials. They are matched only where
// they are written: directly in the folder or in a manager's (managers/<id>/), or anywhere in a folder that holds
// nothing else. Not at any depth: with PERSONA_HOME=~ the data folder is the home, and a project's link.json or
// certs/dev.pem below it is the project's own.
func dataSecret(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) > 2 && parts[0] == "managers" {
		parts = parts[2:]
	}
	// A secret folder, and anything in it: the folder itself too (its listing names what is in it).
	if slices.ContainsFunc(identity.DataSecretDirs, func(dir string) bool {
		d := strings.Split(dir, "/")
		return len(parts) >= len(d) && slices.Equal(parts[:len(d)], d)
	}) {
		return true
	}
	if len(parts) > 1 {
		return false
	}
	base := parts[0]
	if hubKeyExt.MatchString(base) {
		return true
	}
	for _, name := range identity.DataSecrets {
		if strings.HasPrefix(base, name) {
			return true
		}
	}
	return false
}

// offLimitsResolved is offLimits for a path whose folders are real ones (links already followed up to its last
// component: realPath's, a walk's, a write's), with what only such a path may be checked for, reading a file beside
// it: a manager's own folder wherever its --dir put it (#5877), whose credentials are refused as a data folder's are.
// A path as asked is checked by name only (offLimits), so no link is followed before it is.
func (n *Node) offLimitsResolved(path string) error {
	if err := n.offLimits(path); err != nil {
		return err
	}
	if inManagerDir(path) {
		return errOffLimits
	}
	return nil
}

// inManagerDir says whether path is one of a data folder's credentials, as a manager's folder holds them: one named
// as a credential (dataSecret) in a folder that holds a manager's manager.json, or a folder refused whole
// (launcher-settings/, tls/, link-tls/) of one, or anything in it (the folder itself too: a link of that name is not
// followed). Only those folders are looked at, so other paths cost no file access.
func inManagerDir(path string) bool {
	if dataSecret(fold(filepath.Base(path))) && isManagerDir(filepath.Dir(path)) {
		return true
	}
	for d := path; d != filepath.Dir(d); d = filepath.Dir(d) {
		if root, ok := secretDirOf(d); ok && isManagerDir(root) {
			return true
		}
	}
	return false
}

// secretBehind says whether the link at next (its folder real) stands where a secret folder, or a folder on the way to
// one, is named (tls, or extensions of extensions/data): what is behind it would be refused by name there, and
// followed, it would not be.
func (n *Node) secretBehind(next string) bool {
	base := fold(filepath.Base(next))
	for _, dir := range identity.DataSecretDirs {
		names := strings.Split(dir, "/")
		for i, name := range names {
			if name == base {
				probe := filepath.Join(append(append([]string{next}, names[i+1:]...), "x")...)
				if errors.Is(n.offLimitsResolved(probe), errOffLimits) {
					return true
				}
			}
		}
	}
	return false
}

// secretDirOf says whether the folder d is one of identity.DataSecretDirs by its last components (extensions/data:
// its last two), and the folder that holds it.
func secretDirOf(d string) (string, bool) {
	for _, dir := range identity.DataSecretDirs {
		root := d
		names := strings.Split(dir, "/")
		matched := true
		for i := len(names) - 1; i >= 0; i-- {
			if fold(filepath.Base(root)) != names[i] || root == filepath.Dir(root) {
				matched = false
				break
			}
			root = filepath.Dir(root)
		}
		if matched {
			return root, true
		}
	}
	return "", false
}

// isManagerDir says whether dir holds a manager's manager.json: a plain file naming its hub and its token, or one that
// does not read but may hold a token (a hand edit cut short). A project's own manager.json (one that reads without
// them, an empty one, a list) is not one. A link of that name is not followed and counts as one, as does a file only
// a placeholder of which is here (iCloud's Desktop & Documents, OneDrive): opening it would fetch it. A FIFO, a
// socket or a device is never one, and the file is opened without waiting on it or following a link.
func isManagerDir(dir string) bool {
	p := filepath.Join(dir, "manager.json")
	st, err := os.Lstat(p)
	switch {
	case err != nil:
		return false
	case st.Mode()&fs.ModeSymlink != 0:
		return true
	case st.Mode()&(fs.ModeDir|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice|fs.ModeCharDevice) != 0:
		return false
	case !st.Mode().IsRegular() || dataless(st):
		return true
	}
	f, err := openNoBlock(p)
	if err != nil {
		return true // there, a plain file, and not readable here: as a manager's, to be safe
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return false
	} else if st.Size() > 1<<20 {
		return true // more than is read here: its token may be past it
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return true
	}
	var m struct {
		Hub   string `json:"hub"`
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &m) != nil {
		return bytes.Contains(b, []byte(`"token"`))
	}
	return m.Hub != "" && m.Token != ""
}

// pseudoFSSecret says whether the folded path p is a secret Linux's /proc or /sys makes up as it is read (#4172).
// Every process's own folder is refused whole (/proc/<pid>, self, thread-self): its environment (API keys and tokens,
// the hub's and the agents' own among them), command line, memory, maps and open files, and root and cwd, which lead
// back into the whole file system as that process sees it. Files about the machine (cpuinfo, meminfo, loadavg, net/…)
// stay readable.
func pseudoFSSecret(p string) bool {
	if rest, ok := strings.CutPrefix(p, "/proc/"); ok {
		first, _, _ := strings.Cut(rest, "/")
		return first == "self" || first == "thread-self" || isPID(first) || slices.Contains(procSecrets, first)
	}
	if rest, ok := strings.CutPrefix(p, "/sys/"); ok {
		for _, d := range sysSecretDirs {
			if rest == d || strings.HasPrefix(rest, d+"/") {
				return true
			}
		}
	}
	return false
}

// isPID: a /proc entry named by a process id.
func isPID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// inDataDirs are where the folded path p is in each data folder it is in: one named .persona or .persona anywhere
// (this machine's, another user's, a copy), and the one PERSONA_HOME names for this node (identity.DataDir), as
// written or with its links followed. Every one is kept, not the first: a data folder PERSONA_HOME puts below a
// .persona folder (~/.persona/inst2) has its credentials where they are written in it, not one level below .persona.
func (n *Node) inDataDirs(p string) []string {
	var rels []string
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if compat.IsStateDir(part) {
			rels = append(rels, strings.Join(parts[i+1:], "/"))
		}
	}
	for _, dir := range n.dataDirs() {
		if rel, ok := strings.CutPrefix(p, dir+"/"); ok {
			rels = append(rels, rel)
		}
	}
	return rels
}

// dataDirs are this node's data folder (folded), as written and with its links followed: looked up once, not on
// every component of every path a client asks about. None without a home or PERSONA_HOME to find it by.
func (n *Node) dataDirs() []string {
	n.dataDirOnce.Do(func() {
		dir := identity.DataDir(n.home)
		if !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		n.dataDirList = []string{fold(dir)}
		if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
			n.dataDirList = append(n.dataDirList, fold(real))
		}
	})
	return n.dataDirList
}

// homeRel is the folded path p relative to home, home as written or with its links followed: realPath hands over
// the physical path, so a home behind a link (/var -> /private/var) is matched both ways.
func homeRel(p, home string) (string, bool) {
	home = filepath.Clean(home)
	if rel, ok := strings.CutPrefix(p, fold(home)+"/"); ok {
		return rel, true
	}
	if real, err := filepath.EvalSymlinks(home); err == nil && real != home {
		return strings.CutPrefix(p, fold(real)+"/")
	}
	return "", false
}

// realPath follows the links in an absolute, clean path one component at a time, checking each step with offLimits
// before looking at it. A component that is not there ends the walk: the rest is joined on as written, and the error
// is the one looking at it gave (fs.ErrNotExist, fs.ErrPermission).
func (n *Node) realPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path is not absolute: %s", path)
	}
	if err := n.offLimits(path); err != nil {
		return "", err
	}
	vol := filepath.VolumeName(path)
	done := vol + string(filepath.Separator)
	pending := splitPath(path[len(vol):])
	links := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			done = filepath.Dir(done)
			continue
		}
		next := filepath.Join(done, c)
		if err := n.offLimitsResolved(next); err != nil {
			return "", err
		}
		st, err := os.Lstat(next)
		if err != nil {
			// What is left is not there to follow: as named, it is still not a manager's folder's credential (a file
			// a Git panel reads from a commit, gone from the working tree).
			// A remainder that climbs (..) could lead anywhere once cleaned, so only one below next is looked at.
			rest := filepath.Join(append([]string{next}, pending...)...)
			if !slices.Contains(pending, "..") {
				if err := n.offLimitsResolved(rest); err != nil {
					return "", err
				}
			}
			return rest, err
		}
		if st.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}
		if n.secretBehind(next) {
			return "", errOffLimits
		}
		if links++; links > maxLinks {
			return "", fmt.Errorf("too many links: %s", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			v := filepath.VolumeName(target)
			done, target = v+string(filepath.Separator), target[len(v):]
		}
		pending = append(splitPath(target), pending...)
	}
	return done, nil
}

func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator })
}

// absPath makes a path absolute against the node's own working directory (a relative path asked with no cwd).
func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// statSafe is os.Stat through realPath; path is how it was asked (resolved, before links).
func (n *Node) statSafe(path string) (string, os.FileInfo, error) {
	real, err := n.realPath(absPath(path))
	if err != nil {
		return "", nil, err
	}
	st, err := os.Stat(real)
	return real, st, err
}
