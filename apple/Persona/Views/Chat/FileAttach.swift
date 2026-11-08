import SwiftUI
import UniformTypeIdentifiers

// Sending files to an agent (docs/protocol/protocol.md「发送文件」): picked files are copied out of the picker's sandbox,
// uploaded one after another to the temp directory of the machine the conversation runs on, and each path lands in
// the input box at the cursor. Nothing is sent: the user goes on typing. Pictures keep using image attachments.

/// Where paths go in the input box. Offsets are UTF-16 (what TextSelection converts to and from cleanly).
nonisolated enum PathInsert {
    /// `paths` replace `range` (the end when nil), space-separated and set off from the text around them; returns the
    /// new text and the cursor after the inserted paths.
    static func insert(_ paths: [String], into text: String, at range: Range<Int>?) -> (text: String, cursor: Int) {
        let units = Array(text.utf16)
        let r = range.map { max(0, min($0.lowerBound, units.count))..<max(0, min($0.upperBound, units.count)) } ?? units.count..<units.count
        let before = String(decoding: units[..<r.lowerBound], as: UTF16.self)
        let after = String(decoding: units[r.upperBound...], as: UTF16.self)
        var insert = paths.map(quoted).joined(separator: " ")
        if let c = before.last, !c.isWhitespace { insert = " " + insert }
        if after.first.map({ !$0.isWhitespace }) ?? true { insert += " " } // at the end too: ready for the next word
        return (before + insert + after, before.utf16.count + insert.utf16.count)
    }

    /// A path with spaces or quotes is single-quoted, so the agent reads it as one path.
    static func quoted(_ path: String) -> String {
        guard path.contains(where: { $0.isWhitespace || $0 == "'" || $0 == "\"" }) else { return path }
        return "'" + path.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    /// A composer's text with path inserted at its cursor, the cursor moved after it and the field focused.
    /// The cursor counts only while the field is being edited: an unfocused TextField still reports a selection, and
    /// after a relaunch with a restored draft it pointed into the middle of it.
    @MainActor static func edit(_ path: String, text: String, selection: Binding<TextSelection?>,
                                focused: FocusState<Bool>.Binding) -> String {
        let r = insert([path], into: text, at: focused.wrappedValue ? range(selection.wrappedValue, in: text) : nil)
        selection.wrappedValue = .cursor(utf16: r.cursor)
        focused.wrappedValue = true
        return r.text
    }

    /// The selection as UTF-16 offsets into text, nil when there is none.
    static func range(_ selection: TextSelection?, in text: String) -> Range<Int>? {
        guard let selection else { return nil }
        let r: Range<String.Index>
        switch selection.indices {
        case .selection(let s): r = s
        case .multiSelection(let set): guard let first = set.ranges.first else { return nil }; r = first
        @unknown default: return nil
        }
        // The selection may come from an earlier text (an upload ends after the draft changed): an index past the
        // end traps in utf16Offset, so clamp it first.
        let offset = { (i: String.Index) in min(i, text.endIndex).utf16Offset(in: text) }
        let lo = offset(r.lowerBound), hi = offset(r.upperBound)
        return lo..<max(lo, hi)
    }
}

@MainActor @Observable
final class FileUploads {
    static let limit: Int64 = 200 << 20

    struct Item: Identifiable {
        let id = UUID()
        var name: String
        var size: Int64
        var sent: Int64 = 0
        var waiting = true // behind another upload
    }

    private(set) var items: [Item] = []
    @ObservationIgnored private var tasks: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var last: Task<Void, Never>?

    /// Queues picked files for machine; done gets each one's path on the machine, in the order picked.
    /// dest "health": a report for the health space, kept in its reports/ (docs/assistant/health.md「体检报告」); nil the temp directory.
    func start(_ urls: [URL], machine: MachineRef, model: AppModel, dest: String? = nil, done: @escaping (String) -> Void) {
        let features = model.features(machine.group)
        if !features.isEmpty, !features.contains("file_upload") {
            model.toast = Toast(String(localized: "The hub is too old to receive files. Update the hub and try again."), style: .error)
            return
        }
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            let size = Int64((try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
            if size > Self.limit {
                if scoped { url.stopAccessingSecurityScopedResource() }
                model.toast = Toast(String(localized: "“\(url.lastPathComponent)” is \(size.storageText), over the 200 MB limit, and can't be sent"), style: .error)
                continue
            }
            let item = Item(name: url.lastPathComponent, size: size)
            items.append(item)
            let prev = last
            let task = Task { [weak self] in
                defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                await prev?.value
                guard let self, !Task.isCancelled else { return }
                await self.run(item.id, url: url, machine: machine, model: model, dest: dest, done: done)
            }
            tasks[item.id] = task
            last = task
        }
    }

    /// `start` for files wanted together: done gets their paths once every upload has ended (a failed or cancelled one
    /// is left out; nothing got through, no call). It runs even if the view that started them has gone.
    func startAll(_ urls: [URL], machine: MachineRef, model: AppModel, dest: String? = nil, done: @escaping ([String]) -> Void) {
        let got = Collected()
        start(urls, machine: machine, model: model, dest: dest) { got.paths.append($0) }
        let ending = last
        Task {
            await ending?.value
            if !got.paths.isEmpty { done(got.paths) }
        }
    }

    private final class Collected { var paths: [String] = [] }

    func cancel(_ id: UUID) {
        tasks.removeValue(forKey: id)?.cancel()
        items.removeAll { $0.id == id }
    }

    private func run(_ id: UUID, url: URL, machine: MachineRef, model: AppModel, dest: String?, done: (String) -> Void) async {
        defer {
            tasks[id] = nil
            items.removeAll { $0.id == id }
        }
        update(id) { $0.waiting = false }
        guard let api = model.api(machine.group) else { return }
        let name = url.lastPathComponent
        do {
            let staged = try await Self.stage(url)
            defer { try? FileManager.default.removeItem(at: staged.deletingLastPathComponent()) }
            update(id) { $0.size = (try? staged.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? $0.size }
            let out = try await api.upload(file: staged, name: name, machine: machine.id, dest: dest) { sent in
                Task { @MainActor [weak self] in self?.update(id) { $0.sent = max($0.sent, sent) } }
            }
            guard !Task.isCancelled else { return }
            Haptics.tap()
            done(out.path)
        } catch {
            if let msg = Self.message(error, name: name) { model.toast = Toast(msg, style: .error) }
        }
    }

    private func update(_ id: UUID, _ change: (inout Item) -> Void) {
        if let i = items.firstIndex(where: { $0.id == id }) { change(&items[i]) }
    }

    /// A private copy to upload from: the picker's URL may be an iCloud placeholder or lose its access mid-upload.
    nonisolated static func stage(_ url: URL, directory: URL? = nil) async throws -> URL {
        try await Task.detached {
            let dir = directory ?? FileManager.default.temporaryDirectory.appending(path: "uploads/" + UUID().uuidString)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let dest = dir.appending(path: url.lastPathComponent)
            var coordError: NSError?
            var copyError: Error?
            NSFileCoordinator().coordinate(readingItemAt: url, options: .withoutChanges, error: &coordError) { src in
                do { try FileManager.default.copyItem(at: src, to: dest) } catch { copyError = error }
            }
            if let e = coordError ?? copyError {
                try? FileManager.default.removeItem(at: dir)
                throw e
            }
            return dest
        }.value
    }

    /// What to tell the user when a file did not make it; nil when they cancelled it.
    nonisolated static func message(_ error: Error, name: String) -> String? {
        if error.isCancellation { return nil }
        if let e = error as? APIError {
            switch e.code {
            case "too_large": return String(localized: "“\(name)” is over 200 MB and can't be sent")
            case "node_outdated": return e.message
            default: break
            }
            if e.status == 404 { return String(localized: "The hub is too old to receive files. Update the hub and try again.") }
        }
        return String(localized: "Couldn't send “\(name)”: \(error.localizedDescription)")
    }
}

/// The uploads in flight above the input box: name, progress, cancel.
struct UploadsBar: View {
    var uploads: FileUploads

    var body: some View {
        VStack(spacing: Theme.Space.sm) {
            ForEach(uploads.items) { item in
                AttachmentRow(symbol: Theme.Icon.file, name: item.name,
                              status: item.waiting ? String(localized: "Waiting [File Attach 26c8cf]") : "\(item.sent.storageText) / \(item.size.storageText)",
                              progress: item.size > 0 ? Double(min(item.sent, item.size)) / Double(item.size) : 0,
                              removeLabel: String(localized: "Cancel upload of \(item.name)")) { uploads.cancel(item.id) }
            }
        }
    }
}

/// One row above the input box (a file uploading, a video on its way or ready): icon, name, status, the progress bar
/// while there is one (nil: none), and the button that cancels or removes it.
struct AttachmentRow: View {
    var symbol: String
    var name: String
    var status: String
    var progress: Double?
    var removeLabel: String
    var remove: () -> Void

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(symbol).font(Theme.Font.body).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack {
                    Text(name).font(Theme.Font.secondary).lineLimit(1).truncationMode(.middle)
                    Spacer(minLength: 8)
                    Text(status).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                }
                if let progress { ProgressView(value: progress).tint(Theme.accent) }
            }
            Button(action: remove) {
                ThemedIcon(Theme.Icon.clear).font(Theme.Font.title).symbolRenderingMode(.hierarchical)
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(removeLabel)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }
}

extension View {
    /// The Files picker of a composer: any kind of file, several at once.
    func fileUploadPicker(isPresented: Binding<Bool>, picked: @escaping ([URL]) -> Void) -> some View {
        fileImporter(isPresented: isPresented, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result, !urls.isEmpty { picked(urls) }
        }
    }
}
