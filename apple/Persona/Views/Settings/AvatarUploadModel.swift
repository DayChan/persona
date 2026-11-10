import CoreGraphics
import Foundation
import ImageIO
import Observation
import UniformTypeIdentifiers

// Settings › Appearance › Avatar › New from Photo (#2405, docs/apple/persona.md "Uploading an image"): the user picks an
// image, crops it square (a circle shows what the avatar keeps), names it, and the hub makes a package of it
// (POST /api/v1/looks/uploads, only when the hub lists `look_uploads`). The phone then downloads the package and chooses
// it, as when a package is chosen in the picker; the hub chooses it too, for the other devices. Avatars made this way
// can be deleted again (DELETE /api/v1/looks/uploads/{id}).

// MARK: The hub

/// POST /api/v1/looks/uploads's body.
nonisolated struct LookUpload: Encodable, Sendable, Equatable {
    var name: String
    var subtitle: String?
    /// Base64 of a PNG or JPEG: the crop, square and upright.
    var image: String
    /// Chosen on the hub too, so the other devices follow.
    var choose: Bool
}

/// The avatar the hub made, as GET /api/v1/looks lists it (only what the phone uses).
nonisolated struct UploadedLook: Decodable, Sendable, Hashable {
    var id: String
    var name: String?
    var rev: String?
    var bytes: Int64?
    var manifest: PersonaManifest?
    var uploaded: Bool?
}

nonisolated extension APIClient {
    /// POST /api/v1/looks/uploads: makes an avatar from the image (docs/protocol/protocol.md). A refusal is an APIError
    /// whose message is the hub's, in the user's language, and whose `code` says which (no_name, too_large…).
    func uploadLook(_ upload: LookUpload) async throws -> UploadedLook {
        struct Answer: Decodable { var look: UploadedLook }
        // A photo of a megabyte or two in base64: more time than a plain request.
        let data = try await request("POST", "/api/v1/looks/uploads", body: upload, timeout: 120)
        return try JSON.decoder.decode(Answer.self, from: data).look
    }

    /// DELETE /api/v1/looks/uploads/{id}: only an avatar made from an upload (409 `not_uploaded` for any other).
    func deleteUploadedLook(_ id: String) async throws {
        _ = try await request("DELETE", "/api/v1/looks/uploads/\(id)")
    }
}

// MARK: The image

