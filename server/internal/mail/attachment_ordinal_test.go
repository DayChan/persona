package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

type ordinalAttachment struct {
	index    int
	filename string
	data     string
}

// Each bit makes one physical attachment undecodable. The last two have identical
// names and bytes, so a reader must use the ordinal rather than a filename fallback.
func attachmentOrdinalMessage(bad int, nested bool) (string, []ordinalAttachment) {
	raw := "From: Sender <sender@example.com>\r\nTo: recipient@example.com\r\n" +
		"Cc: copy@example.com\r\nSubject: MIME order\r\nMessage-ID: <order@example.com>\r\n" +
		"In-Reply-To: <parent@example.com>\r\nReferences: <root@example.com> <parent@example.com>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nvalid body\r\n" +
		"--outer\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>valid body</p>\r\n" +
		"--outer\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=inline.png\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nYWJj\r\n" +
		"--outer\r\nContent-Type: text/plain; name=notes.txt\r\n\r\nnon-attachment text\r\n"
	boundary := "outer"
	if nested {
		raw += "--outer\r\nContent-Type: multipart/mixed; boundary=inner\r\n\r\n"
		boundary = "inner"
	}
	var want []ordinalAttachment
	for i, part := range []struct{ filename, encoded, data string }{
		{"", "eHl6", "xyz"},
		{"same.pdf", "YWJj", "abc"},
		{"same.pdf", "YWJj", "abc"},
	} {
		raw += "--" + boundary + "\r\nContent-Type: application/pdf\r\n"
		if i > 0 {
			raw += "Content-Disposition: attachment; filename=" + part.filename + "\r\n"
		} // An unnamed non-text part is also an AttachmentHeader.
		encoded := part.encoded
		if bad&(1<<i) != 0 {
			// Includes a decoded prefix before failure, which must not be listed.
			encoded = "YWJj!!!!"
		} else {
			want = append(want, ordinalAttachment{i + 1, part.filename, part.data})
		}
		raw += "Content-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"
	}
	if nested {
		raw += "--inner--\r\n"
	}
	// Explicit text attachments count, unlike inline text or inline images.
	raw += "--outer\r\nContent-Type: text/csv\r\nContent-Disposition: attachment; filename=table.csv\r\n\r\na,b\r\n--outer--\r\n"
	want = append(want, ordinalAttachment{4, "table.csv", "a,b"})
	return raw, want
}

func checkOrdinalAttachments(t *testing.T, msg *MailMessage, want []ordinalAttachment) {
	t.Helper()
	if msg.Text != "valid body" || msg.HTML != "<p>valid body</p>" || msg.InReplyTo != "parent@example.com" ||
		!reflect.DeepEqual(msg.References, []string{"root@example.com", "parent@example.com"}) ||
		!reflect.DeepEqual(msg.To, []Address{{Email: "recipient@example.com"}}) ||
		!reflect.DeepEqual(msg.Cc, []Address{{Email: "copy@example.com"}}) {
		t.Fatalf("body or headers changed: %+v", msg)
	}
	if len(msg.Attachments) != len(want) {
		t.Fatalf("attachments = %+v, want %d decodable parts", msg.Attachments, len(want))
	}
	for i, expected := range want {
		a := msg.Attachments[i]
		mime := "application/pdf"
		if expected.index == 4 {
			mime = "text/csv"
		}
		if !strings.HasPrefix(a.ID, strconv.Itoa(expected.index)+"-") || a.Filename != expected.filename ||
			a.Mime != mime || a.Size != int64(len(expected.data)) {
			t.Errorf("attachment = %+v, want physical part %+v (%s)", a, expected, mime)
		}
	}
}

