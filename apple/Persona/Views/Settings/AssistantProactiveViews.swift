import BeautifulUI
import MarkdownUI
import PersonaKit
import SwiftUI

// Settings › Assistant: what the assistant does on its own (routines, follow-ups it scheduled), what it remembers, and what it
// is told about the user (docs/assistant/assistant.md). Every page reads straight from the hub; nothing is cached on the phone.

// MARK: - Formatting

enum AssistantScheduleText {
    /// Short weekday names, Monday first.
    static var dayNames: [String] {
        [String(localized: "Mon [Assistant Proactive Views 51a75f]", comment: "Weekday, short: Monday"), String(localized: "Tue [Assistant Proactive Views 084b42]", comment: "Weekday, short: Tuesday"),
         String(localized: "Wed [Assistant Proactive Views a4c331]", comment: "Weekday, short: Wednesday"), String(localized: "Thu", comment: "Weekday, short: Thursday"),
         String(localized: "Fri [Assistant Proactive Views c9b87f]", comment: "Weekday, short: Friday"), String(localized: "Sat [Assistant Proactive Views de07b5]", comment: "Weekday, short: Saturday"),
         String(localized: "Sun [Assistant Proactive Views 85217f]", comment: "Weekday, short: Sunday")]
    }
    /// "Mon, Wed, Fri": the separator between weekday names.
    private static func dayList(_ days: Set<Int>) -> String {
        days.sorted().map { dayNames[$0 - 1] }.joined(separator: String(localized: "、", comment: "Separator between weekday names, e.g. Mon, Wed"))
    }
    static let workdays = [1, 2, 3, 4, 5]

    /// "Every Day" / "Weekdays" / "Weekends" / "Every Mon, Wed, Fri".
    static func repeats(_ days: [Int]) -> String {
        let d = Set(days)
        if d.isEmpty || d.count == 7 { return String(localized: "Every Day") }
        if d == Set(workdays) { return String(localized: "Weekdays") }
        if d == [6, 7] { return String(localized: "Weekends") }
        let list = dayList(d)
        return String(localized: "Every \(list)")
    }

    /// The rule in words, as Reminders says it: "Every weekday 9:00 AM" / "Every Day 9:30 PM" / "Every Fri 5:00 PM" /
    /// "Every 30 min" / "Once · Today 15:30".
    static func sentence(_ r: APIClient.ScheduledTask) -> String {
        switch r.repeats {
        case .interval: return every(r.everyMinutes)
        case .once: return r.at.map { String(localized: "Once · \(moment($0))") } ?? String(localized: "Once")
        case .daily: break
        }
        let d = Set(r.weekdays)
        let days: String
        if d.isEmpty || d.count == 7 { days = String(localized: "Every Day") }
        else if d == Set(workdays) { days = String(localized: "Every weekday") }
        else if d == [6, 7] { days = String(localized: "Every weekend") }
        else { let list = dayList(d); days = String(localized: "Every \(list)") }
        let time = clock(r.time)
        return String(localized: "\(days) \(time)", comment: "Repeat rule and time of day, e.g. Every weekday 9:00 AM")
    }

    /// "09:00" → "9:00 AM", "21:30" → "9:30 PM", "00:15" → "12:15 AM".
    static func clock(_ hhmm: String) -> String {
        let parts = hhmm.split(separator: ":").compactMap { Int($0) }
        guard parts.count == 2 else { return hhmm }
        let (h, m) = (parts[0], parts[1])
        let h12 = h % 12 == 0 ? 12 : h % 12
        let time = String(format: "%d:%02d", h12, m)
        return switch h {
        case 0..<5: String(localized: "\(time) AM [Assistant Proactive Views 44e8c1]", comment: "12-hour time, 0:00–4:59")
        case 5..<9: String(localized: "\(time) AM [Assistant Proactive Views 699a88]", comment: "12-hour time, 5:00–8:59")
        case 9..<12: String(localized: "\(time) AM [Assistant Proactive Views 125b02]", comment: "12-hour time, 9:00–11:59")
        case 12..<13: String(localized: "\(time) PM [Assistant Proactive Views 989511]", comment: "12-hour time, 12:00–12:59")
        case 13..<18: String(localized: "\(time) PM [Assistant Proactive Views c3a451]", comment: "12-hour time, 13:00–17:59")
        default: String(localized: "\(time) PM [Assistant Proactive Views af60c6]", comment: "12-hour time, 18:00–23:59")
        }
    }

    /// The day part of a moment: "Today" / "Tomorrow" / "Yesterday" / "Wed" / "Nov 2".
    static func day(_ date: Date, now: Date = .now) -> String {
        let cal = Calendar.current
        if cal.isDateInToday(date) { return String(localized: "Today") }
        if cal.isDateInTomorrow(date) { return String(localized: "Tomorrow") }
        if cal.isDateInYesterday(date) { return String(localized: "Yesterday") }
        let days = cal.dateComponents([.day], from: cal.startOfDay(for: now), to: cal.startOfDay(for: date)).day ?? 0
        if abs(days) < 7 { let name = dayNames[(cal.component(.weekday, from: date) + 5) % 7]; return String(localized: "\(name) [Assistant Proactive Views 918479]") }
        return date.formatted(.dateTime.month().day())
    }

    /// "Every 30 min" / "Every 2 hr" / "Every 1 hr 30 min".
    static func every(_ minutes: Int) -> String {
        if minutes < 60 { return String(localized: "Every \(minutes) minutes") }
        let hours = minutes / 60, rest = minutes % 60
        return rest == 0 ? String(localized: "Every \(hours) hours") : String(localized: "Every \(hours) hr \(rest) min")
    }

    /// The intervals the editor offers.
    static let intervals = [5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 240, 360, 480, 720, 1440]

    /// Its title, or the first line of the prompt for a follow-up without one.
    static func title(_ r: APIClient.ScheduledTask) -> String {
        if !r.title.isEmpty { return r.title }
        return r.prompt.split(whereSeparator: \.isNewline).first.map(String.init) ?? r.prompt
    }

    /// The rule in words; a one-off says where it runs.
    static func rule(_ r: APIClient.ScheduledTask) -> String {
        r.repeats == .once ? (r.sessionID == nil ? String(localized: "Once · New chat") : String(localized: "Once · Continues the chat")) : sentence(r)
    }

    /// The mark for a task whose Push When Done is Only Its Alerts; nil when it pushes after every run.
    static func pushMark(_ r: APIClient.ScheduledTask) -> String? {
        r.pushesOnlyAsked ? String(localized: "Pushes only when it alerts you") : nil
    }

    /// The capsule row's rule line: the rule, then the push mark (on the line rather than as a second pill, which
    /// squeezes the title on a phone). The plain row puts the mark on a line of its own, since its rule line is one line.
    static func ruleLine(_ r: APIClient.ScheduledTask) -> String {
        let rule = rule(r)
        return r.pushesOnlyAsked ? String(localized: "\(rule) · Pushes only when it alerts you") : rule
    }

    /// What it runs with when that isn't the default, in a few words: "claude-fast · Auto-Edit · Thinking Low"; `danger`
    /// for Allow Everything.
    static func runsWith(_ r: APIClient.ScheduledTask) -> (text: String, danger: Bool)? {
        let parts = [r.launcher, SessionModes.modeLabel(r.permissionMode), r.effort.map { let e = SessionModes.effortLabel($0) ?? $0; return String(localized: "Thinking \(e) [Session Modes e77236]") }]
            .compactMap { $0 }
        return parts.isEmpty ? nil : (parts.joined(separator: " · "), r.permissionMode == "bypassPermissions")
    }

