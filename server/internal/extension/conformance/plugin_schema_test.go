package conformance_test

// The plugin protocol's schemas (schema/extension/connector.schema.json, channel.schema.json, kb.schema.json, todos.schema.json, models.schema.json) describe what the server's
// own code sends and reads: every call and event they list is held to the Go types the hub and connector/serve put on
// the wire, field by field, and every connector method the server knows is in them. The schema is what an extension
// is written from; this keeps it from drifting from the code.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr/storage"
	"github.com/nwverse/persona/server/internal/kbwire"
)

const schemaDir = "../../../../schema/extension"

// goTypes are the Go types each definition a call names is on the wire. Params under the plugin protocol carry the
// contribution besides (extension.Targeted adds it).
var goTypes = map[string]any{
	"Empty": struct{}{},

	"ConnectorParams":               extension.Target{},
	"ConnectorStatus":               connector.Status{},
	"ConnectorManifest":             serve.Manifest{},
	"ConnectorCatalog":              connector.CatalogResult{},
	"ConnectorCatalogCallParams":    connector.CatalogQuery{},
	"ConnectorTools":                serve.ToolsResult{},
	"ConnectorCallParams":           serve.CallParams{},
	"ConnectorCallResult":           serve.CallResult{},
	"ConnectorDescribeResult":       serve.DescribeResult{},
	"ConnectorDoParams":             serve.DoParams{},
	"ConnectorRecallParams":         serve.RecallParams{},
	"ConnectorRecallResult":         serve.RecallResult{},
	"ConnectorThreadParams":         serve.ThreadParams{},
	"ConnectorThreadPage":           connector.ThreadPage{},
	"ConnectorMediaParams":          serve.MediaParams{},
	"ConnectorMedia":                connector.Media{},
	"ConnectorCollectResult":        serve.CollectResult{},
	"ConnectorInboxThing":           connector.InboxThing{},
	"ConnectorInboxReply":           serve.ReplyParams{},
	"ConnectorSettleParams":         serve.SettleParams{},
	"ConnectorSettled":              serve.SettleResult{},
	"ConnectorChangesParams":        serve.ChangesParams{},
	"ConnectorChangesResult":        serve.ChangesResult{},
	"ConnectorHistoryChats":         serve.HistoryChatsResult{},
	"ConnectorHistoryParams":        serve.HistoryParams{},
	"ConnectorHistoryPage":          connectorwire.HistoryPage{},
	"ConnectorHistoryAnchorsParams": serve.HistoryAnchorsParams{},
	"ConnectorHistoryAnchors":       connectorwire.HistoryAnchors{},
	"ConnectorItems":                serve.ItemsResult{},
	"ConnectorDocTitleParams":       serve.DocTitleParams{},
	"ConnectorDocTitle":             serve.DocTitleResult{},
	"ConnectorSnapshotParams":       serve.SnapshotParams{},
	"ConnectorSnapshot":             connector.SnapshotData{},
	"ConnectorGroupToolParams":      connector.GroupToolCall{},
	"ConnectorGroupAppendParams":    connector.GroupAppend{},
	"ConnectorGroupFallbackParams":  connector.GroupFallback{},
	"ConnectorGroupFallback":        connector.GroupFallbackResult{},
	"ConnectorLoginParams":          serve.LoginParams{},
	"ConnectorPrompt":               connector.Prompt{},
	"ConnectorOptions":              serve.OptionsResult{},
	"ConnectorOptionsParams":        serve.OptionsParams{},
	"ConnectorMachineParams":        serve.MachineParams{},
	"ConnectorAuthSource":           serve.AuthSource{},
	"ConnectorAuth":                 serve.AuthView{},
	"ConnectorSecret":               serve.SecretData{},
	"ConnectorSecretSaveParams":     serve.SecretData{},
	"ConnectorPostsPutParams":       serve.PostsPutParams{},
	"ConnectorPostsPut":             serve.PostsPutResult{},
	"ConnectorPostsCommentsParams":  serve.PostsCommentsParams{},
	"ConnectorPostsPendingParams":   serve.PostsPendingParams{},
	"ConnectorPostsPending":         serve.PostsPendingResult{},
	"ConnectorArchiveChatsParams":   serve.ArchiveChatsParams{},
	"ConnectorArchivedChats":        serve.ArchiveChatsResult{},
	"ConnectorGhTokenParams":        serve.GhTokenParams{},
	"ConnectorGhToken":              serve.GhTokenResult{},
	"ConnectorChangedEvent":         serve.ChangedEvent{},
	"ConnectorInitParams":           serve.InitParams{},
	"ConnectorSentRecordParams":     connector.SentRecordParams{},

	"ChannelReceiveParams": channel.ReceiveParams{},
	"ChannelStatusParams":  channel.StatusParams{},
	"ChannelSendParams":    channel.SendParams{},
	"ChannelSendResult":    channel.SendResult{},
	"ChannelTypingParams":  channel.TypingParams{},
	"ChannelEditParams":    channel.EditParams{},

	"KBListParams":     assistantwire.KBListParams{},
	"KBListResult":     assistantwire.KBListResult{},
	"KBGetParams":      assistantwire.KBGetParams{},
	"KBGetResult":      assistantwire.KBGetResult{},
	"KBDayParams":      assistantwire.KBDayParams{},
	"KBDayResult":      assistantwire.KBDayResult{},
	"KBSearchParams":   assistantwire.KBSearchParams{},
	"KBSearchResult":   assistantwire.KBSearchResult{},
	"KBUpdateParams":   assistantwire.KBUpdateParams{},
	"KBUpdateResult":   assistantwire.KBUpdateResult{},
	"KBRecentParams":   assistantwire.KBRecentParams{},
	"KBRecentResult":   assistantwire.KBRecentResult{},
	"KBOwnerParams":    struct{}{},
	"KBOwner":          assistantwire.KBOwner{},
	"KBChangedEvent":   assistantwire.KBChanged{},
	"KBForgottenEvent": assistantwire.KBForgottenEvent{},

	"AssistantConversationsParams": assistantwire.AssistantConversationsParams{},
	"AssistantConversationsResult": assistantwire.AssistantConversationsResult{},
	"AssistantMessagesParams":      assistantwire.AssistantMessagesParams{},
	"AssistantMessagesResult":      assistantwire.AssistantMessagesResult{},

	"ModelCompleteParams": assistantwire.ModelCompleteParams{},
	"ModelCompleteResult": assistantwire.ModelCompleteResult{},
	"ModelPause":          assistantwire.ModelPause{},
	"ModelInvalidOutput":  assistantwire.ModelInvalidOutput{},

	"TodoChangedEvent":  assistantwire.TodoChangedEvent{},
	"TodoDecisionEvent": assistantwire.TodoDecisionEvent{},

	// Storage params are the actual manager-engine decoder types. Dynamic result
	// objects are validated against these wire DTOs before committing/acknowledging.
	"StorageEmptyParams":       struct{}{},
	"StorageRequestResult":     wire.StorageRequestResult{},
	"StorageKeyParams":         storage.KeyParams{},
	"StorageEntry":             storage.Entry{},
	"StorageListParams":        storage.ListParams{},
	"StorageListResult":        wire.StorageListResult{},
	"StorageTransactionParams": storage.Transaction{},
	"StorageTransactionResult": wire.StorageTransactionResult{},
	"StorageQueueListParams":   storage.QueueList{},
	"StorageQueueListResult":   wire.StorageQueueListResult{},
	"StorageEnqueueParams":     storage.Enqueue{},
	"StorageJobResult":         wire.StorageJobResult{},
	"StorageClaimParams":       storage.Claim{},
	"StorageClaimResult":       wire.StorageClaimResult{},
	"StorageSettleParams":      storage.Settle{},
	"StorageDeleteResult":      wire.StorageDeleteResult{},
	"StorageRetryParams":       storage.Retry{},
	"StorageBlobCreateParams":  storage.CreateBlob{},
	"StorageBlobCreateResult":  wire.StorageBlobCreateResult{},
	"StorageBlobWriteParams":   storage.WriteBlob{},
	"StorageBlobWriteResult":   wire.StorageBlobWriteResult{},
	"StorageBlobMutateParams":  storage.MutateBlob{},
	"StorageBlobFinishResult":  wire.StorageBlobFinishResult{},
	"StorageBlobReadParams":    storage.ReadBlob{},
	"StorageBlobReadResult":    wire.StorageBlobReadResult{},
}

