package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestKBRPCReadsAndUpdates(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	e := kbEng(t, s)
	c := &kb.Chat{ID: "example-chat", Source: "example", Name: "Example"}
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 12, 0, 0, 0, e.Store.Zone())
	if err := e.Store.AppendMessages(c, []kb.Line{{At: at, Sender: "Example", Text: "a searchable line\ncontinued"},
		{At: at.Add(time.Minute), Sender: "Example", Text: "second"}}); err != nil {
		t.Fatal(err)
	}
	ask := func(method, raw string) string {
		t.Helper()
		out, ok, err := s.kbRequest(context.Background(), method, json.RawMessage(raw))
		if !ok || err != nil {
			t.Fatalf("%s: handled=%v err=%v", method, ok, err)
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := ask(assistantwire.MethodKBUpdate, `{"kind":"projects","id":"example","name":"Example","summary":"confirmed context","tags":["work"],"chats":["example-chat"]}`); got != `{"id":"example"}` {
		t.Fatal(got)
	}
	if got := ask(assistantwire.MethodKBList, `{"kind":"projects","tag":"work","limit":1}`); !strings.Contains(got, `"id":"example"`) || !strings.Contains(got, `"tags":["work"]`) {
		t.Fatal(got)
	}
	if got := ask(assistantwire.MethodKBGet, `{"kind":"projects","id":"example"}`); !strings.Contains(got, "confirmed context") || !strings.Contains(got, "example-chat") || strings.Contains(got, `"agents"`) {
		t.Fatal(got)
	}
	if got := ask(assistantwire.MethodKBDay, `{"id":"example-chat","day":"2026-01-02"}`); !strings.Contains(got, `"n":3`) || !strings.Contains(got, `"n":5`) || !strings.Contains(got, `\ncontinued`) {
		t.Fatal(got)
	}
	if got := ask(assistantwire.MethodKBSearch, `{"query":"searchable","messages":true,"limit":1}`); !strings.Contains(got, "messages/2026-01-02.md") {
		t.Fatal(got)
	}
	if got := ask(assistantwire.MethodKBSearch, `{"query":"searchable","messages":false}`); got != `{"hits":[]}` {
		t.Fatal(got)
	}
	if got := ask(assistantwire.MethodKBList, `{"kind":"people"}`); got != `{"items":[],"tags":[],"total":0}` {
		t.Fatal(got)
	}
	if err := e.Store.AppendMessages(c, []kb.Line{{At: time.Now(), Sender: "Example", Text: "recent content"}}); err != nil {
		t.Fatal(err)
	}
	if got := ask(assistantwire.MethodKBGet, `{"kind":"chats","id":"example-chat","recent_chars":0}`); strings.Contains(got, `"recent"`) {
		t.Fatalf("explicit zero must omit recent messages: %s", got)
	}
	if got := ask(assistantwire.MethodKBGet, `{"kind":"chats","id":"example-chat"}`); !strings.Contains(got, "recent content") {
		t.Fatalf("default recent messages: %s", got)
	}
}

func TestKBRPCRejectsInvalidParamsBeforeWriting(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	for _, tc := range []struct{ method, raw string }{
		{assistantwire.MethodKBUpdate, `{"kind":"projects","id":"bad","summmary":"lost correction"}`},
		{assistantwire.MethodKBUpdate, `{"kind":"projects","id":" "}`},
		{assistantwire.MethodKBUpdate, `{"kind":"unknown","id":"bad"}`},
		{assistantwire.MethodKBUpdate, `{"kind":"projects","id":"bad"} {}`},
		{assistantwire.MethodKBUpdate, `null`},
		{assistantwire.MethodKBUpdate, `[]`},
		{assistantwire.MethodKBList, `{"kind":"unknown"}`},
		{assistantwire.MethodKBList, `{"kind":"projects","limit":201}`},
		{assistantwire.MethodKBList, `{"kind":"projects","limit":-1}`},
		{assistantwire.MethodKBList, `{"kind":"projects","offset":-1}`},
		{assistantwire.MethodKBGet, `{"kind":"people"}`},
		{assistantwire.MethodKBGet, `{"kind":"people","id":"x","recent_chars":20001}`},
		{assistantwire.MethodKBGet, `{"kind":"people","id":"x","recent_chars":-1}`},
		{assistantwire.MethodKBDay, `{"id":"example","day":"2026-02-30"}`},
		{assistantwire.MethodKBDay, `{"day":"2026-01-02"}`},
		{assistantwire.MethodKBSearch, `{"query":" "}`},
		{assistantwire.MethodKBSearch, `{"query":"x","limit":201}`},
		{assistantwire.MethodKBSearch, `{"query":"x","limit":-1}`},
		{assistantwire.MethodKBSearch, `{"query":"x","messages":"false"}`},
		{assistantwire.MethodKBUpdate, `{"kind":"projects","id":"bad","summary":"` + strings.Repeat("x", 1<<20) + `"}`},
	} {
		_, handled, err := s.kbRequest(context.Background(), tc.method, json.RawMessage(tc.raw))
		var re *rpc.Error
		if !handled || !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
			t.Fatalf("%s %s: handled=%v err=%v", tc.method, tc.raw, handled, err)
		}
	}
	if projects := kbEng(t, s).Store.Projects(); len(projects) != 0 {
		t.Fatalf("invalid calls wrote projects: %+v", projects)
	}
	for _, method := range []string{assistantwire.MethodKBGet, assistantwire.MethodKBDay, assistantwire.MethodKBUpdate} {
		raw := `{"kind":"people","id":"missing"}`
		if method == assistantwire.MethodKBDay {
			raw = `{"id":"missing","day":"2026-01-02"}`
		}
		_, _, err := s.kbRequest(context.Background(), method, json.RawMessage(raw))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "not_found" {
			t.Fatalf("%s missing: %v", method, err)
		}
	}
	if _, handled, _ := s.kbRPCRequest(context.Background(), "kb.forget", json.RawMessage(`{}`)); handled {
		t.Fatal("public RPC took an internal method")
	}
}

