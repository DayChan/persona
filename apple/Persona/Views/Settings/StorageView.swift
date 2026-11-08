import SwiftUI
import WebKit

/// What the app keeps on this phone, by kind, measured from its own container.
nonisolated enum StorageKind: String, CaseIterable, Identifiable, Sendable {
    case agentChats, chatImages, feishuMedia, pageState, web, network, other, appData

    var id: String { rawValue }

    var title: String {
        switch self {
        case .agentChats: String(localized: "Agent Chats")
        case .chatImages: String(localized: "Images in Chats")
        case .feishuMedia: String(localized: "Lark Images and Files")
        case .pageState: String(localized: "Lists and Lark Chats")
        case .web: String(localized: "Web Pages")
        case .network: String(localized: "Network Cache")
        case .other: String(localized: "Other Caches")
        case .appData: String(localized: "App Data")
        }
    }

    var detail: String {
        switch self {
        case .agentChats: String(localized: "The last two turns of each chat, shown first when you open it; background sync writes here too")
        case .chatImages: String(localized: "Screenshots and other images, downloaded only once")
        case .feishuMedia: String(localized: "Images, videos, and files from Lark messages")
        case .pageState: String(localized: "Agents and Inbox lists, and recent messages in Lark chats")
        case .web: String(localized: "Web pages opened in the app (sign-ins not included)")
        case .network: String(localized: "Copies the system keeps for network requests")
        case .other: String(localized: "Temporary files and other caches")
        case .appData: String(localized: "Settings and pairing info, kept when you clear caches")
        }
    }

    var icon: String {
        switch self {
        case .agentChats: Theme.Icon.agentChat
        case .chatImages: Theme.Icon.photos
        case .feishuMedia: "paperclip"
        case .pageState: Theme.Icon.list
        case .web: Theme.Icon.safari
        case .network: "network"
        case .other: "shippingbox"
        case .appData: "gearshape"
        }
    }

    var color: Color {
        switch self {
        case .agentChats: .blue
        case .chatImages: .purple
        case .feishuMedia: .teal
        case .pageState: .orange
        case .web: .pink
        case .network: .indigo
        case .other: .gray
        case .appData: .secondary
        }
    }

    var clearable: Bool { self != .appData }
}