// The calls of the server the schemas must list: the connector methods connector/serve answers and calls (the bare
// opening's init aside), the channel methods, the knowledge-base methods the hub forwards for extensions, and its
// events.
var serverCalls = []string{
	serve.MManifest, serve.MStatus, serve.MLoginStep, serve.MLogout, serve.MChanges, serve.MCollect, serve.MReply,
	serve.MCatalog, serve.MCatalogCall, serve.MTools, serve.MCall, serve.MActions, serve.MDescribe, serve.MDo, serve.MRecall, serve.MThread, serve.MMedia, serve.MItems,
	serve.MDocTitle, serve.MArchiveMedia, serve.MSnapshot, serve.MGroupTool, serve.MGroupAppend, serve.MGroupFallback,
	serve.MOptionsGet, serve.MOptionsSet, serve.MSettle, serve.MMachineOnline, serve.MAuthGet, serve.MAuthSet,
	serve.MSecretsLoad, serve.MSecretsSave, serve.MSecretsDelete, serve.MPostsPut, serve.MPostsComments,
	serve.MPostsPending, serve.MArchiveChats, serve.MGhToken, serve.EChanged, serve.MHistoryChats, serve.MHistory,
	serve.MHistoryAnchors,
}

// sent.record is the hub's own (extmgr answers it for a connector that declares sends_as_user), outside the
// connector protocol's namespace.
var sentCalls = []string{connector.MethodSentRecord}

