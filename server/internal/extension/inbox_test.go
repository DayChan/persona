package extension

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// inboxExtension makes the example an inbox plugin: its connector has an inbox (and fallback when asked); rules adds
// triage_rules of that type to its settings ("" none).
func inboxExtension(fallback bool, rules string) func(m map[string]any) {
	return func(m map[string]any) {
		caps := []any{"status", "inbox"}
		if fallback {
			caps = append(caps, "fallback")
		}
		m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "examplenotes", "name": "Example Notes", "capabilities": caps}}}
		if rules != "" {
			props := m["configuration"].(map[string]any)["properties"].(map[string]any)
			props[TriageRulesField] = map[string]any{"type": rules, "title": "分拣规则"}
		}
	}
}

// A plugin with an inbox connector brings triage_rules (a string at the top of its settings); one without an inbox
// needs none. Which of its inbox connectors have no fallback of their own.
func TestCheckTriageRules(t *testing.T) {
	for name, c := range map[string]struct {
		change func(m map[string]any)
		ok     bool
	}{
		"no inbox":         {func(map[string]any) {}, true},
		"inbox, rules":     {inboxExtension(false, "string"), true},
		"inbox, no rules":  {inboxExtension(false, ""), false},
		"rules not string": {inboxExtension(true, "integer"), false},
	} {
		m, err := Parse(with(t, c.change))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := CheckTriageRules(m); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	m, _ := Parse(with(t, inboxExtension(false, "string")))
	if inbox, needs := InboxConnectors(m); len(inbox) != 1 || len(needs) != 1 {
		t.Fatalf("without fallback: %v %v", inbox, needs)
	}
	m, _ = Parse(with(t, inboxExtension(true, "string")))
	if inbox, needs := InboxConnectors(m); len(inbox) != 1 || len(needs) != 0 {
		t.Fatalf("with fallback: %v %v", inbox, needs)
	}
}

// A package whose inbox connector has no triage_rules is neither installed nor taken as an upgrade; one installed
// before is not checked again.
func TestPreviewRefusesInboxWithoutRules(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	bad := t.TempDir()
	writePackage(t, bad, inboxExtension(true, ""))
	if _, err := r.Preview(ctx, "dir:"+bad, ""); err == nil || !strings.Contains(err.Error(), TriageRulesField) {
		t.Fatalf("installed without triage_rules: %v", err)
	}
	good := t.TempDir()
	writePackage(t, good, inboxExtension(true, "string"))
	pv, err := r.Preview(ctx, "dir:"+good, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	next := t.TempDir()
	writePackage(t, next, func(m map[string]any) {
		inboxExtension(true, "")(m)
		m["version"] = "1.3.0"
	})
	if _, err := r.Preview(ctx, "dir:"+next, "example-notes"); err == nil || !strings.Contains(err.Error(), TriageRulesField) {
		t.Fatalf("upgraded to a version without triage_rules: %v", err)
	}
}

// CanEnable is asked before a plugin is installed (on at once), switched on, or upgraded while on; a refusal keeps the
// preview, so the same one goes through once it is allowed. Off, an upgrade is not asked; nor is a rollback.
func TestCanEnable(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	refuse := errors.New("not now")
	var asked []string
	allowed := false
	r.CanEnable = func(_ context.Context, source string, m *Manifest) error {
		asked = append(asked, source+" "+m.Version)
		if allowed {
			return nil
		}
		return refuse
	}
	pkg := t.TempDir()
	writePackage(t, pkg, inboxExtension(false, "string"))
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); !errors.Is(err, refuse) || r.Get("example-notes") != nil {
		t.Fatalf("installed while refused: %v", err)
	}
	allowed = true
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatalf("the same preview once allowed: %v", err)
	}

	allowed = false
	if err := r.SetEnabled(ctx, "example-notes", true, "cli"); err != nil {
		t.Fatalf("already on: %v", err) // not a switch on: not asked
	}
	next := t.TempDir()
	writePackage(t, next, func(m map[string]any) {
		inboxExtension(false, "string")(m)
		m["version"] = "1.3.0"
	})
	up, err := r.Preview(ctx, "dir:"+next, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", up.ID, "", "cli"); !errors.Is(err, refuse) || r.Get("example-notes").Record.Version != "1.2.0" {
		t.Fatalf("upgraded while on and refused: %v", err)
	}
	if err := r.SetEnabled(ctx, "example-notes", false, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", up.ID, "", "cli"); err != nil {
		t.Fatalf("off, an upgrade is not asked: %v", err)
	}
	if err := r.SetEnabled(ctx, "example-notes", true, "cli"); !errors.Is(err, refuse) || r.Get("example-notes").Record.Enabled {
		t.Fatalf("switched on while refused: %v", err)
	}
	n := len(asked)
	if _, err := r.Rollback(ctx, "example-notes", "cli"); err != nil || len(asked) != n {
		t.Fatalf("rollback: %v, asked %v", err, asked[n:])
	}
	if !strings.HasPrefix(asked[0], SourceInstalled+" ") {
		t.Fatalf("asked %v", asked)
	}
}
