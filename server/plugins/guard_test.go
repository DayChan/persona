// Package plugins_test guards server/plugins, which holds the plugins built and bundled with personad
// (docs/extensions/extensions.md, the extensions' sources): each is a plugin like any other, written against the Go SDK (sdk/go)
// and its own packages only, and run only by its own program in cmd/.
package plugins_test

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	repo    = "github.com/nwverse/persona"
	module  = repo + "/server"
	sdk     = repo + "/sdk/go"
	plugins = module + "/plugins" // holds the plugins; no package of its own
)

// within says the import path p is path or a package under it.
func within(p, path string) bool { return p == path || strings.HasPrefix(p, path+"/") }

// pluginOf is the plugin an import path belongs to (…/plugins/example/client → …/plugins/example), "" for none.
func pluginOf(p string) string {
	rel, ok := strings.CutPrefix(p, plugins+"/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rel, "/")
	return plugins + "/" + name
}

// own says a plugin or its program may use the package p: the standard library, a third-party module, the SDK, or a
// package of its own (one of mine). Nothing else of this repository.
func own(p string, mine ...string) bool {
	if !within(p, repo) || within(p, sdk) {
		return true
	}
	return slices.ContainsFunc(mine, func(m string) bool { return within(p, m) })
}

// parse reads a .go file's imports, whatever its build constraints.
func parse(fset *token.FileSet, path string) ([]string, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var imps []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		imps = append(imps, p)
	}
	return imps, nil
}

