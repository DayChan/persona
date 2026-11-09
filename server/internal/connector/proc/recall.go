package proc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Taking back what the program sent (connector.Recaller, docs/extensions/connectors.md "Sent as the user"): a program whose manifest
// declares recall is asked only once the user agreed. It is a Recaller only by that declaration (connector.RecallerOf),
// and each method checks it again: this type implements every interface. Programs from before recall.can / recall.do keep
// their legacy connector.recall path.

var _ connector.Recaller = (*Conn)(nil)

// recallWordsMost is how much of a program's own words a refusal keeps.
const recallWordsMost = 200

// CanRecall answers from the manifest alone, as it must (a page of the ledger asks it for every row): a program that
// declares recall can be asked about any action. A current program gets recall.can immediately before execution, so its
// own action and age limits are checked without a network call for every row of a page.
func (c *Conn) CanRecall(string, time.Duration) bool { return c.Manifest().Has(connector.CapRecall) }

// Recall asks the program to take the message back and returns nil only for done or already_recalled (recalled true on
// the legacy path). A current program first gets the read-only can; an older one, which answers that it has no such
// method, gets only the legacy recall. No path sends both current and legacy writes.
func (c *Conn) Recall(ctx context.Context, m connector.SentMessage) error {
	if !c.Manifest().Has(connector.CapRecall) {
		return fmt.Errorf("%w: %s does not say it can take a message back", connector.ErrCannotRecall, c.cfg.Name)
	}
	// A row with no time is before any install time, so it is refused too: it cannot be shown to be this install's.
	if nb := c.cfg.RecallNotBefore; !nb.IsZero() && m.At.Before(nb) {
		return fmt.Errorf("%w: the send is older than this install of %s", connector.ErrCannotRecall, c.cfg.Name)
	}
	target, canonicalTarget := recallTarget(m.Target)
	can, legacy, err := c.recallCan(ctx, m)
	if err != nil {
		return err
	}
	if legacy {
		return c.recallLegacy(ctx, m, target)
	}
	if !can {
		return fmt.Errorf("%w: %s says this message cannot be taken back", connector.ErrCannotRecall, c.cfg.Name)
	}
	params := serve.RecallDoParams{RequestID: c.recallRequestID(m, canonicalTarget), Action: m.Action,
		MessageRef: m.Ref, Target: target, At: m.At}
	var out serve.RecallDoResult
	if err := c.call(ctx, serve.MRecallDo, params, &out); err != nil {
		return recallFailure(c.cfg.Name, err)
	}
	return recallDoOutcome(c.cfg.Name, out)
}

// recallCan asks the current protocol's read-only preflight. legacy is true only when the program says the method does
// not exist, which is how a program built before recall.can / recall.do answers; every other failure is final.
func (c *Conn) recallCan(ctx context.Context, m connector.SentMessage) (can, legacy bool, err error) {
	age := time.Duration(0)
	if !m.At.IsZero() {
		age = max(time.Since(m.At), 0)
	}
	var out serve.RecallCanResult
	err = c.call(ctx, serve.MRecallCan, serve.RecallCanParams{Action: m.Action, AgeSeconds: int64(age / time.Second)}, &out)
	if err == nil {
		return out.Can, false, nil
	}
	var re *rpc.Error
	if errors.As(err, &re) && (re.Code == serve.CodeUnknownMethod || re.Code == serve.CodeNoCapability) {
		return false, true, nil
	}
	return false, false, recallFailure(c.cfg.Name, err)
}

// recallLegacy is the protocol before recall.can / recall.do, kept unchanged for older programs.
func (c *Conn) recallLegacy(ctx context.Context, m connector.SentMessage, target map[string]any) error {
	var out serve.RecallResult
	params := serve.RecallParams{Action: m.Action, MessageRef: m.Ref, Target: target, At: m.At}
	if err := c.call(ctx, serve.MRecall, params, &out); err != nil {
		return recallFailure(c.cfg.Name, err)
	}
	if !out.Recalled {
		return fmt.Errorf("%w: %s did not say it was taken back", connector.ErrRecallUnknown, c.cfg.Name)
	}
	return nil
}

