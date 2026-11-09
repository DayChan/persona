import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// UI version 6's pictures (docs/extensions.md「UI version 6」): an `image` component, and the `ref` pictures of rows,
// tiles, headers and comments. A `url` is https and loaded without any of the app's credentials
// (`ExtensionRemoteImage`); a `ref` comes back from the hub as a `path` it signed, `/api/v1/extensions/{id}/-/image/{token}`,
// which only `ExtensionHubImages` loads, with the device's own credentials, and only for that pattern and the extension
// whose page it is. The two never mix: the hub's token is never sent to an address an extension wrote.

/// Where a page's `ref` pictures are loaded: the extension the page is of and the hub it is on. Set by the page's
/// renderer; without it (an assistant card, which has no `ref`), a `path` is not loaded.
struct ExtensionImageScope {
    var owner: String
    var api: () -> APIClient?
    /// The hub said 410 (the page was drawn more than 15 minutes ago): draw the page again for new tokens.
    var expired: () -> Void
}

extension EnvironmentValues {
    @Entry var extensionImageScope: ExtensionImageScope? = nil
}

/// A picture from the hub's image proxy, by its signed path: kept in memory (a page drawn again gets new tokens for
/// the same pictures, so the last one stays on screen until the new one is in), requests for the same path shared.
@MainActor final class ExtensionHubImages {
    static let shared = ExtensionHubImages()

    enum Outcome { case image(UIImage), expired, failed }

    private let cache = NSCache<NSString, UIImage>()
    private var inflight: [String: Task<Outcome, Never>] = [:]

    private init() { cache.totalCostLimit = 64 << 20 }

    /// The hub's own image path for `owner`, nil for anything else (another extension's, another route, a full URL).
    nonisolated static func checked(_ path: String?, owner: String) -> String? {
        guard let path, !owner.isEmpty else { return nil }
        let prefix = "/api/v1/extensions/" + owner + "/-/image/"
        guard path.hasPrefix(prefix) else { return nil }
        let token = path.dropFirst(prefix.count)
        let allowed = Set("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-")
        guard !token.isEmpty, token.count <= 2048, token.allSatisfy(allowed.contains), !owner.contains("/") else { return nil }
        return path
    }

    /// The pixel width to ask for: the step at or above what the picture is drawn at (320 / 640 / 1280 / 2048).
    nonisolated static func width(points: CGFloat, scale: CGFloat) -> Int {
        let px = points * max(scale, 1)
        return [320, 640, 1280].first { CGFloat($0) >= px } ?? 2048
    }

    func cached(_ path: String, width: Int) -> UIImage? { cache.object(forKey: key(path, width)) }

    func image(_ path: String, width: Int, api: APIClient) async -> Outcome {
        let k = key(path, width) as String
        if let img = cache.object(forKey: k as NSString) { return .image(img) }
        if let t = inflight[k] { return await t.value }
        let t = Task<Outcome, Never> {
            do {
                var api = api
                api.session = APIClient.uncachedSession
                let data = try await api.request("GET", path, query: ["width": String(width)], timeout: 20)
                guard let img = UIImage(data: data) else { return .failed }
                return .image(img)
            } catch let e as APIError where e.status == 410 {
                return .expired
            } catch {
                return .failed
            }
        }
        inflight[k] = t
        let out = await t.value
        inflight[k] = nil
        if case .image(let img) = out { cache.setObject(img, forKey: k as NSString, cost: Self.cost(img)) }
        return out
    }

    private func key(_ path: String, _ width: Int) -> NSString { "\(width)@\(path)" as NSString }

    private static func cost(_ img: UIImage) -> Int {
        #if os(macOS)
        Int(img.size.width * img.size.height * 4)
        #else
        Int(img.size.width * img.scale * img.size.height * img.scale * 4)
        #endif
    }
}

/// An extension's picture, wherever it comes from: an https `url` without credentials, or the hub's signed `path`
/// (`ExtensionHubImages`). A placeholder while it loads; `failed` says it could not be had.
struct ExtensionPicture: View {
    var url: String?
    var path: String?
    var fill = true
    /// The width it is drawn at, for the size the hub asks the extension for.
    var points: CGFloat = Theme.Size.box300
    @Binding var failed: Bool
    /// The picture once it is in (the full-screen viewer shows it without loading it again).
    var loaded: (UIImage) -> Void = { _ in }
    @Environment(\.extensionImageScope) private var scope
    @Environment(\.displayScale) private var displayScale
    @State private var image: UIImage?
    @State private var expiredOnce = false

    var body: some View {
        ZStack {
            Rectangle().fill(Theme.Fill.chip)
            if let image {
                Image(uiImage: image).resizable().aspectRatio(contentMode: fill ? .fill : .fit)
            }
        }
        .task(id: (url ?? "") + "|" + (path ?? "")) { await load() }
    }

    private func load() async {
        if let s = url, let u = ExtensionComponentRowLabel.image(s) {
            guard let data = try? await ExtensionRemoteImage.session.data(from: u).0, let img = UIImage(data: data) else {
                if image == nil { failed = true }
                return
            }
            show(img)
            return
        }
        guard let scope, let p = ExtensionHubImages.checked(path, owner: scope.owner), let api = scope.api() else {
            failed = true
            return
        }
        let w = ExtensionHubImages.width(points: points, scale: displayScale)
        if let img = ExtensionHubImages.shared.cached(p, width: w) { show(img); return }
        switch await ExtensionHubImages.shared.image(p, width: w, api: api) {
        case .image(let img): show(img)
        case .expired:
            // Once per picture: a page that keeps answering with old tokens is not asked again and again.
            if !expiredOnce { expiredOnce = true; scope.expired() }
            if image == nil { failed = true }
        case .failed:
            if image == nil { failed = true }
        }
    }

