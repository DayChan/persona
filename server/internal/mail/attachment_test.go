package mail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestAttachmentDownloadRangeAndIdentity(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.ignoreListReturn(t)
	service := newServiceWithAccount(t, srv)
	raw := "From: sender@example.com\r\nTo: me@example.com\r\nSubject: file\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=parts\r\n\r\n" +
		"--parts\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--parts\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nMDEyMzQ1Njc4OQ==\r\n--parts--\r\n"
	uid := uint32(srv.appendMessage(t, "INBOX", raw))
	message, err := service.GetMessage(context.Background(), "acc_test", uid, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Attachments) != 1 || message.Attachments[0].ID == "" || message.Attachments[0].Size != 10 {
		t.Fatalf("attachments: %+v", message.Attachments)
	}
	path := "/api/v1/mail/messages/acc_test/" + itoa(uid) + "/attachments/" + message.Attachments[0].ID + "?mailbox=INBOX"
	for _, test := range []struct {
		rangeHeader        string
		status             int
		body, contentRange string
	}{
		{"", 200, "0123456789", ""},
		{"bytes=2-5", 206, "2345", "bytes 2-5/10"},
		{"bytes=6-", 206, "6789", "bytes 6-9/10"},
		{"bytes=40-", 416, "", "bytes */10"},
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if test.rangeHeader != "" {
			req.Header.Set("Range", test.rangeHeader)
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, req)
		if response.Code != test.status || test.body != "" && response.Body.String() != test.body || response.Header().Get("Content-Range") != test.contentRange {
			t.Fatalf("range %q: %d %s %s", test.rangeHeader, response.Code, response.Header().Get("Content-Range"), response.Body)
		}
	}
	for _, invalidPath := range []string{
		strings.Replace(path, "acc_test", "missing", 1),
		strings.Replace(path, message.Attachments[0].ID, "1-"+strings.Repeat("0", 64), 1),
	} {
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, invalidPath, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("invalid identity: %d %s", response.Code, response.Body)
		}
	}
	again, err := service.GetMessage(context.Background(), "acc_test", uid, "INBOX")
	if err != nil || again.Seen || again.Attachments[0].ID != message.Attachments[0].ID {
		t.Fatalf("download changed flags or identity: %+v %v", again, err)
	}
}

// mail_read lists each attachment's id, and FetchAttachment hands over that attachment's bytes, within the limit only.
func TestFetchAttachmentForSaving(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.ignoreListReturn(t)
	service := newServiceWithAccount(t, srv)
	raw := "From: billing@example.com\r\nTo: me@example.com\r\nSubject: statement\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=parts\r\n\r\n" +
		"--parts\r\nContent-Type: text/plain\r\n\r\nyour statement\r\n" +
		"--parts\r\nContent-Type: text/csv\r\nContent-Disposition: attachment; filename=a.csv\r\n\r\nx,y\r\n" +
		"--parts\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=statement.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nMDEyMzQ1Njc4OQ==\r\n--parts--\r\n"
	uid := uint32(srv.appendMessage(t, "INBOX", raw))
	ctx := context.Background()

	args, _ := json.Marshal(map[string]any{"account_id": "acc_test", "uid": uid})
	res := service.Call(ctx, ToolRead, args, nil)
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("mail_read: %+v", res)
	}
	out := res.Content[0].Text
	msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
	if err != nil || len(msg.Attachments) != 2 {
		t.Fatalf("attachments: %+v %v", msg, err)
	}
	pdf := msg.Attachments[1]
	for _, a := range msg.Attachments {
		if !strings.Contains(out, " id="+a.ID+"\n") {
			t.Fatalf("mail_read does not list the id of %s:\n%s", a.Filename, out)
		}
	}
	if !strings.Contains(out, "- statement.pdf (application/pdf, 10 bytes) id="+pdf.ID+"\n") {
		t.Fatalf("mail_read:\n%s", out)
	}

	a, data, err := service.FetchAttachment(ctx, "acc_test", uid, "", pdf.ID, MaxSavedAttachment)
	if err != nil || string(data) != "0123456789" || a.Filename != "statement.pdf" || a.Mime != "application/pdf" || a.Size != 10 {
		t.Fatalf("fetch: %+v %q %v", a, data, err)
	}
	if _, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", pdf.ID, 9); !errors.Is(err, ErrAttachmentTooLarge) || data != nil {
		t.Fatalf("over the limit: %q %v", data, err)
	}
	for _, id := range []string{msg.Attachments[0].ID[:10], "2-" + strings.Repeat("0", 64), "../1"} {
		if _, _, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", id, MaxSavedAttachment); err == nil {
			t.Fatalf("id %q fetched", id)
		}
	}
	if _, _, err := service.FetchAttachment(ctx, "missing", uid, "INBOX", pdf.ID, MaxSavedAttachment); err == nil {
		t.Fatal("unknown account fetched")
	}
	if _, _, err := service.FetchAttachment(ctx, "acc_test", 0, "INBOX", pdf.ID, MaxSavedAttachment); err == nil {
		t.Fatal("uid 0 fetched")
	}
	if again, err := service.GetMessage(ctx, "acc_test", uid, "INBOX"); err != nil || again.Seen {
		t.Fatalf("fetching marked the message read: %+v %v", again, err)
	}
}
