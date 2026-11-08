import Foundation
import Darwin

/// Separate from Fleet's appearance files: this snapshot contains credentials and must never be exported or logged.
nonisolated struct ExtensionSurfaceCredential: Codable, Hashable, Sendable {
    var url: URL
    var token: String
    var deviceID: String?
    var name: String
    /// Only the wrist's answer-only token; nil decodes snapshots written before this metadata existed.
    var answerOnly: Bool? = nil
    /// Forwarded from the phone's authenticated hello, never inferred from an empty public health response.
    var capabilities: [String]? = nil

    var isAnswerOnly: Bool {
        #if os(watchOS)
        // An upgraded complication may run before the containing watch app rewrites its older snapshot.
        true
        #else
        answerOnly == true
        #endif
    }

    func sameAuthorization(as other: Self) -> Bool {
        url == other.url && token == other.token
    }
}

nonisolated struct ExtensionSurfaceKey: Codable, Hashable, Sendable {
    var group: String
    var owner: String
    var contribution: String

    /// JSON gives a reversible identity without delimiter collisions, including the default group.
    var id: String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return (try? encoder.encode(self)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }
    init(group: String, owner: String, contribution: String) {
        self.group = group; self.owner = owner; self.contribution = contribution
    }
    init?(id: String) {
        guard let data = id.data(using: .utf8), let key = try? JSONDecoder().decode(Self.self, from: data) else { return nil }
        self = key
    }
}

nonisolated struct ExtensionSurfaceCache: Codable, Sendable {
    var key: ExtensionSurfaceKey
    var family: String
    var locale: String
    var timeZone: String
    var answer: ExtensionWidgetAnswer
    var invalidated: Bool = false

    func fresh(at now: Date) -> Bool { !invalidated && (answer.expiration ?? .distantPast) > now }
}

nonisolated struct ExtensionSurfaceGroup: Codable, Sendable {
    var credential: ExtensionSurfaceCredential
    /// A generation fences replies across removal and re-pairing with the same credential.
    var generation: UUID = UUID()
    var widgets: [ExtensionWidgetPlace] = []
    var shortcuts: [ExtensionShortcutPlace] = []
    var cache: [ExtensionSurfaceCache] = []
    var invalidation: UInt64 = 0
}

nonisolated struct ExtensionSurfaceSnapshot: Codable, Sendable {
    var groups: [String: ExtensionSurfaceGroup] = [:]
}

