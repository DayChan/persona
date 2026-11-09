package proto

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/screen"
)

type answerFixture struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       []string `json:"source"`
	FromTest     string   `json:"from_test"`
	Derived      bool     `json:"derived"`
	DesignChange bool     `json:"design_change"`
	Input        struct {
		Profile     string          `json:"profile"`
		Interaction json.RawMessage `json:"interaction"`
		Entry       string          `json:"entry"`
		Kind        string          `json:"kind"`
		Locked      bool            `json:"locked"`
	} `json:"input"`
	Expected struct {
		Allowed    bool            `json:"allowed"`
		Resolution json.RawMessage `json:"resolution"`
		Actions    []struct {
			ID             string `json:"id"`
			Mode           string `json:"mode"`
			Authentication string `json:"authentication"`
			RequiresUnlock *bool  `json:"requires_unlock"`
			AvailableNow   *bool  `json:"available_now"`
		} `json:"actions"`
	} `json:"expected"`
}

func answerFixtureRoot(t *testing.T) string {
	t.Helper()
	// go test runs in the package directory; runtime.Caller is not usable under -trimpath.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "../../.."))
}

func answerFixtures(t *testing.T, domain string) []answerFixture {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(answerFixtureRoot(t), "spec/fixtures", domain, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixture files: %v", err)
	}
	var all []answerFixture
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			SchemaVersion int             `json:"schema_version"`
			Cases         []answerFixture `json:"cases"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if doc.SchemaVersion != 1 || len(doc.Cases) == 0 {
			t.Fatalf("invalid fixture document: %s", file)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, bytes.TrimSpace(data), "", "  "); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(append(pretty.Bytes(), '\n'), data) {
			t.Fatalf("expected two-space JSON: %s", file)
		}
		all = append(all, doc.Cases...)
	}
	return all
}

func TestResolutionFixtures(t *testing.T) {
	names := map[string]bool{}
	policies := map[string]int{}
	for _, domain := range []string{"answer", "approval"} {
		for _, c := range answerFixtures(t, domain) {
			t.Run(c.Name, func(t *testing.T) {
				if !regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`).MatchString(c.Name) {
					t.Fatal("name must be snake_case")
				}
				if names[c.Name] {
					t.Fatal("duplicate fixture name")
				}
				names[c.Name] = true
				if c.Description == "" || len(c.Source) == 0 {
					t.Fatal("missing provenance")
				}
				flags := 0
				if c.Derived {
					flags++
				}
				if c.DesignChange {
					flags++
				}
				if c.FromTest != "" {
					flags++
				}
				if flags != 1 {
					t.Fatal("exactly one provenance marker is required")
				}
				if (c.Input.Profile == "unified") != c.DesignChange {
					t.Fatal("design changes must be explicit")
				}
				if domain == "approval" {
					key := c.Input.Profile + "/" + c.Input.Entry + "/" + c.Input.Kind
					if c.Input.Locked {
						key += "/locked"
					} else {
						key += "/unlocked"
					}
					policies[key]++
					if len(c.Expected.Actions) == 0 {
						t.Fatal("missing policy actions")
					}
					ids := map[string]bool{}
					for _, a := range c.Expected.Actions {
						if ids[a.ID] {
							t.Fatal("duplicate action")
						}
						ids[a.ID] = true
						if c.Input.Profile == "unified" && c.Input.Entry != "conversation" && a.Mode != "" && a.Mode != "acceptEdits" {
							t.Fatal("peripheral plan approval must use acceptEdits")
						}
						if !c.Input.Locked && (a.RequiresUnlock == nil || *a.RequiresUnlock) {
							t.Fatal("unlocked entry must not request unlock")
						}
						if a.RequiresUnlock != nil && (a.AvailableNow == nil || *a.RequiresUnlock == *a.AvailableNow) {
							t.Fatal("unlock and immediate availability disagree")
						}
						if a.Authentication == "required" && c.Input.Locked && (a.RequiresUnlock == nil || !*a.RequiresUnlock) {
							t.Fatal("locked authenticated action must require unlock")
						}
					}
					return
				}
				var ix Interaction
				if err := json.Unmarshal(c.Input.Interaction, &ix); err != nil {
					t.Fatal(err)
				}
				if ix.Dialog != nil {
					parsed := screen.ParseDialog(ix.Screen)
					if parsed != nil {
						parsed.Cursor = ""    // where the ❯ is: the hub's own, never on the wire
						parsed.LegacyRev = "" // the rev older hubs and nodes compute (#5961): never on the wire either (#5970)
					}
					if !reflect.DeepEqual(parsed, ix.Dialog) {
						t.Fatalf("screen parser differs from fixture dialog: %+v / %+v", parsed, ix.Dialog)
					}
				}
				if ix.ID == "" || ix.Kind == "" {
					t.Fatal("missing interaction identity")
				}
				raw := c.Expected.Resolution
				if len(raw) == 0 {
					t.Fatal("missing resolution (use null for no request)")
				}
				if bytes.Equal(raw, []byte("null")) {
					return
				}
				if !c.Expected.Allowed {
					t.Fatal("disabled operation must not emit a resolution")
				}
				var r Resolution
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&r); err != nil {
					t.Fatalf("hub Resolution decoder: %v", err)
				}
				encoded, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				var before, after map[string]any
				if err := json.Unmarshal(raw, &before); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &after); err != nil {
					t.Fatal(err)
				}
				// Swift encodes non-nil empty strings; Go omits their zero values.
				for k, v := range before {
					if v == "" && (k == "feedback" || k == "message" || k == "text") {
						delete(before, k)
					}
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("wire fields lost: %s -> %s", raw, encoded)
				}
				if r.Dismiss {
					return
				}
				switch ix.Kind {
				case IxQuestion:
					if len(r.Answers) != len(ix.Questions) {
						t.Fatal("every question needs an answer")
					}
					for _, q := range ix.Questions {
						if r.Answers[q.Question] == "" {
							t.Fatal("missing original question key")
						}
					}
				case IxPlan:
					if r.Approve == nil {
						t.Fatal("approve must remain a boolean pointer, including false")
					}
					if *r.Approve && r.Mode != "acceptEdits" && r.Mode != "bypassPermissions" && r.Mode != "default" {
						t.Fatal("explicit supported mode required")
					}
				case IxPermission:
					if r.Allow == nil {
						t.Fatal("allow must remain a boolean pointer, including false")
					}
				case IxScreen:
					if r.Option != "" && (ix.Dialog == nil || ix.Dialog.Option(r.Option) == nil) {
						t.Fatal("option must exist in hub dialog")
					}
					if r.Option != "" && (len(r.Keys) != 0 || len(r.PreKeys) != 0) {
						t.Fatal("hub options must not include legacy keys")
					}
				default:
					t.Fatalf("unknown interaction kind: %s", ix.Kind)
				}
			})
		}
	}
	for _, profile := range []string{"ios_current", "unified"} {
		for _, entry := range []string{"conversation", "notification", "lock_screen", "watch", "siri_shortcut"} {
			for _, kind := range []string{IxPlan, IxPermission, IxQuestion, IxScreen} {
				for _, lock := range []string{"locked", "unlocked"} {
					if policies[profile+"/"+entry+"/"+kind+"/"+lock] != 1 {
						t.Errorf("missing or duplicate policy: %s/%s/%s/%s", profile, entry, kind, lock)
					}
				}
			}
		}
	}
}

