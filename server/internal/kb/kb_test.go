package kb

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeSource struct {
	chats []SourceChat
	msgs  map[string][]SourceMessage // by chat, oldest first; positions are indexes
}

func (f *fakeSource) Name() string                                     { return "lark" }
func (f *fakeSource) Chats(context.Context, int) ([]SourceChat, error) { return f.chats, nil }
func (f *fakeSource) Messages(_ context.Context, c SourceChat, after string, limit int) ([]SourceMessage, string, bool, error) {
	all := f.msgs[c.ID]
	start := 0
	if after == "" {
		start = max(0, len(all)-limit)
	} else {
		for i, m := range all {
			if m.Position == after {
				start = i + 1
			}
		}
	}
	end := min(len(all), start+limit)
	page := all[start:end]
	newer := after
	if len(page) > 0 {
		newer = page[len(page)-1].Position
	}
	return page, newer, end < len(all), nil
}
func (f *fakeSource) Download(_ context.Context, _, dst string) error {
	return writeFile(dst, []byte("img"))
}

// fakeModel answers compression and project prompts with canned JSON.
type fakeModel struct{ calls []string }

func (m *fakeModel) JSONMax(_ context.Context, system, user string, _ int, out any) error {
	m.calls = append(m.calls, user)
	var reply string
	switch {
	case strings.Contains(system, "项目档案"):
		reply = `{"summary":"## 目标\n上线","one_line":"灰度中","goal":"把 X 上线","kind":"short","status":"active","importance":"P1","relevance":80,"tags":["发布"]}`
	case strings.Contains(system, "文档、会议、日程打标"):
		reply = `{"items":[{"id":"events/e1","importance":"P2","relevance":50,"tags":["会议"],"project":"X 上线","note":"周会"}]}`
	default:
		reply = `{"summary":"## 这是什么\n讨论 X","one_line":"在推 X 上线","importance":"P1","relevance":75,"tags":["发布","#后端"],
"projects":[{"name":"X 上线","kind":"short","aliases":["X launch"],"update":"定了周五灰度","my_role":"负责人"}],
"people":[{"name":"张三","note":"后端负责人"}]}`
	}
	return json.Unmarshal([]byte(reply), out)
}

func msg(pos int, sender, text string, at time.Time) SourceMessage {
	return SourceMessage{ID: "m" + string(rune('a'+pos)), Position: string(rune('a' + pos)), SenderID: "u-" + sender,
		SenderName: sender, Type: "TEXT", Text: text, At: at}
}

