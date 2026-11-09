package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const toolLoopsSetting = "agents.tool_loops"

type savedToolLoop struct {
	Machine         string             `json:"machine"`
	Session         string             `json:"session"`
	Since           time.Time          `json:"since"`
	UpdatedAt       time.Time          `json:"updated_at"`
	Tool            string             `json:"tool"`
	Checks          int                `json:"checks"`
	CheckedCount    int                `json:"checked_count"`
	Silenced        bool               `json:"silenced"`
	SilencedByInput bool               `json:"silenced_by_input,omitempty"`
	ExpiredID       string             `json:"expired_id,omitempty"`
	Card            *proto.Interaction `json:"card,omitempty"`
}

// toolLoopSnapshot makes map iteration order irrelevant when comparing durable snapshots.
func toolLoopSnapshot(rows []savedToolLoop) ([]byte, error) {
	rows = append([]savedToolLoop{}, rows...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Machine != rows[j].Machine {
			return rows[i].Machine < rows[j].Machine
		}
		return rows[i].Session < rows[j].Session
	})
	return json.Marshal(rows)
}

// loadToolLoops must finish before checking loops, answering cards or publishing
// a snapshot. Failed reads are retried; an empty fallback would lose decisions.
func (s *Service) loadToolLoops(ctx context.Context) error {
	s.work.loopLoadMu.Lock()
	defer s.work.loopLoadMu.Unlock()
	s.work.mu.Lock()
	loaded := s.work.loopLoaded
	s.work.mu.Unlock()
	if loaded {
		return nil
	}
	var rows []savedToolLoop
	var snapshot []byte
	if st := s.store(); st != nil {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		found, err := st.Setting(readCtx, toolLoopsSetting, &rows)
		cancel()
		if err != nil {
			return err
		}
		if found {
			snapshot, err = toolLoopSnapshot(rows)
			if err != nil {
				return err
			}
		}
	}
	s.work.mu.Lock()
	s.work.init()
	for _, row := range rows {
		key := [2]string{row.Machine, row.Session}
		if row.Machine == "" || row.Session == "" || s.work.loops[key] != nil {
			continue
		}
		updatedAt := row.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		state := &toolLoopState{since: row.Since, updatedAt: updatedAt, tool: row.Tool, checks: row.Checks, checkedCount: row.CheckedCount, silenced: row.Silenced, silencedByInput: row.SilencedByInput, expiredID: row.ExpiredID, uncertain: true}
		if reason, invalid := s.work.loopInvalid[key]; invalid {
			if reason == loopTurnChanged {
				state.checks = 0
			}
			if reason != loopCandidateGone && !state.since.IsZero() {
				state.silenced, state.silencedByInput = true, true
			} else if !state.silenced {
				state.since, state.tool = time.Time{}, ""
			}
		} else if row.Card != nil && row.Card.ID != "" {
			state.cardID = row.Card.ID
			s.work.cards[row.Card.ID] = *row.Card
		}
		s.work.loops[key] = state
	}
	hadInvalid := len(s.work.loopInvalid) != 0
	s.work.loopInvalid = nil
	s.work.loopSaved = len(rows) != 0
	s.work.loopSnapshot = snapshot
	s.work.loopLoaded = true
	s.work.mu.Unlock()
	if hadInvalid {
		return s.saveToolLoops(ctx)
	}
	return nil
}

// expireToolLoopCard keeps the card visible until its terminal state is saved.
// A failed save leaves the same card for the next tick to retry.
func (s *Service) expireToolLoopCard(ctx context.Context, k [2]string) error {
	s.work.mu.Lock()
	state := s.work.loops[k]
	if state == nil || state.answering || state.expiredID != "" {
		s.work.mu.Unlock()
		return nil
	}
	card, ok := s.work.cards[state.cardID]
	if !ok || time.Now().Before(card.ExpiresAt) {
		s.work.mu.Unlock()
		return nil
	}
	id := state.cardID
	state.answering, state.consuming, state.silenced = true, true, true
	state.consumedSaved, state.expiredID = false, id
	s.work.mu.Unlock()
	if err := s.saveToolLoops(ctx, k); err != nil {
		s.work.mu.Lock()
		if s.work.loops[k] == state && state.cardID == id && state.consuming {
			state.answering, state.consuming, state.silenced = false, false, false
			state.expiredID = ""
		}
		s.work.mu.Unlock()
		return err
	}
	s.work.mu.Lock()
	valid := s.work.loops[k] == state && state.cardID == id && state.consuming
	if valid {
		delete(s.work.cards, id)
		state.cardID, state.answering, state.consuming = "", false, false
	}
	s.work.mu.Unlock()
	if valid {
		s.publishSoon()
	}
	return nil
}