    /// The last run in a few words, nil when it never ran: "Last Today 09:00 · Succeeded".
    static func lastRun(_ r: APIClient.ScheduledTask) -> (text: String, failed: Bool)? {
        guard let at = r.lastRun else { return nil }
        let word = switch r.lastStatus {
        case "failed": String(localized: "Failed")
        case "skipped": String(localized: "Skipped")
        case "ok": String(localized: "Ran")
        default: String(localized: "Ran")
        }
        let when = moment(at)
        return (String(localized: "Last \(when) · \(word)"), r.lastStatus == "failed")
    }

    /// An SF Symbol for a task, from its title.
    static func symbol(_ r: APIClient.ScheduledTask) -> String {
        let t = r.title // matched against the Chinese titles of the templates and of what Persona creates
        if t.contains("早") { return "sun.horizon" } // i18n-ignore
        if t.contains("晚") || t.contains("回顾") { return "moon.stars" } // i18n-ignore
        if t.contains("周报") || t.contains("报告") { return Theme.Icon.document } // i18n-ignore
        if t.contains("会") { return Theme.Icon.meeting } // i18n-ignore
        if t.contains("盘点") || t.contains("待办") { return "checklist" } // i18n-ignore
        if t.contains("巡检") || t.contains("检查") { return "binoculars" } // i18n-ignore
        return switch r.repeats {
        case .once: Theme.Icon.followOn
        case .interval: Theme.Icon.inProgress
        case .daily: "alarm"
        }
    }

    /// A future moment, roughly: "Soon" / "in 25 min" / "in 2 hr" / "in 3 days".
    static func until(_ date: Date, now: Date = .now) -> String {
        let s = date.timeIntervalSince(now)
        if s < 60 { return s < -60 ? String(localized: "Overdue") : String(localized: "Soon") }
        if s < 3600 { let minutes = Int(s / 60); return String(localized: "in \(minutes) minutes") }
        if s < 86400 {
            let h = Int(s / 3600), m = Int(s.truncatingRemainder(dividingBy: 3600) / 60)
            return h < 3 && m >= 10 ? String(localized: "in \(h) hr \(m) min") : String(localized: "in \(h) hours")
        }
        let days = Calendar.current.dateComponents([.day], from: Calendar.current.startOfDay(for: now),
                                                   to: Calendar.current.startOfDay(for: date)).day ?? 0
        let n = max(days, 1)
        return String(localized: "in \(n) days")
    }

    /// "17:05", always 24-hour: `.hour(.twoDigits(amPM: .omitted))` in a Chinese locale gives the 12-hour "05:05".
    static func hm(_ date: Date) -> String { hmFormatter.string(from: date) }
    private static let hmFormatter: DateFormatter = {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "HH:mm"
        return f
    }()

    /// "Today 15:30" / "Tomorrow 09:00" / "Wed 09:00" / "Nov 2 09:00".
    static func moment(_ date: Date, now: Date = .now) -> String {
        let cal = Calendar.current
        let time = hm(date)
        if cal.isDateInToday(date) { return String(localized: "Today \(time)") }
        if cal.isDateInTomorrow(date) { return String(localized: "Tomorrow \(time)") }
        let days = cal.dateComponents([.day], from: cal.startOfDay(for: now), to: cal.startOfDay(for: date)).day ?? 0
        if days > 1, days < 7 { let name = dayNames[(cal.component(.weekday, from: date) + 5) % 7]; return String(localized: "\(name) \(time) [Assistant Proactive Views a5627f]") }
        let day = date.formatted(.dateTime.month().day())
        return String(localized: "\(day) \(time)", comment: "Date and 24-hour time, e.g. Nov 2 09:00")
    }
}

// MARK: - Scheduled Tasks

/// Everything Persona does on its own, on one page: what runs next, what repeats (every day at a time, every N
/// minutes; the rule in words, next and last run, an on/off switch), the one-offs it scheduled from a conversation,
/// and the history of runs. Each opens the same editor: when, the prompt, and what it runs with.
struct AssistantRoutinesView: View {
    @Environment(AppModel.self) private var model
    @State private var page = AssistantRoutinesModel()
    @State private var editing: APIClient.ScheduledTask?
    @State private var creating: APIClient.ScheduledTask?

    var body: some View {
        let repeating = page.repeating, oneOff = page.oneOff, lastRuns = page.lastRuns, upcoming = page.upcoming
        List {
            if !upcoming.isEmpty {
                Section {
                    ForEach(upcoming) { u in AssistantUpcomingRow(item: u) }
                } header: {
                    Text("Up Next")
                }
            }
            if !repeating.isEmpty {
                Section {
                    ForEach(repeating) { t in row(t) }
                } header: {
                    Text("Scheduled Tasks")
                }
            }
            if !oneOff.isEmpty {
                Section {
                    ForEach(oneOff) { t in row(t) }
                } header: {
                    Text("One-Time Follow-ups")
                }
            }
            if !lastRuns.isEmpty {
                Section {
                    ForEach(lastRuns.prefix(3)) { r in AssistantRunRow(run: r) }
                    NavigationLink { AssistantRunsView() } label: { Text("All Runs").lineLimit(1) }
                } header: {
                    Text("Recent Runs")
                }
            }
        }
        .themedPage()
        .overlay {
            if !page.loaded {
                ProgressView()
            } else if let error = page.error, page.tasks.isEmpty {
                ContentUnavailableView("Couldn't Load Scheduled Tasks", systemImage: Theme.Icon.error, description: Text(error))
            } else if page.tasks.isEmpty {
                ContentUnavailableView("No Scheduled Tasks", systemImage: Theme.Icon.schedule,
                                       description: Text("Tap + at the top right, or say “give me a morning briefing at 9 every day” or “check in every 30 minutes” in a chat to have Persona do something on a schedule."))
            }
        }
        .pageTitle(String(localized: "Scheduled Tasks"))
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Section("Start from a Template") {
                        ForEach(AssistantRoutineTemplate.all) { t in
                            Button { creating = t.routine } label: { Label(t.routine.title, systemImage: t.symbol) }
                        }
                    }
                    Button { creating = AssistantRoutineTemplate.blank } label: { Label("Blank", systemImage: Theme.Icon.compose) }
                } label: {
                    ThemedIcon(Theme.Icon.add)
                }
                .accessibilityLabel("New Scheduled Task")
            }
        }
        .navigationDestination(item: $editing) { t in
            AssistantTaskEditor(task: t, save: page.save, delete: page.delete)
        }
        .sheet(item: $creating) { t in
            NavigationStack { AssistantTaskEditor(task: t, save: page.save, delete: page.delete) }
        }
        .task { await load() }
        .refreshable { await page.load() }
    }

    private func row(_ t: APIClient.ScheduledTask) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            AssistantTaskRow(task: t, open: { editing = t }) { on in
                var changed = t
                changed.enabled = on
                Haptics.tap()
                Task { _ = await page.save(changed) }
            }
            chats(t)
        }
        .modifier(AssistantTaskRowLook())
        .swipeActions(edge: .trailing, allowsFullSwipe: t.repeats == .once) {
            Button(role: .destructive) { Task { await page.delete(t) } } label: { Label("Delete", systemImage: Theme.Icon.delete) }
        }
        .contextMenu {
            Button { Task { await page.run(t) } } label: { Label("Run Now", systemImage: Theme.Icon.run) }
            Button { editing = t } label: { Label("Edit [Message Menu 051836]", systemImage: Theme.Icon.compose) }
            Button(role: .destructive) { Task { await page.delete(t) } } label: { Label("Delete", systemImage: Theme.Icon.delete) }
        }
    }

    /// The conversations the task's runs went into, under its row (#2838): they are kept here, not in the sidebar's
    /// Recent. A tap opens one.
    @ViewBuilder private func chats(_ t: APIClient.ScheduledTask) -> some View {
        let chats = AssistantTaskChats.chats(of: t, runs: page.lastRuns, machineID: model.assistantConfig?.machineID ?? model.hubMachineID)
        if !chats.isEmpty {
            FlowLayoutRow(spacing: Theme.Space.xs) {
                ForEach(Array(chats.enumerated()), id: \.element.key) { i, c in
                    let label = AssistantTaskChats.label(c, index: i)
                    Button {
                        Haptics.tap()
                        model.openAssistantConversation(model.inHub(c.key)) // a task's conversation is the hub-level group's
                    } label: {
                        Chip(text: label, symbol: Theme.Icon.conversation, look: .tint(Theme.accent))
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Open the \(label) chat of “\(AssistantScheduleText.title(t))”")
                    .accessibilityIdentifier("task-chat-\(t.id)-\(i)")
                }
            }
            .padding(.leading, AppStyle.current.usesBeautifulUI ? Theme.Space.l : Theme.Size.wellM + Theme.Space.m)
            .padding(.bottom, AppStyle.current.usesBeautifulUI ? Theme.Space.s : Theme.Space.xxs)
        }
    }

    private func load() async {
        await page.load()
        #if DEBUG
        switch UserDefaults.standard.string(forKey: "openAssistantPage") {
        case "routine" where editing == nil: editing = page.repeating.first
        case "intervalTask" where editing == nil: editing = page.repeating.first { $0.repeats == .interval }
        case "followup" where editing == nil: editing = page.oneOff.first
        case "newRoutine" where creating == nil: creating = AssistantRoutineTemplate.all.last?.routine
        default: break
        }
        #endif
    }
}

