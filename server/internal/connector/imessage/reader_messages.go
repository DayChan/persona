package imessage

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// msgRow is one message as the queries select it.
type msgRow struct {
	Conv        int     `json:"conv"`
	ChatRow     int64   `json:"chat"` // the chat's ROWID (Changes, Search)
	RowID       int64   `json:"rowid"`
	GUID        string  `json:"guid"`
	Date        int64   `json:"date"`
	Text        *string `json:"text"`
	Body        *string `json:"body"` // hex(attributedBody)
	FromMe      int     `json:"me"`
	Handle      int64   `json:"handle"`
	Service     *string `json:"service"`
	Read        int     `json:"read"`
	Item        int     `json:"item"`
	Action      int     `json:"action"`
	Other       int64   `json:"other"`
	Title       *string `json:"title"`
	Attachments int     `json:"att"`
	Audio       int     `json:"audio"`
	Thread      *string `json:"thread"`
	Edited      int64   `json:"edited"`
	Retracted   int64   `json:"retracted"`
	Balloon     *string `json:"balloon"`
}

const msgColumns = `m.ROWID AS rowid, m.guid AS guid, m.date AS date, m.text AS text, hex(m.attributedBody) AS body,
  m.is_from_me AS me, m.handle_id AS handle, m.service AS service, m.is_read AS read, m.item_type AS item,
  m.group_action_type AS action, m.other_handle AS other, m.group_title AS title, m.cache_has_attachments AS att,
  m.is_audio_message AS audio, m.thread_originator_guid AS thread, m.date_edited AS edited,
  m.date_retracted AS retracted, m.balloon_bundle_id AS balloon`

// text is a message's words: its text column, or what its attributedBody holds. ok is false when neither can be
// read (the app shows that it could not).
func (m msgRow) text() (string, bool) {
	t := str(m.Text)
	if strings.TrimSpace(strings.ReplaceAll(t, "￼", "")) == "" && m.Body != nil && *m.Body != "" {
		blob, err := hex.DecodeString(*m.Body)
		if err != nil {
			return "", false
		}
		if t, err = bodyText(blob); err != nil {
			return "", false
		}
	}
	return strings.TrimSpace(strings.ReplaceAll(t, "￼", "")), true
}

// Messages is a page of a conversation's messages, the newest ones or those before the cursor a previous page
// returned, oldest first. Tapbacks are left out. keep KeepContacts refuses a conversation that does not go to Persona.
func (r *Reader) Messages(ctx context.Context, id string, limit int, before, keep string) (MessagesPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	convs, err := r.conversations(ctx)
	if err != nil {
		return MessagesPage{}, err
	}
	var cv *conversation
	for _, c := range convs {
		if c.id == id {
			cv = c
			break
		}
	}
	if cv == nil {
		return MessagesPage{}, ErrNoChat
	}
	if keep == KeepContacts {
		people, err := r.members(ctx, []*conversation{cv})
		if err != nil {
			return MessagesPage{}, err
		}
		wrote, err := r.wrote(ctx, []*conversation{cv})
		if err != nil {
			return MessagesPage{}, err
		}
		if !wrote[cv.id] && !anyNamed(people[cv.id]) {
			return MessagesPage{}, ErrNoChat
		}
	}
	where := ""
	if before != "" {
		at, row, ok := strings.Cut(before, ".")
		d, err1 := strconv.ParseInt(at, 10, 64)
		n, err2 := strconv.ParseInt(row, 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return MessagesPage{}, fmt.Errorf("bad cursor")
		}
		where = fmt.Sprintf(" AND (j.message_date < %d OR j.message_date = %d AND j.message_id < %d)", d, d, n)
	}
	var rows []msgRow
	if err := r.query(ctx, r.chatDB(), fmt.Sprintf(`SELECT 0 AS conv, %s FROM chat_message_join j JOIN message m ON m.ROWID = j.message_id
WHERE j.chat_id IN (%s) AND m.associated_message_type = 0%s ORDER BY j.message_date DESC, j.message_id DESC LIMIT %d`,
		msgColumns, ints(cv.chats), where, limit+1), &rows); err != nil {
		return MessagesPage{}, err
	}
	page := MessagesPage{Messages: []Message{}}
	if len(rows) > limit {
		rows, page.More = rows[:limit], true
	}
	if page.More {
		oldest := rows[len(rows)-1]
		page.Before = fmt.Sprintf("%d.%d", oldest.Date, oldest.RowID)
	}
	var ids []int64
	var withFiles []int64
	var threads []string
	for _, m := range rows {
		ids = append(ids, m.Handle, m.Other)
		if m.Attachments != 0 {
			withFiles = append(withFiles, m.RowID)
		}
		if t := str(m.Thread); t != "" {
			threads = append(threads, t)
		}
	}
	handles, err := r.handles(ctx, ids)
	if err != nil {
		return MessagesPage{}, err
	}
	files, err := r.attachments(ctx, withFiles)
	if err != nil {
		return MessagesPage{}, err
	}
	replied, err := r.replied(ctx, threads)
	if err != nil {
		return MessagesPage{}, err
	}
	names := r.names(ctx)
	people, err := r.members(ctx, []*conversation{cv})
	if err != nil {
		return MessagesPage{}, err
	}
	page.Chat = r.chat(cv, people[cv.id], names)
	for i := len(rows) - 1; i >= 0; i-- {
		m := message(rows[i], names, handles, files[rows[i].RowID])
		if t := str(rows[i].Thread); t != "" {
			m.ReplyTo = replied[t]
		}
		page.Messages = append(page.Messages, m)
	}
	return page, nil
}

