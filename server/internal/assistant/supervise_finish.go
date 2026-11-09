package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// promoteSupervision is monotone within a wake-up: a later silent finish cannot hide a user action.
func (s *Service) promoteSupervision(self string) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if _, held := b.hold[self]; held {
		b.visible[self] = true
	}
}

// toolSupervisionFinish records the caller's disposition in its persisted MCP result.
// The transcript remains intact; clients fold the entire triage turn, not just its input.
func (s *Service) toolSupervisionFinish(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		Visibility string `json:"visibility"`
		Summary    string `json:"summary"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	a.Summary = strings.TrimSpace(a.Summary)
	if (a.Visibility != "silent" && a.Visibility != "user") || a.Summary == "" || utf8.RuneCountInString(a.Summary) > 1000 {
		return "", errors.New("visibility must be silent or user, with a summary of 1–1000 characters")
	}
	b := &s.supervise
	b.mu.Lock()
	_, held := b.hold[self]
	if !held || a.Visibility == "silent" && b.visible[self] {
		b.mu.Unlock()
		return "", errors.New("this is not an unpromoted supervision turn; normal user messages cannot be hidden")
	}
	level := "triage"
	if a.Visibility == "user" {
		level = "user"
		b.visible[self] = true
	}
	b.mu.Unlock()
	body, err := json.Marshal(map[string]string{"visibility": a.Visibility, "summary": a.Summary, "level": level})
	return string(body), err
}
