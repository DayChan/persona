package feishubot

import "testing"

// The received forms of a card, a to-do and a vote, shaped like the examples in the open platform's "message content"
// reference (https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/im-v1/message/events/message_content, sections
// interactive, todo and vote), with made-up text, keys and ids.
func TestParseContentCardsTodosVotes(t *testing.T) {
	mentions := []Mention{{Key: "@_user_1", ID: UserID{OpenID: "ou_example"}, Name: "Alice"}}
	docCard := `{"title":"Card title","elements":[` +
		`[{"tag":"button","text":"Primary","type":"primary"},{"tag":"button","text":"Secondary","type":"default"},{"tag":"button","text":"Danger","type":"danger"}],` +
		`[{"tag":"a","href":"https://example.com/","text":"Example"},{"tag":"text","text":" brings chat and docs together, "},` +
		`{"tag":"at","user_id":"@_user_1","user_name":""},{"tag":"text","text":" works faster."}],` +
		`[{"tag":"hr"}],` +
		`[{"tag":"text","text":"Image title"},{"tag":"img","image_key":"img_example"}],` +
		`[{"tag":"note","elements":[{"tag":"img","image_key":"img_example"},{"tag":"text","text":"A note"}]}],` +
		`[{"tag":"text","text":"Pick one: "},{"tag":"select_static","options":["One","Two"],"placeholder":"Choose"}],` +
		`[{"tag":"text","text":"More"},{"tag":"overflow","options":["Open the docs"]}],` +
		`[{"tag":"text","text":"Due"},{"tag":"date_picker","placeholder":"Pick a date","initial_date":"2021-1-1"}]]}`
	cases := []struct {
		name, msgType, content string
		mentions               []Mention
		want                   string
	}{
		{"card: the documented example", "interactive", docCard, mentions, "[卡片] Card title\n" +
			"Primary Secondary Danger\n" +
			"[Example](https://example.com/) brings chat and docs together, @Alice works faster.\n" +
			"---\n" +
			"Image title[图片]\n" +
			"[图片]A note\n" +
			"Pick one: Choose\n" +
			"More\n" +
			"Due"},
		{"card: an at node without its mention keeps the key", "interactive",
			`{"title":"","elements":[[{"tag":"text","text":"ask "},{"tag":"at","user_id":"@_user_1","user_name":""}]]}`, nil,
			"[卡片] ask @_user_1"},
		{"card: an at node by name, by open id and for everyone", "interactive",
			`{"title":"T","elements":[[{"tag":"at","user_id":"ou_other","user_name":"Bob"},{"tag":"text","text":" "},` +
				`{"tag":"at","user_id":"ou_example"},{"tag":"text","text":" "},{"tag":"at","user_id":"all"}]]}`, mentions,
			"[卡片] T\n@Bob @Alice @所有人"},
		{"card: no text", "interactive", `{"title":"","elements":[[{"tag":"date_picker"}]]}`, nil, "[卡片]"},
		{"card: another shape is walked in key order", "interactive",
			`{"c":{"text":"three"},"a":{"text":"one"},"b":{"content":"two","d":[{"title":"four"}]}}`, nil,
			"[卡片] one two four three"},
		{"card: not JSON", "interactive", `{`, nil, "[卡片]"},
		{"todo: the documented example", "todo",
			`{"task_id":"task-0001","summary":{"title":"","content":[[{"tag":"text","text":"Eat fruit, "},` +
				`{"tag":"a","text":"plan","href":"https://example.com/plan"},{"tag":"text","text":" with "},` +
				`{"tag":"at","user_id":"@_user_1"}]]},"due_time":"1623124318000"}`, mentions,
			"[任务] Eat fruit, [plan](https://example.com/plan) with @Alice"},
		{"todo: a summary with a title and two lines", "todo",
			`{"task_id":"task-0002","summary":{"title":"Weekly","content":[[{"tag":"text","text":"one"}],[{"tag":"text","text":"two"}]]}}`, nil,
			"[任务] **Weekly**\none\ntwo"},
		{"todo: no summary", "todo", `{"task_id":"task-0003"}`, nil, "[任务]"},
		{"vote: the documented example", "vote", `{"topic":"Lunch?","options":["Noodles","Rice"]}`, nil,
			"[投票] Lunch?: Noodles / Rice"},
		{"vote: a topic only", "vote", `{"topic":"Lunch?","options":[]}`, nil, "[投票] Lunch?"},
		{"vote: options only", "vote", `{"options":["Noodles","Rice"]}`, nil, "[投票] Noodles / Rice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseContent(tc.msgType, tc.content, tc.mentions).Text; got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A card reads the same every time: the walk over a shape that is not the received one does not follow Go's map order.
func TestParseContentCardOrderIsStable(t *testing.T) {
	const card = `{"a":{"text":"one"},"b":{"text":"two"},"c":{"text":"three"},"d":{"text":"four"},"e":{"text":"five"}}`
	const want = "[卡片] one two three four five"
	for i := range 20 {
		if got := ParseContent("interactive", card, nil).Text; got != want {
			t.Fatalf("parse %d: got %q, want %q", i, got, want)
		}
	}
}
