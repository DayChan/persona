import Foundation

/// A message Persona typed into a conversation for the model, as if the user had (docs/protocol/protocol.md「Persona 注入的消息」):
/// a quiet task's notice, a scheduled run, a wake-up, the background tasks a node restart stopped. The model reads its
/// words; the apps show `line` in their place, centered, never a user bubble and never the instructions inside. The hub
/// marks such items (`Item.injected`); an older hub does not, and `Injected.parse` reads the text the way the hub does
/// (`prompts.ParseInjected`; the shared cases are `spec/fixtures/injected`).
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

    enum CodingKeys: String, CodingKey {
        case kind, state, title, machine, line, detail
        case todoID = "todo_id"
    }

    init(kind: String, state: String? = nil, title: String? = nil, machine: String? = nil, todoID: String? = nil,
         line: String, detail: String? = nil) {
        self.kind = kind
        self.state = state
        self.title = title
        self.machine = machine
        self.todoID = todoID
        self.line = line
        self.detail = detail
    }

    var isTaskNotice: Bool { kind == "task_notice" }
    var isFollowup: Bool { kind == "followup" }

    /// The item as one of Persona's messages for the model: the hub's mark, else read off its text. A line an app sent
    /// (`client_msg_id`) or another session did (`from`) is the user's whatever it says.
    static func of(_ item: Item) -> Injected? {
        guard item.role == "user", item.kind == .text else { return nil }
        if MessageSource.protectsUser(item.text ?? "") { return nil }
        if let marked = item.injected { return marked }
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
