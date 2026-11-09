package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// Options are what a Service runs with.
type Options struct {
	Hub Hub
	// Store is read on every use: nil, or a func returning nil, means no database (unit tests); the state then lives
	// in memory only.
	Store func() Store
	Logf  func(format string, args ...any)
	// KB is the knowledge base it keeps (kb.go); nil: none.
	KB *KBOptions
	// Devices is the manager's own connections to the computers: with them, what comes from the nodes is its own
	// (fleet.go); nil (unit tests): no fleet.
	Devices Devices
	// Trust says which computers wait to be confirmed (machine_trust.go): the devices, from the hub's handover; nil
	// (unit tests): none.
	Trust MachineTrust
	// SecretKey is the path of the key the model providers' keys are sealed with (the manager's own secret.key,
	// models_launchers.go); "" makes one for this run only (tests).
	SecretKey string
	// ID is the manager this assistant is ("" and assistantwire.AssistantID: the built-in one). Its directories on a
	// computer are named after it (identity.AssistantDirName, docs/assistant/managers.md "Directories on a computer").
	ID string
}

// Service is the assistant. It implements Handler.
type Service struct {
	// id is Options.ID: the manager this assistant is.
	id string
	// nodeAnswering: the computers' requests being answered now, but the ones that wait on a person (idle.go).
	nodeAnswering atomic.Int64
	// ext is the extensions' tools in this process (extensions.go SetExtensions); nil: none (tests).
	ext ExtensionTools

	hub   Hub
	store func() Store
	// trustSrc is Options.Trust.
	trustSrc MachineTrust
	logf     func(format string, args ...any)
	// runRetry is how long after a failed run the retry goes (0: runRetryDelay; tests shorten it).
	runRetry time.Duration
	// turnPoll is how often a run's watcher reads its turn (0: turnPollInterval; tests shorten it).
	turnPoll time.Duration
	// taskRetry is how long a task waits before its one retry after a dropped link (0: taskStartRetryDelay; tests
	// shorten it).
	taskRetry time.Duration
	// superviseTimes are how supervision messages gather and space out (zero fields: the package's; tests shorten
	// them, supervise_signals.go superviseTimings).
	superviseTimes superviseTimings
	// retryNow is the clock a due retry checks the task's time against (retryStillDue; nil: time.Now, tests set it).
	retryNow func() time.Time

	mux    *http.ServeMux
	routes []string

	epoch string
	pubMu sync.Mutex
	seq   uint64

	startingAssistants assistantStarts // machine_privacy.go

	titles     titleBook         // titles.go
	prefs      settingsBook      // profile.go
	main       mainBook          // start.go
	archived   archiveBook       // conversations.go
	origins    originBook        // origins.go
	guests     guestBook         // guest.go
	relays     relayBook         // conversations.go
	quiet      quietBook         // quiet.go
	supervise  superviseBook     // supervise_signals.go
	reports    reportBook        // reports.go
	upReports  reportRate        // agent_control.go: report_to_supervisor
	noteRate   reportRate        // todo_decisions.go: todo_decision_add
	adoptMu    sync.Mutex        // agent_control.go: one agent_supervise at a time
	placeBook  placeBook         // places.go
	accept     acceptBook        // acceptance.go
	later      laterBook         // later.go: questions that can wait for a briefing
	brief      briefBook         // briefing.go
	reporters  reporterBook      // reporters.go
	sched      schedule          // schedule.go
	tasks      tasksState        // tasks.go
	questions  questions         // tools.go (ask_user)
	askTodos   askTodoBook       // ask_todo.go: the questions nobody answered, now to-dos
	live       contextState      // context.go
	todos      todoBook          // todos.go
	work       workBook          // agents_work.go: the Agents business
	reply      replyBook         // agents_reply.go: the reminders of questions left unanswered
	chans      channelsBook      // channels.go
	fbot       feishuBook        // feishu.go
	tposts     threadPoster      // feishu_thread_posts.go: what a todo tells its chat thread
	tevents    todoEvents        // todo_events.go: what the extensions are told of the todos
	standing   standingBoard     // feishu_board.go: the standing "waiting for you" card
	kb         kbState           // kb.go
	inbox      inboxBook         // inbox.go
	board      boardBook         // workspace_board.go: the workspace board
	viewEvents viewNotifications // views.go: only coalesced notifications; contents are stored first

	devs          Devices // the computers (devices.go); nil: no fleet
	agentArchives agentArchiveBook
	agentNative   agentNativeBook // the tools' own archives (agent_archive_native.go)
	fleet         fleetBook       // what comes from the nodes (fleet.go)
	// delegateHook stands in for Delegate in tests (fleet_delegate.go).
	delegateHook func(context.Context, DelegateParams) (DelegateResult, error)
	// fleetEnded, when set, sees every card's end the channels are told (tests).
	fleetEnded func(ix proto.Interaction, r *proto.Resolution)
	// ixResolved, when set, sees every answered card the assistant is told of (tests).
	ixResolved func(InteractionResolved)
	models     modelsState   // models.go: the model configuration
	health     healthBook    // launcher_health.go: each machine's launchers
	trust      trustBook     // machine_trust.go: the computers waiting to be confirmed
	ready      readinessBook // readiness.go: the main conversation's readiness

	// acceptSweepHook, when set, runs in the acceptance sweeps' unlocked window, after a todo is read (tests).
	acceptSweepHook func(id string)
}

