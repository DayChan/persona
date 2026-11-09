import Foundation

/// Pure, round-wide visibility policy shared by every chat UI. Pass all items in the round (including continuations
/// after recaps), and a result index from the complete loaded transcript: a receipt can arrive after the next input.
nonisolated enum Supervision {
    /// Only these bare tools or tools in Persona's exact MCP namespaces count, never unrelated servers with the same
    /// suffix. Plugin namespaces and the compatibility server id follow the web's `supervisionTool` contract.
    static func tool(_ name: String?) -> String? {
        guard var name else { return nil }
        if name.hasPrefix("mcp__") {
            let rest = String(name.dropFirst(5))
            guard let separator = rest.range(of: "__", options: .backwards) else { return nil }
            var server = String(rest[..<separator.lowerBound])
            if server.hasPrefix("plugin_") { server = String(server.dropFirst(7)) }
            guard server == "persona" || server == LegacyNames.product else { return nil }
            name = String(rest[separator.upperBound...])
        }
        switch name {
        case "notify_user", "ask_user", "supervision_finish", "task_review": return name
        default: return nil
        }
    }

    static func visible(_ items: [Item], results: [String: Item], live: Bool) -> Bool {
        if items.contains(where: { it in
            it.supervisionUser == true || it.fold?.supervisionUser == true ||
                (Injected.of(it).map { $0.isSupervision && $0.level == "user" } ?? false)
        }) { return true }
        for use in items where use.kind == .toolUse {
            guard let name = tool(use.toolName) else { continue }
            guard let result = use.toolUseID.flatMap({ results[$0] }) else {
                // ask_user needs the person before it can return; only a live pending question is visible.
                if name == "ask_user", live { return true }
                continue
            }
            guard result.isError != true else { continue }
            if name == "task_review" {
                if case .string(let verdict)? = use.toolInput?["verdict"], verdict == "accepted" || verdict == "needs_user" { return true }
            } else if name != "supervision_finish" {
                return true
            } else if let text = result.text, let receipt = try? JSONDecoder().decode(JSONValue.self, from: Data(text.utf8)),
                      case .string("user")? = receipt["visibility"] {
                return true // Only the successful receipt, never the call's requested visibility or prose.
            }
        }
        return false
    }
}

