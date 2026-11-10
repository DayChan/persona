import Foundation
import Observation

/// Entry availability is independent of the personal list's search, unread filters and removed rows.
@MainActor @Observable final class PersonalAgentEntryModel {
    nonisolated struct Connection: Hashable, Sendable {
        var configuration: ServerConfig
        var group: String

        init(_ api: APIClient) { configuration = api.config; group = api.group }
    }

    private var connection: Connection?
    private var available = false
    private var revision = UUID()

    func isAvailable(for api: APIClient?) -> Bool {
        guard let api, connection == Connection(api) else { return false }
        return available
    }

    /// Subscribe before loading so an account/session created during the first request is not missed.
    func watch(_ api: APIClient?) async {
        guard let api else { connection = nil; available = false; return }
        let changes = PersonalAgentSync.shared.changes()
        await refresh(api)
        for await _ in changes {
            guard !Task.isCancelled else { return }
            await refresh(api)
        }
    }

    func refresh(_ api: APIClient) async {
        let key = Connection(api)
        if connection != key { connection = key; available = false }
        let request = UUID()
        revision = request
        do {
            let sources = try await api.personalAgentSources()
            let configured = Set(sources.filter {
                $0.enabled && !["missing", "disabled", "no_accounts"].contains($0.setup ?? "")
                    && !($0.accounts ?? []).isEmpty
            }.map(\.source))
            var found = false
            if !configured.isEmpty {
                let page = try await api.personalAgentListPage(SessionListQuery())
                found = page.sessions.contains { configured.contains($0.source) }
            }
            guard !Task.isCancelled, connection == key, revision == request else { return }
            available = found
        } catch {
            guard !Task.isCancelled, connection == key, revision == request else { return }
            available = false
        }
    }
}

@MainActor @Observable final class PersonalAgentListModel {
    private(set) var page: SessionListModel<PersonalAgentSession>?
    /// Removed here and not re-pulled yet: their rows leave at once, and come back if the hub refuses.
    private(set) var going: Set<String> = []
    /// What the folded Removed (N) section lists (GET /agents/hidden); empty on a hub without it.
    private(set) var removed: [PersonalAgentSession] = []
    private var configuration: ServerConfig?
    private var group: String?

    func connect(_ api: APIClient) {
        guard configuration != api.config || group != api.group else { return }
        page?.cancel()
        configuration = api.config
        group = api.group
        removed = []
        page = SessionListModel(id: { $0.id }, removalID: { $0 },
            fetch: { try await api.personalAgentListPage($0, cursor: $1, since: $2) },
            counts: { try await api.sessionListCounts($0, personal: true) })
        page?.changed = { PersonalAgentIndex.update($0) }
    }

    func loadRemoved(_ api: APIClient) async {
        removed = (try? await api.hiddenPersonalAgents()) ?? []
    }

    /// Remove from List: the row goes at once and stays gone while the list is pulled again (the returned task); a
    /// refusal puts it back and is thrown for the caller to say. Returns once the hub took it, so Undo shows at once.
    @discardableResult
    func remove(_ s: PersonalAgentSession, api: APIClient) async throws -> Task<Void, Never> {
        going.insert(s.id)
        do {
            try await api.hidePersonalAgent(s.id)
        } catch {
            going.remove(s.id)
            throw error
        }
        return Task {
            await pull(api)
            going.remove(s.id)
        }
    }

    /// Restore (and the toast's Undo): back in the list.
    func restore(_ s: PersonalAgentSession, api: APIClient) async throws {
        removed.removeAll { $0.id == s.id }
        do {
            try await api.unhidePersonalAgent(s.id)
        } catch {
            await loadRemoved(api)
            throw error
        }
        await pull(api)
    }

    private func pull(_ api: APIClient) async {
        if let page { if page.paged { await page.synchronize() } else { await page.reload() } }
        await loadRemoved(api)
    }
}
