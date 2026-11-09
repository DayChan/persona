package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The model configuration's reads are memoized per request (withSettingsMemo, models_launchers.go): one read of each
// settings key, the same answers, nothing kept past the request.

// settingReadCounter counts the reads of each settings key on top of a downStore.
type settingReadCounter struct {
	*downStore
	mu    sync.Mutex
	reads map[string]int
}

func (c *settingReadCounter) Setting(ctx context.Context, key string, out any) (bool, error) {
	c.mu.Lock()
	c.reads[key]++
	c.mu.Unlock()
	return c.downStore.Setting(ctx, key, out)
}

// take is the reads counted since the last call, by key.
func (c *settingReadCounter) take() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	reads := c.reads
	c.reads = map[string]int{}
	return reads
}

// memoFixture is Settings › Model configuration with something in each key it reads: three keyed API providers
// (api0 hidden, so the fallback is api1), a hidden ~/.zshrc launcher, kb and an extension's purpose set. Its store
// counts the reads.
func memoFixture(t *testing.T) (*Service, *downStore, *settingReadCounter) {
	t.Helper()
	s, f, st := modelService(t)
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "work", BaseURL: "https://gw.example.com"}}}
	f.modelExts = []assistantwire.ModelExtension{{ID: "curator", Name: "Curator"}}
	f.mu.Unlock()
	key, err := s.sealKey("private-test-key")
	if err != nil {
		t.Fatal(err)
	}
	var list []storedLauncher
	for i := range 3 {
		list = append(list, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: fmt.Sprintf("api%d", i),
			BaseURL: fmt.Sprintf("https://api%d.example.com", i), Model: "m"}, KeySealed: key})
	}
	putLaunchers(t, s, list...)
	if err := s.putSetting(context.Background(), launchersHiddenKey, []string{"api0", "work"}); err != nil {
		t.Fatal(err)
	}
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.api2", Model: "large"})
	setModel(t, s, assistantwire.ExtensionPurposePrefix+"curator", ModelBinding{Provider: "launcher.api1"})
	counter := &settingReadCounter{downStore: st, reads: map[string]int{}}
	s.store = func() Store { return counter }
	return s, st, counter
}

