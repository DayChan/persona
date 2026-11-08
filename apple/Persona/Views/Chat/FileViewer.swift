import MarkdownUI
import PDFKit
import PersonaKit
import SwiftUI

nonisolated struct FileContent: Decodable, Sendable {
    var path: String
    var name: String
    var size: Int64
    var mime: String?
    var text: String?
    var data: String?
    var truncated: Bool
    var isDir: Bool
    var entries: [String]?
    /// Where this page starts, and where the next one does (0 / nil: the end). Older nodes send neither.
    var offset: Int64?
    var next: Int64?
    /// Asked with `stream=1` (hub feature `file_stream`): a binary file comes described, its bytes from `/file/download`.
    var stream: Bool?
    var revision: String?

    enum CodingKeys: String, CodingKey {
        case path, name, size, mime, text, data, truncated, entries, offset, next, stream, revision
        case isDir = "is_dir"
    }
}

extension APIClient {
    /// A file or a directory on a machine; a text file bigger than a page from `offset` (the page before's `next`).
    /// `stream`: a binary file without its bytes (hub feature `file_stream`, docs/protocol/protocol.md「文件预览」).
    func file(machine: String, path: String, cwd: String?, offset: Int64 = 0, stream: Bool = false) async throws -> FileContent {
        try await get("/api/v1/machines/\(machine)/file",
                      query: ["path": path, "cwd": cwd ?? "", "offset": offset > 0 ? String(offset) : "", "stream": stream ? "1" : ""])
    }

    /// A whole file's bytes (`/file/download`), for what is drawn from memory (images, PDFs).
    func fileBytes(machine: String, path: String, cwd: String?) async throws -> Data {
        try await request("GET", "/api/v1/machines/\(machine)/file/download", query: ["path": path, "cwd": cwd ?? ""], timeout: 120)
    }
}

/// A file mentioned in a conversation, opened on the machine the session runs on.
struct FileRef: Identifiable, Hashable {
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    var machineID: String
    var path: String
    var cwd: String?
    /// The line to show (1-based), from a message's `path:12`: the viewer scrolls to it and marks it.
    var line: Int? = nil
    var id: String { MachineRef(group: group, id: machineID).path + ":" + path }
    var machineRef: MachineRef { MachineRef(group: group, id: machineID) }
}

nonisolated enum FilePaths {
    private static let memo = TextMemo<[String]>()

    /// File-looking paths in text (`PathRefs`: without their lines), in order, without duplicates or URL fragments
    /// (memoized: rows ask on every redraw). The ones not there on the machine are left out where they are shown.
    static func find(in text: String, limit: Int = 12) -> [String] {
        limit == 12 ? memo(text) { scan($0, limit: 12) } : scan(text, limit: limit)
    }

    /// A path in backticks is whole here: the paths inside a span that is one are not chips of their own (its folder,
    /// the words before a space), they would only repeat it.
    private static func scan(_ text: String, limit: Int) -> [String] {
        let refs = PathRefs.find(in: text, limit: 80)
        let spans = refs.filter(\.whole).map(\.range)
        let kept = refs.filter { r in r.whole || !spans.contains { NSIntersectionRange($0, r.range).length > 0 } }
        return PathRefs.unique(kept, limit: limit)
    }

    /// A picture, a video or a sound: keeps its chip under the message even when its words link it (#1706).
    static func isMedia(_ path: String) -> Bool {
        let name = (path as NSString).lastPathComponent
        let ext = (name as NSString).pathExtension.lowercased()
        return ["png", "jpg", "jpeg", "gif", "webp", "heic"].contains(ext)
            || VideoFileType.matches(name: name, mime: nil) || AudioFileType.matches(name: name, mime: nil)
    }
}