var channelCalls = []string{channel.MReceive, channel.MStatus, channel.MSend, channel.MTyping, channel.MEdit}

var kbCalls = []string{assistantwire.MethodKBList, assistantwire.MethodKBGet, assistantwire.MethodKBDay,
	assistantwire.MethodKBSearch, assistantwire.MethodKBUpdate, assistantwire.MethodKBOwner, assistantwire.MethodKBRecent,
	"event." + assistantwire.EventKBChanged, "event." + assistantwire.EventKBForgotten}

// The model call the hub forwards for extensions granted models (models.schema.json).
var modelCalls = []string{assistantwire.MethodModelComplete}

// The assistant's conversations the hub forwards for extensions granted assistant.read (assistant.schema.json).
var assistantCalls = []string{assistantwire.MethodAssistantConversations, assistantwire.MethodAssistantMessages}

// The to-do events the hub tells the extensions granted todos.read (todos.schema.json).
var todoCalls = []string{"event." + assistantwire.EventTodoChanged, "event." + assistantwire.EventTodoDecision}

var storageCalls = []string{"storage.request", "storage.get", "storage.list", "storage.transaction",
	"storage.queue.list", "storage.queue.enqueue", "storage.queue.claim", "storage.queue.ack", "storage.queue.forget", "storage.queue.retry",
	"storage.blob.create", "storage.blob.write", "storage.blob.finish", "storage.blob.delete", "storage.blob.read"}

func TestPluginSchemasListTheServersCalls(t *testing.T) {
	var want []string
	for _, m := range serverCalls {
		want = append(want, serve.InPlugin(m))
	}
	for _, m := range channelCalls {
		want = append(want, channel.InPlugin(m))
	}
	want = append(want, kbCalls...)
	want = append(want, todoCalls...)
	want = append(want, modelCalls...)
	want = append(want, assistantCalls...)
	want = append(want, sentCalls...)
	want = append(want, storageCalls...)
	for _, name := range want {
		if _, ok := wire.Calls[name]; !ok {
			t.Errorf("%s: the server has it, schema/extension does not list it (x-methods / x-events)", name)
		}
	}
	for name := range wire.Calls {
		if (strings.HasPrefix(name, serve.Namespace+".") || strings.HasPrefix(name, channel.Namespace+".") ||
			strings.HasPrefix(name, "kb.") || strings.HasPrefix(name, "event.kb.") || strings.HasPrefix(name, "event.todo.") || strings.HasPrefix(name, "sent.") || strings.HasPrefix(name, "model.") || strings.HasPrefix(name, "assistant.") || strings.HasPrefix(name, "storage.")) &&
			!slices.Contains(want, name) {
			t.Errorf("%s: schema/extension lists it, the server has no such call", name)
		}
	}
	if c := wire.Calls[serve.InPlugin(serve.EChanged)]; !c.Event || c.From != "program" {
		t.Errorf("connector.changed: %+v, want an event from the program", c)
	}
}

