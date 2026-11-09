package assistant

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// A connector's program writes a feed's public posts into the knowledge base's post archive (moments/ in the data
// dir, the name it had when the hub's own crawler was the only writer) and reads which posts' comments to read again:
// connector.KnowledgeBase, docs/assistant/knowledge.md "External posts". The program calls the hub; the hub checks the connector may
// write now ("Send to knowledge base") and passes the call on with the connector's names (kb.posts_*, kb.archived_chats). The archive
// is the knowledge base's to keep: the index, pruning past the text retention, and finding each project's posts
// (kb.LinkMoments) stay here whoever writes the posts.

var errKBNone = i18n.Err("assistant-kb_posts.none")

// kbPostsEngine is the knowledge base, for a connector's posts (kb_unavailable while it is not open: the program
// reads the same again next round).
func (s *Service) kbPostsEngine(ctx context.Context) (*kb.Engine, error) {
	if s.kbReadOnly(ctx) {
		return nil, kbError(kb.ErrReadOnly)
	}
	e, err := s.kbGet(ctx)
	if errors.Is(err, errKBOff) {
		return nil, errKBNone
	}
	return e, kbError(err)
}

func (s *Service) kbPutPosts(ctx context.Context, p assistantwire.KBPosts) ([]string, error) {
	s.kb.postWrites.Lock()
	defer s.kb.postWrites.Unlock()
	e, err := s.kbPostsEngine(ctx)
	if err != nil {
		return nil, err
	}
	if len(p.Posts) > connector.PostsPerPut {
		return nil, errors.New("too many posts in one call")
	}
	st := e.Store
	now := time.Now().UTC()
	out := make([]string, len(p.Posts))
	for i, in := range p.Posts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		post := in.Post
		if strings.TrimSpace(post.ID) == "" || len(in.Comments) > connector.PostCommentsMost {
			out[i] = connector.PostRefused
			continue
		}
		old, ok := st.MomentPost(post.ID)
		if ok && old.Source != "" && old.Source != p.Connector {
			out[i] = connector.PostRefused
			continue
		}
		m := &kb.MomentPost{ID: post.ID, Author: post.Author, AuthorID: post.AuthorID, Anonymous: post.Anonymous, Categories: post.Categories,
			Text: clipBytes(post.Text, connector.PostTextMost), Images: post.Images, Reactions: post.Reactions, CommentCount: post.CommentCount,
			At: post.At.UTC(), SeenAt: now, CommentsOf: -1, Mine: post.Mine, Source: p.Connector, Feed: post.Feed}
		// It concerns the user when the connector archives it as a conversation too (one of its names, the post's id).
		m.Concerns = slices.ContainsFunc(p.Sources, func(src string) bool {
			_, ok := st.Chat(kb.ChatID(src, post.ID))
			return ok
		})
		out[i] = connector.PostNew
		if ok {
			m.CommentsOf, m.CommentsAt = old.CommentsOf, old.CommentsAt
			m.Concerns = m.Concerns || old.Concerns
			out[i] = connector.PostSame
			if old.CommentCount != post.CommentCount {
				out[i] = connector.PostChanged
			}
		}
		comments := []kb.MomentComment(nil)
		switch {
		case in.Comments != nil:
			comments = make([]kb.MomentComment, 0, len(in.Comments))
			for _, c := range in.Comments {
				comments = append(comments, kb.MomentComment{Author: c.Author, ReplyTo: c.ReplyTo, Text: clipBytes(c.Text, connector.PostTextMost), At: c.At.UTC()})
			}
			m.CommentsOf, m.CommentsAt = post.CommentCount, now
		case post.CommentCount == 0 && m.CommentsOf != 0: // nothing to read: settled at once
			comments = []kb.MomentComment{}
			m.CommentsOf, m.CommentsAt = 0, now
		}
		if err := st.PutMomentPost(m, comments); err != nil {
			return nil, err
		}
	}
	s.kb.postsDirty.Store(true)
	return out, nil
}

func (s *Service) kbPutComments(ctx context.Context, p assistantwire.KBPosts) error {
	s.kb.postWrites.Lock()
	defer s.kb.postWrites.Unlock()
	e, err := s.kbPostsEngine(ctx)
	if err != nil {
		return err
	}
	if len(p.Comments) > connector.PostCommentsMost {
		return errors.New("too many comments")
	}
	post, ok := e.Store.MomentPost(p.ID)
	if !ok || post.Source != "" && post.Source != p.Connector {
		return errors.New("no such post of this connector's in the archive")
	}
	comments := make([]kb.MomentComment, 0, len(p.Comments))
	for _, c := range p.Comments {
		comments = append(comments, kb.MomentComment{Author: c.Author, ReplyTo: c.ReplyTo, Text: clipBytes(c.Text, connector.PostTextMost), At: c.At.UTC()})
	}
	post.Source, post.CommentCount, post.CommentsOf, post.CommentsAt = p.Connector, p.Count, p.Count, time.Now().UTC()
	if err := e.Store.PutMomentPost(post, comments); err != nil {
		return err
	}
	s.kb.postsDirty.Store(true)
	return nil
}

func (s *Service) kbPendingPosts(ctx context.Context, p assistantwire.KBPosts) ([]connector.PendingPost, error) {
	e, err := s.kbPostsEngine(ctx)
	if err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 || limit > connector.PendingPostsMost {
		limit = connector.PendingPostsMost
	}
	cutoff := time.Now().Add(-kbTextRetention(e))
	var todo []*kb.MomentPost
	for _, post := range e.Store.MomentPosts() {
		if (post.Source == "" || post.Source == p.Connector) && !post.At.Before(cutoff) && post.CommentCount > 0 && post.CommentsOf != post.CommentCount {
			todo = append(todo, post)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].At.After(todo[j].At) })
	out := []connector.PendingPost{}
	for _, post := range todo {
		if len(out) >= limit {
			break
		}
		out = append(out, connector.PendingPost{ID: post.ID, CommentCount: post.CommentCount, At: post.At})
	}
	return out, nil
}

func (s *Service) kbArchivedChats(ctx context.Context, p assistantwire.KBPosts) ([]connector.ArchivedChat, error) {
	e, err := s.kbPostsEngine(ctx)
	if err != nil {
		return nil, err
	}
	source := p.Source
	if source == "" {
		source = p.Connector
	}
	if source != p.Connector && !slices.Contains(p.Sources, source) {
		return nil, errors.New("not one of the connector's names: " + source)
	}
	out := []connector.ArchivedChat{}
	for _, c := range e.Store.Chats() {
		if c.Source == source {
			out = append(out, connector.ArchivedChat{ID: c.SourceID, Name: c.Name, Cursor: c.Cursor, LastAt: c.LastAt, Messages: c.Messages})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// kbPostsUpkeep rewrites the post archive's index after posts came in, and prunes it past the text retention once
// a day (kbLoop; the hub's own crawler did both itself).
func (s *Service) kbPostsUpkeep(e *kb.Engine, daily bool) {
	s.kb.postWrites.Lock()
	defer s.kb.postWrites.Unlock()
	if daily {
		if n := e.Store.PruneMoments(time.Now().Add(-kbTextRetention(e))); n > 0 {
			s.logf("kb: pruned %d post(s) past the text retention", n)
			s.kb.postsDirty.Store(true)
		}
	}
	if s.kb.postsDirty.Swap(false) {
		if _, err := e.Store.WriteMomentIndex(); err != nil {
			s.logf("kb: posts index: %v", err)
		}
	}
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
