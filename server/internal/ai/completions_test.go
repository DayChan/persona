package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestChatCompletionsStreaming(t *testing.T) {
	for _, base := range []string{"/prefix", "/prefix/v1/"} {
		t.Run(base, func(t *testing.T) {
			var request struct {
				Model     string    `json:"model"`
				Messages  []Message `json:"messages"`
				Stream    bool      `json:"stream"`
				MaxTokens int       `json:"max_tokens"`
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/prefix/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer example-key" || r.Header.Get("Accept") != "text/event-stream" {
					t.Errorf("unexpected request: %s, headers %v", r.URL.Path, r.Header)
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, ": keepalive\r\n\r\n")
				fmt.Fprint(w, "data: {\"model\":\"model-v2\",\r\ndata: \"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"secret\",\"content\":\"Hello\"}}]}\r\n\r\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"other\"}},{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"length\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\ndata: [DONE]\n\n")
			}))
			defer s.Close()
			turns := []Message{{RoleUser, "first"}, {RoleAssistant, "second"}, {RoleUser, "third"}}
			reply, err := New(Config{API: "chat", BaseURL: s.URL + base, APIKey: "example-key", Model: "model"}).Chat(context.Background(), Request{System: "system", Messages: turns, MaxTokens: 100})
			if err != nil {
				t.Fatal(err)
			}
			wantTurns := append([]Message{{Role: "system", Content: "system"}}, turns...)
			if request.Model != "model" || !request.Stream || request.MaxTokens != 100 || !reflect.DeepEqual(request.Messages, wantTurns) {
				t.Fatalf("request: %+v", request)
			}
			want := Reply{Text: "Hello world", Model: "model-v2", Truncated: true, Usage: Usage{InputTokens: 8, OutputTokens: 3, CacheReadInputTokens: 4}}
			if reply != want {
				t.Fatalf("reply = %+v, want %+v", reply, want)
			}
		})
	}
}

func TestChatCompletionsTokenParameterRetry(t *testing.T) {
	for _, test := range []struct {
		name, errorMessage string
		retry              bool
	}{
		{"unsupported", "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.", true},
		{"unrelated", "max_tokens is invalid", false},
		{"no unsupported", "max_tokens and max_completion_tokens cannot both be set", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests []map[string]any
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests = append(requests, body)
				if len(requests) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": test.errorMessage}})
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer s.Close()
			r, err := New(Config{API: "chat", BaseURL: s.URL, APIKey: "example-key", Model: "model"}).Chat(context.Background(), Request{MaxTokens: 42})
			if requests[0]["max_tokens"] != float64(42) {
				t.Fatalf("first request: %v", requests[0])
			}
			if test.retry {
				if err != nil || r.Text != "ok" || len(requests) != 2 || requests[1]["max_completion_tokens"] != float64(42) {
					t.Fatalf("reply=%+v err=%v requests=%v", r, err, requests)
				}
				if _, exists := requests[1]["max_tokens"]; exists {
					t.Fatalf("both fields sent: %v", requests[1])
				}
			} else if err == nil || len(requests) != 1 {
				t.Fatalf("err=%v requests=%v", err, requests)
			}
		})
	}
}

func TestChatCompletionsErrors(t *testing.T) {
	for _, tt := range []struct {
		name, stream string
		want         error
		contains     string
	}{
		{"refusal", `data: {"choices":[{"index":0,"delta":{"refusal":"no"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n", ErrRefused, ""},
		{"filter", `data: {"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}` + "\n\n", ErrRefused, ""},
		{"error", `data: {"error":{"message":"provider failed"}}` + "\n\n", nil, "provider failed"},
		{"malformed", "data: {bad json}\n\n", nil, "decode stream"},
		{"interrupted", `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n", nil, "without a completed reply"},
		{"empty done", "data: [DONE]\n\n", nil, "without a reply"},
		{"empty choice", `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n", nil, "without a reply"},
		{"reasoning only", `data: {"choices":[{"delta":{"reasoning_content":"private","content":"  "},"finish_reason":"stop"}]}` + "\n\n", nil, "without a reply"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readCompletions(strings.NewReader(tt.stream))
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error=%v, want %v", err, tt.want)
			}
			if tt.contains != "" && (err == nil || !strings.Contains(err.Error(), tt.contains)) {
				t.Fatalf("error=%v, want %q", err, tt.contains)
			}
		})
	}
}

func TestChatCompletionsRateLimitAndReadError(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"quota exhausted"}}`)
		}))
		defer s.Close()
		_, err := New(Config{API: "chat", BaseURL: s.URL, APIKey: "example-key", Model: "model"}).Chat(context.Background(), Request{MaxTokens: 20})
		if !IsPaused(err) {
			t.Fatalf("expected paused quota error, got %v", err)
		}
	})
	t.Run("read error", func(t *testing.T) {
		_, err := readCompletions(io.MultiReader(strings.NewReader("data: {\"choices\":[]}"), failingReader{}))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestChatCompletionsJSONRetriesTruncation(t *testing.T) {
	var limits []int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		limits = append(limits, req.MaxTokens)
		if len(limits) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answer\\\":42}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer s.Close()
	var out struct{ Answer int }
	err := New(Config{API: "chat", BaseURL: s.URL, APIKey: "example-key", Model: "model"}).JSONMax(context.Background(), "system", "user", 16, &out)
	if err != nil || out.Answer != 42 || !reflect.DeepEqual(limits, []int{16, 32}) {
		t.Fatalf("output=%+v limits=%v error=%v", out, limits, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
