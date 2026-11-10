package transcript

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A Meta that has read many large task notifications keeps no copy of them: a node keeps one Meta per transcript for
// its whole uptime (node/bgscan.go), and the notifications it had shown were once kept whole to know them again. It
// still knows them: the same notification again, as a user line or as a queued_command attachment, is not shown twice.
func TestTaskNotificationsAreNotKept(t *testing.T) {
	const count, size = 64, 64 << 10
	notification := func(i int) string {
		return fmt.Sprintf("<task-notification>\n<task-id>task-%d</task-id>\n<tool-use-id>toolu_%d</tool-use-id>\n"+
			"<output-file>/tmp/example/tasks/task-%d.output</output-file>\n<status>completed</status>\n"+
			"<summary>Background command \"build %d\" completed (exit code 0)</summary>\n<result>%s</result>\n</task-notification>",
			i, i, i, i, strings.Repeat("x", size))
	}
	userLine := func(i int) []byte {
		b, _ := json.Marshal(map[string]any{"type": "user", "uuid": fmt.Sprintf("u%d", i), "timestamp": "2026-01-01T00:00:00Z",
			"message": map[string]any{"role": "user", "content": notification(i)}})
		return b
	}
	var meta Meta
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range count {
		if items := ParseLine(i, userLine(i), &meta); len(items) != 1 || items[0].Task == nil || items[0].Task.TaskID != fmt.Sprintf("task-%d", i) {
			t.Fatalf("notification %d: %+v", i, items)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if kept := int64(after.HeapAlloc) - int64(before.HeapAlloc); kept > count*size/8 {
		t.Errorf("%d notifications of %d KiB: %d KiB kept after reading them", count, size>>10, kept>>10)
	}

	again, _ := json.Marshal(map[string]any{"type": "attachment", "uuid": "q0", "timestamp": "2026-01-01T00:00:00Z",
		"attachment": map[string]any{"type": "queued_command", "commandMode": "task-notification", "prompt": notification(0)}})
	for _, line := range [][]byte{userLine(0), again} {
		if items := ParseLine(count, line, &meta); len(items) != 0 {
			t.Fatalf("a notification shown already is shown again: %+v", items)
		}
	}
	other := strings.Replace(string(userLine(0)), "exit code 0", "exit code 1", 1)
	if items := ParseLine(count+1, []byte(other), &meta); len(items) != 1 || items[0].Kind != proto.KindNotice {
		t.Fatalf("the same task reporting something else is not shown: %+v", items)
	}
	runtime.KeepAlive(&meta)
}
