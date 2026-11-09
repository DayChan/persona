import SwiftUI

// The pieces of the to-dos' calendar (#2029, docs/assistant/todos.md "Calendar"): an entry's block and chip, the day
// view's week strip, the time axis of a day or a week, and the month. What goes where is `TodoCalendar` (Shared).

/// Where a new to-do made on the calendar is due: at `time`, or all day on `day` when it is nil.
struct TodoCalendarSlot: Identifiable, Equatable {
    var day: Date
    var time: Date?
    var id: Date { time ?? day }
}

/// An entry's colors, as the list draws its to-do: its state's, red once overdue (before the state: what is late is
/// what to see first), faded when done.
struct CalendarEntryLook {
    var entry: CalendarEntry

    var color: Color { entry.overdue ? Theme.danger : entry.state.todoTone.dot }
    var text: Color { entry.overdue ? Theme.danger : entry.state.todoTone.text }
    var ground: Color { color.opacity(Theme.CalendarGrid.tint) }
    var fade: Double { entry.done ? Theme.CalendarGrid.doneFade : 1 }

    /// "18:00", or "Was due 11:30" once it has passed.
    var time: String {
        let t = entry.start.formatted(date: .omitted, time: .shortened)
        return entry.overdue ? String(localized: "Was due \(t)") : t
    }

    /// What VoiceOver reads: the title, its state and when.
    var spoken: String {
        let when = entry.allDay ? String(localized: "All Day") : time
        return [entry.title, entry.stateLabel, when].joined(separator: ", ")
    }
}

/// High priority and a pending reminder, as on the list's row.
struct CalendarEntryMarks: View {
    var entry: CalendarEntry

    var body: some View {
        if entry.highPriority {
            ThemedIcon(Theme.Icon.highPriority)
                .foregroundStyle(Theme.caution)
        }
        if entry.reminding {
            ThemedIcon(Theme.Icon.reminder)
                .foregroundStyle(.secondary)
        }
    }
}

/// A to-do on the time axis: its top edge is when it is due. `narrow` (a phone's week) keeps the title alone.
struct CalendarEntryBlock: View {
    var entry: CalendarEntry
    var narrow = false

    var body: some View {
        let look = CalendarEntryLook(entry: entry)
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if narrow {
                Text(entry.title)
                    .font(Theme.Font.labelStrong)
                    .lineLimit(2)
                    .strikethrough(entry.done)
                if entry.highPriority {
                    ThemedIcon(Theme.Icon.highPriority)
                        .font(Theme.Font.label)
                        .foregroundStyle(Theme.caution)
                }
            } else {
                HStack(spacing: Theme.Space.xs) {
                    Text(entry.title)
                        .font(Theme.Font.captionStrong)
                        .lineLimit(1)
                        .strikethrough(entry.done)
                    CalendarEntryMarks(entry: entry)
                        .font(Theme.Font.label)
                }
                HStack(spacing: Theme.Space.xs) {
                    Text(entry.stateLabel)
                        .foregroundStyle(look.text)
                    Text(verbatim: "· \(look.time)")
                        .foregroundStyle(entry.overdue ? Theme.danger : Color.secondary)
                }
                .font(Theme.Font.label)
                .lineLimit(1)
            }
        }
        .padding(.leading, Theme.Space.s)
        .padding(.trailing, Theme.Space.xxs)
        .padding(.vertical, Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(alignment: .leading) {
            Rectangle().fill(look.color).frame(width: Theme.Size.bar)
        }
        .background(look.ground)
        .clipShape(.rect(cornerRadius: Theme.Radius.badge))
        .opacity(look.fade)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(look.spoken)
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("todo.calendar.entry")
    }
}

/// Several to-dos whose blocks would overlap in a phone's narrow week column: one block that says how many, in the
/// colors of each; tapping it opens their day.
struct CalendarGroupBlock: View {
    var entries: [CalendarEntry]

    var body: some View {
        let first = CalendarEntryLook(entry: entries[0])
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.xxs) {
                ForEach(entries.prefix(Theme.CalendarGrid.dots)) { e in
                    Circle().fill(CalendarEntryLook(entry: e).color)
                        .frame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                }
            }
            Text("\(entries.count) to-dos")
                .font(Theme.Font.labelStrong)
                .lineLimit(1)
        }
        .padding(.leading, Theme.Space.s)
        .padding(.vertical, Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(alignment: .leading) {
            Rectangle().fill(first.color).frame(width: Theme.Size.bar)
        }
        .background(first.ground)
        .clipShape(.rect(cornerRadius: Theme.Radius.badge))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(entries.map(\.title).joined(separator: ", "))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("todo.calendar.group")
    }
}

