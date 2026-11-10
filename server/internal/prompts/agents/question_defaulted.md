Nobody answered by the deadline ({{.Deadline}}). You said this can be undone, so your recommended options were taken by default and recorded as decisions on to-do {{.TodoID}}:
{{range .Choices}}- {{.Question}} → {{.Choice}}
{{end}}Go ahead with them. Tell the user in a line which choices were taken by default at the deadline; they can still change them.
