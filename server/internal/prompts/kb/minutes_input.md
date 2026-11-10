{{template "kb/minutes_meeting" .}}{{if gt .Parts 1}}这是第 {{.Part}} / {{.Parts}} 段。
{{end}}
转写：
{{.Transcript}}
