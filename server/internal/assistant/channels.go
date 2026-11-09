package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The channels (docs/extensions/channels.md): the ways of talking to Persona from a chat app — the Feishu bot (feishu*.go) and
// the bots plugins contribute (the channel extension point, channels_plugin.go). A message from a channel goes into the
// conversation the channel talks to, the turn is followed and its answer sent back the same way (channels_runs.go);
// what is asked in a conversation a channel drives goes there too. All of that is the assistant's. The hub keeps the
// transport — the open platform's long connection, pairing, the plugins' programs — hands the assistant what comes in
// (channels.receive, feishu.message, feishu.card_action) and posts what it is given (app.call feishu.*, channels.*).
// While the assistant is not connected the hub answers the owner's messages "the assistant can't be reached right now" itself.

// The channels' state: each plugin channel's settings and the conversations its messages went to lately (channels),
// the runs, the asks waiting in text and the messages taken (channels.state). The same keys the hub kept before; only
// the assistant reads and writes them now.
const (
	channelsKey      = "channels"
	channelsStateKey = "channels.state"
)

type channelPrefs struct {
	Target   *channel.Target `json:"target,omitempty"`
	Features map[string]bool `json:"features,omitempty"`
	// Recent are the conversations its messages went to lately (newest first, a few).
	Recent []channel.Conversation `json:"recent,omitempty"`
}

const channelRecentKeep = 5

type channelsState struct {
	Runs []*channel.Run `json:"runs,omitempty"`
	Asks []*channel.Ask `json:"asks,omitempty"`
	// Seen are, by channel, the ids of the latest messages taken (the chat apps deliver some again).
	Seen map[string][]string `json:"seen,omitempty"`
}

const channelSeenKeep = 300

// channelsBook is the channels' state in memory.
type channelsBook struct {
	mu     sync.Mutex
	loaded bool
	prefs  map[string]*channelPrefs
	// st is what the runs wait on (channels_runs.go), loaded apart from prefs.
	st       channelsState
	stLoaded bool
	live     map[string]*chanLive // run id -> its live progress (not persisted)
	// answered: conversation -> when its last run was answered (no "finished" push, no mirror for it)
	answered map[[2]string]time.Time
	// notified: channel + conversation (machine, session) -> when its notify_user copy arrived
	notified map[[3]string]time.Time
	// answering: the runs whose answer is going out now (finishChannelRun): a later message is not folded into them
	answering map[string]bool
	// follows: how many runs follow each conversation (session.follow)
	follows map[[2]string]int
	// followRetry: the followed conversations whose session.follow failed -> when a run's watcher asks again
	followRetry map[[2]string]time.Time
	// announced: the cards this process heard of (interaction.announced, or pending when it first connected); one
	// that came while it was not connected is taken when it is back (channelsCatchUp).
	announced map[string]bool
	connected bool
	// ended: the cards that ended lately (interaction.ended), by when: one still being sent to a channel then is closed
	// (or not asked) as soon as it is out.
	ended map[string]time.Time
	// lastEnded: channel -> the latest text ask there that ended without being answered there (answered in the app,
	// in a terminal, withdrawn), by when: a late reply to it is told so instead of going into the conversation.
	lastEnded map[string]endedAsk
	// generation changes on unbind so an in-flight message or question cannot restore the old binding's state.
	generation map[string]uint64
	// list is the hub's list of the channels, kept a moment (the runs look at it every second).
	list   []channel.View
	listAt time.Time
	now    func() time.Time // tests
}

func (c *channelsBook) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (s *Service) loadChannelsLocked(ctx context.Context) {
	c := &s.chans
	if c.loaded {
		return
	}
	c.prefs = map[string]*channelPrefs{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, channelsKey, &c.prefs); err != nil {
			s.logf("channels: load: %v", err)
			return // not marked loaded: the next look tries again
		}
	}
	if c.prefs == nil {
		c.prefs = map[string]*channelPrefs{}
	}
	c.loaded = true
}

// saveChannelsLocked writes the settings, once they were read: saving what is in memory after a failed read would
// write one channel's settings over every other's.
func (s *Service) saveChannelsLocked() {
	st := s.store()
	if st == nil || !s.chans.loaded {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, channelsKey, s.chans.prefs); err != nil {
		s.logf("channels: save: %v", err)
	}
}

