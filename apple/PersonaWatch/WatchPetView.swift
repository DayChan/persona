import CryptoKit
import ImageIO
import SwiftUI
import WatchKit
import WidgetKit

/// The Milk Frog's files on this watch (docs/apple/persona.md "The Milk Frog on the watch"): fetched from the hub, by hash,
/// and shown only once all of them are here. An older hub has none, and the watch simply shows no frog.
@Observable
final class WatchPetPack {
    private(set) var manifest: WatchPetManifest?
    @ObservationIgnored private var fetching = false
    @ObservationIgnored private var fetched = false

    private static let directory = URL.cachesDirectory.appending(path: "pet", directoryHint: .isDirectory)
    private static let manifestFile = directory.appending(path: "pet.json")

    init() {
        guard let data = try? Data(contentsOf: Self.manifestFile),
              let saved = try? JSONDecoder().decode(WatchPetManifest.self, from: data),
              saved.isUsable, Self.allThere(saved) else { return }
        manifest = saved
    }

    func clipURL(_ name: String) -> URL? { manifest?.clips[name].map { Self.file($0.sha256) } }
    func faceURL(_ state: WatchPetState) -> URL? { manifest?.faces[state.clip].map { Self.file($0.sha256) } }

    /// Once a launch: what is missing comes down, so an interrupted fetch goes on from the file it stopped at.
    func fetch(_ api: APIClient) async {
        guard !fetching, !fetched else { return }
        fetching = true
        defer { fetching = false }
        do {
            let next: WatchPetManifest = try await api.get(WatchPet.manifestPath)
            guard next.isUsable else { return }
            try FileManager.default.createDirectory(at: Self.directory, withIntermediateDirectories: true)
            for (file, hash) in next.clips.values.map({ ($0.file, $0.sha256) }) + next.faces.values.map({ ($0.file, $0.sha256) }) {
                try await Self.fetchFile(file, hash: hash, api: api)
            }
            try JSONEncoder().encode(next).write(to: Self.manifestFile, options: .atomic)
            manifest = next
            fetched = true
            Self.removeAllBut(next)
        } catch { /* The next refresh tries again; the frog stays as it was. */ }
    }

    private static func file(_ hash: String) -> URL { directory.appending(path: hash + ".png") }

    private static func hashes(_ m: WatchPetManifest) -> [String] {
        m.clips.values.map(\.sha256) + m.faces.values.map(\.sha256)
    }

    private static func allThere(_ m: WatchPetManifest) -> Bool {
        hashes(m).allSatisfy { FileManager.default.fileExists(atPath: file($0).path) }
    }

    private static func fetchFile(_ name: String, hash: String, api: APIClient) async throws {
        let target = file(hash)
        guard !FileManager.default.fileExists(atPath: target.path) else { return }
        let part = directory.appending(path: hash + ".part")
        defer { try? FileManager.default.removeItem(at: part) }
        try await api.download("/api/v1/personas/\(PersonaSelection.packageID)/\(name)", query: [:], to: part) { _, _ in }
        let digest = SHA256.hash(data: try Data(contentsOf: part)).map { String(format: "%02x", $0) }.joined()
        guard digest == hash else { throw CocoaError(.fileReadCorruptFile) }
        try FileManager.default.moveItem(at: part, to: target)
    }

    private static func removeAllBut(_ m: WatchPetManifest) {
        let keep = Set(hashes(m).map { $0 + ".png" } + [manifestFile.lastPathComponent])
        let found = (try? FileManager.default.contentsOfDirectory(atPath: directory.path)) ?? []
        for name in found where !keep.contains(name) { try? FileManager.default.removeItem(at: directory.appending(path: name)) }
    }
}

/// A clip's frames, decoded off the main thread and held while it plays: one screen's worth of decoding per clip
/// instead of one per frame, which is what the battery would notice.
private final class WatchPetFrames: Sendable {
    let images: [CGImage]

    nonisolated init?(url: URL) {
        guard let src = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        let opts = [kCGImageSourceShouldCacheImmediately: true] as CFDictionary
        images = (0..<CGImageSourceGetCount(src)).compactMap { CGImageSourceCreateImageAtIndex(src, $0, opts) }
        if images.isEmpty { return nil }
    }
}

/// Plays one clip. While the wrist is down the system stops the animation, so it draws the clip's still frame.
private struct WatchPetClipView: View {
    let url: URL
    let clip: WatchPetManifest.Clip
    /// When the clip started; a new one plays it again from the top.
    let start: Date

    @Environment(\.isLuminanceReduced) private var dimmed
    @State private var frames: WatchPetFrames?

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / clip.fps, paused: dimmed)) { timeline in
            if let images = frames?.images, let image = frame(images, at: timeline.date) {
                Image(decorative: image, scale: 2).resizable().scaledToFit()
            }
        }
        .task(id: url) {
            let url = url
            frames = await Task.detached(priority: .userInitiated) { WatchPetFrames(url: url) }.value
        }
    }

    private func frame(_ images: [CGImage], at date: Date) -> CGImage? {
        if dimmed { return images[min(clip.still, images.count - 1)] }
        return images[max(0, Int(date.timeIntervalSince(start) * clip.fps)) % images.count]
    }
}

