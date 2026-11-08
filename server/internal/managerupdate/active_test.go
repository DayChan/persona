package managerupdate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func pendingFixture(t *testing.T, phase string) (*Controller, Plan) {
	t.Helper()
	c, p := fixture(t)
	prepare(t, p)
	if err := copyExclusive(p.Binary, filepath.Join(txDir(p), "previous")); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(activePath(p), p); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(txDir(p), "receipt.json"), receipt(p, phase, nil)); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquire(filepath.Join(p.Dir, ".manager-update.lock"))
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	return c, p
}

func TestStartRejectsForeignNonterminalTransaction(t *testing.T) {
	for _, phase := range []string{"checked", "applied", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			c, a := pendingFixture(t, phase)
			b := a
			b.RunID = "second-run"
			before := tree(t, c.Home)
			if err := c.Start(context.Background(), b); err == nil {
				t.Fatal("foreign pending transaction accepted")
			}
			if !reflect.DeepEqual(before, tree(t, c.Home)) {
				t.Fatal("rejected start changed transaction or service state")
			}
		})
	}
}

func TestStaleGuardianCannotUndoSuccessor(t *testing.T) {
	for _, active := range []string{"missing", "checked", "applied", "confirmed", "rollback"} {
		t.Run(active, func(t *testing.T) {
			c, a := pendingFixture(t, "applied")
			b := a
			b.RunID = "second-run"
			prepare(t, b)
			if err := replaceImage(c.Exe, a.Binary); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(txDir(b), "receipt.json"), receipt(b, "confirmed", nil)); err != nil {
				t.Fatal(err)
			}
			if active == "missing" {
				if err := os.Remove(activePath(a)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := writeJSON(activePath(b), b); err != nil {
					t.Fatal(err)
				}
				if err := writeJSON(filepath.Join(txDir(b), "receipt.json"), receipt(b, active, nil)); err != nil {
					t.Fatal(err)
				}
			}
			before := tree(t, c.Home)
			if _, err := c.Run(context.Background(), a); err == nil {
				t.Fatal("stale guardian accepted missing or foreign ownership")
			}
			if _, err := c.rollback(context.Background(), a, nil); err == nil {
				t.Fatal("rollback accepted missing or foreign ownership")
			}
			if !reflect.DeepEqual(before, tree(t, c.Home)) {
				t.Fatal("stale guardian altered successor state or executable")
			}
		})
	}
}

func TestStartReapsOnlyBoundTerminalActive(t *testing.T) {
	for _, phase := range []string{"confirmed", "rollback", "missing", "malformed", "wrong-run", "wrong-manager"} {
		t.Run(phase, func(t *testing.T) {
			c, a := pendingFixture(t, "confirmed")
			path := filepath.Join(txDir(a), "receipt.json")
			r := receipt(a, phase, nil)
			switch phase {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if phase == "wrong-run" {
					r.Phase = "confirmed"
					r.RunID = "other"
				}
				if phase == "wrong-manager" {
					r.Phase = "confirmed"
					r.ManagerID = "m-other"
				}
				if err := writeJSON(path, r); err != nil {
					t.Fatal(err)
				}
			}
			b := a
			b.RunID = "second-run"
			before := tree(t, c.Home)
			err := c.Start(context.Background(), b)
			if phase == "confirmed" || phase == "rollback" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(activePath(a)); !os.IsNotExist(err) {
					t.Fatalf("terminal marker not cleared: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("unbound terminal receipt accepted")
				}
				if !reflect.DeepEqual(before, tree(t, c.Home)) {
					t.Fatal("invalid receipt changed state")
				}
			}
		})
	}
}

func TestOldTerminalGuardianLeavesForeignMarker(t *testing.T) {
	for _, phase := range []string{"confirmed", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			c, a := pendingFixture(t, phase)
			b := a
			b.RunID = "second-run"
			if err := writeJSON(activePath(b), b); err != nil {
				t.Fatal(err)
			}
			before := tree(t, c.Home)
			r, err := c.Run(context.Background(), a)
			if err != nil || r.Phase != phase {
				t.Fatalf("phase=%s err=%v", r.Phase, err)
			}
			if !reflect.DeepEqual(before, tree(t, c.Home)) {
				t.Fatal("terminal guardian changed foreign state")
			}
		})
	}
}
