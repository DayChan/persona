package devices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
)

// testLinkClient is a Persona Link connected to d through a relay: it says its hello, then reads until the link ends.
type testLinkClient struct {
	closed chan websocket.CloseError // the close frame the manager sent
	msgs   chan link.Message         // what the manager said, as long as there is room
	conn   *websocket.Conn
}

// send says msg to the manager.
func (c *testLinkClient) send(t *testing.T, msg link.Message) {
	t.Helper()
	b, _ := json.Marshal(msg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// next is the next message of type typ the manager says within 5 s.
func (c *testLinkClient) next(t *testing.T, typ string) link.Message {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-c.msgs:
			if m.Type == typ {
				return m
			}
		case <-timeout:
			t.Fatalf("no %s from the manager", typ)
		}
	}
}

// dialTestLink connects a Persona Link of machine id to d and waits until d has it as the machine's link.
func dialTestLink(t *testing.T, d *Devices, id string) (*testLinkClient, *mgrLink) {
	t.Helper()
	return dialTestLinkHello(t, d, link.Message{Type: "hello", Machine: id, Version: "test"})
}

// dialTestLinkHello is dialTestLink with this hello.
func dialTestLinkHello(t *testing.T, d *Devices, hello link.Message) (*testLinkClient, *mgrLink) {
	t.Helper()
	id := hello.Machine
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		d.acceptLink(conn, devicewire.Accept{ID: "relay-1", Kind: kindLink, MachineID: id, TokenID: "token-1"})
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	b, _ := json.Marshal(hello)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	c := &testLinkClient{closed: make(chan websocket.CloseError, 1), msgs: make(chan link.Message, 64), conn: conn}
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				var ce websocket.CloseError
				errors.As(err, &ce)
				c.closed <- ce
				return
			}
			var m link.Message
			if json.Unmarshal(data, &m) == nil {
				select {
				case c.msgs <- m:
				default:
				}
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := d.cfgMachine(id); m != nil {
			if l := m.currentLink(); l != nil {
				return c, l
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the link never became the machine's")
	return nil, nil
}

// wantLinkEnded checks the manager ended l with 1001 and why, and Persona Link got that close frame.
func wantLinkEnded(t *testing.T, c *testLinkClient, l *mgrLink, why string) {
	t.Helper()
	select {
	case <-l.ctx.Done():
		if cause := context.Cause(l.ctx); cause == nil || cause.Error() != why {
			t.Fatalf("link ended with %v, want %q", cause, why)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the link outlived its machine")
	}
	select {
	case ce := <-c.closed:
		if ce.Code != websocket.StatusGoingAway || ce.Reason != why {
			t.Fatalf("Persona Link got close %d %q, want 1001 %q", ce.Code, ce.Reason, why)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Persona Link never got a close frame")
	}
}

func linkEntry(name string) json.RawMessage {
	b, _ := json.Marshal(MachineConfig{ID: "lap", Name: name, Kind: kindLink})
	return b
}

// An entry edited (a new machine for it): the link of the machine it replaced is ended, so Persona Link dials again
// and becomes the new machine's, which would otherwise wait for it forever.
func TestLinkEndsWhenItsEntryIsReplaced(t *testing.T) {
	d := newTestDevices(t)
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}})
	old := d.cfgMachine("lap")
	c, l := dialTestLink(t, d, "lap")
	work, _ := json.Marshal(MachineConfig{ID: "lap", Name: "Laptop", Kind: kindLink, Trust: "work"})
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{work}})
	if d.cfgMachine("lap") == old {
		t.Fatal("the changed entry kept its machine")
	}
	wantLinkEnded(t, c, l, "its entry changed")
	if _, l2 := dialTestLink(t, d, "lap"); l2.m != d.cfgMachine("lap") {
		t.Fatal("the link that came back is not the new machine's")
	}
}

// An entry removed: its link is ended, rather than Persona Link showing a hub that no longer has the computer.
func TestLinkEndsWhenItsEntryIsRemoved(t *testing.T) {
	d := newTestDevices(t)
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}})
	c, l := dialTestLink(t, d, "lap")
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test"})
	wantLinkEnded(t, c, l, "no longer in the list")
}
