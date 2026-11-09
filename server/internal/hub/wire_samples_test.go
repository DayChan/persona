package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/transcript"
)

var updateWireSamples = flag.Bool("update", false, "rewrite wire JSON golden fixtures")

type wireExpectation struct {
	Accepted bool           `json:"accepted"`
	Fields   map[string]any `json:"fields"`
}

type wireSample struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      []string        `json:"source"`
	FromTest    string          `json:"from_test,omitempty"`
	Derived     bool            `json:"derived,omitempty"`
	Decoder     string          `json:"decoder"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	Input       json.RawMessage `json:"input"`
	Expected    wireExpectation `json:"expected"`
}

// TestWireSamples captures production encoders and handlers, then compares the whole fixture byte for byte.
// Expected fields describe the current iOS decoder; the README contains the read-only Swift cross-check.
func TestWireSamples(t *testing.T) {
	files := wireSamples(t)
	dir := filepath.Join("..", "..", "..", "spec", "fixtures", "wire")
	names := map[string]bool{}
	validName := regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	for file, cases := range files {
		for _, c := range cases {
			if names[c.Name] || !validName.MatchString(c.Name) || len(c.Source) == 0 || c.Description == "" ||
				c.Derived == (c.FromTest != "") || !json.Valid(c.Input) {
				t.Fatalf("invalid fixture metadata: %s", c.Name)
			}
			names[c.Name] = true
		}
		t.Run(file, func(t *testing.T) {
			data, err := json.MarshalIndent(cases, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, '\n')
			path := filepath.Join(dir, file+".json")
			if *updateWireSamples {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(golden, data) {
				t.Fatalf("wire golden differs: %s (%v); from server run: go test -count=1 -p 1 ./internal/hub -run '^TestWireSamples$' -update", path, err)
			}
			// Read the committed fixture as consumers do; do not only test the in-memory builders.
			var saved []wireSample
			if err := json.Unmarshal(golden, &saved); err != nil {
				t.Fatal(err)
			}
			for _, c := range saved {
				t.Run(c.Name, func(t *testing.T) { wireDecodeGo(t, c) })
			}
		})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			if _, ok := files[entry.Name()[:len(entry.Name())-5]]; !ok {
				t.Errorf("unregistered wire fixture: %s", entry.Name())
			}
		}
	}
}

func wireDecodeGo(t *testing.T, c wireSample) {
	t.Helper()
	// Compatibility mutations intentionally exercise Swift-only fallback semantics.
	if c.Decoder == "compat_event" || c.Decoder == "compat_session" {
		return
	}
	var target any
	switch c.Decoder {
	case "sessions":
		target = &struct {
			Sessions []proto.Session `json:"sessions"`
		}{}
	case "machines":
		target = &struct {
			Machines []proto.Machine `json:"machines"`
		}{}
	case "items", "items_after", "items_range":
		target = &struct {
			Items []proto.Item `json:"items"`
			itemSpan
		}{}
	case "interactions":
		target = &struct {
			Interactions []proto.Interaction `json:"interactions"`
		}{}
	case "fleet":
		target = &struct {
			Fleet FleetState `json:"fleet"`
		}{}
	case "inbox":
		target = &struct {
			Items []assistant.InboxItem `json:"items"`
		}{}
	case "event":
		target = &struct {
			Type        string             `json:"type"`
			Items       []proto.Item       `json:"items"`
			Sessions    []proto.Session    `json:"sessions"`
			Machines    []proto.Machine    `json:"machines"`
			Interaction *proto.Interaction `json:"interaction"`
			Fleet       *FleetState        `json:"fleet"`
			Revs        map[string]int64   `json:"revs"`
			itemSpan
		}{}
	default:
		target = &map[string]any{}
	}
	if err := json.Unmarshal(c.Input, target); err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
}

func wireSamples(t *testing.T) map[string][]wireSample {
	t.Helper()
	files := map[string][]wireSample{}
	add := func(file, name, description, decoder string, input json.RawMessage, fields map[string]any, sources ...string) {
		files[file] = append(files[file], wireSample{Name: name, Description: description, Source: sources,
			Derived: true, Decoder: decoder, Input: input, Expected: wireExpectation{Accepted: true, Fields: fields}})
	}
	httpJSON := func(v any) json.RawMessage {
		w := httptest.NewRecorder()
		writeJSON(w, v)
		return wireBody(t, w, http.StatusOK)
	}
	ws := func(v any) json.RawMessage {
		c := &client{out: make(chan []byte, 1)}
		c.sendJSON(v)
		return wireReceive(t, c)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	h := &Hub{version: "fixture-version", cfg: &Config{Name: "家里"}} // a fixed name, not this machine's
	h.public.id = "00000000-0000-4000-8000-000000000001"            // fixed, like the times
	hello := h.hello()
	// Replace only the random client ID, leaving all protocol fields from hello intact.
	hello["client_id"] = "000000000001"
	add("connection", "hello_features", "握手携带当前分支的能力列表。", "event", ws(hello),
		map[string]any{"/type": "hello", "/features": h.features()}, "server/internal/hub/hello.go: Hub.hello", "apple/Shared/LiveConnection.swift: ServerEvent.decode", "docs/protocol/protocol.md: 鉴权")
	w := httptest.NewRecorder()
	healthReq := httptest.NewRequest("GET", "/api/v1/health", nil)
	healthReq.RemoteAddr = "127.0.0.1:1234" // from the LAN, where version and features are shown
	h.health(w, healthReq)
	add("connection", "health_features", "健康检查与握手使用同一份能力列表。", "health", wireBody(t, w, 200),
		map[string]any{"/ok": true, "/version": "fixture-version", "/features": h.features()}, "server/internal/hub/hello.go: Hub.health", "docs/protocol/protocol.md: 鉴权")
	add("connection", "revs_changed_sessions", "版本事件只携带发生变化的会话及其版本。", "event",
		ws(map[string]any{"type": "revs", "machine_id": "m1", "revs": map[string]int64{"s1": 120, "s2": 7}}),
		map[string]any{"/type": "revs", "/machine_id": "m1", "/revs": map[string]int64{"s1": 120, "s2": 7}},
		"server/internal/hub/machine.go: Machine.onEvent", "apple/PersonaTests/SyncEventTests.swift: decodesRevs")
	files["connection"][2].Derived = false
	files["connection"][2].FromTest = "SyncEventTests.decodesRevs"

	firstPane := 0
	sessions := []proto.Session{{MachineID: "m1", SessionID: "s1", Title: "示例任务", Cwd: "/home/user/project", Status: proto.StatusBlocked,
		PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Agent: "claude", Launcher: "example", Model: "example-model",
		PermissionMode: "plan", Effort: "high", StartedByPersona: true, SpaceKind: "herdr", SpaceID: "w1", SpaceName: "示例空间", SpaceIndex: 1,
		SlotID: "t1", SlotName: "示例任务", SlotIndex: 1, PaneIndex: &firstPane, UpdatedAt: now, Rev: 120, PendingInteractions: 1,
		Queued: []string{"请补充示例。"}, Suggestion: "继续检查示例", Background: []proto.BackgroundTask{{ID: "task_example", Kind: "shell", Description: "检查示例", Command: "pwd", StartedAt: now}},
		Subagents: []proto.Subagent{{ID: "agent_example", SessionID: "s1@agent_example", Type: "Explore", Running: true, StartedAt: now}},
		Loop:      &proto.LoopInfo{Kind: "wakeup", Prompt: "检查示例状态", NextAt: now.Add(time.Hour), Ticks: 1}},
		{MachineID: "m1", SessionID: "s2", Title: "离线示例", Cwd: "/home/user/project", Status: proto.StatusOffline, UpdatedAt: now}}
	// The states as the manager works them out (assistant/fleet.go).
	sessions[0].State, sessions[1].State = proto.StateNeedsYou, proto.StateOffline
	sessions[0].StateSince, sessions[1].StateSince = now, now
	add("sessions", "sessions_list", "会话列表保留状态、模式和后台任务信息。", "sessions", httpJSON(map[string]any{"sessions": sessions}),
		map[string]any{"/sessions/0/state": "needs_you", "/sessions/0/rev": 120, "/sessions/0/permission_mode": "plan", "/sessions/0/background/0/kind": "shell", "/sessions/1/state": "offline"},
		"server/internal/proto/session.go: Session", "server/internal/hub/http.go: Hub.listSessions", "apple/Shared/Models.swift: Session.init(from:)")
	add("sessions", "sessions_event", "会话事件以信封中的机器标识为准。", "event", ws(map[string]any{"type": "sessions", "machine_id": "m1", "sessions": sessions}),
		map[string]any{"/type": "sessions", "/machine_id": "m1", "/sessions/0/machine_id": "m1", "/sessions/1/status": "offline"},
		"server/internal/hub/http.go: Hub.serveWS", "apple/Shared/LiveConnection.swift: ServerEvent.decode")

	m := newMachine(h, MachineConfig{ID: "m1", Name: "示例电脑", Kind: "ssh", SSHHost: "example.com"})
	m.online = true
	m.lastSeen = now
	m.info = proto.NodeInfo{Hostname: "example.com", OS: "linux", Arch: "arm64", Home: "/home/user", Version: "fixture-version"}
	offline := newMachine(h, MachineConfig{ID: "m2", Name: "离线电脑", Kind: "ssh", SSHHost: "example.com"})
	machines := []proto.Machine{m.snapshot(), offline.snapshot()}
	// What the machine is comes with the manager's report (its monitoring, internal/devices metrics.go).
	machines[0].System = &proto.System{OS: "linux", Distro: "linux", Name: "示例系统", Version: "1", Kernel: "example", Arch: "arm64", Cores: 2, Memory: 1073741824}
	add("machines", "machines_list", "机器列表同时包含在线信息和离线机器的零时间。", "machines", httpJSON(map[string]any{"machines": machines}),
		map[string]any{"/machines/0/online": true, "/machines/0/system/arch": "arm64", "/machines/1/online": false},
		"server/internal/hub/machine.go: Machine.snapshot", "apple/Shared/Models.swift: Machine", "apple/Shared/MachineMetrics.swift: MachineSystem")
	add("machines", "machines_event", "机器事件使用与列表一致的机器类型。", "event", ws(map[string]any{"type": "machines", "machines": machines}),
		map[string]any{"/type": "machines", "/machines/0/id": "m1", "/machines/1/id": "m2"}, "server/internal/hub/http.go: Hub.serveWS", "apple/Shared/LiveConnection.swift: ServerEvent.decode")

	item := func(id string, seq int, role, kind, text string) proto.Item {
		return proto.Item{ID: id, Seq: seq, TS: now, Role: role, Kind: kind, Text: text}
	}
	text := item("entry_text#0", 1, "user", proto.KindText, "请检查示例。")
	thought := item("entry_thinking#0", 2, "assistant", proto.KindThinking, "先读取示例文件。")
	use := item("entry_tool#0", 3, "assistant", proto.KindToolUse, "")
	use.ToolUseID = "call_example"
	use.ToolName = "Read"
	use.ToolInput = json.RawMessage(`{"file_path":"/home/user/project/example.txt","custom_Key":{"keep_snake_case":true}}`)
	result := item("entry_result#0", 4, "user", proto.KindToolResult, "示例内容")
	result.ToolUseID = use.ToolUseID
	failed := item("entry_failed#0", 5, "user", proto.KindToolResult, "示例文件不存在")
	failed.ToolUseID = "call_missing"
	failed.IsError = true
	notice := item("entry_notice#0", 6, "system", proto.KindNotice, "示例上下文已整理。")
	recap := item("entry_recap#0", 7, "assistant", proto.KindRecap, "已完成示例检查。")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 1))); err != nil {
		t.Fatal(err)
	}
	img := item("entry_image#0", 8, "user", proto.KindImage, "")
	img.Image = &proto.Image{Mime: "image/png", Data: base64.StdEncoding.EncodeToString(pngData.Bytes())}
	task := item("entry_task#0", 9, "system", proto.KindNotice, "示例后台任务完成。")
	zero := 0
	task.Wake = true
	task.Task = &proto.TaskNote{Kind: "shell", Status: "completed", Title: "检查示例", TaskID: "task_example", ToolUseID: "call_example", ExitCode: &zero, OutputFile: "/home/user/project/output.txt", Detail: "检查完成。"}
	queued := item("entry_queued#0", 10, "user", proto.KindText, "再检查一次。")
	queued.Queued = true
	all := []proto.Item{text, thought, use, result, failed, notice, recap, img, task, queued}
	all = proto.WithoutImageData(all)
	page := map[string]any{"items": all, "has_more": false, "history": "history_example", "to_seq": 10}
	add("items", "items_all_kinds", "历史页覆盖文本、工具、图片占位、通知和任务通知。", "items", httpJSON(page),
		map[string]any{"/items/0/kind": "text", "/items/1/kind": "thinking", "/items/2/tool_input/custom_Key/keep_snake_case": true, "/items/3/tool_use_id": "call_example", "/items/4/is_error": true, "/items/5/kind": "notice", "/items/6/kind": "recap", "/items/7/image/data": "", "/items/7/image/lazy": true, "/items/7/image/width": 2, "/items/8/task/exit_code": 0, "/items/8/wake": true, "/items/9/queued": true},
		"server/internal/proto/item.go: Item, WithoutImageData", "server/internal/hub/http.go: Hub.sessionItems", "apple/Shared/Models.swift: Item, ImagePayload, TaskNote")
	folded := transcript.FoldWork([]proto.Item{text, thought, use, result, recap})
	add("items", "items_folded_work", "折叠条目由真实折叠函数生成计数及范围。", "items", httpJSON(map[string]any{"items": folded, "has_more": true}),
		map[string]any{"/items/1/kind": "fold", "/items/1/fold/items": 3, "/items/1/fold/tools": 1, "/items/1/fold/thoughts": 1, "/items/1/fold/through_seq": 4, "/has_more": true},
		"server/internal/transcript/fold.go: FoldWork", "apple/Shared/Models.swift: Fold", "docs/protocol/protocol.md: 折叠的工作")
	todoUse := use
	todoUse.ToolName = "TodoWrite"
	todoUse.ToolInput = json.RawMessage(`{"todos":[{"content":"检查表单","status":"in_progress","activeForm":"检查中"}]}`)
	todoResult := result
	todoResult.Text = "OK"
	todoFold := transcript.FoldWork([]proto.Item{text, thought, todoUse, todoResult, recap})
	add("items", "items_folded_tasks", "折叠的任务快照保留成功结果与原调用位置，旧 iOS 忽略新增字段。", "items", httpJSON(map[string]any{"items": todoFold, "has_more": true}),
		map[string]any{"/items/1/kind": "fold", "/items/1/fold/items": 3, "/items/1/fold/tools": 1, "/items/1/fold/through_seq": 4},
		"server/internal/transcript/fold.go: FoldWork", "server/internal/proto/item.go: FoldTaskSnapshot", "apple/Shared/Models.swift: Fold", "docs/protocol/protocol.md: 折叠的工作")
	add("items", "items_inline_image", "未启用懒加载时图片保留内联数据。", "items", httpJSON(map[string]any{"items": []proto.Item{img}, "has_more": false}),
		map[string]any{"/items/0/image/mime": "image/png", "/items/0/image/data": img.Image.Data, "/items/0/image/lazy": nil},
		"server/internal/proto/item.go: Image", "apple/Shared/Models.swift: ImagePayload")
	monitor := task
	monitor.ID, monitor.Seq = "entry_monitor#0", 11
	monitor.Task = &proto.TaskNote{Kind: "monitor", Status: "event", Title: "示例监控", TaskID: "monitor_example", Detail: "示例值 > 0 & < 10"}
	agent := task
	agent.ID, agent.Seq = "entry_agent#0", 12
	agent.Task = &proto.TaskNote{Kind: "agent", Status: "completed", Title: "示例子任务", TaskID: "agent_example", Detail: "子任务已完成。"}
	add("items", "items_task_notifications", "监控事件和子任务结果都作为带 task 的 notice 传输。", "items", httpJSON(map[string]any{"items": []proto.Item{monitor, agent}, "has_more": false}),
		map[string]any{"/items/0/kind": "notice", "/items/0/task/kind": "monitor", "/items/0/task/detail": "示例值 > 0 & < 10", "/items/1/task/kind": "agent"},
		"server/internal/proto/item.go: TaskNote", "apple/Shared/Models.swift: TaskNote")

	wireItemEvents(t, add, now, []proto.Item{text, img})
	durations := transcript.ParseLine(9, []byte(`{"type":"system","subtype":"turn_duration","uuid":"duration_example","parentUuid":"entry_text","durationMs":12345,"timestamp":"2026-01-02T03:04:05Z"}`), &transcript.Meta{})
	add("items", "items_recorded_turn_duration", "轮次耗时保留来源及原生关联；旧客户端忽略未知内容类型和可选字段。", "items",
		httpJSON(map[string]any{"items": durations, "has_more": false}),
		map[string]any{"/items/0/id": "duration_example#0", "/items/0/role": "system"},
		"server/internal/transcript/transcript.go: ParseLine", "server/internal/proto/item.go: TurnDuration", "apple/Shared/Models.swift: ItemKind.init")
	durationClient := &client{out: make(chan []byte, 1), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	durationHub := &Hub{clients: map[*client]struct{}{durationClient: {}}}
	durationHub.itemsToSubscribers("m1", "s1", durations, itemSpan{History: "history_example"}, false)
	add("item_events", "items_live_recorded_turn_duration", "轮次耗时沿用订阅 items 事件，不新增事件或顶层字段。", "event", wireReceive(t, durationClient),
		map[string]any{"/type": "items", "/items/0/id": "duration_example#0"},
		"server/internal/hub/http.go: Hub.itemsToSubscribers", "apple/Shared/LiveConnection.swift: ServerEvent.decode")

	ixs := []proto.Interaction{}
	for _, kind := range []string{proto.IxQuestion, proto.IxPlan, proto.IxPermission, proto.IxScreen} {
		ix := proto.Interaction{ID: "ix_" + kind, MachineID: "m1", SessionID: "s1", SessionTitle: "示例任务", Cwd: "/home/user/project", Kind: kind, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		switch kind {
		case proto.IxQuestion:
			ix.ToolName = "AskUserQuestion"
			ix.Resumable = true
			ix.Questions = []proto.Question{{Question: "选择示例颜色", Header: "颜色", Options: []proto.QuestionOption{{Label: "蓝色", Description: "示例选项"}, {Label: "绿色"}}}, {Question: "选择检查项", MultiSelect: true, Options: []proto.QuestionOption{{Label: "格式"}, {Label: "内容"}}}}
		case proto.IxPlan:
			ix.ToolName = "ExitPlanMode"
			ix.Plan = "## 示例计划\n\n读取文件后汇报结果。"
		case proto.IxPermission:
			ix.ToolName = "Bash"
			ix.ToolUseID = "call_permission"
			ix.Summary = "查看当前目录"
			ix.ToolInput = json.RawMessage(`{"command":"pwd"}`)
		case proto.IxScreen:
			ix.Screen = "请选择示例操作\n❯ 1. Continue\n  2. Type something.\nEnter to confirm · Esc to cancel"
			ix.Dialog = screen.ParseDialog(ix.Screen)
			if ix.Dialog == nil {
				t.Fatal("sample dialog was not parsed")
			}
		}
		ixs = append(ixs, ix)
		fields := map[string]any{"/type": "interaction", "/interaction/kind": kind, "/interaction/id": ix.ID}
		if ix.Dialog != nil {
			fields["/interaction/dialog/options/0/key"] = "1"
			fields["/interaction/dialog/options/1/kind"] = "type_something"
		}
		add("interactions", "interaction_"+kind, "交互卡携带对应类型的提问、计划、权限或屏幕信息。", "event", ws(map[string]any{"type": "interaction", "interaction": ix}), fields,
			"server/internal/proto/interaction.go: Interaction", "server/internal/hub/http.go: Hub.serveWS", "apple/Shared/Models.swift: Interaction, ScreenDialog", "docs/protocol/protocol.md: 终端对话框（screen 交互）")
	}
	secret := ixs[3]
	for _, mode := range []string{"form", "url"} {
		ix := proto.Interaction{ID: "ix_elicitation_" + mode, MachineID: "m1", SessionID: "s1", Kind: proto.IxElicitation,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), MCPServerName: "example-mcp", AgentID: "agent_example", AgentType: "worker",
			Elicitation: &proto.Elicitation{Mode: mode, Message: "请确认示例请求"}}
		fields := map[string]any{"/interaction/kind": "elicitation", "/interaction/mcp_server_name": "example-mcp", "/interaction/elicitation/mode": mode, "/interaction/tool_use_id": nil}
		if mode == "form" {
			ix.Elicitation.RequestedSchema = json.RawMessage(`{"type":"object","properties":{"choice":{"type":"string","enum":["yes","no"]}},"required":["choice"]}`)
			fields["/interaction/elicitation/requested_schema/properties/choice/type"] = "string"
		} else {
			ix.Elicitation.URL, ix.Elicitation.ID = "https://example.com/confirm", "request_example"
			fields["/interaction/elicitation/url"] = ix.Elicitation.URL
			fields["/interaction/elicitation/elicitation_id"] = ix.Elicitation.ID
		}
		h := newTestHub(t)
		c := &client{out: make(chan []byte, 1), meta: clientMeta{features: map[string]bool{"elicitation": true}}}
		h.clients[c] = struct{}{}
		h.broadcast(map[string]any{"type": "interaction", "interaction": ix})
		add("interactions", "interaction_elicitation_"+mode, "仅向声明 elicitation 能力的连接下发，旧 iOS 不接收此样本。", "event", wireReceive(t, c), fields,
			"server/internal/hub/http.go: Hub.broadcast", "server/internal/proto/interaction.go: Elicitation", "android/core/contracts/src/main/kotlin/persona/core/contracts/Elicitation.kt")
	}
	secret.ID = "ix_secret"
	secret.Screen = "Password:"
	secret.Dialog = nil
	secret.Secret = screen.SecretPrompt(secret.Screen)
	add("interactions", "interaction_secret", "密码提示只携带密码框标记，不携带用户输入。", "event", ws(map[string]any{"type": "interaction", "interaction": secret}),
		map[string]any{"/interaction/kind": "screen", "/interaction/secret": true, "/interaction/dialog": nil}, "server/internal/screen/secret.go: SecretPrompt", "apple/Shared/Models.swift: Interaction.secret")
	ixs = append(ixs, secret)
	add("interactions", "interactions_list", "待处理列表使用与事件相同的交互类型。", "interactions", httpJSON(map[string]any{"interactions": ixs}),
		map[string]any{"/interactions/0/kind": "question", "/interactions/0/questions/1/multi_select": true, "/interactions/4/secret": true}, "server/internal/hub/http.go: Hub.routes", "apple/Shared/Models.swift: Interaction")
	add("interactions", "interaction_resolved", "已处理事件仅用交互标识撤回卡片。", "event", ws(map[string]any{"type": "interaction_resolved", "id": "ix_question"}), map[string]any{"/type": "interaction_resolved", "/id": "ix_question"}, "server/internal/hub/hub.go: Hub.resolve", "apple/Shared/LiveConnection.swift: ServerEvent.decode")

	working := sessions[0]
	working.SessionID = "s_working"
	working.State = proto.StateWorking
	working.PendingInteractions = 0
	unread := working
	unread.SessionID = "s_unread"
	unread.State = proto.StateUnread
	unread.Loop = nil
	// The fleet as the manager works it out (assistant/fleet_live.go buildFleet) from these three agents and the first card.
	fleet := FleetState{Phase: "needsYou", NeedsYou: 1, Working: 1, Unread: 1, WorkingSince: float64(now.Unix()), Details: true, Items: []FleetItem{
		{ID: "m1/s1", Title: sessions[0].Title, Place: "示例电脑 · 示例空间 › 示例任务", Kind: ixs[0].Kind, Detail: ixs[0].Questions[0].Question,
			Interaction: ixs[0].ID, Since: float64(ixs[0].CreatedAt.Unix())},
		{ID: "m1/s_working", Title: working.Title, Place: "示例电脑 · 示例空间 › 示例任务", Kind: "working", Since: float64(now.Unix())},
		{ID: "m1/s_unread", Title: unread.Title, Place: "示例电脑 · 示例空间 › 示例任务", Kind: "unread", Since: float64(unread.UpdatedAt.Unix())},
	}}
	_ = unread
	add("fleet", "fleet_snapshot", "Fleet 使用驼峰字段和 Unix 秒时间。", "fleet", httpJSON(map[string]any{"fleet": fleet}), map[string]any{"/fleet/phase": "needsYou", "/fleet/needsYou": 1, "/fleet/working": 1, "/fleet/unread": 1, "/fleet/workingSince": now.Unix()}, "server/internal/hub/liveactivity.go: buildFleet, FleetState", "apple/Shared/FleetState.swift: FleetState")
	add("fleet", "fleet_event", "Fleet 事件沿用快照结构。", "event", ws(map[string]any{"type": "fleet", "fleet": fleet}), map[string]any{"/type": "fleet", "/fleet/items/0/kind": "question", "/fleet/items/0/interaction": "ix_question"}, "server/internal/hub/liveactivity.go: liveActivities", "apple/Shared/LiveConnection.swift: ServerEvent.decode")

	inboxLark := assistant.InboxItem{ID: "lark:feed_example:10", Source: "lark", Kind: assistant.KindMention, Account: "feishu", AccountName: "飞书", Title: "示例讨论", Sender: "小王", Preview: "请确认示例。", Count: 1, StartedAt: now, UpdatedAt: now, Tier: assistant.TierAction, Lark: &assistant.InboxLark{FeedID: "feed_example", IsGroup: true, StartPosition: "10", MentionPosition: "10"}, Context: "仅详情展示的示例内容", Last: "10", TriagedAt: "10", Agent: &assistant.InboxAgent{State: "drafted", MachineID: "m1", SessionID: "s1", Draft: "已收到示例。"}}
	inboxMail := assistant.InboxItem{ID: "mail:account_example:thread_example:11", Source: "mail", Kind: assistant.KindMail, Account: "account_example", AccountName: "user@example.com", Title: "示例邮件", Preview: "示例已确认。", Count: 1, StartedAt: now, UpdatedAt: now, DoneAt: &now, DoneBy: "user", Read: true, Mail: &assistant.InboxMail{AccountID: "account_example", Mailbox: "INBOX", UIDs: []uint32{11}, MessageID: "message_example", From: assistant.MailAddress{Name: "小王", Email: "sender@example.com"}, Thread: "thread_example", Direct: true}}
	add("inbox", "inbox_list", "收件箱列表包含飞书和邮件条目，并移除内部上下文。", "inbox", httpJSON(map[string]any{"items": []assistant.InboxItem{inboxLark.Listed(), inboxMail.Listed()}}), map[string]any{"/items/0/source": "lark", "/items/0/context": nil, "/items/0/agent/draft": "已收到示例。", "/items/1/source": "mail", "/items/1/mail/uids/0": 11, "/items/1/done_by": "user"}, "server/internal/assistant/inbox.go: InboxItem.listed, Service.inboxRoutes", "apple/Shared/InboxModels.swift: InboxItem")
	wireFeed(t, add, httpJSON, now)
	wireErrors(t, add)
	wireCompatibility(t, files, add, ws, httpJSON, now)
	return files
}

func wireBody(t *testing.T, w *httptest.ResponseRecorder, status int) json.RawMessage {
	t.Helper()
	if w.Code != status || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("invalid HTTP sample: status %d, body %s", w.Code, w.Body.String())
	}
	return append(json.RawMessage(nil), w.Body.Bytes()...)
}
func wireReceive(t *testing.T, c *client) json.RawMessage {
	t.Helper()
	select {
	case b := <-c.out:
		if !json.Valid(b) {
			t.Fatal("invalid WS sample")
		}
		return b
	case <-time.After(time.Second):
		t.Fatal("no WS sample produced")
		return nil
	}
}

type wireAdder func(string, string, string, string, json.RawMessage, map[string]any, ...string)

// wireNode exercises the real RPC consumer without launching a node or opening a network connection.
func wireNode(t *testing.T, reply func(json.RawMessage) any) *Machine {
	t.Helper()
	return wireNodeMethod(t, "session.items", reply)
}

func wireNodeMethod(t *testing.T, expectedMethod string, reply func(json.RawMessage) any) *Machine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hp, np := rpc.New("h", h2nW), rpc.New("n", n2hW)
	np.OnRequest = func(_ context.Context, method string, p json.RawMessage) (any, error) {
		if method != expectedMethod {
			return nil, fmt.Errorf("unexpected method: %s", method)
		}
		return reply(p), nil
	}
	done := make(chan struct{}, 2)
	go func() { defer func() { done <- struct{}{} }(); _ = hp.Serve(ctx, n2hR) }()
	go func() { defer func() { done <- struct{}{} }(); _ = np.Serve(ctx, h2nR) }()
	t.Cleanup(func() {
		cancel()
		_ = h2nR.Close()
		_ = h2nW.Close()
		_ = n2hR.Close()
		_ = n2hW.Close()
		for range 2 {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("RPC cleanup timed out")
			}
		}
	})
	m := newMachine(&Hub{}, MachineConfig{ID: "m1"})
	m.peer = hp
	return m
}

func wireItemEvents(t *testing.T, add wireAdder, now time.Time, items []proto.Item) {
	from, to := 0, 8
	c := &client{out: make(chan []byte, 1), subs: map[[2]string]bool{{"m1", "s1"}: true}, lazy: true}
	h := &Hub{clients: map[*client]struct{}{c: {}}}
	h.itemsToSubscribers("m1", "s1", items, itemSpan{History: "history_example", FromSeq: &from, ToSeq: &to}, false)
	add("item_events", "items_live_lazy", "实时事件保留起点零值，并按订阅要求移除图片数据。", "event", wireReceive(t, c), map[string]any{"/type": "items", "/from_seq": 0, "/to_seq": 8, "/backlog": false, "/has_more": nil, "/more_after": false, "/items/1/image/lazy": true, "/items/1/image/data": ""}, "server/internal/hub/http.go: Hub.itemsToSubscribers", "apple/Shared/LiveConnection.swift: ServerEvent.decode")
	from, to = 8, 10
	h.itemsToSubscribers("m1", "s1", nil, itemSpan{History: "history_example", FromSeq: &from, ToSeq: &to}, false)
	add("item_events", "items_empty_advance", "没有内容块的行仍发送空数组和连续性范围。", "event", wireReceive(t, c), map[string]any{"/items": []any{}, "/from_seq": 8, "/to_seq": 10}, "server/internal/hub/http.go: Hub.itemsToSubscribers", "docs/protocol/protocol.md: items 的连续性", "apple/Shared/LiveConnection.swift: ServerEvent.decode")
	for _, scenario := range []string{"latest", "catchup", "capped"} {
		calls := 0
		m := wireNode(t, func(p json.RawMessage) any {
			calls++
			var args struct {
				AfterSeq int `json:"after_seq"`
			}
			if err := json.Unmarshal(p, &args); err != nil {
				return nil
			}
			seq := args.AfterSeq + 1
			out := map[string]any{"items": []proto.Item{{ID: fmt.Sprintf("entry_%d#0", seq), Seq: seq, TS: now, Role: "assistant", Kind: proto.KindText, Text: "示例回复。"}}, "history": "history_example", "to_seq": seq, "has_more": true}
			if scenario != "latest" {
				out["from_seq"] = args.AfterSeq
				out["more_after"] = scenario == "capped"
			}
			return out
		})
		c := &client{out: make(chan []byte, backlogPages+1)}
		since := 0
		if scenario != "latest" {
			since = 10
		}
		(&Hub{}).sendBacklog(context.Background(), c, m, "s1", since)
		var raw json.RawMessage
		for len(c.out) > 0 {
			raw = wireReceive(t, c)
		}
		fields := map[string]any{"/backlog": true, "/history": "history_example", "/more_after": false}
		switch scenario {
		case "latest":
			fields["/has_more"] = true
			fields["/from_seq"] = nil
			fields["/to_seq"] = 1
		case "catchup":
			fields["/has_more"] = nil
			fields["/from_seq"] = 10
			fields["/to_seq"] = 11
		case "capped":
			if calls != backlogPages {
				t.Fatalf("expected capped backlog, got %d pages", calls)
			}
			fields["/has_more"] = nil
			fields["/more_after"] = true
			fields["/from_seq"] = 10 + backlogPages - 1
			fields["/to_seq"] = 10 + backlogPages
		}
		add("item_events", "items_backlog_"+scenario, "订阅补发按最新页、接续补发或补发上限设置分页字段。", "event", raw, fields, "server/internal/hub/http.go: Hub.sendBacklog", "apple/Shared/LiveConnection.swift: ServerEvent.decode", "docs/protocol/protocol.md: WebSocket / items 的连续性")
	}
	m := wireNode(t, func(json.RawMessage) any {
		return map[string]any{"items": items, "has_more": false, "more_after": true, "history": "history_example", "from_seq": 0, "to_seq": 8}
	})
	req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?after_seq=0&lazy_images=1", nil)
	req.SetPathValue("sid", "s1")
	w := httptest.NewRecorder()
	(&Hub{}).sessionItems(w, req, m)
	add("items", "items_http_after", "HTTP 增量页保留连续性、后续页标记和图片占位。", "items_after", wireBody(t, w, 200), map[string]any{"/from_seq": 0, "/to_seq": 8, "/more_after": true, "/items/1/image/lazy": true}, "server/internal/hub/http.go: Hub.sessionItems", "apple/Shared/APIClient.swift: APIClient.ItemsResponse, APIClient.NewerResponse")
	rangeNode := wireNode(t, func(json.RawMessage) any {
		return map[string]any{"items": items[:1], "more": true}
	})
	req = httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?from_seq=1&through_seq=8", nil)
	req.SetPathValue("sid", "s1")
	w = httptest.NewRecorder()
	(&Hub{}).sessionItems(w, req, rangeNode)
	add("items", "items_fold_range", "展开折叠的分页使用 more 表示范围尚未读完。", "items_range", wireBody(t, w, 200),
		map[string]any{"/items/0/id": "entry_text#0", "/more": true},
		"server/internal/hub/http.go: Hub.sessionItems", "apple/Shared/APIClient.swift: APIClient.foldedWork.Range")
}

func wireErrors(t *testing.T, add wireAdder) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	r.RemoteAddr = "127.0.0.1:1234" // from the LAN, so the answer is the plain 401 every client sees
	newTestHub(t).auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unauthenticated request reached handler") })).ServeHTTP(w, r)
	add("errors", "unauthorized", "未鉴权响应保持字符串 error 字段。", "error", wireBody(t, w, 401), map[string]any{"/error": "invalid token"}, "server/internal/hub/http.go: Hub.auth", "apple/Shared/APIClient.swift: APIError.fields(in:)")
	for _, code := range []string{"input_draft", "dialog_open", "prompt_changed"} {
		data := json.RawMessage(`{"delivered":"none"}`)
		if code == "input_draft" {
			data = json.RawMessage(`{"delivered":"none","draft":"❯ 示例草稿","clearable":"示例草稿"}`)
		}
		w := httptest.NewRecorder()
		callErr(w, &rpc.Error{Code: code, Message: "请先处理示例终端状态。", Data: data})
		fields := map[string]any{"/error": "请先处理示例终端状态。", "/code": code, "/delivered": "none"}
		if code == "input_draft" {
			fields["/draft"] = "❯ 示例草稿"
			fields["/clearable"] = "示例草稿"
		}
		add("errors", "conflict_"+code, "拒绝操作的错误字段与 error 平铺在同一层。", "error", wireBody(t, w, 409), fields, "server/internal/hub/http.go: callErr", "apple/Shared/APIClient.swift: APIError.fields(in:)", "docs/protocol/protocol.md: 发消息 / 终端对话框")
	}
}

func wireCompatibility(t *testing.T, files map[string][]wireSample, add wireAdder, ws, httpJSON func(any) json.RawMessage, now time.Time) {
	add("compatibility", "unknown_event_ignored", "未知事件被当前 iOS 忽略。", "compat_event", ws(map[string]any{"type": "future_event", "extra": true}), map[string]any{}, "apple/Shared/LiveConnection.swift: ServerEvent.decode default", "docs/archive/redesign/README.md: D5")
	files["compatibility"][0].Expected.Accepted = false
	add("compatibility", "hello_unknown_field", "握手中的未知键不会影响已知字段。", "compat_event", ws(map[string]any{"type": "hello", "features": []string{"future_feature"}, "future_field": map[string]any{"enabled": true}}), map[string]any{"/type": "hello", "/features": []string{"future_feature"}}, "apple/Shared/LiveConnection.swift: ServerEvent.Envelope, ServerEvent.decode")
	add("compatibility", "hello_missing_features", "旧握手缺少能力列表时按空列表处理。", "compat_event", ws(map[string]any{"type": "hello"}), map[string]any{"/features": []any{}}, "apple/Shared/LiveConnection.swift: ServerEvent.decode")
	add("compatibility", "items_legacy_null", "旧事件的空 items 按空数组处理，连续性字段保持缺省。", "compat_event", ws(map[string]any{"type": "items", "machine_id": "m1", "session_id": "s1", "items": nil}),
		map[string]any{"/items": []any{}, "/history": nil, "/from_seq": nil, "/to_seq": nil, "/backlog": false, "/more_after": false},
		"apple/Shared/LiveConnection.swift: ServerEvent.decode", "docs/protocol/protocol.md: items 的连续性")
	add("compatibility", "session_unknown_enums", "未知会话状态降级为 unknown。", "compat_session", httpJSON(proto.Session{SessionID: "s1", Status: "future_status", State: "future_state", UpdatedAt: now}), map[string]any{"/status": "unknown", "/state": "unknown"}, "apple/Shared/Models.swift: SessionStatus.init(from:), AgentState.init(from:)", "docs/archive/redesign/README.md: D5")
	add("compatibility", "session_missing_fields", "旧会话缺字段时使用当前 iOS 的默认值。", "compat_session", httpJSON(map[string]any{"session_id": "s1", "status": "blocked"}), map[string]any{"/machine_id": "", "/title": "", "/state": "needs_you", "/assistant": false, "/pending_interactions": 0}, "apple/Shared/Models.swift: Session.init(from:)")
	add("compatibility", "item_unknown_kind", "未知内容块类型降级为 unknown。", "compat_event", ws(map[string]any{"type": "items", "machine_id": "m1", "session_id": "s1", "items": []proto.Item{{ID: "entry_future#0", Seq: 1, TS: now, Role: "assistant", Kind: "future_kind"}}}), map[string]any{"/items/0/kind": "unknown"}, "apple/Shared/Models.swift: ItemKind.init(from:)")
	add("compatibility", "fleet_bad_optional_stats", "Fleet 的可选统计字段类型错误时只丢弃统计字段。", "compat_event", ws(map[string]any{"type": "fleet", "fleet": map[string]any{"phase": "working", "working": 1, "stats": "invalid"}}), map[string]any{"/fleet/phase": "working", "/fleet/working": 1, "/fleet/stats": nil}, "apple/Shared/FleetState.swift: FleetState.init(from:)")
	add("compatibility", "fleet_unknown_values", "未知 Fleet 阶段变为空阶段，未知条目类型变为 screen。", "compat_event", ws(map[string]any{"type": "fleet", "fleet": map[string]any{"phase": "later", "items": []any{map[string]any{"id": "a/b", "kind": "teleport"}}}}),
		map[string]any{"/fleet/phase": "", "/fleet/items/0/kind": "screen"}, "apple/PersonaTests/FleetStateTests.swift: lenientDecoding")
	files["compatibility"][len(files["compatibility"])-1].Derived = false
	files["compatibility"][len(files["compatibility"])-1].FromTest = "FleetStateTests.lenientDecoding"
	for _, bad := range []struct {
		name, desc string
		body       any
	}{
		{"hello_wrong_known_type", "已知信封键类型错误会让整条握手被丢弃。", map[string]any{"type": "hello", "features": []string{}, "state": map[string]any{"enabled": true}}},
		{"session_bad_optional_field", "当前 iOS 不会单独忽略类型错误的会话增强字段。", map[string]any{"type": "sessions", "machine_id": "m1", "sessions": []any{map[string]any{"session_id": "s1", "background": "invalid"}}}},
		{"interaction_unknown_kind", "当前 iOS 不支持未知交互类型的降级解码。", map[string]any{"type": "interaction", "interaction": proto.Interaction{ID: "ix_future", MachineID: "m1", SessionID: "s1", Kind: "future_kind", CreatedAt: now, ExpiresAt: now}}},
	} {
		add("compatibility", bad.name, bad.desc, "compat_event", ws(bad.body), map[string]any{}, "apple/Shared/LiveConnection.swift: ServerEvent.decode", "apple/Shared/Models.swift: InteractionKind, Session.init(from:)", "docs/archive/redesign/README.md: D5（现状差异）")
		files["compatibility"][len(files["compatibility"])-1].Expected.Accepted = false
	}
	add("compatibility", "session_zero_time", "Go 零时间能解码，但不算有意义的日期。", "compat_session", httpJSON(proto.Session{SessionID: "s1"}), map[string]any{"/updated_at_meaningful": false, "/seen_at_meaningful": false}, "server/internal/proto/session.go: Session", "apple/Shared/JSONValue.swift: JSON.decoder, Date.isMeaningful")
	add("compatibility", "date_at_cutoff", "当前 iOS 将 2000 年起点本身也判为无意义日期。", "compat_session", httpJSON(map[string]any{"session_id": "s1", "updated_at": "2000-01-01T00:00:00Z"}), map[string]any{"/updated_at_meaningful": false}, "apple/Shared/JSONValue.swift: Date.isMeaningful", "docs/archive/redesign/README.md: D5（现状差异）")
	for i := range files["errors"] {
		if i == 0 {
			files["errors"][i].HTTPStatus = 401
		} else {
			files["errors"][i].HTTPStatus = 409
		}
	}
}

// wireFeed: an assistant conversation's feed with an answered record in full (#1231) and one from before it.
func wireFeed(t *testing.T, add func(file, name, description, decoder string, input json.RawMessage, fields map[string]any, sources ...string),
	httpJSON func(any) json.RawMessage, now time.Time) {
	ix := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "第一步放在哪里？"}, {Question: "示例插件叫什么？"}}}
	r := proto.Resolution{Answers: map[string]string{"第一步放在哪里？": "放到独立仓库（推荐）\n先只迁一个", "示例插件叫什么？": "example-plugin"}}
	// The record as the assistant writes it when the user answers a card of a conversation's task's agent.
	h := newTestHub(t)
	connectAssistant(t, h)
	taskIn(t, h, "conv_example")
	ix.MachineID, ix.SessionID = "box", "s1"
	h.asst().InteractionResolved(context.Background(), assistantwire.InteractionResolved{Interaction: ix, Resolution: r, Status: "resolved"})
	settleAssistant(h)
	var written []assistant.FeedItem
	all, _ := feed(h, "conv_example")
	for _, f := range all {
		if f.Kind == "answered" {
			written = append(written, f)
		}
	}
	if len(written) != 1 {
		t.Fatalf("feed = %+v", all)
	}
	full := written[0].Body
	legacy, _ := json.Marshal(map[string]string{"question": "使用 Bash", "answer": "批准了"})
	items := []assistant.FeedItem{
		{ID: "feed_1", Conversation: "conv_example", At: now, Kind: "answered", Ref: "todo_example", Body: full},
		{ID: "feed_2", Conversation: "conv_example", At: now, Kind: "answered", Ref: "todo_example", Body: legacy},
	}
	add("feed", "feed_answered_items", "回答记录带一行摘要，并按问题成对保存完整的问题和回答（保留换行）；旧记录只有摘要。", "feed",
		httpJSON(map[string]any{"items": items, "plans": map[string]*assistant.TaskPlan{}}),
		map[string]any{"/items/0/kind": "answered", "/items/0/body/answer": "放到独立仓库（推荐） 先只迁一个；example-plugin",
			"/items/0/body/items/0/answer": "放到独立仓库（推荐）\n先只迁一个", "/items/0/body/items/1/question": "示例插件叫什么？",
			"/items/1/body/answer": "批准了", "/items/1/body/items": nil},
		"server/internal/assistant/answered.go: answeredBody, answeredItems", "apple/Shared/Tasks.swift: FeedItem.answeredItems",
		"docs/protocol/protocol.md: 任务和对话动态")
}
