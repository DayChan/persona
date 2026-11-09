package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

var validID = lazyre.New(`^[a-z][a-z0-9]*$`)

// Ask asks the user on the phone to allow a write (the hub's approval card) and says whether they did.
type Ask func(ctx context.Context, summary string, input json.RawMessage) bool

// Registry holds the hub's connectors, in the order added, and the write gate's rules.
type Registry struct {
	mu   sync.RWMutex
	list []Connector
	// rules are the hand-written ones (hub.json connector_gate), read once at start; user are the rules the user made
	// from the ledger (SetUserRules), which can change while it runs and come after them.
	rules []Rule
	user  []Rule
	off   map[string]bool // switched off by the user (SetEnabled)
	// feedsOff are the feeds the user turned off, by connector (SetFeed).
	feedsOff map[string]map[Feed]bool
	// run is what Start was given, to start a connector switched on later; stop cancels a running one's Run / Watch.
	run  *starter
	stop map[string]context.CancelFunc
	// sent is told of every gated write that went out (SetSent).
	sent func(ctx context.Context, s Sent)
}

type starter struct {
	ctx     context.Context
	changed func(id string, scope Scope)
	logf    func(string, ...any)
	backoff watchBackoff
}

// NewRegistry holds no connector yet; rules are the write gate's (hub.json connector_gate, ValidateRules).
func NewRegistry(rules []Rule) *Registry {
	return &Registry{rules: rules, off: map[string]bool{}, feedsOff: map[string]map[Feed]bool{}, stop: map[string]context.CancelFunc{}}
}

// SetUserRules sets the rules the user made from the ledger ("don't ask again in this chat", ChatRuleFor), replacing
// the ones set before, at once and without a restart: the next write is decided by them. They come after the
// hand-written rules given to NewRegistry, so a hand-written deny or confirm that matches still wins, and an allow of
// theirs is clamped to a card as any allow is (Manifest.ConfirmAlways, ConfirmEach). A rule that is not a one-chat
// allow (UserRule.Validate) is left out and named in the error, the rest are set all the same: a rule left out can only
// make the gate ask more. nil or empty leaves the gate as the hand-written rules alone make it.
func (r *Registry) SetUserRules(list []UserRule) error {
	rules, err := userRuleSet(list)
	r.mu.Lock()
	r.user = rules
	r.mu.Unlock()
	return err
}

// UserRules are the rules SetUserRules set, as the gate runs them (do not change them): what the node's Lark gate is
// served too. nil when none are, and for a nil Registry.
func (r *Registry) UserRules() []Rule {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.user
}

// gateRules are the rules a write is decided by: the hand-written ones, then the user's. With no user rule they are the
// hand-written ones as they were given.
func (r *Registry) gateRules() []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.user) == 0 {
		return r.rules
	}
	return slices.Concat(r.rules, r.user)
}

// switchedOff are the connectors switched off, by instance: StatusOf answers StateDisabled for them, so every pipe
// that goes by the status (tools, the inbox, the archive) leaves them alone without knowing about the switch.
var switchedOff sync.Map

