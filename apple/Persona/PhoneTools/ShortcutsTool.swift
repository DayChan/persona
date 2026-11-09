import Foundation

/// The user's own shortcuts Persona may run (#2283). Only shortcuts the user allowed on the phone run: registering one
/// asks first, and running one opens the Shortcuts app from the foreground, which comes back to Persona with its
/// result (x-callback-url).
enum ShortcutsTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_shortcuts_list",
                    description: """
                    List the Shortcuts the user allowed Persona to run, with what each is for. Only these can be run \
                    with phone_shortcuts_run.
                    """,
                    inputSchema: Schema.object([:])
                ),
                source: .shortcuts,
                run: { _ in list() }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_shortcuts_register",
                    description: """
                    Ask the user to allow Persona to run one of their Shortcuts by its exact name, with what it is \
                    for. The user approves on the phone; after that phone_shortcuts_run can run it without asking \
                    again.
                    """,
                    inputSchema: Schema.object([
                        "name": Schema.string("The shortcut's exact name in the Shortcuts app."),
                        "purpose": Schema.string("What it does, in a few words, e.g. turns on the evening lights."),
                    ], required: ["name"])
                ),
                source: .shortcuts,
                confirmation: { args in registerConfirmation(args) },
                run: { args in try register(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_shortcuts_forget",
                    description: """
                    Stop allowing Persona to run one of the user's Shortcuts (when the user says so). The shortcut \
                    itself stays in the Shortcuts app.
                    """,
                    inputSchema: Schema.object([
                        "name": Schema.string("The shortcut's name as phone_shortcuts_list shows it."),
                    ], required: ["name"])
                ),
                source: .shortcuts,
                run: { args in try forget(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_shortcuts_run",
                    description: """
                    Run one of the Shortcuts the user allowed (phone_shortcuts_list), optionally with text input. \
                    It opens the Shortcuts app, which comes back to Persona when it finishes: the result is its \
                    output text, cancelled or error, or that no answer came back within 90 seconds (it may still be \
                    running). Persona must be open on the iPhone; nothing runs in the background.
                    """,
                    inputSchema: Schema.object([
                        "name": Schema.string("The shortcut's name as phone_shortcuts_list shows it."),
                        "input": Schema.string("Optional text passed to the shortcut as its input."),
                    ], required: ["name"])
                ),
                source: .shortcuts,
                run: { args in try await run(args) }
            ),
        ]
        #else
        []
        #endif
    }

    // MARK: - Allowed shortcuts

    /// One shortcut the user allowed.
    nonisolated struct Allowed: Codable, Equatable, Sendable {
        var name: String
        var purpose: String?
    }

    /// The allowed shortcuts, kept on this phone (they name the user's own shortcuts; they change rarely).
    enum Allowlist {
        static let key = "phoneShortcuts.allowed"

        static var all: [Allowed] {
            get {
                guard let data = UserDefaults.standard.data(forKey: key),
                      let list = try? JSONDecoder().decode([Allowed].self, from: data) else { return [] }
                return list
            }
            set {
                UserDefaults.standard.set(try? JSONEncoder().encode(newValue), forKey: key)
            }
        }

        /// The allowed shortcut `name` names: exact first, then ignoring case and surrounding spaces.
        static func find(_ name: String, in list: [Allowed]) -> Allowed? {
            list.first { $0.name == name } ?? list.first { $0.name.trimmingCharacters(in: .whitespaces).caseInsensitiveCompare(name) == .orderedSame }
        }
    }

    static func list() -> [ToolContent] {
        let rows = Allowlist.all.map { Output.object([("name", .string($0.name)), ("purpose", Output.opt($0.purpose))]) }
        return Output.json(Output.object([
            ("count", .number(Double(rows.count))),
            ("shortcuts", .array(rows)),
            ("note", rows.isEmpty ? .string("None allowed yet: use phone_shortcuts_register with the shortcut's exact name.") : nil),
        ]))
    }

    static func registerConfirmation(_ args: JSONValue) -> String {
        let name = ToolArgs.string(args, "name") ?? "?"
        var lines = [String(localized: "Let Persona run the shortcut “\(name)”?")]
        if let purpose = ToolArgs.string(args, "purpose") { lines.append(String(localized: "For: \(purpose)")) }
        lines.append(String(localized: "It will run without asking again. You can stop it any time."))
        return lines.joined(separator: "\n")
    }

    static func register(_ args: JSONValue) throws -> [ToolContent] {
        guard let name = ToolArgs.string(args, "name") else { throw ToolFailure(.invalidArgument, "name is required") }
        var list = Allowlist.all
        list.removeAll { $0.name.caseInsensitiveCompare(name) == .orderedSame }
        list.append(Allowed(name: name, purpose: ToolArgs.string(args, "purpose")))
        Allowlist.all = list
        return Output.json(Output.object([("name", .string(name)), ("allowed", .bool(true))]))
    }

    static func forget(_ args: JSONValue) throws -> [ToolContent] {
        guard let name = ToolArgs.string(args, "name") else { throw ToolFailure(.invalidArgument, "name is required") }
        var list = Allowlist.all
        guard let found = Allowlist.find(name, in: list) else {
            throw ToolFailure(.invalidArgument, "\"\(name)\" is not an allowed shortcut")
        }
        list.removeAll { $0 == found }
        Allowlist.all = list
        return Output.json(Output.object([("name", .string(found.name)), ("allowed", .bool(false))]))
    }

    // MARK: - Running

    /// The x-callback-url that runs a shortcut and comes back to Persona with `token`.
    nonisolated enum Link {
        static let callbackHost = "shortcut-callback"

        /// Only unreserved characters stay as they are: a callback URL's own `?`, `=` and `&` must not split the outer query.
        private static let unreserved = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")

        static func encode(_ text: String) -> String {
            text.addingPercentEncoding(withAllowedCharacters: unreserved) ?? text
        }

        static func run(name: String, input: String?, token: String, scheme: String = "persona") -> URL? {
            var pairs: [(String, String)] = [("name", name)]
            if let input {
                pairs.append(("input", "text"))
                pairs.append(("text", input))
            }
            for outcome in ["success", "cancel", "error"] {
                pairs.append(("x-\(outcome)", "\(scheme)://\(callbackHost)/\(outcome)?token=\(encode(token))"))
            }
            let query = pairs.map { "\($0.0)=\(encode($0.1))" }.joined(separator: "&")
            return URL(string: "shortcuts://x-callback-url/run-shortcut?\(query)")
        }

        /// How a callback ended: (token, outcome, text). nil when the URL is not a shortcut callback.
        static func parse(_ url: URL) -> (token: String, outcome: String, text: String?)? {
            guard url.host == callbackHost, let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
                  let token = parts.queryItems?.first(where: { $0.name == "token" })?.value else { return nil }
            let outcome = url.lastPathComponent
            guard ["success", "cancel", "error"].contains(outcome) else { return nil }
            let text = parts.queryItems?.first { $0.name == "result" || $0.name == "errorMessage" }?.value
            return (token, outcome, text)
        }
    }

    #if os(iOS)
    static func run(_ args: JSONValue) async throws -> [ToolContent] {
        guard let name = ToolArgs.string(args, "name") else { throw ToolFailure(.invalidArgument, "name is required") }
        guard let allowed = Allowlist.find(name, in: Allowlist.all) else {
            throw ToolFailure(.invalidArgument, "\"\(name)\" is not a shortcut the user allowed; ask first with phone_shortcuts_register")
        }
        try Foreground.require("run a shortcut")
        let token = UUID().uuidString
        guard let url = Link.run(name: allowed.name, input: ToolArgs.string(args, "input"), token: token) else {
            throw ToolFailure(.invalidArgument, "cannot build a Shortcuts link for \"\(allowed.name)\"")
        }
        let waiting = ShortcutCallbacks.shared.expect(token)
        guard await Foreground.open(url) else {
            ShortcutCallbacks.shared.cancel(token)
            throw ToolFailure(.unavailable, "iOS did not open the Shortcuts app (is it installed?).")
        }
        let answer = await waiting.value
        var pairs: [(String, JSONValue?)] = [("name", .string(allowed.name))]
        switch answer {
        case .some(let (outcome, text)):
            pairs.append(("result", .string(outcome == "success" ? "finished" : outcome == "cancel" ? "cancelled" : "error")))
            pairs.append(("output", Output.opt(text)))
        case .none:
            pairs.append(("result", .string("no_answer")))
            pairs.append(("note", .string("Shortcuts did not come back within 90 seconds; it may still be running or may have stopped. Ask the user.")))
        }
        return Output.json(Output.object(pairs))
    }
    #endif
}

#if os(iOS)
/// Shortcut runs waiting for Shortcuts to call back (`persona://shortcut-callback/…`, handled in PersonaApp's onOpenURL).
final class ShortcutCallbacks {
    static let shared = ShortcutCallbacks()
    static let wait: Duration = .seconds(90)

    private var pending: [String: CheckedContinuation<(String, String?)?, Never>] = [:]

    /// Starts waiting for `token`'s callback; the task answers nil after `wait`.
    func expect(_ token: String) -> Task<(String, String?)?, Never> {
        Task { @MainActor in
            await withCheckedContinuation { continuation in
                pending[token] = continuation
                Task { @MainActor [weak self] in
                    try? await Task.sleep(for: Self.wait)
                    self?.finish(token, with: nil)
                }
            }
        }
    }

    func cancel(_ token: String) { finish(token, with: nil) }

    /// True when `url` was a shortcut's callback (consumed here).
    func handle(_ url: URL) -> Bool {
        guard let callback = ShortcutsTool.Link.parse(url) else { return false }
        finish(callback.token, with: (callback.outcome, callback.text))
        return true
    }

    private func finish(_ token: String, with answer: (String, String?)?) {
        pending.removeValue(forKey: token)?.resume(returning: answer)
    }
}
#endif