/// One task on Scheduled Tasks: its icon, title, the rule in words, next and last run, what it runs with when that isn't the
/// default, and an on/off switch. Tapping opens the editor.
struct AssistantTaskRow: View {
    var task: APIClient.ScheduledTask
    var open: () -> Void
    var toggle: (Bool) -> Void

    var body: some View {
        if AppStyle.current.usesBeautifulUI { capsule } else { plain }
    }

    /// The BeautifulUI style: its task capsule. Its mark (red when it runs with Allow Everything), the title over the rule in
    /// words, the next run, what it runs with and the last run under that, its switch; a tap opens the editor, as the
    /// plain row does.
    private var capsule: some View {
        let t = task
        let with = AssistantScheduleText.runsWith(t)
        let next = t.enabled ? (t.nextRun ?? t.at) : nil
        let notes = [t.enabled ? next.map { let m = AssistantScheduleText.moment($0); return String(localized: "Next \(m) [Session Panes 0881ca]") } : String(localized: "Off [Git Hub Thread Models 6744b4]"),
                     with?.text, AssistantScheduleText.lastRun(t)?.text,
                     t.createdBy == "assistant" && t.repeats != .once ? String(localized: "By Persona") : nil].compactMap { $0 }
        return TaskRow(TaskItem(id: t.id, title: AssistantScheduleText.title(t),
                                subtitle: AssistantScheduleText.ruleLine(t),
                                footnote: notes.joined(separator: " · "), status: .pending(step: 0)),
                       accessory: .disclosure, subtitleLines: 2,
                       action: {
                           Haptics.tap()
                           open()
                       }) {
            IconTile(AssistantScheduleText.symbol(t), tint: with?.danger == true ? Theme.danger : t.enabled ? Theme.accent : Theme.Hue.gray)
        } trailing: {
            Toggle(AssistantScheduleText.title(t), isOn: Binding(get: { t.enabled }, set: toggle))
                .labelsHidden()
                .accessibilityIdentifier("task-switch-\(t.id)")
        } detail: {
            EmptyView()
        }
        .opacity(t.enabled ? 1 : 0.75)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("task-\(t.id)")
    }

    @ViewBuilder private var plain: some View {
        let t = task
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(AssistantScheduleText.symbol(t))
                .font(Theme.Font.heading)
                .foregroundStyle(.white)
                .scaledFrame(width: Theme.Size.wellM, height: Theme.Size.wellM)
                .background((t.enabled ? Theme.accent : Color.secondary).gradient, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(AssistantScheduleText.title(t)).font(Theme.Font.heading).lineLimit(1)
                    if t.createdBy == "assistant" && t.repeats != .once {
                        Text("By Persona").font(Theme.Font.labelMedium).foregroundStyle(Theme.accent)
                            .padding(.horizontal, Theme.Space.sm).padding(.vertical, 1)
                            .background(Theme.accent.opacity(0.12), in: .capsule)
                            .lineLimit(1)
                            .fixedSize()
                    }
                }
                Text(AssistantScheduleText.rule(t))
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Group {
                    if !t.enabled {
                        Text("Off [Git Hub Thread Models 6744b4]")
                    } else if let next = t.nextRun ?? t.at {
                        Text("Next \(AssistantScheduleText.moment(next)) · \(AssistantScheduleText.until(next))")
                    }
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .monospacedDigit()
                .lineLimit(1)
                if let with = AssistantScheduleText.runsWith(t) {
                    Label(with.text, systemImage: with.danger ? Theme.Icon.allowEverything : Theme.Icon.effort)
                        .labelStyle(AssistantCompactLabel())
                        .font(Theme.Font.label)
                        .foregroundStyle(with.danger ? Theme.danger : Color.secondary)
                        .lineLimit(1)
                }
                if let mark = AssistantScheduleText.pushMark(t) {
                    Label(mark, systemImage: Theme.Icon.notifications)
                        .labelStyle(AssistantCompactLabel())
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                if let last = AssistantScheduleText.lastRun(t) {
                    Label(last.text, systemImage: last.failed ? Theme.Icon.errorFilled : Theme.Icon.done)
                        .labelStyle(AssistantCompactLabel())
                        .font(Theme.Font.label)
                        .foregroundStyle(last.failed ? Theme.danger : Color.secondary)
                        .lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
            .onTapGesture(perform: open)
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isButton)
            .accessibilityIdentifier("task-\(t.id)")
            Toggle(AssistantScheduleText.title(t), isOn: Binding(get: { t.enabled }, set: toggle))
                .labelsHidden()
                .accessibilityIdentifier("task-switch-\(t.id)")
        }
        .padding(.vertical, Theme.Space.xxs)
        .opacity(t.enabled ? 1 : 0.75)
    }
}

/// Starting points for the + menu.
struct AssistantRoutineTemplate: Identifiable {
    var routine: APIClient.ScheduledTask
    var symbol: String
    var id: String { routine.title }

    static let blank = APIClient.ScheduledTask(title: "", prompt: "", time: "09:00", weekdays: AssistantScheduleText.workdays)

