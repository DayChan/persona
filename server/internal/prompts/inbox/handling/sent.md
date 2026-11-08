{{.Owner}}确认了，已经发出{{if .Edited}}（{{.Owner}}改过文字，发出去的是下面这段）{{end}}：
{{.Text}}

{{if .Handoff}}对方之后的回复不会转到这里，不用再交建议。{{else}}现在等对方回。调 inbox_suggest 只给 recap 更新总结，不用再出建议；对方回了会发到这里。{{end}}
