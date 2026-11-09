package extmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// Exercise the exact built-in registration used by attachExtensions, without
// starting an iLink process or using credentials from the test machine.
func TestWeChatBuiltinHandoffPreservesOwnershipAndSecrets(t *testing.T) {
	h, store := extensionTestHub(t)
	r := h.extensions.reg
	r.OnChange = nil
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "personad-wechat")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := wechatManifest(h, path)
	if err := h.addWeChatBuiltin(r, path); err != nil {
		t.Fatal(err)
	}
	before := r.Get("wechat")
	if before == nil || before.Record.Source != extension.SourceBuiltin || !before.Record.Enabled || before.Dir != filepath.Dir(path) || !reflect.DeepEqual(before.Manifest, want) {
		t.Fatal("registration changed the un-replaced built-in")
	}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.Get("wechat"); got != before || got.Record.Source != extension.SourceBuiltin {
		t.Fatal("without a package the built-in lost ownership")
	}
	const sealed = "synthetic sealed credential blob"
	if err := store.PutConnectorSecret(ctx, "wechat", sealed); err != nil {
		t.Fatal(err)
	}
	pkg := t.TempDir()
	m := wechatManifest(h, path)
	m.Version = "0.1.0"
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, extension.ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, filepath.Base(path)), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if got := r.Get("wechat"); got == nil || got.Record.Source != extension.SourceInstalled || got.Record.ID != before.Record.ID {
		t.Fatal("same-id package did not replace the built-in")
	}
	if v, kept, err := store.ConnectorSecret(ctx, "wechat"); err != nil || !kept || v != sealed {
		t.Fatal("installation changed connector credentials")
	}
	if err := r.Uninstall(ctx, "wechat", true, "test"); err != nil {
		t.Fatal(err)
	}
	if got := r.Get("wechat"); got == nil || got.Record.Source != extension.SourceBuiltin || !got.Record.Enabled || !reflect.DeepEqual(got.Manifest, before.Manifest) {
		t.Fatal("uninstall did not restore the unchanged built-in")
	}
	if v, kept, err := store.ConnectorSecret(ctx, "wechat"); err != nil || !kept || v != sealed {
		t.Fatal("rollback changed connector credentials")
	}
}
