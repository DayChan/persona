package assistant

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// A source's plan (history/plan-<source>.json): the chats its list offers, with their kinds, flags and estimates, and
// the owner's approval. What is read of each chat, and how far back, is not the plan's: the knowledge base's selection
// decides it (kb_selection.go, kb.Selection.ReadChat), per-item settings included, the same as for daily ingestion.
// Regenerating the list from the source updates names, kinds, flags and estimates; a chat not in the list is never
// imported.
//
// Approval records what the selection would read of every chat then (Approved, a fingerprint). A later change that
// reads more (a chat that was not read, a whole chat that was read only around the owner's messages, a kind of
// personal message not looked for, further back), new chats in the list among them, takes the approval back: nothing
// runs until the owner approves again (kb_history_resume). A change that reads less needs no approval.

// historyUnlisted is the kind of a chat the plan did not list but the source's search found the owner's messages in
// (often one the account left): the selection judges it as a group.
const historyUnlisted = "unlisted"

type historyPlan struct {
	Source      string            `json:"source"`
	GeneratedAt time.Time         `json:"generated_at"`
	ApprovedAt  *time.Time        `json:"approved_at,omitempty"`
	Chats       []historyPlanChat `json:"chats"`
	// Approved is what each chat would be read as when the owner approved (by chat id; a chat left out was not read),
	// and ApprovedUnlisted what a chat the search finds outside the list would be.
	Approved         map[string]kb.ReadPlan `json:"approved,omitempty"`
	ApprovedUnlisted *kb.ReadPlan           `json:"approved_unlisted,omitempty"`
}