    static let all: [AssistantRoutineTemplate] = [
        .init(routine: .init(title: String(localized: "Meeting Prep"),
                             prompt: "看看今天飞书日历和 iPhone 日历里的会。对每个需要准备的会，用 schedule_followup 在会前 10 分钟" + // i18n-ignore: prompt for the assistant (docs/protocol/i18n.md: prompts stay Chinese)
                             "安排一次会前准备：相关文档、上次的会议纪要、还没定的事、我该说的要点。今天没有要准备的会就简单说一声。", // i18n-ignore
                             time: "08:45", weekdays: AssistantScheduleText.workdays), symbol: Theme.Icon.meeting),
        .init(routine: .init(title: String(localized: "Weekly Report"),
                             prompt: "帮我起草这周的周报：从飞书日历、会议纪要、文档和消息里整理这周做了什么、进展和结果、" + // i18n-ignore
                             "遇到的问题、下周计划；各台电脑上的 Agent 这周完成的主要改动也算进去。先给我草稿，问我要不要改，不要直接发出去。", // i18n-ignore
                             time: "17:00", weekdays: [5]), symbol: Theme.Icon.document),
        .init(routine: .init(title: String(localized: "Agent Check-in"),
                             prompt: "看看各台电脑上的 Agent：哪些在等我、哪些卡住或报错了、哪些跑完了没人看。" + // i18n-ignore
                             "有要我处理的就列出来（一行一个，说清楚在哪台电脑、等什么）；都正常就回一句「都正常」。", // i18n-ignore
                             repeats: .interval, everyMinutes: 30), symbol: "binoculars"),
        .init(routine: .init(title: String(localized: "End-of-Day Review"),
                             prompt: "下班前盘点：今天还有哪些消息没回、答应别人的事还没做、各台电脑上还在跑或在等我的 Agent，" + // i18n-ignore
                             "明天上午有哪些会要准备。按紧急程度列出来，最后问我哪些要设提醒。", // i18n-ignore
                             time: "18:30", weekdays: AssistantScheduleText.workdays), symbol: Theme.Icon.todo),
    ]
}

/// Edits one task: when (once / every day / every N minutes), the prompt, what it runs with (launcher, permission
/// mode, effort) and which conversation runs go into. An existing one is saved when leaving the page (like iOS
/// Settings); a new one comes up as a sheet with Cancel / Add, so trying a template and backing out doesn't create
/// anything.
struct AssistantTaskEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var draft: APIClient.ScheduledTask
    /// What the hub has; nil until a new task is first saved.
    @State private var saved: APIClient.ScheduledTask?
    @State private var running = false
    @State private var resetting = false
    @State private var confirmDelete = false
    @State private var confirmBypass = false
    @State private var deleted = false
    @State private var runs: [APIClient.ScheduleRun] = []
    /// Presented as the "new task" sheet: nothing is saved unless Add (or Run Now) is tapped.
    private let creating: Bool
    /// The task as the sheet opened: Cancel asks first once it differs (Mac).
    private let opened: APIClient.ScheduledTask
    /// The conversation it came from, to switch back to after turning Continue in Original Chat off.
    private let conversation: String?
    var save: (APIClient.ScheduledTask) async -> APIClient.ScheduledTask?
    var delete: (APIClient.ScheduledTask) async -> Void

    init(task: APIClient.ScheduledTask, save: @escaping (APIClient.ScheduledTask) async -> APIClient.ScheduledTask?,
         delete: @escaping (APIClient.ScheduledTask) async -> Void) {
        _draft = State(initialValue: task)
        _saved = State(initialValue: task.id.isEmpty ? nil : task)
        creating = task.id.isEmpty
        opened = task
        conversation = task.sessionID
        self.save = save
        self.delete = delete
    }

    private var isValid: Bool {
        let titled = draft.repeats == .once || !draft.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return titled && !draft.prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
    private var changed: Bool { saved.map { !Self.same($0, draft) } ?? true }
    /// Push When Done shows when the hub takes it (task_push), or the task already has it.
    private var showsPush: Bool { model.hubFeatures.contains(APIClient.ScheduledTask.pushFeature) || opened.pushesOnlyAsked }
    private var machineID: String { model.assistantConfig?.machineID ?? model.hubMachineID }

    var body: some View {
        Form {
            Section {
                TextField(draft.repeats == .once ? String(localized: "Title (optional)") : String(localized: "Title, e.g. “Morning briefing”"), text: $draft.title)
                Toggle("On [Assistant Proactive Views 8da97d]", isOn: $draft.enabled)
            }
            whenSection
            Section {
                #if os(macOS)
                // A grouped Mac form shows a titled field as label + trailing value: the prompt took the right half.
                TextField(String(localized: "What should Persona do?"), text: $draft.prompt, prompt: Text("What should Persona do?"), axis: .vertical)
                    .labelsHidden()
                    .lineLimit(8...)
                #else
                TextField(String(localized: "What should Persona do?"), text: $draft.prompt, axis: .vertical)
                    .lineLimit(8...)
                #endif
            } header: {
                Text("Prompt")
            } footer: {
                Text("Write it as you would in a chat: what to look at and how to report. It can use schedule_followup to set up one-time follow-ups, like 10 minutes before a meeting.")
            }
            runsWithSection
            if showsPush { pushSection }
            conversationSection
            Section {
                Button { Task { await runNow() } } label: {
                    HStack {
                        Label("Run Now", systemImage: Theme.Icon.run).lineLimit(1)
                        Spacer()
                        if running { ProgressView() }
                    }
                }
                .disabled(running || !isValid)
                // The task's own conversation has its own row above.
                if let last = saved?.lastSessionID, !last.isEmpty, last != saved?.runSessionID {
                    Button { open(SessionKey(machineID: machineID, sessionID: last)) } label: {
                        HStack {
                            Label("View Last Result", systemImage: Theme.Icon.conversation).lineLimit(1)
                            Spacer()
                            if let at = saved?.lastRun {
                                Text(Fmt.relative(at)).font(Theme.Font.body).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                    }
                }
            } footer: {
                if saved?.lastStatus == "failed", let err = saved?.lastError {
                    Text("The last run didn't start: \(err)").foregroundStyle(Theme.danger)
                } else {
                    Text("Running now doesn't change its schedule. See the result in the chat when it's done.")
                }
            }
            if !runs.isEmpty {
                Section {
                    ForEach(runs.prefix(5)) { r in AssistantRunRow(run: r, showTitle: false) }
                    if runs.count > 5, let id = saved?.id {
                        NavigationLink { AssistantRunsView(ref: id) } label: { Text("All \(runs.count) Runs").lineLimit(1) }
                    }
                } header: {
                    Text("History")
                }
            }
            if !creating {
                Section {
                    Button(role: .destructive) { confirmDelete = true } label: {
                        Label("Delete Scheduled Task [Assistant Proactive Views 1c8557]", systemImage: Theme.Icon.delete).lineLimit(1)
                    }
                }
            }
        }
        .themedPage()
        .navigationTitle(creating ? String(localized: "New Scheduled Task") : (draft.title.isEmpty ? String(localized: "Scheduled Tasks") : draft.title))
        .navigationBarTitleDisplayMode(.inline)
        .sheetCancel(unsaved: !Self.same(opened, draft), when: creating) { dismiss() }
        .toolbar {
            if creating {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add [Beautiful U I Bridge 7a8a11]") {
                        Task { if await persist(leaving: false) != nil { dismiss() } }
                    }
                    .disabled(!isValid || running)
                }
            }
        }
        .confirmationDialog("Delete “\(AssistantScheduleText.title(draft))”?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                guard let s = saved else { return }
                deleted = true
                Task { await delete(s) }
                dismiss()
            }
        } message: {
            Text("It won't run again. Chats from earlier runs stay.")
        }
        .alert("Allow Everything?", isPresented: $confirmBypass) {
            Button("Allow Everything", role: .destructive) { draft.permissionMode = "bypassPermissions" }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("When this task runs, it won't ask before running any command, changing any file or using any tool, and no one can stop it midway. Use it only for prompts you trust and whose mistakes you can live with.")
        }
        .onDisappear { if !creating { Task { await persist(leaving: true) } } }
        .task(id: saved?.id) {
            guard let id = saved?.id, !id.isEmpty, let api = model.api else { return }
            runs = (try? await api.scheduleRuns(ref: id)) ?? []
        }
        #if DEBUG
        .onAppear { if UserDefaults.standard.string(forKey: "openAssistantPage") == "bypassTask" { confirmBypass = true } }
        #endif
    }

