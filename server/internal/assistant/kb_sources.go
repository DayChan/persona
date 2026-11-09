package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// The sources are the hub's connectors (docs/assistant/knowledge.md "Pipeline"): the knowledge base asks the hub which of them
// to read now and reads them through it (app.call connectors.*), as data. Each connector's cursor is a file in the
// data dir, its batches are ingested as chats "<id>-<chat id>"; compressing, tagging and projects follow as for any
// chat. The cursor only moves past what was ingested, so while the assistant is away nothing is lost: the next run
// reads from where the last one stopped.

const (
	kbConnectorPoll  = 10 * time.Minute // how often a connector is asked for changes, unless its manifest says (Poll)
	kbConnectorPages = 3                // calls per connector and run
	kbConnectorLimit = 20               // conversations per call
)

// kbConnectors archives what moved in every connector the hub lists (signed in, "Send to knowledge base" on) that is due: its
// manifest's Poll, else kbConnectorPoll, since it was last asked.
func (s *Service) kbConnectors(ctx context.Context, e *kb.Engine) {
	s.kb.sourceWrites.Lock()
	defer s.kb.sourceWrites.Unlock()
	if !s.kbWritable(ctx) {
		return
	}
	var list assistantwire.Archives
	if err := s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchives, map[string]any{}, &list); err != nil {
		s.logf("kb: the connectors to archive: %v", err)
		return
	}
	for _, src := range list.Archives {
		if ctx.Err() != nil {
			return
		}
		every := kbConnectorPoll
		if src.PollMS > 0 {
			every = time.Duration(src.PollMS) * time.Millisecond
		}
		s.kb.mu.Lock()
		if s.kb.archivedAt == nil {
			s.kb.archivedAt = map[string]time.Time{}
		}
		due := time.Since(s.kb.archivedAt[src.ID]) >= every
		if due {
			s.kb.archivedAt[src.ID] = time.Now()
		}
		s.kb.mu.Unlock()
		if !due {
			continue
		}
		var a connector.Archive = busArchive{s: s, id: src.ID}
		if src.Media {
			a = busArchiveMedia{busArchive{s: s, id: src.ID}}
		}
		if _, err := archiveConnector(ctx, e, src.ID, a, src.Sources...); err != nil {
			s.logf("kb: %s: %v", src.ID, err)
		}
	}
}

// kbCollectItems records every connector's items (documents, meetings and events) and tags new ones.
func (s *Service) kbCollectItems(ctx context.Context, e *kb.Engine) {
	s.kb.sourceWrites.Lock()
	defer s.kb.sourceWrites.Unlock()
	if !s.kbWritable(ctx) {
		return
	}
	var list assistantwire.Archives
	if err := s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchives, map[string]any{}, &list); err != nil {
		s.logf("kb: the connectors' items: %v", err)
		return
	}
	if len(list.Items) == 0 {
		return
	}
	for _, id := range list.Items {
		if err := e.CollectItems(ctx, busItems{s: s, id: id}, id); err != nil {
			s.logf("kb: items: %v", err)
		}
	}
	if err := e.TagItems(ctx); err != nil && !ai.IsPaused(err) {
		s.logf("kb: tag items: %v", err)
	}
}

// busArchive is a connector's archive read through the hub (connectors.changes).
type busArchive struct {
	s  *Service
	id string
}

func (b busArchive) Changes(ctx context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	if b.s.kbReadOnly(ctx) {
		return nil, cursor, false, kb.ErrReadOnly
	}
	var out assistantwire.ChangesResult
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsChanges, map[string]any{"id": b.id, "cursor": cursor, "limit": limit}, &out)
	if err != nil {
		return nil, cursor, false, err
	}
	return out.Batches, out.Next, out.More, nil
}

// busArchiveMedia is one whose messages' attachments the hub hands over too (connectors.archive_media).
type busArchiveMedia struct{ busArchive }

func (b busArchiveMedia) ArchiveMedia(ctx context.Context, ref string) (connector.Media, error) {
	if b.s.kbReadOnly(ctx) {
		return connector.Media{}, kb.ErrReadOnly
	}
	var out struct {
		Media connector.Media `json:"media"`
	}
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchiveMedia, map[string]any{"id": b.id, "ref": ref}, &out)
	return out.Media, err
}