// swiftBlock copies a declaration verbatim. It does not rewrite the iOS implementation.
func swiftBlock(t *testing.T, source, marker string) string {
	t.Helper()
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("Swift declaration missing: %s", marker)
	}
	open := strings.Index(source[start:], "{") + start
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[start : i+1]
			}
		}
	}
	t.Fatalf("unterminated Swift declaration: %s", marker)
	return ""
}

// The generated Swift executable only lives in t.TempDir. Enable this on macOS for source-backed checks.
func TestResolutionFixturesIOS(t *testing.T) {
	if os.Getenv("PERSONA_VERIFY_IOS_FIXTURES") != "1" {
		t.Skip("set PERSONA_VERIFY_IOS_FIXTURES=1 on macOS to run the read-only Swift oracle")
	}
	root := answerFixtureRoot(t)
	read := func(path string) string {
		b, e := os.ReadFile(filepath.Join(root, path))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	models := read("apple/Shared/Models.swift")
	views := read("apple/Persona/Views/Interactions/InteractionViews.swift")
	chat := read("apple/Shared/ChatStore.swift")
	notification := read("apple/Persona/App/NotificationActions.swift")
	source := "import Foundation\n" + read("apple/Shared/JSONValue.swift") + "\n"
	for _, marker := range []string{"nonisolated struct SessionKey", "nonisolated enum InteractionKind", "nonisolated struct QuestionOption", "nonisolated struct Question:", "nonisolated struct Interaction:", "nonisolated struct Resolution:", "nonisolated struct ScreenDialog:"} {
		source += swiftBlock(t, models, marker) + "\n"
	}
	source += swiftBlock(t, chat, "nonisolated enum ScreenParser") + "\n" + swiftBlock(t, chat, "nonisolated private extension Character") + "\n"
	source += "enum NotificationActions {\n" + notification[strings.Index(notification, "    static let allow"):strings.Index(notification, "    static func category")] + swiftBlock(t, notification, "static func resolution(action:") + "\n}\n"
	source += "enum ApproveFirstIntent {\n" + swiftBlock(t, read("apple/Persona/Intents/ApproveFirstIntent.swift"), "static func resolution(for ix:") + "\n}\n"
	source += swiftQuestionSupport + "\n"
	source += swiftResolveSupport + "\n@MainActor final class ResolveProbe {\n" + swiftResolveFields + swiftBlock(t, read("apple/Persona/App/AppModel.swift"), "func resolve(_ ix:") + "\n}\n"
	question := swiftBlock(t, views, "struct QuestionForm:")
	source += "@MainActor final class QuestionProbe {\n" + swiftQuestionFields
	for _, marker := range []string{"private var questions:", "private var needsFooter:", "private func pick(", "private var isLast:", "private func advance(", "private func answer(", "private var allAnswered:", "private var canAdvance:", "private func submit("} {
		source += swiftBlock(t, question, marker) + "\n"
	}
	source += swiftQuestionRun + "\n}\n"
	source += "struct ScreenProbe { var interaction: Interaction\n" + swiftBlock(t, views, "private func answer(_ opt: ScreenParser.Dialog.Option") + "\nfunc run(_ key: String, _ text: String?) -> Resolution { answer(interaction.screenDialog!.options.first { $0.key == key }!, text: text) }\n}\n"
	// Compile the literal button constructors from the actual view, rather than duplicate their modes.
	source += "func button(_ action: String, _ feedback: String) -> Resolution {\nswitch action {\n"
	for _, pair := range [][2]string{{"approve_all", "Resolution(approve: true, mode: \"bypassPermissions\")"}, {"approve_edits", "Resolution(approve: true, mode: \"acceptEdits\")"}, {"reject", "Resolution(approve: false, feedback: feedback)"}, {"allow", "Resolution(allow: true)"}, {"deny", "Resolution(allow: false)"}, {"always", "Resolution(allow: true, always: true)"}, {"dismiss", "Resolution(dismiss: true)"}} {
		src := views
		if pair[0] == "dismiss" {
			src = models
		}
		pos := strings.Index(src, pair[1])
		if pos < 0 {
			t.Fatalf("button constructor changed: %s", pair[0])
		}
		source += "case \"" + pair[0] + "\": return " + src[pos:pos+len(pair[1])] + "\n"
	}
	source += "default: fatalError(action)\n}\n}\n"
	watch := read("apple/PersonaWatch/PersonaWatchApp.swift")
	watchExpr := "Resolution(approve: true, mode: \"acceptEdits\")"
	at := strings.Index(watch, watchExpr)
	if at < 0 {
		t.Fatal("watch plan constructor changed")
	}
	source += "func watchPlan() -> Resolution { " + watch[at:at+len(watchExpr)] + " }\n" + swiftFixtureMain
	dir := t.TempDir()
	file := filepath.Join(dir, "main.swift")
	binary := filepath.Join(dir, "verify")
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	compile := exec.CommandContext(ctx, "swiftc", "-parse-as-library", file, "-o", binary)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("swiftc: %v\n%s", err, out)
	}
	run := exec.CommandContext(ctx, binary, filepath.Join(root, "spec/fixtures/answer"), filepath.Join(root, "spec/fixtures/approval/ios_current.json"))
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("Swift fixtures: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

const swiftQuestionSupport = `
enum Haptics { static func tap() {}; static func success() {}; static func attention() {} }
struct Animation { static var snappy: Self { .init() }; static func snappy(duration: Double) -> Self { .init() } }
func withAnimation(_ animation: Animation, _ body: () -> Void) { body() }
@MainActor final class Spy { var result: Resolution?; func resolve(_ ix: Interaction, _ r: Resolution) async { result = r } }
`
const swiftQuestionFields = `
var interaction: Interaction
var picks: [String: Set<String>] = [:]
var other: [String: String] = [:]
var page = 0
var sending = false
var writing = false
var otherFocused = false
let model = Spy()
init(_ ix: Interaction) { interaction = ix }
`
const swiftQuestionRun = `
func run(_ op: [String: Any]) async -> [String: Any] {
 page = op["page"] as? Int ?? 0
 sending = op["sending"] as? Bool ?? false
 picks = (op["picks"] as? [String: [String]] ?? [:]).mapValues { Set($0) }
 other = op["other"] as? [String: String] ?? [:]
 let action = op["action"] as! String
 let allowed = action == "pick_option" ? !sending : canAdvance && !sending
 if allowed {
  if action == "pick_option" { pick(questions[page], questions[page].options[op["option_index"] as! Int]) }
  else if action == "advance" { advance() }
  else { submit() }
 }
 for _ in 0..<100 where model.result == nil && sending { try! await Task.sleep(for: .milliseconds(10)) }
 var out: [String: Any] = ["allowed": allowed, "resolution": wire(model.result), "next_page": page, "picks": picks.mapValues { $0.sorted() }]
 if let q = questions.first, let option = q.options.first {
  let d = QuestionOption.display(option.label)
  out["display"] = ["text": d.0, "recommended": d.1]
 }
 return out
}
`
const swiftFixtureMain = `
func wire(_ r: Resolution?) -> Any { r.map { try! JSONSerialization.jsonObject(with: JSONEncoder().encode($0)) } ?? NSNull() }
func check(_ expected: [String: Any], _ actual: [String: Any], _ name: String) {
 for (key,value) in expected {
  let lhs = try! JSONSerialization.data(withJSONObject: ["v":value], options: [.sortedKeys])
  let rhs = try! JSONSerialization.data(withJSONObject: ["v":actual[key] ?? NSNull()], options: [.sortedKeys])
  if lhs != rhs { fatalError("\(name).\(key): expected \(String(decoding: lhs,as: UTF8.self)), got \(String(decoding: rhs,as: UTF8.self))") }
 }
}
@main struct Main {
 @MainActor static func main() async throws {
  let folder = URL(fileURLWithPath: CommandLine.arguments[1])
  let files = try FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil).filter { $0.pathExtension == "json" }.sorted { $0.path < $1.path }
  var count = 0
  for file in files {
   let doc = try JSONSerialization.jsonObject(with: Data(contentsOf:file)) as! [String:Any]
   for c in doc["cases"] as! [[String:Any]] {
    let input = c["input"] as! [String:Any]
    if input["profile"] as! String != "ios_current" { continue }
    let ix = try JSONDecoder().decode(Interaction.self, from: JSONSerialization.data(withJSONObject: input["interaction"]!))
    let op = input["operation"] as! [String:Any], expected = c["expected"] as! [String:Any], name = c["name"] as! String
    let action = op["action"] as! String
    var actual: [String:Any]
    if action == "resolve" {
     let p = ResolveProbe()
     p.api = (op["api_available"] as! Bool) ? ProbeAPI(op["outcome"] as! String) : nil
     p.connection.networkUp = op["network_up"] as! Bool
     p.interactions[ix.id] = ix
     let r = try JSONDecoder().decode(Resolution.self, from: JSONSerialization.data(withJSONObject: op["resolution"]!))
     await p.resolve(ix,r)
     actual = ["allowed": p.api != nil && p.connection.networkUp, "resolution": wire(p.api?.sent), "card_present": p.interactions[ix.id] != nil]
    } else if action == "inspect_dialog" {
     var dialog: Any = NSNull()
     if let d = ix.screenDialog { dialog = ["question": d.question, "detail": d.detail as Any? ?? NSNull(), "steps": d.steps.map { ["label": $0.label, "done": $0.done] as [String: Any] }] }
     actual = ["allowed": true, "resolution": NSNull(), "dialog": dialog]
    } else if ix.kind == .question && action != "dismiss" { actual = await QuestionProbe(ix).run(op) }
    else {
     var allowed = true; var r: Resolution?
     if op["entry"] as? String == "notification" { r = NotificationActions.resolution(action:action,text:op["text"] as? String) }
     else if action == "screen_option" {
      let text = (op["text"] as? String)?.trimmingCharacters(in:.whitespacesAndNewlines)
      allowed = text == nil || !text!.isEmpty
      if allowed { r = ScreenProbe(interaction:ix).run(op["option"] as! String,text) }
     } else if action == "terminal_text" {
      let text = op["text"] as! String; allowed = !text.isEmpty
      if allowed { r = Resolution(keys:["enter"],text:text) }
     } else if action == "terminal_keys" { r = Resolution(keys:op["keys"] as? [String]) }
     else {
      if action == "always" { allowed = !ix.sessionID.isEmpty }
      if action.hasPrefix("approve_") { allowed = !(op["rejecting"] as? Bool ?? false) }
      if allowed { r = button(action,op["feedback"] as? String ?? "") }
     }
     actual = ["allowed":allowed,"resolution":wire(r)]
    }
    check(expected,actual,name); count += 1
   }
  }
  print("Verified \(count) iOS answer fixtures against source declarations and button constructors")
  let policies = try JSONSerialization.jsonObject(with: Data(contentsOf:URL(fileURLWithPath:CommandLine.arguments[2]))) as! [String:Any]
  var modes = 0
  for c in policies["cases"] as! [[String:Any]] {
   let input = c["input"] as! [String:Any], expected = c["expected"] as! [String:Any]
   let entry = input["entry"] as! String, kind = input["kind"] as! String
   for a in expected["actions"] as! [[String:Any]] {
    guard let mode = a["mode"] as? String else { continue }
    let r: Resolution
    if entry == "notification" || entry == "lock_screen" { r = NotificationActions.resolution(action:NotificationActions.approvePlan)! }
    else if entry == "siri_shortcut" {
     let data = try JSONSerialization.data(withJSONObject:["id":"example_request","machine_id":"example_machine","session_id":"example_session","kind":kind])
     r = ApproveFirstIntent.resolution(for:try JSONDecoder().decode(Interaction.self,from:data))!
    } else if entry == "watch" { r = watchPlan() } else { r = button(a["id"] as! String,"") }
    precondition(r.mode == mode,"approval mode mismatch"); modes += 1
   }
  }
  print("Verified \(modes) current approval modes; authentication is checked separately against source and iOS test assertions")
 }
}
`

const swiftResolveSupport = `
struct APIError: Error { var status: Int; var code: String }
struct ProbeConnection { var networkUp = true }
struct Toast { enum Style { case error }; init(_ text: String, style: Style) {} }
@MainActor final class ProbeAPI {
 var outcome: String; var sent: Resolution?
 init(_ outcome: String) { self.outcome = outcome }
 func resolve(_ id: String, _ r: Resolution) async throws {
  sent = r
  switch outcome {
  case "gone": throw APIError(status:410,code:"")
  case "prompt_changed": throw APIError(status:409,code:"prompt_changed")
  case "error": throw APIError(status:500,code:"")
  default: break
  }
 }
}
`
const swiftResolveFields = `
var api: ProbeAPI?
var connection = ProbeConnection()
var interactions: [String:Interaction] = [:]
var toast: Toast?
func notDone() {}
func show(_ error: Error) {}
`

// UI-only gates and authentication metadata cannot run without SwiftUI/AppIntents hosts.
// Keep their exact source evidence checked; the runtime Swift oracle covers the callable parts.
func TestResolutionFixtureSourceEvidence(t *testing.T) {
	root := answerFixtureRoot(t)
	evidence := map[string][]string{
		"apple/Persona/Views/Interactions/InteractionViews.swift": {
			".disabled(!canAdvance || sending)", ".disabled(sending)", "if interaction.sessionID != \"\" {",
			"if rejecting {", "rejecting = false", ".disabled(text.isEmpty)",
			"if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {",
			"let t = text.trimmingCharacters(in: .whitespacesAndNewlines)",
			"Resolution(keys: [\"enter\"], text: t)", "Resolution(keys: keys)",
		},
		"apple/PersonaWatch/PersonaWatchApp.swift": {
			"Resolution(approve: true, mode: \"acceptEdits\")", "Resolution(approve: false, feedback: \"请重新考虑这个计划\")",
			"Resolution(allow: true)", "Resolution(allow: false)", "Resolution(option: opt.key)", "Resolution(keys: [opt.key])",
			"Button(\"Submit\") { submit() }", "act(.dismissed)",
		},
		"apple/PersonaTests/NotificationActionsTests.swift": {
			"#expect(allow.options.contains(.authenticationRequired))",
			"#expect(!deny.options.contains(.authenticationRequired))",
			"#expect(plan.actions.allSatisfy { $0.options.contains(.authenticationRequired) })",
			"#expect(cats[\"INTERACTION_QUESTION\"]?.actions.isEmpty == true)",
			"#expect(cats[\"INTERACTION_SCREEN\"]?.actions.isEmpty == true)",
		},
		"apple/PersonaTests/FleetIntentsTests.swift": {
			"#expect(AllowPermissionIntent.authenticationPolicy == .requiresAuthentication)",
			"#expect(ApprovePlanIntent.authenticationPolicy == .requiresAuthentication)",
			"#expect(DenyPermissionIntent.authenticationPolicy == .alwaysAllowed)",
		},
		"apple/PersonaTests/IntentsTests.swift": {
			"#expect(ApproveFirstIntent.authenticationPolicy == .requiresAuthentication)",
		},
		"apple/PersonaWidgets/FleetLiveActivity.swift": {
			"Button(intent: ApprovePlanIntent(interactionID: id))", "Link(destination: FleetLink.url(item)!)", ".widgetURL(FleetLink.whole(state))",
		},
	}
	for path, snippets := range evidence {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range snippets {
			if !strings.Contains(string(data), snippet) {
				t.Errorf("source evidence changed in %s: %s", path, snippet)
			}
		}
	}

	fleet, err := os.ReadFile(filepath.Join(root, "apple/LiveActivity/FleetIntents.swift"))
	if err != nil {
		t.Fatal(err)
	}
	for name, policy := range map[string]string{"AllowPermissionIntent": ".requiresAuthentication", "ApprovePlanIntent": ".requiresAuthentication", "DenyPermissionIntent": ".alwaysAllowed"} {
		declaration := swiftBlock(t, string(fleet), "struct "+name+":")
		if !strings.Contains(declaration, "authenticationPolicy: IntentAuthenticationPolicy { "+policy+" }") {
			t.Errorf("authentication policy changed: %s", name)
		}
	}
	notifications, err := os.ReadFile(filepath.Join(root, "apple/Persona/App/NotificationActions.swift"))
	if err != nil {
		t.Fatal(err)
	}
	// Each action's identifier and unlock options; the title is a localization key (#5430) and may change freely.
	for id, options := range map[string]string{
		"allow": ".authenticationRequired", "deny": ".destructive", "approvePlan": ".authenticationRequired", "rejectPlan": ".authenticationRequired",
	} {
		re := regexp.MustCompile(`identifier: ` + id + `, title: String\(localized: "[^"]*"\), options: \[` + regexp.QuoteMeta(options) + `\]`)
		if !re.MatchString(string(notifications)) {
			t.Errorf("notification authentication changed: %s must keep options [%s]", id, options)
		}
	}
	// Siri says yes from a locked phone only after an unlock, like the Live Activity (#1878). The watch declares no
	// policy of its own; its fixtures never infer one from omission.
	data, err := os.ReadFile(filepath.Join(root, "apple/Persona/Intents/ApproveFirstIntent.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(swiftBlock(t, string(data), "struct ApproveFirstIntent:"), "authenticationPolicy: IntentAuthenticationPolicy { .requiresAuthentication }") {
		t.Fatal("Siri authentication policy changed: revisit current fixtures")
	}
}
