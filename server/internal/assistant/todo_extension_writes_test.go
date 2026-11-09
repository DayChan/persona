package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// doExt is an extension's request as the hub relays it: its role and which extension (the hub sets both, and it relays
// only the routes the extension's scopes open, extwire.RouteScopes).
func (e *todoEnv) doExt(ext, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(RoleHeader, roleExtension)
	if ext != "" {
		r.Header.Set(ExtensionHeader, ext)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

// An extension granted todos.write changes todos through the routes that scope opens: create, edit, attach and detach
// an agent. A read-only device still may not. (Before, the assistant took the extension's role for a read-only one
// and turned every todos.write route away with 403.)
func TestTodoWritesFromAnExtension(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.f.setSessions("box", proto.Session{SessionID: "s1", Title: "迁移 Agent"})
	var made struct{ Todo Todo }
	w := e.doExt("ats", "POST", "/api/v1/todos", `{"title":"发 1.3 版"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create as an extension: %d %s", w.Code, w.Body)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &made)
	id := made.Todo.ID
	if w := e.doExt("ats", "PATCH", "/api/v1/todos/"+id, `{"notes":"ATS 任务 t1"}`); w.Code != http.StatusOK {
		t.Fatalf("edit as an extension: %d %s", w.Code, w.Body)
	}
	if w := e.doExt("ats", "PUT", "/api/v1/todos/"+id+"/agents/box/s1", ""); w.Code != http.StatusOK {
		t.Fatalf("link an agent as an extension: %d %s", w.Code, w.Body)
	}
	if got := e.get(id); got == nil || got.Notes != "ATS 任务 t1" || len(got.Agents) != 1 {
		t.Fatalf("todo after the extension's writes = %+v", got)
	}
	if w := e.doExt("ats", "DELETE", "/api/v1/todos/"+id+"/agents/box/s1", ""); w.Code != http.StatusOK {
		t.Fatalf("unlink as an extension: %d %s", w.Code, w.Body)
	}
	if w := e.doAs("answer", "PATCH", "/api/v1/todos/"+id, `{"notes":"x"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a read-only device edits: %d", w.Code)
	}
	// Deleting a todo stays with full credentials (the hub does not relay it for a token; the assistant agrees).
	if w := e.doExt("ats", "DELETE", "/api/v1/todos/"+id, ""); w.Code != http.StatusForbidden {
		t.Fatalf("an extension deletes a todo: %d", w.Code)
	}
}

// An extension writes outcome decisions only (a scheduler's acceptance evidence), with its provenance, never the user's
// manual ones and never superseding; a ref makes a replay write nothing. A device may still write either source.
func TestTodoOutcomeDecisionsFromAnExtension(t *testing.T) {
	e := newTodoEnv(t, todoTestDB(t))
	id := e.create("发 1.3 版").ID
	path := "/api/v1/todos/" + id + "/decisions"

	post := func(ext, body string) (int, struct {
		Decision db.TodoDecisionRow
		Inserted bool
	}) {
		var out struct {
			Decision db.TodoDecisionRow
			Inserted bool
		}
		w := e.doExt(ext, "POST", path, body)
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	evidence := `{"question":"验收通过了吗？","choice":"通过","reason":"npm test 退出码 0","ref":"task_1/1"}`
	code, first := post("ats", evidence)
	if code != http.StatusOK || !first.Inserted || first.Decision.Source != db.DecisionOutcome ||
		first.Decision.SourceMachine != "extension:ats" || first.Decision.SourceMessage != "ext:ats:task_1/1" {
		t.Fatalf("outcome from an extension: %d %+v", code, first)
	}
	if code, again := post("ats", evidence); code != http.StatusOK || again.Inserted {
		t.Fatalf("a replay of the same ref: %d %+v", code, again)
	}
	if live := listDecisions(t, e, id, ""); len(live) != 1 {
		t.Fatalf("decisions after a replay = %+v", live)
	}
	for name, body := range map[string]string{
		"manual":     `{"question":"q","choice":"c","source":"manual"}`,
		"card":       `{"question":"q","choice":"c","source":"card"}`,
		"supersedes": `{"question":"q","choice":"c","supersedes_id":"` + first.Decision.ID + `"}`,
		"ref with #": `{"question":"q","choice":"c","ref":"a#o"}`,
		"long ref":   `{"question":"q","choice":"c","ref":"` + strings.Repeat("r", decisionRefMax+1) + `"}`,
		"empty":      `{"question":"","choice":"c"}`,
	} {
		if code, _ := post("ats", body); code != http.StatusBadRequest {
			t.Errorf("%s from an extension: %d, want 400", name, code)
		}
	}
	if code, _ := post("", evidence); code != http.StatusBadRequest {
		t.Errorf("an extension token without its extension: %d, want 400", code)
	}
	var dev struct{ Decision db.TodoDecisionRow }
	_ = json.Unmarshal(e.must("POST", path, `{"question":"结论","choice":"先发 iOS","source":"outcome"}`).Body.Bytes(), &dev)
	if dev.Decision.Source != db.DecisionOutcome || dev.Decision.SourceMachine != "" {
		t.Fatalf("an outcome from a device = %+v", dev.Decision)
	}
	if w := e.do("POST", path, `{"question":"q","choice":"c","ref":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a ref from a device: %d", w.Code)
	}
	if w := e.doAs("answer", "POST", path, `{"question":"q","choice":"c"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a read-only device: %d", w.Code)
	}
}
