package managerupdate

import (
	"errors"
	"os"
)

func sameTarget(a, b Plan) bool {
	return a.Schema == b.Schema && a.ManagerID == b.ManagerID && a.Dir == b.Dir &&
		a.Binary == b.Binary && a.Unit == b.Unit && a.PlistPath == b.PlistPath &&
		a.UID == b.UID && a.ConfigSHA256 == b.ConfigSHA256 && a.PlistSHA256 == b.PlistSHA256
}

func requireActive(p Plan) error {
	pending, err := activePending(p)
	if err != nil {
		return err
	}
	if !pending {
		return errors.New("transaction no longer owns the active manager update")
	}
	return nil
}

// Called under the manager lock before scheduling a new guardian. Only a bound
// terminal receipt permits cleanup; a missing or malformed receipt fails closed.
func rejectPendingActivation(p Plan) error {
	path := activePath(p)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := canonical(path); err != nil {
		return err
	}
	active, err := ReadPlan(path)
	if err != nil {
		return err
	}
	if err := active.validateShape(); err != nil {
		return err
	}
	if !sameTarget(active, p) {
		return errors.New("active transaction target identity mismatch")
	}
	r, err := readReceipt(active)
	if err != nil {
		return err
	}
	if r.Phase != "confirmed" && r.Phase != "rollback" {
		return errors.New("another nonterminal transaction owns the manager update")
	}
	return clearActive(active)
}
