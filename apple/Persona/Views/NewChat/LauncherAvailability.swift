import Foundation

/// Why New Agent grays out a launcher on the computer picked (#1643, docs/server/launcher-health.md): its health there is
/// blocked. It stays listed with the reason, and choosing it shows how to fix it rather than picking it; the help's Try
/// Anyway picks it after all (a start with it that works clears the record). A failing or repairing launcher is still
/// offered as before: the hub is not done with it.
nonisolated struct LauncherBlock: Hashable, Sendable {
    /// The failure's class (`LauncherHealth.kind`); "" when the record has none.
    var kind: String
    /// The record's raw text (redacted by the hub), shown under Details; nil when it has none.
    var detail: String?

    /// The new terminal tab's shell could not run it, though the login shell the node checks in can (#1642).
    var notInTerminal: Bool { kind == LauncherHealth.notInTerminalKind }

    /// The one line under its name.
    var reason: String {
        switch kind {
        case LauncherHealth.notInTerminalKind: String(localized: "Not available in this computer's terminal")
        case "auth": String(localized: "Its key or sign-in was rejected")
        case "model_route": String(localized: "Its gateway can't use this model")
        case "cli_outdated": String(localized: "Its CLI is too old")
        case "timeout": String(localized: "Didn't answer in its last check")
        default: String(localized: "Failed its last check on this computer")
        }
    }

    /// The help's title.
    var title: String {
        notInTerminal ? String(localized: "Not Available in the Terminal") : String(localized: "Unavailable on This Computer")
    }
}

extension Launcher {
    /// Why New Agent grays it out on the computer its list is for; nil when it can be picked (never checked, working,
    /// or still being retried / repaired by the hub).
    nonisolated var block: LauncherBlock? {
        guard let h = health, h.state == LauncherHealth.blockedState else { return nil }
        let detail = h.error?.trimmingCharacters(in: .whitespacesAndNewlines)
        return LauncherBlock(kind: h.kind ?? "", detail: detail?.isEmpty == false ? detail : nil)
    }
}

/// New Agent's launcher list (`LauncherPicker`) and the pick it starts with.
nonisolated enum LauncherChoices {
    /// The rows for a search: every launcher whose name or model has the query, blocked ones too (grayed with why,
    /// never hidden, the decision in #1643).
    static func shown(_ launchers: [Launcher], query: String) -> [Launcher] {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return launchers }
        return launchers.filter { $0.name.localizedCaseInsensitiveContains(q) || ($0.model ?? "").localizedCaseInsensitiveContains(q) }
    }

    /// The grayed-out launchers under their reasons, for a menu that can only title a section (Run Agent Here's picker):
    /// reasons in the order their first launcher is listed, launchers as listed.
    static func blockedByReason(_ launchers: [Launcher]) -> [(reason: String, launchers: [Launcher])] {
        var out: [(reason: String, launchers: [Launcher])] = []
        for l in launchers {
            guard let reason = l.block?.reason else { continue }
            if let i = out.firstIndex(where: { $0.reason == reason }) { out[i].launchers.append(l) } else { out.append((reason, [l])) }
        }
        return out
    }

    /// The launcher a computer's sheet starts with when it has no default to follow: the one used there last unless it is
    /// blocked now, else the first that is not; only when every one is blocked, the last one (or the first), grayed
    /// reason and all on the row, rather than nothing to start with.
    static func initial(saved: String?, in launchers: [Launcher]) -> String {
        if let saved, let l = launchers.first(where: { $0.name == saved }), l.block == nil { return saved }
        if let l = launchers.first(where: { $0.block == nil }) { return l.name }
        if let saved, launchers.contains(where: { $0.name == saved }) { return saved }
        return launchers.first?.name ?? ""
    }
}