// recallTarget is the target object for the wire and its canonical JSON for the operation id. A target that is not an
// object goes as none, as the legacy protocol did.
func recallTarget(raw json.RawMessage) (map[string]any, json.RawMessage) {
	var target map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &target)
	}
	canonical, _ := json.Marshal(target)
	return target, canonical
}

// recallRequestID is stable for this connector and external message, without exposing the ledger row id. It lets the
// program share a duplicate call and remember a success; failures may be tried again explicitly.
func (c *Conn) recallRequestID(m connector.SentMessage, canonicalTarget json.RawMessage) string {
	h := sha256.New()
	for _, field := range [][]byte{[]byte(c.cfg.ID), []byte(m.Action), []byte(m.Ref), canonicalTarget,
		[]byte(m.At.UTC().Format(time.RFC3339Nano))} {
		fmt.Fprintf(h, "%d:", len(field))
		_, _ = h.Write(field)
	}
	return "recall_" + hex.EncodeToString(h.Sum(nil)[:16])
}

// recallDoOutcome maps a current result. Only the two states that confirm the message is gone succeed; an absent or
// unrecognized status is unknown, never a refusal that claims nothing happened and never success.
func recallDoOutcome(name string, out serve.RecallDoResult) error {
	words := func() string { return recallWords(name, out.Error) }
	switch out.Status {
	case serve.RecallDone, serve.RecallAlreadyRecalled:
		return nil
	case serve.RecallNotRecallable:
		return fmt.Errorf("%w: %s", connector.ErrCannotRecall, words())
	case serve.RecallUnavailable:
		return fmt.Errorf("%w: %s", connector.ErrRecallUnavailable, words())
	case serve.RecallRefused:
		return errors.New(words())
	case serve.RecallUnknown:
		return fmt.Errorf("%w: %s", connector.ErrRecallUnknown, words())
	default:
		return fmt.Errorf("%w: %s did not return a known recall outcome", connector.ErrRecallUnknown, name)
	}
}

// recallFailure is what a failed recall call means to connector.Recaller: nothing was sent when the program is not
// running, cannot take that message back or cannot act now; the outcome is unknown when the request went out and no
// answer came (a connection that dropped, a deadline) or the program says so; anything else the program answers is the
// service refusing, in the program's words cut to one sentence.
func recallFailure(name string, err error) error {
	var re *rpc.Error
	switch {
	case errors.Is(err, errNotRunning), errors.Is(err, host.ErrNotRunning):
		return fmt.Errorf("%w: %s is not running", connector.ErrRecallUnavailable, name)
	case errors.As(err, &re):
		words := recallWords(name, re.Message)
		switch re.Code {
		case serve.CodeNotRecallable, serve.CodeUnknownMethod, serve.CodeNoCapability:
			return fmt.Errorf("%w: %s", connector.ErrCannotRecall, words)
		case serve.CodeNotSignedIn, serve.CodeOffline, serve.CodeRateLimited:
			return fmt.Errorf("%w: %s", connector.ErrRecallUnavailable, words)
		case serve.CodeRecallUnknown, extension.CodeTimeout:
			return fmt.Errorf("%w: %s", connector.ErrRecallUnknown, words)
		}
		return errors.New(words)
	}
	// No answer: the request may have reached the program (the connection dropped, the deadline passed, or what was
	// written or read half way failed).
	return fmt.Errorf("%w: no answer from %s", connector.ErrRecallUnknown, name)
}

// recallWords is a program's error as one sentence: its first line, cut; a program that said nothing is named.
func recallWords(name, message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	if r := []rune(line); len(r) > recallWordsMost {
		line = string(r[:recallWordsMost])
	}
	if line = strings.TrimSpace(line); line == "" {
		return name + " did not take it back"
	}
	return line
}
