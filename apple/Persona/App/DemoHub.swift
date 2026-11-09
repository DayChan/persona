import Foundation

/// Demo Mode (docs/apple/ios.md「体验模式」): a made-up hub inside the app, for someone without one (App Review, a friend
/// with the TestFlight link). It sits under the data layer, so no page knows: `APIClient.request` and `LiveConnection`
/// hand the demo config's traffic to it, and it answers with the hub's JSON. The data is scripts/mock/mock_hub.py's, written
/// to Resources/Demo.json by scripts/mock/make_demo_data.py, with times moved to now. Nothing leaves the phone: what would
/// reach a computer, Feishu or mail says "Nothing is actually sent in demo mode" (`onNotSent`); a message sent to an agent is echoed
/// with a canned reply, so the chat behaves like one.
@MainActor
final class DemoHub {
    static let shared = DemoHub()

    /// Routes the demo config's requests and socket here (once, at launch; only the demo config uses them).
    static func install() {
        APIClient.local = { req in try await DemoHub.shared.answer(req) }
        LiveConnection.makeLocal = { DemoSocket() }
    }

    /// An action the demo does not carry out was taken (AppModel shows the toast).
    var onNotSent: (() -> Void)?

    private var data: [String: JSONValue] = [:]
    private var machines: [JSONValue] = []
    private var sessions: [JSONValue] = []
    private var items: [String: [JSONValue]] = [:]
    private var interactions: [JSONValue] = []
    private var inbox: [JSONValue] = []
    private var todos: [JSONValue] = []
    private var lark: [String: [JSONValue]] = [:]
    private var tasks: [JSONValue] = []
    private var modelBindings: [String: JSONValue] = [:]
    /// Settings › Model Providers' providers, the wizard's and the import's answers.
    private let providers = DemoProviders()
    /// Settings › Claude Code › Blocked Config Folders: machine id → the rules typed in the demo (it has no other directories).
    private var blockedClaudeDirs: [String: [JSONValue]] = [:]
    /// Settings › Claude Code › Live Replies, by machine (on unless turned off, as on a hub).
    private var liveReplies: [String: Bool] = [:]
    private var memory: [JSONValue] = []
    private var prefs: JSONValue = .object([:])
    private var pins: JSONValue = .object([:])
    private var mutes: JSONValue = .object(["spaces": .array([]), "sessions": .array([]), "allowed": .array([])])
    private var tokens: [JSONValue] = []
    /// When the demo started: the metrics' made-up events (a build, a machine away) sit relative to it.
    private var start = Date.now
    private var loaded = false
    private var sockets: [WeakSocket] = []
    private var made = 0

    private static let history = "demo-1"
    static let reply = String(localized: "This is demo mode: your message didn't actually reach a computer, and the agents here are sample data. Once you connect your own hub, Claude carries on on that computer.")

    /// The demo's agents and conversations (drafts typed into them go with the demo).
    var sessionKeys: Set<SessionKey> {
        load()
        return Set(sessions.compactMap { s in
            s["machine_id"]?.string.flatMap { m in s["session_id"]?.string.map { SessionKey(machineID: m, sessionID: $0) } }
        })
    }

    /// The Persona tab's main conversation.
    var mainConversation: SessionKey? {
        load()
        return data["assistant"]?["main"]?.string.map { SessionKey(machineID: data["assistant"]?["machine_id"]?.string ?? "server", sessionID: $0) }
    }

    /// Back to the data as shipped (entering Demo Mode again starts over).
    func reset() {
        loaded = false
        modelBindings = [:]
        providers.reset()
        sockets = []
    }

    private func load() {
        guard !loaded else { return }
        loaded = true
        start = .now
        guard let url = Bundle.main.url(forResource: "Demo", withExtension: "json"), let raw = try? Data(contentsOf: url),
              let root = try? JSON.decoder.decode(JSONValue.self, from: raw), case .object(let all) = Self.resolve(root, now: start)
        else { return }
        data = all
        machines = all["machines"]?.array ?? []
        sessions = all["sessions"]?.array ?? []
        items = (all["items"]?.object ?? [:]).compactMapValues(\.array)
        interactions = all["interactions"]?.array ?? []
        inbox = all["inbox"]?.array ?? []
        todos = all["todos"]?.array ?? []
        lark = (all["lark"]?.object ?? [:]).compactMapValues(\.array)
        tasks = all["tasks"]?.array ?? []
        memory = all["memory"]?.array ?? []
        prefs = all["prefs"] ?? .object([:])
        pins = all["pins"] ?? .object([:])
        tokens = all["tokens"]?.array ?? []
    }

    /// "@rel:<seconds>" → that moment as an ISO date, "@unix:<seconds>" → Unix seconds (make_demo_data.py).
    private static func resolve(_ v: JSONValue, now: Date) -> JSONValue {
        switch v {
        case .object(let o): return .object(o.mapValues { resolve($0, now: now) })
        case .array(let a): return .array(a.map { resolve($0, now: now) })
        case .string(let s) where s.hasPrefix("@rel:"):
            return .string(iso(now.addingTimeInterval(Double(s.dropFirst(5)) ?? 0)))
        case .string(let s) where s.hasPrefix("@unix:"):
            return .number((now.timeIntervalSince1970 + (Double(s.dropFirst(6)) ?? 0)).rounded())
        default: return v
        }
    }

    private static func iso(_ d: Date) -> String { d.formatted(.iso8601) }

    /// Subscription Usage (docs/server/usage-windows.md): a Claude and a Codex account, their windows relative to now.
    private static func usageWindows(now: Date = .now) -> JSONValue {
        func win(_ label: String, _ mins: Int, _ used: Double, _ left: TimeInterval) -> JSONValue {
            ["label": .string(label), "window_minutes": .number(Double(mins)), "used_percent": .number(used),
             "resets_at": .string(iso(now.addingTimeInterval(left)))]
        }
        let accounts: [JSONValue] = [
            ["id": "claude-demo", "provider": "claude", "name": "Claude", "plan": "Max", "local": true,
             "sampled_at": .string(iso(now.addingTimeInterval(-20))),
             "windows": .array([win("5h", 300, 72, 80 * 60), win("7d", 10080, 38, (2 * 24 + 6) * 3600)])],
            ["id": "codex-demo", "provider": "codex", "name": "Codex Pro", "plan": "pro", "local": true,
             "sampled_at": .string(iso(now.addingTimeInterval(-240))),
             "windows": .array([win("5h", 300, 18, 3 * 3600), win("7d", 10080, 64, (3 * 24 + 2) * 3600)])],
        ]
        return ["accounts": .array(accounts), "checked_at": .string(iso(now))]
    }

    // MARK: HTTP

    func answer(_ req: LocalRequest) async throws -> Data {
        load()
        let out = try route(req)
        return try JSON.encoder.encode(out)
    }

