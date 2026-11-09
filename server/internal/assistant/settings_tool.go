package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// persona_settings: Persona changes its own settings for the user (docs/assistant/assistant.md "Operating Persona itself"). The
// assistant's own ("About me", its launcher, "notify tasks the main chat starts only in the main chat") are its to read and change; the hub's (the
// inbox's rules, the blocked Claude config directories, mail accounts…) are the hub's settings app's, which asks the
// user itself (app.tool persona_settings). Reads are free; every change is a card on the phone that says what changes
// (before → after) and is applied only when approved.

// ownSetting is one of the assistant's own settings persona_settings offers.
type ownSetting struct {
	Setting
	get func(ctx context.Context) (string, error)
	set func(ctx context.Context, v json.RawMessage) (*ownChange, error)
}

// ownChange is a change waiting for the phone: what the card says and how it is made (none: nothing to change).
type ownChange struct {
	summary  string                   // for the assistant: the change made nothing to do
	cardText func(l i18n.Lang) string // what the card says, in the user's language
	apply    func(ctx context.Context) (string, error)
}

// card is what the approval card says in l.
func (c *ownChange) card(l i18n.Lang) string {
	if c.cardText != nil {
		return c.cardText(l)
	}
	return c.summary
}

func (s *Service) ownSettings() []ownSetting {
	getSet := []string{"get", "set"}
	return []ownSetting{
		{Setting: Setting{Key: "assistant.notes", Title: "关于我", Actions: getSet,
			About: "what Persona knows about the user (设置 › 助手 › 关于我), Markdown appended to the assistant's instructions; new conversations read it",
			Value: "a string (Markdown; the whole text)"},
			get: func(ctx context.Context) (string, error) { return orNone(s.notes(ctx)), nil },
			set: s.setNotes},
		{Setting: Setting{Key: "assistant.main_tasks_quiet", Title: "主对话派的任务只在主对话里通知", Actions: getSet,
			About: "on (the default): tasks the main conversation opens (task_plan, agent_start) send no pushes, stay off the Live Activity and " +
				"task badges, and the main conversation is told when one is done, waits for the user or fails instead; off: they notify like any agent. " +
				"Applies to tasks opened from now on",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.mainTasksQuiet(ctx)), nil },
			set: s.setMainTasksQuiet},
		{Setting: Setting{Key: "assistant.start_requested_tasks", Title: "我在对话里说过的，直接开始", Actions: getSet,
			About: "on (the default): task_plan tasks marked user_requested (the user asked for that very task in this turn) start at once " +
				"instead of waiting for 开始 on the plan card; off: only read-only tasks and remembered kinds start without asking",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.startRequestedTasks(ctx)), nil },
			set: s.setStartRequestedTasks},
		{Setting: Setting{Key: "assistant.recording_days", Title: "录音在知识库里保留", Actions: getSet,
			About: "how many days the phone's and watch's recording transcripts stay in the knowledge base (kind recordings); older " +
				"ones are deleted from it (the audio and the recordings on the phone are not touched). 14 by default",
			Value: "a whole number of days, 1–365"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.recordingDays(ctx)), nil },
			set: s.setRecordingDays},
		{Setting: Setting{Key: "assistant.done_needs_delivery", Title: "上线并实测才算做完", Actions: getSet,
			About: "off (the default): a task_plan task closes when its work is judged done; on: only once it is live and checked " +
				"(or has nothing to ship): stopped at a PR, merged or off by default it stays open with where it is held, its agent is told " +
				"so with its prompt, and the ones waiting for the user's nod are listed together in the snapshot. Applies from the next judgment / task",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.doneNeedsDelivery(ctx)), nil },
			set: s.setDoneNeedsDelivery},
		{Setting: Setting{Key: "assistant.launcher", Title: "Persona 的启动器", Actions: getSet,
			About: "which launcher (model) new Persona conversations start with by default",
			Value: "a launcher name from this setting's get"},
			get: s.getLauncher,
			set: s.setLauncher},
		{Setting: Setting{Key: "model_profiles.tiers", Title: "Agent presets (the model profiles' tiers)", Actions: getSet,
			About: "the Agent presets: each model profile's named tiers, what agent_start / task_plan's tier and the New Agent " +
				"form's presets run on (launcher, model, effort) and when to use each; get lists every profile's, set adds, " +
				"changes or removes one tier, in every profile unless profiles names some (a switch then keeps it)",
			Value: modelTiersValue},
			get: s.getModelTiers,
			set: s.setModelTiers},
	}
}

