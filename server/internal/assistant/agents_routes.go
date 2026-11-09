package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Agents business's routes (assistantwire's agentRoutes, relayed by the hub): the triage's switches, its numbers,
// and renaming the tabs nobody named.

func (s *Service) agentRoutes() {
	s.handle("GET /api/v1/agents/triage", func(w http.ResponseWriter, r *http.Request) {
		sw, err := s.readTriageSwitches(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("assistant-agents_work.read_switches_error", i18n.Args{"error": err.Error()}))
			return
		}
		apierr.WriteJSON(w, sw)
	})
	s.handle("PUT /api/v1/agents/triage", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change the triage"))
			return
		}
		var request struct {
			TriageSwitches
			ToolLoop *bool `json:"tool_loop"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		// An app that does not know the reminder's switch (#5096) leaves it as it was. While the saved switches cannot be
		// read, a PUT still goes through (it saves the whole set): then the reminder's switch it leaves out is the default.
		prev, _ := s.readTriageSwitches(r.Context())
		in := request.TriageSwitches
		in.ToolLoop = prev.ToolLoop
		if request.ToolLoop != nil {
			in.ToolLoop = *request.ToolLoop
		}
		if in.ReplyReminder == nil {
			in.ReplyReminder = prev.ReplyReminder
		}
		switch {
		case in.ReplyReminderMinutes == 0:
			in.ReplyReminderMinutes = prev.ReplyReminderMinutes
		case in.ReplyReminderMinutes < 0:
			in.ReplyReminderMinutes = 0 // back to the default
		}
		if err := s.putTriageSwitches(r.Context(), in); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, in)
	})
	s.handle("GET /api/v1/agents/work/stats", func(w http.ResponseWriter, r *http.Request) {
		hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if hours <= 0 || hours > 24*30 {
			hours = 24
		}
		st := s.workDB()
		if st == nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		events, err := st.WorkEvents(r.Context(), time.Now().Add(-time.Duration(hours)*time.Hour))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, summarizeWork(hours, events))
	})
	// {"dry_run": true, "all": false}: rename the tabs of running agents nobody named after their task.
	s.handle("POST /api/v1/tabs/autoname", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DryRun bool `json:"dry_run"`
			All    bool `json:"all"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		apierr.WriteJSON(w, map[string]any{"tabs": s.autonameTabs(ctx, in.DryRun, in.All)})
	})
}

// workStats is GET /api/v1/agents/work/stats: how the triage did over the last hours (docs/assistant/agent-work.md "Evaluation").
type workStats struct {
	Hours    int            `json:"hours"`
	Triaged  map[string]int `json:"triaged"` // "done", "blocked/user", … "unsure", "failed"
	By       map[string]int `json:"by"`      // rule, model
	Actions  map[string]int `json:"actions"` // continued, woke, asked, answered, withdrawn
	Resumed  map[string]int `json:"resumed"` // by user / hub after a triaged turn
	Unread   sampleStats    `json:"unread"`  // "Unread" over the hours
	NeedsYou sampleStats    `json:"needs_you"`
	// Waits: how long a triaged turn waited for the user's first look or reply (seconds), by what it was.
	Waits map[string]waitStats `json:"waits"`
}

type sampleStats struct {
	Samples int     `json:"samples"`
	Avg     float64 `json:"avg"`
	Max     int     `json:"max"`
	Last    int     `json:"last"`
}

type waitStats struct {
	N      int     `json:"n"`
	Median float64 `json:"median_seconds"`
	P90    float64 `json:"p90_seconds"`
}

// summarizeWork counts the events; a turn's wait is its first look or reply (later ones are not waits).
func summarizeWork(hours int, events []db.WorkEvent) workStats {
	s := workStats{Hours: hours, Triaged: map[string]int{}, By: map[string]int{}, Actions: map[string]int{}, Resumed: map[string]int{},
		Waits: map[string]waitStats{}}
	waits := map[string][]float64{}
	firstLook := map[string]bool{} // machine/session/turn handled already
	var unread, needs []int
	for _, e := range events {
		str := func(k string) string { v, _ := e.Detail[k].(string); return v }
		num := func(k string) float64 { v, _ := e.Detail[k].(float64); return v }
		switch e.Kind {
		case "triaged":
			k := str("phase")
			if k == "" {
				k = "unsure"
			} else if on := str("on"); on != "" {
				k += "/" + on
			}
			s.Triaged[k]++
			s.By[str("by")]++
		case "failed":
			s.Triaged["failed"]++
		case "continued", "limit_continued", "woke", "asked", "answered", "withdrawn":
			s.Actions[e.Kind]++
		case "resumed", "seen":
			if e.Kind == "resumed" {
				s.Resumed[str("by")]++
				if str("by") == "hub" {
					continue
				}
			}
			turn := e.At.Add(-time.Duration(num("after") * float64(time.Second))).Truncate(time.Second)
			key := e.MachineID + "/" + e.SessionID + "/" + turn.Format(time.RFC3339)
			if firstLook[key] {
				continue
			}
			firstLook[key] = true
			k := str("phase")
			if on := str("on"); on != "" {
				k += "/" + on
			}
			waits[k] = append(waits[k], num("after"))
		case "sample":
			u, _ := e.Detail[proto.StateUnread].(float64)
			n, _ := e.Detail[proto.StateNeedsYou].(float64)
			unread, needs = append(unread, int(u)), append(needs, int(n))
		}
	}
	s.Unread, s.NeedsYou = samples(unread), samples(needs)
	for k, v := range waits {
		slices.Sort(v)
		s.Waits[k] = waitStats{N: len(v), Median: v[len(v)/2], P90: v[min(len(v)-1, len(v)*9/10)]}
	}
	return s
}

func samples(v []int) sampleStats {
	if len(v) == 0 {
		return sampleStats{}
	}
	s := sampleStats{Samples: len(v), Last: v[len(v)-1]}
	sum := 0
	for _, n := range v {
		sum += n
		s.Max = max(s.Max, n)
	}
	s.Avg = float64(sum) / float64(len(v))
	return s
}