/// Tappable chips for the paths a message mentions that are there on the conversation's machine (`FilePresence`;
/// the row asks with `lookUpFiles`). The others stay plain text in the message.
struct FileChips: View {
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
        if !shown.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: Theme.Space.sm) {
                    ForEach(shown, id: \.self) { p in
                        Button { open(p) } label: {
                            LinkBadge(symbol: FileViewer.symbol(for: p),
                                      text: (p as NSString).lastPathComponent.isEmpty ? p : (p as NSString).lastPathComponent)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("file-chip-" + p)
                        .contextMenu { Button { UIPasteboard.general.string = p } label: { Label("Copy Path", systemImage: Theme.Icon.copy) } }
                    }
                }
            }
            .scrollClipDisabled()
        }
    }
}

/// Asks whether a row's paths are there (`FilePresence`) as it appears, so `FileChips` can link them: on the row
/// itself, which always appears (an empty chip row would not).
struct LookUpFiles: ViewModifier {
    @Environment(AppModel.self) private var model
    @Environment(\.fileScope) private var scope
    var paths: [String]

    func body(content: Content) -> some View {
        content.task(id: paths) {
            guard !paths.isEmpty, let scope else { return }
            model.filePresence.ask(paths, in: scope, api: model.api(scope.machine.group))
        }
    }
}

extension View {
    func lookUpFiles(_ paths: [String]) -> some View { modifier(LookUpFiles(paths: paths)) }
}

