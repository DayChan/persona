package feishubot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func cardFence(card string) string { return "```persona-card\n" + card + "\n```" }

// Each kind of card becomes the few lines a Feishu reader takes in at once: never the JSON, never a code block.
func TestPersonaCardsBecomeNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, card string
		want       []string
	}{
		{"stat", `{"type":"stat","title":"今天步数","value":8432,"unit":"步","note":"比昨天 +12%"}`,
			[]string{"今天步数：**8,432 步** · 比昨天 +12%\n"}},
		{"stat without a title", `{"type":"stat","value":7.25,"unit":"小时"}`,
			[]string{"数值：**7.3 小时**\n"}},
		{"line", `{"type":"line","title":"近 7 天睡眠","unit":"小时","note":"平均 6.7 小时","points":[{"label":"10/28","value":6.9},{"label":"10/29","value":6.4},{"label":"10/30","value":7.1},{"label":"10/31","value":6.2}]}`,
			[]string{"**近 7 天睡眠** ▆▃█▁\n", "10/28 6.9 · 10/29 6.4 · 10/30 7.1 · 10/31 6.2（小时）\n", "平均 6.7 小时\n"}},
		{"bar from zero", `{"type":"bar","title":"今天每餐热量","unit":"kcal","points":[{"label":"早餐","value":555},{"label":"午餐","value":900},{"label":"晚餐","value":750}]}`,
			[]string{"**今天每餐热量** ▅█▇\n", "早餐 555 · 午餐 900 · 晚餐 750（kcal）\n"}},
		{"line with a value", `{"type":"line","unit":"kg","value":61.8,"points":[{"label":"周一","value":62.4},{"label":"周日","value":61.8}]}`,
			[]string{"█▁\n", "周一 62.4 · 周日 61.8（kg）\n", "当前 **61.8 kg**\n"}},
		{"one bar", `{"type":"bar","title":"今天","points":[{"label":"早餐","value":555}]}`,
			[]string{"**今天**\n", "早餐 555\n"}},
		{"stacked bar", `{"type":"stacked_bar","title":"今天的热量分配","subtitle":"推荐全天摄入","total":{"value":2250,"unit":"kcal"},"segments":[{"label":"早餐","value":555,"note":"已吃"},{"label":"午餐","value":900},{"label":"晚餐","value":750}],"rest":{"label":"还能吃","value":45}}`,
			[]string{"| 今天的热量分配 · 推荐全天摄入 | 2,250 kcal |\n| --- | --- |\n| 早餐 · 已吃 | 555 kcal |\n| 午餐 | 900 kcal |\n| 晚餐 | 750 kcal |\n| 还能吃 | 45 kcal |\n"}},
		{"stacked bar without a total", `{"type":"stacked_bar","unit":"小时","note":"按手表记录","segments":[{"label":"深睡","value":1.2},{"label":"浅睡","value":4.1}]}`,
			[]string{"| 合计 | 5.3 小时 |\n", "| 深睡 | 1.2 小时 |\n", "|\n\n按手表记录\n"}},
		{"ring with what remains", `{"type":"ring","title":"今天的热量","total":{"value":2000,"unit":"kcal"},"segments":[{"label":"早餐","value":480},{"label":"午餐","value":720}]}`,
			[]string{"| 今天的热量 | 2,000 kcal |\n| --- | --- |\n| 早餐 | 480 kcal |\n| 午餐 | 720 kcal |\n| 剩余 | 800 kcal |\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PersonaCards("结论。\n\n" + cardFence(tc.card) + "\n\n后面的话")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, bad := range []string{"```", `"type"`, "persona-card"} {
				if strings.Contains(got, bad) {
					t.Errorf("%q left in\n%s", bad, got)
				}
			}
			if !strings.HasPrefix(got, "结论。\n\n") || !strings.HasSuffix(got, "\n\n后面的话") {
				t.Errorf("the text around the card changed:\n%s", got)
			}
		})
	}
}

// A card the apps cannot draw either leaves its title, or nothing; never its JSON.
func TestPersonaCardsTheAppsCannotDraw(t *testing.T) {
	for _, tc := range []struct{ name, card, want string }{
		{"unknown kind with a title", `{"type":"pie","title":"占比","segments":[{"label":"a","value":1}]}`, "占比\n"},
		{"unknown kind", `{"type":"pie","segments":[{"label":"a","value":1}]}`, ""},
		{"one point", `{"type":"line","title":"睡眠","points":[{"label":"周一","value":6.5}]}`, "睡眠\n"},
		{"no value", `{"type":"stat","title":"今天步数"}`, "今天步数\n"},
		{"a negative part", `{"type":"stacked_bar","segments":[{"label":"a","value":-1},{"label":"b","value":3}]}`, ""},
		{"not JSON", `{"type":"stat","value":`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PersonaCards("前面\n\n" + cardFence(tc.card) + "\n\n后面")
			if want := "前面\n\n" + tc.want + "\n后面"; tc.want != "" && got != want {
				t.Errorf("got %q, want %q", got, want)
			}
			if tc.want == "" && strings.Contains(got, "{") {
				t.Errorf("JSON left: %q", got)
			}
			if strings.Contains(got, "```") {
				t.Errorf("code block left: %q", got)
			}
		})
	}
	// never closed (the answer was cut): nothing of it shows
	if got := PersonaCards("前面\n\n```persona-card\n{\"type\":\"stat\",\"value\":1"); strings.Contains(got, "{") || strings.Contains(got, "```") {
		t.Errorf("an unclosed card: %q", got)
	}
}

