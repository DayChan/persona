// Package proc is a connector that runs in a program of its own (connector/serve) and answers for it in the hub. It
// implements every capability and declares what the program said it has (a proxy's manifest): the hub uses only
// those. The program is either a plugin's, run by the plugin host (Config.Link: the hub's way, docs/extensions/extensions.md), or
// started here with the connector protocol's own opening (Config.Path). The Telegram extension is one (#1704): its
// dependencies would double personad's size (docs/archive/extensions/plan-telegram.md).
package proc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Link is a plugin's process (plugin/host) as one of its connectors reaches it: calls go to the connector namespace
// with the connector's id as the contribution; the process runs while the connector holds it.
type Link struct {
	Proc *host.Proc
	ID   string // the contribution: the connector's id
}

func (l Link) call(ctx context.Context, method string, params, out any) error {
	p, err := extension.Targeted(params, l.ID)
	if err != nil {
		return err
	}
	err = l.Proc.Call(ctx, serve.InPlugin(method), p, out)
	if errors.Is(err, host.ErrNotRunning) {
		return fmt.Errorf("%w", errNotRunning)
	}
	return err
}

// Config is how to run the program.
type Config struct {
	// ID and Name are the connector's before the program says (while it starts, or cannot).
	ID, Name string
	// Manifest is what the program declares, known before it ever runs: a connector switched off from the start
	// still lists its capabilities and can be signed in to and out of. The program's own (at init) is checked against
	// it, a difference logged; this one is used. Without capabilities here, the program's is used once it runs.
	Manifest connector.Manifest
	Path     string // the program (next to personad)
	Args     []string
	Env      []string // added to the hub's environment
	// Version is what `<Path> version -v` must print (the hub's own): the two come from one build. "" skips it.
	Version string
	// Settings is sent with init: the connector's section of hub.json. SettingsNow, when set, is asked instead at
	// each start (what the hub learns later: the user's time zone).
	Settings    json.RawMessage
	SettingsNow func() json.RawMessage
	// Secrets is the hub's store for this connector: the program's secrets.* calls reach only this one.
	Secrets connector.Secrets
	// KB is the knowledge base's side of the program's posts.* and archive.chats (nil: none here); only a connector
	// declaring an archive may use it.
	KB   connector.KnowledgeBase
	Logf func(string, ...any)
	// Link, when set, is the plugin process the connector runs in: Path, Args, Env, Version and Settings are then the
	// plugin host's business, and the hub passes the program's calls and events on (Handle, Event).
	Link *Link
	// GhToken, when set, answers the program's credentials.gh_token: a computer's gh login. The hub sets it only for
	// an extension it granted gh_login (docs/extensions/extensions.md "Permissions"); nil refuses.
	GhToken func(ctx context.Context, machine, host string) (string, error)
	// RecallNotBefore, when set, is when this install of the extension began (extension.Record.InstalledAt): a send the
	// ledger wrote before it was not this extension's (an earlier one with the connector's id, since uninstalled), so
	// Recall does not offer it to the program. Zero: no such limit (built-in and hub.json extensions, which are never
	// replaced by another under the same id).
	RecallNotBefore time.Time
}

// Restarts of the program after it exits: doubling from the first to the last, back to the first once it ran a while.
var (
	restartFirst = 5 * time.Second
	restartLast  = 5 * time.Minute
	ranAWhile    = time.Minute
	statusEvery  = 30 * time.Second // how often a running program's status is asked (Status answers from memory)
	callTimeout  = 2 * time.Minute
)

