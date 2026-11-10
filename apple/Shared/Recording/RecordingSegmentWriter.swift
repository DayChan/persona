import AVFoundation
import os

/// Adapted from Hourhand: records the microphone into back-to-back AAC segments (16 kHz mono `.m4a`) with no gap between files.
///
/// The input tap converts to 16 kHz and hands buffers to a serial queue that writes the current file; when a file
/// reaches `RecordingAudioSpec.segmentSeconds` it is closed (finalized) and reported through `onSegment`, and the next buffer
/// opens the next file. Pausing stops the tap, so the timeline (`elapsed`, segment offsets, marks) never contains
/// the paused time. Used by both the watch and the iPhone (docs/apple/recording.md).
nonisolated final class RecordingSegmentWriter: @unchecked Sendable {
    struct Segment: Sendable {
        let index: Int
        let url: URL
        let start: Double
        let duration: Double
    }

    enum RecorderError: LocalizedError {
        case noInput
        var errorDescription: String? { "No microphone input is available." }
    }

    let directory: URL
    /// Called on the writer queue when a segment file is finalized.
    var onSegment: (@Sendable (Segment) -> Void)?

    private let engine = AVAudioEngine()
    private let queue = DispatchQueue(label: "persona.recording-writer", qos: .userInitiated)
    private let outFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: RecordingAudioSpec.sampleRate, channels: 1, interleaved: false)!

    // Writer state: only touched on `queue`.
    private var file: AVAudioFile?
    private var fileFrames: AVAudioFramePosition = 0
    private var index: Int
    private var segmentStart: Double

    // Shared with readers on other threads.
    private struct Shared {
        var frames: AVAudioFramePosition = 0
        var level: Float = 0
        var running = false
        var failure: String?
        var finished: [Segment] = []
        var segmentFrames = AVAudioFramePosition(RecordingAudioSpec.segmentSeconds * RecordingAudioSpec.sampleRate)
    }
    private let shared = OSAllocatedUnfairLock(initialState: Shared())
    private var converter: AVAudioConverter?

    /// - Parameters:
    ///   - firstIndex / startOffset: continue an existing recording (after an interruption the app couldn't resume).
    init(directory: URL, firstIndex: Int = 0, startOffset: Double = 0) {
        self.directory = directory
        self.index = firstIndex
        self.segmentStart = startOffset
        shared.withLock { $0.frames = AVAudioFramePosition(startOffset * RecordingAudioSpec.sampleRate) }
    }

    /// Recorded time on the recording timeline (pauses excluded), seconds.
    var elapsed: Double { Double(shared.withLock { $0.frames }) / RecordingAudioSpec.sampleRate }
    /// Recent input level, 0…1 (perceptual, for meters).
    var level: Float { shared.withLock { $0.level } }
    var failure: String? { shared.withLock { $0.failure } }
    var isRunning: Bool { shared.withLock { $0.running } }
    /// After a stop, take every finalized segment synchronously so callbacks the main thread has not handled yet do not leave segments out of the end message's count.
    var finishedSegments: [Segment] { shared.withLock { $0.finished } }

    /// Length of the segments being written, seconds. Lowered while someone is watching the text arrive (the iPhone app
    /// is open), so the next segment closes sooner; the current one closes as soon as it is longer than the new length.
    var segmentLength: Double {
        get { Double(shared.withLock { $0.segmentFrames }) / RecordingAudioSpec.sampleRate }
        set { shared.withLock { $0.segmentFrames = AVAudioFramePosition(max(10, newValue) * RecordingAudioSpec.sampleRate) } }
    }

    func start() throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try installTap()
        engine.prepare()
        do { try engine.start() } catch { engine.inputNode.removeTap(onBus: 0); throw error }
        shared.withLock { $0.running = true }
    }

    func pause() {
        engine.pause()
        shared.withLock { $0.running = false; $0.level = 0 }
    }

    func resume() throws {
        let input = engine.inputNode
        if input.numberOfInputs == 0 { throw RecorderError.noInput }
        if converter?.inputFormat != input.outputFormat(forBus: 0) {
            try restartAfterConfigurationChange()
            return
        }
        try engine.start()
        shared.withLock { $0.running = true }
    }

    /// Re-creates the tap after the input route or format changed (AVAudioEngineConfigurationChange), keeping the
    /// current segment open.
    func restartAfterConfigurationChange() throws {
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        try installTap()
        engine.prepare()
        try engine.start()
        shared.withLock { $0.running = true }
    }

    /// Closes the segment being written now, while paused (a phone call): an app killed before the recording resumes keeps
    /// it, rather than losing up to a whole segment. The next buffer opens the next one. Nil if nothing was written since.
    @discardableResult
    func cut() -> Segment? {
        queue.sync { closeFile() }
    }

    /// Whether a notification's object is this writer's engine: `.AVAudioEngineConfigurationChange` comes from every engine
    /// in the process (dictation's, a voice call's), and only this one's changes concern the recording.
    func owns(_ object: ObjectIdentifier?) -> Bool {
        object == ObjectIdentifier(engine)
    }

    /// Stops recording and finalizes the last segment; returns it (nil if nothing was written since the last one).
    @discardableResult
    func stop() -> Segment? {
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        shared.withLock { $0.running = false; $0.level = 0 }
        return queue.sync { closeFile() }
    }

    // MARK: - Tap

    private func installTap() throws {
        let input = engine.inputNode
        let inFormat = input.outputFormat(forBus: 0)
        guard inFormat.sampleRate > 0, inFormat.channelCount > 0 else { throw RecorderError.noInput }
        converter = AVAudioConverter(from: inFormat, to: outFormat)
        input.installTap(onBus: 0, bufferSize: 4096, format: inFormat) { [weak self] buffer, _ in
            self?.process(buffer)
        }
    }

    private func process(_ buffer: AVAudioPCMBuffer) {
        guard let converter, buffer.frameLength > 0 else { return }
        let ratio = outFormat.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 32
        guard let out = AVAudioPCMBuffer(pcmFormat: outFormat, frameCapacity: capacity) else { return }
        var consumed = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if consumed { status.pointee = .noDataNow; return nil }
            consumed = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, out.frameLength > 0 else { return }
        let level = Self.level(of: out)
        let frames = AVAudioFramePosition(out.frameLength)
        shared.withLock {
            $0.frames += frames
            $0.level = $0.level * 0.6 + level * 0.4
        }
        let box = BufferBox(buffer: out)
        queue.async { [weak self] in self?.write(box.buffer) }
    }

    private static func level(of buffer: AVAudioPCMBuffer) -> Float {
        guard let data = buffer.floatChannelData?[0] else { return 0 }
        let n = Int(buffer.frameLength)
        var sum: Float = 0
        for i in 0..<n { sum += data[i] * data[i] }
        let rms = (sum / Float(max(n, 1))).squareRoot()
        // -50 dB … 0 dB → 0 … 1
        let db = 20 * log10(max(rms, 0.000_01))
        return max(0, min(1, (db + 50) / 50))
    }

    // MARK: - Writer (queue)

    private func write(_ buffer: AVAudioPCMBuffer) {
        var offset: AVAudioFrameCount = 0
        while offset < buffer.frameLength {
            if file == nil { openFile() }
            guard file != nil else { return }
            let segmentFrames = shared.withLock { $0.segmentFrames }
            if fileFrames >= segmentFrames, let segment = closeFile() {
                // The segment length was lowered below what this file already holds.
                onSegment?(segment)
                continue
            }
            let room = AVAudioFrameCount(segmentFrames - fileFrames)
            let n = min(room, buffer.frameLength - offset)
            if let slice = Self.slice(buffer, from: offset, count: n) {
                do { try file?.write(from: slice) } catch { shared.withLock { $0.failure = String(localized: "Couldn't write the recording file") }; return }
            }
            fileFrames += AVAudioFramePosition(n)
            offset += n
            if fileFrames >= segmentFrames, let segment = closeFile() {
                onSegment?(segment)
            }
        }
    }

    private func openFile() {
        let url = directory.appendingPathComponent(RecordingSegment.fileName(index))
        try? FileManager.default.removeItem(at: url)
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: RecordingAudioSpec.sampleRate,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: RecordingAudioSpec.bitRate,
        ]
        do {
            file = try AVAudioFile(forWriting: url, settings: settings, commonFormat: .pcmFormatFloat32, interleaved: false)
            fileFrames = 0
        } catch {
            shared.withLock { $0.failure = String(localized: "Couldn't create the recording file") }
        }
    }

    /// Finalizes the open file. Returns nil when there was nothing in it.
    private func closeFile() -> Segment? {
        guard let url = file?.url else { return nil }
        // On watchOS 10 releasing the last writer reference finalizes the file. Do not retain it in write(_:)
        // across this call: onSegment must see the finalized AAC container.
        autoreleasepool {
            if #available(watchOS 11, *) { file?.close() }
            file = nil
        }
        guard fileFrames > 0 else {
            try? FileManager.default.removeItem(at: url)
            return nil
        }
        let duration = Double(fileFrames) / RecordingAudioSpec.sampleRate
        let segment = Segment(index: index, url: url, start: segmentStart, duration: duration)
        shared.withLock { $0.finished.append(segment) }
        index += 1
        segmentStart += duration
        fileFrames = 0
        return segment
    }

    private static func slice(_ buffer: AVAudioPCMBuffer, from start: AVAudioFrameCount, count: AVAudioFrameCount) -> AVAudioPCMBuffer? {
        if start == 0 && count == buffer.frameLength { return buffer }
        guard let out = AVAudioPCMBuffer(pcmFormat: buffer.format, frameCapacity: count),
              let src = buffer.floatChannelData?[0], let dst = out.floatChannelData?[0] else { return nil }
        dst.update(from: src.advanced(by: Int(start)), count: Int(count))
        out.frameLength = count
        return out
    }
}

/// Hands a freshly converted buffer (owned by nobody else) to the writer queue.
nonisolated private struct BufferBox: @unchecked Sendable { let buffer: AVAudioPCMBuffer }
