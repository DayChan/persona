package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

func TestHerdrCoreUnknownForegroundRefusesStart(t *testing.T) {
	for _, foreground := range []any{nil, map[string]any{}, map[string]any{"name": "unknown"}} {
		hc := newFakeHC(t)
		term := hcTerminal("t1", "", "", 50, "zsh", "/work", "", "", "")
		term["foreground"] = foreground
		hc.setTerminals(term)
		err := hcHost(hc).Run(context.Background(), host.PaneRef{Kind: host.HerdrCore, ID: "t1"}, "true start")
		if !errors.Is(err, host.ErrNotDelivered) || len(hc.trace("terminal.send")) != 0 {
			t.Errorf("foreground %v: error %v, sends %v", foreground, err, hc.trace("terminal.send"))
		}
	}
}

func TestHerdrCoreExplicitSpaceNeedsLayout(t *testing.T) {
	for name, doc := range map[string]any{"missing": nil, "unknown": map[string]any{"spaces": "new format"}} {
		t.Run(name, func(t *testing.T) {
			hc := newFakeHC(t)
			hc.doc = doc
			created, err := hcHost(hc).CreateSlot(context.Background(), "S1", "/work/api", "agent")
			if created != nil || !errors.Is(err, host.ErrNotDelivered) || len(hc.trace("terminal.run", "doc.set")) != 0 {
				t.Fatalf("created %+v, error %v, calls %v", created, err, hc.trace())
			}
		})
	}
}

func TestHerdrCoreSpaceFolderIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, folder, title, cwd string
		spaces                   int
	}{
		{"renamed", "/work/api", "Backend", "/work/api", 1},
		{"same basename", "/first/api", "api", "/second/api", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _, hc, _ := hcNode(t)
			hc.doc = appDoc(tc.folder)
			layoutSpaces(hc.doc.(map[string]any))[0]["name"] = tc.title
			h := hcHost(hc)
			ctx := context.Background()
			ws, err := n.workspaceFor(ctx, h, tc.cwd)
			if err != nil {
				t.Fatal(err)
			}
			created, err := n.newTab(ctx, h, tc.cwd, ws, "agent", "")
			if err != nil {
				t.Fatal(err)
			}
			spaces := layoutSpaces(docOf(t, hc))
			if len(spaces) != tc.spaces {
				t.Fatalf("spaces %v", spaces)
			}
			for _, space := range spaces {
				if space["id"] == created.SpaceID && space["folder"] != tc.cwd {
					t.Errorf("started in %v for %s", space, tc.cwd)
				}
			}
		})
	}
}

func TestHerdrCoreConcurrentSpaceReused(t *testing.T) {
	hc := newFakeHC(t)
	hc.doc = map[string]any{"spaces": []any{}}
	sets := 0
	hc.handle("doc.set", func(p map[string]any) any {
		hc.mu.Lock()
		defer hc.mu.Unlock()
		sets++
		if sets == 1 {
			hc.doc, hc.docVer = appDoc("/work/api"), hc.docVer+1
			hc.terms[0]["labels"].(map[string]any)[herdrSeenLabel] = "7" // the app has already shown the terminal
			return hcFail{"conflict"}
		}
		hc.doc, hc.docVer = p["value"], hc.docVer+1
		return map[string]any{"version": hc.docVer}
	})
	created, err := hcHost(hc).CreateSpace(context.Background(), "/work/api", "api")
	if err != nil {
		t.Fatal(err)
	}
	spaces := layoutSpaces(docOf(t, hc))
	if len(spaces) != 1 || created.SpaceID != "S1" || len(layoutTabs(spaces[0])) != 2 {
		t.Fatalf("created %+v, spaces %v", created, spaces)
	}
	term, err := hcHost(hc).c.Get(context.Background(), created.Pane.ID)
	if err != nil {
		t.Fatal(err)
	}
	if term.Labels[herdrSpaceLabel] != "S1" || term.Labels[herdrTabLabel] != created.SlotID || term.Labels[herdrWhereLabel] != "api / 2" || term.Labels[herdrSeenLabel] != "7" {
		t.Errorf("labels %v", term.Labels)
	}
}

func TestHerdrCoreStartUsesCoreShell(t *testing.T) {
	for _, tc := range []struct{ node, core string }{{"/bin/zsh", "/usr/bin/fish"}, {"/usr/bin/fish", "/bin/zsh"}} {
		t.Run(filepath.Base(tc.core), func(t *testing.T) {
			n, _, hc, home := hcNode(t)
			withPaneShell(t, tc.node)
			n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond
			proj := mkdir(t, filepath.Join(home, "project"))
			hc.doc = appDoc(proj)
			hc.handle("terminal.run", func(p map[string]any) any {
				term := hcTerminal("t101", "", "", 50, tc.core, proj, "", "", "")
				term["argv"], term["labels"] = []string{tc.core}, p["labels"]
				hc.setTerminals(term)
				return term
			})
			var line, script string
			hc.handle("terminal.send", func(p map[string]any) any {
				line = str(p["text"])
				files, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*"))
				if len(files) == 1 {
					b, err := os.ReadFile(files[0])
					if err != nil {
						return hcFail{"unavailable"}
					}
					script = string(b)
				}
				return map[string]any{}
			})
			_, _ = request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": proj})
			prefix := ". "
			fish := filepath.Base(tc.core) == "fish"
			if fish {
				prefix = "source "
			}
			if !strings.HasPrefix(line, prefix) || script == "" || strings.Contains(script, "\nunset ") == fish {
				t.Errorf("core %s, node %s: line %q, script %q", tc.core, tc.node, line, script)
			}
		})
	}
}

