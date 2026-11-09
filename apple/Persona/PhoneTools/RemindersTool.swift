import CoreLocation
import EventKit
import Foundation

/// Reminder tools: list, create, change, complete and delete; a reminder can go off on arriving at or leaving a place
/// (iOS watches the place itself). Writes ask the user first.
enum RemindersTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_reminders_list",
                    description: """
                    List reminders, by default only the incomplete ones. Returns id, title, due date \
                    (ISO 8601 with timezone; reminders with a date but no time are due at 00:00), \
                    completed flag, list name, priority (0 = none, 1–4 high, 5 medium, 6–9 low) \
                    the first 300 characters of notes, and `location` when it goes off at a place \
                    ({title, latitude, longitude, radius_meters, trigger: arrive|leave}). Use the id with \
                    phone_reminders_update, phone_reminders_complete and phone_reminders_delete.
                    """,
                    inputSchema: Schema.object([
                        "list": Schema.string("Only return reminders from this list (calendar) name."),
                        "include_completed": Schema.boolean("Include completed reminders. Default false."),
                        "limit": Schema.integer("Maximum number of reminders, default 50, max 200.", min: 1, max: 200),
                    ])
                ),
                source: .reminders,
                run: { args in try await list(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_reminders_create",
                    description: """
                    Create a reminder. `due` is optional ISO 8601 with a timezone \
                    (e.g. 2026-10-24T18:00:00+08:00). priority is 0 (none, the default), 1–4 high, \
                    5 medium or 6–9 low. `location` makes it go off when the user arrives at or leaves a \
                    place: iOS watches the place itself, nothing has to run. The user must approve the \
                    reminder on the phone. Returns the new reminder's id.
                    """,
                    inputSchema: Schema.object([
                        "title": Schema.string("Reminder title."),
                        "due": Schema.string("Optional due date, ISO 8601 with a timezone."),
                        "notes": Schema.string("Optional notes."),
                        "list": Schema.string("Reminder list name. Default: the default list for new reminders."),
                        "priority": Schema.integer("0 none (default), 1–4 high, 5 medium, 6–9 low.", min: 0, max: 9),
                        "location": PlaceTrigger.schema,
                    ], required: ["title"])
                ),
                source: .reminders,
                confirmation: { args in
                    let title = ToolArgs.string(args, "title") ?? String(localized: "(Untitled)")
                    var summary = String(localized: "New reminder “\(title)”")
                    if let due = ToolArgs.string(args, "due") { summary += String(localized: "\\nDue: \(due)") }
                    if let place = try? PlaceTrigger.parse(args["location"]) { summary += place.summary }
                    if let list = ToolArgs.string(args, "list") { summary += String(localized: "\\nList: \(list)") }
                    return summary
                },
                run: { args in try await create(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_reminders_complete",
                    description: """
                    Mark a reminder as completed. Pass the id returned by phone_reminders_list. \
                    The user must approve on the phone.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Reminder identifier from phone_reminders_list."),
                    ], required: ["id"])
                ),
                source: .reminders,
                confirmation: { args in completeConfirmation(args) },
                run: { args in try await complete(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_reminders_update",
                    description: """
                    Change a reminder: pass its id from phone_reminders_list and only the fields to change. \
                    `due: null` removes the due date, `notes` replaces the old notes (null removes them), \
                    `location: null` stops it going off at a place, and a new `location` replaces the old one. \
                    `list` moves it to another list. The user must approve on the phone.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Reminder identifier from phone_reminders_list."),
                        "title": Schema.string("New title."),
                        "due": Schema.string("New due date, ISO 8601 with a timezone; null removes it."),
                        "notes": Schema.string("New notes; they replace the old ones, null removes them."),
                        "priority": Schema.integer("0 none, 1–4 high, 5 medium, 6–9 low.", min: 0, max: 9),
                        "list": Schema.string("Move the reminder to the list (calendar) with this name."),
                        "location": PlaceTrigger.schema,
                    ], required: ["id"])
                ),
                source: .reminders,
                confirmation: { args in changeConfirmation(args) },
                run: { args in try await update(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_reminders_delete",
                    description: """
                    Delete a reminder the user no longer needs. Pass the id returned by \
                    phone_reminders_list. The user must approve on the phone.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Reminder identifier from phone_reminders_list."),
                    ], required: ["id"])
                ),
                source: .reminders,
                confirmation: { args in deleteConfirmation(args) },
                run: { args in try await delete(args) }
            ),
        ]
    }

    // MARK: - Listing

    /// A plain-value copy of the reminder fields we report. EventKit objects are
    /// tied to their store and are not Sendable, so the callback converts them
    /// before handing anything back.
    struct ReminderRow: Sendable {
        var id: String
        var title: String
        var due: DateComponents?
        var completed: Bool
        var completionDate: Date?
        var list: String
        var priority: Int
        var notes: String?
        var place: PlaceTrigger?
    }

    private static func fetch(_ store: EKEventStore, in lists: [EKCalendar]?) async -> [ReminderRow] {
        let predicate = store.predicateForReminders(in: lists)
        return await withCheckedContinuation { (continuation: CheckedContinuation<[ReminderRow], Never>) in
            // EventKit calls back on its own queue: `@Sendable` keeps the block off the main actor (BackgroundCallbackTests).
            store.fetchReminders(matching: predicate) { @Sendable reminders in
                let rows = (reminders ?? []).compactMap { reminder -> ReminderRow? in
                    guard let calendar = reminder.calendar else { return nil }
                    return ReminderRow(
                        id: reminder.calendarItemIdentifier,
                        title: reminder.title ?? "",
                        due: reminder.dueDateComponents,
                        completed: reminder.isCompleted,
                        completionDate: reminder.completionDate,
                        list: calendar.title,
                        priority: reminder.priority,
                        notes: reminder.notes,
                        place: PlaceTrigger(reminder)
                    )
                }
                continuation.resume(returning: rows)
            }
        }
    }

    static func list(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireReminders()
        let includeCompleted = ToolArgs.bool(args, "include_completed") ?? false
        let limit = ToolArgs.int(args, "limit", fallback: 50, min: 1, max: 200)

        let store = EKEventStore()
        var lists: [EKCalendar]?
        if let wanted = ToolArgs.string(args, "list")?.lowercased() {
            let all = store.calendars(for: .reminder)
            let matched = all.filter { $0.title.lowercased() == wanted || $0.title.lowercased().contains(wanted) }
            guard !matched.isEmpty else {
                let names = all.map(\.title).sorted().joined(separator: ", ")
                throw ToolFailure(.invalidArgument, "no reminder list named \"\(wanted)\"; available lists: \(names)")
            }
            lists = matched
        }

        let rows = await fetch(store, in: lists)
        var pending: [ReminderRow] = []
        var completed: [ReminderRow] = []
        for row in rows {
            if row.completed { completed.append(row) } else { pending.append(row) }
        }
        func sortKey(_ row: ReminderRow) -> Date {
            if let components = row.due, let date = Calendar.current.date(from: components) { return date }
            return Date.distantFuture
        }
        pending.sort { sortKey($0) < sortKey($1) }
        completed.sort { ($0.completionDate ?? .distantPast) > ($1.completionDate ?? .distantPast) }

        var chosen = pending
        if includeCompleted { chosen += completed }
        let truncated = chosen.count > limit
        chosen = Array(chosen.prefix(limit))

        return Output.json(Output.object([
            ("count", .number(Double(chosen.count))),
            ("pending", .number(Double(pending.count))),
            ("completed", .number(Double(completed.count))),
            ("truncated", .bool(truncated)),
            ("reminders", .array(chosen.map(encode))),
        ]))
    }

    // MARK: - Creating

    static func create(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireReminders()
        guard let title = ToolArgs.string(args, "title") else {
            throw ToolFailure(.invalidArgument, "title is required")
        }
        let store = EKEventStore()
        let reminder = EKReminder(eventStore: store)
        reminder.title = title
        if let notes = ToolArgs.string(args, "notes") { reminder.notes = notes }
        if let priority = ToolArgs.int(args, "priority"), (0...9).contains(priority) {
            reminder.priority = priority
        }
        if let due = try ToolDate.parse(ToolArgs.string(args, "due")) {
            let parts = Calendar.current.dateComponents([.year, .month, .day, .hour, .minute], from: due)
            reminder.dueDateComponents = parts
        }
        if let place = try PlaceTrigger.parse(args["location"]) { place.apply(to: reminder) }
        if let wanted = ToolArgs.string(args, "list")?.lowercased() {
            let lists = store.calendars(for: .reminder)
            guard let match = lists.first(where: { $0.title.lowercased() == wanted })
                ?? lists.first(where: { $0.title.lowercased().contains(wanted) }) else {
                let names = lists.map(\.title).sorted().joined(separator: ", ")
                throw ToolFailure(.invalidArgument, "no reminder list named \"\(wanted)\"; available lists: \(names)")
            }
            reminder.calendar = match
        } else if let fallback = store.defaultCalendarForNewReminders() {
            reminder.calendar = fallback
        }
        guard reminder.calendar != nil else {
            throw ToolFailure(.unavailable, "此 iPhone 上没有可写入的提醒事项列表 / No writable reminder list is configured") // i18n-ignore
        }
        try store.save(reminder, commit: true)
        return Output.json(encode(reminder))
    }

    /// Takes back a reminder the conversation added (its Undo, docs/assistant/unified-entry.md); false when it is gone already.
    static func remove(id: String) throws -> Bool {
        let store = EKEventStore()
        guard let item = reminder(id: id, in: store) else { return false }
        try store.remove(item, commit: true)
        return true
    }

    // MARK: - Completing

    /// The reminder an id points at, or nil when it is gone or reminders access is missing
    /// (EventKit answers nil in both cases).
    private static func reminder(id: String, in store: EKEventStore) -> EKReminder? {
        store.calendarItem(withIdentifier: id) as? EKReminder
    }

    /// What the approval card names for a reminder about to be completed.
    struct CompletionTarget: Equatable {
        var title: String
        var list: String?
        var due: String?
    }

    /// Approval text for phone_reminders_complete. It runs before the permission check,
    /// so it looks the reminder up itself and names it; an opaque id alone tells the
    /// user nothing about what they approve.
    static func completeConfirmation(_ args: JSONValue) -> String {
        let id = ToolArgs.string(args, "id")
        let target = id.flatMap { reminder(id: $0, in: EKEventStore()) }.map { item in
            CompletionTarget(title: item.title ?? "", list: item.calendar?.title, due: dueString(item.dueDateComponents))
        }
        return completeSummary(id: id, target: target)
    }

    /// Pure formatting half of `completeConfirmation`. Without a target the card keeps the id
    /// and says why nothing matched; the confirmation cannot skip the prompt, and `complete`
    /// then fails with invalidArgument, so the text does not promise anything about running.
    static func completeSummary(id: String?, target: CompletionTarget?) -> String {
        guard let target else {
            let shown = id ?? "?"
            return String(localized: "Can't find this reminder (or Persona has no access to Reminders): \(shown)")
        }
        let title = target.title.isEmpty ? String(localized: "(Untitled)") : target.title
        var summary = target.list.map { list in String(localized: "Mark “\(title)” (\(list)) as completed") }
            ?? String(localized: "Mark “\(title)” as completed")
        if let due = target.due { summary += String(localized: "\\nDue: \(due)") }
        return summary
    }

    static func complete(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireReminders()
        guard let id = ToolArgs.string(args, "id") else {
            throw ToolFailure(.invalidArgument, "id is required")
        }
        let store = EKEventStore()
        guard let item = reminder(id: id, in: store) else {
            throw ToolFailure(.invalidArgument, "no reminder with id \"\(id)\"; call phone_reminders_list to get a current id")
        }
        item.isCompleted = true
        item.completionDate = Date()
        try store.save(item, commit: true)
        return Output.json(encode(item))
    }

    // MARK: - Changing and deleting

    static func update(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireReminders()
        guard let id = ToolArgs.string(args, "id") else {
            throw ToolFailure(.invalidArgument, "id is required")
        }
        let place = try PlaceTrigger.parse(args["location"])
        let store = EKEventStore()
        guard let item = reminder(id: id, in: store) else {
            throw ToolFailure(.invalidArgument, "no reminder with id \"\(id)\"; call phone_reminders_list to get a current id")
        }
        if let title = ToolArgs.string(args, "title") { item.title = title }
        if args["notes"] == .null {
            item.notes = nil
        } else if let notes = ToolArgs.string(args, "notes") {
            item.notes = notes
        }
        if let priority = ToolArgs.int(args, "priority") {
            guard (0...9).contains(priority) else { throw ToolFailure(.invalidArgument, "priority must be 0–9") }
            item.priority = priority
        }
        if let wanted = ToolArgs.string(args, "list") {
            item.calendar = try reminderList(named: wanted, in: store)
        }
        if args["due"] == .null {
            item.dueDateComponents = nil
        } else if let due = try ToolDate.parse(ToolArgs.string(args, "due")) {
            item.dueDateComponents = Calendar.current.dateComponents([.year, .month, .day, .hour, .minute], from: due)
        }
        if args["location"] == .null {
            PlaceTrigger.clear(item)
        } else if let place {
            place.apply(to: item)
        }
        try store.save(item, commit: true)
        return Output.json(encode(item))
    }

    static func delete(_ args: JSONValue) async throws -> [ToolContent] {
        try CalendarAccess.requireReminders()
        guard let id = ToolArgs.string(args, "id") else {
            throw ToolFailure(.invalidArgument, "id is required")
        }
        guard try remove(id: id) else {
            throw ToolFailure(.invalidArgument, "no reminder with id \"\(id)\"; call phone_reminders_list to get a current id")
        }
        return Output.json(Output.object([("id", .string(id)), ("deleted", .bool(true))]))
    }

    /// The reminder a change or a deletion is about, looked up for its approval card like `completeConfirmation`.
    private static func target(_ args: JSONValue) -> (String?, CompletionTarget?) {
        let id = ToolArgs.string(args, "id")
        let target = id.flatMap { reminder(id: $0, in: EKEventStore()) }.map { item in
            CompletionTarget(title: item.title ?? "", list: item.calendar?.title, due: dueString(item.dueDateComponents))
        }
        return (id, target)
    }

    static func changeConfirmation(_ args: JSONValue) -> String {
        let (id, found) = target(args)
        return changeSummary(id: id, target: found, args: args)
    }

    static func deleteConfirmation(_ args: JSONValue) -> String {
        let (id, found) = target(args)
        return deleteSummary(id: id, target: found)
    }

    /// Pure half of `changeConfirmation`: what the card says will change.
    static func changeSummary(id: String?, target: CompletionTarget?, args: JSONValue) -> String {
        guard let target else { return completeSummary(id: id, target: nil) }
        let title = target.title.isEmpty ? String(localized: "(Untitled)") : target.title
        var summary = String(localized: "Change reminder “\(title)”")
        if let new = ToolArgs.string(args, "title") { summary += String(localized: "\\nNew title: \(new)") }
        // What else gets written shows too: replaced notes are gone after Allow (#1876, #2277).
        var more: [String] = []
        if args["notes"] == .null {
            more.append(String(localized: "Notes removed"))
        } else if let notes = ToolArgs.string(args, "notes") {
            let shown = String(notes.prefix(200))
            more.append(String(localized: "Notes replaced with: \(shown)"))
        }
        if let priority = ToolArgs.int(args, "priority") {
            let level = priorityName(priority)
            more.append(String(localized: "Priority: \(level)"))
        }
        if let list = ToolArgs.string(args, "list") { more.append(String(localized: "Move to list: \(list)")) }
        if !more.isEmpty { summary += "\n" + more.joined(separator: "\n") }
        if args["due"] == .null {
            summary += String(localized: "\\nNo due date")
        } else if let due = ToolArgs.string(args, "due") {
            summary += String(localized: "\\nDue: \(due)")
        }
        if args["location"] == .null {
            summary += String(localized: "\\nNo longer goes off at a place")
        } else if let place = try? PlaceTrigger.parse(args["location"]) {
            summary += place.summary
        }
        return summary
    }

    /// Pure half of `deleteConfirmation`.
    static func deleteSummary(id: String?, target: CompletionTarget?) -> String {
        guard let target else { return completeSummary(id: id, target: nil) }
        let title = target.title.isEmpty ? String(localized: "(Untitled)") : target.title
        var summary = target.list.map { list in String(localized: "Delete “\(title)” (\(list))") }
            ?? String(localized: "Delete “\(title)”")
        if let due = target.due { summary += String(localized: "\\nDue: \(due)") }
        return summary
    }

    /// The reminder list named `wanted`: an exact name first, then one containing it, as `create` picks.
    private static func reminderList(named wanted: String, in store: EKEventStore) throws -> EKCalendar {
        let lists = store.calendars(for: .reminder)
        let key = wanted.lowercased()
        guard let match = lists.first(where: { $0.title.lowercased() == key })
            ?? lists.first(where: { $0.title.lowercased().contains(key) }) else {
            let names = lists.map(\.title).sorted().joined(separator: ", ")
            throw ToolFailure(.invalidArgument, "no reminder list named \"\(wanted)\"; available lists: \(names)")
        }
        return match
    }

    /// EventKit's priority as the Reminders app names it.
    static func priorityName(_ priority: Int) -> String {
        switch priority {
        case 1...4: String(localized: "High [Reminder Priority]")
        case 5: String(localized: "Medium [Reminder Priority]")
        case 6...9: String(localized: "Low [Reminder Priority]")
        default: String(localized: "None [Reminder Priority]")
        }
    }

    static func encode(_ reminder: EKReminder) -> JSONValue {
        Output.object([
            ("id", .string(reminder.calendarItemIdentifier)),
            ("title", Output.opt(reminder.title)),
            ("due", Output.opt(dueString(reminder.dueDateComponents))),
            ("completed", .bool(reminder.isCompleted)),
            ("completed_at", Output.opt(reminder.completionDate.map(ToolDate.string))),
            ("list", Output.opt(reminder.calendar?.title)),
            ("priority", .number(Double(reminder.priority))),
            ("notes", Output.opt(reminder.notes.map { String($0.prefix(300)) })),
            ("location", PlaceTrigger(reminder)?.json),
        ])
    }

    static func encode(_ row: ReminderRow) -> JSONValue {
        Output.object([
            ("id", .string(row.id)),
            ("title", Output.opt(row.title)),
            ("due", Output.opt(dueString(row.due))),
            ("completed", .bool(row.completed)),
            ("completed_at", Output.opt(row.completionDate.map(ToolDate.string))),
            ("list", Output.opt(row.list)),
            ("priority", .number(Double(row.priority))),
            ("notes", Output.opt(row.notes.map { String($0.prefix(300)) })),
            ("location", row.place?.json),
        ])
    }

    private static func dueString(_ components: DateComponents?) -> String? {
        guard let components else { return nil }
        let calendar = Calendar.current
        if let date = calendar.date(from: components) { return ToolDate.string(date) }
        // A bare date with no time still deserves to be shown.
        guard let year = components.year, let month = components.month, let day = components.day else { return nil }
        return String(format: "%04d-%02d-%02d", year, month, day)
    }
}

/// A reminder that goes off on arriving at or leaving a place (`location` in the reminder tools). iOS watches the
/// circle itself through the reminder's alarm, so nothing in Persona runs in the background.
nonisolated struct PlaceTrigger: Equatable, Sendable {
    enum Kind: String, Sendable { case arrive, leave }

    var title: String
    var latitude: Double
    var longitude: Double
    var radius: Double
    var kind: Kind

    /// Small circles miss: a fix indoors is often 50–100 m off. Large ones go off far from the place.
    static let defaultRadius = 150.0
    static let radiusRange = 100.0...2000.0

    static var schema: JSONValue {
        .object([
            "type": .string("object"),
            "additionalProperties": .bool(false),
            "description": .string("""
            Go off when the user arrives at or leaves this place. Choose by what they have to do: on the way \
            (buy milk on the way home) is leaving where they set out from; once there (unpack at home) is arriving.
            """),
            "properties": .object([
                "title": Schema.string("The place's name the user knows, e.g. Home or Office."),
                "latitude": Schema.number("Latitude."),
                "longitude": Schema.number("Longitude."),
                "radius_meters": Schema.integer("Circle around the place, default 150, 100–2000.", min: 100, max: 2000),
                "trigger": Schema.string("arrive or leave.", values: ["arrive", "leave"]),
            ]),
            "required": .array(["title", "latitude", "longitude", "trigger"].map { .string($0) }),
        ])
    }

    /// nil when there is none (absent or null); throws when it is there but cannot be a place.
    static func parse(_ value: JSONValue?) throws -> PlaceTrigger? {
        guard let value, value != .null else { return nil }
        guard let title = ToolArgs.string(value, "title") else {
            throw ToolFailure(.invalidArgument, "location.title is required")
        }
        guard let latitude = value["latitude"]?.double, (-90...90).contains(latitude),
              let longitude = value["longitude"]?.double, (-180...180).contains(longitude) else {
            throw ToolFailure(.invalidArgument, "location needs latitude (-90…90) and longitude (-180…180)")
        }
        guard let kind = ToolArgs.string(value, "trigger").flatMap(Kind.init(rawValue:)) else {
            throw ToolFailure(.invalidArgument, "location.trigger must be arrive or leave")
        }
        let radius = value["radius_meters"]?.double ?? defaultRadius
        let clamped = Swift.min(Swift.max(radius, radiusRange.lowerBound), radiusRange.upperBound)
        return PlaceTrigger(title: title, latitude: latitude, longitude: longitude, radius: clamped, kind: kind)
    }

    /// The place a reminder goes off at, if any.
    init?(_ reminder: EKReminder) {
        guard let alarm = reminder.alarms?.first(where: { $0.structuredLocation != nil && $0.proximity != .none }),
              let place = alarm.structuredLocation, let fix = place.geoLocation else { return nil }
        self.init(title: place.title ?? "", latitude: fix.coordinate.latitude, longitude: fix.coordinate.longitude,
                  radius: place.radius > 0 ? place.radius : Self.defaultRadius,
                  kind: alarm.proximity == .enter ? .arrive : .leave)
    }

    init(title: String, latitude: Double, longitude: Double, radius: Double, kind: Kind) {
        self.title = title
        self.latitude = latitude
        self.longitude = longitude
        self.radius = radius
        self.kind = kind
    }

    /// Replaces the reminder's place alarm with this one; its time alarms stay.
    func apply(to reminder: EKReminder) {
        Self.clear(reminder)
        let place = EKStructuredLocation(title: title)
        place.geoLocation = CLLocation(latitude: latitude, longitude: longitude)
        place.radius = radius
        let alarm = EKAlarm()
        alarm.structuredLocation = place
        alarm.proximity = kind == .arrive ? .enter : .leave
        reminder.addAlarm(alarm)
    }

    static func clear(_ reminder: EKReminder) {
        for alarm in reminder.alarms ?? [] where alarm.structuredLocation != nil { reminder.removeAlarm(alarm) }
    }

    var json: JSONValue {
        Output.object([
            ("title", .string(title)),
            ("latitude", .number(latitude)),
            ("longitude", .number(longitude)),
            ("radius_meters", .number(radius.rounded())),
            ("trigger", .string(kind.rawValue)),
        ])
    }

    /// The approval card's line for it.
    var summary: String {
        switch kind {
        case .arrive: String(localized: "\\nWhen: arriving at “\(title)”")
        case .leave: String(localized: "\\nWhen: leaving “\(title)”")
        }
    }
}
