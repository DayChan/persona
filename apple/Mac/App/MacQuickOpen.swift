import Foundation

/// ⌘K Quick Open (docs/apple/mac.md「搜索」): one field over the agents, the Persona conversations, the open conversation's
/// files (`MacFileFind`), the inbox, the window's sections and the two "new" actions, like Xcode's Open Quickly. ↑↓ choose, ⏎ opens, Esc closes.
enum MacQuickOpen {
    enum Target: Hashable {
        case agent(SessionKey)
        case conversation(SessionKey)
        /// An inbox item, by its `InboxItem.key`.
        case inbox(String)
        /// A file under the open conversation's directory, relative to it.
        case file(String)
        case section(MacSection)
        case newAgent
        case newConversation
    }

    struct Hit: Identifiable, Hashable {
        var id: String
        var title: String
        var subtitle: String
        var icon: String
        var state: AgentState?
        var target: Target
        var score: Int
        var date: Date?
        /// Waiting on you: listed before the rest of the same score.
        var urgent = false
        /// A file: UTF-16 offsets into its relative path of the characters the query matched.
        var marks: [Int] = []
    }

    struct Group: Identifiable, Hashable {
        var id: String { title }
        var title: String
        var hits: [Hit]
    }

    static let agentLimit = 30
    static let otherLimit = 10

    /// Title starts with the query 100, contains it 80, every word somewhere (title, folder, machine, space, last
    /// line) 40, else no match. An empty query matches everything with 1.
    static func score(title: String, hay: [String?], query q: String) -> Int {
        if q.isEmpty { return 1 }
        let t = title.lowercased()
        if t.hasPrefix(q) { return 100 }
        if t.contains(q) { return 80 }
        let h = (hay.compactMap { $0 } + [title]).joined(separator: " ").lowercased()
        let words = q.split(whereSeparator: \.isWhitespace).map(String.init)
        return !words.isEmpty && words.allSatisfy { h.contains($0) } ? 40 : 0
    }

    /// What a file hit counts as against the other groups: above an agent that only has the words somewhere, below
    /// one whose title has them.
    static let fileScore = 60

