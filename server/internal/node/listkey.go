package node

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// listKey ignores fields that churn without meaning (updated_at of live sessions).
func listKey(list []proto.Session) string { return listKeySized(list, 0) }

// listKeySized is listKey with the builder grown to size first (the last key's length: lists change little). It runs
// on every publish, so the common fields go straight into the builder; fmt only writes the rare composite ones.
func listKeySized(list []proto.Session, size int) string {
	var w keyWriter
	w.b.Grow(size)
	for i := range list {
		s := &list[i]
		w.str(s.SessionID, '|')
		w.str(s.Status, '|')
		w.str(s.Title, '|')
		w.str(s.PaneID, '|')
		w.str(s.SpaceName, '|')
		w.str(s.ParentSpaceID, '|')
		w.str(s.SlotName, '|')
		w.str(s.LastText, '|')
		w.int(s.UpdatedAt.Unix()/10, '|')
		w.quotes(s.Queued)
		w.b.WriteByte('|')
		w.str(s.Suggestion, '|')
		w.str(s.SpawnFrom, '\n')
		w.b.WriteString("hierarchy:")
		w.quote(s.ParentThreadID, '/')
		w.quote(s.ParentSessionID, '/')
		w.quote(s.RootSessionID, '/')
		w.str(strconv.FormatBool(s.IsSubagent), '/')
		w.quote(s.RepoRoot, '/')
		w.quote(s.SpaceID, '/')
		w.quote(s.SpaceKind, '\n')
		writeUltracodeKey(&w, s)
		if s.Activity != "" { // a new step shows at once, not with the next 10 s of updated_at
			w.b.WriteString("activity:")
			w.str(s.Activity, '/')
			w.int(unixOf(s.ActivityAt), '\n')
		}
		// A new reason or link alone is a change.
		w.b.WriteString("attention:")
		w.quote(s.AttentionReason, '/')
		w.quote(s.FailureReason, '/')
		w.str(s.ExternalURL, '\n')
		// A topic dispatched or a session's chat known: the todos follow it at once.
		if s.Chat != "" || len(s.Dispatched) > 0 {
			w.b.WriteString("botmux:")
			w.str(s.Chat, '/')
			w.str(s.Topic, '/')
			w.quotes(s.Dispatched)
			w.b.WriteByte('\n')
		}
		// The pane's place in its tab, present or not: a swap or a closed shell pane changes only this (and 0 is not none).
		w.b.WriteString("pane:")
		w.str(s.SlotID, '/')
		w.int(int64(s.SlotIndex), '/')
		if s.PaneIndex != nil {
			w.int(int64(*s.PaneIndex), '\n')
		} else {
			w.str("none", '\n')
		}
		if c := s.Compaction; c != nil {
			w.b.WriteString("compaction:")
			w.str(c.State, '/')
			w.int(c.StartedAt.Unix(), '\n')
		}
		// A Codex thread's effort, which may change on the computer without a turn (codex_effort.go).
		if len(s.Efforts) > 0 {
			fmt.Fprintf(&w.b, "effort:%s/%v\n", s.Effort, s.Efforts)
		}
		w.b.WriteString("source:")
		w.str(s.SourceKind, '/')
		w.str(s.SourceThreadID, '/')
		w.str(s.SourceInstanceID, '/')
		w.str(s.ReadOnlyReason, '/')
		if s.Capabilities != nil {
			fmt.Fprint(&w.b, s.Capabilities)
		} else {
			w.b.WriteString("<nil>")
		}
		w.b.WriteByte('/')
		w.str(strconv.FormatBool(s.Attached), '/')
		w.str(s.CodexRoute, '/')
		w.str(s.CodexRouteNote, '\n')
		for _, t := range s.Background {
			w.str(t.ID, ',')
		}
		w.str(strconv.FormatBool(s.BackgroundWaiting), '\n')
		// Workflow journals advance without changing the main transcript's rev, and so do its agents: each one's
		// latest tool call (its name, and its calls to tell one Read from the next) goes out as often as the
		// workflow cache reads them (workflowReadInterval). Tokens and the last text ride along.
		for _, run := range s.Workflows {
			w.b.WriteString("workflow:")
			w.str(run.TaskID, '/')
			w.str(run.Status, '/')
			w.quote(run.Phase, '/')
			w.int(int64(run.AgentsDone), '/')
			w.int(int64(run.AgentsStarted), '/')
			w.int(int64(run.AgentsRunning), '/')
			w.int(int64(run.AgentsFailed), '\n')
			for _, a := range run.Agents {
				if a.LatestTool != nil {
					w.b.WriteString(a.LatestTool.Name)
				}
				w.b.WriteByte('/')
				w.int(int64(a.Calls), ',')
			}
		}
		if s.Loop != nil {
			w.b.WriteString("loop")
			w.tmp = strconv.AppendInt(w.tmp[:0], s.Loop.NextAt.Unix(), 10)
			w.b.Write(w.tmp)
		}
		if g := s.Goal; g != nil {
			w.b.WriteString("goal:")
			w.str(g.Status, '/')
			w.quote(g.Objective, '/')
			w.quote(g.Reason, '\n')
		}
		if p := s.Plan; p != nil { // a step done or started is pushed when it happens
			w.b.WriteString("plan:")
			w.int(int64(p.Done), '/')
			w.int(int64(p.Total), '/')
			w.quote(p.Current, '\n')
		}
		// A subagent's new call shows at once (its card says what it does now, #5602); the list goes out at most once a
		// second, with the poll.
		for _, a := range s.Subagents {
			w.str(a.ID, ':')
			w.str(strconv.FormatBool(a.Running), ':')
			w.int(int64(a.Steps), ':')
			if a.LastTool != nil {
				w.str(a.LastTool.ID, ':')
			}
			w.int(a.UpdatedAt.Unix()/10, ',')
		}
		for _, p := range s.Ports {
			w.b.WriteString("port")
			w.int(int64(p.Port), '/')
			w.int(int64(p.PID), '/')
			w.str(p.Title, ',')
		}
	}
	return w.b.String()
}

// keyWriter writes listKey's fields, each followed by its separator; tmp is the scratch numbers and quotes go
// through.
type keyWriter struct {
	b   strings.Builder
	tmp []byte
}

func (w *keyWriter) str(s string, sep byte) {
	w.b.WriteString(s)
	w.b.WriteByte(sep)
}

func (w *keyWriter) int(v int64, sep byte) {
	w.tmp = strconv.AppendInt(w.tmp[:0], v, 10)
	w.b.Write(w.tmp)
	w.b.WriteByte(sep)
}

// quote writes s as %q would.
func (w *keyWriter) quote(s string, sep byte) {
	w.tmp = strconv.AppendQuote(w.tmp[:0], s)
	w.b.Write(w.tmp)
	w.b.WriteByte(sep)
}

// quotes writes a list of strings as %q would ([] when empty, the rare others through fmt).
func (w *keyWriter) quotes(list []string) {
	if len(list) == 0 {
		w.b.WriteString("[]")
		return
	}
	fmt.Fprintf(&w.b, "%q", list)
}

func unixOf(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}
