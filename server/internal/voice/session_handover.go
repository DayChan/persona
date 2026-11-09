package voice

import (
	"encoding/json"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/prompts"
	"slices"
	"strings"
	"time"
)

// runTool runs a function call in the background and answers it (mu held). ask_persona is a task for the main
// conversation: its call stays open and its one result is the task's answer (docs/lessons/voice.md: a call can wait
// minutes; answering at once had the model speak twice and left the answer to be said out of turn). What writes
// (ask_persona, send_to_agent) waits for the user-speech gate, as it was at the call.
func (c *conversation) runTool(gen int, e event) {
	e.name = compat.Tool(e.name)
	if !slices.Contains(c.toolSet(), e.name) {
		c.logf("call %s: %s is a tool this session did not declare: ignored", firstNonEmpty(e.callID, "-"), e.name)
		return
	}
	at, reply := c.gate.agent, c.reply
	label := toolLabels[e.name]
	if label == "" {
		label = e.name
	}
	switch e.name {
	case "ask_persona", "delegate_task":
		// A handover: protocol 2 reports it with run frames, protocol 1 with a tool line.
		var in struct {
			Task string `json:"task"`
		}
		_ = json.Unmarshal([]byte(e.args), &in)
		if c.version < 2 {
			c.sendApp(map[string]any{"type": "tool", "id": e.callID, "label": label, "done": false})
			c.sendApp(map[string]any{"type": "tool", "id": e.callID, "done": true})
		}
		if e.id != "" {
			c.delegations[e.id] = gen // its result is the call's answer, whether or not the call came with its delegation
		}
		c.handOver(handoverName(e.id, e.callID), strings.TrimSpace(in.Task), e.id, gen, at, reply)
		return
	}
	c.sendApp(map[string]any{"type": "tool", "id": e.callID, "label": label, "done": false})
	delete(c.delegations, e.id) // its delegation is the tool's: the result answers it
	answer := func(out string) {
		// A tool's result goes on its call only (not said once that is gone: the model has moved on).
		c.deliver(&sentWords{words: plainWords(out), delegation: e.id, gen: gen, onCall: true})
		c.sendApp(map[string]any{"type": "tool", "id": e.callID, "done": true})
	}
	if e.name == "send_to_agent" {
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(e.args), &in)
		c.whenSpoken("call "+firstNonEmpty(e.callID, "-")+" (send_to_agent)", at, func(spoken bool) {
			if !spoken {
				answer(prompts.RenderVoiceRefused(prompts.VoiceRefused{Lang: string(c.language()), Code: "unprompted",
					Task: clipRunes(in.Text, taskMost), Agent: true}))
				return
			}
			answer(c.toolOutput(e))
		})
		return
	}
	go func() { answer(c.toolOutput(e)) }()
}

func (c *conversation) toolOutput(e event) string {
	return runTool(c.ctx, c.pl.hub, c.language(), e.name, e.args)
}

// startTask hands a delegation the model made to the main conversation (mu held). Nothing is sent back on the
// delegation until the task's answer: the model has already told the user it handed it over, and anything on the
// delegation has it speak again (docs/lessons/voice.md "Conclusions" 4).
func (c *conversation) startTask(delegationID, text string, at time.Time, reply string) {
	c.handOver(handoverName(delegationID, ""), strings.TrimSpace(text), delegationID, c.delegations[delegationID], at, reply)
}

// handOver gives task to the main conversation once the user-speech gate lets it (gate.go; at is its agent time when
// the model handed this over, reply the model's reply that did), else tells the model it was not sent (mu held; it
// goes on in the background). The answer goes back on delegation, of upstream session gen, while that is open. name
// is what the log calls it.
func (c *conversation) handOver(name, task, delegation string, gen int, at time.Time, reply string) {
	if c.version >= 2 {
		c.handOverBound(name, task, delegation, gen, at, reply)
		return
	}
	if card := c.askingCardLocked(); card != "" { // a question asked by voice: this is its answer (asks.go)
		c.logf("%s: the answer to card %s (%s)", name, card, taskSize(task))
		c.answerCard(name, card, task, delegation, gen, at)
		return
	}
	c.logf("%s: a task for the main conversation (%s)", name, taskSize(task))
	c.whenSaid(name, at, reply, func(spoken bool) {
		// What was said, taken now: the caption of the user's request often comes after the model's call (gate.go).
		c.mu.Lock()
		if task == "" { // the model said nothing of what to do: what the user said
			task = c.lastUserLine()
		}
		recent := c.recent(recentLines)
		c.mu.Unlock()
		r := &voiceRun{Tasks: []string{clipRunes(task, taskMost)}, Delegation: delegation, Gen: gen}
		refuse := func(code string) {
			c.tell(r, plainWords(prompts.RenderVoiceRefused(prompts.VoiceRefused{Lang: string(c.language()), Code: code, Task: r.Tasks[0]})), true, false)
		}
		switch {
		case !spoken:
			refuse("unprompted")
		case task == "":
			c.logf("%s: refused (empty)", name)
			refuse("empty")
		default:
			c.mu.Lock()
			c.tasks++
			id := fmt.Sprintf("d%d", c.tasks)
			c.sendApp(map[string]any{"type": "tool", "id": id, "label": "Persona 主对话处理中", "done": false})
			c.mu.Unlock()
			c.logf("run %s: for %s, in session %d", id, name, sessionNo(gen))
			c.runs.handOver(task, delegation, gen, id, recent)
		}
	})
}

