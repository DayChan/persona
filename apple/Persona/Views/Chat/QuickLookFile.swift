import CryptoKit
import ImageIO
import QuickLook
import SwiftUI
#if os(macOS)
import Quartz
#endif

/// Files the file viewer hands to the system's QuickLook (docs/apple/ios.md「文件查看器」): Office and iWork documents and
/// RTF, which no viewer of ours draws. Feishu attachments open the same way (`LarkAttachmentView`).
nonisolated enum QuickLookFileType {
    static let extensions: Set<String> = ["docx", "doc", "xlsx", "xls", "pptx", "ppt", "pages", "numbers", "key", "rtf"]

    /// `text`: the node sent the file as text. RTF is text, so is a PEM key named .key, which stays code.
    static func matches(name: String, text: String?) -> Bool {
        let ext = (name as NSString).pathExtension.lowercased()
        guard extensions.contains(ext) else { return false }
        switch ext {
        case "rtf": return text == nil || text?.hasPrefix("{\\rtf") == true
        default: return text == nil
        }
    }
}

/// A machine's file fetched whole into a temporary folder of its own, named as on the machine (QuickLook and AVPlayer
/// pick the format by the name), with the bytes so far for a progress bar.
@MainActor
enum MachineFileDownload {
    static func fetch(_ ref: FileRef, file: FileContent, api: APIClient, prefix: String,
                      progress: @escaping @MainActor @Sendable (Int64, Int64) -> Void) async throws -> URL {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(prefix + UUID().uuidString, isDirectory: true)
        let url = directory.appendingPathComponent((file.name as NSString).lastPathComponent)
        var kept = false
        defer { if !kept { try? FileManager.default.removeItem(at: directory) } }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try await api.download("/api/v1/machines/\(ref.machineID)/file/download",
                               query: ["path": file.path, "cwd": ref.cwd ?? ""], to: url) { received, total in
            Task { @MainActor in progress(received, total) }
        }
        try Task.checkCancellation()
        kept = true
        return url
    }

    /// Removes what `fetch` made: the folder around the file.
    static func remove(_ url: URL?) {
        if let url { try? FileManager.default.removeItem(at: url.deletingLastPathComponent()) }
    }
}

@MainActor @Observable
final class QuickLookFileModel {
    let ref: FileRef
    let file: FileContent
    @ObservationIgnored private let api: APIClient?
    private(set) var localURL: URL?
    private(set) var error: String?
    private(set) var received: Int64 = 0
    private(set) var total: Int64 = 0
    @ObservationIgnored private var generation = 0

    init(ref: FileRef, file: FileContent, api: APIClient?) {
        self.ref = ref
        self.file = file
        self.api = api
        total = file.size
    }

    func load() async {
        stop()
        error = nil
        received = 0
        guard let api else {
            error = String(localized: "Not connected to the hub [App Model 93d955]")
            return
        }
        let mine = generation
        do {
            let url = try await MachineFileDownload.fetch(ref, file: file, api: api, prefix: "persona-preview-") { [weak self] received, total in
                guard let self, self.generation == mine else { return }
                self.received = received
                if total > 0 { self.total = total }
            }
            guard generation == mine else { return MachineFileDownload.remove(url) }
            localURL = url
        } catch {
            guard generation == mine, !Task.isCancelled else { return }
            self.error = error.shown
        }
    }

    func stop() {
        generation += 1
        MachineFileDownload.remove(localURL)
        localURL = nil
    }
}

/// A document the file viewer cannot draw itself, shown by QuickLook inside the page once it is downloaded.
struct QuickLookFileView: View {
    let model: QuickLookFileModel

    var body: some View {
        Group {
            if let error = model.error {
                EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Can't Preview"), message: error)
            } else if let url = model.localURL {
                QuickLookView(url: url)
                    .accessibilityIdentifier("file-quicklook")
            } else {
                VStack(spacing: Theme.Space.m) {
                    ProgressView(value: Double(model.received), total: Double(max(model.total, 1)))
                    Text("Downloading file…")
                        .font(Theme.Font.body)
                    Text(model.received.storageText + " / " + model.total.storageText)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                }
                .padding(Theme.Space.xl)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: ObjectIdentifier(model)) { await model.load() }
        .onDisappear { model.stop() }
    }
}

#if os(iOS) || os(visionOS)
/// QuickLook's preview of one local file, embedded (no bars of its own: the page's toolbar has the menu).
struct QuickLookView: UIViewControllerRepresentable {
    var url: URL

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ controller: QLPreviewController, context: Context) {
        guard context.coordinator.url != url else { return }
        context.coordinator.url = url
        controller.reloadData()
    }

    func makeCoordinator() -> Source { Source(url: url) }

    final class Source: NSObject, QLPreviewControllerDataSource {
        var url: URL
        init(url: URL) { self.url = url }
        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }
        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> any QLPreviewItem { url as NSURL }
    }
}
#else
/// QuickLook's preview of one local file, embedded (the Mac's `QLPreviewView`).
struct QuickLookView: NSViewRepresentable {
    var url: URL

