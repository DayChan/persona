package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// minutesModel answers every knowledge-base call with reply.
type minutesModel struct{ reply string }

func (m minutesModel) JSONMax(_ context.Context, _, _ string, _ int, out any) error {
	return json.Unmarshal([]byte(m.reply), out)
}

// A meeting recording through the phone's routes: its minutes are written, pushed once, read back by the detail, the
// reconciliation and kb_get / kb_list; confirmed action items become to-dos once, however often the app asks.
func TestMinutesHTTP(t *testing.T) {
	f := newFakeHub()
	// The push also goes to the Lark bot and the channels from goroutines of their own: they must not log into a
	// finished test.
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(string, ...any) {})
	e := kbEng(t, s)
	model := minutesModel{reply: `{"title":"发布评审","points":[{"text":"导出丢条目","line":1}],` +
		`"decisions":[{"text":"先保留上限","line":2}],"actions":[{"who":"Bo","what":"补上回归测试","by":"周五","due_date":"2026-11-13","line":3},{"what":"发邮件","line":3}]}`}
	e.Model, e.ModelFor = model, func(string) kb.Model { return model }
	server := httptest.NewServer(s)
	defer server.Close()
	request := func(method, path, role string, body any, want int) string {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		if role != "" {
			req.Header.Set(RoleHeader, role)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, raw, want)
		}
		return string(raw)
	}
	const id = "22222222-2222-4222-8222-222222222222"
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	upload := map[string]any{"revision": 1, "started_at": start, "device": "phone", "complete": false, "kind": "meeting",
		"calendar": map[string]any{"title": "发布评审", "attendees": []string{"Ada", "Bo"}, "id": "ignored"},
		"segments": []map[string]any{{"start": 0, "end": 9, "text": "导出会丢掉大条目"}, {"start": 10, "end": 19, "text": "先保留上限"}, {"start": 20, "end": 29, "text": "Bo 补上回归测试"}}}
	path := "/api/v1/recordings/" + id
	request("PUT", path, "", upload, 200)
	request("POST", path+"/minutes", "", nil, 409) // the transcript is not complete
	upload["revision"], upload["complete"] = 2, true
	request("PUT", path, "", upload, 200)
	if !strings.Contains(request("GET", "/api/v1/recordings", "", nil, 200), `"minutes_state":"pending"`) {
		t.Fatal("reconciliation lacks the pending minutes")
	}
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	pushes := f.pushed()
	if len(pushes) != 1 || pushes[0].ID != "ntf_minutes_"+id+"_1" || pushes[0].Kind != NotifyPush || !strings.Contains(pushes[0].Body, "发布评审") {
		t.Fatalf("pushes %+v", pushes)
	}
	var detail struct {
		Detail struct {
			Meta    map[string]any `json:"meta"`
			Minutes kb.Minutes     `json:"minutes"`
		} `json:"detail"`
	}
	_ = json.Unmarshal([]byte(request("GET", "/api/v1/kb/recordings/"+id, "", nil, 200)), &detail)
	if detail.Detail.Meta["minutes_state"] != "ready" || detail.Detail.Meta["kind"] != "meeting" || detail.Detail.Minutes.Version != 1 ||
		len(detail.Detail.Minutes.Actions) != 2 || detail.Detail.Minutes.Actions[0].At != 20 {
		t.Fatalf("detail %+v", detail.Detail)
	}

	// Confirming: one to-do per action, the same one however often; the minutes say which.
	todosPath := path + "/minutes/todos"
	var out struct {
		Todos   []Todo     `json:"todos"`
		Minutes kb.Minutes `json:"minutes"`
	}
	_ = json.Unmarshal([]byte(request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{"a1"}}, 200)), &out)
	if len(out.Todos) != 1 || out.Todos[0].Title != "Bo: 补上回归测试" || out.Todos[0].DueDate != "2026-11-13" ||
		!strings.Contains(out.Todos[0].Notes, "发布评审") || out.Minutes.Actions[0].TodoID != out.Todos[0].ID || out.Minutes.Actions[1].TodoID != "" {
		t.Fatalf("confirmed %+v", out)
	}
	first := out.Todos[0].ID
	_ = json.Unmarshal([]byte(request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{"a1", "a2", "a1"}}, 200)), &out)
	if len(out.Todos) != 2 || out.Todos[0].ID != first || out.Minutes.Actions[1].TodoID != out.Todos[1].ID {
		t.Fatalf("again %+v", out)
	}
	mine := 0
	s.todos.mu.RLock()
	for tid := range s.todos.todos {
		if strings.HasPrefix(tid, "todo_mtg_") {
			mine++
		}
	}
	s.todos.mu.RUnlock()
	if mine != 2 {
		t.Fatalf("%d to-dos from the minutes, want 2", mine)
	}
	if body := request("POST", todosPath, "", map[string]any{"version": 7, "actions": []string{"a1"}}, 409); !strings.Contains(body, codeMinutesStale) {
		t.Fatalf("stale: %s", body)
	}
	request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{"a9"}}, 404)
	request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{}}, 400)
	request("POST", todosPath, "answer", map[string]any{"version": 1, "actions": []string{"a1"}}, 403)
	request("POST", "/api/v1/recordings/33333333-3333-4333-8333-333333333333/minutes/todos", "", map[string]any{"version": 1, "actions": []string{"a1"}}, 404)

	// The assistant reads them: kb_get prints the minutes before the transcript, kb_list marks the recording.
	got, err := s.toolKB(context.Background(), "kb_get", json.RawMessage(`{"kind":"recordings","id":"`+id+`"}`))
	if err != nil || !strings.Contains(got, "minutes.md:\n# 发布评审") || !strings.Contains(got, "transcript.md:") ||
		strings.Index(got, "minutes.md:") > strings.Index(got, "transcript.md:") {
		t.Fatalf("kb_get: %v\n%s", err, got)
	}
	if list, _ := s.toolKB(context.Background(), "kb_list", json.RawMessage(`{"kind":"recordings"}`)); !strings.Contains(list, " minutes") {
		t.Fatalf("kb_list: %s", list)
	}

	// Regenerate: pending again (the old minutes stay), a new version and a second push once written.
	if body := request("POST", path+"/minutes", "", nil, 202); !strings.Contains(body, `"state":"pending"`) {
		t.Fatalf("regenerate: %s", body)
	}
	e.WriteMinutes(context.Background(), 2)
	if pushes := f.pushed(); len(pushes) != 2 || pushes[1].ID != "ntf_minutes_"+id+"_2" {
		t.Fatalf("pushes %+v", pushes)
	}
	request("POST", "/api/v1/recordings/33333333-3333-4333-8333-333333333333/minutes", "", nil, 404)
	// Deleting the recording takes the minutes with it.
	request("DELETE", path, "", nil, 200)
	request("GET", "/api/v1/kb/recordings/"+id, "", nil, 404)
}
