package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestInvocationRunBindsInitializationAndExactTransportAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	seen := make(chan string, 4)
	s := spec(t, "rpc")
	s.Startup = true
	s.Init = func(ctx context.Context) (extension.InitializeParams, error) {
		seen <- InvocationRun(ctx)
		return extension.InitializeParams{Extension: extension.ExtensionInfo{ID: "demo", Version: "1.0.0"}}, nil
	}
	s.Handle = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		return InvocationRun(ctx), nil
	}
	p := Start(ctx, s)
	defer p.Stop()
	var first string
	select {
	case first = <-seen:
	case <-ctx.Done():
		t.Fatal("initialization did not start")
	}
	if first == "" {
		t.Fatal("initialization has no trusted run identity")
	}
	var callback string
	if err := p.Call(ctx, "demo.ask", nil, &callback); err != nil || callback != first {
		t.Fatal("callback identity differs from its initialized transport", callback, err)
	}
	var ignored any
	if err := p.Call(ctx, "demo.exit", nil, &ignored); err != nil {
		t.Fatal(err)
	}
	var next string
	select {
	case next = <-seen:
	case <-ctx.Done():
		t.Fatal("replacement process did not initialize")
	}
	if next == "" || next == first {
		t.Fatal("replacement reused the previous process attempt identity")
	}
	if err := p.Call(ctx, "demo.ask", nil, &callback); err != nil || callback != next {
		t.Fatal("replacement callback was attributed to the old run", callback, err)
	}
	if InvocationRun(context.Background()) != "" {
		t.Fatal("an unbound context acquired runtime identity")
	}
}

func TestInvocationRunCancelsOutstandingHostRequestWhenStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, cancelled := make(chan struct{}), make(chan struct{})
	s := spec(t, "rpc")
	s.Startup = true
	s.Handle = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	p := Start(ctx, s)
	defer p.Stop()
	reply := make(chan error, 1)
	go func() {
		var out string
		reply <- p.Call(ctx, "demo.ask", nil, &out)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("host callback was not entered")
	}
	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("retired process left a host storage request active")
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("owned process did not stop")
	}
	select {
	case <-reply:
	case <-ctx.Done():
		t.Fatal("original caller did not finish")
	}
}
