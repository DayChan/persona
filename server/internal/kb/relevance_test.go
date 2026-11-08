package kb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
)

// modelReplying is a real ai.Client whose Messages API answers every call with text, so the reply goes through the
// same parsing (ai.FirstObject, then unmarshalReply) as a model's on a running hub.
func modelReplying(t *testing.T, text string) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
	}))
	t.Cleanup(srv.Close)
	return ai.New(ai.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
}

// A model sometimes writes the relevance score as a string ("80"). The digest is still the model's answer: its
// summary is kept and the score is read from the string, instead of the whole compression failing on that one field
// (#1099: "cannot unmarshal string into Go struct field chatDigest.relevance of type int").
func TestCompressTakesARelevanceWrittenAsAString(t *testing.T) {
	e := testEngine(t, modelReplying(t, `{"summary":"Meeting on Friday.","one_line":"Friday","importance":"P2","relevance":"80","tags":["meeting"]}`))
	c := dueChat(t, e)
	if err := e.Compress(context.Background(), c); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	got, _ := e.Store.Chat(c.ID)
	if got.Relevance != 80 || got.Importance != "P2" {
		t.Errorf("tags = %s / %d, want P2 / 80", got.Importance, got.Relevance)
	}
	if e.Store.Summary(KindChat, c.ID) == "" {
		t.Error("the summary was not written")
	}
}

// A score that is no number at all is ignored: the chat keeps the relevance it had, and the rest of the answer counts.
func TestCompressKeepsTheRelevanceWhenTheModelWritesAWord(t *testing.T) {
	e := testEngine(t, modelReplying(t, `{"summary":"Meeting on Friday.","one_line":"Friday","importance":"P1","relevance":"high","tags":["meeting"]}`))
	c := dueChat(t, e)
	c.Relevance = 40
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	if err := e.Compress(context.Background(), c); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	got, _ := e.Store.Chat(c.ID)
	if got.Relevance != 40 || got.Importance != "P1" {
		t.Errorf("tags = %s / %d, want P1 / 40 (importance taken, relevance kept)", got.Importance, got.Relevance)
	}
	if e.Store.Summary(KindChat, c.ID) == "" {
		t.Error("the summary was not written")
	}
}

func TestModelScoreReadsWhatModelsWrite(t *testing.T) {
	cases := map[string]modelScore{
		`80`: 80, `0`: 0, `100`: 100, `80.4`: 80,
		`"80"`: 80, `" 75 "`: 75, `"80.6"`: 81, `"100"`: 100,
		`"high"`: -1, `""`: -1, `"NaN"`: -1, `"Inf"`: -1, `150`: -1, `-5`: -1, `"101"`: -1,
		`true`: -1, `{}`: -1, `[]`: -1,
		`null`: 0, // as an int field: null leaves it as it is
	}
	for in, want := range cases {
		var v struct {
			Relevance modelScore `json:"relevance"`
		}
		if err := json.Unmarshal([]byte(`{"relevance":`+in+`}`), &v); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if v.Relevance != want {
			t.Errorf("%s: relevance = %d, want %d", in, v.Relevance, want)
		}
	}
	var absent struct {
		Relevance modelScore `json:"relevance"`
	}
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil || absent.Relevance != 0 {
		t.Errorf("absent: relevance = %d, %v; want 0", absent.Relevance, err)
	}
}

// A project rewrite, a batch of tagged items and a recording summary read the same way: one odd score no longer fails
// the rest (before #1099 one quoted score failed the whole batch of up to 40 items).
func TestProjectAndItemDigestsTakeAQuotedRelevance(t *testing.T) {
	var p projectDigest
	if err := json.Unmarshal([]byte(`{"summary":"s","importance":"P1","relevance":"75"}`), &p); err != nil || p.Relevance != 75 {
		t.Errorf("projectDigest relevance = %d, %v; want 75", p.Relevance, err)
	}
	var it itemTags
	if err := json.Unmarshal([]byte(`{"items":[{"id":"a","relevance":60},{"id":"b","relevance":"70"},{"id":"c","relevance":"n/a"}]}`), &it); err != nil {
		t.Fatalf("itemTags: %v", err)
	}
	got := []modelScore{}
	for _, x := range it.Items {
		got = append(got, x.Relevance)
	}
	if len(got) != 3 || got[0] != 60 || got[1] != 70 || got[2] != -1 {
		t.Errorf("item relevances = %v, want [60 70 -1]", got)
	}
	var r recordingSummary
	if err := json.Unmarshal([]byte(`{"title":"t","summary":"s","relevance":"80"}`), &r); err != nil || r.Relevance != 80 {
		t.Errorf("recordingSummary relevance = %d, %v; want 80", r.Relevance, err)
	}
}

// A recording's summary reads the same way end to end: a quoted score is taken, and the title and summary the model
// wrote are kept instead of the recording staying unsummarized.
func TestSummarizeRecordingsTakesARelevanceWrittenAsAString(t *testing.T) {
	e := testEngine(t, modelReplying(t, `{"title":"Weekly sync","summary":"Release on Friday.","one_line":"Friday","importance":"P2","relevance":"80","tags":["sync"]}`))
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if _, err := e.PutRecording(recID, recUpload(1, now.Add(-time.Hour), true, "release first", "Friday rollout"), now); err != nil {
		t.Fatal(err)
	}
	if n := e.SummarizeRecordings(context.Background(), 3); n != 1 {
		t.Fatalf("summarized %d, want 1", n)
	}
	r, _ := e.Store.Recording(recID)
	if r.Relevance != 80 || r.Importance != "P2" || r.AITitle != "Weekly sync" {
		t.Errorf("recording = %s / %d / %q, want P2 / 80 / \"Weekly sync\"", r.Importance, r.Relevance, r.AITitle)
	}
	if e.Store.Summary(KindRecording, recID) == "" {
		t.Error("the summary was not written")
	}
}
