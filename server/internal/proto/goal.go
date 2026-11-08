package proto

import "time"

// GoalInfo is a goal the agent keeps working toward until it holds (docs/integrations/agent-goal.md): Claude Code's /goal, whose
// Stop hook checks the condition each time the agent would stop, or a Codex thread goal.
type GoalInfo struct {
	Objective string `json:"objective"`
	// Status is one of the Goal* values: Claude Code's goals are active until met (complete); Codex has the others too.
	Status string `json:"status"`
	// Reason is Claude Code's latest check of the condition: why it holds, or what is still missing.
	Reason      string    `json:"reason,omitempty"`
	TokensUsed  int       `json:"tokens_used,omitempty"`
	TokenBudget int       `json:"token_budget,omitempty"` // Codex: the budget the goal may spend, 0 when there is none
	Seconds     int       `json:"seconds,omitempty"`      // time spent on it so far
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// PlanInfo is an agent's latest task list, counted (docs/integrations/agent-goal.md "Plan"): Done of Total items completed, and
// Current, the item in progress (its activeForm, else its subject) on one line, "" when none is. At is when the
// counts or Current last changed.
type PlanInfo struct {
	Done    int       `json:"done"`
	Total   int       `json:"total"`
	Current string    `json:"current,omitempty"`
	At      time.Time `json:"at"`
}

// A goal's statuses (Codex's thread_goals.status; Claude Code's goals are only active or complete).
const (
	GoalActive        = "active"
	GoalPaused        = "paused"
	GoalBlocked       = "blocked"
	GoalUsageLimited  = "usage_limited"
	GoalBudgetLimited = "budget_limited"
	GoalComplete      = "complete"
)

// LoopInfo describes a /loop: Claude re-runs Prompt on its own, either by scheduling its next wakeup each tick
// (ScheduleWakeup, kind "wakeup") or with a cron job (CronCreate, kind "cron").
type LoopInfo struct {
	Kind            string    `json:"kind"`
	Prompt          string    `json:"prompt,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	NextAt          time.Time `json:"next_at,omitempty"`
	Cron            string    `json:"cron,omitempty"`
	Ticks           int       `json:"ticks,omitempty"` // wakeups scheduled so far
}
