package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Several devices answering one card at once through the route the hub relays (#1073, spec/fixtures/sync
// both_devices_answer_once): one answer reaches the agent; every other device is told it was answered already, and
// with that same answer, so each shows the card answered as the agent got it, and none is left with it open.
func TestAnswersFromSeveralDevicesAgreeOnTheOneThatWon(t *testing.T) {
	for round := range 40 {
		ft, ix, done := answerFleet(t)
		h := ft.s.fleetHandler(http.NotFoundHandler())
		picks := []string{"发", "不发", "发"}
		type reply struct {
			code int
			body struct {
				OK         bool              `json:"ok"`
				Already    bool              `json:"already"`
				Resolution *proto.Resolution `json:"resolution"`
			}
		}
		replies := make([]reply, len(picks))
		var wg sync.WaitGroup
		for i, pick := range picks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(`{"answers":{"发吗？":"`+pick+`"}}`)))
				replies[i].code = w.Code
				_ = json.Unmarshal(w.Body.Bytes(), &replies[i].body)
			}()
		}
		wg.Wait()
		got := <-done
		won := 0
		for i, r := range replies {
			switch {
			case r.code != http.StatusOK:
				t.Fatalf("round %d: device %d got %d", round, i, r.code)
			case !r.body.Already:
				won++
				if picks[i] != got.Answers["发吗？"] {
					t.Fatalf("round %d: device %d was told its %q went, the agent got %q", round, i, picks[i], got.Answers["发吗？"])
				}
			case r.body.Resolution == nil || r.body.Resolution.Answers["发吗？"] != got.Answers["发吗？"]:
				t.Fatalf("round %d: device %d was told it was answered already, with %+v; the agent got %q", round, i, r.body.Resolution, got.Answers["发吗？"])
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d devices were told their answer went", round, won)
		}
		if n := len(ft.s.pendingInteractions()); n != 0 {
			t.Fatalf("round %d: %d cards still pending", round, n)
		}
	}
}
