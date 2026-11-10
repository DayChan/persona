import Foundation

/// Segments describe themselves, so the watch's uploads can arrive out of order. The format comes from Hourhand's WatchProtocol.
nonisolated struct RecordingSegment: Codable, Hashable, Sendable {
    var index: Int
    var start: Double
    var duration: Double
    var sentences: [RecordingSentence]? = nil
    var delivered = false

    static func fileName(_ index: Int) -> String { String(format: "seg-%03d.m4a", index) }
}

nonisolated struct RecordingSentence: Codable, Hashable, Sendable {
    var start: Double
    var end: Double
    var text: String
}

/// What makes a recording a meeting's (docs/apple/recording.md「Meeting recordings」): started as one, and the phone's calendar
/// event it happened during, when there was one. The event's own fields are a copy from the time it started; nothing here
/// follows later calendar edits.
nonisolated struct RecordingMeeting: Codable, Hashable, Sendable {
    /// The calendar event (`EKEvent.calendarItemExternalIdentifier`, the same on every device), nil without one.
    var eventID: String?
    var title: String?
    var start: Date?
    var end: Date?
    var location: String?
    /// Who was invited, by display name (the organizer first); empty without an event or attendees.
    var attendees: [String] = []

    init(eventID: String? = nil, title: String? = nil, start: Date? = nil, end: Date? = nil, location: String? = nil, attendees: [String] = []) {
        self.eventID = eventID; self.title = title; self.start = start; self.end = end; self.location = location; self.attendees = attendees
    }

    enum CodingKeys: String, CodingKey { case eventID, title, start, end, location, attendees }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        eventID = try c.decodeIfPresent(String.self, forKey: .eventID)
        title = try c.decodeIfPresent(String.self, forKey: .title)
        start = try c.decodeIfPresent(Date.self, forKey: .start)
        end = try c.decodeIfPresent(Date.self, forKey: .end)
        location = try c.decodeIfPresent(String.self, forKey: .location)
        attendees = try c.decodeIfPresent([String].self, forKey: .attendees) ?? []
    }
}

nonisolated struct RecordingEntry: Codable, Identifiable, Hashable, Sendable {
    enum Source: String, Codable, Sendable { case phone, watch }
    var id: UUID
    var createdAt: Date
    var source: Source
    var title = ""
    var locale = "zh-CN"
    var active = true
    var expectedSegments: Int? = nil
    var segments: [RecordingSegment] = []
    var marks: [Double] = []
    var endDelivered = false
    /// Last local change: the knowledge-base revision (ms) the phone uploads it as (docs/apple/recording.md「Synchronization」).
    var updatedAt: Date? = nil
    /// Set when it was started as a meeting recording (or a watch recording arrived during a calendar event); nil for a note.
    var meeting: RecordingMeeting? = nil

    /// Monotonic per recording: every save moves `updatedAt` forward, so a later upload always carries a larger one.
    var revision: Int64 { Int64(((updatedAt ?? createdAt).timeIntervalSince1970 * 1000).rounded()) }

    var duration: Double { segments.map { $0.start + $0.duration }.max() ?? 0 }
    var isMeeting: Bool { meeting != nil }
    var displayTitle: String {
        guard title.isEmpty else { return title }
        let when = createdAt.formatted(date: .abbreviated, time: .shortened)
        return isMeeting ? String(localized: "Meeting · \(when)") : String(localized: "Recording · \(when)")
    }
    var sourceTitle: String { source == .watch ? "Apple Watch" : String(localized: "iPhone") }
    var receivedAll: Bool {
        guard !active, let count = expectedSegments, count > 0 else { return false }
        return Set(segments.map(\.index)) == Set(0..<count)
    }
    var transcribedAll: Bool { receivedAll && segments.allSatisfy { $0.sentences != nil } }
    var sentences: [RecordingSentence] {
        segments.sorted { $0.index < $1.index }.flatMap { $0.sentences ?? [] }
    }
    var transcript: String { sentences.map { "[\(RecordingFormat.clock($0.start))] \($0.text)" }.joined(separator: "\n") }
}

nonisolated struct RecordingTransfer: Codable, Sendable {
    var id: UUID
    var createdAt: Date
    var locale: String
    var segment: RecordingSegment?
    var total: Int?
    var marks: [Double]

    static let key = "personaRecording"
    var dictionary: [String: Any] {
        let data = (try? JSONEncoder().encode(self)) ?? Data()
        return [Self.key: data, LegacyNames.recordingKey: data]
    }
    init(entry: RecordingEntry, segment: RecordingSegment? = nil) {
        id = entry.id; createdAt = entry.createdAt; locale = entry.locale
        self.segment = segment; total = segment == nil ? entry.expectedSegments : nil; marks = entry.marks
    }
    init?(dictionary: [String: Any]?) {
        guard let data = (dictionary?[Self.key] ?? dictionary?[LegacyNames.recordingKey]) as? Data,
              let value = try? JSONDecoder().decode(Self.self, from: data), value.valid else { return nil }
        self = value
    }
    var valid: Bool {
        guard marks.allSatisfy({ $0.isFinite && $0 >= 0 }), marks.count <= 10_000,
              locale.count <= 100, createdAt.timeIntervalSince1970.isFinite else { return false }
        if let segment {
            return segment.index >= 0 && segment.index < 100_000 && segment.start.isFinite && segment.start >= 0
                && segment.duration.isFinite && segment.duration > 0 && segment.duration <= 600 && total == nil
        }
        return total.map { $0 >= 0 && $0 <= 100_000 } ?? false
    }
}

/// What every way of starting a recording on a phone shares (docs/apple/recording.md).
nonisolated enum RecordingDefaults {
    /// The language a recording starts in: the Recordings page's picker remembers it, Siri and the Control follow it.
    static let localeKey = "recording.locale"
    /// Chinese until chosen (the on-device Chinese model also writes the English words said among it).
    static let locale = "zh-CN"
}

nonisolated enum RecordingAudioSpec {
    static let sampleRate: Double = 16_000
    static let bitRate = 32_000
    static let segmentSeconds: Double = 60
    static let liveSegmentSeconds: Double = 30
    static let transferBatchSeconds: Double = 180
    static let lowPowerTransferBatchSeconds: Double = 300
}

nonisolated enum RecordingFormat {
    static func clock(_ seconds: Double) -> String {
        guard seconds.isFinite, seconds >= 0, seconds < Double(Int.max) else { return "0:00" }
        let value = Int(seconds)
        return value >= 3600 ? String(format: "%d:%02d:%02d", value / 3600, value / 60 % 60, value % 60)
            : String(format: "%d:%02d", value / 60, value % 60)
    }
}
