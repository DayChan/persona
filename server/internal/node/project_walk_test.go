package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// globProjectFiles is what walkProjects stands in for: the two globs the usage meter ran over a projects directory.
func globProjectFiles(t testing.TB, root string) (mains, subs []string) {
	t.Helper()
	mains, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	subs, err = filepath.Glob(filepath.Join(root, "*", "*", "subagents", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return mains, subs
}

func requireWalkMatchesGlob(t testing.TB, root string) {
	t.Helper()
	wantMains, wantSubs := globProjectFiles(t, root)
	mains, subs := walkProjects(root)
	if !slices.Equal(mains, wantMains) {
		t.Fatalf("transcripts differ from the glob's\n got %q\nwant %q", mains, wantMains)
	}
	if !slices.Equal(subs, wantSubs) {
		t.Fatalf("subagent transcripts differ from the glob's\n got %q\nwant %q", subs, wantSubs)
	}
}

// The walk lists exactly what the two globs list, in their order, across what a projects directory can hold: hidden
// names, a directory named like a transcript (listed, and walked into), a transcript or a whole project reached
// through a symlink, dangling links, a file where a directory is expected (a project, a session folder, subagents),
// names with glob metacharacters, and a directory that cannot be read.
func TestWalkProjectsMatchesGlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	elsewhere := t.TempDir()
	mkdir := func(p ...string) string {
		t.Helper()
		d := filepath.Join(append([]string{root}, p...)...)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		t.Helper()
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks: %v", err)
		}
	}
	proj := mkdir("-work-app")
	write(filepath.Join(proj, "0e000000-0000-4000-8000-000000000001.jsonl"))
	write(filepath.Join(proj, ".hidden.jsonl"))
	write(filepath.Join(proj, ".jsonl"))
	write(filepath.Join(proj, "notes.txt"))
	write(filepath.Join(proj, "x[1]*?.jsonl"))
	write(filepath.Join(proj, "0e000000-0000-4000-8000-000000000001", "subagents", "agent-b.jsonl"))
	write(filepath.Join(proj, "0e000000-0000-4000-8000-000000000001", "subagents", "agent-a.jsonl"))
	write(filepath.Join(proj, "0e000000-0000-4000-8000-000000000001", "subagents", "agent-a.meta.json"))
	write(filepath.Join(proj, "0e000000-0000-4000-8000-000000000001", "tool-results", "r.jsonl"))
	write(filepath.Join(proj, "dir.jsonl", "subagents", "agent-c.jsonl")) // a directory named like a transcript
	mkdir("-work-app", "0e000000-0000-4000-8000-000000000002", "subagents", "nested.jsonl")
	write(filepath.Join(proj, "file-session"))
	mkdir("-work-app", "subagents-as-file")
	write(filepath.Join(proj, "subagents-as-file", "subagents"))
	write(filepath.Join(elsewhere, "agents", "agent-l.jsonl"))
	mkdir("-work-app", "linked-subagents")
	link(filepath.Join(elsewhere, "agents"), filepath.Join(proj, "linked-subagents", "subagents"))
	write(filepath.Join(elsewhere, "session", "subagents", "agent-s.jsonl"))
	link(filepath.Join(elsewhere, "session"), filepath.Join(proj, "linked-session"))
	write(filepath.Join(elsewhere, "t.jsonl"))
	link(filepath.Join(elsewhere, "t.jsonl"), filepath.Join(proj, "linked.jsonl"))
	link(filepath.Join(elsewhere, "missing.jsonl"), filepath.Join(proj, "dangling.jsonl"))
	write(filepath.Join(elsewhere, "project", "p.jsonl"))
	write(filepath.Join(elsewhere, "project", "s", "subagents", "agent-p.jsonl"))
	link(filepath.Join(elsewhere, "project"), filepath.Join(root, "-linked-project"))
	link(filepath.Join(elsewhere, "missing"), filepath.Join(root, "-dangling-project"))
	link(filepath.Join(elsewhere, "t.jsonl"), filepath.Join(root, "-file-project"))
	link(root, filepath.Join(proj, "loop")) // back up to the root: both stop at the patterns' depth
	write(filepath.Join(root, "stray.jsonl"))
	write(filepath.Join(root, ".hidden-project", "h.jsonl"))
	write(filepath.Join(root, "-work-[x]", "m.jsonl"))
	write(filepath.Join(root, "-work-é", "é.jsonl"))
	locked := mkdir("-locked")
	write(filepath.Join(locked, "l.jsonl"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	requireWalkMatchesGlob(t, root)
	mains, subs := walkProjects(root)
	if len(mains) < 10 || len(subs) < 6 {
		t.Fatalf("the fixture lists too little to compare: %q %q", mains, subs)
	}
	if m, s := walkProjects(filepath.Join(root, "missing")); m != nil || s != nil {
		t.Fatalf("a missing projects directory lists %q %q", m, s)
	}
}

// buildProjectTree makes a tree under root from b, three bytes per entry: the directory it goes in, its kind (a file,
// a directory, a link to a directory, a link to a file, a dangling link) and its name.
func buildProjectTree(t testing.TB, root string, b []byte) {
	t.Helper()
	names := []string{"a", "s.jsonl", "subagents", ".h.jsonl", "agent-1.jsonl", "subagents", "x[1]", "d.jsonl", "n.txt", "*", "é.jsonl", ".jsonl", "subagents", "b"}
	dirs, files := []string{root}, []string{}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+2 < len(b) && i < 3*64; i += 3 {
		parent, kind, name := dirs[int(b[i])%len(dirs)], b[i+1], names[int(b[i+2])%len(names)]
		p := filepath.Join(parent, name)
		if _, err := os.Lstat(p); err == nil {
			continue
		}
		var err error
		switch kind % 5 {
		case 0:
			if err = os.WriteFile(p, []byte("{}\n"), 0o600); err == nil {
				files = append(files, p)
			}
		case 1:
			if err = os.Mkdir(p, 0o700); err == nil {
				dirs = append(dirs, p)
			}
		case 2:
			err = os.Symlink(dirs[int(kind/5)%len(dirs)], p)
		case 3:
			if len(files) > 0 {
				err = os.Symlink(files[int(kind/5)%len(files)], p)
			}
		case 4:
			err = os.Symlink(filepath.Join(root, "missing"), p)
		}
		if err != nil {
			t.Skipf("building the tree: %v", err)
		}
	}
}

// Generated trees: the walk lists what the globs list.
func FuzzWalkProjectsMatchesGlob(f *testing.F) {
	f.Add([]byte{0, 1, 0, 1, 1, 2, 2, 1, 1, 2, 0, 4, 1, 1, 13, 3, 1, 2, 4, 0, 1, 4, 0, 4, 5, 2, 0, 0, 7, 3})
	f.Add([]byte{0, 1, 0, 1, 1, 0, 2, 1, 2, 3, 0, 1, 3, 0, 4, 0, 7, 0, 0, 2, 7, 1, 2, 12, 2, 5, 13, 0, 3, 3})
	f.Add([]byte{0, 6, 0, 0, 0, 1, 0, 9, 3, 0, 1, 7, 2, 1, 2, 2, 1, 5, 3, 0, 4, 3, 1, 12, 4, 0, 1, 4, 3, 10})
	f.Fuzz(func(t *testing.T, b []byte) {
		root := filepath.Join(t.TempDir(), "projects")
		buildProjectTree(t, root, b)
		requireWalkMatchesGlob(t, root)
	})
}

// The generated trees above, from a fixed run of seeds: the walk lists what the globs list in every one.
func TestWalkProjectsMatchesGlobOnGeneratedTrees(t *testing.T) {
	var mains, subs int
	for seed := range 200 {
		b := make([]byte, 90)
		x := uint32(seed)*2654435761 + 1
		for i := range b {
			x ^= x << 13
			x ^= x >> 17
			x ^= x << 5
			b[i] = byte(x)
		}
		root := filepath.Join(t.TempDir(), "projects")
		buildProjectTree(t, root, b)
		t.Run(fmt.Sprint(seed), func(t *testing.T) { requireWalkMatchesGlob(t, root) })
		m, s := walkProjects(root)
		mains, subs = mains+len(m), subs+len(s)
	}
	if mains < 500 || subs < 500 {
		t.Fatalf("the trees held %d transcripts and %d subagent transcripts: too few to compare", mains, subs)
	}
	t.Logf("%d transcripts, %d subagent transcripts", mains, subs)
}

// The usage meter lists its transcripts every 10 seconds, and the walk stats none that the directory says is a file.
// Over 20 projects of 20 transcripts, each with a subagent's, the walk made 9,106 allocations on Linux and the two
// globs 11,479: 2,373 fewer. With a stat per transcript put back, the walk made 9,906, only 1,573 fewer. The test
// bounds that saving, not the walk's own count: reading a directory allocates a different amount on each system (two
// more per read on macOS), and the globs read every directory the walk reads, so that cost cancels out. The bound
// leaves the walk the headroom an absolute bound would: 3% of its count, about 270 allocations.
func TestWalkProjectsAllocationBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	for p := range 20 {
		project := filepath.Join(root, fmt.Sprintf("-work-%d", p))
		for s := range 20 {
			agents := filepath.Join(project, fmt.Sprintf("s%d", s), "subagents")
			if err := os.MkdirAll(agents, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{filepath.Join(project, fmt.Sprintf("s%d.jsonl", s)), filepath.Join(agents, "agent-a.jsonl")} {
				if err := os.WriteFile(f, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if m, s := walkProjects(root); len(m) != 400 || len(s) != 400 {
		t.Fatalf("listed %d transcripts and %d subagent transcripts, want 400 of each", len(m), len(s))
	}
	if raceEnabled {
		t.Skip("race instrumentation allocates")
	}
	walk := testing.AllocsPerRun(10, func() { walkProjects(root) })
	globs := testing.AllocsPerRun(10, func() { globProjectFiles(t, root) })
	if saved := globs - walk; saved < 2100 {
		t.Errorf("listing 800 transcripts made %.0f allocations and the two globs %.0f: %.0f fewer, want at least 2,100", walk, globs, saved)
	}
}

// raceEnabled: built with -race, whose instrumentation allocates on its own (race_test.go).
var raceEnabled bool

// projectTranscripts lists what the two globProjects calls listed, in their order: every projects directory's
// transcripts, then every one's subagent transcripts. A projects directory whose path holds a glob metacharacter is
// still read as the globs read it (as a pattern).
func TestProjectTranscriptsMatchesGlobProjects(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, name := range []string{"plain", "a[b]"} {
		t.Run(name, func(t *testing.T) {
			n, base := testNode(t)
			n.home = filepath.Join(base, name)
			for _, home := range []string{n.home, filepath.Join(base, "ab")} { // "ab" is what the pattern a[b] matches
				for _, p := range []string{"-one/s1.jsonl", "-one/s1/subagents/agent-1.jsonl", "-two/s2.jsonl", "-two/s2/subagents/agent-2.jsonl"} {
					path := filepath.Join(home, ".claude", "projects", filepath.FromSlash(p))
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := append(n.globProjects("*", "*.jsonl"), n.globProjects("*", "*", "subagents", "*.jsonl")...)
			got := n.projectTranscripts()
			if !slices.Equal(got, want) {
				t.Fatalf("got %q\nwant %q", got, want)
			}
			if name == "plain" && len(got) != 4 {
				t.Fatalf("listed %q, want the four transcripts", got)
			}
		})
	}
}

// A usage sweep reports what it reported when it listed the transcripts with the two globs and stat'ed each again
// before reading it: Claude Code transcripts and their subagents', one written before the window, and a Codex rollout.
func TestUsageSweepReportMatchesGlobbedFiles(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n, home := testNode(t)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	cutoff := now.Add(-usageWindow)
	projects := filepath.Join(home, ".claude", "projects")
	write := func(p, body string, at time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(projects, "-work-app", "s1.jsonl"), claudeReply(now, "m1", "claude-opus-5-5", 2, 100, 1000, 50)+
		claudeReply(now.Add(-time.Minute), "m2", "claude-sonnet-5-5", 5, 0, 10, 7), now)
	write(filepath.Join(projects, "-work-app", "s1", "subagents", "agent-a.jsonl"), claudeReply(now, "a1", "claude-haiku-5-5", 1, 0, 0, 1), now)
	write(filepath.Join(projects, "-work-web", "s2.jsonl"), claudeReply(now.Add(-2*time.Minute), "m3", "claude-opus-5-5", 3, 0, 30, 9), now)
	write(filepath.Join(projects, "-work-web", "old.jsonl"), claudeReply(now.Add(-30*time.Hour), "m4", "claude-opus-5-5", 1, 1, 1, 1),
		now.Add(-30*time.Hour))
	stamp := `{"timestamp":"` + now.Format(time.RFC3339Nano) + `",`
	writeRollout(t, home, "0199a213-81c0-7800-8aa1-bbab2a035a53", "/work/app", now, now,
		stamp+`"type":"turn_context","payload":{"model":"gpt-6-codex"}}`,
		stamp+`"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1000},`+
			`"last_token_usage":{"input_tokens":900,"cached_input_tokens":800,"output_tokens":100}}}}`)

	// What usageFiles listed before the walk, each scanned with its own stat.
	var want []string
	paths := append(n.globProjects("*", "*.jsonl"), n.globProjects("*", "*", "subagents", "*.jsonl")...)
	for _, r := range rollouts(n.codexHome()) {
		paths = append(paths, r.path)
	}
	before := newUsageMeter()
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.ModTime().After(cutoff) && n.scopedTranscriptAllows(p) {
			want = append(want, p)
			scanFile(t, before, p, cutoff)
		}
	}
	var got []string
	after := newUsageMeter()
	for _, f := range n.usageFiles(cutoff) {
		got = append(got, f.path)
		after.scan(f.path, f.st, cutoff)
	}
	if !slices.Equal(got, want) || len(got) != 4 {
		t.Fatalf("usage files %q, want %q", got, want)
	}
	before.prune(cutoff)
	after.prune(cutoff)
	report := func(m *usageMeter) string { // the buckets in a fixed order: the report lists them in its map's
		t.Helper()
		params, ok := m.report(now)
		if !ok {
			t.Fatal("no report")
		}
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		var buckets []json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fields["buckets"], &buckets); err != nil {
			t.Fatal(err)
		}
		slices.SortFunc(buckets, func(a, b json.RawMessage) int { return bytes.Compare(a, b) })
		if fields["buckets"], err = json.Marshal(buckets); err != nil {
			t.Fatal(err)
		}
		if b, err = json.Marshal(fields); err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if a, b := report(after), report(before); a != b {
		t.Fatalf("the report differs\n got %s\nwant %s", a, b)
	}
}
