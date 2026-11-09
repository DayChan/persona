import BeautifulUI
import SwiftUI

// Pages grouped by manager (docs/assistant/managers.md "Grouping"; the web's groupByManager, Android's ManagerRoster.groups):
// with more than one installed manager, a merged list's items go under each manager's name, the default first and then
// the roster's order, empty groups left out. An item without `manager`, or with one the roster doesn't have installed, is
// the default's. A request for another manager's item names that manager (`APIClient.manager(_:)`); the default's
// requests name none, as before. With one manager, or a hub without a roster, nothing groups and nothing is named.

/// The hub's roster as a grouped page reads it (GET /api/v1/managers).
nonisolated struct ManagerRoster: Equatable, Sendable {
    /// The hub's own assistant: the default when the roster names none.
    static let assistant = "assistant"

    var managers: [HubManager]
    /// The default manager: the roster's `default`, else the one marked default, else the hub's own assistant.
    var defaultID: String

    init(managers: [HubManager], defaultID: String? = nil) {
        self.managers = managers
        self.defaultID = defaultID.flatMap { $0.isEmpty ? nil : $0 } ?? managers.first(where: \.isDefault)?.id ?? Self.assistant
    }

    init(_ list: APIClient.ManagerList) { self.init(managers: list.managers, defaultID: list.defaultID) }

    /// The installed managers (one still installing isn't one yet): the default first, then the roster's order.
    var installed: [HubManager] {
        let all = managers.filter { $0.installing == nil }
        return all.filter { $0.id == defaultID } + all.filter { $0.id != defaultID }
    }

    /// More than one installed manager: the pages group by manager.
    var isMulti: Bool { installed.count > 1 }

    /// Whose an item stamped `manager` is: that manager while the roster has it installed, else the default.
    func owner(_ manager: String?) -> String {
        guard let manager, !manager.isEmpty, installed.contains(where: { $0.id == manager }) else { return defaultID }
        return manager
    }

    /// The manager the requests for an item stamped `manager` name; nil for the default's, which name none.
    func named(_ manager: String?) -> String? {
        let id = owner(manager)
        return id == defaultID ? nil : id
    }

    /// `items` under their managers, the default first, empty groups left out; nil with one manager (nothing groups).
    func groups<T>(_ items: [T], manager: (T) -> String?) -> [ManagerGroup<T>]? {
        guard isMulti else { return nil }
        var byOwner: [String: [T]] = [:]
        for item in items { byOwner[owner(manager(item)), default: []].append(item) }
        return installed.compactMap { m in byOwner[m.id].map { ManagerGroup(manager: m, items: $0) } }
    }

    /// The roster for a page about to group: nil from a hub without one (404 with no code), the page's last one when
    /// it can't be read now.
    static func read(_ api: APIClient, keeping last: ManagerRoster?) async -> ManagerRoster? {
        do {
            return ManagerRoster(try await api.managers())
        } catch let e as APIError where e.status == 404 && e.code == nil {
            return nil
        } catch {
            return last
        }
    }
}

extension ManagerRoster {
    /// The name of manager `id`, for the page of one of its things to say whose it is; nil for the default's page
    /// (`id` nil), or when the roster can't be read or lacks it.
    static func name(of id: String?, _ api: APIClient?) async -> String? {
        guard let id, let api, let roster = await read(api.manager(nil), keeping: nil) else { return nil }
        return roster.managers.first { $0.id == id }?.name
    }
}

/// One manager's share of a grouped list.
nonisolated struct ManagerGroup<T>: Identifiable {
    var manager: HubManager
    var items: [T]
    var id: String { manager.id }
}

/// A group's heading on a page grouped by manager: its dot, its name and how many, and while it is offline when it last
/// synced (from the roster's `last_seen`). The name as the user wrote it, never in capitals.
struct ManagerHeading: View {
    var manager: HubManager
    var count: Int

    var body: some View {
        let status = ManagersText.status(manager, now: .now)
        let offline = manager.online ? nil : status.text
        Group {
            if AppStyle.current.usesBeautifulUI {
                BUISectionHeading(manager.name, count: count) {
                    if let offline {
                        HStack(spacing: Theme.Space.sm) {
                            managerDot(status.tone)
                            Text(offline).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                }
            } else {
                HStack(spacing: Theme.Space.sm) {
                    managerDot(status.tone)
                    Text(manager.name).lineLimit(1).layoutPriority(1)
                    Text("\(count)").monospacedDigit().foregroundStyle(.secondary)
                    Spacer(minLength: Theme.Space.s)
                    if let offline { Text(offline).lineLimit(1) }
                }
            }
        }
        .textCase(nil)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier("manager-heading-\(manager.id)")
    }
}
