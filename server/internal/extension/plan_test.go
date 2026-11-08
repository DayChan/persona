package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type blockingPlanStore struct {
	*memStore
	fail             atomic.Bool
	entered, release chan struct{}
	commits          atomic.Int32
}

func (s *blockingPlanStore) PutExtensionPlan(_ context.Context, records []Record) error {
	if s.fail.CompareAndSwap(true, false) {
		close(s.entered)
		<-s.release
		return errors.New("synthetic commit failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		s.rows[record.StorageKey()] = record
	}
	s.commits.Add(1)
	return nil
}

type uncertainPlanStore struct {
	*blockingPlanStore
	unknown bool
}

func (s *uncertainPlanStore) PutExtensionPlan(ctx context.Context, rows []Record) error {
	if err := s.blockingPlanStore.PutExtensionPlan(ctx, rows); err != nil {
		return err
	}
	if s.unknown {
		return &CommitUnknownError{Err: errors.New("synthetic lost commit reply")}
	}
	return nil
}

func TestPlanUnknownCommitDoesNotPretendToRestoreDatabase(t *testing.T) {
	store := &uncertainPlanStore{blockingPlanStore: &blockingPlanStore{memStore: newMemStore()}}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	installDependency(t, reg, "example")
	pv, err := reg.Preview(t.Context(), dependencyPackage(t, "example", "2.0.0", nil, nil), "example")
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	reg.OnChange = func(Change) { called++ }
	store.unknown = true
	if _, err = reg.UpgradePlan(t.Context(), "example", pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Fatal("unknown commit presented as success")
	}
	if store.rows["example"].Version != "2.0.0" || reg.Snapshot("example").Record.Version != "1.0.0" || called != 0 {
		t.Fatal("unknown commit published or rewrote database")
	}
	if _, err = os.Stat(filepath.Join(reg.installedDir("example", "2.0.0"), ManifestFile)); err != nil {
		t.Fatal("possibly committed package removed:", err)
	}
	if err = reg.SetEnabled(t.Context(), "example", false, "test"); err == nil {
		t.Fatal("uncertain records were overwritten by a later mutation")
	}
	if _, err = reg.UpgradePlan(t.Context(), "example", pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Fatal("unknown plan was blindly retried")
	}
	loaded := NewRegistry(reg.dir, store, rot13{}, 202611100000)
	if err = loaded.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p := loaded.Snapshot("example"); p.Record.Version != "2.0.0" || p.Problem != "" {
		t.Fatal("reload cannot reconcile committed row and preserved package")
	}
}

func TestPlanMutationCallbacksCanReenterAfterCommit(t *testing.T) {
	reg := NewRegistry(t.TempDir(), newMemStore(), rot13{}, 202611100000)
	installDependency(t, reg, "example")
	reg.OnChange = func(change Change) {
		if change.Kind == "disabled" {
			if err := reg.SetEnabled(t.Context(), "example", true, "test"); err != nil {
				t.Error(err)
			}
		}
	}
	done := make(chan error, 1)
	go func() { done <- reg.SetEnabled(t.Context(), "example", false, "test") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnChange runs under operation lock")
	}
	if !reg.Snapshot("example").Record.Enabled {
		t.Fatal("callback mutation did not apply")
	}
}

func TestPlanPreparationFailureRestoresEarlierPackages(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore()}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	installDependency(t, reg, "first")
	before := reg.Snapshot("first").Record
	commits := store.commits.Load()
	first, err := reg.Preview(t.Context(), dependencyPackage(t, "first", "2.0.0", nil, nil), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reg.Preview(t.Context(), dependencyPackage(t, "second", "1.0.0", nil, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	main, err := reg.Preview(t.Context(), dependencyPackage(t, "main", "1.0.0", []Dependency{{ID: "first"}, {ID: "second"}}, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	main.Bundle = []*Preview{first, second}
	blocked := filepath.Dir(reg.installedDir("second", "1.0.0"))
	if err = os.WriteFile(blocked, []byte("synthetic path obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs := []PreviewRef{{PreviewID: first.ID, Digest: first.Digest}, {PreviewID: second.ID, Digest: second.Digest}}
	if _, err = reg.InstallPlan(t.Context(), main.ID, main.Digest, refs, "test"); err == nil {
		t.Fatal("package preparation failure accepted")
	}
	if store.commits.Load() != commits || !reflect.DeepEqual(before, reg.Snapshot("first").Record) {
		t.Fatal("preparation failure changed records")
	}
	if _, err = os.Stat(filepath.Join(first.root, ManifestFile)); err != nil {
		t.Fatal("earlier package not restored to preview:", err)
	}
	if _, err = os.Stat(reg.installedDir("first", "2.0.0")); !os.IsNotExist(err) {
		t.Fatal("new package left installed after preparation failure")
	}
	if err = os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.InstallPlan(t.Context(), main.ID, main.Digest, refs, "test"); err != nil {
		t.Fatal("preparation failure no longer retryable:", err)
	}
}

func TestPlanRecoveryDoesNotReplaceAnotherCommittedUpgrade(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore(), entered: make(chan struct{}), release: make(chan struct{})}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	installDependency(t, reg, "example")
	var previews []*Preview
	for i := 0; i < 2; i++ {
		pv, err := reg.Preview(t.Context(), dependencyPackage(t, "example", "2.0.0", nil, nil), "example")
		if err != nil {
			t.Fatal(err)
		}
		previews = append(previews, pv)
	}
	store.fail.Store(true)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		_, err := reg.Upgrade(t.Context(), "example", previews[0].ID, previews[0].Digest, "test")
		first <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first commit did not reach injected failure")
	}
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := reg.Upgrade(t.Context(), "example", previews[1].ID, previews[1].Digest, "test")
		second <- err
	}()
	<-started
	close(store.release)
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("first plan did not fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first plan stuck")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal("second upgrade:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second plan stuck")
	}
	p := reg.Snapshot("example")
	if p.Record.Version != "2.0.0" || store.rows["example"].Version != "2.0.0" {
		t.Fatal("recovery overwrote the other committed version")
	}
	if _, err := os.Stat(filepath.Join(p.Dir, ManifestFile)); err != nil {
		t.Fatal("recovery removed the committed package:", err)
	}
	if p.Record.PreviousVersion != "1.0.0" {
		t.Fatal("previous version not retained")
	}
	if reg.PreviewOf(previews[0].ID) == nil {
		t.Fatal("failed plan no longer retryable")
	}
}

func TestPlanRejectsRemoteManifestChangedAfterConsent(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore()}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	calls := 0
	reg.RemoteManifest = func(context.Context, Remote) (*Manifest, error) {
		calls++
		raw := []byte(`{"manifest_version":1,"id":"remote-example","name":"Remote","version":"1.0.0","contributes":{"providers":[{"id":"remote-provider","name":"Remote","base_url":"https://provider.example.com"}]}}`)
		m, err := Parse(raw)
		if calls > 1 {
			m.Name = "Changed after consent"
		}
		return m, err
	}
	pv, err := reg.Preview(t.Context(), "remote:rpc=/tmp/example-rpc.sock", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.InstallPlan(t.Context(), pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Fatal("changed remote manifest accepted")
	}
	if store.commits.Load() != 0 || reg.Get("remote-example") != nil {
		t.Fatal("remote prevalidation failure wrote state")
	}
}

func TestPackageFingerprintSeparatesFileBoundaries(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	if err := os.WriteFile(a, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := packageFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(a, []byte("first\x00b\x00100644\x00second"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := packageFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("file bytes could impersonate the next path/header")
	}
}

func TestPlanRejectsStagedPackageChangedAfterConsent(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore()}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	dir := dependencyPackage(t, "example", "1.0.0", nil, nil)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("original package content"), 0o644); err != nil {
		t.Fatal(err)
	}
	pv, err := reg.Preview(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(pv.root, "README.md"), []byte("changed after consent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.InstallPlan(t.Context(), pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Error("package bytes changed after consent were accepted")
	}
	if reg.Get("example") != nil || store.commits.Load() != 0 {
		t.Error("tampered package changed installed state")
	}
}

func TestPlanRechecksInstalledTransitiveRequirements(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore()}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	installDependency(t, reg, "branch")
	pv, err := reg.Preview(t.Context(), dependencyPackage(t, "main", "1.0.0", []Dependency{{ID: "branch"}}, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	installDependency(t, reg, "new-child")
	branch, err := reg.Preview(t.Context(), dependencyPackage(t, "branch", "2.0.0", []Dependency{{ID: "new-child", Version: ">=2.0.0"}}, nil), "branch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Upgrade(t.Context(), "branch", branch.ID, branch.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	before := store.commits.Load()
	if _, err = reg.InstallPlan(t.Context(), pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Fatal("installed branch changed after preview but its stricter child requirement was not checked")
	}
	if reg.Get("main") != nil || store.commits.Load() != before {
		t.Fatal("transitive validation failure wrote state")
	}
}

func TestPlanChecksSavedConfigWithoutRequiringNewSettings(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			store := &blockingPlanStore{memStore: newMemStore()}
			reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
			configuration := func(kind string) func(map[string]any) {
				return func(m map[string]any) {
					m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{"saved": map[string]any{"type": kind}, "new_setting": map[string]any{"type": "string"}}, "required": []string{"new_setting"}}
				}
			}
			pv, err := reg.Preview(t.Context(), dependencyPackage(t, "example", "1.0.0", nil, configuration("string")), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reg.Install(t.Context(), pv.ID, pv.Digest, "test"); err != nil {
				t.Fatal(err)
			}
			row := store.rows["example"]
			row.Config = json.RawMessage(`{"saved":"retained"}`)
			if err = store.PutExtension(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			reg = NewRegistry(reg.dir, store, rot13{}, 202611100000)
			if err = reg.Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := reg.Snapshot("example").Record
			kind := "integer"
			if compatible {
				kind = "string"
			}
			pv, err = reg.Preview(t.Context(), dependencyPackage(t, "example", "2.0.0", nil, configuration(kind)), "example")
			if err != nil {
				t.Fatal(err)
			}
			_, err = reg.UpgradePlan(t.Context(), "example", pv.ID, pv.Digest, nil, "test")
			if compatible {
				if err != nil {
					t.Fatal("missing new setting should remain needs_setup:", err)
				}
				if len(reg.Missing(t.Context(), reg.Get("example"))) == 0 {
					t.Fatal("missing setting was silently filled")
				}
			} else {
				if err == nil {
					t.Fatal("incompatible saved setting accepted")
				}
				if !reflect.DeepEqual(before, reg.Snapshot("example").Record) {
					t.Fatal("invalid config upgrade changed state")
				}
			}
		})
	}
}

func TestPlanPublishesOnlyAfterEveryRecordCommits(t *testing.T) {
	store := &blockingPlanStore{memStore: newMemStore()}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	var members []*Preview
	for _, id := range []string{"first", "second"} {
		pv, err := reg.Preview(t.Context(), dependencyPackage(t, id, "1.0.0", nil, nil), "")
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, pv)
	}
	mainDir := dependencyPackage(t, "main", "1.0.0", []Dependency{{ID: "first"}, {ID: "second"}}, nil)
	main, err := reg.Preview(t.Context(), mainDir, "")
	if err != nil {
		t.Fatal(err)
	}
	main.Bundle = members
	called := 0
	reg.OnChange = func(change Change) {
		called++
		if store.commits.Load() != 1 {
			t.Error("publish happened before database commit")
		}
		for _, id := range []string{"first", "second", "main"} {
			if reg.Snapshot(id) == nil || store.rows[id].ID == "" {
				t.Error("partial registry/database visible at publish:", id)
			}
		}
	}
	refs := []PreviewRef{{PreviewID: members[0].ID, Digest: members[0].Digest}, {PreviewID: members[1].ID, Digest: members[1].Digest}}
	if _, err = reg.InstallPlan(t.Context(), main.ID, main.Digest, refs, "test"); err != nil {
		t.Fatal(err)
	}
	if called != 3 {
		t.Fatalf("OnChange calls=%d", called)
	}
}