    func makeNSView(context: Context) -> QLPreviewView {
        let view = QLPreviewView(frame: .zero, style: .normal) ?? QLPreviewView()
        view.autostarts = true
        view.previewItem = url as NSURL
        return view
    }

    func updateNSView(_ view: QLPreviewView, context: Context) {
        if (view.previewItem as? NSURL) as URL? != url { view.previewItem = url as NSURL }
    }

    static func dismantleNSView(_ view: QLPreviewView, coordinator: ()) { view.close() }
}
#endif

/// Thumbnails for the file list (the Files tab) and the chat's file cards, drawn by the machine (`GET …/file/thumb`, hub
/// feature `file_thumb`, docs/protocol/protocol.md「文件预览」), three at a time, kept in memory and on disk:
/// - with the file's size and modified time (a listing): by what the file is, so a changed file is asked again;
/// - with only its path (a card in a conversation; size 0, no time): by the path.
/// Every disk copy is checked once per launch with its opaque ETag: a node upgrade can change the picture without
/// changing the source file, including when this app upgraded before the node did.
/// A file the machine cannot draw (`no_thumbnail`), or a hub without the feature, keeps its symbol.
@MainActor
final class FileThumbnails {
    static let shared = FileThumbnails()

    /// What is worth asking for: what the node, or Quick Look on a Mac, can draw. Code and text keep their symbol.
    nonisolated static let extensions: Set<String> = QuickLookFileType.extensions.union([
        "png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "tiff", "tif", "bmp", "svg", "pdf",
    ]).union(VideoFileType.extensions)

    private let images = NSCache<NSString, UIImage>()
    private var failed = Set<String>()
    /// Keys asked this launch (304 or a new picture).
    private var checked = Set<String>()
    private var running = 0
    private var waiting: [CheckedContinuation<Void, Never>] = []
    private let dir: URL

    /// `directory`: where the pictures are kept (a test's own; the next launch is a new instance on the same one).
    init(directory: URL? = nil) {
        dir = directory ?? URL.cachesDirectory.appending(path: "file-thumbnails", directoryHint: .isDirectory)
        images.totalCostLimit = 24 << 20
    }

    /// `size` 0 with no `modified`: only the path is known (not an empty file).
    nonisolated static func wanted(name: String, size: Int64, modified: Date? = .distantPast) -> Bool {
        (size > 0 || modified == nil) && extensions.contains((name as NSString).pathExtension.lowercased())
    }

    nonisolated static func key(group: String, machine: String, path: String, size: Int64, modified: Date?) -> String {
        let id = [group, machine, path, String(size), String(Int64((modified?.timeIntervalSince1970 ?? 0) * 1000))].joined(separator: "\n")
        return SHA256.hash(data: Data(id.utf8)).prefix(16).map { String(format: "%02x", $0) }.joined()
    }

    /// The size to ask for, in pixels: the node takes 32–1024.
    nonisolated static func pixels(side: CGFloat, scale: CGFloat) -> Int { min(1024, max(32, Int((side * scale).rounded(.up)))) }

    func cached(_ key: String) -> UIImage? { images.object(forKey: key as NSString) }

    /// The thumbnail of a listed file, `side` points on its long edge; nil when there is none (and not asked again).
    func thumbnail(_ ref: FileRef, size: Int64, modified: Date?, side: CGFloat, scale: CGFloat, api: APIClient) async -> UIImage? {
        let key = Self.key(group: ref.group, machine: ref.machineID, path: ref.path, size: size, modified: modified)
        let settled = checked.contains(key)
        if settled, let image = images.object(forKey: key as NSString) { return image }
        guard !failed.contains(key) else { return nil }
        let file = dir.appending(path: key), tagFile = dir.appending(path: key + ".etag")
        let document = Self.isDocument(ref.path)
        let disk = await Task.detached(priority: .utility) { () -> (UIImage?, String?) in
            (Self.decode(try? Data(contentsOf: file), document: document), try? String(contentsOf: tagFile, encoding: .utf8))
        }.value
        if let image = disk.0 {
            store(image, key)
            if settled { return image }
        }
        await acquire()
        defer { release() }
        guard !Task.isCancelled else { return disk.0 }
        let query = ["path": ref.path, "cwd": ref.cwd ?? "", "size": String(Self.pixels(side: side, scale: scale))]
        let answer: (data: Data?, etag: String?)
        do {
            answer = try await api.conditionalGet("/api/v1/machines/\(ref.machineID)/file/thumb", query: query, etag: disk.0 == nil ? nil : disk.1)
        } catch {
            if !Task.isCancelled { failed.insert(key) } // no_thumbnail, an old node: the symbol for this session
            return disk.0
        }
        checked.insert(key)
        guard let data = answer.data else { return disk.0 } // 304: the one on disk is current
        guard let image = await Task.detached(priority: .utility, operation: { Self.decode(data, document: document) }).value else {
            failed.insert(key)
            return nil
        }
        let dir = dir, tag = answer.etag
        await Task.detached(priority: .utility) {
            try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try? data.write(to: file, options: .atomic)
            if let tag { try? tag.write(to: tagFile, atomically: true, encoding: .utf8) } else { try? FileManager.default.removeItem(at: tagFile) }
        }.value
        store(image, key)
        return image
    }

