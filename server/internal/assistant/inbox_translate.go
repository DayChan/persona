package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Translation (docs/apple/ios.md "Translation") is the manager's. The app decides what needs translating (its own setting, its
// own language detection) and asks (POST /api/v1/translate, /api/v1/translate/mail, relayed by the hub); the manager
// asks the translation model (purpose "translate": a cheap one is enough) and keeps the answers in memory, so opening
// the same mail again is instant. Mail is translated by reference (the hub reads the message: inbox.mail_body,
// readable), short texts (Feishu messages of chosen chats) in batches.
//
// New mail is translated as it comes in, not when it is opened (2026-11-01): after each mail collection, open mail
// items whose text is in another script than a target some device asked for (Settings › Translation sent to the hub with the
// device's prefs, InboxConfig.Translate) get the translation stored on the item. The list shows the translated subject
// and preview; opening the mail finds the whole translation there (storedMailTranslation).

const (
	translateBatch    = 30    // texts in one request
	translateText     = 2000  // characters of one short text
	translateMailText = 12000 // characters of a mail body translated
	translateCacheMax = 4000
)

type translateCache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *translateCache) get(k string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *translateCache) put(k, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= translateCacheMax {
		c.m = map[string]string{} // crude, but a day's worth of mail fits many times over
	}
	c.m[k] = v
}

