import Foundation
#if os(iOS) && canImport(AlarmKit)
import AlarmKit
import SwiftUI
#endif

/// Alarms and timers through AlarmKit (#2278): they ring like the Clock app's, through silent mode and Focus. Persona
/// only sees and changes the ones it set itself; the Clock app's own alarms are out of reach.
enum AlarmsTool {
    static var tools: [PhoneTool] {
        #if os(iOS) && canImport(AlarmKit)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarms_list",
                    description: """
                    List the alarms and timers Persona set on the iPhone, with id, label, state (scheduled, \
                    countdown, paused, alerting), time or repeat days, and a timer's length. Alarms made in the \
                    Clock app are not visible to Persona and cannot be changed.
                    """,
                    inputSchema: Schema.object([:])
                ),
                source: .alarms,
                run: { _ in try list() }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_create",
                    description: """
                    Set an alarm that rings like the Clock app's, even in silent mode or Focus. `time` is either \
                    HH:mm (24-hour, the next time it comes, or on the `repeat` days) or an ISO 8601 date and time \
                    with a timezone for one exact moment. The user must approve on the phone. Returns the alarm's id.
                    """,
                    inputSchema: Schema.object([
                        "time": Schema.string("HH:mm (24-hour), or ISO 8601 date-time with a timezone."),
                        "repeat": Schema.array("Days it repeats on, with HH:mm: mon, tue, wed, thu, fri, sat, sun. Default: once.",
                                               of: Schema.string("A weekday.", values: AlarmRequest.weekdays)),
                        "label": Schema.string("What the alarm is for, shown when it rings."),
                    ], required: ["time"])
                ),
                source: .alarms,
                confirmation: { args in alarmConfirmation(args) },
                run: { args in try await createAlarm(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_timer_start",
                    description: """
                    Start a countdown timer that rings when it ends, even in silent mode or Focus. The user must \
                    approve on the phone. Returns the timer's id (pause, resume and cancel it with that id).
                    """,
                    inputSchema: Schema.object([
                        "seconds": Schema.integer("Length in seconds, 1–86400.", min: 1, max: 86400),
                        "label": Schema.string("What the timer is for."),
                    ], required: ["seconds"])
                ),
                source: .alarms,
                confirmation: { args in timerConfirmation(args) },
                run: { args in try await startTimer(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_cancel",
                    description: """
                    Cancel (delete) an alarm or timer Persona set: pass its id from phone_alarms_list. The user must \
                    approve on the phone.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Alarm or timer id from phone_alarms_list."),
                    ], required: ["id"])
                ),
                source: .alarms,
                confirmation: { args in cancelConfirmation(args) },
                run: { args in try cancel(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_pause",
                    description: """
                    Pause a running timer Persona set (its countdown stops until resumed). Pass its id from \
                    phone_alarms_list.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Timer id from phone_alarms_list."),
                    ], required: ["id"])
                ),
                source: .alarms,
                run: { args in try pause(args, resume: false) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_alarm_resume",
                    description: """
                    Resume a paused timer Persona set. Pass its id from phone_alarms_list.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Timer id from phone_alarms_list."),
                    ], required: ["id"])
                ),
                source: .alarms,
                run: { args in try pause(args, resume: true) }
            ),
        ]
        #else
        []
        #endif
    }

    // MARK: - Arguments

    /// When an alarm rings, read from the arguments before anything is set.
    nonisolated struct AlarmRequest: Equatable, Sendable {
        nonisolated enum When: Equatable, Sendable {
            /// One exact moment.
            case at(Date)
            /// A time of day: the next one, or on these weekdays (mon…sun) every week.
            case time(hour: Int, minute: Int, weekdays: [String])
        }

        static let weekdays = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]

        var when: When
        var label: String?

        static func parse(_ args: JSONValue, now: Date = Date()) throws -> AlarmRequest {
            guard let raw = ToolArgs.string(args, "time") else { throw ToolFailure(.invalidArgument, "time is required") }
            var days: [String] = []
            if case .array(let items)? = args["repeat"] {
                for item in items {
                    let day = String((item.string ?? "").lowercased().prefix(3))
                    guard weekdays.contains(day) else {
                        throw ToolFailure(.invalidArgument, "repeat takes mon, tue, wed, thu, fri, sat or sun")
                    }
                    if !days.contains(day) { days.append(day) }
                }
            }
            let label = ToolArgs.string(args, "label")
            let clock = raw.split(separator: ":")
            if clock.count == 2, let hour = Int(clock[0]), let minute = Int(clock[1]), raw.count <= 5 {
                guard (0...23).contains(hour), (0...59).contains(minute) else {
                    throw ToolFailure(.invalidArgument, "time \"\(raw)\" is not a time of day (00:00–23:59)")
                }
                return AlarmRequest(when: .time(hour: hour, minute: minute, weekdays: weekdays.filter(days.contains)), label: label)
            }
            guard days.isEmpty else {
                throw ToolFailure(.invalidArgument, "repeat goes with a time of day (HH:mm), not a date")
            }
            guard let date = try ToolDate.parse(raw), date > now else {
                throw ToolFailure(.invalidArgument, "time \"\(raw)\" is in the past")
            }
            return AlarmRequest(when: .at(date), label: label)
        }
    }

    /// A timer's length, 1 second to 24 hours.
    nonisolated static func timerSeconds(_ args: JSONValue) throws -> Int {
        guard let seconds = ToolArgs.int(args, "seconds"), (1...86_400).contains(seconds) else {
            throw ToolFailure(.invalidArgument, "seconds must be 1–86400")
        }
        return seconds
    }

    // MARK: - Approval cards

    static func alarmSummary(_ request: AlarmRequest) -> String {
        var lines: [String] = []
        switch request.when {
        case .at(let date):
            let when = CardTime.text(date)
            lines.append(String(localized: "Set an alarm for \(when)"))
        case .time(let hour, let minute, let days):
            let when = String(format: "%02d:%02d", hour, minute)
            lines.append(String(localized: "Set an alarm at \(when)"))
            if !days.isEmpty {
                let names = days.map(weekdayName).joined(separator: ", ")
                lines.append(String(localized: "Repeats: \(names)"))
            }
        }
        if let label = request.label { lines.append(String(localized: "Label: \(label)")) }
        return lines.joined(separator: "\n")
    }

    static func timerSummary(seconds: Int, label: String?) -> String {
        let length = Duration.seconds(seconds).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .wide))
        var lines = [String(localized: "Start a timer for \(length)")]
        if let label { lines.append(String(localized: "Label: \(label)")) }
        return lines.joined(separator: "\n")
    }

    /// The weekday's short name in the phone's language (for example, `mon` → `Mon`).
    nonisolated static func weekdayName(_ day: String) -> String {
        guard let index = AlarmRequest.weekdays.firstIndex(of: day) else { return day }
        var calendar = Calendar.current
        calendar.locale = Locale.current
        // shortWeekdaySymbols starts on Sunday.
        return calendar.shortWeekdaySymbols[(index + 1) % 7]
    }

    static func alarmConfirmation(_ args: JSONValue) -> String {
        guard let request = try? AlarmRequest.parse(args) else {
            return String(localized: "Invalid parameters; nothing will be set.")
        }
        return alarmSummary(request)
    }

    static func timerConfirmation(_ args: JSONValue) -> String {
        guard let seconds = try? timerSeconds(args) else {
            return String(localized: "Invalid parameters; nothing will be set.")
        }
        return timerSummary(seconds: seconds, label: ToolArgs.string(args, "label"))
    }

    static func cancelConfirmation(_ args: JSONValue) -> String {
        let id = ToolArgs.string(args, "id") ?? "?"
        if let label = Labels.all[id] { return String(localized: "Cancel the alarm “\(label)”") }
        return String(localized: "Cancel the alarm or timer \(id)")
    }

    /// Labels of the alarms Persona set, by id: AlarmKit does not hand an alarm's text back.
    enum Labels {
        static let key = "phoneAlarms.labels"

        static var all: [String: String] {
            get { UserDefaults.standard.dictionary(forKey: key) as? [String: String] ?? [:] }
            set { UserDefaults.standard.set(newValue, forKey: key) }
        }
    }

    // MARK: - AlarmKit

    #if os(iOS) && canImport(AlarmKit)
    static func list() throws -> [ToolContent] {
        try AlarmsAccess.require()
        let alarms = try AlarmManager.shared.alarms
        let labels = Labels.all
        // Forget the labels of alarms that are gone.
        let live = Set(alarms.map { $0.id.uuidString })
        if labels.keys.contains(where: { !live.contains($0) }) { Labels.all = labels.filter { live.contains($0.key) } }
        let rows = alarms.map { alarm -> JSONValue in
            Output.object([
                ("id", .string(alarm.id.uuidString)),
                ("label", Output.opt(labels[alarm.id.uuidString])),
                ("state", .string(stateName(alarm.state))),
                ("schedule", alarm.schedule.map(scheduleJSON)),
                ("timer_seconds", alarm.countdownDuration?.preAlert.map { JSONValue.number($0.rounded()) }),
            ])
        }
        return Output.json(Output.object([("count", .number(Double(rows.count))), ("alarms", .array(rows))]))
    }

    static func createAlarm(_ args: JSONValue) async throws -> [ToolContent] {
        let request = try AlarmRequest.parse(args)
        try AlarmsAccess.require()
        let schedule: Alarm.Schedule
        switch request.when {
        case .at(let date):
            schedule = .fixed(date)
        case .time(let hour, let minute, let days):
            let weekdays = days.compactMap(localeWeekday)
            let time = Alarm.Schedule.Relative.Time(hour: hour, minute: minute)
            schedule = .relative(Alarm.Schedule.Relative(time: time, repeats: weekdays.isEmpty ? .never : .weekly(weekdays)))
        }
        let id = UUID()
        let label = request.label ?? String(localized: "Alarm [Persona Alarm]")
        let configuration = AlarmManager.AlarmConfiguration<PersonaAlarmData>.alarm(
            schedule: schedule, attributes: attributes(label: label, timer: false))
        let alarm = try await AlarmManager.shared.schedule(id: id, configuration: configuration)
        Labels.all[id.uuidString] = label
        return Output.json(Output.object([
            ("id", .string(alarm.id.uuidString)),
            ("label", .string(label)),
            ("state", .string(stateName(alarm.state))),
            ("schedule", alarm.schedule.map(scheduleJSON)),
        ]))
    }

    static func startTimer(_ args: JSONValue) async throws -> [ToolContent] {
        let seconds = try timerSeconds(args)
        try AlarmsAccess.require()
        let id = UUID()
        let label = ToolArgs.string(args, "label") ?? String(localized: "Timer [Persona Alarm]")
        let configuration = AlarmManager.AlarmConfiguration<PersonaAlarmData>.timer(
            duration: TimeInterval(seconds), attributes: attributes(label: label, timer: true))
        let alarm = try await AlarmManager.shared.schedule(id: id, configuration: configuration)
        Labels.all[id.uuidString] = label
        return Output.json(Output.object([
            ("id", .string(alarm.id.uuidString)),
            ("label", .string(label)),
            ("seconds", .number(Double(seconds))),
            ("ends_at", .string(ToolDate.string(Date().addingTimeInterval(TimeInterval(seconds))))),
            ("state", .string(stateName(alarm.state))),
        ]))
    }

    static func cancel(_ args: JSONValue) throws -> [ToolContent] {
        try AlarmsAccess.require()
        let id = try alarmID(args)
        try AlarmManager.shared.cancel(id: id)
        Labels.all[id.uuidString] = nil
        return Output.json(Output.object([("id", .string(id.uuidString)), ("cancelled", .bool(true))]))
    }

    static func pause(_ args: JSONValue, resume: Bool) throws -> [ToolContent] {
        try AlarmsAccess.require()
        let id = try alarmID(args)
        if resume { try AlarmManager.shared.resume(id: id) } else { try AlarmManager.shared.pause(id: id) }
        return Output.json(Output.object([("id", .string(id.uuidString)), ("state", .string(resume ? "countdown" : "paused"))]))
    }

    /// An id Persona's own alarms have; anything else is refused (the Clock app's alarms are out of reach).
    private static func alarmID(_ args: JSONValue) throws -> UUID {
        guard let raw = ToolArgs.string(args, "id"), let id = UUID(uuidString: raw) else {
            throw ToolFailure(.invalidArgument, "id must be an id from phone_alarms_list")
        }
        guard try AlarmManager.shared.alarms.contains(where: { $0.id == id }) else {
            throw ToolFailure(.invalidArgument, "no alarm or timer of Persona's has id \(raw); call phone_alarms_list")
        }
        return id
    }

    private static func attributes(label: String, timer: Bool) -> AlarmAttributes<PersonaAlarmData> {
        let stop = AlarmButton(text: LocalizedStringResource(stringLiteral: String(localized: "Stop [Persona Alarm]")),
                               textColor: Theme.accent, systemImageName: Theme.Icon.interrupt)
        let alert = AlarmPresentation.Alert(title: LocalizedStringResource(stringLiteral: label), stopButton: stop)
        let presentation: AlarmPresentation
        if timer {
            let pause = AlarmButton(text: LocalizedStringResource(stringLiteral: String(localized: "Pause [Persona Alarm]")),
                                    textColor: Theme.accent, systemImageName: Theme.Icon.pause)
            let resume = AlarmButton(text: LocalizedStringResource(stringLiteral: String(localized: "Resume [Persona Alarm]")),
                                     textColor: Theme.accent, systemImageName: Theme.Icon.run)
            presentation = AlarmPresentation(
                alert: alert,
                countdown: AlarmPresentation.Countdown(title: LocalizedStringResource(stringLiteral: label), pauseButton: pause),
                paused: AlarmPresentation.Paused(title: LocalizedStringResource(stringLiteral: label), resumeButton: resume))
        } else {
            presentation = AlarmPresentation(alert: alert)
        }
        return AlarmAttributes(presentation: presentation, metadata: PersonaAlarmData(label: label), tintColor: Theme.accent)
    }

    private static func localeWeekday(_ day: String) -> Locale.Weekday? {
        switch day {
        case "mon": .monday
        case "tue": .tuesday
        case "wed": .wednesday
        case "thu": .thursday
        case "fri": .friday
        case "sat": .saturday
        case "sun": .sunday
        default: nil
        }
    }

    private static func stateName(_ state: Alarm.State) -> String {
        switch state {
        case .scheduled: "scheduled"
        case .countdown: "countdown"
        case .paused: "paused"
        case .alerting: "alerting"
        @unknown default: "unknown"
        }
    }

    private static func scheduleJSON(_ schedule: Alarm.Schedule) -> JSONValue {
        if case .fixed(let date) = schedule {
            return Output.object([("at", .string(ToolDate.string(date)))])
        }
        if case .relative(let relative) = schedule {
            var days: [JSONValue] = []
            if case .weekly(let weekdays) = relative.repeats {
                days = weekdays.map { .string(String(describing: $0).prefix(3).lowercased()) }
            }
            return Output.object([
                ("time", .string(String(format: "%02d:%02d", relative.time.hour, relative.time.minute))),
                ("repeat", days.isEmpty ? nil : .array(days)),
            ])
        }
        return .null
    }
    #endif
}

#if os(iOS) && canImport(AlarmKit)
/// What Persona keeps with an alarm it sets (AlarmKit's metadata).
nonisolated struct PersonaAlarmData: AlarmMetadata {
    var label: String?
}

/// AlarmKit's permission (`NSAlarmKitUsageDescription`).
enum AlarmsAccess {
    static func status() -> PhoneAuthStatus {
        switch AlarmManager.shared.authorizationState {
        case .authorized: .granted
        case .denied: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        _ = try? await AlarmManager.shared.requestAuthorization()
        return status()
    }

    static func require() throws {
        switch status() {
        case .granted, .limited, .notNeeded: return
        case .denied: throw ToolFailure(.denied, "Alarms (AlarmKit); nothing was set")
        case .notDetermined: throw ToolFailure(.denied, "Alarms access has not been requested yet; nothing was set")
        }
    }
}
#elseif os(iOS)
/// An SDK without AlarmKit: no alarm tools.
enum AlarmsAccess {
    static func status() -> PhoneAuthStatus { .denied }
    static func request() async -> PhoneAuthStatus { .denied }
}
#endif