/// An entry in a row of its own: the all-day row, a day of the month. `compact` (a phone's week or month) is a few
/// characters with a thin bar.
struct CalendarEntryChip: View {
    var entry: CalendarEntry
    var compact = false
    var showsTime = false

    var body: some View {
        let look = CalendarEntryLook(entry: entry)
        HStack(spacing: Theme.Space.xs) {
            if showsTime && !entry.allDay {
                Text(entry.start.formatted(date: .omitted, time: .shortened))
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            Text(entry.title)
                .foregroundStyle(entry.overdue ? Theme.danger : Color.primary)
                .strikethrough(entry.done)
                .lineLimit(1)
            if !compact {
                CalendarEntryMarks(entry: entry)
            }
        }
        .font(compact ? Theme.Font.label : Theme.Font.caption)
        .padding(.leading, compact ? Theme.Space.xs : Theme.Space.s)
        .padding(.trailing, Theme.Space.xxs)
        .padding(.vertical, Theme.Space.xxs)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(alignment: .leading) {
            Rectangle().fill(look.color).frame(width: compact ? Theme.Size.rule : Theme.Size.bar)
        }
        .background(look.ground)
        .clipShape(.rect(cornerRadius: compact ? Theme.Radius.thumbnail : Theme.Radius.badge))
        .opacity(look.fade)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(look.spoken)
        .accessibilityIdentifier("todo.calendar.entry")
    }
}

/// The day view's week: tap a day to show it; under each date a dot for each of its open to-dos (a few), in their colors.
struct TodoCalendarWeekStrip: View {
    var anchor: Date
    var entries: [CalendarEntry]
    var pick: (Date) -> Void

    private let calendar = Calendar.current

