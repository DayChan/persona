package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Policies (docs/assistant/inbox.md "Policies", #5747): the user says, for a chat, a sender or a repository, how its items are
// handled: the tier they get (always Needs you, FYI, noise) and what the AI twin does with them (takes them first, never
// touches them). A policy matches what a mute matches (mutesOf: the same tags, read off the item, the same for every
// source) and is decided here, in code, not by a model: the tier is set after triage (the model's or the rules'), so
// the summary and the title stay; the twin's part is read when the inbox escalates and queues. A mute wins over a
// policy (the item is filed); among policies the most specific tag wins (sender, then chat, then repository).

// inboxPoliciesKey is where the policies are kept.
const inboxPoliciesKey = "inbox_policies"

// What a policy does with the AI twin.
const (
	TwinFirst = "first" // escalated even when not Needs you, and started before the others
	TwinNever = "never" // never escalated: the item stays the user's
)

// InboxPolicy is a policy the user set: items of Source tagged Tag = Value get Tier (when set) and the twin's Twin
// (when set).
type InboxPolicy struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	SourceName string    `json:"source_name,omitempty"` // listed only
	Tag        string    `json:"tag"`
	Value      string    `json:"value"`
	Name       string    `json:"name"`
	Tier       string    `json:"tier,omitempty"` // action | fyi | noise; "" leaves the tier to triage
	Twin       string    `json:"twin,omitempty"` // first | never; "" leaves the twin as configured
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// policyTagRank orders the tags from the most specific: a sender's policy beats its chat's.
var policyTagRank = []string{connector.TagSender, connector.TagChat, connector.TagRepo}

func validPolicyTier(t string) bool {
	return t == "" || t == TierAction || t == TierFYI || t == TierNoise
}

func validPolicyTwin(t string) bool { return t == "" || t == TwinFirst || t == TwinNever }

// policyOf is the policy that applies to it, nil when none does; under b.mu.
func (b *inboxBook) policyOf(it *InboxItem) *InboxPolicy {
	if len(b.policies) == 0 {
		return nil
	}
	var best *InboxPolicy
	rank := len(policyTagRank)
	for _, o := range mutesOf(it) {
		r := slices.Index(policyTagRank, o.Tag)
		if r < 0 || r >= rank {
			continue
		}
		for i := range b.policies {
			if p := &b.policies[i]; p.Source == it.Source && p.Tag == o.Tag && p.Value == o.Value {
				best, rank = p, r
				break
			}
		}
	}
	return best
}

// policyTwin is what the AI twin does with it by its policy ("" as configured); under b.mu.
func (b *inboxBook) policyTwin(it *InboxItem) string {
	if p := b.policyOf(it); p != nil {
		return p.Twin
	}
	return ""
}

// applyPolicyTier gives a just triaged item the tier its policy sets: when that changes the tier, the reason says
// which policy did (the model's summary and title stay); a tier that is not Needs you is not urgent. It says whether
// it changed the item; under b.mu.
func (b *inboxBook) applyPolicyTier(it *InboxItem) bool {
	p := b.policyTierChange(it)
	if p == nil {
		return false
	}
	it.Tier, it.Reason = p.Tier, b.policyReason(p.Name)
	if it.Tier != TierAction {
		it.Urgent = false
	}
	return true
}

// policyTierChange is the policy that would change its tier, nil when none would; under b.mu.
func (b *inboxBook) policyTierChange(it *InboxItem) *InboxPolicy {
	p := b.policyOf(it)
	if p == nil || p.Tier == "" || it.Tier == p.Tier {
		return nil
	}
	return p
}

// policyReason is an item's reason when policy name set its tier.
func (b *inboxBook) policyReason(name string) string {
	return i18n.T(b.userLang(), "assistant-inbox.by_policy", i18n.Args{"name": name})
}

// applyPolicies gives the open triaged items the policies cover their tier (a policy set or changed now); items an
// event's conversation is handling keep theirs (it decides). Under b.mu. The writes are settled (inbox_rollback.go):
// an item whose row fails gets its old tier back, and setting the policy again gives it the new one.
func (b *inboxBook) applyPolicies() []*inboxWrite {
	var changed []*inboxWrite
	for _, it := range b.items {
		if !it.open() || it.Tier == "" || it.handled() || b.policyTierChange(it) == nil {
			continue
		}
		w := beginWrite(it) // only the items it changes are snapshot
		b.applyPolicyTier(it)
		b.noteTriaged(it)
		b.settle(w)
		changed = append(changed, w)
	}
	return changed
}

// loadInboxPolicies reads the policies kept, joining any added before; false when the database could not be read.
func (s *Service) loadInboxPolicies(ctx context.Context) bool {
	st := s.store()
	if st == nil {
		return true
	}
	b := &s.inbox
	b.policiesMu.Lock()
	defer b.policiesMu.Unlock()
	var kept []InboxPolicy
	if _, err := st.Setting(ctx, inboxPoliciesKey, &kept); err != nil {
		s.logf("inbox: policies: %v", err)
		return false
	}
	b.mu.Lock()
	for _, p := range b.policies {
		if !slices.ContainsFunc(kept, func(k InboxPolicy) bool { return samePolicyTarget(k, p) }) {
			kept = append(kept, p)
		}
	}
	b.policies = kept
	b.mu.Unlock()
	return true
}

func samePolicyTarget(a, b InboxPolicy) bool {
	return a.Source == b.Source && a.Tag == b.Tag && a.Value == b.Value
}

// putPolicies keeps the given policies snapshot; the caller holds policiesMu. A nil store keeps them in memory only.
func (s *Service) putPolicies(ctx context.Context, policies []InboxPolicy) error {
	st := s.store()
	if st == nil {
		return nil
	}
	return st.PutSetting(ctx, inboxPoliciesKey, policies)
}

// setPolicy adds a policy or, when one for the same target is there (set by id or by target), changes its tier and
// twin to p's; a policy that then sets neither is removed. It persists before memory changes (a failed save changes
// nothing, a retry is idempotent), applies the tiers to the open items and returns the policy and how many it changed.
// When some of those items' rows fail, they keep their old tier and it returns errInboxNotPersisted: the policy stays
// kept, and setting it again (the same target, idempotent) applies it to them.
func (s *Service) setPolicy(ctx context.Context, p InboxPolicy) (InboxPolicy, int, error) {
	b := &s.inbox
	b.policiesMu.Lock()
	defer b.policiesMu.Unlock()
	now := time.Now().UTC()
	b.mu.Lock()
	next := slices.Clone(b.policies)
	i := slices.IndexFunc(next, func(x InboxPolicy) bool { return p.ID != "" && x.ID == p.ID || p.ID == "" && samePolicyTarget(x, p) })
	if i >= 0 {
		x := next[i]
		x.Tier, x.Twin, x.UpdatedAt = p.Tier, p.Twin, now
		p = x
		if p.Tier == "" && p.Twin == "" {
			next = slices.Delete(next, i, i+1)
		} else {
			next[i] = p
		}
	} else {
		if p.ID != "" { // removed meanwhile
			b.mu.Unlock()
			return p, 0, errNoPolicy
		}
		p.ID = fmt.Sprintf("p%d", time.Now().UnixNano())
		p.CreatedAt, p.UpdatedAt = now, now
		next = append(next, p)
	}
	b.mu.Unlock()
	if err := s.putPolicies(ctx, next); err != nil {
		return p, 0, err
	}
	b.mu.Lock()
	b.policies = next
	changed := b.applyPolicies()
	b.mu.Unlock()
	failed := s.saveWrites(ctx, changed, "its tier by a policy")
	if p.Twin == TwinFirst {
		s.handInbox(ctx) // what it covers may be the twin's now; the kept policy's twin part holds either way
	}
	if len(failed) > 0 {
		return p, 0, errInboxNotPersisted
	}
	return p, len(changed), nil
}

// removePolicy takes policy id away; the items keep the tier it gave them (the next triage decides again).
func (s *Service) removePolicy(ctx context.Context, id string) (bool, error) {
	b := &s.inbox
	b.policiesMu.Lock()
	defer b.policiesMu.Unlock()
	b.mu.Lock()
	next := slices.DeleteFunc(slices.Clone(b.policies), func(p InboxPolicy) bool { return p.ID == id })
	removed := len(next) != len(b.policies)
	b.mu.Unlock()
	if !removed {
		return false, nil
	}
	if err := s.putPolicies(ctx, next); err != nil {
		return true, err
	}
	b.mu.Lock()
	b.policies = next
	b.mu.Unlock()
	return true, nil
}

// listedPolicies are the policies with their source's name.
func (s *Service) listedPolicies() []InboxPolicy {
	b := &s.inbox
	b.mu.Lock()
	policies := slices.Clone(b.policies)
	b.mu.Unlock()
	for i := range policies {
		policies[i].SourceName = b.sourceName(policies[i].Source)
	}
	return policies
}

var (
	errNoPolicyOption = i18n.ErrEnglish("inbox-errors.cannot_policy")
	errPolicyTier     = i18n.ErrEnglish("inbox-errors.policy_tier")
	errPolicyTwin     = i18n.ErrEnglish("inbox-errors.policy_twin")
	errNoPolicy       = i18n.ErrEnglish("inbox-errors.no_policy")
)

func (s *Service) inboxPolicyRoutes() {
	s.handle("GET /api/v1/inbox/policies", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		apierr.WriteJSON(w, map[string]any{"policies": s.listedPolicies()})
	})
	// Set the policy of what an item has under a tag ({"item_id", "tag", "tier", "twin"}): the value is read off the
	// item; a policy for it already there is changed, and one left setting nothing is removed.
	s.handle("POST /api/v1/inbox/policies", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			ItemID string `json:"item_id"`
			Tag    string `json:"tag"`
			Tier   string `json:"tier"`
			Twin   string `json:"twin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if !validPolicyTier(in.Tier) {
			apierr.Write(w, http.StatusBadRequest, errPolicyTier)
			return
		}
		if !validPolicyTwin(in.Twin) {
			apierr.Write(w, http.StatusBadRequest, errPolicyTwin)
			return
		}
		b := &s.inbox
		b.mu.Lock()
		it, ok := b.items[in.ItemID]
		var source string
		var opt *MuteOption
		if ok {
			source = it.Source
			for _, o := range mutesOf(it) {
				if o.Tag == in.Tag {
					opt = &o
					break
				}
			}
		}
		b.mu.Unlock()
		if !ok {
			apierr.Write(w, http.StatusNotFound, errNoInboxItem)
			return
		}
		if opt == nil {
			apierr.Write(w, http.StatusBadRequest, errNoPolicyOption)
			return
		}
		p, applied, err := s.setPolicy(r.Context(), InboxPolicy{Source: source, Tag: opt.Tag, Value: opt.Value, Name: opt.Name, Tier: in.Tier, Twin: in.Twin})
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		p.SourceName = b.sourceName(p.Source)
		apierr.WriteJSON(w, map[string]any{"policy": p, "applied": applied})
	})
	// Change a policy's tier and twin ({"tier", "twin"}, both as they should be); setting neither removes it.
	s.handle("PATCH /api/v1/inbox/policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Tier string `json:"tier"`
			Twin string `json:"twin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if !validPolicyTier(in.Tier) {
			apierr.Write(w, http.StatusBadRequest, errPolicyTier)
			return
		}
		if !validPolicyTwin(in.Twin) {
			apierr.Write(w, http.StatusBadRequest, errPolicyTwin)
			return
		}
		id := r.PathValue("id")
		b := &s.inbox
		b.mu.Lock()
		there := slices.ContainsFunc(b.policies, func(p InboxPolicy) bool { return p.ID == id })
		b.mu.Unlock()
		if !there {
			apierr.Write(w, http.StatusNotFound, errNoPolicy)
			return
		}
		p, applied, err := s.setPolicy(r.Context(), InboxPolicy{ID: id, Tier: in.Tier, Twin: in.Twin})
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		p.SourceName = b.sourceName(p.Source)
		apierr.WriteJSON(w, map[string]any{"policy": p, "applied": applied})
	})
	s.handle("DELETE /api/v1/inbox/policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		if _, err := s.removePolicy(r.Context(), r.PathValue("id")); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// applyRuledPolicies gives the round's items its rules triaged (a connector's tier, no model to wait for) their
// policy's tier; the ones the model sorts get it when it answers. Under b.mu.
func (b *inboxBook) applyRuledPolicies(items []*InboxItem) {
	for _, it := range items {
		if it.open() && it.Tier != "" && it.TriagedAt == it.Last && !it.handled() {
			b.roundWrite(it)
			b.applyPolicyTier(it)
		}
	}
}

// sortQueued orders the events waiting for the twin: what a policy puts first, then the newest. Under b.mu.
func (b *inboxBook) sortQueued(queued []*InboxItem) {
	sort.SliceStable(queued, func(i, j int) bool {
		if fi, fj := b.policyTwin(queued[i]) == TwinFirst, b.policyTwin(queued[j]) == TwinFirst; fi != fj {
			return fi
		}
		return queued[i].UpdatedAt.After(queued[j].UpdatedAt)
	})
}