/// Cross-process transactions serialize app/catalog/timeline writers; each transaction atomically replaces the file.
/// No fallback to the app sandbox, UserDefaults, Keychain, or another group's credential.
nonisolated struct ExtensionSurfaceStore: Sendable {
    var root: URL?
    static var shared: Self {
        #if DEBUG
        if ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil { return Self(root: nil) }
        #if os(macOS)
        if let path = UserDefaults.standard.string(forKey: "macSnapshot") {
            let directory = URL(fileURLWithPath: path).standardizedFileURL.deletingLastPathComponent()
            guard directory.path.hasPrefix("/tmp/") || directory.path.hasPrefix("/private/tmp/") else { return Self(root: nil) }
            // A local snapshot must not replace the user's real Shortcuts/App Group credentials.
            return Self(root: directory.appendingPathComponent("ExtensionSurfaces", isDirectory: true))
        }
        #endif
        #endif
        let name = Bundle.main.object(forInfoDictionaryKey: "PersonaAppGroup") as? String
        let container = name.flatMap { FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: $0) }
        return Self(root: container?.appendingPathComponent("ExtensionSurfaces", isDirectory: true))
    }

    func read() -> ExtensionSurfaceSnapshot {
        (try? transaction(write: false) { $0 }) ?? ExtensionSurfaceSnapshot()
    }

    func replaceCredentials(_ credentials: [String: ExtensionSurfaceCredential]) throws {
        try transaction { snapshot in
            var replacement: [String: ExtensionSurfaceGroup] = [:]
            for (id, credential) in credentials {
                if var old = snapshot.groups[id], old.credential.sameAuthorization(as: credential) {
                    // Metadata changes do not rotate the pairing or erase offline projections.
                    old.credential = credential
                    replacement[id] = old
                } else {
                    replacement[id] = ExtensionSurfaceGroup(credential: credential)
                }
            }
            snapshot.groups = replacement
        }
    }

    func revoke(_ group: String, generation: UUID? = nil) throws {
        try transaction { snapshot in
            if generation == nil || snapshot.groups[group]?.generation == generation { snapshot.groups[group] = nil }
        }
    }

    func catalog(group: String, expected: ExtensionSurfaceGroup, widgets: [ExtensionWidgetPlace], shortcuts: [ExtensionShortcutPlace]) throws {
        try transaction { snapshot in
            guard var current = snapshot.groups[group], current.generation == expected.generation,
                  current.credential.capabilities == expected.credential.capabilities else { return }
            current.widgets = widgets
            current.shortcuts = shortcuts
            current.cache.removeAll { cached in
                !widgets.contains { $0.extension == cached.key.owner && $0.id == cached.key.contribution && $0.families.contains(cached.family) }
            }
            snapshot.groups[group] = current
        }
    }

    func save(_ cache: ExtensionSurfaceCache, expected: ExtensionSurfaceGroup) throws {
        try transaction { snapshot in
            guard var current = snapshot.groups[cache.key.group], current.generation == expected.generation,
                  current.invalidation == expected.invalidation,
                  current.credential.capabilities == expected.credential.capabilities,
                  current.widgets.contains(where: { $0.extension == cache.key.owner && $0.id == cache.key.contribution && $0.families.contains(cache.family) }) else { return }
            current.cache.removeAll { $0.key == cache.key && $0.family == cache.family && $0.locale == cache.locale && $0.timeZone == cache.timeZone }
            current.cache.append(cache)
            // Bounds protect the shared container from accumulating old locale/family variants indefinitely.
            current.cache = Array(current.cache.suffix(128))
            snapshot.groups[cache.key.group] = current
        }
    }

    func invalidate(group: String?, owner: String, contribution: String?) throws {
        try transaction { snapshot in
            for id in Array(snapshot.groups.keys) where group == nil || id == group {
                guard var current = snapshot.groups[id] else { continue }
                current.invalidation &+= 1
                for i in current.cache.indices where current.cache[i].key.owner == owner && (contribution == nil || current.cache[i].key.contribution == contribution) {
                    current.cache[i].invalidated = true
                }
                snapshot.groups[id] = current
            }
        }
    }

    @discardableResult private func transaction<T>(write: Bool = true, _ body: (inout ExtensionSurfaceSnapshot) throws -> T) throws -> T {
        guard let root else { throw CocoaError(.fileNoSuchFile) }
        let fm = FileManager.default
        try fm.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        #if os(iOS) || os(watchOS) || os(visionOS)
        try fm.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication], ofItemAtPath: root.path)
        #endif
        var excludedRoot = root
        var values = URLResourceValues(); values.isExcludedFromBackup = true
        try excludedRoot.setResourceValues(values)
        let descriptor = open(root.appendingPathComponent("store.lock").path, O_CREAT | O_RDWR, 0o600)
        guard descriptor >= 0 else { throw CocoaError(.fileReadNoPermission) }
        defer { close(descriptor) }
        guard flock(descriptor, LOCK_EX) == 0 else { throw CocoaError(.fileReadUnknown) }
        defer { flock(descriptor, LOCK_UN) }
        let file = root.appendingPathComponent("snapshot.json")
        // An unreadable protected file is not an empty snapshot and must never be overwritten.
        var snapshot: ExtensionSurfaceSnapshot
        if fm.fileExists(atPath: file.path) { snapshot = try JSONDecoder().decode(ExtensionSurfaceSnapshot.self, from: Data(contentsOf: file)) }
        else { snapshot = ExtensionSurfaceSnapshot() }
        let result = try body(&snapshot)
        if write {
            var options: Data.WritingOptions = [.atomic]
            #if os(iOS) || os(watchOS) || os(visionOS)
            options.insert(.completeFileProtectionUntilFirstUserAuthentication)
            #endif
            try JSONEncoder().encode(snapshot).write(to: file, options: options)
            try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        }
        return result
    }
}

nonisolated extension ExtensionSurfaceSnapshot {
    func cache(_ key: ExtensionSurfaceKey, family: String, locale: String = ExtensionUIContext.appLocale,
               timeZone: String = TimeZone.current.identifier) -> ExtensionSurfaceCache? {
        groups[key.group]?.cache.first { $0.key == key && $0.family == family && $0.locale == locale && $0.timeZone == timeZone }
    }

    /// Always returns every paired group, even when it has no widget configurations: reports are full replacement.
    func reports(_ configurations: [(ExtensionSurfaceKey, String)]) -> [String: [ExtensionDeviceWidget]] {
        groups.mapValues { _ in [] }.merging(Dictionary(grouping: configurations.filter { key, family in
            groups[key.group]?.widgets.contains { $0.extension == key.owner && $0.id == key.contribution && $0.families.contains(family) } == true
        }, by: { $0.0.group }).mapValues { entries in
            Array(Set(entries.map { ExtensionDeviceWidget(extension: $0.0.owner, id: $0.0.contribution, family: $0.1) }))
                .sorted { ($0.extension, $0.id, $0.family) < ($1.extension, $1.id, $1.family) }
        }, uniquingKeysWith: { _, report in Array(report.prefix(64)) })
    }
}
