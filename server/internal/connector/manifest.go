package connector

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Capability is something a connector declares it does (Manifest.Capabilities). The hub's pipes go by the
// declaration: a capability's interface is looked for (As) only when the manifest declares it.
type Capability string

// Capabilities and the interface each stands for. The names are the ones GET /api/v1/connectors lists (except
// CapSettle, CapGroup, CapMeta, CapFallback and CapRecall, which only the hub uses). CapFallback is a promise, not an
// interface: every inbox thing carries a fallback tier (ThingExtra.RuleTier), so its items never need the model to come in
// (docs/extensions/extensions.md). CapRecall is a declaration with no interface to check: a program's connector says it
// can take back what it sent (connector.recall), and the hub offers it a recall only then (RecallerOf); a built-in
// connector that can recall just implements Recaller.
const (
	CapStatus   Capability = "status"   // Accounts
	CapTools    Capability = "tools"    // ReadTools
	CapCatalog  Capability = "catalog"  // Catalog
	CapActions  Capability = "actions"  // Actions
	CapEvents   Capability = "events"   // Events
	CapInbox    Capability = "inbox"    // Inbox
	CapSettle   Capability = "settle"   // InboxSettler
	CapArchive  Capability = "archive"  // Archive
	CapChats    Capability = "chats"    // Chats
	CapItems    Capability = "items"    // Items
	CapLogin    Capability = "login"    // Login
	CapOptions  Capability = "options"  // Options
	CapThreads  Capability = "threads"  // Threads
	CapSnapshot Capability = "snapshot" // Snapshot
	CapGroup    Capability = "group"    // Grouper
	CapMeta     Capability = "meta"     // EventWriter (with CapGroup and MetaDescription)
	CapFallback Capability = "fallback" // (none: every inbox thing carries extra.rule_tier)
	CapMachines Capability = "machines" // Machines
	CapRecall   Capability = "recall"   // (none: a program's connector declares it; Recaller is found by RecallerOf)
	// History import (docs/protocol/connector-protocol.md "History"): CapHistory needs CapArchive, CapHistoryAnchors
	// needs CapHistory.
	CapHistory        Capability = "history"         // History
	CapHistoryAnchors Capability = "history_anchors" // HistoryAnchors
)

type capability struct {
	cap    Capability
	iface  reflect.Type
	listed bool
}

// capabilities are every capability with its interface, in the order GET /api/v1/connectors lists them ("hosts"
// goes after "events": it is listed when Manifest.Hosts has any).
var capabilities = []capability{
	{CapStatus, reflect.TypeFor[Accounts](), true},
	{CapTools, reflect.TypeFor[ReadTools](), true},
	{CapCatalog, reflect.TypeFor[Catalog](), true},
	{CapActions, reflect.TypeFor[Actions](), true},
	{CapEvents, reflect.TypeFor[Events](), true},
	{CapInbox, reflect.TypeFor[Inbox](), true},
	{CapSettle, reflect.TypeFor[InboxSettler](), false},
	{CapArchive, reflect.TypeFor[Archive](), true},
	{CapChats, reflect.TypeFor[Chats](), true},
	{CapItems, reflect.TypeFor[Items](), true},
	{CapLogin, reflect.TypeFor[Login](), true},
	{CapOptions, reflect.TypeFor[Options](), true},
	{CapThreads, reflect.TypeFor[Threads](), true},
	{CapSnapshot, reflect.TypeFor[Snapshot](), true},
	{CapGroup, reflect.TypeFor[Grouper](), false},
	{CapMeta, reflect.TypeFor[EventWriter](), false},
	{CapFallback, nil, false},
	{CapMachines, reflect.TypeFor[Machines](), false},
	{CapRecall, nil, false},
	{CapHistory, reflect.TypeFor[History](), true},
	{CapHistoryAnchors, reflect.TypeFor[HistoryAnchors](), true},
}

