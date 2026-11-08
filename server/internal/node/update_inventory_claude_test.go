package node

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUpdateClaudeBackgroundRequiresExplicitObservation(t *testing.T) {
	p := &assistantProc{}
	if _, _, err := p.observedUpdateTasks(); err == nil {
		t.Fatal("initial nil task cache became an explicit empty inventory")
	}
	if _, err := p.recordBackgroundTasks(json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	list, revision, err := p.observedUpdateTasks()
	if err != nil || len(list) != 0 || revision != 1 {
		t.Fatalf("explicit empty observation lost: %v %d %v", list, revision, err)
	}
}

func TestUpdateClaudeBackgroundEventWithoutListKeepsActiveWork(t *testing.T) {
	n := assistantNode(t)
	p := &assistantProc{id: "11111111-2222-3333-4444-555555555555"}
	stream := strings.NewReader("{\"type\":\"system\",\"subtype\":\"background_tasks_changed\",\"tasks\":[{\"description\":\"synthetic work\"}]}\n" +
		"{\"type\":\"system\",\"subtype\":\"background_tasks_changed\"}\n")
	n.readAssistant(p, stream)
	if _, _, err := p.observedUpdateTasks(); err == nil {
		t.Fatal("omitted event list became complete empty evidence")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tasks) != 1 {
		t.Fatal("omitted event list erased active work")
	}
}

func TestUpdateClaudeMalformedInventoryInvalidatesWithoutDroppingTasks(t *testing.T) {
	for _, value := range []string{"", "null", `{}`, `[null]`, `"invalid"`} {
		t.Run(value, func(t *testing.T) {
			p := &assistantProc{}
			if _, err := p.recordBackgroundTasks(json.RawMessage(`[{"description":"synthetic work"}]`)); err != nil {
				t.Fatal(err)
			}
			if _, err := p.recordBackgroundTasks(json.RawMessage(value)); err == nil {
				t.Fatal("invalid inventory was accepted")
			}
			if _, _, err := p.observedUpdateTasks(); err == nil {
				t.Fatal("invalid event retained a complete-inventory claim")
			}
			p.mu.Lock()
			kept := len(p.tasks) == 1 && p.tasks[0] == "synthetic work"
			p.mu.Unlock()
			if !kept {
				t.Fatal("unknown evidence erased the existing active task")
			}
		})
	}
}
