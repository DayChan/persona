任务：{{.Title}}

派活时的要求：
{{.Prompt}}
{{if .Criteria}}
当前任务的验收标准：
{{range .Criteria}}- {{.}}
{{end}}{{end}}
{{if eq (len .Replies) 1}}Agent 停下时最后的回复{{else}}各个 Agent 停下时最后的回复{{end}}：
{{range .Replies}}
--- {{.Machine}}
{{with .From}}这一轮的发送方：{{.}}
{{end}}{{with .Source}}这一轮的来源：{{.}}（旁路来源，不是用户对本任务的新要求）
{{end}}{{with .Earlier}}它上一轮的回复（同一个会话里，这一轮之前）：
{{.}}

{{end}}{{with .Ask}}这一轮收到的请求（不一定来自用户，不替代原任务）：
{{.}}

它的回复：
{{end}}{{.Text}}
{{end}}{{if or .MergedPR .Build}}
线索（只是提示）：回复里提到了{{if .MergedPR}}已合并的 PR{{end}}{{if and .MergedPR .Build}}、{{end}}{{if .Build}}TestFlight 构建号{{end}}。
{{end}}