func (s *Service) toolPersonaSettings(ctx context.Context, call ToolCall, args json.RawMessage) proto.ToolResult {
	var a struct {
		Action string          `json:"action"`
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult(fmt.Sprintf("invalid arguments: %v", err), true)
	}
	own := s.ownSettings()
	if a.Action == "" || a.Action == "list" {
		text, err := s.listSettings(ctx, own)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(text, false)
	}
	for _, o := range own {
		if o.Key == a.Key {
			text, err := s.ownSettingAction(ctx, call, o, a.Action, a.Value)
			if err != nil {
				return proto.TextResult(err.Error(), true)
			}
			return proto.TextResult(text, false)
		}
	}
	return s.appTool(ctx, AppSettings, call, args) // the hub's
}

// listSettings is action list: the assistant's settings and the hub's, by key.
func (s *Service) listSettings(ctx context.Context, own []ownSetting) (string, error) {
	var res struct {
		Settings []Setting `json:"settings"`
	}
	if err := s.appCall(ctx, AppSettings, SettingsList, map[string]any{}, &res); err != nil {
		return "", err
	}
	all := res.Settings
	for _, o := range own {
		all = append(all, o.Setting)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	var b strings.Builder
	b.WriteString("Settings you can read and (with the user's approval on the phone) change:\n")
	for _, st := range all {
		fmt.Fprintf(&b, "- %s (%s): %s. Actions: %s.", st.Key, st.Title, st.About, strings.Join(st.Actions, ", "))
		if st.Value != "" {
			fmt.Fprintf(&b, " Value: %s.", st.Value)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Routines and follow-ups have their own tools (schedule_routine, schedule_followup); knowledge base tags are kb_update. " +
		"Not changeable here: tokens, pairing, devices, the machines the hub reaches, permission modes.")
	return b.String(), nil
}

// ownSettingAction reads one of the assistant's settings, or changes it once the user approves on the phone.
func (s *Service) ownSettingAction(ctx context.Context, call ToolCall, o ownSetting, action string, value json.RawMessage) (string, error) {
	switch action {
	case "get":
		return o.get(ctx)
	case "set":
	default:
		return "", fmt.Errorf("%s does not take %q (it takes %s)", o.Key, action, strings.Join(o.Actions, ", "))
	}
	change, err := o.set(ctx, value)
	if err != nil {
		return "", err
	}
	if change.apply == nil {
		return change.summary, nil // nothing to change
	}
	l := s.lang(ctx)
	title := o.Title
	if k, ok := settingTitles[o.Key]; ok {
		title = i18n.T(l, "assistant-settings_tool."+k)
	}
	shown, _ := json.Marshal(map[string]string{"setting": title, "action": action})
	now := time.Now().UTC()
	r, err := s.ask(ctx, proto.Interaction{ID: "ix_" + randomHex(6), MachineID: call.MachineID, SessionID: call.SessionID,
		Kind: proto.IxPermission, ToolName: "persona_settings", Summary: change.card(l), ToolInput: shown, SessionTitle: "Persona",
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)})
	if err != nil {
		return "", err
	}
	if r.Allow == nil || !*r.Allow {
		return "The user declined on the phone: nothing changed.", nil
	}
	return change.apply(ctx)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return s
}

// settingTitles are the settings' titles for the card the user approves on, in this file's namespace (Title stays
// what the assistant reads).
var settingTitles = map[string]string{"assistant.notes": "title_notes", "assistant.main_tasks_quiet": "title_main_tasks_quiet",
	"assistant.start_requested_tasks": "title_start_requested_tasks", "assistant.launcher": "title_launcher",
	"assistant.recording_days": "title_recording_days", "assistant.done_needs_delivery": "title_done_needs_delivery",
	"model_profiles.tiers": "title_model_tiers"}

// clipped is a value as the card shows it, in l.
func clipped(l i18n.Lang, s string) string {
	if strings.TrimSpace(s) == "" {
		s = i18n.T(l, "assistant-settings_tool.none")
	}
	return clip(oneLine(s), 120)
}

func (s *Service) setNotes(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var text string
	if err := json.Unmarshal(v, &text); err != nil {
		return nil, errors.New("value must be a string")
	}
	text = strings.TrimSpace(text)
	before := s.notes(ctx)
	if text == before {
		return &ownChange{summary: "关于我没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_notes", i18n.Args{"before": clipped(l, before), "after": clipped(l, text)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.Notes = &text; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return "关于我 updated; new conversations read it (this one keeps what it started with).", nil
		}}, nil
}

func (s *Service) setMainTasksQuiet(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return nil, errors.New("value must be true or false")
	}
	before := s.mainTasksQuiet(ctx)
	if on == before {
		return &ownChange{summary: "「主对话派的任务只在主对话里通知」没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	word := map[bool]string{true: "assistant-settings_tool.on", false: "assistant-settings_tool.off"}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_main_tasks_quiet", i18n.Args{"before": i18n.T(l, word[before]), "after": i18n.T(l, word[on])})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MainTasksQuiet = &on; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.main_tasks_quiet is %v for tasks opened from now on (existing ones keep theirs).", on), nil
		}}, nil
}

