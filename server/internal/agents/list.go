package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/sessionlist"
)

// indexSessions runs only when a source cache is refreshed, not on every HTTP
// request. Extension rows are already compact and are also their own summaries.
func (s *Service) indexSessions(source string, list []Session) {
	if list == nil {
		s.listIndex.RemoveSource(source)
		return
	}
	if !s.has(source) {
		return // removed after the refresh took its snapshot of sources; Remove already dropped its rows
	}
	items := make([]sessionlist.Item, 0, len(list))
	seen := make(map[string]bool, len(list))
	// what the user removed stays out of the pages and the counts (hidden.go); read once per refresh, not per request
	hidden := s.hiddenNow(context.Background())
	for _, row := range list {
		if row.ID == "" || seen[row.ID] || hiddenStill(row, hidden) {
			continue
		}
		seen[row.ID] = true
		state := "idle"
		if row.UnreadCount > 0 {
			state = "unread"
		}
		raw, _ := json.Marshal(row)
		items = append(items, sessionlist.Item{
			ID: row.ID, Source: source, Provider: row.Provider, Group: row.AccountID,
			State: state, UpdatedAt: time.UnixMilli(row.RankTimeMs),
			SearchText: strings.Join([]string{row.Title, row.Provider, row.AccountID}, "\n"),
			Summary:    json.RawMessage(raw), Full: json.RawMessage(raw),
		})
	}
	s.listIndex.Replace(source, items)
}

func (s *Service) refreshListIndex(ctx context.Context, opts sessionlist.Options) error {
	for _, src := range s.all() {
		if opts.Source != "" && src.Name() != opts.Source {
			continue
		}
		_, err := s.sessionsOf(ctx, src, false)
		if errors.Is(err, context.Canceled) {
			return err
		}
		if err != nil {
			s.logf("agents: %s: paginated sessions: %v", src.Name(), err)
			if opts.Source == "" && s.skipsFailed(src.Name()) {
				s.listIndex.RemoveSource(src.Name()) // match legacy aggregate behavior: unavailable sources are omitted
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Service) ListPage(ctx context.Context, opts sessionlist.Options) (sessionlist.Page, error) {
	if opts.PaneSource != "" {
		return sessionlist.Page{}, &sessionlist.Error{Code: "invalid_query"}
	}
	if opts.Cursor == "" {
		if err := s.refreshListIndex(ctx, opts); err != nil {
			return sessionlist.Page{}, err
		}
	}
	return s.listIndex.Query(opts)
}

func (s *Service) ListCounts(ctx context.Context, opts sessionlist.Options) (sessionlist.Counts, error) {
	if opts.PaneSource != "" {
		return sessionlist.Counts{}, &sessionlist.Error{Code: "invalid_query"}
	}
	if err := s.refreshListIndex(ctx, opts); err != nil {
		return sessionlist.Counts{}, err
	}
	return s.listIndex.Counts(opts)
}
