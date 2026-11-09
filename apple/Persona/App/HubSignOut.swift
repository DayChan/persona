import Foundation

// Disconnecting in Settings signs this device out of each hub it forgets (#1824). Only a revoked credential makes the
// hub drop the device's push and Live Activity registrations (hub `Pusher.signedOut`); forgetting the hub here does not.
// The local cleanup runs either way, and a toast names each hub that may still reach the device.

/// What a Settings disconnect is signing this device out of (`AppModel.hubSignOut`): the row that started it shows
/// progress, the disconnect and pairing entries are disabled, and pairing is refused until it ends.
nonisolated enum HubSignOutScope: Equatable, Sendable {
    case all
    case group(String)
    case hub(String)
}

extension AppModel {
    /// Settings › Disconnect and Forget Server / Disconnect All Hubs: every hub.
    func disconnectAll() async {
        await signingOut(registry.hubs, scope: .all) { $0.unpair() }
    }

    /// Settings › hub › Disconnect This Group, and Settings › Devices › Revoke and Unpair: every hub of the group (the
    /// last group: `unpair`).
    func disconnectGroup(_ group: String) async {
        await signingOut(registry.members(group), scope: .group(group)) { $0.removeGroup(group) }
    }

    /// Settings › hub › Disconnect This Hub: that hub alone. The group's push registration and the watch's credential
    /// were made at its first hub: when that one goes, the next first hub gets them. A credential upgrade already
    /// running may still have used the removed hub; the watch then gets its new credential at the next Sync to Apple
    /// Watch or launch.
    func disconnectHub(_ id: String) async {
        guard let hub = registry.hubs.first(where: { $0.id == id }) else { return }
        let wasFirst = registry.primary(hub.group)?.id == id
        await signingOut([hub], scope: .hub(id)) { $0.removeHub(id) }
        guard wasFirst, !registry.hubs.contains(where: { $0.id == id }), link(hub.group) != nil else { return }
        PushRegistrar.shared.registerIfPossible()
        await upgradeCredentials()
    }

    /// Pairing waits while a disconnect runs (`pair`, every way in: Settings, the connection card, pairing links): the
    /// disconnect forgets the hubs of a group or all of them when it ends, and a hub paired meanwhile could go with them
    /// without being signed out.
    func refusePairingWhileSigningOut() throws {
        guard hubSignOut != nil else { return }
        throw APIError(status: 0, message: String(localized: "A hub is being disconnected. Pair again when it's done."))
    }

    /// Signs out of `hubs`, then runs the local cleanup. A second one while one runs does nothing; the entries that
    /// start one are disabled meanwhile (`hubSignOut`).
    private func signingOut(_ hubs: [PairedHub], scope: HubSignOutScope, then forget: (AppModel) -> Void) async {
        guard hubSignOut == nil else { return }
        hubSignOut = scope
        defer { hubSignOut = nil }
        let traded = tradedTokens
        let missed = await Self.signOut(hubs) { hub in
            APIClient(config: DeviceCredentialTrade.current(hub.config, traded: traded), group: hub.group)
        }
        forget(self)
        guard !missed.isEmpty else { return }
        let names = ListFormatter.localizedString(byJoining: missed.map(\.displayName))
        toast = Toast(String(localized: "Couldn't sign out of \(names). Revoke this device in Settings › Devices on another device."),
                      style: .error)
    }

    /// Signs out of every hub at once. Returns the hubs this device or its watch may still be signed in to, in the
    /// order given: out of reach, refusing, or on a credential that cannot be revoked by id. Demo hubs are skipped.
    nonisolated static func signOut(_ hubs: [PairedHub], client: @escaping @Sendable (PairedHub) -> APIClient,
                                    timeout: TimeInterval = 10) async -> [PairedHub] {
        let missed = await withTaskGroup(of: (Int, Bool).self) { group in
            for (i, hub) in hubs.enumerated() where !hub.config.isDemo {
                group.addTask {
                    (i, await Self.signOut(at: client(hub), watch: hub.watchDeviceID, traded: hub.deviceID != nil, timeout: timeout))
                }
            }
            var out = Set<Int>()
            for await (i, done) in group where !done { out.insert(i) }
            return out
        }
        return hubs.indices.filter(missed.contains).map { hubs[$0] }
    }

    /// Revokes this device's credential at one hub (`current` of GET /auth/tokens), and before it the watch's
    /// answer-only one made there. true: the hub holds neither any more, except after a 401 (below), where the watch's
    /// credential can no longer be looked up or revoked and is left as it is. `traded`: this device had traded the
    /// pairing token for its own credential there (`PairedHub.deviceID`).
    nonisolated static func signOut(at api: APIClient, watch: String?, traded: Bool, timeout: TimeInterval) async -> Bool {
        let current: String
        do {
            current = try await api.deviceTokens(timeout: timeout).current ?? ""
        } catch let error as APIError where error.status == 401 {
            // Its own credential was revoked already (Settings › Devices on another device), and the hub dropped its
            // pushes then. A pairing token never traded has expired instead; the pushes registered with it stay.
            return traded
        } catch {
            return false
        }
        var watchGone = true
        if let watch, !watch.isEmpty, watch != current {
            // The watch registers no pushes, but its credential still reads and answers this hub's agents.
            watchGone = await revoke(watch, at: api, timeout: timeout)
        }
        // Not revocable by id: the hub's master token (no id), and a pairing token (`pair_<hash>`) whose trade for the
        // device's own failed, which also keeps the pushes registered with it.
        guard !current.isEmpty, !current.hasPrefix("pair_") else { return false }
        return await revoke(current, at: api, timeout: timeout) && watchGone
    }

    /// DELETE /auth/tokens/{id}. true: the credential is gone, revoked meanwhile (404) included.
    private nonisolated static func revoke(_ id: String, at api: APIClient, timeout: TimeInterval) async -> Bool {
        do {
            try await api.revokeToken(id, timeout: timeout)
            return true
        } catch let error as APIError where error.status == 404 {
            return true
        } catch {
            return false
        }
    }
}