/// Reads a file (or lists a directory) on a machine (docs/apple/ios.md「文件查看器」): code colored by its language with
/// line numbers, JSON as a foldable outline, Markdown rendered (or its source), images and PDFs inline, Office and
/// iWork documents in QuickLook (`QuickLookFileView`). Text shows
/// at once and is colored in the background; a file bigger than a page loads the next one as the end comes near.
struct FileViewer: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State var ref: FileRef
    var key: SessionKey? = nil
    @State private var page: FileViewerModel?
    @State private var history: [FileRef] = []
    @State private var link: LinkTarget?

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(page?.file?.name ?? (ref.path as NSString).lastPathComponent)
                .navigationSubtitle(model.machine(ref.machineRef)?.name ?? ref.machineID)
                .navigationBarTitleDisplayMode(.inline)
                .pageToolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        if let back = history.last {
                            Button { history.removeLast(); ref = back } label: { Label("Back [Task Sheets 572cf4]", systemImage: Theme.Icon.back) }
                        } else {
                            Button("Done [Extension System Presentation c0b3fb]") { dismiss() }
                                .lineLimit(1)
                                .accessibilityIdentifier("file-close")
                        }
                    }
                    ToolbarItem(placement: .primaryAction) { menu }
                }
                .task(id: ref) { await open() }
                .onAppear { FrameMeter.shared.start((ref.path as NSString).lastPathComponent) }
                .onDisappear { FrameMeter.shared.stop() }
        }
        .environment(\.openLink, openLink)
        .environment(\.openURL, OpenURLAction { url in
            guard ["http", "https"].contains(url.scheme?.lowercased() ?? "") else {
                #if os(macOS)
                return LinkPolicy.system(url)
                #else
                return .systemAction
                #endif
            }
            openLink(url)
            return .handled
        })
        #if os(macOS)
        .macLinkPanel($link, key: key)
        // Outside the panel: its GeometryReader takes any size, so a minimum inside it never reached the sheet and the
        // sheet opened at its toolbar's 470 × 80 (#1502). The stack's bars are the sheet window's, still on top of this.
        .macLargeSheet(bars: true)
        #else
        .sheet(item: $link) { target in LinkSheet(target: target, key: key) }
        #endif
    }

    private func openLink(_ url: URL) {
        let machine = ref.machineRef
        if let port = PortLinks.link(url, hostnames: model.hostnames(of: machine)) {
            Task {
                do {
                    let forwarded = try await model.forwardedURL(port, machine: machine)
                    let name = model.machine(machine)?.title ?? machine.id
                    link = LinkTarget(url: forwarded, forwarded: "\(name) :\(port.port)")
                } catch {
                    model.toast = Toast(error.localizedDescription, style: .error)
                }
            }
        } else {
            link = LinkTarget(url: url)
        }
    }

    private func open() async {
        page?.video?.stop()
        page?.preview?.stop()
        page?.audio?.stop()
        let p = FileViewerModel(ref: ref, api: model.api(ref.group), stream: model.features(ref.group).contains("file_stream"))
        page = p
        await p.load()
    }

    @ViewBuilder private var content: some View {
        if let page, let file = page.file {
            switch page.kind {
            case .directory:
                List(file.entries ?? [], id: \.self) { e in
                    Button {
                        history.append(ref)
                        ref = FileRef(group: ref.group, machineID: ref.machineID, path: (file.path as NSString).appendingPathComponent(e), cwd: ref.cwd)
                    } label: {
                        Label(e, systemImage: e.hasSuffix("/") ? Theme.Icon.folder : FileViewer.symbol(for: e))
                            .foregroundStyle(Color.primary)
                    }
                }
                .themedPage()
            case .html where !page.showSource:
                HTMLFileView(html: page.text ?? "", dir: (file.path as NSString).deletingLastPathComponent, fetch: page.fetch,
                             scripts: page.htmlScripts, network: page.htmlNetwork, onLink: openLink)
            case .notebook where !page.showSource && page.notebook != nil:
                if let notebook = page.notebook { NotebookFileView(notebook: notebook) }
            case .table where !page.showSource:
                if let table = page.table { TableFileView(model: table) }
            case .table where page.preview != nil:
                if let preview = page.preview { QuickLookFileView(model: preview) }
            case .markdown where !page.showSource:
                MarkdownFile(page: page, home: model.machine(ref.machineRef)?.home, loadImage: loadImageFile)
            case .markdown, .json, .code, .table, .notebook, .html:
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    meta(page).padding(.horizontal, Theme.Space.l)
                    if let source = page.shownSource {
                        CodeLines(source: source, version: page.version, onNearEnd: { Task { await page.loadMore() } },
                                  onFirstLines: page.shown, focus: page.focusLine)
                            .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                            .padding(.horizontal, Theme.Space.m)
                            .accessibilityIdentifier("file-code")
                    } else {
                        ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                }
                .padding(.top, Theme.Space.s)
            case .image:
                ScrollView {
                    meta(page).padding(.horizontal, Theme.Space.l)
                    Base64Image(payload: ImagePayload(mime: file.mime ?? "image/png", data: file.data ?? ""), title: file.name).padding(Theme.Space.l)
                }
            case .pdf:
                if let data = file.data, let bytes = Data(base64Encoded: data), let doc = PDFDocument(data: bytes) {
                    PDFKitView(document: doc)
                }
            case .video:
                if let video = page.video { VideoFileView(model: video) }
            case .quickLook:
                if let preview = page.preview { QuickLookFileView(model: preview) }
            case .audio:
                if let audio = page.audio { AudioFileView(model: audio) }
            case .binary:
                EmptyStateView(symbol: Theme.Icon.missingFile, title: String(localized: "Can't Preview"),
                               message: "\(file.mime ?? String(localized: "Binary file")) · \(file.size.storageText)")
            }
        } else if let error = page?.error {
            EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Can't Open This File"), message: error)
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private var menu: some View {
        Menu {
            if let page {
                switch page.kind {
                case .markdown:
                    Button { page.showSource.toggle() } label: {
                        Label(page.showSource ? String(localized: "Render Markdown") : String(localized: "View Source [File Viewer 4731a3]"), systemImage: page.showSource ? "doc.richtext" : Theme.Icon.code)
                    }
                case .html:
                    Button { page.showSource.toggle() } label: {
                        Label(page.showSource ? String(localized: "Preview as Web Page") : String(localized: "View Source [File Viewer 4731a3]"), systemImage: page.showSource ? Theme.Icon.document : Theme.Icon.code)
                    }
                    if !page.showSource {
                        Toggle(isOn: Binding(get: { page.htmlScripts }, set: { page.htmlScripts = $0 })) {
                            Label("Allow Scripts", systemImage: Theme.Icon.code)
                        }
                        Toggle(isOn: Binding(get: { page.htmlNetwork }, set: { page.htmlNetwork = $0 })) {
                            Label("Allow Network Access", systemImage: Theme.Icon.openExternal)
                        }
                    }
                case .notebook where page.notebook != nil:
                    Button { page.showSource.toggle() } label: {
                        Label(page.showSource ? String(localized: "View as Notebook") : String(localized: "View Source [File Viewer 4731a3]"), systemImage: page.showSource ? Theme.Icon.document : Theme.Icon.code)
                    }
                case .table:
                    Button { page.showSource.toggle() } label: {
                        Label(page.showSource ? String(localized: "View as Table") : page.preview != nil ? String(localized: "View Source [File Viewer 1d8406]") : String(localized: "View Source [File Viewer 4731a3]"),
                              systemImage: page.showSource ? Theme.Icon.sheet : page.preview != nil ? Theme.Icon.document : Theme.Icon.code)
                    }
                case .json where page.outline != nil:
                    Button { page.showSource.toggle() } label: {
                        Label(page.showSource ? String(localized: "View as Tree") : String(localized: "View Source [File Viewer 4731a3]"), systemImage: page.showSource ? "list.triangle" : Theme.Icon.code)
                    }
                    if !page.showSource, let outline = page.outline {
                        Button { outline.setAll(open: false) } label: { Label("Collapse All", systemImage: Theme.Icon.shrink) }
                        if outline.canOpenAll {
                            Button { outline.setAll(open: true) } label: { Label("Expand All", systemImage: Theme.Icon.enlarge) }
                        }
                    }
                default:
                    EmptyView()
                }
            }
            Button { UIPasteboard.general.string = ref.path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
            if let text = page?.text {
                Button { UIPasteboard.general.string = text } label: { Label("Copy Contents", systemImage: Theme.Icon.copy) }
                ShareLink(item: text, subject: Text(page?.file?.name ?? "")) { Label("Share", systemImage: Theme.Icon.share) }
            } else if let local = page?.localFile {
                ShareLink(item: local) { Label("Share", systemImage: Theme.Icon.share) }
            }
        } label: { ThemedIcon(Theme.Icon.moreMenu) }
        .arrowlessMenu()
        .accessibilityLabel("More")
    }

    /// The path, the size, and what is not shown yet: pages still on the machine, lines past the coloring caps, why
    /// a .json shows as text.
    private func meta(_ page: FileViewerModel) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.sm) {
                Text(Fmt.path(page.file?.path ?? ref.path, home: model.machine(ref.machineRef)?.home)).lineLimit(1).truncationMode(.head)
                Text("·")
                Text((page.file?.size ?? 0).storageText)
                if let lang = page.language { Text("· " + lang) }
            }
            .foregroundStyle(.secondary)
            if let note = page.shownNote { Text(note).foregroundStyle(Theme.cautionText).lineLimit(2) }
        }
        .font(Theme.Font.code)
    }

    /// An image the markdown file refers to, read from the same machine.
    private func loadImageFile(_ path: String) async -> FileContent? {
        try? await model.api(ref.group)?.file(machine: ref.machineID, path: path, cwd: ref.cwd)
    }

    static func symbol(for path: String) -> String {
        switch (path as NSString).pathExtension.lowercased() {
        case "md", "markdown", "txt": Theme.Icon.document
        case "png", "jpg", "jpeg", "gif", "webp", "heic": "photo"
        case "mp4", "mov", "m4v", "mpeg", "mpg", "webm", "mkv", "avi": Theme.Icon.video
        case "mp3", "m4a", "aac", "wav", "flac", "aif", "aiff", "caf", "m4b", "ogg", "opus": Theme.Icon.voiceMessage
        case "pdf": "doc.richtext"
        case "doc", "docx", "pages", "rtf": Theme.Icon.document
        case "xls", "xlsx", "numbers", "csv", "tsv": Theme.Icon.sheet
        case "ppt", "pptx": Theme.Icon.slides
        case "json", "yml", "yaml", "toml", "plist": "curlybraces"
        case "": path.hasSuffix("/") ? Theme.Icon.folder : Theme.Icon.file
        default: Theme.Icon.code
        }
    }
}

