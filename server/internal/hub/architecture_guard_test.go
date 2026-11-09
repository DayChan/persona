package hub

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
)

// The hub is the communication center only: clients <-> manager, hub <-> hub, connections, pairing / auth, admin,
// push delivery and relays. Business (the assistant, inbox, todos, usage, boards, extensions, devices' data) is the
// manager's (internal/assistant, internal/devices, internal/extmgr). These tests pin the hub's surface so new business
// lands in the manager instead (docs/server/architecture.md "The hub is only the communication center").

const guardDoc = "see docs/server/architecture.md「hub 只做沟通中心」"

const modulePath = "github.com/nwverse/persona/server/"

// hubSources parses the hub's non-test files.
func hubSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = f
	}
	return out
}

// readList reads a testdata list: one entry per line, `#` starts a comment, blank lines skipped.
func readList(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The routes the hub answers itself (registered with a literal pattern in this package, minus the manager's it
// relays) are exactly testdata/hub_routes_allowed.txt.
func TestHubAnswersOnlyItsOwnRoutes(t *testing.T) {
	relayed := slices.Concat(assistantwire.Routes, assistantwire.InboxRoutes, devicewire.Routes, devicewire.MetricsRoutes, extwire.Routes)
	own := map[string]string{} // pattern -> file
	for name, f := range hubSources(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			switch call.Args[0].(type) {
			case *ast.BasicLit, *ast.BinaryExpr: // the same patterns route_scopes_test.go reads
				if p, ok := patternOf(call.Args[0]); ok && !slices.Contains(relayed, p) {
					own[p] = name
				}
			}
			return true
		})
	}
	allowed := readList(t, "hub_routes_allowed.txt")
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for p := range own {
			if !yield(p) {
				return
			}
		}
	}) {
		if !slices.Contains(allowed, p) {
			t.Errorf("the hub answers %q itself (%s), which is not in testdata/hub_routes_allowed.txt. "+
				"Business routes belong to the manager: implement it in internal/assistant, internal/devices or internal/extmgr "+
				"and let the hub relay it by listing it in assistantwire.Routes / devicewire.Routes / extwire.Routes. "+
				"Only if it is truly the hub's own (connections, pairing / auth, admin, push delivery, relays) add the line to "+
				"the allowlist; %s", p, own[p], guardDoc)
		}
	}
	for _, p := range allowed {
		if _, ok := own[p]; !ok {
			t.Errorf("testdata/hub_routes_allowed.txt lists %q, which the hub no longer answers itself: delete the line", p)
		}
	}
}

// The manager's packages and the connectors' implementations never end up in the hub's binary through the hub.
func TestHubDoesNotImportTheManager(t *testing.T) {
	forbidden := []string{ // anywhere in the hub's dependencies
		"internal/agents", "internal/ai", "internal/assistant", "internal/assistantskills", "internal/connector",
		"internal/devices", "internal/extmgr", "internal/extension/host", "internal/feishubot", "internal/kb",
		"internal/lark", "internal/mail", "internal/mcpserver", "internal/node", "internal/office",
		"plugins",
		"internal/tunnel", // tailcat: the manager dials the computers, with its own key (files_guard_test.go)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		rel, ok := strings.CutPrefix(dep, modulePath)
		if !ok {
			continue
		}
		for _, f := range forbidden {
			if rel == f || strings.HasPrefix(rel, f+"/") {
				t.Errorf("the hub depends on %s, which is the manager's: move the code that needs it into the manager "+
					"and reach it through the contract packages (assistantwire, devicewire, extwire; the types they carry "+
					"are in kbwire, connectorwire, aiwire); %s", dep, guardDoc)
			}
		}
	}
}

// The hub calls no model: it hands model settings on as aiwire.Config, imports nothing of internal/ai, and talks to
// no model API itself. Triage, titles, summaries and judging are the manager's.
func TestHubCallsNoModel(t *testing.T) {
	modelHosts := []string{"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com", "/chat/completions"}
	for name, f := range hubSources(t) {
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == modulePath+"internal/ai" {
				t.Errorf("%s imports internal/ai: the hub calls no model, the manager does (internal/assistant); a model "+
					"setting the hub hands on is an aiwire.Config; %s", name, guardDoc)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if n, ok := n.(*ast.BasicLit); ok && n.Kind == token.STRING {
				for _, h := range modelHosts {
					if strings.Contains(n.Value, h) {
						t.Errorf("%s names the model API %q: the hub calls no model, the manager does; %s", name, h, guardDoc)
					}
				}
			}
			return true
		})
	}
}

// The hub's Store is exactly testdata/hub_store_methods.txt: new business tables are the manager's (its own store).
func TestHubStoreStaysSmall(t *testing.T) {
	typ := reflect.TypeFor[Store]()
	var have []string
	for i := range typ.NumMethod() {
		have = append(have, typ.Method(i).Name)
	}
	allowed := readList(t, "hub_store_methods.txt")
	for _, m := range have {
		if !slices.Contains(allowed, m) {
			t.Errorf("hub.Store gained %s, which is not in testdata/hub_store_methods.txt. Business data is the manager's: "+
				"put the table behind the manager's store (internal/assistant, internal/devices, internal/extmgr). Only state of the "+
				"hub's own (connections, devices' tokens, pairing, push delivery) goes in the list; %s", m, guardDoc)
		}
	}
	for _, m := range allowed {
		if !slices.Contains(have, m) {
			t.Errorf("testdata/hub_store_methods.txt lists %s, which hub.Store no longer has: delete the line", m)
		}
	}
}
