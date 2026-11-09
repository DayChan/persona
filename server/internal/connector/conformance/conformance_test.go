package conformance

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
)

var snake = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Every field that crosses the wire has an explicit snake_case name (a third party writes these keys by hand).
func TestWireTypesAreTagged(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(ty reflect.Type, path string) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || seen[ty] || ty.PkgPath() == "time" {
			return
		}
		seen[ty] = true
		for i := range ty.NumField() {
			f := ty.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := splitTag(f.Tag.Get("json"))
			if name == "" || name == "-" || !snake.MatchString(name) {
				t.Errorf("%s.%s has no snake_case json name (%q)", path, f.Name, f.Tag.Get("json"))
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	for _, v := range []any{connector.CatalogResult{}, connector.CatalogQuery{}, serve.Manifest{}, serve.InitParams{}, serve.LoginParams{}, serve.ChangesParams{}, serve.ChangesResult{},
		serve.CollectResult{}, serve.ReplyParams{}, serve.ToolsResult{}, serve.CallParams{}, serve.CallResult{}, serve.DescribeResult{}, serve.DoParams{}, serve.ThreadParams{}, serve.MediaParams{}, connector.ThreadPage{}, connector.Media{}, serve.SecretData{},
		serve.ChangedEvent{}, connector.Status{}, connector.Prompt{}, connector.InboxThing{}, connector.ChatBatch{}, kb.SourceMessage{},
		serve.HistoryChatsResult{}, serve.HistoryParams{}, serve.HistoryAnchorsParams{}, connectorwire.HistoryPage{}, connectorwire.HistoryAnchors{}} {
		walk(reflect.TypeOf(v), reflect.TypeOf(v).String())
	}
}

func splitTag(tag string) (name, rest string, ok bool) {
	for i := range len(tag) {
		if tag[i] == ',' {
			return tag[:i], tag[i+1:], true
		}
	}
	return tag, "", false
}