// Conn is the connector of one program.
type Conn struct {
	cfg    Config
	events chan struct{} // a scope is pending; one slot, so Watch wakes once for any number of them

	mu sync.Mutex
	// pending are the scopes the program reported changed that Watch has not passed on yet: one each, so a backlog
	// of one scope never pushes out another's.
	pending map[connector.Scope]bool
	peer    *rpc.Peer // nil while the program is not running
	man     serve.Manifest
	tools   []proto.Tool
	actions []proto.Tool
	status  connector.Status
	// options are the connector's own settings (connector.Options) as the program last gave them; sent (true) once
	// the running program has the ones the hub set, which go again each time it starts.
	options json.RawMessage
	sent    bool
	// auth is the user's choice of where the sign-in comes from (SetAuth), given again at each start: the program
	// does not keep it.
	auth *serve.AuthSource
	// trouble is what failed in the last refresh ("" nothing), so the same failure is logged once (noteTrouble).
	trouble string
	// refreshing is the round of fetch running (nil when none); again is the round asked for while it runs, which
	// starts when it ends.
	refreshing, again *round
	// run is the context of the Run under way (hold), nil outside one: what calls with no caller's context of their
	// own (MachineOnline, SetOptions) use, so they end with the connector instead of outliving it.
	run context.Context
}

// round is one fetch that every refresh asked for meanwhile shares. Its context ends only once every asker's has,
// so one asker giving up (a sign-in request whose client went away) does not cancel it for the others.
type round struct {
	done   chan struct{} // closed when the fetch is done
	ctx    context.Context
	cancel context.CancelFunc
	live   int           // askers whose context has not ended; guarded by Conn.mu
	stops  []func() bool // unregister the askers' context.AfterFunc once the round is over
}

// New is the connector of the program cfg describes; nothing runs until Run.
func New(cfg Config) *Conn {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Conn{cfg: cfg, events: make(chan struct{}, 1), pending: map[connector.Scope]bool{},
		man:    serve.Manifest{ID: cfg.ID, Name: cfg.Name},
		status: connector.Status{State: connector.StateError, Error: cfg.Name + " 进程还没启动"}}
}

func (c *Conn) ID() string { return c.cfg.ID }

func (c *Conn) Name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.man.Name
}

// Manifest is the one Config declares, as a proxy's; without one, what the program said at init (nothing before).
// Its status is always the hub's answer for it (connector.StatusOf asks a proxy's).
func (c *Conn) Manifest() connector.Manifest {
	if len(c.cfg.Manifest.Capabilities) > 0 {
		m := c.cfg.Manifest
		m.Proxy = true
		return m
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.man.Of()
}

// sameManifest says whether the program declares what Config does (capabilities in any order).
func sameManifest(declared, said connector.Manifest) bool {
	norm := func(m connector.Manifest) connector.Manifest {
		m.Capabilities = slices.Clone(m.Capabilities)
		slices.Sort(m.Capabilities)
		m.Proxy = false
		return m
	}
	a, b := norm(declared), norm(said)
	return slices.Equal(a.Capabilities, b.Capabilities) && slices.Equal(a.Hosts, b.Hosts) && slices.Equal(a.AgentTools, b.AgentTools) &&
		slices.Equal(a.Unlisted, b.Unlisted) && slices.Equal(a.ConfirmAlways, b.ConfirmAlways) && a.Poll == b.Poll && slices.Equal(a.Kinds, b.Kinds) &&
		slices.Equal(a.ArchiveSources, b.ArchiveSources) && a.MetaDescription == b.MetaDescription && a.CLI == b.CLI
}

// Status answers from memory: the program's last status, or why it is not running.
func (c *Conn) Status(context.Context) connector.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Conn) setDown(why string) {
	c.mu.Lock()
	c.peer = nil
	c.status = connector.Status{State: connector.StateError, Error: why}
	c.mu.Unlock()
}

// Run keeps the program running until ctx ends: checks it is the hub's build, starts it, restarts it after a
// backoff when it exits. A linked one holds its plugin's process instead, which the plugin host runs.
func (c *Conn) Run(ctx context.Context) {
	ctx, stop := c.hold(ctx)
	defer stop()
	if c.cfg.Link != nil {
		c.runLinked(ctx)
		return
	}
	wait := restartFirst
	for ctx.Err() == nil {
		start := time.Now()
		ch, err := c.spawn(ctx)
		if err == nil {
			c.mu.Lock()
			c.peer, c.sent = ch.peer, false
			c.mu.Unlock()
			c.refresh(ctx)
			c.keepUp(ctx, ch)
			ch.stop()
		}
		if ctx.Err() != nil {
			c.setDown(c.cfg.Name + " 已停止")
			return
		}
		if time.Since(start) >= ranAWhile {
			wait = restartFirst
		}
		why := c.cfg.Name + " 进程在重启"
		if err != nil {
			why = err.Error()
		}
		c.setDown(why)
		c.cfg.Logf("connector %s: the program stopped (%v), again in %s", c.cfg.ID, ch.err(err), wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, restartLast)
	}
}

