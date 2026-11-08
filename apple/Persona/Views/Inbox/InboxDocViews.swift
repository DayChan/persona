import SwiftUI

// Feishu documents in the inbox (docs/assistant/inbox.md「云文档」): a sheet / Base / docx that @'d the user or commented to
// them. The rows about the user show with their status columns, each a menu that moves the status in the document;
// the AI twin's draft can propose moves, made when it is sent.

/// A status column's value as a chip; tapping lists the values and moving to one writes it to the document.
struct DocStatusMenu: View {
    var item: InboxItem
    var row: InboxItem.DocRow
    var status: InboxItem.DocStatus
    /// The column's name before the value (the detail page; the list row has no room).
    var showName = false
    private var store: InboxStore { .shared }

    var body: some View {
        let moving = store.moving.contains(item.id + "|" + row.ref + "|" + status.field)
        Menu {
            Section(status.name) {
                ForEach(status.options, id: \.name) { o in
                    Button {
                        store.setDocStatus(item, row: row, status: status, to: o.name)
                    } label: {
                        if o.name == status.value { Label(o.name, systemImage: Theme.Icon.done) } else { Text(o.name) }
                    }
                }
            }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if moving {
                    ProgressView().controlSize(.mini)
                } else {
                    Circle().fill(DocStatusColor.dot(status, in: status.value)).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                }
                let value = status.value.isEmpty ? String(localized: "Not set") : status.value
                Text(showName ? String(localized: "\(status.name): \(value)") : value)
                ThemedIcon(Theme.Icon.choices).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
            }
            .font(Theme.Font.captionMedium)
            .lineLimit(1)
            .foregroundStyle(.primary)
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.xs)
            .background(DocStatusColor.fill(status, in: status.value), in: .capsule)
        }
        .tint(.primary) // the label keeps its own colors, not the app's accent
        .disabled(moving || item.isDone)
        .accessibilityLabel("\(status.name): \(status.value.isEmpty ? String(localized: "Not set") : status.value). Tap to change.")
    }
}

/// The document's own option colors are pastel fills (fine in light mode, too bright on dark): the chip takes the
/// hue faintly, the dot takes it whole.
enum DocStatusColor {
    static func color(_ status: InboxItem.DocStatus, in value: String) -> Color? {
        guard let hex = status.options.first(where: { $0.name == value })?.color, let c = parse(hex) else { return nil }
        return c
    }

    static func dot(_ status: InboxItem.DocStatus, in value: String) -> Color {
        color(status, in: value).map { $0.mix(with: .black, by: 0.25) } ?? Color.secondary.opacity(0.5)
    }

    static func fill(_ status: InboxItem.DocStatus, in value: String) -> Color {
        color(status, in: value).map { $0.opacity(0.35) } ?? Color.secondary.opacity(0.12)
    }

    private static func parse(_ hex: String) -> Color? {
        var s = hex.trimmingCharacters(in: .whitespaces)
        if s.hasPrefix("#") { s.removeFirst() }
        guard s.count == 6, let v = UInt32(s, radix: 16) else { return nil }
        return Color(hex: v)
    }
}

/// Under an inbox row: the first row about the user with its status, and how many more there are.
struct InboxDocRowsPreview: View {
    var item: InboxItem

    var body: some View {
        if let rows = item.doc?.rows, let first = rows.first {
            HStack(spacing: Theme.Space.sm) {
                ThemedIcon(Theme.Icon.followOn).font(Theme.Font.label).foregroundStyle(.secondary)
                Text(first.title).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    .layoutPriority(-1)
                ForEach((first.status ?? []).prefix(1)) { st in
                    DocStatusMenu(item: item, row: first, status: st)
                }
                if rows.count > 1 {
                    Text("+\(rows.count - 1) rows").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).fixedSize()
                }
                Spacer(minLength: 0)
            }
        }
    }
}

/// The 40pt square in place of a chat avatar: what kind of document.
struct InboxDocIcon: View {
    var doc: InboxItem.Doc

