#if os(iOS) || os(visionOS)
import AudioToolbox
import AVFoundation
import Foundation
import os
import PersonaKit

// The call's audio on an iPhone and in CarPlay (docs/assistant/voice.md 6.4). Adapted from the audio half of the earlier
// CarPlay `DuplexVoiceSession` in PR #1931 (coordination in #1951); #1931 now calls through `VoiceConversation`
// with this engine. Play-and-record / voice-chat with voice processing, AVAudioConverter down to the extension's
// PCM16, an AVAudioPlayerNode at its output rate, dropping what `clear` cancels by generation, and ending on a call
// or a media services reset. The microphone goes into a `VoiceUplink` (held until `ready`, muted as silence, measured
// for the orb). The route follows `VoiceRoute` (#3015): a connected headset plays and records, else the loudspeaker,
// and a device coming or going during the call is followed; a route change rebuilds the tap and converter (or the whole
// engine) instead of ending the call; voice processing that will not run falls back to plain capture. Nothing reads
// `engine.inputNode` before `VoiceCapture.plan` says there is an input that starts (#5029): the simulator records
// without voice processing, after a short AudioQueue recording proved the Mac lets it. Only APIs in the Xcode 26.0 SDK.

/// Turns the microphone's buffers into PCM16 at the extension's input rate and hands them to the uplink. The tap calls
/// `feed` on the audio thread, one buffer at a time (the converter's one-buffer handoff is #1931's).
nonisolated final class VoiceCapturePump: @unchecked Sendable {
    private let converter: AVAudioConverter
    private let target: AVAudioFormat
    private let sourceRate: Double
    private let uplink: VoiceUplink
    private let rate: Int
    // Touched only from the audio thread (feed) and the converter's input block, which runs inside feed.
    private var input: AVAudioPCMBuffer?
    private var supplied = false

    init(converter: AVAudioConverter, target: AVAudioFormat, sourceRate: Double, uplink: VoiceUplink, rate: Int) {
        self.converter = converter
        self.target = target
        self.sourceRate = sourceRate
        self.uplink = uplink
        self.rate = rate
    }

    func feed(_ buffer: AVAudioPCMBuffer) {
        guard sourceRate > 0,
              let out = AVAudioPCMBuffer(pcmFormat: target,
                                         frameCapacity: AVAudioFrameCount(Double(buffer.frameLength) * target.sampleRate / sourceRate) + 1)
        else { return }
        input = buffer
        supplied = false
        var failure: NSError?
        let status = converter.convert(to: out, error: &failure, withInputFrom: { _, inputStatus in
            self.supply(inputStatus)
        })
        input = nil
        guard status != .error, failure == nil, out.frameLength > 0, let channel = out.int16ChannelData?[0] else { return }
        uplink.push(VoicePCM.data(UnsafeBufferPointer(start: channel, count: Int(out.frameLength))), rate: rate)
    }

    private func supply(_ status: UnsafeMutablePointer<AVAudioConverterInputStatus>) -> AVAudioBuffer? {
        guard !supplied, let buffer = input else {
            status.pointee = .noDataNow
            return nil
        }
        supplied = true
        status.pointee = .haveData
        return buffer
    }
}

