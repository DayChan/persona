import Foundation
import Photos
#if canImport(UIKit)
import UIKit
#endif

/// Photo tools: list recent assets and hand one image back to Claude.
enum PhotosTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_photos_recent",
                    description: """
                    List recent photos and videos, newest first. Each row has the Photos local \
                    identifier, creation date (ISO 8601), media type (image/video), pixel width and \
                    height, whether it is a screenshot, whether it is a favorite, duration in seconds \
                    for videos, and the latitude/longitude where the photo was taken when it is \
                    geotagged. Pass an id to phone_photo_get to actually see the image.
                    """,
                    inputSchema: Schema.object([
                        "limit": Schema.integer("Maximum number of assets, default 20, max 100.", min: 1, max: 100),
                        "days": Schema.integer("Only assets from the last N days. Default: no limit.", min: 1, max: 3650),
                        "screenshots_only": Schema.boolean("Only screenshots. Default false."),
                    ])
                ),
                source: .photos,
                run: { args in try await recent(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_photo_get",
                    description: """
                    Get one photo as an image so you can look at it (read a screenshot, describe a \
                    photo, transcribe a whiteboard). Pass the id from phone_photos_recent. The image \
                    is JPEG, at most max_size pixels on the long edge (default 1280, max 2048), \
                    quality 0.7. Returns the image plus a short text description.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Photos local identifier from phone_photos_recent."),
                        "max_size": Schema.integer("Longest edge in pixels, default 1280, max 2048.", min: 256, max: 2048),
                    ], required: ["id"])
                ),
                source: .photos,
                run: { args in try await image(args) }
            ),
        ]
    }

    // MARK: - Listing

    static func recent(_ args: JSONValue) async throws -> [ToolContent] {
        try PhotosAccess.requireAccess()
        let limit = ToolArgs.int(args, "limit", fallback: 20, min: 1, max: 100)
        let days = ToolArgs.int(args, "days")
        let screenshotsOnly = ToolArgs.bool(args, "screenshots_only") ?? false

        let options = PHFetchOptions()
        options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: false)]
        if let days {
            let from = Calendar.current.date(byAdding: .day, value: -days, to: Date()) ?? Date.distantPast
            options.predicate = NSPredicate(format: "creationDate >= %@", from as NSDate)
        }
        let fetched = PHAsset.fetchAssets(with: options)
        var rows: [JSONValue] = []
        fetched.enumerateObjects { asset, _, stop in
            if screenshotsOnly && !asset.mediaSubtypes.contains(.photoScreenshot) { return }
            guard rows.count < limit else {
                stop.pointee = true
                return
            }
            rows.append(encode(asset))
        }
        if rows.isEmpty {
            return Output.json(Output.object([
                ("count", .number(0)),
                ("assets", .array([])),
                ("note", .string(screenshotsOnly
                    ? "No screenshots matched. Try without screenshots_only."
                    : "No photos matched. The user may have granted access to only some photos.")),
            ]))
        }
        return Output.json(Output.object([
            ("count", .number(Double(rows.count))),
            ("access", .string(PhotosAccess.status() == .limited ? "limited" : "full")),
            ("assets", .array(rows)),
        ]))
    }

    static func encode(_ asset: PHAsset) -> JSONValue {
        let subtypes: [String] = [
            (asset.mediaSubtypes.contains(.photoScreenshot), "screenshot"),
            (asset.mediaSubtypes.contains(.photoLive), "live_photo"),
            (asset.mediaSubtypes.contains(.photoPanorama), "panorama"),
            (asset.mediaSubtypes.contains(.videoHighFrameRate), "slo_mo"),
        ].compactMap { $0.0 ? $0.1 : nil }

        var place: JSONValue?
        if let location = asset.location {
            place = Output.object([
                ("latitude", .number(location.coordinate.latitude)),
                ("longitude", .number(location.coordinate.longitude)),
            ])
        }
        return Output.object([
            ("id", .string(asset.localIdentifier)),
            ("created", .string(ToolDate.string(asset.creationDate ?? asset.modificationDate ?? Date()))),
            ("type", .string(asset.mediaType == .video ? "video" : asset.mediaType == .image ? "image" : "other")),
            ("width", .number(Double(asset.pixelWidth))),
            ("height", .number(Double(asset.pixelHeight))),
            ("favorite", .bool(asset.isFavorite)),
            ("subtypes", subtypes.isEmpty ? nil : .array(subtypes.map { .string($0) })),
            ("duration_seconds", asset.mediaType == .video ? .number(round1(asset.duration)) : nil),
            ("location", place),
        ])
    }

    // MARK: - Image

    static func image(_ args: JSONValue) async throws -> [ToolContent] {
        try PhotosAccess.requireAccess()
        guard let id = ToolArgs.string(args, "id") else {
            throw ToolFailure(.invalidArgument, "id is required")
        }
        let maxSize = ToolArgs.int(args, "max_size", fallback: 1280, min: 256, max: 2048)

        let fetched = PHAsset.fetchAssets(withLocalIdentifiers: [id], options: nil)
        guard let asset = fetched.firstObject else {
            throw ToolFailure(.invalidArgument, "no photo with id \"\(id)\"; call phone_photos_recent for current ids")
        }
        guard asset.mediaType == .image else {
            throw ToolFailure(.unavailable, "这条是视频，只支持图片 / That asset is a video; only still images can be returned") // i18n-ignore
        }

        let data: Data = try await loadImageData(asset)
        guard let source = UIImage(data: data) else {
            throw ToolFailure(.unavailable, "无法解码这张照片 / Could not decode the photo data") // i18n-ignore
        }
        let resized = resized(source, maxEdge: CGFloat(maxSize))
        guard let jpeg = resized.jpegData(compressionQuality: 0.7) else {
            throw ToolFailure(.unavailable, "无法编码这张照片 / Could not encode the photo as JPEG") // i18n-ignore
        }

        let description = Output.object([
            ("id", .string(asset.localIdentifier)),
            ("created", .string(ToolDate.string(asset.creationDate ?? asset.modificationDate ?? Date()))),
            ("width", .number(Double(asset.pixelWidth))),
            ("height", .number(Double(asset.pixelHeight))),
            ("returned_width", .number(Double(resized.size.width * resized.scale))),
            ("returned_height", .number(Double(resized.size.height * resized.scale))),
            ("bytes", .number(Double(jpeg.count))),
        ])
        return [.text(description.pretty), .image(jpeg, mime: "image/jpeg")]
    }

    /// Asks Photos for the full-size representation, so iCloud originals are
    /// downloaded when needed.
    /// Also the composer's recent screenshot (`RecentScreenshots.take`).
    static func loadImageData(_ asset: PHAsset) async throws -> Data {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Data, any Error>) in
            let options = PHImageRequestOptions()
            options.isNetworkAccessAllowed = true
            options.deliveryMode = .highQualityFormat
            options.isSynchronous = false
            options.version = .current
            let gate = SingleResume(continuation)
            // May come back off the main thread (an iCloud download): `@Sendable` keeps it off the main actor.
            PHImageManager.default().requestImageDataAndOrientation(for: asset, options: options) { @Sendable data, _, _, info in
                // The handler can be called more than once (degraded, then full).
                if let cancelled = info?[PHImageCancelledKey] as? Bool, cancelled {
                    gate.fail(ToolFailure(.unavailable, "照片读取被取消 / The photo request was cancelled")) // i18n-ignore
                    return
                }
                if let error = info?[PHImageErrorKey] as? any Error {
                    gate.fail(error)
                    return
                }
                if let data, !data.isEmpty {
                    gate.succeed(data)
                } else if let inCloud = info?[PHImageResultIsInCloudKey] as? Bool, inCloud {
                    gate.fail(ToolFailure(.unavailable, "照片还在 iCloud 里且无法下载 / The photo is in iCloud and could not be downloaded")) // i18n-ignore
                }
            }
        }
    }

    private static func resized(_ image: UIImage, maxEdge: CGFloat) -> UIImage {
        let longEdge = max(image.size.width, image.size.height) * image.scale
        guard longEdge > maxEdge else { return image }
        let factor = maxEdge / longEdge
        let target = CGSize(width: (image.size.width * image.scale * factor).rounded(),
                            height: (image.size.height * image.scale * factor).rounded())
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        return UIGraphicsImageRenderer(size: target, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: target))
        }
    }

    private static func round1(_ value: Double) -> Double {
        (value * 10).rounded() / 10
    }
}
