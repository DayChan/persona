import Foundation
import ImageIO
import Testing
@testable import Persona

@MainActor
struct QuickLookFileTests {
    @Test func officeAndIWorkGoToQuickLook() {
        for name in ["报告.docx", "old.DOC", "plan.xlsx", "a.xls", "deck.pptx", "b.ppt", "note.pages", "sheet.numbers", "talk.key"] {
            #expect(QuickLookFileType.matches(name: name, text: nil), "\(name)")
        }
        #expect(QuickLookFileType.matches(name: "notes.rtf", text: "{\\rtf1\\ansi hello}"))
        // A PEM key named .key is text: it stays code. So does a .rtf that is not RTF.
        #expect(!QuickLookFileType.matches(name: "server.key", text: "-----BEGIN PRIVATE KEY-----\n"))
        #expect(!QuickLookFileType.matches(name: "notes.rtf", text: "plain words"))
        #expect(!QuickLookFileType.matches(name: "data.bin", text: nil))
        #expect(!QuickLookFileType.matches(name: "report.pdf", text: nil))
    }

    @Test func viewerPicksQuickLookAndDownloadsTheFile() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/file", json: """
            {"path":"/work/docs/报告.docx","name":"报告.docx","size":5,"mime":"","data":"UEsDBA==","truncated":false,"is_dir":false}
            """)
        hub.on("GET", "/api/v1/machines/box/file/download") { request in
            #expect(request.query["path"] == "/work/docs/报告.docx")
            #expect(request.query["cwd"] == "/work")
            return Data("PK\u{3}\u{4}!".utf8)
        }
        let model = FileViewerModel(ref: FileRef(machineID: "box", path: "docs/报告.docx", cwd: "/work"), api: hub.client)
        await model.load()
        #expect(model.kind == .quickLook)
        let preview = try #require(model.preview)
        #expect(model.video == nil)
        await preview.load()
        let local = try #require(preview.localURL)
        #expect(local.lastPathComponent == "报告.docx") // QuickLook picks the format by the name
        #expect(model.localFile == local)
        #expect(try Data(contentsOf: local) == Data("PK\u{3}\u{4}!".utf8))
        preview.stop()
        #expect(preview.localURL == nil)
        #expect(!FileManager.default.fileExists(atPath: local.deletingLastPathComponent().path))
    }

    @Test func rtfSentAsTextStillPreviews() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/file", json: """
            {"path":"/work/a.rtf","name":"a.rtf","size":20,"mime":"text/rtf","text":"{\\\\rtf1\\\\ansi hi}","truncated":false,"is_dir":false}
            """)
        let model = FileViewerModel(ref: FileRef(machineID: "box", path: "a.rtf", cwd: "/work"), api: hub.client)
        await model.load()
        #expect(model.kind == .quickLook)
    }

    @Test func downloadFailureIsShown() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/file/download") { _ in throw APIError(status: 409, message: "需要重新部署") }
        let file = FileContent(path: "/work/a.pptx", name: "a.pptx", size: 9, truncated: false, isDir: false)
        let model = QuickLookFileModel(ref: FileRef(machineID: "box", path: "a.pptx", cwd: "/work"), file: file, api: hub.client)
        await model.load()
        #expect(model.error?.contains("需要重新部署") == true)
        #expect(model.localURL == nil)
    }

    @Test func thumbnailsOnlyForWhatCanBeDrawn() {
        #expect(FileThumbnails.wanted(name: "a.docx", size: 1200))
        #expect(FileThumbnails.wanted(name: "icon.PNG", size: 900))
        #expect(FileThumbnails.wanted(name: "guide.pdf", size: 80 << 20))
        #expect(FileThumbnails.wanted(name: "clip.mov", size: 100))
        #expect(!FileThumbnails.wanted(name: "empty.docx", size: 0))
        #expect(!FileThumbnails.wanted(name: "main.swift", size: 100))
        #expect(FileThumbnails.pixels(side: 34, scale: 3) == 102)
        #expect(FileThumbnails.pixels(side: 4, scale: 1) == 32)
        #expect(FileThumbnails.pixels(side: 900, scale: 3) == 1024)
    }

    @Test func thumbnailComesFromTheMachine() async throws {
        let hub = FakeHub()
        let png = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGP4DwQACfsD/fteaysAAAAASUVORK5CYII=")!
        var asked = 0
        hub.on("GET", "/api/v1/machines/box/file/thumb") { request in
            asked += 1
            #expect(request.query["path"] == "/w/\(asked == 1 ? "a" : "b").docx")
            #expect(request.query["cwd"] == "/w")
            #expect(request.query["size"] == "102")
            if asked == 2 { throw APIError(status: 404, message: "no_thumbnail") }
            return png
        }
        let at = Date(timeIntervalSinceReferenceDate: Double.random(in: 0..<1e9)) // a key no earlier run cached
        let a = FileRef(machineID: "box", path: "/w/a.docx", cwd: "/w")
        let image = await FileThumbnails.shared.thumbnail(a, size: 10, modified: at, side: 34, scale: 3, api: hub.client)
        #expect(image != nil)
        #expect(await FileThumbnails.shared.thumbnail(a, size: 10, modified: at, side: 34, scale: 3, api: hub.client) != nil)
        #expect(asked == 1) // kept
        let b = FileRef(machineID: "box", path: "/w/b.docx", cwd: "/w")
        #expect(await FileThumbnails.shared.thumbnail(b, size: 10, modified: at, side: 34, scale: 3, api: hub.client) == nil)
        #expect(await FileThumbnails.shared.thumbnail(b, size: 10, modified: at, side: 34, scale: 3, api: hub.client) == nil)
        #expect(asked == 2) // no_thumbnail is not asked again
    }

    @Test func pathOnlyThumbnailsAreCheckedOncePerLaunchByETag() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("thumbs-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        let png = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGP4DwQACfsD/fteaysAAAAASUVORK5CYII=")!
        let hub = FakeHub()
        var asked: [String] = []
        hub.on("GET", "/api/v1/machines/box/file/thumb") { request in
            let tag = request.query["If-None-Match"]
            asked.append(tag ?? "-")
            if tag == "\"t1\"" { throw APIError(status: 304, message: "") }
            return png
        }
        let ref = FileRef(machineID: "box", path: "/w/made.docx", cwd: "/w")
        #expect(FileThumbnails.wanted(name: ref.path, size: 0, modified: nil))
        let launch1 = FileThumbnails(directory: dir)
        #expect(await launch1.thumbnail(ref, size: 0, modified: nil, side: 56, scale: 2, api: hub.client) != nil)
        #expect(await launch1.thumbnail(ref, size: 0, modified: nil, side: 56, scale: 2, api: hub.client) != nil)
        #expect(asked == ["-"]) // once a launch
        // The node tagged it (the fake transport carries no headers): the next launch asks with the tag and keeps it on 304.
        let key = FileThumbnails.key(group: "", machine: "box", path: ref.path, size: 0, modified: nil)
        try "\"t1\"".write(to: dir.appending(path: key + ".etag"), atomically: true, encoding: .utf8)
        let launch2 = FileThumbnails(directory: dir)
        #expect(await launch2.thumbnail(ref, size: 0, modified: nil, side: 56, scale: 2, api: hub.client) != nil)
        #expect(asked == ["-", "\"t1\""])
        // A listed file is also checked once on the next launch: processing may change without a source change.
        let listed = FileRef(machineID: "box", path: "/w/listed.docx", cwd: "/w")
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(await launch2.thumbnail(listed, size: 9, modified: at, side: 56, scale: 2, api: hub.client) != nil)
        #expect(await FileThumbnails(directory: dir).thumbnail(listed, size: 9, modified: at, side: 56, scale: 2, api: hub.client) != nil)
        #expect(asked.count == 4)
    }

    @Test func listedThumbnailsRevalidatePreUpgradePixelsWithoutASourceChange() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("thumb-upgrade-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        func png(width: Int, height: Int) throws -> Data {
            let ctx = try #require(CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                                             space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
            ctx.setFillColor(red: 1, green: 0, blue: 0, alpha: 1)
            ctx.fill(CGRect(x: 0, y: 0, width: width, height: height))
            let out = NSMutableData()
            let dest = try #require(CGImageDestinationCreateWithData(out, "public.png" as CFString, 1, nil))
            CGImageDestinationAddImage(dest, try #require(ctx.makeImage()), nil)
            #expect(CGImageDestinationFinalize(dest))
            return out as Data
        }
        let sideways = try png(width: 60, height: 30), upright = try png(width: 30, height: 60)
        let ref = FileRef(machineID: "box", path: "/w/portrait.jpg", cwd: "/w")
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        // This is the unchanged pre-upgrade key and persisted bytes, including an opaque legacy validator.
        let key = FileThumbnails.key(group: "", machine: "box", path: ref.path, size: 1234, modified: at)
        let file = dir.appending(path: key), tagFile = dir.appending(path: key + ".etag")
        try sideways.write(to: file)
        try "\"legacy-opaque\"".write(to: tagFile, atomically: true, encoding: .utf8)
        #expect(FileThumbnails.decode(try Data(contentsOf: file), document: false)?.size.width == 60)
        let hub = FakeHub()
        var nodeUpgraded = false
        var asked: [String?] = []
        hub.on("GET", "/api/v1/machines/box/file/thumb") { request in
            let tag = request.query["If-None-Match"]
            asked.append(tag)
            if !nodeUpgraded || tag == "\"current-opaque\"" { throw APIError(status: 304, message: "") }
            #expect(tag == "\"legacy-opaque\"")
            return upright
        }
        // Updated app, old node: a legitimate 304 keeps the disk copy for this launch only.
        let before = FileThumbnails(directory: dir)
        #expect(await before.thumbnail(ref, size: 1234, modified: at, side: 56, scale: 2, api: hub.client)?.size.width == 60)
        #expect(await before.thumbnail(ref, size: 1234, modified: at, side: 56, scale: 2, api: hub.client)?.size.width == 60)
        #expect(asked == ["\"legacy-opaque\""])
        // Node upgrades later. The source identity is identical; the next launch must still ask.
        nodeUpgraded = true
        let after = FileThumbnails(directory: dir)
        let image = await after.thumbnail(ref, size: 1234, modified: at, side: 56, scale: 2, api: hub.client)
        #expect(image?.size.width == 30 && image?.size.height == 60)
        #expect(try Data(contentsOf: file) == upright)
        #expect(asked == ["\"legacy-opaque\"", "\"legacy-opaque\""])
        #expect(await after.thumbnail(ref, size: 1234, modified: at, side: 56, scale: 2, api: hub.client)?.size.width == 30)
        #expect(asked.count == 2)
        // The real transport stores this header. A current opaque validator keeps the corrected bytes on 304.
        try "\"current-opaque\"".write(to: tagFile, atomically: true, encoding: .utf8)
        #expect(await FileThumbnails(directory: dir).thumbnail(ref, size: 1234, modified: at, side: 56, scale: 2, api: hub.client)?.size.width == 30)
        #expect(asked.last! == "\"current-opaque\"")
    }

    @Test func streamedImagesAndPDFsAreDownloadedWhole() async throws {
        for (name, mime, kind) in [("big.pdf", "application/pdf", FileViewerModel.Kind.pdf), ("shot.png", "image/png", .image)] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/machines/box/file") { request in
                #expect(request.query["stream"] == "1")
                return Data("""
                    {"path":"/w/\(name)","name":"\(name)","size":3,"mime":"\(mime)","stream":true,"revision":"r1","truncated":false,"is_dir":false}
                    """.utf8)
            }
            hub.on("GET", "/api/v1/machines/box/file/download") { request in
                #expect(request.query["path"] == "/w/\(name)")
                return Data([1, 2, 3])
            }
            let model = FileViewerModel(ref: FileRef(machineID: "box", path: name, cwd: "/w"), api: hub.client, stream: true)
            await model.load()
            #expect(model.kind == kind)
            #expect(model.file?.data == Data([1, 2, 3]).base64EncodedString())
            #expect(model.file?.revision == "r1")
        }
    }

    @Test func oldHubsAreNotAskedToStream() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/file") { request in
            #expect(request.query["stream"] == nil)
            return Data(#"{"path":"/w/a.png","name":"a.png","size":3,"mime":"image/png","data":"AQID","truncated":false,"is_dir":false}"#.utf8)
        }
        let model = FileViewerModel(ref: FileRef(machineID: "box", path: "a.png", cwd: "/w"), api: hub.client)
        await model.load()
        #expect(model.kind == .image && model.file?.data == "AQID")
    }

    @Test func thumbnailKeyChangesWithTheFile() {
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let key = FileThumbnails.key(group: "", machine: "box", path: "/w/a.docx", size: 10, modified: at)
        #expect(key == FileThumbnails.key(group: "", machine: "box", path: "/w/a.docx", size: 10, modified: at))
        #expect(key != FileThumbnails.key(group: "", machine: "box", path: "/w/a.docx", size: 11, modified: at))
        #expect(key != FileThumbnails.key(group: "", machine: "box", path: "/w/a.docx", size: 10, modified: at.addingTimeInterval(1)))
        #expect(key != FileThumbnails.key(group: "", machine: "mini", path: "/w/a.docx", size: 10, modified: at))
        #expect(key != FileThumbnails.key(group: "work", machine: "box", path: "/w/a.docx", size: 10, modified: at))
    }

    @Test func aDarkDocumentPageIsNoThumbnail() throws {
        func png(gray: CGFloat) throws -> Data {
            let ctx = try #require(CGContext(data: nil, width: 16, height: 16, bitsPerComponent: 8, bytesPerRow: 0,
                                             space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
            ctx.setFillColor(red: gray, green: gray, blue: gray, alpha: 1)
            ctx.fill(CGRect(x: 0, y: 0, width: 16, height: 16))
            let out = NSMutableData()
            let dest = try #require(CGImageDestinationCreateWithData(out, "public.png" as CFString, 1, nil))
            CGImageDestinationAddImage(dest, try #require(ctx.makeImage()), nil)
            CGImageDestinationFinalize(dest)
            return out as Data
        }
        let dark = try png(gray: 0.12), light = try png(gray: 0.97)
        #expect(FileThumbnails.decode(dark, document: true) == nil) // Quick Look's RTF on a Mac in dark mode
        #expect(FileThumbnails.decode(light, document: true) != nil)
        #expect(FileThumbnails.decode(dark, document: false) != nil) // a dark photo is a photo
        #expect(FileThumbnails.isDocument("/w/a.RTF") && FileThumbnails.isDocument("/w/b.pdf") && !FileThumbnails.isDocument("/w/c.png"))
    }

    @Test func documentSymbols() {
        #expect(FileViewer.symbol(for: "a.docx") == Theme.Icon.document)
        #expect(FileViewer.symbol(for: "a.numbers") == Theme.Icon.sheet)
        #expect(FileViewer.symbol(for: "a.pptx") == Theme.Icon.slides)
    }
}
