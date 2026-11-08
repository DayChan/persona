package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/proto"
)

// mailConnector is mail as a connector (docs/plans/plan-connectors.md "3. Migration"): so far what goes into the knowledge
// base, by one cursor per account (the newest UID archived, see Changes), its inbox, its tools and mail_send through the gate;
// Settings › Connectors draws its row from the connectors list (?all=1, migration M3–M6).
type mailConnector struct{ h *Manager }

// kbMailPoll is how often the knowledge base asks mail for changes (its manifest's Poll).
const kbMailPoll = 6 * time.Minute

func (mailConnector) ID() string   { return "mail" }
func (mailConnector) Name() string { return "邮件" }

// Manifest: the coding agents get mail too; IMAP is slow and Gmail goes through a proxy, so it is asked for changes
// less often.
func (mailConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapTools, connector.CapActions, connector.CapArchive},
		AgentTools: []string{"mail_*"}, Poll: kbMailPoll}
}

// Archive paging: a page of the inbox per ListMessages, at most mailArchivePages of them per account and call, at
// most mailArchiveReads full texts per call (the rest their snippet).
const (
	mailArchivePage  = 40
	mailArchivePages = 5
	mailArchiveReads = 10
)

// Changes is inbox mail newer than the last UID archived per account, oldest first, one chat per account and
// correspondent (mail-<account>-<address>); subject + text, 4 000 characters. An account that cannot be read is
// skipped this time.
//
// The cursor is {"<account>": uid}, as mail-cursors.json always was, plus per account (absent when zero):
// "<account>#v" the mailbox's UIDVALIDITY, "<account>#t" the newest archived mail's time (unix seconds), and a hole
// "<account>#f" < UID < "<account>#h" still to archive. The hole is what lies between the old cursor and the newest
// page when more than a page came since (the hub was down, the account failed for a while): it is read page by page
// downwards, more is true until it is filled. A changed UIDVALIDITY means the server numbered the mailbox anew:
// the UIDs kept mean nothing, so it starts again from the newest page, with only mail newer than "<account>#t".
func (c mailConnector) Changes(ctx context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	cursors := map[string]uint32{}
	if cursor != "" {
		_ = json.Unmarshal([]byte(cursor), &cursors)
	}
	accounts, err := c.h.mail.ListAccounts()
	if err != nil {
		return nil, cursor, false, nil
	}
	var batches []connector.ChatBatch
	reads, more := 0, false
	for _, a := range accounts {
		st := loadMailCursor(cursors, a.ID)
		msgs, ok := c.mailChanges(ctx, a.ID, &st)
		if !ok {
			continue
		}
		for _, m := range msgs {
			text := m.Snippet
			if reads < mailArchiveReads {
				if full, err := c.h.mail.GetMessage(ctx, a.ID, m.UID, m.Mailbox); err == nil && strings.TrimSpace(full.Text) != "" {
					text = full.Text
				}
				reads++
			}
			at, _ := time.Parse(time.RFC3339, m.Date)
			addr := strings.ToLower(m.From.Email)
			name := m.From.Name
			if name == "" {
				name = addr
			}
			batches = append(batches, connector.ChatBatch{
				Chat: kb.SourceChat{ID: a.ID + "-" + addr, Name: name + " · 邮件", Kind: "mail"},
				Messages: []kb.SourceMessage{{ID: fmt.Sprint(m.UID), SenderID: addr, SenderName: name, At: at, Type: "TEXT",
					SenderHandles: []kb.Handle{{Kind: kb.HandleEmail, Value: addr}},
					Mine:          strings.EqualFold(addr, a.Email),
					Text:          "【" + m.Subject + "】" + clipRunes(strings.TrimSpace(text), 4000)}},
			})
		}
		st.store(cursors, a.ID)
		more = more || st.holeTop != 0 || st.deferred
	}
	raw, _ := json.Marshal(cursors)
	return batches, string(raw), more, nil
}

