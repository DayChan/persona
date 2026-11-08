package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// ---- mail ----

// collectMail applies a round's mail: each account's latest page, then the full text of the new mail (the round's
// new items too: they are triaged on it) and of items collected before their text was read.
func (s *Service) collectMail(ctx context.Context, read *assistantwire.InboxMailRead) []*InboxItem {
	b := &s.inbox
	b.readSource("mail", "邮箱")
	var changed []*InboxItem
	for _, a := range read.Accounts {
		changed = append(changed, b.applyMail(a, a.Messages, time.Now())...)
		b.mu.Lock()
		var unread []*InboxItem
		b.each(func(it *InboxItem) {
			if it.Mail != nil && it.Mail.AccountID == a.ID && it.open() && !it.Read && it.BodyOf != it.Last {
				unread = append(unread, it)
			}
		})
		b.mu.Unlock()
		for _, it := range unread {
			if s.readMailBody(ctx, a, it) {
				changed = append(changed, it)
			}
		}
	}
	return changed
}

// readMailBody replaces the list's 2 KB snippet with the newest message's text (HTML-only newsletters have nothing
// but CSS in their first bytes). The hub reads it peeking: the mail stays unread.
func (s *Service) readMailBody(ctx context.Context, a assistantwire.InboxMailAccount, it *InboxItem) bool {
	b := &s.inbox
	b.mu.Lock()
	if it.Mail == nil || len(it.Mail.UIDs) == 0 {
		b.mu.Unlock()
		return false
	}
	uid, mailbox, last := it.Mail.UIDs[len(it.Mail.UIDs)-1], it.Mail.Mailbox, it.Last
	b.mu.Unlock()
	m, err := s.inboxMailBody(ctx, assistantwire.InboxMailRef{AccountID: a.ID, UID: uid, Mailbox: mailbox})
	if err != nil {
		s.logf("inbox: read mail %s/%d: %v", a.Email, uid, err)
		return false
	}
	text := strings.TrimSpace(m.Text)
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	b.roundWrite(it)
	it.BodyOf = last
	if text == "" {
		return true
	}
	it.Preview = clip(oneLine(text), 200)
	var to []string
	for _, t := range m.To {
		to = append(to, t.Email)
	}
	var cc []string
	for _, t := range m.Cc {
		cc = append(cc, t.Email)
	}
	it.Context = fmt.Sprintf("邮箱 %s 收到的未读邮件（共 %d 封，下面是最新一封）：\n发件人：%s <%s>\n收件人：%s\n抄送：%s\n时间：%s\n主题：%s\n\n%s",
		a.Email, len(it.Mail.UIDs), m.From.Name, m.From.Email, strings.Join(to, ", "), strings.Join(cc, ", "), m.Date, m.Subject, clip(text, 3000))
	return true
}

var replyPrefix = regexp.MustCompile(`(?i)^\s*((re|fw|fwd|aw|回复|答复|转发)\s*[:：]\s*|\[[^\]]{1,30}\]\s*)+`)