    private func claudeDirsState(_ id: String) -> JSONValue {
        let home = machines.first(where: { $0["id"]?.string == id })?["home"]?.string ?? "/Users/demo"
        let dir: JSONValue = ["path": .string(home + "/.claude"), "default": true, "source": "default", "running": 0]
        return ["dirs": [dir], "blocked": .array(blockedClaudeDirs[id] ?? []), "home": .string(home), "live": true]
    }

    private func body(_ req: LocalRequest) -> JSONValue {
        req.body.flatMap { try? JSON.decoder.decode(JSONValue.self, from: $0) } ?? .object([:])
    }

    private func notFound() -> APIError { APIError(status: 404, message: String(localized: "Demo mode doesn't have this data")) }

    private func logMiss(_ req: LocalRequest) {
        #if DEBUG
        NSLog("DemoHub miss: %@ %@", req.method, req.path) // DemoModeTests' walk-through: what no page should ask for
        #endif
    }

    /// An action the demo does not pretend to do: it fails with "Nothing is actually sent in demo mode" (an info toast, not an error:
    /// AppModel.show), so no page claims it worked.
    private func refuse() -> APIError {
        onNotSent?()
        return APIError(status: 0, message: DemoText.notSent, fields: ["code": DemoText.code])
    }

    private func route(_ req: LocalRequest) throws -> JSONValue {
        let p = req.path.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
        guard p.count >= 3, p[0] == "api", p[1] == "v1" else { throw notFound() }
        let rest = Array(p.dropFirst(2))
        do {
            return req.method == "GET" ? try get(rest, req.query) : try change(req.method, rest, body(req))
        } catch let e as APIError where e.status == 404 {
            if req.path != "/api/v1/fleet" { logMiss(req) } // no Live Activity in the demo: nothing there on purpose
            throw e
        }
    }

    private func get(_ p: [String], _ q: [String: String]) throws -> JSONValue {
        #if DEBUG
        if let sample = DemoKBSample.answer(p, q, kb: data["kb"]) { return sample } // knowledge-search screenshots
        #endif
        if let answer = try providers.get(p, q) { return answer }
        if p.count == 4, p[0] == "machines", p[2] == "health", p[3] == "reports" { return ["reports": .array([])] }
        switch p {
        case ["health"]: return ["ok": true, "version": "demo"]
        case ["machines"]: return ["machines": .array(machines)]
        case ["metrics"]:
            return ["machines": .array(machines.compactMap { $0["id"]?.string }.map { id in
                guard case .object(var o) = metrics(id, span: 3600, step: 60) else { return .null }
                if let card = usageCard(id) { o["usage"] = card }
                return .object(o)
            }), "step": 60]
        case ["sessions"]: return ["sessions": .array(sessions)]
        case ["usage", "windows"]: return Self.usageWindows()
        case ["interactions"]: return ["interactions": .array(interactions)]
        case ["interactions", "history"]: return ["interactions": .array([])]
        case ["auth", "tokens"]: return ["tokens": .array(tokens), "current": "dev_phone"]
        case ["pins"]: return pins
        case ["mutes"]: return mutes
        case ["assistant"]:
            var a = data["assistant"]?.object ?? [:]
            // ?space=health lists the health space's own (docs/assistant/health.md); the plain list never holds them.
            let space = q["space"] ?? ""
            a["conversations"] = .array(sessions.filter { $0["assistant"]?.bool == true && ($0["space"]?.string ?? "") == space })
            return .object(a)
        case ["model-config"]: return DemoModelCatalog.configuration(tasks: tasks, overrides: modelBindings)
        case ["assistant", "tasks"]: return ["tasks": .array(tasks)]
        case ["assistant", "places"]: return ["places": .array([])] // Saved Places, empty in the demo
        case ["assistant", "runs"]:
            let runs = data["runs"]?.array ?? []
            return ["runs": .array(q["ref"].map { ref in runs.filter { $0["ref_id"]?.string == ref } } ?? runs)]
        case ["assistant", "memory"]:
            if q["space"] == "health" { return ["files": .array([]), "dir": "/home/me/.persona/health/memory"] }
            return ["files": .array(memory), "dir": "/home/me/.persona/assistant/memory"]
        case ["inbox"]: return ["items": .array(inbox)]
        case ["todos"]: return ["todos": .array(todos)]
        case ["mail", "accounts"]: return ["accounts": data["mail_accounts"] ?? .array([])]
        case ["mail", "messages"]: return ["messages": .array([]), "has_more": false]
        case ["mail", "oauth", "google", "config"]: return ["configured": true] // the page as on a set-up hub
        case ["lark", "unread-count"]: return ["count": 4]
        case ["lark", "status"]: return ["configured": true, "connected": true, "name": "我", "last_event_at": .number((Date.now.timeIntervalSince1970 - 95).rounded(.down))] // i18n-ignore
        case ["lark", "conversations"]: return ["conversations": data["lark_conversations"] ?? .array([]), "next_cursor": "", "account_id": "demo-lark-account"]
        case ["lark", "pinned"]: return ["conversations": .array([]), "next_cursor": "", "account_id": "demo-lark-account"]
        case ["lark", "labels"]: return ["labels": .array([])]
        case ["lark", "flags"]: return ["conversations": .array([]), "messages": .array([])]
        case ["lark", "emojis"]: return ["emojis": .object([:])]
        case ["kb-search"]: return DemoKnowledgeSearch.answer(q["q"] ?? "", kb: data["kb"])
        case ["lark", "reactions", "quick"]: return ["keys": .array([])]
        default: break
        }
        switch (p.first ?? "", p.count) {
        case ("model-config", 3) where p[1] == "purposes":
            let catalog = DemoModelCatalog.configuration(tasks: tasks, overrides: modelBindings)
            guard let purpose = catalog["purposes"]?.array?.first(where: { $0["id"]?.string == p[2] }) else { throw notFound() }
            return ["purpose": purpose, "providers": catalog["providers"] ?? []]
        case ("machines", 3):
            let id = p[1]
            switch p[2] {
            case "usage":
                return ["usage": usage(id, range: q["range"] ?? "24h", model: q["model"], launcher: q["launcher"])]
            case "metrics":
                let (span, step) = ["6h": (6 * 3600, 60), "24h": (86400, 240)][q["range"] ?? ""] ?? (3600, 10)
                return ["metrics": metrics(id, span: span, step: step), "range": .string(q["range"] ?? "1h"), "step": .number(Double(step))]
            case "launchers":
                guard online(id) else { throw APIError(status: 503, message: "\(id): machine is offline") }
                return ["launchers": data["launchers"] ?? .array([]), "default": DemoProviders.launcherDefault]
            case "dirs":
                guard online(id) else { throw APIError(status: 503, message: "\(id): machine is offline") }
                return data["dirs"] ?? .object([:])
            case "claude-settings", "codex-settings": return ["settings": .array([])]
            case "claude-dirs": return claudeDirsState(id)
            case "claude-live-replies": return ["on": .bool(liveReplies[id] ?? true)]
            #if os(macOS)
            case "file": // the Mac's file viewer (and its ⌘F find): one made-up source file, whatever is asked for
                let path = q["path"] ?? "Sources/Forecast.swift"
                let text = Self.demoFile
                return ["path": .string(path), "name": .string((path as NSString).lastPathComponent), "size": .number(Double(text.utf8.count)),
                        "mime": "text/plain; charset=utf-8", "text": .string(text), "truncated": false, "is_dir": false, "offset": 0, "next": 0]
            #endif
            default: throw notFound()
            }
        #if os(macOS)
        case ("machines", 4) where p[2] == "fs" && p[3] == "find": // ⌘K's Files: a made-up checkout
            return ["cwd": .string(q["cwd"] ?? ""), "truncated": false, "source": "git",
                    "files": .array(Self.demoFiles.map { .string($0) })]
        #endif
        case ("machines", 4) where p[2] == "git" && p[3] == "status": // no repositories in the demo: the chat shows no git card
            return ["is_repo": false, "ahead": 0, "behind": 0, "files": .array([]), "stashes": 0, "commits": .array([])]
        case ("devices", 3) where p[2] == "prefs": return prefs
        case ("inbox", 2):
            guard let it = inbox.first(where: { $0["id"]?.string == p[1] }), case .object(var o) = it else { throw notFound() }
            o["context"] = o["preview"]
            return .object(o)
        case ("mail", 4) where p[1] == "messages":
            guard let m = data["mail"]?[p[3]] else { throw notFound() }
            return ["message": m]
        case ("assistant", 4) where p[1] == "conversations" && p[3] == "always": return ["grants": .array([])]
        case ("lark", 4) where p[1] == "conversations" && p[3] == "messages":
            return ["messages": .array(lark[p[2]] ?? []), "has_older": false, "has_newer": false]
        case ("lark", 4) where p[1] == "conversations" && p[3] == "labels":
            return ["label_ids": .array([]), "labels": .array([])]
        case ("kb", 2):
            return ["items": data["kb"]?[p[1]] ?? .array([]), "tags": .array([])]
        case ("kb", 3):
            guard let e = data["kb"]?[p[1]]?.array?.first(where: { $0["id"]?.string == p[2] }) else { throw notFound() }
            return ["detail": ["kind": .string(p[1]), "id": .string(p[2]), "path": .string("\(p[1])/\(p[2]).md"),
                               "summary": e["one_line"] ?? "", "log": .array([]), "links": .array([])], "agents": .array([])]
        case ("sessions", 4):
            return try sessionGet(SessionKey(machineID: p[1], sessionID: p[2]), p[3], q)
        case ("sessions", 6) where p[3] == "items" && p[5] == "full":
            guard let it = data["full"]?[p[4]] else { throw notFound() }
            return it
        default: throw notFound()
        }
    }