// hold makes ctx the connector's run (life) until stop, which ends what was started under it.
func (c *Conn) hold(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.run = ctx
	c.mu.Unlock()
	return ctx, func() {
		c.mu.Lock()
		if c.run == ctx {
			c.run = nil
		}
		c.mu.Unlock()
		cancel()
	}
}

// life is the context of the connector's run; outside one, a background one (a call then finds the program not
// running, as before Run).
func (c *Conn) life() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return context.Background()
	}
	return c.run
}

// runLinked holds the plugin's process while ctx lasts, refreshing the status and tools each time it comes up and
// every so often while it is up.
func (c *Conn) runLinked(ctx context.Context) {
	p := c.cfg.Link.Proc
	release := p.Hold()
	defer release()
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	up := false
	for {
		changes := p.Changes()
		switch ready := p.Ready(); {
		case ready && !up:
			up = true
			c.mu.Lock()
			c.sent = false
			c.mu.Unlock()
			c.refresh(ctx)
		case !ready:
			up = false
			state, why := p.Status()
			switch state {
			case host.StateError:
			case host.StateStarting:
				why = c.cfg.Name + " 正在启动"
			default:
				why = c.cfg.Name + " 进程还没启动"
			}
			c.setDown(why)
		}
		select {
		case <-ctx.Done():
			c.setDown(c.cfg.Name + " 已停止")
			return
		case <-changes:
		case <-t.C:
			if up {
				c.refresh(ctx)
			}
		}
	}
}

// Handle answers a linked program's calls (connector.secrets.*, without the namespace), as fromProgram.
func (c *Conn) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return c.fromProgram(ctx, method, params)
}

// Event takes a linked program's event (changed, without the namespace).
func (c *Conn) Event(event string, params json.RawMessage) { c.onEvent(event, params) }

// keepUp refreshes the status every so often until the program exits or ctx ends.
func (c *Conn) keepUp(ctx context.Context, ch *child) {
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch.peer.Done():
			return
		case <-t.C:
			c.refresh(ctx)
		}
	}
}

// refresh asks the running program for its status and tools and waits for the answer (or ctx), one ask at a time:
// see kick.
func (c *Conn) refresh(ctx context.Context) {
	select {
	case <-c.kick(ctx):
	case <-ctx.Done():
	}
}

// kick starts a refresh and returns a channel closed when it is done. While one runs, it is not run alongside (a
// program that says changed often would have many in flight, and an older answer landing last would overwrite a
// newer one): one more round is due when it ends, which all the asks meanwhile share.
func (c *Conn) kick(ctx context.Context) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing != nil {
		if c.again == nil {
			c.again = newRound(ctx)
		}
		c.join(c.again, ctx)
		return c.again.done
	}
	r := newRound(ctx)
	c.join(r, ctx)
	c.refreshing = r
	go c.refreshRounds(r)
	return r.done
}

// newRound is a round not yet run, whose context keeps ctx's values but not its end (join adds that).
func newRound(ctx context.Context) *round {
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &round{done: make(chan struct{}), ctx: rctx, cancel: cancel}
}

// join makes ctx one of r's askers: r's context ends once every asker's has. c.mu is held.
func (c *Conn) join(r *round, ctx context.Context) {
	r.live++
	r.stops = append(r.stops, context.AfterFunc(ctx, func() {
		c.mu.Lock()
		r.live--
		last := r.live == 0
		c.mu.Unlock()
		if last {
			r.cancel()
		}
	}))
}

