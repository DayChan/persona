package node

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Turn state from Persona's own hooks (docs/protocol/agent-states.md "Hook turn signals").
//
// A session that runs Persona's hooks tells the node when a turn starts (UserPromptSubmit) and ends (Stop). Counting them
// is a stronger sign than anything the node can read off the terminal or the transcript: a turn is open while prompts
// outnumber the Stops that answered them. A session without hooks (started outside Persona) never gets here and keeps
// the heuristics of claudeStatus. The counters live in memory: after a node restart a session is hookless until its
// next hook event, which is the safe direction (the heuristics decide meanwhile).

// Tunables of the judgement.
const (
	// hookStopGrace: how long after a Stop the transcript may still be written by the turn that Stop ended (the closing
	// lines are flushed around the hook); only a write later than this is a new turn the hooks did not announce.
	hookStopGrace = 5 * time.Second
	// hookQuietWaiting: an open turn whose transcript has been quiet this long while Claude's title shows its waiting
	// mark (✳) and the host says it is not working was interrupted: Esc fires no Stop.
	hookQuietWaiting = 8 * time.Second
	// hookQuietUnknown: the same without a title to tell (an older Claude, a title the user set): longer, since a tool
	// call can be silent for a while.
	hookQuietUnknown = 2 * claudeFresh
)

// hookTurn is what a session's hooks have told the node since it started.
type hookTurn struct {
	prompts   int       // UserPromptSubmit events
	completed int       // prompts as of the last Stop: the turns that were answered
	stopped   bool      // a Stop was seen
	promptAt  time.Time // the last UserPromptSubmit
	stopAt    time.Time // the last Stop
	pid       int       // the claude process the hooks run under, 0 when unknown
	start     string    // that process' start ("linux:<starttime>", "windows:<creation time>"), "" when the platform cannot tell
	bypass    bool      // that process can be switched to Allow All while it runs (claudeCanBypass), read once per process
	touched   time.Time
}

// seen: the hooks have said something about a turn, so they decide (SessionStart and Notification alone say nothing).
func (t hookTurn) seen() bool { return t.prompts > 0 || t.stopped }

// open: a prompt came that no Stop has answered.
func (t hookTurn) open() bool { return t.prompts > t.completed }

// hookTurns keeps hookTurn per session. The zero value and a nil pointer are both usable (a node built without it
// has no hook state).
type hookTurns struct {
	mu sync.Mutex
	m  map[string]*hookTurn
}

// maxHookTurns: when this many sessions are tracked, the ones quiet for hookTurnStale are dropped on the next insert.
// A record is a few words, so the cap is only a bound on a node that sees sessions for months.
const maxHookTurns = 512

// hookTurnStale: a session quiet this long is not coming back to a turn the node still waits on.
const hookTurnStale = 7 * 24 * time.Hour

// entry returns the session's record; callers hold h.mu.
func (h *hookTurns) entry(id string, now time.Time) *hookTurn {
	if h.m == nil {
		h.m = map[string]*hookTurn{}
	}
	t := h.m[id]
	if t == nil {
		if len(h.m) >= maxHookTurns {
			for k, v := range h.m {
				if now.Sub(v.touched) > hookTurnStale {
					delete(h.m, k)
				}
			}
		}
		t = &hookTurn{}
		h.m[id] = t
	}
	t.touched = now
	return t
}

// agent records the claude process the hooks run under. A different process than before is a new run of the
// session (a resume): what the old one counted says nothing about it.
func (h *hookTurns) agent(id string, pid int, start string, now time.Time) {
	if h == nil || pid <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.entry(id, now)
	if t.pid != 0 && (t.pid != pid || t.start != start) {
		*t = hookTurn{touched: now}
	}
	if t.pid == 0 {
		t.bypass = claudeCanBypass(pid) // its arguments, read once (no exec): this runs on every hook
	}
	t.pid, t.start = pid, start
}

// canBypass: the session's claude was started so that Allow All is in its shift+tab cycle (#1379).
func (h *hookTurns) canBypass(id string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.m[id]
	return t != nil && t.bypass
}

// prompt counts a UserPromptSubmit.
func (h *hookTurns) prompt(id string, now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.entry(id, now)
	t.prompts++
	t.promptAt = now
}