/// A message Persona typed into a conversation for the model, as if the user had (docs/protocol/protocol.md「Persona 注入的消息」):
/// a quiet task's notice, a scheduled run, a wake-up, the background tasks a node restart stopped. The model reads its
/// words; the apps show `line` in their place, never a user bubble or the instructions inside. The hub marks such items
/// (`Item.injected`); for an older hub, `Injected.parse` reads the exact templates (`prompts.ParseInjected`).
nonisolated struct Injected: Codable, Hashable, Sendable {
    /// task_notice | followup | system
    var kind: String
    /// task_notice: done / needs / interrupted / failed; followup: once / daily / interval / wake; system: lost_tasks.
    var state: String?
    /// The task's name, the scheduled task's, or what a woken agent waits for.
    var title: String?
    /// task_notice: the computer its agent runs on (a name).
    var machine: String?
    /// task_notice: the task, when the hub could tell which.
    var todoID: String?
    /// The words shown for it.
    var line: String
    /// followup (once / daily / interval): what the task asks, shown when the line is opened.
    var detail: String?
    /// supervise: triage (quiet unless promoted) or user. Older hub marks omit it; `of` defaults them to triage.
    var level: String?

    enum CodingKeys: String, CodingKey {
        case kind, state, title, machine, line, detail, level
        case todoID = "todo_id"
    }

    init(kind: String, state: String? = nil, title: String? = nil, machine: String? = nil, todoID: String? = nil,
         line: String, detail: String? = nil, level: String? = nil) {
        self.kind = kind
        self.state = state
        self.title = title
        self.machine = machine
        self.todoID = todoID
        self.line = line
        self.detail = detail
        self.level = level
    }

    var isTaskNotice: Bool { kind == "task_notice" }
    var isFollowup: Bool { kind == "followup" }
    var isSupervision: Bool { state == "supervise" }

    /// The item as one of Persona's messages for the model: the hub's mark, else read off its text. A line an app sent
    /// (`client_msg_id`) or another session did (`from`) is the user's whatever it says.
    static func of(_ item: Item) -> Injected? {
        guard item.role == "user", item.kind == .text else { return nil }
        if MessageSource.protectsUser(item.text ?? "") { return nil }
        if var marked = item.injected {
            if marked.isSupervision, marked.level == nil { marked.level = parse(item.text ?? "")?.level ?? "triage" }
            return marked
        }
        guard item.clientMsgID == nil, item.from == nil, let text = item.text else { return nil }
        return parse(text)
    }

    /// What `raw` is when it is one of Persona's messages for the model; nil for anything else (what the user wrote
    /// stays theirs: only the templates' exact shapes are taken).
    static func parse(_ raw: String) -> Injected? { memo(raw, compute) }

    // MARK: Reading the text

    private static let memo = TextMemo<Injected?>(limit: 1024)
    private static let noticeHead = try! NSRegularExpression(pattern: #"^🔔 任务「(.+)」(做完了|在等用户|被模型服务出错打断了|没开起来)(?:（(.+)）)?$"#) // i18n-ignore: matches the hub's text
    private static let followupHead = try! NSRegularExpression(pattern: #"^⏰ (.+)（(你之前安排的跟进|定时任务(?:，每 [^）]+)?)）$"#) // i18n-ignore: matches the hub's text
    private static let wakeHead = try! NSRegularExpression(pattern: #"^⏰（Persona 叫醒你，第 \d+ 次）你上一轮停下时说在等「(.*?)」"#) // i18n-ignore: matches the hub's text
    private static let leadContext = try! NSRegularExpression(pattern: #"^\s*<"# + HubNaming.contextPattern + #"\b[^>]*>[\s\S]*?</"# + HubNaming.contextPattern + ">") // i18n-ignore: matches the hub's text
    private static let supervisionHead = try! NSRegularExpression(pattern: #"^📋 督办：你派出去的任务有 (\d+) 个要你处理$"#) // i18n-ignore: matches the hub's text
    private static let supervisionMarker = "这是 Persona 的督办消息" // i18n-ignore: matches the hub's text
    private static let supervisionLevel = try! NSRegularExpression(pattern: #"(?m)^\s*<persona-supervision level="(triage|user)"\s*/>\s*$"#)
    private static let noticeMarker = "这是 Persona 的任务通知" // i18n-ignore: matches the hub's text
    private static let lostTasksHead = "⚠️ 运行你的 node 刚重启过" // i18n-ignore: matches the hub's text
    private static let kbHintHead = "（知识库" // i18n-ignore: matches the hub's text
    private static let noticeStates = ["做完了": "done", "在等用户": "needs", "被模型服务出错打断了": "interrupted", "没开起来": "failed"] // i18n-ignore: matches the hub's text
    /// A task notice in one sentence per state, so a translation can put the title where it belongs.
    private static func noticeLine(_ state: String, title: String) -> String {
        switch state {
        case "done": String(localized: "Task “\(title)” is done")
        case "needs": String(localized: "Task “\(title)” needs you")
        case "interrupted": String(localized: "Task “\(title)” was interrupted")
        case "failed": String(localized: "Task “\(title)” didn't start")
        default: String(localized: "Task “\(title)”")
        }
    }

    private static func compute(_ raw: String) -> Injected? {
        guard !MessageSource.protectsUser(raw) else { return nil }
        var text = raw
        while let m = leadContext.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)),
              let r = Range(m.range, in: text) {
            text = String(text[r.upperBound...])
        }
        text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // The way Go's strings.Cut and the regexes see it: lines split at \n only (a \r\n stays in the line).
        let head: String, rest: String
        if let nl = text.firstIndex(of: "\n") {
            head = String(text[..<nl])
            rest = String(text[text.index(after: nl)...])
        } else {
            head = text
            rest = ""
        }
        if groups(supervisionHead, head) != nil, rest.contains(supervisionMarker) {
            let level = groups(supervisionLevel, rest)?[0] == "user" ? "user" : "triage"
            return Injected(kind: "system", state: "supervise", line: String(localized: "Supervision [Chat Layout]"), level: level)
        }
        if head.hasPrefix("🔔 任务「") { // i18n-ignore
            guard let g = groups(noticeHead, head), rest.contains(noticeMarker), let state = noticeStates[g[1] ?? ""] else { return nil }
            let title = g[0] ?? ""
            return Injected(kind: "task_notice", state: state, title: title, machine: g[2].flatMap { $0.isEmpty ? nil : $0 },
                            line: noticeLine(state, title: title))
        }
        if head.hasPrefix("⏰（") { // i18n-ignore
            guard let g = groups(wakeHead, head) else { return nil }
            let wait = g[0] ?? ""
            return Injected(kind: "followup", state: "wake", title: wait, line: String(localized: "Persona woke it up to check whether “\(wait)” is ready"))
        }
        if head.hasPrefix("⏰ ") {
            guard let g = groups(followupHead, head) else { return nil }
            let title = g[0] ?? "", what = g[1] ?? ""
            let detail = followupDetail(rest)
            let d = detail.isEmpty ? nil : detail
            if what == "你之前安排的跟进" { // i18n-ignore
                return Injected(kind: "followup", state: "once", title: title, line: String(localized: "Follow-up “\(title)” is due"), detail: d)
            }
            return Injected(kind: "followup", state: what.hasPrefix("定时任务，每 ") ? "interval" : "daily", title: title, // i18n-ignore
                            line: String(localized: "Scheduled task “\(title)” started"), detail: d)
        }
        if head.hasPrefix(lostTasksHead) {
            let n = rest.split(separator: "\n", omittingEmptySubsequences: false).filter { $0.hasPrefix("- ") }.count
            return Injected(kind: "system", state: "lost_tasks", line: n > 0 ? String(localized: "The node restarted; \(n) background tasks stopped") : String(localized: "The node restarted; background tasks stopped"))
        }
        return nil
    }

    /// The capture groups of `re` on the whole of `s` (nil for a group that took no part); nil when it does not match.
    private static func groups(_ re: NSRegularExpression, _ s: String) -> [String?]? {
        guard let m = re.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) else { return nil }
        return (1..<m.numberOfRanges).map { i in Range(m.range(at: i), in: s).map { String(s[$0]) } }
    }

    /// A run's prompt: after the notes under its first line, without the knowledge base hint it may end with.
    private static func followupDetail(_ rest: String) -> String {
        let s = "\n" + rest
        guard let gap = s.range(of: "\n\n") else { return "" }
        var body = String(s[gap.upperBound...]).trimmingCharacters(in: .whitespacesAndNewlines)
        if let hint = body.range(of: "\n\n" + kbHintHead, options: .backwards) {
            body = String(body[..<hint.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
        } else if body.hasPrefix(kbHintHead) {
            body = ""
        }
        return body
    }
}
