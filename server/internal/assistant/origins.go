package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/compat"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Where an assistant conversation comes from (docs/protocol/protocol.md "Where assistant conversations come from", #3264): the user's own (the app,
// a channel's /new) or one the hub opened by itself (the inbox's AI twin, a scheduled task, conversation_new, a
// plugin's /agent). The apps keep the user's in "Recent" and fold the others away. An origin is noted when the hub starts
// the conversation and never changes; conversations from before are backfilled from what the hub can still tell
// (backfillOrigins), and what it can't is the user's.
//
// The origins live in the assistant's store (settings assistant.origins) and go to the hub in the snapshot, like
// the titles.

const originsKey = "assistant.origins"

// The origins (proto.Session.Origin).
const (
	OriginUser      = "user"
	OriginChannel   = "channel"
	OriginInbox     = "inbox"
	OriginTask      = "task"
	OriginSpawned   = "spawned"
	OriginLarkAgent = "lark_agent"
	OriginExtension = "extension"
)

// purposeLarkAgent is the model purpose the Feishu plugin's /agent conversations run under.
const purposeLarkAgent = "lark.agent"

// Who noted an origin: the start itself, or the backfill (which a redo may judge again).
const (
	originByStart    = "start"
	originByBackfill = "backfill"
)

type convOrigin struct {
	MachineID string    `json:"machine_id"`
	Origin    string    `json:"origin"`
	Ref       string    `json:"ref,omitempty"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
}

type originBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*convOrigin // by session id
	// checked: the sessions the backfill has seen since the hub started (an origin it could not save is tried again
	// after a restart, not on every list).
	checked map[string]bool
	running bool // a backfill from a list is on (originsChanged): the next list waits for it
}

// originsLocked reads the book once; b.mu is held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) originsLocked(ctx context.Context) bool {
	b := &s.origins
	if b.loaded {
		return true
	}
	m := map[string]*convOrigin{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, originsKey, &m); err != nil {
			s.logf("load conversation origins: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*convOrigin{}
	}
	b.m, b.loaded = m, true
	if b.checked == nil {
		b.checked = map[string]bool{}
	}
	return true
}

type originVersionStore interface {
	ReadOrigins(context.Context) (json.RawMessage, int64, bool, error)
	CompareOrigins(context.Context, int64, json.RawMessage) (bool, error)
}

// refreshOriginsOnMissLocked reads committed origins after an authorization miss. A read failure never grants access.
func (s *Service) refreshOriginsOnMissLocked(ctx context.Context) bool {
	vs, ok := s.store().(originVersionStore)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, _, found, err := vs.ReadOrigins(ctx)
	if err != nil {
		return false
	}
	current := make(map[string]*convOrigin)
	if found && json.Unmarshal(raw, &current) != nil {
		return false
	}
	if current == nil {
		current = make(map[string]*convOrigin)
	}
	s.origins.m, s.origins.loaded = current, true
	if s.origins.checked == nil {
		s.origins.checked = make(map[string]bool)
	}
	return true
}

// changeOriginsLocked merges one operation into the latest map and publishes it only after persistence.
// The legacy JSON remains an unwrapped session-id map. The caller holds origins.mu.
func (s *Service) changeOriginsLocked(ctx context.Context, change func(map[string]*convOrigin) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, st := &s.origins, s.store()
	for range 8 {
		current := make(map[string]*convOrigin)
		var revision int64
		vs, versioned := st.(originVersionStore)
		if st == nil {
			for id, origin := range b.m {
				current[id] = origin
			}
		} else if versioned {
			raw, rev, found, err := vs.ReadOrigins(ctx)
			if err != nil {
				return err
			}
			revision = rev
			if found && json.Unmarshal(raw, &current) != nil {
				return errors.New("invalid conversation origins")
			}
		} else {
			if _, err := st.Setting(ctx, originsKey, &current); err != nil {
				return err
			}
		}
		if current == nil {
			current = make(map[string]*convOrigin)
		}
		changed := change(current)
		if changed && st != nil {
			if versioned {
				raw, err := json.Marshal(current)
				if err != nil {
					return err
				}
				ok, err := vs.CompareOrigins(ctx, revision, raw)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
			} else if err := st.PutSetting(ctx, originsKey, current); err != nil {
				return err
			}
		}
		b.m, b.loaded = current, true
		if b.checked == nil {
			b.checked = make(map[string]bool)
		}
		return nil
	}
	return errors.New("conversation origins changed repeatedly")
}

// originSet notes where a conversation the hub just started comes from: it replaces whatever the backfill may have
// guessed from the list in between.
func (s *Service) originSet(ctx context.Context, machine, id, origin, ref string) {
	if id == "" {
		return
	}
	if origin == "" {
		origin = OriginUser
	}
	b := &s.origins
	b.mu.Lock()
	changed := false
	// The conversation was already sent; client disconnect must not discard its source.
	err := s.changeOriginsLocked(context.WithoutCancel(ctx), func(current map[string]*convOrigin) bool {
		changed = false
		if o := current[id]; o != nil && o.By == originByStart {
			return false // an established start is never replaced by a stale manager
		}
		current[id] = &convOrigin{MachineID: machine, Origin: origin, Ref: ref, By: originByStart, At: time.Now().UTC()}
		changed = true
		return true
	})
	b.mu.Unlock()
	if err != nil {
		s.logf("save conversation origins: %v", err)
		return
	}
	if changed {
		s.publish(ctx)
	}
}

// recordRetryOrigin stores an observed task start without granting authority on a failed write.
// A different authoritative origin wins over a delayed retry.
func (s *Service) recordRetryOrigin(ctx context.Context, machine, id, origin, ref string) error {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if current := b.m[id]; current != nil && current.By == originByStart &&
		(!s.sameMachine(current.MachineID, machine) || current.Origin != origin || current.Ref != ref) {
		return nil // a newer in-memory start is authoritative too
	}
	return s.changeOriginsLocked(ctx, func(current map[string]*convOrigin) bool {
		if existing := current[id]; existing != nil && existing.By == originByStart {
			return false // never replace another established start
		}
		current[id] = &convOrigin{MachineID: machine, Origin: origin, Ref: ref, By: originByStart, At: time.Now().UTC()}
		return true
	})
}

// retryStartedOrigin is bounded by the manager lifetime and does not extend startup authorization.
func (s *Service) retryStartedOrigin(ctx context.Context, machine, id, origin, ref string) {
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.recordRetryOrigin(attempt, machine, id, origin, ref)
		cancel()
		if err == nil {
			return
		}
		s.logf("retry conversation origin: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

type shownOrigin struct{ origin, ref, machine string }

// conversationOrigins are the origins to show, by session id; false while they could not be read.
func (s *Service) conversationOrigins() (map[string]shownOrigin, bool) {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(context.Background()) {
		return nil, false
	}
	out := make(map[string]shownOrigin, len(b.m))
	for id, o := range b.m {
		out[id] = shownOrigin{o.Origin, o.Ref, o.MachineID}
	}
	return out, true
}

// ---- the backfill ----

// originEvidence is what the hub still knows about the conversations it started before origins were noted.
type originEvidence struct {
	inbox map[string]*InboxItem // the AI twin's conversations, by session id
	tasks map[string]string     // a task's own conversations → the task's id
}

// gatherOriginEvidence reads the inbox's agents and the tasks' conversations (their own, and those in the history).
func (s *Service) gatherOriginEvidence(ctx context.Context) originEvidence {
	ev := originEvidence{inbox: map[string]*InboxItem{}, tasks: map[string]string{}}
	b := &s.inbox
	if b.loaded.Load() {
		b.mu.Lock()
		for _, it := range b.items {
			if it.Agent != nil && it.Agent.SessionID != "" {
				cp := *it
				ag := *it.Agent
				cp.Agent = &ag
				ev.inbox[it.Agent.SessionID] = &cp
			}
		}
		b.mu.Unlock()
	}
	if sc, err := s.lockedSchedule(ctx); err == nil {
		into := map[string]bool{} // conversations tasks send into: the user's, not the task's
		for _, t := range sc.tasks {
			if t.SessionID != "" {
				into[t.SessionID] = true
			}
		}
		for _, t := range sc.tasks {
			if t.RunSessionID != "" {
				ev.tasks[t.RunSessionID] = t.ID
			}
		}
		for _, r := range sc.runs {
			if r.SessionID != "" && !into[r.SessionID] && ev.tasks[r.SessionID] == "" {
				ev.tasks[r.SessionID] = r.RefID
			}
		}
		sc.mu.Unlock()
	}
	return ev
}

// The first messages of the conversations the hub opens (prompts/inbox/agent.md, assistant/relay.md, task_run.md, and
// the Feishu plugin's /agent), as the node lists an untitled conversation: its first message.
var (
	inboxFirst   = "这是收件箱里的一件事"
	relayFirst   = lazyre.New(`^<` + compat.ContextPattern() + ` kind="context" title="来自对话「[^"]*」"[^>]*>\s*这条消息不是用户本人发的，是 Persona 的另一段对话（([A-Za-z0-9._-]+)）`)
	larkAgentRe  = lazyre.New(`^<` + compat.ContextPattern() + ` kind="instructions" title="给 Agent 的说明">[\s\S]*?/agent`)
	taskFirstPfx = "⏰"
)