// refreshRounds runs fetch for r, then the round asked for meanwhile, until none is.
func (c *Conn) refreshRounds(r *round) {
	for r != nil {
		c.fetch(r.ctx)
		close(r.done)
		c.mu.Lock()
		stops := r.stops
		c.refreshing, c.again = c.again, nil
		next := c.refreshing
		c.mu.Unlock()
		for _, stop := range stops {
			stop()
		}
		r.cancel()
		r = next
	}
}

// fetch asks the running program for its status and tools. A tools or actions list whose call fails keeps the
// last one; a status call that fails puts the connector in error, so a hung program is not shown as healthy.
func (c *Conn) fetch(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, callTimeout)
	defer cancel()
	var call func(method string, params, out any) error
	var peer *rpc.Peer // the child's run asked; nil for a linked program
	if l := c.cfg.Link; l != nil {
		if !l.Proc.Ready() {
			return
		}
		call = func(method string, params, out any) error { return l.call(ctx, method, params, out) }
	} else {
		c.mu.Lock()
		peer = c.peer
		c.mu.Unlock()
		if peer == nil {
			return
		}
		call = func(method string, params, out any) error { return peer.Call(ctx, method, params, out) }
	}
	c.syncOptions(ctx, call)
	var st connector.Status
	if err := call(serve.MStatus, struct{}{}, &st); err != nil {
		if parent.Err() != nil {
			return // stopping, not a fault of the program
		}
		c.mu.Lock()
		if c.cfg.Link != nil || c.peer == peer {
			c.status = connector.Status{State: connector.StateError, Error: c.cfg.Name + " 没有回应：" + err.Error()}
		}
		c.mu.Unlock()
		c.noteTrouble(serve.MStatus + ": " + err.Error())
		return
	}
	tools, toolsErr := listTools(call, serve.MTools)
	var actions []proto.Tool
	var actionsErr error
	if c.Manifest().Has(connector.CapActions) {
		actions, actionsErr = listTools(call, serve.MActions)
	}
	c.mu.Lock()
	if c.cfg.Link != nil || c.peer == peer {
		c.status = st
		if toolsErr == nil {
			c.tools = tools
		}
		if actionsErr == nil {
			c.actions = actions
		}
	}
	c.mu.Unlock()
	if parent.Err() != nil {
		return
	}
	var trouble []string
	for _, e := range []struct {
		method string
		err    error
	}{{serve.MTools, toolsErr}, {serve.MActions, actionsErr}} {
		if e.err != nil {
			trouble = append(trouble, e.method+": "+e.err.Error()+" (the last list is kept)")
		}
	}
	c.noteTrouble(strings.Join(trouble, "; "))
}

// listTools asks for the program's tools or actions; one answering it has none of them has an empty list.
func listTools(call func(method string, params, out any) error, method string) ([]proto.Tool, error) {
	var r serve.ToolsResult
	err := call(method, struct{}{}, &r)
	if re := (*rpc.Error)(nil); errors.As(err, &re) && re.Code == serve.CodeNoCapability {
		return nil, nil
	}
	return r.Tools, err
}

// noteTrouble logs what went wrong in a refresh ("" nothing) when it differs from the last refresh's, so a program
// failing every refresh the same way is logged once and again when it recovers.
func (c *Conn) noteTrouble(trouble string) {
	c.mu.Lock()
	last := c.trouble
	c.trouble = trouble
	c.mu.Unlock()
	switch {
	case trouble == last:
	case trouble == "":
		c.cfg.Logf("connector %s: the program answers again", c.cfg.ID)
	default:
		c.cfg.Logf("connector %s: refresh: %s", c.cfg.ID, trouble)
	}
}

// child is one run of the program.
type child struct {
	cmd    *exec.Cmd
	peer   *rpc.Peer
	stdin  io.Closer
	served chan error
}

func (ch *child) err(start error) error {
	if start != nil || ch == nil {
		return start
	}
	return ch.peer.Err()
}

// stop closes the program's stdin (it exits when the hub goes away) and kills it if it does not.
func (ch *child) stop() {
	_ = ch.stdin.Close()
	ch.peer.Close(rpc.ErrClosed)
	done := make(chan struct{})
	go func() { _ = ch.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = ch.cmd.Process.Kill()
		<-done
	}
}

