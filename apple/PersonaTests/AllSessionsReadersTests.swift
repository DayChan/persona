import Foundation
import Testing

/// Only lists of agents read `AppModel.allSessions` (docs/apple/ios-state.md「第 5 步」): a view that reads it is drawn
/// again by every push about any agent on any machine, which is what steps 1–3 took out of the chat pages, the tab
/// badges and the top bars. A view about one session reads its cell (`model.session(_:)`); an action that needs every
/// agent when it is tapped (the @ menu, the next agent) reads `model.sessionsNow`, which nothing watches. Reads the
/// sources next to this file, like `HoldSourceTests`.
struct AllSessionsReadersTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    /// The files under `Persona/Views` and `Mac` that may read it, how many times, and why. Exact: a read taken out
    /// lowers its number here, a new one needs a reason here.
    private static let allowed: [String: (count: Int, why: String)] = [
        "SpaceView.swift": (1, "a space's agents"),
        "MachineSpacesView.swift": (1, "a machine's spaces and agents"),
        "MachineDetailView.swift": (1, "机器: each machine's agents for its card, through allSessions(inFront:) (behind another section, as they are now)"),
        "InboxActions.swift": (1, "交给 Agent: the agents to pick from"),
        "MacAgentCommands.swift": (1, "Agent › 下一个需要你的 is enabled while one needs you"),
        "MacAttention.swift": (3, "the Dock badge and notifications follow every agent (not a view)"),
        "MacDockMenu.swift": (1, "the Dock menu lists the agents"),
        "MacMenuBar.swift": (1, "the menu bar extra lists the agents (MacMenuBarState, not a view: the label and the menu read its snapshot)"),
        "MacQuickOpenPanel.swift": (1, "快速打开 searches every agent"),
        "TodoHandoffSheet.swift": (1, "hand-off to an Agent: the agents to pick from, as InboxActions does"),
        "TodosView.swift": (2, "the todo list shows each Agent's progress (its latest line), so it follows every Agent, like the Dock and the menu bar"),
        "MacTodosPage.swift": (2, "the Mac todo list shows each Agent's progress (its latest line), like the Dock and the menu bar"),
        "TodoDetailView.swift": (1, "the Agent rows show each Agent's latest line, the same way the todo list does"),
        "TodoCalendarView.swift": (1, "the calendar colors each to-do by its Agents' state, the same way the todo list does (#2029)"),
    ]

    private static func reads(_ text: String) -> Int {
        text.components(separatedBy: "\n")
            .map { l in l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l }
            .reduce(0) { $0 + $1.components(separatedBy: "allSessions").count - 1 }
    }

    /// The folders it reads are there (after a move, a check reading nothing passes for nothing).
    @Test func readsTheSources() {
        let n = (FileManager.default.enumerator(at: Self.apple.appending(path: "Persona/Views"), includingPropertiesForKeys: nil)?
            .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" }.count) ?? 0
        #expect(n > 50)
    }

    @Test func onlyListsOfAgentsReadThemAll() {
        var found: [String: Int] = [:]
        for dir in ["Persona/Views", "Mac"] {
            let files = (FileManager.default.enumerator(at: Self.apple.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL } ?? []).filter { $0.pathExtension == "swift" }
            for file in files {
                let n = (try? String(contentsOf: file, encoding: .utf8)).map(Self.reads) ?? 0
                if n > 0 { found[file.lastPathComponent, default: 0] += n }
            }
        }
        var wrong: [String] = []
        for (name, n) in found.sorted(by: { $0.key < $1.key }) where n != Self.allowed[name]?.count {
            wrong.append("\(name): \(n) (allowed \(Self.allowed[name]?.count ?? 0))")
        }
        for name in Self.allowed.keys.sorted() where found[name] == nil {
            wrong.append("\(name): 0 (allowed \(Self.allowed[name]!.count): take it out of the list)")
        }
        #expect(wrong.isEmpty, """
            Views read `model.allSessions`: one session → `model.session(_:)`; an action → `model.sessionsNow`; \
            a new list of agents → add it to `allowed` with why:
            \(wrong.joined(separator: "\n"))
            """)
    }
}