// guessOrigin is where a conversation the hub noted nothing about comes from, from the evidence and its first
// message (title: the node's, which is the first message for one nobody named; SessionsChanged has the node's own
// list); the user's when nothing tells.
func guessOrigin(id, title, by string, ev originEvidence) (origin, ref string) {
	if it := ev.inbox[id]; it != nil {
		return OriginInbox, it.ID
	}
	if task := ev.tasks[id]; task != "" {
		return OriginTask, task
	}
	switch by {
	case titleByTask:
		return OriginTask, ""
	case titleByInbox:
		return OriginInbox, ""
	}
	raw := strings.TrimSpace(title)
	switch {
	case strings.HasPrefix(raw, inboxFirst):
		return OriginInbox, ""
	case strings.HasPrefix(raw, taskFirstPfx):
		return OriginTask, ""
	}
	if m := relayFirst.FindStringSubmatch(raw); m != nil {
		return OriginSpawned, m[1]
	}
	if larkAgentRe.MatchString(raw) {
		return OriginLarkAgent, ""
	}
	return OriginUser, ""
}

// hubStarted: the titles book says the hub started the conversation (named after its first turn, or about to be),
// which the app's new conversations, conversation_new's, a channel's and a plugin's all are.
func hubStarted(by string) bool {
	return by == titlePending || by == titleAuto || by == titleByAssistant
}

