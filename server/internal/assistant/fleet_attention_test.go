package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
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

// fixtureInputs are a case's agents as fleetInputs makes them, muted by the push's rule (mutedAs), and its cards, every
// one shown (the web app gets a card only once the apps are told of it).
func fixtureInputs(t *testing.T, c attentionCase) ([]fleetInput, []proto.Interaction, map[string]bool) {
	s := newFleetTestNoStore(t).s
	setMutes(s, c.Input.Mutes)
	var agents []fleetInput
	for _, sess := range c.Input.Sessions {
		agents = append(agents, fleetInput{Machine: "Mac", Session: sess, Muted: s.mutedAs(sess.MachineID, sess.SessionID, sess.SpaceName)})
	}
	return agents, c.Input.Interactions, allShown(c.Input.Interactions)
}

// allShown is every card of ixs as shown to the apps.
func allShown(ixs []proto.Interaction) map[string]bool {
	shown := map[string]bool{}
	for _, ix := range ixs {
		shown[ix.ID] = true
	}
	return shown
}

// The summary lists and counts what the web badge counts (quiet tasks and the assistant left out), one item per agent,
// newest first; an item notifies as the push would (not muted, not waiting only on the assistant's decision).
func TestLinkAttentionFollowsTheSharedFixtures(t *testing.T) {
	for _, c := range readAttentionCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			agents, ixs, shown := fixtureInputs(t, c)
			got := linkAttention("en", agents, ixs, shown)
			if got.NeedsYou != c.Expected.NeedsYou || got.More != c.Expected.More || got.Rev != "" || got.WebURL != "" {
				t.Fatalf("needs_you %d more %d rev %q web %q; want %d %d", got.NeedsYou, got.More, got.Rev, got.WebURL,
					c.Expected.NeedsYou, c.Expected.More)
			}
			items := []link.AttentionItem{}
			for _, it := range got.Items {
				it.Title, it.Place, it.Detail = "", "", "" // texts are the other tests'
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
	agents, ixs, _ := fixtureInputs(t, named("badge_rules"))
	agents = slices.DeleteFunc(agents, func(a fleetInput) bool { return a.Session.Quiet }) // as fleetInputs does
	st := buildFleet(agents, ixs)
	var ids []string
	for _, it := range st.Items {
		ids = append(ids, it.ID)
	}
	if st.NeedsYou != 7 || strings.Join(ids, " ") != "mac/s-perm mac/s-assistant mac/s-looking" {
		t.Fatalf("Live Activity: needs you %d, items %v", st.NeedsYou, ids)
	}
	if agents, ixs, _ := fixtureInputs(t, named("codex_subagent_counts")); buildFleet(agents, ixs).NeedsYou != 0 {
		t.Fatal("Live Activity counts a Codex sub-agent")
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
	a := linkAttention("en", agents, ixs, allShown(ixs))
	if a.NeedsYou != 23 || a.More != 3 || len(a.Items) != link.AttentionMaxItems {
		t.Fatalf("needs_you %d more %d items %d", a.NeedsYou, a.More, len(a.Items))
	}
	it := a.Items[0]
	if utf8.RuneCountInString(it.Title) != linkTextMax || utf8.RuneCountInString(it.Place) != linkTextMax ||
		utf8.RuneCountInString(it.Detail) != linkDetailMax || !strings.HasSuffix(it.Title, "…") || it.Since != "2026-11-09T07:00:00Z" {
		t.Fatalf("item %+v", it)
	}
	exact := strings.Repeat("t", linkTextMax)
	if it := linkItem("en", needer{agent: fleetInput{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: "s",
		Title: exact}}}); it.Title != exact {
		t.Fatalf("a title of exactly %d runes was cut: %q", linkTextMax, it.Title)
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
	a := linkAttention("en", agents, ixs, allShown(ixs))
	var got []string
	for _, it := range a.Items {
		got = append(got, it.SessionID)
	}
	if a.NeedsYou != 23 || a.More != 3 || len(got) != link.AttentionMaxItems || got[0] != "s-dialog" || got[1] != "s-21" ||
		got[19] != "s-03" || slices.Contains(got, "s-02") || a.Items[0].Since != "2026-11-09T08:00:00Z" {
		t.Fatalf("needs_you %d more %d items %v", a.NeedsYou, a.More, got)
	}
}

// fleetInputs reads the sessions, then the cards. A card answered in between leaves a row that counts a card no
// longer read: listed as the web badge counts it, but not notified, as no terminal dialog waits there. A card raised in
// between and not decided on yet does not notify either. The Live Activity shows the first as a dialog, as it did.
func TestLinkAttentionCardsReadApartFromTheirRows(t *testing.T) {
	answered := []fleetInput{{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateNeedsYou,
		PendingInteractions: 1}}}
	if a := linkAttention("en", answered, nil, nil); a.NeedsYou != 1 || a.Items[0].Kind != proto.IxScreen || a.Items[0].Notify {
		t.Fatalf("a card answered between the reads: %+v", a)
	}
	if st := buildFleet(answered, nil); st.NeedsYou != 1 || st.Items[0].Kind != proto.IxScreen {
		t.Fatalf("Live Activity %+v", st)
	}
	raised := []fleetInput{{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateNeedsYou}}}
	ixs := []proto.Interaction{{ID: "ix-1", MachineID: "mac", SessionID: "s-1", Kind: proto.IxPermission, CreatedAt: time.Now()}}
	if a := linkAttention("en", raised, ixs, nil); a.NeedsYou != 1 || a.Items[0].InteractionID != "ix-1" || a.Items[0].Notify {
		t.Fatalf("a card raised between the reads: %+v", a)
	}
}

