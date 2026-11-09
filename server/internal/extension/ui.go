package extension

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Declarative UI (docs/extensions/extensions.md "Declarative UI"): a plugin never runs code in the apps. It answers ui.render with a View
// — a few fixed kinds of items — and the apps draw it with their own components (each style's, in light and dark);
// a button's action goes back to the plugin (ui.action). The same View is drawn by iOS, Android and the web.

// UIItem is a contribution to ui: a place in the apps a plugin draws.
type UIItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Placement is one of UISlots: extension_page (a page opened from the extension's settings; plugin_page is its old
	// name, the extension is told the one it wrote), launcher_section (a section
	// on the page of a launcher whose gateway is one of Hosts, or of any launcher when there are none: the plugin then
	// answers unsupported for gateways not its own; it may send requests there with the key, as quota does), nav_page,
	// overview_card, capsule_card, machine_section, inbox_item.
	Placement string   `json:"placement"`
	Hosts     []string `json:"hosts,omitempty"`
	// Icon is a nav_page's icon, from UIIcons (each app draws its own); the default when empty.
	Icon string `json:"icon,omitempty"`
	// Opens is the ui (a nav_page or extension_page of the same extension) a compact card or widget opens when
	// tapped.
	Opens string `json:"opens,omitempty"`
	// Connector is the connector an inbox_item draws the items of: one of the plugin's own connectors.
	Connector string `json:"connector,omitempty"`
	// Families are the sizes a widget can be drawn at (WidgetFamilies); RefreshMinutes how often it is drawn again on
	// its own (WidgetRefreshDefault when 0). A widget's only.
	Families       []string `json:"families,omitempty"`
	RefreshMinutes int      `json:"refresh_minutes,omitempty"`
}

// Refresh is how often widget u is drawn again on its own.
func (u UIItem) Refresh() time.Duration {
	return time.Duration(cmp.Or(u.RefreshMinutes, WidgetRefreshDefault)) * time.Minute
}

// UISlot is a place in the apps plugins may draw (docs/extensions/extensions.md "UI slots").
type UISlot struct {
	Name string
	// Size is what the plugin is asked to draw there: compact (a card) or full (a page, a section).
	Size string
	// Context is what the apps say about where it is drawn: launcher, machine, item.
	Context string
	// Badge: the plugin may put a badge (ui.badge) on its entry.
	Badge bool
	// Shown: the user is shown before installing that the plugin draws here (extension-permissions.slot_<Name>), and
	// agrees again when an upgrade adds it (Explain, Digest): it sits among the app's own pages, outside the plugin's
	// settings.
	Shown bool
	// Since is the UI version that brought it.
	Since int
}

// UISlots are the places this hub lets plugins draw, in the order the apps list them.
var UISlots = []UISlot{
	{Name: "plugin_page", Size: SizeFull, Since: 1},
	// extension_page is plugin_page's new name: the same slot (PageSlot).
	{Name: "extension_page", Size: SizeFull, Since: 3},
	{Name: "launcher_section", Size: SizeFull, Context: "launcher", Since: 1},
	{Name: "nav_page", Size: SizeFull, Badge: true, Shown: true, Since: 2},
	{Name: "overview_card", Size: SizeCompact, Shown: true, Since: 2},
	{Name: "capsule_card", Size: SizeCompact, Shown: true, Since: 3},
	{Name: "machine_section", Size: SizeFull, Context: "machine", Shown: true, Since: 2},
	// inbox_item draws the body of an inbox item from the plugin's connector (its own header stays the app's): the
	// same items a plugin's connector brought in, with the hub's triage on them. Drawn with UI version 2's items.
	{Name: "inbox_item", Size: SizeFull, Context: "item", Shown: true, Since: 2},
	// widget is drawn outside the app (docs/extensions/extensions.md "System-level places"): a home screen widget, a Lock Screen or watch
	// face complication. The size it is asked for is the family (FitWidget holds the view to it); the apps list
	// widgets on their own (GET /api/v1/extensions/-/widgets), not with the slots drawn in the app.
	{Name: "widget", Size: WidgetFamilySmall, Shown: true, Since: 3},
}

