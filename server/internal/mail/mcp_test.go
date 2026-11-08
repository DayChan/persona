package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"testing"
	"time"
)

func TestMCPTools(t *testing.T) {
	s := newServiceWithAccount(t, newTestIMAP(t))

	tools := s.Tools()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("tool %s schema is not JSON: %v", tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("tool %s schema type = %v", tool.Name, schema["type"])
		}
	}
	for _, want := range []string{ToolAccounts, ToolSearch, ToolRead, ToolSaveAttachment, ToolSend} {
		if !names[want] {
			t.Errorf("tool %s is missing", want)
		}
	}
}

func TestMCPAccountsAndSearch(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	srv.appendMessage(t, "INBOX", rawMessage("Quarterly report", "boss@example.com", "Numbers are attached"))

	res := s.Call(ctx, ToolAccounts, json.RawMessage(`{}`), nil)
	if res.IsError {
		t.Fatalf("mail_accounts: %v", res.Content)
	}
	out := resultText(t, res)
	if !strings.Contains(out, "acc_test") || !strings.Contains(out, "me@example.com") {
		t.Errorf("mail_accounts output = %q", out)
	}

	res = s.Call(ctx, ToolSearch, json.RawMessage(`{"account_id":"acc_test","query":"quarterly","limit":5}`), nil)
	if res.IsError {
		t.Fatalf("mail_search: %v", res.Content)
	}
	out = resultText(t, res)
	if !strings.Contains(out, "Quarterly report") || !strings.Contains(out, "boss@example.com") {
		t.Errorf("mail_search output = %q", out)
	}

	res = s.Call(ctx, ToolSearch, json.RawMessage(`{"account_id":"acc_test","query":"no-such-text"}`), nil)
	if res.IsError {
		t.Fatalf("empty search should not be an error: %v", res.Content)
	}
	if !strings.Contains(resultText(t, res), "No messages matched") {
		t.Errorf("empty search output = %q", resultText(t, res))
	}

	res = s.Call(ctx, ToolSearch, json.RawMessage(`not json`), nil)
	if !res.IsError {
		t.Error("malformed arguments should be an error")
	}
}

// When the server cannot search (NetEase 163), mail_search says the headers were matched instead and, when a page
// stopped before the older mail, which before_uid carries on, also when it found nothing yet.
func TestMCPSearchServerCannotSearch(t *testing.T) {
	srv := newTestIMAP(t)
	srv.blindSearch.Store(true)
	s := newServiceWithAccount(t, srv)
	s.scan = localScan{batch: 1, budget: 1, time: time.Minute}
	ctx := context.Background()

	srv.appendMessage(t, "INBOX", rawMessage("Quarterly report", "boss@example.com", "Numbers are attached"))
	newer := srv.appendMessage(t, "INBOX", rawMessage("Lunch", "friend@example.com", "See you at noon"))

	res := s.Call(ctx, ToolSearch, json.RawMessage(`{"account_id":"acc_test","query":"quarterly"}`), nil)
	out := resultText(t, res)
	if res.IsError || !strings.Contains(out, "No messages matched among the ones looked at") ||
		!strings.Contains(out, fmt.Sprintf("pass before_uid=%d", newer)) || !strings.Contains(out, "senders and recipients were matched here") {
		t.Fatalf("first page = %q", out)
	}

	res = s.Call(ctx, ToolSearch, json.RawMessage(fmt.Sprintf(`{"account_id":"acc_test","query":"quarterly","before_uid":%d}`, newer)), nil)
	out = resultText(t, res)
	if res.IsError || !strings.Contains(out, "Quarterly report") || strings.Contains(out, "before_uid=") ||
		!strings.Contains(out, "does not look into message bodies") {
		t.Fatalf("second page = %q", out)
	}
}

// Every account's inbox at once has no before_uid that continues them all (UIDs are per account): mail_search
// sends the agent to one account instead.
func TestMCPSearchAllAccountsPaging(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	for _, subject := range []string{"One", "Two", "Three"} {
		srv.appendMessage(t, "INBOX", rawMessage(subject, "a@example.com", "x"))
	}

	res := s.Call(context.Background(), ToolSearch, json.RawMessage(`{"limit":2}`), nil)
	out := resultText(t, res)
	if res.IsError || !strings.Contains(out, "Three") || !strings.Contains(out, "search one account with account_id") ||
		strings.Contains(out, "before_uid=") {
		t.Fatalf("all inboxes = %q", out)
	}
}

func TestMCPRead(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	uid := srv.appendMessage(t, "INBOX", rawMessage("Read me", "a@example.com", "the body"))
	args, _ := json.Marshal(map[string]any{"account_id": "acc_test", "uid": uid})
	res := s.Call(ctx, ToolRead, args, nil)
	if res.IsError {
		t.Fatalf("mail_read: %v", res.Content)
	}
	out := resultText(t, res)
	for _, want := range []string{"Subject: Read me", "the body", "a@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("mail_read output missing %q:\n%s", want, out)
		}
	}

	// Reading does not mark the message as seen.
	msg, err := s.GetMessage(ctx, "acc_test", uint32(uid), "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Seen {
		t.Error("mail_read marked the message as seen")
	}
}