/// Measures and clears the app's caches. Everything here can be fetched again from the hub.
nonisolated enum StorageUsage {
    private static var caches: URL { URL.cachesDirectory }
    private static var library: URL { URL.libraryDirectory }
    private static var bundleID: String { Bundle.main.bundleIdentifier ?? "" }

    /// Bytes on disk per kind (allocated size, what Settings › iPhone Storage counts).
    static func scan() -> [StorageKind: Int64] {
        var out: [StorageKind: Int64] = [:]
        func add(_ k: StorageKind, _ url: URL) { out[k, default: 0] += size(of: url) }
        for url in ownCaches() {
            let name = url.lastPathComponent
            if name == bundleID { // URLCache, and WebKit's cache when it lives below
                for sub in children(url) { add(sub.lastPathComponent == "WebKit" ? .web : .network, sub) }
            } else {
                add(kind(cachesEntry: name), url)
            }
        }
        for url in ownTemp() {
            add(url.lastPathComponent.hasPrefix("lark-") ? .feishuMedia : .other, url)
        }
        // Outside a sandbox Library and Documents are the user's (walking ~/Library never finished): not counted.
        guard sandboxed else { return out }
        for url in children(library) where url.lastPathComponent != "Caches" {
            add(url.lastPathComponent == "WebKit" ? .web : .appData, url)
        }
        add(.appData, URL.documentsDirectory)
        return out
    }

    /// Whether an entry of the caches folder is Persona's to count and clear. In a sandbox (iOS, a signed Mac build)
    /// the folder is the app's own; a Mac build without one (an ad-hoc debug build) shares ~/Library/Caches with
    /// every other app, so only the folders Persona writes count there.
    static func owned(cachesEntry name: String, sandboxed: Bool, bundleID: String) -> Bool {
        sandboxed || name == bundleID || ChatCache.isFolder(name)
            || ["chat-images", "lark-media", "state", "attached-videos"].contains(name)
    }

    /// Whether an entry of the temp folder is Persona's: all of it in a sandbox; outside one $TMPDIR is the user's
    /// (another program's lark- folder could be there), so none of it.
    static func owned(tempEntry name: String, sandboxed: Bool) -> Bool { sandboxed }

    /// Whether the app runs in a sandbox (its own caches, temp and Library). Always on iOS.
    private static var sandboxed: Bool {
        #if os(macOS)
        ProcessInfo.processInfo.environment["APP_SANDBOX_CONTAINER_ID"] != nil
        #else
        true
        #endif
    }

    /// The caches folder's entries that are Persona's (all of them in a sandbox).
    private static func ownCaches() -> [URL] {
        children(caches).filter { owned(cachesEntry: $0.lastPathComponent, sandboxed: sandboxed, bundleID: bundleID) }
    }

    /// The temp folder's entries that are Persona's (`owned(tempEntry:)`).
    private static func ownTemp() -> [URL] {
        children(URL.temporaryDirectory).filter { owned(tempEntry: $0.lastPathComponent, sandboxed: sandboxed) }
    }

    static func kind(cachesEntry name: String) -> StorageKind {
        if ChatCache.isFolder(name) { return .agentChats }
        return switch name {
        case "chat-images": .chatImages
        case "lark-media": .feishuMedia
        case "state": .pageState
        default: name == "WebKit" || name.hasPrefix("com.apple.WebKit") ? .web : .other
        }
    }

    /// Removes the files of one kind. Folders stay (the loaders write into them without creating them again).
    static func clear(_ kind: StorageKind) {
        for url in ownCaches() {
            let name = url.lastPathComponent
            if name == bundleID {
                if kind == .network { URLCache.shared.removeAllCachedResponses() }
                continue
            }
            if self.kind(cachesEntry: name) == kind {
                if kind == .other { remove(url) } else { removeContents(of: url) }
            }
        }
        if kind == .feishuMedia || kind == .other {
            for url in ownTemp() where url.lastPathComponent.hasPrefix("lark-") == (kind == .feishuMedia) {
                remove(url)
            }
        }
    }

    static func size(of url: URL) -> Int64 {
        let keys: Set<URLResourceKey> = [.totalFileAllocatedSizeKey, .fileAllocatedSizeKey, .isRegularFileKey]
        if let v = try? url.resourceValues(forKeys: keys), v.isRegularFile == true {
            return Int64(v.totalFileAllocatedSize ?? v.fileAllocatedSize ?? 0)
        }
        guard let e = FileManager.default.enumerator(at: url, includingPropertiesForKeys: Array(keys)) else { return 0 }
        var total: Int64 = 0
        for case let f as URL in e {
            guard let v = try? f.resourceValues(forKeys: keys), v.isRegularFile == true else { continue }
            total += Int64(v.totalFileAllocatedSize ?? v.fileAllocatedSize ?? 0)
        }
        return total
    }

    private static func children(_ url: URL) -> [URL] {
        (try? FileManager.default.contentsOfDirectory(at: url, includingPropertiesForKeys: nil)) ?? []
    }

    private static func remove(_ url: URL) { try? FileManager.default.removeItem(at: url) }

    private static func removeContents(of url: URL) {
        for c in children(url) { remove(c) }
    }
}

/// Settings › Storage: how much the app keeps on this phone, by kind, and clearing it.
struct StorageView: View {
    @State private var usage: [StorageKind: Int64]?
    @State private var clearing: StorageKind?
    @State private var confirmAll = false
    @State private var confirmOne: StorageKind?

    private var total: Int64 { usage?.values.reduce(0, +) ?? 0 }
    private var clearableTotal: Int64 { StorageKind.allCases.filter(\.clearable).reduce(0) { $0 + (usage?[$1] ?? 0) } }

    var body: some View {
        SettingsForm {
            Section {
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(usage == nil ? String(localized: "Calculating…") : total.storageText)
                            .font(Theme.Font.display).fontDesign(.rounded)
                            .contentTransition(.numericText())
                        Spacer()
                        if usage != nil {
                            Text("Including \(clearableTotal.storageText) of caches").font(Theme.Font.secondary).foregroundStyle(.secondary)
                        }
                    }
                    bar
                }
                .padding(.vertical, Theme.Space.sm)
            } footer: {
                #if os(macOS)
                Text("Space Persona uses on this Mac. Caches can all be fetched again from the hub: after clearing, chats and images download again when you open them.")
                #else
                Text("Space Persona uses on this phone. Caches can all be fetched again from the hub: after clearing, chats and images download again when you open them.")
                #endif
            }

            Section {
                ForEach(StorageKind.allCases) { kind in row(kind) }
            }

