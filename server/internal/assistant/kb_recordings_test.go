package assistant

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// Exercise the manager's real upload/detail/search routes and index callback;
// all storage and HTTP traffic belong to this synthetic fixture.
func TestRecordingFailedUploadHTTPRetainsAcceptedRevision(t *testing.T) {
	index := newKBStore()
	service := kbServiceOn(t, newFakeHub(), index, t.TempDir(), nil)
	engine := kbEng(t, service)
	server := httptest.NewServer(service)
	defer server.Close()
	const id = "11111111-1111-4111-8111-111111111111"
	upload := kb.RecordingUpload{Revision: 1, StartedAt: time.Now().UTC(), Device: "phone", Title: "Accepted sample", Segments: []kb.RecordingSegment{{Text: "accepted orchard", End: 1}}}
	request := func(method, path string, body any, want int) string {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s (%v)", method, path, resp.StatusCode, raw, err)
		}
		return string(raw)
	}
	path := "/api/v1/recordings/" + id
	request("PUT", path, upload, 200)
	temp := engine.Store.Path(kb.KindRecording, id, "meta.json.tmp")
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	upload.Revision, upload.Title, upload.Segments[0].Text = 2, "Rejected sample", "rejected zeppelin"
	request("PUT", path, upload, 500)
	detail := request("GET", "/api/v1/kb/recordings/"+id, nil, 200)
	if !strings.Contains(detail, "accepted orchard") || strings.Contains(detail, "rejected zeppelin") || !strings.Contains(detail, `"revision":1`) {
		t.Fatalf("detail = %s", detail)
	}
	search := request("GET", "/api/v1/kb-search?q=rejected", nil, 200)
	if strings.Contains(search, id) {
		t.Fatalf("rejected search = %s", search)
	}
	states := request("GET", "/api/v1/recordings", nil, 200)
	if !strings.Contains(states, `"revision":1`) {
		t.Fatalf("states = %s", states)
	}
	index.mu.Lock()
	name := index.rows[kb.KindRecording+"/"+id]
	index.mu.Unlock()
	if name != "Accepted sample" {
		t.Fatalf("index published rejected metadata: %q", name)
	}
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}
	request("PUT", path, upload, 200)
	detail = request("GET", "/api/v1/kb/recordings/"+id, nil, 200)
	if !strings.Contains(detail, "rejected zeppelin") || !strings.Contains(detail, `"revision":2`) {
		t.Fatalf("retry detail = %s", detail)
	}
}
