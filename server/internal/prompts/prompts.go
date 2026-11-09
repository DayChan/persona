// Package prompts holds every prompt personad writes for a model: one file per prompt under a directory per scene
// (assistant/, bot/, lark/, inbox/, kb/, tools/, shared/, voice/, wechat/, todo/), Go text/template syntax, embedded in the
// binary. Callers pass data (machines, the owner's name, collected context…) to a typed render function here; no
// prompt text is put together with fmt or + anywhere else. See docs/assistant/prompts.md.
//
// File conventions:
//   - A template's name is its path without ".md" ("inbox/triage"); one prompt includes another with
//     {{template "shared/user_rules" .}}.
//   - The file's last newline is not part of the prompt (editors add one). A prompt that ends with a newline
//     has an empty last line.
//   - mcp/<scene>/<tool>.md are the MCP tools' descriptions and input schemas, read as they are (mcp.go).
package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"
)

//go:embed agents assistant bot channel inbox kb tools shared mcp voice todo
var files embed.FS

// templates is every template parsed so far, in one text/template set so {{template}} finds the ones it includes.
// Each is parsed the first time it is rendered, with the templates it includes (#2475): a process renders a few of
// them, and all of them parsed hold about 1.5 MB, the voice ones alone nearly half. A short-lived process that writes
// no prompt (a hook, #1308) parses none. Names parses them all, so a broken file still fails the tests.
var templates = newTemplateSet()

type templateSet struct {
	// mu: one parse at a time, and a template is looked up only once its includes are parsed too. Rendering runs
	// beside a parse (text/template guards its set).
	mu   sync.Mutex
	root *template.Template
}

func newTemplateSet() *templateSet {
	return &templateSet{root: template.New("").Option("missingkey=error").Funcs(funcs)}
}

// lookup is the template name, parsed first, with the templates it includes, if it has not been yet.
func (s *templateSet) lookup(name string) (*template.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.parse(name); err != nil {
		return nil, err
	}
	return s.root.Lookup(name), nil
}

// parse adds name's file to the set unless it is there, then each template it includes. Callers hold mu.
func (s *templateSet) parse(name string) error {
	if s.root.Lookup(name) != nil {
		return nil
	}
	raw, err := files.ReadFile(name + ".md")
	if err != nil {
		return err
	}
	t, err := s.root.New(name).Parse(strings.TrimSuffix(string(raw), "\n"))
	if err != nil {
		return err
	}
	for _, include := range includes(t.Root, nil) {
		if err := s.parse(include); err != nil {
			return fmt.Errorf("%s includes %s: %w", name, include, err)
		}
	}
	return nil
}

// parseAll adds every template file to the set.
func (s *templateSet) parseAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") || strings.HasPrefix(path, "mcp/") {
			return err // mcp/ holds tool definitions, not templates (mcp.go)
		}
		return s.parse(strings.TrimSuffix(path, ".md"))
	})
}

// includes appends to out the names of the templates n includes ({{template "name"}}; a name is always a constant).
func includes(n parse.Node, out []string) []string {
	switch n := n.(type) {
	case *parse.ListNode:
		if n != nil {
			for _, c := range n.Nodes {
				out = includes(c, out)
			}
		}
	case *parse.IfNode:
		out = includes(n.ElseList, includes(n.List, out))
	case *parse.RangeNode:
		out = includes(n.ElseList, includes(n.List, out))
	case *parse.WithNode:
		out = includes(n.ElseList, includes(n.List, out))
	case *parse.TemplateNode:
		out = append(out, n.Name)
	}
	return out
}

// render executes one template. The templates are fixed and every one has a golden test, so a failure is a bug in
// this package: it panics rather than hand a model half a prompt.
func render(name string, data any) string {
	return templates.render(name, data)
}

func (s *templateSet) render(name string, data any) string {
	var b strings.Builder
	t, err := s.lookup(name)
	if err == nil {
		err = t.Execute(&b, data)
	}
	if err != nil {
		panic(fmt.Sprintf("prompts: %s: %v", name, err))
	}
	return b.String()
}

// Names lists every template (for tests), parsing all of them.
func Names() []string {
	if err := templates.parseAll(); err != nil {
		panic(fmt.Sprintf("prompts: %v", err))
	}
	var out []string
	for _, t := range templates.root.Templates() {
		if t.Name() != "" {
			out = append(out, t.Name())
		}
	}
	return out
}

var funcs = template.FuncMap{
	// attr escapes text for an XML attribute.
	"attr": func(s string) string {
		return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
	},
	"join": strings.Join,
	// ctx is text for inside a context block, unable to close it (ContextBody).
	"ctx": ContextBody,
	// output is an agent's words for inside an <agent_output> block, unable to open or close one (AgentOutput).
	"output": AgentOutput,
	// indent puts by before every line and ends with one newline.
	"indent": func(by, s string) string {
		lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
		for i, l := range lines {
			lines[i] = by + l
		}
		return strings.Join(lines, "\n") + "\n"
	},
	// chomp drops trailing newlines (indent's, where the template goes on on the same line).
	"chomp": func(s string) string { return strings.TrimRight(s, "\n") },
	// inc is i+1 (a list's numbers from a range's index).
	"inc": func(i int) int { return i + 1 },
	// blank: only whitespace.
	"blank": func(s string) bool { return strings.TrimSpace(s) == "" },
	// first is at most n elements of a slice.
	"first": func(n int, list any) any {
		v := reflect.ValueOf(list)
		if v.Len() > n {
			return v.Slice(0, n).Interface()
		}
		return list
	},
}
