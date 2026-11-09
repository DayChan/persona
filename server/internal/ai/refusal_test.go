package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A declined request is a RefusalError (errors.Is ErrRefused) carrying the upstream's reason, on both API kinds, with
// the call's usage still in the reply (#1210).
func TestChatRefusalsCarryTheStopReasonAndUsage(t *testing.T) {
	completed := `{"type":"response.completed","response":{"model":"gpt-x","usage":{"input_tokens":10,"output_tokens":2}}}`
	for _, tc := range []struct {
		name, api, reason string
		body              []string // Messages: one JSON body; Responses: the stream's events
	}{
		{"messages", "", "refusal",
			[]string{`{"model":"m-2025","content":[],"stop_reason":"refusal","usage":{"input_tokens":10,"output_tokens":2}}`}},
		{"responses refusal part", "responses", "refusal", []string{
			`{"type":"response.content_part.added","part":{"type":"refusal","refusal":""}}`,
			`{"type":"response.refusal.delta","delta":"I can't help with that."}`,
			completed,
		}},
		{"responses refusal delta only", "responses", "refusal", []string{
			`{"type":"response.refusal.delta","delta":"I can't help with that."}`,
			completed,
		}},
		{"responses refusal beside text", "responses", "refusal", []string{
			`{"type":"response.output_text.delta","delta":"partly "}`,
			`{"type":"response.content_part.done","part":{"type":"refusal","refusal":"no"}}`,
			completed,
		}},
		{"responses content filter", "responses", "content_filter", []string{
			`{"type":"response.output_text.delta","delta":"cut"}`,
			`{"type":"response.incomplete","response":{"model":"gpt-x","incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":10,"output_tokens":2}}}`,
		}},
		{"chat refusal", "chat", "refusal", []string{
			`{"model":"gpt-x","choices":[{"index":0,"delta":{"refusal":"no"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
			`[DONE]`,
		}},
		{"chat content filter", "chat", "content_filter", []string{
			`{"model":"gpt-x","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
			`[DONE]`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.api == "" {
					_, _ = w.Write([]byte(tc.body[0]))
					return
				}
				for _, ev := range tc.body {
					_, _ = w.Write([]byte("data: " + ev + "\n\n"))
				}
			}))
			defer srv.Close()
			r, err := New(Config{API: tc.api, BaseURL: srv.URL, APIKey: "k", Model: "m"}).Chat(context.Background(),
				Request{Messages: []Message{{RoleUser, "a"}}})
			var refusal *RefusalError
			if !errors.As(err, &refusal) || !errors.Is(err, ErrRefused) || refusal.StopReason != tc.reason {
				t.Fatalf("err = %#v, want a refusal %q", err, tc.reason)
			}
			if r.Text != "" || r.Usage.InputTokens != 10 || r.Usage.OutputTokens != 2 || r.Model == "" {
				t.Fatalf("reply %+v", r)
			}
		})
	}
}

// An incomplete response for another reason (max_output_tokens) is still a plain failure, not a refusal.
func TestResponsesIncompleteForAnotherReasonIsNoRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n"))
	}))
	defer srv.Close()
	_, err := New(Config{API: "responses", BaseURL: srv.URL, APIKey: "k", Model: "m"}).Chat(context.Background(),
		Request{Messages: []Message{{RoleUser, "a"}}})
	if err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want a failure that is not a refusal", err)
	}
}
