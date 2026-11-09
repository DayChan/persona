package update

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Manifest is latest.json: the release the publisher put up (docs/operations/hub-update.md "Release manifest and signature").
type Manifest struct {
	Version     string          `json:"version"`
	Build       int64           `json:"build"`
	Commit      string          `json:"commit"`
	Released    time.Time       `json:"released"`
	MinAppBuild int             `json:"min_app_build,omitempty"`
	Protocol    int             `json:"protocol"`
	Notes       string          `json:"notes,omitempty"`
	Files       map[string]File `json:"files"` // "linux-amd64" -> its download
	// Ancestors is every commit the release's code contains (EncodeAncestors of git rev-list <commit>): a hub installs
	// it by itself only when its own commit is one of them (Build.Hold).
	Ancestors string `json:"ancestors,omitempty"`
	// Programs are the programs released with personad that the hub runs beside it (persona-hub and persona-manager): name ->
	// "linux-amd64" -> its download. The hub swaps them with personad, and goes back with it. Older hubs ignore them.
	Programs map[string]map[string]File `json:"programs,omitempty"`
	// Nodes are the other programs a computer's node can run (NodePrograms: persona-node, the device's own): name ->
	// "linux-amd64" -> its download. A manager sends one to a node that runs it (docs/operations/node-update.md); personad's own
	// downloads (Files) run a node too. Older hubs ignore them.
	Nodes map[string]map[string]File `json:"nodes,omitempty"`
	// NodeProtocol, NodeProtocolMin and NodeCapabilities are what this release's nodes announce in node.info
	// (internal/nodeproto, docs/protocol/protocol.md "Node handshake"): a manager of another build sends one only when it
	// drives such a node. Releases from before leave them out (0: unknown, never taken in another build's place).
	NodeProtocol     int      `json:"node_protocol,omitempty"`
	NodeProtocolMin  int      `json:"node_protocol_min,omitempty"`
	NodeCapabilities []string `json:"node_capabilities,omitempty"`
}

// NodePrograms are the programs in Manifest.Nodes, each built from server/cmd/<name>.
var NodePrograms = []string{"persona-node"}

// ProgramNames are the programs released with personad, each built from server/cmd/<name>: the
// hub's and the manager's own programs (personad runs the same code: docs/server/server.md "Programs").
var ProgramNames = []string{"persona-hub", "persona-manager"}

// SplitPrograms are the ProgramNames named persona-* (no legacy name): their artifacts are <name>-<os>-<arch>.gz. A
// service can run one in personad's place, so a release without one is not always installed (internal/hub unreplaced).
var SplitPrograms = []string{"persona-hub", "persona-manager"}

// program names the program a download belongs to ("persona-manager-linux-amd64.gz" -> "persona-manager"), "" for
// personad's own.
func program(gz string) string {
	prog, _ := artifactIdentity(gz)
	return prog
}

// ancestorBytes is how much of each commit id Ancestors keeps: 6 bytes, 12 hex digits. A few KB for a thousand
// commits; a commit that is not there matches one by chance about once in 10^10 lookups at ten thousand.
const ancestorBytes = 6

var errNoAncestors = errors.New("the manifest lists no commits")

// EncodeAncestors packs commit ids (hex, as git rev-list prints them) for Manifest.Ancestors: each one's first 6
// bytes, sorted, base64.
func EncodeAncestors(commits []string) (string, error) {
	seen := map[string]bool{}
	var keys []string
	for _, c := range commits {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		k, err := commitKey(c)
		if err != nil {
			return "", err
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "", errNoAncestors
	}
	sort.Strings(keys)
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(keys, ""))), nil
}

// commitKey is the first 6 bytes of a hex commit id.
func commitKey(c string) (string, error) {
	if len(c) < 2*ancestorBytes {
		return "", fmt.Errorf("commit %q is too short to look up", c)
	}
	b, err := hex.DecodeString(c[:2*ancestorBytes])
	if err != nil {
		return "", fmt.Errorf("commit %q is not hex", c)
	}
	return string(b), nil
}

// Contains says whether the release's code includes commit: Ancestors lists it.
func (m *Manifest) Contains(commit string) (bool, error) {
	if m.Ancestors == "" {
		return false, errNoAncestors
	}
	raw, err := base64.StdEncoding.DecodeString(m.Ancestors)
	if err != nil || len(raw)%ancestorBytes != 0 {
		return false, errors.New("the manifest's list of commits does not read")
	}
	k, err := commitKey(commit)
	if err != nil {
		return false, err
	}
	at := func(i int) string { return string(raw[i*ancestorBytes : (i+1)*ancestorBytes]) }
	n := len(raw) / ancestorBytes
	i := sort.Search(n, func(i int) bool { return at(i) >= k })
	return i < n && at(i) == k, nil
}

