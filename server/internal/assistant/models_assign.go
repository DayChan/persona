package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/i18n"
)

// assign (POST /api/v1/launchers, docs/assistant/onboarding.md 5.4): a provider added in the onboarding becomes what the main chat and
// new Agents run on, but only where nobody has chosen anything yet; a choice the user made is never replaced.

// assignablePurposes are the categories assign may set: the two that run Claude Code on a computer.
var assignablePurposes = []string{"assistant", "agent.new"}

// Why assign left a category as it was.
const (
	keptCustomized   = "customized"   // someone chose its model (here, or the main chat's launcher in Settings › Assistant)
	keptIncompatible = "incompatible" // the category cannot run on the provider
	keptFailed       = "failed"       // reading or writing the configuration failed
)

// keptPurpose is a category assign left as it was, and why.
type keptPurpose struct {
	Purpose string `json:"purpose"`
	Reason  string `json:"reason"`
}

// assignIfDefault puts each of ids on provider while it is still at its default, under the configuration's lock.
func (s *Service) assignIfDefault(ctx context.Context, provider string, ids []string) ([]string, []keptPurpose) {
	assigned, kept := []string{}, []keptPurpose{}
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		s.logf("launchers: read providers before assignment: %v", err)
		for _, id := range ids {
			kept = append(kept, keptPurpose{Purpose: id, Reason: keptFailed})
		}
		return assigned, kept
	}
	ctx = withManagedSnapshot(ctx, stored)
	providers := s.modelProviders(ctx)
	for _, id := range ids {
		reason, err := s.assignOne(ctx, id, provider, providers)
		if err != nil {
			s.logf("launchers: assign %s to %s: %v", id, provider, err)
			reason = keptFailed
		}
		if reason != "" {
			kept = append(kept, keptPurpose{Purpose: id, Reason: reason})
			continue
		}
		assigned = append(assigned, id)
	}
	return assigned, kept
}

// assignOne sets category id to provider if it is at its default: no model chosen for it and, for the main chat, the
// assistant's launcher still the built-in claude. The reason it was kept, else "".
func (s *Service) assignOne(ctx context.Context, id, provider string, providers []ModelProvider) (string, error) {
	p, err := s.modelPurpose(ctx, id, providers)
	if err != nil {
		return "", err
	}
	if p.Override.Provider != "" || p.Override.Model != "" {
		return keptCustomized, nil
	}
	base, _, err := s.modelBaseline(ctx, p)
	if err != nil {
		return "", err
	}
	if base.Provider != launcherProvider("claude") {
		return keptCustomized, nil
	}
	b := p.Override
	b.Provider, b.Effort = provider, "" // a provider's model and effort are its own (patchModel)
	if err := s.validateModel(ctx, p, mergeModel(base, b), providers); err != nil {
		s.logf("launchers: assign %s to %s: %s", id, provider, i18n.Localize(err, i18n.En))
		return keptIncompatible, nil
	}
	if err := s.saveModelRecord(ctx, id, p.Revision, b); err != nil {
		return "", err
	}
	return "", nil
}
