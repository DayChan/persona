package assistant

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// attentionFixtures is shared with the web app's tests (web/src/lib/attention-fixture.test.ts): the same sessions give
// the summary here and the web badge there one count.
const attentionFixtures = "../../../spec/fixtures/attention/cases.json"

type attentionCase struct {
	Name  string `json:"name"`
	Input struct {
		Sessions     []proto.Session     `json:"sessions"`
		Interactions []proto.Interaction `json:"interactions"`
		Mutes        Pins                `json:"mutes"`
	} `json:"input"`
	Expected struct {
		NeedsYou int                  `json:"needs_you"`
		More     int                  `json:"more"`
		Items    []link.AttentionItem `json:"items"`
	} `json:"expected"`
}

func readAttentionCases(t *testing.T) []attentionCase {
	t.Helper()
	b, err := os.ReadFile(attentionFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []attentionCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("%s: %v (%d cases)", attentionFixtures, err, len(f.Cases))
	}
	return f.Cases
}

// fixtureInputs are a case's agents as fleetInputs makes them, muted by the push's rule (mutedAs).
func fixtureInputs(t *testing.T, c attentionCase) ([]fleetInput, []proto.Interaction) {
	s := newFleetTestNoStore(t).s
	setMutes(s, c.Input.Mutes)
	var agents []fleetInput
	for _, sess := range c.Input.Sessions {
		agents = append(agents, fleetInput{Machine: "Mac", Session: sess, Muted: s.mutedAs(sess.MachineID, sess.SessionID, sess.SpaceName)})
	}
	return agents, c.Input.Interactions
}

// The summary lists and counts what the web badge counts (quiet tasks and the assistant left out), one item per agent,
// newest first; an item notifies as the push would (not muted, not waiting only on the assistant's decision).
func TestLinkAttentionFollowsTheSharedFixtures(t *testing.T) {
	for _, c := range readAttentionCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			agents, ixs := fixtureInputs(t, c)
			got := linkAttention("en", agents, ixs)
			if got.NeedsYou != c.Expected.NeedsYou || got.More != c.Expected.More || got.Rev != "" || got.WebURL != "" {
				t.Fatalf("needs_you %d more %d rev %q web %q; want %d %d", got.NeedsYou, got.More, got.Rev, got.WebURL,
					c.Expected.NeedsYou, c.Expected.More)
			}
			items := []link.AttentionItem{}
			for _, it := range got.Items {
				it.Title, it.Place, it.Detail, it.Since = "", "", "", "" // texts are the other tests'
				items = append(items, it)
			}
			want, _ := json.Marshal(c.Expected.Items)
			if have, _ := json.Marshal(items); string(have) != string(want) {
				t.Fatalf("items\n have %s\n want %s", have, want)
			}
			if got.Items == nil {
				t.Fatal("items is null: nothing needs you is an empty list")
			}
		})
	}
}

// The Live Activity keeps its own count on the same agents (docs/apple/live-activity.md): the assistant's questions and
// the ones it is still deciding on counted, muted agents and Codex sub-agents left out.
func TestLiveActivityKeepsItsOwnCount(t *testing.T) {
	cases := readAttentionCases(t)
	named := func(name string) attentionCase {
		i := slices.IndexFunc(cases, func(c attentionCase) bool { return c.Name == name })
		if i < 0 {
			t.Fatalf("no case %s", name)
		}
		return cases[i]
	}
	agents, ixs := fixtureInputs(t, named("badge_rules"))
	agents = slices.DeleteFunc(agents, func(a fleetInput) bool { return a.Session.Quiet }) // as fleetInputs does
	st := buildFleet(agents, ixs)
	var ids []string
	for _, it := range st.Items {
		ids = append(ids, it.ID)
	}
	if st.NeedsYou != 7 || strings.Join(ids, " ") != "mac/s-perm mac/s-assistant mac/s-looking" {
		t.Fatalf("Live Activity: needs you %d, items %v", st.NeedsYou, ids)
	}
	if st := buildFleet(fixtureInputs(t, named("codex_subagent_counts"))); st.NeedsYou != 0 {
		t.Fatalf("Live Activity counts a Codex sub-agent: %+v", st)
	}
}