/// A Markdown file rendered, a piece at a time in a lazy stack (`MarkdownChunks`), with the chat's look
/// (`.persona`: fitted tables, links that end where `Links` says) and colored code fences. No text selection: it made
/// opening 40 % slower and scrolling hitch more (docs/apple/ios.md「文件查看器」); the source view copies lines.
private struct MarkdownFile: View {
    var page: FileViewerModel
    var home: String?
    var loadImage: (String) async -> FileContent?
    @State private var position = ScrollPosition(edge: .top)
    @State private var offset: CGFloat = 0
    @State private var scroller: AutoScroller?

    var body: some View {
        if let chunks = page.chunks {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    Text(Fmt.path(page.file?.path ?? "", home: home))
                        .font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                        .padding(.bottom, Theme.Space.m)
                    ForEach(chunks) { c in
                        Markdown(c.content, imageBaseURL: page.baseURL)
                            .markdownTheme(.persona)
                            .markdownCodeSyntaxHighlighter(PersonaCodeHighlighter())
                            .markdownImageProvider(MachineImageProvider(load: loadImage))
                            .onAppear { if c.id == 0 { page.shown() } }
                    }
                    if page.next > 0 {
                        Button { Task { await page.loadMore() } } label: {
                            Label(page.loadingMore ? String(localized: "Loading… [File Viewer 86b6d0]") : String(localized: "Read More"), systemImage: "arrow.down.doc").lineLimit(1)
                        }
                        .themedButton()
                        .disabled(page.loadingMore)
                        .padding(.top, Theme.Space.l)
                    }
                }
                .padding(Theme.Space.l)
            }
            .scrollPosition($position)
            .onScrollGeometryChange(for: CGFloat.self, of: { $0.contentOffset.y }) { _, y in
                offset = y
                FrameMeter.shared.noteScroll()
            }
            .onAppear(perform: autoScroll)
            .accessibilityIdentifier("file-markdown")
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    /// The measurements' steady scroll (`AutoScroller`, DEBUG flags only).
    private func autoScroll() {
        guard AutoScroller.on, scroller == nil else { return }
        final class Target { var y: CGFloat = 0 }
        let target = Target()
        let s = AutoScroller { dy in
            target.y = max(0, target.y + dy)
            position.scrollTo(y: target.y)
        }
        scroller = s
        s.start()
    }
}

