package update

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeGz(t *testing.T, path string, content []byte) {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(content)
	zw.Close()
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A program released with personad (persona-examplechat-<os>-<arch>.gz) is listed under its name, not taken for a
// personad platform ("examplechat-linux-amd64").
func TestBuildManifestPrograms(t *testing.T) {
	programs, split := ProgramNames, SplitPrograms
	ProgramNames = append(append([]string(nil), programs...), "persona-examplechat")
	SplitPrograms = append(append([]string(nil), split...), "persona-examplechat")
	t.Cleanup(func() { ProgramNames, SplitPrograms = programs, split })
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, "personad-linux-amd64.gz"), []byte("personad"))
	writeGz(t, filepath.Join(dir, "persona-examplechat-linux-amd64.gz"), []byte("persona-examplechat"))
	writeGz(t, filepath.Join(dir, "persona-examplechat-darwin-arm64.gz"), []byte("persona-examplechat for a Mac"))
	writeGz(t, filepath.Join(dir, "persona-hub-linux-amd64.gz"), []byte("persona-hub"))
	writeGz(t, filepath.Join(dir, "persona-manager-linux-amd64.gz"), []byte("persona-manager"))
	writeGz(t, filepath.Join(dir, "persona-node-linux-amd64.gz"), []byte("not released with the hub"))
	m, err := BuildManifest(dir, Manifest{Version: "v1", Build: 202611030000})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files["linux-amd64"].Name != "personad-linux-amd64.gz" {
		t.Fatalf("files %+v", m.Files)
	}
	chat := m.Programs["persona-examplechat"]
	if len(chat) != 2 || chat["linux-amd64"].Name != "persona-examplechat-linux-amd64.gz" || chat["darwin-arm64"].BinarySHA256 == "" {
		t.Fatalf("programs %+v", m.Programs)
	}
	// the hub's and the manager's programs: listed under their names; persona-node is not released with the hub
	for _, prog := range []string{"persona-hub", "persona-manager"} {
		if f := m.Programs[prog]["linux-amd64"]; f.Name != prog+"-linux-amd64.gz" || f.BinarySHA256 == "" {
			t.Fatalf("%s: %+v", prog, m.Programs)
		}
	}
	if _, ok := m.Programs["persona-node"]; ok {
		t.Fatalf("persona-node taken for a program: %+v", m.Programs)
	}
	// Only programs, no personad: not a release.
	only := t.TempDir()
	writeGz(t, filepath.Join(only, "persona-examplechat-linux-amd64.gz"), []byte("x"))
	if _, err := BuildManifest(only, Manifest{Version: "v1", Build: 1}); err == nil {
		t.Fatal("a release without personad")
	}
}

