#if os(iOS) || os(visionOS) || os(watchOS)
import AVFoundation
import Observation
#if os(iOS) || os(visionOS)
import UIKit
#else
import WatchKit
#endif

/// Hourhand's phone / watch session management, sharing the segment engine, pausing, bookmarks and resource protection.
@Observable
final class RecordingCapture {
    static let shared = RecordingCapture()
    enum State { case idle, starting, recording, paused }
    private(set) var state: State = .idle
    private(set) var elapsed: Double = 0
    private(set) var recordingID: UUID?
    var error: String?
    /// Non-blocking hint, e.g. the Bluetooth microphone was not found and the phone mic is used.
    private(set) var notice: String?
    var onSaved: ((UUID) -> Void)?
    /// Paused by the system (a phone call, Siri, an alarm), not by the user: it picks up again when the system ends the
    /// interruption, or when the app comes back to the front (`resumeAfterInterruption`).
    private(set) var interrupted = false
    /// The system ended an interruption but the microphone could not be opened again (the app was in the background): the
    /// phone tells the user to come back (a notification), since nothing else will resume it.
    @ObservationIgnored var onInterruptionNotResumed: (() -> Void)?
    /// The recorded time now, read without observing it (`elapsed` changes four times a second).
    var timeline: Double { writer?.elapsed ?? sampleElapsed }
    @ObservationIgnored private var sampleElapsed: Double = 0
    @ObservationIgnored private var input: RecordingInput = .phone
    @ObservationIgnored private var writer: RecordingSegmentWriter?
    @ObservationIgnored private var ticker: Task<Void, Never>?
    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    @ObservationIgnored private var generation = UUID()
    @ObservationIgnored private var activeMicOwner: UUID?
    @ObservationIgnored private var resumeTask: Task<Void, Never>?

