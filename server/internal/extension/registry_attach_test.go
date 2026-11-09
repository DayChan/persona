package extension

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func messageKey(err error) string {
	var m *i18n.Message
	if errors.As(err, &m) {
		return m.Key
	}
	return ""
}

// Attaching an installed package and detaching it again keep all of it: the package (what its skills are read from),
// the settings, the secrets and the data. Only where its program runs changes, and no upgrade or rollback replaces the
// package meanwhile.
func TestAttachDetachKeepThePackageSettingsSecretsAndData(t *testing.T) {
	ctx := context.Background()
	r, store, changes := newTestRegistry(t)
	pkgDir, data := installNotes(t, r)
	config := json.RawMessage(`{"base_url":"https://notes.example.com/a"}`)
	if err := r.SetConfig(ctx, "example-notes", config, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "k1", "cli"); err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot("example-notes").Record
	settings := string(r.Config(r.Get("example-notes")))
	var dialed Remote
	r.RemoteManifest = func(_ context.Context, rm Remote) (*Manifest, error) {
		dialed = rm
		return Parse(with(t, func(map[string]any) {}))
	}
	kept := func(when string, p *Extension) {
		t.Helper()
		rec := p.Record
		if p.Dir != pkgDir || p.Problem != "" || rec.Origin != before.Origin || rec.Version != before.Version ||
			rec.GrantedDigest != before.GrantedDigest || string(rec.Manifest) != string(before.Manifest) {
			t.Fatalf("%s: the package changed: %+v (dir %s, problem %q)", when, rec, p.Dir, p.Problem)
		}
		if string(r.Config(p)) != settings || !strings.Contains(settings, "notes.example.com/a") {
			t.Fatalf("%s: settings %s", when, r.Config(p))
		}
		if s, err := r.Secrets(ctx, "example-notes"); err != nil || s["api_key"] != "k1" {
			t.Fatalf("%s: secrets %v %v", when, s, err)
		}
		for _, f := range []string{filepath.Join(pkgDir, "bin", "notes"), filepath.Join(data, "token")} {
			if _, err := os.Stat(f); err != nil {
				t.Fatalf("%s: %v", when, err)
			}
		}
	}

	*changes = nil
	p, err := r.Attach(ctx, "example-notes", "remote:rpc=/tmp/example-notes.sock", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if dialed.RPCSocket != "/tmp/example-notes.sock" || p.Record.Remote == nil || *p.Record.Remote != dialed || !p.Record.AttachedPackage() {
		t.Fatalf("attached on %+v: %+v", dialed, p.Record.Remote)
	}
	if row := store.rows["example-notes"]; row.Remote == nil || row.Remote.RPCSocket != dialed.RPCSocket {
		t.Fatalf("not saved: %+v", row.Remote)
	}
	kept("attached", p)
	if len(*changes) != 1 || (*changes)[0].Kind != "attached" || (*changes)[0].Old.Record.Remote != nil {
		t.Fatalf("%+v", *changes)
	}

	pkg2 := t.TempDir()
	writePackage(t, pkg2, func(m map[string]any) { m["version"] = "1.3.0" })
	if _, err := r.Preview(ctx, "dir:"+pkg2, "example-notes"); messageKey(err) != "extension-status.attached_detach_first" {
		t.Fatal("upgrade while attached:", err)
	}
	if _, err := r.Rollback(ctx, "example-notes", "cli"); messageKey(err) != "extension-status.attached_detach_first" {
		t.Fatal("rollback while attached:", err)
	}

	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p2 := r2.Get("example-notes"); p2.Record.Remote == nil || p2.Dir != pkgDir || p2.Problem != "" {
		t.Fatalf("after a restart: %+v %s %q", p2.Record.Remote, p2.Dir, p2.Problem)
	}

	if _, err := r.Attach(ctx, "example-notes", "remote:rpc=/tmp/example-notes-2.sock", "cli"); err != nil || r.Get("example-notes").Record.Remote.RPCSocket != "/tmp/example-notes-2.sock" {
		t.Fatal("attaching again moves it to the new socket:", err)
	}
	p, err = r.Detach(ctx, "example-notes", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.Remote != nil || store.rows["example-notes"].Remote != nil {
		t.Fatal("still attached")
	}
	kept("detached", p)
	if last := (*changes)[len(*changes)-1]; last.Kind != "detached" || last.Old.Record.Remote == nil {
		t.Fatalf("%+v", last)
	}
	var audit []string
	for _, a := range store.audit {
		if strings.HasPrefix(a, "example-notes attach ") || strings.HasPrefix(a, "example-notes detach ") {
			audit = append(audit, strings.Fields(a)[1])
		}
	}
	if !slices.Equal(audit, []string{"attach", "attach", "detach"}) {
		t.Fatal(store.audit)
	}
	if _, err := r.Preview(ctx, "dir:"+pkg2, "example-notes"); err != nil {
		t.Fatal("upgrade once detached:", err)
	}
}

// Attaching holds the program to the package: the sockets its runtime uses, the same id and the same version. A
// refusal changes nothing.
func TestAttachRefusesAProgramThatIsNotThePackages(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	installNotes(t, r)
	id, version := "example-notes", "1.2.0"
	var unreachable error
	r.RemoteManifest = func(context.Context, Remote) (*Manifest, error) {
		if unreachable != nil {
			return nil, unreachable
		}
		return Parse(with(t, func(m map[string]any) { m["id"], m["version"] = id, version }))
	}
	for _, c := range []struct {
		name, source, key string
		set               func()
	}{
		{"not sockets", "dir:/tmp/example-notes", "extension-status.attach_source", nil},
		{"no protocol socket for a program that speaks it", "remote:http=/tmp/h.sock", "extension-status.attach_sockets", nil},
		{"an HTTP socket it does not serve", "remote:rpc=/tmp/r.sock,http=/tmp/h.sock", "extension-status.attach_sockets", nil},
		{"another program", "remote:rpc=/tmp/r.sock", "extension-status.wrong_id", func() { id = "other-notes" }},
		{"another version", "remote:rpc=/tmp/r.sock", "extension-status.wrong_version", func() { version = "1.3.0" }},
		{"nothing on the socket", "remote:rpc=/tmp/r.sock", "extension-status.attach_unreachable", func() { unreachable = errors.New("connection refused") }},
	} {
		id, version, unreachable = "example-notes", "1.2.0", nil
		if c.set != nil {
			c.set()
		}
		if _, err := r.Attach(ctx, "example-notes", c.source, "cli"); messageKey(err) != c.key {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.Get("example-notes").Record.Remote != nil || store.rows["example-notes"].Remote != nil {
			t.Fatalf("%s: attached anyway", c.name)
		}
	}
	if _, err := r.Attach(ctx, "nothing-here", "remote:rpc=/tmp/r.sock", "cli"); messageKey(err) != "extension-status.extension_missing" {
		t.Fatal(err)
	}
	if _, err := r.Detach(ctx, "example-notes", "cli"); messageKey(err) != "extension-status.detach_not_attached" {
		t.Fatal(err)
	}
}

// One installed from remote: has no package to run or keep, whatever its origin says (a row from before origins were
// kept has none), and one without a program has nothing to attach.
func TestAttachNeedsAPackageWithAProgram(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	r.RemoteManifest = func(context.Context, Remote) (*Manifest, error) {
		return Parse(with(t, func(m map[string]any) { m["id"] = "remote-notes" }))
	}
	pv, err := r.Preview(ctx, "remote:rpc=/tmp/remote-notes.sock", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Attach(ctx, "remote-notes", "remote:rpc=/tmp/other.sock", "cli"); messageKey(err) != "extension-status.attach_remote_install" {
		t.Fatal(err)
	}
	if _, err := r.Detach(ctx, "remote-notes", "cli"); messageKey(err) != "extension-status.attach_remote_install" {
		t.Fatal(err)
	}
	if p := r.Get("remote-notes"); p.Dir != "" || p.Record.AttachedPackage() {
		t.Fatalf("a remote: install has no package: %q", p.Dir)
	}
	row := store.rows["remote-notes"]
	row.Origin = ""
	store.rows["remote-notes"] = row
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p := r2.Get("remote-notes"); p.Dir != "" || p.Problem != "" || p.Record.AttachedPackage() {
		t.Fatalf("with no origin: %q %q", p.Dir, p.Problem)
	}

	installDir(t, r, func(m map[string]any) { m["id"] = "no-program" })
	if _, err := r.Attach(ctx, "no-program", "remote:rpc=/tmp/r.sock", "cli"); messageKey(err) != "extension-status.attach_no_program" {
		t.Fatal(err)
	}
}

// A source runtime's program is the manager's to run (in its isolate), so its package cannot be attached.
func TestAttachRefusesASourceRuntime(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	installNotes(t, r)
	r.mu.Lock()
	p := r.extensions["example-notes"]
	rt := *p.Full.Runtime
	rt.Mode, rt.Source = RuntimeDeno, &SourceRuntime{Entry: "src/main.ts", Bundle: "bundle/main.mjs"}
	p.Full.Runtime = &rt
	r.mu.Unlock()
	r.RemoteManifest = func(context.Context, Remote) (*Manifest, error) { return Parse(with(t, func(map[string]any) {})) }
	if _, err := r.Attach(ctx, "example-notes", "remote:rpc=/tmp/example-notes.sock", "cli"); messageKey(err) != "extension-status.attach_no_program" {
		t.Fatal(err)
	}
	if store.rows["example-notes"].Remote != nil || r.Get("example-notes").Record.Remote != nil {
		t.Fatal("attached")
	}
}

// An upgrade previewed before the package was attached is not installed over it once attached.
func TestAttachedPackageRefusesAnUpgradePreviewedBefore(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	installNotes(t, r)
	pkg2 := t.TempDir()
	writePackage(t, pkg2, func(m map[string]any) { m["version"] = "1.3.0" })
	pv, err := r.Preview(ctx, "dir:"+pkg2, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	r.RemoteManifest = func(context.Context, Remote) (*Manifest, error) { return Parse(with(t, func(map[string]any) {})) }
	if _, err := r.Attach(ctx, "example-notes", "remote:rpc=/tmp/example-notes.sock", "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "cli"); messageKey(err) != "extension-status.attached_detach_first" {
		t.Fatal("the upgrade previewed before:", err)
	}
	if row := store.rows["example-notes"]; row.Version != "1.2.0" || !row.AttachedPackage() {
		t.Fatalf("%s %+v", row.Version, row.Remote)
	}
}

// Detaching runs the package again, so it has to be there.
func TestDetachNeedsThePackage(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	pkgDir, _ := installNotes(t, r)
	r.RemoteManifest = func(context.Context, Remote) (*Manifest, error) { return Parse(with(t, func(map[string]any) {})) }
	if _, err := r.Attach(ctx, "example-notes", "remote:rpc=/tmp/example-notes.sock", "cli"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(pkgDir); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Detach(ctx, "example-notes", "cli"); messageKey(err) != "extension-status.package_missing" {
		t.Fatal(err)
	}
	if r.Get("example-notes").Record.Remote == nil {
		t.Fatal("detached without its package")
	}
}