func TestKBRPCListPagesEveryEntity(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	e := kbEng(t, s)
	for i := range 205 {
		id := fmt.Sprintf("example-%03d", i)
		if err := e.Store.PutProject(&kb.Project{Slug: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for offset := 0; ; {
		raw, _ := json.Marshal(assistantwire.KBListParams{Kind: kb.KindProject, Limit: 200, Offset: offset})
		out, handled, err := s.kbRequest(context.Background(), assistantwire.MethodKBList, raw)
		if !handled || err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		page := out.(assistantwire.KBListResult)
		if page.Total != 205 || len(page.Items) != min(200, 205-offset) {
			t.Fatalf("offset %d: %+v", offset, page)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate entity %s", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset != offset+len(page.Items) {
			t.Fatalf("next offset: %d", *page.NextOffset)
		}
		offset = *page.NextOffset
	}
	if len(seen) != 205 {
		t.Fatalf("listed %d of 205 entities", len(seen))
	}
	out, _, err := s.kbRequest(context.Background(), assistantwire.MethodKBList, json.RawMessage(`{"kind":"projects","offset":999999999}`))
	if err != nil {
		t.Fatal(err)
	}
	page := out.(assistantwire.KBListResult)
	if page.Total != 205 || len(page.Items) != 0 || page.NextOffset != nil {
		t.Fatalf("past the end: %+v", page)
	}
}

// An extension's kb.update (as the manager hands it over, #6100): attributed, with evidence and the replaced values;
// refused with conflict on what the user holds; evidence and unlock only for extensions; kb.list since.
func TestKBRPCExtensionWrites(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	e := kbEng(t, s)
	if err := e.Store.PutChat(&kb.Chat{ID: "example-chat", Source: "example", Name: "Example"}); err != nil {
		t.Fatal(err)
	}
	ask := func(method, raw string) (string, error) {
		t.Helper()
		out, ok, err := s.kbRequest(context.Background(), method, json.RawMessage(raw))
		if !ok {
			t.Fatalf("%s not handled", method)
		}
		b, _ := json.Marshal(out)
		return string(b), err
	}
	ext := func(params string) (string, error) {
		return ask(assistantwire.MethodKBExtensionUpdate, `{"extension":"curator","params":`+params+`}`)
	}
	got, err := ext(`{"kind":"chats","id":"example-chat","summary":"reviewed","evidence":[{"chat":"example-chat","day":"2026-01-02","n":3}]}`)
	if err != nil || got != `{"id":"example-chat","before":{"summary":""}}` {
		t.Fatalf("%s %v", got, err)
	}
	got, _ = ask(assistantwire.MethodKBGet, `{"kind":"chats","id":"example-chat","recent_chars":0}`)
	if !strings.Contains(got, `\"by\":\"ext:curator\"`) && !strings.Contains(got, `"by":"ext:curator"`) {
		t.Fatalf("provenance not in kb.get: %s", got)
	}
	if _, err := ask(assistantwire.MethodKBExtensionUpdate, `{"extension":"renamed-curator","storage_id":"curator","params":{"kind":"chats","id":"example-chat","summary":"reviewed again"}}`); err != nil {
		t.Fatalf("renamed installation lost its own field: %v", err)
	}
	got, _ = ask(assistantwire.MethodKBGet, `{"kind":"chats","id":"example-chat","recent_chars":0}`)
	if !strings.Contains(got, "ext:curator") || strings.Contains(got, "ext:renamed-curator") {
		t.Fatalf("stored field ownership moved: %s", got)
	}
	if _, err := e.Apply(kb.KindChat, "example-chat", kb.Update{Name: "the user's name"}); err != nil {
		t.Fatal(err)
	}
	_, err = ext(`{"kind":"chats","id":"example-chat","name":"extension name"}`)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeKBConflict || string(re.Data) != `{"owners":{"name":"user"}}` {
		t.Fatalf("conflict: %v", err)
	}
	for _, bad := range []string{
		`{"kind":"chats","id":"example-chat","evidence":[{"chat":"","day":"2026-01-02"}]}`,
		`{"kind":"chats","id":"example-chat","evidence":[{"chat":"c","day":"Jan 2"}]}`,
		`{"kind":"chats","id":"example-chat","unlock":["nope"]}`,
	} {
		if _, err := ext(bad); !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, err := ask(assistantwire.MethodKBUpdate, `{"kind":"chats","id":"example-chat","unlock":["summary"]}`); !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
		t.Fatalf("unlock without an extension: %v", err)
	}
	if _, err := ask(assistantwire.MethodKBExtensionUpdate, `{"params":{"kind":"chats","id":"example-chat"}}`); !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
		t.Fatalf("no extension: %v", err)
	}
	if got, err := ask(assistantwire.MethodKBList, `{"kind":"chats","since":"2999-01-01T00:00:00Z"}`); err != nil || got != `{"items":[],"tags":[],"total":0}` {
		t.Fatalf("since: %s %v", got, err)
	}
	if _, err := ask(assistantwire.MethodKBList, `{"kind":"chats","since":"yesterday"}`); !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
		t.Fatalf("bad since: %v", err)
	}
}

// kb.recent is one feed over every source: chats of any source (with it), documents, meetings, events, recordings.
func TestKBRPCRecentAcrossSources(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	e := kbEng(t, s)
	now := time.Now().UTC()
	for _, c := range []*kb.Chat{
		{ID: "lark-a", Source: "lark", Name: "A", LastAt: now.Add(-time.Hour)},
		{ID: "mail-b", Source: "mail", Name: "B", LastAt: now.Add(-2 * time.Hour)},
		{ID: "wechat-c", Source: "wechat", Name: "C", LastAt: now.Add(-72 * time.Hour)},
	} {
		if err := e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.PutItem(&kb.Item{Kind: kb.KindEvent, ID: "ev", Title: "Standup", At: now.Add(-30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	out, ok, err := s.kbRequest(context.Background(), assistantwire.MethodKBRecent, json.RawMessage(`{"since":"`+since+`"}`))
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	r := out.(assistantwire.KBRecentResult)
	var got []string
	for _, en := range r.Items {
		got = append(got, en.Kind+"/"+en.ID+"/"+en.Source)
	}
	if strings.Join(got, ",") != "events/ev/,chats/lark-a/lark,chats/mail-b/mail" || r.Total != 3 {
		t.Fatalf("recent: %v", got)
	}
	out, _, _ = s.kbRequest(context.Background(), assistantwire.MethodKBRecent, json.RawMessage(`{"since":"`+since+`","kinds":["chats"],"limit":1}`))
	if r := out.(assistantwire.KBRecentResult); len(r.Items) != 1 || r.NextOffset == nil || *r.NextOffset != 1 {
		t.Fatalf("page: %+v", r)
	}
	var re *rpc.Error
	for _, bad := range []string{`{}`, `{"since":"x"}`, `{"since":"` + since + `","kinds":["nope"]}`} {
		if _, _, err := s.kbRequest(context.Background(), assistantwire.MethodKBRecent, json.RawMessage(bad)); !errors.As(err, &re) || re.Code != bus.CodeInvalidParams {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// Disabled, starting and successfully empty are different responses on both read transports.
func TestKBAvailabilityCodes(t *testing.T) {
	f := newFakeHub()
	f.hello.KB = &KBSettings{Off: true}
	off := withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t), KB: &KBOptions{Dir: t.TempDir()}}))
	for _, method := range []string{assistantwire.MethodKBList, assistantwire.MethodKBGet, assistantwire.MethodKBSearch} {
		raw := json.RawMessage(`{"kind":"projects"}`)
		if method == assistantwire.MethodKBGet {
			raw = json.RawMessage(`{"kind":"projects","id":"example"}`)
		}
		if method == assistantwire.MethodKBSearch {
			raw = json.RawMessage(`{"query":"example"}`)
		}
		_, handled, err := off.kbRequest(context.Background(), method, raw)
		var coded *rpc.Error
		if !handled || !errors.As(err, &coded) || coded.Code != assistantwire.CodeKBDisabled {
			t.Fatalf("%s: handled=%v err=%v", method, handled, err)
		}
	}
	var coded *rpc.Error
	if !errors.As(kbError(errKBNotOpen), &coded) || coded.Code != assistantwire.CodeKBUnavailable {
		t.Fatal("starting lost its code")
	}
	ready := newKBService(t, newFakeHub())
	rec := httptest.NewRecorder()
	ready.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kb/projects", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
}