// message turns a row into what the app shows.
func message(m msgRow, names *contacts, handles map[int64]string, files []Attachment) Message {
	out := Message{ID: strconv.FormatInt(m.RowID, 10), At: appleTime(m.Date), Kind: "message", FromMe: m.FromMe == 1,
		Service: str(m.Service), Edited: m.Edited != 0, Unsent: m.Retracted != 0, Unread: m.FromMe == 0 && m.Read == 0,
		Attachments: files}
	if !out.FromMe {
		if h, ok := handles[m.Handle]; ok {
			out.Sender = &Person{Handle: h, Name: names.name(h)}
		}
	}
	who := "你"
	if out.Sender != nil {
		who = out.Sender.shown()
	} else if !out.FromMe {
		who = "有人"
	}
	switch m.Item {
	case 0:
	case 1:
		out.Kind = "event"
		other := "有人"
		if h, ok := handles[m.Other]; ok {
			other = Person{Handle: h, Name: names.name(h)}.shown()
		}
		if m.Action == 1 {
			out.Text = who + "把" + other + "移出了群聊"
		} else {
			out.Text = who + "把" + other + "加进了群聊"
		}
		return out
	case 2:
		out.Kind = "event"
		if t := strings.TrimSpace(str(m.Title)); t != "" {
			out.Text = who + "把群名改成「" + t + "」"
		} else {
			out.Text = who + "去掉了群名"
		}
		return out
	case 3:
		out.Kind = "event"
		switch m.Action {
		case 0:
			out.Text = who + "离开了群聊"
		case 1:
			out.Text = who + "换了群头像"
		case 2:
			out.Text = who + "去掉了群头像"
		default:
			out.Text = "群聊有变动"
		}
		return out
	default: // FaceTime, location sharing and the like
		out.Kind = "event"
		out.Text = "[系统消息]"
		return out
	}
	if out.Unsent {
		return out
	}
	text, ok := m.text()
	out.Text = text
	out.Undecoded = !ok
	if m.Audio != 0 {
		for i := range out.Attachments {
			out.Attachments[i].Kind = "audio"
		}
	}
	if out.Text == "" && len(out.Attachments) == 0 && !out.Undecoded && str(m.Balloon) != "" {
		out.Text = "[来自 App 的消息]"
	}
	return out
}

// attachments describe the files of messages, by message ROWID.
func (r *Reader) attachments(ctx context.Context, messages []int64) (map[int64][]Attachment, error) {
	out := map[int64][]Attachment{}
	if len(messages) == 0 {
		return out, nil
	}
	var rows []struct {
		Message int64   `json:"message"`
		Name    *string `json:"name"`
		Mime    *string `json:"mime"`
		Size    int64   `json:"size"`
	}
	if err := r.query(ctx, r.chatDB(), `SELECT j.message_id AS message, a.transfer_name AS name, a.mime_type AS mime, a.total_bytes AS size
FROM message_attachment_join j JOIN attachment a ON a.ROWID = j.attachment_id
WHERE j.message_id IN (`+ints(messages)+`) AND a.hide_attachment = 0 ORDER BY j.message_id, a.ROWID`, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		mime := str(row.Mime)
		kind := "file"
		switch {
		case strings.HasPrefix(mime, "image/"):
			kind = "image"
		case strings.HasPrefix(mime, "video/"):
			kind = "video"
		case strings.HasPrefix(mime, "audio/"):
			kind = "audio"
		}
		out[row.Message] = append(out[row.Message], Attachment{Kind: kind, Name: str(row.Name), Mime: mime, Size: row.Size})
	}
	return out, nil
}

// guidLike is what a message guid looks like; anything else is not spliced into SQL.
func guidLike(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == ':' || c == '/' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// replied are the starts of the messages that others answer in a thread, by guid.
func (r *Reader) replied(ctx context.Context, guids []string) (map[string]string, error) {
	out := map[string]string{}
	var quoted []string
	for _, g := range guids {
		if guidLike(g) {
			quoted = append(quoted, "'"+g+"'")
		}
	}
	if len(quoted) == 0 {
		return out, nil
	}
	var rows []msgRow
	if err := r.query(ctx, r.chatDB(), "SELECT 0 AS conv, "+msgColumns+" FROM message m WHERE m.guid IN ("+strings.Join(quoted, ",")+")", &rows); err != nil {
		return nil, err
	}
	for _, m := range rows {
		text, ok := m.text()
		if !ok || text == "" {
			continue
		}
		if r := []rune(text); len(r) > 40 {
			text = string(r[:40]) + "…"
		}
		out[m.GUID] = text
	}
	return out, nil
}