func TestPipeline(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Noon yesterday in the archive's zone (time.Local here), not "an hour ago": the messages span t0..t0+3m and are
	// read back from t0's day file, so a t0 just before midnight put the later ones in the next day's file (#656).
	y, m, d := time.Now().AddDate(0, 0, -1).Date()
	t0 := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	src := &fakeSource{
		chats: []SourceChat{{ID: "f1", Name: "X 项目群", Kind: "group", UpdatedAt: t0.Add(2 * time.Minute)}},
		msgs: map[string][]SourceMessage{"f1": {
			msg(0, "张三", "周五灰度", t0),
			msg(1, "李四", "好的\n第二行", t0.Add(time.Minute)),
			{ID: "mc", Position: "c", Mine: true, Type: "IMAGE", At: t0.Add(2 * time.Minute), Media: []string{"/api/media/k1"}},
		}},
	}
	model := &fakeModel{}
	var indexed []string
	e := &Engine{Store: st, Sources: []Source{src}, Model: model, Owner: "我",
		Index: func(kind, id, _ string, _ Tagging, _ string, _ time.Time) { indexed = append(indexed, kind+"/"+id) }}

	changed, err := e.Archive(context.Background())
	if err != nil || len(changed) != 1 || changed[0] != "lark-f1" {
		t.Fatalf("archive: %v %v", changed, err)
	}
	day := t0.Local().Format("2006-01-02")
	raw, _ := os.ReadFile(st.Path(KindChat, "lark-f1", "messages", day+".md"))
	text := string(raw)
	for _, want := range []string{"# X 项目群 · " + day, "张三: 周五灰度", "李四: 好的\n  第二行", "我: [附件](attachments/"} {
		if !strings.Contains(text, want) {
			t.Fatalf("day file lacks %q:\n%s", want, text)
		}
	}
	c, _ := st.Chat("lark-f1")
	if c.Pending != 3 || c.Cursor != "c" || len(c.People) != 2 {
		t.Fatalf("chat meta: %+v", c)
	}

	// Nothing new: no second copy.
	if changed, _ := e.Archive(context.Background()); len(changed) != 0 {
		t.Fatalf("re-archived: %v", changed)
	}
	// One more message after the cursor.
	src.msgs["f1"] = append(src.msgs["f1"], msg(3, "张三", "灰度完成", t0.Add(3*time.Minute)))
	src.chats[0].UpdatedAt = t0.Add(3 * time.Minute)
	if changed, _ := e.Archive(context.Background()); len(changed) != 1 {
		t.Fatalf("new message not archived")
	}
	raw, _ = os.ReadFile(st.Path(KindChat, "lark-f1", "messages", day+".md"))
	if strings.Count(string(raw), "周五灰度") != 1 || !strings.Contains(string(raw), "灰度完成") {
		t.Fatalf("append: %s", raw)
	}

	// Compression: the chat is quiet for long enough.
	due := e.DueForCompression(time.Now())
	if len(due) != 1 {
		t.Fatalf("due: %d", len(due))
	}
	if err := e.Compress(context.Background(), due[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.calls[0], "灰度完成") || !strings.Contains(model.calls[0], "周五灰度") {
		t.Fatalf("model input lacks the messages: %s", model.calls[0])
	}
	c, _ = st.Chat("lark-f1")
	if c.Pending != 0 || c.Importance != "P1" || c.Relevance != 75 || strings.Join(c.Tags, ",") != "发布,后端" || c.OneLine != "在推 X 上线" {
		t.Fatalf("tags: %+v", c)
	}
	if !strings.Contains(st.Summary(KindChat, "lark-f1"), "讨论 X") {
		t.Fatal("chat summary not written")
	}
	p, ok := st.Project("x-上线")
	if !ok || len(p.Chats) != 1 || !p.Dirty || p.MyRole != "负责人" || len(p.People) != 2 || p.Aliases[0] != "X launch" {
		t.Fatalf("project: %+v %v", p, ok)
	}
	if log := st.Log("x-上线", 5); len(log) != 1 || !strings.Contains(log[0], "定了周五灰度") {
		t.Fatalf("log: %v", log)
	}
	per, _ := st.Person("lark-u-张三")
	if per.Note != "后端负责人" || len(per.Projects) != 1 {
		t.Fatalf("person: %+v", per)
	}
	if s := st.Summary(KindPerson, "lark-u-张三"); !strings.Contains(s, "X 上线") || !strings.Contains(s, "X 项目群") {
		t.Fatalf("person summary: %s", s)
	}
	// The alias finds the same project.
	if e.FindProject("x launch") == nil {
		t.Fatal("alias not matched")
	}

	if n := e.RefreshProjects(context.Background(), 5); n != 1 {
		t.Fatalf("refreshed %d", n)
	}
	p, _ = st.Project("x-上线")
	if p.Dirty || p.OneLine != "灰度中" || p.Goal != "把 X 上线" || p.Relevance != 80 {
		t.Fatalf("project after refresh: %+v", p)
	}

	// Items get tagged and linked to the project.
	_ = st.PutItem(&Item{Kind: KindEvent, ID: "e1", Title: "X 周会", At: t0})
	if err := e.TagItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	it, _ := st.Item(KindEvent, "e1")
	if it.Importance != "P2" || len(it.Projects) != 1 {
		t.Fatalf("item: %+v", it)
	}
	p, _ = st.Project("x-上线")
	if len(p.Items) != 1 || p.Items[0] != "events/e1" {
		t.Fatalf("project items: %v", p.Items)
	}

	if hits := e.Search("灰度 完成", true, 10); len(hits) == 0 || !strings.Contains(hits[0].Line, "灰度完成") {
		t.Fatalf("search: %+v", hits)
	}
	if list := e.List(KindProject, "发布"); len(list) != 1 || list[0].Name != "X 上线" {
		t.Fatalf("list: %+v", list)
	}
	if pr := e.Prompt(5, 5); !strings.Contains(pr, "X 上线") || !strings.Contains(pr, "X 项目群") {
		t.Fatalf("prompt: %s", pr)
	}
	if len(indexed) == 0 {
		t.Fatal("index not called")
	}
}

func TestLockedTagsStay(t *testing.T) {
	tg := Tagging{Importance: "P0", Relevance: 90, Locked: true}
	applyTags(&tg, "P3", 10, []string{"x"}, time.Now())
	if tg.Importance != "P0" || tg.Relevance != 90 || tg.Tags[0] != "x" {
		t.Fatalf("%+v", tg)
	}
}

func TestPrune(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	now := time.Now()
	c := &Chat{ID: "lark-a", Name: "a"}
	old, recent := now.AddDate(0, 0, -400), now.AddDate(0, 0, -40)
	_ = st.AppendMessages(c, []Line{{At: old, Sender: "x", Text: "old"}, {At: recent, Sender: "x", Text: "recent"}})
	_ = writeFile(st.Path(KindChat, "lark-a", "attachments", recent.Local().Format("2006-01-02"), "f.jpg"), []byte("x"))
	if n := st.Prune(now, 365, 30); n != 2 {
		t.Fatalf("removed %d", n)
	}
	if days := st.Days("lark-a"); len(days) != 1 || days[0] != recent.Local().Format("2006-01-02") {
		t.Fatalf("days: %v", days)
	}
}

// A chat archived for the first time starts with its newest page, which for a quiet chat reaches back years; the
// first mail of a quiet mailbox can be as old. Nothing past the text retention is written: the daily prune would
// delete it and the next chat or mailbox read for the first time would write such days again. The oldest day the
// prune keeps is written whole.
func TestArchiveKeepsToTheTextRetention(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	now := time.Now()
	edge := now.AddDate(0, 0, -365).In(st.Zone())
	edge = time.Date(edge.Year(), edge.Month(), edge.Day(), 0, 1, 0, 0, st.Zone()) // first minute of the oldest kept day
	var msgs []SourceMessage
	for i, at := range []time.Time{now.AddDate(0, 0, -900), now.AddDate(0, 0, -700), now.AddDate(0, 0, -400), edge,
		now.AddDate(0, 0, -30), now.AddDate(0, 0, -1)} {
		msgs = append(msgs, msg(i, "张三", at.Format(time.DateOnly), at))
	}
	src := &fakeSource{chats: []SourceChat{{ID: "q", Name: "安静的群", Kind: "group", UpdatedAt: msgs[5].At}},
		msgs: map[string][]SourceMessage{"q": msgs}}
	e := &Engine{Store: st, Sources: []Source{src}, TextDays: 365}
	ctx := context.Background()
	if _, err := e.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	mails := []SourceMessage{msg(6, "李四", "【旧】500 天前", now.AddDate(0, 0, -500)), msg(7, "李四", "【新】刚才", now.Add(-time.Hour))}
	if _, err := e.Ingest(ctx, src, SourceChat{ID: "li", Name: "李四 · 邮件", Kind: "mail"}, mails); err != nil {
		t.Fatal(err)
	}
	cut := edge.Format(time.DateOnly)
	for _, id := range st.IDs(KindChat) {
		for _, d := range st.Days(id) {
			if d < cut {
				t.Errorf("%s: day %s written, past the 365-day retention", id, d)
			}
		}
	}
	if n := e.Prune(now); n != 0 {
		t.Errorf("the daily prune removed %d day(s) just written", n)
	}
	if days := st.Days("lark-q"); len(days) == 0 || days[0] != cut {
		t.Errorf("chat days %v, want the oldest kept day %s first", days, cut)
	}
	if c, _ := st.Chat("lark-q"); c.Messages != 3 {
		t.Errorf("chat counts %d lines, want the 3 within the retention", c.Messages)
	}
	if c, _ := st.Chat("lark-li"); c.Messages != 1 {
		t.Errorf("mail chat counts %d lines, want 1", c.Messages)
	}
}

func TestSlug(t *testing.T) {
	if s := Slug("Persona 2.0 发布!"); s != "persona-2-0-发布" {
		t.Fatal(s)
	}
	if s := Slug("Persona 2.0 发布!"); s != "persona-2-0-发布" {
		t.Fatal(s)
	}
	if s := SafeID("../a/b c"); s != "a_b_c" {
		t.Fatal(s)
	}
}

// A long id is cut to 120 bytes on a character boundary: half a character is not a name macOS can create.
func TestSafeIDCutsWholeCharacters(t *testing.T) {
	id := "lark-" + strings.Repeat("项目", 30) // the 120th byte falls inside a character
	s := SafeID(id)
	if !utf8.ValidString(s) || len(s) > 120 || len(s) < 117 || !strings.HasPrefix(id, s) {
		t.Fatalf("SafeID(%q) = %q (%d bytes)", id, s, len(s))
	}
	if s := SafeID(strings.Repeat("a", 130)); s != strings.Repeat("a", 120) {
		t.Fatalf("an ASCII id is cut at 120 bytes, got %d", len(s))
	}
	st, _ := NewStore(t.TempDir())
	if err := st.PutItem(&Item{Kind: KindDoc, ID: id, Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if it, ok := st.Item(KindDoc, id); !ok || it.Title != "t" {
		t.Fatalf("the item did not read back: %v %v", it, ok)
	}
}

func TestTopicChatNoDuplicates(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	t0 := time.Now().Add(-time.Hour).Truncate(time.Minute)
	src := &topicSource{msgs: []SourceMessage{msg(0, "a", "one", t0), msg(1, "b", "two", t0.Add(time.Minute))}}
	e := &Engine{Store: st, Sources: []Source{src}}
	_, _ = e.Archive(context.Background())
	src.msgs = append(src.msgs, msg(2, "a", "three", t0.Add(2*time.Minute)))
	src.at = t0.Add(2 * time.Minute)
	_, _ = e.Archive(context.Background())
	c, _ := st.Chat("lark-t")
	if c.Messages != 3 {
		t.Fatalf("archived %d lines, want 3", c.Messages)
	}
}

// topicSource always returns the newest page and no cursor, like a topic group.
type topicSource struct {
	msgs []SourceMessage
	at   time.Time
}

func (s *topicSource) Name() string { return "lark" }
func (s *topicSource) Chats(context.Context, int) ([]SourceChat, error) {
	at := s.at
	if at.IsZero() {
		at = s.msgs[len(s.msgs)-1].At
	}
	return []SourceChat{{ID: "t", Name: "话题群", Kind: "group", Topic: true, UpdatedAt: at}}, nil
}
func (s *topicSource) Messages(context.Context, SourceChat, string, int) ([]SourceMessage, string, bool, error) {
	return s.msgs, "", false, nil
}
func (s *topicSource) Download(context.Context, string, string) error { return nil }

func TestHasPeople(t *testing.T) {
	cases := map[string]bool{"acc-no_reply@email.apple.com": false, "acc-noreply@example.com": false, "acc-newsletter@example.org": false,
		"acc-zhang.san@example.com": true, "acc-hello@example.org": false,
		// Missed before the 2026-10-31 widening: they came out as people, and their receipts and promotions as relevant.
		"acc-receipts@shop.example": false, "acc-applesupport@email.apple.com": false, "acc-contact@tools.example": false,
		"acc-12345-abc@submissions.list.example": false, "acc-someone@em-s.storage.example": false, "acc-x@txn.storage.example": false,
		"acc-mailer-daemon@mail.example": false, "acc-notify@example.com": false, "acc-bank@message.us.bank.example": false, "acc-remy.yu@bank.example": true, "acc-emma@agency.example": true}
	for src, want := range cases {
		if got := HasPeople(&Chat{Kind: "mail", SourceID: src}); got != want {
			t.Errorf("%s: %v", src, got)
		}
	}
	if HasPeople(&Chat{Kind: "bot"}) || !HasPeople(&Chat{Kind: "group"}) {
		t.Error("kinds")
	}
}

// Mail never replied to, tagged under older rules, goes through compression once more (automated senders told so);
// exchanges the owner wrote in, hand-tagged chats and chats already under the current rules stay as they are.
func TestRetagMail(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	done := t0.Add(time.Hour)
	put := func(c *Chat) {
		c.Created, c.LastAt, c.CompressedAt, c.CoveredUntil = t0, t0, done, t0.Add(time.Minute)
		if err := st.PutChat(c); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendMessages(c, []Line{{At: t0, Sender: "Shop", Text: "Your receipt"}}); err != nil {
			t.Fatal(err)
		}
	}
	receipt := &Chat{ID: "mail-a-receipts@shop.example", Source: "mail", SourceID: "a-receipts@shop.example", Kind: "mail", Name: "Shop · 邮件",
		Tagging: Tagging{Importance: "P2", Relevance: 70}}
	person := &Chat{ID: "mail-a-zhang.san@example.com", Source: "mail", SourceID: "a-zhang.san@example.com", Kind: "mail", Name: "张三 · 邮件", Mine: true}
	locked := &Chat{ID: "mail-a-noreply@bank.example", Source: "mail", SourceID: "a-noreply@bank.example", Kind: "mail", Name: "Bank · 邮件",
		Tagging: Tagging{Importance: "P1", Relevance: 90, Locked: true}}
	current := &Chat{ID: "mail-a-news@site.example", Source: "mail", SourceID: "a-news@site.example", Kind: "mail", Name: "Site · 邮件", TagRev: tagRev}
	for _, c := range []*Chat{receipt, person, locked, current} {
		put(c)
	}
	model := &fakeModel{}
	e := &Engine{Store: st, Model: model, Owner: "我"}
	if n := e.RetagMail(); n != 1 {
		t.Fatalf("queued %d, want only the receipt", n)
	}
	due := e.DueForCompression(time.Now())
	if len(due) != 1 || due[0].ID != receipt.ID {
		t.Fatalf("due = %v", due)
	}
	if err := e.Compress(context.Background(), due[0]); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 1 || !strings.Contains(model.calls[0], "自动发出") || !strings.Contains(model.calls[0], "Your receipt") {
		t.Fatalf("model input = %q", model.calls)
	}
	c, _ := st.Chat(receipt.ID)
	if c.TagRev != tagRev || c.Pending != 0 {
		t.Fatalf("after: rev %d pending %d", c.TagRev, c.Pending)
	}
	if e.RetagMail() != 0 {
		t.Fatal("queued again under the same rules")
	}
}

func TestSearchExtraDirs(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notes := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(notes, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("background/machines/box.md", "---\ndescription: box 是 GPU 机器\n---\n登录 box 要先 kinit\n")
	write(".git/HEAD", "box kinit in git internals\n")
	write(".git/notes.md", "box kinit in a dot dir\n")
	write("raw.txt", "box kinit in a non-Markdown file\n")
	e := &Engine{Store: st, ExtraDirs: []string{notes}}
	hits := e.Search("box kinit", true, 10)
	if len(hits) != 1 || hits[0].Line != "登录 box 要先 kinit" || hits[0].Path != filepath.Join(notes, "background/machines/box.md") {
		t.Fatalf("hits: %+v", hits)
	}
	if hits := (&Engine{Store: st}).Search("box kinit", true, 10); len(hits) != 0 {
		t.Fatalf("without extra dirs: %+v", hits)
	}
}