/// The picked image made ready to crop, and the crop made ready for the hub.
nonisolated enum AvatarImage {
    /// Pictures are decoded no larger than this: a 48-megapixel photo would take 190 MB, and the crop sent is 1024 px.
    static let loadSide = 4096
    /// The largest crop sent (the hub keeps 512 px at most; docs/apple/persona.md "Uploading an image").
    static let outputSide = 1024
    /// The hub refuses a smaller side (`too_small`).
    static let minSide = 64
    /// A photo whose PNG is larger than this goes as JPEG instead.
    static let pngLimit = 1_500_000
    /// A file larger than this is not read at all.
    static let fileLimit = 100_000_000

    /// Why a picked file can't become an avatar.
    struct Problem: LocalizedError, Equatable {
        var errorDescription: String?
    }

    /// Decodes a picked image upright (its orientation applied), the first frame of an animated one, at most `loadSide`.
    static func load(_ data: Data) throws -> CGImage {
        guard let src = CGImageSourceCreateWithData(data as CFData, nil), CGImageSourceGetCount(src) > 0 else {
            throw Problem(errorDescription: String(localized: "This file isn't an image Persona can read. Choose a photo, PNG, JPEG, WebP or GIF."))
        }
        let opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceThumbnailMaxPixelSize: loadSide,
            kCGImageSourceShouldCacheImmediately: true,
        ]
        guard let image = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else {
            throw Problem(errorDescription: String(localized: "This file isn't an image Persona can read. Choose a photo, PNG, JPEG, WebP or GIF."))
        }
        guard min(image.width, image.height) >= minSide else {
            throw Problem(errorDescription: String(localized: "This image is too small. Choose one at least \(minSide) pixels on each side."))
        }
        return image
    }

    /// Reads an image file the user picked (the Mac's open panel, the Files app), within its security scope.
    static func read(_ url: URL) throws -> Data {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        if let size = try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize, size > fileLimit {
            throw Problem(errorDescription: String(localized: "This file is too large to be an avatar. Choose a smaller image."))
        }
        return try Data(contentsOf: url)
    }

    /// The part of `image` inside `rect` (its pixels), square, at most `outputSide`, in sRGB: the hub reads no color profile.
    static func render(_ image: CGImage, rect: CGRect) -> CGImage? {
        guard let cut = image.cropping(to: rect.integral) else { return nil }
        let side = min(outputSide, min(cut.width, cut.height))
        let opaque = isOpaque(image)
        guard side > 0, let space = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: side, height: side, bitsPerComponent: 8, bytesPerRow: 0, space: space,
                                  bitmapInfo: (opaque ? CGImageAlphaInfo.noneSkipLast : CGImageAlphaInfo.premultipliedLast).rawValue)
        else { return nil }
        ctx.interpolationQuality = .high
        ctx.draw(cut, in: CGRect(x: 0, y: 0, width: side, height: side))
        return ctx.makeImage()
    }

    /// PNG; a photo whose PNG is large goes as JPEG (an image with transparency stays PNG: JPEG would fill it black).
    static func encode(_ image: CGImage) -> Data? {
        let png = encoded(image, as: .png)
        if let png, png.count <= pngLimit || !isOpaque(image) { return png }
        return encoded(image, as: .jpeg, quality: 0.9) ?? png
    }

    static func isOpaque(_ image: CGImage) -> Bool {
        switch image.alphaInfo {
        case .none, .noneSkipFirst, .noneSkipLast: true
        default: false
        }
    }

    private static func encoded(_ image: CGImage, as type: UTType, quality: Double? = nil) -> Data? {
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, type.identifier as CFString, 1, nil) else { return nil }
        let props = quality.map { [kCGImageDestinationLossyCompressionQuality: $0] as CFDictionary }
        CGImageDestinationAddImage(dest, image, props)
        return CGImageDestinationFinalize(dest) ? out as Data : nil
    }
}

// MARK: The crop

/// Where the square is on the image, in its pixels. Zoom 1 is the shorter side across; zooming in stops where the square
/// would be under 64 px (the hub's smallest) or an eighth of the shorter side.
nonisolated struct AvatarCrop: Equatable, Sendable {
    static let zoomLimit = 8.0

    let width: Double
    let height: Double
    private(set) var zoom: Double = 1
    /// The square's center.
    private(set) var center: CGPoint

    init(width: Int, height: Int) {
        self.width = Double(width)
        self.height = Double(height)
        center = CGPoint(x: Double(width) / 2, y: Double(height) / 2)
    }

    var shorter: Double { min(width, height) }
    var maxZoom: Double { max(1, min(Self.zoomLimit, shorter / Double(AvatarImage.minSide))) }
    /// The square's side in the image's pixels.
    var side: Double { shorter / zoom }

    /// The square in whole pixels, inside the image.
    var rect: CGRect {
        let s = max(1, min(shorter, side.rounded()))
        let x = min(max(0, (center.x - s / 2).rounded()), width - s)
        let y = min(max(0, (center.y - s / 2).rounded()), height - s)
        return CGRect(x: x, y: y, width: s, height: s)
    }

    /// Zoomed about the square's center, within 1…maxZoom.
    func zoomed(to z: Double) -> AvatarCrop {
        var c = self
        c.zoom = min(max(1, z.isFinite ? z : 1), maxZoom)
        c.clamp()
        return c
    }

    /// Moved by a drag of `translation` points over a square `viewSide` points wide: the picture follows the finger, so
    /// the square goes the other way. It stops at the image's edges.
    func panned(by translation: CGSize, viewSide: Double) -> AvatarCrop {
        guard viewSide > 0 else { return self }
        var c = self
        let k = side / viewSide
        c.center = CGPoint(x: center.x - translation.width * k, y: center.y - translation.height * k)
        c.clamp()
        return c
    }

    private mutating func clamp() {
        let h = side / 2
        center = CGPoint(x: min(max(center.x, h), width - h), y: min(max(center.y, h), height - h))
    }
}

