package imessage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// chatRow is one row of Messages' chat table with its newest message's date.
type chatRow struct {
	RowID   int64   `json:"rowid"`
	GUID    string  `json:"guid"`
	Style   int     `json:"style"`
	Ident   *string `json:"ident"`
	Service *string `json:"service"`
	Name    *string `json:"name"`
	Last    int64   `json:"last"`
}

// conversation is the chats of one conversation (a person's chats over each service, or one group).
type conversation struct {
	id      string
	group   bool
	name    string
	ident   string // a one-to-one chat's number or address
	chats   []int64
	service []string
	last    int64
}

// convID hashes what identifies a conversation, so the id says nothing about who is in it.
func convID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return ID + "-" + hex.EncodeToString(sum[:6])
}

// conversations groups every chat with a message into conversations, newest first.
func (r *Reader) conversations(ctx context.Context) ([]*conversation, error) {
	var rows []chatRow
	err := r.query(ctx, r.chatDB(), `SELECT c.ROWID AS rowid, c.guid AS guid, c.style AS style, c.chat_identifier AS ident,
  c.service_name AS service, c.display_name AS name, j.last AS last
FROM chat c JOIN (SELECT chat_id, max(message_date) AS last FROM chat_message_join GROUP BY chat_id) j ON j.chat_id = c.ROWID`, &rows)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*conversation{}
	var out []*conversation
	for _, c := range rows {
		group := c.Style == 43
		key := "group:" + c.GUID
		if !group {
			key = "person:" + strings.ToLower(str(c.Ident))
		}
		cv := byKey[key]
		if cv == nil {
			cv = &conversation{id: convID(key), group: group, ident: str(c.Ident)}
			byKey[key] = cv
			out = append(out, cv)
		}
		cv.chats = append(cv.chats, c.RowID)
		if s := str(c.Service); s != "" && !contains(cv.service, s) {
			cv.service = append(cv.service, s)
		}
		if c.Last > cv.last {
			cv.last = c.Last
		}
		if n := strings.TrimSpace(str(c.Name)); n != "" && cv.name == "" {
			cv.name = n
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].last != out[j].last {
			return out[i].last > out[j].last
		}
		return out[i].id < out[j].id
	})
	for _, cv := range out {
		sort.Strings(cv.service)
	}
	return out, nil
}

// Chats is a page of conversations, newest first, before the cursor a previous page returned. keep KeepContacts
// leaves out what does not go to Persona (Changes); anything else lists them all (the app's own view).
func (r *Reader) Chats(ctx context.Context, limit int, before, keep string) (ChatsPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	convs, err := r.conversations(ctx)
	if err != nil {
		return ChatsPage{}, err
	}
	start := 0
	if before != "" {
		at, id, ok := parseChatCursor(before)
		if !ok {
			return ChatsPage{}, fmt.Errorf("bad cursor")
		}
		start = sort.Search(len(convs), func(i int) bool {
			return convs[i].last < at || convs[i].last == at && convs[i].id > id
		})
	}
	page := convs[start:min(len(convs), start+limit)]
	out := ChatsPage{Chats: []Chat{}, More: start+limit < len(convs)}
	if len(page) == 0 {
		return out, nil
	}
	if out.More {
		last := page[len(page)-1]
		out.Before = fmt.Sprintf("%d.%s", last.last, last.id)
	}
	people, err := r.members(ctx, page)
	if err != nil {
		return ChatsPage{}, err
	}
	previews, err := r.previews(ctx, page)
	if err != nil {
		return ChatsPage{}, err
	}
	unread, err := r.unread(ctx, page)
	if err != nil {
		return ChatsPage{}, err
	}
	names := r.names(ctx)
	handles, err := r.handles(ctx, previewHandles(previews))
	if err != nil {
		return ChatsPage{}, err
	}
	var withFiles []int64
	for _, p := range previews {
		if p.Attachments != 0 {
			withFiles = append(withFiles, p.RowID)
		}
	}
	files, err := r.attachments(ctx, withFiles)
	if err != nil {
		return ChatsPage{}, err
	}
	wrote := map[string]bool{}
	if keep == KeepContacts {
		if wrote, err = r.wrote(ctx, page); err != nil {
			return ChatsPage{}, err
		}
	}
	for i, cv := range page {
		c := r.chat(cv, people[cv.id], names)
		if keep == KeepContacts && !wrote[cv.id] && !anyNamed(c.Members) {
			continue
		}
		if p, ok := previews[i]; ok {
			c.FromMe = p.FromMe == 1
			c.Preview = preview(p, names, handles, files[p.RowID], c.Group)
		}
		c.Unread = unread[i]
		out.Chats = append(out.Chats, c)
	}
	return out, nil
}

func parseChatCursor(s string) (int64, string, bool) {
	at, id, ok := strings.Cut(s, ".")
	n, err := strconv.ParseInt(at, 10, 64)
	return n, id, ok && err == nil
}

// chat is a conversation as the app shows it: a group's name, or else its people's names.
func (r *Reader) chat(cv *conversation, members []Person, names *contacts) Chat {
	c := Chat{ID: cv.id, Group: cv.group, Members: members, Services: cv.service, Last: appleTime(cv.last), Name: cv.name}
	if c.Members == nil {
		c.Members = []Person{}
	}
	if c.Services == nil {
		c.Services = []string{}
	}
	if c.Name == "" {
		var shown []string
		for _, p := range members {
			shown = append(shown, p.shown())
		}
		switch {
		case len(shown) == 0 && !cv.group && cv.ident != "":
			c.Name = names.name(cv.ident)
			if c.Name == "" {
				c.Name = cv.ident
			}
		case len(shown) == 0:
			c.Name = "（没有成员）"
		case !cv.group || len(shown) <= 3:
			c.Name = strings.Join(shown, "、")
		default:
			c.Name = fmt.Sprintf("%s等 %d 人", strings.Join(shown[:3], "、"), len(shown))
		}
	}
	return c
}