// resolved is path's real absolute location, links followed.
func resolved(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// inside says the location path is dir or under it, ignoring case where the file system does by default (macOS,
// Windows): a link to …/PLUGINS/example leads into the plugins there.
func inside(path, dir string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// samePlace says the locations a and b are the same, as inside judges.
func samePlace(a, b string) bool { return inside(a, b) && inside(b, a) }

// fail reports what breaks one rule once, with the first few cases: a program that should not be one (the hub
// importing a plugin, say) would otherwise give an error for each of its many imports.
func fail(t *testing.T, rule string, cases []string) {
	t.Helper()
	if len(cases) == 0 {
		return
	}
	const shown = 5
	more := ""
	if len(cases) > shown {
		more = fmt.Sprintf("\n\t… and %d more", len(cases)-shown)
		cases = cases[:shown]
	}
	t.Errorf("%s:\n\t%s%s", rule, strings.Join(cases, "\n\t"), more)
}

// unbuilt says the go tool never builds the file at path: its name starts with . or _ (an editor's lock or backup). A
// directory so named is read all the same: a package in it can still be imported by its path.
func unbuilt(path string, d fs.DirEntry) bool {
	if name := d.Name(); d.IsDir() || !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_") {
		return false
	}
	info, err := os.Stat(path) // a link: what it leads to
	return d.Type()&fs.ModeSymlink == 0 || err != nil || !info.IsDir()
}

// imports are what every .go file under dir (a plugin's, a program's or the SDK's) imports, by file: tests, testdata
// and every build constraint included (a _darwin.go or a `//go:build public` file counts as much as any). Such a
// directory's files are its own: a link in it is an error (WalkDir does not follow one, and a package could hide behind
// it). A file whose imports cannot be read (a template, a broken fixture) is noted and left: it cannot be built, so
// it imports nothing.
func imports(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if unbuilt(path, d) {
			return nil
		}
		name, _ := filepath.Rel("..", path) // as the other guard names files: from server/
		if d.Type()&fs.ModeSymlink != 0 {
			t.Errorf("%s is a symbolic link: the files of a plugin, its program and the SDK are their own", name)
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		imps, err := parse(fset, path)
		if err != nil {
			t.Logf("%s: %v (cannot be built, so it imports nothing)", name, err)
			return nil
		}
		out[name] = imps
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// runs are the plugins the program in dir runs, each with the first file that imports it: those its non-test files
// import (a test may import a plugin without making its directory that plugin's program). A linked .go file counts
// (the go tool reads through it); a file that cannot be read is left to the checks that read every file.
func runs(t *testing.T, dir string) map[string]string {
	t.Helper()
	used := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || unbuilt(path, d) || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		imps, err := parse(fset, path)
		if err != nil {
			return nil
		}
		for _, p := range imps {
			if pl := pluginOf(p); pl != "" && used[pl] == "" {
				used[pl], _ = filepath.Rel("..", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// part is a plugin or a program as the guard holds it: the packages of this repository it may use, and the rule it is
// held to, said as an error would say it.
type part struct {
	allowed []string
	rule    string
}

// A plugin and its program use the SDK, the standard library, third-party modules and their own packages: no
// server/internal package, no other plugin, nothing else of this repository's. What a plugin needs of the hub goes
// through the plugin protocol. Every file is read, whatever its build constraints, so one built only on some platform
// or with some tag cannot slip through; the go tool's own view (go list -deps, for each platform) is held to it too,
// by where each package's code is as well as by its path. The SDK is let through by its path: TestTheSDKStandsAlone
// reads it the same way.
func TestPluginsDependOnlyOnTheSDK(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]part{} // by directory, relative to server/
	for _, e := range entries {
		switch {
		case unbuilt(e.Name(), e):
		case e.Type()&os.ModeSymlink != 0:
			t.Errorf("plugins/%s is a symbolic link: a plugin's files are its own", e.Name())
		case e.IsDir() && moduleFile(filepath.Join(e.Name(), "go.mod")):
			// A module of its own: not built as the server's, held as TestOnlyProgramsImportPlugins holds one.
			checkNestedModule(t, "plugins/"+e.Name(), filepath.Join(e.Name(), "go.mod"))
		case e.IsDir():
			mine["plugins/"+e.Name()] = part{
				allowed: []string{plugins + "/" + e.Name()},
				rule: "plugins/" + e.Name() + " is a plugin: it uses only the SDK (sdk/go), the standard library, " +
					"third-party modules and its own packages",
			}
		case strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go"):
			t.Errorf("plugins/%s: plugins/ holds the plugins, not a package of its own", e.Name())
		}
	}
	// The programs that run a plugin: every directory under cmd/ whose code imports one. Only they (and tests) may import
	// a plugin (TestOnlyProgramsImportPlugins), and the SDK imports nothing of the server's, so a program cannot reach a
	// plugin through another package.
	cmds, err := os.ReadDir("../cmd")
	if err != nil {
		t.Fatal(err)
	}
	cmdDir := resolved(t, "../cmd")
	var others []string // the programs that run no plugin
	for _, e := range cmds {
		path := filepath.Join("../cmd", e.Name())
		if unbuilt(path, e) {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			// A compatibility alias (personad → personad, …) links to a program in cmd/, scanned under its own name. A
			// link to a file or to nothing holds no program.
			target, err := filepath.EvalSymlinks(path)
			if info, statErr := os.Stat(path); err == nil && statErr == nil && info.IsDir() &&
				!samePlace(filepath.Dir(resolved(t, target)), cmdDir) {
				to, _ := filepath.Rel(resolved(t, ".."), resolved(t, target))
				t.Errorf("cmd/%s links to %s: a compatibility alias leads to a program directly in cmd/", e.Name(), to)
			}
			continue
		}
		if !e.IsDir() || moduleFile(filepath.Join(path, "go.mod")) { // a module of its own: TestOnlyProgramsImportPlugins
			continue
		}
		used := runs(t, path)
		switch len(used) {
		case 0:
			others = append(others, e.Name())
		case 1:
			for pl, file := range used {
				mine["cmd/"+e.Name()] = part{
					allowed: []string{pl, module + "/cmd/" + e.Name()},
					rule: fmt.Sprintf("cmd/%s imports the plugin %s (in %s), so it is held as that plugin's own program: "+
						"only such a program imports a plugin, and it uses only the SDK (sdk/go), the plugin, third-party "+
						"modules and its own packages", e.Name(), pl, file),
				}
			}
		default:
			t.Errorf("cmd/%s runs %v: a program runs one plugin (each in a directory of its own in cmd/)", e.Name(),
				slices.Sorted(maps.Keys(used)))
		}
	}
	dirs := slices.Sorted(maps.Keys(mine))
	for _, dir := range dirs {
		pt := mine[dir]
		var cases []string
		for file, imps := range imports(t, filepath.Join("..", dir)) {
			for _, p := range imps {
				if !own(p, pt.allowed...) {
					cases = append(cases, file+" imports "+p)
				}
			}
		}
		slices.Sort(cases)
		fail(t, pt.rule+"; these imports break it", cases)
	}

	// And what the go tool resolves for each platform the server ships on, tests included (a dependency reached
	// indirectly), by path and by where its code is: this repository's code is the SDK's or the plugin's and program's
	// own, whatever its path.
	repoDir, sdkDir := resolved(t, "../.."), resolved(t, "../../sdk/go")
	vendorDir := filepath.Join(resolved(t, ".."), "vendor")
	for _, dir := range dirs {
		pt := mine[dir]
		ownDirs := []string{sdkDir, resolved(t, filepath.Join("..", dir))}
		for _, a := range pt.allowed {
			if rel, ok := strings.CutPrefix(a, module+"/"); ok {
				ownDirs = append(ownDirs, resolved(t, filepath.Join("..", rel)))
			}
		}
		var cases []string
		for _, d := range deps(t, dir, true) {
			at := slices.ContainsFunc(ownDirs, func(o string) bool { return inside(d.dir, o) })
			if !own(d.path, pt.allowed...) || d.dir != "" && inside(d.dir, repoDir) && !at {
				c := fmt.Sprintf("%s (%s, for %s)", d.path, d.dir, d.platform)
				if inside(d.dir, vendorDir) {
					c += ": a vendored copy, not the SDK in sdk/go that these guards read"
				}
				cases = append(cases, c)
			}
		}
		fail(t, pt.rule+"; it depends on these, as the go tool resolves it", cases)
	}
	// Nor has another program a plugin among its dependencies: one reached through a published module, say, which none
	// of the files read here imports.
	pluginDir := resolved(t, ".")
	for _, name := range others {
		var cases []string
		for _, d := range deps(t, "cmd/"+name, false) {
			if within(d.path, plugins) || d.dir != "" && inside(d.dir, pluginDir) {
				cases = append(cases, fmt.Sprintf("%s (%s, for %s)", d.path, d.dir, d.platform))
			}
		}
		fail(t, "cmd/"+name+" imports no plugin, yet depends on one, as the go tool resolves it: only a plugin's own "+
			"program runs it", cases)
	}
}

// pkg is a package as the go tool lists it: its import path, for code from a local directory where it is, and the
// first build (platform and tags) it was listed for.
type pkg struct{ path, dir, platform string }

// platforms are the GOOS/GOARCH pairs the server ships for: the release builds (scripts/release/publish.sh) and the
// node on Windows (docs/operations/windows.md).
var platforms = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"}

// buildTags are the tag sets the server is built with: the release builds' public, and none (the Docker image,
// scripts/deploy/deploy.sh). A `// +build` line still counts, so a file can differ between them.
var buildTags = []string{"", "public"}

// localDir is the template that prints a package's directory when its code is local: the main module's, or a module's
// put in place by a directory. The standard library and published modules print nothing (their directories can be
// anywhere, the module cache inside a checkout too).
const localDir = `{{with .Module}}{{if .Main}}{{$.Dir}}{{else}}` +
	`{{with .Replace}}{{if not .Version}}{{$.Dir}}{{end}}{{end}}{{end}}{{end}}`

// noCgoFlags are the GOFLAGS in effect (the environment's or go env's) but those that need cgo.
func noCgoFlags(goBin string) string {
	out, _ := exec.Command(goBin, "env", "GOFLAGS").Output()
	var keep []string
	for _, f := range strings.Fields(string(out)) {
		name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=") // -race, --race, -race=true
		if !slices.Contains([]string{"race", "msan", "asan"}, name) {
			keep = append(keep, f)
		}
	}
	return strings.Join(keep, " ")
}

// deps are the packages dir (relative to server/) and its subpackages depend on for each platform and set of build
// tags, their tests' too if tests, local ones located with links followed. A test's variants are named by the package
// they test (…/x_test, "…/x [….test]" and …/x.test are …/x).
func deps(t *testing.T, dir string, tests bool) []pkg {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	type build struct{ platform, tags string }
	var builds []build
	for _, platform := range platforms {
		for _, tags := range buildTags {
			builds = append(builds, build{platform, tags})
		}
	}
	flags := noCgoFlags(goBin)
	outs, errs := make([][]byte, len(builds)), make([]error, len(builds))
	var wg sync.WaitGroup
	for i, b := range builds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args := []string{"list", "-deps", "-tags", b.tags, "-f", "{{.ImportPath}}\t" + localDir}
			if tests {
				args = append(args, "-test")
			}
			goos, goarch, _ := strings.Cut(b.platform, "/")
			cmd := exec.Command(goBin, append(args, "../"+dir+"/...")...)
			// As the builds are made; without the flags that need cgo (GOFLAGS=-race, say), which go list would refuse so.
			cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS="+flags)
			outs[i], errs[i] = cmd.Output()
		}()
	}
	wg.Wait()
	var pkgs []pkg
	seen := map[[2]string]bool{}
	for i, b := range builds {
		name := b.platform
		if b.tags != "" {
			name += " -tags " + b.tags
		}
		if err := errs[i]; err != nil {
			msg := ""
			if ee, ok := err.(*exec.ExitError); ok {
				msg = string(ee.Stderr)
			}
			t.Fatalf("go list %s for %s: %v %s", dir, name, err, msg)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(outs[i])), "\n") {
			path, loc, _ := strings.Cut(line, "\t")
			path, _, _ = strings.Cut(path, " ")
			p := pkg{path: strings.TrimSuffix(strings.TrimSuffix(path, ".test"), "_test"), platform: name}
			if loc != "" {
				p.dir = resolved(t, loc)
			}
			if key := [2]string{p.path, p.dir}; !seen[key] {
				seen[key] = true
				pkgs = append(pkgs, p)
			}
		}
	}
	return pkgs
}

// The SDK is what a plugin may use of this repository, let through by its path: so it is read like a plugin, every
// file whatever its build constraints, and imports nothing of this repository but itself (no server package, so no
// plugin either), with no link in it. And the SDK is the only code of this repository a build takes from outside
// server/: the server's go.mod puts no other local directory, nor a version of this repository's code, in place of a
// module, and a workspace in the repository (an editor's, say) uses only the server and the SDK.
// So all code in a build of the server is the server's (held by these guards), the SDK's or a published module's.
func TestTheSDKStandsAlone(t *testing.T) {
	for file, imps := range imports(t, "../../sdk/go") {
		for _, p := range imps {
			if !own(p) {
				t.Errorf("%s imports %s: the SDK imports nothing of this repository but itself", file, p)
			}
		}
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	if out, err := exec.Command(goBin, "env", "GOWORK").Output(); err != nil {
		t.Fatalf("go env GOWORK: %v", err)
	} else if w := strings.TrimSpace(string(out)); w != "" && w != "off" {
		if inside(resolved(t, w), resolved(t, "../..")) {
			checkWorkspace(t, goBin, w, false)
		} else { // a developer's own: what is checked in builds without it
			t.Logf("the server builds in the workspace %s, outside the repository: not judged", w)
		}
	}
	out, err := exec.Command(goBin, "mod", "edit", "-json", "../go.mod").Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod struct {
		Replace []struct {
			Old, New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	for _, r := range mod.Replace {
		if r.Old.Path == sdk && r.New.Path == "../sdk/go" && r.New.Version == "" {
			continue // the SDK, read by this test
		}
		if why := badReplace(r.Old.Path, r.New.Path, r.New.Version); why != "" {
			t.Errorf("go.mod replaces %s with %s: %s", r.Old.Path, strings.TrimSpace(r.New.Path+" "+r.New.Version), why)
		}
	}
}

// badReplace says why a replace (other than the SDK's own) would bring code past these guards, "" if it does not: a
// directory is code from elsewhere under a module's path; a version of this repository's own modules, or one for the
// SDK, is this repository's code (a copy of a plugin, say) under another path, or an SDK this test has not read.
func badReplace(old, newPath, newVersion string) string {
	switch {
	case newVersion == "":
		return "only the SDK comes from a directory"
	case within(newPath, repo):
		return "a version of this repository's own code would come in under another path"
	case within(old, sdk):
		return "the SDK is the one in sdk/go"
	}
	return ""
}

// checkWorkspace holds the workspace the server builds in to the server and the SDK, replacing modules only as go.mod
// may: a workspace builds code from elsewhere as a module's. A nested module's own workspace (nested) may use anything
// but the server.
func checkWorkspace(t *testing.T, goBin, file string, nested bool) {
	t.Helper()
	out, err := exec.Command(goBin, "work", "edit", "-json", file).Output()
	if err != nil {
		t.Fatalf("go work edit -json %s: %v", file, err)
	}
	var w struct {
		Use     []struct{ DiskPath string }
		Replace []struct {
			Old, New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &w); err != nil {
		t.Fatal(err)
	}
	server := resolved(t, "..")
	allowed := []string{server, resolved(t, "../../sdk/go")}
	for _, u := range w.Use {
		p := u.DiskPath
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(file), p)
		}
		loc, err := filepath.EvalSymlinks(p)
		switch {
		case nested && (err != nil || samePlace(loc, server)):
			t.Errorf("the workspace %s uses %s: a module of its own would use the server's code past these guards",
				file, u.DiskPath)
		case !nested && (err != nil || !slices.Contains(allowed, loc)):
			t.Errorf("the workspace %s uses %s: the server builds in a workspace of the server and the SDK only",
				file, u.DiskPath)
		}
	}
	for _, r := range w.Replace {
		if why := badReplace(r.Old.Path, r.New.Path, r.New.Version); why != "" {
			t.Errorf("the workspace %s replaces %s with %s: %s", file, r.Old.Path,
				strings.TrimSpace(r.New.Path+" "+r.New.Version), why)
		}
	}
}

// A plugin is run by its program only. Whatever a file's build constraints, and wherever it is (testdata, _ and .
// directories too: the go tool leaves those out of patterns, not out of an import):
//   - no package outside a program's tree (cmd/<program>/…) imports a plugin;
//   - no package outside a program's tree imports one of the program's packages (cmd/internal, which Go lets only cmd/
//     import, is shared by the programs that run no plugin, and is no program's tree);
//   - no link brings a plugin's code, a program's, or code these guards do not read into another part of the server;
//   - a nested module, which the walk skips, does not use the server's code, nor does a workspace of its own
//     (checkLink holds links into one).
//
// _test.go files may break these: a test is not linked into a program. With the SDK importing nothing of the
// server's (TestTheSDKStandsAlone), the manager never builds a plugin's code into itself, and a program cannot reach a
// plugin through a package in between.
func TestOnlyProgramsImportPlugins(t *testing.T) {
	root, pluginDir, cmdDir := resolved(t, ".."), resolved(t, "."), resolved(t, "../cmd")
	toolDirs := goToolDirs(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && samePlace(path, pluginDir) {
			return filepath.SkipDir // TestPluginsDependOnlyOnTheSDK
		}
		if d.IsDir() && slices.ContainsFunc(toolDirs, func(dir string) bool { return samePlace(path, dir) }) {
			return filepath.SkipDir // a module cache or Go's own tree inside the checkout: published code, not the server's
		}
		if d.IsDir() && path != root {
			if gomod := filepath.Join(path, "go.mod"); moduleFile(gomod) {
				checkNestedModule(t, rel, gomod)
				if gowork := filepath.Join(path, "go.work"); moduleFile(gowork) { // a module may be built in its own
					checkWorkspace(t, goBinOrSkip(t), gowork, true)
				}
				return filepath.SkipDir // a module of its own (a copy of the repository, say): checkLink holds links into it
			}
		}
		if unbuilt(path, d) {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			checkLink(t, rel, path, root, pluginDir, cmdDir)
			if !strings.HasSuffix(path, ".go") { // a linked .go file is read like any: the go tool reads through it
				return nil
			}
		} else if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		imps, err := parse(fset, path)
		if err != nil { // a template or a broken fixture: it cannot be built, so it imports nothing
			t.Logf("%s: %v (cannot be built, so it imports nothing)", rel, err)
			return nil
		}
		program := "" // the program whose tree the file is in; cmd/internal is shared by the programs, so it is none
		if len(parts) >= 3 && parts[0] == "cmd" && parts[1] != "internal" {
			program = module + "/cmd/" + parts[1]
		}
		for _, p := range imps {
			switch {
			case p == plugins:
				t.Errorf("%s imports %s: plugins/ holds the plugins, not a package of its own", rel, p)
			case within(p, plugins) && program == "":
				t.Errorf("%s imports the plugin %s: only its program (in cmd/<program>/) runs a plugin", rel, p)
			case within(p, module+"/cmd") && !within(p, program) && !within(p, module+"/cmd/internal"):
				t.Errorf("%s imports %s: a program's packages are its own (only its tree in cmd/ imports them)", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkLink holds a link in the server (outside plugins/): it may not lead into plugins/ to a directory or code (a
// plugin's files are built into its program only), into a program's tree in cmd/ from outside it (a compatibility
// alias directly in cmd/ aside), nor to code the guards do not read (outside the server, or in a module of its own
// under it, which the go tool builds as the server's through the link all the same). Code is a .go file, by
// the link's name or the target's (the go tool compiles a linked file by the link's name), or a directory with a .go
// file or a link in it. Links to data, and links that lead nowhere, carry no code.
func checkLink(t *testing.T, rel, path, root, pluginDir, cmdDir string) {
	t.Helper()
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		return
	}
	codeFile := !info.IsDir() && (strings.HasSuffix(path, ".go") || strings.HasSuffix(target, ".go"))
	switch {
	case inside(target, pluginDir) && (info.IsDir() || codeFile):
		t.Errorf("%s links into plugins/: a plugin's files are built into its program only", rel)
	case inside(target, cmdDir) && (info.IsDir() || codeFile) && !sameProgram(rel, target, root):
		t.Errorf("%s links into cmd/: a program's files are its own", rel)
	case unread(target, root) && (codeFile || info.IsDir() && holdsCode(target)):
		t.Errorf("%s links to code these guards do not read (outside server/, or in a module of its own)", rel)
	}
}

// goToolDirs are the go tool's own directories (the module cache and GOROOT), wherever they are.
func goToolDirs(t *testing.T) []string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	out, err := exec.Command(goBin, "env", "GOMODCACHE", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	var dirs []string
	for _, d := range strings.Split(strings.TrimSpace(string(out)), "\n") { // one per line: a path may hold spaces
		if loc, err := filepath.EvalSymlinks(d); err == nil {
			dirs = append(dirs, loc)
		}
	}
	return dirs
}

// goBinOrSkip is the go tool; without one the test is skipped.
func goBinOrSkip(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	return goBin
}

// checkNestedModule holds a module of its own under the server (rel is its directory): it may not use the server's
// code, which it would build past these guards (Go's internal rule judges by import path, so it could reach
// server/internal too), nor code from a directory in place of a module.
func checkNestedModule(t *testing.T, rel, gomod string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	out, err := exec.Command(goBin, "mod", "edit", "-json", gomod).Output()
	if err != nil {
		t.Errorf("%s: go mod edit -json: %v", rel, err)
		return
	}
	var mod struct {
		Require []struct{ Path string }
		Replace []struct {
			Old, New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	for _, r := range mod.Require {
		if within(r.Path, repo) && !within(r.Path, sdk) {
			t.Errorf("%s is a module of its own that requires %s: it would use this repository's code past these guards",
				rel, r.Path)
		}
	}
	for _, r := range mod.Replace {
		if r.Old.Path == sdk && r.New.Version == "" {
			continue // the SDK, from its directory
		}
		if why := badReplace(r.Old.Path, r.New.Path, r.New.Version); why != "" {
			t.Errorf("%s is a module of its own that replaces %s with %s: %s", rel, r.Old.Path,
				strings.TrimSpace(r.New.Path+" "+r.New.Version), why)
		}
	}
}

// moduleFile says path is a go.mod as the go tool counts one: a file (a directory so named is not).
func moduleFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// unread says the guards do not read the location path: it is outside root (the server), or in a module of its own
// under it, which TestOnlyProgramsImportPlugins skips.
func unread(path, root string) bool {
	if !inside(path, root) {
		return true
	}
	for dir := path; !samePlace(dir, root); dir = filepath.Dir(dir) {
		if moduleFile(filepath.Join(dir, "go.mod")) {
			return true
		}
	}
	return false
}

// sameProgram says the link at rel (relative to the server) may lead to target in cmd/: it is in the same program's
// tree, or it is a compatibility alias directly in cmd/ for a program directly in cmd/.
func sameProgram(rel, target, root string) bool {
	to, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	from, dest := strings.Split(filepath.ToSlash(rel), "/"), strings.Split(filepath.ToSlash(to), "/")
	if from[0] != "cmd" {
		return false
	}
	if len(from) == 2 && len(dest) == 2 { // cmd/personad → cmd/personad
		return true
	}
	return len(from) >= 3 && len(dest) >= 2 && from[1] == dest[1]
}

// holdsCode says the directory dir has a .go file or a link (which could lead to one) anywhere in it.
func holdsCode(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 || !d.IsDir() && strings.HasSuffix(path, ".go") {
			found = true // what cannot be read could hold code too
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