    /// Where runs go (docs/assistant/assistant.md「定时任务」): the conversation it came from (Continue in Original Chat), else the task's own
    /// one, which the first run starts and the next ones continue; New Chat Next Time lets the next run start over.
    private var conversationSection: some View {
        Section {
            // A task that pushes only what it sends runs in its own conversation (the hub clears session_id too).
            if conversation != nil && !draft.pushesOnlyAsked {
                Toggle("Continue in Original Chat", isOn: Binding(get: { draft.sessionID != nil }, set: { draft.sessionID = $0 ? conversation : nil }))
            }
            if draft.sessionID == nil {
                if let own = saved?.ownConversation {
                    Button { open(own) } label: {
                        HStack {
                            Label("Runs in the Same Chat", systemImage: Theme.Icon.conversation).lineLimit(1)
                            Spacer()
                            ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.caption).foregroundStyle(.tertiary)
                        }
                    }
                    .accessibilityIdentifier("task.ownConversation")
                    Button { Task { await startOver() } } label: {
                        HStack {
                            Label("New Chat Next Time", systemImage: Theme.Icon.newConversation).lineLimit(1)
                            Spacer()
                            if resetting { ProgressView() }
                        }
                    }
                    .disabled(resetting)
                    .accessibilityIdentifier("task.freshConversation")
                } else {
                    Label("Runs in the Same Chat", systemImage: Theme.Icon.conversation).lineLimit(1)
                        .accessibilityIdentifier("task.ownConversation")
                }
            }
        } header: {
            Text("Chat")
        } footer: {
            Text(draft.sessionID != nil ? String(localized: "Each run goes into the chat that scheduled it and continues with its context.")
                 : saved?.ownConversation != nil ? String(localized: "Each run goes into this task's own chat and can see earlier results. When it gets too long or you want a fresh start, tap “New Chat Next Time”; the old chat stays.")
                 : String(localized: "The first run starts a new chat; later runs continue in it and can see earlier results."))
        }
    }

    /// Push When Done (docs/assistant/assistant.md「跑完推送」): every run's answer is pushed, or only what a run sends with notify_user.
    private var pushSection: some View {
        Section {
            SegmentedChoice(String(localized: "Push When Done"), [(value: "", label: String(localized: "Every Run")), (value: APIClient.ScheduledTask.pushAsked, label: String(localized: "Only Its Alerts"))],
                            selection: push)
                .accessibilityIdentifier("task-push")
        } header: {
            Text("Push When Done")
        } footer: {
            Text(draft.pushesOnlyAsked
                 ? String(localized: "Pushes only when a run decides you need to know and alerts you; otherwise it stays quiet. Errors and interruptions are pushed as usual. These tasks always run in their own chat, and messages you send in that chat aren't pushed when done either.")
                 : String(localized: "Pushes the answer to your phone after every run."))
        }
    }

    /// Only Its Alerts leaves Continue in Original Chat: such a task runs in its own conversation. Every Run is sent as "" (the default),
    /// so a task switched back is saved as such; one never switched sends nothing.
    private var push: Binding<String> {
        Binding { draft.push ?? "" } set: { new in
            withAnimation(Theme.Motion.standard) {
                draft.push = new
                if new == APIClient.ScheduledTask.pushAsked { draft.sessionID = nil }
            }
        }
    }

    private func startOver() async {
        guard let id = saved?.id, let api = model.api else { return }
        resetting = true
        defer { resetting = false }
        do {
            let t = try await api.freshTaskConversation(id)
            saved?.runMachineID = t.runMachineID
            saved?.runSessionID = t.runSessionID
            Haptics.success()
            model.toast = Toast(String(localized: "The next run will start a new chat"), style: .success)
        } catch {
            model.show(error)
        }
    }

    @ViewBuilder private var whenSection: some View {
        Section {
            Group {
                if AppStyle.current.usesBeautifulUI {
                    BUISegmented([.init(APIClient.ScheduledTask.Repeat.once, String(localized: "Once")), .init(.daily, String(localized: "Every Day")), .init(.interval, String(localized: "Interval [Assistant Proactive Views 100107]"))],
                                 selection: repeats, fills: true)
                } else {
                    Picker("Repeat", selection: repeats) {
                        Text("Once").tag(APIClient.ScheduledTask.Repeat.once)
                        Text("Every Day").tag(APIClient.ScheduledTask.Repeat.daily)
                        Text("Interval [Assistant Proactive Views 100107]").tag(APIClient.ScheduledTask.Repeat.interval)
                    }
                    .pickerStyle(.segmented)
                }
            }
            .listRowInsets(EdgeInsets(top: 10, leading: 16, bottom: 10, trailing: 16))
            switch draft.repeats {
            case .once:
                DatePicker("Time", selection: at, in: Date.now..., displayedComponents: [.date, .hourAndMinute])
            case .daily:
                DatePicker("Time", selection: time, displayedComponents: .hourAndMinute)
                AssistantWeekdayPicker(days: $draft.weekdays)
                    .listRowInsets(EdgeInsets(top: 12, leading: 16, bottom: 12, trailing: 16))
            case .interval:
                Picker("Interval [Assistant Proactive Views 4f5df7]", selection: $draft.everyMinutes) {
                    ForEach(intervals, id: \.self) { m in Text(AssistantScheduleText.every(m)).tag(m) }
                }
            }
        } header: {
            Text("When")
        } footer: {
            if draft.enabled, let next = saved?.nextRun, saved.map({ Self.same($0, draft) }) == true {
                Text("Next run: \(AssistantScheduleText.moment(next)) (\(AssistantScheduleText.until(next)))")
            } else {
                switch draft.repeats {
                case .once: Text("Runs once at the set time, then leaves the list (it stays in the run history).")
                #if os(macOS)
                case .daily: Text("\(AssistantScheduleText.repeats(draft.weekdays)) at \(draft.time), in your time zone.")
                #else
                case .daily: Text("\(AssistantScheduleText.repeats(draft.weekdays)) at \(draft.time), in your phone's time zone.")
                #endif
                case .interval: Text("Runs \(AssistantScheduleText.every(draft.everyMinutes)), counting from when it's saved.")
                }
            }
        }
    }

    /// The model (launcher), permission mode and effort a run starts claude with; "" in a picker = the default.
    private var runsWithSection: some View {
        Section {
            ModelConfigurationLink(title: draft.id.isEmpty ? String(localized: "Default Task Model") : String(localized: "Task Model"),
                                   purposeID: draft.id.isEmpty ? "schedule" : "task.\(draft.id)")
            // A Menu around an inline picker: a menu-style Picker greys out a value that has an icon, and Allow Everything is red.
            LabeledContent {
                Menu {
                    Picker("Permission Mode", selection: mode) {
                        Label { Text("Default [Prompt Composer 844b8c]"); Text("As usual: asks before changing files or running commands") } icon: { ThemedIcon(Theme.Icon.askFirst) }.tag("")
                        ForEach(SessionModes.permissionModes.filter { $0.id != "default" }) { o in
                            Label { Text(o.title); Text(o.id == "bypassPermissions" ? String(localized: "Never asks; risky") : o.detail) } icon: { ThemedIcon(o.symbol) }
                                .tag(o.id)
                        }
                    }
                    .pickerStyle(.inline)
                } label: {
                    HStack(spacing: Theme.Space.xs) {
                        Text(SessionModes.modeLabel(draft.permissionMode) ?? String(localized: "Default [Prompt Composer 844b8c]")).lineLimit(1)
                        ThemedIcon(Theme.Icon.choices).font(Theme.Font.labelStrong).imageScale(.small)
                    }
                    .foregroundStyle(draft.permissionMode == "bypassPermissions" ? Theme.danger : Theme.accent)
                }
                .accessibilityIdentifier("task-mode")
            } label: {
                Label("Permission Mode", systemImage: Theme.Icon.askFirst).lineLimit(1)
            }
        } header: {
            Text("How It Runs")
        } footer: {
            if draft.permissionMode == "bypassPermissions" {
                Label("Allow Everything: it won't ask before running commands or changing files.", systemImage: "exclamationmark.shield.fill")
                    .foregroundStyle(Theme.danger)
            } else {
                Text(String(localized: "Scheduled tasks run in the background. When one needs your approval you get a notification, and it waits until someone responds. To let it run commands without asking, choose “Auto” (it judges the risk itself).")
                     + (draft.sessionID != nil ? String(localized: " When it continues in the original chat, that chat switches to these settings too.", comment: "Appended right after the previous sentence") : ""))
            }
        }
    }

    private var intervals: [Int] {
        var list = AssistantScheduleText.intervals
        if !list.contains(draft.everyMinutes) { list.append(draft.everyMinutes); list.sort() }
        return list
    }

    /// Allow Everything asks first.
    private var mode: Binding<String> {
        Binding { draft.permissionMode ?? "" } set: { new in
            if new == "bypassPermissions" && draft.permissionMode != new {
                confirmBypass = true
            } else {
                draft.permissionMode = new.isEmpty ? nil : new
            }
        }
    }

    /// Switching to Once picks a time an hour from now; to Interval, half an hour.
    private var repeats: Binding<APIClient.ScheduledTask.Repeat> {
        Binding { draft.repeats } set: { r in
            withAnimation(Theme.Motion.standard) {
                draft.repeats = r
                if r == .once, (draft.at ?? .distantPast) < .now {
                    let hour = Calendar.current.dateInterval(of: .hour, for: .now.addingTimeInterval(3600))?.start ?? .now
                    draft.at = hour
                }
                if r == .interval, draft.everyMinutes < 5 { draft.everyMinutes = 30 }
            }
        }
    }

    private var at: Binding<Date> {
        Binding { draft.at ?? .now.addingTimeInterval(3600) } set: { draft.at = $0 }
    }

    /// The DatePicker edits "HH:MM" through a date today.
    private var time: Binding<Date> {
        Binding {
            let parts = draft.time.split(separator: ":").compactMap { Int($0) }
            return Calendar.current.date(bySettingHour: parts.first ?? 9, minute: parts.count > 1 ? parts[1] : 0, second: 0, of: .now) ?? .now
        } set: { d in
            let c = Calendar.current.dateComponents([.hour, .minute], from: d)
            draft.time = String(format: "%02d:%02d", c.hour ?? 0, c.minute ?? 0)
        }
    }

    /// Ignores the hub-only fields (last / next run) and, outside its rule, the fields another rule uses.
    static func same(_ a: APIClient.ScheduledTask, _ b: APIClient.ScheduledTask) -> Bool {
        guard a.title == b.title, a.prompt == b.prompt, a.repeats == b.repeats, a.enabled == b.enabled, a.sessionID == b.sessionID,
              a.launcher == b.launcher, a.permissionMode == b.permissionMode, a.effort == b.effort,
              (a.push ?? "") == (b.push ?? "") else { return false }
        return switch a.repeats {
        case .once: a.at == b.at
        case .daily: a.time == b.time && Set(a.weekdays) == Set(b.weekdays)
        case .interval: a.everyMinutes == b.everyMinutes
        }
    }

    @discardableResult private func persist(leaving: Bool) async -> APIClient.ScheduledTask? {
        guard !deleted, changed else { return saved }
        guard isValid else {
            if leaving {
                model.toast = Toast(draft.repeats == .once ? String(localized: "A prompt is required; your changes weren't saved") : String(localized: "A title and prompt are required; your changes weren't saved"), style: .error)
            }
            return nil
        }
        var t = draft
        t.title = t.title.trimmingCharacters(in: .whitespacesAndNewlines)
        if Set(t.weekdays).count == 7 { t.weekdays = [] }
        t.weekdays.sort()
        guard let s = await save(t) else { return nil }
        saved = s
        if !leaving { draft.id = s.id }
        return s
    }

    private func runNow() async {
        guard let api = model.api else { return }
        running = true
        defer { running = false }
        guard let t = await persist(leaving: false) else { return }
        do {
            let key = try await api.runTask(t.id)
            Haptics.success()
            open(key)
        } catch {
            model.show(error)
        }
    }

    private func open(_ key: SessionKey) {
        if creating { dismiss() }
        model.openAssistantConversation(model.inHub(key)) // a task's conversation is the hub-level group's Persona
    }
}