// The capabilities the schema lists (what an extension's author declares) are the ones the hub knows, and recall is
// spelled as the hub reads it.
func TestConnectorSchemaCapabilitiesAreTheHubs(t *testing.T) {
	for _, c := range wire.ConnectorCapabilities {
		if !connector.Known(connector.Capability(c)) {
			t.Errorf("the schema lists the capability %q, this hub does not know it", c)
		}
	}
	if wire.ConnectorCapabilityRecall != string(connector.CapRecall) || !slices.Contains(wire.ConnectorCapabilities, string(connector.CapRecall)) {
		t.Errorf("recall: the schema says %q, the hub %q", wire.ConnectorCapabilityRecall, connector.CapRecall)
	}
}

func TestPluginSchemasFollowTheGoTypes(t *testing.T) {
	s := loadSchemas(t)
	defs := map[string]bool{}
	for _, c := range wire.Calls {
		defs[c.Params] = true
		if c.Result != "" {
			defs[c.Result] = true
		}
	}
	defs["ConnectorInitParams"] = true
	names := make([]string, 0, len(defs))
	for d := range defs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		v, ok := goTypes[d]
		if !ok {
			t.Errorf("%s: no Go type to hold it to (add it to goTypes)", d)
			continue
		}
		s.compare(t, d, s.def(t, d), reflect.TypeOf(v), map[string]bool{})
	}
}

// What the program fixtures (connector/testdata/wire/<program>) answer holds to the schemas.
func TestConnectorFixturesHoldToTheSchemas(t *testing.T) {
	byFile := map[string]string{
		"actions.describe": "connector.actions.describe", "actions.do": "connector.actions.do",
		"actions.list": "connector.actions.list", "archive.changes": "connector.archive.changes",
		"inbox.collect": "connector.inbox.collect", "login.step": "connector.login.step",
		"login.step.done": "connector.login.step", "login.step.refused": "connector.login.step",
		"status": "connector.status", "thread.page": "connector.thread.page", "tools.call": "connector.tools.call",
		"catalog.list": "connector.catalog.list", "tools.list": "connector.tools.list", "history.chats": "connector.history.chats",
		"archive.history": "connector.archive.history", "archive.history.start": "connector.archive.history",
		"archive.history.unreadable": "connector.archive.history", "archive.history.paced": "connector.archive.history",
		"history.anchors": "connector.history.anchors",
	}
	dirs, _ := filepath.Glob("../../connector/testdata/wire/*")
	if len(dirs) == 0 {
		t.Fatal("no connector wire fixtures")
	}
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			name := strings.TrimSuffix(filepath.Base(f), ".json")
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var err2 error
			switch name {
			case "persona-plugin":
				err2 = wire.ValidateJSON("Manifest", raw)
			case "initialize":
				err2 = wire.ValidateJSON("InitializeResult", raw)
			case "changed":
				err2 = wire.ValidateParams("connector.changed", raw)
			default:
				call, ok := byFile[name]
				if !ok {
					t.Errorf("%s: which call answers it? (add it to byFile)", f)
					continue
				}
				err2 = wire.ValidateResult(call, raw)
			}
			if err2 != nil {
				t.Errorf("%s: %v", f, err2)
			}
		}
	}
}

type schemas map[string]map[string]any // file → its JSON

func loadSchemas(t *testing.T) schemas {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(schemaDir, "*.schema.json"))
	s := schemas{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		s[filepath.Base(f)] = doc
	}
	return s
}

func (s schemas) def(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, doc := range s {
		if d, ok := doc["$defs"].(map[string]any)[name].(map[string]any); ok {
			return d
		}
	}
	t.Fatalf("no definition %s in schema/extension", name)
	return nil
}