func TestHerdrCoreShellForProbeAndAdoptedPane(t *testing.T) {
	for _, shell := range []string{"/bin/zsh", "/usr/bin/fish"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			hc := newFakeHC(t)
			hc.handle("terminal.run", func(map[string]any) any {
				term := hcTerminal("t1", "", "", 50, shell, "/work", "", "", "")
				term["argv"] = []string{shell}
				return term
			})
			created, err := hcHost(hc).CreateSpace(context.Background(), "/work", "work")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if probe := (&Node{}).probeFor(ctx, startID, "claude", paneShells(ctx, created)); probe.paneShell != filepath.Base(shell) {
				t.Errorf("probe shell %q", probe.paneShell)
			}
			ctx = withAdoptedPane(ctx, created, []string{"/bin/bash"})
			if shells := paneShells(ctx, created); len(shells) != 1 || shells[0] != "/bin/bash" {
				t.Errorf("adopted shell %v", shells)
			}
		})
	}
}

func TestHerdrCoreExplicitSpaceStaysSelectedOnConflict(t *testing.T) {
	hc := newFakeHC(t)
	hc.doc = appDoc("/elsewhere")
	sets := 0
	hc.handle("doc.set", func(p map[string]any) any {
		hc.mu.Lock()
		defer hc.mu.Unlock()
		sets++
		if sets == 1 {
			doc := appDoc("/work/api")
			layoutSpaces(doc)[0]["id"] = "S2"
			doc["spaces"] = append(doc["spaces"].([]any), layoutSpaces(appDoc("/elsewhere"))[0])
			hc.doc, hc.docVer = doc, hc.docVer+1
			return hcFail{"conflict"}
		}
		hc.doc, hc.docVer = p["value"], hc.docVer+1
		return map[string]any{"version": hc.docVer}
	})
	created, err := hcHost(hc).CreateSlot(context.Background(), "S1", "/work/api", "agent")
	if err != nil {
		t.Fatal(err)
	}
	spaces := layoutSpaces(docOf(t, hc))
	if created.SpaceID != "S1" || len(layoutTabs(spaces[0])) != 1 || len(layoutTabs(spaces[1])) != 2 {
		t.Fatalf("created %+v, spaces %v", created, spaces)
	}
}

func TestHerdrCorePlacementCleanupErrors(t *testing.T) {
	for _, kind := range []string{"unavailable", "still live"} {
		t.Run(kind, func(t *testing.T) {
			hc := newFakeHC(t)
			hc.doc = appDoc("/work")
			hc.handle("doc.set", func(map[string]any) any { return hcFail{"conflict"} })
			hc.handle("terminal.kill", func(map[string]any) any {
				if kind == "still live" {
					return map[string]any{"state": "live"}
				}
				return hcFail{kind}
			})
			created, err := hcHost(hc).CreateSpace(context.Background(), "/work", "work")
			if created != nil || err == nil || !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "cleanup of t101 failed") {
				t.Fatalf("created %+v, error %v", created, err)
			}
			if len(hc.trace("doc.set")) != layoutTries || len(hc.trace("terminal.kill")) != 1 || len(hc.trace("terminal.send")) != 0 {
				t.Errorf("calls %v", hc.trace())
			}
		})
	}
}

func TestHerdrCoreUnknownOpenedShellIsClosed(t *testing.T) {
	for _, argv := range [][]string{nil, {"unknown"}} {
		hc := newFakeHC(t)
		hc.doc = appDoc("/work")
		hc.handle("terminal.run", func(map[string]any) any { return map[string]any{"address": "t1", "argv": argv} })
		created, err := hcHost(hc).CreateSpace(context.Background(), "/work", "work")
		if created != nil || !errors.Is(err, host.ErrNotDelivered) || len(hc.trace("terminal.kill")) != 1 || len(hc.trace("terminal.send", "doc.set")) != 0 {
			t.Errorf("argv %v: created %+v, error %v, calls %v", argv, created, err, hc.trace())
		}
	}
}

func TestHerdrCorePlacementChangesBeforeCommit(t *testing.T) {
	for _, scenario := range []string{"space removed", "layout removed", "label refused"} {
		t.Run(scenario, func(t *testing.T) {
			hc := newFakeHC(t)
			hc.doc = appDoc("/work")
			hc.handle("terminal.run", func(p map[string]any) any {
				hc.mu.Lock()
				defer hc.mu.Unlock()
				switch scenario {
				case "space removed":
					hc.doc = map[string]any{"spaces": []any{}}
				case "layout removed":
					hc.doc = nil
				case "label refused":
					layoutSpaces(hc.doc.(map[string]any))[0]["name"] = "Renamed"
				}
				term := hcTerminal("t1", "", "", 50, "zsh", "/work", "", "", "")
				term["labels"] = p["labels"]
				return term
			})
			hc.handle("terminal.label", func(map[string]any) any { return hcFail{"unavailable"} })
			created, err := hcHost(hc).CreateSlot(context.Background(), "S1", "/work", "agent")
			if created != nil || err == nil || len(hc.trace("terminal.kill")) != 1 || len(hc.trace("doc.set", "terminal.send")) != 0 {
				t.Errorf("created %+v, error %v, calls %v", created, err, hc.trace())
			}
		})
	}
}