// finishExpiredToolLoop retries interaction history independently of card
// withdrawal. The saved id survives a restart until history is finished.
func (s *Service) finishExpiredToolLoop(ctx context.Context, k [2]string) {
	s.work.mu.Lock()
	state := s.work.loops[k]
	if state == nil || state.expiredID == "" || state.consuming {
		s.work.mu.Unlock()
		return
	}
	id := state.expiredID
	s.work.mu.Unlock()
	if st := s.fleetStore(); st != nil {
		writeCtx, cancel := context.WithTimeout(ctx, historyTimeout)
		err := st.FinishInteraction(writeCtx, id, "expired", nil)
		cancel()
		if err != nil {
			s.logf("tool loop: expire history: %v", err)
			return
		}
	}
	s.work.mu.Lock()
	current := s.work.loops[k]
	if current == nil || current.expiredID != id {
		s.work.mu.Unlock()
		return
	}
	current.expiredID = ""
	s.work.mu.Unlock()
	if err := s.saveToolLoops(ctx); err != nil {
		s.work.mu.Lock()
		if current := s.work.loops[k]; current != nil && current.expiredID == "" {
			current.expiredID = id
		}
		s.work.mu.Unlock()
		s.logf("tool loop: clear expired history: %v", err)
	}
}

// saveToolLoops serializes snapshots and takes each snapshot after acquiring the
// write lock. An older captured state cannot overwrite a newer transition.
func (s *Service) saveToolLoops(ctx context.Context, committing ...[2]string) error {
	st := s.store()
	if st == nil {
		return nil
	}
	s.work.loopSaveMu.Lock()
	defer s.work.loopSaveMu.Unlock()
	s.work.mu.Lock()
	rows := make([]savedToolLoop, 0, len(s.work.loops))
	var target *toolLoopState
	for key, state := range s.work.loops {
		commit := len(committing) > 0 && key == committing[0]
		if commit {
			target = state
		}
		if state.updatedAt.IsZero() {
			state.updatedAt = time.Now().UTC()
		}
		row := savedToolLoop{Machine: key[0], Session: key[1], Since: state.since, UpdatedAt: state.updatedAt, Tool: state.tool, Checks: state.checks, CheckedCount: state.checkedCount, Silenced: state.silenced, SilencedByInput: state.silencedByInput, ExpiredID: state.expiredID}
		switch {
		case state.consuming && (state.consumedSaved || commit):
			// The terminal decision is durable; never restore its old card.
		case state.consuming:
			// Another session's write must not commit this unfinished answer.
			row.Silenced, row.ExpiredID = false, ""
			if card, ok := s.work.cards[state.cardID]; ok {
				row.Card = &card
			}
		case state.pendingCard != nil && (state.pendingSaved || commit):
			card := *state.pendingCard
			row.Card = &card
		case state.pendingCard != nil:
			row.Silenced = state.pendingPriorSilenced
		case state.cardID != "":
			if card, ok := s.work.cards[state.cardID]; ok {
				row.Card = &card
			}
		}
		rows = append(rows, row)
	}
	snapshot, err := toolLoopSnapshot(rows)
	unchanged := err == nil && bytes.Equal(snapshot, s.work.loopSnapshot)
	s.work.mu.Unlock()
	if err != nil {
		return err
	}
	if !unchanged {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = st.PutSetting(writeCtx, toolLoopsSetting, rows)
		cancel()
		if err != nil {
			return err
		}
	}
	s.work.mu.Lock()
	s.work.loopSnapshot = snapshot
	s.work.loopSaved = len(rows) != 0
	if target != nil && s.work.loops[committing[0]] == target {
		if target.consuming {
			target.consumedSaved = true
		}
		if target.pendingCard != nil {
			target.pendingSaved = true
		}
	}
	s.work.mu.Unlock()
	return nil
}