// Manifest is what a connector says it is and does, in one place (docs/extensions/connectors.md "2 · 1"): read whenever the
// hub needs it, so it answers from memory. A program's (connector/proc) is what the program said when it started.
type Manifest struct {
	Capabilities []Capability
	// Hosts are the web hosts its links live on (github.com, or an Enterprise host only the config knows): the app
	// uses them for the link card's icon and "Open in GitHub".
	Hosts []string
	// AgentTools are the tools (patterns, path.Match: "mail_*") the coding agents get too, not only the assistant.
	AgentTools []string
	// Unlisted are the tools (patterns, path.Match) left out of the models' tool lists: a front end of the
	// connector's own calls them by name (Telegram's tg CLI and its skill). It is not a permission: called by name,
	// one runs like any other (an action through the gate), and an agent only gets those AgentTools gives it.
	Unlisted []string
	// ConfirmAlways are the actions (patterns, path.Match: "example_send") the gate never lets a rule allow: whatever
	// connector_gate says, the user confirms them on the phone (Decide). A deny rule still denies. It is a floor the
	// connector sets for its writes, not a permission: the user's own sends (Registry.Run) are not gated at all.
	ConfirmAlways []string
	// Poll is how often the hub asks Archive for changes; 0 is the hub's default.
	Poll time.Duration
	// Kinds are the kinds its inbox things have (InboxThing.Kind): prompts/inbox/kinds/<id>.md tells the models what
	// each means. A thing of a kind not here is still taken, and logged.
	Kinds []string
	// ArchiveSources are other names its archive may file conversations under (ChatBatch.Source), besides its id:
	// one kind of conversation kept apart from the rest (a feed's posts the user is in, besides the chats), or ids
	// the knowledge base had before a connector moved (docs/protocol/connector-protocol.md "Knowledge base"). Same pattern as an id.
	ArchiveSources []string
	// MetaDescription says what its events' meta is, for the model that relates new messages to them: required with
	// CapMeta, refused without it.
	MetaDescription string
	// CLI is the command on the assistant's PATH that is this connector's command line (Telegram's tg): the node
	// writes it to run `personad tool --cli <id>`, which has the connector's <id>_cli tool read each command line and
	// calls the tool it names, through the gate (docs/protocol/connector-protocol.md "Command line"). Empty: none.
	CLI string
	// Proxy: it answers for a program or a machine (connector/proc), so it implements every interface and has only
	// what it declares. Check holds the others to "declared exactly what it implements".
	Proxy bool
}

// Has says whether the manifest declares cp.
func (m Manifest) Has(cp Capability) bool { return slices.Contains(m.Capabilities, cp) }

// Known says whether this hub has capability cp (Check refuses a connector declaring one it does not).
func Known(cp Capability) bool {
	return slices.ContainsFunc(capabilities, func(x capability) bool { return x.cap == cp })
}

// ForAgents says whether the coding agents get the tool name too.
func (m Manifest) ForAgents(name string) bool {
	return slices.ContainsFunc(m.AgentTools, matching(name))
}

// Listed says whether the tool name goes in the models' tool lists (Unlisted).
func (m Manifest) Listed(name string) bool { return !slices.ContainsFunc(m.Unlisted, matching(name)) }

// AlwaysConfirms says whether the action name is one the gate always has the user confirm (ConfirmAlways).
func (m Manifest) AlwaysConfirms(name string) bool {
	return slices.ContainsFunc(m.ConfirmAlways, matching(name))
}

// matching is whether a pattern (path.Match) matches name.
func matching(name string) func(string) bool {
	return func(p string) bool { ok, err := path.Match(p, name); return err == nil && ok }
}