    var body: some View {
        RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
            .fill(tint.opacity(0.15))
            .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
            .overlay(ThemedIcon(doc.symbol).font(Theme.Font.bodyStrong).foregroundStyle(tint))
    }

    private var tint: Color {
        switch doc.type {
        // Feishu's own colors for its document types: sheets green, Bases purple, docs blue.
        case "sheet": LarkColor.text("green") ?? Theme.accent
        case "base": LarkColor.text("purple") ?? Theme.accent
        default: LarkColor.text("blue") ?? Theme.accent
        }
    }
}

/// An opened document item: what happened, the rows about the user (each field, each status a menu), and the
/// document itself one tap away.
struct InboxDocDetailView: View {
    var item: InboxItem
    @Environment(\.feishuOpen) private var feishuOpen

    var body: some View {
        let doc = item.doc
        List {
            Section {
                HStack(alignment: .top, spacing: Theme.Space.m) {
                    if let doc { InboxDocIcon(doc: doc) }
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        Text(item.title).font(Theme.Font.heading).lineLimit(2)
                        Text([doc?.typeName, item.preview, LarkTime.short(item.updatedAt)].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · "))
                            .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
                if let doc, let url = URL(string: doc.url) {
                    Button {
                        feishuOpen?(.doc(url: url.absoluteString, title: item.title))
                    } label: {
                        Label("Open \(doc.typeName) [Inbox Doc Views 4f91d8]", systemImage: Theme.Icon.openDocument).lineLimit(1)
                    }
                }
                if let error = doc?.error, !error.isEmpty {
                    // What it means and what to do first; the hub's own words (often an English API error) under it.
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Label("Couldn't read this doc, so the rows that mention you aren't shown. Open it to take a look.", systemImage: Theme.Icon.error)
                            .font(Theme.Font.caption).foregroundStyle(Theme.danger)
                        Text(error).font(Theme.Font.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
            }
            if let rows = doc?.rows, !rows.isEmpty {
                Section {
                    ForEach(rows) { row in rowCard(row) }
                } header: {
                    Text("Rows that mention you").textCase(nil)
                } footer: {
                    Text("Status changes are written straight to the \(doc?.typeName ?? String(localized: "Document")).").textCase(nil)
                }
            }
            if let reply = doc?.reply {
                Section {
                    Label("Replies go to: \(reply.label)", systemImage: Theme.Icon.conversation).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
        }
        .listStyle(.insetGrouped)
        .themedPage()
    }

    private func rowCard(_ row: InboxItem.DocRow) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                Text(row.title).font(Theme.Font.bodyStrong).lineLimit(2)
                Spacer(minLength: 4)
                if let t = row.table, !t.isEmpty, (item.doc?.rows?.contains { $0.table != t } ?? false) || item.doc?.type == "base" {
                    Chip(text: t, look: .neutral)
                }
            }
            ForEach(row.fields ?? [], id: \.name) { f in
                (Text(f.name + "  ").foregroundStyle(.secondary) + Text(f.value))
                    .font(Theme.Font.secondary).lineLimit(3)
            }
            if let status = row.status, !status.isEmpty {
                FlowLayoutRow(spacing: Theme.Space.sm) {
                    ForEach(status) { st in DocStatusMenu(item: item, row: row, status: st, showName: true) }
                }
            }
        }
        .padding(.vertical, Theme.Space.xs)
    }
}

/// The AI twin's proposed moves, as lines ("Status: In Progress → Done · Write docs"), for the draft card and sheet.
enum DocChangeText {
    static func line(_ change: InboxItem.DocChange, in doc: InboxItem.Doc?) -> String {
        guard let (row, st) = doc?.describe(change) else { return change.value }
        let from = st.value.isEmpty ? String(localized: "Not set") : st.value
        return String(localized: "\(st.name): \(from) → \(change.value) · \(row.title)")
    }
}
