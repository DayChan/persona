package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The hub's http.request is served by one handler per service, built on the first request, not by a new pair of
// ServeMuxes per request; a service put in place since (Serve) gets one of its own and answers from then on.
func TestRelayedRequestsShareTheHandler(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "jb_x", Devices: &fakeDevices{}})
	b.helloOnce.Do(func() { close(b.helloed) })
	service := func(title string) *Service {
		s := New(Options{Hub: newFakeHub()})
		s.inbox.init()
		s.inbox.items["x"] = &InboxItem{ID: "x", Title: title, UpdatedAt: time.Now()}
		return s
	}
	get := func(s *Service) string {
		t.Helper()
		b.mu.Lock()
		b.svc = s
		b.mu.Unlock()
		params, _ := json.Marshal(assistantwire.HTTPRequest{Method: "GET", Path: "/api/v1/inbox"})
		out, err := b.request(context.Background(), assistantwire.MethodHTTP, params)
		res, ok := out.(assistantwire.HTTPResponse)
		if err != nil || !ok || res.Status != 200 {
			t.Fatalf("GET: %v %+v", err, out)
		}
		return string(res.Body)
	}
	first, second := service("first"), service("second")
	if body := get(first); !strings.Contains(body, `"title":"first"`) {
		t.Fatalf("first service: %s", body)
	}
	kept := b.httpH
	get(first)
	if b.httpH != kept {
		t.Fatal("the handler was built again for the same service")
	}
	if body := get(second); !strings.Contains(body, `"title":"second"`) || b.httpH == kept {
		t.Fatalf("the new service is not the one answering: %s", body)
	}
}