/// Seven round day buttons (Mon … Sun) plus Every Day / Weekdays shortcuts; empty `days` means every day.
struct AssistantWeekdayPicker: View {
    @Binding var days: [Int]

    private var selected: Set<Int> { days.isEmpty ? Set(1...7) : Set(days) }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: 0) {
                ForEach(1...7, id: \.self) { d in
                    let on = selected.contains(d)
                    Button { toggle(d) } label: {
                        Text(AssistantScheduleText.dayNames[d - 1])
                            .font(Theme.Font.secondaryStrong)
                            .foregroundStyle(on ? Color.white : Color.primary)
                            .scaledFrame(width: Theme.Size.wellXL, height: Theme.Size.wellXL)
                            .background(Circle().fill(on ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(Color.secondary.opacity(0.14))))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(String(localized: "\(AssistantScheduleText.dayNames[d - 1]) [Assistant Proactive Views 918479]"))
                    .accessibilityAddTraits(on ? .isSelected : [])
                    .frame(maxWidth: .infinity)
                }
            }
            HStack(spacing: Theme.Space.s) {
                shortcut(String(localized: "Every Day"), [])
                shortcut(String(localized: "Weekdays"), AssistantScheduleText.workdays)
                shortcut(String(localized: "Weekends"), [6, 7])
                Spacer(minLength: 0)
            }
        }
    }

    private func shortcut(_ title: String, _ value: [Int]) -> some View {
        let on = AssistantScheduleText.repeats(days) == AssistantScheduleText.repeats(value)
        return Button {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) { days = value }
        } label: {
            Text(title)
                .font(Theme.Font.captionMedium)
                .lineLimit(1)
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.sm)
                .foregroundStyle(on ? Theme.accent : Color.secondary)
                .background(Capsule().fill(on ? Theme.accent.opacity(0.14) : Color.secondary.opacity(0.1)))
        }
        .buttonStyle(.plain)
    }

    private func toggle(_ d: Int) {
        var s = selected
        if s.contains(d) {
            guard s.count > 1 else { Haptics.error(); return } // no days would read as "every day"
            s.remove(d)
        } else {
            s.insert(d)
        }
        Haptics.tap()
        withAnimation(Theme.Motion.standard) { days = s.count == 7 ? [] : s.sorted() }
    }
}