// jsonBody is v as apierr.WriteJSON writes it.
func jsonBody(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// One listing read every provider list and record again for each purpose (42 reads of 8 keys with nothing
// installed); now each key once, and one purpose's page 3 reads.
func TestModelConfigReadsEachSettingOnce(t *testing.T) {
	s, _, counter := memoFixture(t)
	if r := modelRequest(s, "GET", "", ""); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	reads := counter.take()
	for key, n := range reads {
		if n > 1 {
			t.Errorf("GET model-config read %s %d times", key, n)
		}
	}
	for _, key := range []string{launchersManagedKey, launchersHiddenKey, modelRecordKey("kb"), modelRecordKey("extension.curator")} {
		if reads[key] != 1 {
			t.Errorf("GET model-config read %s %d times, want once: %v", key, reads[key], reads)
		}
	}
	// One purpose's page: the providers, the hidden ones and the category's record; an extension's own record besides.
	for _, c := range []struct {
		id   string
		most int
	}{{"kb", 3}, {"inbox.draft", 3}, {"extension.curator", 4}} {
		if r := modelRequest(s, "GET", c.id, ""); r.Code != 200 {
			t.Fatalf("%s: %d %s", c.id, r.Code, r.Body.String())
		}
		reads = counter.take()
		total := 0
		for key, n := range reads {
			if n > 1 {
				t.Errorf("GET purposes/%s read %s %d times", c.id, key, n)
			}
			total += n
		}
		if total > c.most {
			t.Errorf("GET purposes/%s made %d settings reads, want at most %d: %v", c.id, total, c.most, reads)
		}
	}
}

// The memo changes how often the settings are read, not what the pages say: both bodies are byte for byte what
// the same reads straight from the store give.
func TestModelConfigMemoChangesNoAnswer(t *testing.T) {
	s, _, _ := memoFixture(t)
	ctx := context.Background() // no memo: every read reaches the store
	providers := s.modelProviders(ctx)
	ids := slices.Clone(modelConfigIDs)
	for _, x := range s.modelExtensions(ctx) {
		ids = append(ids, assistantwire.ExtensionPurposePrefix+x.StorageKey())
	}
	purposes := []ModelPurpose{}
	for _, id := range ids {
		p, err := s.modelPurpose(ctx, id, providers)
		if err != nil {
			t.Fatal(err)
		}
		purposes = append(purposes, p)
	}
	if got, want := modelRequest(s, "GET", "", "").Body.String(), jsonBody(t, map[string]any{"purposes": purposes, "providers": providers}); got != want {
		t.Fatalf("GET model-config changed with the memo:\n got %s\nwant %s", got, want)
	}
	for _, id := range append(ids, "inbox.draft", "translate") {
		p, err := s.modelPurpose(ctx, id, providers)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := modelRequest(s, "GET", id, "").Body.String(), jsonBody(t, map[string]any{"purpose": p, "providers": providers}); got != want {
			t.Fatalf("GET purposes/%s changed with the memo:\n got %s\nwant %s", id, got, want)
		}
	}
	// What the fixture set shows: kb's own binding, the fallback past the hidden api0, the rc-file launcher hidden.
	var page struct {
		Purposes  []ModelPurpose  `json:"purposes"`
		Providers []ModelProvider `json:"providers"`
	}
	if err := json.Unmarshal([]byte(modelRequest(s, "GET", "", "").Body.String()), &page); err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(page.Providers, func(p ModelProvider) bool { return p.ID == "launcher.work" }); i < 0 || !page.Providers[i].Hidden {
		t.Fatalf("the hidden rc-file launcher: %+v", page.Providers)
	}
	for _, p := range page.Purposes {
		if p.ID == "kb" && (p.Effective.Provider != "launcher.api2" || p.Effective.Model != "large") {
			t.Fatalf("kb: %+v", p.Effective)
		}
		if p.ID == "inbox.triage" && p.Effective.Provider != "launcher.api1" {
			t.Fatalf("inbox.triage falls back past the hidden api0: %+v", p.Effective)
		}
	}
}

// The memo lives as long as its request: a change made between two pages, or by the request that answers with
// the purpose (PUT returns its page after the write), shows in the next page.
func TestModelConfigMemoEndsWithItsRequest(t *testing.T) {
	s, _, _ := memoFixture(t)
	before := purposeOf(t, s, "kb")
	r := modelRequest(s, "PUT", "kb", fmt.Sprintf(`{"revision":%d,"provider":"launcher.api1","model":"small"}`, before.Revision))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	var put struct {
		Purpose ModelPurpose `json:"purpose"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &put); err != nil {
		t.Fatal(err)
	}
	if b := put.Purpose.Effective; b.Provider != "launcher.api1" || b.Model != "small" || put.Purpose.Revision != before.Revision+1 {
		t.Fatalf("PUT answered with the old binding: %+v", put.Purpose)
	}
	if b := purposeOf(t, s, "kb").Effective; b.Provider != "launcher.api1" || b.Model != "small" {
		t.Fatalf("the next page kept the old binding: %+v", b)
	}

	page := func() (providers []ModelProvider, triage ModelBinding) {
		t.Helper()
		var out struct {
			Purposes  []ModelPurpose  `json:"purposes"`
			Providers []ModelProvider `json:"providers"`
		}
		if err := json.Unmarshal(modelRequest(s, "GET", "", "").Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, p := range out.Purposes {
			if p.ID == "inbox.triage" {
				triage = p.Effective
			}
		}
		return out.Providers, triage
	}
	if _, triage := page(); triage.Provider != "launcher.api1" {
		t.Fatalf("inbox.triage did not fall back past the hidden api0: %+v", triage)
	}
	if r := request(s, "PUT", "/api/v1/launchers/api1", `{"kind":"api","base_url":"https://api1.example.com","model":"newer"}`); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	providers, _ := page()
	if i := slices.IndexFunc(providers, func(p ModelProvider) bool { return p.ID == "launcher.api1" }); i < 0 || providers[i].Model != "newer" {
		t.Fatalf("the next page kept the old provider: %+v", providers)
	}
	if r := request(s, "POST", "/api/v1/launchers/api0/restore", ""); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if _, triage := page(); triage.Provider != "launcher.api0" {
		t.Fatalf("the next page kept api0 hidden: %+v", triage)
	}
}

// A failed read is not kept: the next read in the same request tries the store again. A good one is, missing keys
// included, and every caller gets its own copy to change.
func TestSettingsMemoKeepsNoFailure(t *testing.T) {
	s, st, counter := memoFixture(t)
	ctx := withSettingsMemo(context.Background())
	if withSettingsMemo(ctx) != ctx {
		t.Fatal("a second withSettingsMemo started another memo")
	}
	st.failKey(launchersManagedKey, errors.New("database unavailable"))
	st.failKey(modelRecordKey("kb"), errors.New("database unavailable"))
	var list []storedLauncher
	if _, err := memoSetting(ctx, counter, launchersManagedKey, &list); err == nil {
		t.Fatal("the failure was not returned")
	}
	if got := s.storedLaunchers(ctx); got != nil {
		t.Fatalf("read through a failing store: %+v", got)
	}
	if _, err := s.modelRecord(ctx, "kb"); err == nil {
		t.Fatal("the record's failure was not returned")
	}
	st.failKey(launchersManagedKey, nil)
	st.failKey(modelRecordKey("kb"), nil)
	if r, err := s.modelRecord(ctx, "kb"); err != nil || r.Binding.Provider != "launcher.api2" {
		t.Fatalf("the failure was kept: %+v %v", r, err)
	}
	got := s.storedLaunchers(ctx)
	if len(got) != 3 {
		t.Fatalf("the failure was kept: %+v", got)
	}
	got[0].Name = "changed"
	hidden := slices.DeleteFunc(s.hiddenLaunchers(ctx), func(n string) bool { return n == "api0" })
	if again := s.storedLaunchers(ctx); again[0].Name != "api0" {
		t.Fatalf("a caller's change reached the next caller: %+v", again[0])
	}
	if again := s.hiddenLaunchers(ctx); !slices.Equal(again, []string{"api0", "work"}) || !slices.Equal(hidden, []string{"work"}) {
		t.Fatalf("a caller's change reached the next caller: %v (changed %v)", again, hidden)
	}
	var missing []string
	for range 2 {
		if found, err := memoSetting(ctx, counter, "launchers.never", &missing); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
	}
	reads := counter.take()
	// Two failed reads of the providers and one of the record, then one good read each, the missing key's included.
	if reads[launchersManagedKey] != 3 || reads[modelRecordKey("kb")] != 2 || reads[launchersHiddenKey] != 1 || reads["launchers.never"] != 1 {
		t.Fatalf("reads: %v", reads)
	}
	// No memo: every call reads.
	s.storedLaunchers(context.Background())
	s.storedLaunchers(context.Background())
	if n := counter.take()[launchersManagedKey]; n != 2 {
		t.Fatalf("%d reads without a memo", n)
	}
}

// dbSettingReads counts the settings rows read from Postgres (one SELECT each).
type dbSettingReads struct {
	*db.DB
	reads atomic.Int64
}

func (st *dbSettingReads) Setting(ctx context.Context, key string, out any) (bool, error) {
	st.reads.Add(1)
	return st.DB.Setting(ctx, key, out)
}

// BenchmarkModelConfigRoute serves Settings › Model configuration from Postgres with six keyed API providers stored;
// settings_reads/op is the settings rows one request reads.
func BenchmarkModelConfigRoute(b *testing.B) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		b.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(database.Close)
	if err := database.TruncateForTest(ctx); err != nil {
		b.Fatal(err)
	}
	st := &dbSettingReads{DB: database}
	f := newFakeHub().withMachine()
	f.ownModels = true
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: func(string, ...any) {}})
	sealed, err := s.sealKey("sk-bench")
	if err != nil {
		b.Fatal(err)
	}
	var list []storedLauncher
	for i := range 6 {
		list = append(list, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: fmt.Sprintf("api%d", i),
			BaseURL: fmt.Sprintf("https://api%d.example.com", i), Model: "m"}, KeySealed: sealed})
	}
	if err := s.putSetting(ctx, launchersManagedKey, list); err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct{ name, path string }{
		{"list", "/api/v1/model-config"},
		{"purpose_kb", "/api/v1/model-config/purposes/kb"},
	} {
		b.Run(c.name, func(b *testing.B) {
			st.reads.Store(0)
			b.ReportAllocs()
			for b.Loop() {
				rec := httptest.NewRecorder()
				s.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
				if rec.Code != 200 {
					b.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body.String())
				}
			}
			b.ReportMetric(float64(st.reads.Load())/float64(b.N), "settings_reads/op")
		})
	}
}