// mailThread is what keeps replies in one item: the subject without Re: / Fwd: / [list] prefixes. A subject that is
// empty once they are gone says nothing about which mails go together: those are grouped by who sent them too, so a
// subject-less mail from one person is not folded under another's (#4852).
func mailThread(subject string, from ...string) string {
	s := strings.ToLower(strings.Join(strings.Fields(replyPrefix.ReplaceAllString(subject, "")), " "))
	if s == "" && len(from) > 0 && strings.TrimSpace(from[0]) != "" {
		s = "\x00from:" + strings.ToLower(strings.TrimSpace(from[0]))
	}
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// applyMail turns one account's latest inbox page into items: unseen messages grouped by thread.
func (b *inboxBook) applyMail(a assistantwire.InboxMailAccount, msgs []assistantwire.InboxMailSummary, now time.Time) []*InboxItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	var changed []*InboxItem
	index := b.threads(func(it *InboxItem) (string, bool) {
		if it.Mail == nil || it.Mail.AccountID != a.ID {
			return "", false
		}
		return it.Mail.Thread, true
	})
	superseded, stay := b.supersedeSubjectless(index, msgs, now)
	changed = append(changed, superseded...)
	threads := map[string][]assistantwire.InboxMailSummary{}
	var order []string
	for _, m := range msgs {
		t := mailThread(m.Subject, m.From.Email)
		if stay[m.UID] {
			t = subjectlessThread // the old item that keeps its mail (supersedeSubjectless)
		}
		if _, ok := threads[t]; !ok {
			order = append(order, t)
		}
		threads[t] = append(threads[t], m)
	}
	for _, t := range order {
		list := threads[t]
		sort.Slice(list, func(i, j int) bool { return list[i].UID < list[j].UID })
		var unseen []assistantwire.InboxMailSummary
		for _, m := range list {
			d, _ := time.Parse(time.RFC3339, m.Date)
			if !m.Seen && now.Sub(d) <= inboxWindow {
				unseen = append(unseen, m)
			}
		}
		open, done := index.openFor(t)
		if open == nil && done != nil {
			last, _ := strconv.ParseUint(done.Last, 10, 32)
			var fresh []assistantwire.InboxMailSummary
			for _, m := range unseen {
				if uint64(m.UID) > last {
					fresh = append(fresh, m)
				}
			}
			unseen = fresh
		}
		// Mail the thread's other items hold (a done one still unread on the server, an older one moved back) is
		// theirs: it does not come into the open one too, counted twice (#3796).
		held := map[uint32]bool{}
		for _, it := range index.by[t] {
			if k, ok := index.key(it); ok && k == t && it != open {
				for _, uid := range it.Mail.UIDs {
					held[uid] = true
				}
			}
		}
		if len(held) > 0 {
			unseen = slices.DeleteFunc(unseen, func(m assistantwire.InboxMailSummary) bool { return held[m.UID] })
		}
		if len(unseen) == 0 {
			// Read in another client: done unless it needs the user.
			if open != nil && !open.Read {
				b.roundWrite(open)
				open.Read = true
				if open.Tier != TierAction && open.Tier != "" {
					b.file(open, now, "read")
				}
				changed = append(changed, open)
			}
			continue
		}
		newest := unseen[len(unseen)-1]
		if open != nil && open.Last == strconv.FormatUint(uint64(newest.UID), 10) && len(open.Mail.UIDs) == len(unseen) {
			continue
		}
		it := open
		var prev inboxSeen
		if it != nil {
			prev = seenOf(it)
			b.roundWrite(it)
		}
		if it == nil {
			it = &InboxItem{ID: fmt.Sprintf("mail:%s:%s:%d", a.ID, t, unseen[0].UID), Source: "mail", Kind: KindMail,
				Account: a.ID, Mail: &InboxMail{AccountID: a.ID, Mailbox: newest.Mailbox, Thread: t}}
			it.StartedAt, _ = time.Parse(time.RFC3339, unseen[0].Date)
			b.add("mail", it)
			b.roundNew(it)
			index.note(it)
		}
		it.AccountName = a.Email
		it.Title = newest.Subject
		if it.Title == "" {
			it.Title = "（无主题）"
		}
		it.Sender = newest.From.Name
		if it.Sender == "" {
			it.Sender = newest.From.Email
		}
		it.Preview = clip(oneLine(newest.Snippet), 200)
		it.Count = len(unseen)
		it.UpdatedAt, _ = time.Parse(time.RFC3339, newest.Date)
		it.Read = false
		it.Last = strconv.FormatUint(uint64(newest.UID), 10)
		it.Mail.UIDs = it.Mail.UIDs[:0]
		for _, m := range unseen {
			it.Mail.UIDs = append(it.Mail.UIDs, m.UID)
		}
		it.Mail.MessageID, it.Mail.From = newest.MessageID, newest.From
		it.Mail.Direct = false
		for _, to := range newest.To {
			if strings.EqualFold(to.Email, a.Email) {
				it.Mail.Direct = true
			}
		}
		it.Context = mailContext(a, unseen)
		b.noteReceived(it, prev, mailMessages(unseen, prev.uids))
		changed = append(changed, it)
	}
	return changed
}

// subjectlessThread is the key every subject-less mail had before #4852 grouped them by sender too.
var subjectlessThread = mailThread("")