var _ Handler = (*Service)(nil)

// New makes the assistant; nothing runs until the hub calls it.
func New(o Options) *Service {
	s := &Service{hub: o.Hub, store: o.Store, logf: o.Logf, mux: http.NewServeMux(), epoch: randomHex(8), tasks: newTasksState(),
		devs: o.Devices, trustSrc: o.Trust, id: o.ID}
	s.inbox.lang = func() i18n.Lang { return s.lang(context.Background()) }
	if s.store == nil {
		s.store = func() Store { return nil }
	}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	// The kept old keys too while a rotation keeps them (keyrotate.go).
	s.models.secret = secretkey.NewWithFallback(o.SecretKey)
	s.archiveRoutes()
	s.agentArchiveRoutes()
	s.alwaysRoutes()
	s.reporterRoutes()
	s.originRoutes()
	s.profileRoutes()
	s.readinessRoutes()
	s.startRoutes()
	s.scheduleRoutes()
	s.placeRoutes()
	s.taskRoutes()
	s.viewActionRoutes()
	s.todayRoutes()
	s.briefRoutes()
	s.todoRoutes()
	s.agentRoutes()
	s.workspaceBoardRoutes()
	s.channelRoutes()
	s.kbInit(o.KB)
	s.kbRoutes()
	s.inboxRoutes()
	s.modelConfigRoutes()
	s.modelProfileRoutes()
	s.launcherRoutes()
	s.machineLauncherRoutes()
	s.launcherQuotaRoutes()
	s.providerProbeRoutes()
	s.launcherImportRoutes()
	s.launcherHealthRoutes()
	return s
}

// handle adds one of the routes the hub relays.
func (s *Service) handle(pattern string, fn http.HandlerFunc) {
	s.routes = append(s.routes, pattern)
	s.mux.HandleFunc(pattern, fn)
}

func (s *Service) Routes() []string { return append([]string(nil), s.routes...) }

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// fullRole: the request came from a device that may do everything (RoleHeader; none: a test's own request).
func fullRole(r *http.Request) bool {
	role := r.Header.Get(RoleHeader)
	return role == "" || role == roleFull
}

// owner is how the assistant calls the user: manager.json's assistant.owner, else the Chinese word for "user".
func (s *Service) owner(ctx context.Context) string {
	if o := strings.TrimSpace(s.hello(ctx).Owner); o != "" {
		return o
	}
	return "用户"
}

// hello is the hub's answer to manager.hello (Hub.Hello); an error leaves it empty.
func (s *Service) hello(ctx context.Context) Hello {
	h, err := s.hub.Hello(ctx)
	if err != nil {
		s.logf("assistant: hello: %v", err)
	}
	return h
}