func (s schemas) ref(t *testing.T, ref string) (string, map[string]any) {
	t.Helper()
	_, name, _ := strings.Cut(ref, "#/$defs/")
	return name, s.def(t, name)
}

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage{})
)

// compare holds the schema node at to the Go type g: the same JSON kind, and for an object the same fields.
func (s schemas) compare(t *testing.T, at string, node map[string]any, g reflect.Type, seen map[string]bool) {
	t.Helper()
	for g.Kind() == reflect.Pointer {
		g = g.Elem()
	}
	if ref, ok := node["$ref"].(string); ok {
		name, target := s.ref(t, ref)
		if target["type"] == "string" {
			s.compare(t, at, target, g, seen)
			return
		}
		key := name + "|" + g.String()
		if seen[key] {
			return
		}
		seen[key] = true
		s.compare(t, at+"→"+name, target, g, seen)
		return
	}
	kind := func(want ...reflect.Kind) {
		t.Helper()
		if !slices.Contains(want, g.Kind()) {
			t.Errorf("%s: the schema says %v, Go has %s", at, node["type"], g)
		}
	}
	switch node["type"] {
	case nil:
		if g != rawType && g.Kind() != reflect.Interface {
			t.Errorf("%s: the schema says any JSON, Go has %s", at, g)
		}
	case "string":
		switch {
		case node["format"] == "date-time":
			if g != timeType {
				t.Errorf("%s: the schema says a date-time, Go has %s", at, g)
			}
		case g.Kind() == reflect.Slice && g.Elem().Kind() == reflect.Uint8:
			if !strings.Contains(strings.ToLower(asString(node["description"])), "base64") {
				t.Errorf("%s: Go writes bytes as base64, the schema does not say so", at)
			}
		default:
			kind(reflect.String)
		}
	case "integer":
		kind(reflect.Int, reflect.Int32, reflect.Int64)
	case "number":
		kind(reflect.Float64, reflect.Float32)
	case "boolean":
		kind(reflect.Bool)
	case "array":
		kind(reflect.Slice)
		if g.Kind() == reflect.Slice {
			items, _ := node["items"].(map[string]any)
			s.compare(t, at+"[]", items, g.Elem(), seen)
		}
	case "object":
		props, _ := node["properties"].(map[string]any)
		if props == nil && node["additionalProperties"] != false {
			kind(reflect.Map)
			if ap, ok := node["additionalProperties"].(map[string]any); ok && g.Kind() == reflect.Map {
				s.compare(t, at+"{}", ap, g.Elem(), seen)
			}
			return
		}
		kind(reflect.Struct)
		if g.Kind() != reflect.Struct {
			return
		}
		fields := map[string]goField{}
		jsonFields(g, fields)
		required := map[string]bool{}
		for _, r := range asSlice(node["required"]) {
			required[r.(string)] = true
		}
		for name, p := range props {
			f, ok := fields[name]
			if !ok {
				// Under the plugin protocol the hub adds the contribution to the params (extension.Targeted).
				if name != "contribution" {
					t.Errorf("%s.%s: in the schema, not in Go's %s", at, name, g)
				}
				continue
			}
			// The contribution is left out only under the bare opening (protocol 2 on its own), which has none.
			if required[name] && f.omitempty && name != "contribution" {
				t.Errorf("%s.%s: required in the schema, Go's %s leaves it out when empty", at, name, g)
			}
			s.compare(t, at+"."+name, p.(map[string]any), f.typ, seen)
		}
		for name := range fields {
			if _, ok := props[name]; !ok {
				t.Errorf("%s.%s: Go's %s sends it, the schema does not say it", at, name, g)
			}
		}
	default:
		t.Errorf("%s: type %v", at, node["type"])
	}
}

type goField struct {
	typ       reflect.Type
	omitempty bool
}

