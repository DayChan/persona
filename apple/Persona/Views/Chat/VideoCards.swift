import AVKit
import SwiftUI

/// The files a sent message carries (`AttachedFile`), above its text: one card each.
struct AttachedFileCards: View {
    @Environment(\.fileScope) private var scope
    var files: [AttachedFile]

    var body: some View {
        VStack(alignment: .trailing, spacing: Theme.Space.sm) {
            ForEach(files, id: \.path) { f in AttachedVideoCard(file: f, machine: scope?.machine) }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

/// A video sent with a message: name, size and length; a tap plays it in the system's player — this phone's copy
/// when there is one (`AttachedVideos`), else downloaded from the machine first (the card shows how far; a second
/// tap stops it).
struct AttachedVideoCard: View {
    @Environment(AppModel.self) private var model
    var file: AttachedFile
    var machine: MachineRef?
    @State private var received: Int64?
    @State private var task: Task<Void, Never>?
    #if os(macOS)
    @State private var playing: MacVideo?
    #endif

    var body: some View {
        Button(action: tap) {
            HStack(spacing: Theme.Space.m) {
                ZStack {
                    RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).fill(Theme.accent.opacity(0.14))
                    if received != nil {
                        ProgressView().controlSize(.small)
                    } else {
                        ThemedIcon(Theme.Icon.run).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                    }
                }
                .scaledFrame(width: Theme.Size.avatarL, height: Theme.Size.avatarL)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(file.name).font(Theme.Font.secondaryMedium).foregroundStyle(Color.primary)
                        .lineLimit(1).truncationMode(.middle)
                    Text(status).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 0)
            }
            .padding(Theme.Space.s)
            .padding(.trailing, Theme.Space.xs)
            .scaledFrame(maxWidth: Theme.Size.box280, alignment: .leading)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .contentShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Video \(file.name)")
        .accessibilityValue(status)
        .accessibilityHint(received == nil ? String(localized: "Play") : String(localized: "Stop download"))
        .accessibilityIdentifier("video-card")
        .onDisappear { task?.cancel() }
        #if os(macOS)
        .sheet(item: $playing) { v in
            VideoPlayer(player: v.player)
                .macLargeSheet()
                .onAppear { v.player.play() }
                .onDisappear { v.player.pause() }
                .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done [Extension System Presentation c0b3fb]") { playing = nil }.lineLimit(1) } }
        }
        #endif
    }

    private var status: String {
        guard let received else { return file.detail }
        let total = max(file.size, 1)
        return String(localized: "Downloading \(Int(Double(min(received, total)) / Double(total) * 100))%")
    }

    private func tap() {
        if let task { // downloading: stop
            task.cancel()
            self.task = nil
            received = nil
            return
        }
        guard let machine else { return }
        if let local = AttachedVideos.local(machine, file.path) { return play(local) }
        guard let api = model.api(machine.group) else {
            model.toast = Toast(String(localized: "Not connected to the hub [App Model 93d955]"), style: .error)
            return
        }
        received = 0
        let file = file
        task = Task {
            let dir = FileManager.default.temporaryDirectory.appending(path: "attached-video-" + UUID().uuidString)
            defer {
                try? FileManager.default.removeItem(at: dir)
                task = nil
                received = nil
            }
            do {
                try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                let dest = dir.appending(path: (file.path as NSString).lastPathComponent)
                try await api.download("/api/v1/machines/\(machine.id)/file/download", query: ["path": file.path, "cwd": ""], to: dest) { got, _ in
                    Task { @MainActor in if received != nil { received = got } }
                }
                try Task.checkCancellation()
                guard try await AVURLAsset(url: dest).load(.isPlayable) else {
                    throw APIError(status: 0, message: String(localized: "This format or codec can't play on this device"))
                }
                try Task.checkCancellation()
                play(AttachedVideos.keep(dest, machine, file.path) ?? dest)
            } catch {
                if !error.isCancellation, let shown = error.shown { model.toast = Toast(String(localized: "Can't play “\(file.name)”: \(shown)"), style: .error) }
            }
        }
    }

    private func play(_ url: URL) {
        #if os(macOS)
        playing = MacVideo(player: AVPlayer(url: url))
        #else
        SystemVideoPlayer.present(url)
        #endif
    }
}

#if os(macOS)
private struct MacVideo: Identifiable {
    let id = UUID()
    let player: AVPlayer
}
#else
/// The system's full-screen player (AVPlayerViewController), presented over whatever is on top.
enum SystemVideoPlayer {
    static func present(_ url: URL) {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let scene = scenes.first { $0.activationState == .foregroundActive } ?? scenes.first
        guard var top = scene?.keyWindow?.rootViewController ?? scene?.windows.first?.rootViewController else { return }
        while let next = top.presentedViewController, !next.isBeingDismissed { top = next }
        let vc = AVPlayerViewController()
        vc.player = AVPlayer(url: url)
        vc.modalPresentationStyle = .fullScreen
        top.present(vc, animated: true) { vc.player?.play() }
    }
}
#endif
