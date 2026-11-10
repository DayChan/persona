// Package serve runs a connector in a program of its own, for the hub (connector/proc): the hub starts the program
// and talks to it over its stdin / stdout with the hub↔node JSON-lines RPC (internal/rpc). One connector per program.
// Connector programs are written on the Go SDK now (sdk/go/ext, docs/protocol/connector-protocol.md): this runner (Main,
// MainWith, Serve) serves only the connector conformance kit (connector/conformance) and tests, and connector/proc
// speaks the method names and types here.
//
// The hub calls the methods below; the program calls back for its secret (secrets.*: the hub binds them to this
// program's connector) and tells the hub what changed (the changed event). Nothing here logs what crosses the pipe.
// A write (actions.do) comes only once the hub's gate let it through; the program never starts one itself.
//
// A program answers two openings: init and the bare method names (connector protocol 2 on its own), and the plugin
// protocol's initialize with the same methods in the connector namespace (connector.status, …; docs/extensions/extensions.md).
// The extension manager runs connector programs as plugins (connector/proc with a Link). Only connector/proc's own tests
// still start a program with init (Config.Path), and the conformance kit checks both openings, so both stay.
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Build makes the connector once the hub has sent its configuration (hub.json's section for it); secrets is the
// hub's store for this connector.
type Build func(config json.RawMessage, secrets connector.Secrets) (connector.Connector, error)

// Init is what a connector program is started with.
type Init struct {
	Config json.RawMessage
	// Declared are, under the plugin protocol, the secrets the plugin's manifest declares and the user filled in (an
	// API key), by name; the connector's own state still goes to the hub's store (secrets).
	Declared map[string]string
	// DataDir is the plugin's own directory for caches (plugin protocol only).
	DataDir string
	// Channel is the hub for a connector that is also a channel (docs/extensions/channels.md): it hands the owner's messages
	// over. Plugin protocol only; nil under the bare opening.
	Channel channel.Hub
	// Locale is the user's language as the hub said it at this start (plugin protocol only; "" under the bare
	// opening): what the connector writes for people reads in it.
	Locale string
}

// Listener is a connector that also hears the hub's events: a plugin that contributes the events extension point
// (docs/extensions/extensions.md "Events") next to its connector. Hear gets the event's name without its namespace
// ("agent.needs_you") and its parameters, on a goroutine of its own; nothing is answered.
type Listener interface {
	Hear(ctx context.Context, event string, params json.RawMessage)
}

// BuildWith is Build with all a program is started with.
type BuildWith func(init Init, secrets connector.Secrets) (connector.Connector, error)

// Restarts of a failing Watch inside the program (the hub's Registry restarts the hub side).
var (
	watchFirst = 5 * time.Second
	watchLast  = 5 * time.Minute
)

// Main serves build's connector on in / out until the hub closes them (the program then exits) or ctx ends. What the
// connector logs goes to the standard logger (the program's stderr, which the hub keeps).
func Main(ctx context.Context, in io.Reader, out io.Writer, build Build) error {
	return MainWith(ctx, in, out, func(i Init, s connector.Secrets) (connector.Connector, error) { return build(i.Config, s) })
}

// MainWith is Main for a program that needs its declared secrets or data directory (a plugin's connector).
func MainWith(ctx context.Context, in io.Reader, out io.Writer, build BuildWith) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := rpc.New("c", out)
	s := &server{peer: peer, build: build, ctx: ctx}
	peer.OnRequest = s.handle
	peer.OnEvent = s.event
	return peer.Serve(ctx, in)
}

// event passes the hub's event.* notifications to a connector that listens (Listener); the others are dropped, as
// before there were any.
func (s *server) event(name string, params json.RawMessage) {
	topic, ok := strings.CutPrefix(name, "event.")
	if !ok {
		return
	}
	s.mu.Lock()
	l, ok := s.c.(Listener)
	s.mu.Unlock()
	if ok {
		go l.Hear(s.ctx, topic, params)
	}
}

// Serve serves c, built and running already (its Run and Watch are the caller's), on in / out: what a test holds a
// connector to the protocol with (connector/conformance). Its secrets are its own.
func Serve(ctx context.Context, in io.Reader, out io.Writer, c connector.Connector) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := rpc.New("c", out)
	s := &server{peer: peer, ctx: ctx, c: c, running: true}
	peer.OnRequest = s.handle
	return peer.Serve(ctx, in)
}

