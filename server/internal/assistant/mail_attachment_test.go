package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A node's mail.attachment (the bytes mail_save_attachment saves on its machine) gets the checks of mcp.call before
// the extensions are asked: never for a work machine, as the assistant only for an assistant conversation, for a
// guest only when it was given the tool.
func TestMailAttachmentRequestChecksTheCaller(t *testing.T) {
	s, d, _, ext := privacyService(t)
	ctx := context.Background()
	d.setSessions("server", []proto.Session{
		{SessionID: "main", Cwd: "/home/u/.persona/assistant"},
		{SessionID: "ordinary", Cwd: "/code/example"},
	})
	if err := s.guestSet(ctx, "server", "guest-1", []string{"lark_agent_reply"}); err != nil {
		t.Fatal(err)
	}
	ask := func(machine string, req proto.MailAttachmentRequest) (any, error) {
		t.Helper()
		req.AccountID, req.UID, req.AttachmentID = "acc1", 7, "2-abc"
		raw, _ := json.Marshal(req)
		out, handled, err := s.NodeRequest(ctx, machine, proto.MethodMailAttachment, raw)
		if !handled {
			t.Fatalf("%s: not handled", machine)
		}
		return out, err
	}

	for _, refused := range []struct {
		machine string
		req     proto.MailAttachmentRequest
	}{
		{"work", proto.MailAttachmentRequest{SessionID: "ordinary"}},
		{"work", proto.MailAttachmentRequest{SessionID: "main", Assistant: true}},
		{"server", proto.MailAttachmentRequest{SessionID: "ordinary", Assistant: true}}, // not an assistant conversation
		{"other", proto.MailAttachmentRequest{SessionID: "main", Assistant: true}},      // not the assistant's machine
		{"server", proto.MailAttachmentRequest{SessionID: "guest-1"}},                   // a guest not given the tool
		{"server", proto.MailAttachmentRequest{SessionID: "unknown", Guest: true}},      // a guest the hub does not know
	} {
		if out, err := ask(refused.machine, refused.req); err == nil {
			t.Errorf("%s %+v: handed over %+v", refused.machine, refused.req, out)
		}
	}
	if len(ext.attachments) != 0 {
		t.Fatalf("refused callers reached the extensions: %+v", ext.attachments)
	}

	for _, allowed := range []struct {
		machine string
		req     proto.MailAttachmentRequest
	}{
		{"other", proto.MailAttachmentRequest{SessionID: "ordinary"}},
		{"server", proto.MailAttachmentRequest{SessionID: "main", Assistant: true}},
	} {
		out, err := ask(allowed.machine, allowed.req)
		if a, ok := out.(proto.MailAttachment); err != nil || !ok || string(a.Data) != "%PDF" {
			t.Fatalf("%s %+v: %+v %v", allowed.machine, allowed.req, out, err)
		}
	}
	if len(ext.attachments) != 2 || ext.attachments[1].AccountID != "acc1" || !ext.attachments[1].Assistant || ext.attachments[0].Assistant {
		t.Fatalf("asked %+v", ext.attachments)
	}
}
