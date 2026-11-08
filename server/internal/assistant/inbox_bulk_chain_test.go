package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

type pendingChainIndex struct{}

func TestInboxBulkFilterIncludesIntermediatePendingOpen(t *testing.T) {
	profiles := []struct {
		name     string
		fails    func(int) bool
		wantOpen bool
	}{
		{"filings fail", func(i int) bool { return i%2 == 1 }, true},
		{"reopens fail", func(i int) bool { return i%2 == 0 }, false},
		{"all fail", func(int) bool { return true }, false},
		{"all commit", func(int) bool { return false }, false},
	}
	for _, depth := range []int{2, 4, 8} {
		for _, bulkFirst := range []bool{false, true} {
			for _, action := range []string{bulkDone, bulkDismiss} {
				for _, profile := range profiles {
					t.Run(fmt.Sprintf("depth=%d/bulk=%v/action=%s/%s", depth, bulkFirst, action, profile.name), func(t *testing.T) {
						h, st := newBulkFailHub(t)
						it := filingItem(h, "item")
						h.inbox.file(it, time.Now().Add(-time.Minute), "user")
						h.inbox.closeHandling(it, "user", time.Now().Add(-time.Minute))
						if depth == 8 {
							for len(it.Chain) < chainMax {
								h.inbox.chainAdd(it, InboxChainEntry{Kind: ChainInstructed, Text: "Existing instruction"})
							}
						}
						initialChain, initialSeq := slices.Clone(it.Chain), it.ChainSeq
						arrived, source, tier := filingItem(h, "arrived"), filingItem(h, "source"), filingItem(h, "tier")
						for _, item := range []*InboxItem{it, arrived, source, tier} {
							item.Tier, item.Last = TierFYI, "1"
						}
						source.Source, tier.Tier = "example-source", TierAction
						if failed := h.saveInbox(context.Background(), []*InboxItem{it, arrived, source, tier}); len(failed) != 0 {
							t.Fatal("seeding stored rows failed")
						}
						mux := h.routes()
						type response struct {
							code int
							body map[string]any
						}
						call := func(index int) response {
							handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), pendingChainIndex{}, index)))
							})
							path, body := "/api/v1/inbox/item/done", fmt.Sprintf(`{"done":%t}`, index%2 == 1)
							if index == 0 && bulkFirst {
								path, body = "/api/v1/inbox/bulk", `{"action":"reopen","ids":["item"]}`
							}
							code, out := inboxCall(t, handler, "POST", path, body)
							return response{code, out}
						}
						results := make([]chan response, depth)
						for i := range results {
							results[i] = make(chan response, 1)
						}
						var once sync.Once
						var filtered map[string]any
						var body string
						var late *InboxItem
						stage := -1 // read and written only inside the serialized store calls
						probe := &filingProbeStore{inboxPutFailStore: st, beforeContext: func(ctx context.Context) {
							var marked bool
							stage, marked = ctx.Value(pendingChainIndex{}).(int)
							st.failAll = marked && profile.fails(stage)
							if !marked {
								stage = -1
							}
						}, before: func(string, []byte) {
							once.Do(func() {
								code, list := inboxCall(t, mux, "GET", "/api/v1/inbox?sources=all", "")
								visible := false
								for _, v := range list["items"].([]any) {
									item := v.(map[string]any)
									visible = visible || item["id"] == it.ID && item["done_at"] == nil
								}
								if code != 200 || !visible {
									t.Error("the intermediate reopen was not listed open")
								}
								asOf := list["as_of"].(string)
								listedAt, err := time.Parse(time.RFC3339Nano, asOf)
								if err != nil {
									t.Fatal(err)
								}
								h.inbox.mu.Lock()
								arrived.Last = "2"
								late = filingItem(h, "late")
								late.Tier, late.Last, late.ArrivedOf, late.ArrivedAt = TierFYI, "1", "1", listedAt.Add(time.Second)
								h.inbox.mu.Unlock()
								body = fmt.Sprintf(`{"action":%q,"filter":{"source":"mail","tier":"fyi"},"as_of":%q}`, action, asOf)
								for i := 1; i < depth; i++ {
									index := i
									go func() { results[index] <- call(index) }()
									waitForItem(t, h, it.ID, func(*InboxItem) bool { return h.inbox.mark[it.ID] >= uint64(index+1) })
								}
								if it.open() || st.doneAt(it.ID) == nil {
									t.Error("the current and stored endpoints should both be closed")
								}
								code, filtered = inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", body)
								if code != 200 {
									t.Errorf("filter status = %d", code)
								}
							})
							if depth == 2 && stage == 1 {
								// A failed intermediate reopen cannot become committed; a successful one is now the base.
								_, out := inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", body)
								want := []string{}
								if !profile.fails(0) {
									want = []string{it.ID}
								}
								if !slices.Equal(jsonIDs(out["failed"]), want) {
									t.Errorf("filter retained an impossible open state or lost the committed one: %v", out)
								}
							}
						}}
						h.store = func() Store { return probe }
						results[0] <- call(0)
						for i, ch := range results {
							select {
							case out := <-ch:
								wantCode := 200
								if profile.fails(i) && (i != 0 || !bulkFirst) {
									wantCode = 500
								}
								if out.code != wantCode || i == 0 && bulkFirst && (len(jsonIDs(out.body["failed"])) == 1) != profile.fails(i) {
									t.Errorf("stage %d = HTTP %d %v, want HTTP %d", i, out.code, out.body, wantCode)
								}
							case <-time.After(5 * time.Second):
								t.Fatal("a pending chain request did not resolve")
							}
						}
						if !slices.Equal(jsonIDs(filtered["failed"]), []string{it.ID}) || len(filtered["changed"].([]any)) != 0 ||
							len(jsonIDs(filtered["unchanged"])) != 0 || len(jsonIDs(filtered["ids"])) != 0 || filtered["undo"] != nil {
							t.Fatalf("filter omitted a listed intermediate open state: %v", filtered)
						}
						wantSkipped := []string{"arrived:changed", "late:changed"}
						if !slices.Equal(bulkIDs(filtered["skipped"].([]any)), wantSkipped) || it.open() != profile.wantOpen {
							t.Fatalf("chain outcome or guards changed: open=%v, filter=%v", it.open(), filtered)
						}
						if (profile.wantOpen || profile.name == "all fail") &&
							(!reflect.DeepEqual(it.Chain, initialChain) || it.ChainSeq != initialSeq) {
							t.Fatal("failed filings changed the original chain")
						}
						st.imu.Lock()
						var row InboxItem
						err := json.Unmarshal(st.rows[it.ID], &row)
						st.imu.Unlock()
						if err != nil || stateOf(h, &row) != stateOf(h, it) {
							t.Fatalf("chain resolution left storage behind memory: %s / %s, %v", stateOf(h, &row), stateOf(h, it), err)
						}
						_, retry := inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", body)
						wantChanged := 0
						if profile.wantOpen {
							wantChanged = 1
						}
						if len(jsonIDs(retry["failed"])) != 0 || len(retry["changed"].([]any)) != wantChanged ||
							!slices.Equal(bulkIDs(retry["skipped"].([]any)), wantSkipped) || it.open() || st.doneAt(it.ID) == nil {
							t.Fatalf("same-cutoff retry did not resolve correctly: %v", retry)
						}
						if !arrived.open() || !late.open() || !source.open() || !tier.open() || st.hasRow(late.ID) ||
							st.doneAt(arrived.ID) != nil || st.doneAt(source.ID) != nil || st.doneAt(tier.ID) != nil {
							t.Fatal("a source/tier/as_of protected row was filed")
						}
					})
				}
			}
		}
	}
}