    private func sessionGet(_ k: SessionKey, _ what: String, _ q: [String: String]) throws -> JSONValue {
        let list = items[k.sessionID] ?? []
        let last = list.last?["seq"]?.int ?? 0
        switch what {
        case "items":
            if let a = q["after_seq"].flatMap(Int.init) {
                return ["items": .array(list.filter { ($0["seq"]?.int ?? 0) > a }), "more_after": false, "history": .string(Self.history),
                        "from_seq": .number(Double(a)), "to_seq": .number(Double(max(a, last)))]
            }
            if let through = q["through_seq"].flatMap(Int.init) {
                let from = q["from_seq"].flatMap(Int.init) ?? 0
                return ["items": .array(list.filter { (from...through).contains($0["seq"]?.int ?? -1) }), "more": false]
            }
            let before = q["before_seq"].flatMap(Int.init)
            let page = before.map { b in list.filter { ($0["seq"]?.int ?? 0) < b } } ?? list
            return ["items": .array(q["fold_work"] == "1" ? Self.foldWork(page) : page), "has_more": false,
                    "history": .string(Self.history), "to_seq": .number(Double(before.map { $0 - 1 } ?? last))]
        case "info":
            let s = session(k)
            return ["session_id": .string(k.sessionID), "cwd": s?["cwd"] ?? .null,
                    "model": .string(s?["agent"]?.string == "codex" ? "gpt-5.5" : "claude-opus-5"), "launcher": s?["launcher"] ?? .null,
                    "git_branch": "main", "version": "2.1.320", "permission_mode": s?["permission_mode"] ?? .null,
                    "effort": s?["effort"] ?? .null, "context_tokens": 48200, "output_tokens": 12800, "compactions": 0, "turns": 6,
                    "started_at": .string(Self.iso(.now.addingTimeInterval(-2400))), "last_activity": .string(Self.iso(.now))]
        case "screen": return ["text": data["screen"] ?? "", "status": "idle"]
        default: throw notFound()
        }
    }