/// Whether the simulator's input starts, asked before a call reads `AVAudioEngine.inputNode` (#5029). The simulator
/// records from the Mac's microphone through the Mac's audio server; where the Mac does not let it (seen on a Mac that
/// had never decided whether the simulator may use the microphone), the input never starts. The engine's I/O unit then
/// waits on its setup RPC, which times out and aborts the app; an AudioQueue waits in `AudioQueueStart` instead, without
/// aborting. So a short AudioQueue recording runs first on a queue of its own, and the call waits for it a moment. One
/// that does not come back is left waiting, and no other starts until it does.
nonisolated final class VoiceInputProbe: @unchecked Sendable {
    static let shared = VoiceInputProbe(attempt: VoiceInputProbe.recordBriefly)
    /// A simulator whose input works starts it in well under this.
    static let timeout: TimeInterval = 3

    private let attempt: @Sendable () -> OSStatus
    private let queue = DispatchQueue(label: "voice.input-probe")
    private let lock = NSLock()
    // Guarded by `lock`.
    private var outstanding = false
    private var status: OSStatus = noErr

    init(attempt: @escaping @Sendable () -> OSStatus) {
        self.attempt = attempt
    }

    /// An attempt has not come back yet.
    var isOutstanding: Bool { lock.withLock { outstanding } }

    /// Blocks the caller for at most `timeout`.
    func run(timeout: TimeInterval = VoiceInputProbe.timeout) -> VoiceCapture.Probe {
        let free = lock.withLock {
            guard !outstanding else { return false }
            outstanding = true
            return true
        }
        guard free else { return .timedOut }
        let done = DispatchSemaphore(value: 0)
        queue.async { [self] in
            let result = attempt()
            lock.withLock {
                status = result
                outstanding = false
            }
            done.signal()
        }
        guard done.wait(timeout: .now() + timeout) == .success else { return .timedOut }
        let result = lock.withLock { status }
        return result == noErr ? .started : .failed(result)
    }

    /// Starts and stops a mono 16-bit recording: noErr once the input ran.
    private static func recordBriefly() -> OSStatus {
        var format = AudioStreamBasicDescription(mSampleRate: 16000, mFormatID: kAudioFormatLinearPCM,
                                                 mFormatFlags: kLinearPCMFormatFlagIsSignedInteger | kLinearPCMFormatFlagIsPacked,
                                                 mBytesPerPacket: 2, mFramesPerPacket: 1, mBytesPerFrame: 2, mChannelsPerFrame: 1,
                                                 mBitsPerChannel: 16, mReserved: 0)
        var made: AudioQueueRef?
        var status = AudioQueueNewInputWithDispatchQueue(&made, &format, 0, DispatchQueue.global(qos: .utility)) { @Sendable _, _, _, _, _ in }
        guard status == noErr, let queue = made else { return status }
        defer { AudioQueueDispose(queue, true) }
        var buffer: AudioQueueBufferRef?
        status = AudioQueueAllocateBuffer(queue, 3200, &buffer)
        guard status == noErr, let buffer else { return status }
        status = AudioQueueEnqueueBuffer(queue, buffer, 0, nil)
        guard status == noErr else { return status }
        status = AudioQueueStart(queue, nil)
        if status == noErr { AudioQueueStop(queue, true) }
        return status
    }
}

@MainActor final class PhoneVoiceAudio: VoiceAudio {
    nonisolated static let log = Logger(subsystem: "persona", category: "voice")

    /// Whether the microphone runs with echo cancellation (false on the simulator and where voice processing would not start).
    private(set) var cancelsEcho = false

    private var engine: AVAudioEngine?
    /// The engine's input node, once `wire` has read it: taking the tap off goes through this, not `engine.inputNode`
    /// (reading that property again can set the I/O unit up again).
    private var input: AVAudioInputNode?
    private var player: AVAudioPlayerNode?
    private var playFormat: AVAudioFormat?
    private var pump: VoiceCapturePump?
    private var uplink: VoiceUplink?
    private var format = VoiceAudioFormat.provisional
    private var events: (@MainActor (VoiceAudioEvent) -> Void)?
    /// Bumped by `clear` and every rebuild: completions of buffers scheduled before are ignored.
    private var generation = 0
    /// The loudness of the voice's buffers still queued, in order: the first is the one playing.
    private var queued: [Float] = []
    private var activated = false
    private var observers: [NSObjectProtocol] = []
    /// The engine's configuration-change observer: a rebuilt engine gets its own.
    private var engineObserver: NSObjectProtocol?
    /// How the microphone was opened at the start: a rebuilt engine opens it the same way.
    private var capture = VoiceCapture.voiceProcessing
    private var route = VoiceRoute.Follower(startedAt: .now)
    /// The call asked for a microphone or the loudspeaker: taken back at the end, so a recording or dictation after it
    /// starts from the system's own route.
    private var preferredInput = false
    private var overrodeOutput = false

    /// Bluetooth explicitly (#3015): hands-free first, so a headset's microphone records too (`.voiceChat` implies it,
    /// said here so nothing depends on that); A2DP only so a device that can only listen (a speaker, headphones without
    /// hands-free) still plays the voice, with the phone's microphone. A device with both is routed over hands-free (the
    /// SDK: HFP ports get the higher priority). The loudspeaker, not the earpiece, when nothing is connected. No AirPlay:
    /// its delay is too long for a conversation.
    static let sessionOptions: AVAudioSession.CategoryOptions = [.defaultToSpeaker, .allowBluetoothHFP, .allowBluetoothA2DP]

