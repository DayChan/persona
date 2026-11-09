package assistant

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The readers of a machine's sessions that keep a few rows (nodeSessionsWhere, boardSessions) answer as they did when
// each copied the whole list: the duplicate ids, parent chains, directories and aliases below are where a filter that
// kept the wrong rows would differ.
func TestSessionReadersAnswerAsBefore(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.d.add("idle", "Idle")
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "s1", Status: proto.StatusDone},
		{SessionID: "r1"}, {SessionID: "c1", ParentSessionID: "r1", Status: proto.StatusWorking},
		{SessionID: "r2"}, {SessionID: "m2", ParentSessionID: "r2", Status: proto.StatusDone},
		{SessionID: "g2", ParentSessionID: "m2", Status: proto.StatusBlocked},
		{SessionID: "r3"}, {SessionID: "c3", ParentSessionID: "r3", Status: proto.StatusDone},
		{SessionID: "d4", ParentSessionID: "r4"}, {SessionID: "x4", ParentSessionID: "d4", Status: proto.StatusWorking},
		{SessionID: "d4"}, // the last row of an id is the one read
		{SessionID: "y5", RootSessionID: "r5", Status: proto.StatusWorking},
		{SessionID: "a6", ParentSessionID: "b6", Status: proto.StatusWorking},
		{SessionID: "b6", ParentSessionID: "a6", Status: proto.StatusWorking},
	})
	children := func(root proto.Session) bool { return ft.s.completionChildrenRunning("mac", root) }
	for _, c := range []struct {
		name      string
		got, want bool
	}{
		{"a listed agent is not gone", ft.s.agentGone(proto.Interaction{MachineID: "mac", SessionID: "s1"}), false},
		{"an unlisted agent is gone", ft.s.agentGone(proto.Interaction{MachineID: "mac", SessionID: "none"}), true},
		{"a machine listing nothing has none gone", ft.s.agentGone(proto.Interaction{MachineID: "idle", SessionID: "none"}), false},
		{"a working child", children(proto.Session{SessionID: "r1"}), true},
		{"a blocked grandchild under a done child", children(proto.Session{SessionID: "r2"}), true},
		{"a done child", children(proto.Session{SessionID: "r3"}), false},
		{"a parent named only by an id's earlier row", children(proto.Session{SessionID: "r4"}), false},
		{"a child naming its root", children(proto.Session{SessionID: "r5"}), true},
		{"a parent loop", children(proto.Session{SessionID: "r6"}), false},
		{"a child is no root", children(proto.Session{SessionID: "c1", ParentSessionID: "r1"}), false},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.want)
		}
	}

	d := newBoardDevs()
	s, _ := newBoardService(t, newMemoryBoardStore(), d)
	d.add("laptop", directoryNode)
	d.setSessions("laptop", []proto.Session{
		{SessionID: "a", Cwd: "/work/app", SpaceName: "App"},
		{SessionID: "b", Cwd: "/work/app", SpaceName: "Later"},
		{SessionID: "c", Cwd: "/work/app/", SpaceName: "Slash"},
		{SessionID: "d", Cwd: "/elsewhere", WorkspaceCwd: "/work/lib"},
		{SessionID: "e"},
		{SessionID: "f", Cwd: `C:\Code\X`, SpaceName: "Win"}, {SessionID: "g", Cwd: "c:/Code/X/.", SpaceName: "Win again"},
		{SessionID: "h", Cwd: `\\Server\Share\a`, SpaceName: "UNC"}, {SessionID: "i", Cwd: "//server/share/a", SpaceName: "UNC again"},
		{SessionID: "j", Cwd: "/work/weather", SpaceName: "The node lists it"},
	})
	var listed proto.WorkspacesResponse
	if w := boardCall(s, "GET", "/api/v1/workspaces", "", roleFull); json.Unmarshal(w.Body.Bytes(), &listed) != nil {
		t.Fatalf("workspaces: %s", w.Body)
	}
	got := map[string]string{}
	for _, ws := range listed.Workspaces {
		got[ws.Cwd] = ws.Title
	}
	want := map[string]string{"/work/weather": "Weather", "/work/app": "App", "/work/lib": "/elsewhere", "c:/Code/X": "Win",
		"//server/share/a": "UNC"}
	if len(listed.Workspaces) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("workspaces %v, want %v", listed.Workspaces, want)
	}
	id := workspaceForSession("laptop", proto.Session{Cwd: "/work/app"}).ID
	st := boardState(t, boardCall(s, "POST", "/api/v1/workspace-board",
		`{"revision":0,"action":"add_card","machine_id":"laptop","workspace_id":"`+id+`","column_id":"todo"}`, roleFull))
	if len(st.Cards) != 1 || st.Cards[0].Title != "App" {
		t.Errorf("a card for a session's directory takes its first session's name: %+v", st.Cards)
	}
}

// busyBoard is a manager whose computer lists 8,200 sessions in 20 directories.
func busyBoard(t *testing.T) *Service {
	d := newBoardDevs()
	s, _ := newBoardService(t, newMemoryBoardStore(), d)
	d.add("laptop", directoryNode)
	list := make([]proto.Session, 8200)
	for i := range list {
		list[i] = proto.Session{SessionID: fmt.Sprintf("session-%05d", i), Cwd: fmt.Sprintf("/code/project-%02d", i%20),
			Agent: "claude", Status: proto.StatusDone, Title: "Example task", LastText: strings.Repeat("Example result. ", 32)}
	}
	d.setSessions("laptop", list)
	return s
}

// On a machine listing 8,200 sessions the readers copy the rows they keep, not the list: a copy is 12.1 MB, which
// agentGone and the board read once each before, completionChildrenRunning twice (25.1 MB) and decoratedUnarchived
// twice (25.8 MB; it still needs one, as its own to decorate). Each bound is the measured bytes per call + 3%.
func TestSessionReadersCopyNoList(t *testing.T) {
	s := assistantInventory(t)
	board := busyBoard(t)
	for _, c := range []struct {
		name string
		most uint64
		read func()
	}{
		{"agentGone", 2225, func() { s.agentGone(proto.Interaction{MachineID: "box", SessionID: "session-08199"}) }},
		{"completionChildrenRunning", 1690, func() { s.completionChildrenRunning("box", proto.Session{SessionID: "session-00100"}) }},
		{"decoratedUnarchived", 13710700, func() { s.decoratedUnarchived("box") }},
		{"GET /api/v1/workspaces", 177600, func() { boardCall(board, "GET", "/api/v1/workspaces", "", roleFull) }},
	} {
		c.read()
		const runs = 20
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range runs {
			c.read()
		}
		runtime.ReadMemStats(&after)
		if per := (after.TotalAlloc - before.TotalAlloc) / runs; per > c.most {
			t.Errorf("%s allocated %d bytes a call, want at most %d", c.name, per, c.most)
		}
	}
}
