package proto

// MethodMailAttachment is the node's request for the bytes of a mail attachment that mail_save_attachment saves on
// its machine (node/mailattachment.go). The manager answers it only for a caller the mail connector offers the tool
// to, with the same checks as mcp.call (assistant/fleet_tools.go mailAttachment).
const MethodMailAttachment = "mail.attachment"

// MaxMailAttachment bounds the attachment a MailAttachment carries (mail.MaxSavedAttachment, which a test keeps
// equal): it crosses the node's connection in one message, base64 in JSON, under the connection's 64 MiB line.
const MaxMailAttachment = 25 << 20

// MailAttachmentRequest asks for one attachment: the arguments of mail_save_attachment that name it, and who called
// the tool. Which machine asks is the connection's, never a field.
type MailAttachmentRequest struct {
	AccountID    string `json:"account_id"`
	UID          uint32 `json:"uid"`
	Mailbox      string `json:"mailbox,omitempty"`
	AttachmentID string `json:"attachment_id"`
	SessionID    string `json:"session_id,omitempty"`
	Assistant    bool   `json:"assistant,omitempty"`
	Guest        bool   `json:"guest,omitempty"`
}

// MailAttachment is one attachment as the mail says it (Filename is the sender's, not cleaned) and its decoded bytes.
type MailAttachment struct {
	Filename string `json:"filename"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
	Data     []byte `json:"data"`
}
