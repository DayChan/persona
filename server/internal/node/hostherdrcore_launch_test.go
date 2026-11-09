package node

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
)

// appDoc is a Herdr app layout document: one space for folder with one tab holding t1, plus a key the node does not
// know, which an edit must keep.
func appDoc(folder string) map[string]any {
	return map[string]any{"spaces": []any{map[string]any{"id": "S1", "name": "", "folder": folder, "tabs": []any{
		map[string]any{"id": "T1", "name": "", "root": map[string]any{"id": "T1", "type": "row", "children": []any{
			map[string]any{"id": "P1", "type": "block", "content": map[string]any{"terminal": "t1"}},
		}}},
	}}}, "sidebar": map[string]any{"width": 240}}
}

// docOf is the fake's layout document as JSON-decoded maps.
func docOf(t *testing.T, hc *fakeHC) map[string]any {
	t.Helper()
	hc.mu.Lock()
	b, _ := json.Marshal(hc.doc)
	hc.mu.Unlock()
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func layoutSpaces(d map[string]any) []map[string]any {
	var out []map[string]any
	for _, s := range d["spaces"].([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

func layoutTabs(space map[string]any) []map[string]any {
	var out []map[string]any
	for _, t := range space["tabs"].([]any) {
		out = append(out, t.(map[string]any))
	}
	return out
}

func hcHost(hc *fakeHC) *herdrCoreHost { return &herdrCoreHost{c: &herdrcore.Client{Socket: hc.sock}} }

// A start on a Herdr App machine opens a shell the way the app does (kept, its terminal identity, the app's labels)
// as a new tab of the app's space for the directory, written into the layout document with everything else in it
// kept; the launcher goes into it through the start file. A start whose claude never comes up closes that terminal.
func TestHerdrCoreStartsInTheAppsSpace(t *testing.T) {
	n, _, hc, home := hcNode(t)
	n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond // the fake shell never runs claude
	proj := mkdir(t, filepath.Join(home, "proj"))
	hc.doc = appDoc(proj)
	hc.setTerminals(hcTerminal("t1", "", "", 50, "zsh", proj, "S1", "T1", "proj / 1"))
	const id = "0e000000-0000-4000-8000-000000002532"

	_, err := request(t, n, "session.start", map[string]any{"session_id": id, "cwd": proj, "prompt": "hi"})
	if err == nil {
		t.Fatal("started without a claude")
	}
	runs := hc.trace("terminal.run")
	if len(runs) != 1 {
		t.Fatalf("terminal.run %v (%v)", runs, err)
	}
	var run map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(runs[0], "terminal.run ")), &run)
	labels, _ := run["labels"].(map[string]any)
	env, _ := run["env"].(map[string]any)
	if run["cwd"] != proj || run["keep"] != true || run["no_parent"] != true || run["argv"] != nil ||
		labels[herdrSpaceLabel] != "S1" || !strings.HasPrefix(str(labels[herdrTabLabel]), "tb") ||
		labels[herdrWhereLabel] != "proj / 2" || labels[herdrSeenLabel] != "0" ||
		env["TERM"] != "xterm-256color" || env["TERM_PROGRAM"] != "ghostty" {
		t.Errorf("terminal.run %s", runs[0])
	}

	d := docOf(t, hc)
	sp := layoutSpaces(d)
	if len(sp) != 1 || d["sidebar"] == nil {
		t.Fatalf("document %v", d)
	}
	tabs := layoutTabs(sp[0])
	if len(tabs) != 2 || tabs[0]["id"] != "T1" || tabs[1]["id"] != labels[herdrTabLabel] || tabs[1]["name"] != "agent-1" {
		t.Fatalf("tabs %v", tabs)
	}
	root := tabs[1]["root"].(map[string]any)
	block := root["children"].([]any)[0].(map[string]any)
	if root["id"] != tabs[1]["id"] || root["type"] != "row" || block["type"] != "block" ||
		!strings.HasPrefix(str(block["id"]), "p") || block["content"].(map[string]any)["terminal"] != "t101" {
		t.Errorf("new tab %v", tabs[1])
	}

	sends := hc.trace("terminal.send")
	if len(sends) != 1 || !strings.Contains(sends[0], `"terminal":"t101"`) || !strings.Contains(sends[0], `"text":". `) ||
		!strings.Contains(sends[0], startScriptPrefix) {
		t.Errorf("terminal.send %v", sends)
	}
	if k := hc.trace("terminal.kill"); len(k) != 1 || !strings.Contains(k[0], "t101") {
		t.Errorf("failed start's terminal not closed: %v", k)
	}
}

// A new space for a directory the app has none for, added to the document as the app adds one (its folder, no name);
// a conflict (the app wrote the document meanwhile) redoes the change on what the app wrote. Its tab and the space
// are then renamed in the document.
func TestHerdrCoreNewSpaceAfterAConflict(t *testing.T) {
	hc := newFakeHC(t)
	hc.doc = appDoc("/elsewhere")
	sets := 0
	hc.handle("doc.set", func(p map[string]any) any {
		hc.mu.Lock()
		defer hc.mu.Unlock()
		sets++
		if sets == 1 { // the app adds a space of its own first
			d := appDoc("/elsewhere")
			d["spaces"] = append(d["spaces"].([]any), map[string]any{"id": "S9", "name": "app", "folder": "", "tabs": []any{}})
			hc.doc, hc.docVer = d, hc.docVer+1
			return hcFail{"conflict"}
		}
		if v, _ := p["version"].(float64); v != hc.docVer {
			return hcFail{"conflict"}
		}
		hc.doc, hc.docVer = p["value"], hc.docVer+1
		return map[string]any{"version": hc.docVer}
	})
	h := hcHost(hc)
	ctx := context.Background()
	c, err := h.CreateSpace(ctx, "/work/api", "api")
	if err != nil {
		t.Fatal(err)
	}
	sp := layoutSpaces(docOf(t, hc))
	if sets != 2 || len(sp) != 3 || sp[1]["id"] != "S9" {
		t.Fatalf("doc.set %d times, spaces %v", sets, sp)
	}
	if sp[2]["id"] != c.SpaceID || !strings.HasPrefix(c.SpaceID, "sp") || len(c.SpaceID) != 34 || sp[2]["folder"] != "/work/api" ||
		sp[2]["name"] != "" || len(layoutTabs(sp[2])) != 1 || layoutTabs(sp[2])[0]["id"] != c.SlotID || c.Pane.ID != "t101" {
		t.Errorf("created %+v in %v", c, sp[2])
	}

	if err := h.RenameSlot(ctx, c.SlotID, "review"); err != nil {
		t.Fatal(err)
	}
	if err := h.RenameSpace(ctx, c.SpaceID, "backend"); err != nil {
		t.Fatal(err)
	}
	sp = layoutSpaces(docOf(t, hc))
	if sp[2]["name"] != "backend" || layoutTabs(sp[2])[0]["name"] != "review" || sp[0]["name"] != "" {
		t.Errorf("renamed: %v", sp)
	}
	if err := h.RenameSlot(ctx, "tb-gone", "x"); err == nil {
		t.Error("renamed a tab the app does not have")
	}
	if err := h.RenameSpace(ctx, dirSpace+"/work", "x"); !errors.Is(err, host.ErrUnsupported) {
		t.Errorf("rename of a directory group: %v", err)
	}
}

// Without a layout document the node knows (none: the app never ran on this core; or another form), nothing is
// written: the shell starts unplaced, in its directory's group. Renaming is unsupported there.
func TestHerdrCoreUnknownLayoutIsNotWritten(t *testing.T) {
	for name, doc := range map[string]any{
		"none":           nil,
		"spaces not set": map[string]any{"spaces": map[string]any{}},
		"tab without id": map[string]any{"spaces": []any{map[string]any{"id": "S1", "folder": "/x", "tabs": []any{map[string]any{"root": map[string]any{}}}}}},
	} {
		hc := newFakeHC(t)
		hc.doc = doc
		h := hcHost(hc)
		c, err := h.CreateSlot(context.Background(), "", "/work/api", "agent-1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.SpaceID != dirSpace+"/work/api" || c.SlotID != "" || c.Pane.ID != "t101" {
			t.Errorf("%s: created %+v", name, c)
		}
		if r := hc.trace("terminal.run"); len(r) != 1 || strings.Contains(r[0], "labels") {
			t.Errorf("%s: terminal.run %v", name, r)
		}
		if s := hc.trace("doc.set"); len(s) != 0 {
			t.Errorf("%s: wrote %v", name, s)
		}
		if err := h.RenameSpace(context.Background(), "S1", "x"); !errors.Is(err, host.ErrUnsupported) {
			t.Errorf("%s: rename %v", name, err)
		}
	}
}

// A space the app no longer has is refused before anything starts; a document the core would not take ends the
// terminal made for it.
func TestHerdrCoreStartRefusals(t *testing.T) {
	hc := newFakeHC(t)
	hc.doc = appDoc("/x")
	h := hcHost(hc)
	if _, err := h.CreateSlot(context.Background(), "S404", "/x", "agent-1"); !errors.Is(err, host.ErrNotDelivered) {
		t.Errorf("gone space: %v", err)
	}
	if r := hc.trace("terminal.run"); len(r) != 0 {
		t.Errorf("started %v", r)
	}

	hc.handle("doc.set", func(map[string]any) any { return hcFail{"unavailable"} })
	if _, err := h.CreateSlot(context.Background(), "S1", "/x", "agent-1"); err == nil {
		t.Error("a terminal the document did not take")
	}
	if k := hc.trace("terminal.kill"); len(k) != 1 || !strings.Contains(k[0], "t101") {
		t.Errorf("not ended: %v", k)
	}
}

// Run submits the command only to a shell: with anything else in front (claude, a finished terminal) nothing is typed.
func TestHerdrCoreRunOnlyIntoAShell(t *testing.T) {
	hc := newFakeHC(t)
	h := hcHost(hc)
	hc.setTerminals(hcTerminal("t1", "", "", 50, "zsh", "/x", "", "", ""), hcTerminal("t2", "claude-code", "idle", 60, "claude", "/x", "", "", ""))
	ref := func(id string) host.PaneRef { return host.PaneRef{Kind: host.HerdrCore, ID: id} }
	if err := h.Run(context.Background(), ref("t1"), "claude --session-id x"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"t2", "t9"} {
		if err := h.Run(context.Background(), ref(p), "claude"); !errors.Is(err, host.ErrNotDelivered) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if s := hc.trace("terminal.send"); len(s) != 1 || !strings.Contains(s[0], `"terminal":"t1"`) {
		t.Errorf("terminal.send %v", s)
	}
}

// space.rename and a session's tab name write the app's layout document.
func TestHerdrCoreRename(t *testing.T) {
	n, _, hc, home := hcNode(t)
	writeTranscript(t, home, hcCwd, sidA, time.Now().Add(-time.Minute), `{"type":"custom-title","customTitle":"x"}`)
	writeClaudePID(t, home, 4242, sidA)
	hc.doc = appDoc(hcCwd)
	hc.setTerminals(hcTerminal("t1", "claude-code", "idle", 4242, "claude", hcCwd, "S1", "T1", "hcproj / 1"))
	if m := byID(listSessions(t, n)); m[sidA].SlotID != "T1" {
		t.Fatalf("not listed: %v", m)
	}
	if _, err := request(t, n, "space.rename", map[string]any{"workspace_id": "S1", "name": "backend"}); err != nil {
		t.Fatal(err)
	}
	if _, err := request(t, n, "session.rename", map[string]any{"session_id": sidA, "tab_name": "review"}); err != nil {
		t.Fatal(err)
	}
	sp := layoutSpaces(docOf(t, hc))
	if sp[0]["name"] != "backend" || layoutTabs(sp[0])[0]["name"] != "review" {
		t.Errorf("document %v", sp)
	}
}