func (s *Service) setStartRequestedTasks(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return nil, errors.New("value must be true or false")
	}
	before := s.startRequestedTasks(ctx)
	if on == before {
		return &ownChange{summary: "「我在对话里说过的，直接开始」没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	word := map[bool]string{true: "assistant-settings_tool.on", false: "assistant-settings_tool.off"}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_start_requested_tasks", i18n.Args{"before": i18n.T(l, word[before]), "after": i18n.T(l, word[on])})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.StartRequestedTasks = &on; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.start_requested_tasks is %v for plans from now on.", on), nil
		}}, nil
}

func (s *Service) setRecordingDays(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var days int
	if err := json.Unmarshal(v, &days); err != nil || days < minRecordingDays || days > maxRecordingDays {
		return nil, errors.New("value must be a whole number of days, 1–365")
	}
	before := s.recordingDays(ctx)
	if days == before {
		return &ownChange{summary: "「录音在知识库里保留」没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_recording_days", i18n.Args{"before": before, "after": days})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.RecordingDays = &days; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.kbWake() // a shorter retention prunes now
			return fmt.Sprintf("assistant.recording_days is %d: recordings older than that leave the knowledge base.", days), nil
		}}, nil
}

func (s *Service) setDoneNeedsDelivery(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return nil, errors.New("value must be true or false")
	}
	before := s.doneNeedsDelivery(ctx)
	if on == before {
		return &ownChange{summary: "「上线并实测才算做完」没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	word := map[bool]string{true: "assistant-settings_tool.on", false: "assistant-settings_tool.off"}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_done_needs_delivery", i18n.Args{"before": i18n.T(l, word[before]), "after": i18n.T(l, word[on])})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.DoneNeedsDelivery = &on; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.done_needs_delivery is %v from the next judgment and task on.", on), nil
		}}, nil
}

// launchers are the launchers of the assistant's machine, as the apps list them.
func (s *Service) launchers(ctx context.Context) ([]proto.Launcher, error) {
	machine := s.settings(ctx).MachineID
	if _, ok := s.device(ctx, machine); !ok {
		return nil, errors.New("the assistant's machine is not configured")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.machineLaunchers(cctx, machine)
}

func (s *Service) getLauncher(ctx context.Context) (string, error) {
	now := s.launcherNow(ctx)
	ls, err := s.launchers(ctx)
	if err != nil {
		return "now: " + now + " (the machine's launchers could not be listed: " + err.Error() + ")", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "now: %s\navailable:\n", now)
	for _, l := range ls {
		fmt.Fprintf(&b, "- %s (model %s)\n", l.Name, orNone(l.Model))
	}
	return b.String(), nil
}

// setLauncher writes the launcher into Model configuration › Main chat, keeping its model and effort.
func (s *Service) setLauncher(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var name string
	if err := json.Unmarshal(v, &name); err != nil || strings.TrimSpace(name) == "" {
		return nil, errors.New("value must be a launcher name")
	}
	name = strings.TrimSpace(name)
	before := s.launcherNow(ctx)
	if name == before {
		return &ownChange{summary: "启动器没有变化"}, nil
	}
	ls, err := s.launchers(ctx)
	if err != nil {
		return nil, err
	}
	known := false
	for _, l := range ls {
		known = known || l.Name == name
	}
	if !known {
		return nil, fmt.Errorf("no launcher %q on the assistant's machine (get lists them)", name)
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	rec, err := s.modelRecord(ctx, "assistant")
	if err != nil {
		return nil, err
	}
	rec.Binding.Provider = "launcher." + name
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_launcher", i18n.Args{"before": before, "after": name})
	},
		apply: func(ctx context.Context) (string, error) {
			if err := s.saveModel(ctx, "assistant", rec); err != nil {
				return "", err
			}
			return "New Persona conversations start with " + name + ".", nil
		}}, nil
}
