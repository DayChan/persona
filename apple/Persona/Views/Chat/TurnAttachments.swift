import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// What a turn made and looked at (#1543): the documents its answer names, as cards under the answer's words
// (`DocumentCards`, part of `AssistantText`), and the images its work looked at, a strip of bare thumbnails between the
// work line and the answer (`ViewedImagesRow`, a line of its own, so pictures coming in while the turn runs leave the
// answer alone). Names and sizes show on a long press, on hover on the Mac, and in the viewer, never under a thumbnail.

/// The height of a card and of the strip's thumbnails.
private let tileHeight = Theme.Size.box64

/// A thumbnail is as wide as its picture (between ¾ and 1.6 of its height): a phone screenshot narrow, a screen wide.
private func tileWidth(_ aspect: CGFloat?, height: CGFloat) -> CGFloat {
    height * min(max(aspect ?? 1, 0.75), 1.6)
}

/// A document as a card: `thumb` on the left (its first page), its name, and what it is.
struct AttachmentCard<Thumb: View>: View {
    var title: String
    var subtitle: String
    @ViewBuilder var thumb: Thumb

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            thumb
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.secondaryStrong).foregroundStyle(.primary).lineLimit(1).truncationMode(.middle)
                Text(subtitle).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Space.s)
        .scaledFrame(width: Theme.Size.box240, height: tileHeight, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .contentShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }
}

/// A row that scrolls sideways from the page's edge to the screen's, fading where there is more: what the strip
/// and the cards scroll in.
private struct AttachmentScroller<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: Theme.Space.s) { content }
        }
        .scrollIndicators(.hidden)
        // Out to the list's margins, so the row scrolls to the screen's edge and starts where the text does.
        .padding(.horizontal, -Theme.Space.page)
        .contentMargins(.horizontal, Theme.Space.page, for: .scrollContent)
        .edgeFade()
    }
}

// MARK: Documents

/// The documents an answer names (Word, Excel, PowerPoint, iWork, PDF: what has a first page to show), as cards with
/// that page (`FileThumbnailIcon`, M1); a tap opens the file as its chip did. Other files stay chips (`FileChips`).
struct DocumentCards: View {
    @Environment(AppModel.self) private var model
    @Environment(\.fileScope) private var scope
    var paths: [String]
    var open: (String) -> Void

    private var shown: [String] {
        guard let scope else { return [] }
        return Array(paths.filter { model.filePresence.linkable($0, in: scope) }.prefix(6))
    }