// firstMessage is the text of a conversation's first user message ("" when it has none); false when it can't be read.
func (s *Service) firstMessage(ctx context.Context, machine, id string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "after_seq": 0, "lazy_images": true}, &res); err != nil {
		s.logf("conversation origins: first message of %s: %v", short(id), err)
		return "", false
	}
	for _, it := range res.Items {
		if it.Role == "user" && it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "" {
			return it.Text, true
		}
	}
	return "", true
}

// isAssistantRow: c runs in one of this manager's assistant directories (the hub's Assistant flag on a
// decorated list; the node's own lists only have the directory).
func (s *Service) isAssistantRow(c proto.Session) bool {
	return c.Assistant || c.PaneID == "" && c.Cwd != "" && identity.IsAssistantDirOf(c.Cwd, s.id)
}

// BackfillResult is what one backfill did.
type BackfillResult struct {
	Checked int            `json:"checked"`
	Filled  map[string]int `json:"filled"`
}

// backfillOrigins notes an origin for every assistant conversation of the list that has none (redo: and judges the
// backfill's own again). It never replaces one the start noted, so it can run any number of times.
func (s *Service) backfillOrigins(ctx context.Context, machine string, list []proto.Session, redo bool) (BackfillResult, error) {
	res := BackfillResult{Filled: map[string]int{}}
	b := &s.origins
	b.mu.Lock()
	if !s.originsLocked(ctx) {
		b.mu.Unlock()
		return res, errors.New("conversation origins could not be read")
	}
	var todo []proto.Session
	for _, c := range list {
		if c.SessionID == "" || !s.isAssistantRow(c) {
			continue
		}
		o := b.m[c.SessionID]
		if o != nil && (o.By != originByBackfill || !redo) {
			continue
		}
		if o == nil && !redo && b.checked[c.SessionID] {
			continue
		}
		todo = append(todo, c)
	}
	b.mu.Unlock()
	if len(todo) == 0 {
		return res, nil
	}

	ev := s.gatherOriginEvidence(ctx)
	main := s.settings(ctx).Main
	guesses := make([]originGuess, 0, len(todo))
	for _, c := range todo {
		by, _ := s.titleOf(c.SessionID)
		origin, ref := guessOrigin(c.SessionID, c.Title, by, ev)
		if origin == OriginUser && hubStarted(by) {
			// The node's title has the first message without its context blocks, which is all a relayed message
			// or a /agent request starts with: read the message itself. One that can't be read now is left for the
			// next list rather than taken for the user's.
			first, ok := s.firstMessage(ctx, firstNonEmpty(c.MachineID, machine), c.SessionID)
			if !ok {
				continue
			}
			if first != "" {
				origin, ref = guessOrigin(c.SessionID, first, by, ev)
			}
		}
		if c.SessionID == main {
			origin, ref = OriginUser, ""
		}
		guesses = append(guesses, originGuess{c, origin, ref})
	}

	b.mu.Lock()
	now := time.Now().UTC()
	changed := false
	err := s.changeOriginsLocked(ctx, func(current map[string]*convOrigin) bool {
		changed = false
		res.Filled = make(map[string]int)
		for _, g := range guesses {
			id := g.c.SessionID
			if o := current[id]; o != nil && (o.By != originByBackfill || !redo) {
				continue // another manager or a start recorded it meanwhile
			}
			if o := current[id]; o != nil && (o.Origin == g.origin && o.Ref == g.ref || g.origin == OriginUser) {
				continue
			}
			mid := firstNonEmpty(g.c.MachineID, machine)
			current[id] = &convOrigin{MachineID: mid, Origin: g.origin, Ref: g.ref, By: originByBackfill, At: now}
			res.Filled[g.origin]++
			changed = true
		}
		return changed
	})
	if err == nil {
		for _, g := range guesses {
			b.checked[g.c.SessionID] = true
			res.Checked++
		}
	}
	b.mu.Unlock()
	if err != nil {
		return res, err
	}
	if changed {
		s.backfillInboxTitles(inboxTitles(guesses, ev))
		s.logf("conversation origins: backfilled %d on %s %v", res.Checked, machine, res.Filled)
		s.publish(ctx)
	}
	return res, nil
}