// Only persona-card blocks change: other code, a persona-view block (drawn elsewhere), a card shown as an example
// inside another code block, and text with no card at all stay as they were.
func TestPersonaCardsLeaveTheRest(t *testing.T) {
	for _, in := range []string{
		"没有卡片，`code` 和 **加粗**。\n\n```json\n{\"type\":\"stat\",\"value\":1}\n```\n",
		"````markdown\n```persona-card\n{\"type\":\"stat\",\"value\":1}\n```\n````\n",
		"```persona-view\n{\"type\":\"metric\",\"label\":\"a\",\"value\":\"1\",\"fallback\":\"a 1\"}\n```\n",
	} {
		if got := PersonaCards(in); got != in {
			t.Errorf("changed:\n%q\n→\n%q", in, got)
		}
	}
}

// What the model writes is shown as written: a | in a label does not split the table, ** does not bold.
func TestPersonaCardsEscapeTheModelsWords(t *testing.T) {
	got := PersonaCards(cardFence(`{"type":"stacked_bar","title":"A|B","segments":[{"label":"**早**|餐","value":1,"note":"<b>"}]}`))
	if strings.Count(got, "|") != 3*3 || strings.Contains(got, "**早**") || strings.Contains(got, "<b>") {
		t.Errorf("not escaped:\n%s", got)
	}
}

// A long series keeps its last 31 points, the cut marked.
func TestPersonaCardsLongSeries(t *testing.T) {
	var pts []string
	for i := 1; i <= 40; i++ {
		pts = append(pts, fmt.Sprintf(`{"label":"d%d","value":%d}`, i, i))
	}
	got := PersonaCards(cardFence(`{"type":"line","points":[` + strings.Join(pts, ",") + `]}`))
	if !strings.Contains(got, "… · d10 10 · ") || strings.Contains(got, "d9 9") || !strings.Contains(got, "d40 40") {
		t.Errorf("got\n%s", got)
	}
	if n := strings.Count(strings.SplitN(got, "\n", 2)[0], "") - 1; n != 31 {
		t.Errorf("sparkline of %d marks:\n%s", n, got)
	}
}

// A table never swallows the line after it, and does not run into the line before it.
func TestPersonaCardsKeepTablesApart(t *testing.T) {
	got := PersonaCards("前面一句\n" + cardFence(`{"type":"stacked_bar","segments":[{"label":"a","value":1}]}`) + "\n后面一句")
	if !strings.Contains(got, "前面一句\n\n| 合计") || !strings.Contains(got, "| a | 1 |\n\n后面一句") {
		t.Errorf("got %q", got)
	}
}