    private func change(_ method: String, _ p: [String], _ body: JSONValue) throws -> JSONValue {
        if p.count == 3, p[0] == "model-config", p[1] == "purposes", method == "PATCH" {
            let id = p[2]
            let catalog = DemoModelCatalog.configuration(tasks: tasks, overrides: modelBindings)
            guard let purpose = catalog["purposes"]?.array?.first(where: { $0["id"]?.string == id }) else { throw notFound() }
            guard body["revision"]?.int == purpose["revision"]?.int else { throw APIError(status: 409, message: String(localized: "The settings changed. Reload and try again.")) }
            var binding = purpose["override"]?.object ?? [:]
            for field in ["provider", "model", "effort", "runtime"] {
                guard let value = body[field] else { continue }
                if value == .null { binding.removeValue(forKey: field) } else { binding[field] = value }
            }
            modelBindings[id] = ["override": .object(binding), "revision": .number(Double((purpose["revision"]?.int ?? 0) + 1))]
            return try get(p, [:])
        }
        if let answer = try providers.change(method, p, body) { return answer }
        switch p {
        case ["sessions"]:
            throw APIError(status: 403, message: String(localized: "Can't start a new session in demo mode. Connect your own hub and try again."))
        case ["assistant", "conversations"]:
            return ["session": startConversation(body["prompt"]?.string ?? "")]
        case ["assistant", "tasks"]: // a new one: saved as it is, next run at its time
            guard case .object(var t) = body else { throw refuse() }
            made += 1
            t["id"] = .string("task_demo\(made)")
            t["next_run"] = t["at"] ?? .null
            tasks.append(.object(t))
            return ["task": .object(t)]
        case ["inbox", "refresh"]: return ["items": .array(inbox)]
        case ["translate"], ["translate", "mail"]:
            throw APIError(status: 403, message: String(localized: "Translation isn't available in demo mode. Connect your own hub and try again."))
        case ["todos"]:
            made += 1
            let newTodo: JSONValue = .object([
                "id": .string("todo_demo\(made)"),
                "title": body["title"] ?? "新待办", // i18n-ignore
                "notes": body["notes"] ?? .null,
                "done_at": .null,
                "created_at": .string(Self.iso(.now)),
                "updated_at": .string(Self.iso(.now)),
                "agents": .array([])
            ])
            todos.insert(newTodo, at: 0)
            return ["todo": newTodo]
        case ["pins"]:
            pins = body
            return pins
        case ["mutes"]:
            mutes = body
            return mutes
        case ["assistant", "warm"], ["devices"]: return ["ok": true]
        default: break
        }
        switch (p.first ?? "", p.count) {
        case ("sessions", 4):
            let k = SessionKey(machineID: p[1], sessionID: p[2])
            switch p[3] {
            case "messages":
                echo(k, body["text"]?.string ?? "")
                return ["ok": true]
            case "seen":
                markSeen(k.sessionID)
                return ["ok": true]
            case "modes":
                edit(k.sessionID) { s in
                    for f in ["permission_mode", "effort"] { if let v = body[f], v != .null { s[f] = v } }
                }
                broadcastSessions(k.machineID)
                return ["ok": true]
            default: throw refuse()
            }
        case ("interactions", 3) where p[2] == "resolve":
            resolve(p[1])
            onNotSent?()
            return ["ok": true]
        case ("devices", 3) where p[2] == "prefs":
            if case .object(var o) = prefs, case .object(let new) = body {
                for (k, v) in new where k != "island" { o[k] = v }
                prefs = .object(o)
            }
            return prefs
        case ("devices", 3): return ["ok": true] // live-activity reports
        case ("todos", 2) where method == "PATCH":
            guard let i = todos.firstIndex(where: { $0["id"]?.string == p[1] }), case .object(var t) = todos[i],
                  case .object(let edit) = body else { throw notFound() }
            if let title = edit["title"] { t["title"] = title }
            if let notes = edit["notes"] { t["notes"] = notes }
            if let done = edit["done"]?.bool { t["done_at"] = done ? .string(Self.iso(.now)) : .null }
            // Due date, reminder, priority (#1683): either due field replaces the other; a new reminder is pushed again.
            if edit["due_at"] != nil || edit["due_date"] != nil {
                t["due_at"] = edit["due_at"] ?? .null
                t["due_date"] = edit["due_date"] ?? .null
            }
            if let remind = edit["remind_at"], remind != t["remind_at"] {
                t["remind_at"] = remind
                t["reminded_at"] = .null
            }
            if let priority = edit["priority"] { t["priority"] = priority }
            t["updated_at"] = .string(Self.iso(.now))
            todos[i] = .object(t)
            return ["todo": .object(t)]
        case ("todos", 2) where method == "DELETE":
            todos.removeAll { $0["id"]?.string == p[1] }
            return ["ok": true]
        case ("todos", 5) where p[2] == "agents" && method == "PUT":
            guard let i = todos.firstIndex(where: { $0["id"]?.string == p[1] }), case .object(var t) = todos[i] else { throw notFound() }
            var ags = t["agents"]?.array ?? []
            ags.removeAll { $0["machine_id"]?.string == p[3] && $0["session_id"]?.string == p[4] }
            ags.insert(.object([
                "machine_id": .string(p[3]),
                "session_id": .string(p[4]),
                "linked_at": .string(Self.iso(.now)),
                "released_at": .null,
                "title": .string("演示 Agent"), // i18n-ignore
                "agent": .string("claude")
            ]), at: 0)
            t["agents"] = .array(ags)
            todos[i] = .object(t)
            return ["ok": true]
        case ("todos", 5) where p[2] == "agents" && method == "DELETE":
            guard let i = todos.firstIndex(where: { $0["id"]?.string == p[1] }), case .object(var t) = todos[i] else { throw notFound() }
            var ags = t["agents"]?.array ?? []
            ags.removeAll { $0["machine_id"]?.string == p[3] && $0["session_id"]?.string == p[4] }
            t["agents"] = .array(ags)
            todos[i] = .object(t)
            return ["ok": true]
        case ("assistant", 4) where p[1] == "tasks": throw refuse() // Run Now
        case ("assistant", 4) where p[1] == "conversations" && (p[3] == "archive" || p[3] == "unarchive"):
            edit(p[2]) { $0["archived"] = .bool(p[3] == "archive") }
            broadcastSessions("server")
            return ["ok": true, "archived": .bool(p[3] == "archive")]
        case ("assistant", 3) where p[1] == "tasks" && method == "DELETE":
            tasks.removeAll { $0["id"]?.string == p[2] }
            return ["ok": true]
        case ("assistant", 3) where p[1] == "tasks":
            guard let i = tasks.firstIndex(where: { $0["id"]?.string == p[2] }), case .object(var t) = tasks[i],
                  case .object(let edit) = body else { throw notFound() }
            for (k, v) in edit where k != "id" { t[k] = v.string == "" ? .null : v } // "" = the default
            if t["push"]?.string == "asked" { t["session_id"] = .null } // Push When Done: such a task runs in its own conversation
            if edit["enabled"]?.bool == false { t["next_run"] = .null } else if t["repeat"]?.string != "daily" { t["next_run"] = t["at"] ?? .null }
            tasks[i] = .object(t)
            return ["task": .object(t)]
        case ("assistant", 3) where p[1] == "memory":
            memory.removeAll { $0["name"]?.string == p[2] }
            if method != "DELETE" {
                memory.append(["name": .string(p[2]), "content": body["content"] ?? "", "updated_at": .string(Self.iso(.now))])
            }
            return ["ok": true]
        case ("inbox", 3) where p[2] == "done":
            editInbox(p[1]) { $0["done_at"] = body["done"]?.bool == true ? .string(Self.iso(.now)) : .null }
            return ["ok": true]
        case ("inbox", 3) where p[2] == "draft":
            let it = inbox.first { $0["id"]?.string == p[1] }
            return ["text": it?["agent"]?["draft"] ?? "好的，我看一下，晚点回复你。"] // i18n-ignore
        case ("inbox", 3) where p[2] == "reply": throw refuse()
        case ("lark", 4) where p[1] == "conversations" && p[3] == "read": return ["ok": true]
        case ("mail", 5) where p[1] == "messages" && p[4] == "flags": return ["ok": true] // opening a mail marks it read
        case ("lark", 4) where p[1] == "conversations" && p[3] == "messages":
            let n = (lark[p[2]]?.compactMap { $0["position"]?.string.flatMap(Int.init) }.max() ?? 0) + 1
            let text = body["text"]?.string ?? ""
            lark[p[2], default: []].append(["id": .string("om_demo_\(n)"), "position": .string(String(n)), "sender_name": "我", // i18n-ignore
                                             "is_mine": true, "type": "TEXT", "text": .string(text), "recalled": false, "reply_count": 0,
                                             "created_at": .string(Self.iso(.now)), "parts": [["kind": "text", "text": .string(text)]]])
            onNotSent?()
            return ["ok": true]
        case ("auth", 3) where method == "DELETE":
            tokens = tokens.map { t in
                guard t["id"]?.string == p[2], case .object(var o) = t else { return t }
                o["revoked_at"] = .string(Self.iso(.now))
                return .object(o)
            }
            return ["ok": true]
        case ("machines", 3) where p[2] == "claude-live-replies" && method == "PUT":
            guard let on = body["on"]?.bool else { throw refuse() }
            liveReplies[p[1]] = on
            return ["on": .bool(on)]
        case ("machines", 4) where p[2] == "claude-dirs" && p[3] == "blocked" && method == "PUT":
            blockedClaudeDirs[p[1]] = body["blocked"]?.array ?? []
            return claudeDirsState(p[1])
        case ("machines", 3) where p[2] == "engine" && method == "PUT":
            guard let i = machines.firstIndex(where: { $0["id"]?.string == p[1] }), case .object(var machine) = machines[i] else { throw notFound() }
            guard let raw = body["engine"]?.string,
                  MachineEngine.allCases.contains(MachineEngine(rawValue: raw)) ||
                    machine["engines"]?.array?.contains(where: { $0["id"]?.string == raw && $0["available"]?.bool == true }) == true else {
                throw APIError(status: 400, message: String(localized: "Unknown engine"))
            }
            machine["engine"] = .string(raw)
            machines[i] = .object(machine)
            broadcast(["type": "machines", "machines": .array(machines)])
            return machines[i]
        case ("machines", 3) where p[2] == "reconnect": return ["ok": true]
        case ("kb", _): return ["ok": true]
        default: throw refuse()
        }
    }

