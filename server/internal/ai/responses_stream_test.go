package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// responsesStream sends one request to a Responses API that writes body as its whole event stream.
func responsesStream(t *testing.T, body string) (Reply, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	return New(Config{API: "responses", BaseURL: srv.URL, APIKey: "k", Model: "m"}).Chat(context.Background(),
		Request{Messages: []Message{{RoleUser, "a"}}})
}

// A stream that stops before response.completed, response.incomplete or response.failed was cut: its text is only
// part of the answer, so it is an error rather than a short reply.
func TestResponsesStreamCutBeforeItsTerminalEventFails(t *testing.T) {
	for name, body := range map[string]string{
		"after deltas": `data: {"type":"response.output_text.delta","delta":"half a ti"}` + "\n\n",
		"before any":   `data: {"type":"response.created"}` + "\n\n",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			r, err := responsesStream(t, body)
			if !errors.Is(err, io.ErrUnexpectedEOF) || r.Text != "" {
				t.Fatalf("reply %+v, err %v; want no text and io.ErrUnexpectedEOF", r, err)
			}
		})
	}
}

// Events are framed the SSE way: the data lines of one event are joined with "\n", comments and other fields are
// skipped, CRLF line ends work, the last event needs no blank line after it, and a data line after data that already
// makes a whole object starts the next event.
func TestResponsesStreamFramesEventsTheSSEWay(t *testing.T) {
	completed := `{"type":"response.completed","response":{"model":"gpt-x"}}`
	for name, body := range map[string]string{
		"multi-line data": "data: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"hello\"}\n\n" +
			"data: " + completed + "\n\n",
		"comment and CRLF": ": keep-alive\r\n\r\n" +
			"event: response.output_text.delta\r\nid: 1\r\ndata:{\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\r\n\r\n" +
			": keep-alive\r\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\r\n\r\n" +
			"event: response.completed\r\ndata: " + completed + "\r\n\r\n",
		"no blank line after the last event": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
			"data: " + completed,
		"only a line end after the last event": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
			"data: " + completed + "\n",
		// a relay sending one data line per event and no blank line between: a line after a whole object starts the next
		"no blank line between events": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n" + "data: " + completed + "\n",
		// a line ending in } inside an object that goes on does not end the event
		"nested object across lines": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\n" + "data: \"model\":\"gpt-x\"}\n" + "data: }\n\n",
		// the terminal event ends the reply even when more data lines follow it with no blank line between
		"data after the terminal event": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n" +
			"data: " + completed + "\n" + "data: [DONE]\n" + "data: {\"type\":\"response.output_text.delta\",\"delta\":\" more\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			r, err := responsesStream(t, body)
			if err != nil || r.Text != "hello" || r.Model != "gpt-x" {
				t.Fatalf("reply %+v, err %v; want hello from gpt-x", r, err)
			}
		})
	}
}
