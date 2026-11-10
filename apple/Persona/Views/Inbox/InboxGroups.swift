import Foundation

/// Groups inside the inbox's sections (docs/assistant/inbox.md "Grouping and selection", #2059): Needs You, FYI and Collapsed by source or by
/// time (the switch at the top, kept on this device), Done always by when it was done. The same rules as the web's
/// lib/inbox-groups.ts. Pure: tests give `now` and the calendar.
nonisolated enum InboxGroups {
    enum Grouping: String { case source, time }

    struct Group: Identifiable, Equatable {
        /// Stable for the folds kept on this device: "lark", "mail:<account>", "c:<connector>", "today", "d:2026-11-06"…
        var key: String
        var title: String
        var items: [InboxItem]
        var id: String { key }
    }

    /// The time group of a moment: "just" (within the hour; Done only, so a slip is easy to find and undo), "today",
    /// "yesterday", "d:YYYY-MM-DD" for each of the 2 to 6 days before (the last week, not the calendar week), "week"
    /// (7 to 29 days ago) and "month" (30 days or more). Calendar days in `calendar`'s time zone; the future is today.
    static func timeKey(_ date: Date?, now: Date, recent: Bool = false, calendar: Calendar = .current) -> String {
        guard let date else { return "month" }
        if recent, now.timeIntervalSince(date) <= 3600 { return "just" }
        let days = max(0, calendar.dateComponents([.day], from: calendar.startOfDay(for: date), to: calendar.startOfDay(for: now)).day ?? 0)
        switch days {
        case 0: return "today"
        case 1: return "yesterday"
        case 2...6:
            let c = calendar.dateComponents([.year, .month, .day], from: date)
            return String(format: "d:%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
        case 7...29: return "week"
        default: return "month"
        }
    }

    /// A time group's title: "Just Now", "Today", "Yesterday", "Fri, Nov 6", "7+ Days Ago", "1+ Month Ago".
    static func timeTitle(_ key: String, calendar: Calendar = .current) -> String {
        switch key {
        case "just": return String(localized: "Done Just Now")
        case "today": return String(localized: "Today")
        case "yesterday": return String(localized: "Yesterday")
        case "week": return String(localized: "7+ Days Ago")
        case "month": return String(localized: "1+ Month Ago")
        default:
            guard let day = day(of: key, calendar: calendar) else { return key }
            return day.formatted(.dateTime.weekday(.abbreviated).month(.abbreviated).day())
        }
    }

    private static func day(of key: String, calendar: Calendar) -> Date? {
        let p = key.dropFirst(2).split(separator: "-").compactMap { Int($0) }
        guard key.hasPrefix("d:"), p.count == 3 else { return nil }
        return calendar.date(from: DateComponents(year: p[0], month: p[1], day: p[2]))
    }

    /// Where a time group goes: newest first.
    private static func rank(_ key: String, now: Date, calendar: Calendar) -> Int {
        switch key {
        case "just": return 0
        case "today": return 1
        case "yesterday": return 2
        case "week": return 100
        case "month": return 101
        default:
            guard let d = day(of: key, calendar: calendar) else { return 99 }
            return 2 + (calendar.dateComponents([.day], from: d, to: calendar.startOfDay(for: now)).day ?? 0)
        }
    }

    /// The items by time group, newest group first, each keeping the items' order.
    static func byTime(_ items: [InboxItem], now: Date, at: (InboxItem) -> Date?, recent: Bool = false, calendar: Calendar = .current) -> [Group] {
        var order: [String] = []
        var groups: [String: [InboxItem]] = [:]
        for it in items {
            let k = timeKey(at(it), now: now, recent: recent, calendar: calendar)
            if groups[k] == nil { order.append(k) }
            groups[k, default: []].append(it)
        }
        return order.sorted { rank($0, now: now, calendar: calendar) < rank($1, now: now, calendar: calendar) }
            .map { Group(key: $0, title: timeTitle($0, calendar: calendar), items: groups[$0] ?? []) }
    }

    /// An item's source group: Lark, each mail account, each connector or extension source (by its account), else its source.
    static func sourceKey(_ it: InboxItem) -> String {
        if it.connector != nil { return "c:\(it.account)" }
        if it.source == "mail" { return "mail:\(it.account)" }
        return it.source
    }

    /// The items by source in the filter chips' order (`order`: Lark, the mail accounts, the connectors); a source the
    /// chips don't name comes after them, in the order it first shows up.
    static func bySource(_ items: [InboxItem], order: [String], mailName: (String) -> String) -> [Group] {
        var seen: [String] = []
        var groups: [String: [InboxItem]] = [:]
        for it in items {
            let k = sourceKey(it)
            if groups[k] == nil { seen.append(k) }
            groups[k, default: []].append(it)
        }
        let rank = { (k: String) in order.firstIndex(of: k) ?? order.count }
        return seen.enumerated().sorted { (rank($0.element), $0.offset) < (rank($1.element), $1.offset) }.map(\.element).map { k in
            let list = groups[k] ?? []
            let title: String
            if k == "lark" || k == "feishu" {
                title = String(localized: "Lark [Inbox Event Views 7e4419]")
            } else if k.hasPrefix("mail:") {
                title = String(localized: "Mail · \(mailName(String(k.dropFirst(5))))")
            } else {
                let name = list.first?.accountName ?? ""
                title = name.isEmpty ? list.first?.source ?? k : name
            }
            return Group(key: k, title: title, items: list)
        }
    }

    /// Done's groups: by when they were done, "Just Now" first.
    static func done(_ items: [InboxItem], now: Date, calendar: Calendar = .current) -> [Group] {
        byTime(items, now: now, at: \.doneAt, recent: true, calendar: calendar)
    }

    /// Where a Done group starts and ends (end not included), so it can load its own items (#2969): Just Now and Today
    /// run on past now (no end), 1+ Month has no start. Local midnights in `calendar`'s zone, as timeKey's days are.
    static func timeRange(_ key: String, now: Date, calendar: Calendar = .current) -> (start: Date?, end: Date?) {
        let today = calendar.startOfDay(for: now)
        let back = { (n: Int) in calendar.date(byAdding: .day, value: -n, to: today) ?? today }
        switch key {
        case "just": return (now.addingTimeInterval(-3600), nil)
        case "today": return (today, nil)
        case "yesterday": return (back(1), today)
        case "week": return (back(29), back(6))
        case "month": return (nil, back(29))
        default:
            guard let d = day(of: key, calendar: calendar) else { return (nil, nil) }
            return (d, calendar.date(byAdding: .day, value: 1, to: d))
        }
    }

    /// Done's group counts from the hub's per-day counts ("YYYY-MM-DD" in this device's zone; GET /api/v1/inbox/done/days,
    /// #2967): each day's count goes to the group its day falls in. Just Now is part of Today and all in the list: `just`
    /// (how many it holds) comes off Today. The web's dayGroupCounts.
    static func dayCounts(_ days: [String: Int], now: Date, just: Int = 0, calendar: Calendar = .current) -> [String: Int] {
        var out: [String: Int] = [:]
        for (day, n) in days {
            guard let d = Self.day(of: "d:" + day, calendar: calendar) else { continue }
            // the day's noon: its key whatever the day's length (a daylight-saving day has 23 or 25 hours)
            let noon = calendar.date(byAdding: .hour, value: 12, to: d) ?? d
            out[timeKey(noon, now: now, calendar: calendar), default: 0] += n
        }
        if just > 0 {
            out["just"] = just
            let today = (out["today"] ?? 0) - just
            out["today"] = today > 0 ? today : nil
        }
        return out
    }

    /// Time group keys in Done's order (newest first), each once: for groups that come from counts as well as from items.
    static func sortedKeys(_ keys: some Sequence<String>, now: Date, calendar: Calendar = .current) -> [String] {
        Set(keys).sorted { rank($0, now: now, calendar: calendar) < rank($1, now: now, calendar: calendar) }
    }

    /// Groups open at first: every group of Needs You, FYI and Collapsed; in Done only "Just Now" and "Today".
    static func openAtFirst(section: String, key: String) -> Bool {
        section != "done" || key == "just" || key == "today"
    }
}