    /// The groups to list, empty ones dropped: Agent, Persona Chat, Files, Inbox, Go, Actions — unless a later group has
    /// a better kind of match (typing Settings puts the section above an agent that merely mentions it). `files`: what
    /// `MacFileFind` matched in the open conversation's directory, best first.
    static func sections(_ query: String, sessions: [Session], machines: [Machine], inbox: [InboxItem],
                         files: [MacFileFind.Hit] = [], supportsTodos: Bool = true,
                         showsMessages: Bool = false) -> [Group] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let names = Dictionary(machines.map { ($0.id, $0.name) }, uniquingKeysWith: { a, _ in a })
        var agents: [Hit] = [], conversations: [Hit] = []
        for s in sessions {
            let machine = names[s.machineID] ?? s.machineID
            let sc = score(title: s.displayTitle, hay: [s.cwd, s.spaceName, machine, s.lastText], query: q)
            guard sc > 0 else { continue }
            if s.assistant {
                conversations.append(Hit(id: "j:\(s.machineID)/\(s.sessionID)", title: s.displayTitle, subtitle: "Persona",
                                         icon: Theme.Icon.ai, state: s.state, target: .conversation(s.key), score: sc,
                                         date: s.updatedAt, urgent: s.state == .needsYou))
            } else {
                let place = [machine, s.spaceName ?? s.folderName].filter { !$0.isEmpty }.joined(separator: " › ")
                agents.append(Hit(id: "a:\(s.machineID)/\(s.sessionID)", title: s.displayTitle, subtitle: place,
                                  icon: Theme.Icon.agents, state: s.state, target: .agent(s.key), score: sc,
                                  date: s.updatedAt, urgent: s.state == .needsYou))
            }
        }
        let mail = inbox.compactMap { it -> Hit? in
            let sc = score(title: it.title, hay: [it.sender, it.preview, it.accountName], query: q)
            guard sc > 0 else { return nil }
            return Hit(id: "i:\(it.key)", title: it.title, subtitle: [it.sender, it.preview].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: String(localized: ": ", comment: "Separator between two parts of a one-line summary, e.g. a tool name and what it does")),
                       icon: Theme.Icon.inbox, target: .inbox(it.key), score: sc, date: it.updatedAt)
        }
        let fileHits = q.isEmpty ? [] : files.map { f -> Hit in
            let cut = f.path.lastIndex(of: "/")
            let name = cut.map { String(f.path[f.path.index(after: $0)...]) } ?? f.path
            let folder = cut.map { String(f.path[..<$0]) } ?? ""
            return Hit(id: "f:\(f.path)", title: name, subtitle: folder, icon: Theme.Icon.file, target: .file(f.path),
                       score: fileScore, marks: f.matched)
        }
        let groups = [
            Group(title: "Agent", hits: Array(ranked(agents).prefix(agentLimit))),
            Group(title: String(localized: "Persona Chats"), hits: Array(ranked(conversations).prefix(otherLimit))),
            Group(title: String(localized: "Files"), hits: fileHits),
            Group(title: String(localized: "Inbox"), hits: Array(ranked(mail).prefix(otherLimit))),
            Group(title: String(localized: "Go [New Chat View fa9607]"), hits: places(q, supportsTodos: supportsTodos, showsMessages: showsMessages)),
            Group(title: String(localized: "Actions [Mac Quick Open be37d8]"), hits: actions(q)),
        ].filter { !$0.hits.isEmpty }
        let tier = { (g: Group) in (g.hits.map(\.score).max() ?? 0) / 20 }
        return groups.enumerated().sorted { a, b in
            let (x, y) = (tier(a.element), tier(b.element))
            return x != y ? x > y : a.offset < b.offset
        }.map(\.element)
    }

    /// Best match first, then what waits on you, then newest; ties keep the list's order.
    private static func ranked(_ hits: [Hit]) -> [Hit] {
        hits.enumerated().sorted { a, b in
            let (x, y) = (a.element, b.element)
            if x.score != y.score { return x.score > y.score }
            if x.urgent != y.urgent { return x.urgent }
            let (dx, dy) = (x.date ?? .distantPast, y.date ?? .distantPast)
            return dx != dy ? dx > dy : a.offset < b.offset
        }.map(\.element)
    }

    /// The window's sections, with the other words they answer to.
    private static func places(_ q: String, supportsTodos: Bool, showsMessages: Bool) -> [Hit] {
        let words: [MacSection: [String]] = [
            .assistant: ["persona", "assistant", "助手", "主对话"], // i18n-ignore: search alias
            .agents: ["agents", "claude", "codex", "会话"], // i18n-ignore: search alias
            .inbox: ["inbox", "mail", "lark", "邮件", "飞书"], // i18n-ignore: search alias
            .todos: ["todo", "todos", "待办"], // i18n-ignore: search alias
            .messages: ["messages", "chats", "lark", "feishu", "飞书", "私聊", "群聊", "会话"], // i18n-ignore: search alias
            .knowledge: ["knowledge", "kb", "项目", "人", "文档", "会议"], // i18n-ignore: search alias
            .machines: ["machines", "monitor", "电脑", "监控", "cpu", "内存"], // i18n-ignore: search alias
            .settings: ["settings", "preferences", "偏好"], // i18n-ignore: search alias
        ]
        return MacSection.visible(supportsTodos: supportsTodos, showsMessages: showsMessages).compactMap { s in
            let sc = score(title: s.title, hay: words[s] ?? [], query: q)
            guard sc > 0 else { return nil }
            return Hit(id: "p:\(s.rawValue)", title: s.title, subtitle: "", icon: s.icon, target: .section(s), score: sc)
        }
    }

    private static func actions(_ q: String) -> [Hit] {
        let all: [(String, String, String, Target, [String])] = [
            ("a:new-agent", String(localized: "New Agent [Mac Quick Open 0587b9]"), String(localized: "Start Claude Code / Codex on a computer  ⌘N"), .newAgent,
             ["new agent", "start", "启动", "claude", "codex"]), // i18n-ignore
            ("a:new-persona", String(localized: "New Persona Chat"), String(localized: "Ask Persona  ⇧⌘N"), .newConversation,
             ["new chat", "persona", "对话", "助手"]), // i18n-ignore
        ]
        return all.compactMap { id, title, sub, target, words in
            let sc = score(title: title, hay: words, query: q)
            guard sc > 0 else { return nil }
            return Hit(id: id, title: title, subtitle: sub, icon: Theme.Icon.compose, target: target, score: sc)
        }
    }

    /// ↑ / ↓, wrapping around.
    static func move(_ index: Int, by delta: Int, count: Int) -> Int {
        guard count > 0 else { return 0 }
        return ((index + delta) % count + count) % count
    }
}