type originGuess struct {
	c           proto.Session
	origin, ref string
}

type inboxTitle struct {
	machine, id, topic string
	started            time.Time
}

// inboxTitles are the titles the AI twin's backfilled conversations get from their items.
func inboxTitles(gs []originGuess, ev originEvidence) []inboxTitle {
	var out []inboxTitle
	for _, g := range gs {
		it := ev.inbox[g.c.SessionID]
		if g.origin != OriginInbox || it == nil {
			continue
		}
		started := g.c.UpdatedAt
		if it.Agent.StartedAt != nil {
			started = *it.Agent.StartedAt
		}
		out = append(out, inboxTitle{machine: g.c.MachineID, id: g.c.SessionID, topic: it.Title, started: started})
	}
	return out
}

// backfillInboxTitles names the AI twin's older conversations after their item, like the new ones (titleFixed): only
// those nobody named.
func (s *Service) backfillInboxTitles(list []inboxTitle) {
	for _, t := range list {
		if by, _ := s.titleOf(t.id); by != "" {
			continue
		}
		s.titleFixed(t.machine, t.id, t.started, titleInbox, t.topic)
	}
}

// originsChanged runs the backfill in the background for a machine's new list (SessionsChanged).
func (s *Service) originsChanged(machine string, list []proto.Session) {
	b := &s.origins
	b.mu.Lock()
	if !s.originsLocked(context.Background()) {
		b.mu.Unlock()
		return
	}
	need := false
	for _, c := range list {
		if c.SessionID != "" && b.m[c.SessionID] == nil && !b.checked[c.SessionID] && s.isAssistantRow(c) {
			need = true
			break
		}
	}
	if !need || b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.mu.Unlock()
	go func() {
		defer func() {
			b.mu.Lock()
			b.running = false
			b.mu.Unlock()
		}()
		// The first run after an upgrade may read the first message of many conversations, one at a time.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := s.backfillOrigins(ctx, machine, list, false); err != nil {
			s.logf("conversation origins: backfill on %s: %v", machine, err)
		}
	}()
}

// originRoutes: POST /api/v1/assistant/origins/backfill runs the backfill over the assistant's machines' lists now.
func (s *Service) originRoutes() {
	s.handle("POST /api/v1/assistant/origins/backfill", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only a full device can run the backfill"))
			return
		}
		var in struct {
			Redo bool `json:"redo"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		machine := s.settings(ctx).MachineID
		list, err := s.sessions(ctx, machine)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		res, err := s.backfillOrigins(ctx, machine, list, in.Redo)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, res)
	})
}