// Widgets: the families a widget is drawn at and how often it is drawn again (docs/extensions/extensions.md "System-level places").
const (
	WidgetFamilySmall    = wire.WidgetFamilySmall
	WidgetRefreshDefault = 30 // minutes
	WidgetRefreshLeast   = 15
	WidgetRefreshMost    = 24 * 60
)

// WidgetFamilies are the sizes a widget can be drawn at: small / medium / large on the home screen, accessory_* on the
// Lock Screen and watch faces.
var WidgetFamilies = wire.WidgetFamilies

// Slot is the slot named name.
func Slot(name string) (UISlot, bool) {
	i := slices.IndexFunc(UISlots, func(s UISlot) bool { return s.Name == name })
	if i < 0 {
		return UISlot{}, false
	}
	return UISlots[i], true
}

// Sizes, platforms and icons of the UI.
const (
	SizeCompact = "compact"
	SizeFull    = "full"
)

var (
	UIPlatforms = []string{"ios", "mac", "android", "web"}
	// UIIcons is the fixed icon set (a nav_page's, a row's, an empty state's): the schema's NavIcon.
	UIIcons = wire.NavIcons
)

// UIVersion is the version of the UI this hub knows: 1 the first eight items and two slots; 2 the slots of UISlots,
// chart and stat_grid, refresh_after and the render context; 3 markdown, table, empty and loading, a row's icon, avatar
// and thumbnail, components' keys, a view's search and filter, a section's more, pushing and popping pages,
// ui.invalidate; 4 the code hosting components (header, breadcrumb, file_tree, code, diff, comment_thread, composer), a
// view's tabs and split layout, an action's values (#2951); 5 code review (#3136): a diff's context lines, viewed marks,
// generated files, drafts of a pending review and its bar, resolving threads, a code's line links, and an action
// answering with lines or with components in place; 6 pictures, controls in rows, candlestick, area, donut and pie
// charts and sparks, a row's extra actions, grids and carousels, date, time, multiple choice and multi-line fields,
// title styles, named colors and more icons (#5464). An app says which it draws (context.ui_version, 1 when it says
// nothing); the hub leaves out of a View what the app does not know, or draws it with what it knows (Fit).
const UIVersion = 6

// PageSlot says whether placement is the extension's own page: extension_page, or plugin_page as it was called.
func PageSlot(placement string) bool {
	return placement == "extension_page" || placement == "plugin_page"
}

// FullPage says whether placement is a whole page a push may open: an extension_page or a nav_page.
func FullPage(placement string) bool { return PageSlot(placement) || placement == "nav_page" }

// UIMachine is the machine a machine_section is drawn for.
type UIMachine struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OS     string `json:"os,omitempty"`
	Online bool   `json:"online"`
}

