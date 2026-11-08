package extmgr

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/proto"
)

// attachmentMail hands over one attachment for mail.attachment and keeps what it was asked.
type attachmentMail struct {
	replyMail
	asked []string
}

func (m *attachmentMail) FetchAttachment(_ context.Context, account string, uid uint32, mailbox, id string, max int64) (mail.Attachment, []byte, error) {
	m.asked = append(m.asked, fmt.Sprintf("%s/%d/%s/%s/%d", account, uid, mailbox, id, max))
	return mail.Attachment{ID: id, Filename: "statement.pdf", Mime: "application/pdf", Size: 4}, []byte("%PDF"), nil
}

// The node saving mail_save_attachment gets the bytes, within the limit, only while the mail connector offers the
// tool to that caller: not with mail off, nor once the user switched the connector off.
func TestMailAttachmentForTheNode(t *testing.T) {
	if proto.MaxMailAttachment != mail.MaxSavedAttachment {
		t.Fatalf("proto allows %d bytes, mail %d", proto.MaxMailAttachment, mail.MaxSavedAttachment)
	}
	h := newTestManager(t)
	m := &attachmentMail{}
	h.mail = m
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	req := proto.MailAttachmentRequest{AccountID: "acc1", UID: 7, Mailbox: "INBOX", AttachmentID: "2-abc"}

	got, err := h.MailAttachment(context.Background(), req)
	if err != nil || got.Filename != "statement.pdf" || got.Mime != "application/pdf" || got.Size != 4 || string(got.Data) != "%PDF" {
		t.Fatalf("got %+v %v", got, err)
	}
	if want := fmt.Sprintf("acc1/7/INBOX/2-abc/%d", mail.MaxSavedAttachment); len(m.asked) != 1 || m.asked[0] != want {
		t.Fatalf("asked %v, want %s", m.asked, want)
	}

	if err := h.connectors.SetEnabled("mail", false); err != nil {
		t.Fatal(err)
	}
	for _, assistant := range []bool{false, true} {
		req.Assistant = assistant
		if _, err := h.MailAttachment(context.Background(), req); !errors.Is(err, errMailAttachmentUnavailable) {
			t.Fatalf("mail switched off, assistant %v: %v", assistant, err)
		}
	}
	if len(m.asked) != 1 {
		t.Fatalf("fetched with the connector off: %v", m.asked)
	}

	h.mail = nil
	if _, err := h.MailAttachment(context.Background(), req); !errors.Is(err, errMailAttachmentUnavailable) {
		t.Fatalf("mail off: %v", err)
	}
}