// Twenty items at most, the rest counted in more; the texts clipped; the largest summary far below what a post and
// the link take.
func TestLinkAttentionCapsAndClips(t *testing.T) {
	long := strings.Repeat("<", 300) // escaped in JSON: the most bytes a rune can cost there
	var agents []fleetInput
	var ixs []proto.Interaction
	at := time.Date(2026, 11, 9, 7, 0, 0, 123456789, time.UTC)
	for i := range 23 {
		sid := fmt.Sprintf("%064d", i)
		agents = append(agents, fleetInput{Machine: long, Session: proto.Session{MachineID: strings.Repeat("m", 64), SessionID: sid,
			State: proto.StateNeedsYou, Title: long, SpaceName: long, SlotName: long}})
		ixs = append(ixs, proto.Interaction{ID: strings.Repeat("i", 60) + fmt.Sprint(i), MachineID: strings.Repeat("m", 64), SessionID: sid,
			Kind: proto.IxQuestion, CreatedAt: at, Questions: []proto.Question{{Question: long}}})
	}
	a := linkAttention("en", agents, ixs)
	if a.NeedsYou != 23 || a.More != 3 || len(a.Items) != link.AttentionMaxItems {
		t.Fatalf("needs_you %d more %d items %d", a.NeedsYou, a.More, len(a.Items))
	}
	it := a.Items[0]
	if utf8.RuneCountInString(it.Title) != linkTextMax+1 || utf8.RuneCountInString(it.Place) != linkTextMax+1 ||
		utf8.RuneCountInString(it.Detail) != linkDetailMax+1 || it.Since != "2026-11-09T07:00:00Z" {
		t.Fatalf("item %+v", it)
	}
	a.Rev, a.WebURL = strings.Repeat("f", 16), "https://hub.example.com/"+strings.Repeat("p", 200)
	b, _ := json.Marshal(link.Message{Type: "attention", Attention: &a})
	if len(b) >= link.AttentionBodyMax || len(b) >= 1<<20 {
		t.Fatalf("the largest summary is %d bytes: over the post cap %d or the link's read limit", len(b), link.AttentionBodyMax)
	}
}

// The newest first: past the cap the oldest go to more, so an agent that just started waiting is always listed and
// Persona Link can notify it. A card's time counts, else when the agent went "Needs you".
func TestLinkAttentionKeepsTheNewest(t *testing.T) {
	at := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	var agents []fleetInput
	var ixs []proto.Interaction
	for i := range 22 { // cards oldest first, as the fleet keeps them
		sid := fmt.Sprintf("s-%02d", i)
		agents = append(agents, fleetInput{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: sid, State: proto.StateNeedsYou}})
		ixs = append(ixs, proto.Interaction{ID: "ix-" + sid, MachineID: "mac", SessionID: sid, Kind: proto.IxPermission,
			CreatedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	agents = append(agents, fleetInput{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: "s-dialog",
		State: proto.StateNeedsYou, StateSince: at.Add(time.Hour)}}) // a terminal dialog newer than every card
	a := linkAttention("en", agents, ixs)
	var got []string
	for _, it := range a.Items {
		got = append(got, it.SessionID)
	}
	if a.NeedsYou != 23 || a.More != 3 || len(got) != link.AttentionMaxItems || got[0] != "s-dialog" || got[1] != "s-21" ||
		got[19] != "s-03" || slices.Contains(got, "s-02") || a.Items[0].Since != "2026-11-09T08:00:00Z" {
		t.Fatalf("needs_you %d more %d items %v", a.NeedsYou, a.More, got)
	}
}

// An agent whose row still counts a card answered before the cards were read (fleetInputs reads them one after the
// other) is not taken for one waiting in a terminal dialog: no screen item, so Persona Link does not notify it.
func TestLinkAttentionSkipsACardAnsweredBetweenTheReads(t *testing.T) {
	agents := []fleetInput{{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateNeedsYou,
		PendingInteractions: 1}}}
	if a := linkAttention("en", agents, nil); a.NeedsYou != 0 || len(a.Items) != 0 {
		t.Fatalf("summary %+v", a)
	}
	if st := buildFleet(agents, nil); st.NeedsYou != 0 {
		t.Fatalf("Live Activity %+v", st)
	}
}

// The summary of the fleet's own inputs: an agent is "Needs you" by its pending card (its state is worked out from it),
// with the texts the Agents list shows.
func TestLinkAttentionOfTheFleetsInputs(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.d.setSessions("mac", []proto.Session{{SessionID: "s-1", Cwd: "/home/user/project", Title: "Fix the login redirect",
		Status: proto.StatusIdle, UpdatedAt: time.Now()}})
	summary := func() link.Attention {
		agents, ixs := s.fleetInputs()
		return linkAttention("en", agents, ixs)
	}
	if a := summary(); a.NeedsYou != 0 || a.Items == nil {
		t.Fatalf("before a card %+v; want an empty list", a)
	}
	putPending(s, &pending{ix: proto.Interaction{ID: "ix-1", MachineID: "mac", SessionID: "s-1", Kind: proto.IxPermission,
		ToolName: "Bash", Summary: "npm test", CreatedAt: time.Now()}})
	a := summary()
	if a.NeedsYou != 1 || a.Items[0] != (link.AttentionItem{MachineID: "mac", SessionID: "s-1", Title: "Fix the login redirect",
		Place: "Mac", Kind: proto.IxPermission, Detail: "npm test", Notify: true, InteractionID: "ix-1", Since: a.Items[0].Since,
		Route: "/s/mac/s-1"}) ||
		a.Items[0].Since == "" {
		t.Fatalf("after a card %+v", a)
	}
}