// spawn checks the program and starts it, sending init.
func (c *Conn) spawn(ctx context.Context) (*child, error) {
	if _, err := os.Stat(c.cfg.Path); err != nil {
		return nil, fmt.Errorf("没有找到 %s 的程序（%s）", c.cfg.Name, c.cfg.Path)
	}
	if c.cfg.Version != "" {
		vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		vcmd := exec.CommandContext(vctx, c.cfg.Path, "version", "-v")
		vcmd.Env = append(os.Environ(), c.cfg.Env...)
		out, err := vcmd.Output()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s 的程序运行不了：%v", c.cfg.Name, err)
		}
		if got := strings.TrimSpace(string(out)); got != strings.TrimSpace(c.cfg.Version) {
			return nil, fmt.Errorf("%s 的程序和 hub 不是同一个版本（%s / %s）", c.cfg.Name, firstLine(got), firstLine(c.cfg.Version))
		}
	}
	cmd := exec.Command(c.cfg.Path, c.cfg.Args...)
	cmd.Env = append(os.Environ(), c.cfg.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s 的程序起不来：%v", c.cfg.Name, err)
	}
	go c.logLines(stderr)
	peer := rpc.New("h", stdin)
	peer.OnRequest = c.fromProgram
	peer.OnEvent = c.onEvent
	ch := &child{cmd: cmd, peer: peer, stdin: stdin, served: make(chan error, 1)}
	go func() { ch.served <- peer.Serve(context.Background(), stdout) }()
	ictx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	settings := c.cfg.Settings
	if c.cfg.SettingsNow != nil {
		settings = c.cfg.SettingsNow()
	}
	var man serve.Manifest
	if err := peer.Call(ictx, serve.MInit, serve.InitParams{Protocol: serve.Protocol, Config: settings}, &man); err != nil {
		ch.stop()
		return nil, fmt.Errorf("%s 的程序没准备好：%v", c.cfg.Name, err)
	}
	if man.Protocol != serve.Protocol {
		ch.stop()
		return nil, fmt.Errorf("%s 的程序用的协议版本是 %d，hub 要 %d", c.cfg.Name, man.Protocol, serve.Protocol)
	}
	if man.ID != c.cfg.ID {
		ch.stop()
		return nil, fmt.Errorf("the program says it is %q, not %q", man.ID, c.cfg.ID)
	}
	if d := c.cfg.Manifest; len(d.Capabilities) > 0 && !sameManifest(d, man.Of()) {
		c.cfg.Logf("connector %s: the program declares %v, the hub registered it with %v (the hub's is used)", c.cfg.ID, man.Of(), d)
	}
	c.mu.Lock()
	c.man = man
	c.mu.Unlock()
	return ch, nil
}

// logLineMost is the longest line of a program's log kept whole; a longer one is cut (the rest of it dropped).
const logLineMost = 64 << 10

// logLines keeps what the program writes to stderr (its own log: it keeps secrets and messages out of it), until
// stderr ends. Nothing else reads it: stopping early (a line too long) would leave the program stuck on its next
// write once the pipe is full (#3959).
func (c *Conn) logLines(r io.Reader) {
	br := bufio.NewReaderSize(r, logLineMost)
	for {
		b, err := br.ReadSlice('\n')
		line := strings.TrimRight(string(b), "\r\n")
		if errors.Is(err, bufio.ErrBufferFull) {
			line += "…"
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
		}
		if len(b) > 0 {
			c.cfg.Logf("connector %s: %s", c.cfg.ID, line)
		}
		if err != nil {
			return
		}
	}
}