// File is one platform's download.
type File struct {
	Name         string `json:"name"`   // personad-linux-amd64.gz, next to latest.json
	Size         int64  `json:"size"`   // of the .gz
	SHA256       string `json:"sha256"` // of the .gz
	BinarySHA256 string `json:"binary_sha256"`
}

// ParseManifest reads latest.json.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("latest.json: %w", err)
	}
	if m.Build <= 0 || m.Version == "" || len(m.Files) == 0 {
		return nil, fmt.Errorf("latest.json is missing its version, build or files")
	}
	compatiblePrograms(&m)
	return &m, nil
}

// BuildManifest describes the downloads in dir: every personad-<os>-<arch>.gz there, every program's
// <program>-<os>-<arch>.gz and every node program's (persona-node-<os>-<arch>.gz), with its hashes and the hash of
// what it unpacks to.
func BuildManifest(dir string, m Manifest) (*Manifest, error) {
	gzs, err := manifestArtifacts(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range NodePrograms {
		more, err := filepath.Glob(filepath.Join(dir, p+"-*-*.gz"))
		if err != nil {
			return nil, err
		}
		for _, gz := range more {
			f, err := describe(gz)
			if err != nil {
				return nil, err
			}
			if m.Nodes == nil {
				m.Nodes = map[string]map[string]File{}
			}
			if m.Nodes[p] == nil {
				m.Nodes[p] = map[string]File{}
			}
			m.Nodes[p][strings.TrimSuffix(strings.TrimPrefix(filepath.Base(gz), p+"-"), ".gz")] = f
		}
	}
	m.Files = map[string]File{}
	for _, p := range gzs {
		base := filepath.Base(p)
		prog, platform := artifactIdentity(base)
		// Compatibility parsing recognizes retired programs, but new releases never publish them.
		if prog != "" && !slices.Contains(ProgramNames, prog) {
			continue
		}
		f, err := describe(p)
		if err != nil {
			return nil, err
		}
		if prog != "" {
			if m.Programs == nil {
				m.Programs = map[string]map[string]File{}
			}
			if m.Programs[prog] == nil {
				m.Programs[prog] = map[string]File{}
			}
			m.Programs[prog][platform] = f
			continue
		}
		m.Files[platform] = f
	}
	if len(m.Files) == 0 {
		return nil, fmt.Errorf("no personad-*.gz in %s", dir)
	}
	compatiblePrograms(&m)
	return &m, nil
}

func describe(gz string) (File, error) {
	f, err := os.Open(gz)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	outer := sha256.New()
	n, err := io.Copy(outer, f)
	if err != nil {
		return File{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return File{}, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", gz, err)
	}
	inner := sha256.New()
	if _, err := io.Copy(inner, zr); err != nil {
		return File{}, fmt.Errorf("%s: %w", gz, err)
	}
	return File{Name: filepath.Base(gz), Size: n, SHA256: hex.EncodeToString(outer.Sum(nil)), BinarySHA256: hex.EncodeToString(inner.Sum(nil))}, nil
}

// CheckNext is publish.sh's check before a release: its build number must be larger than the one served, and the
// served release's commit an ancestor of this one (isAncestor runs git), or a release from an older branch would
// read as newer. Nothing served yet (served nil) passes.
func CheckNext(served *Manifest, build int64, commit string, isAncestor func(older, newer string) (bool, error)) error {
	if served == nil {
		return nil
	}
	if build <= served.Build {
		return fmt.Errorf("build %d is not newer than the one served (%d): is this machine's clock right?", build, served.Build)
	}
	if served.Commit == "" || served.Commit == commit {
		return nil
	}
	ok, err := isAncestor(served.Commit, commit)
	if err != nil {
		return fmt.Errorf("cannot tell whether the served release's commit %.12s comes before this one (git fetch?): %w", served.Commit, err)
	}
	if !ok {
		return fmt.Errorf("the served release's commit %.12s is not an ancestor of %.12s: this would publish an older branch as newer", served.Commit, commit)
	}
	return nil
}

// NodeFile is the download of a node that runs program on plat ("linux-amd64"): program's own (Nodes) when the
// release has it, else personad's (personad runs every node). program is "" or personad for personad's.
func (m *Manifest) NodeFile(program, plat string) (File, bool) {
	if f, ok := m.Nodes[program][plat]; ok && program != "" {
		return f, true
	}
	f, ok := m.Files[plat]
	return f, ok
}