// mailCursor is one account's part of the mail archive cursor (see Changes).
type mailCursor struct {
	top       uint32 // newest UID archived
	validity  uint32 // UIDVALIDITY the UIDs belong to, 0 unknown (a cursor from before it was kept)
	newest    uint32 // newest archived mail's time, unix seconds
	holeFloor uint32 // with holeTop != 0: UIDs holeFloor < uid < holeTop are not archived yet
	holeTop   uint32
	deferred  bool // this call left the newest page for the next, the hole still open (not stored)
}

func loadMailCursor(cursors map[string]uint32, id string) mailCursor {
	return mailCursor{top: cursors[id], validity: cursors[id+"#v"], newest: cursors[id+"#t"],
		holeFloor: cursors[id+"#f"], holeTop: cursors[id+"#h"]}
}

func (st mailCursor) store(cursors map[string]uint32, id string) {
	cursors[id] = st.top
	for key, v := range map[string]uint32{"#v": st.validity, "#t": st.newest, "#f": st.holeFloor, "#h": st.holeTop} {
		if v == 0 {
			delete(cursors, id+key)
		} else {
			cursors[id+key] = v
		}
	}
}

// mailChanges is the account's inbox mail to archive now, oldest first, moving st past it; ok is false when the
// inbox could not be read (st is left as it was).
func (c mailConnector) mailChanges(ctx context.Context, account string, st *mailCursor) ([]mail.MailSummary, bool) {
	res, err := c.h.mail.ListMessages(ctx, mail.ListOptions{AccountID: account, Limit: mailArchivePage})
	if err != nil {
		return nil, false
	}
	var after time.Time // only mail newer than this, after a renumbering
	if st.validity != 0 && res.UIDValidity != 0 && st.validity != res.UIDValidity {
		if st.newest != 0 {
			after = time.Unix(int64(st.newest), 0)
		}
		*st = mailCursor{newest: st.newest}
	}
	if res.UIDValidity != 0 {
		st.validity = res.UIDValidity
	}
	var out []mail.MailSummary
	take := func(m mail.MailSummary) {
		at, _ := time.Parse(time.RFC3339, m.Date)
		if !after.IsZero() && !at.After(after) {
			return
		}
		// Clamped to now: a mail dated in the future would otherwise hide everything until then after a renumbering.
		if t := min(at.Unix(), time.Now().Unix()); t > 0 && uint32(t) > st.newest {
			st.newest = uint32(t)
		}
		out = append(out, m)
	}
	// Below the newest page, the hole page by page, at most mailArchivePages pages in all.
	pages := 1
	fill := func() {
		for ; st.holeTop != 0 && pages < mailArchivePages; pages++ {
			page, err := c.h.mail.ListMessages(ctx, mail.ListOptions{AccountID: account, Limit: mailArchivePage, BeforeUID: st.holeTop})
			if err != nil || (page.UIDValidity != 0 && page.UIDValidity != st.validity) {
				pages = mailArchivePages // read again next time; a renumbering in between is seen on the newest page then
				return
			}
			for _, m := range page.Messages {
				if m.UID > st.holeFloor && m.UID < st.holeTop {
					take(m)
				}
			}
			if pageReaches(page, st.holeFloor) || page.NextBeforeUID >= st.holeTop {
				st.holeFloor, st.holeTop = 0, 0
			} else {
				st.holeTop = page.NextBeforeUID
			}
		}
	}
	// An open hole first: the newest page may only be taken once it is filled, else it would open a second one.
	fill()
	if reached := pageReaches(res, st.top); st.holeTop == 0 || reached {
		top := st.top
		for _, m := range res.Messages {
			if m.UID > st.top {
				take(m)
				top = max(top, m.UID)
			}
		}
		if st.top > 0 && !reached {
			st.holeFloor, st.holeTop = st.top, res.NextBeforeUID
		}
		st.top = top
	} else {
		st.deferred = true
	}
	fill() // the hole the newest page left, with the pages still left
	slices.SortFunc(out, func(a, b mail.MailSummary) int { return cmp.Compare(a.UID, b.UID) })
	return out, true
}

