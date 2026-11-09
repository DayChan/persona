package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// historyIDs walks GET /api/v1/inbox/done page by page (query: limit, sources…) and returns the ids in the order
// listed, and how many pages it took.
func historyIDs(t *testing.T, h http.Handler, query string) ([]string, int) {
	t.Helper()
	var ids []string
	cursor := ""
	for pages := 1; pages <= 50; pages++ {
		code, out := inboxCall(t, h, "GET", "/api/v1/inbox/done?"+query+cursor, "")
		if code != http.StatusOK {
			t.Fatalf("page %d: %d %v", pages, code, out)
		}
		items, _ := out["items"].([]any)
		for _, it := range items {
			m := it.(map[string]any)
			if _, has := m["context"]; has {
				t.Fatalf("a history item carries its context: %v", m)
			}
			ids = append(ids, m["id"].(string))
		}
		next, _ := out["next"].(map[string]any)
		if next == nil {
			return ids, pages
		}
		cursor = "&before=" + url.QueryEscape(next["before"].(string)) + "&before_id=" + url.QueryEscape(next["before_id"].(string))
	}
	t.Fatalf("no end to the history: %v", ids)
	return nil, 0
}

// The history pages the stored done items newest first, past what memory holds: items a bulk action filed at one
// instant are split across pages without repeats or gaps, open items never show, connectors' only with sources=all.
func TestInboxHistoryPagesTheStoredDoneItems(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	now := time.Now()
	store := func(it *InboxItem) {
		t.Helper()
		raw, _ := json.Marshal(it)
		if err := h.st.PutInboxItem(ctx, it.ID, raw, it.UpdatedAt, it.DoneAt); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-30 * 24 * time.Hour) // done long ago: stored, not loaded
	store(&InboxItem{ID: "mail:old", Source: "mail", Kind: KindMail, UpdatedAt: old, DoneAt: &old, DoneBy: "user", Context: "示例正文"})
	gh := now.Add(-time.Minute)
	store(&InboxItem{ID: "github:1", Source: "github", Kind: "mention", UpdatedAt: gh, DoneAt: &gh, Connector: &InboxConnector{Thread: "t"}})
	store(&InboxItem{ID: "mail:open", Source: "mail", Kind: KindMail, UpdatedAt: now})
	var bulk []string
	for i := range 5 {
		id := fmt.Sprintf("mail:b%d", i)
		store(&InboxItem{ID: id, Source: "mail", Kind: KindMail, UpdatedAt: now.Add(-time.Hour)})
		bulk = append(bulk, id)
	}
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	_, loaded := h.inbox.items["mail:old"]
	h.inbox.mu.Unlock()
	if loaded {
		t.Fatal("an item done 30 days ago was loaded into memory")
	}
	mux := h.routes()
	ids, _ := json.Marshal(bulk)
	if code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", `{"action":"dismiss","ids":`+string(ids)+`}`); code != http.StatusOK {
		t.Fatalf("bulk: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	at := *h.inbox.items["mail:b0"].DoneAt
	for _, id := range bulk {
		if !h.inbox.items[id].DoneAt.Equal(at) {
			h.inbox.mu.Unlock()
			t.Fatal("the bulk action filed its items at different instants")
		}
	}
	h.inbox.mu.Unlock()

	// Pages of 2 end twice among the five filed together; the third is full, so a fourth (empty) ends it.
	got, pages := historyIDs(t, mux, "limit=2")
	if want := "mail:b4,mail:b3,mail:b2,mail:b1,mail:b0,mail:old"; strings.Join(got, ",") != want || pages != 4 {
		t.Fatalf("history in %d pages: %v, want %s in 4", pages, got, want)
	}
	got, pages = historyIDs(t, mux, "limit=3&sources=all")
	if want := "mail:b4,mail:b3,mail:b2,mail:b1,mail:b0,github:1,mail:old"; strings.Join(got, ",") != want || pages != 3 {
		t.Fatalf("history with the connectors in %d pages: %v, want %s in 3", pages, got, want)
	}
	// Listed as the list lists it: the version a bulk action sends back, no bookkeeping.
	_, out := inboxCall(t, mux, "GET", "/api/v1/inbox/done?limit=1", "")
	first := out["items"].([]any)[0].(map[string]any)
	if first["version"] == nil || first["done_by"] != "dismissed" || first["last"] != nil {
		t.Fatalf("a history item: %v", first)
	}
}

// Without a database the history is the done items memory holds, in the same order and pages.
func TestInboxHistoryFromMemory(t *testing.T) {
	s := newTestService(t, newFakeHub())
	if s.inboxDB() != nil {
		t.Fatal("the test service has an inbox store")
	}
	ctx := context.Background()
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tie, newer := now.Add(-time.Hour), now.Add(-time.Minute)
	s.inbox.mu.Lock()
	for _, id := range []string{"mail:a", "mail:c", "mail:b", "mail:d"} {
		s.inbox.items[id] = &InboxItem{ID: id, Source: "mail", Kind: KindMail, UpdatedAt: tie, DoneAt: &tie}
	}
	s.inbox.items["mail:new"] = &InboxItem{ID: "mail:new", Source: "mail", Kind: KindMail, UpdatedAt: newer, DoneAt: &newer}
	s.inbox.items["mail:open"] = &InboxItem{ID: "mail:open", Source: "mail", Kind: KindMail, UpdatedAt: now}
	s.inbox.items["github:1"] = &InboxItem{ID: "github:1", Source: "github", UpdatedAt: now, DoneAt: &now, Connector: &InboxConnector{Thread: "t"}}
	s.inbox.mu.Unlock()
	got, pages := historyIDs(t, s, "limit=2")
	if want := "mail:new,mail:d,mail:c,mail:b,mail:a"; strings.Join(got, ",") != want || pages != 3 {
		t.Fatalf("history in %d pages: %v, want %s in 3", pages, got, want)
	}
	if got, _ := historyIDs(t, s, "sources=all"); strings.Join(got, ",") != "github:1,mail:new,mail:d,mail:c,mail:b,mail:a" {
		t.Fatalf("history with the connectors: %v", got)
	}
}

// A page holds 50 items unless the request says (1 to 200); a bad limit or cursor is 400.
func TestInboxHistoryLimits(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ctx := context.Background()
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.inbox.mu.Lock()
	for i := range inboxHistoryMax + 10 {
		id := fmt.Sprintf("mail:%03d", i)
		s.inbox.items[id] = &InboxItem{ID: id, Source: "mail", Kind: KindMail, UpdatedAt: now, DoneAt: &now}
	}
	s.inbox.mu.Unlock()
	for query, want := range map[string]int{"": inboxHistoryLimit, "limit=1": 1, "limit=200": inboxHistoryMax} {
		code, out := inboxCall(t, s, "GET", "/api/v1/inbox/done?"+query, "")
		if items, _ := out["items"].([]any); code != http.StatusOK || len(items) != want || out["next"] == nil {
			t.Fatalf("%q: %d, %d items, next %v; want %d and a next", query, code, len(items), out["next"], want)
		}
	}
	for _, query := range []string{"limit=0", "limit=-1", "limit=201", "limit=x", "before=yesterday", "before_id=mail:001",
		"before=2026-11-09"} {
		if code, out := inboxCall(t, s, "GET", "/api/v1/inbox/done?"+query, ""); code != http.StatusBadRequest || out["error"] == nil {
			t.Fatalf("%q: %d %v, want 400", query, code, out)
		}
	}
	// A cursor with an offset reads as the same instant.
	at := now.In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339Nano)
	code, out := inboxCall(t, s, "GET", "/api/v1/inbox/done?limit=200&before="+url.QueryEscape(at)+"&before_id=mail:100", "")
	if items, _ := out["items"].([]any); code != http.StatusOK || len(items) != 100 || out["next"] != nil {
		t.Fatalf("before mail:100: %d, %d items, next %v", code, len(items), out["next"])
	}
}