    // MARK: State

    private func session(_ k: SessionKey) -> JSONValue? {
        sessions.first { $0["session_id"]?.string == k.sessionID && $0["machine_id"]?.string == k.machineID }
    }

    private func online(_ machine: String) -> Bool {
        machines.contains { $0["id"]?.string == machine && $0["online"]?.bool == true }
    }

    private func edit(_ sid: String, _ change: (inout [String: JSONValue]) -> Void) {
        for i in sessions.indices where sessions[i]["session_id"]?.string == sid {
            guard case .object(var o) = sessions[i] else { continue }
            change(&o)
            sessions[i] = .object(o)
        }
    }

    private func editInbox(_ id: String, _ change: (inout [String: JSONValue]) -> Void) {
        for i in inbox.indices where inbox[i]["id"]?.string == id {
            guard case .object(var o) = inbox[i] else { continue }
            change(&o)
            inbox[i] = .object(o)
        }
    }

    private func markSeen(_ sid: String) {
        edit(sid) { s in
            guard s["state"]?.string == "unread" else { return }
            s["state"] = "idle"
            s["status"] = "idle"
            s["seen_at"] = .string(Self.iso(.now))
        }
    }

    private func append(_ sid: String, _ new: [String: JSONValue]) -> (item: JSONValue, from: Int) {
        let from = items[sid]?.last?["seq"]?.int ?? 0
        made += 1
        var o = new
        o["id"] = .string("demo-\(made)#0")
        o["seq"] = .number(Double(from + 1))
        o["ts"] = .string(Self.iso(.now))
        let item = JSONValue.object(o)
        items[sid, default: []].append(item)
        return (item, from)
    }

    /// A message to an agent: it shows as sent, the agent "works" a moment and answers that this is a demo.
    private func echo(_ k: SessionKey, _ text: String) {
        let (mine, from) = append(k.sessionID, ["role": "user", "kind": "text", "text": .string(text)])
        broadcastItems(k, [mine], from: from)
        edit(k.sessionID) { s in
            s["state"] = "working"
            s["status"] = "working"
            s["updated_at"] = .string(Self.iso(.now))
        }
        broadcastSessions(k.machineID)
        Task {
            try? await Task.sleep(for: .seconds(1.5))
            respond(k, Self.reply)
        }
    }

    private func respond(_ k: SessionKey, _ text: String) {
        let (reply, from) = append(k.sessionID, ["role": "assistant", "kind": "text", "text": .string(text)])
        broadcastItems(k, [reply], from: from)
        edit(k.sessionID) { s in
            let assistant = s["assistant"]?.bool == true
            s["state"] = assistant ? "idle" : "unread"
            s["status"] = assistant ? "idle" : "done"
            s["last_text"] = .string(text)
            s["updated_at"] = .string(Self.iso(.now))
        }
        broadcastSessions(k.machineID)
    }

    /// Answering a question / plan card: the card goes, the agent picks up and comes back with the demo's reply.
    private func resolve(_ id: String) {
        guard let ix = interactions.first(where: { $0["id"]?.string == id }),
              let m = ix["machine_id"]?.string, let sid = ix["session_id"]?.string else { return }
        interactions.removeAll { $0["id"]?.string == id }
        broadcast(["type": "interaction_resolved", "id": .string(id)])
        let k = SessionKey(machineID: m, sessionID: sid)
        edit(sid) { s in
            s["pending_interactions"] = 0
            s["state"] = "working"
            s["status"] = "working"
        }
        broadcastSessions(m)
        Task {
            try? await Task.sleep(for: .seconds(1.5))
            respond(k, String(localized: "Got it, continuing with your choice.\\n\\n\(Self.reply)"))
        }
    }

    private func startConversation(_ prompt: String) -> JSONValue {
        made += 1
        let sid = String(format: "d0e0a0b0-0000-4000-8000-%012d", made)
        let title = String(prompt.prefix(20))
        sessions.append(["machine_id": "server", "session_id": .string(sid), "title": .string(title.isEmpty ? "新对话" : title), // i18n-ignore
                         "cwd": "/home/me/.persona/assistant", "status": "working", "state": "working", "agent": "claude",
                         "launcher": "claude", "model": "opus", "started_by_persona": true, "assistant": true,
                         "updated_at": .string(Self.iso(.now)), "last_text": "", "permission_mode": "default", "effort": "high",
                         "pending_interactions": 0, "rev": 1000])
        _ = append(sid, ["role": "user", "kind": "text", "text": .string(prompt)])
        broadcastSessions("server")
        let k = SessionKey(machineID: "server", sessionID: sid)
        Task {
            try? await Task.sleep(for: .seconds(1.5))
            respond(k, Self.reply)
        }
        return session(k) ?? .null
    }

    // MARK: Socket

