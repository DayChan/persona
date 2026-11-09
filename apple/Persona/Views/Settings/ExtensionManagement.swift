import Foundation

/// Dependency status in the details and the install preview; not the manifest's version constraint itself.
nonisolated struct ExtensionDependencyStatus: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var version: String?
    var state: String
    var installed: String?
    var name: String?

    var title: String { name?.isEmpty == false ? name! : id }
    var description: String {
        switch state {
        case "ok": return installed.map { String(localized: "Ready · \($0)") } ?? String(localized: "Ready [Extension Management ab27f8]")
        case "missing": return String(localized: "Install this extension first")
        case "disabled": return String(localized: "Turn on this extension first")
        case "old":
            let needed = version ?? String(localized: "a newer version"), current = installed ?? String(localized: "Unknown [Session Modes 4d8c1c]")
            return String(localized: "Needs \(needed), has \(current)")
        default: return String(localized: "Unknown [Connectors Views 93e474]")
        }
    }
}

/// Each contribution's effective switch; an older hub has no contributions field, and no fake switches are made up.
nonisolated struct ExtensionContributionSwitch: Codable, Hashable, Sendable, Identifiable {
    var point: String
    var contributionID: String
    var title: String?
    var enabled: Bool
    var canDisable: Bool
    var via: String?

    var id: String { point + "/" + contributionID }
    var label: String { title?.isEmpty == false ? title! : contributionID }

    enum CodingKeys: String, CodingKey {
        case point, title, enabled, via
        case contributionID = "id", canDisable = "can_disable"
    }
}

/// Before disabling, shows the affected extensions the server returned; cascade is sent only after confirming.
nonisolated struct ExtensionDisableConfirmation {
    var message: String
    var dependents: [String]

    init(error: APIError) {
        dependents = (error.details?["dependents"]?.array ?? []).compactMap { item in
            item["name"]?.string ?? item["id"]?.string
        }
        let names = dependents.joined(separator: "、")
        message = dependents.isEmpty ? error.message : String(localized: "These extensions depend on it and will be turned off too: \(names). They're only turned off, not uninstalled; their settings and data stay.")
    }
}

nonisolated struct ExtensionRemovalResult: Decodable, Sendable {
    var removedMembers: [String]?
    /// What the hub could not clean up (#4466): the extension is removed, these are for the user to do by hand. nil
    /// from an older hub.
    var warnings: [String]?
    var cleanupWarnings: [String] { warnings ?? [] }
    enum CodingKeys: String, CodingKey { case removedMembers = "removed_members", warnings }
}

/// An uninstall that went through with cleanup left to do (#4819): every warning, shown until the user closes it.
nonisolated struct ExtensionRemovalNotice: Identifiable, Equatable, Sendable {
    var id = UUID()
    var title: String
    var warnings: [String]
}

/// One confirmation covers each preview's own permission summary; for an older hub that needs no bundle, the new field is not sent.
nonisolated struct ExtensionInstallRequest: Encodable {
    struct BundleReference: Encodable {
        var previewID: String
        var permissionsDigest: String
        enum CodingKeys: String, CodingKey {
            case previewID = "preview_id", permissionsDigest = "permissions_digest"
        }
    }
    var previewID: String
    var permissionsDigest: String
    var bundle: [BundleReference]?
    /// `async`: a hub with `extension_operations` answers at once with the operation and pushes its steps (#3307).
    var background: Bool?

    init(preview: ExtensionPreview, background: Bool = false) {
        self.background = background ? true : nil
        previewID = preview.previewID
        permissionsDigest = preview.permissionsDigest
        let refs = (preview.bundle ?? []).map { BundleReference(previewID: $0.previewID, permissionsDigest: $0.permissionsDigest) }
        bundle = refs.isEmpty ? nil : refs
    }

    enum CodingKeys: String, CodingKey {
        case previewID = "preview_id", permissionsDigest = "permissions_digest", bundle, background = "async"
    }
}

/// Unsaved forms live only in memory, kept apart per hub and extension; rebuilding the view or reloading does not drop the drafts.
@MainActor final class ExtensionDrafts {
    static let shared = ExtensionDrafts()
    /// An extension's draft by hub, id and manager (another manager's copy of the same id has its own).
    struct Key: Hashable { var source: String; var id: String; var manager: String? = nil }
    struct Draft { var config: [String: JSONValue]; var numbers: [String: String] }
    var values: [Key: Draft] = [:]
}

extension APIClient {
    func setExtensionContribution(_ ext: String, point: String, id: String, enabled: Bool) async throws -> HubExtensionDetail {
        try JSON.decoder.decode(HubExtensionDetail.self, from: try await request("PUT",
            extensionPath("/\(ext)/-/contributions/\(point)/\(id)"), body: ["enabled": enabled]))
    }
}
