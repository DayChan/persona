package proc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// The current protocol's six outcomes, including two ways the message is known to be gone and two answers that leave
// its state unknown. No answer except done / already_recalled may succeed.
func TestRecallCurrentProtocolOutcomes(t *testing.T) {
	c, _, _ := linkedRaw(t, declaresRecall, time.Time{}, "PERSONA_PROC_RECALL_V2=1")
	for _, tc := range []struct {
		ref  string
		want error
		text string
	}{
		{"ok", nil, ""},
		{"already", nil, ""},
		{"cannot", connector.ErrCannotRecall, "older than the service allows"},
		{"signed-out", connector.ErrRecallUnavailable, "the account is logged out"},
		{"limited", connector.ErrRecallUnavailable, "slow down"},
		{"offline", connector.ErrRecallUnavailable, "the computer is not connected"},
		{"unknown", connector.ErrRecallUnknown, "the service did not answer"},
		{"empty", connector.ErrRecallUnknown, "known recall outcome"},
		{"bogus", connector.ErrRecallUnknown, "known recall outcome"},
	} {
		err := recallOf(c, tc.ref)
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("%s: %v", tc.ref, err)
		case tc.want != nil && (!errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.text)):
			t.Errorf("%s: %v, want %v with %q", tc.ref, err, tc.want, tc.text)
		}
	}
	err := recallOf(c, "refused")
	if err == nil || err.Error() != strings.Repeat("字", 200) || errors.Is(err, connector.ErrCannotRecall) ||
		errors.Is(err, connector.ErrRecallUnavailable) || errors.Is(err, connector.ErrRecallUnknown) {
		t.Errorf("a refusal: %v", err)
	}
}

// A current do has exactly the legacy identity fields plus request_id. At remains for the connector's own time limit;
// no ledger text, origin, approval record or credential is added. Explicit retries of one message use the same opaque id.
func TestRecallCurrentProtocolFieldsAndStableRequestID(t *testing.T) {
	c, _, logFile := linkedRaw(t, declaresRecall, time.Time{}, "PERSONA_PROC_RECALL_V2=1")
	at := time.Date(2026, 11, 7, 3, 4, 5, 0, time.UTC)
	m := connector.SentMessage{Action: "examplechat_send", Ref: "unknown", At: at,
		Target: json.RawMessage(`{"method":"POST","command":"ex","conversation":"c1"}`)}
	for range 2 {
		if err := c.Recall(context.Background(), m); !errors.Is(err, connector.ErrRecallUnknown) {
			t.Fatalf("recall: %v", err)
		}
	}
	got := asked(t, logFile)
	if len(got) != 2 {
		t.Fatalf("the explicit retries asked %d times", len(got))
	}
	var firstID string
	for _, params := range got {
		wantKeys := []string{"action", "at", "contribution", "message_ref", "request_id", "target"}
		if len(params) != len(wantKeys) {
			t.Fatalf("params are %v, want exactly %v", params, wantKeys)
		}
		for _, key := range wantKeys {
			if params[key] == nil {
				t.Errorf("no %s in %v", key, params)
			}
		}
		str := func(k string) string { var s string; _ = json.Unmarshal(params[k], &s); return s }
		id := str("request_id")
		if id == "" || !strings.HasPrefix(id, "recall_") || firstID != "" && id != firstID {
			t.Errorf("request ids %q and %q", firstID, id)
		}
		firstID = id
		if str("action") != m.Action || str("message_ref") != m.Ref || str("at") != "2026-11-07T03:04:05Z" || str("contribution") != "example" {
			t.Errorf("params %v", params)
		}
	}
}

// A current can=false stops before any write. A program that implements can but not do is not sent a second kind of
// write through legacy recall: its partial implementation is not_recallable and the log stays empty.
func TestRecallCurrentCanAndPartialProgram(t *testing.T) {
	t.Run("can refuses", func(t *testing.T) {
		c, _, logFile := linkedRaw(t, declaresRecall, time.Time{}, "PERSONA_PROC_RECALL_V2=1")
		err := c.Recall(context.Background(), connector.SentMessage{Action: "cannot", Ref: "ok", At: time.Now()})
		if !errors.Is(err, connector.ErrCannotRecall) || len(asked(t, logFile)) != 0 {
			t.Fatalf("%v, writes %v", err, asked(t, logFile))
		}
	})
	t.Run("can without do", func(t *testing.T) {
		c, _, logFile := linkedRaw(t, declaresRecall, time.Time{}, "PERSONA_PROC_RECALL_V2=can-only")
		if err := recallOf(c, "ok"); !errors.Is(err, connector.ErrCannotRecall) || len(asked(t, logFile)) != 0 {
			t.Fatalf("%v, writes %v", err, asked(t, logFile))
		}
	})
}