// Done items leave memory after inboxForget but their rows stay: the history reaches them until inboxKeepHistory,
// when a round (at most every inboxPurgeEvery) or the next load deletes them.
func TestInboxDoneRowsOutliveMemory(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	now := time.Now()
	put := func(id string, done time.Time) {
		t.Helper()
		raw, _ := json.Marshal(&InboxItem{ID: id, Source: "mail", Kind: KindMail, UpdatedAt: done, DoneAt: &done})
		if err := h.st.PutInboxItem(ctx, id, raw, done, &done); err != nil {
			t.Fatal(err)
		}
	}
	rowKept := func(id string) bool {
		h.st.imu.Lock()
		defer h.st.imu.Unlock()
		_, ok := h.st.rows[id]
		return ok
	}
	put("mail:past", now.Add(-inboxKeepHistory-time.Hour)) // purged at load
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	if rowKept("mail:past") {
		t.Fatal("the load kept a row done past inboxKeepHistory")
	}
	// Done past inboxForget while the manager runs: held until the next round.
	forgotten := now.Add(-inboxForget - time.Minute)
	put("mail:forgotten", forgotten)
	h.inbox.mu.Lock()
	h.inbox.items["mail:forgotten"] = &InboxItem{ID: "mail:forgotten", Source: "mail", Kind: KindMail, UpdatedAt: forgotten, DoneAt: &forgotten}
	h.inbox.mu.Unlock()
	put("mail:expiring", now.Add(-inboxKeepHistory-time.Minute)) // stored meanwhile; the round purges it
	h.inbox.mu.Lock()
	h.inbox.purgedAt = now.Add(-inboxPurgeEvery)
	h.inbox.mu.Unlock()
	h.collectInbox(ctx, roundPoll)
	h.inbox.mu.Lock()
	_, held := h.inbox.items["mail:forgotten"]
	h.inbox.mu.Unlock()
	if held {
		t.Fatal("the round kept an item done past inboxForget in memory")
	}
	if !rowKept("mail:forgotten") || rowKept("mail:expiring") {
		t.Fatalf("after the round: forgotten row kept %v (want true), expiring row kept %v (want false)",
			rowKept("mail:forgotten"), rowKept("mail:expiring"))
	}
	if list := h.inboxList(true); len(list) != 0 {
		t.Fatalf("the list shows %d items", len(list))
	}
	if got, _ := historyIDs(t, h.routes(), ""); strings.Join(got, ",") != "mail:forgotten" {
		t.Fatalf("history: %v", got)
	}
	// A round within inboxPurgeEvery of the last purge does not purge again.
	put("mail:later", now.Add(-inboxKeepHistory-time.Minute))
	h.collectInbox(ctx, roundPoll)
	if !rowKept("mail:later") {
		t.Fatal("a round purged again within inboxPurgeEvery")
	}
}

