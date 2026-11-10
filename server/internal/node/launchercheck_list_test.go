package node

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A launcher health check runs a real headless claude a hundred times a day on each computer, each leaving a
// transcript: they filled the 80 newest offline sessions the list keeps and pushed the user's own Agents out of it
// (#2959, 76–80 of the 80 newest were checks on every machine looked at). The check's transcripts are no sessions of
// the user's: both the current and the older name are left out before the newest are counted.
func TestLauncherCheckTranscriptsTakeNoSlots(t *testing.T) {
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	projects := filepath.Join(n.home, ".claude", "projects")
	writeIn := func(project, id, cwd string, age time.Duration) {
		dir := filepath.Join(projects, project)
		_ = os.MkdirAll(dir, 0o700)
		p := filepath.Join(dir, id+".jsonl")
		body := `{"type":"user","uuid":"u1","timestamp":"2026-11-10T10:00:00Z","cwd":"` + cwd + `","message":{"role":"user","content":"hi"}}` + "\n" +
			`{"type":"assistant","uuid":"a1","timestamp":"2026-11-10T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}` + "\n"
		_ = os.WriteFile(p, []byte(body+strings.Repeat(" ", 4096)+"\n"), 0o600)
		at := time.Now().Add(-age)
		_ = os.Chtimes(p, at, at)
	}
	write := func(project, id string, age time.Duration) { writeIn(project, id, "/src/app", age) }
	const gone = "dddddddd-0000-0000-0000-000000000001"    // a check's: its temp directory is gone
	const mine = "dddddddd-0000-0000-0000-000000000002"    // the user's: its directory exists
	kept := filepath.Join(t.TempDir(), "persona-check-42") // named like a check's, and real
	_ = os.MkdirAll(kept, 0o700)
	writeIn("-tmp-persona-check-42", mine, kept, 30*time.Hour)
	writeIn("-tmp-persona-check-1001641372", gone, "/tmp/persona-check-1001641372", 30*time.Hour)
	writeIn("-private-tmp-jarvis-check-3821031972", gone, "/tmp/jarvis-check-3821031972", 25*time.Hour) // an older node's check, old prefix
	for i := 0; i < 100; i++ {
		writeIn("-tmp-persona-check-1000"+fmt.Sprint(500000+i), fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", i), "/tmp/persona-check-100050000"+fmt.Sprint(i), time.Duration(100-i)*time.Minute)
	}
	for i := 0; i < 5; i++ { // the user's own, quieter than the checks
		write("-src-app", fmt.Sprintf("cccccccc-0000-0000-0000-%012d", i), 10*time.Hour+time.Duration(i)*time.Minute)
	}
	listed := map[string]bool{}
	for _, s := range n.scanOffline() {
		listed[s.SessionID] = true
	}
	for i := 0; i < 5; i++ {
		if id := fmt.Sprintf("cccccccc-0000-0000-0000-%012d", i); !listed[id] {
			t.Fatalf("a quiet session of the user's is pushed out by the checks: %v", listed)
		}
	}
	for id := range listed {
		if strings.HasPrefix(id, "eeeeeeee") || id == gone {
			t.Fatalf("a check's transcript is listed: %v", listed)
		}
	}
	if !listed[mine] {
		t.Fatal("the user's own work in a directory named like a check's is hidden")
	}
}

// Only sessions that are listed take one of the 80: a transcript in a blocked directory (an automation's own
// directory) or a probe that never said anything used to take a slot and push a listed session out.
func TestOnlyListedSessionsTakeSlots(t *testing.T) {
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	projects := filepath.Join(n.home, ".claude", "projects")
	words := `{"type":"user","uuid":"u1","timestamp":"2026-11-10T10:00:00Z","cwd":"%s","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-10T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}` + "\n"
	write := func(project, id, cwd string, body string, age time.Duration) {
		dir := filepath.Join(projects, project)
		_ = os.MkdirAll(dir, 0o700)
		p := filepath.Join(dir, id+".jsonl")
		_ = os.WriteFile(p, []byte(fmt.Sprintf(body, cwd)+strings.Repeat(" ", 4096)), 0o600)
		at := time.Now().Add(-age)
		_ = os.Chtimes(p, at, at)
	}
	const quiet = "dddddddd-0000-0000-0000-000000000003" // the user's, quieter than everything below
	write("-src-app", quiet, "/src/app", words, 10*time.Hour)
	for i := 0; i < 80; i++ { // newer, and none of them is listed
		write("-src-bot", fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", i), "/src/bot", words, 5*time.Hour+time.Duration(79-i)*time.Minute)
		write("-src-probe", fmt.Sprintf("ffffffff-0000-0000-0000-%012d", i), "/src/probe",
			`{"type":"system","timestamp":"2026-11-10T10:00:00Z","cwd":"%s"}`+"\n", 3*time.Hour+time.Duration(79-i)*time.Minute)
	}
	n.setBlockedClaudeDirs([]string{"/src/bot"})
	listed := map[string]bool{}
	for _, s := range n.scanOffline() {
		listed[s.SessionID] = true
	}
	if !listed[quiet] {
		t.Fatal("blocked and wordless transcripts took all 80 slots and pushed the user's quieter session out")
	}
	for id := range listed {
		if strings.HasPrefix(id, "eeeeeeee") || strings.HasPrefix(id, "ffffffff") {
			t.Fatalf("a blocked or wordless transcript is listed: %v", listed)
		}
	}
}

// removeCheckProject removes the projects directory Claude Code keeps for the check's temp directory — it makes one
// (with an empty memory directory) even under --no-session-persistence, and a claude without the flag writes the
// transcript there — and nothing else: the suffix's random digits make the name unique. On macOS Claude Code names it
// after the resolved path while MkdirTemp returns the symlinked one (/var → /private/var): both names are removed.
func TestRemoveCheckProjectRemovesOnlyItsOwn(t *testing.T) {
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	projects := filepath.Join(n.home, ".claude", "projects")
	dir := filepath.Join(t.TempDir(), "persona-check-1001641372") // not under n.home: the encoded name is what finds it
	check := filepath.Join(projects, nonAlnum.ReplaceAllString(dir, "-"))
	mine := filepath.Join(projects, "-tmp-persona-check-42")        // a real directory the user called the same
	lookalike := filepath.Join(projects, "-tmp-x-persona-check-42") // another, differing in the middle
	for _, d := range []string{check, mine, lookalike} {
		_ = os.MkdirAll(filepath.Join(d, "memory"), 0o700)
		_ = os.WriteFile(filepath.Join(d, "memory", "m.txt"), []byte("keep"), 0o600)
	}
	n.removeCheckProject(dir)
	if _, err := os.Stat(check); !os.IsNotExist(err) {
		t.Fatalf("the check's directory is still there: %v", err)
	}
	for _, d := range []string{mine, lookalike} {
		if _, err := os.Stat(filepath.Join(d, "memory", "m.txt")); err != nil {
			t.Fatalf("%s was removed: %v", d, err)
		}
	}
}

// macOS's symlinked temp parent: Claude Code encodes the resolved path (/private/var/folders…), MkdirTemp hands the node
// the symlinked one (/var/folders…), and only the directory named for the resolved path is on the disk.
func TestRemoveCheckProjectResolvesTheTempSymlink(t *testing.T) {
	if runtime.GOOS != "darwin" { // /var → /private/var is the case it covers; on Linux the raw path is the real one
		t.Skip("macOS only: the raw encoding there is checked by TestRemoveCheckProjectRemovesOnlyItsOwn")
	}
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	projects := filepath.Join(n.home, ".claude", "projects")
	raw, err := os.MkdirTemp("", launcherCheckDir) // /var/folders/…/T/persona-check-<digits>
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(raw)
	real, err := filepath.EvalSymlinks(raw) // /private/var/folders/…: the name Claude Code keeps
	if err != nil {
		t.Fatal(err)
	}
	if real == raw {
		t.Skip("this machine resolves the temp directory to the same path")
	}
	coded := nonAlnum.ReplaceAllString(real, "-") // what Claude Code names it (#3101)
	check := filepath.Join(projects, coded)
	_ = os.MkdirAll(filepath.Join(check, "memory"), 0o700)
	n.removeCheckProject(raw)
	if _, err := os.Stat(check); !os.IsNotExist(err) {
		t.Fatalf("the directory named for the resolved path (%s) is still there: %v", coded, err)
	}
}