            Section {
                #if os(macOS)
                // A standard button at the trailing end, not a bar the width of the form (#1221).
                HStack(spacing: Theme.Space.s) {
                    Spacer(minLength: 0)
                    if clearing != nil { ProgressView().controlSize(.small) }
                    Button("Clear All Caches…", role: .destructive) { confirmAll = true }
                        .lineLimit(1)
                        .formButton()
                        .disabled(usage == nil || clearing != nil || clearableTotal == 0)
                }
                #else
                Button(role: .destructive) { confirmAll = true } label: {
                    HStack {
                        Spacer()
                        if clearing != nil { ProgressView() } else { Text("Clear All Caches").lineLimit(1) }
                        Spacer()
                    }
                }
                .disabled(usage == nil || clearing != nil || clearableTotal == 0)
                #endif
            }
        }
        .themedPage()
        .detailTitle(String(localized: "Storage"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await measure() }
        .refreshable { await measure() }
        .confirmationDialog("Clear all \(clearableTotal.storageText) of caches? Settings and pairing info are kept.", isPresented: $confirmAll, titleVisibility: .visible) {
            Button("Clear [Image Editor bce237]", role: .destructive) { Task { await clear(StorageKind.allCases.filter(\.clearable)) } }
        }
        .confirmationDialog(confirmOne.map { String(localized: "Clear “\($0.title)” (\((usage?[$0] ?? 0).storageText))?") } ?? "",
                            isPresented: Binding(get: { confirmOne != nil }, set: { if !$0 { confirmOne = nil } }), titleVisibility: .visible) {
            Button("Clear [Image Editor bce237]", role: .destructive) { if let k = confirmOne { Task { await clear([k]) } } }
        }
    }

    /// Like Settings › iPhone Storage: one bar, a segment per kind.
    private var bar: some View {
        GeometryReader { geo in
            HStack(spacing: 1.5) {
                ForEach(StorageKind.allCases) { kind in
                    let bytes = usage?[kind] ?? 0
                    if total > 0, bytes > 0 {
                        kind.color.frame(width: max(2, geo.size.width * CGFloat(bytes) / CGFloat(total)))
                    }
                }
                if total == 0 { Theme.Fill.chip }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.Fill.chip)
            .clipShape(.rect(cornerRadius: Theme.Radius.badge, style: .continuous))
        }
        .scaledFrame(height: Theme.Size.markXS)
        .animation(Theme.Motion.standard, value: usage)
        .accessibilityHidden(true)
    }

    private func row(_ kind: StorageKind) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(kind.icon)
                .font(Theme.Font.bodyStrong)
                .foregroundStyle(.white)
                .scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
                .background(kind == .appData ? Color.gray : kind.color, in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(kind.title).lineLimit(1)
                Text(kind.detail).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: Theme.Space.s)
            if clearing == kind {
                ProgressView()
            } else {
                Text(usage.map { ($0[kind] ?? 0).storageText } ?? "—")
                    .font(Theme.Font.body.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .fixedSize()
            }
        }
        .contentShape(.rect)
        .swipeActions {
            if kind.clearable, (usage?[kind] ?? 0) > 0 {
                Button("Clear [Image Editor bce237]", role: .destructive) { confirmOne = kind }
            }
        }
        .contextMenu {
            if kind.clearable, (usage?[kind] ?? 0) > 0 {
                Button(role: .destructive) { confirmOne = kind } label: { Label("Clear [Image Editor bce237]", systemImage: Theme.Icon.delete) }
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func measure() async {
        let result = await Task.detached(priority: .userInitiated) { StorageUsage.scan() }.value
        withAnimation(Theme.Motion.standard) { usage = result }
    }

    private func clear(_ kinds: [StorageKind]) async {
        clearing = kinds.first
        for kind in kinds {
            clearing = kind
            if kind == .web {
                let types: Set<String> = [WKWebsiteDataTypeDiskCache, WKWebsiteDataTypeMemoryCache, WKWebsiteDataTypeFetchCache,
                                          WKWebsiteDataTypeOfflineWebApplicationCache]
                await WKWebsiteDataStore.default().removeData(ofTypes: types, modifiedSince: .distantPast)
            }
            await Task.detached(priority: .userInitiated) { StorageUsage.clear(kind) }.value
            forgetInMemory(kind)
        }
        clearing = nil
        Haptics.success()
        await measure()
    }

    /// What was cleared from disk is also dropped from memory, and the background syncs fetch it again.
    private func forgetInMemory(_ kind: StorageKind) {
        switch kind {
        case .agentChats: AgentSync.shared.forget()
        case .chatImages: ChatImageLoader.clearMemory()
        case .feishuMedia: LarkMedia.shared.clearMemory()
        case .pageState: LarkSync.shared.forget()
        default: break
        }
    }
}