    fileprivate func attach(_ s: DemoSocket) {
        load()
        sockets.removeAll { $0.socket == nil }
        sockets.append(WeakSocket(socket: s))
        s.send(["type": "hello", "server_version": "demo", "client_id": "demo",
                "features": ["file_upload", "terminal_stream", "task_push"]])
        s.send(["type": "machines", "machines": .array(machines)])
        for m in Set(sessions.compactMap { $0["machine_id"]?.string }).sorted() {
            s.send(["type": "sessions", "machine_id": .string(m), "sessions": .array(sessions.filter { $0["machine_id"]?.string == m })])
        }
    }

    fileprivate func received(_ msg: JSONValue, from s: DemoSocket) {
        guard let type = msg["type"]?.string else { return }
        let key = msg["machine_id"]?.string.flatMap { m in msg["session_id"]?.string.map { SessionKey(machineID: m, sessionID: $0) } }
        switch type {
        case "subscribe":
            guard let key else { return }
            let list = items[key.sessionID] ?? []
            let last = list.last?["seq"]?.int ?? 0
            var out: [String: JSONValue] = ["type": "items", "machine_id": .string(key.machineID), "session_id": .string(key.sessionID),
                                            "backlog": true, "history": .string(Self.history)]
            if let since = msg["since_seq"]?.int, since > 0 {
                out["items"] = .array(list.filter { ($0["seq"]?.int ?? 0) > since })
                out["from_seq"] = .number(Double(since))
                out["to_seq"] = .number(Double(max(since, last)))
            } else {
                out["items"] = .array(msg["fold_work"]?.bool == true ? Self.foldWork(list) : list)
                out["has_more"] = false
                out["to_seq"] = .number(Double(last))
            }
            s.send(.object(out))
        case "seen":
            if let key { markSeen(key.sessionID) }
        case "terminal_subscribe":
            if let key { s.play(key, frames: data["terminal_frames"]?.array ?? []) }
        case "terminal_unsubscribe":
            s.stopPlaying()
        case "terminal_input":
            onNotSent?()
        default: break
        }
    }

    private func broadcast(_ msg: JSONValue) {
        for w in sockets { w.socket?.send(msg) }
    }

    private func broadcastSessions(_ machine: String) {
        broadcast(["type": "sessions", "machine_id": .string(machine), "sessions": .array(sessions.filter { $0["machine_id"]?.string == machine })])
    }

    private func broadcastItems(_ k: SessionKey, _ new: [JSONValue], from: Int) {
        broadcast(["type": "items", "machine_id": .string(k.machineID), "session_id": .string(k.sessionID), "items": .array(new),
                   "history": .string(Self.history), "from_seq": .number(Double(from)), "to_seq": .number(Double(from + new.count))])
    }

    // MARK: Folding (transcript.FoldWork, like mock_hub.fold_work)