/// Loading and preparing one file for `FileViewer` (MVVM: the page only shows this): picks how to show it, makes
/// the code document, the JSON outline or the Markdown pieces off the main thread, and fetches further pages.
@MainActor @Observable
final class FileViewerModel {
    enum Kind { case directory, markdown, json, code, image, pdf, video, audio, quickLook, table, notebook, html, binary }

    let ref: FileRef
    @ObservationIgnored private let api: APIClient?
    /// Ask for binary files without their bytes and download them (the hub has `file_stream`).
    @ObservationIgnored private let stream: Bool
    private(set) var file: FileContent?
    private(set) var error: String?
    private(set) var kind: Kind = .code
    private(set) var video: VideoFileModel?
    private(set) var preview: QuickLookFileModel?
    private(set) var audio: AudioFileModel?
    /// CSV / TSV / xlsx as a table (`TableFileView`); an xlsx also has `preview`, the menu's QuickLook.
    private(set) var table: TableFileModel?
    /// An .ipynb read and parsed (`NotebookFileView`); nil while it loads or when it cannot be read (then the text).
    private(set) var notebook: Notebook?
    /// An .html preview's switches (the ⋯ menu): both off by default.
    var htmlScripts = false
    var htmlNetwork = false
    /// The source as text (code, Markdown's source, JSON before its outline or when it is not JSON).
    private(set) var code: CodeFileSource?
    private(set) var outline: JSONOutlineSource?
    private(set) var chunks: [MarkdownChunk]?
    var showSource = false
    /// The line asked for (0-based), scrolled to and marked in the source.
    private(set) var focusLine: Int?
    /// Where the next page starts; 0: none.
    private(set) var next: Int64 = 0
    private(set) var loadingMore = false
    private(set) var language: String?
    /// What is not shown yet (pages on the machine, why a .json shows as text).
    private(set) var note: String?
    /// The "read so far" note last shown, to clear it once reading is done.
    @ObservationIgnored private var progressNote: String?
    /// Lines past the coloring caps: only said where the text itself shows (not over a JSON outline).
    private(set) var colorNote: String?