// pageReaches says a page (newest first, down from its before_uid) holds every UID above floor up to it.
func pageReaches(page *mail.ListResult, floor uint32) bool {
	return !page.HasMore || page.NextBeforeUID == 0 || page.NextBeforeUID <= floor+1
}

// Tools are mail's (prompts/mcp/mail) without mail_send, which is an action: the user allows each mail on the phone
// (the gate), the card as it always was.
func (c mailConnector) Tools() []proto.Tool {
	var out []proto.Tool
	for _, t := range c.h.mail.Tools() {
		if t.Name != mail.ToolSend {
			out = append(out, t)
		}
	}
	return out
}

func (c mailConnector) Actions() []proto.Tool {
	for _, t := range c.h.mail.Tools() {
		if t.Name == mail.ToolSend {
			return []proto.Tool{t}
		}
	}
	return nil
}

func (c mailConnector) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return toolText(c.h.mail.Call(ctx, name, args, nil))
}

// Describe refuses a mail_send that could not be sent before the user is asked.
func (mailConnector) Describe(_ context.Context, name string, args json.RawMessage) (connector.Action, string, error) {
	summary, err := mail.SendSummary(args)
	return connector.Action{Name: name}, summary, err
}

// Do sends it (the gate let it through).
func (c mailConnector) Do(ctx context.Context, name string, args json.RawMessage) (string, error) {
	done, err := c.DoRef(ctx, name, args)
	return done.Text, err
}

var _ connector.RefActions = mailConnector{}

// DoRef is Do with the Message-ID written into the header of the mail handed to the transport, without the angle
// brackets (connector.RefActions); whether a provider keeps it as written has not been verified.
func (c mailConnector) DoRef(ctx context.Context, name string, args json.RawMessage) (connector.DoResult, error) {
	res, messageID := c.h.mail.CallRef(ctx, name, args, nil)
	text, err := toolText(res)
	return connector.DoResult{Text: text, MessageRef: messageID}, err
}

// errMailAttachmentUnavailable: mail_save_attachment is not offered to the caller (mail is off, not set up, or not
// given to it), so neither are the bytes.
var errMailAttachmentUnavailable = errors.New("mail is not available")

// MailAttachment answers a node's mail.attachment (proto.MethodMailAttachment) for mail_save_attachment: one
// attachment's bytes, at most proto.MaxMailAttachment, only while the mail connector offers the tool to that caller
// (assistant: an assistant conversation, which the assistant has checked; an agent gets it only when mail's tools go to
// the agents too). The machine and conversation checks of mcp.call are the assistant's (fleet_tools.go mailAttachment).
func (h *Manager) MailAttachment(ctx context.Context, req proto.MailAttachmentRequest) (proto.MailAttachment, error) {
	if !h.ready(ctx) || h.mail == nil || h.connectors == nil || !h.offersTool(ctx, mail.ToolSaveAttachment, req.Assistant) {
		return proto.MailAttachment{}, errMailAttachmentUnavailable
	}
	a, data, err := h.mail.FetchAttachment(ctx, req.AccountID, req.UID, req.Mailbox, req.AttachmentID, proto.MaxMailAttachment)
	if err != nil {
		return proto.MailAttachment{}, err
	}
	return proto.MailAttachment{Filename: a.Filename, Mime: a.Mime, Size: a.Size, Data: data}, nil
}

// offersTool says whether a connector that is ok lists the tool name for this caller: the agents' tools to everyone,
// the assistant-only ones to an assistant conversation (what AgentTools hands out).
func (h *Manager) offersTool(ctx context.Context, name string, assistant bool) bool {
	is := func(t proto.Tool) bool { return t.Name == name }
	return slices.ContainsFunc(h.connectors.ToolsFor(ctx, true), is) ||
		assistant && slices.ContainsFunc(h.connectors.ToolsFor(ctx, false), is)
}

// toolText is a tool result as text, its error as an error (connector.ReadTools / Actions return them that way).
func toolText(r proto.ToolResult) (string, error) {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	if r.IsError {
		return "", errors.New(b.String())
	}
	return b.String(), nil
}
