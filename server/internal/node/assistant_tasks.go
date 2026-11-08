package node

import (
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// recordBackgroundTasks preserves the difference between an explicit empty
// runtime event and an absent, null or malformed list. Unknown evidence keeps
// existing task descriptions, so it cannot erase visible active work.
func (p *assistantProc) recordBackgroundTasks(raw json.RawMessage) ([]string, error) {
	var event *[]*struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &event); err != nil || event == nil {
		p.invalidateUpdateTasks()
		return nil, errors.New("explicit background task list unavailable")
	}
	tasks := make([]string, 0, len(*event))
	for _, item := range *event {
		if item == nil {
			p.invalidateUpdateTasks()
			return nil, errors.New("background task entry is not an object")
		}
		tasks = append(tasks, item.Description)
	}
	p.mu.Lock()
	p.tasks, p.tasksObserved = tasks, true
	p.tasksRevision++
	p.lastActive = time.Now()
	p.mu.Unlock()
	return tasks, nil
}

func (p *assistantProc) invalidateUpdateTasks() {
	p.mu.Lock()
	p.tasksObserved = false
	p.tasksRevision++
	p.mu.Unlock()
}

func (p *assistantProc) observedUpdateTasks() ([]string, uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.tasksObserved {
		return nil, p.tasksRevision, errors.New("runtime background inventory has not been explicitly observed")
	}
	return slices.Clone(p.tasks), p.tasksRevision, nil
}
