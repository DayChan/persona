package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// Before the channels' pipeline the WeChat program kept, in its own sealed state, where the owner's messages went
// (/new), the turns it waited on and what it had asked in text. migrateWeChatState moves those three over to the
// channels' state (settings channels and channels.state, the assistant's since) once, before the program starts and
// before the hub starts the assistant. This compatibility migration also applies to the marketplace extension,
// which keeps only its token, cursor, context token and the messages it
// saw, and would drop the rest the first time it saves. The hub reads the sealed state, so the move is its.

// The assistant's keys and what of them the move writes (the assistant's channels.go keeps the rest as it is).
const (
	channelsKey      = "channels"
	channelsStateKey = "channels.state"
)

type wechatLegacy struct {
	Target *struct {
		Machine string `json:"machine"`
		Session string `json:"session"`
		Fresh   bool   `json:"fresh"`
	} `json:"target"`
	Runs []struct {
		Machine string    `json:"machine"`
		Session string    `json:"session"`
		Base    int       `json:"base"`
		MsgIDs  []string  `json:"msg_ids"`
		Probe   string    `json:"probe"`
		Started time.Time `json:"started"`
		Seen    bool      `json:"seen"`
		Held    bool      `json:"held"`
	} `json:"runs"`
	Asks []struct {
		ID        string            `json:"id"`
		Machine   string            `json:"machine"`
		Session   string            `json:"session"`
		Kind      string            `json:"kind"`
		Title     string            `json:"title"`
		Questions []proto.Question  `json:"questions"`
		Answers   map[string]string `json:"answers"`
		Step      int               `json:"step"`
		Options   []string          `json:"options"`
		At        time.Time         `json:"at"`
	} `json:"asks"`
}

func (h *Manager) migrateWeChatState(ctx context.Context) {
	if h.store == nil {
		return
	}
	sec := h.secretsFor("wechat")
	data, err := sec.Load(ctx)
	if err != nil || len(data) == 0 {
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	_, t := raw["target"]
	_, r := raw["runs"]
	_, a := raw["asks"]
	if !t && !r && !a {
		return
	}
	var old wechatLegacy
	if err := json.Unmarshal(data, &old); err != nil {
		h.logf("channels: wechat's old state does not read: %v", err)
		return
	}
	prefs := map[string]map[string]json.RawMessage{}
	state := map[string]json.RawMessage{}
	if _, err := h.store.Setting(ctx, channelsKey, &prefs); err != nil {
		h.logf("channels: wechat's old state: %v", err)
		return
	}
	if _, err := h.store.Setting(ctx, channelsStateKey, &state); err != nil {
		h.logf("channels: wechat's old state: %v", err)
		return
	}
	if prefs == nil {
		prefs = map[string]map[string]json.RawMessage{}
	}
	if state == nil {
		state = map[string]json.RawMessage{}
	}
	if pref := prefs["wechat"]; pref == nil || pref["target"] == nil {
		var t *channel.Target
		switch {
		case old.Target == nil:
		case old.Target.Fresh:
			t = &channel.Target{Kind: channel.TargetNew}
		case old.Target.Session != "":
			t = &channel.Target{Kind: channel.TargetConversation, MachineID: old.Target.Machine, SessionID: old.Target.Session}
		}
		if t != nil {
			if pref == nil {
				pref = map[string]json.RawMessage{}
				prefs["wechat"] = pref
			}
			pref["target"], _ = json.Marshal(t)
		}
	}
	var runs []*channel.Run
	var asks []*channel.Ask
	_ = json.Unmarshal(state["runs"], &runs)
	_ = json.Unmarshal(state["asks"], &asks)
	for i, o := range old.Runs {
		if o.Session == "" {
			continue
		}
		runs = append(runs, &channel.Run{ID: fmt.Sprintf("%s_migrated_%d", "wechat", i), Channel: "wechat",
			MachineID: o.Machine, SessionID: o.Session, TargetKind: channel.TargetConversation, Base: o.Base, MsgIDs: o.MsgIDs, Probe: o.Probe,
			Started: o.Started, Seen: o.Seen, Held: o.Held})
	}
	for _, o := range old.Asks {
		asks = append(asks, &channel.Ask{ID: o.ID, Channel: "wechat", MachineID: o.Machine, SessionID: o.Session, Kind: o.Kind,
			Title: o.Title, Questions: o.Questions, Answers: o.Answers, Step: o.Step, Options: o.Options, At: o.At})
	}
	state["runs"], _ = json.Marshal(runs)
	state["asks"], _ = json.Marshal(asks)
	if err := h.store.PutSetting(ctx, channelsKey, prefs); err != nil {
		h.logf("channels: wechat's old state: %v", err)
		return
	}
	if err := h.store.PutSetting(ctx, channelsStateKey, state); err != nil {
		h.logf("channels: wechat's old state: %v", err)
		return
	}
	delete(raw, "target")
	delete(raw, "runs")
	delete(raw, "asks")
	out, _ := json.Marshal(raw)
	if err := sec.Save(ctx, out); err != nil {
		h.logf("channels: wechat's state: %v", err)
		return
	}
	h.logf("channels: wechat's target, %d turns and %d questions moved to the channels' state", len(old.Runs), len(old.Asks))
}
