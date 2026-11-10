#if os(iOS)
import EventKit
import Foundation

/// The phone calendar's event happening now, which names a meeting recording (docs/apple/recording.md「Meeting
/// recordings」). It reads only once full access is granted: starting a recording never waits on a prompt (the app asks
/// once, after a meeting recording it started has begun, `MeetingRecorder`).
nonisolated enum MeetingCalendar {
    /// One event as `pick` sees it (EventKit's objects can't be made in a test).
    struct Candidate: Equatable, Sendable {
        var id: String?
        var title: String
        var start: Date
        var end: Date
        var allDay = false
        var declined = false
        var location: String?
        var attendees: [String] = []
    }

    static var canRead: Bool { EKEventStore.authorizationStatus(for: .event) == .fullAccess }
    /// Never asked yet: the app may ask (write-only and denied are the user's answer).
    static var canAsk: Bool { EKEventStore.authorizationStatus(for: .event) == .notDetermined }

    /// Asks for full access to events. True when granted.
    static func requestAccess() async -> Bool {
        (try? await EKEventStore().requestFullAccessToEvents()) ?? false
    }

    /// The meeting on at `date`; nil without access, or with nothing on but all-day events.
    static func meeting(at date: Date = .now) -> RecordingMeeting? {
        guard canRead else { return nil }
        let store = EKEventStore()
        let predicate = store.predicateForEvents(withStart: date.addingTimeInterval(-24 * 3600), end: date.addingTimeInterval(lead + 60),
                                                 calendars: nil)
        // A moved occurrence of a repeating meeting (`isDetached`) is the real one that day: kept.
        return pick(store.events(matching: predicate).map(Candidate.init), at: date).map(RecordingMeeting.init)
    }

    /// `meeting(at:)` off the main thread: EventKit's reads are synchronous calls to its daemon.
    static func meetingOffMain(at date: Date = .now) async -> RecordingMeeting? {
        await Task.detached { meeting(at: date) }.value
    }

    /// How early a meeting counts as on: people sit down and start recording before the hour.
    static let lead: TimeInterval = 5 * 60

    /// Of the events on at `date` (all-day, declined and untitled ones aside, and those starting within `lead`), the one
    /// that started last: back-to-back meetings overlap at their ends, and the one just begun is the one in the room.
    static func pick(_ events: [Candidate], at date: Date) -> Candidate? {
        events.filter { e in
            !e.allDay && !e.declined && !e.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                && e.start.addingTimeInterval(-lead) <= date && date < e.end
        }
        .max { a, b in a.start == b.start ? a.end > b.end : a.start < b.start }
    }
}

extension MeetingCalendar.Candidate {
    nonisolated init(_ event: EKEvent) {
        let external = event.calendarItemExternalIdentifier
        id = (external?.isEmpty == false ? external : nil) ?? event.eventIdentifier
        title = (event.title ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        start = event.startDate
        end = event.endDate
        allDay = event.isAllDay
        declined = event.attendees?.contains { $0.isCurrentUser && $0.participantStatus == .declined } ?? false
        let place = event.location?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        location = place.isEmpty ? nil : place
        attendees = MeetingCalendar.names(organizer: event.organizer?.name, attendees: (event.attendees ?? []).map(\.name))
    }
}

extension MeetingCalendar {
    /// Who was invited, by name: the organizer first, each once, at most 50.
    nonisolated static func names(organizer: String?, attendees: [String?]) -> [String] {
        var seen = Set<String>()
        return ([organizer] + attendees).compactMap { name -> String? in
            guard let name = name?.trimmingCharacters(in: .whitespacesAndNewlines), !name.isEmpty, seen.insert(name).inserted else { return nil }
            return name
        }
        .prefix(50).map { $0 }
    }
}

extension RecordingMeeting {
    nonisolated init(_ event: MeetingCalendar.Candidate) {
        self.init(eventID: event.id, title: event.title, start: event.start, end: event.end, location: event.location, attendees: event.attendees)
    }
}
#endif
