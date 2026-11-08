package assistant

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The manager carries the extensions, the connectors and the bots too (internal/extmgr, docs/extensions/extensions.md "Who manages what"):
// this file is how the bus connection hands that layer the hub's extensions.* calls and events and the relayed
// requests, and how what the assistant asked the hub for of them (the inbox's sources, the channels' transport, the
// connectors for the knowledge base and its directory) goes to it directly instead, as do the assistant's events that
// were the hub's to pass on to the extensions (inbox.changed, kb.changed).

// Extensions is the manager's extensions, connectors and bots (internal/extmgr.Manager).
type Extensions interface {
	Hello(res json.RawMessage)
	BusUp() // returns once its report went: the outbox goes after it (outbox.go)
	Request(ctx context.Context, method string, params json.RawMessage) (any, bool, error)
	Event(name string, params json.RawMessage) bool
	// AppCall answers the assistant's app.calls that are its (handled false: the hub's).
	AppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error)
	// AssistantSaid takes the assistant's events that are its (handled false: they go to the hub).
	AssistantSaid(event string, params json.RawMessage) bool
	http.Handler
}

// localAppCall is an app.call the manager's extensions answer.
func (b *BusHub) localAppCall(ctx context.Context, p AppCall) (json.RawMessage, bool, error) {
	if e := b.opts.Extensions; e != nil {
		return e.AppCall(ctx, p)
	}
	return nil, false, nil
}

// notifyHub sends the hub an event, unless it is the extensions' to take here.
func (b *BusHub) notifyHub(event string, params any) error {
	if e := b.opts.Extensions; e != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		if e.AssistantSaid(event, raw) {
			return nil
		}
		return b.client.Notify(event, json.RawMessage(raw))
	}
	return b.client.Notify(event, params)
}

// Answer is what the extensions ask the assistant in this process: the hub's calls they used to make over the bus
// (channels.receive, feishu.message, inbox.collected, kb.*…), answered as the bus would.
func (b *BusHub) Answer(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	out, err := b.answer(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if raw, ok := out.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(out)
}

// TellLocal is an event the extensions tell the assistant in this process (feishu.owner, kb.kick…).
func (b *BusHub) TellLocal(event string, params json.RawMessage) { b.queueEvent(event, params) }

// ExtensionTools are the nodes' MCP tools and gated commands that are the extensions' (internal/extmgr, in this
// process): the connectors', Cua's, ego's, a gated command's card text, lark-cli's settings for the assistant's machine.
type ExtensionTools interface {
	AgentTools(ctx context.Context, assistant bool) (agents, assistantOnly []proto.Tool)
	CallTool(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool)
	// ConnectorTool says whether name is a connector's tool (an extension's), not Cua's or ego's: what a guest
	// conversation may be given (guest.go).
	ConnectorTool(name string) bool
	AssistantSummary(ctx context.Context, tool, summary string) (string, bool)
	AssistantLark(ctx context.Context) (proto.AssistantLark, error)
	// AnswerComputerElicit answers the node's computer.elicit (LCU runtime confirmations on the phone/web cards).
	AnswerComputerElicit(ctx context.Context, machine string, params json.RawMessage) (map[string]any, error)
	// RecordNodeSent keeps a write the node's gate proxy forwarded in the sent-as-user ledger (the node's lark.sent);
	// machine is the connection's, never a field of params.
	RecordNodeSent(ctx context.Context, machine string, params json.RawMessage) (any, error)
	// MailAttachment is one mail attachment's bytes for the node that saves it (mail_save_attachment, the node's
	// mail.attachment), only while the mail connector offers the tool to that caller; the caller's machine and
	// conversation are checked first (fleet_tools.go mailAttachment).
	MailAttachment(ctx context.Context, req proto.MailAttachmentRequest) (proto.MailAttachment, error)
}

// SetExtensions gives the service the extensions' tools (before Run).
func (s *Service) SetExtensions(e ExtensionTools) { s.ext = e }

// AskCard is askHub for the extensions in this process: a card of theirs (a mail send, Cua's apps, persona_settings),
// waited for like a hook's; ctx ending withdraws it.
func (s *Service) AskCard(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	return s.askHub(ctx, ix)
}

// mainMachine is where the assistant's conversations run (the extensions give only it the skills' gated commands).
func (s *Service) mainMachine(ctx context.Context) string { return s.settings(ctx).MachineID }

// userTZ is the user's time zone by name, "" while it is not known.
func (s *Service) userTZ() string { return s.settings(context.Background()).Timezone }

// toolsNow are the assistant's tools (a connector's may not shadow one).
func (s *Service) toolsNow() []proto.Tool { return s.Tools(context.Background()) }
