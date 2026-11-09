package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

const superviseDedupKey = "assistant.supervision_dedup"
const superviseDedupWindow = 30 * time.Minute

type superviseFingerprint struct {
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
}

func digestFingerprint(e prompts.SuperviseEntry, g superviseSignal) (string, string) {
	// State timestamps and elapsed minutes deliberately do not participate: a new
	// turn or a periodic sweep is not new information when the last summary is unchanged.
	key, _ := json.Marshal([]string{g.Todo, g.Machine, e.Machine, g.Session, e.AgentID, g.Kind, g.Report})
	text := e.Last
	if text == "" {
		text = g.Detail
	}
	payload, _ := json.Marshal([]any{strings.Join(strings.Fields(text), " "), strings.Join(strings.Fields(e.Ask), " "), e.Said, e.Evidence, e.Step, e.Steps})
	sum := sha256.Sum256(payload)
	return string(key), hex.EncodeToString(sum[:])
}

// reserveSupervision checkpoints deduplication before dispatch. A failed dispatch
// and queue admission releases the reservation. dedupMu is held by sendDigest.
func (s *Service) reserveSupervision(ctx context.Context, entries []prompts.SuperviseEntry, signals []superviseSignal, now time.Time) ([]prompts.SuperviseEntry, []superviseSignal, func(), error) {
	b := &s.supervise
	history := b.dedup
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, superviseDedupKey, &history); err != nil {
			return nil, signals, nil, err
		}
	}
	if history == nil {
		history = map[string]superviseFingerprint{}
	}
	previous := maps.Clone(history)
	next := maps.Clone(history)
	for key, seen := range next {
		if now.Sub(seen.At) >= superviseDedupWindow {
			delete(next, key)
		}
	}
	var out []prompts.SuperviseEntry
	var kept []superviseSignal
	for i, entry := range entries {
		key, hash := digestFingerprint(entry, signals[i])
		if seen, ok := next[key]; ok && seen.Hash == hash && now.Sub(seen.At) < superviseDedupWindow {
			continue
		}
		next[key] = superviseFingerprint{Hash: hash, At: now.UTC()}
		out, kept = append(out, entry), append(kept, signals[i])
	}
	if len(out) == 0 {
		return nil, nil, func() {}, nil
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, superviseDedupKey, next); err != nil {
			return nil, signals, nil, err
		}
	}
	b.dedup = next
	return out, kept, func() {
		b.dedup = previous
		if st := s.store(); st != nil {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := st.PutSetting(cctx, superviseDedupKey, previous); err != nil {
				s.logf("supervise: could not release deduplication reservation: %v", err)
			}
		}
	}, nil
}

func (s *Service) restoreDigest(signals []superviseSignal, at, prev time.Time) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.Equal(at) {
		b.last = prev
	}
	for _, g := range signals {
		if b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
		if _, newer := b.pending[g.id()]; !newer {
			b.pending[g.id()] = g
		}
	}
	s.retryDigestLocked(s.superviseTimings().urgent)
}
