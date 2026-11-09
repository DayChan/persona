package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// rawModelService is an assistant whose "Knowledge base" connection speaks api ("" for the Messages API, or
// "responses") to handler, with an extension kb-curator granted models.
func rawModelService(t *testing.T, api string, handler http.HandlerFunc) *Service {
	t.Helper()
	s, f, _ := modelService(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe, not this test's client
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("upstream-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.hello.Models.AI = ai.Config{API: api, BaseURL: srv.URL, APIKeyFile: key, Model: "kb-model"}
	f.modelExts = []assistantwire.ModelExtension{{ID: "kb-curator", Name: "kb curator"}}
	return s
}

// A declined input reaches the extension as model_refused with the upstream's stop_reason and what the call spent,
// from either API kind; an upstream 5xx is still model_failed (#1210).
func TestExtensionCompleteCodesARefusal(t *testing.T) {
	for _, tc := range []struct {
		name, api, reason string
		handler           http.HandlerFunc
	}{
		{"messages", "", "refusal", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"model":"kb-model","content":[],"stop_reason":"refusal","usage":{"input_tokens":40,"output_tokens":3}}`))
		}},
		{"responses", "responses", "refusal", func(w http.ResponseWriter, r *http.Request) {
			for _, ev := range []string{
				`{"type":"response.content_part.added","part":{"type":"refusal","refusal":""}}`,
				`{"type":"response.refusal.delta","delta":"no"}`,
				`{"type":"response.completed","response":{"model":"kb-model","usage":{"input_tokens":40,"output_tokens":3}}}`,
			} {
				_, _ = w.Write([]byte("data: " + ev + "\n\n"))
			}
		}},
		{"responses content filter", "responses", "content_filter", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":40,"output_tokens":3}}}` + "\n\n"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := rawModelService(t, tc.api, tc.handler)
			_, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"x"}],"json_schema":{"type":"object"}}`)
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != assistantwire.CodeModelRefused {
				t.Fatalf("err = %v, want %s", err, assistantwire.CodeModelRefused)
			}
			var data assistantwire.ModelRefusal
			if err := json.Unmarshal(re.Data, &data); err != nil {
				t.Fatal(err)
			}
			want := assistantwire.ModelRefusal{StopReason: tc.reason, Usage: assistantwire.ModelUsage{InputTokens: 40, OutputTokens: 3, Requests: 1}}
			if data != want {
				t.Fatalf("data %+v, want %+v", data, want)
			}
		})
	}
}

func TestExtensionCompleteKeepsAnUpstreamErrorAsModelFailed(t *testing.T) {
	s := rawModelService(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"upstream broke"}}`, http.StatusInternalServerError)
	})
	_, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"x"}]}`)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeModelFailed {
		t.Fatalf("err = %v, want %s", err, assistantwire.CodeModelFailed)
	}
}