type server struct {
	peer  *rpc.Peer
	build BuildWith
	ctx   context.Context
	// running: c was handed over built and running (Serve); init only answers its manifest.
	running bool

	mu sync.Mutex
	c  connector.Connector
	// inPlugin: opened with initialize (the plugin protocol), not init. Atomic, like cid: the secrets read them
	// while init holds mu (a connector may load its secret while it is built).
	inPlugin atomic.Bool
	// cid is the connector's id, for the secrets' calls under the plugin protocol: what initialize said is active
	// until the connector is built, then its own.
	cid atomic.Pointer[string]
	// once runs each actions.do once by its request id.
	once writes
	// recalls shares recall.do by request id and remembers its successes.
	recalls recallRuns
	// chid is the channel the connector is (the plugin protocol's active channel; its own id when none).
	chid string
	// sent are the refs of the latest channel.send done (or still running), and what each answered: the hub sends
	// one again when it did not hear back.
	sent sentRefs
}

// initialize opens the plugin protocol: the connector is built as with init, and the answer says this program is the
// plugin the hub started (a first-party program is the hub's build: it takes the id and version it is given) and
// contributes the connector.
func (s *server) initialize(params json.RawMessage) (any, error) {
	var p extension.InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.Protocol != extension.Protocol {
		return nil, &rpc.Error{Code: extension.CodeWrongProtocol, Message: fmt.Sprintf("plugin protocol %d, not %d", p.Protocol, extension.Protocol)}
	}
	s.mu.Lock()
	if s.c == nil {
		s.inPlugin.Store(true)
		if ids := p.Active["connectors"]; len(ids) == 1 {
			s.cid.Store(&ids[0])
		}
		if ids := p.Active["channel"]; len(ids) == 1 {
			s.chid = ids[0]
		}
	}
	s.mu.Unlock()
	m, err := s.init(Init{Config: p.Config, Declared: p.Secrets, DataDir: p.Extension.DataDir, Channel: channelHub{s}, Locale: p.Hub.Locale})
	if err != nil {
		return nil, err
	}
	item, _ := json.Marshal([]extension.ConnectorItem{ItemOf(m)})
	return extension.InitializeResult{Protocol: extension.Protocol, ID: p.Extension.ID, Version: p.Extension.Version,
		Contributes: map[string]json.RawMessage{"connectors": item}}, nil
}

// init builds the connector the first time and starts its Run and Watch; a second init answers the same.
func (s *server) init(init Init) (Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		if s.running {
			return ManifestOf(s.c), connector.Check(s.c)
		}
		return ManifestOf(s.c), nil
	}
	c, err := s.build(init, &secrets{s: s})
	if err != nil {
		return Manifest{}, err
	}
	if err := connector.Check(c); err != nil {
		return Manifest{}, err
	}
	s.c = c
	id := c.ID()
	s.cid.Store(&id)
	if r, ok := c.(connector.Runner); ok {
		go r.Run(s.ctx)
	}
	if e, ok := c.(connector.Events); ok {
		go s.watch(e)
	}
	return ManifestOf(c), nil
}

// watch passes the connector's changes on to the hub, starting a Watch that fails again after a backoff.
func (s *server) watch(e connector.Events) {
	wait := watchFirst
	for s.ctx.Err() == nil {
		start := time.Now()
		err := e.Watch(s.ctx, func(scope connector.Scope) { s.notifyChanged(scope) })
		if s.ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			wait = watchFirst
		}
		log.Printf("watch stopped (%v), again in %s", err, wait)
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, watchLast)
	}
}

// notifyChanged tells the hub something changed, in the opening's words.
func (s *server) notifyChanged(scope connector.Scope) {
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if s.inPlugin.Load() && c != nil {
		_ = s.peer.Notify(InPlugin(EChanged), ChangedEvent{Contribution: c.ID(), Scope: string(scope)})
		return
	}
	_ = s.peer.Notify(EChanged, ChangedEvent{Scope: string(scope)})
}

// secrets is the hub's store for this program's connector, over the pipe.
type secrets struct{ s *server }

func (x *secrets) call(ctx context.Context, method string, params, out any) error {
	if !x.s.inPlugin.Load() {
		return x.s.peer.Call(ctx, method, params, out)
	}
	id := ""
	if p := x.s.cid.Load(); p != nil {
		id = *p
	}
	p, err := extension.Targeted(params, id)
	if err != nil {
		return err
	}
	return x.s.peer.Call(ctx, InPlugin(method), p, out)
}

func (x *secrets) Load(ctx context.Context) ([]byte, error) {
	var out SecretData
	err := x.call(ctx, MSecretsLoad, struct{}{}, &out)
	return out.Data, err
}

func (x *secrets) Save(ctx context.Context, data []byte) error {
	return x.call(ctx, MSecretsSave, SecretData{Data: data}, nil)
}

func (x *secrets) Delete(ctx context.Context) error {
	return x.call(ctx, MSecretsDelete, struct{}{}, nil)
}
