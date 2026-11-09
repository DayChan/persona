{{if .Files}}用户给的文件{{if .Copied}}（已从助手那台电脑复制到这台）{{end}}：
{{range .Files}}- {{.}}
{{end}}{{end}}{{if .Missing}}这些附件没能带过来，需要的话请用户直接发给你：
{{range .Missing}}- {{.}}
{{end}}{{end}}{{if .Pictures}}<attached_images>
{{range .Pictures}}{{.}}
path: {{.}}
{{end}}</attached_images>{{end}}