    var shownNote: String? {
        let parts = [note, shownSource === code ? colorNote : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: String(localized: "; ", comment: "Separator between two notes about a file"))
    }
    /// Bumped when the shown source is a new one.
    private(set) var version = 0
    @ObservationIgnored private var text_: [String] = []
    @ObservationIgnored private let perf: FilePerf

    /// JSON is read whole (up to this) before it is parsed into an outline.
    static let maxJSONBytes: Int64 = 24 << 20

    init(ref: FileRef, api: APIClient?, stream: Bool = false) {
        self.ref = ref
        self.api = api
        self.stream = stream
        perf = FilePerf(ref.path)
    }

    var text: String? { text_.isEmpty ? nil : text_.joined() }
    /// A file on the same machine by its absolute path (an HTML page's pictures and style sheets, `HTMLResources`).
    var fetch: HTMLResources.Fetch {
        let api = api, machine = ref.machineID, cwd = ref.cwd
        return { path in try? await api?.fileBytes(machine: machine, path: path, cwd: cwd) }
    }

    /// The file as downloaded for its player or QuickLook, to share.
    var localFile: URL? { preview?.localURL ?? video?.localURL ?? audio?.localURL }

    var shownSource: CodeLineSource? {
        if kind == .json, !showSource, let outline { return outline }
        return code
    }

    var baseURL: URL { URL(fileURLWithPath: ((file?.path ?? ref.path) as NSString).deletingLastPathComponent, isDirectory: true) }

    func load() async {
        guard let api else { return }
        let f: FileContent
        do {
            f = try await api.file(machine: ref.machineID, path: ref.path, cwd: ref.cwd, stream: stream)
        } catch {
            self.error = error.shown
            return
        }
        perf.mark("fetched", bytes: f.text?.utf8.count ?? 0)
        file = f
        next = f.next ?? 0
        guard !f.isDir else {
            kind = .directory
            return
        }
        if f.text == nil, TableFileType.isWorkbook(f.name) {
            kind = .table
            table = TableFileModel(ref: ref, file: f, api: api)
            preview = QuickLookFileModel(ref: ref, file: f, api: api)
            return
        }
        if QuickLookFileType.matches(name: f.name, text: f.text) {
            kind = .quickLook
            preview = QuickLookFileModel(ref: ref, file: f, api: api)
            return
        }
        if f.text == nil, AudioFileType.matches(name: f.name, mime: f.mime) {
            kind = .audio
            audio = AudioFileModel(ref: ref, file: f, api: api)
            return
        }
        if f.text == nil, VideoFileType.matches(name: f.name, mime: f.mime) {
            kind = .video
            video = VideoFileModel(ref: ref, file: f, api: api)
            return
        }
        guard let text = f.text else {
            let mime = f.mime ?? ""
            kind = mime.hasPrefix("image/") ? .image : mime == "application/pdf" ? .pdf : .binary
            if kind != .binary, f.stream == true {
                file = nil // the spinner until the bytes are here
                await fetchBytes(f)
            }
            return
        }
        text_ = [text]
        let firstLine = text.prefix(200).split(separator: "\n", maxSplits: 1).first.map(String.init)
        let ext = (f.name as NSString).pathExtension.lowercased()
        let lang = SyntaxLanguage.detect(name: f.name, firstLine: firstLine)
            ?? ((f.mime ?? "").contains("json") ? .json : nil)
        language = lang?.id
        kind = lang?.id == "markdown" && ext != "" ? .markdown : lang?.id == "json" && !["jsonl", "ndjson"].contains(ext) ? .json : .code
        if TableFileType.isDelimited(f.name) {
            kind = .table
            table = TableFileModel(ref: ref, file: f, api: api)
        }
        if ext == "ipynb" { kind = .notebook }
        if ext == "html" || ext == "htm" { kind = .html }
        let doc = await Task.detached(priority: .userInitiated) { CodeDocument(text: text, language: lang) }.value
        setCode(CodeFileSource(doc))
        if let line = ref.line, line > 0 { await goTo(line) }
        switch kind {
        case .markdown: await renderMarkdown()
        case .json: await buildOutline()
        case .notebook: await buildNotebook()
        default: break
        }
        if f.truncated, f.next == nil { note = String(localized: "File too large; showing the first \(Int64(text.utf8.count).storageText)") }
    }