    var body: some View {
        let shown = shown
        if let scope, !shown.isEmpty {
            AttachmentScroller {
                ForEach(shown, id: \.self) { p in
                    Button { open(p) } label: {
                        AttachmentCard(title: Self.name(p), subtitle: Self.kind(p)) {
                            FileThumbnailIcon(ref: FileRef(group: scope.machine.group, machineID: scope.machine.id, path: p, cwd: scope.cwd),
                                              size: 0, modified: nil, symbol: FileViewer.symbol(for: p),
                                              width: Theme.Size.wellL, height: Theme.Size.avatarXL)
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("file-card-" + p)
                    .contextMenu { Button { UIPasteboard.general.string = p } label: { Label("Copy Path", systemImage: Theme.Icon.copy) } }
                }
            }
        }
    }

    static func isDocument(_ path: String) -> Bool { FileThumbnails.isDocument(path) }

    static func name(_ path: String) -> String {
        let name = (path as NSString).lastPathComponent
        return name.isEmpty ? path : name
    }

    /// What a document is, as the Files app says it.
    static func kind(_ path: String) -> String {
        switch (path as NSString).pathExtension.lowercased() {
        case "docx", "doc": String(localized: "Word document")
        case "xlsx", "xls": String(localized: "Excel spreadsheet")
        case "pptx", "ppt": String(localized: "PowerPoint presentation")
        case "pages": String(localized: "Pages document")
        case "numbers": String(localized: "Numbers spreadsheet")
        case "key": String(localized: "Keynote presentation")
        case "rtf": String(localized: "RTF document")
        case "pdf": String(localized: "PDF document")
        default: String(localized: "Document")
        }
    }
}

// MARK: Images the turn looked at

/// The images a turn's work looked at, between the work line and the answer (`ViewedImages`): thumbnails as tall as a
/// card in a strip that scrolls sideways, one alone a little bigger. Nothing written around them: a tap opens the viewer
/// on this turn's images, a long press (hover on the Mac) shows the picture with its name and where it came from.
struct ViewedImagesRow: View, Equatable {
    var images: [ViewedImage]
    nonisolated static func == (a: Self, b: Self) -> Bool { a.images == b.images }
    @Environment(AppModel.self) private var model
    @Environment(\.viewImages) private var viewImages
    @Environment(\.fileScope) private var scope
    @Environment(\.imageZoom) private var pageZoom

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("ViewedImagesRow")
        #endif
        Group {
            if images.count == 1, let im = images.first {
                tapped(im, index: 0) { ViewedImageThumb(image: im, height: Theme.Size.box80, badge: true) }
            } else {
                AttachmentScroller {
                    ForEach(Array(images.enumerated()), id: \.element.id) { n, im in
                        tapped(im, index: n) { ViewedImageThumb(image: im, height: tileHeight, badge: true) }
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        #if DEBUG
        // Screenshots: `-openViewedImage N` opens the viewer on the strip's Nth image (1 is the first) once it shows.
        // Once per launch: the list builds the row again as the chat updates, and a second open swapped the shown
        // viewer for a new one mid-presentation, which closed it.
        .task {
            let n = UserDefaults.standard.integer(forKey: "openViewedImage")
            if n > 0, images.indices.contains(n - 1), !Self.debugOpened {
                Self.debugOpened = true
                try? await Task.sleep(for: .seconds(1))
                open(n - 1)
            }
        }
        #endif
    }

    private func tapped<Content: View>(_ im: ViewedImage, index: Int, @ViewBuilder label: () -> Content) -> some View {
        let caption = caption(im)
        // A picture that did not load: the tap asks the hub again instead of opening a viewer with nothing in it (#559).
        let again = ImageRetries.shared.canRetry(im.payload)
        return Button { if !ImageRetries.shared.retry(im.payload) { open(index) } } label: { label() }
            .buttonStyle(.plain)
            .matchedTransitionSource(id: "viewed-" + im.id, in: pageZoom ?? fallbackZoom)
            .accessibilityLabel(again ? Text("Image didn't load. Tap to retry") : Text(caption))
            .accessibilityHint(again ? Text(verbatim: "") : Text("View full size"))
            .accessibilityIdentifier("viewed-image-\(index)")
            #if os(macOS)
            .help(caption)
            #endif
            .contextMenu {
                if let path = im.path {
                    Button { UIPasteboard.general.string = path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
                }
            } preview: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    ViewedImageThumb(image: im, height: Theme.Size.box240)
                    Text(caption).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                .padding(Theme.Space.m)
                // The preview is drawn in the context menu's own host, which the app's environment does not reach: the
                // thumbnail's model was missing there and the app crashed on a long press (#1490).
                .environment(model)
                .environment(\.fileScope, scope)
            }
    }
    @Namespace private var fallbackZoom
    #if DEBUG
    @MainActor private static var debugOpened = false
    #endif

    private func open(_ index: Int) {
        Haptics.tap()
        let items = images.map { im in
            ViewerImage(payload: im.payload, file: file(im), title: title(im), detail: machineName(im)) as ViewerImage
        }
        viewImages?(ImageViewing(images: [], index: index, conversation: [], source: "viewed-" + images[index].id, viewed: items))
    }

    private func file(_ im: ViewedImage) -> FileRef? {
        guard let path = im.path, let scope else { return nil }
        return FileRef(group: scope.machine.group, machineID: scope.machine.id, path: path, cwd: scope.cwd)
    }

    /// The name of the file, or what took the picture.
    func title(_ im: ViewedImage) -> String {
        switch im.source {
        case .screenshot: String(localized: "Screenshot")
        case .browser: String(localized: "Web page screenshot")
        case .read, .mcp, .tool: im.name ?? (im.source == .mcp ? Self.toolName(im.tool) : String(localized: "images"))
        }
    }

    /// What a long press, a hover and the viewer say: its name, and the machine of a screenshot or a picture's size
    /// (「屏幕截图 · Studio」「首页.png · 480×300」).
    func caption(_ im: ViewedImage) -> String {
        var parts = [title(im)]
        if let m = machineName(im) {
            parts.append(m)
        } else if let w = im.payload?.width, let h = im.payload?.height {
            parts.append("\(w)×\(h)")
        }
        return parts.joined(separator: " · ")
    }

    /// The machine a screenshot or a browser's call named, by its name here.
    func machineName(_ im: ViewedImage) -> String? {
        guard let m = im.machine else { return nil }
        return model.machines.first { $0.id == m && $0.group == (scope?.machine.group ?? $0.group) }?.name
            ?? model.machines.first { $0.name == m }?.name ?? m
    }

    /// An MCP tool's own name ("mcp__server__snap" → "snap").
    static func toolName(_ tool: String) -> String {
        guard tool.hasPrefix("mcp__"), let r = tool.range(of: "__", options: .backwards) else { return tool }
        return String(tool[r.upperBound...])
    }
}

/// One image the turn looked at, filling a rounded tile `height` tall (as wide as its shape allows): its bytes from the
/// hub (scaled down, cached by `ChatImageLoader`), or the file's thumbnail (`FileThumbnails`) when only its path is
/// known. `badge`: a small mark for a screenshot or a web page.
struct ViewedImageThumb: View {
    var image: ViewedImage
    var height: CGFloat
    var badge = false
    @Environment(AppModel.self) private var model
    @Environment(\.fileScope) private var scope
    @Environment(\.displayScale) private var displayScale
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var picture: UIImage?
    /// Whose last load gave no picture, asked again by the row's tap (#559). Only a payload's: a file's keeps its symbol.
    private let retries = ImageRetries.shared

    /// The long edge to decode to, in points: the picture fitted in it still fills the widest or the narrowest tile.
    private var side: CGFloat { height * Theme.sizeScale(typeSize) * 1.6 }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
        let aspect = picture.map { $0.size.width / max($0.size.height, 1) } ?? image.aspect
        ZStack(alignment: .bottomLeading) {
            Theme.Fill.well
            if let picture = picture ?? cached {
                Image(uiImage: picture).resizable().scaledToFill()
            } else if let p = image.payload, retries.didFail(p) {
                // Only a payload's failure is said: a file's thumbnail that did not come keeps its symbol
                // (`FileThumbnails` keeps that failure for the session, a retry would not ask again).
                ImageNotLoaded(canRetry: p.ref != nil)
            } else if let p = image.payload, retries.attempt(p) > 0 { // asked again: the tap shows it was taken
                ProgressView().controlSize(.small).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ThemedIcon(symbol).font(Theme.Font.secondary).foregroundStyle(.tertiary)
            }
        }
        .scaledFrame(width: tileWidth(aspect, height: height), height: height)
        .clipShape(shape)
        .overlay(shape.stroke(Theme.cardStroke, lineWidth: Theme.Size.hairline))
        .overlay(alignment: .bottomLeading) {
            // Not over the not-loaded placeholder on a small tile.
            if badge, !showsNotLoaded, image.source == .screenshot || image.source == .browser {
                ThemedIcon(symbol)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.white)
                    .scaledFrame(width: Theme.Size.iconXS, height: Theme.Size.iconXS)
                    .background(.black.opacity(0.45), in: .circle)
                    .padding(Theme.Space.xs)
                    .accessibilityHidden(true)
            }
        }
        .animation(Theme.Motion.state, value: picture != nil)
        .task(id: "\(image.id)#\(image.payload.map(retries.attempt) ?? 0)") {
            let loaded = await load()
            picture = loaded
            if let p = image.payload, !Task.isCancelled { retries.loaded(p, ok: loaded != nil) }
        }
    }

    /// The tile shows `ImageNotLoaded`: no picture, and its payload's load failed.
    private var showsNotLoaded: Bool { image.payload.map(retries.didFail) == true && picture == nil && cached == nil }

    private var symbol: String {
        switch image.source {
        case .screenshot: Theme.Icon.screen
        case .browser: Theme.Icon.safari
        case .read, .mcp, .tool: Theme.Icon.photo
        }
    }

    /// Back on screen: no placeholder flash.
    private var cached: UIImage? { image.payload.flatMap { ChatImageLoader.cached($0, side: side * displayScale) } }

    private func load() async -> UIImage? {
        if let p = image.payload { return await ChatImageLoader.image(p, side: side * displayScale) } // pixels: decoded at scale 1
        guard let path = image.path, let scope, model.features(scope.machine.group).contains("file_thumb"),
              let api = model.api(scope.machine.group) else { return nil }
        let ref = FileRef(group: scope.machine.group, machineID: scope.machine.id, path: path, cwd: scope.cwd)
        return await FileThumbnails.shared.thumbnail(ref, size: 0, modified: nil, side: side, scale: displayScale, api: api)
    }
}
