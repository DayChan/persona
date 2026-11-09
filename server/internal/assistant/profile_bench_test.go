package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// assistantInventory is a manager whose machine lists 8,200 sessions, 14 of them the assistant's conversations.
func assistantInventory(tb testing.TB) *Service {
	f := newFakeHub()
	f.hello.Assistant.MachineID = "box"
	f.hello.Assistant.Machines = []string{"box"}
	f.devices = []Device{{ID: "box", Home: "/home/u"}}
	d := newFleetDevs()
	d.add("box", "Computer")
	st := newFleetMem()
	s := New(Options{Hub: f, Devices: d, Store: func() Store { return st }})
	list := make([]proto.Session, 8200)
	for i := range list {
		list[i] = proto.Session{SessionID: fmt.Sprintf("session-%05d", i), Cwd: "/code/example", Agent: "claude",
			Status: proto.StatusDone, Title: "Example task", LastText: strings.Repeat("Example result. ", 32)}
		if i < 14 {
			list[i].Cwd = "/home/u/.persona/assistant"
		}
	}
	d.setSessions("box", list)
	if got := s.view(context.Background(), false); len(got.Conversations) != 14 {
		tb.Fatalf("conversations = %d, want 14", len(got.Conversations))
	}
	return s
}

// BenchmarkAssistantViewLargeInventory measures the complete response path:
// a small Persona list on a machine with thousands of coding conversations.
func BenchmarkAssistantViewLargeInventory(b *testing.B) {
	s := assistantInventory(b)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(s.view(ctx, false)); err != nil {
			b.Fatal(err)
		}
	}
}

// The view copies only the assistant's 14 conversations out of the machine's 8,200 sessions: 100,665 bytes a view
// measured, where it allocated 12.3 MB when it copied the whole list to pick them.
func TestAssistantViewCopiesOnlyItsConversations(t *testing.T) {
	// TotalAlloc is process-wide. Other tests can leave asynchronous work alive,
	// so measure in a fresh process without changing the view's allocation budget.
	const child = "PERSONA_TEST_ASSISTANT_VIEW_ALLOCATION_CHILD"
	if os.Getenv(child) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAssistantViewCopiesOnlyItsConversations$", "-test.count=1")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated view allocation check: %v\n%s", err, output)
		}
		return
	}
	s := assistantInventory(t)
	ctx := context.Background()
	const runs = 50
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		s.view(ctx, false)
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / runs; per > 103685 {
		t.Fatalf("one view allocated %d bytes, want at most 103685", per)
	}
}
