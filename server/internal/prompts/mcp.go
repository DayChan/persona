package prompts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
)

// MCPTool is one MCP tool as the model sees it: what it is for and its arguments (with their descriptions).
type MCPTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // compact
}

// mcp/<scene>/<tool>.md: the description (one paragraph), an empty line, then the input schema in a ```json block.
// Not templates: read as they are, on first use (as the templates are).
var mcpTools = sync.OnceValue(loadMCP)

const schemaFence = "\n\n```json\n"

func loadMCP() map[string]MCPTool {
	out := map[string]MCPTool{}
	err := fs.WalkDir(files, "mcp", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		desc, rest, ok := strings.Cut(string(raw), schemaFence)
		schema, ok2 := strings.CutSuffix(rest, "\n```\n")
		if !ok || !ok2 || desc == "" {
			return fmt.Errorf("%s: want the description, an empty line and a ```json block with the input schema", p)
		}
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(schema)); err != nil {
			return fmt.Errorf("%s: input schema: %w", p, err)
		}
		name := strings.TrimSuffix(path.Base(p), ".md")
		if _, dup := out[name]; dup {
			return fmt.Errorf("%s: tool %s defined twice", p, name)
		}
		out[name] = MCPTool{Name: name, Description: desc, InputSchema: b.Bytes()}
		return nil
	})
	if err != nil {
		panic("prompts: " + err.Error())
	}
	return out
}

// Tool is the MCP tool named name (mcp/<scene>/<name>.md); an unknown name is a bug and panics.
func Tool(name string) MCPTool {
	t, ok := mcpTools()[name]
	if !ok {
		panic("prompts: no MCP tool " + name)
	}
	return t
}

// ToolNames lists every MCP tool defined here (for tests).
func ToolNames() []string {
	out := make([]string, 0, len(mcpTools()))
	for n := range mcpTools() {
		out = append(out, n)
	}
	return out
}