func translateKey(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// translateFingerprint names the translation model now (models_resolve.go): a changed model does not answer from
// the cache.
func (s *Service) translateFingerprint(ctx context.Context) (string, error) {
	return s.modelFingerprint(ctx, purposeTranslate)
}

// translateTexts translates short texts, keeping their order; cached ones are not sent again.
func (s *Service) translateTexts(ctx context.Context, texts []string, target string) ([]string, error) {
	lang, ok := assistantwire.TranslateLanguages[target]
	if !ok {
		return nil, i18n.ErrEnglish("inbox-errors.translation_target", i18n.Args{"target": strconv.Quote(target)})
	}
	fingerprint, err := s.translateFingerprint(ctx)
	if err != nil {
		return nil, err
	}
	b := &s.inbox
	out := make([]string, len(texts))
	var todo []int
	for i, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if v, ok := b.translations.get(translateKey("t", fingerprint, target, t)); ok {
			out[i] = v
		} else {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return out, nil
	}
	in := make([]string, len(todo))
	total := 0
	for j, i := range todo {
		in[j] = clip(strings.TrimSpace(texts[i]), translateText)
		total += len([]rune(in[j]))
	}
	body, _ := json.Marshal(map[string]any{"texts": in})
	var res struct {
		Texts []string `json:"texts"`
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// A reasoning model spends hidden tokens first (docs/lessons/agents.md): leave plenty.
	if err := s.inboxModelJSON(cctx, purposeTranslate, prompts.RenderTranslateTexts(lang), string(body), min(4000+total*3, 32000), &res); err != nil {
		return nil, err
	}
	if len(res.Texts) != len(in) {
		return nil, i18n.ErrEnglish("inbox-errors.translation_count", i18n.Args{"actual": len(res.Texts), "expected": len(in)})
	}
	for j, i := range todo {
		v := strings.TrimSpace(res.Texts[j])
		out[i] = v
		if v != "" {
			b.translations.put(translateKey("t", fingerprint, target, strings.TrimSpace(texts[i])), v)
		}
	}
	return out, nil
}

// mailTranslation is POST /api/v1/translate/mail's answer.
type mailTranslation struct {
	ModelFingerprint string `json:"model_fingerprint,omitempty"`
	Subject          string `json:"subject"`
	Text             string `json:"text"`
	Clipped          bool   `json:"clipped,omitempty"` // the body was longer than translated
}

// translateMail translates one message's subject and body (the HTML version as readable text when there is no text
// part): the translation the inbox keeps of it when there is one by the same model, else a new one.
func (s *Service) translateMail(ctx context.Context, ref assistantwire.InboxMailRef, target string) (mailTranslation, error) {
	if _, ok := assistantwire.TranslateLanguages[target]; !ok {
		return mailTranslation{}, i18n.ErrEnglish("inbox-errors.translation_target", i18n.Args{"target": strconv.Quote(target)})
	}
	fingerprint, err := s.translateFingerprint(ctx)
	if err != nil {
		return mailTranslation{}, err
	}
	if t, ok := s.storedMailTranslation(ctx, ref, target, fingerprint); ok { // translated when it came in
		return t, nil
	}
	return s.translateMailFresh(ctx, ref, target, fingerprint)
}

// translateMailFresh is translateMail without the inbox's translation (the background pass makes those).
func (s *Service) translateMailFresh(ctx context.Context, ref assistantwire.InboxMailRef, target, fingerprint string) (mailTranslation, error) {
	lang, ok := assistantwire.TranslateLanguages[target]
	if !ok {
		return mailTranslation{}, i18n.ErrEnglish("inbox-errors.translation_target", i18n.Args{"target": strconv.Quote(target)})
	}
	b := &s.inbox
	key := translateKey("m", fingerprint, ref.AccountID, ref.Mailbox, fmt.Sprint(ref.UID), target)
	if v, ok := b.translations.get(key); ok {
		var t mailTranslation
		if json.Unmarshal([]byte(v), &t) == nil {
			return t, nil
		}
	}
	ref.Readable = true
	m, err := s.inboxMailBody(ctx, ref)
	if err != nil {
		return mailTranslation{}, err
	}
	text := strings.TrimSpace(m.Text)
	var t mailTranslation
	if r := []rune(text); len(r) > translateMailText {
		text, t.Clipped = string(r[:translateMailText]), true
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	in := prompts.RenderTranslateMailInput(m.Subject, text)
	if err := s.inboxModelJSON(cctx, purposeTranslate, prompts.RenderTranslateMail(lang), in, min(4000+len([]rune(in))*3, 48000), &t); err != nil {
		return mailTranslation{}, err
	}
	t.Subject, t.Text = strings.TrimSpace(t.Subject), strings.TrimSpace(t.Text)
	if t.Text == "" && text != "" {
		return mailTranslation{}, i18n.ErrEnglish("inbox-errors.translation_empty")
	}
	t.ModelFingerprint = fingerprint
	raw, _ := json.Marshal(t)
	b.translations.put(key, string(raw))
	return t, nil
}

// writeTranslateError preserves expected service failures; other translation failures remain upstream errors.
func writeTranslateError(w http.ResponseWriter, err error) {
	var failure interface {
		error
		Code() string
		HTTPStatus() int
	}
	if errors.As(err, &failure) {
		apierr.WriteCode(w, failure.HTTPStatus(), failure.Code(), failure)
		return
	}
	apierr.Write(w, http.StatusBadGateway, err)
}

// translateRoutes are the translation's routes (InboxRoutes).
func (s *Service) translateRoutes() {
	s.handle("POST /api/v1/translate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Texts  []string `json:"texts"`
			Target string   `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Texts) == 0 || len(in.Texts) > translateBatch {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.translation_batch", i18n.Args{"max": translateBatch}))
			return
		}
		out, err := s.translateTexts(r.Context(), in.Texts, in.Target)
		if err != nil {
			writeTranslateError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"texts": out})
	})
	s.handle("POST /api/v1/translate/mail", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			AccountID string `json:"account_id"`
			UID       uint32 `json:"uid"`
			Mailbox   string `json:"mailbox"`
			Target    string `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.AccountID == "" || in.UID == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.mail_target_required"))
			return
		}
		if in.Mailbox == "" {
			in.Mailbox = "INBOX"
		}
		t, err := s.translateMail(r.Context(), assistantwire.InboxMailRef{AccountID: in.AccountID, UID: in.UID, Mailbox: in.Mailbox}, in.Target)
		if err != nil {
			writeTranslateError(w, err)
			return
		}
		apierr.WriteJSON(w, t)
	})
}

// InboxTranslation is one item's translation into one language.
type InboxTranslation struct {
	Subject          string `json:"subject"`
	Preview          string `json:"preview"`
	Text             string `json:"text,omitempty"` // the whole body; not in the list
	Clipped          bool   `json:"clipped,omitempty"`
	ModelFingerprint string `json:"model_fingerprint,omitempty"`
	Of               string `json:"of,omitempty"` // the item's Last it translates
}

// foreignScript reports whether text is in another writing system than target's. Scripts, not languages: English
// mail is translated for a Chinese target, French is not for an English one (both Latin). Too little text says no.
func foreignScript(text, target string) bool {
	var han, kana, hangul, latin, cyrillic, letters int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			kana++
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Latin, r):
			latin++
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		default:
			continue
		}
		letters++
	}
	if letters < 12 {
		return false
	}
	share := func(n int) float64 { return float64(n) / float64(letters) }
	switch strings.SplitN(target, "-", 2)[0] {
	case "zh":
		// Latin words inside Chinese mail (names, code) are common: Chinese is a fifth of the letters at least.
		return share(han) < 0.2 || share(kana) > 0.1
	case "ja":
		return share(kana) < 0.05
	case "ko":
		return share(hangul) < 0.2
	case "ru":
		return share(cyrillic) < 0.3
	default:
		return share(latin) < 0.5
	}
}