// Forgetting a source's data deletes its rows kept only as history too, and counts them.
func TestInboxForgetDeletesTheHistory(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, it := range []*InboxItem{
		{ID: "example:old", Source: "example", UpdatedAt: old, DoneAt: &old},
		{ID: "mail:old", Source: "mail", UpdatedAt: old, DoneAt: &old},
	} {
		raw, _ := json.Marshal(it)
		if err := h.st.PutInboxItem(ctx, it.ID, raw, it.UpdatedAt, it.DoneAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	before := h.in.pushes()
	if n, err := h.InboxForget(ctx, "example"); err != nil || n != 1 {
		t.Fatalf("forget: %d %v", n, err)
	}
	if h.st.rows["example:old"] != nil || h.st.rows["mail:old"] == nil || h.in.pushes() != before+1 {
		t.Fatalf("after forgetting: rows %v, %d pushes", h.st.rows, h.in.pushes()-before)
	}
	if n, err := h.InboxForget(ctx, "example"); err != nil || n != 0 || h.in.pushes() != before+1 {
		t.Fatalf("forget again: %d %v", n, err)
	}
}

// An item done 20 days ago (stored, not held in memory) still opens, read-only; filing it again finds no item, as
// before; reopening it, one at a time or in bulk, takes its row back into memory and lists it again.
func TestInboxHistoryItemOpensAndComesBack(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	for _, id := range []string{"mail:a", "mail:b", "mail:c", "mail:d"} {
		it := &InboxItem{ID: id, Source: "mail", Kind: KindMail, Title: "示例", UpdatedAt: old, DoneAt: &old, DoneBy: "user",
			Context: "示例正文", Last: "7", Mail: &InboxMail{AccountID: "a1", Mailbox: "INBOX", UIDs: []uint32{7}, MessageID: "m@example.com",
				From: MailAddress{Email: "sender@example.com"}}}
		raw, _ := json.Marshal(it)
		if err := h.st.PutInboxItem(ctx, it.ID, raw, it.UpdatedAt, it.DoneAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	held := func(id string) bool {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		_, ok := h.inbox.items[id]
		return ok
	}
	listed := func(id string) bool {
		for _, it := range h.inboxList(true) {
			if it.ID == id {
				return it.DoneAt == nil
			}
		}
		return false
	}

	code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/mail:a", "")
	if code != http.StatusOK || out["id"] != "mail:a" || out["context"] != "示例正文" || out["done_at"] == nil || out["reply_target"] == nil || out["last"] != nil {
		t.Fatalf("detail of a stored done item: %d %v", code, out)
	}
	if held("mail:a") {
		t.Fatal("reading a stored done item took it into memory")
	}
	if code, _ := inboxCall(t, mux, "GET", "/api/v1/inbox/mail:none", ""); code != http.StatusNotFound {
		t.Fatalf("an unknown item: %d", code)
	}

	// Filing it again: no item, nothing written.
	writes := func() int { h.st.imu.Lock(); defer h.st.imu.Unlock(); return h.st.ids["mail:a"] }
	before := writes()
	for _, body := range []string{`{"done":true}`, `{"done":true,"dismiss":true}`} {
		if code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/mail:a/done", body); code != http.StatusNotFound {
			t.Fatalf("%s on a stored done item: %d %v", body, code, out)
		}
	}
	_, out = inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", `{"action":"dismiss","ids":["mail:a"]}`)
	if sk, _ := out["skipped"].([]any); len(sk) != 1 || sk[0].(map[string]any)["reason"] != bulkSkipMissing {
		t.Fatalf("bulk dismiss: %v", out)
	}
	_, out = inboxCall(t, mux, "POST", "/api/v1/inbox/done", `{"done":true,"items":[{"id":"mail:a"}]}`)
	if sk, _ := out["skipped"].([]any); len(sk) != 1 || sk[0].(map[string]any)["reason"] != bulkSkipMissing {
		t.Fatalf("bulk done: %v", out)
	}
	if writes() != before || held("mail:a") {
		t.Fatalf("filing a stored done item wrote it (%d writes) or took it into memory", writes()-before)
	}

	// Reopening: one item, then in bulk (both ways).
	if code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/mail:a/done", `{"done":false}`); code != http.StatusOK || out["done_at"] != nil {
		t.Fatalf("reopen: %d %v", code, out)
	}
	_, out = inboxCall(t, mux, "POST", "/api/v1/inbox/bulk", `{"action":"reopen","ids":["mail:b","mail:none"]}`)
	if ids, _ := out["ids"].([]any); len(ids) != 1 || ids[0] != "mail:b" || len(out["skipped"].([]any)) != 1 {
		t.Fatalf("bulk reopen: %v", out)
	}
	_, out = inboxCall(t, mux, "POST", "/api/v1/inbox/done", `{"done":false,"items":[{"id":"mail:c"}]}`)
	if ch, _ := out["changed"].([]any); len(ch) != 1 || ch[0].(map[string]any)["id"] != "mail:c" {
		t.Fatalf("done:false in bulk: %v", out)
	}
	for _, id := range []string{"mail:a", "mail:b", "mail:c"} {
		h.st.imu.Lock()
		stored := h.st.done[id]
		h.st.imu.Unlock()
		if !listed(id) || stored != nil {
			t.Fatalf("%s after the reopen: listed open %v, stored done %v", id, listed(id), stored)
		}
	}
	if held("mail:d") {
		t.Fatal("an item nobody reopened came into memory")
	}
}

// Without a database there is no history to read or bring back: a done item memory forgot is gone.
func TestInboxHistoryItemWithoutADatabase(t *testing.T) {
	s := newTestService(t, newFakeHub())
	if err := s.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, _ := inboxCall(t, s, "GET", "/api/v1/inbox/mail:gone", ""); code != http.StatusNotFound {
		t.Fatalf("detail: %d", code)
	}
	if code, _ := inboxCall(t, s, "POST", "/api/v1/inbox/mail:gone/done", `{"done":false}`); code != http.StatusNotFound {
		t.Fatalf("reopen: %d", code)
	}
}
