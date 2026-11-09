package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// Muting (docs/assistant/inbox.md "Mute", docs/extensions/connectors.md "2 · 5"): the user mutes a repository, a chat or a sender from an
// item's menu; the inbox keeps the mute (settings "inbox_mutes", only it reads and writes them) and, the same for every source, files
// the open items it covers as done and the ones that come in later as soon as they do. What an item can be muted by (its `mutes`) is a
// connector's tags (InboxThing.Tags) or, for Feishu and mail, read off the item.

// inboxMutesKey is where the mutes are kept.
const inboxMutesKey = "inbox_mutes"

// MuteOption is one thing an item can be muted by: a tag (connector.TagRepo, TagChat, TagSender) and its value,
// with a name for people.
type MuteOption struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
	Name  string `json:"name"`
}

// InboxMute is a mute the user set: nothing of Source tagged Tag = Value stays in the inbox.
type InboxMute struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	SourceName string    `json:"source_name,omitempty"` // listed only
	Tag        string    `json:"tag"`
	Value      string    `json:"value"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
}

var mutableTags = []string{connector.TagRepo, connector.TagChat, connector.TagSender}

// mutesOf is what it can be muted by.
func mutesOf(it *InboxItem) []MuteOption {
	var out []MuteOption
	add := func(tag, value, name string) {
		if value == "" || slices.ContainsFunc(out, func(m MuteOption) bool { return m.Tag == tag }) {
			return
		}
		if name == "" {
			name = value
		}
		out = append(out, MuteOption{Tag: tag, Value: value, Name: name})
	}
	switch {
	case it.Connector != nil:
		for _, t := range it.Connector.Tags {
			if slices.Contains(mutableTags, t.Key) {
				add(t.Key, t.Value, t.Name)
			}
		}
	case it.Lark != nil && it.Doc == nil && it.Moment == nil:
		add(connector.TagChat, it.Lark.FeedID, it.Title)
		if it.Lark.IsGroup {
			add(connector.TagSender, it.Lark.SenderID, it.Sender)
		}
	case it.Mail != nil:
		add(connector.TagSender, strings.ToLower(it.Mail.From.Email), it.Mail.From.Name)
	}
	return out
}

// mutedBy is the mute that covers it, nil when none does.
func mutedBy(mutes []InboxMute, it *InboxItem) *InboxMute {
	for _, o := range mutesOf(it) {
		for i := range mutes {
			if m := &mutes[i]; m.Source == it.Source && m.Tag == o.Tag && m.Value == o.Value {
				return m
			}
		}
	}
	return nil
}

// applyMutes files the open items the mutes cover as done (read), saying which mute; under b.mu. The writes are
// settled (inbox_rollback.go): a filing whose row fails reopens the item, and the next round files it again.
func (b *inboxBook) applyMutes(now time.Time) []*inboxWrite {
	if len(b.mutes) == 0 {
		return nil
	}
	var changed []*inboxWrite
	for _, it := range b.items {
		if !it.open() {
			continue
		}
		if m := mutedBy(b.mutes, it); m != nil {
			w := beginWrite(it)
			b.fileMuted(it, now, m.Name)
			b.settle(w)
			changed = append(changed, w)
		}
	}
	return changed
}

// fileMuted keeps done_by=read for compatibility, but records the mute as the cause. Under b.mu.
func (b *inboxBook) fileMuted(it *InboxItem, at time.Time, name string) {
	it.done(at, "read")
	b.chainAdd(it, InboxChainEntry{Kind: ChainDone, At: at.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.done_muted")})
	it.Reason = b.mutedReason(name)
}

func itemsOf(ws []*inboxWrite) []*InboxItem {
	out := make([]*InboxItem, len(ws))
	for i, w := range ws {
		out[i] = w.it
	}
	return out
}

// loadInboxMutes reads the mutes kept, joining any added before; false when the database could not be read.
func (s *Service) loadInboxMutes(ctx context.Context) bool {
	st := s.store()
	if st == nil {
		return true
	}
	b := &s.inbox
	b.mutesMu.Lock()
	defer b.mutesMu.Unlock()
	var kept []InboxMute
	if _, err := st.Setting(ctx, inboxMutesKey, &kept); err != nil {
		s.logf("inbox: mutes: %v", err)
		return false
	}
	b.mu.Lock()
	for _, m := range b.mutes {
		if !slices.ContainsFunc(kept, func(k InboxMute) bool { return k.Source == m.Source && k.Tag == m.Tag && k.Value == m.Value }) {
			kept = append(kept, m)
		}
	}
	b.mutes = kept
	b.mu.Unlock()
	return true
}

// putMutes keeps the given mutes snapshot; the caller holds mutesMu. A nil store means mutes are memory-only.
func (s *Service) putMutes(ctx context.Context, mutes []InboxMute) error {
	st := s.store()
	if st == nil {
		return nil
	}
	return st.PutSetting(ctx, inboxMutesKey, mutes)
}

// addMute adds a mute (the same one twice is one) and files what it covers; it returns the mute and how many
// items it filed.
func (s *Service) addMute(ctx context.Context, m InboxMute) (InboxMute, int, error) {
	b := &s.inbox
	b.mutesMu.Lock()
	defer b.mutesMu.Unlock()
	b.mu.Lock()
	i := slices.IndexFunc(b.mutes, func(x InboxMute) bool { return x.Source == m.Source && x.Tag == m.Tag && x.Value == m.Value })
	if i >= 0 {
		m = b.mutes[i]
	} else {
		if m.ID == "" {
			m.ID = fmt.Sprintf("m%d", time.Now().UnixNano())
		}
		if m.CreatedAt.IsZero() {
			m.CreatedAt = time.Now().UTC()
		}
	}
	// Persist a new mute before it enters memory: a failed save leaves the mute and the items it would file
	// untouched, so the failure shows to the user and a retry is idempotent (a mute already here files as before).
	if i < 0 {
		next := append(slices.Clone(b.mutes), m)
		b.mu.Unlock()
		if err := s.putMutes(ctx, next); err != nil {
			return m, 0, err
		}
		b.mu.Lock()
		b.mutes = append(b.mutes, m)
	}
	changed := b.applyMutes(time.Now())
	b.mu.Unlock()
	// The mute stays kept; only the items whose rows failed reopen, so the answer is an error and a retry (the same
	// mute, idempotent) files them again.
	if failed := s.saveWrites(ctx, changed, "its filing by a mute"); len(failed) > 0 {
		return m, 0, errInboxNotPersisted
	}
	return m, len(changed), nil
}

// removeMutes takes the mutes drop says yes to away; what they covered stays done. The mutes stay live until the
// removal is persisted (a failed removal changes nothing); unmuting never reopens what a mute filed anyway.
func (s *Service) removeMutes(ctx context.Context, drop func(InboxMute) bool) error {
	b := &s.inbox
	b.mutesMu.Lock()
	defer b.mutesMu.Unlock()
	b.mu.Lock()
	next := slices.DeleteFunc(slices.Clone(b.mutes), drop)
	removed := len(next) != len(b.mutes)
	b.mu.Unlock()
	// Persist the smaller set first: on failure the mutes in memory stay, so a retry removes them cleanly.
	if !removed {
		return nil
	}
	if err := s.putMutes(ctx, next); err != nil {
		return err
	}
	b.mu.Lock()
	b.mutes = next
	b.mu.Unlock()
	return nil
}

// ---- a source's muted repositories ----
//
// GitHub's settings page (and older apps) keep muted repositories in its options ("muted_repos"). They are the
// mutes of source github, tag repo: the hub lists them in its options from here, sets them through here, and moves
// the ones kept in its options before over once (InboxRepoMutes).

// repoMute is the mute of a source's repository.
func repoMute(source, repo string) InboxMute {
	return InboxMute{Source: source, Tag: connector.TagRepo, Value: strings.ToLower(repo), Name: repo}
}

// InboxRepoMutes is inbox.repo_mutes: the source's muted repositories, after making p.Repos its repository mutes (the
// others unmuted) or adding them (p.Add) when p.Set.
func (s *Service) InboxRepoMutes(ctx context.Context, p assistantwire.InboxRepoMutes) ([]string, error) {
	if err := s.inboxReady(ctx); err != nil { // the mutes kept are not known: neither listed nor set
		return nil, err
	}
	if p.Set {
		keep := map[string]bool{}
		for _, r := range p.Repos {
			if !strings.Contains(r, "/") {
				return nil, i18n.ErrEnglish("inbox-errors.muted_repository", i18n.Args{"repo": strconv.Quote(r)})
			}
			keep[strings.ToLower(r)] = true
		}
		if !p.Add {
			if err := s.removeMutes(ctx, func(m InboxMute) bool { return m.Source == p.Source && m.Tag == connector.TagRepo && !keep[m.Value] }); err != nil {
				return nil, err
			}
		}
		for _, r := range p.Repos {
			if _, _, err := s.addMute(ctx, repoMute(p.Source, r)); err != nil {
				return nil, err
			}
		}
	}
	repos := []string{}
	for _, m := range s.listedMutes() {
		if m.Source == p.Source && m.Tag == connector.TagRepo {
			repos = append(repos, m.Name)
		}
	}
	return repos, nil
}

// listedMutes are the mutes with their source's name.
func (s *Service) listedMutes() []InboxMute {
	b := &s.inbox
	b.mu.Lock()
	mutes := slices.Clone(b.mutes)
	b.mu.Unlock()
	for i := range mutes {
		mutes[i].SourceName = b.sourceName(mutes[i].Source)
	}
	return mutes
}

var errNoMuteOption = i18n.ErrEnglish("inbox-errors.cannot_mute")

func (s *Service) inboxMuteRoutes() {
	s.handle("GET /api/v1/inbox/mutes", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		apierr.WriteJSON(w, map[string]any{"mutes": s.listedMutes()})
	})
	// Mute what an item has under a tag ({"item_id", "tag"}): the value is read off the item.
	s.handle("POST /api/v1/inbox/mutes", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			ItemID string `json:"item_id"`
			Tag    string `json:"tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		b := &s.inbox
		b.mu.Lock()
		it, ok := b.items[in.ItemID]
		var source string
		var opt *MuteOption
		if ok {
			source = it.Source
			for _, o := range mutesOf(it) {
				if o.Tag == in.Tag {
					opt = &o
					break
				}
			}
		}
		b.mu.Unlock()
		if !ok {
			apierr.Write(w, http.StatusNotFound, errNoInboxItem)
			return
		}
		if opt == nil {
			apierr.Write(w, http.StatusBadRequest, errNoMuteOption)
			return
		}
		m, settled, err := s.addMute(r.Context(), InboxMute{Source: source, Tag: opt.Tag, Value: opt.Value, Name: opt.Name})
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		m.SourceName = b.sourceName(m.Source)
		apierr.WriteJSON(w, map[string]any{"mute": m, "settled": settled})
	})
	s.handle("DELETE /api/v1/inbox/mutes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		id := r.PathValue("id")
		if err := s.removeMutes(r.Context(), func(m InboxMute) bool { return m.ID == id }); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