    /// Opened at a line (a message's `path:12`): the source, read up to that line, with the line marked. Rendered
    /// Markdown, a JSON outline, a table have no lines to go to.
    private func goTo(_ line: Int) async {
        showSource = true
        var pages = 0
        while next > 0, (code?.lineCount ?? 0) < line, pages < 8 {
            let before = next
            await loadMore()
            pages += 1
            if next == before { break }
        }
        if let n = code?.lineCount, n > 0 { focusLine = min(line, n) - 1 }
    }

    /// A streamed image or PDF: its bytes from `/file/download`, whole (the old answer stopped at 2 MB).
    private func fetchBytes(_ f: FileContent) async {
        guard let api else { return }
        do {
            let bytes = try await api.fileBytes(machine: ref.machineID, path: f.path, cwd: ref.cwd)
            var whole = f
            whole.data = await Task.detached(priority: .userInitiated) { bytes.base64EncodedString() }.value
            whole.truncated = false
            file = whole
        } catch {
            self.error = error.shown
        }
    }

    private func setCode(_ source: CodeFileSource) {
        code = source
        source.onColored = { [weak self, weak source] in
            guard let self, let source else { return }
            self.perf.mark("colored", bytes: 0)
            self.colorNote = source.doc.capped ? String(localized: "Only the first \(source.doc.highlightedLines) lines are highlighted; the rest is plain text") : nil
        }
        version += 1
        updateNote()
    }

    private func updateNote() {
        guard let f = file else { return }
        if next > 0 {
            let read = next.storageText
            let all = f.size.storageText
            note = kind == .json ? String(localized: "Read \(read) of \(all), reading the whole file…") : String(localized: "Read \(read) of \(all); scroll down to read more")
            progressNote = note
        } else if note != nil, note == progressNote {
            note = nil
        }
    }

    /// The first content is on screen (the code view's first lines, or the first Markdown piece).
    func shown() { perf.mark("shown", bytes: 0) }

    /// The next page, appended (code), re-cut (Markdown).
    func loadMore() async {
        guard next > 0, !loadingMore, let api else { return }
        loadingMore = true
        defer { loadingMore = false }
        guard let f = try? await api.file(machine: ref.machineID, path: ref.path, cwd: ref.cwd, offset: next), let t = f.text else { return }
        text_.append(t)
        next = f.next ?? 0
        code?.append(t)
        if kind == .markdown { await renderMarkdown() }
        updateNote()
    }

