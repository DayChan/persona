package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/nwverse/persona/server/internal/proto"
)

type questionPersistence struct {
	done chan struct{}
	err  error
}

func durableQuestion(ix proto.Interaction) bool {
	return ix.Kind == proto.IxQuestion && (ix.ToolName == workCardTool || ix.ToolName == toolLoopCardTool)
}

// These owner cards are reconstructed from snapshots. A successful response must
// survive that reconstruction before the owner is allowed to act on it.
func (s *Service) persistQuestion(p *pending, status string, resolution proto.Resolution) error {
	err := s.writeQuestionOutcome(p.ix, status, resolution)
	s.fleet.mu.Lock()
	attempt := p.persisting
	attempt.err = err
	if err != nil {
		p.claimed, p.resolution = false, nil
		p.answeredBy, p.answeredReason, p.answeredWith = "", "", nil
		p.persistenceErr = err
	}
	p.persisting = nil
	close(attempt.done)
	s.fleet.mu.Unlock()
	return err
}

func (s *Service) writeQuestionOutcome(ix proto.Interaction, status string, resolution proto.Resolution) error {
	st := s.fleetStore()
	if st == nil {
		return fmt.Errorf("owned question outcome store is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	defer cancel()
	// The initial best-effort insert may have failed. FinishInteraction alone can
	// succeed without updating a row; ensure the row exists, then verify its value.
	if err := st.InsertInteraction(ctx, ix); err != nil {
		return fmt.Errorf("record owned question: %w", err)
	}
	if err := st.FinishInteraction(ctx, ix.ID, status, &resolution); err != nil {
		return fmt.Errorf("record owned question outcome: %w", err)
	}
	keptStatus, kept, err := st.InteractionOutcome(ctx, ix.ID)
	if err != nil {
		return fmt.Errorf("verify owned question outcome: %w", err)
	}
	expected, err := json.Marshal(resolution)
	if err != nil {
		return fmt.Errorf("encode owned question outcome: %w", err)
	}
	actual, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("encode stored question outcome: %w", err)
	}
	if keptStatus != status || kept == nil || !bytes.Equal(actual, expected) {
		return fmt.Errorf("owned question outcome was not persisted")
	}
	return nil
}