func (p Person) shown() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Handle
}

// members are each conversation's people (the user is never one of them), by conversation id.
func (r *Reader) members(ctx context.Context, convs []*conversation) (map[string][]Person, error) {
	var chats []int64
	owner := map[int64]string{}
	for _, cv := range convs {
		for _, id := range cv.chats {
			chats = append(chats, id)
			owner[id] = cv.id
		}
	}
	var rows []struct {
		Chat   int64 `json:"chat"`
		Handle int64 `json:"handle"`
	}
	if err := r.query(ctx, r.chatDB(), "SELECT chat_id AS chat, handle_id AS handle FROM chat_handle_join WHERE chat_id IN ("+
		ints(chats)+") ORDER BY chat_id, ROWID", &rows); err != nil {
		return nil, err
	}
	var ids []int64
	for _, row := range rows {
		ids = append(ids, row.Handle)
	}
	handles, err := r.handles(ctx, ids)
	if err != nil {
		return nil, err
	}
	names := r.names(ctx)
	out := map[string][]Person{}
	seen := map[string]bool{}
	for _, row := range rows {
		h, ok := handles[row.Handle]
		if !ok {
			continue
		}
		conv := owner[row.Chat]
		// The same person over iMessage and SMS is two handles with one address.
		if key := conv + "\x00" + strings.ToLower(h); !seen[key] {
			seen[key] = true
			out[conv] = append(out[conv], Person{Handle: h, Name: names.name(h)})
		}
	}
	return out, nil
}

// handles are the numbers and addresses of handle rows.
func (r *Reader) handles(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	var want []int64
	for _, id := range ids {
		if id > 0 {
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return out, nil
	}
	var rows []struct {
		RowID int64  `json:"rowid"`
		ID    string `json:"id"`
	}
	if err := r.query(ctx, r.chatDB(), "SELECT ROWID AS rowid, id FROM handle WHERE ROWID IN ("+ints(want)+")", &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.RowID] = row.ID
	}
	return out, nil
}

// previews are each conversation's newest message that is not a tapback, by index in convs.
func (r *Reader) previews(ctx context.Context, convs []*conversation) (map[int]msgRow, error) {
	parts := make([]string, len(convs))
	for i, cv := range convs {
		parts[i] = fmt.Sprintf(`SELECT * FROM (SELECT %d AS conv, %s FROM chat_message_join j JOIN message m ON m.ROWID = j.message_id
  WHERE j.chat_id IN (%s) AND m.associated_message_type = 0 ORDER BY j.message_date DESC, j.message_id DESC LIMIT 1)`, i, msgColumns, ints(cv.chats))
	}
	var rows []msgRow
	if err := r.query(ctx, r.chatDB(), strings.Join(parts, "\nUNION ALL\n"), &rows); err != nil {
		return nil, err
	}
	out := map[int]msgRow{}
	for _, row := range rows {
		out[row.Conv] = row
	}
	return out, nil
}

func previewHandles(rows map[int]msgRow) []int64 {
	var out []int64
	for _, row := range rows {
		out = append(out, row.Handle, row.Other)
	}
	return out
}

// preview is one line for the list: the text, or what the message is.
func preview(m msgRow, names *contacts, handles map[int64]string, files []Attachment, group bool) string {
	msg := message(m, names, handles, files)
	text := msg.Text
	switch {
	case msg.Unsent:
		text = "撤回了一条消息"
	case msg.Undecoded:
		text = "[无法显示的消息]"
	case text == "" && len(msg.Attachments) > 0:
		text = msg.Attachments[0].placeholder()
	}
	if group && msg.Kind == "message" && !msg.FromMe && msg.Sender != nil {
		text = msg.Sender.shown() + "：" + text
	}
	if r := []rune(text); len(r) > 80 {
		text = string(r[:80]) + "…"
	}
	return text
}

func (a Attachment) placeholder() string {
	switch a.Kind {
	case "image":
		return "[图片]"
	case "video":
		return "[视频]"
	case "audio":
		return "[语音]"
	}
	return "[文件]"
}

// unread counts each conversation's messages from others not read yet, by index in convs.
func (r *Reader) unread(ctx context.Context, convs []*conversation) (map[int]int, error) {
	var chats []int64
	index := map[int64]int{}
	for i, cv := range convs {
		for _, id := range cv.chats {
			chats = append(chats, id)
			index[id] = i
		}
	}
	var rows []struct {
		Chat int64 `json:"chat"`
		N    int   `json:"n"`
	}
	if err := r.query(ctx, r.chatDB(), `SELECT j.chat_id AS chat, count(*) AS n FROM chat_message_join j JOIN message m ON m.ROWID = j.message_id
WHERE j.chat_id IN (`+ints(chats)+`) AND m.is_read = 0 AND m.is_from_me = 0 AND m.item_type = 0 AND m.associated_message_type = 0
GROUP BY j.chat_id`, &rows); err != nil {
		return nil, err
	}
	out := map[int]int{}
	for _, row := range rows {
		out[index[row.Chat]] += row.N
	}
	return out, nil
}