// supersedeSubjectless settles an open item from before #4852 that holds subject-less mail of several senders under
// the empty subject's key, under b.mu: no round matches it by its senders any more, so each mail is listed once
// (#3796, #4920).
//
//   - One the owner has to act on (Needs you) or the AI twin worked on keeps its mail as it was (stay: those UIDs stay under
//     the empty subject's key), with its tier and draft; it is read and filed like any item. A sender item a round
//     since #4852 opened with only that mail is filed as superseded.
//   - Any other is filed as superseded once every unread mail of it on the page has a sender (each goes into its
//     sender's item); while some have none it stays open for those, and the round keeps them in it.
//   - When all of its mail on the page is read elsewhere it is read like any item; while none is on the page it is
//     left alone.
//
// An item the owner moved back (Move back) is filed as superseded again the same way.
func (b *inboxBook) supersedeSubjectless(index *threadIndex, msgs []assistantwire.InboxMailSummary, now time.Time) ([]*InboxItem, map[uint32]bool) {
	open, _ := index.openFor(subjectlessThread)
	if open == nil {
		return nil, nil
	}
	byUID := make(map[uint32]assistantwire.InboxMailSummary, len(msgs))
	for _, m := range msgs {
		byUID[m.UID] = m
	}
	if open.Tier == TierAction || open.Agent != nil {
		stay := map[uint32]bool{}
		for _, uid := range open.Mail.UIDs {
			stay[uid] = true
		}
		// A hub that already ran #4852 opened sender items for the same mail: one holding only mail the old item
		// keeps would list it a second time and no round touches it again, so it is filed. One with other mail too
		// keeps only that (the round lists its thread's mail, without these); one the owner has to act on, or with a
		// draft, is left as it is.
		var filed []*InboxItem
		for _, it := range b.items {
			if it == open || !it.open() || it.Mail == nil || it.Mail.AccountID != open.Mail.AccountID ||
				it.Mail.Mailbox != open.Mail.Mailbox || it.Mail.Thread == subjectlessThread ||
				it.Tier == TierAction || it.Agent != nil || len(it.Mail.UIDs) == 0 ||
				slices.ContainsFunc(it.Mail.UIDs, func(uid uint32) bool { return !stay[uid] }) {
				continue
			}
			b.roundWrite(it)
			it.done(now, "superseded")
			filed = append(filed, it)
		}
		return filed, stay
	}
	onPage, seen, moved, remain := 0, 0, 0, 0
	for _, uid := range open.Mail.UIDs {
		m, ok := byUID[uid]
		switch {
		case !ok:
			continue
		case m.Seen:
			seen++
		case strings.TrimSpace(m.From.Email) != "" && mailThread(m.Subject, m.From.Email) != subjectlessThread:
			moved++
		default:
			remain++
		}
		onPage++
	}
	switch {
	case moved > 0 && remain == 0:
		b.roundWrite(open)
		open.done(now, "superseded")
	case onPage > 0 && seen == onPage && !open.Read:
		b.roundWrite(open)
		open.Read = true
		if open.Tier != "" {
			open.done(now, "read")
		}
	default:
		return nil, nil // nothing on the page, or mail without a sender that the round keeps in it
	}
	return []*InboxItem{open}, nil
}

// mailMessages are the mails of a batch (those not in the item before) for its received entry.
func mailMessages(unseen []assistantwire.InboxMailSummary, had []uint32) []InboxChainMessage {
	var out []InboxChainMessage
	for _, m := range unseen {
		if slices.Contains(had, m.UID) {
			continue
		}
		sender := m.From.Name
		if sender == "" {
			sender = m.From.Email
		}
		msg := InboxChainMessage{Sender: sender, Text: clip(oneLine(m.Snippet), chainMessageText)}
		if d, err := time.Parse(time.RFC3339, m.Date); err == nil {
			d = d.UTC()
			msg.At = &d
		}
		out = append(out, msg)
	}
	return out
}

func mailContext(a assistantwire.InboxMailAccount, msgs []assistantwire.InboxMailSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "邮箱 %s 收到的未读邮件：\n", a.Email)
	for _, m := range msgs {
		var to []string
		for _, t := range m.To {
			to = append(to, t.Email)
		}
		fmt.Fprintf(&b, "---\n发件人：%s <%s>\n收件人：%s\n时间：%s\n主题：%s\n%s\n", m.From.Name, m.From.Email, strings.Join(to, ", "),
			m.Date, m.Subject, clip(oneLine(m.Snippet), inboxText))
	}
	return strings.TrimSpace(b.String())
}
