import EventKit
import Foundation

/// Calendar tools: read a time window, and create an event after the user
/// confirms on the phone.
enum CalendarTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_calendar_events",
                    description: """
                    List calendar events in a time window. Defaults to the next 7 days starting \
                    at 00:00 today. Times are ISO 8601 with a timezone offset \
                    (e.g. 2026-10-24T14:00:00+08:00). Returns each event's id, title, start, end, \
                    all-day flag, calendar name, location, the first 300 characters of notes and \
                    the attendees. Use it to check whether the user is free.
                    """,
                    inputSchema: Schema.object([
                        "start": Schema.string("Window start, ISO 8601 (e.g. 2026-10-24T00:00:00+08:00). Default: 00:00 today."),
                        "end": Schema.string("Window end, ISO 8601. Default: 7 days after start."),
                        "query": Schema.string("Only return events whose title, location or notes contain this text."),
                        "limit": Schema.integer("Maximum number of events to return, default 50, max 200.", min: 1, max: 200),
                    ])
                ),
                source: .calendar,
                run: { args in try await list(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_calendar_create_event",
                    description: """
                    Create a calendar event. `start` and `end` are ISO 8601 with a timezone \
                    (e.g. 2026-10-24T14:00:00+08:00). For an all-day event pass all_day=true and \
                    dates at 00:00. The user must approve the event on the phone before it is saved. \
                    Returns the new event's identifier.
                    """,
                    inputSchema: Schema.object([
                        "title": Schema.string("Event title."),
                        "start": Schema.string("Start time, ISO 8601 with a timezone."),
                        "end": Schema.string("End time, ISO 8601 with a timezone. Must be after start."),
                        "location": Schema.string("Optional location."),
                        "notes": Schema.string("Optional notes."),
                        "all_day": Schema.boolean("Mark the event as all-day. Default false."),
                        "calendar": Schema.string("Calendar name (title) to put the event in. Default: the default calendar for new events."),
                    ], required: ["title", "start", "end"])
                ),
                source: .calendar,
                confirmation: { args in
                    let title = ToolArgs.string(args, "title") ?? "(untitled)"
                    let start = ToolArgs.string(args, "start") ?? "?"
                    let end = ToolArgs.string(args, "end") ?? "?"
                    let location = ToolArgs.string(args, "location")
                    var summary = String(localized: "New event “\(title)”\\n\(start) → \(end)")
                    if ToolArgs.bool(args, "all_day") == true { summary += String(localized: " (all day)") }
                    if let location { summary += String(localized: "\\nLocation: \(location)") }
                    return summary
                },
                run: { args in try await create(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_calendar_update_event",
                    description: """
                    Change or reschedule a calendar event: pass its id from phone_calendar_events and only the \
                    fields to change. For a repeating event (recurring=true) also pass occurrence_start, the \
                    occurrence's start as phone_calendar_events returned it, and span: "this" (default) changes \
                    only that occurrence, "future" it and every later one. A new start without an end keeps the \
                    duration. The user sees before and after on the phone and must approve.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Event id from phone_calendar_events."),
                        "occurrence_start": Schema.string("For a repeating event: the start of the occurrence to change, ISO 8601, as phone_calendar_events returned it."),
                        "span": Schema.string("this (default): only this occurrence; future: this and all later occurrences.", values: ["this", "future"]),
                        "title": Schema.string("New title."),
                        "start": Schema.string("New start, ISO 8601 with a timezone."),
                        "end": Schema.string("New end, ISO 8601 with a timezone."),
                        "location": Schema.string("New location; an empty string removes it."),
                        "notes": Schema.string("New notes; they replace the old notes, an empty string removes them."),
                        "all_day": Schema.boolean("Make the event all-day or not."),
                    ], required: ["id"])
                ),
                source: .calendar,
                confirmation: { args in changeConfirmation(args) },
                run: { args in try await update(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_calendar_delete_event",
                    description: """
                    Delete (cancel) a calendar event: pass its id from phone_calendar_events. For a repeating event \
                    also pass occurrence_start; span "this" (default) deletes only that occurrence, "future" it and \
                    every later one. The user must approve on the phone.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Event id from phone_calendar_events."),
                        "occurrence_start": Schema.string("For a repeating event: the start of the occurrence to delete, ISO 8601, as phone_calendar_events returned it."),
                        "span": Schema.string("this (default): only this occurrence; future: this and all later occurrences.", values: ["this", "future"]),
                    ], required: ["id"])
                ),
                source: .calendar,
                confirmation: { args in deleteConfirmation(args) },
                run: { args in try await delete(args) }
            ),
        ]
    }

    // MARK: - Listing

    static func list(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireEvents()
        let calendar = Calendar.current
        let now = Date()
        let today = calendar.startOfDay(for: now)
        let start = try ToolDate.parse(ToolArgs.string(args, "start")) ?? today
        let end = try ToolDate.parse(ToolArgs.string(args, "end")) ?? calendar.date(byAdding: .day, value: 7, to: start) ?? today
        guard end > start else {
            throw ToolFailure(.invalidArgument, "end must be after start")
        }
        let limit = ToolArgs.int(args, "limit", fallback: 50, min: 1, max: 200)
        let query = ToolArgs.string(args, "query")?.lowercased()

        let store = EKEventStore()
        let predicate = store.predicateForEvents(withStart: start, end: end, calendars: nil)
        var events = store.events(matching: predicate).filter { !$0.isDetached }
        events.sort { $0.startDate < $1.startDate }

        let (picked, truncated) = Self.firstMatches(events, limit: limit) { event in
            query.map { Self.matches(event, query: $0) } ?? true
        }
        let rows = picked.map(Self.encode)

        return Output.json(.object([
            "start": .string(ToolDate.string(start)),
            "end": .string(ToolDate.string(end)),
            "count": .number(Double(rows.count)),
            "truncated": .bool(truncated),
            "events": .array(rows),
        ]))
    }

    /// The first `limit` items that pass `include`. `truncated` is true only when
    /// another matching item exists past them, so items the filter rejects never count.
    static func firstMatches<T>(_ items: [T], limit: Int, where include: (T) -> Bool) -> (kept: [T], truncated: Bool) {
        var kept: [T] = []
        for item in items where include(item) {
            guard kept.count < limit else { return (kept, true) }
            kept.append(item)
        }
        return (kept, false)
    }

    private static func matches(_ event: EKEvent, query: String) -> Bool {
        for value in [event.title, event.location, event.notes] {
            if let value, value.lowercased().contains(query) { return true }
        }
        return false
    }

    /// Takes back an event the conversation added (its Undo, docs/assistant/unified-entry.md); false when it is gone already.
    static func remove(id: String) throws -> Bool {
        let store = EKEventStore()
        guard let event = store.event(withIdentifier: id) else { return false }
        try store.remove(event, span: .thisEvent, commit: true)
        return true
    }

    static func encode(_ event: EKEvent) -> JSONValue {
        let attendees: [JSONValue] = (event.attendees ?? []).map { attendee in
            Output.object([
                ("name", Output.opt(attendee.name)),
                ("role", .string(Self.roleName(attendee.participantRole))),
                ("status", .string(Self.statusName(attendee.participantStatus))),
                ("is_current_user", .bool(attendee.isCurrentUser)),
            ])
        }
        let notes = event.notes.map { String($0.prefix(300)) }
        return Output.object([
            ("id", .string(event.eventIdentifier ?? "")),
            ("title", Output.opt(event.title)),
            ("start", .string(ToolDate.string(event.startDate))),
            ("end", .string(ToolDate.string(event.endDate))),
            ("all_day", .bool(event.isAllDay)),
            ("calendar", Output.opt(event.calendar?.title)),
            ("location", Output.opt(event.location)),
            ("notes", Output.opt(notes)),
            ("attendees", attendees.isEmpty ? nil : .array(attendees)),
            ("recurring", event.hasRecurrenceRules ? .bool(true) : nil),
        ])
    }

    private static func roleName(_ role: EKParticipantRole) -> String {
        switch role {
        case .required: "required"
        case .optional: "optional"
        case .chair: "chair"
        case .nonParticipant: "non_participant"
        default: "unknown"
        }
    }

    private static func statusName(_ status: EKParticipantStatus) -> String {
        switch status {
        case .accepted: "accepted"
        case .declined: "declined"
        case .tentative: "tentative"
        case .pending: "pending"
        case .delegated: "delegated"
        case .completed: "completed"
        case .inProcess: "in_process"
        default: "unknown"
        }
    }

    // MARK: - Creating

    static func create(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireEvents()
        guard let title = ToolArgs.string(args, "title") else {
            throw ToolFailure(.invalidArgument, "title is required")
        }
        guard let start = try ToolDate.parse(ToolArgs.string(args, "start")) else {
            throw ToolFailure(.invalidArgument, "start is required, as ISO 8601 with a timezone")
        }
        guard let end = try ToolDate.parse(ToolArgs.string(args, "end")) else {
            throw ToolFailure(.invalidArgument, "end is required, as ISO 8601 with a timezone")
        }
        guard end > start else {
            throw ToolFailure(.invalidArgument, "end must be after start")
        }

        let store = EKEventStore()
        let event = EKEvent(eventStore: store)
        event.title = title
        event.startDate = start
        event.endDate = end
        event.isAllDay = ToolArgs.bool(args, "all_day") ?? false
        if let location = ToolArgs.string(args, "location") { event.location = location }
        if let notes = ToolArgs.string(args, "notes") { event.notes = notes }

        if let wanted = ToolArgs.string(args, "calendar")?.lowercased() {
            let calendars = store.calendars(for: .event)
            guard let match = calendars.first(where: { ($0.title.lowercased()) == wanted })
                ?? calendars.first(where: { $0.title.lowercased().contains(wanted) }) else {
                let names = calendars.map(\.title).sorted().joined(separator: ", ")
                throw ToolFailure(.invalidArgument, "no calendar named \"\(wanted)\"; available calendars: \(names)")
            }
            event.calendar = match
        } else if let fallback = store.defaultCalendarForNewEvents {
            event.calendar = fallback
        }
        guard event.calendar != nil else {
            throw ToolFailure(.unavailable, "此 iPhone 上没有可写入的日历 / No writable calendar is configured on this iPhone") // i18n-ignore
        }

        try store.save(event, span: .thisEvent, commit: true)
        return Output.json(Output.object([
            ("id", .string(event.eventIdentifier ?? "")),
            ("title", .string(title)),
            ("start", .string(ToolDate.string(start))),
            ("end", .string(ToolDate.string(end))),
            ("calendar", Output.opt(event.calendar?.title)),
        ]))
    }

    // MARK: - Changing and deleting (#2276)

    /// The event (one occurrence of a repeating one) a change or a deletion names. A repeating event without
    /// `occurrence_start` is refused: its id alone names every occurrence, and guessing one moves the wrong meeting.
    static func target(_ args: JSONValue, in store: EKEventStore) throws -> EKEvent {
        guard let id = ToolArgs.string(args, "id") else {
            throw ToolFailure(.invalidArgument, "id is required")
        }
        let occurrence = try ToolDate.parse(ToolArgs.string(args, "occurrence_start"))
        if let occurrence {
            let window = store.predicateForEvents(withStart: occurrence.addingTimeInterval(-86_400),
                                                  end: occurrence.addingTimeInterval(86_400), calendars: nil)
            let match = store.events(matching: window).first {
                $0.eventIdentifier == id && abs($0.startDate.timeIntervalSince(occurrence)) < 60
            }
            guard let match else {
                throw ToolFailure(.invalidArgument, "no occurrence of event \"\(id)\" starts at \(ToolDate.string(occurrence)); "
                    + "call phone_calendar_events to get current ids and starts")
            }
            return match
        }
        guard let event = store.event(withIdentifier: id) else {
            throw ToolFailure(.invalidArgument, "no event with id \"\(id)\"; call phone_calendar_events to get a current id")
        }
        if event.hasRecurrenceRules {
            throw ToolFailure(.invalidArgument, "\"\(event.title ?? id)\" repeats: pass occurrence_start (the start of the "
                + "occurrence from phone_calendar_events) and span this or future")
        }
        return event
    }

    static func parseSpan(_ args: JSONValue) throws -> EKSpan {
        switch ToolArgs.string(args, "span") {
        case nil, "this"?: .thisEvent
        case "future"?: .futureEvents
        case let other?: throw ToolFailure(.invalidArgument, "span must be this or future, not \"\(other)\"")
        }
    }

    /// What a change does to an event, worked out before anything is written: the card shows it and `update` writes it.
    nonisolated struct EventChange: Equatable, Sendable {
        var title: String?
        var start: Date?
        var end: Date?
        var location: String?
        var notes: String?
        var allDay: Bool?

        static func parse(_ args: JSONValue, currentStart: Date, currentEnd: Date) throws -> EventChange {
            var change = EventChange()
            change.title = ToolArgs.string(args, "title")
            change.location = args["location"]?.string.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            change.notes = args["notes"]?.string
            change.allDay = ToolArgs.bool(args, "all_day")
            let start = try ToolDate.parse(ToolArgs.string(args, "start"))
            let end = try ToolDate.parse(ToolArgs.string(args, "end"))
            if start != nil || end != nil {
                let newStart = start ?? currentStart
                // A new start alone keeps the event as long as it was.
                let newEnd = end ?? newStart.addingTimeInterval(currentEnd.timeIntervalSince(currentStart))
                guard newEnd > newStart else { throw ToolFailure(.invalidArgument, "end must be after start") }
                change.start = newStart
                change.end = newEnd
            }
            guard change != EventChange() else {
                throw ToolFailure(.invalidArgument, "nothing to change: pass title, start, end, location, notes or all_day")
            }
            return change
        }
    }

    /// The event as the card names it before the change.
    nonisolated struct EventTarget: Equatable, Sendable {
        var title: String
        var start: Date
        var end: Date
        var allDay: Bool
        var calendar: String?
        var location: String?
        var recurring: Bool
    }

    static func eventTarget(_ event: EKEvent) -> EventTarget {
        EventTarget(title: event.title ?? "", start: event.startDate, end: event.endDate, allDay: event.isAllDay,
                    calendar: event.calendar?.title, location: event.location, recurring: event.hasRecurrenceRules)
    }

    static func update(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireEvents()
        let store = EKEventStore()
        let event = try target(args, in: store)
        let span = try parseSpan(args)
        let change = try EventChange.parse(args, currentStart: event.startDate, currentEnd: event.endDate)
        if let title = change.title { event.title = title }
        if let location = change.location { event.location = location.isEmpty ? nil : location }
        if let notes = change.notes { event.notes = notes.isEmpty ? nil : notes }
        if let allDay = change.allDay { event.isAllDay = allDay }
        if let start = change.start, let end = change.end {
            event.startDate = start
            event.endDate = end
        }
        try store.save(event, span: span, commit: true)
        var row = encode(event)
        if case .object(var fields) = row {
            fields["span"] = .string(span == .futureEvents ? "future" : "this")
            row = .object(fields)
        }
        return Output.json(row)
    }

    static func delete(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireEvents()
        let store = EKEventStore()
        let event = try target(args, in: store)
        let span = try parseSpan(args)
        let id = event.eventIdentifier ?? ""
        let title = event.title ?? ""
        let start = event.startDate ?? Date()
        try store.remove(event, span: span, commit: true)
        return Output.json(Output.object([
            ("id", .string(id)),
            ("title", Output.opt(title)),
            ("start", .string(ToolDate.string(start))),
            ("span", .string(span == .futureEvents ? "future" : "this")),
            ("deleted", .bool(true)),
        ]))
    }

    static func changeConfirmation(_ args: JSONValue) -> String {
        guard CalendarAccess.status().isUsable, let event = try? target(args, in: EKEventStore()) else {
            return missingSummary(args)
        }
        let found = eventTarget(event)
        let change = try? EventChange.parse(args, currentStart: found.start, currentEnd: found.end)
        return changeSummary(found, change: change, future: (try? parseSpan(args)) == .futureEvents)
    }

    static func deleteConfirmation(_ args: JSONValue) -> String {
        guard CalendarAccess.status().isUsable, let event = try? target(args, in: EKEventStore()) else {
            return missingSummary(args)
        }
        return deleteSummary(eventTarget(event), future: (try? parseSpan(args)) == .futureEvents)
    }

    /// The card when the event cannot be found: the tool then fails without writing anything.
    static func missingSummary(_ args: JSONValue) -> String {
        let id = ToolArgs.string(args, "id") ?? "?"
        return String(localized: "Can't find this event (or Persona has no access to Calendar): \(id)")
    }

    /// Pure half of `changeConfirmation`: the event, then each field before → after.
    static func changeSummary(_ target: EventTarget, change: EventChange?, future: Bool) -> String {
        let title = target.title.isEmpty ? String(localized: "(Untitled)") : target.title
        var lines = [String(localized: "Change event “\(title)”")]
        if let calendar = target.calendar { lines.append(String(localized: "Calendar: \(calendar)")) }
        guard let change else {
            lines.append(String(localized: "Invalid parameters; nothing will be changed."))
            return lines.joined(separator: "\n")
        }
        let allDay = change.allDay ?? target.allDay
        if let start = change.start, let end = change.end {
            let before = CardTime.range(target.start, target.end, allDay: target.allDay)
            let after = CardTime.range(start, end, allDay: allDay)
            lines.append(String(localized: "Time: \(before) → \(after)"))
        } else if change.allDay != nil {
            lines.append(allDay ? String(localized: "Becomes an all-day event") : String(localized: "No longer all-day"))
        }
        if let new = change.title { lines.append(String(localized: "Title: \(title) → \(new)")) }
        if let new = change.location {
            lines.append(new.isEmpty ? String(localized: "Location removed") : String(localized: "Location: \(new)"))
        }
        if let notes = change.notes {
            let shown = String(notes.prefix(200))
            lines.append(notes.isEmpty ? String(localized: "Notes removed") : String(localized: "Notes replaced with: \(shown)"))
        }
        if target.recurring {
            lines.append(future ? String(localized: "This and all future occurrences") : String(localized: "Only this occurrence"))
        }
        return lines.joined(separator: "\n")
    }

    /// Pure half of `deleteConfirmation`.
    static func deleteSummary(_ target: EventTarget, future: Bool) -> String {
        let title = target.title.isEmpty ? String(localized: "(Untitled)") : target.title
        var lines = [String(localized: "Delete event “\(title)”"),
                     CardTime.range(target.start, target.end, allDay: target.allDay)]
        if let calendar = target.calendar { lines.append(String(localized: "Calendar: \(calendar)")) }
        if target.recurring {
            lines.append(future ? String(localized: "This and all future occurrences") : String(localized: "Only this occurrence"))
        }
        return lines.joined(separator: "\n")
    }
}
