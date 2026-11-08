package extmgr

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The `only_conclusions` switch (#1226) is off by default and for settings saved before it; an app that does not know it
// leaves it as it was; the assistant's feishu.status says it and feishu.configure sets it; the channel page lists it.
func TestBotOnlyConclusionsSwitch(t *testing.T) {
	w := newBotWorld(t)
	w.pair(t)
	feats := func() map[string]any {
		f, _ := w.feishu(t, assistantwire.FeishuStatusGet, nil)["features"].(map[string]any)
		return f
	}
	if f := feats(); f["only_conclusions"] != false {
		t.Fatalf("default features %v: only_conclusions must be off", f)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"features":{"main":true,"cards":true,"only_conclusions":true}}`); code != 200 {
		t.Fatal(code)
	}
	if f := feats(); f["only_conclusions"] != true || f["reports"] != true {
		t.Fatalf("only_conclusions on: %v", f)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"features":{"main":true,"cards":true,"agents":true}}`); code != 200 {
		t.Fatal(code)
	}
	if f := feats(); f["only_conclusions"] != true || f["agents"] != true {
		t.Fatalf("an older app's PUT: %v", f)
	}
	w.feishu(t, assistantwire.FeishuConfigure, map[string]any{"features": map[string]bool{"only_conclusions": false}})
	if f := feats(); f["only_conclusions"] != false || f["agents"] != true {
		t.Fatalf("only_conclusions off by the assistant: %v", f)
	}
}

func TestChannelListHasTheOnlyConclusionsSwitch(t *testing.T) {
	w := newChannelWorld(t)
	out, err := w.channels(t, assistantwire.ChannelsList, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out["channels"].([]any) {
		m := c.(map[string]any)
		if m["id"] != "feishu" {
			continue
		}
		for _, f := range m["features"].([]any) {
			if fm := f.(map[string]any); fm["id"] == "only_conclusions" {
				if fm["on"] == true || fm["title"] == "" || fm["description"] == "" {
					t.Fatalf("the only_conclusions switch %v", fm)
				}
				return
			}
		}
		t.Fatalf("no only_conclusions switch in %v", m["features"])
	}
	t.Fatal("no Lark bot channel")
}
