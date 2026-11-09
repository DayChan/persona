package extmgr

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
)

// A connector's program writes a feed's public posts into the knowledge base's post archive and reads which posts'
// comments to read again (connector.KnowledgeBase, docs/assistant/knowledge.md "External posts"). The archive is the knowledge
// base's, the assistant's: the hub checks the connector is enabled with "Add to knowledge base" on and passes each call on
// with the connector's names (kb.posts_*, kb.archived_chats). While the assistant is not connected a call waits for it
// a while (kbPostsWait), then fails (assistant_offline): the program's round ends there and it reads the same again
// next round.

// kbFor is the knowledge base as connector id's program sees it.
func (h *Manager) kbFor(id string) connector.KnowledgeBase { return kbPosts{h: h, id: id} }

type kbPosts struct {
	h  *Manager
	id string
}

// kbPostsWait is how long a program's call waits for the assistant to connect (the programs start with the hub,
// before the assistant is up; an assistant restarting). Tests shorten it.
var kbPostsWait = 30 * time.Second

// call passes one of the program's calls on, when this connector may write to the knowledge base now.
func (k kbPosts) call(ctx context.Context, method string, p assistantwire.KBPosts, out any) error {
	k.h.kbPostsMu.RLock()
	defer k.h.kbPostsMu.RUnlock()
	if k.h.kbReadOnly() {
		return kb.ErrReadOnly
	}
	if k.h.kbOff() {
		return errKBOff
	}
	if k.h.connectors != nil && (!k.h.connectors.Enabled(k.id) || !k.h.connectors.Feeds(k.id, connector.FeedArchive)) {
		return errArchiveOff
	}
	p.Connector, p.Sources = k.id, k.sources()
	return k.h.kbAskWaiting(ctx, kbPostsWait, method, p, out)
}

// sources are the names the connector's conversations are filed under: its id, then its manifest's ArchiveSources.
func (k kbPosts) sources() []string {
	out := []string{k.id}
	if c := k.h.connectorByID(k.id); c != nil {
		out = append(out, c.Manifest().ArchiveSources...)
	}
	return out
}

func (k kbPosts) PutPosts(ctx context.Context, posts []connector.PostPut) ([]string, error) {
	if len(posts) > connector.PostsPerPut {
		return nil, errors.New("too many posts in one call")
	}
	var out struct {
		Statuses []string `json:"statuses"`
	}
	err := k.call(ctx, assistantwire.MethodKBPostsPut, assistantwire.KBPosts{Posts: posts}, &out)
	return out.Statuses, err
}

func (k kbPosts) PutComments(ctx context.Context, id string, count int, comments []connector.PostComment) error {
	if comments == nil {
		comments = []connector.PostComment{}
	}
	return k.call(ctx, assistantwire.MethodKBPostsComments, assistantwire.KBPosts{ID: id, Count: count, Comments: comments}, nil)
}

func (k kbPosts) PendingPosts(ctx context.Context, limit int) ([]connector.PendingPost, error) {
	var out struct {
		Posts []connector.PendingPost `json:"posts"`
	}
	err := k.call(ctx, assistantwire.MethodKBPostsPending, assistantwire.KBPosts{Limit: limit}, &out)
	return out.Posts, err
}

func (k kbPosts) ArchivedChats(ctx context.Context, source string) ([]connector.ArchivedChat, error) {
	var out struct {
		Chats []connector.ArchivedChat `json:"chats"`
	}
	err := k.call(ctx, assistantwire.MethodKBArchivedChats, assistantwire.KBPosts{Source: source}, &out)
	return out.Chats, err
}