    func start(_ format: VoiceAudioFormat, uplink: VoiceUplink, events: @escaping @MainActor (VoiceAudioEvent) -> Void) throws {
        stop()
        self.format = format
        self.uplink = uplink
        self.events = events
        let session = AVAudioSession.sharedInstance()
        do {
            try session.setCategory(.playAndRecord, mode: .voiceChat, options: Self.sessionOptions)
            try session.setActive(true)
        } catch {
            Self.log.error("voice: audio session failed: \(error.localizedDescription, privacy: .public)")
            throw VoiceAudioError.session
        }
        activated = true
        // The route before the engine, so it is built for the device that plays.
        route = VoiceRoute.Follower(startedAt: .now)
        follow(route.starting(Self.snapshot(session)), why: "start")
        let capture = VoiceCapture.plan(recordingAllowed: AVAudioApplication.shared.recordPermission == .granted,
                                        inputAvailable: session.isInputAvailable, simulator: VoiceCapture.simulator,
                                        probe: { VoiceInputProbe.shared.run() })
        self.capture = capture
        do {
            switch capture {
            case .unavailable(let problem):
                Self.log.error("voice: no microphone to record from (\(String(describing: problem), privacy: .public))")
                throw problem == .permission ? VoiceAudioError.permission : VoiceAudioError.noInput
            case .voiceProcessing, .plain:
                try build(voiceProcessing: capture == .voiceProcessing)
            }
        } catch {
            stop()
            throw error
        }
        observe()
        // Once more with the engine running: voice processing can move the route as it starts.
        follow(route.starting(Self.snapshot(session)), why: "engine")
    }

    func reconfigure(_ format: VoiceAudioFormat) throws {
        guard format != self.format else { return }
        self.format = format
        guard let engine else { return }
        engine.stop()
        try rewire(engine)
    }

    func play(_ pcm: Data) { schedule(pcm, voice: true) }

    func cue(_ pcm: Data) { schedule(pcm, voice: false) }

    func clear() {
        generation += 1
        let was = !queued.isEmpty
        queued = []
        if let player {
            player.stop()
            if engine?.isRunning == true { player.play() }
        }
        if was {
            events?(.outputLevel(0))
            events?(.playing(false))
        }
    }

    func stop() {
        for token in observers { NotificationCenter.default.removeObserver(token) }
        observers = []
        if let engineObserver { NotificationCenter.default.removeObserver(engineObserver) }
        engineObserver = nil
        input?.removeTap(onBus: 0)
        engine?.stop()
        player?.stop()
        engine = nil
        input = nil
        player = nil
        playFormat = nil
        pump = nil
        generation += 1
        queued = []
        if activated {
            activated = false
            let session = AVAudioSession.sharedInstance()
            if preferredInput { try? session.setPreferredInput(nil) }
            if overrodeOutput { try? session.overrideOutputAudioPort(.none) }
            try? session.setActive(false, options: .notifyOthersOnDeactivation)
        }
        preferredInput = false
        overrodeOutput = false
        events = nil
        uplink = nil
    }

    // MARK: Engine

    /// Voice processing first, so the speaker's sound is taken out of the microphone; where it will not run, plain capture.
    /// Only after `VoiceCapture.plan` said the input starts: the first read of `engine.inputNode` sets the I/O unit up.
    private func build(voiceProcessing: Bool) throws {
        let engine = AVAudioEngine()
        cancelsEcho = false
        if voiceProcessing {
            do {
                // Before the formats are read: voice processing changes the input node's format.
                try engine.inputNode.setVoiceProcessingEnabled(true)
                cancelsEcho = true
            } catch {
                Self.log.notice("voice: no voice processing, capturing without echo cancellation: \(error.localizedDescription, privacy: .public)")
            }
        } else {
            Self.log.notice("voice: the simulator, capturing without echo cancellation")
        }
        do {
            try wire(engine)
            engine.prepare()
            try engine.start()
            self.engine = engine
        } catch where cancelsEcho {
            // Voice processing that turns on and then will not let the engine start.
            Self.log.notice("voice: the engine would not start with voice processing, capturing without echo cancellation")
            input?.removeTap(onBus: 0)
            engine.stop()
            cancelsEcho = false
            let plain = AVAudioEngine()
            try wire(plain)
            plain.prepare()
            do { try plain.start() } catch { throw VoiceAudioError.engine }
            self.engine = plain
        }
    }

