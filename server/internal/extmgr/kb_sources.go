package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
)

// The hub keeps the knowledge base's sources (docs/assistant/knowledge.md "Who owns what"): the knowledge base, the assistant's,
// asks which connectors to read now and reads them through the hub (app.call connectors.*), data in, data out. The
// cursors are the knowledge base's: what it did not ingest it reads again next time, so nothing waits here.

// kbSourcesCall answers the connectors' app.call methods for the knowledge base; false for the others.
func (h *Manager) kbSourcesCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	switch p.Method {
	case assistantwire.ConnectorsArchives, assistantwire.ConnectorsChanges, assistantwire.ConnectorsArchiveMedia,
		assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle, assistantwire.ConnectorsOwner,
		assistantwire.ConnectorsHistoryChats, assistantwire.ConnectorsHistory, assistantwire.ConnectorsHistoryAnchors:
	default:
		return nil, false, nil
	}
	if p.AppID != assistantwire.AppConnectors {
		return nil, false, nil
	}
	if h.kbReadOnly() {
		return nil, true, kb.ErrReadOnly
	}
	if h.kbOff() { // manager.json kb.enabled false: nothing goes to a knowledge base
		if p.Method == assistantwire.ConnectorsArchives {
			raw, err := json.Marshal(assistantwire.Archives{Archives: []assistantwire.ArchiveSource{}, Items: []string{}})
			return raw, true, err
		}
		return nil, true, errKBOff
	}
	switch p.Method {
	case assistantwire.ConnectorsHistoryChats, assistantwire.ConnectorsHistory, assistantwire.ConnectorsHistoryAnchors:
		return h.kbHistoryCall(ctx, p)
	}
	var a struct {
		ID     string `json:"id"`
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
		Ref    string `json:"ref"`
		URL    string `json:"url"`
	}
	if len(p.Params) > 0 {
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, true, err
		}
	}
	var out any
	var err error
	switch p.Method {
	case assistantwire.ConnectorsArchives:
		out = h.kbArchives(ctx)
	case assistantwire.ConnectorsOwner:
		out, err = h.kbConnectorOwner(ctx)
	case assistantwire.ConnectorsChanges:
		var arch connector.Archive
		if arch, err = h.kbArchive(a.ID); err == nil {
			var res assistantwire.ChangesResult
			res.Batches, res.Next, res.More, err = arch.Changes(ctx, a.Cursor, a.Limit)
			if res.Batches == nil {
				res.Batches = []connector.ChatBatch{}
			}
			out = res
		}
	case assistantwire.ConnectorsArchiveMedia:
		var arch connector.Archive
		if arch, err = h.kbArchive(a.ID); err == nil {
			media, ok := arch.(connector.ArchiveMedia)
			if !ok {
				return nil, true, errors.New("no attachments")
			}
			mctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			var m connector.Media
			if m, err = media.ArchiveMedia(mctx, a.Ref); err == nil && len(m.Data) > h.kbMediaMost() {
				err = fmt.Errorf("attachment of %d bytes", len(m.Data))
			}
			out = map[string]any{"media": m}
		}
	case assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle:
		c := h.connectorByID(a.ID)
		is, ok := connector.As[connector.Items](c)
		if c == nil || !ok {
			return nil, true, fmt.Errorf("connector %q has no items", a.ID)
		}
		if !h.connectors.Enabled(a.ID) || !h.connectors.Feeds(a.ID, connector.FeedArchive) {
			return nil, true, errArchiveOff
		}
		if p.Method == assistantwire.ConnectorsDocTitle {
			out = map[string]any{"title": is.ItemSource().DocTitle(ctx, a.URL)}
			break
		}
		var items []kbwire.Item
		items, err = is.ItemSource().Items(ctx)
		if items == nil {
			items = []kbwire.Item{}
		}
		out = assistantwire.KBItems{Items: items}
	}
	if err != nil {
		return nil, true, err
	}
	raw, err := json.Marshal(out)
	return raw, true, err
}

// kbArchives are the connectors whose conversations, documents, meetings and events the knowledge base reads now:
// signed in, enabled and with "Add to knowledge base" on. With either switch off, the connector is not asked at all.
func (h *Manager) kbArchives(ctx context.Context) assistantwire.Archives {
	out := assistantwire.Archives{Archives: []assistantwire.ArchiveSource{}, Items: []string{}}
	if h.connectors == nil {
		return out
	}
	for _, c := range h.connectors.All() {
		a, archive := connector.As[connector.Archive](c)
		_, items := connector.As[connector.Items](c)
		if (!archive && !items) || !h.connectors.Feeds(c.ID(), connector.FeedArchive) || connector.StatusOf(ctx, c).State != connector.StateOK || ctx.Err() != nil {
			continue
		}
		if items {
			out.Items = append(out.Items, c.ID())
		}
		if !archive {
			continue
		}
		_, media := a.(connector.ArchiveMedia)
		_, history := connector.As[connector.History](c)
		_, anchors := connector.As[connector.HistoryAnchors](c)
		out.Archives = append(out.Archives, assistantwire.ArchiveSource{ID: c.ID(), PollMS: c.Manifest().Poll.Milliseconds(),
			Sources: h.archiveSources(c), Media: media, History: history, HistoryAnchors: history && anchors})
	}
	return out
}