type translateJob struct {
	it      *InboxItem
	target  string
	account string
	mailbox string
	uid     uint32
	last    string
}

// translateInbox translates what needs it, in the background, one pass at a time.
func (s *Service) translateInbox(ctx context.Context) {
	// Like triageInbox: the pass outlives the caller, since a pull to refresh answers before it is done.
	ctx = context.WithoutCancel(ctx)
	cfg := s.inboxConfig(ctx)
	targets := cfg.Translate
	if len(targets) == 0 {
		return
	}
	fingerprint, err := s.translateFingerprint(ctx)
	b := &s.inbox
	if err != nil || !b.translating.TryLock() {
		return
	}
	b.mu.Lock()
	var jobs []translateJob
	for _, it := range b.items {
		if it.Mail == nil || !it.open() || len(it.Mail.UIDs) == 0 || it.BodyOf != it.Last {
			continue
		}
		for _, t := range targets {
			if have := it.Translations[t]; have != nil && have.Of == it.Last && have.ModelFingerprint == fingerprint {
				continue
			}
			if !foreignScript(it.Title+"\n"+it.Preview, t) {
				continue
			}
			jobs = append(jobs, translateJob{it, t, it.Mail.AccountID, it.Mail.Mailbox, it.Mail.UIDs[len(it.Mail.UIDs)-1], it.Last})
		}
	}
	b.mu.Unlock()
	if len(jobs) == 0 {
		b.translating.Unlock()
		return
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].it.UpdatedAt.After(jobs[j].it.UpdatedAt) }) // newest first
	go func() {
		defer b.translating.Unlock()
		for _, j := range jobs {
			if ctx.Err() != nil {
				return
			}
			t, err := s.translateMailFresh(ctx, assistantwire.InboxMailRef{AccountID: j.account, UID: j.uid, Mailbox: j.mailbox}, j.target, fingerprint)
			if err != nil {
				if paused, _ := modelPaused(err); paused { // the rest wait for the next pass
					return
				}
				s.logf("inbox: translate %s into %s: %v", j.it.ID, j.target, err)
				continue
			}
			b.mu.Lock()
			if j.it.Last != j.last { // new mail in the thread meanwhile: the next pass
				b.mu.Unlock()
				continue
			}
			w := beginWrite(j.it)
			if j.it.Translations == nil {
				j.it.Translations = map[string]*InboxTranslation{}
			}
			j.it.Translations[j.target] = &InboxTranslation{Subject: t.Subject, Preview: clip(oneLine(t.Text), 200), Text: t.Text,
				Clipped: t.Clipped, Of: j.last, ModelFingerprint: t.ModelFingerprint}
			b.settle(w)
			b.mu.Unlock()
			// Undone (#4981), the item holds what its row does: the next pass makes this one again, from the cache.
			s.saveWrites(ctx, []*inboxWrite{w}, "its translation")
		}
	}()
}

// storedMailTranslation is the translation an inbox item keeps of this mail, made by the model fingerprint names.
func (s *Service) storedMailTranslation(ctx context.Context, ref assistantwire.InboxMailRef, target, fingerprint string) (mailTranslation, bool) {
	if s.inboxReady(ctx) != nil {
		return mailTranslation{}, false
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.items {
		m := it.Mail
		if m == nil || m.AccountID != ref.AccountID || len(m.UIDs) == 0 || m.UIDs[len(m.UIDs)-1] != ref.UID || (ref.Mailbox != "" && m.Mailbox != ref.Mailbox) {
			continue
		}
		if t := it.Translations[target]; t != nil && t.Of == it.Last && t.Text != "" && t.ModelFingerprint == fingerprint {
			return mailTranslation{Subject: t.Subject, Text: t.Text, Clipped: t.Clipped, ModelFingerprint: fingerprint}, true
		}
	}
	return mailTranslation{}, false
}