    /// The tap and the converter for the input's format as it is now, and a player at the output rate.
    private func wire(_ engine: AVAudioEngine) throws {
        guard let uplink,
              let playFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(format.outputRate),
                                             channels: 1, interleaved: false),
              let captureFormat = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: Double(format.inputRate),
                                                channels: 1, interleaved: true)
        else { throw VoiceAudioError.format }
        let input = engine.inputNode
        let hardware = input.outputFormat(forBus: 0)
        guard hardware.sampleRate > 0, hardware.channelCount > 0,
              let converter = AVAudioConverter(from: hardware, to: captureFormat) else { throw VoiceAudioError.format }
        let player = AVAudioPlayerNode()
        engine.attach(player)
        engine.connect(player, to: engine.mainMixerNode, format: playFormat)
        let pump = VoiceCapturePump(converter: converter, target: captureFormat, sourceRate: hardware.sampleRate,
                                    uplink: uplink, rate: format.inputRate)
        Self.installTap(input, pump)
        self.input = input
        self.player = player
        self.playFormat = playFormat
        self.pump = pump
    }

    /// Takes the engine apart and puts it together again for the input as it is now (a new route or new rates). What
    /// was queued to play is gone with the old player.
    private func rewire(_ engine: AVAudioEngine) throws {
        input?.removeTap(onBus: 0)
        input = nil
        if let player { engine.detach(player) }
        player = nil
        pump = nil
        dropQueued()
        // The new route may have no input (#5029): reading the input node then would set up an I/O unit that cannot start.
        guard AVAudioSession.sharedInstance().isInputAvailable else { throw VoiceAudioError.noInput }
        try wire(engine)
        engine.prepare()
        do { try engine.start() } catch { throw VoiceAudioError.engine }
    }

    /// A new engine for the route as it is now, opened the way the call started (voice processing, plain capture if it
    /// will not run): the old one would not start again there (voice processing on a route it does not run on, a
    /// headset still coming up).
    private func rebuild() throws {
        if let engineObserver { NotificationCenter.default.removeObserver(engineObserver) }
        engineObserver = nil
        input?.removeTap(onBus: 0)
        engine?.stop()
        engine = nil
        input = nil
        player = nil
        playFormat = nil
        pump = nil
        dropQueued()
        guard AVAudioSession.sharedInstance().isInputAvailable else { throw VoiceAudioError.noInput }
        try build(voiceProcessing: capture == .voiceProcessing)
        watchEngine()
    }

    /// What was queued to play is gone with the old player.
    private func dropQueued() {
        let was = !queued.isEmpty
        generation += 1
        queued = []
        if was {
            events?(.outputLevel(0))
            events?(.playing(false))
        }
    }

    // The tap runs on the audio thread: its closure is made here, outside the main actor.
    nonisolated private static func installTap(_ input: AVAudioInputNode, _ pump: VoiceCapturePump) {
        input.installTap(onBus: 0, bufferSize: 1024, format: input.outputFormat(forBus: 0)) { buffer, _ in
            pump.feed(buffer)
        }
    }

    // MARK: Playback

    private func schedule(_ pcm: Data, voice: Bool) {
        guard let player, let playFormat, engine?.isRunning == true else { return }
        let samples = VoicePCM.floats(pcm)
        guard !samples.isEmpty,
              let buffer = AVAudioPCMBuffer(pcmFormat: playFormat, frameCapacity: AVAudioFrameCount(samples.count)),
              let channel = buffer.floatChannelData?[0]
        else { return }
        buffer.frameLength = AVAudioFrameCount(samples.count)
        samples.withUnsafeBufferPointer { channel.update(from: $0.baseAddress!, count: samples.count) }
        let generation = generation
        if voice {
            let level = VoicePCM.rms(samples)
            if queued.isEmpty {
                events?(.playing(true))
                events?(.outputLevel(level))
            }
            queued.append(level)
        }
        player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { @Sendable [weak self] _ in
            Task { @MainActor in self?.played(generation, voice: voice) }
        }
        if !player.isPlaying { player.play() }
    }

    /// One buffer finished: the next one's loudness, or quiet when none is left.
    private func played(_ generation: Int, voice: Bool) {
        guard voice, generation == self.generation, !queued.isEmpty else { return }
        queued.removeFirst()
        if let next = queued.first {
            events?(.outputLevel(next))
        } else {
            events?(.outputLevel(0))
            events?(.playing(false))
        }
    }

    // MARK: System

    private func observe() {
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: AVAudioSession.interruptionNotification, object: nil,
                                            queue: .main) { @Sendable [weak self] note in
            let type = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt).flatMap(AVAudioSession.InterruptionType.init)
            guard type == .began else { return }
            MainActor.assumeIsolated { self?.events?(.interrupted) }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: nil,
                                            queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.events?(.reset) }
        })
        // Headphones in or out, Bluetooth connected or gone: choose the route again (`VoiceRoute`).
        observers.append(center.addObserver(forName: AVAudioSession.routeChangeNotification, object: nil,
                                            queue: .main) { @Sendable [weak self] note in
            let reason = (note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt)
                .flatMap(AVAudioSession.RouteChangeReason.init)
            MainActor.assumeIsolated { self?.routeMoved(reason) }
        })
        watchEngine()
    }

    /// A route change stops the engine when it changes the I/O (and can change the input's format): set the tap and the
    /// converter up again and go on, else a new engine; only when that fails too does the call end.
    private func watchEngine() {
        if let engineObserver { NotificationCenter.default.removeObserver(engineObserver) }
        engineObserver = nil
        guard let engine else { return }
        engineObserver = NotificationCenter.default.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine,
                                                                queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.routeChanged() }
        }
    }

    private func routeMoved(_ reason: AVAudioSession.RouteChangeReason?) {
        guard engine != nil else { return }
        let change: VoiceRoute.Change = switch reason {
        case .newDeviceAvailable: .deviceArrived
        case .oldDeviceUnavailable: .deviceLeft
        default: .other
        }
        let session = AVAudioSession.sharedInstance()
        follow(route.changed(Self.snapshot(session), change, at: .now), why: Self.describe(reason))
        // An engine the change stopped without telling (the configuration change normally does).
        routeChanged()
    }

    private func routeChanged() {
        guard let engine, !engine.isRunning else { return }
        do {
            try rewire(engine)
            Self.log.notice("voice: audio route changed, microphone set up again (\(Self.describe(AVAudioSession.sharedInstance().currentRoute), privacy: .public))")
        } catch {
            Self.log.notice("voice: the engine would not start again on the new route, building a new one")
            do {
                try rebuild()
                Self.log.notice("voice: new engine on the new route (echo cancellation \(self.cancelsEcho, privacy: .public))")
            } catch {
                Self.log.error("voice: audio route changed and the microphone could not be set up again")
                events?(.failed)
            }
        }
    }

    // MARK: Route

    /// Applies what `VoiceRoute` decided. Failing to move the route never ends the call: it goes on where it is.
    private func follow(_ plan: VoiceRoute.Plan, why: String) {
        let session = AVAudioSession.sharedInstance()
        if let id = plan.input {
            if let port = session.availableInputs?.first(where: { $0.uid == id }) {
                do {
                    try session.setPreferredInput(port)
                    preferredInput = true
                } catch {
                    Self.log.notice("voice: could not prefer the \(port.portType.rawValue, privacy: .public) microphone: \(error.localizedDescription, privacy: .public)")
                }
            }
        }
        if plan.speaker {
            do {
                try session.overrideOutputAudioPort(.speaker)
                overrodeOutput = true
            } catch {
                Self.log.notice("voice: could not move the voice to the speaker: \(error.localizedDescription, privacy: .public)")
            }
        }
        // Port kinds only, never device names (they can carry a person's name).
        Self.log.notice("voice: route after \(why, privacy: .public): \(Self.describe(session.currentRoute), privacy: .public)\(plan == .keep ? "" : " (moved)", privacy: .public)")
    }

    private static func snapshot(_ session: AVAudioSession) -> VoiceRoute.Snapshot {
        let route = session.currentRoute
        return VoiceRoute.Snapshot(outputs: route.outputs.map(device), inputs: route.inputs.map(device),
                                   available: (session.availableInputs ?? []).map(device))
    }

    private static func device(_ port: AVAudioSessionPortDescription) -> VoiceRoute.Device {
        VoiceRoute.Device(port: kind(port.portType), id: port.uid, name: port.portName)
    }

    static func kind(_ port: AVAudioSession.Port) -> VoiceRoute.Port {
        switch port {
        case .builtInMic: .builtInMic
        case .builtInReceiver: .builtInReceiver
        case .builtInSpeaker: .builtInSpeaker
        case .headphones: .headphones
        case .headsetMic: .headsetMic
        case .bluetoothHFP: .bluetoothHFP
        case .bluetoothA2DP: .bluetoothA2DP
        case .bluetoothLE: .bluetoothLE
        case .usbAudio: .usbAudio
        case .carAudio: .carAudio
        default: .other
        }
    }

    private static func describe(_ route: AVAudioSessionRouteDescription) -> String {
        "out " + route.outputs.map(\.portType.rawValue).joined(separator: "+")
            + ", in " + route.inputs.map(\.portType.rawValue).joined(separator: "+")
    }

    private static func describe(_ reason: AVAudioSession.RouteChangeReason?) -> String {
        switch reason {
        case .newDeviceAvailable: "a new device"
        case .oldDeviceUnavailable: "a device gone"
        case .categoryChange: "a category change"
        case .override: "an override"
        case .wakeFromSleep: "waking"
        case .noSuitableRouteForCategory: "no suitable route"
        case .routeConfigurationChange: "a configuration change"
        default: "a route change"
        }
    }
}
#endif
