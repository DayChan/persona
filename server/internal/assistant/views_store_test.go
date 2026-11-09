package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestViewsPersistAcrossAssistantRestart(t *testing.T) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	f := newFakeHub().withMachine()
	f.hello.PersonaView = true
	f.setSessions("server", proto.Session{SessionID: "view-test-" + randomHex(8), Assistant: true})
	f.mu.Lock()
	conversation := f.sessions["server"][0].SessionID
	f.mu.Unlock()
	s := newServiceOn(t, f, database)
	out, err := s.toolView(ctx, "server", conversation, "view_show", json.RawMessage(`{"view":{"fallback":"Before","sections":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var made struct{ ID string }
	if err := json.Unmarshal([]byte(out), &made); err != nil {
		t.Fatal(err)
	}
	update := json.RawMessage(`{"id":"` + made.ID + `","view":{"fallback":"After","sections":[]}}`)
	if _, err := s.toolView(ctx, "server", conversation, "view_update", update); err != nil {
		t.Fatal(err)
	}
	resumed := newServiceOn(t, f, database)
	got, err := resumed.toolView(ctx, "server", conversation, "view_get", json.RawMessage(`{"id":"`+made.ID+`"}`))
	if err != nil || !strings.Contains(got, `"version":2`) || !strings.Contains(got, `"fallback":"After"`) {
		t.Fatalf("%s %v", got, err)
	}
	views, err := resumed.conversationViews(ctx, conversation)
	var card wire.AssistantView
	if err == nil {
		err = json.Unmarshal(views[made.ID], &card)
	}
	if err != nil || len(views) != 1 || card.Version != 2 {
		t.Fatalf("%s %v", views[made.ID], err)
	}
	items, _, err := resumed.feed(ctx, conversation, time.Time{})
	if err != nil || len(items) != 1 || items[0].Kind != feedView || items[0].Ref != made.ID {
		t.Fatalf("%+v %v", items, err)
	}
}

// Two processes on one database, as the blue-green slots are during a switch: a card one updates is new in the other's
// next feed snapshot, although the other already cached the old version.
func TestViewsSnapshotFollowsAnotherProcess(t *testing.T) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.hello.PersonaView = true
	conversation := "view-test-" + randomHex(8)
	f.setSessions("server", proto.Session{SessionID: conversation, Assistant: true})
	slot := func() *Service {
		database, err := db.Open(ctx, url, db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(database.Close)
		return newServiceOn(t, f, database)
	}
	blue, green := slot(), slot()
	out, err := blue.toolView(ctx, "server", conversation, "view_show", json.RawMessage(`{"view":{"fallback":"Before","sections":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var made struct{ ID string }
	if err := json.Unmarshal([]byte(out), &made); err != nil {
		t.Fatal(err)
	}
	snapshot := func() wire.AssistantView {
		t.Helper()
		views, err := blue.conversationViews(ctx, conversation)
		var card wire.AssistantView
		if err == nil {
			err = json.Unmarshal(views[made.ID], &card)
		}
		if err != nil || len(views) != 1 {
			t.Fatalf("%+v %v", views, err)
		}
		return card
	}
	before := snapshot()
	if before.Version != 1 || before.View.Fallback != "Before" || len(blue.viewMemo.index) != 1 {
		t.Fatalf("%+v, memo %d", before, len(blue.viewMemo.index))
	}
	if _, err := green.toolView(ctx, "server", conversation, "view_update", json.RawMessage(`{"id":"`+made.ID+`","view":{"fallback":"After","sections":[]}}`)); err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	if after.Version != 2 || after.View.Fallback != "After" || !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("before %+v, after %+v", before, after)
	}
}