// The bot's answers go through CardMarkdown: a card in one comes out as numbers, and the chat list's summary of the
// message shows the card's title, not its JSON.
func TestFeishuAnswersShowCardsAsNumbers(t *testing.T) {
	answer := "今天走了 8,432 步。\n\n" + cardFence(`{"type":"stat","title":"今天步数","value":8432,"unit":"步"}`)
	got := CardMarkdown(answer)
	if !strings.Contains(got, "今天步数：**8,432 步**") || strings.Contains(got, "```") {
		t.Errorf("CardMarkdown:\n%s", got)
	}
	var v struct {
		Config struct {
			Summary struct {
				Content string `json:"content"`
			} `json:"summary"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(NewCard("", "", answer).JSON()), &v); err != nil {
		t.Fatal(err)
	}
	if s := v.Config.Summary.Content; strings.Contains(s, "{") || strings.Contains(s, "persona-card") || !strings.Contains(s, "今天步数") {
		t.Errorf("summary %q", s)
	}
}

// What a health answer with charts looks like in Feishu: the bars as a sparkline and their numbers, the day's split as
// a small table, the rest of the answer as it was.
func ExamplePersonaCards() {
	fmt.Print(PersonaCards("今天一共吃了 **1,830 kcal**，还能吃 420 kcal。\n\n" +
		cardFence(`{"type":"bar","title":"今天每餐热量","unit":"kcal","points":[{"label":"早餐","value":480},{"label":"午餐","value":870},{"label":"晚餐","value":480}]}`) + "\n\n" +
		cardFence(`{"type":"stacked_bar","title":"今天的热量分配","subtitle":"推荐全天摄入","total":{"value":2250,"unit":"kcal"},"segments":[{"label":"早餐","value":480},{"label":"午餐","value":870},{"label":"晚餐","value":480,"note":"到现在"}],"rest":{"label":"还能吃","value":420}}`) + "\n\n" +
		cardFence(`{"type":"stat","title":"今天步数","value":9085,"unit":"步","note":"比昨天 +52%"}`) + "\n"))
	// Output:
	// 今天一共吃了 **1,830 kcal**，还能吃 420 kcal。
	//
	// **今天每餐热量** ▅█▅
	// 早餐 480 · 午餐 870 · 晚餐 480（kcal）
	//
	// | 今天的热量分配 · 推荐全天摄入 | 2,250 kcal |
	// | --- | --- |
	// | 早餐 | 480 kcal |
	// | 午餐 | 870 kcal |
	// | 晚餐 · 到现在 | 480 kcal |
	// | 还能吃 | 420 kcal |
	//
	// 今天步数：**9,085 步** · 比昨天 +52%
}

// Numbers at the ends of float64 (a garbled or hostile card) still give a sparkline, never a panic.
func TestPersonaCardsExtremeValues(t *testing.T) {
	for _, card := range []string{
		`{"type":"line","title":"t","points":[{"label":"a","value":-1e308},{"label":"b","value":1e308},{"label":"c","value":0}]}`,
		`{"type":"bar","title":"t","points":[{"label":"a","value":1e308},{"label":"b","value":-1e308}]}`,
	} {
		got := PersonaCards(cardFence(card))
		if !strings.Contains(got, "**t** ") {
			t.Errorf("no sparkline in\n%s", got)
		}
	}
}

// A stacked bar with many parts keeps the table short: the first parts, then the others folded into one row.
func TestPersonaCardsLongStackedBar(t *testing.T) {
	var segs []string
	for i := 0; i < 100; i++ {
		segs = append(segs, fmt.Sprintf(`{"label":"p%d","value":1}`, i))
	}
	got := PersonaCards(cardFence(`{"type":"stacked_bar","title":"t","segments":[` + strings.Join(segs, ",") + `]}`))
	if rows := strings.Count(got, "\n| "); rows > mostPoints+1 {
		t.Errorf("%d rows, want at most %d:\n%s", rows, mostPoints+1, got)
	}
	if !strings.Contains(got, "| t | 100 |") || !strings.Contains(got, "| 其他 | 70 |") {
		t.Errorf("want total 100 and 其他 70 in\n%s", got)
	}
}