// channelPrefLocked is channel id's settings (created on first use; the caller holds s.chans.mu).
func (s *Service) channelPrefLocked(id string) *channelPrefs {
	p := s.chans.prefs[id]
	if p == nil {
		p = &channelPrefs{}
		s.chans.prefs[id] = p
	}
	if p.Features == nil {
		p.Features = map[string]bool{}
		for k, v := range channel.DefaultFeatures {
			p.Features[k] = v
		}
	}
	return p
}

// loadChannelStateLocked reads the runs once (the caller holds s.chans.mu); the runs an older hub kept in the Feishu
// bot's state come over the first time.
//
// The state is decoded into a fresh value, never into c.st: runs and asks started while it could not be read are
// already in c.st, and their watchers read them without the lock. Decoding in place would reuse those pointers and
// write the stored runs' fields over them. They are kept next to what was stored instead.
func (s *Service) loadChannelStateLocked(ctx context.Context) {
	c := &s.chans
	if c.stLoaded {
		return
	}
	var stored channelsState
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, channelsStateKey, &stored); err != nil {
			s.logf("channels: load state: %v", err)
			return
		}
	}
	if stored.Seen == nil {
		stored.Seen = map[string][]string{}
	}
	var kept int
	stored.Runs, kept = keepLive(stored.Runs, c.st.Runs, func(r *channel.Run) string { return r.ID })
	var asks int
	stored.Asks, asks = keepLive(stored.Asks, c.st.Asks, func(a *channel.Ask) string { return a.ID })
	c.st = stored
	c.stLoaded = true
	if kept+asks > 0 {
		s.saveChannelStateLocked()
		s.logf("channels: %d runs and %d asks started while the state was unreadable kept", kept, asks)
	}
	if old := s.takeLegacyBotRuns(ctx); len(old) > 0 {
		c.st.Runs = append(c.st.Runs, old...)
		s.saveChannelStateLocked()
		s.logf("channels: %d waiting turns of the Feishu bot moved to the channels' runs", len(old))
	}
}

// keepLive is stored with the items held in memory added: one with the id of a stored item takes its place (it is
// the one being watched), the others are appended; n is how many came from memory.
func keepLive[T any](stored, live []*T, id func(*T) string) (out []*T, n int) {
	if len(live) == 0 {
		return stored, 0
	}
	at := make(map[string]int, len(stored))
	for i, x := range stored {
		at[id(x)] = i
	}
	for _, x := range live {
		if i, ok := at[id(x)]; ok {
			stored[i] = x
		} else {
			at[id(x)] = len(stored)
			stored = append(stored, x)
		}
		n++
	}
	return stored, n
}

// saveChannelStateLocked writes the runs, asks and seen messages, once they were read (the same as the settings).
func (s *Service) saveChannelStateLocked() error {
	st := s.store()
	if st == nil || !s.chans.stLoaded {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, channelsStateKey, s.chans.st); err != nil {
		s.logf("channels: save state: %v", err)
		return err
	}
	return nil
}

// noteChannelConversation remembers that channel id's message went to a conversation (the detail's recent list, the
// conversation's source marks).
func (s *Service) noteChannelConversation(id, machine, session string) {
	c := &s.chans
	c.mu.Lock()
	s.loadChannelsLocked(context.Background())
	p := s.channelPrefLocked(id)
	p.Recent = slices.DeleteFunc(p.Recent, func(x channel.Conversation) bool { return x.MachineID == machine && x.SessionID == session })
	p.Recent = append([]channel.Conversation{{MachineID: machine, SessionID: session, At: time.Now().UTC()}}, p.Recent...)
	if len(p.Recent) > channelRecentKeep {
		p.Recent = p.Recent[:channelRecentKeep]
	}
	s.saveChannelsLocked()
	c.mu.Unlock()
}