// MARK: The crop sheet

/// The crop sheet (`AvatarCropView`): the picked image, where the square is, the name and description, and the upload.
/// After the hub made it, the avatar is downloaded and chosen on this phone (the hub chose it for the others).
@MainActor @Observable
final class AvatarUploadModel: Identifiable {
    struct Deps {
        var api: () -> APIClient?
        var store: () -> PersonaStore
        static var live: Deps { .init(api: { AppModel.shared.api }, store: { AppModel.shared.personas }) }
    }

    /// The hub's bounds (`bad_name`, `bad_subtitle`), in characters as it counts them (Unicode scalars).
    static let maxName = 40
    static let maxSubtitle = 80

    let id = UUID()
    let image: CGImage
    var crop: AvatarCrop
    var name = "" {
        didSet { if let cut = Self.clipped(name, to: Self.maxName) { name = cut } }
    }
    var subtitle = "" {
        didSet { if let cut = Self.clipped(subtitle, to: Self.maxSubtitle) { subtitle = cut } }
    }
    private(set) var uploading = false
    /// Why the last upload failed: the hub's own words when it refused.
    private(set) var error: String?
    /// The avatar the hub made (after a successful upload). Kept when it could not be downloaded here yet: trying again
    /// downloads it, without sending the image again (the hub would make a second one).
    private(set) var made: UploadedLook?
    /// Made, on this device and chosen: nothing more to do.
    private(set) var done = false
    /// What the store had heard of the hub's choices when the image went (`PersonaStore.heard`): a choice heard since
    /// is newer than the upload's `choose`.
    @ObservationIgnored private var asked = 0
    @ObservationIgnored private let deps: Deps

    init(image: CGImage, deps: Deps = .live) {
        self.image = image
        crop = AvatarCrop(width: image.width, height: image.height)
        self.deps = deps
    }

    var trimmedName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }
    var trimmedSubtitle: String { subtitle.trimmingCharacters(in: .whitespacesAndNewlines) }
    var canUpload: Bool { !uploading && !done && (made != nil || !trimmedName.isEmpty) }

    /// `text` cut to `max` Unicode scalars without splitting a character; nil when it fits.
    static func clipped(_ text: String, to max: Int) -> String? {
        guard text.unicodeScalars.count > max else { return nil }
        var out = "", n = 0
        for ch in text {
            n += ch.unicodeScalars.count
            if n > max { break }
            out.append(ch)
        }
        return out
    }

    /// What is sent for `image` (base64 of the crop's PNG or JPEG).
    func body(image: String) -> LookUpload {
        LookUpload(name: trimmedName, subtitle: trimmedSubtitle.isEmpty ? nil : trimmedSubtitle, image: image, choose: true)
    }

    /// Crops, encodes and sends the image; then the new avatar is downloaded and chosen here. True once it is chosen
    /// here; false (with `error`) when it was not made, or was made but could not be downloaded: then the next call only
    /// tries the download again. One at a time: a second tap while it runs does nothing.
    func upload() async -> Bool {
        guard canUpload else { return false }
        guard let api = deps.api() else {
            error = String(localized: "Not connected to the hub yet")
            return false
        }
        uploading = true
        defer { uploading = false }
        error = nil
        let look: UploadedLook
        if let made {
            look = made
        } else {
            let source = image, rect = crop.rect
            let encoded = await Task.detached(priority: .userInitiated) { () -> String? in
                AvatarImage.render(source, rect: rect).flatMap(AvatarImage.encode)?.base64EncodedString()
            }.value
            guard let encoded else {
                error = String(localized: "Couldn't prepare this image. Try another one.")
                return false
            }
            do {
                asked = deps.store().heard
                look = try await api.uploadLook(body(image: encoded))
            } catch {
                self.error = Self.message(for: error)
                return false
            }
            made = look
        }
        guard await adopt(look, api: api) else {
            error = String(localized: "The avatar was made, but this device couldn't download it. Try again.")
            return false
        }
        done = true
        return true
    }

    /// The new avatar in the store's list, on the phone and chosen. The list is read after the hub answered: a refresh
    /// that was under way may have asked before the avatar was made (`PersonaStore.reload`). The upload's `choose` chose
    /// it on the hub already: it is not sent again, and another device's choice heard since (newer) stays. True once
    /// it is on this device: chosen here, or replaced on the hub meanwhile.
    private func adopt(_ look: UploadedLook, api: APIClient) async -> Bool {
        let store = deps.store()
        if !store.offered.contains(where: { $0.id == look.id }) { await store.reload(api) }
        guard let p = store.offered.first(where: { $0.id == look.id }) else { return false }
        await store.choose(p, api: api, onHub: asked)
        return store.isOnPhone(p)
    }

    /// The hub's message for a refusal as it is (it is in the user's language already); for no answer at all, a line
    /// that says so.
    static func message(for error: any Error) -> String {
        error.shown ?? String(localized: "Couldn't reach the hub. Check the connection and try again.")
    }
}