func TestInboxBulkFilterKeepsOlderPendingAfterNewerFailure(t *testing.T) {
	for _, depth := range []int{3, 5, 9} {
		t.Run(fmt.Sprint("depth=", depth), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			h.inbox.file(it, time.Now().Add(-time.Minute), "user")
			h.inbox.closeHandling(it, "user", time.Now().Add(-time.Minute))
			if failed := h.saveInbox(context.Background(), []*InboxItem{it}); len(failed) != 0 {
				t.Fatal("seeding the closed row failed")
			}
			// Construct each request's real mutation phase, then choose a save order independently of mutex waiters.
			var writes []*inboxFiling
			for i := 0; i < depth; i++ {
				h.inbox.mu.Lock()
				w := h.inbox.beginFiling(it, time.Now(), "user")
				if i%2 == 0 {
					it.DoneAt, it.DoneBy = nil, ""
				} else {
					h.inbox.file(it, w.now, w.why)
					w.close = true
				}
				h.inbox.settleFiling(w)
				writes = append(writes, w)
				h.inbox.mu.Unlock()
			}
			mux := h.routes()
			_, list := inboxCall(t, mux, "GET", "/api/v1/inbox", "")
			body := fmt.Sprintf(`{"action":"done","filter":{"source":"mail"},"as_of":%q}`, list["as_of"])
			st.failAll = true
			if failed := h.saveInboxFilings(context.Background(), writes[depth-1:]); !failed[it.ID] {
				t.Fatal("the newest pending reopen did not fail")
			}
			if it.open() || st.doneAt(it.ID) == nil {
				t.Fatal("the newer failure should leave both endpoints closed, with an older reopen still pending")
			}
			st.failAll = false
			code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", body)
			if code != 200 || !slices.Equal(jsonIDs(out["failed"]), []string{it.ID}) || len(out["changed"].([]any)) != 0 {
				t.Fatalf("a newer failure hid the older pending open state: HTTP %d %v", code, out)
			}
			for i, w := range writes[:depth-1] {
				st.failAll = i%2 == 1 // older reopens commit, filings fail
				if failed := h.saveInboxFilings(context.Background(), []*inboxFiling{w}); failed[it.ID] != st.failAll {
					t.Fatalf("older stage %d outcome = %v", i, failed)
				}
			}
			if !it.open() || st.doneAt(it.ID) != nil {
				t.Fatal("the surviving older reopen was lost")
			}
			st.failAll = false
			code, out = inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", body)
			if code != 200 || !slices.Equal(jsonIDs(out["ids"]), []string{it.ID}) || len(jsonIDs(out["failed"])) != 0 || it.open() {
				t.Fatalf("same-cutoff retry failed after the chain resolved: HTTP %d %v", code, out)
			}
		})
	}
}