// jsonFields are what encoding/json writes of struct g, by name (embedded structs' fields inline).
func jsonFields(g reflect.Type, out map[string]goField) {
	for i := range g.NumField() {
		f := g.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() && !f.Anonymous {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			jsonFields(f.Type, out)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = goField{f.Type, strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")}
	}
}

func asString(v any) string { s, _ := v.(string); return s }
func asSlice(v any) []any   { s, _ := v.([]any); return s }

// The hub refuses an empty list for any of its points ("contributes.<point>: empty"): the manifest schema's list for
// each of them holds at least one item, or an empty one passes the SDK's and a catalog's validation, then fails at
// install.
func TestManifestSchemaContributesListsAreNotEmpty(t *testing.T) {
	props := loadSchemas(t).def(t, "Contributes")["properties"].(map[string]any)
	for name := range extension.Points {
		list, ok := props[name].(map[string]any)
		if !ok {
			t.Errorf("contributes.%s: the hub has the point, manifest.schema.json Contributes does not list it", name)
			continue
		}
		if list["minItems"] != float64(1) {
			t.Errorf("contributes.%s: minItems %v, want 1 (the hub refuses an empty list)", name, list["minItems"])
		}
	}
}

// The manifest schema's events.subscribe enum is the events the hub has (extension.EventTopics): an event added to one
// and not the other makes a manifest that the server accepts fail the schema, or the reverse.
func TestManifestSchemaListsEveryEventTopic(t *testing.T) {
	s := loadSchemas(t)
	def := s.def(t, "EventsItem")
	items := def["properties"].(map[string]any)["subscribe"].(map[string]any)["items"].(map[string]any)
	var got []string
	for _, v := range asSlice(items["enum"]) {
		got = append(got, v.(string))
	}
	var want []string
	for topic := range extension.EventTopics {
		want = append(want, topic)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("manifest.schema.json EventsItem.subscribe enum %v, extension.EventTopics %v", got, want)
	}
}

// The manifest schema's contributes lists exactly the hub's built-in points (extension.Points), and each point's item
// refuses unknown fields as the hub's strict decode does: an open item lets a typo pass the SDK's and a catalog's
// validation, then fail at install.
func TestManifestSchemaContributesAreTheHubsPoints(t *testing.T) {
	s := loadSchemas(t)
	props := s.def(t, "Contributes")["properties"].(map[string]any)
	for name := range extension.Points {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: the hub has the point, manifest.schema.json Contributes does not list it", name)
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := extension.Points[name]; !ok {
			t.Errorf("%s: manifest.schema.json Contributes lists it, the hub has no such point", name)
			continue
		}
		items, _ := props[name].(map[string]any)["items"].(map[string]any)
		ref := asString(items["$ref"])
		if ref == "" {
			t.Errorf("contributes.%s: items is not a $ref to a definition", name)
			continue
		}
		def, item := s.ref(t, ref)
		if item["additionalProperties"] != false {
			t.Errorf("contributes.%s: %s does not refuse unknown fields (additionalProperties: false), the hub does", name, def)
		}
	}
}

// The connector schema's enums for what the knowledge base's selection matches on are the hub's own lists: an item's
// signals are kbwire.Signals (in that order) and a history chat's kinds the connectorwire.HistoryKind* ones, so a
// value added on one side only makes the SDK and the hub's conformance disagree.
func TestConnectorSchemaSelectionEnumsAreTheHubsLists(t *testing.T) {
	s := loadSchemas(t)
	enum := func(def, field string, items bool) []string {
		node := s.def(t, def)["properties"].(map[string]any)[field].(map[string]any)
		if items {
			node = node["items"].(map[string]any)
		}
		var out []string
		for _, v := range asSlice(node["enum"]) {
			out = append(out, v.(string))
		}
		return out
	}
	if got := enum("ConnectorKBItem", "signals", true); !slices.Equal(got, kbwire.Signals) {
		t.Errorf("connector.schema.json ConnectorKBItem.signals enum %v, kbwire.Signals %v", got, kbwire.Signals)
	}
	if got := enum("ConnectorHistoryChat", "kind", false); !slices.Equal(got, connectorwire.HistoryKinds) {
		t.Errorf("connector.schema.json ConnectorHistoryChat.kind enum %v, connectorwire.HistoryKinds %v", got, connectorwire.HistoryKinds)
	}
}