    private func show(_ img: UIImage) {
        image = img
        failed = false
        loaded(img)
    }
}

nonisolated extension ExtensionImageSource {
    /// The width over the height the extension says it has, when it says both.
    var ratio: CGFloat? {
        guard let w = width, let h = height, w > 0, h > 0 else { return nil }
        return CGFloat(w) / CGFloat(h)
    }
}

/// An extension's `image`: a frame of its aspect drawn before the picture comes (the page does not jump), the picture
/// cropped to it or fitted in it, its caption under it; a tap opens it full screen unless `no_zoom`. A picture that
/// cannot be had says its `alt` (as VoiceOver does) with the broken-picture mark.
struct ExtensionImageItem: View {
    var item: ExtensionImageComponent
    @State private var failed = false
    @State private var loaded: UIImage?
    @State private var viewing: ViewerImage?

    /// The aspect's width over height: square 1:1, landscape 4:3 (the default), wide 16:9, portrait 3:4, auto from the
    /// size it says (4:3 until it says).
    static func ratio(_ aspect: String?, source: ExtensionImageSource, loaded: CGSize? = nil) -> CGFloat {
        switch aspect {
        case "square": return 1
        case "wide": return 16 / 9
        case "portrait": return 3 / 4
        case "auto":
            if let r = source.ratio { return r }
            if let s = loaded, s.width > 0, s.height > 0 { return s.width / s.height }
            return 4 / 3
        default: return 4 / 3
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            picture
            if let c = item.caption, !c.isEmpty {
                Text(c).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(3)
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .fullScreenCover(item: $viewing) { v in ImageViewer(items: [v], index: 0) }
    }

    private var picture: some View {
        let frame = Color.clear
            .aspectRatio(Self.ratio(item.aspect, source: item.src, loaded: loaded?.size), contentMode: .fit)
            .frame(maxWidth: .infinity)
            .overlay {
                ExtensionPicture(url: item.src.url, path: item.src.path, fill: item.fit != "fit", failed: $failed,
                                 loaded: { loaded = $0 })
                    .overlay { if failed { failure } }
            }
            .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(item.alt)
            .accessibilityAddTraits(.isImage)
            .accessibilityIdentifier("plugin-ui-image-\(item.key ?? item.alt)")
        return Group {
            if item.noZoom != true {
                Button { open() } label: { frame }
                    .buttonStyle(.plain)
                    .disabled(loaded == nil)
                    .accessibilityHint(String(localized: "全屏查看"))
            } else {
                frame
            }
        }
    }

    private var failure: some View {
        VStack(spacing: Theme.Space.s) {
            Image(systemName: Theme.Icon.brokenImage).font(Theme.Font.title).foregroundStyle(.tertiary)
            Text(item.alt).font(Theme.Font.caption).foregroundStyle(.secondary).multilineTextAlignment(.center).lineLimit(3)
        }
        .padding(Theme.Space.m)
    }

    private func open() {
        guard let img = loaded, let payload = Self.payload(img) else { return }
        viewing = ViewerImage(payload: payload, title: item.caption?.isEmpty == false ? item.caption : item.alt)
    }

    /// The picture as the viewer's bytes: what was loaded, not loaded again (a hub token may be gone by then).
    static func payload(_ img: UIImage) -> ImagePayload? {
        #if os(macOS)
        guard let tiff = img.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff),
              let png = rep.representation(using: .png, properties: [:]) else { return nil }
        var p = ImagePayload(mime: "image/png", data: png.base64EncodedString())
        p.width = rep.pixelsWide
        p.height = rep.pixelsHigh
        #else
        guard let jpeg = img.jpegData(compressionQuality: 0.92) else { return nil }
        var p = ImagePayload(mime: "image/jpeg", data: jpeg.base64EncodedString())
        p.width = Int(img.size.width * img.scale)
        p.height = Int(img.size.height * img.scale)
        #endif
        return p
    }
}

/// A row's or a tile's thumbnail: its https `url` without credentials, or the hub's `path` for a `ref`.
struct ExtensionThumbnailImage: View {
    var url: String?
    var path: String?
    var points: CGFloat = Theme.Size.box80
    @State private var failed = false

    var body: some View {
        ExtensionPicture(url: url, path: path, points: points, failed: $failed)
            .overlay { if failed { Image(systemName: Theme.Icon.brokenImage).font(Theme.Font.label).foregroundStyle(.tertiary) } }
    }
}

/// A person's or a chat's picture (a row's, a header's, a comment's avatar): its https `url`, the hub's `path` for a
/// `ref`, else (or when it cannot be had) the initials.
struct ExtensionAvatarImage: View {
    var url: String?
    var path: String?
    var text: String
    var size: CGFloat = Theme.Size.wellL
    @State private var failed = false

    var body: some View {
        Group {
            if let u = url.flatMap(ExtensionComponentRowLabel.image) {
                ExtensionRemoteImage(url: u, fallback: text)
            } else if path != nil, !failed {
                ExtensionPicture(url: nil, path: path, points: size, failed: $failed)
            } else {
                Monogram(name: text, size: size, fill: Theme.Fill.chip, font: Theme.Font.labelStrong, letters: 2)
            }
        }
        .scaledFrame(width: size, height: size)
        .clipShape(.circle)
    }
}