// fromProgram answers the program's calls: its secret, and only its own (the hub bound Secrets to this connector;
// nothing the program sends picks another), and the knowledge base's (KB, bound the same way).
func (c *Conn) fromProgram(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case serve.MGhToken:
		return c.ghToken(ctx, params)
	case serve.MPostsPut, serve.MPostsComments, serve.MPostsPending, serve.MArchiveChats:
		return c.fromProgramKB(ctx, method, params)
	}
	if c.cfg.Secrets == nil {
		return nil, errors.New("no secrets store")
	}
	switch method {
	case serve.MSecretsLoad:
		data, err := c.cfg.Secrets.Load(ctx)
		return serve.SecretData{Data: data}, err
	case serve.MSecretsSave:
		var p serve.SecretData
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, c.cfg.Secrets.Save(ctx, p.Data)
	case serve.MSecretsDelete:
		return struct{}{}, c.cfg.Secrets.Delete(ctx)
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

func (c *Conn) fromProgramKB(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if !c.Manifest().Has(connector.CapArchive) {
		return nil, &rpc.Error{Code: serve.CodeNoCapability, Message: c.cfg.Name + " does not declare an archive"}
	}
	if c.cfg.KB == nil {
		return nil, errors.New("no knowledge base on this hub")
	}
	switch method {
	case serve.MPostsPut:
		var p serve.PostsPutParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		st, err := c.cfg.KB.PutPosts(ctx, p.Posts)
		return serve.PostsPutResult{Statuses: st}, err
	case serve.MPostsComments:
		var p serve.PostsCommentsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, c.cfg.KB.PutComments(ctx, p.ID, p.CommentCount, p.Comments)
	case serve.MPostsPending:
		var p serve.PostsPendingParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ps, err := c.cfg.KB.PendingPosts(ctx, p.Limit)
		return serve.PostsPendingResult{Posts: ps}, err
	default:
		var p serve.ArchiveChatsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		cs, err := c.cfg.KB.ArchivedChats(ctx, p.Source)
		return serve.ArchiveChatsResult{Chats: cs}, err
	}
}

func (c *Conn) onEvent(event string, params json.RawMessage) {
	if event != serve.EChanged {
		return
	}
	var e serve.ChangedEvent
	if json.Unmarshal(params, &e) != nil {
		return
	}
	scope := connector.Scope(e.Scope)
	if !scope.Known() {
		c.cfg.Logf("connector %s: unknown scope %q changed", c.cfg.ID, e.Scope)
		return
	}
	c.mu.Lock()
	c.pending[scope] = true
	c.mu.Unlock()
	select {
	case c.events <- struct{}{}:
	default: // Watch is woken already and takes this scope too
	}
}

// Watch passes the program's changes on until ctx ends; the hub restarts it like any connector's.
func (c *Conn) Watch(ctx context.Context, changed func(connector.Scope)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.events:
			c.mu.Lock()
			pending := c.pending
			c.pending = map[connector.Scope]bool{}
			c.mu.Unlock()
			if len(pending) == 0 { // the scopes went with the wake before, as onEvent's signal came after
				continue
			}
			c.kick(ctx) // what changed may be its status too (signed out elsewhere)
			for _, scope := range []connector.Scope{connector.ScopeChats, connector.ScopeItems, connector.ScopeInbox} {
				if pending[scope] {
					changed(scope)
				}
			}
		}
	}
}

var errNotRunning = errors.New("not running")