// lang is the user's language (the hub's hello, docs/protocol/i18n.md "Protocol"): what the assistant's own texts for people are
// written in when no request is in front of them (task notices, cards, records in a conversation).
func (s *Service) lang(ctx context.Context) i18n.Lang {
	return i18n.Lang(s.hello(ctx).Locale).Or(i18n.Default)
}

// call is a DeviceCall whose answer is read into out (nil: not read).
func (s *Service) call(ctx context.Context, device, method string, params, out any) error {
	if p, ok := params.(map[string]any); ok && method == DeviceAssistantSend {
		if id, _ := p["session_id"].(string); id != "" && s.supervising(id) {
			s.releasePushes(ctx, id) // something else speaks there: its turns are pushed again (a supervision message holds them anew)
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p := DeviceCall{Device: device, Method: method, Params: raw}
	res, ok, err := s.localFleetDeviceCall(ctx, p) // what the hub answered before the nodes' events were the manager's
	if !ok {
		res, err = s.hub.DeviceCall(ctx, p)
	}
	if err != nil || out == nil || len(res) == 0 {
		return err
	}
	return json.Unmarshal(res, out)
}

// appCall is an AppCall whose answer is read into out (nil: not read).
func (s *Service) appCall(ctx context.Context, app, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p := AppCall{AppID: app, Method: method, Params: raw}
	res, ok, err := s.localFleetAppCall(ctx, p) // the interaction queue (fleet_routes.go)
	if !ok {
		res, err = s.hub.AppCall(ctx, p)
	}
	if err != nil || out == nil || len(res) == 0 {
		return err
	}
	return json.Unmarshal(res, out)
}

// sessions are a machine's sessions as the apps get them; ErrNoDevice when the hub has no such machine.
func (s *Service) sessions(ctx context.Context, machine string) ([]proto.Session, error) {
	list, _, err := s.sessionsMuted(ctx, machine)
	return list, err
}

// sessionsMuted is sessions, and which of them are muted (the agent or its space: pushes off), by session id, as
// sessions.list says.
func (s *Service) sessionsMuted(ctx context.Context, machine string) ([]proto.Session, map[string]bool, error) {
	var res struct {
		Sessions []proto.Session `json:"sessions"`
		Muted    []string        `json:"muted"`
	}
	err := s.call(ctx, machine, DeviceSessionsList, map[string]any{}, &res)
	muted := make(map[string]bool, len(res.Muted))
	for _, id := range res.Muted {
		muted[id] = true
	}
	return res.Sessions, muted, err
}

// session is one session as the node listed it.
func (s *Service) session(ctx context.Context, machine, id string) (proto.Session, bool) {
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(ctx, machine, DeviceSessionGet, map[string]any{"session_id": id}, &res); err != nil {
		return proto.Session{}, false
	}
	return res.Session, res.OK
}

// devices are the user's machines.
func (s *Service) devices(ctx context.Context) []Device {
	devices, _ := s.devicesAndPhone(ctx)
	return devices
}

// devicesAndPhone are the user's machines, and whether the phone's app is connected (phone_* tools work now).
func (s *Service) devicesAndPhone(ctx context.Context) ([]Device, bool) {
	var res struct {
		Devices     []Device `json:"devices"`
		PhoneOnline bool     `json:"phone_online"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &res); err != nil {
		s.logf("assistant: devices: %v", err)
	}
	return res.Devices, res.PhoneOnline
}

// device is the machine by id; false when the hub has none.
func (s *Service) device(ctx context.Context, id string) (Device, bool) {
	for _, d := range s.devices(ctx) {
		if d.ID == id {
			return d, true
		}
	}
	return Device{}, false
}

// startParams are what a new conversation starts with.
type startParams struct {
	Text           string `json:"text"`
	PermissionMode string `json:"permission_mode,omitempty"`
	Purpose        string `json:"purpose,omitempty"` // the model configuration's purpose; "" is the assistant's
	Space          string `json:"space,omitempty"`   // "health" starts it in the health space (docs/assistant/health.md)
	Origin         string `json:"origin,omitempty"`  // where it comes from (origins.go); "" is the user's
	OriginRef      string `json:"origin_ref,omitempty"`
}

// kbReady: there is a knowledge base (kb.go; on, opened or about to).
func (s *Service) kbReady(ctx context.Context) bool { return s.kbOn(ctx) }

// push asks the hub to push n to the user (notify.request).
func (s *Service) push(ctx context.Context, n Notification) {
	n.ID, n.Kind = "ntf_"+randomHex(8), NotifyPush
	if n.Asked { // the conversation pushed what matters itself: its own finished push is left out (fleet_push.go)
		s.noteAsked(n.SessionID)
	}
	if err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n}); err != nil {
		s.logf("assistant: push: %v", err)
	}
}

// event tells the apps what changed (ws.broadcast: the hub sends ev to them as it is).
func (s *Service) event(ctx context.Context, ev map[string]any) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.hub.Broadcast(ctx, Broadcast{Message: raw})
}

// ---- the snapshot ----

// publish reports the whole snapshot (manager.snapshot) after a change; the hub shows it.
func (s *Service) publish(ctx context.Context) {
	s.pubMu.Lock()
	snap := s.snapshot(ctx)
	s.seq++
	snap.Epoch, snap.Seq, snap.At = s.epoch, s.seq, time.Now().UTC()
	s.pubMu.Unlock()
	if s.fleetOn() {
		s.fleetTakeSnapshot(snap) // the lists' decoration, its own cards on the phone (fleet_snapshot.go)
		snap.Interactions = nil
	}
	if _, err := s.hub.Snapshot(ctx, snap); err != nil && !errors.Is(err, ErrStaleSnapshot) {
		s.logf("assistant: snapshot: %v", err)
	}
}

func (s *Service) Resync(ctx context.Context) { s.publish(ctx) }

// snapshot is what the assistant shows now; parts that could not be read are left out and the snapshot is partial.
func (s *Service) snapshot(ctx context.Context) Snapshot {
	views := map[string]*SessionView{}
	view := func(id, machine string) *SessionView {
		v := views[id]
		if v == nil {
			v = &SessionView{MachineID: machine, SessionID: id}
			views[id] = v
		}
		return v
	}
	var snap Snapshot
	titles, ok := s.conversationTitles()
	snap.Partial = snap.Partial || !ok
	for id, t := range titles {
		view(id, t.machine).Title = t.title
	}
	archived, ok := s.archivedSet()
	snap.Partial = snap.Partial || !ok
	origins, ok := s.conversationOrigins()
	snap.Partial = snap.Partial || !ok
	for id, o := range origins {
		v := view(id, o.machine)
		v.Origin, v.OriginRef = o.origin, o.ref
	}
	for id, machine := range archived {
		view(id, machine).Archived = true
	}
	snap.Tasks = s.taskViews(ctx)
	snap.Partial = snap.Partial || snap.Tasks == nil
	snap.Todos = s.todoReport(ctx)
	snap.Partial = snap.Partial || snap.Todos == nil
	snap.Profile = s.profile(ctx, snap.Tasks, snap.Todos)
	snap.Partial = snap.Partial || snap.Profile == nil
	snap.Channels = s.channelsReport()
	snap.Partial = snap.Partial || snap.Channels == nil
	cfg := s.settings(ctx)
	here := cfg.MachineID
	queued, ok := s.queuedRelays()
	snap.Partial = snap.Partial || !ok
	for id, q := range queued {
		view(id, here).Queued = q // a conversation sent to is on the assistant's machine
	}
	for _, id := range s.heldConversations() {
		view(id, here).Supervising = true // a supervision message started its turn: not pushed (supervise_digest.go)
	}
	if cfg.Main != "" { // not snap.Profile: that is left out while a settings read fails
		view(cfg.Main, cfg.MachineID).Title = mainTitle // never its first message, a model's name or a rename (#1313)
	}
	snap.Inbox = s.inboxReport(ctx)
	snap.Partial = snap.Partial || snap.Inbox == nil
	snap.Work = s.workReport(ctx) // before the cards: they are read back with it
	snap.Partial = snap.Partial || snap.Work == nil
	snap.Interactions = append(s.pendingQuestions(), s.workCards()...)
	snap.Sessions = make([]SessionView, 0, len(views))
	for _, v := range views {
		snap.Sessions = append(snap.Sessions, *v)
	}
	sort.Slice(snap.Sessions, func(i, j int) bool { return snap.Sessions[i].SessionID < snap.Sessions[j].SessionID })
	return snap
}

// ---- what the hub saw ----

// UserSpoke: the user is talking in the conversation again: back in the list, and it works for them, not for a chain
// of conversations.
func (s *Service) UserSpoke(ctx context.Context, r SessionRef) {
	s.releasePushes(ctx, r.SessionID) // the user speaks: its turns are pushed as ever (supervise_digest.go)
	s.withdrawAskUser(r.SessionID)    // a question it waits on is taken back: the user's words are not left behind its turn
	s.unarchiveOnSend(r.MachineID, r.SessionID)
	s.userSpoke(r.SessionID)
	if s.reporterStopped(ctx, r.SessionID) { // talked to again: its tasks are told to it again (#3308)
		s.setReporterStopped(ctx, r.SessionID, "")
	}
}

// TurnStarted: one of a task's agents is working; what was going to be done about an agent's last turn is off.
func (s *Service) TurnStarted(ctx context.Context, r SessionRef) {
	s.taskWorking(ctx, r.MachineID, r.SessionID)
	s.workStarted(ctx, r.MachineID, r.SessionID, time.Now())
}

// TurnEnded: a task's agent may be done (in the background: it waits for the agents to settle), an agent's turn is
// triaged; a new conversation is named after its first turn.
func (s *Service) TurnEnded(ctx context.Context, r SessionRef) {
	go s.registerInlineTurn(context.WithoutCancel(ctx), r)
	go s.taskTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID)
	go s.workTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID)    // its triage (agents_work_turns.go)
	go s.channelTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID) // a channel's answer, the sync mirror
	// A usage limit marks the launcher there until it resets (launcher_health.go).
	go s.launcherLimitTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID)
	s.titleAfterTurn(ctx, r.MachineID, r.SessionID)
}

// InteractionResolved: the user dealt with a card of a task's agent: the conversation keeps a record of it, and the
// todo of the agent the calls made in it (its decisions).
func (s *Service) InteractionResolved(ctx context.Context, r InteractionResolved) {
	if f := s.ixResolved; f != nil {
		f(r)
	}
	s.taskAnswered(ctx, r.Interaction, r.Resolution, r.Status)
	s.cardDecisions(ctx, r.Interaction, r.Resolution, r.Status)
	// A quiet task's waiting notice may still be in its delay window or queued behind a turn: drop it now unless the
	// task still waits on another card. An agent machine the recheck cannot reach is not an answer, so the notice
	// is left for the drain-time recheck.
	if t, ok := s.taskOf(ctx, r.Interaction.MachineID, r.Interaction.SessionID); r.Status != "failed" && ok && t.Quiet {
		if waits, known := s.taskNeedsRecheck(ctx, t); known && !waits {
			if s.dropQueuedNeeds(t.ID) {
				s.publish(ctx)
			}
		}
	}
}

// SessionsChanged: messages from other conversations go to those no longer running; the todos follow the list
// (todo_sessions.go).
func (s *Service) SessionsChanged(ctx context.Context, d DeviceSessions) {
	s.drainRelays(d.MachineID, d.Sessions)
	s.originsChanged(d.MachineID, d.Sessions) // conversations from before origins were noted (origins.go)
	s.todosSessionsChanged(ctx, d)
	s.nativeArchivesListed(d.MachineID) // agent_archive_native.go
	s.followStarts(ctx, d)              // the app's starts, for launcher health (launcher_health_starts.go)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
