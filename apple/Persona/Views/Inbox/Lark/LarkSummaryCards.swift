import SwiftUI

/// Cards the Lark gateway summarises itself (`metadata.variant`): forwarded chats, group / contact cards, meetings,
/// calendar events, votes, tasks, red packets (port of `SummaryCardView`).
struct LarkSummaryCardView: View {
    var meta: JSONValue
    var fallbackTitle: String

    var body: some View {
        let variant = meta["variant"]?.string ?? ""
        Group {
            switch variant {
            case "merge-forward": LarkMergeForwardCard(meta: meta, title: title)
            case "share-group", "share-user": LarkShareCard(meta: meta, title: title, isGroup: variant == "share-group")
            default: LarkGenericSummaryCard(meta: meta, title: title, variant: variant)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(LarkColor.cardBackground)
        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(LarkColor.cardStroke))
    }

    private var title: String { meta["title"]?.string ?? fallbackTitle }
}

/// "Chat History": a few previews; tap opens every forwarded message.
struct LarkMergeForwardCard: View {
    @Environment(\.larkDisplay) private var display
    var meta: JSONValue
    var title: String
    @State private var showing = false

    var body: some View {
        let previews = meta["messages"]?.array ?? meta["previews"]?.array ?? []
        let count = meta["messageCount"]?.int ?? previews.count
        Button { showing = true } label: {
            VStack(alignment: .leading, spacing: Theme.Space.sm) {
                Text(title.isEmpty ? String(localized: "Chat History [Lark Summary Cards b790c0]") : title).font(Theme.Font.secondaryStrong).lineLimit(2).multilineTextAlignment(.leading)
                ForEach(Array(previews.prefix(4).enumerated()), id: \.offset) { _, p in
                    Text("\(p["senderName"]?.string ?? ""): \(Fmt.plain(display.forwardedPreview(p)))")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Divider().padding(.top, Theme.Space.xxs)
                HStack {
                    Label("Chat History [Lark Summary Cards b790c0]", systemImage: Theme.Icon.conversation).labelStyle(.titleAndIcon)
                    Spacer()
                    Text("\(count) items [Assistant Profile View 29b8db]")
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
            }
            .padding(Theme.Space.m)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .sheet(isPresented: $showing) { LarkForwardedView(title: title, meta: meta) }
    }
}

/// Every message of a merge-forward, rendered like the chat.
struct LarkForwardedView: View {
    @Environment(\.larkDisplay) private var display
    var title: String
    var meta: JSONValue
    @Environment(\.dismiss) private var dismiss

    private var messages: [LarkForwardedMessage] {
        guard let raw = meta["messages"], let data = try? JSON.encoder.encode(raw) else { return [] }
        return (try? JSON.decoder.decode([LarkForwardedMessage].self, from: data)) ?? []
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Theme.Space.l) {
                    ForEach(messages) { m in
                        HStack(alignment: .top, spacing: Theme.Space.m) {
                            LarkAvatarImage(name: m.senderName ?? "?", url: m.senderAvatarUrl.map(Self.hubURL), size: 32)
                            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                                HStack(spacing: Theme.Space.sm) {
                                    Text(m.senderName ?? "").font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                                    if let d = m.createdAt { Text(d.formatted(date: .abbreviated, time: .shortened)).font(Theme.Font.label).foregroundStyle(.tertiary) }
                                }
                                if m.isRecalled == true && !(display.antiRecall && m.hasRetainedRecallContent == true) {
                                    Text("Message recalled").font(Theme.Font.caption).foregroundStyle(.secondary)
                                } else if let encryption = m.olk {
                                    LarkEncryptedContent(encryption: encryption)
                                } else if let source = m.parts, !source.isEmpty {
                                    let parts = display.shown(source)
                                    LarkPartsView(parts: parts)
                                        .environment(\.larkContext, LarkRenderContext(messageID: m.isRecalled == true ? nil : m.id, images: LarkPartsView.imageURLs(parts)))
                                } else {
                                    Text(display.shown(m.textPreview ?? "")).font(Theme.Font.body)
                                }
                                if m.isRecalled == true && display.antiRecall && m.hasRetainedRecallContent == true {
                                    Text("Recalled").font(Theme.Font.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
                .padding(Theme.Space.l)
            }
            .navigationTitle(title.isEmpty ? String(localized: "Chat History [Lark Summary Cards b790c0]") : title)
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } } }
        }
    }

    /// Avatars inside forwarded messages are Lark gateway paths; the hub serves them under /api/v1/lark.
    static func hubURL(_ s: String) -> String {
        if s.hasPrefix("/api/media/") { return "/api/v1/lark/media/" + s.dropFirst(11) }
        return s
    }
}

/// Group / contact card: picture, name, eyebrow, facts.
struct LarkShareCard: View {
    var meta: JSONValue
    var title: String
    var isGroup: Bool

    var body: some View {
        let fields = (meta["fields"]?.array ?? []).compactMap { f -> String? in
            guard let l = f["label"]?.string, let v = f["value"]?.string, !v.isEmpty else { return nil }
            return "\(l) \(LarkFormat.fieldValue(v))"
        }
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.m) {
                LarkAvatarImage(name: title, url: meta["imageUrl"]?.string, isGroup: isGroup, size: 44)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(title).font(Theme.Font.secondaryStrong).lineLimit(2)
                    if !fields.isEmpty { Text(fields.joined(separator: " · ")).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1) }
                }
            }
            Divider()
            HStack {
                Text(meta["eyebrow"]?.string ?? (isGroup ? String(localized: "Group card") : String(localized: "Contact card")))
                Spacer()
                if meta["isDissolved"]?.bool == true { Text("Disbanded") }
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
        }
        .padding(Theme.Space.m)
    }
}

