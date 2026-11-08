package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// answerArchiveCard answers the next card on the phone (allow or decline) and returns it; fails when none comes.
func answerArchiveCard(t *testing.T, s *Service, allow bool, shown chan<- proto.Interaction) {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if qs := s.pendingQuestions(); len(qs) == 1 {
			raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
			if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: qs[0].ID, Answer: raw}); err != nil {
				t.Error(err)
			}
			shown <- qs[0]
			return
		}
	}
	t.Error("no card on the phone")
	close(shown)
}

func archiveTool(t *testing.T, ft *fleetTest, name string, args map[string]any) (string, error) {
	t.Helper()
	return runJSON(t, ft.s, "conv1", name, args)
}

// agent_archive / agent_unarchive: the apps' reversible archive, without a card; agents_list leaves archived ones out
// unless asked for them; already archived / not archived change nothing; unknown sessions and Persona's own
// conversations are refused.
func TestAgentArchiveTool(t *testing.T) {
	ft, row := closeTestService(t, nil)
	args := map[string]any{"machine": "mac", "session_id": row.SessionID}
	res, err := archiveTool(t, ft, "agent_archive", args)
	if err != nil || !strings.Contains(res, "Archived") || !strings.Contains(res, "still open on the computer") {
		t.Fatalf("archive: %s %v", res, err)
	}
	if !archivedIDs(t, ft.s)[row.SessionID] || len(retiredOf(ft)) != 0 || len(ft.s.pendingQuestions()) != 0 {
		t.Fatal("archive: not archived, or closed, or asked")
	}
	if list := ft.s.describeAgents(context.Background(), "all", "", ""); !strings.Contains(list, row.SessionID+" ") ||
		!strings.Contains(list, "· archived") {
		t.Fatalf("all: %s", list)
	}
	if list := ft.s.describeAgents(context.Background(), "idle", "", ""); strings.Contains(list, row.SessionID) {
		t.Fatalf("idle lists an archived session: %s", list)
	}
	if list := ft.s.describeAgents(context.Background(), "archived", "", ""); !strings.Contains(list, row.SessionID) {
		t.Fatalf("archived: %s", list)
	}
	if res, err := archiveTool(t, ft, "agent_archive", args); err != nil || !strings.Contains(res, "archived already") {
		t.Fatalf("again: %s %v", res, err)
	}

	res, err = archiveTool(t, ft, "agent_unarchive", args)
	if err != nil || !strings.Contains(res, "Restored") {
		t.Fatalf("unarchive: %s %v", res, err)
	}
	if archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("still archived")
	}
	if list := ft.s.describeAgents(context.Background(), "archived", "", ""); strings.Contains(list, row.SessionID) {
		t.Fatalf("archived lists a restored session: %s", list)
	}
	if res, err := archiveTool(t, ft, "agent_unarchive", args); err != nil || !strings.Contains(res, "not archived") {
		t.Fatalf("unarchive again: %s %v", res, err)
	}

	if _, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": "no-such-session"}); err == nil ||
		!strings.Contains(err.Error(), "no agent") {
		t.Fatalf("unknown session: %v", err)
	}
	machine, _ := ft.s.fleetMachine("mac")
	conv := proto.Session{SessionID: "persona-conv", Agent: "claude", Cwd: machine.assistantCwd(), Status: proto.StatusIdle,
		UpdatedAt: time.Now().Add(-time.Hour)}
	ft.d.setSessions("mac", []proto.Session{row, conv})
	if _, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": conv.SessionID}); err == nil ||
		!strings.Contains(err.Error(), "conversation_archive") {
		t.Fatalf("assistant conversation: %v", err)
	}
	if archivedIDs(t, ft.s)[conv.SessionID] {
		t.Fatal("archived an assistant conversation")
	}
}