/// Icon and text closer together than the default label.
struct AssistantCompactLabel: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: Theme.Space.xs) { configuration.icon; configuration.title }
    }
}

// MARK: - Memory

/// One memory file split into its frontmatter and body.
struct AssistantMemoryEntry: Identifiable, Hashable {
    var file: APIClient.MemoryFile
    var name: String?
    var description: String?
    var type: String?
    var body: String
    var id: String { file.name }

    static let index = "MEMORY.md"

    init(_ file: APIClient.MemoryFile) {
        self.file = file
        body = file.content
        let lines = file.content.components(separatedBy: "\n")
        guard lines.first?.trimmingCharacters(in: .whitespaces) == "---",
              let end = lines.dropFirst().firstIndex(where: { $0.trimmingCharacters(in: .whitespaces) == "---" }) else { return }
        for line in lines[1..<end] {
            guard let colon = line.firstIndex(of: ":") else { continue }
            let key = line[..<colon].trimmingCharacters(in: .whitespaces)
            var value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
            if value.count >= 2, let q = value.first, q == "\"" || q == "'", value.last == q { value = String(value.dropFirst().dropLast()) }
            guard !value.isEmpty else { continue }
            switch key { // `type` sits under `metadata:` (indented); older files have it at the top level
            case "name": name = value
            case "description": description = value
            case "type": type = value
            default: break
            }
        }
        body = lines[(end + 1)...].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var typeTitle: String? {
        switch type {
        case "user": String(localized: "User", comment: "Memory kind")
        case "feedback": String(localized: "Feedback", comment: "Memory kind")
        case "project": String(localized: "Project", comment: "Memory kind")
        case "reference": String(localized: "Reference", comment: "Memory kind")
        default: type
        }
    }

    var typeColor: Color {
        switch type {
        case "user": Theme.Hue.purple
        case "feedback": Theme.Hue.orange
        case "project": Theme.Hue.green
        case "reference": Theme.Hue.blue
        default: .secondary
        }
    }
}

struct AssistantMemoryView: View {
    @State private var page: AssistantMemoryModel
    /// The health space's memory (Health › Health Memory, docs/assistant/health.md) instead of the assistant's.
    private var health: Bool { page.space == Session.healthSpace }

    init(space: String? = nil) { _page = State(initialValue: AssistantMemoryModel(space: space)) }
    @State private var pendingDelete: AssistantMemoryEntry?
    @State private var debugOpen: AssistantMemoryEntry?

    private var files: [APIClient.MemoryFile] { page.files }
    private var loaded: Bool { page.loaded }
    private var error: String? { page.error }

    private var entries: [AssistantMemoryEntry] {
        files.filter { $0.name != AssistantMemoryEntry.index }.map(AssistantMemoryEntry.init)
            .sorted { ($0.file.updatedAt ?? .distantPast) > ($1.file.updatedAt ?? .distantPast) }
    }
    private var index: APIClient.MemoryFile? { files.first { $0.name == AssistantMemoryEntry.index } }

    /// The index lines "- [Title](file.md) — description", by file: friendlier titles than the slugs in the frontmatter.
    private var indexTitles: [String: (title: String, hook: String?)] {
        guard let index else { return [:] }
        var out: [String: (String, String?)] = [:]
        for line in index.content.split(whereSeparator: \.isNewline) {
            guard let open = line.firstIndex(of: "["), let close = line[open...].firstIndex(of: "]"),
                  line[close...].hasPrefix("]("), let paren = line[close...].firstIndex(of: ")") else { continue }
            let title = String(line[line.index(after: open)..<close])
            let file = String(line[line.index(close, offsetBy: 2)..<paren])
            let rest = line[line.index(after: paren)...].trimmingCharacters(in: .whitespaces)
            let hook = rest.drop { "—–-: ".contains($0) }.trimmingCharacters(in: .whitespaces)
            out[file] = (title, hook.isEmpty ? nil : hook)
        }
        return out
    }

    var body: some View {
        let entries = entries, titles = indexTitles
        List {
            if !entries.isEmpty {
                Section {
                    ForEach(entries) { e in
                        let t = titles[e.file.name]
                        NavigationLink {
                            AssistantMemoryDetail(entry: e, title: t?.title, space: page.space, onSaved: { await load() })
                        } label: {
                            row(e, title: t?.title ?? e.name ?? e.file.name, detail: e.description ?? t?.hook)
                        }
                        .swipeActions(edge: .trailing) {
                            Button { pendingDelete = e } label: { Label("Delete", systemImage: Theme.Icon.delete) }.tint(Theme.danger)
                        }
                        #if os(macOS)
                        // A swipe needs a trackpad: the right click deletes too.
                        .contextMenu {
                            Button(role: .destructive) { pendingDelete = e } label: { Label("Delete", systemImage: Theme.Icon.delete) }
                        }
                        #endif
                    }
                } footer: {
                    Text(health ? String(localized: "What's noted in health chats is read only by health chats; Persona's other chats can't see it. Fix anything wrong right here, or tell it in a health chat.")
                                : String(localized: "What Persona remembers, one file per memory, read at the start of each new chat. Fix anything that's wrong here, or tell it in a chat."))
                }
            }
            if let index, !entries.isEmpty {
                Section {
                    NavigationLink {
                        AssistantMemoryDetail(entry: AssistantMemoryEntry(index), title: String(localized: "Index"), space: page.space, onSaved: nil)
                    } label: {
                        Label { Text("Index").foregroundStyle(Color.primary) } icon: { ThemedIcon(Theme.Icon.list) }
                    }
                } footer: {
                    Text("MEMORY.md: the table of contents Persona keeps itself; it's updated when you delete a memory.")
                }
            }
        }
        .themedPage()
        .overlay {
            if !loaded {
                ProgressView()
            } else if let error, files.isEmpty {
                ContentUnavailableView("Couldn't Load Memory", systemImage: Theme.Icon.error, description: Text(error))
            } else if entries.isEmpty {
                ContentUnavailableView("No Memories Yet", systemImage: Theme.Icon.memory,
                                       description: Text(health ? String(localized: "Goals, allergies, medications and more noted in health chats appear here.")
                                                                : String(localized: "Persona keeps your preferences and the corrections you've made here.")))
            }
        }
        .pageTitle(health ? String(localized: "Health Memory") : String(localized: "Memory [Assistant Profile View 7a6335]"))
        .confirmationDialog("Delete This Memory?", isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
                            titleVisibility: .visible, presenting: pendingDelete) { e in
            Button("Delete", role: .destructive) { Task { await delete(e) } }
        } message: { e in
            Text("\(health ? String(localized: "Health Chats") : "Persona") will no longer remember “\(titles[e.file.name]?.title ?? e.name ?? e.file.name)”.")
        }
        .navigationDestination(item: $debugOpen) { e in
            AssistantMemoryDetail(entry: e, title: titles[e.file.name]?.title, space: page.space, onSaved: { await load() })
        }
        .task { await load() }
        .refreshable { await load() }
    }

