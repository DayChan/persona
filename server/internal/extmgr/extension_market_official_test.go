package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

// useOfficialMarket makes spec, signed by key, the official market for one test (TestMain turns it off for the rest).
func useOfficialMarket(t *testing.T, spec, key string) {
	was := extension.Official
	extension.Official = extension.OfficialMarket{Spec: spec, SigningKey: key}
	t.Cleanup(func() { extension.Official = was })
}

// waitMarketsRead waits for the background reads of the markets to end.
func waitMarketsRead(t *testing.T, h *Manager) {
	t.Helper()
	ms := h.marketsState()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		ms.mu.Lock()
		n := len(ms.reading)
		ms.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the markets are still being read")
		}
	}
}

// reloadMarkets makes h read its markets from the store again, as a restarted (or upgraded) hub does.
func reloadMarkets(h *Manager) {
	ms := h.marketsState()
	ms.mu.Lock()
	ms.loaded, ms.byID = false, nil
	ms.mu.Unlock()
}

func marketList(t *testing.T, h *Manager, phone string) []map[string]any {
	t.Helper()
	code, out := call(t, h.routes(), "GET", "/api/v1/extensions/-/markets", phone, "")
	if code != 200 {
		t.Fatalf("markets: %d %v", code, out)
	}
	var list []map[string]any
	b, _ := json.Marshal(out["markets"])
	_ = json.Unmarshal(b, &list)
	return list
}

// The official market (#2351): a hub upgraded from before it gets it once, with its pinned key, listed before the
// owner's own markets (which stay); a restart does not add it twice, and one its owner removed does not come back.
func TestOfficialExtensionMarketIsAddedOnceAndFirst(t *testing.T) {
	h, phone, _ := marketHub(t)
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: map[string][]byte{"example-ui-1.0.0.tar.gz": pkg}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	key := newMarketKey(t)
	// Its index calls itself after its publisher; the apps are shown "Official extensions" all the same (#5017).
	ix, _ := json.Marshal(map[string]any{"market_version": 1, "name": "Example 扩展", "signing_key": key.text,
		"extensions": []any{marketEntry(t, dir, "release:v1/example-ui-1.0.0.tar.gz", pkg)}})
	gh.files[extension.IndexFile] = key.sign(ix)
	useOfficialMarket(t, "github:example/plugins", key.text)

	// The owner's market, added long before (an internal one): it stays, after the official one.
	st := h.marketStore()
	now := time.Now()
	own := extension.MarketRecord{ID: "0123456789", Spec: "https://plugins.example.com/team/persona-extensions.json", Name: "团队扩展",
		AddedAt: now.Add(-72 * time.Hour), FetchedAt: &now, Warnings: []string{}}
	if err := st.PutExtensionMarket(context.Background(), own); err != nil {
		t.Fatal(err)
	}

	list := marketList(t, h, phone)
	waitMarketsRead(t, h)
	if len(list) != 2 || list[0]["spec"] != "github:example/plugins" || list[0]["official"] != true || list[1]["id"] != own.ID || list[1]["official"] != nil {
		t.Fatalf("the official market is not first, before the owner's: %v", list)
	}
	official := fmt.Sprint(list[0]["id"])
	// Its index was read in the background and verifies with the pinned key.
	list = marketList(t, h, phone)
	if s, _ := list[0]["signature"].(map[string]any); list[0]["name"] != "官方扩展" || s["state"] != extension.SigSigned || s["key_id"] != keyIDText(t, key.text) {
		t.Fatalf("the official market's index is not read with its pinned key: %v", list[0])
	}
	// The catalog lists its extensions first too.
	if code, out := call(t, h.routes(), "GET", "/api/v1/extensions/-/catalog", phone, ""); code != 200 {
		t.Fatalf("catalog: %d %v", code, out)
	} else if items, _ := out["extensions"].([]any); len(items) != 1 || items[0].(map[string]any)["market"] != official || items[0].(map[string]any)["market_name"] != "官方扩展" {
		t.Fatalf("catalog: %v", out["extensions"])
	}
	waitMarketsRead(t, h)

	// Restarted: still one official market.
	reloadMarkets(h)
	if list = marketList(t, h, phone); len(list) != 2 || list[0]["id"] != official {
		t.Fatalf("a restart added the official market again: %v", list)
	}
	// Adding it by hand, however it is written, is adding it twice.
	for _, spec := range []string{"github:example/plugins", "https://github.com/Example/plugins", "github:example/plugins@main"} {
		if code, out := call(t, h.routes(), "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, spec)); code != 400 {
			t.Fatalf("%s added a second official market: %d %v", spec, code, out)
		}
	}

	// Removed by its owner: it does not come back.
	if code, out := call(t, h.routes(), "DELETE", "/api/v1/extensions/-/markets/"+official, phone, ""); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	reloadMarkets(h)
	if list = marketList(t, h, phone); len(list) != 1 || list[0]["id"] != own.ID {
		t.Fatalf("a removed official market came back: %v", list)
	}
	waitMarketsRead(t, h)
}

// The owner added the official repository by hand before #2351 (another spelling, a branch): that one is the official
// market, first, and no second one is added.
func TestOfficialExtensionMarketAddedByHandIsNotAddedAgain(t *testing.T) {
	h, phone, _ := marketHub(t)
	server := httptest.NewTLSServer(&fakeGitHub{files: map[string][]byte{}, assets: map[string][]byte{}})
	defer server.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = server.Client(), server.URL
	useOfficialMarket(t, "github:example/plugins", newMarketKey(t).text)
	now := time.Now()
	st := h.marketStore()
	own := extension.MarketRecord{ID: "aaaaaaaaaa", Spec: "https://plugins.example.com/persona-extensions.json", AddedAt: now.Add(-2 * time.Hour), FetchedAt: &now, Warnings: []string{}}
	byHand := extension.MarketRecord{ID: "bbbbbbbbbb", Spec: "github:Example/plugins@main", AddedAt: now.Add(-time.Hour), FetchedAt: &now, Warnings: []string{}}
	for _, r := range []extension.MarketRecord{own, byHand} {
		if err := st.PutExtensionMarket(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	list := marketList(t, h, phone)
	waitMarketsRead(t, h)
	if len(list) != 2 || list[0]["id"] != byHand.ID || list[0]["official"] != true || list[1]["id"] != own.ID {
		t.Fatalf("the official repository added by hand: %v", list)
	}
}

// The official market's key is pinned, not trusted on first use: an index signed by another key is refused.
func TestOfficialExtensionMarketKeyIsPinned(t *testing.T) {
	h, phone, _ := marketHub(t)
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: map[string][]byte{}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	pinned, other := newMarketKey(t), newMarketKey(t)
	ix, _ := json.Marshal(map[string]any{"market_version": 1, "name": "冒充的", "signing_key": other.text, "extensions": []any{}})
	gh.files[extension.IndexFile] = other.sign(ix)
	useOfficialMarket(t, "github:example/plugins", pinned.text)

	list := marketList(t, h, phone)
	waitMarketsRead(t, h)
	code, out := call(t, h.routes(), "POST", "/api/v1/extensions/-/markets/"+fmt.Sprint(list[0]["id"])+"/refresh", phone, "")
	if s, _ := out["signature"].(map[string]any); code == 200 || out["code"] != "market_signature" {
		t.Fatalf("an index signed by another key was taken: %d %v %v", code, out, s)
	}
	if list = marketList(t, h, phone); list[0]["name"] == "冒充的" {
		t.Fatalf("the official market took an index its pinned key refuses: %v", list[0])
	}
}