    /// Each run of tool calls (with their results) and thoughts becomes one fold item.
    static func foldWork(_ list: [JSONValue]) -> [JSONValue] {
        let uses = Set(list.filter { $0["kind"]?.string == "tool_use" }.compactMap { $0["tool_use_id"]?.string })
        let results = Set(list.filter { $0["kind"]?.string == "tool_result" }.compactMap { $0["tool_use_id"]?.string })
        func inside(_ x: JSONValue) -> Bool {
            let kind = x["kind"]?.string ?? ""
            if kind == "thinking" { return true }
            guard let t = x["tool_use_id"]?.string else { return false }
            return ["tool_use", "tool_result", "image"].contains(kind) && uses.contains(t) && results.contains(t)
        }
        var out: [JSONValue] = []
        var i = 0
        while i < list.count {
            guard inside(list[i]) else {
                out.append(list[i])
                i += 1
                continue
            }
            var j = i
            while j < list.count && inside(list[j]) { j += 1 }
            let run = Array(list[i..<j])
            let size = run.reduce(0) { $0 + ($1["text"]?.string?.count ?? 0) + ($1["tool_input"]?.pretty.count ?? 0) }
            if run.count < 3 && size < 4096 {
                out += run
            } else {
                let first = run[0], last = run[run.count - 1]
                out.append(["id": .string("fold:" + (first["id"]?.string ?? "")), "seq": first["seq"] ?? 0, "ts": first["ts"] ?? .null,
                            "role": "assistant", "kind": "fold",
                            "fold": ["items": .number(Double(run.count)),
                                     "tools": .number(Double(run.filter { $0["kind"]?.string == "tool_use" }.count)),
                                     "thoughts": .number(Double(run.filter { $0["kind"]?.string == "thinking" && !($0["text"]?.string ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }.count)),
                                     "until": last["ts"] ?? .null, "through_seq": last["seq"] ?? 0, "through_id": last["id"] ?? ""]])
            }
            i = j
        }
        return out
    }

    #if os(macOS)
    /// ⌘K's Files in the demo (Mac): the files of a small app, whatever directory is asked for.
    static let demoFiles = [
        "README.md", "Package.swift", "Sources/Forecast.swift", "Sources/App/WeatherApp.swift",
        "Sources/App/Views/ForecastView.swift", "Sources/App/Views/SettingsView.swift", "Sources/App/Models/Forecast.swift",
        "Sources/App/Models/Location.swift", "Tests/ForecastTests.swift", "docs/design.md",
    ]
    /// The one file the Mac's demo file viewer shows (made up, like the rest of the demo).
    private static let demoFile = """
        import Foundation

        /// Tomorrow's forecast for a city, from the weather service.
        struct Forecast: Codable, Equatable {
            var city: String
            var high: Double
            var low: Double
            var summary: String
        }

        enum ForecastError: Error {
            case badCity(String)
            case offline
        }

        final class ForecastClient {
            private let session: URLSession
            private var cache: [String: Forecast] = [:]

            init(session: URLSession = .shared) {
                self.session = session
            }

            /// The forecast for `city`, from the cache when it is there.
            func forecast(for city: String) async throws -> Forecast {
                if let cached = cache[city] { return cached }
                guard !city.isEmpty else { throw ForecastError.badCity(city) }
                let url = URL(string: "https://weather.example.com/v1/forecast?city=\\(city)")!
                let (data, _) = try await session.data(from: url)
                let forecast = try JSONDecoder().decode(Forecast.self, from: data)
                cache[city] = forecast
                return forecast
            }

            func clearCache() {
                cache = [:]
            }
        }

        """
    #endif

    // MARK: Metrics (mock_hub.metric_at: made-up but smooth curves)

    private struct Profile { var cpu: Double; var mem: Double; var rx: Double; var tx: Double; var swap: Double; var disk: Double; var diskTotal: Double }
    private static let profiles: [String: Profile] = [
        "server": Profile(cpu: 38, mem: 0.62, rx: 2.4e6, tx: 0.35, swap: 0, disk: 0.64, diskTotal: 1000),
        "macbook": Profile(cpu: 18, mem: 0.55, rx: 4.5e5, tx: 0.2, swap: 1.2, disk: 0.71, diskTotal: 1000),
        "studio": Profile(cpu: 27, mem: 0.31, rx: 1.2e6, tx: 0.6, swap: 0, disk: 0.38, diskTotal: 2000),
        "nas": Profile(cpu: 6, mem: 0.42, rx: 3e5, tx: 1.4, swap: 0.4, disk: 0.93, diskTotal: 4000),
        "mini": Profile(cpu: 4, mem: 0.28, rx: 8e4, tx: 0.5, swap: 0, disk: 0.46, diskTotal: 1000),
    ]
    private static let gb = Double(1 << 30)

    private func system(_ mid: String) -> JSONValue? { machines.first { $0["id"]?.string == mid }?["system"] }

    /// Whether the machine was sending at t: the Mac Studio was away for two hours this morning, the NAS is offline.
    private func present(_ mid: String, _ t: Int) -> Bool {
        let t0 = Int(start.timeIntervalSince1970)
        if mid == "studio" && (t0 - 7 * 3600..<t0 - 5 * 3600).contains(t) { return false }
        if mid == "nas" && t >= t0 - 3 * 3600 { return false }
        return Self.profiles[mid] != nil
    }

    private func reading(_ mid: String, _ t: Int) -> (cpu: Double, load: Double, mem: Double, swap: Double, rx: Double, tx: Double, disk: Double) {
        let p = Self.profiles[mid]!
        let cores = system(mid)?["cores"]?.double ?? 8
        let memory = system(mid)?["memory"]?.double ?? 16 * Self.gb
        var x = UInt64(t / 10) &* 0x9E37_79B9_7F4A_7C15 ^ UInt64(mid.utf8.reduce(0) { $0 &* 31 &+ Int($1) })
        x ^= x >> 29
        x = x &* 0xBF58_476D_1CE4_E5B9
        x ^= x >> 32
        let h = Double(x % 1_000_000) / 1_000_000 // jitter 0…1
        let day = sin(2 * .pi * Double(t % 86400) / 86400)
        let wave = sin(Double(t) / 1900) * 0.5 + sin(Double(t) / 530 + Double(mid.count)) * 0.3
        let building = mid == "server" && t > Int(start.timeIntervalSince1970) - 25 * 60 // a build running when the demo opened
        let cpu = min(99.5, max(0.5, building ? 92 + 6 * h : p.cpu + 10 * day + 12 * wave + 8 * h))
        let memFrac = building ? 0.88 + 0.02 * h : p.mem + 0.05 * day + 0.04 * wave
        let rx = max(0, p.rx * (1 + 0.6 * wave + 0.5 * h))
        return (cpu: (cpu * 10).rounded() / 10, load: ((cores * cpu / 100 * (0.9 + 0.3 * h)) * 100).rounded() / 100,
                mem: (memFrac * memory).rounded(), swap: (p.swap * Self.gb).rounded(), rx: rx.rounded(), tx: (rx * p.tx * (0.7 + 0.6 * h)).rounded(),
                disk: (p.disk * p.diskTotal * Self.gb).rounded())
    }

    // MARK: Token usage (docs/server/machines.md "Token usage")

    /// Which models and launchers a demo machine runs, and its share of the work.
    private static let usageMix: [String: (scale: Double, runs: [(model: String, launcher: String, share: Double)])] = [
        "server": (1, [("claude-opus-5-5", "claude", 0.62), ("gpt-5.5", "codex", 0.3), ("claude-haiku-5-5", "claude", 0.08)]),
        "macbook": (0.55, [("claude-opus-5-5", "claude", 0.7), ("claude-sonnet-5-5", "sonnetcc", 0.3)]),
        "studio": (0.25, [("claude-opus-5-5", "claude", 1)]),
    ]

    /// A made-up minute of a machine's usage: quiet nights, busy afternoons, one bucket per model and launcher.
    private func usageMinute(_ mid: String, _ minute: Int) -> [(model: String, launcher: String, tokens: UsageTokens)] {
        guard let mix = Self.usageMix[mid], present(mid, minute) else { return [] }
        let hour = Double(minute % 86400) / 3600
        let work = (1...15).contains(hour) ? max(0.02, sin(Double.pi * (hour - 1) / 14)) : 0.03
        let jitter = Double(abs((minute / 60 &* 2_654_435_761 &+ mid.count &* 97) % 1000)) / 1000
        let w = work * (0.5 + jitter) * mix.scale
        return mix.runs.map { r in
            let f = w * r.share
            let codex = r.model.hasPrefix("gpt")
            return (r.model, r.launcher, UsageTokens(input: Int64(2600 * f * (0.5 + jitter)), cacheWrite: codex ? 0 : Int64(21000 * f * (0.4 + jitter)),
                                                     cacheRead: Int64(310_000 * f * (codex ? 0.8 : 1)), output: Int64(5400 * f * (0.6 + jitter)),
                                                     requests: max(1, Int(f * 6))))
        }
    }

    private static func json(_ t: UsageTokens) -> [String: JSONValue] {
        ["in": .number(Double(t.input)), "cache_write": .number(Double(t.cacheWrite)), "cache_read": .number(Double(t.cacheRead)),
         "out": .number(Double(t.output)), "req": .number(Double(t.requests))]
    }

    /// GET /api/v1/machines/{id}/usage, like the manager's.
    private func usage(_ mid: String, range: String, model: String?, launcher: String?) -> JSONValue {
        let (span, step) = ["1h": (3600, 60), "7d": (7 * 86400, 3600)][range] ?? (86400, 600)
        let now = Int(Date.now.timeIntervalSince1970)
        let first = (now - span) / step * step + step
        let wantLauncher = launcher == "-" ? "" : launcher
        var points: [JSONValue] = []
        var total = UsageTokens()
        var models: [String: UsageTokens] = [:], launchers: [String: UsageTokens] = [:]
        // A week by minute is 10,000 minutes: sample each step's middle minute, scaled to the step.
        let perStep = max(1, step / 60)
        var t = first
        while t <= now / step * step {
            var sum = UsageTokens()
            let sample = usageMinute(mid, t + step / 2 / 60 * 60)
            for r in sample {
                let scaled = UsageTokens(input: r.tokens.input * Int64(perStep), cacheWrite: r.tokens.cacheWrite * Int64(perStep),
                                         cacheRead: r.tokens.cacheRead * Int64(perStep), output: r.tokens.output * Int64(perStep),
                                         requests: r.tokens.requests * perStep)
                if wantLauncher == nil || r.launcher == wantLauncher { models[r.model, default: UsageTokens()] = models[r.model, default: UsageTokens()] + scaled }
                if model == nil || r.model == model { launchers[r.launcher, default: UsageTokens()] = launchers[r.launcher, default: UsageTokens()] + scaled }
                if (model == nil || r.model == model) && (wantLauncher == nil || r.launcher == wantLauncher) { sum = sum + scaled }
            }
            total = total + sum
            var p = Self.json(sum)
            p["t"] = .number(Double(t))
            if sample.isEmpty, Self.usageMix[mid] != nil, !present(mid, t + step - 60) { p["gap"] = true }
            points.append(.object(p))
            t += step
        }
        let shares = { (m: [String: UsageTokens]) -> JSONValue in
            .array(m.sorted { $0.value.total > $1.value.total }.map { name, v in .object(Self.json(v).merging(["name": .string(name)]) { a, _ in a }) })
        }
        return ["machine_id": .string(mid), "online": .bool(online(mid)), "range": .string(range), "step": .number(Double(step)),
                "points": .array(points), "total": .object(Self.json(total)), "models": shares(models), "launchers": shares(launchers),
                "launchers_known": true, "stored": true]
    }

    /// A machine's tokens on its card.
    private func usageCard(_ mid: String) -> JSONValue? {
        guard Self.usageMix[mid] != nil else { return nil }
        let now = Int(Date.now.timeIntervalSince1970)
        let minute = now / 60 * 60
        var today = UsageTokens()
        var t = now / 86400 * 86400
        while t <= minute { // every 10 minutes, scaled: enough for a demo's total
            for r in usageMinute(mid, t) {
                today = today + UsageTokens(input: r.tokens.input * 10, cacheWrite: r.tokens.cacheWrite * 10, cacheRead: r.tokens.cacheRead * 10,
                                            output: r.tokens.output * 10, requests: r.tokens.requests * 10)
            }
            t += 600
        }
        let recent = (0..<60).map { i in usageMinute(mid, minute - (59 - i) * 60).reduce(Int64(0)) { $0 + $1.tokens.total } }
        return ["today": .object(Self.json(today)), "per_minute": .number(Double(recent.suffix(3).reduce(0, +) / 3)),
                "recent": .array(recent.map { .number(Double($0)) })]
    }

    private func metrics(_ mid: String, span: Int, step: Int) -> JSONValue {
        let now = Int(Date.now.timeIntervalSince1970)
        var points: [JSONValue] = []
        var missing = 0
        if Self.profiles[mid] != nil {
            var t = (now - span) / step * step
            while t <= now {
                defer { t += step }
                guard present(mid, t) else {
                    missing += 1
                    if missing > 3, points.last?["gap"]?.bool != true { points.append(["t": .number(Double(t - (missing - 1) * step)), "gap": true]) }
                    continue
                }
                missing = 0
                let v = reading(mid, t)
                points.append(["t": .number(Double(t)), "cpu": .number(v.cpu), "load": .number(v.load), "mem": .number(v.mem),
                               "swap": .number(v.swap), "rx": .number(v.rx), "tx": .number(v.tx), "disk": .number(v.disk)])
            }
        }
        var latest: JSONValue = .null
        let t = now / 10 * 10
        if let p = Self.profiles[mid], present(mid, t) {
            let v = reading(mid, t)
            var o: [String: JSONValue] = ["t": .number(Double(t)), "cpu": .number(v.cpu), "load": .number(v.load), "mem_used": .number(v.mem),
                                          "mem_total": system(mid)?["memory"] ?? 0, "swap_used": .number(v.swap),
                                          "swap_total": .number(v.swap > 0 ? 4 * Self.gb : 0), "net_rx": .number(v.rx), "net_tx": .number(v.tx),
                                          "disk_used": .number(v.disk), "disk_total": .number(p.diskTotal * Self.gb)]
            if mid == "server" {
                let cores = Int(system(mid)?["cores"]?.double ?? 16)
                var perCore: [JSONValue] = []
                for i in 0..<cores {
                    let spread = 9.0 * Double((i * 37) % 7 - 3)
                    let pct: Double = min(100, max(1, v.cpu + spread))
                    perCore.append(.number((pct * 10).rounded() / 10))
                }
                o["cores"] = .array(perCore)
            }
            latest = .object(o)
        }
        let m = machines.first { $0["id"]?.string == mid }
        return ["machine_id": .string(mid), "online": m?["online"] ?? false, "system": m?["system"] ?? .null, "latest": latest,
                "points": .array(points)]
    }
}

private struct WeakSocket {
    weak var socket: DemoSocket?
}

/// The demo hub's end of the app's socket: one per `LiveConnection.open`.
@MainActor
final class DemoSocket: LocalSocket {
    private var deliver: ((Data) -> Void)?
    private var playing: Task<Void, Never>?