// SetEnabled switches a connector on or off. Off, it does not run (its Run and Watch are cancelled), offers no tools
// and brings nothing into the inbox or the knowledge base, and its status is StateDisabled; on, it starts again.
// Signing in and out (Login) still work while it is off. The hub keeps the choice (it calls this before Start).
func (r *Registry) SetEnabled(id string, on bool) error {
	c := r.Get(id)
	if c == nil {
		return fmt.Errorf("no connector %q", id)
	}
	if !reflect.TypeOf(c).Comparable() {
		return fmt.Errorf("connector %q cannot be switched off (not comparable)", id)
	}
	r.mu.Lock()
	var cancel context.CancelFunc
	if on {
		delete(r.off, id)
		switchedOff.Delete(c)
	} else {
		r.off[id] = true
		switchedOff.Store(c, true)
		cancel = r.stop[id]
		delete(r.stop, id)
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if on {
		r.startOne(c)
	}
	return nil
}

// StartOff marks a connector not registered yet as switched off, so adding it after Start does not run it first.
func (r *Registry) StartOff(id string) {
	r.mu.Lock()
	r.off[id] = true
	r.mu.Unlock()
}

// Enabled says whether a connector is switched on (every one is, until SetEnabled says otherwise).
func (r *Registry) Enabled(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.off[id]
}

// Feed is where a connector's things go besides the assistant's tools: each is on until the user turns it off
// (Settings › Connectors' "Add to knowledge base" and "Add to inbox"), while the connector itself keeps running.
type Feed string

const (
	FeedArchive Feed = "archive" // its conversations, items and posts into the knowledge base
	FeedInbox   Feed = "inbox"   // its things into the inbox (Inbox)
)

// feedCaps is the capability a feed needs.
var feedCaps = map[Feed]Capability{FeedArchive: CapArchive, FeedInbox: CapInbox}

// SetFeed turns one of a connector's feeds on or off. The hub keeps the choice (it calls this before Start).
func (r *Registry) SetFeed(id string, f Feed, on bool) error {
	c := r.Get(id)
	if c == nil {
		return fmt.Errorf("no connector %q", id)
	}
	cp, ok := feedCaps[f]
	if !ok {
		return fmt.Errorf("no feed %q", f)
	}
	if !c.Manifest().Has(cp) {
		return fmt.Errorf("%s has no %s", c.Name(), cp)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		delete(r.feedsOff[id], f)
	} else {
		if r.feedsOff[id] == nil {
			r.feedsOff[id] = map[Feed]bool{}
		}
		r.feedsOff[id][f] = true
	}
	return nil
}

// Feeds says whether a connector's feed is on (every one is, until SetFeed says otherwise).
func (r *Registry) Feeds(id string, f Feed) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.feedsOff[id][f]
}

// Add registers a connector; its ID must be new and look like one, its manifest pass Check.
func (r *Registry) Add(c Connector) error {
	id := c.ID()
	if !validID.MatchString(id) {
		return fmt.Errorf("connector id %q: lowercase letters and digits only", id)
	}
	if err := Check(c); err != nil {
		return err
	}
	r.mu.Lock()
	if slices.ContainsFunc(r.list, func(o Connector) bool { return o.ID() == id }) {
		r.mu.Unlock()
		return fmt.Errorf("connector %q is registered twice", id)
	}
	r.list = append(r.list, c)
	r.mu.Unlock()
	r.startOne(c) // added after Start (a plugin installed or switched on): it runs at once, unless switched off
	return nil
}

// Remove stops a connector and takes it out (a plugin switched off or removed). Its switches stay, for when it
// comes back.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	i := slices.IndexFunc(r.list, func(o Connector) bool { return o.ID() == id })
	if i < 0 {
		r.mu.Unlock()
		return
	}
	c := r.list[i]
	r.list = slices.Delete(r.list, i, i+1)
	cancel := r.stop[id]
	delete(r.stop, id)
	r.mu.Unlock()
	if reflect.TypeOf(c).Comparable() {
		switchedOff.Delete(c)
	}
	if cancel != nil {
		cancel()
	}
}

// All are the connectors in the order added.
func (r *Registry) All() []Connector {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.list)
}

// Get is the connector with id, nil when none is registered.
func (r *Registry) Get(id string) Connector {
	for _, c := range r.All() {
		if c.ID() == id {
			return c
		}
	}
	return nil
}

// Owner is the connector a tool name belongs to (its "<ID>_" prefix), nil when none.
func (r *Registry) Owner(tool string) Connector {
	id, _, ok := strings.Cut(tool, "_")
	if !ok {
		return nil
	}
	return r.Get(id)
}

// ToolNames are the names of every read tool and action a connector offers, whatever its status.
func ToolNames(c Connector) []string {
	var out []string
	for _, tl := range allTools(c) {
		out = append(out, tl.Name)
	}
	return out
}