// A busy agent is not archived (nothing changes); one that finished and waits for the user's reply is.
func TestAgentArchiveToolRefusesBusy(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) { s.Status, s.State = proto.StatusWorking, proto.StateWorking })
	_, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": row.SessionID})
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "Nothing changed") {
		t.Fatalf("busy: %v", err)
	}
	if len(archivedIDs(t, ft.s)) != 0 {
		t.Fatal("a busy agent was archived")
	}
	ft, row = closeTestService(t, func(s *proto.Session) { s.AwaitsReply, s.Asks = true, "Merge it?" })
	if res, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": row.SessionID}); err != nil ||
		!strings.Contains(res, "Archived") {
		t.Fatalf("awaiting reply: %s %v", res, err)
	}
}

// agent_archive with close asks on the phone every time: declined, nothing changes; allowed, it is archived and closed.
func TestAgentArchiveCloseToolConfirms(t *testing.T) {
	ft, row := closeTestService(t, nil)
	args := map[string]any{"machine": "mac", "session_id": row.SessionID, "close": true}
	shown := make(chan proto.Interaction, 1)
	go answerArchiveCard(t, ft.s, false, shown)
	res, err := archiveTool(t, ft, "agent_archive", args)
	if err != nil || !strings.Contains(res, "declined") {
		t.Fatalf("declined: %s %v", res, err)
	}
	if card := <-shown; card.Kind != proto.IxPermission || card.ToolName != "agent_archive" || card.SessionID != "conv1" ||
		!strings.Contains(card.Summary, "mac") {
		t.Fatalf("card: %+v", card)
	}
	if len(retiredOf(ft)) != 0 || len(archivedIDs(t, ft.s)) != 0 {
		t.Fatal("a declined archive and close changed something")
	}

	go answerArchiveCard(t, ft.s, true, shown)
	res, err = archiveTool(t, ft, "agent_archive", args)
	if err != nil || !strings.Contains(res, "close: closed") {
		t.Fatalf("allowed: %s %v", res, err)
	}
	<-shown
	if got := retiredOf(ft); len(got) != 1 || got[0] != row.SessionID || !archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatalf("retired %v, archived %v", got, archivedIDs(t, ft.s))
	}
}

// A busy agent is refused before the card; one that runs a /loop can still be archived without close.
func TestAgentArchiveCloseToolRefusesBusy(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) { s.Loop = &proto.LoopInfo{} })
	args := map[string]any{"machine": "mac", "session_id": row.SessionID, "close": true}
	_, err := archiveTool(t, ft, "agent_archive", args)
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "without close") {
		t.Fatalf("busy: %v", err)
	}
	if len(ft.s.pendingQuestions()) != 0 || len(retiredOf(ft)) != 0 || len(archivedIDs(t, ft.s)) != 0 {
		t.Fatal("a refused archive and close asked or changed something")
	}
	delete(args, "close")
	if res, err := archiveTool(t, ft, "agent_archive", args); err != nil || !strings.Contains(res, "Archived") {
		t.Fatalf("archive only: %s %v", res, err)
	}
}

// When the close does not happen the session stays archived, and the answer says it is still open and why.
func TestAgentArchiveCloseToolReportsPartialFailure(t *testing.T) {
	ft, row := closeTestService(t, nil)
	ft.f.mu.Lock()
	ft.f.retireErr = &rpc.Error{Code: "not_retirable", Message: "this session belongs to another tool"}
	ft.f.mu.Unlock()
	shown := make(chan proto.Interaction, 1)
	go answerArchiveCard(t, ft.s, true, shown)
	res, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": row.SessionID, "close": true})
	<-shown
	if err != nil || !strings.Contains(res, "still open on the computer (close: refused)") ||
		!strings.Contains(res, "this session belongs to another tool") || !strings.Contains(res, "agent_unarchive") {
		t.Fatalf("refused close: %s %v", res, err)
	}
	if !archivedIDs(t, ft.s)[row.SessionID] || len(retiredOf(ft)) != 0 {
		t.Fatal("a refused close undid the archive, or closed it")
	}
}