// As is c as the capability interface T, when its manifest declares that capability: the one way the hub's pipes
// find a capability (a proxy implements every interface). T must be one of the capabilities' interfaces.
func As[T any](c Connector) (T, bool) {
	var zero T
	t := reflect.TypeFor[T]()
	i := slices.IndexFunc(capabilities, func(x capability) bool {
		return x.iface == t
	})
	if i < 0 {
		panic("connector.As: " + t.String() + " is not a capability")
	}
	if c == nil || !c.Manifest().Has(capabilities[i].cap) {
		return zero, false
	}
	v, ok := c.(T)
	return v, ok
}

// Check is what Registry.Add holds a connector's manifest to: every capability known and declared once, each
// declared one implemented, and (unless it is a proxy) each implemented one declared; kinds only with an inbox.
func Check(c Connector) error {
	m := c.Manifest()
	var errs []error
	for i, cp := range m.Capabilities {
		if !Known(cp) {
			errs = append(errs, fmt.Errorf("unknown capability %q", cp))
		} else if slices.Index(m.Capabilities, cp) != i {
			errs = append(errs, fmt.Errorf("capability %q declared twice", cp))
		}
	}
	impl := reflect.TypeOf(c)
	for _, x := range capabilities {
		if x.iface == nil { // a promise: nothing to implement
			continue
		}
		has, declared := impl.Implements(x.iface), m.Has(x.cap)
		switch {
		case declared && !has:
			errs = append(errs, fmt.Errorf("declares %q but is no %s", x.cap, x.iface.Name()))
		case has && !declared && !m.Proxy:
			errs = append(errs, fmt.Errorf("is a %s but does not declare %q", x.iface.Name(), x.cap))
		}
	}
	if len(m.Kinds) > 0 && !m.Has(CapInbox) {
		errs = append(errs, errors.New("declares inbox kinds without an inbox"))
	}
	if m.Has(CapGroup) && !m.Has(CapInbox) {
		errs = append(errs, errors.New("declares group without an inbox"))
	}
	if m.Has(CapMeta) && !m.Has(CapGroup) {
		errs = append(errs, errors.New("declares meta without group"))
	}
	if m.Has(CapMeta) && strings.TrimSpace(m.MetaDescription) == "" {
		errs = append(errs, errors.New("declares meta without a meta_description"))
	}
	if !m.Has(CapMeta) && m.MetaDescription != "" {
		errs = append(errs, errors.New("has a meta_description without declaring meta"))
	}
	if m.Has(CapFallback) && !m.Has(CapInbox) {
		errs = append(errs, errors.New("declares fallback without an inbox"))
	}
	if m.Has(CapHistory) && !m.Has(CapArchive) {
		errs = append(errs, errors.New("declares history without an archive"))
	}
	if m.Has(CapHistoryAnchors) && !m.Has(CapHistory) {
		errs = append(errs, errors.New("declares history_anchors without history"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("connector %s: %w", c.ID(), err)
	}
	return nil
}

// Capabilities are the names GET /api/v1/connectors lists for c: what its manifest declares, in a fixed order, with
// "hosts" when it has any.
func Capabilities(c Connector) []string {
	m := c.Manifest()
	out := []string{}
	for _, x := range capabilities {
		if x.listed && m.Has(x.cap) {
			out = append(out, string(x.cap))
		}
		if x.cap == CapEvents && len(m.Hosts) > 0 {
			out = append(out, "hosts")
		}
	}
	return out
}

// CheckKinds holds a connector's inbox kinds to what tells the models about them (prompts/inbox/kinds/<id>.md):
// describe renders the description of a kind. Each declared kind must read differently from the others, so at most
// one reads as the template's fallback (its else) and a kind the template does not know shows up as a twin of it.
func CheckKinds(kinds []string, describe func(kind string) string) error {
	seen := map[string]string{}
	var errs []error
	for _, k := range kinds {
		d := describe(k)
		if other, ok := seen[d]; ok {
			errs = append(errs, fmt.Errorf("kinds %q and %q read the same: %q", other, k, d))
		}
		seen[d] = k
	}
	return errors.Join(errs...)
}
