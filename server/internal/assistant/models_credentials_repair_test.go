package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func corruptProviderCredential(t *testing.T, st *memStore, index int, field string) []storedLauncher {
	t.Helper()
	list := storedProviders(t, st)
	switch field {
	case "key":
		list[index].KeySealed = "invalid-synthetic-ciphertext"
	case "headers":
		list[index].HeadersSealed = "invalid-synthetic-ciphertext"
	case "environment":
		list[index].EnvSealed = "invalid-synthetic-ciphertext"
	default:
		t.Fatalf("unknown credential field %s", field)
	}
	if err := st.PutSetting(context.Background(), launchersManagedKey, list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestProviderCredentialRepairPreservesSealedRecords(t *testing.T) {
	for _, tc := range []struct {
		name, field, body, plain string
		index                    int
	}{
		{"API key", "key", `{"kind":"api","api":"responses","base_url":"https://api.example.com/v1","model":"original","api_key":"synthetic-replacement-key"}`, "synthetic-replacement-key", 0},
		{"launcher key", "key", `{"base_url":"https://gw.example.com","model":"original","args":["--verbose"],"api_key":"synthetic-replacement-key"}`, "synthetic-replacement-key", 1},
		{"launcher headers", "headers", `{"base_url":"https://gw.example.com","model":"original","args":["--verbose"],"headers":"X-Test: synthetic-replacement-header"}`, "X-Test: synthetic-replacement-header", 1},
		{"launcher environment", "environment", `{"base_url":"https://gw.example.com","model":"original","args":["--verbose"],"env":"X_TEST_TOKEN=synthetic-replacement-env"}`, "X_TEST_TOKEN=synthetic-replacement-env", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, st := providerReadFixture(t)
			before := corruptProviderCredential(t, st.memStore, tc.index, tc.field)
			pushes := len(providerPushes(f))
			r := request(s, "PUT", "/api/v1/launchers/"+before[tc.index].Name, tc.body)
			if r.Code != http.StatusOK {
				t.Fatalf("explicit credential repair: %d %s", r.Code, r.Body)
			}
			after := storedProviders(t, st.memStore)
			if len(after) != len(before) {
				t.Fatal("repair lost a provider")
			}
			want := before[tc.index]
			want.Updated = after[tc.index].Updated
			var sealed string
			switch tc.field {
			case "key":
				sealed = after[tc.index].KeySealed
				want.KeySealed = sealed
			case "headers":
				sealed = after[tc.index].HeadersSealed
				want.HeadersSealed = sealed
			case "environment":
				sealed = after[tc.index].EnvSealed
				want.EnvSealed = sealed
			}
			if plain, err := s.openKey(sealed); err != nil || plain != tc.plain {
				t.Fatalf("replacement credential was not sealed: %v", err)
			}
			if !reflect.DeepEqual(want, after[tc.index]) || !reflect.DeepEqual(before[1-tc.index], after[1-tc.index]) {
				t.Fatal("repair changed another sealed record or unrelated field")
			}
			if len(providerPushes(f)) != pushes+1 {
				t.Fatal("valid repaired snapshot was not published")
			}
		})
	}
}

func TestProviderCredentialRemovalPreservesHealthyProvider(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run(fmt.Sprintf("provider %d", index), func(t *testing.T) {
			s, f, st := providerReadFixture(t)
			if index == 1 {
				for _, id := range []string{"kb", "inbox.triage"} {
					if err := st.memStore.PutSetting(context.Background(), modelRecordKey(id), modelRecord{Binding: ModelBinding{Provider: "launcher.old_api"}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := corruptProviderCredential(t, st.memStore, index, "key")
			count := len(providerPushes(f))
			r := request(s, "DELETE", "/api/v1/launchers/"+before[index].Name, "")
			if r.Code != http.StatusOK {
				t.Fatalf("remove unused corrupt provider: %d %s", r.Code, r.Body)
			}
			after := storedProviders(t, st.memStore)
			if len(after) != 1 || !reflect.DeepEqual(before[1-index], after[0]) {
				t.Fatal("removal changed the complete surviving record")
			}
			if len(providerPushes(f)) != count+1 {
				t.Fatal("valid remaining snapshot was not published")
			}
		})
	}
}

func TestProviderCredentialFailureRetainsRecordsAndNodeConfiguration(t *testing.T) {
	for _, field := range []string{"key", "headers", "environment"} {
		t.Run(field, func(t *testing.T) {
			s, f, st := providerReadFixture(t)
			before := corruptProviderCredential(t, st.memStore, 1, field)
			count := len(providerPushes(f))
			var logs []string
			s.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
			r := request(s, "PUT", "/api/v1/launchers/old_api", providerEditBody)
			if r.Code != http.StatusOK {
				t.Fatalf("edit another readable record: %d %s", r.Code, r.Body)
			}
			got := storedProviders(t, st.memStore)
			want := before[0]
			want.Updated, want.Model = got[0].Updated, "edited"
			if !reflect.DeepEqual(want, got[0]) || !reflect.DeepEqual(before[1], got[1]) {
				t.Fatal("unrelated edit changed the corrupt sealed record")
			}
			s.pushManaged(context.Background(), "")
			s.pushManaged(context.Background(), "server") // the newly trusted computer's publication path
			if calls := s.managedNodeCalls(context.Background(), "server"); len(calls) != 0 {
				t.Fatal("credential failure produced reconnect configuration")
			}
			if len(providerPushes(f)) != count {
				t.Fatal("credential failure cleared or partially published node providers")
			}
			if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "node publication deferred") {
				t.Fatal("skipped publication was not reported")
			}
			repair := map[string]any{"base_url": "https://gw.example.com", "model": "original", "args": []string{"--verbose"}}
			switch field {
			case "key":
				repair["api_key"] = "synthetic-recovered-key"
			case "headers":
				repair["headers"] = "X-Test: synthetic-recovered-header"
			case "environment":
				repair["env"] = "X_TEST_TOKEN=synthetic-recovered-env"
			}
			body, err := json.Marshal(repair)
			if err != nil {
				t.Fatal(err)
			}
			r = request(s, "PUT", "/api/v1/launchers/old_launcher", string(body))
			if r.Code != http.StatusOK || len(providerPushes(f)) != count+1 {
				t.Fatalf("publication did not recover after repair: %d %s", r.Code, r.Body)
			}
		})
	}
}

func TestProviderCredentialImportKeepsHeadersAndEnvironmentOpaque(t *testing.T) {
	for _, field := range []string{"key", "headers", "environment"} {
		t.Run(field, func(t *testing.T) {
			s, f, st := providerReadFixture(t)
			before := corruptProviderCredential(t, st.memStore, 1, field)
			settings, writes := providerSettings(st.memStore)
			pushes := providerPushes(f)
			preview := request(s, "GET", "/api/v1/launchers/import?machine=server", "")
			commit := request(s, "POST", "/api/v1/launchers/import", providerImportBody)
			if field == "key" {
				if preview.Code < 500 || commit.Code < 500 {
					t.Fatal("import silently discarded an unreadable matching key")
				}
				after, n := providerSettings(st.memStore)
				if writes != n || !reflect.DeepEqual(settings, after) {
					t.Fatal("failed key matching changed durable records")
				}
			} else {
				if preview.Code != http.StatusOK || commit.Code != http.StatusOK {
					t.Fatalf("opaque unrelated credentials blocked import: preview=%d commit=%d", preview.Code, commit.Code)
				}
				if !strings.Contains(preview.Body.String(), `"existing":"old_launcher"`) {
					t.Fatal("readable key did not match the existing provider")
				}
				after := storedProviders(t, st.memStore)
				if len(after) != 3 {
					t.Fatalf("import retained %d providers, want three", len(after))
				}
				want := before[1]
				want.Updated, want.Models = after[1].Updated, after[1].Models
				if !reflect.DeepEqual(before[0], after[0]) || !reflect.DeepEqual(want, after[1]) {
					t.Fatal("import changed opaque sealed credentials or unrelated fields")
				}
			}
			if !reflect.DeepEqual(pushes, providerPushes(f)) {
				t.Fatal("import published a partial or empty credential configuration")
			}
		})
	}
}

func TestProviderCredentialProfilePreservesOpaqueRecords(t *testing.T) {
	s, f, st := providerReadFixture(t)
	before := corruptProviderCredential(t, st.memStore, 1, "key")
	pushes := providerPushes(f)
	_, err := s.saveModelProfile(context.Background(), nil, "synthetic", ProfileActor{}, func(p *ModelProfile, _ bool) error {
		p.Name = "Synthetic profile"
		p.Launchers = map[string]LauncherModels{"old_launcher": {Model: profileValue("profile-model")}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyModelProfile(context.Background(), "synthetic", ProfileActor{}); err != nil {
		t.Fatal(err)
	}
	after := storedProviders(t, st.memStore)
	want := before[1]
	want.Model, want.Updated = "profile-model", after[1].Updated
	if !reflect.DeepEqual(before[0], after[0]) || !reflect.DeepEqual(want, after[1]) {
		t.Fatal("profile changed opaque sealed records beyond the requested model")
	}
	if !reflect.DeepEqual(pushes, providerPushes(f)) {
		t.Fatal("profile notification published broken credentials")
	}
	if _, err := s.rollbackModelProfile(context.Background(), ProfileActor{}); err != nil {
		t.Fatal(err)
	}
	rolled := storedProviders(t, st.memStore)
	want.Model, want.Updated = before[1].Model, rolled[1].Updated
	if !reflect.DeepEqual(before[0], rolled[0]) || !reflect.DeepEqual(want, rolled[1]) || !reflect.DeepEqual(pushes, providerPushes(f)) {
		t.Fatal("profile rollback changed opaque credentials or node configuration")
	}
}