    var body: some View {
        let days = TodoCalendar.days(.week, anchor: anchor, calendar: calendar)
        let names = TodoCalendar.weekdaySymbols(calendar: calendar, narrow: true)
        HStack(spacing: Theme.Space.flush) {
            ForEach(Array(days.enumerated()), id: \.offset) { i, day in
                let picked = calendar.isDate(day, inSameDayAs: anchor)
                let today = calendar.isDateInToday(day)
                let open = TodoCalendar.on(day, entries, calendar: calendar).filter { !$0.done }
                Button { pick(day) } label: {
                    VStack(spacing: Theme.Space.xxs) {
                        Text(names[i])
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                        Text(verbatim: String(calendar.component(.day, from: day)))
                            .font(picked ? Theme.Font.bodyStrong : Theme.Font.body)
                            .foregroundStyle(picked ? Theme.onSolid : today ? Theme.accent : Color.primary)
                            .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                            .background {
                                if picked { Circle().fill(Theme.accent) }
                            }
                        HStack(spacing: Theme.Space.xxs) {
                            ForEach(open.prefix(Theme.CalendarGrid.dots)) { e in
                                Circle().fill(CalendarEntryLook(entry: e).color)
                                    .frame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                            }
                        }
                        .frame(height: Theme.Size.dotXS)
                    }
                    .frame(maxWidth: .infinity)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(day.formatted(date: .complete, time: .omitted))
                .accessibilityValue(open.isEmpty ? "" : String(localized: "\(open.count) to-dos"))
                .accessibilityAddTraits(picked ? .isSelected : [])
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.bottom, Theme.Space.s)
        .accessibilityIdentifier("todo.calendar.strip")
    }
}

/// A day or a week on a time axis: the all-day row on top, then the hours, each to-do at its due time, side by side
/// where they overlap; a red line at the moment. Tapping the empty axis offers a new to-do due there.
struct TodoCalendarTimeline: View {
    var days: [Date]
    var entries: [CalendarEntry]
    /// A phone's week: the hours alone in the gutter, a few characters a block, overlapping ones as one.
    var narrow: Bool
    /// The week's row of days (the day view has its strip instead).
    var showsDayHeader: Bool
    var open: (CalendarEntry) -> Void
    var pickDay: (Date) -> Void
    var add: (TodoCalendarSlot) -> Void

    @Environment(\.displayScale) private var displayScale
    private let calendar = Calendar.current
    private let hour = Theme.CalendarGrid.hour
    /// One device pixel: the grid's lines (0.5 pt is nothing on a 1x screen).
    private var pixel: CGFloat { 1 / displayScale }
    private var gutter: CGFloat { narrow ? Theme.CalendarGrid.gutterNarrow : Theme.CalendarGrid.gutter }
    /// How long a to-do's block is on the axis.
    private var blockMinutes: Double { Double(Theme.Size.tapTarget / hour) * 60 }

    var body: some View {
        VStack(spacing: Theme.Space.flush) {
            if showsDayHeader { dayHeader }
            allDayRow
            Rectangle().fill(Theme.CalendarGrid.lineStrong).frame(height: pixel)
            ScrollViewReader { proxy in
                ScrollView(.vertical) {
                    TimelineView(.everyMinute) { context in
                        axis(now: context.date)
                    }
                }
                .scrollIndicators(.hidden)
                .onAppear { proxy.scrollTo(firstHour, anchor: .top) }
                .onChange(of: days.first) { proxy.scrollTo(firstHour, anchor: .top) }
                // the page may open before the to-dos are read: once they are, the morning's come into sight
                .onChange(of: timed.isEmpty) { _, empty in if !empty { proxy.scrollTo(firstHour, anchor: .top) } }
            }
        }
    }

    private var timed: [CalendarEntry] { entries.filter { !$0.allDay } }

    private var firstHour: Int {
        TodoCalendar.firstHour(days: days, timed: timed, now: .now, calendar: calendar)
    }

    // MARK: Header and all-day row

    private var dayHeader: some View {
        let names = TodoCalendar.weekdaySymbols(calendar: calendar, narrow: narrow)
        return HStack(spacing: Theme.Space.flush) {
            Spacer().frame(width: gutter)
            ForEach(Array(days.enumerated()), id: \.offset) { i, day in
                let today = calendar.isDateInToday(day)
                Button { pickDay(day) } label: {
                    let number = Text(verbatim: String(calendar.component(.day, from: day)))
                        .font(today ? Theme.Font.bodyStrong : Theme.Font.body)
                        .foregroundStyle(today ? Theme.onSolid : Color.primary)
                        .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                        .background {
                            if today { Circle().fill(Theme.accent) }
                        }
                    Group {
                        if narrow {
                            VStack(spacing: Theme.Space.xxs) {
                                Text(names[i]).font(Theme.Font.label).foregroundStyle(.secondary)
                                number
                            }
                            .frame(maxWidth: .infinity)
                        } else {
                            HStack(spacing: Theme.Space.xs) {
                                Text(names[i]).font(Theme.Font.caption).foregroundStyle(.secondary)
                                number
                            }
                            .padding(.leading, Theme.Space.s)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(day.formatted(date: .complete, time: .omitted))
                .accessibilityIdentifier("todo.calendar.dayHeader")
            }
        }
        .padding(.bottom, Theme.Space.xs)
    }

    private var allDayRow: some View {
        HStack(alignment: .top, spacing: Theme.Space.flush) {
            Text("All Day")
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .padding(.trailing, Theme.Space.sm)
                .frame(width: gutter, alignment: .trailing)
                .padding(.top, Theme.Space.xs)
            ForEach(Array(days.enumerated()), id: \.offset) { _, day in
                let all = TodoCalendar.on(day, entries, calendar: calendar).filter(\.allDay)
                let fit: (shown: [CalendarEntry], more: Int) = narrow ? TodoCalendar.fit(all, lines: 2) : (all, 0)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    ForEach(fit.shown) { e in
                        Button { open(e) } label: { CalendarEntryChip(entry: e, compact: narrow) }
                            .buttonStyle(.plain)
                    }
                    if fit.more > 0 {
                        Text(verbatim: "+\(fit.more)")
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal, Theme.Space.xxs)
                .padding(.vertical, Theme.Space.xs)
                .frame(maxWidth: .infinity, minHeight: Theme.Size.iconS, alignment: .topLeading)
                .contentShape(.rect)
                .onTapGesture { add(TodoCalendarSlot(day: day, time: nil)) }
            }
        }
    }

    // MARK: The axis

    private func axis(now: Date) -> some View {
        let todayShown = days.contains { calendar.isDate($0, inSameDayAs: now) }
        let nowMinutes = TodoCalendar.minutes(now, in: now, calendar: calendar)
        return ZStack(alignment: .topLeading) {
            VStack(spacing: Theme.Space.flush) {
                ForEach(0..<24, id: \.self) { h in
                    hourRow(h, hideLabel: todayShown && abs(Double(h * 60) - nowMinutes) < Double(TodoCalendar.nowLabelRoom))
                        .id(h)
                }
            }
            GeometryReader { geo in
                let column = (geo.size.width - gutter) / CGFloat(max(days.count, 1))
                ForEach(Array(days.enumerated()), id: \.offset) { i, day in
                    dayColumn(day, width: column, now: now)
                        .frame(width: column, height: hour * 24)
                        .offset(x: gutter + column * CGFloat(i))
                }
            }
        }
        .frame(height: hour * 24)
        // over the axis, so its height (half above the line) moves nothing under it
        .overlay(alignment: .topLeading) {
            if todayShown {
                Text(now.formatted(date: .omitted, time: .shortened))
                    .font(Theme.Font.labelHeavy)
                    .foregroundStyle(Theme.danger)
                    .lineLimit(1)
                    .padding(.trailing, Theme.Space.xs)
                    .frame(width: gutter, alignment: .trailing)
                    .alignmentGuide(.top) { d in d[VerticalAlignment.center] }
                    .offset(y: y(nowMinutes))
            }
        }
    }

    /// An hour of the axis: its line at the top, its time centered on the line. Both over the row, so the hour's line is
    /// exactly where the to-dos due at that hour start, with a label or without.
    private func hourRow(_ h: Int, hideLabel: Bool) -> some View {
        Color.clear
            .frame(height: hour)
            .overlay(alignment: .top) {
                Rectangle().fill(Theme.CalendarGrid.line)
                    .frame(height: pixel)
                    .padding(.leading, gutter)
            }
            .overlay(alignment: .topLeading) {
                if h > 0 && !hideLabel {
                    Text(hourLabel(h))
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                        .lineLimit(1)
                        .padding(.trailing, Theme.Space.sm)
                        .frame(width: gutter, alignment: .trailing)
                        .alignmentGuide(.top) { d in d[VerticalAlignment.center] }
                }
            }
    }

    private func hourLabel(_ h: Int) -> String {
        let date = calendar.date(bySettingHour: h, minute: 0, second: 0, of: days.first ?? .now) ?? .now
        // a phone's week has the hour alone ("14", not the formatter's hour with its Chinese unit, which the narrow gutter cut short)
        return narrow ? String(h) : date.formatted(date: .omitted, time: .shortened)
    }

    private func y(_ minutes: Double) -> CGFloat { CGFloat(minutes / 60) * hour }

    @ViewBuilder private func dayColumn(_ day: Date, width: CGFloat, now: Date) -> some View {
        let mine = TodoCalendar.on(day, timed, calendar: calendar)
        ZStack(alignment: .topLeading) {
            Color.clear
                .contentShape(.rect)
                .gesture(SpatialTapGesture().onEnded { tap in
                    let minutes = Double(tap.location.y / hour) * 60
                    add(TodoCalendarSlot(day: day, time: TodoCalendar.slot(day: day, minutes: minutes, snap: Theme.CalendarGrid.snapMinutes, calendar: calendar)))
                })
                .accessibilityIdentifier("todo.calendar.axis")
            if days.count > 1 {
                Rectangle().fill(Theme.CalendarGrid.line).frame(width: pixel)
            }
            if narrow {
                ForEach(TodoCalendar.clusters(mine, minutes: blockMinutes), id: \.first?.id) { group in
                    let start = TodoCalendar.minutes(group[0].start, in: day, calendar: calendar)
                    Group {
                        if group.count == 1 {
                            Button { open(group[0]) } label: { CalendarEntryBlock(entry: group[0], narrow: true) }
                        } else {
                            Button { pickDay(day) } label: { CalendarGroupBlock(entries: group) }
                        }
                    }
                    .buttonStyle(.plain)
                    .frame(width: width - Theme.Space.xs, height: Theme.Size.tapTarget - Theme.Space.xxs)
                    .offset(x: Theme.Space.xxs, y: y(start) + pixel)
                }
            } else {
                ForEach(TodoCalendar.lanes(mine, minutes: blockMinutes)) { placed in
                    let lane = (width - Theme.Space.xs) / CGFloat(placed.lanes)
                    let start = TodoCalendar.minutes(placed.entry.start, in: day, calendar: calendar)
                    Button { open(placed.entry) } label: { CalendarEntryBlock(entry: placed.entry) }
                        .buttonStyle(.plain)
                        .frame(width: lane - Theme.Space.xxs, height: Theme.Size.tapTarget - Theme.Space.xxs)
                        .offset(x: Theme.Space.xxs + lane * CGFloat(placed.lane), y: y(start) + pixel)
                }
            }
            if calendar.isDate(now, inSameDayAs: day) {
                let at = y(TodoCalendar.minutes(now, in: day, calendar: calendar))
                Rectangle().fill(Theme.danger)
                    .frame(height: Theme.Size.lineMedium)
                    .offset(y: at)
                    .allowsHitTesting(false)
                Circle().fill(Theme.danger)
                    .frame(width: Theme.Size.dot, height: Theme.Size.dot)
                    .offset(x: -Theme.Size.dot / 2, y: at - Theme.Size.dot / 2 + Theme.Size.lineMedium / 2)
                    .allowsHitTesting(false)
            }
        }
    }
}

/// A month: the weeks it touches, each day with its first entries and how many more; tapping a day opens it.
struct TodoCalendarMonth: View {
    var anchor: Date
    var entries: [CalendarEntry]
    var wide: Bool
    var open: (CalendarEntry) -> Void
    var pickDay: (Date) -> Void

    @Environment(\.displayScale) private var displayScale
    private let calendar = Calendar.current
    /// One device pixel: the grid's lines.
    private var pixel: CGFloat { 1 / displayScale }

    var body: some View {
        let days = TodoCalendar.days(.month, anchor: anchor, calendar: calendar)
        let rows = max(days.count / 7, 1)
        let names = TodoCalendar.weekdaySymbols(calendar: calendar, narrow: !wide)
        VStack(spacing: Theme.Space.flush) {
            HStack(spacing: Theme.Space.flush) {
                ForEach(names.indices, id: \.self) { i in
                    Text(names[i])
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: wide ? .leading : .center)
                        .padding(.leading, wide ? Theme.Space.s : Theme.Space.flush)
                }
            }
            .padding(.bottom, Theme.Space.xs)
            GeometryReader { geo in
                let cell = max(Theme.CalendarGrid.monthCell, geo.size.height / CGFloat(rows))
                ScrollView(.vertical) {
                    VStack(spacing: Theme.Space.flush) {
                        ForEach(0..<rows, id: \.self) { r in
                            HStack(spacing: Theme.Space.flush) {
                                ForEach(0..<7, id: \.self) { c in
                                    dayCell(days[r * 7 + c])
                                        .frame(maxWidth: .infinity)
                                        .frame(height: cell)
                                        .overlay(alignment: .leading) {
                                            if c > 0 { Rectangle().fill(Theme.CalendarGrid.line).frame(width: pixel) }
                                        }
                                }
                            }
                            .overlay(alignment: .top) {
                                Rectangle().fill(Theme.CalendarGrid.line).frame(height: pixel)
                            }
                        }
                    }
                }
                .scrollIndicators(.hidden)
                .scrollDisabled(cell * CGFloat(rows) <= geo.size.height)
            }
        }
    }

    private func dayCell(_ day: Date) -> some View {
        let inMonth = calendar.isDate(day, equalTo: anchor, toGranularity: .month)
        let today = calendar.isDateInToday(day)
        let fit = TodoCalendar.fit(TodoCalendar.on(day, entries, calendar: calendar), lines: Theme.CalendarGrid.monthLines)
        return VStack(alignment: wide ? .leading : .center, spacing: Theme.Space.xxs) {
            Text(verbatim: String(calendar.component(.day, from: day)))
                .font(today ? Theme.Font.captionHeavy : Theme.Font.caption)
                .foregroundStyle(today ? Theme.onSolid : inMonth ? Color.primary : Color.secondary)
                .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                .background {
                    if today { Circle().fill(Theme.accent) }
                }
                .frame(maxWidth: .infinity, alignment: wide ? .leading : .center)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                ForEach(fit.shown) { e in
                    if wide {
                        Button { open(e) } label: { CalendarEntryChip(entry: e, showsTime: true) }
                            .buttonStyle(.plain)
                    } else {
                        CalendarEntryChip(entry: e, compact: true)
                    }
                }
                if fit.more > 0 {
                    Text(wide ? String(localized: "\(fit.more) more") : "+\(fit.more)")
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .padding(.leading, Theme.Space.xs)
                }
            }
        }
        .padding(.horizontal, Theme.Space.xxs)
        .padding(.top, Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .opacity(inMonth ? 1 : Theme.CalendarGrid.outsideFade)
        .contentShape(.rect)
        .onTapGesture { pickDay(day) }
        .accessibilityElement(children: wide ? .contain : .combine)
        .accessibilityLabel(day.formatted(date: .complete, time: .omitted))
        .accessibilityIdentifier("todo.calendar.monthDay")
    }
}