func TestParseMessageAttachmentMIMEOrdinals(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for bad := 0; bad < 8; bad++ {
			t.Run(fmt.Sprintf("nested=%t/bad=%03b", nested, bad), func(t *testing.T) {
				raw, want := attachmentOrdinalMessage(bad, nested)
				msg := &MailMessage{MailSummary: MailSummary{AccountID: "account", Mailbox: "Archive", UID: 7, Seen: true, Flagged: true}}
				parseMessage([]byte(raw), msg)
				checkOrdinalAttachments(t, msg, want)
				if msg.AccountID != "account" || msg.Mailbox != "Archive" || msg.UID != 7 || !msg.Seen || !msg.Flagged {
					t.Fatalf("summary changed: %+v", msg.MailSummary)
				}
			})
		}
	}
}

// Exercise the identities returned by mail_read through both production consumers:
// HTTP download and FetchAttachment, the handoff used by mail_save_attachment.
func TestAttachmentMIMEOrdinalsDownloadAndSave(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	service := newServiceWithAccount(t, srv)
	ctx := context.Background()
	for _, nested := range []bool{false, true} {
		for bad := 0; bad < 8; bad++ {
			t.Run(fmt.Sprintf("nested=%t/bad=%03b", nested, bad), func(t *testing.T) {
				raw, want := attachmentOrdinalMessage(bad, nested)
				uid := uint32(srv.appendMessage(t, "INBOX", raw, imap.FlagFlagged))
				msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
				if err != nil {
					t.Fatal(err)
				}
				checkOrdinalAttachments(t, msg, want)
				if msg.Subject != "MIME order" || msg.MessageID != "order@example.com" || msg.From.Email != "sender@example.com" ||
					msg.Snippet != "valid body" || !msg.HasAttach || msg.Seen || !msg.Flagged {
					t.Fatalf("summary changed: %+v", msg.MailSummary)
				}
				args, _ := json.Marshal(map[string]any{"account_id": "acc_test", "uid": uid})
				read := service.Call(ctx, ToolRead, args, nil)
				if read.IsError || len(read.Content) != 1 {
					t.Fatalf("mail_read: %+v", read)
				}
				for i, a := range msg.Attachments {
					if !strings.Contains(read.Content[0].Text, " id="+a.ID+"\n") {
						t.Fatalf("mail_read omitted %s", a.ID)
					}
					path := "/api/v1/mail/messages/acc_test/" + itoa(uid) + "/attachments/"
					response := httptest.NewRecorder()
					service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+a.ID, nil))
					if response.Code != http.StatusOK || response.Body.String() != want[i].data ||
						response.Header().Get("Content-Type") != a.Mime || response.Header().Get("ETag") != `"`+a.ID+`"` {
						t.Fatalf("download %s: %d %q %v", a.ID, response.Code, response.Body.String(), response.Header())
					}
					if _, data, err := service.FetchAttachment(ctx, "acc_test", uid, "", a.ID, a.Size-1); !errors.Is(err, ErrAttachmentTooLarge) || data != nil {
						t.Fatalf("oversized save returned bytes: %q %v", data, err)
					}
					// A refused save must leave a subsequent fetch with the same identity usable.
					metadata, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", a.ID, MaxSavedAttachment)
					if err != nil || string(data) != want[i].data || metadata != a {
						t.Fatalf("save handoff %s: %+v %q %v", a.ID, metadata, data, err)
					}
					_, digest, _ := strings.Cut(a.ID, "-")
					for _, wrong := range []string{"1-" + strings.Repeat("0", 64), strconv.Itoa(want[i].index%4+1) + "-" + digest} {
						response := httptest.NewRecorder()
						service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+wrong, nil))
						if response.Code == http.StatusOK || response.Code == http.StatusPartialContent {
							t.Fatalf("download accepted wrong identity %s", wrong)
						}
						if _, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", wrong, MaxSavedAttachment); err == nil || data != nil {
							t.Fatalf("save accepted wrong identity %s: %q %v", wrong, data, err)
						}
					}
				}
				if again, err := service.GetMessage(ctx, "acc_test", uid, "INBOX"); err != nil || !reflect.DeepEqual(again, msg) {
					t.Fatalf("downloads or failed saves changed the message: %+v %v", again, err)
				}
			})
		}
	}
}