/// Meetings, events, votes, tasks and anything else: accent bar, glyph, eyebrow, title, status, facts, footer.
struct LarkGenericSummaryCard: View {
    var meta: JSONValue
    var title: String
    var variant: String

    var body: some View {
        let fields = (meta["fields"]?.array ?? []).compactMap { f -> (String, String)? in
            guard let l = f["label"]?.string, let v = f["value"]?.string, !v.isEmpty else { return nil }
            return (l, LarkFormat.fieldValue(v))
        }
        let accent = Self.accent(variant)
        HStack(spacing: 0) {
            Rectangle().fill(accent).frame(width: Theme.Size.barWide)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack(alignment: .top, spacing: Theme.Space.m) {
                    Group {
                        if let img = meta["imageUrl"]?.string {
                            LarkRemoteImage(url: img, size: larkThumbEdge(points: 36), maxPixel: 120)
                        } else {
                            Image(systemName: Self.glyph(variant)).font(.system(size: 17, weight: .semibold)).foregroundStyle(accent)
                                .frame(maxWidth: .infinity, maxHeight: .infinity).background(accent.opacity(0.12))
                        }
                    }
                    .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
                    .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        if let eyebrow = meta["eyebrow"]?.string { Text(eyebrow).font(Theme.Font.label).foregroundStyle(.secondary) }
                        Text(title).font(Theme.Font.secondaryStrong).lineLimit(3).fixedSize(horizontal: false, vertical: true)
                    }
                    Spacer(minLength: 4)
                    if let status = meta["status"]?.string {
                        Text(status).font(Theme.Font.labelMedium).lineLimit(1).foregroundStyle(.secondary)
                            .padding(.horizontal, Theme.Space.sm).padding(.vertical, Theme.Space.xxs).background(Color.primary.opacity(0.06), in: .capsule).fixedSize()
                    }
                }
                if let desc = meta["description"]?.string, !desc.isEmpty {
                    Text(Self.stripHTML(desc)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(4)
                }
                if let vote = meta["vote"], let options = vote["options"]?.array, !options.isEmpty {
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        ForEach(Array(options.enumerated()), id: \.offset) { _, o in
                            Label(o["content"]?.string ?? "", systemImage: (vote["maxPick"]?.int ?? 1) > 1 ? Theme.Icon.uncheckedBox : Theme.Icon.unchecked)
                                .font(Theme.Font.secondary)
                        }
                    }
                }
                if !fields.isEmpty {
                    Grid(alignment: .leading, horizontalSpacing: 10, verticalSpacing: 4) {
                        ForEach(Array(fields.enumerated()), id: \.offset) { _, f in
                            GridRow {
                                Text(f.0).foregroundStyle(.secondary).fixedSize()
                                Text(f.1).lineLimit(3)
                            }
                            .font(Theme.Font.caption)
                        }
                    }
                }
                if let footer = meta["footer"]?.string { Text(LarkFormat.fieldValue(footer)).font(Theme.Font.label).foregroundStyle(.tertiary) }
                let copy = (meta["actions"]?.array ?? []).first { $0["type"]?.string == "copyMeetingNumber" }
                if let value = copy?["value"]?.string {
                    Button {
                        UIPasteboard.general.string = value
                        Haptics.success()
                        AppModel.shared.toast = Toast(String(localized: "Meeting ID copied"), style: .success)
                    } label: { Label("Copy Meeting ID", systemImage: Theme.Icon.copy).lineLimit(1).font(Theme.Font.labelMedium) }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
            }
            .padding(Theme.Space.m)
        }
    }

    static func glyph(_ v: String) -> String {
        switch v {
        case "video-chat", "meeting": "video.fill"
        case "calendar": "calendar"
        case "vote": "chart.bar.fill"
        case "hongbao": "gift.fill"
        case "todo", "task": "checklist"
        case "share-user": Theme.Icon.person
        case "location": Theme.Icon.place
        case "mail": Theme.Icon.mailFilled
        default: Theme.Icon.fileText
        }
    }

    static func accent(_ v: String) -> Color {
        switch v {
        case "hongbao": LarkColor.hue("red")
        case "calendar": LarkColor.hue("orange")
        case "vote": LarkColor.hue("purple")
        case "todo", "task": LarkColor.hue("green")
        default: LarkColor.blue
        }
    }

    static func stripHTML(_ s: String) -> String {
        guard s.contains("<") else { return LarkFormat.entities(s) }
        let noTags = s.replacingOccurrences(of: "<br\\s*/?>|</p>|</div>", with: "\n", options: .regularExpression)
            .replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
        return LarkFormat.entities(noTags).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
