{{- $w := .Now.Weekday -}}
- 现在：{{.Now.Format "2006-01-02"}} 周{{if eq $w 0}}日{{else if eq $w 1}}一{{else if eq $w 2}}二{{else if eq $w 3}}三{{else if eq $w 4}}四{{else if eq $w 5}}五{{else}}六{{end}} {{.Now.Format "15:04"}}（{{.Zone}}）
- 模型档位：{{with .ModelProfile}}{{if .Unreadable}}unavailable (model_profiles_list to retry){{else if .Gone}}{{.ID}}，已被删除（没有 Agent 预设；model_profile_switch 换一个）{{else}}{{ctx (or .Name .ID)}} ({{.ID}}){{with .Tiers}}；Agent 预设（agent_start / task_plan 的 tier）：{{range $i, $t := .}}{{if $i}}；{{end}}{{$t.Name}} = {{ctx $t.Launcher}} · {{if $t.Model}}{{ctx $t.Model}}{{else}}默认模型{{end}}{{with $t.Effort}} · {{.}}{{end}}{{with $t.Description}}（{{ctx .}}）{{end}}{{end}}{{else}}；没有 Agent 预设{{end}}{{end}}{{else}}未设置（model_profiles_list 查看，model_profile_switch 切换）{{end}}
- 在线的电脑：{{range $i, $m := .Up}}{{if $i}}、{{end}}{{$m.Name}}（{{$m.ID}}）{{end}}{{with .Down}}；离线：{{range $i, $m := .}}{{if $i}}、{{end}}{{$m.Name}}（{{$m.ID}}）{{end}}{{end}}
- Agents：需要你 {{len .Needs}} 个，完成未看 {{len .Unread}} 个，工作中 {{len .Working}} 个（详情用 agents_list）
{{with .Needs}}  - 需要你：{{template "assistant/snapshot_agents" .}}
{{end}}{{with .Unread}}  - 完成未看：{{template "assistant/snapshot_agents" .}}
{{end}}{{with .Awaiting}}  - 在问用户、用户看过还没回的（用户问有什么要处理时带上；别替用户回答）：{{template "assistant/snapshot_agents" .}}
{{end}}{{with .Waiting}}  - 在等的（hub 到时会叫醒它们，不用管）：{{template "assistant/snapshot_agents" .}}
{{end}}{{with .Done}}  - 做完的（24 小时内）：{{template "assistant/snapshot_agents" .}}
{{end}}{{with .Muted}}  - 已屏蔽（用户关了它们的通知，上面的数不算它们；别主动提，问到再说）：{{template "assistant/snapshot_agents" .}}
{{end}}{{with .Working}}  - 工作中：{{range $i, $a := first 8 .}}{{if $i}}；{{end}}{{$a.Label}}{{with $a.Ultracode}}〔ultracode={{.}}{{if eq . "on"}}：每个实质任务都会跑多 Agent workflow，等结果时别催{{end}}〕{{end}}{{with $a.Plan}}（{{template "assistant/snapshot_plan" .}}）{{end}}{{end}}{{if gt (len .) 8}} 等 {{len .}} 个{{end}}
{{end}}{{with .Workflows}}  - 后台 workflow 在跑（这些 Agent 这一轮停着是在等结果，不是做完了，结果回来会叫醒它们；别关它们）：{{range $i, $w := first 6 .}}{{if $i}}；{{end}}{{$w.Agent}}（{{$w.Machine}}，session_id={{$w.Session}}）的{{template "assistant/snapshot_workflow" $w}}{{end}}{{if gt (len .) 6}} 等 {{len .}} 个{{end}}
{{end}}{{with .Tasks}}- 从对话开的任务（task_plan；问已有结果看这里，不够用 agent_read 读它的对话；要它新查、新做或改方向才用 agent_send）：
{{range .}}{{$t := .}}  - {{.Title}}（{{.ID}}{{with .Link}}，链接 {{.}}{{end}}）：{{.State}}{{with .Health}}；督办：{{.}}{{if $t.DoneWhen}}，验收标准 {{$t.DoneWhen}} 条{{else}}，没写验收标准{{end}}{{end}}{{with .Machine}}，{{.}}{{end}}{{with .Session}} session_id={{.}}{{end}}{{with .Last}}：{{.}}{{end}}{{with .Doing}}{{if $t.Last}}；{{else}}：{{end}}现在{{.}}{{with $t.DoingFor}}（{{.}}了）{{end}}{{end}}{{with .Subagents}}；子 Agent 在跑：{{join . "、"}}{{end}}{{with .Workflows}}；后台 workflow 在跑（在等它的结果，没做完）：{{range $i, $w := .}}{{if $i}}、{{end}}{{template "assistant/snapshot_workflow" $w}}{{end}}{{end}}{{with .Delivery}}；交付：{{.}}{{end}}{{with .Plan}}；{{template "assistant/snapshot_plan" .}}{{end}}{{with .Reported}}；它最近向你汇报（{{if .Minutes}}{{.Minutes}} 分钟前{{else}}刚刚{{end}}）：{{if eq .Kind "question"}}要你拍板{{else if eq .Kind "blocked"}}卡住了{{else if eq .Kind "done"}}说做完了{{else}}进度{{end}}{{with .Step}}，第 {{.}}{{with $t.Reported.Steps}}/{{.}}{{end}} 步{{end}}：{{.Text}}{{end}}
{{end}}{{end}}{{with .Approvals}}- 等用户点头才能上线的（{{len .}} 件；合到一起一次列给用户批，别一件件问）：{{range $i, $a := .}}{{if $i}}；{{end}}「{{$a.Title}}」（{{$a.ID}}{{with $a.Link}}，链接 {{.}}{{end}}）{{$a.Stage}}，{{$a.Gate}}{{end}}
{{end}}{{with .Plans}}- 等用户点「开始」的计划（主对话里的卡片，用户点了才开工；汇报时要说）：{{range $i, $p := .}}{{if $i}}；{{end}}{{$p.At.Format "01-02 15:04"}} {{join $p.Titles "、"}}{{if gt $p.Tasks (len $p.Titles)}} 等 {{$p.Tasks}} 件{{end}}{{end}}{{if gt $.PlansTotal (len .)}} 等 {{$.PlansTotal}} 张{{end}}
{{end}}{{with .Asked}}- 你用 ask_user 问过、到点没人回、转成待办在等用户拍板的（用户在待办里记了决定会作为新消息发给发问的对话；别再问，也别先按某个选项去做，除非用户已经在对话里直接答了）：{{range $i, $a := .}}{{if $i}}；{{end}}「{{$a.Question}}」（{{$a.TodoID}}，{{$a.At.Format "01-02 15:04"}} 问的）{{end}}
{{end}}{{with .Unaccepted}}- 等用户验收的（已部署的请验收，或任务的结论卡，用户还没回「通过」「OK」）：{{range $i, $a := .}}{{if $i}}；{{end}}「{{$a.Title}}」：{{$a.Verdict}}{{with $a.Changed}}；{{.}}{{end}}{{with $a.Link}}（{{.}}）{{end}}，{{$a.Since.Format "01-02"}} 起{{end}}{{if gt $.UnacceptedTotal (len .)}} 等 {{$.UnacceptedTotal}} 个{{end}}
{{end}}{{if .PhoneOnline}}- iPhone：App 在线，phone_* 工具可用
{{else}}- iPhone：App 不在前台，phone_* 工具会先尝试在后台唤醒 App，失败就请他打开 App
{{end}}{{with .KB}}- 知识库（kb_get 看详情）：
{{indent "  " .}}{{end}}{{with .Chats}}{{if .Total}}- 飞书未读会话 {{.Total}} 个（免打扰的不算；用 lark_chats / lark_messages 看）
{{with .AtMe}}  - @我：{{range $i, $c := first 6 .}}{{if $i}}；{{end}}{{$c.Name}}（{{$c.Unread}} 条）：{{$c.Preview}}{{end}}
{{end}}{{with .Other}}  - 其他：{{range $i, $c := first 8 .}}{{if $i}}、{{end}}{{$c.Name}}（{{$c.Unread}} 条）{{end}}
{{end}}{{else}}- 飞书：没有未读（免打扰的不算）
{{end}}{{end}}{{with .Upcoming}}{{if .Events}}- 飞书日历今天还没结束的会（已经开完的不在这里）：{{range $i, $e := .Events}}{{if $i}}；{{end}}{{$e.Start.Format "15:04"}}–{{$e.End.Format "15:04"}} {{$e.Title}}{{end}}
{{else}}- 飞书日历：今天接下来没有会了（已经开完的不在这里）
{{end}}{{end}}- 已安排的跟进：{{with .Followups}}{{range $i, $f := .}}{{if $i}}；{{end}}{{$f.At.Format "01-02 15:04"}} {{$f.Label}}{{with $f.Every}}（每 {{.}}）{{end}}{{end}}{{else}}没有{{end}}
{{with .Routines}}- 每天的定时任务：{{range $i, $r := .}}{{if $i}}；{{end}}{{$r.Label}}（{{$r.Rule}}）{{if not $r.LastAt.IsZero}}，上次{{$r.LastAt.Format "01-02 15:04"}}{{if eq $r.LastStatus "ok"}} 成功{{else if eq $r.LastStatus "failed"}} 失败{{with $r.LastWhy}}：{{.}}{{end}}{{else if eq $r.LastStatus "skipped"}} 跳过{{with $r.LastWhy}}：{{.}}{{end}}{{end}}{{end}}{{end}}{{if gt $.RoutinesTotal (len .)}} 等 {{$.RoutinesTotal}} 个{{end}}
{{end}}{{with .Missed}}- 没成功的定时任务（昨天 18:00 以来；汇报时要说）：{{range $i, $m := .}}{{if $i}}；{{end}}{{$m.At.Format "01-02 15:04"}} {{$m.Label}}{{if $m.Skipped}}跳过了{{else}}失败了{{end}}{{with $m.Why}}：{{.}}{{end}}{{end}}{{if gt $.MissedTotal (len .)}} 等 {{$.MissedTotal}} 次{{end}}
{{end}}