    private func row(_ e: AssistantMemoryEntry, title: String, detail: String?) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Text(title).font(Theme.Font.heading).lineLimit(1)
                Spacer(minLength: 4)
                if let t = e.typeTitle { AssistantMemoryTag(title: t, color: e.typeColor) }
            }
            if let detail {
                Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    private func load() async {
        await page.load()
        #if DEBUG
        if ["memoryFile", "memoryEdit"].contains(UserDefaults.standard.string(forKey: "openAssistantPage")), debugOpen == nil {
            debugOpen = entries.first
        }
        #endif
    }

    private func delete(_ e: AssistantMemoryEntry) async { await page.delete(e.file.name) }
}

struct AssistantMemoryTag: View {
    var title: String
    var color: Color

    var body: some View {
        if AppStyle.current.usesBeautifulUI {
            BUIHueTag(title, base: color) // the kind's words on its own tint
        } else {
            Text(title)
                .font(Theme.Font.labelStrong)
                .lineLimit(1)
                .foregroundStyle(color)
                .padding(.horizontal, Theme.Space.s)
                .padding(.vertical, Theme.Space.xxs)
                .background(Capsule().fill(color.opacity(0.14)))
        }
    }
}

/// One memory, rendered; Edit opens the whole file (frontmatter included) as plain text. `onSaved == nil`: read-only.
struct AssistantMemoryDetail: View {
    @Environment(AppModel.self) private var model
    @State var entry: AssistantMemoryEntry
    var title: String?
    /// Whose memory it is (`AssistantMemoryModel.space`): saved back to the same one.
    var space: String? = nil
    var onSaved: (() async -> Void)?
    @State private var editing = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                if onSaved != nil {
                    VStack(alignment: .leading, spacing: Theme.Space.sm) {
                        HStack(spacing: Theme.Space.s) {
                            if let t = entry.typeTitle { AssistantMemoryTag(title: t, color: entry.typeColor) }
                            if let at = entry.file.updatedAt {
                                Text("Updated \(Fmt.relative(at))").font(Theme.Font.label).foregroundStyle(.secondary)
                            }
                        }
                        if let d = entry.description {
                            Text(d).font(Theme.Font.secondary).foregroundStyle(.secondary)
                        }
                    }
                    Divider()
                }
                Markdown(entry.body.isEmpty ? String(localized: "(Empty)") : Links.markdown(entry.body))
                    .markdownTheme(.persona)
                    .textSelection(.enabled)
                    .environment(\.openURL, OpenURLAction { url in
                        #if os(macOS)
                        url.scheme == nil ? .discarded : LinkPolicy.system(url) // NSWorkspace would run any scheme
                        #else
                        url.scheme == nil ? .discarded : .systemAction
                        #endif
                    })
                Text(entry.file.name).font(Theme.Font.code).foregroundStyle(.tertiary).padding(.top, Theme.Space.s)
            }
            .padding(Theme.Space.xl)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .themedPage()
        .pageTitle(title ?? entry.name ?? entry.file.name)
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            if onSaved != nil {
                ToolbarItem(placement: .primaryAction) { Button("Edit [Message Menu 051836]") { editing = true } }
            }
        }
        .sheet(isPresented: $editing) {
            AssistantMemoryEditor(name: entry.file.name, text: entry.file.content, space: space) { content in
                var f = entry.file
                f.content = content
                f.updatedAt = .now
                entry = AssistantMemoryEntry(f)
                await onSaved?()
            }
        }
        #if DEBUG
        .onAppear { if UserDefaults.standard.string(forKey: "openAssistantPage") == "memoryEdit", onSaved != nil { editing = true } }
        #endif
    }
}

struct AssistantMemoryEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var name: String
    @State var text: String
    var space: String? = nil
    var saved: (String) async -> Void
    @State private var saving = false
    /// The text as the sheet opened: Cancel asks first once it differs (Mac).
    @State private var opened: String?

    var body: some View {
        NavigationStack {
            TextEditor(text: $text)
                .font(Theme.Font.body)
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)
                .scrollContentBackground(.hidden)
                .padding(.horizontal, Theme.Space.m)
                .background(Theme.pageBackground)
                .navigationTitle(name)
                .navigationBarTitleDisplayMode(.inline)
                .sheetCancel(unsaved: opened.map { $0 != text } ?? false) { dismiss() }
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button { Task { await save() } } label: { if saving { ProgressView() } else { Text("Save") } }
                            .disabled(saving)
                    }
                }
                .onAppear { if opened == nil { opened = text } }
        }
        .interactiveDismissDisabled(saving)
        #if os(macOS)
        // A Mac sheet sizes to its content: a text editor alone came up a few lines tall.
        .macSheet()
        #endif
    }

    private func save() async {
        guard let api = model.api else { return }
        saving = true
        defer { saving = false }
        do {
            try await api.saveMemory(name: name, content: text, space: space)
            await saved(text)
            Haptics.success()
            dismiss()
        } catch {
            model.show(error)
        }
    }
}

// MARK: - About Me

/// What the assistant is told about the user; saved when leaving the page (or with Save).
struct AssistantNotesView: View {
    @Environment(AppModel.self) private var model
    @State private var text = ""
    @State private var original: String?
    @State private var saving = false
    @FocusState private var focused: Bool

    private var changed: Bool { original != nil && text != original }

    var body: some View {
        Form {
            Section {
                TextEditor(text: $text)
                    .font(Theme.Font.body)
                    .scaledFrame(minHeight: Theme.Size.box380)
                    .focused($focused)
                    .listRowInsets(EdgeInsets(top: 8, leading: 12, bottom: 8, trailing: 12))
            } footer: {
                Text("This is added to the end of Persona's instructions and applies to new chats. You might note where your code lives, which skills you use, which machines or folders to leave alone, and how you like to get updates.")
            }
        }
        .themedPage()
        .pageTitle(String(localized: "About Me"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button { Task { await save() } } label: { if saving { ProgressView() } else { Text("Save") } }
                    .disabled(!changed || saving)
            }
        }
        .task {
            guard original == nil else { return }
            if model.assistantConfig == nil { await model.refreshAssistant() }
            text = model.assistantConfig?.notes ?? ""
            original = text
        }
        .onDisappear { if changed { Task { await save() } } }
    }

    private func save() async {
        guard let api = model.api, changed else { return }
        saving = true
        defer { saving = false }
        let value = text
        do {
            _ = try await api.setAssistant(notes: value)
            original = value
            focused = false
            await model.refreshAssistant()
            Haptics.success()
            model.toast = Toast(String(localized: "Saved; applies to new chats"), style: .success)
        } catch {
            model.show(error)
        }
    }
}

/// A task on Scheduled Tasks' list: in the BeautifulUI style its capsule stands on the page (no list background or line);
/// otherwise the list's row.
private struct AssistantTaskRowLook: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI {
            content
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.l, bottom: Theme.Space.xs, trailing: Theme.Space.l))
        } else {
            content
        }
    }
}