// call runs one method on the running program.
func (c *Conn) call(ctx context.Context, method string, params, out any) error {
	if l := c.cfg.Link; l != nil {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		err := l.call(ctx, method, params, out)
		if errors.Is(err, errNotRunning) {
			return fmt.Errorf("%s: %w", c.cfg.Name, errNotRunning)
		}
		return err
	}
	c.mu.Lock()
	peer := c.peer
	c.mu.Unlock()
	if peer == nil {
		return fmt.Errorf("%s: %w", c.cfg.Name, errNotRunning)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return peer.Call(ctx, method, params, out)
}

// LoginStep needs the program running: a sign-in keeps its state (Telegram's code request) in it between steps.
func (c *Conn) LoginStep(ctx context.Context, step string, values map[string]string) (connector.Prompt, error) {
	var p connector.Prompt
	if err := c.call(ctx, serve.MLoginStep, serve.LoginParams{Step: step, Values: values}, &p); err != nil {
		if errors.Is(err, errNotRunning) {
			return connector.Prompt{Step: step, Error: "先打开「同步 " + c.cfg.Name + "」再登录"}, nil
		}
		return p, err
	}
	c.refresh(ctx)
	return p, nil
}

// Logout signs out through the program; while it is not running (switched off), a run of it just for this.
func (c *Conn) Logout(ctx context.Context) error {
	err := c.call(ctx, serve.MLogout, struct{}{}, nil)
	if !errors.Is(err, errNotRunning) {
		c.refresh(ctx)
		return err
	}
	if l := c.cfg.Link; l != nil {
		release := l.Proc.Hold()
		defer release()
		wctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		if err := l.Proc.Wait(wctx); err != nil {
			return err
		}
		return c.call(ctx, serve.MLogout, struct{}{}, nil)
	}
	ch, err := c.spawn(ctx)
	if err != nil {
		return err
	}
	defer ch.stop()
	lctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return ch.peer.Call(lctx, serve.MLogout, struct{}{}, nil)
}

func (c *Conn) Changes(ctx context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	var out serve.ChangesResult
	err := c.call(ctx, serve.MChanges, serve.ChangesParams{Cursor: cursor, Limit: limit}, &out)
	if err != nil {
		return nil, cursor, false, err
	}
	return out.Batches, out.Next, out.More, nil
}

func (c *Conn) Collect(ctx context.Context) ([]connector.InboxThing, error) {
	var out serve.CollectResult
	err := c.call(ctx, serve.MCollect, struct{}{}, &out)
	return out.Things, err
}

func (c *Conn) Reply(ctx context.Context, r connector.InboxReply) error {
	return c.call(ctx, serve.MReply, r, nil)
}

// GroupTool runs one of the program's tools for relating messages (group.tool); the answer is its JSON as it is.
func (c *Conn) GroupTool(ctx context.Context, call connector.GroupToolCall) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.call(ctx, serve.MGroupTool, call, &out)
	return out, err
}

func (c *Conn) GroupAppend(ctx context.Context, a connector.GroupAppend) (connector.InboxThing, error) {
	var out connector.InboxThing
	err := c.call(ctx, serve.MGroupAppend, a, &out)
	return out, err
}

func (c *Conn) GroupFallback(ctx context.Context, f connector.GroupFallback) (connector.GroupFallbackResult, error) {
	var out connector.GroupFallbackResult
	err := c.call(ctx, serve.MGroupFallback, f, &out)
	return out, err
}

// Tools answers from memory: what the program listed when it was last asked.
func (c *Conn) Tools() []proto.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.tools)
}

func (c *Conn) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var out serve.CallResult
	err := c.call(ctx, serve.MCall, serve.CallParams{Name: name, Args: args}, &out)
	return out.Text, err
}

// Actions answers from memory, as Tools.
func (c *Conn) Actions() []proto.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.actions)
}

func (c *Conn) Describe(ctx context.Context, name string, args json.RawMessage) (connector.Action, string, error) {
	var out serve.DescribeResult
	err := c.call(ctx, serve.MDescribe, serve.CallParams{Name: name, Args: args}, &out)
	return out.Action, out.Summary, err
}

// Do sends the write once, with an id of its own: the program runs one id at most once, and Do never sends it again
// (a run that timed out may have happened; only the user asks again).
func (c *Conn) Do(ctx context.Context, name string, args json.RawMessage) (string, error) {
	done, err := c.DoRef(ctx, name, args)
	return done.Text, err
}

// DoRef is Do with the message_ref the program answered, when it did (connector.RefActions): a program that does not
// know the field answers the text alone, and the ref is empty.
func (c *Conn) DoRef(ctx context.Context, name string, args json.RawMessage) (connector.DoResult, error) {
	var id [16]byte
	_, _ = rand.Read(id[:])
	var out serve.CallResult
	err := c.call(ctx, serve.MDo, serve.DoParams{Name: name, Args: args, RequestID: hex.EncodeToString(id[:])}, &out)
	return connector.DoResult{Text: out.Text, MessageRef: out.MessageRef}, err
}

func (c *Conn) Thread(ctx context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	var out connector.ThreadPage
	err := c.call(ctx, serve.MThread, serve.ThreadParams{Thread: thread, Before: before, Limit: limit}, &out)
	return out, err
}