    private func store(_ image: UIImage, _ key: String) {
        images.setObject(image, forKey: key as NSString, cost: Int(image.size.width * image.size.height * 4 * 4))
    }

    private func acquire() async {
        if running < 3 {
            running += 1
            return
        }
        await withCheckedContinuation { waiting.append($0) }
    }

    private func release() {
        if waiting.isEmpty { running -= 1 } else { waiting.removeFirst().resume() }
    }

    /// A page of a document (not a picture or a video): its thumbnail is a light page, or it is not one.
    nonisolated static func isDocument(_ path: String) -> Bool {
        let ext = (path as NSString).pathExtension.lowercased()
        return QuickLookFileType.extensions.contains(ext) || ext == "pdf"
    }

    /// `document`: a dark page is not taken. Quick Look on a Mac in dark mode draws RTF and text on a dark page,
    /// which showed as a black square in the list; the symbol says more.
    nonisolated static func decode(_ data: Data?, document: Bool) -> UIImage? {
        guard let data, let src = CGImageSourceCreateWithData(data as CFData, nil),
              let cg = CGImageSourceCreateImageAtIndex(src, 0, nil) else { return nil }
        if document, let light = lightness(cg), light < 0.35 { return nil }
        #if os(iOS) || os(visionOS)
        return UIImage(cgImage: cg)
        #else
        return NSImage(cgImage: cg, size: NSSize(width: cg.width, height: cg.height))
        #endif
    }

    /// How light a picture is on the whole, 0 (black) to 1 (white): drawn into 8 × 8 gray pixels over white.
    nonisolated static func lightness(_ cg: CGImage) -> Double? {
        let side = 8
        var pixels = [UInt8](repeating: 0, count: side * side)
        let drawn = pixels.withUnsafeMutableBytes { buf -> Bool in
            guard let ctx = CGContext(data: buf.baseAddress, width: side, height: side, bitsPerComponent: 8, bytesPerRow: side,
                                      space: CGColorSpaceCreateDeviceGray(), bitmapInfo: CGImageAlphaInfo.none.rawValue) else { return false }
            ctx.setFillColor(gray: 1, alpha: 1)
            ctx.fill(CGRect(x: 0, y: 0, width: side, height: side))
            ctx.draw(cg, in: CGRect(x: 0, y: 0, width: side, height: side))
            return true
        }
        guard drawn else { return nil }
        return Double(pixels.reduce(0) { $0 + Int($1) }) / Double(pixels.count * 255)
    }
}

/// A file's symbol, replaced by its thumbnail once there is one (`FileThumbnails`): the Files tab's rows at the default
/// size, a chat's file card bigger (`width` × `height`, the picture fitted in). Size 0 and no `modified`: only the path
/// is known, the thumbnail is checked by its ETag.
struct FileThumbnailIcon: View {
    let ref: FileRef
    let size: Int64
    let modified: Date?
    let symbol: String
    var width: CGFloat = Theme.Size.icon
    var height: CGFloat = Theme.Size.wellM
    @Environment(AppModel.self) private var model
    @Environment(\.displayScale) private var displayScale
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var image: UIImage?

    private var side: CGFloat { max(width, height) * Theme.sizeScale(typeSize) }
    private var wanted: Bool { FileThumbnails.wanted(name: ref.path, size: size, modified: modified) }
    private var key: String { FileThumbnails.key(group: ref.group, machine: ref.machineID, path: ref.path, size: size, modified: modified) }

    var body: some View {
        Group {
            if let image = image ?? (wanted ? FileThumbnails.shared.cached(key) : nil) {
                Image(uiImage: image).resizable().scaledToFit()
                    .clipShape(.rect(cornerRadius: Theme.Radius.thumbnail, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: Theme.Radius.thumbnail, style: .continuous).stroke(Theme.Fill.pressed, lineWidth: Theme.Size.hairline))
                    .scaledFrame(width: width, height: height)
                    .accessibilityHidden(true)
            } else {
                ThemedIcon(symbol)
                    .font(Theme.Font.body)
                    .foregroundStyle(Color.secondary)
                    .scaledFrame(width: width)
            }
        }
        .task(id: key) {
            guard wanted, image == nil, model.features(ref.group).contains("file_thumb"), let api = model.api(ref.group) else { return }
            image = await FileThumbnails.shared.thumbnail(ref, size: size, modified: modified, side: side, scale: displayScale, api: api)
        }
    }
}
