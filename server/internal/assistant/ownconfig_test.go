package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The manager's manager.json goes over the hub's hello section by section; what it lacks keeps the hello's (an older
// hub still sends them), and a machine it names that the hub doesn't have is not used.
func TestOwnConfigOverTheHello(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manager.json")
	mine := `{"id":"x","token":"bus","assistant":{"owner":" Example ","machine":"laptop","launcher":"codex"},
		"agents":{"triage":{"max_retries":2}},"kb":{"enabled":false,"read_only":true,"text_days":30,"ai":{"model":"k"},"attachment_days_by_source":{"chat":3650,"off":0,"neg":-1}},
		"ai":{"base_url":"https://gateway.example.com","model":"m"},
		"inbox":{"triage_ai":{"model":"t"},"agent":{"machine":"gone"}},"hook_timeout_minutes":0}`
	if err := os.WriteFile(file, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := readOwnConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	hub := Hello{OwnerNotes: "the hub's notes", Assistant: assistantwire.Placement{MachineID: "server", Machines: []string{"server", "laptop"}},
		Models: assistantwire.HubModels{JudgeAI: &aiwire.Config{Model: "old"}, InboxAgentMachine: "server"},
		Notify: assistantwire.NotifyTimings{LongTurnMS: 5}}
	h := c.over(hub)
	if h.Owner != "Example" || h.OwnerNotes != "the hub's notes" || h.Assistant.MachineID != "laptop" || h.Assistant.Launcher != "codex" {
		t.Fatalf("assistant %+v %q %q", h.Assistant, h.Owner, h.OwnerNotes)
	}
	if h.Agents.Triage.MaxRetries != 2 || h.KB == nil || !h.KB.Off || !h.KB.ReadOnly || h.KB.TextDays != 30 || h.Models.KBAI == nil || h.Models.KBAI.Model != "k" {
		t.Fatalf("agents/kb %+v %+v %+v", h.Agents, h.KB, h.Models.KBAI)
	}
	if len(h.KB.AttachmentDaysBySource) != 1 || h.KB.AttachmentDaysBySource["chat"] != 3650 || h.KB.AttachmentDays != 0 {
		t.Fatalf("attachment days by source %+v", h.KB)
	}
	// Of inbox each of the hello's keys is replaced only when manager.json has it: its judge model stays the hello's.
	if h.Models.AI.Model != "m" || h.Models.TriageAI == nil || h.Models.TriageAI.Model != "t" || h.Models.JudgeAI == nil ||
		h.Models.JudgeAI.Model != "old" || h.Models.InboxAgentMachine != "" {
		t.Fatalf("models %+v", h.Models)
	}
	if h.Notify.HookTimeoutMS != 1800000 || h.Notify.LongTurnMS != 5 {
		t.Fatalf("notify %+v", h.Notify)
	}
	// Nothing in manager.json: the hello as it came, with the knowledge base's defaults.
	none, _ := readOwnConfig(filepath.Join(t.TempDir(), "manager.json"))
	if h := none.over(hub); h.OwnerNotes != "the hub's notes" || h.Models.JudgeAI.Model != "old" || h.KB == nil || h.KB.Off || h.KB.AttachmentDaysBySource != nil {
		t.Fatalf("empty %+v", h)
	}
	// A machine the hub doesn't have keeps the hub's default.
	if err := os.WriteFile(file, []byte(`{"assistant":{"machine":"elsewhere"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ = readOwnConfig(file)
	if h := c.over(hub); h.Assistant.MachineID != "server" {
		t.Fatalf("machine %q", h.Assistant.MachineID)
	}
}

// A manager.json that stops reading while the manager runs keeps the sections it last read: the next hello does not
// fall back to the bare defaults. And a manager does not start with one whose hello sections do not fit.
func TestOwnConfigKeepsTheLastThatRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "manager.json")
	if err := os.WriteFile(file, []byte(`{"assistant":{"owner":"Example"},"kb":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewBusHub(BusOptions{ConfigFile: file, Logf: t.Logf})
	if h := b.setHello(Hello{}); h.Owner != "Example" || h.KB == nil || !h.KB.Off {
		t.Fatalf("first hello %+v", h)
	}
	if err := os.WriteFile(file, []byte(`{"assistant":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := b.setHello(Hello{}); h.Owner != "Example" || h.KB == nil || !h.KB.Off {
		t.Fatalf("after a bad edit %+v", h)
	}
	if err := os.WriteFile(file, []byte(`{"assistant":{"owner":"Other"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := b.setHello(Hello{}); h.Owner != "Other" || h.KB.Off {
		t.Fatalf("fixed %+v", h)
	}
	if err := os.WriteFile(file, []byte(`{"agents":{"triage":"often"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := moveConfig(dir, file, nil, t.Logf); err == nil {
		t.Fatal("a manager started with agents of the wrong shape")
	}
}

// A built-in manager elsewhere (assistant.external) takes the hub's hello-era sections over from the handshake's
// answer into its manager.json, and goes by them; what it keeps of the hello has no copy of them left.
func TestSetHelloTakesTheHubsSectionsOver(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manager.json")
	b := NewBusHub(BusOptions{ConfigFile: file, Logf: t.Logf})
	h := b.setHello(Hello{LegacyConfig: []byte(`{"assistant":{"owner":"Example"},"kb":{"enabled":false}}`)})
	if h.Owner != "Example" || h.KB == nil || !h.KB.Off || h.LegacyConfig != nil {
		t.Fatalf("hello %+v", h)
	}
	if c, err := readOwnConfig(file); err != nil || c.Assistant == nil || c.Assistant.Owner == nil || *c.Assistant.Owner != "Example" {
		t.Fatalf("manager.json %+v %v", c, err)
	}
}

// The hub's sections that could not be taken over (manager.json's directory a link to nothing) are still gone by, as
// the hello used to carry them: not the bare defaults (the knowledge base on).
func TestSetHelloUsesTheHubsSectionsItCouldNotTake(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "not-mounted"), filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	b := NewBusHub(BusOptions{ConfigFile: filepath.Join(dir, "gone", "manager.json"), Logf: t.Logf})
	h := b.setHello(Hello{LegacyConfig: []byte(`{"assistant":{"owner":"Example"},"kb":{"enabled":false}}`)})
	if h.Owner != "Example" || h.KB == nil || !h.KB.Off {
		t.Fatalf("hello %+v", h)
	}
	// manager.updated does not carry them again: they are still gone by until they are taken over.
	if h := b.setHello(Hello{}); h.Owner != "Example" || h.KB == nil || !h.KB.Off {
		t.Fatalf("after manager.updated %+v", h)
	}
	// Once the directory is there, the next hello takes them over, and they are manager.json's from then on.
	if err := os.Remove(filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gone"), 0o700); err != nil {
		t.Fatal(err)
	}
	if h := b.setHello(Hello{}); h.Owner != "Example" || !h.KB.Off || b.legacy != nil {
		t.Fatalf("taken over %+v (pending %s)", h, b.legacy)
	}
	if c, err := readOwnConfig(filepath.Join(dir, "gone", "manager.json")); err != nil || c.KB == nil {
		t.Fatalf("manager.json %+v %v", c, err)
	}
}

func TestOwnConfigRejectsWorkAssistantMachine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manager.json")
	write := func(machine string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(`{"assistant":{"machine":"`+machine+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := Hello{Assistant: assistantwire.Placement{MachineID: "personal", Machines: []string{"personal", "work"}, WorkMachines: []string{"work"}}}
	write("personal")
	b := NewBusHub(BusOptions{ConfigFile: file, Logf: t.Logf})
	if got := b.setHello(h); got.Assistant.MachineID != "personal" {
		t.Fatalf("personal placement: %+v", got.Assistant)
	}
	write("work")
	c, err := readOwnConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validatePlacement(h); err == nil {
		t.Fatal("accepted a work assistant machine")
	}
	if got := c.over(h); got.Assistant.MachineID != "personal" {
		t.Fatalf("work override: %+v", got.Assistant)
	}
	if got := b.setHello(h); got.Assistant.MachineID != "personal" {
		t.Fatalf("invalid live edit changed placement: %+v", got.Assistant)
	}
	// A fresh manager reports the bad configuration before its handshake can expose any data.
	fresh := NewBusHub(BusOptions{ConfigFile: file, Logf: t.Logf})
	raw, _ := json.Marshal(h)
	if err := fresh.onHello(raw); err == nil {
		t.Fatal("handshake accepted work assistant.machine")
	}
	// A previously valid setting cannot restore a machine whose trust has changed.
	write("personal")
	h.Assistant.WorkMachines = append(h.Assistant.WorkMachines, "personal")
	if got := b.setHello(h); got.Assistant.MachineID != "" {
		t.Fatalf("stale configuration restored work placement: %+v", got.Assistant)
	}
}

func TestRejectedPlacementStillRestrictsConnectedWorkMachine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(file, []byte(`{"assistant":{"machine":"computer"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	b := NewBusHub(BusOptions{ConfigFile: file, Logf: t.Logf})
	h := Hello{Assistant: assistantwire.Placement{MachineID: "computer", Machines: []string{"computer"}}}
	b.setHello(h)
	d := newFleetDevs()
	d.add("computer", "Computer") // the existing node still has the old, personal configuration
	s := New(Options{Hub: b, Devices: d, Logf: t.Logf})
	if s.isWorkMachine("computer") {
		t.Fatal("initial personal configuration is restricted")
	}
	h.Assistant.WorkMachines = []string{"computer"}
	raw, _ := json.Marshal(h)
	if err := b.onHello(raw); err == nil {
		t.Fatal("invalid placement accepted")
	}
	if !s.isWorkMachine("computer") {
		t.Fatal("rejected handshake retained stale personal access")
	}
	if got := s.mcpCall(context.Background(), "computer", "session", "phone_contacts", nil, false); !got.IsError {
		t.Fatal("existing node retained phone access")
	}
}
