package assistant

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
)

const feedView = "view"

// viewStore is kept separate so older test stores fail closed instead of creating non-durable cards.
type viewStore interface {
	CreateAssistantView(context.Context, db.AssistantViewRow, db.FeedRow) error
	AssistantView(context.Context, string, string) (*db.AssistantViewRow, error)
	AssistantViews(context.Context, string) ([]db.AssistantViewRow, error)
	UpdateAssistantView(context.Context, string, string, int64, json.RawMessage, time.Time) (*db.AssistantViewRow, error)
}

type viewNotifications struct {
	mu      sync.Mutex
	pending map[string]wire.AssistantView
}

func (s *Service) viewsStore() (viewStore, error) {
	st, ok := s.store().(viewStore)
	if !ok {
		return nil, errors.New("live views require a durable database")
	}
	return st, nil
}

func storedView(row db.AssistantViewRow) (wire.AssistantView, error) {
	var v wire.View
	if err := json.Unmarshal(row.View, &v); err != nil {
		return wire.AssistantView{}, err
	}
	return wire.AssistantView{ID: row.ID, Conversation: row.Conversation, Version: row.Version, View: v, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (s *Service) toolView(ctx context.Context, machine, conversation, name string, raw json.RawMessage) (string, error) {
	if !s.hello(ctx).PersonaView {
		return "", errors.New("assistant views are disabled")
	}
	if machine == "" || conversation == "" {
		return "", errors.New("forbidden: view tools require the calling assistant conversation")
	}
	if session, ok := s.session(ctx, machine, conversation); !ok || !session.Assistant {
		return "", errors.New("forbidden: only assistant conversations may create or update views")
	}
	st, err := s.viewsStore()
	if err != nil {
		return "", err
	}
	var in struct {
		ID      string          `json:"id"`
		View    json.RawMessage `json:"view"`
		Version int64           `json:"version"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	if name == "view_show" {
		v, err := wire.ParseAssistantView(in.View)
		if err != nil {
			return "", fmt.Errorf("invalid view: %w", err)
		}
		view, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		at := time.Now().UTC()
		row := db.AssistantViewRow{ID: "view_" + randomHex(8), Conversation: conversation, MachineID: machine, View: view, Version: 1, CreatedAt: at, UpdatedAt: at}
		out, err := storedView(row)
		if err != nil {
			return "", err
		}
		body, err := json.Marshal(map[string]any{"view": out})
		if err != nil {
			return "", err
		}
		feed := db.FeedRow{ID: "feed_" + randomHex(8), Conversation: conversation, At: at, Kind: feedView, Ref: row.ID, Body: body}
		if err := st.CreateAssistantView(ctx, row, feed); err != nil {
			return "", err
		}
		s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": conversation, "item": FeedItem{ID: feed.ID, Conversation: conversation, At: at, Kind: feedView, Ref: row.ID, Body: body}})
		s.viewChanged(out)
		return viewToolJSON(map[string]any{"id": row.ID, "version": row.Version})
	}
	if in.ID == "" {
		return "", errors.New("id is required")
	}
	row, err := st.AssistantView(ctx, conversation, in.ID)
	if errors.Is(err, db.ErrAssistantViewNotFound) {
		return "", errors.New("forbidden: no view with this id in the calling conversation")
	}
	if err != nil {
		return "", err
	}
	if row.MachineID != machine {
		return "", errors.New("forbidden: this card was created by an assistant conversation on another machine")
	}
	if name == "view_get" {
		out, err := storedView(*row)
		if err != nil {
			return "", err
		}
		actions := []wire.ViewAction{}
		if actionStore, ok := st.(viewActionStore); ok {
			a, err := actionStore.AssistantViewAction(ctx, conversation, in.ID)
			if err != nil {
				return "", err
			}
			if a != nil {
				actions = append(actions, wireViewAction(*a))
			}
		}
		return viewToolJSON(struct {
			wire.AssistantView
			Actions []wire.ViewAction `json:"actions"`
		}{AssistantView: out, Actions: actions})
	}
	v, err := wire.ParseAssistantView(in.View)
	if err != nil {
		return "", fmt.Errorf("invalid view: %w", err)
	}
	view, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	version := in.Version
	if version == 0 {
		version = row.Version
	}
	updated, err := st.UpdateAssistantView(ctx, conversation, in.ID, version, view, time.Now().UTC())
	if errors.Is(err, db.ErrAssistantViewVersion) {
		return "", errors.New("version conflict: call view_get, then update from its version")
	}
	if err != nil {
		return "", err
	}
	out, err := storedView(*updated)
	if err != nil {
		return "", err
	}
	s.viewChanged(out)
	return viewToolJSON(map[string]any{"version": out.Version})
}

func viewToolJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}

// Persist first; only notifications are coalesced. A restart loses no card contents or feed positions.
func (s *Service) viewChanged(v wire.AssistantView) {
	b := &s.viewEvents
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]wire.AssistantView{}
	}
	old, exists := b.pending[v.ID]
	if !exists || old.Version < v.Version {
		b.pending[v.ID] = v
	}
	if exists {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	time.AfterFunc(time.Second, func() {
		b.mu.Lock()
		latest := b.pending[v.ID]
		delete(b.pending, v.ID)
		b.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.event(ctx, map[string]any{"type": "assistant_view", "conversation": latest.Conversation, "view": latest})
	})
}

// viewKeyStore is optional: a store without it reads and decodes every card of the conversation on each feed GET.
type viewKeyStore interface {
	AssistantViewKeys(context.Context, string) ([]db.AssistantViewKey, error)
	AssistantViewsByID(context.Context, string, []string) ([]db.AssistantViewKeyed, error)
}

// viewMemoBytes caps the encoded cards kept for feed snapshots; the least recently read conversations go first.
const viewMemoBytes = 4 << 20

// viewMemo keeps each live card's encoded JSON for the feed's views snapshot, so a page reads and decodes only the cards
// that changed. It is only a cache: every request first reads the cards' (version, xmin) from the database and uses an
// encoding only when both still match, so a card written by another process (the other blue-green slot) is read again.
// It holds encoded bytes, never decoded views.
type viewMemo struct {
	mu    sync.Mutex
	most  int // 0: viewMemoBytes
	bytes int
	lru   list.List // *viewMemoConversation, most recently read first
	index map[string]*list.Element
}

type viewMemoConversation struct {
	conversation string
	bytes        int
	cards        map[string]viewMemoCard
}

type viewMemoCard struct {
	version int64
	xmin    uint32
	raw     json.RawMessage
}

func (c viewMemoCard) matches(k db.AssistantViewKey) bool {
	return c.version == k.Version && c.xmin == k.Xmin
}

// reuse puts the encodings that still match keys into out and returns the ids to read again.
func (m *viewMemo) reuse(conversation string, keys []db.AssistantViewKey, out map[string]json.RawMessage) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cards map[string]viewMemoCard
	if e := m.index[conversation]; e != nil {
		cards = e.Value.(*viewMemoConversation).cards
	}
	var missing []string
	for _, k := range keys {
		if c, ok := cards[k.ID]; ok && c.matches(k) {
			out[k.ID] = c.raw
			continue
		}
		missing = append(missing, k.ID)
	}
	return missing
}

// keep records the cards just read, forgets the ones keys no longer list, and evicts conversations over the cap.
func (m *viewMemo) keep(conversation string, keys []db.AssistantViewKey, read map[string]viewMemoCard) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index == nil {
		m.index = map[string]*list.Element{}
	}
	e := m.index[conversation]
	var old map[string]viewMemoCard
	if e != nil {
		m.lru.MoveToFront(e)
		old = e.Value.(*viewMemoConversation).cards
		if len(read) == 0 && len(old) == len(keys) && allMatch(old, keys) {
			return
		}
	}
	cards := make(map[string]viewMemoCard, len(keys))
	size := 0
	for _, k := range keys {
		c, ok := read[k.ID]
		if !ok {
			// Not read this time: keep it only if it still is what the database listed.
			if c, ok = old[k.ID]; !ok || !c.matches(k) {
				continue
			}
		}
		cards[k.ID] = c
		size += len(k.ID) + len(c.raw)
	}
	most := m.most
	if most == 0 {
		most = viewMemoBytes
	}
	if e != nil {
		m.drop(e)
	}
	if len(cards) == 0 || size > most {
		return
	}
	m.index[conversation] = m.lru.PushFront(&viewMemoConversation{conversation: conversation, bytes: size, cards: cards})
	m.bytes += size
	for m.bytes > most {
		m.drop(m.lru.Back())
	}
}

func (m *viewMemo) drop(e *list.Element) {
	c := m.lru.Remove(e).(*viewMemoConversation)
	delete(m.index, c.conversation)
	m.bytes -= c.bytes
}

func allMatch(cards map[string]viewMemoCard, keys []db.AssistantViewKey) bool {
	for _, k := range keys {
		if c, ok := cards[k.ID]; !ok || !c.matches(k) {
			return false
		}
	}
	return true
}

// encodedView is the card as the feed snapshot carries it.
func encodedView(row db.AssistantViewRow) (json.RawMessage, error) {
	v, err := storedView(row)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// conversationViews is the feed's views snapshot: every live card of the conversation by id, encoded as
// wire.AssistantView. A card that does not decode fails the request and is never cached.
func (s *Service) conversationViews(ctx context.Context, conversation string) (map[string]json.RawMessage, error) {
	st, err := s.viewsStore()
	if err != nil {
		return map[string]json.RawMessage{}, nil
	}
	keyed, ok := st.(viewKeyStore)
	if !ok {
		rows, err := st.AssistantViews(ctx, conversation)
		if err != nil {
			return nil, err
		}
		out := make(map[string]json.RawMessage, len(rows))
		for _, row := range rows {
			if out[row.ID], err = encodedView(row); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	keys, err := keyed.AssistantViewKeys(ctx, conversation)
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(keys))
	missing := s.viewMemo.reuse(conversation, keys, out)
	var read map[string]viewMemoCard
	if len(missing) > 0 {
		rows, err := keyed.AssistantViewsByID(ctx, conversation, missing)
		if err != nil {
			return nil, err
		}
		read = make(map[string]viewMemoCard, len(rows))
		for _, row := range rows {
			raw, err := encodedView(row.AssistantViewRow)
			if err != nil {
				return nil, err
			}
			out[row.ID] = raw
			read[row.ID] = viewMemoCard{version: row.Version, xmin: row.Xmin, raw: raw}
		}
	}
	s.viewMemo.keep(conversation, keys, read)
	return out, nil
}