// The card that stands for an agent is its oldest one handed to the user, whatever came before it; only an agent
// with none is listed by a card it does not notify for (one being decided on), and it notifies once one is handed.
func TestLinkAttentionStandsByTheHandedCard(t *testing.T) {
	at := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	agents := []fleetInput{{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateNeedsYou}}}
	deciding := proto.Interaction{ID: "ix-deciding", MachineID: "mac", SessionID: "s-1", Kind: proto.IxQuestion, CreatedAt: at, Delegating: true}
	undecided := proto.Interaction{ID: "ix-undecided", MachineID: "mac", SessionID: "s-1", Kind: proto.IxPlan, CreatedAt: at.Add(time.Minute)}
	handed := proto.Interaction{ID: "ix-handed", MachineID: "mac", SessionID: "s-1", Kind: proto.IxPermission, CreatedAt: at.Add(2 * time.Minute)}
	for _, tc := range []struct {
		ixs    []proto.Interaction
		shown  map[string]bool
		stands string
		since  string
		notify bool
	}{
		{[]proto.Interaction{deciding, undecided, handed}, map[string]bool{"ix-deciding": true, "ix-handed": true}, "ix-handed", "07:02", true},
		{[]proto.Interaction{deciding, undecided}, map[string]bool{"ix-deciding": true}, "ix-deciding", "07:00", false},
		{[]proto.Interaction{undecided}, nil, "ix-undecided", "07:01", false},
		{[]proto.Interaction{undecided}, map[string]bool{"ix-undecided": true}, "ix-undecided", "07:01", true},
	} {
		a := linkAttention("en", agents, tc.ixs, tc.shown)
		it := a.Items[0]
		if a.NeedsYou != 1 || it.InteractionID != tc.stands || it.Since != "2026-11-09T"+tc.since+":00Z" || it.Notify != tc.notify {
			t.Errorf("cards %v shown %v: %+v; want %s since %s notify %v", len(tc.ixs), tc.shown, it, tc.stands, tc.since, tc.notify)
		}
	}
}

// The same agents and cards read in any order give the same summary, so a look that changes nothing sends nothing: cards
// of the same time stand by id, items of the same time go by machine and session.
func TestLinkAttentionSameInAnyOrder(t *testing.T) {
	at := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	var agents []fleetInput
	var ixs []proto.Interaction
	for i := range 23 {
		sid := fmt.Sprintf("s-%02d", i)
		agents = append(agents, fleetInput{Machine: "Mac", Session: proto.Session{MachineID: "mac", SessionID: sid, State: proto.StateNeedsYou}})
		for j := range 2 { // two cards of the same time each
			ixs = append(ixs, proto.Interaction{ID: fmt.Sprintf("ix-%s-%d", sid, j), MachineID: "mac", SessionID: sid, Kind: proto.IxPermission, CreatedAt: at})
		}
	}
	want := linkAttention("en", agents, ixs, allShown(ixs))
	if want.More != 3 || want.Items[0].InteractionID != "ix-s-00-0" {
		t.Fatalf("summary %+v", want)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		a, x := slices.Clone(agents), slices.Clone(ixs)
		rng.Shuffle(len(a), func(i, j int) { a[i], a[j] = a[j], a[i] })
		rng.Shuffle(len(x), func(i, j int) { x[i], x[j] = x[j], x[i] })
		if got := linkAttention("en", a, x, allShown(ixs)); !reflect.DeepEqual(got, want) {
			t.Fatalf("another order gave another summary:\n%+v\nwant\n%+v", got, want)
		}
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
		agents, ixs, shown := s.fleetInputs()
		return linkAttention("en", agents, ixs, shown)
	}
	if a := summary(); a.NeedsYou != 0 || a.Items == nil {
		t.Fatalf("before a card %+v; want an empty list", a)
	}
	putPending(s, &pending{ix: proto.Interaction{ID: "ix-1", MachineID: "mac", SessionID: "s-1", Kind: proto.IxPermission,
		ToolName: "Bash", Summary: "npm test", CreatedAt: time.Now()}})
	if a := summary(); a.NeedsYou != 1 || a.Items[0].Notify {
		t.Fatalf("a card not shown yet notifies: %+v", a)
	}
	s.markShown("ix-1")
	a := summary()
	if a.NeedsYou != 1 || a.Items[0] != (link.AttentionItem{MachineID: "mac", SessionID: "s-1", Title: "Fix the login redirect",
		Place: "Mac", Kind: proto.IxPermission, Detail: "npm test", Notify: true, InteractionID: "ix-1", Since: a.Items[0].Since,
		Route: "/s/mac/s-1"}) ||
		a.Items[0].Since == "" {
		t.Fatalf("after a card %+v", a)
	}
}