// allTools are c's read tools, then its actions.
func allTools(c Connector) []proto.Tool {
	var out []proto.Tool
	if t, ok := As[ReadTools](c); ok {
		out = append(out, t.Tools()...)
	}
	if a, ok := As[Actions](c); ok {
		out = append(out, a.Actions()...)
	}
	return out
}

// Offers says whether a registered connector offers the tool name (its own: the "<ID>_" prefix and in its list).
// A connector named like the hub's own tools ("lark") does not take those over until it offers them itself.
func (r *Registry) Offers(name string) bool {
	c := r.Owner(name)
	return c != nil && slices.Contains(ToolNames(c), name)
}

// ForAgents says whether the tool name is a connector's that the coding agents get too (Manifest.AgentTools).
func (r *Registry) ForAgents(name string) bool {
	c := r.Owner(name)
	return c != nil && c.Manifest().ForAgents(name)
}

// StatusOf is a connector's status: StateDisabled when the user switched it off; one without a status is always ok.
func StatusOf(ctx context.Context, c Connector) Status {
	if c != nil && reflect.TypeOf(c).Comparable() {
		if _, off := switchedOff.Load(c); off {
			return Status{State: StateDisabled}
		}
	}
	// A proxy's status is always its own: it answers for a program while the program does not run.
	if a, ok := c.(Accounts); ok && (c.Manifest().Has(CapStatus) || c.Manifest().Proxy) {
		return a.Status(ctx)
	}
	return Status{State: StateOK}
}

// Tools are the read tools and the actions of every connector that is ok, in the order added.
func (r *Registry) Tools(ctx context.Context) []proto.Tool {
	return append(r.ToolsFor(ctx, true), r.ToolsFor(ctx, false)...)
}

// ToolsFor are the tools of the connectors that are ok that go to the agents too (agents), or only to the
// assistant (!agents), in the order added; not the unlisted ones (Manifest.Unlisted), which are called by name.
func (r *Registry) ToolsFor(ctx context.Context, agents bool) []proto.Tool {
	var out []proto.Tool
	for _, c := range r.All() {
		if StatusOf(ctx, c).State != StateOK {
			continue
		}
		m := c.Manifest()
		for _, t := range owned(c, allTools(c)) {
			if m.ForAgents(t.Name) == agents && m.Listed(t.Name) {
				out = append(out, t)
			}
		}
	}
	return out
}

// owned drops tools that do not carry the connector's prefix: they would be dispatched to someone else.
func owned(c Connector, tools []proto.Tool) []proto.Tool {
	return slices.DeleteFunc(slices.Clone(tools), func(t proto.Tool) bool { return !strings.HasPrefix(t.Name, c.ID()+"_") })
}

func has(tools []proto.Tool, name string) bool {
	return slices.ContainsFunc(tools, func(t proto.Tool) bool { return t.Name == name })
}

// Call runs the tool name for the AI: a read tool right away, an action only when the gate lets it through — Allow
// runs it, Deny refuses, Confirm runs it once ask says the user allowed it on the phone.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage, ask Ask) proto.ToolResult {
	return r.call(ctx, name, args, ask, true)
}

// Run runs the tool name the user themselves asked for (the app's reply box, sent by their own hand): no gate. The
// gate is there for what the AI writes; asking the user to allow what they just sent would be asking them twice.
func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) proto.ToolResult {
	return r.call(ctx, name, args, nil, false)
}