// stop counts a Stop: every prompt so far is answered. A Stop first seen after a node restart (nothing counted) still
// says the turn is over.
func (h *hookTurns) stop(id string, now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.entry(id, now)
	t.completed = t.prompts
	t.stopped = true
	t.stopAt = now
}

// get is a copy of the session's record.
func (h *hookTurns) get(id string) (hookTurn, bool) {
	if h == nil {
		return hookTurn{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.m[id]
	if t == nil {
		return hookTurn{}, false
	}
	return *t, true
}

// forget drops the session's record: the process that reported it is gone.
func (h *hookTurns) forget(id string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.m, id)
	h.mu.Unlock()
}

// hookSignals is everything judgeHookTurn weighs for one Claude the host calls done, idle or unknown.
type hookSignals struct {
	turn       hookTurn
	spinner    bool      // the terminal title shows Claude Code's working spinner
	waiting    bool      // the title shows its waiting mark
	hasFile    bool      // there is a transcript
	fileMod    time.Time // its last write
	turnOpen   bool      // its tail has a turn started and not finished
	turnClosed bool      // its tail ends with a turn finished (turn_duration, an interrupt)
	now        time.Time
}

// hookVerdict is what the hooks make of the session.
type hookVerdict int

const (
	hookSilent  hookVerdict = iota // the hooks have no say: the heuristics decide
	hookWorking                    // a turn is running
	hookResting                    // no turn is running: the host's word stands, nothing upgrades it to working
)

// judgeHookTurn decides from the hooks' counters, with the transcript and title only to catch what the hooks cannot
// say:
//
//   - prompts > Stops: a turn is open, so working, unless it ended without a Stop. Esc (and a local command that took
//     a prompt hook but never reaches Stop) fires none, so an open turn ends when the transcript says so (an interrupt
//     line after the prompt), or when it is quiet and nothing shows work: no spinner in the title and the host not
//     working. A title with Claude's waiting mark is proof enough after a few seconds; without one, a tool call can be
//     silent for a while, so it takes longer.
//   - prompts == Stops and a Stop seen: no turn, however a stale spinner in the title reads. Except a turn the hooks
//     did not announce (a background task or a /loop waking Claude, a Stop hook that sent it on): the transcript has an
//     open turn written after the Stop, then the heuristics of claudeStatus apply to it.
//   - nothing counted yet: silent.
func judgeHookTurn(s hookSignals) hookVerdict {
	t := s.turn
	if !t.seen() {
		return hookSilent
	}
	if t.open() {
		if s.hasFile && s.turnClosed && !s.fileMod.Before(t.promptAt) {
			return hookResting // the turn closed in the transcript after the prompt: finished or interrupted
		}
		if s.spinner {
			return hookWorking
		}
		last := t.promptAt
		if s.hasFile && s.fileMod.After(last) {
			last = s.fileMod
		}
		limit := hookQuietUnknown
		if s.waiting {
			limit = hookQuietWaiting
		}
		if s.now.Sub(last) >= limit {
			return hookResting
		}
		return hookWorking
	}
	if !t.stopped {
		return hookSilent
	}
	if s.hasFile && s.turnOpen && s.fileMod.After(t.stopAt.Add(hookStopGrace)) &&
		(s.spinner || s.now.Sub(s.fileMod) < claudeFresh) {
		return hookWorking
	}
	return hookResting
}

// stopSignature identifies the reply a Stop ended the turn with ("" for none): the hub does not push the same
// completion twice (hub/machine.go, stopSig). Only a hash leaves the hook handler for it; the text itself goes
// as the snippet it always did.
func stopSignature(reply string) string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(reply))
	return hex.EncodeToString(sum[:8])
}

// sameProcess says whether pid is still the process that was seen with that start. Where the start is known it is the
// check against a reused pid (a different program with the old number); otherwise only whether the pid is alive.
func sameProcess(pid int, start string) bool {
	if pid <= 0 {
		return false
	}
	if start != "" {
		if cur := procStartMark(pid); cur != "" || strings.HasPrefix(start, "linux:") || strings.HasPrefix(start, "windows:") {
			return cur == start
		}
	}
	return alive(pid)
}

// processAlive is sameProcess; a variable so tests can say who lives.
var processAlive = sameProcess