    private func renderMarkdown() async {
        let text = text_.joined()
        chunks = await Task.detached(priority: .userInitiated) {
            MarkdownChunks.split(Links.markdown(MathMarkdown.rewrite(MarkdownHTML.simplify(text)))).enumerated().map {
                MarkdownChunk(id: $0.offset, content: MarkdownContent($0.element))
            }
        }.value
    }

    /// The rest of the file, up to maxJSONBytes (JSON and notebooks are parsed whole).
    private func readAll() async {
        while next > 0, next < Self.maxJSONBytes {
            let before = next
            await loadMore()
            if next == before { break }
        }
    }

    /// A notebook is read to the end and parsed; until then (and when it cannot be read) the text.
    private func buildNotebook() async {
        await readAll()
        let data = Data(text_.joined().utf8)
        let parsed = await Task.detached(priority: .userInitiated) { Result { try Notebook(data: data) } }.value
        switch parsed {
        case .success(let n): notebook = n
        case .failure(let e): note = next > 0 ? String(localized: "File too large; showing as text") : e.localizedDescription
        }
    }

    /// JSON is read to the end (up to maxJSONBytes) and parsed; until then (and when it is not valid JSON) the text.
    private func buildOutline() async {
        await readAll()
        let text = text_.joined()
        let parsed = await Task.detached(priority: .userInitiated) { () -> Result<JSONOutline, Error> in
            Result { try JSONOutline(text: text) }
        }.value
        switch parsed {
        case .success(let o):
            outline = JSONOutlineSource(o)
            if AutoScroller.on { outline?.openBiggest() }
            version += 1
            perf.mark("outline", bytes: o.bytes.count)
        case .failure(let e):
            let why = (e as? JSONOutline.ParseError).map { String(localized: "\($0.message) (byte \($0.offset))") } ?? e.localizedDescription
            note = next > 0 ? String(localized: "File too large; showing as text") : String(localized: "Not valid JSON, showing as text: \(why)")
        }
    }
}

#if os(iOS) || os(visionOS)
struct PDFKitView: UIViewRepresentable {
    var document: PDFDocument
    func makeUIView(context: Context) -> PDFView {
        let v = PDFView()
        v.autoScales = true
        v.document = document
        return v
    }
    func updateUIView(_ v: PDFView, context: Context) {}
}
#else
struct PDFKitView: NSViewRepresentable {
    var document: PDFDocument
    func makeNSView(context: Context) -> PDFView {
        let v = PDFView()
        v.autoScales = true
        v.document = document
        return v
    }
    func updateNSView(_ v: PDFView, context: Context) {}
}
#endif

/// Images in a markdown file on a computer: relative paths load from that machine, web URLs as usual.
struct MachineImageProvider: ImageProvider {
    var load: (String) async -> FileContent?

    func makeImage(url: URL?) -> some View {
        if let url, url.isFileURL {
            MachineMarkdownImage(path: url.path, load: load)
        } else {
            DefaultImageProvider.default.makeImage(url: url)
        }
    }
}

struct MachineMarkdownImage: View {
    var path: String
    var load: (String) async -> FileContent?
    @State private var payload: ImagePayload?
    @State private var failed = false

    var body: some View {
        Group {
            if let payload {
                Base64Image(payload: payload)
            } else if failed {
                // SVG and anything else that is not a bitmap: say what is there instead of a blank.
                Label((path as NSString).lastPathComponent, systemImage: Theme.Icon.photo)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.sm)
                    .background(Color.primary.opacity(0.05), in: .capsule)
            } else {
                RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).fill(Color.primary.opacity(0.05))
                    .scaledFrame(height: Theme.Size.box120)
                    .overlay { ProgressView() }
            }
        }
        .task(id: path) {
            guard payload == nil else { return }
            if let f = await load(path), let data = f.data, let mime = f.mime, mime.hasPrefix("image/"), mime != "image/svg+xml" {
                payload = ImagePayload(mime: mime, data: data)
            } else {
                failed = true
            }
        }
    }
}