func (r *Registry) call(ctx context.Context, name string, args json.RawMessage, ask Ask, gated bool) proto.ToolResult {
	c := r.Owner(name)
	if c == nil {
		return proto.TextResult("unknown tool "+name, true)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if st := StatusOf(ctx, c); st.State != StateOK {
		why := st.Error
		if why == "" {
			why = st.State
		}
		kind := proto.ToolErrorUnavailable
		if st.State == StateRateLimited {
			kind = proto.ToolErrorRateLimited
		}
		return proto.ErrorResult(kind, fmt.Sprintf("%s is not available: %s", c.Name(), why), 0)
	}
	if t, ok := As[ReadTools](c); ok && has(t.Tools(), name) {
		return result(t.Call(ctx, name, args))
	}
	a, ok := As[Actions](c)
	if !ok || !has(a.Actions(), name) {
		return proto.TextResult("unknown tool "+name, true)
	}
	var through LetThrough
	if gated {
		res, how := gate(ctx, r.gateRules(), c, a, name, args, ask)
		if res != nil {
			return *res
		}
		through = how
	}
	done, err := Write(ctx, a, name, args)
	if gated && err == nil {
		r.mu.RLock()
		sent := r.sent
		r.mu.RUnlock()
		if sent != nil {
			sent(ctx, Sent{At: time.Now(), Connector: c.ID(), Action: name, Args: args, Text: writtenText(args), Result: done.Text,
				MessageRef: done.MessageRef, Through: through})
		}
	}
	return result(done.Text, err)
}

// result is a tool's answer, or its error: a program's error with code rate_limited (serve.CodeRateLimited) is a
// rate limit, its data's retry_after how long it lasts.
func result(text string, err error) proto.ToolResult {
	if err == nil {
		return proto.TextResult(text, false)
	}
	if re := (*rpc.Error)(nil); errors.As(err, &re) && re.Code == proto.ToolErrorRateLimited {
		var data struct {
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(re.Data, &data)
		return proto.ErrorResult(proto.ToolErrorRateLimited, err.Error(), int(math.Ceil(data.RetryAfter)))
	}
	return proto.TextResult(err.Error(), true)
}

// Info is one connector as GET /api/v1/connectors lists it.
type Info struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Account      string     `json:"account,omitempty"`
	Via          string     `json:"via,omitempty"`
	Error        string     `json:"error,omitempty"`
	Guide        []string   `json:"guide,omitempty"`
	LastSync     *time.Time `json:"last_sync,omitempty"`
	LastEvent    *time.Time `json:"last_event,omitempty"`
	Capabilities []string   `json:"capabilities"`
	Hosts        []string   `json:"hosts,omitempty"`
	// Enabled: switched on (SetEnabled); off, Status is "disabled".
	Enabled bool `json:"enabled"`
	// Archive and Inbox: its feeds are on (SetFeed); only there when it has an archive, an inbox.
	Archive *bool `json:"archive,omitempty"`
	Inbox   *bool `json:"inbox,omitempty"`
	// Options are its own settings (Options), as it gives them.
	Options json.RawMessage `json:"options,omitempty"`
}

// MachineOnline tells every connector that depends on a computer that machine id just connected.
func (r *Registry) MachineOnline(id string) {
	for _, c := range r.All() {
		if m, ok := As[Machines](c); ok {
			m.MachineOnline(id)
		}
	}
}

// Infos lists every connector with its status and what it can do.
func (r *Registry) Infos(ctx context.Context) []Info {
	out := []Info{}
	for _, c := range r.All() {
		st := StatusOf(ctx, c)
		info := Info{ID: c.ID(), Name: c.Name(), Status: st.State, Account: st.Account, Via: st.Via, Error: st.Error, Guide: st.Guide,
			LastSync: when(st.LastSync), LastEvent: when(st.LastEvent), Capabilities: Capabilities(c), Enabled: r.Enabled(c.ID())}
		if hosts := c.Manifest().Hosts; len(hosts) > 0 {
			info.Hosts = hosts
		}
		if o, ok := As[Options](c); ok {
			info.Options = o.Options()
		}
		if c.Manifest().Has(CapArchive) {
			info.Archive = new(r.Feeds(c.ID(), FeedArchive))
		}
		if c.Manifest().Has(CapInbox) {
			info.Inbox = new(r.Feeds(c.ID(), FeedInbox))
		}
		out = append(out, info)
	}
	return out
}

func when(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