// UIInboxItem is the inbox item an inbox_item is drawn for: the row as the hub keeps it. Meta is the connector's own
// record of what the item gathers, passed as it is stored (null when it has none): the hub never reads it. Extra are
// the item's source blocks as the apps get them (lark, doc, moment, connector…), for a plugin drawing an item that has
// no meta.
type UIInboxItem struct {
	ID      string                     `json:"id"`
	Title   string                     `json:"title"`
	Summary string                     `json:"summary,omitempty"`
	Tier    string                     `json:"tier,omitempty"`
	Reason  string                     `json:"reason,omitempty"`
	Done    bool                       `json:"done"`
	Meta    json.RawMessage            `json:"meta"`
	Extra   map[string]json.RawMessage `json:"extra,omitempty"`
	// Count is how many messages the item holds (for an event, the hub's own count); StartedAt is when it began.
	Count     int       `json:"count,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
}

// UILauncher is the launcher a launcher_section is drawn for; QueryID lets the plugin reach its gateway
// (gateway.http) while it draws.
type UILauncher struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	QueryID string `json:"query_id"`
}

// UIParams are ui.render's and ui.action's parameters: what to draw and where, for whom.
type UIParams struct {
	Contribution string            `json:"contribution"`
	State        string            `json:"state"`
	Action       string            `json:"action,omitempty"`
	Values       map[string]string `json:"values,omitempty"`
	Placement    string            `json:"placement"`
	Size         string            `json:"size"`
	// UIVersion is what the app that asks draws (UIVersion at most): a plugin may draw an older app something it knows.
	UIVersion int          `json:"ui_version"`
	TZ        string       `json:"tz,omitempty"`     // the user's time zone ("Asia/Shanghai"); "" when unknown
	Locale    string       `json:"locale,omitempty"` // the app's language ("zh-Hans-CN")
	Platform  string       `json:"platform,omitempty"`
	Machine   *UIMachine   `json:"machine,omitempty"`
	Launcher  *UILauncher  `json:"launcher,omitempty"`
	Item      *UIInboxItem `json:"item,omitempty"`
}

// BadgeEvent is ui.badge, sent by a plugin: the badge on its nav_page's entry (no text clears it).
type BadgeEvent struct {
	Contribution string `json:"contribution"`
	Text         string `json:"text,omitempty"`
	Tone         string `json:"tone,omitempty"`
}

// BadgeMost is how long a badge is, in characters.
const BadgeMost = 8

// CheckBadge holds a badge to what the apps draw.
func CheckBadge(text, tone string) error {
	if utf8.RuneCountInString(text) > BadgeMost {
		return fmt.Errorf("a badge has at most %d characters", BadgeMost)
	}
	if !slices.Contains(tones, tone) {
		return errors.New("tone is neutral, positive, caution or danger")
	}
	return nil
}

// EventsItem is a contribution to events: the hub's events a plugin is told about. It is only told; it changes
// nothing (docs/extensions/extensions.md "Events").
type EventsItem struct {
	ID        string   `json:"id"`
	Subscribe []string `json:"subscribe"`
}

// ErrorReportsItem is a contribution to error_reports: the error and crash reports it is given (Sources: only those;
// none: all).
type ErrorReportsItem = wire.ErrorReportsItem

// AuthRelayItem is a contribution to auth_relay: the authorization links (hosts and paths) it completes.
type AuthRelayItem = wire.AuthRelayItem

// Events this hub has.
var (
	// EventTopics are the events by topic; how the install screen says each is extension-permissions.event_<topic with
	// _ for .> (Explain). No text here: the catalogs are read on first use, not when the package starts (#1308).
	EventTopics = map[string]bool{
		"kb.changed":      true,
		"kb.forgotten":    true,
		"inbox.changed":   true,
		"agent.needs_you": true,
		"agent.finished":  true,
		"todo.changed":    true,
		"todo.decision":   true,
	}

	// EventScopes are the hub scopes an event needs besides the subscription: only an extension granted the scope
	// (or the legacy full-hub grant) is told. A topic not listed needs none.
	EventScopes = map[string]string{
		"kb.changed":    ScopeKBRead,
		"kb.forgotten":  ScopeKBRead,
		"todo.changed":  ScopeTodosRead,
		"todo.decision": ScopeTodosRead,
	}
)

// The View and its components are the wire types generated from schema/extension/view.schema.json (sdk/go/wire):
// the schema is the one truth of what an extension may draw, and the same types are every app's.
type (
	// View is what an extension draws (ui.render, ui.action): sections of components, a state of its own the apps send
	// back (at most StateMost bytes), refresh_after.
	View    = wire.View
	Section = wire.Section
	// UIElem is one component; Type says which fields mean something (the schema's Component, one flat struct).
	UIElem      = wire.Component
	ChartSeries = wire.ChartSeries
	ChartPoint  = wire.ChartPoint
	Stat        = wire.Stat
	Badge       = wire.Badge
	// UIAction is what a row, a button or a segmented does: ui.action with its ID (and the fields' values when Submit).
	UIAction = wire.Action
	UIChoice = wire.Choice
	// ActionResult is what ui.action answers: a new View, a short message, a link to open, a page to push or pop, or
	// nothing (draw again).
	ActionResult = wire.ActionResult
	// Push is a page an action opens on top: one of the extension's whole pages, with a state.
	Push = wire.Push
	// InvalidateEvent is ui.invalidate, sent by an extension: what it draws changed (one ui, or all).
	InvalidateEvent = wire.InvalidateEvent
	// UI version 4's code hosting parts (#2951): a page's tabs, a file tree's entries, a diff's hunks and threads,
	// comments, a composer's buttons, a code's highlighted lines.
	Tabs           = wire.Tabs
	Tab            = wire.Tab
	TreeEntry      = wire.TreeEntry
	Hunk           = wire.Hunk
	Thread         = wire.Thread
	Comment        = wire.Comment
	ComposerButton = wire.ComposerButton
	LineRange      = wire.LineRange
	// UI version 5's code review parts (#3136): lines of a file a diff's context asked for, the pending review's bar.
	FileLines = wire.FileLines
	Review    = wire.Review
)

// InboxSourceURL is the one link besides https an action may open, and only on an inbox_item, for the item it is
// drawn for: the apps open the item's own source page there (what opening the item showed before the slot: a Feishu
// chat at the item's first message). The id is escaped as one path segment ("lark:F1:1051" stays as it is).
func InboxSourceURL(itemID string) string {
	return "persona://inbox/" + url.PathEscape(itemID) + "/source"
}

// SettingsURL is the other link besides https an action may open, only to its own extension's settings (#5906): the
// apps open 设置 › 扩展 › that extension there (the web /settings?tab=extensions&extension=<id>).
func SettingsURL(id string) string {
	return "persona://settings/extensions/" + url.PathEscape(id)
}

// Limits of a View.
const (
	ItemsMost  = wire.ItemsMost
	StringMost = wire.StringMost
	StateMost  = wire.StateMost
	// A chart has at most SeriesMost series of PointsMost points; a stat_grid at most StatsMost stats.
	SeriesMost = wire.SeriesMost
	PointsMost = wire.PointsMost
	StatsMost  = wire.StatsMost
	// A segmented has at most SegmentsMost choices.
	SegmentsMost = 5
	// A compact view (an overview_card) has at most CompactSections sections and CompactItems items, and no fields.
	CompactSections = 2
	CompactItems    = 12
	// A view has at most StackMost pages pushed on its own (the apps keep no more).
	StackMost = 8
	// A markdown has at most MarkdownMost characters; a table at most TableColumnsMost columns and TableRowsMost rows.
	MarkdownMost     = 20000
	TableColumnsMost = 8
	TableRowsMost    = 100
	// RefreshLeast and RefreshMost bound refresh_after, in seconds.
	RefreshLeast = 30
	RefreshMost  = 24 * 3600
)

// elemSince is each component's type and the UI version that brought it (the schema's x-since).
var elemSince = wire.ComponentSince

// tones are the tones a component may have; empty is the app's own.
var tones = append([]string{""}, wire.Tones...)

func CheckView(v *View) error { return wire.CheckView(v) }

func Actions(v *View) []*UIAction { return wire.Actions(v) }

// ParseView reads and checks a View a plugin answered with.
func ParseView(raw json.RawMessage) (*View, error) {
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if v.Sections == nil {
		v.Sections = []Section{}
	}
	for i := range v.Sections {
		if v.Sections[i].Items == nil {
			v.Sections[i].Items = []UIElem{}
		}
	}
	return &v, CheckView(&v)
}