// kbArchive is connector id's archive, while the user lets it into the knowledge base.
func (h *Manager) kbArchive(id string) (connector.Archive, error) {
	c := h.connectorByID(id)
	a, ok := connector.As[connector.Archive](c)
	if c == nil || !ok {
		return nil, fmt.Errorf("connector %q has no archive", id)
	}
	if !h.connectors.Enabled(id) || !h.connectors.Feeds(id, connector.FeedArchive) {
		return nil, errArchiveOff
	}
	return a, nil
}

var errArchiveOff = i18n.Err("hub-kb_sources.archive_off")

// historyTimeout bounds one history read of a connector in the hub (a program's calls have their own as well). The
// knowledge base never sends a read that timed out again: the source may have served it. A var for the tests.
var historyTimeout = 2 * time.Minute

// kbHistoryCall answers connectors.history_chats, connectors.history and connectors.history_anchors: read-only, only
// for a connector that archives into the knowledge base now (kbArchive) and declares the capability. Nothing is
// decided here: the query goes to the connector checked and with its limit cut, its answer and error come back as
// they are (docs/protocol/connector-protocol.md "History").
func (h *Manager) kbHistoryCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	var params struct {
		ID    string          `json:"id"`
		Query json.RawMessage `json:"query"`
	}
	if len(p.Params) > 0 {
		if err := json.Unmarshal(p.Params, &params); err != nil {
			return nil, true, err
		}
	}
	if _, err := h.kbArchive(params.ID); err != nil {
		return nil, true, err
	}
	c := h.connectorByID(params.ID)
	hist, ok := connector.As[connector.History](c)
	if !ok {
		return nil, true, fmt.Errorf("connector %q has no history", params.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	var out any
	var err error
	switch p.Method {
	case assistantwire.ConnectorsHistoryChats:
		var chats []connectorwire.HistoryChat
		chats, err = hist.HistoryChats(ctx)
		if chats == nil {
			chats = []connectorwire.HistoryChat{}
		}
		out = assistantwire.HistoryChatsResult{Chats: chats}
	case assistantwire.ConnectorsHistory:
		var q connectorwire.HistoryQuery
		if err := unmarshalQuery(params.Query, &q); err != nil {
			return nil, true, err
		}
		if err := q.Check(); err != nil {
			return nil, true, err
		}
		out, err = hist.History(ctx, q.Clamped())
	default:
		anchors, ok := connector.As[connector.HistoryAnchors](c)
		if !ok {
			return nil, true, fmt.Errorf("connector %q cannot search its history", params.ID)
		}
		var q connectorwire.HistoryAnchorsQuery
		if err := unmarshalQuery(params.Query, &q); err != nil {
			return nil, true, err
		}
		if err := q.Check(); err != nil {
			return nil, true, err
		}
		out, err = anchors.HistoryAnchors(ctx, q.Clamped())
	}
	if err != nil {
		return nil, true, err
	}
	raw, err := json.Marshal(out)
	return raw, true, err
}

// unmarshalQuery reads a history query; none is the zero query (which a history read then refuses for its chat).
func unmarshalQuery(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// kbMediaMost is the largest attachment connectors.archive_media hands over (a bigger one stays an attachment
// placeholder): the whole of connector.ArchiveMediaMost wherever the manager runs, since the knowledge base is in its
// process (no bus message carries it; before the move an assistant on another machine got at most 4 MB over the bus).
func (h *Manager) kbMediaMost() int { return connector.ArchiveMediaMost }

// connectorByID is the registered connector id; nil when there is none (or no registry, in tests).
func (h *Manager) connectorByID(id string) connector.Connector {
	if h.connectors == nil {
		return nil
	}
	return h.connectors.Get(id)
}

// archiveSources are the other names c's archive may file conversations under: its manifest's, minus the ids of the
// other connectors (one connector never writes into another's chats).
func (h *Manager) archiveSources(c connector.Connector) []string {
	var out []string
	for _, s := range c.Manifest().ArchiveSources {
		if other := h.connectors.Get(s); other == nil || other.ID() == c.ID() {
			out = append(out, s)
		}
	}
	return out
}

// forgetArchiveSources allows legacy records without connector attribution to be deleted only when their
// source name belongs to this connector alone. Explicitly attributed records are matched by the knowledge base.
func (h *Manager) forgetArchiveSources(c connector.Connector) []string {
	sources := h.archiveSources(c)
	for _, other := range h.connectors.All() {
		if other.ID() == c.ID() {
			continue
		}
		sources = slices.DeleteFunc(sources, func(source string) bool {
			return slices.Contains(other.Manifest().ArchiveSources, source)
		})
	}
	return sources
}

// kbConnectorOwner exposes only identity metadata to the assistant, never account credentials or server settings.
// Other connectors supply their owner's hints with Mine messages while the user lets them into the KB.
func (h *Manager) kbConnectorOwner(ctx context.Context) (assistantwire.KBOwnerSources, error) {
	out := assistantwire.KBOwnerSources{Sources: []assistantwire.KBOwnerSource{}}
	if h.mail == nil || h.connectors == nil || !h.connectors.Feeds("mail", connector.FeedArchive) {
		return out, nil
	}
	accounts, err := h.mail.ListAccounts()
	if err != nil {
		return out, errors.New("mail owner identities could not be read")
	}
	handles := []kb.Handle{}
	for _, account := range accounts {
		if address := strings.TrimSpace(account.Email); address != "" {
			handles = append(handles, kb.Handle{Kind: kb.HandleEmail, Value: address})
		}
	}
	if len(handles) > 0 {
		out.Sources = append(out.Sources, assistantwire.KBOwnerSource{Source: "mail", Handles: handles})
	}
	return out, nil
}