    private init() {
        RecordingLibrary.shared.recover(source: Self.source)
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: AVAudioSession.interruptionNotification, object: nil, queue: .main) { @Sendable [weak self] note in
            let type = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt).flatMap(AVAudioSession.InterruptionType.init)
            MainActor.assumeIsolated { self?.interruption(type) }
        })
        observers.append(center.addObserver(forName: .AVAudioEngineConfigurationChange, object: nil, queue: .main) { @Sendable [weak self] note in
            let engine = note.object.map { ObjectIdentifier($0 as AnyObject) }
            MainActor.assumeIsolated {
                // Every engine posts it (dictation's, a voice call's): only this recording's own engine matters.
                guard let self, self.state == .recording, self.writer?.owns(engine) == true else { return }
                do { try self.writer?.restartAfterConfigurationChange() }
                catch {
                    // Often the route switching to a call before its interruption arrives: paused as by one, so the call's
                    // end (or coming back to the app) picks it up again, and what came before is kept.
                    self.error = String(localized: "The microphone changed, so recording paused")
                    self.writer?.pause(); self.state = .paused; self.interrupted = true
                    if let id = self.recordingID, let segment = self.writer?.cut() { self.save(segment, id: id) }
                }
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated {
                // Paused too (during a call): the engine it would resume is gone with the reset.
                guard let self, self.state == .recording || self.state == .paused else { return }
                self.stop(); self.error = String(localized: "The system audio service reset. What was finished is saved; start recording again.")
            }
        })
    }

    private static var source: RecordingEntry.Source {
        #if os(watchOS)
        .watch
        #else
        .phone
        #endif
    }

    private func configureSession(token: UUID) async throws {
        let session = AVAudioSession.sharedInstance()
        guard generation == token else { return }
        notice = nil
        #if os(iOS) || os(visionOS)
        #if os(iOS)
        if input == .bluetooth {
            try session.setCategory(.playAndRecord, mode: .default, options: [.allowBluetoothHFP])
            try session.setActive(true)
            if let port = await bluetoothInput(session, token: token) {
                guard generation == token else { return }
                try session.setPreferredInput(port)
                return
            }
            guard generation == token else { return }
            notice = String(localized: "No glasses or Bluetooth headset microphone found; using the phone's microphone")
        }
        #endif
        try session.setCategory(.playAndRecord, mode: .default, options: [.defaultToSpeaker])
        try session.setActive(true)
        #if os(iOS)
        let ports = session.availableInputs ?? []
        if let index = RecordingInput.preferredIndex(ports.map(\.portType), for: .phone) {
            try session.setPreferredInput(ports[index])
        }
        #else
        if let mic = session.availableInputs?.first(where: { $0.portType == .builtInMic }) { try session.setPreferredInput(mic) }
        #endif
        #else
        try session.setCategory(.record, mode: .default)
        try session.setActive(true)
        #endif
    }

    #if os(iOS)
    /// The Bluetooth HFP input appears only after the route settles, so poll for up to two seconds.
    private func bluetoothInput(_ session: AVAudioSession, token: UUID) async -> AVAudioSessionPortDescription? {
        var waited = 0
        while generation == token, !Task.isCancelled {
            let ports = session.availableInputs ?? []
            if let index = RecordingInput.preferredIndex(ports.map(\.portType), for: .bluetooth) { return ports[index] }
            guard waited < 10 else { return nil }
            do { try await Task.sleep(for: .milliseconds(200)) } catch { return nil }
            waited += 1
        }
        return nil
    }

    /// Name of the input the current route records from, shown while recording.
    var inputName: String? {
        guard state == .recording || state == .paused else { return nil }
        return AVAudioSession.sharedInstance().currentRoute.inputs.first?.portName
    }
    #endif

    private static func permission() async -> Bool {
        if #available(iOS 17.0, watchOS 10.0, *) { return await AVAudioApplication.requestRecordPermission() }
        return await withCheckedContinuation { continuation in
            AVAudioSession.sharedInstance().requestRecordPermission { @Sendable allowed in continuation.resume(returning: allowed) }
        }
    }

    /// `title` / `meeting`: what the recording is called and, for a meeting recording, its meeting (docs/apple/recording.md).
    func start(locale: String = "zh-CN", input: RecordingInput = .phone, title: String = "", meeting: RecordingMeeting? = nil) async {
        guard state == .idle else { return }
        generation = UUID(); let token = generation
        state = .starting; error = nil; notice = nil
        self.input = input
        let allowed = await Self.permission()
        guard generation == token, state == .starting else { return }
        guard allowed else { state = .idle; error = String(localized: "Allow Persona to use the microphone in Settings"); return }
        if let problem = Self.resourceProblem(starting: true) { state = .idle; error = problem; return }
        let micOwner = UUID()
        guard RecordingMicrophone.shared.claim(micOwner) else {
            state = .idle; error = String(localized: "The microphone is in use for dictation. Finish dictating before recording."); return
        }
        activeMicOwner = micOwner
        do {
            try await configureSession(token: token)
            // Cancelled while Bluetooth discovery was running: cancelStarting() already released the session and mic.
            guard generation == token else { return }
            let entry = try RecordingLibrary.shared.create(source: Self.source, locale: locale, title: title, meeting: meeting)
            recordingID = entry.id
            let writer = RecordingSegmentWriter(directory: RecordingLibrary.shared.folder(entry.id))
            writer.segmentLength = Self.segmentLength
            writer.onSegment = { @Sendable [weak self] segment in
                Task { @MainActor in
                    self?.save(segment, id: entry.id)
                }
            }
            try writer.start()
            self.writer = writer
            elapsed = 0; state = .recording
            startTicker()
        } catch {
            guard generation == token else { return }
            if let id = recordingID { RecordingLibrary.shared.change(id) { $0.active = false; $0.expectedSegments = 0 } }
            recordingID = nil; state = .idle
            releaseAudioSession()
            self.error = String(localized: "Can't open the microphone. Try again later.")
        }
    }

    func cancelStarting() {
        guard state == .starting else { return }
        generation = UUID(); state = .idle; notice = nil
        releaseAudioSession()
    }

    func togglePause() {
        guard let writer else { return }
        if state == .recording {
            interrupted = false
            resumeTask?.cancel(); resumeTask = nil
            writer.pause(); state = .paused
        } else if state == .paused {
            resume(writer)
        }
    }

    /// After a phone call, or when the app comes back to the front while one left the recording paused: carries on.
    /// Nothing when the user paused it.
    func resumeAfterInterruption() {
        guard interrupted, state == .paused, let writer else { return }
        resume(writer)
    }

    private func resume(_ writer: RecordingSegmentWriter) {
        guard resumeTask == nil else { return }
        let token = generation
        resumeTask = Task { @MainActor [weak self] in
            defer { self?.resumeTask = nil }
            guard let self else { return }
            do {
                try await self.configureSession(token: token)
                guard self.generation == token, self.writer === writer, self.state == .paused else { return }
                try writer.resume()
                self.state = .recording; self.interrupted = false; self.error = nil
            } catch {
                guard self.generation == token else { return }
                self.error = String(localized: "Can't open the microphone; the recording is still saved")
                // Still `interrupted`: coming back to the app tries again.
                if self.interrupted { self.onInterruptionNotResumed?() }
            }
        }
    }

    func addMark() {
        guard let id = recordingID, let writer else { return }
        let time = writer.elapsed
        RecordingLibrary.shared.change(id) { entry in
            if let last = entry.marks.last, time - last < 1 { return }
            entry.marks.append(time)
        }
    }

    @discardableResult func stop() -> UUID? {
        if state == .starting { cancelStarting(); return nil }
        guard let writer, let id = recordingID else { return nil }
        generation = UUID()
        resumeTask?.cancel(); resumeTask = nil
        ticker?.cancel(); ticker = nil
        writer.stop()
        // A stop on a segment boundary leaves no tail segment: take every finalized part before writing the end message.
        for segment in writer.finishedSegments { save(segment, id: id) }
        RecordingLibrary.shared.change(id) { entry in
            entry.active = false
            entry.expectedSegments = (entry.segments.map(\.index).max() ?? -1) + 1
        }
        self.writer = nil; recordingID = nil; state = .idle; interrupted = false; notice = nil
        releaseAudioSession()
        onSaved?(id)
        return id
    }

    private func releaseAudioSession() {
        #if os(iOS)
        try? AVAudioSession.sharedInstance().setPreferredInput(nil)
        #endif
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        if let owner = activeMicOwner {
            activeMicOwner = nil
            RecordingMicrophone.shared.release(owner)
        }
    }

    private func save(_ segment: RecordingSegmentWriter.Segment, id: UUID) {
        RecordingLibrary.shared.addSegment(id, RecordingSegment(index: segment.index, start: segment.start, duration: segment.duration))
        onSaved?(id)
    }

    private func startTicker() {
        ticker = Task { [weak self] in
            var count = 0
            while !Task.isCancelled {
                do { try await Task.sleep(for: .milliseconds(250)) } catch { return }
                guard let self, let writer = self.writer else { return }
                self.elapsed = writer.elapsed
                writer.segmentLength = Self.segmentLength
                count += 1
                if let failure = writer.failure ?? RecordingLibrary.shared.error { self.stop(); self.error = failure; return }
                if count % 20 == 0, let problem = Self.resourceProblem(starting: false) { self.stop(); self.error = problem; return }
            }
        }
    }

    private func interruption(_ type: AVAudioSession.InterruptionType?) {
        if type == .began, state == .recording, let writer, let id = recordingID {
            interrupted = true; writer.pause(); state = .paused
            // A long call: what was said before it is kept even if the app is stopped before the call ends (a segment
            // still open would be deleted by `recover`). The next segment starts when the recording resumes.
            if let segment = writer.cut() { save(segment, id: id) }
        } else if type == .ended {
            // The system says whether to resume (`shouldResume`); a recording resumes either way: the meeting goes on.
            resumeAfterInterruption()
        }
    }

    #if DEBUG
    /// Screenshots (`-showMeetingSample`): `id` shows as being recorded `elapsed` seconds in, paused or paused by a call,
    /// with no microphone (Stop does nothing).
    func showSample(_ id: UUID, elapsed: Double, paused: Bool = false, interrupted: Bool = false) {
        recordingID = id; self.elapsed = elapsed; sampleElapsed = elapsed
        self.interrupted = interrupted
        state = paused || interrupted ? .paused : .recording
    }
    #endif

    private static var segmentLength: Double {
        #if os(iOS) || os(visionOS)
        UIApplication.shared.applicationState == .active ? RecordingAudioSpec.liveSegmentSeconds : RecordingAudioSpec.segmentSeconds
        #else
        WatchRecordingTransfer.shared.reachable ? RecordingAudioSpec.liveSegmentSeconds : RecordingAudioSpec.segmentSeconds
        #endif
    }

    private static func resourceProblem(starting: Bool) -> String? {
        let attributes = try? FileManager.default.attributesOfFileSystem(forPath: NSHomeDirectory())
        let free = (attributes?[.systemFreeSize] as? NSNumber)?.int64Value
        if let free, free < (starting ? 300_000_000 : 150_000_000) { return String(localized: "Not enough storage. Saved recordings won't be lost.") }
        #if os(watchOS)
        let device = WKInterfaceDevice.current(); device.isBatteryMonitoringEnabled = true
        if device.batteryState == .unplugged, device.batteryLevel >= 0, device.batteryLevel < 0.05 { return String(localized: "Watch battery is too low. Saved recordings won't be lost.") }
        #endif
        return nil
    }
}
#endif
