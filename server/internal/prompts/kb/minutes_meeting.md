{{with .Pinned}}已确认事实（只读，以此为准）：{{.}}
{{end}}会议：{{if .Title}}「{{.Title}}」，{{end}}{{.Start.Format "2006-01-02"}}（{{.Weekday}}）{{.Start.Format "15:04"}}–{{.End.Format "15:04"}}，录了约 {{.Minutes}} 分钟{{with .Language}}，语言 {{.}}{{end}}
{{with .Meeting}}知识库里同一时段的日历项：「{{.}}」
{{end}}{{with .Attendees}}日历上的参会人：{{join . "、"}}
{{end}}{{with .Location}}日历上的地点：{{.}}
{{end}}