type historyPlanChat struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Muted     bool   `json:"muted,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Bot       bool   `json:"bot,omitempty"`
	Estimate  int    `json:"estimate"`
	// FiledAs is the source the chat is filed under when it is not the connector's id (HistoryChat.Source).
	FiledAs string `json:"filed_as,omitempty"`
}

// filing is the source the chat's knowledge-base chat is under: the source its per-item setting names too.
func (c historyPlanChat) filing(connector string) string {
	if c.FiledAs != "" {
		return c.FiledAs
	}
	return connector
}

// facts are what the selection judges the chat by. A chat the search found outside the list, and a kind the source
// made up, are judged as a group: never read whole by default, and never outside the selection.
func (c historyPlanChat) facts(connector string) kb.ChatFacts {
	kind := c.Kind
	if !isHistoryKind(kind) {
		kind = connectorwire.HistoryKindGroup
	}
	return kb.ChatFacts{Source: c.filing(connector), ID: c.ID, Kind: kind, Bot: c.Bot, Muted: c.Muted, Collapsed: c.Collapsed, Done: c.Done}
}

func isHistoryKind(kind string) bool {
	for _, k := range connectorwire.HistoryKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// historyNeed is what the import reads of a chat now: the selection's read plan, its days bounded by the source's
// history back_days. Not Read: the chat is not read at all.
func historyNeed(sel *kb.Selection, ret kb.Retention, connector string, c historyPlanChat) kb.ReadPlan {
	rp := sel.ReadChat(ret, c.facts(connector))
	if !rp.Read {
		return kb.ReadPlan{}
	}
	if back := sel.BackDays(connector); back > 0 && back < rp.Days {
		rp.Days = back
	}
	return rp
}

// historyMode is how a chat with need is read: whole (kb.HistoryFull) or only what the personal rules keep
// (kb.HistoryAnchor), as its state and the chat's HistoryMode record.
func historyMode(need kb.ReadPlan) string {
	if need.Whole {
		return kb.HistoryFull
	}
	return kb.HistoryAnchor
}

func historyPlanPath(data, source string) string {
	return filepath.Join(historyDir(data), "plan-"+kb.SafeID(source)+".json")
}

// loadHistoryPlan reads a source's plan; false when there is none.
func loadHistoryPlan(data, source string) (*historyPlan, bool, error) {
	var p historyPlan
	ok, err := readJSONFile(historyPlanPath(data, source), &p)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &p, true, nil
}

func saveHistoryPlan(data string, p *historyPlan) error {
	return writeJSONFile(historyPlanPath(data, p.Source), p)
}

// regenerate lays the source's list over the plan: names, kinds, flags and estimates from the list; chats the list
// no longer has stay as they were (the search may still find them).
func (p *historyPlan) regenerate(chats []connectorwire.HistoryChat, now time.Time) {
	at := map[string]int{}
	for i, c := range p.Chats {
		at[c.ID] = i
	}
	for _, hc := range chats {
		if hc.ID == "" {
			continue
		}
		c := historyPlanChat{ID: hc.ID, Name: hc.Name, Kind: hc.Kind, Muted: hc.Muted, Collapsed: hc.Collapsed, Done: hc.Done, Bot: hc.Bot,
			Estimate: max(hc.Estimate, 0), FiledAs: hc.Source}
		if i, ok := at[hc.ID]; ok {
			p.Chats[i] = c
			continue
		}
		at[c.ID] = len(p.Chats)
		p.Chats = append(p.Chats, c)
	}
	p.GeneratedAt = now.UTC()
}

// addUnlisted adds a chat the plan does not list (a search found the owner's messages in it).
func (p *historyPlan) addUnlisted(id, filedAs string) bool {
	if id == "" || p.chat(id) != nil {
		return false
	}
	p.Chats = append(p.Chats, historyPlanChat{ID: id, Kind: historyUnlisted, FiledAs: filedAs})
	return true
}

func (p *historyPlan) chat(id string) *historyPlanChat {
	for i := range p.Chats {
		if p.Chats[i].ID == id {
			return &p.Chats[i]
		}
	}
	return nil
}

// approve records the approval with what the selection reads now.
func (p *historyPlan) approve(sel *kb.Selection, ret kb.Retention, now time.Time) {
	at := now.UTC()
	p.ApprovedAt = &at
	p.Approved = map[string]kb.ReadPlan{}
	for _, c := range p.Chats {
		if need := historyNeed(sel, ret, p.Source, c); need.Read {
			p.Approved[c.ID] = need
		}
	}
	u := historyNeed(sel, ret, p.Source, historyPlanChat{Kind: historyUnlisted})
	p.ApprovedUnlisted = &u
}

// revoke takes the approval back.
func (p *historyPlan) revoke() {
	p.ApprovedAt, p.Approved, p.ApprovedUnlisted = nil, nil, nil
}

// widened counts the chats the selection now reads more of than the approval saw (a chat new to the plan that is
// read among them), and whether a chat the search finds outside the list would be read more. 0 and false for a plan
// not approved.
func (p *historyPlan) widened(sel *kb.Selection, ret kb.Retention) (int, bool) {
	if p.ApprovedAt == nil {
		return 0, false
	}
	n := 0
	for _, c := range p.Chats {
		need := historyNeed(sel, ret, p.Source, c)
		if !need.Read {
			continue
		}
		if was, ok := p.Approved[c.ID]; !ok || need.Wider(was) {
			n++
		}
	}
	u := historyNeed(sel, ret, p.Source, historyPlanChat{Kind: historyUnlisted})
	unlisted := u.Read && (p.ApprovedUnlisted == nil || u.Wider(*p.ApprovedUnlisted))
	return n, unlisted
}

// historyPlanGroups are the plan's chats by what the selection reads of them, the largest estimate first.
type historyPlanGroups struct {
	whole, personal, none []historyPlanChat
}

func (p *historyPlan) grouped(sel *kb.Selection, ret kb.Retention) historyPlanGroups {
	var g historyPlanGroups
	for _, c := range p.Chats {
		switch need := historyNeed(sel, ret, p.Source, c); {
		case !need.Read:
			g.none = append(g.none, c)
		case need.Whole:
			g.whole = append(g.whole, c)
		default:
			g.personal = append(g.personal, c)
		}
	}
	for _, list := range [][]historyPlanChat{g.whole, g.personal, g.none} {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Estimate != list[j].Estimate {
				return list[i].Estimate > list[j].Estimate
			}
			return list[i].Name < list[j].Name
		})
	}
	return g
}

// historyRead is a chat the import reads, with what it reads of it.
type historyRead struct {
	chat historyPlanChat
	need kb.ReadPlan
}

// reading are the chats the selection reads anything of.
func (p *historyPlan) reading(sel *kb.Selection, ret kb.Retention) []historyRead {
	var out []historyRead
	for _, c := range p.Chats {
		if need := historyNeed(sel, ret, p.Source, c); need.Read {
			out = append(out, historyRead{chat: c, need: need})
		}
	}
	return out
}

// wantsUnlisted: a chat the source's search finds that the plan does not list would be read (as a group), so the
// anchors are worth gathering even without a personal chat in the plan.
func historyWantsUnlisted(sel *kb.Selection, ret kb.Retention, connector string) bool {
	return historyNeed(sel, ret, connector, historyPlanChat{Kind: historyUnlisted}).Read
}

// historySelection is the selection the engine goes by (its defaults when it has none).
func historySelection(e *kb.Engine) (*kb.Selection, kb.Retention) {
	if e.Selection != nil {
		if s := e.Selection(); s != nil {
			return s, e.Retention()
		}
	}
	d := kb.Defaults()
	return &d, e.Retention()
}