// A node program (persona-node-<os>-<arch>.gz) is listed under Nodes, and NodeFile gives a node that runs it its own
// download, any other node (or a platform without one) personad's; the node protocol fields round-trip.
func TestBuildManifestNodes(t *testing.T) {
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, "personad-linux-amd64.gz"), []byte("personad"))
	writeGz(t, filepath.Join(dir, "personad-darwin-arm64.gz"), []byte("personad for a Mac"))
	writeGz(t, filepath.Join(dir, "persona-node-linux-amd64.gz"), []byte("persona-node"))
	m, err := BuildManifest(dir, Manifest{Version: "v1", Build: 202611030000, NodeProtocol: 2, NodeProtocolMin: 1,
		NodeCapabilities: []string{"sessions"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 || m.Files["linux-amd64"].Name != "personad-linux-amd64.gz" {
		t.Fatalf("files %+v", m.Files)
	}
	if n := m.Nodes["persona-node"]; len(n) != 1 || n["linux-amd64"].Name != "persona-node-linux-amd64.gz" || n["linux-amd64"].BinarySHA256 == "" {
		t.Fatalf("nodes %+v", m.Nodes)
	}
	for _, c := range []struct{ program, plat, want string }{
		{"persona-node", "linux-amd64", "persona-node-linux-amd64.gz"},
		{"persona-node", "darwin-arm64", "personad-darwin-arm64.gz"}, // none of its own there: personad runs it
		{"personad", "linux-amd64", "personad-linux-amd64.gz"}, {"", "linux-amd64", "personad-linux-amd64.gz"},
	} {
		if f, ok := m.NodeFile(c.program, c.plat); !ok || f.Name != c.want {
			t.Errorf("%s on %s: %+v %v, want %s", c.program, c.plat, f, ok, c.want)
		}
	}
	if _, ok := m.NodeFile("persona-node", "windows-amd64"); ok {
		t.Error("a platform the release has no build for")
	}
	b, _ := json.Marshal(m)
	back, err := ParseManifest(b)
	if err != nil || back.NodeProtocol != 2 || back.NodeProtocolMin != 1 || len(back.NodeCapabilities) != 1 || len(back.Nodes) != 1 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

// A program swapped with the hub goes back with it: the old build when there was one, removed when it is new.
func TestSwapProgramAndRestoreAll(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<none>"
		}
		return string(b)
	}
	target := write("personad", "old personad")
	write("personad-old-program", "old program")
	newHub, newProg := write("new-personad", "new personad"), write("new-program", "new program")

	prev, err := Swap(target, newHub)
	if err != nil {
		t.Fatal(err)
	}
	p := Pending{Target: target, Prev: prev}
	had, err := SwapProgram(filepath.Join(dir, "personad-old-program"), newProg)
	if err != nil || had.Prev == "" {
		t.Fatalf("an existing program: %+v %v", had, err)
	}
	fresh, err := SwapProgram(filepath.Join(dir, "personad-new-program"), newProg)
	if err != nil || fresh.Prev != "" {
		t.Fatalf("a new program: %+v %v", fresh, err)
	}
	p.Programs = []Swapped{had, fresh}
	if read("personad") != "new personad" || read("personad-old-program") != "new program" || read("personad-new-program") != "new program" {
		t.Fatal("not swapped")
	}
	if err := RestoreAll(p); err != nil {
		t.Fatal(err)
	}
	if read("personad") != "old personad" || read("personad-old-program") != "old program" || read("personad-new-program") != "<none>" {
		t.Fatalf("went back to %q %q %q", read("personad"), read("personad-old-program"), read("personad-new-program"))
	}
}

// A program whose swap fails is still there after going back: the hub and the programs swapped before it go back,
// the failed one stays as it was.
func TestAFailedSwapLeavesTheProgram(t *testing.T) {
	for _, tc := range []struct {
		name   string
		had    bool
		newBin func(t *testing.T, target string) string
	}{
		{"the new build cannot be read", true, func(t *testing.T, target string) string {
			return filepath.Join(filepath.Dir(target), "gone")
		}},
		{"the copy to go back to cannot be written", true, func(t *testing.T, target string) string {
			if err := os.MkdirAll(filepath.Join(target+".prev", "in-the-way"), 0o755); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(filepath.Dir(target), "new-program")
		}},
		{"a new program whose build cannot be read", false, func(t *testing.T, target string) string {
			return filepath.Join(filepath.Dir(target), "gone")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, s string) string {
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			}
			read := func(name string) string {
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					return "<none>"
				}
				return string(b)
			}
			target := write("personad", "old personad")
			write("personad-first", "old first")
			program := filepath.Join(dir, "personad-second")
			want := "<none>"
			if tc.had {
				want = "old second"
				write("personad-second", want)
			}
			newHub, newProg := write("new-personad", "new personad"), write("new-program", "new program")

			prev, err := Swap(target, newHub)
			if err != nil {
				t.Fatal(err)
			}
			p := Pending{Target: target, Prev: prev}
			first, err := SwapProgram(filepath.Join(dir, "personad-first"), newProg)
			if err != nil {
				t.Fatal(err)
			}
			second, err := SwapProgram(program, tc.newBin(t, program))
			if err == nil {
				t.Fatal("the swap did not fail")
			}
			if read("personad-second") != want {
				t.Fatalf("the failed swap changed the program: %q", read("personad-second"))
			}
			p.Programs = []Swapped{first, second}
			if err := RestoreAll(p); err != nil {
				t.Fatalf("going back (Swapped %+v): %v", second, err)
			}
			if read("personad") != "old personad" || read("personad-first") != "old first" || read("personad-second") != want {
				t.Fatalf("went back to %q %q %q (Swapped %+v)", read("personad"), read("personad-first"), read("personad-second"), second)
			}
		})
	}
}
