package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBusServiceShutdown(t *testing.T) {
	for _, cause := range []string{"serve returns", "parent canceled"} {
		t.Run(cause, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			serveReturn := make(chan struct{})
			waiting := make(chan struct{})
			result := make(chan error, 1)
			serveErr := errors.New("serve ended")
			go func() {
				result <- runBusService(ctx,
					func(context.Context) error { return nil },
					func(serviceCtx context.Context) {
						close(started)
						<-serviceCtx.Done()
						close(canceled)
						<-release
					},
					func() error {
						select {
						case <-serveReturn:
							return serveErr
						case <-ctx.Done():
							return ctx.Err()
						}
					},
					func(done <-chan struct{}) {
						close(waiting)
						waitBusService(done, nil, func(string, ...any) { t.Error("unexpected timeout") })
					},
				)
			}()
			awaitBusSignal(t, started)
			if cause == "serve returns" {
				close(serveReturn)
			} else {
				cancel()
			}
			awaitBusSignal(t, canceled)
			awaitBusSignal(t, waiting)
			select {
			case err := <-result:
				t.Fatalf("returned before service exited: %v", err)
			default:
			}
			if cause == "serve returns" && ctx.Err() != nil {
				t.Fatal("service shutdown canceled the parent context")
			}
			// Allow the service to finish, then verify Serve's result is preserved.
			unblock()
			select {
			case err := <-result:
				want := serveErr
				if cause == "parent canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("service did not finish")
			}
		})
	}
}

func TestBusServiceShutdownBeforeHandshake(t *testing.T) {
	waiting := make(chan struct{})
	result := make(chan error, 1)
	serveReturn := make(chan struct{})
	started := make(chan struct{}, 1)
	serveErr := errors.New("connection rejected")
	go func() {
		result <- runBusService(context.Background(), func(ctx context.Context) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		}, func(context.Context) { started <- struct{}{} }, func() error {
			<-serveReturn
			return serveErr
		}, func(done <-chan struct{}) { <-done })
	}()
	awaitBusSignal(t, waiting)
	close(serveReturn)
	select {
	case err := <-result:
		if !errors.Is(err, serveErr) {
			t.Fatalf("got %v, want %v", err, serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first handshake wait was not canceled")
	}
	select {
	case <-started:
		t.Fatal("service started without a handshake")
	default:
	}
}

func TestBusServiceShutdownTimeout(t *testing.T) {
	timeout := make(chan time.Time)
	logged := make(chan string, 1)
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	serveReturn := make(chan struct{})
	result := make(chan error, 1)
	defer close(release)
	serveErr := errors.New("serve ended")
	go func() {
		result <- runBusService(context.Background(), func(context.Context) error { return nil }, func(ctx context.Context) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			close(finished)
		}, func() error {
			<-serveReturn
			return serveErr
		}, func(done <-chan struct{}) {
			waitBusService(done, timeout, func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) })
		})
	}()
	awaitBusSignal(t, started)
	close(serveReturn)
	awaitBusSignal(t, canceled)
	close(timeout)
	select {
	case err := <-result:
		if !errors.Is(err, serveErr) {
			t.Fatalf("got %v, want %v", err, serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return on timeout")
	}
	select {
	case <-finished:
		t.Fatal("service exited before being released")
	default:
	}
	select {
	case message := <-logged:
		if !strings.Contains(message, "timed out") || !strings.Contains(message, serviceShutdownTimeout.String()) {
			t.Fatalf("timeout log lacks duration: %q", message)
		}
	default:
		t.Fatal("shutdown timeout was not logged")
	}
}

func awaitBusSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle signal")
	}
}