func (c *Conn) Media(ctx context.Context, ref string) (connector.Media, error) {
	var out connector.Media
	err := c.call(ctx, serve.MMedia, serve.MediaParams{Ref: ref}, &out)
	return out, err
}

// ItemSource is the program's knowledge-base items (items.list, items.doc_title); the hub asks only when it declared
// items.
func (c *Conn) ItemSource() kb.ItemSource { return connItems{c} }

type connItems struct{ c *Conn }

func (s connItems) Items(ctx context.Context) ([]kbwire.Item, error) {
	var out serve.ItemsResult
	err := s.c.call(ctx, serve.MItems, struct{}{}, &out)
	return out.Items, err
}

// DocTitle is "" when the program cannot tell (or fails): a title is a nicety.
func (s connItems) DocTitle(ctx context.Context, url string) string {
	var out serve.DocTitleResult
	if s.c.call(ctx, serve.MDocTitle, serve.DocTitleParams{URL: url}, &out) != nil {
		return ""
	}
	return out.Title
}

// ArchiveMedia is an archived message's attachment (archive.media); a program without it answers unknown_method and
// the line keeps the [attachment] marker (in Chinese).
func (c *Conn) ArchiveMedia(ctx context.Context, ref string) (connector.Media, error) {
	var out connector.Media
	err := c.call(ctx, serve.MArchiveMedia, serve.MediaParams{Ref: ref}, &out)
	return out, err
}

// HistoryChats are the conversations the program can import the history of (history.chats); the hub asks only a
// program declaring history. An error (with its rpc code) comes back as the program said it.
func (c *Conn) HistoryChats(ctx context.Context) ([]connectorwire.HistoryChat, error) {
	var out serve.HistoryChatsResult
	err := c.call(ctx, serve.MHistoryChats, struct{}{}, &out)
	return out.Chats, err
}

// History is one page of a conversation (archive.history). A call that timed out may still have been read by the
// program: it is not sent again here (docs/protocol/connector-protocol.md "History").
func (c *Conn) History(ctx context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	var out connectorwire.HistoryPage
	err := c.call(ctx, serve.MHistory, q, &out)
	return out, err
}

// HistoryAnchors are the owner's messages over a window (history.anchors), for a program declaring history_anchors.
func (c *Conn) HistoryAnchors(ctx context.Context, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	var out connectorwire.HistoryAnchors
	err := c.call(ctx, serve.MHistoryAnchors, q, &out)
	return out, err
}

func firstLine(s string) string {
	line, _, _ := bytes.Cut([]byte(s), []byte("\n"))
	return string(line)
}

// The capabilities Conn answers for: what the hub's pipes look for.
var (
	_ connector.Accounts       = (*Conn)(nil)
	_ connector.Login          = (*Conn)(nil)
	_ connector.Archive        = (*Conn)(nil)
	_ connector.Inbox          = (*Conn)(nil)
	_ connector.ReadTools      = (*Conn)(nil)
	_ connector.Actions        = (*Conn)(nil)
	_ connector.RefActions     = (*Conn)(nil)
	_ connector.Threads        = (*Conn)(nil)
	_ connector.Events         = (*Conn)(nil)
	_ connector.Runner         = (*Conn)(nil)
	_ connector.Items          = (*Conn)(nil)
	_ connector.ArchiveMedia   = (*Conn)(nil)
	_ connector.Snapshot       = (*Conn)(nil)
	_ connector.Grouper        = (*Conn)(nil)
	_ connector.EventWriter    = (*Conn)(nil)
	_ connector.History        = (*Conn)(nil)
	_ connector.HistoryAnchors = (*Conn)(nil)
)

// Snapshot is the program's live state (snapshot.get).
func (c *Conn) Snapshot(ctx context.Context, tz string) (connector.SnapshotData, error) {
	var out connector.SnapshotData
	err := c.call(ctx, serve.MSnapshot, serve.SnapshotParams{TZ: tz}, &out)
	return out, err
}
