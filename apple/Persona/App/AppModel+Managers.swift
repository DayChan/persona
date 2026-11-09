import Foundation

// Several managers on one hub (docs/assistant/managers.md "Clients"), as the app reads them outside Settings › Managers:
// each group's roster, read when its hub says hello and on each of its `managers` pushes. Pages that group a list read
// their own (`ManagerRoster.read`); this one is for what is decided outside such a page: whose copy of an extension a
// push is about, the inbox's groups, a computer whose manager is offline. With one installed manager none of it shows.

extension AppModel {
    /// The roster of the hub-level group's hub; nil from a hub before managers, or until it is read.
    var managerRoster: ManagerRoster? { managerRosters[hub?.group ?? ""] }

    /// Reads group's roster again. A hub without one (404) has none; one that can't be read now keeps the last.
    func refreshManagerRoster(_ group: String) async {
        guard let api = api(group) else { return }
        let read = managerRosterReads[group, default: 0] + 1
        managerRosterReads[group] = read
        let roster = await ManagerRoster.read(api, keeping: managerRosters[group])
        guard read == managerRosterReads[group], managerRosters[group] != roster else { return }
        managerRosters[group] = roster
    }

    /// This device's prefs as a page just saved them, for what reads them here (the Mac's banners, the Live
    /// Activities); a prefs read already on its way doesn't put the old ones back.
    func savedNotifyPrefs(_ prefs: NotifyPrefs) {
        notifyPrefsWrites += 1
        LiveActivityController.shared.settings(prefs)
    }

    /// Whose copy a hub-level push is about, as the pages name managers (`ManagerRoster.named`): nil for the default
    /// manager's, else its id. A push without `manager` is the hub's own assistant's, the default until the roster says
    /// otherwise.
    func pushedBy(_ manager: String?) -> String? { ManagerRoster.pushed(by: manager, defaultID: managerRoster?.defaultID) }

    /// The manager of `machine` while a hub with several managers has that manager offline: the computer comes back
    /// with it, and Reconnect, which goes through it, can't work meanwhile. nil otherwise.
    func offlineManager(of machine: Machine?, group: String) -> HubManager? {
        guard let machine, !machine.online else { return nil }
        return managerRosters[group]?.offlineManager(of: machine.manager)
    }
}

extension ManagerRoster {
    /// `AppModel.pushedBy` with the default manager's id, nil until a roster is read.
    static func pushed(by manager: String?, defaultID: String?) -> String? {
        let id = manager ?? assistant
        return id == (defaultID ?? assistant) ? nil : id
    }

    /// The installed manager a thing stamped `manager` belongs to, when it is offline on a hub with several managers;
    /// nil otherwise (with one manager the pages show none of it).
    func offlineManager(of manager: String?) -> HubManager? {
        guard isMulti else { return nil }
        let id = owner(manager)
        return installed.first { $0.id == id && !$0.online }
    }
}