// busItems is a connector's documents, meetings and events through the hub (connectors.items, connectors.doc_title).
type busItems struct {
	s  *Service
	id string
}

func (b busItems) Items(ctx context.Context) ([]kbwire.Item, error) {
	if b.s.kbReadOnly(ctx) {
		return nil, kb.ErrReadOnly
	}
	var out assistantwire.KBItems
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsItems, map[string]any{"id": b.id}, &out)
	return out.Items, err
}

// DocTitle is "" when the connector cannot tell (or fails): a title is a nicety.
func (b busItems) DocTitle(ctx context.Context, url string) string {
	if b.s.kbReadOnly(ctx) {
		return ""
	}
	var out struct {
		Title string `json:"title"`
	}
	if b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsDocTitle, map[string]any{"id": b.id, "url": url}, &out) != nil {
		return ""
	}
	return out.Title
}

// kbConnectorSource names a connector's chats for Ingest; their attachments come from the connector when it can hand
// them over (connector.ArchiveMedia), else the line keeps the attachment placeholder.
type kbConnectorSource struct {
	id        string
	connector string                 // original connector, even when id is one of its archive sources
	media     connector.ArchiveMedia // nil: no attachments
}

func (s kbConnectorSource) ConnectorID() string {
	if s.connector != "" {
		return s.connector
	}
	return s.id
}

func (s kbConnectorSource) Name() string                                      { return s.id }
func (kbConnectorSource) Chats(context.Context, int) ([]kb.SourceChat, error) { return nil, nil }
func (s kbConnectorSource) Download(ctx context.Context, ref, dst string) error {
	if s.media == nil {
		return errors.New("no attachments")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	m, err := s.media.ArchiveMedia(ctx, ref)
	if err != nil {
		return err
	}
	if len(m.Data) == 0 || len(m.Data) > connector.ArchiveMediaMost {
		return fmt.Errorf("attachment of %d bytes", len(m.Data))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, m.Data, 0o600)
}
func (kbConnectorSource) Messages(context.Context, kb.SourceChat, string, int) ([]kb.SourceMessage, string, bool, error) {
	return nil, "", false, nil
}

// legacyCursors are cursors kept before connectors had cursors/<id>.json: read once when the new file is not
// there yet (mail's per-account UIDs, in the same format), left in place.
var legacyCursors = map[string]string{"mail": "mail-cursors.json"}

type connectorCursor struct {
	Cursor string `json:"cursor"`
}

// archiveConnector reads a connector's changes from its cursor, ingests them and moves the cursor on (only past
// what was ingested: a failure reads the same again next time).
//
// A batch filed under another name (ChatBatch.Source) goes there only when it is one of sources (the connector's
// manifest's ArchiveSources, minus any another connector has as its id): else it is dropped and logged.
func archiveConnector(ctx context.Context, e *kb.Engine, id string, a connector.Archive, sources ...string) (int, error) {
	if e.Store.ReadOnly() {
		return 0, kb.ErrReadOnly
	}
	media, _ := a.(connector.ArchiveMedia)
	src := kbConnectorSource{id: id, connector: id, media: media}
	file := filepath.Join(e.Store.Dir, "cursors", id+".json")
	var cur connectorCursor
	if raw, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(raw, &cur)
	} else if old, ok := legacyCursors[id]; ok {
		if raw, err := os.ReadFile(filepath.Join(e.Store.Dir, old)); err == nil {
			cur.Cursor = string(raw)
		}
	}
	total := 0
	for page := 0; page < kbConnectorPages; page++ {
		batches, next, more, err := a.Changes(ctx, cur.Cursor, kbConnectorLimit)
		if err != nil {
			return total, err
		}
		for _, b := range batches {
			s := src
			if b.Source != "" && b.Source != id {
				if !slices.Contains(sources, b.Source) {
					if e.Logf != nil {
						e.Logf("kb: %s: a batch under %q, not one of its archive sources (dropped)", id, b.Source)
					}
					continue
				}
				s.id = b.Source
			}
			n, err := e.Ingest(ctx, s, b.Chat, b.Messages)
			if err != nil {
				return total, err
			}
			total += n
		}
		cur.Cursor = next
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return total, err
		}
		raw, _ := json.Marshal(cur)
		if err := managerconf.WriteFileAtomic(file, raw, 0o600); err != nil {
			return total, err
		}
		if !more {
			break
		}
	}
	return total, nil
}
