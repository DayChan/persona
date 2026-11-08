package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type decision struct {
	RunID     string `json:"run_id"`
	ManagerID string `json:"manager_id"`
	Decision  string `json:"decision"`
}

func acknowledgementReceipt(p Plan, e error) (Receipt, error) {
	if e != nil {
		return receipt(p, "applied", e), e
	}
	return readReceipt(p)
}

// Acknowledge communicates the publisher's independent Hub verification to the
// running guardian without taking its lifetime lock. Only the guardian finalizes.
func (c *Controller) Acknowledge(ctx context.Context, p Plan, action string) error {
	if action != "confirm" && action != "rollback" {
		return errors.New("invalid activation decision")
	}
	if e := p.validateShape(); e != nil {
		return e
	}
	if c.GOOS != "darwin" || c.UID != p.UID {
		return errors.New("wrong activation owner or platform")
	}
	if e := c.ReleaseCheck(p, c.Build, c.Exe); e != nil {
		return e
	}
	r, e := readReceipt(p)
	if e != nil {
		return e
	}
	if (r.Phase == "confirmed" && action == "confirm") || (r.Phase == "rollback" && action == "rollback") {
		return nil
	}
	if r.Phase != "applied" {
		return errors.New("activation is not awaiting publisher verification")
	}
	if e := requireActive(p); e != nil {
		return e
	}
	if action == "confirm" {
		if r.Ready == nil {
			return errors.New("new manager has not become ready")
		}
		if _, e = c.liveReady(ctx, p); e != nil {
			return e
		}
	}
	return writeJSON(filepath.Join(txDir(p), "decision.json"), decision{RunID: p.RunID, ManagerID: p.ManagerID, Decision: action})
}
func readDecision(p Plan) (string, error) {
	path := filepath.Join(txDir(p), "decision.json")
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	var d decision
	if e = json.Unmarshal(b, &d); e != nil {
		return "", e
	}
	if d.RunID != p.RunID || d.ManagerID != p.ManagerID || (d.Decision != "confirm" && d.Decision != "rollback") {
		return "", errors.New("activation decision identity mismatch")
	}
	return d.Decision, nil
}