func TestMCPSendNeedsApproval(t *testing.T) {
	s := newServiceWithAccount(t, newTestIMAP(t))
	ctx := context.Background()

	sent := false
	s.sendMail = func(context.Context, *MailAccount, string, []string, []byte) error {
		sent = true
		return nil
	}
	args := json.RawMessage(`{"account_id":"acc_test","to":[{"email":"friend@example.com"}],"subject":"Hi","text":"Hello"}`)

	approved := false
	res := s.Call(ctx, ToolSend, args, func(ctx context.Context, summary string, input json.RawMessage) bool {
		approved = true
		if !strings.Contains(summary, "friend@example.com") || !strings.Contains(summary, "Hi") {
			t.Errorf("approval summary = %q", summary)
		}
		if string(input) != string(args) {
			t.Errorf("approval input = %s, want the original arguments", input)
		}
		return false
	})
	if !approved {
		t.Fatal("approve was not called")
	}
	if !res.IsError {
		t.Error("a declined send must report an error to the model")
	}
	if sent {
		t.Error("mail was sent even though the user declined")
	}

	res = s.Call(ctx, ToolSend, args, func(context.Context, string, json.RawMessage) bool { return true })
	if res.IsError {
		t.Fatalf("approved send: %v", res.Content)
	}
	if !sent {
		t.Error("approved send did not reach the SMTP transport")
	}
}

// A send says the Message-ID it went out with: the one in the header of the message the transport was handed, without
// the angle brackets, and a new one for each message. CallRef gives it with mail_send's answer, and nothing for a
// mail that was declined or did not go, or for another tool.
func TestSendSaysTheMessageID(t *testing.T) {
	s := newServiceWithAccount(t, newTestIMAP(t))
	ctx := context.Background()
	var raws [][]byte
	var fail error
	s.sendMail = func(_ context.Context, _ *MailAccount, _ string, _ []string, raw []byte) error {
		if fail == nil {
			raws = append(raws, raw)
		}
		return fail
	}
	headerID := func(raw []byte) string {
		t.Helper()
		m, err := netmail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		id := m.Header.Get("Message-Id")
		if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
			t.Fatalf("Message-Id %q", id)
		}
		return strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">")
	}
	req := SendRequest{AccountID: "acc_test", To: []Address{{Email: "friend@example.com"}}, Subject: "Hi", Text: "Hello"}

	id1, err := s.SendMessage(ctx, req)
	if err != nil || id1 == "" || len(raws) != 1 || id1 != headerID(raws[0]) || strings.ContainsAny(id1, "<> ") {
		t.Fatalf("SendMessage: %q %v, %d message(s)", id1, err, len(raws))
	}
	if id2, err := s.SendMessage(ctx, req); err != nil || id2 == id1 || id2 != headerID(raws[1]) {
		t.Fatalf("the second: %q %v (the first was %q)", id2, err, id1)
	}
	if err := s.Send(ctx, req); err != nil || len(raws) != 3 {
		t.Fatalf("Send: %v, %d message(s)", err, len(raws))
	}

	args := json.RawMessage(`{"account_id":"acc_test","to":[{"email":"friend@example.com"}],"subject":"Hi","text":"Hello"}`)
	yes := func(context.Context, string, json.RawMessage) bool { return true }
	res, id := s.CallRef(ctx, ToolSend, args, yes)
	if res.IsError || id == "" || id != headerID(raws[3]) || res.Content[0].Text != "Mail sent to friend@example.com." {
		t.Fatalf("CallRef: %+v %q", res, id)
	}
	if res, id := s.CallRef(ctx, ToolSend, args, func(context.Context, string, json.RawMessage) bool { return false }); !res.IsError || id != "" || len(raws) != 4 {
		t.Fatalf("declined: %+v %q, %d message(s)", res, id, len(raws))
	}
	fail = errors.New("smtp: refused")
	if res, id := s.CallRef(ctx, ToolSend, args, yes); !res.IsError || id != "" {
		t.Fatalf("a send that failed: %+v %q", res, id)
	}
	if res, id := s.CallRef(ctx, ToolAccounts, nil, nil); res.IsError || id != "" {
		t.Fatalf("another tool: %+v %q", res, id)
	}
}

// The hub has no folder of the agent's to save into: the node on the agent's machine answers mail_save_attachment.
func TestMCPSaveAttachmentNotOnHub(t *testing.T) {
	s := newServiceWithAccount(t, newTestIMAP(t))
	res := s.Call(context.Background(), ToolSaveAttachment, json.RawMessage(`{"account_id":"acc_test","uid":1,"attachment_id":"x"}`), nil)
	if !res.IsError || !strings.Contains(resultText(t, res), "not available here") {
		t.Errorf("mail_save_attachment on the hub = %+v", res)
	}
}

func TestMCPUnknownTool(t *testing.T) {
	s := newServiceWithAccount(t, newTestIMAP(t))
	res := s.Call(context.Background(), "mail_nope", json.RawMessage(`{}`), nil)
	if !res.IsError || !strings.Contains(resultText(t, res), "unknown tool") {
		t.Errorf("unknown tool result = %+v", res)
	}
}

func TestMCPNoAccounts(t *testing.T) {
	s, err := New(t.TempDir(), newMemRecords())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res := s.Call(context.Background(), ToolAccounts, json.RawMessage(`{}`), nil)
	if !res.IsError {
		t.Error("empty account list should be an error so the model asks the user to add one")
	}
}

// resultText flattens a tool result into one string, whatever its content
// shape is.
func resultText(t *testing.T, res interface{}) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var probe struct {
		Content any `json:"content"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	raw, _ := json.Marshal(probe.Content)
	return string(raw)
}