    func open(deliver: @escaping (Data) -> Void) {
        self.deliver = deliver
        DemoHub.shared.attach(self)
    }

    func receive(_ message: [String: Any]) {
        // Through JSON, not JSONValue.from: a boxed 0 or 1 would come out a Bool.
        guard let data = try? JSONSerialization.data(withJSONObject: message),
              let msg = try? JSON.decoder.decode(JSONValue.self, from: data) else { return }
        DemoHub.shared.received(msg, from: self)
    }

    func close() {
        deliver = nil
        stopPlaying()
    }

    /// Like a socket, later: never inside the call that sent the message (the app is still in the middle of it).
    func send(_ msg: JSONValue) {
        guard let data = try? JSON.encoder.encode(msg) else { return }
        DispatchQueue.main.async { [weak self] in self?.deliver?(data) }
    }

    /// The terminal: a made-up Claude screen, its spinner ticking once a second (mock_hub.terminal_frames).
    func play(_ k: SessionKey, frames: [JSONValue]) {
        stopPlaying()
        playing = Task { [weak self] in
            for f in frames {
                guard let self, !Task.isCancelled, case .object(var o) = f else { return }
                o["type"] = "terminal"
                o["machine_id"] = .string(k.machineID)
                o["session_id"] = .string(k.sessionID)
                o["epoch"] = "demo"
                self.send(.object(o))
                try? await Task.sleep(for: .seconds(1))
            }
        }
    }

    func stopPlaying() {
        playing?.cancel()
        playing = nil
    }
}

nonisolated extension JSONValue: ExpressibleByStringLiteral, ExpressibleByBooleanLiteral, ExpressibleByIntegerLiteral,
    ExpressibleByDictionaryLiteral, ExpressibleByArrayLiteral {
    init(stringLiteral value: String) { self = .string(value) }
    init(booleanLiteral value: Bool) { self = .bool(value) }
    init(integerLiteral value: Int) { self = .number(Double(value)) }
    init(dictionaryLiteral elements: (String, JSONValue)...) { self = .object(Dictionary(elements, uniquingKeysWith: { $1 })) }
    init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
}
