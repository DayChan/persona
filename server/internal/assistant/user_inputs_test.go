package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/devices"
)

type userInputDevices struct {
	fakeDevices
	calls  int
	method string
	params json.RawMessage
}

func (d *userInputDevices) List() []devices.Summary { return []devices.Summary{{ID: "example-node"}} }
func (d *userInputDevices) CallNode(_ context.Context, id, method string, params json.RawMessage) (json.RawMessage, error) {
	d.calls++
	d.method = method
	d.params = params
	return json.RawMessage(`{"total":2,"inputs":[{"id":"input-a#0","seq":1},{"id":"input-b#0","seq":20}],"history":"fixture"}`), nil
}
func TestUserInputRouteIsReadOnlyAndPositionOnly(t *testing.T) {
	d := &userInputDevices{}
	s := New(Options{Devices: d})
	response := httptest.NewRecorder()
	s.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/example-node/synthetic/user-inputs", nil))
	if response.Code != 200 || d.calls != 1 || d.method != "session.user-inputs" || string(d.params) != `{"session_id":"synthetic"}` {
		t.Fatalf("status %d method %s params %s body %s", response.Code, d.method, d.params, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("index is cacheable")
	}
	wrong := httptest.NewRecorder()
	s.ServeHTTP(wrong, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/example-node/synthetic/user-inputs", nil))
	if wrong.Code != 405 || d.calls != 1 {
		t.Fatalf("POST %d calls %d", wrong.Code, d.calls)
	}
	unknown := httptest.NewRecorder()
	s.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/unknown/synthetic/user-inputs", nil))
	if unknown.Code != 404 || d.calls != 1 {
		t.Fatalf("unknown %d", unknown.Code)
	}
}