/// The frog's page: it moves here; the row and the watch face are still.
struct WatchPetView: View {
    @Environment(WatchModel.self) private var model
    /// The tap clip's start while it plays (`.idle` only: it is drawn on that pose).
    @State private var tapped: Date?
    /// When the state's clip last started: a tap in any other state plays that clip again.
    @State private var started = Date.now

    var body: some View {
        if let status = model.petStatus {
            VStack(spacing: Theme.Space.s) {
                clip(status).frame(width: Theme.Watch.petSize, height: Theme.Watch.petSize)
                    .contentShape(Rectangle())
                    .onTapGesture { tap(status) }
                Text(status.title).font(.headline).lineLimit(2).multilineTextAlignment(.center)
                if status.state == .offline, let t = model.lastUpdate {
                    Text("Last updated \(t.formatted(.relative(presentation: .named))) [Watch Pet]").font(.footnote).foregroundStyle(.secondary)
                }
            }
            .onChange(of: status.state) { started = .now; tapped = nil }
            .task(id: tapped) {
                guard tapped != nil, let c = model.pet.manifest?.clips[WatchPet.tapClip] else { return }
                try? await Task.sleep(for: .seconds(Double(c.frames) / c.fps))
                if !Task.isCancelled { tapped = nil; started = .now }
            }
        }
    }

    @ViewBuilder private func clip(_ status: WatchPetStatus) -> some View {
        let name = tapped == nil ? status.state.clip : WatchPet.tapClip
        if let url = model.pet.clipURL(name), let c = model.pet.manifest?.clips[name] {
            WatchPetClipView(url: url, clip: c, start: tapped ?? started)
        }
    }

    private func tap(_ status: WatchPetStatus) {
        WKInterfaceDevice.current().play(.click)
        if status.state == .idle { tapped = .now } else { started = .now }
    }
}

/// The list's way in: the frog's face for the state, and what the state is.
struct WatchPetRow: View {
    @Environment(WatchModel.self) private var model
    let status: WatchPetStatus

    var body: some View {
        NavigationLink { WatchPetView() } label: {
            HStack(spacing: Theme.Space.s) {
                if let url = model.pet.faceURL(status.state), let image = UIImage(contentsOfFile: url.path) {
                    Image(uiImage: image).resizable().scaledToFit()
                        .frame(width: Theme.Watch.petFaceSize, height: Theme.Watch.petFaceSize)
                        .clipShape(Circle())
                }
                Text(status.title).font(.headline).lineLimit(2)
            }
        }
    }
}

/// Hands the frog to the watch face (`PersonaWatchWidgets`): the snapshot and the state's faces in the App Group. The
/// face's timeline reloads only when what it shows changes or the last one is getting old, which keeps within
/// WidgetKit's daily budget.
@MainActor
final class WatchPetComplication {
    private var shown: WatchPetSnapshot?
    private var copiedFaces: Set<String> = []
    private static let reloadEvery: TimeInterval = 20 * 60

    func publish(_ status: WatchPetStatus?, at: Date?, pack: WatchPetPack) {
        guard let dir = WatchPetSnapshot.directory else { return }
        let file = dir.appending(path: WatchPetSnapshot.fileName)
        guard let status, let at, let manifest = pack.manifest else {
            if shown != nil || FileManager.default.fileExists(atPath: file.path) {
                try? FileManager.default.removeItem(at: file)
                shown = nil
                WidgetCenter.shared.reloadTimelines(ofKind: WatchPetSnapshot.widgetKind)
            }
            return
        }
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        for state in WatchPetState.allCases {
            guard let face = manifest.faces[state.clip], !copiedFaces.contains(face.sha256),
                  let source = pack.faceURL(state) else { continue }
            let target = dir.appending(path: WatchPetSnapshot.faceName(state.clip))
            try? FileManager.default.removeItem(at: target)
            if (try? FileManager.default.copyItem(at: source, to: target)) != nil { copiedFaces.insert(face.sha256) }
        }
        let next = WatchPetSnapshot(state: status.state.rawValue, title: status.title,
                                    staleTitle: String(localized: "Not updated lately [Watch Pet]"), at: at)
        guard let data = try? JSONEncoder().encode(next) else { return }
        try? data.write(to: file, options: .atomic)
        let changed = shown?.state != next.state || shown?.title != next.title
        let aging = shown.map { next.at.timeIntervalSince($0.at) >= Self.reloadEvery } ?? true
        if changed || aging {
            shown = next
            WidgetCenter.shared.reloadTimelines(ofKind: WatchPetSnapshot.widgetKind)
        }
    }
}
