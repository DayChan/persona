import AVFoundation
import SwiftUI

/// Audio the file viewer plays (docs/apple/ios.md「音频」): what AVFoundation reads on the device.
nonisolated enum AudioFileType {
    static let extensions: Set<String> = ["mp3", "m4a", "aac", "wav", "flac", "aif", "aiff", "caf", "m4b", "oga", "ogg", "opus"]

    static func matches(name: String, mime: String?) -> Bool {
        (mime ?? "").lowercased().hasPrefix("audio/") || extensions.contains((name as NSString).pathExtension.lowercased())
    }

    /// "1:05", "1:02:03".
    static func clock(_ seconds: Double) -> String {
        guard seconds.isFinite, seconds >= 0 else { return "0:00" }
        let s = Int(seconds.rounded(.down))
        return s >= 3600 ? String(format: "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60) : String(format: "%d:%02d", s / 60, s % 60)
    }

    static let speeds: [Float] = [0.75, 1, 1.25, 1.5, 2]
}

/// An audio file downloaded whole into a temporary folder (as video is) and played: play / pause, the position as
/// it plays, seeking, the speed. Leaving the page stops it and removes the copy.
@MainActor @Observable
final class AudioFileModel {
    let ref: FileRef
    let file: FileContent
    @ObservationIgnored private let api: APIClient?
    private(set) var player: AVPlayer?
    private(set) var localURL: URL?
    private(set) var error: String?
    private(set) var received: Int64 = 0
    private(set) var total: Int64 = 0
    private(set) var position: Double = 0
    private(set) var duration: Double = 0
    private(set) var playing = false
    var speed: Float = 1 { didSet { if playing { player?.rate = speed } } }
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var timeObserver: Any?
    @ObservationIgnored private var endObserver: NSObjectProtocol?

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
        var url: URL?
        var kept = false
        defer { if !kept { MachineFileDownload.remove(url) } }
        do {
            let fetched = try await MachineFileDownload.fetch(ref, file: file, api: api, prefix: "persona-audio-") { [weak self] received, total in
                guard let self, self.generation == mine else { return }
                self.received = received
                if total > 0 { self.total = total }
            }
            url = fetched
            guard generation == mine else { return }
            let asset = AVURLAsset(url: fetched)
            guard try await asset.load(.isPlayable) else {
                throw APIError(status: 0, message: String(localized: "This audio format can't play on this device. Convert it to MP3, AAC (M4A), or WAV and try again."))
            }
            let seconds = try await asset.load(.duration).seconds
            guard generation == mine else { return }
            localURL = fetched
            kept = true
            duration = seconds.isFinite ? seconds : 0
            let item = AVPlayerItem(asset: asset)
            let p = AVPlayer(playerItem: item)
            timeObserver = p.addPeriodicTimeObserver(forInterval: CMTime(value: 1, timescale: 4), queue: .main) { @Sendable [weak self] t in
                MainActor.assumeIsolated { self?.position = t.seconds }
            }
            endObserver = NotificationCenter.default.addObserver(forName: .AVPlayerItemDidPlayToEndTime, object: item, queue: .main) { @Sendable [weak self] _ in
                MainActor.assumeIsolated {
                    self?.playing = false
                    self?.player?.seek(to: .zero)
                }
            }
            player = p
        } catch {
            guard generation == mine, !Task.isCancelled else { return }
            self.error = error.shown
        }
    }

    func toggle() {
        guard let player else { return }
        if playing {
            player.pause()
        } else {
            player.playImmediately(atRate: speed)
        }
        playing.toggle()
    }

    func seek(to seconds: Double) {
        position = seconds
        player?.seek(to: CMTime(seconds: seconds, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero)
    }

    func skip(_ seconds: Double) { seek(to: min(max(0, position + seconds), duration)) }

    func stop() {
        generation += 1
        if let timeObserver { player?.removeTimeObserver(timeObserver) }
        timeObserver = nil
        if let endObserver { NotificationCenter.default.removeObserver(endObserver) }
        endObserver = nil
        player?.pause()
        player = nil
        playing = false
        position = 0
        MachineFileDownload.remove(localURL)
        localURL = nil
    }
}

struct AudioFileView: View {
    let model: AudioFileModel
    @State private var scrub: Double?

    var body: some View {
        Group {
            if let error = model.error {
                EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Can't Play Audio"), message: error)
            } else if model.player != nil {
                player
            } else {
                VStack(spacing: Theme.Space.m) {
                    ProgressView(value: Double(model.received), total: Double(max(model.total, 1)))
                    Text("Downloading audio…").font(Theme.Font.body)
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

    private var player: some View {
        VStack(spacing: Theme.Space.xl) {
            Spacer()
            ThemedIcon(Theme.Icon.voiceMessage)
                .font(Theme.Font.display)
                .foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.box80, height: Theme.Size.box80)
                .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
            Text(model.file.name)
                .font(Theme.Font.heading)
                .lineLimit(2)
                .multilineTextAlignment(.center)
            VStack(spacing: Theme.Space.xs) {
                Slider(value: Binding(get: { scrub ?? model.position }, set: { scrub = $0 }),
                       in: 0...max(model.duration, 0.1)) { editing in
                    if !editing, let s = scrub {
                        model.seek(to: s)
                        scrub = nil
                    }
                }
                .accessibilityLabel("Playback position")
                .accessibilityIdentifier("audio-position")
                HStack {
                    Text(AudioFileType.clock(scrub ?? model.position))
                    Spacer()
                    Text("-" + AudioFileType.clock(max(0, model.duration - (scrub ?? model.position))))
                }
                .font(Theme.Font.label)
                .monospacedDigit()
                .foregroundStyle(.secondary)
            }
            HStack(spacing: Theme.Space.xxl) {
                Button { model.skip(-15) } label: { ThemedIcon(Theme.Icon.skipBack) }
                    .accessibilityLabel("Back 15 Seconds")
                Button { model.toggle() } label: {
                    ThemedIcon(model.playing ? Theme.Icon.pause : Theme.Icon.run)
                        .font(Theme.Font.display)
                        .scaledFrame(width: Theme.Size.avatarXL, height: Theme.Size.avatarXL)
                }
                .accessibilityLabel(model.playing ? String(localized: "Pause") : String(localized: "Play"))
                .accessibilityIdentifier("audio-play")
                Button { model.skip(15) } label: { ThemedIcon(Theme.Icon.skipForward) }
                    .accessibilityLabel("Forward 15 Seconds")
            }
            .font(Theme.Font.title)
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
            Menu {
                Picker("Speed", selection: Binding(get: { model.speed }, set: { model.speed = $0 })) {
                    ForEach(AudioFileType.speeds, id: \.self) { s in Text(Self.speedText(s)).tag(s) }
                }
            } label: {
                Text("Speed \(Self.speedText(model.speed))").font(Theme.Font.caption).lineLimit(1)
            }
            .arrowlessMenu()
            .accessibilityIdentifier("audio-speed")
            Spacer()
        }
        .padding(.horizontal, Theme.Space.xl)
        .frame(maxWidth: Theme.Size.readable)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("file-audio")
    }

    static func speedText(_ s: Float) -> String {
        s == s.rounded() ? "\(Int(s))×" : String(format: "%g×", s)
    }
}
