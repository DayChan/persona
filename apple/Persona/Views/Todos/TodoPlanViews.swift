import SwiftUI

// A to-do's due date, reminder and priority (#1683, docs/assistant/todos.md "Due date, reminder, priority"): a line on
// the list's rows and a section on the detail page, where each change is saved as it is made.

/// How a to-do's due date reads: "Thu, Nov 12, 6:00 PM", or the day alone; the year only when it is not this one.
nonisolated enum TodoPlanText {
    static func due(_ todo: TodoItem, now: Date = .now, calendar: Calendar = .current) -> String? {
        if let at = todo.dueAt { return format(at, time: true, now: now, calendar: calendar) }
        if let day = TodosLogic.dueDay(todo, calendar: calendar) { return format(day, time: false, now: now, calendar: calendar) }
        return nil
    }

    static func moment(_ date: Date, now: Date = .now, calendar: Calendar = .current) -> String {
        format(date, time: true, now: now, calendar: calendar)
    }

    private static func format(_ date: Date, time: Bool, now: Date, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).weekday(.abbreviated).month(.abbreviated).day()
        if calendar.component(.year, from: date) != calendar.component(.year, from: now) { style = style.year() }
        if time { style = style.hour().minute() }
        return date.formatted(style)
    }

    /// The day a `due_date` names, written as the hub reads it ("yyyy-MM-dd").
    static func day(_ date: Date, calendar: Calendar = .current) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0) // i18n-ignore: wire format
    }
}

/// The due date, a pending reminder and high priority under a to-do's title in the list; nothing when it has none of them.
struct TodoPlanLine: View {
    var todo: TodoItem

    var body: some View {
        let due = TodoPlanText.due(todo)
        let remind = todo.remindAt != nil && todo.remindedAt == nil && !todo.isDone
        let high = todo.priority == "high" && !todo.isDone
        if due != nil || remind || high {
            HStack(spacing: Theme.Space.sm) {
                if high {
                    ThemedIcon(Theme.Icon.highPriority)
                        .foregroundStyle(Theme.caution)
                        .accessibilityLabel(Text("High priority"))
                }
                if let due {
                    let overdue = TodosLogic.isOverdue(todo)
                    HStack(spacing: Theme.Space.xs) {
                        ThemedIcon(Theme.Icon.dueDate)
                        Text(overdue ? "Was due \(due)" : "Due \(due)")
                            .lineLimit(1)
                    }
                    .foregroundStyle(overdue ? Theme.danger : Color.secondary)
                }
                if remind, let at = todo.remindAt {
                    ThemedIcon(Theme.Icon.reminder)
                        .foregroundStyle(.secondary)
                        .accessibilityLabel(Text("Reminder at \(TodoPlanText.moment(at))"))
                }
            }
            .font(Theme.Font.caption)
            .accessibilityElement(children: .combine)
        }
    }
}

/// What the detail page's plan section edits, as the controls hold it (a date stays in its picker while it is off).
nonisolated struct TodoPlanDraft: Equatable {
    var hasDue = false
    var allDay = true
    var due = Date()
    var hasRemind = false
    var remind = Date()
    var priority = ""

    init(_ todo: TodoItem, now: Date = .now, calendar: Calendar = .current) {
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: now)) ?? now
        let evening = calendar.date(bySettingHour: 18, minute: 0, second: 0, of: tomorrow) ?? tomorrow
        let nextHour = calendar.nextDate(after: now, matching: DateComponents(minute: 0), matchingPolicy: .nextTime) ?? now
        if let at = todo.dueAt {
            hasDue = true
            allDay = false
            due = at
        } else if let day = TodosLogic.dueDay(todo, calendar: calendar) {
            hasDue = true
            due = calendar.date(bySettingHour: 18, minute: 0, second: 0, of: day) ?? day
        } else {
            due = evening
        }
        hasRemind = todo.remindAt != nil
        remind = todo.remindAt ?? nextHour
        priority = todo.priority ?? ""
    }

    /// The PATCH that makes `todo` what this draft says.
    func change(calendar: Calendar = .current) -> APIClient.TodoPlanChange {
        let d: APIClient.TodoPlanChange.Due = !hasDue ? .none : allDay ? .day(TodoPlanText.day(due, calendar: calendar)) : .at(due)
        return APIClient.TodoPlanChange(due: d, remindAt: .some(hasRemind ? remind : nil), priority: priority)
    }

    /// Whether two drafts say the same: the dates of a switch that is off do not count.
    func means(_ other: TodoPlanDraft) -> Bool {
        change() == other.change()
    }
}

/// The detail page's due date, reminder and priority. A change is saved shortly after it is made (a date picker
/// sends every step); the controls follow the to-do again once nothing is being saved.
struct TodoPlanSection: View {
    var todo: TodoItem
    @State private var store = TodoStore.shared
    @State private var draft: TodoPlanDraft
    @State private var saving: Task<Void, Never>?

    init(todo: TodoItem) {
        self.todo = todo
        _draft = State(initialValue: TodoPlanDraft(todo))
    }

    var body: some View {
        Section {
            Toggle(isOn: $draft.hasDue) {
                Label { Text("Due Date") } icon: { ThemedIcon(Theme.Icon.dueDate) }
            }
            .accessibilityIdentifier("todo.plan.due")
            if draft.hasDue {
                Toggle("All Day", isOn: $draft.allDay)
                    .accessibilityIdentifier("todo.plan.allDay")
                DatePicker("Due", selection: $draft.due, displayedComponents: draft.allDay ? [.date] : [.date, .hourAndMinute])
                    .accessibilityIdentifier("todo.plan.dueDate")
            }
            Toggle(isOn: $draft.hasRemind) {
                Label { Text("Remind Me") } icon: { ThemedIcon(Theme.Icon.reminder) }
            }
            .accessibilityIdentifier("todo.plan.remind")
            if draft.hasRemind {
                DatePicker("Remind At", selection: $draft.remind, in: min(draft.remind, .now)..., displayedComponents: [.date, .hourAndMinute])
                    .accessibilityIdentifier("todo.plan.remindAt")
                if let sent = todo.remindedAt, todo.remindAt == draft.remind {
                    Text("Reminded \(TodoPlanText.moment(sent))")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Picker(selection: $draft.priority) {
                Text("High").tag("high")
                Text("Normal").tag("")
                Text("Low").tag("low")
            } label: {
                Label { Text("Priority") } icon: { ThemedIcon(Theme.Icon.highPriority) }
            }
            .accessibilityIdentifier("todo.plan.priority")
        }
        .onChange(of: draft) { _, new in
            guard !new.means(TodoPlanDraft(todo)) else { return }
            save(new)
        }
        .onChange(of: todo) { _, now in
            if saving == nil { draft = TodoPlanDraft(now) }
        }
    }

    private func save(_ new: TodoPlanDraft) {
        saving?.cancel()
        let id = todo.id
        saving = Task {
            try? await Task.sleep(for: .milliseconds(400))
            guard !Task.isCancelled else { return }
            do {
                try await store.updatePlan(id: id, new.change())
            } catch {
                AppModel.shared.show(error)
            }
            saving = nil
        }
    }
}