// whenSpoken calls f, in a goroutine, with whether the user-speech gate is open for what the model did when the gate's
// agent time was at: at once when it is, else once it opens within gateWait (the caption of what the user just said
// can come after the model acted on it). The user's own voice since at, by the time the model acted, opens it too: a
// reply that only calls a tool has the user's caption come after the call's result on the measured service (gate.go).
//
// name is what the log calls what waits (its delegation, its call): the gate's decision on it is a line of the log.
func (c *conversation) whenSpoken(name string, at time.Time, f func(spoken bool)) {
	c.waitGate(name, at, false, "", f)
}

// whenSaid is whenSpoken for a handover that is sent with what was said: while the user's caption of what they said
// last is still to come (captionDue), it waits for it too, the gate open or not, within the same gateWait. The
// caption of the request the model acts on comes 0.1–0.4 s after its call on the measured service (#5506); without
// it the message would go without the user's own words. Not once reply, the model's reply that handed this over, was
// over without saying anything: the service holds that caption until the call's result (#5790), so it cannot come.
func (c *conversation) whenSaid(name string, at time.Time, reply string, f func(spoken bool)) {
	c.waitGate(name, at, true, reply, f)
}

func (c *conversation) waitGate(name string, at time.Time, caption bool, reply string, f func(spoken bool)) {
	heard := c.gate.heardSince(at, time.Now()) // mu held: the user's voice by the time the model acted
	go func() {
		began := time.Now()
		deadline := began.Add(gateWait)
		for {
			c.mu.Lock()
			now := time.Now()
			worded := c.gate.openSince(at, now)
			by := c.gate.openedBy(at, now)
			due := caption && c.captionDue()
			held := due && reply != "" && slices.Contains(c.silent, reply)
			c.mu.Unlock()
			open := heard || worded
			if !worded {
				by = "own voice; their caption had not come"
			}
			if held && open {
				due = false
				by += "; the reply that handed it over said nothing, so its caption comes after the result: not waited for"
			}
			if open && !due || !time.Now().Before(deadline) {
				if open {
					c.logf("%s: the user-speech gate let it through (%s) in %v", name, by, logDur(time.Since(began)))
				} else {
					c.logf("%s: the user-speech gate refused it (unprompted) after %v", name, logDur(time.Since(began)))
				}
				f(open)
				return
			}
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
}

// captionDue: the app's audio had the user's voice in it after their last caption (or typed line) came, within
// captionLateMost (mu held): a caption of it is on its way.
func (c *conversation) captionDue() bool {
	v := c.pace.voice
	return !v.IsZero() && v.After(c.gate.user) && time.Since(v) <= captionLateMost
}

// tell gives the model a run's words: on its delegation (that delegation's one commentary) when final and the
// delegation is still open in this upstream session, else as something to say. agent: they hold an agent's words,
// which closes the user-speech gate until the user speaks again.
func (c *conversation) tell(r *voiceRun, w words, final, agent bool) {
	c.mu.Lock()
	gen, delegation := c.gen, ""
	if final && r.Delegation != "" {
		delegation = c.claim(r.Delegation, r.Gen)
		r.Delegation = ""
	}
	if r.ID != "" {
		// What is said of the run now says more than a heartbeat of it still waiting to be said.
		c.queue = slices.DeleteFunc(c.queue, func(s *sentWords) bool { return s.run == r.ID && s.prio == prioHeartbeat })
	}
	c.mu.Unlock()
	c.deliver(&sentWords{words: w, delegation: delegation, gen: gen, agent: agent})
}

// answerOn gives the model s as the one commentary of delegation (of upstream session gen) while that is open in this
// session, else as something to say.
func (c *conversation) answerOn(delegation string, gen int, s *sentWords) {
	c.mu.Lock()
	s.delegation, s.gen = c.claim(delegation, gen), c.gen
	c.mu.Unlock()
	c.deliver(s)
}

// claim takes delegation, of upstream session gen, for its one commentary (mu held): "" when it is not open in this
// session (its session is gone, or it had its commentary).
func (c *conversation) claim(delegation string, gen int) string {
	if g, open := c.delegations[delegation]; delegation != "" && open && g == gen && g == c.gen {
		delete(c.delegations, delegation)
		return delegation
	}
	return ""
}

// toolsDone ends the app's lines of a run.
func (c *conversation) toolsDone(ids []string) {
	for _, id := range ids {
		c.sendApp(map[string]any{"type": "tool", "id": id, "done": true})
	}
}