// gatedMem is the fleet's in-memory store whose interaction writes wait for gate (or their deadline): a slow database.
type gatedMem struct {
	*fleetMem
	gate chan struct{}
}

func (m *gatedMem) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	select {
	case <-m.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return m.fleetMem.InsertInteraction(ctx, ix)
}

// attentionFlow is a manager whose interaction writes wait for gate and whose assistant decides on a question when
// told (decided), with liveLoop telling its devices (d) the summary, and one blocked agent on "mac" (s1).
type attentionFlow struct {
	s       *Service
	d       *fleetDevs
	gate    chan struct{}
	decided chan DelegateResult
}

func newAttentionFlow(t *testing.T, delegate bool) *attentionFlow {
	t.Helper()
	f, d := newFakeHub(), newFleetDevs()
	d.add("mac", "Mac")
	st := &gatedMem{fleetMem: newFleetMem(), gate: make(chan struct{})}
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d})
	f.hello.Agents.Delegate.Enabled = &delegate
	list := []proto.Session{{SessionID: "s1", Title: "修登录", Cwd: "/code/app", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()}}
	d.setSessions("mac", list)
	f.setSessions("mac", list...)
	af := &attentionFlow{s: s, d: d, gate: st.gate, decided: make(chan DelegateResult)}
	s.delegateHook = func(ctx context.Context, _ DelegateParams) (DelegateResult, error) {
		select {
		case r := <-af.decided:
			return r, nil
		case <-ctx.Done():
			return DelegateResult{}, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.liveLoop(ctx)
	return af
}

// raise has a hook raise ix and wait for its answer (until the test ends).
func (af *attentionFlow) raise(t *testing.T, ix proto.Interaction) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _, _ = af.s.waitInteraction(ctx, ix) }()
	waitUntil(t, func() bool { _, ok := af.s.pendingIx(ix.ID); return ok })
}

// now has liveLoop look now and returns how the summary it hands over lists card id.
func (af *attentionFlow) now(t *testing.T, id string) link.AttentionItem {
	t.Helper()
	before := len(af.d.handedAttention())
	af.s.liveChanged(true)
	var it link.AttentionItem
	waitUntil(t, func() bool {
		all := af.d.handedAttention()
		if len(all) <= before || len(all[len(all)-1].Items) != 1 {
			return false
		}
		it = all[len(all)-1].Items[0]
		return it.InteractionID == id
	})
	return it
}

// notifiedBy waits for a summary that notifies the one item, and checks none before index from did.
func (af *attentionFlow) notifiedBy(t *testing.T, from int) {
	t.Helper()
	waitUntil(t, func() bool {
		all := af.d.handedAttention()
		a := all[len(all)-1]
		return len(a.Items) == 1 && a.Items[0].Notify
	})
	for i, a := range af.d.handedAttention()[:from] {
		if len(a.Items) > 0 && a.Items[0].Notify {
			t.Fatalf("summary %d notified too early: %+v", i, a)
		}
	}
}

// A question goes the whole way a hook's card goes (waitInteraction: registered, recorded, announced, handed to the
// assistant, handed to the user) with liveLoop telling the devices all along: Persona Link's summary lists it from the
// start but notifies it only once it is the user's, as the phone is pushed only then. Before announce decided (the
// database write is slow) and while the assistant decides, it does not notify.
func TestLinkAttentionNotifiesOnlyOnceTheQuestionIsTheUsers(t *testing.T) {
	af := newAttentionFlow(t, true)
	af.raise(t, delegateQuestion("q1"))
	if it := af.now(t, "q1"); it.Notify {
		t.Fatalf("notifies before announce decided on it: %+v", it)
	}
	close(af.gate) // the write goes through: announce hands the question to the assistant
	waitUntil(t, func() bool { ix, ok := af.s.pendingIx("q1"); return ok && ix.Delegating })
	if it := af.now(t, "q1"); it.Notify {
		t.Fatalf("notifies while the assistant decides: %+v", it)
	}
	from := len(af.d.handedAttention())
	af.decided <- DelegateResult{} // the assistant leaves it to the user: tell pushes it
	af.notifiedBy(t, from)
}

// A permission card is not the assistant's to decide: it notifies as soon as announce tells the apps of it (and the
// phone is pushed), not while it is still being recorded.
func TestLinkAttentionNotifiesACardOnceAnnounced(t *testing.T) {
	af := newAttentionFlow(t, false)
	ix := proto.Interaction{ID: "p1", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, ToolName: "Bash", Summary: "npm test",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	af.raise(t, ix)
	if it := af.now(t, "p1"); it.Notify {
		t.Fatalf("notifies before announce decided on it: %+v", it)
	}
	from := len(af.d.handedAttention())
	close(af.gate)
	af.notifiedBy(t, from)
}