// channelsReport is what the hub shows of the channels (Snapshot.Channels): the channels each conversation's messages
// came from, the conversations a channel's turn is followed in or was answered in within the hour; nil while the
// state can't be read.
func (s *Service) channelsReport() *ChannelsReport {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx := context.Background()
	s.loadChannelsLocked(ctx)
	s.loadChannelStateLocked(ctx)
	if !c.loaded || !c.stLoaded {
		return nil
	}
	out := &ChannelsReport{Marks: []ChannelMark{}, Turns: []ChannelTurn{}}
	type mark struct {
		id string
		at time.Time
	}
	marks := map[[2]string][]mark{}
	for id, p := range c.prefs {
		for _, r := range p.Recent {
			k := [2]string{r.MachineID, r.SessionID}
			marks[k] = append(marks[k], mark{id, r.At})
		}
	}
	for k, ms := range marks {
		slices.SortFunc(ms, func(a, b mark) int {
			if n := b.at.Compare(a.at); n != 0 {
				return n
			}
			return compareStrings(a.id, b.id)
		})
		m := ChannelMark{MachineID: k[0], SessionID: k[1]}
		for _, x := range ms {
			m.Channels = append(m.Channels, x.id)
		}
		out.Marks = append(out.Marks, m)
	}
	busy := map[[2]string]bool{}
	for _, r := range c.st.Runs {
		k := [2]string{r.MachineID, r.SessionID}
		if !busy[k] {
			busy[k] = true
			out.Turns = append(out.Turns, ChannelTurn{MachineID: k[0], SessionID: k[1]})
		}
	}
	now := c.clock()
	for k, at := range c.answered {
		if now.Sub(at) > time.Hour {
			delete(c.answered, k)
			continue
		}
		if !busy[k] {
			out.Turns = append(out.Turns, ChannelTurn{MachineID: k[0], SessionID: k[1], Answered: at.UTC()})
		}
	}
	slices.SortFunc(out.Marks, func(a, b ChannelMark) int {
		return compareStrings(a.MachineID+"/"+a.SessionID, b.MachineID+"/"+b.SessionID)
	})
	slices.SortFunc(out.Turns, func(a, b ChannelTurn) int {
		return compareStrings(a.MachineID+"/"+a.SessionID, b.MachineID+"/"+b.SessionID)
	})
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// channelsChanged reports the snapshot again: the hub shows the channels' marks and turns from it.
func (s *Service) channelsChanged() { go s.publish(context.Background()) }

// ---- the channels as the hub has them ----

// channelsNow is the hub's list of the channels (channels.list), kept a few seconds: for what looks often (a run's
// sink every second, every card and turn's end).
func (s *Service) channelsNow(ctx context.Context) ([]channel.View, error) {
	c := &s.chans
	c.mu.Lock()
	if c.list != nil && time.Since(c.listAt) < 3*time.Second {
		out := c.list
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	return s.channelsFresh(ctx)
}

// channelsFresh is the hub's list of the channels now (the app's pages: a switch just changed shows at once).
func (s *Service) channelsFresh(ctx context.Context) ([]channel.View, error) {
	c := &s.chans
	var res struct {
		Channels []channel.View `json:"channels"`
	}
	if err := s.appCall(ctx, AppChannels, ChannelsList, map[string]any{}, &res); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.list, c.listAt = res.Channels, time.Now()
	c.mu.Unlock()
	return res.Channels, nil
}

// forgetChannelList: a switch or binding changed: the next look asks the hub again.
func (s *Service) forgetChannelList() {
	s.chans.mu.Lock()
	s.chans.list = nil
	s.chans.mu.Unlock()
}

// pluginChannel is channel id when a plugin contributes it (false when there is no such channel now, or the hub
// could not be asked).
func (s *Service) pluginChannel(ctx context.Context, id string) (channel.View, bool) {
	v, ok, _ := s.pluginChannelNow(ctx, id)
	return v, ok
}

// pluginChannelNow is pluginChannel telling the hub not answering (an error) from no such channel (false).
func (s *Service) pluginChannelNow(ctx context.Context, id string) (channel.View, bool, error) {
	list, err := s.channelsNow(ctx)
	if err != nil {
		return channel.View{}, false, err
	}
	for _, v := range list {
		if v.ID == id && v.Source == channel.SourcePlugin && v.Kind == channel.KindIM {
			return v, true, nil
		}
	}
	return channel.View{}, false, nil
}

// botLang is the language of what the bots and the channels write: the request's when the context carries one (the
// app's request, withRequestLang), else the user's (docs/protocol/i18n.md "Protocol"), zh-Hans while no client has said one.
func (s *Service) botLang(ctx context.Context) i18n.Lang {
	if l := i18n.From(ctx); l != "" {
		return l
	}
	return i18n.Lang(s.hello(ctx).Locale).Or(i18n.Default)
}

// withRequestLang is r with its Accept-Language (the hub relays the app's headers) in its context, for botLang.
func withRequestLang(r *http.Request) *http.Request {
	if l := i18n.FromRequest(r); l != "" {
		return r.WithContext(i18n.With(r.Context(), l))
	}
	return r
}

// channelList is every channel as the app sees it: the hub's list with what is the assistant's filled in.
func (s *Service) channelList(ctx context.Context) ([]channel.View, error) {
	list, err := s.channelsFresh(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]channel.View, len(list))
	for i, v := range list {
		out[i] = s.filledChannel(ctx, v)
	}
	return out, nil
}

// filledChannel is v with its target, and a plugin channel's features.
func (s *Service) filledChannel(ctx context.Context, v channel.View) channel.View {
	switch {
	case v.ID == channel.Feishu:
		t := s.feishuChannelTarget(ctx)
		v.Target = &t
	case v.Source == channel.SourcePlugin && v.Kind == channel.KindIM && v.Installed:
		c := &s.chans
		c.mu.Lock()
		s.loadChannelsLocked(ctx)
		pref := s.channelPrefLocked(v.ID)
		t := channel.Target{Kind: channel.TargetMain}
		if pref.Target != nil {
			t = *pref.Target
		}
		features := channel.PluginFeatures(s.botLang(ctx), v.Capabilities, pref.Features)
		c.mu.Unlock()
		if t.Kind == channel.TargetConversation && t.Title == "" {
			t.Title = s.conversationTitle(ctx, t.MachineID, t.SessionID)
		}
		v.Target, v.Features = &t, features
	}
	return v
}

// conversationTitle is how a conversation is called in the channel's settings.
func (s *Service) conversationTitle(ctx context.Context, machine, session string) string {
	c, ok := s.session(ctx, machine, session)
	if !ok {
		return ""
	}
	return conversationName(c, s.settings(ctx).Main)
}

func (s *Service) channelView(ctx context.Context, id string) (channel.View, bool, error) {
	list, err := s.channelList(ctx)
	if err != nil {
		return channel.View{}, false, err
	}
	for _, v := range list {
		if v.ID == id {
			return v, true, nil
		}
	}
	return channel.View{}, false, nil
}

func (s *Service) channelDetail(ctx context.Context, id string) (channel.Detail, bool, error) {
	v, ok, err := s.channelView(ctx, id)
	if err != nil || !ok {
		return channel.Detail{}, ok, err
	}
	d := channel.Detail{View: v}
	switch id {
	case channel.Feishu:
		d.SettingsURL = "settings/connectors/feishu-bot"
		d.Waiting = s.channelWaiting(id)
		d.Waiting.Asks = s.feishuCardsWaiting()
	case channel.App:
	default:
		if v.Source == channel.SourcePlugin && v.Plugin != "" {
			d.SettingsURL = "settings/plugins/" + v.Plugin
		}
		d.Waiting = s.channelWaiting(id)
	}
	c := &s.chans
	c.mu.Lock()
	s.loadChannelsLocked(ctx)
	if p := c.prefs[id]; p != nil {
		d.Conversations = slices.Clone(p.Recent)
	}
	c.mu.Unlock()
	for i := range d.Conversations {
		if t := s.conversationTitle(ctx, d.Conversations[i].MachineID, d.Conversations[i].SessionID); t != "" {
			d.Conversations[i].Title = t
		}
	}
	return d, true, nil
}

// isAssistantConversation: session on machine is one of the assistant's conversations, as the hub lists it.
func (s *Service) isAssistantConversation(ctx context.Context, machine, session string) bool {
	list, err := s.sessions(ctx, machine)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(list, func(c proto.Session) bool { return c.SessionID == session && c.Assistant })
}

// ---- the app's interface (relayed by the hub, assistantwire.Routes) ----

func (s *Service) channelRoutes() {
	handle := func(pattern string, fn http.HandlerFunc) {
		s.handle(pattern, func(w http.ResponseWriter, r *http.Request) { fn(w, withRequestLang(r)) })
	}
	handle("GET /api/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.channelList(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"channels": list})
	})
	handle("GET /api/v1/channels/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok, err := s.channelDetail(r.Context(), r.PathValue("id"))
		switch {
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		case !ok:
			apierr.WriteCode(w, http.StatusNotFound, "not_found", errors.New("no such channel"))
		default:
			apierr.WriteJSON(w, d)
		}
	})
	full := func(f func(w http.ResponseWriter, r *http.Request, v channel.View)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, errors.New("this device can only read and answer"))
				return
			}
			v, ok, err := s.channelView(r.Context(), r.PathValue("id"))
			switch {
			case err != nil:
				apierr.Write(w, http.StatusBadGateway, err)
				return
			case !ok:
				apierr.WriteCode(w, http.StatusNotFound, "not_found", errors.New("no such channel"))
				return
			case v.ReadOnly || v.Source == channel.SourceMarket:
				apierr.WriteCode(w, http.StatusConflict, "read_only", errors.New(i18n.T(s.botLang(r.Context()), "assistant-channels.not_set_here", i18n.Args{"name": v.Name})))
				return
			}
			f(w, r, v)
		}
	}
	detail := func(w http.ResponseWriter, r *http.Request, id string) {
		s.forgetChannelList()
		d, _, err := s.channelDetail(r.Context(), id)
		if err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		apierr.WriteJSON(w, d)
	}
	handle("PUT /api/v1/channels/{id}", full(func(w http.ResponseWriter, r *http.Request, v channel.View) {
		var in struct {
			Enabled  *bool           `json:"enabled"`
			Target   *channel.Target `json:"target"`
			Features map[string]bool `json:"features"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.updateChannel(r.Context(), v, in.Enabled, in.Target, in.Features); err != nil {
			var re *rpc.Error
			if errors.As(err, &re) && re.Code == assistantwire.CodeNeedsTriageModel {
				apierr.WriteCode(w, http.StatusConflict, re.Code, errors.New(re.Message))
				return
			}
			apierr.WriteCode(w, http.StatusBadRequest, "invalid", err)
			return
		}
		detail(w, r, v.ID)
	}))
	handle("DELETE /api/v1/channels/{id}/binding", full(func(w http.ResponseWriter, r *http.Request, v channel.View) {
		if err := s.appCall(r.Context(), AppChannels, ChannelsUnbind, map[string]any{"channel": v.ID}, nil); err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		s.forgetChannelTurns(v.ID)
		if v.ID == channel.Feishu {
			s.feishuUnpaired()
		}
		detail(w, r, v.ID)
	}))
	handle("POST /api/v1/channels/{id}/test", full(func(w http.ResponseWriter, r *http.Request, v channel.View) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.testChannel(ctx, v); err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	}))
}

// updateChannel applies the app's changes: only what is given.
func (s *Service) updateChannel(ctx context.Context, v channel.View, enabled *bool, target *channel.Target, features map[string]bool) error {
	l := s.botLang(ctx)
	if target != nil {
		if err := target.Check(); err != nil {
			return err
		}
		if target.Kind == channel.TargetConversation && !s.isAssistantConversation(ctx, target.MachineID, target.SessionID) {
			return errors.New(i18n.T(l, "assistant-channels.assistant_chats_only"))
		}
	}
	known := map[string]bool{}
	for _, f := range v.Features {
		known[f.ID] = true
	}
	for id := range features {
		if !known[id] {
			return errors.New(i18n.T(l, "assistant-channels.no_feature", i18n.Args{"name": v.Name, "feature": id}))
		}
	}
	if v.ID == channel.Feishu {
		return s.updateFeishuChannel(ctx, enabled, target, features)
	}
	if v.Source != channel.SourcePlugin {
		return errors.New(i18n.T(l, "assistant-channels.not_set_here", i18n.Args{"name": v.Name}))
	}
	if enabled != nil {
		if err := s.appCall(ctx, AppChannels, ChannelsEnable, map[string]any{"channel": v.ID, "on": *enabled}, nil); err != nil {
			return err
		}
	}
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelsLocked(ctx)
	p := s.channelPrefLocked(v.ID)
	if target != nil {
		t := *target
		t.Title = ""
		p.Target = &t
	}
	for id, on := range features {
		p.Features[id] = on
		if id == channel.FeatureAllAsks && on {
			p.Features[channel.FeatureAsks] = true
		}
	}
	s.saveChannelsLocked()
	return nil
}

// testChannel sends the owner (only them) a line, to see the channel reaches them.
func (s *Service) testChannel(ctx context.Context, v channel.View) error {
	if v.State != channel.StateConnected {
		return errors.New(i18n.T(s.botLang(ctx), "assistant-channels.not_connected", i18n.Args{"name": v.Name}))
	}
	if v.ID == channel.Feishu {
		return s.appCall(ctx, AppFeishu, FeishuTest, map[string]any{}, nil)
	}
	return s.channelTell(ctx, v.ID, "", i18n.T(s.botLang(ctx), "assistant-channels.test_message"), channel.OutNote)
}

// ---- what the hub hands the channels (Handler) ----

// ChannelUnbound synchronously clears pending state before the connector's lifecycle request reports success.
func (s *Service) ChannelUnbound(_ context.Context, p ChannelUnbound) error {
	if p.Channel == "" {
		return badRequest(errors.New("a channel without an id"))
	}
	return s.forgetChannelTurns(p.Channel)
}

// ChannelReceive is channels.receive: the owner's message from a plugin's channel, taken once (the chat apps deliver
// some again) and handled in the background.
func (s *Service) ChannelReceive(_ context.Context, p ChannelReceive) error {
	var m channel.Message
	if err := json.Unmarshal(p.Message, &m); err != nil {
		return badRequest(err)
	}
	if m.ID == "" {
		return badRequest(errors.New("a message without an id"))
	}
	first, err := s.claimChannelMessage(p.Channel, m.ID)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: err.Error()} // the hub hands it over again
	}
	if !first {
		return nil // delivered again
	}
	go s.channelReceive(p.Channel, m, p.Action)
	return nil
}

// ChannelNoted is channels.noted: a channel's message went into a conversation through the apps' routes.
func (s *Service) ChannelNoted(_ context.Context, p ChannelNoted) {
	s.noteChannelConversation(p.Channel, p.MachineID, p.SessionID)
	s.channelsChanged()
}

// InteractionAnnounced is interaction.announced: a new card goes to the Feishu bot (as a card) and to the plugin
// channels that ask in text, when they drive its conversation (or take every agent's).
func (s *Service) InteractionAnnounced(_ context.Context, p InteractionAnnounced) {
	c := &s.chans
	c.mu.Lock()
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	c.announced[p.Interaction.ID] = true
	c.mu.Unlock()
	go s.feishuInteraction(p.Interaction)
	go s.channelInteraction(p.Interaction)
	go s.taskAsks(p.Interaction)
	s.boardChanged()
}

// InteractionEnded is interaction.ended: a card is over: the Feishu card says how, the text ask stops waiting.
func (s *Service) InteractionEnded(_ context.Context, p InteractionEnded) {
	c := &s.chans
	c.mu.Lock()
	delete(c.announced, p.Interaction.ID)
	now := time.Now()
	for id, at := range c.ended {
		if now.Sub(at) > 10*time.Minute {
			delete(c.ended, id)
		}
	}
	if c.ended == nil {
		c.ended = map[string]time.Time{}
	}
	c.ended[p.Interaction.ID] = now
	c.mu.Unlock()
	l := s.botLang(context.Background())
	how := i18n.T(l, "assistant-channels.ended")
	if p.Resolution != nil {
		how = resolutionText(l, p.Interaction, *p.Resolution)
	}
	// Taken back because the user wrote in the conversation: what they write next is not a late answer to it.
	inChat := p.Resolution != nil && p.Resolution.Dismiss && p.Resolution.Reason == proto.DismissedInChat && p.Resolution.AnsweredBy == ""
	s.feishuInteractionEnded(p.Interaction, how, inChat)
	s.channelInteractionEnded(p.Interaction, inChat)
	s.boardChanged()
}

// channelsPushed: the conversation pushed what matters itself (notify_user): it goes to the channels with pushes on
// too, and each channel's sync mirror leaves the turn alone only after its own copy arrives. A failed push must
// not suppress that channel's turn-end mirror.
func (s *Service) channelsPushed(machine, session, title, body string) {
	go s.feishuPush(machine, session, title, body)
	go s.channelPush(machine, session, title, body)
}

// markPushed records a delivered notify_user copy for one channel and conversation. Its mirror leaves the turn
// alone for the next three minutes (pushedLately). Entries older than an hour are swept.
func (s *Service) markPushed(ch, machine, session string) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notified == nil {
		c.notified = map[[3]string]time.Time{}
	}
	now := c.clock()
	for k, at := range c.notified {
		if now.Sub(at) > time.Hour {
			delete(c.notified, k)
		}
	}
	c.notified[[3]string{ch, machine, session}] = now
}

// interactionEnded: the card ended (interaction.ended came) — maybe while it was being sent to a channel.
func (s *Service) interactionEnded(id string) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.ended[id]
	return ok
}

// pushedLately: this channel received the conversation's push within the last few minutes.
func (s *Service) pushedLately(ch, machine, session string) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clock().Sub(c.notified[[3]string{ch, machine, session}]) < 3*time.Minute
}

// channelsReconnected: a new connection to the hub (manager.hello): the conversations the runs follow are followed
// again (the hub forgot them), the runs left without a watcher are watched again.
func (s *Service) channelsReconnected(ctx context.Context) {
	c := &s.chans
	c.mu.Lock()
	var keys [][2]string
	for k, n := range c.follows {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	c.mu.Unlock()
	for _, k := range keys {
		s.askFollow(ctx, k[0], k[1]) // a failure is asked again by the run's watcher
	}
	s.forgetChannelList()
	s.feishuForgetStatus()
	if _, err := s.feishuStatus(ctx); err != nil { // the pairing as it is now (feishuReconcile)
		s.logf("channels: the Feishu bot's status: %v", err)
	}
	go s.sweepChannelRuns()
	s.channelsCatchUp(ctx)
}

// channelsCatchUp takes what came and went while the assistant was not connected: a card that came meanwhile goes to
// the channels as a new one would, one that ended meanwhile is closed (its Feishu card says "Ended", its text ask
// stops waiting). The first connection of this process only notes what is pending: a card asked before it started may
// have gone out already.
func (s *Service) channelsCatchUp(ctx context.Context) {
	var res struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	if err := s.call(ctx, DeviceAll, DeviceInteractionsList, map[string]any{}, &res); err != nil {
		s.logf("channels: the cards waiting now: %v", err)
		return
	}
	pending := map[string]bool{}
	c := &s.chans
	c.mu.Lock()
	first := !c.connected
	c.connected = true
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	var missed []proto.Interaction
	for _, ix := range res.Interactions {
		pending[ix.ID] = true
		if !c.announced[ix.ID] {
			c.announced[ix.ID] = true
			if !first {
				missed = append(missed, ix)
			}
		}
	}
	for id := range c.announced {
		if !pending[id] {
			delete(c.announced, id)
		}
	}
	var gone []*channel.Ask
	if c.stLoaded {
		for _, a := range c.st.Asks {
			if !pending[a.ID] {
				gone = append(gone, a)
			}
		}
	}
	c.mu.Unlock()
	quiet := map[[2]string]bool{} // a quiet task's agent asks only in the main conversation, never in a channel (#1731)
	if len(missed) > 0 {
		if r := s.todoReport(ctx); r != nil {
			for _, q := range r.Quiet {
				quiet[[2]string{q.MachineID, q.SessionID}] = true
			}
		}
	}
	for _, ix := range missed {
		if !quiet[[2]string{ix.MachineID, ix.SessionID}] {
			s.InteractionAnnounced(ctx, InteractionAnnounced{Interaction: ix})
		}
	}
	for _, a := range gone {
		s.channelInteractionDone(proto.Interaction{ID: a.ID})
	}
	s.closeEndedBotCards(ctx, pending, true)
}

// closeEndedBotCards closes the bot's cards whose interaction is over. Not being in pending is not enough: right
// after a restart the manager's table is empty or partial (interactions are restored, screen and Codex cards rebuilt,
// after this runs), so each is closed only once the history says it ended for good (botCardOver). One that may still
// be waiting is looked at again a little later, once (again).
func (s *Service) closeEndedBotCards(ctx context.Context, pending map[string]bool, again bool) {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx) // cards posted before a restart are in the chat's state
	var maybe []botCard
	for id, card := range b.cards {
		if !pending[id] && card.MessageID != "" {
			maybe = append(maybe, card)
		}
	}
	b.mu.Unlock()
	var unsure []botCard
	for _, card := range maybe {
		switch s.botCardOver(ctx, card.Ix.ID) {
		case cardOver:
			s.feishuInteractionDone(card.Ix, i18n.T(s.botLang(ctx), "assistant-channels.ended"))
		case cardUnsure:
			unsure = append(unsure, card)
		}
	}
	if again && len(unsure) > 0 {
		time.AfterFunc(botCardRecheck, func() {
			cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.closeEndedBotCards(cctx, nil, false)
		})
	}
}

// channelLoop sweeps every few seconds: turns whose watcher was lost (a restart), every channel's.
func (s *Service) channelLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.sweepChannelRuns()
	}
}
