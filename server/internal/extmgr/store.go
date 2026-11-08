package extmgr

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/proto"
)

// Store is the hub's Postgres as this code keeps its state in it (internal/db; the same keys and tables as when
// they were the hub's: plugins, plugin_secrets, plugin_audit, the markets, connector_secrets, mail_accounts, the
// settings of the connectors, bots and channels). The registry's own tables are extension.Store's.
type Store interface {
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
}

// secretStore keeps the connectors' credentials (connector_secrets).
type secretStore interface {
	ConnectorSecret(ctx context.Context, connector string) (string, bool, error)
	PutConnectorSecret(ctx context.Context, connector, sealed string) error
	DeleteConnectorSecret(ctx context.Context, connector string) error
}

// sendStore is the once-only guard of sends (sent_messages).
type sendStore interface {
	ClaimSend(ctx context.Context, id, machine, session string) (string, error)
	SettleSend(ctx context.Context, id, state string) error
}

// mailStore keeps the mail accounts (mail_accounts; mail seals their secrets itself).
type mailStore interface {
	Store
	MailAccounts(ctx context.Context) ([]db.MailAccountRow, error)
	ReplaceMailAccounts(ctx context.Context, rows []db.MailAccountRow) error
}

// Mail is implemented by internal/mail; an interface so the manager can run without it.
type Mail interface {
	Handler() http.Handler
	Tools() []proto.Tool
	Call(ctx context.Context, name string, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) proto.ToolResult
	// CallRef is Call that also says the Message-ID of the mail mail_send handed to the transport ("" for anything else).
	CallRef(ctx context.Context, name string, args json.RawMessage, approve func(ctx context.Context, summary string, input json.RawMessage) bool) (proto.ToolResult, string)
	ListAccounts() ([]mail.MailAccount, error)
	ListMessages(ctx context.Context, opts mail.ListOptions) (*mail.ListResult, error)
	GetMessage(ctx context.Context, accountID string, uid uint32, mailbox string) (*mail.MailMessage, error)
	Send(ctx context.Context, req mail.SendRequest) error
	// FetchAttachment is one attachment's bytes for mail_save_attachment (the node's mail.attachment), at most max.
	FetchAttachment(ctx context.Context, accountID string, uid uint32, mailbox, id string, max int64) (mail.Attachment, []byte, error)
	AddAccount(req mail.AddAccountRequest) (*mail.MailAccount, error)
	DeleteAccount(id string) error
	// ExportAccount and ImportAccount move an account to another manager (sources.go); the JSON holds its secrets.
	ExportAccount(id string) ([]byte, error)
	ImportAccount(data []byte) (*mail.MailAccount, error)
}

// NewMail is set by the mail integration (mail_glue.go); secretKey is the key file the passwords are sealed with.
var NewMail func(dataDir, secretKey string, store mailStore, google GoogleConfig, microsoft MicrosoftConfig) (Mail, error)