// MARK: The picker

/// Settings › Appearance › Avatar's part in this: whether the hub makes avatars from images (`look_uploads`), the
/// picked image turned into a crop sheet, and deleting an avatar made that way.
@MainActor @Observable
final class AvatarUploadsModel {
    static let feature = "look_uploads"

    struct Deps {
        var api: () -> APIClient?
        var features: () -> Set<String>
        var isDemo: () -> Bool
        var store: () -> PersonaStore
        var crop: (CGImage) -> AvatarUploadModel = { AvatarUploadModel(image: $0) }
        static var live: Deps {
            .init(api: { AppModel.shared.api }, features: { AppModel.shared.hubFeatures }, isDemo: { AppModel.shared.isDemo },
                  store: { AppModel.shared.personas })
        }
    }

    /// Ids being deleted.
    private(set) var deleting: Set<String> = []
    /// Why the last pick or delete failed.
    private(set) var error: String?
    /// Reading the picked image.
    private(set) var loading = false
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    /// New from Photo is offered: the hub makes avatars from images (older hubs don't; Demo Mode has only the default).
    var available: Bool { !deps.isDemo() && deps.features().contains(Self.feature) }

    func canDelete(_ p: Persona) -> Bool { available && p.isUploaded }

    /// The crop sheet for the picked bytes; nil (with `error`) when they are not an image that can be one.
    func picked(_ data: Data) async -> AvatarUploadModel? {
        loading = true
        defer { loading = false }
        do {
            let image = try await Task.detached(priority: .userInitiated) { try AvatarImage.load(data) }.value
            error = nil
            return deps.crop(image)
        } catch {
            self.error = error.localizedDescription
            return nil
        }
    }

    /// The picked item could not be read at all (Photos had no data for it, the file was unreadable).
    func failedToRead(_ error: (any Error)?) {
        self.error = error?.shown ?? String(localized: "Couldn't open this image. Try another one.")
    }

    /// Deletes an avatar made from an upload. When it was chosen here, the default is chosen (the hub does the same for
    /// the other devices). One already gone from the hub counts as deleted.
    func delete(_ p: Persona) async -> Bool {
        guard canDelete(p), !deleting.contains(p.id), let api = deps.api() else { return false }
        let store = deps.store()
        let asked = store.heard // what the hub pushes after its own push of the default is newer (`PersonaStore.choose`)
        deleting.insert(p.id)
        defer { deleting.remove(p.id) }
        do {
            try await api.deleteUploadedLook(p.id)
        } catch let e as APIError where e.status == 404 {
            // Already gone: carry on as deleted.
        } catch {
            self.error = AvatarUploadModel.message(for: error)
            return false
        }
        error = nil
        if store.chosenID == p.id { await store.choose(.builtin, api: api, onHub: asked) } // the hub chose it already
        // Read after the delete: a refresh under way may still list it.
        await store.reload(api)
        return true
    }
}
