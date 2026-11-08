import SwiftUI

/// Settings › Model › a launcher › Quota (docs/integrations/launchers.md「额度」): what its key may still spend at its gateway, as a quota plugin
/// read it (titled with the gateway name the hub sends) —
/// a budget on the key, each account / provider it draws on with its windows (5 hr, weekly, monthly…), and the key's own use.
/// Read by the hub (a Persona launcher) or by a machine that has the launcher (a ~/.zshrc one: the key stays there).
struct LauncherQuotaSection: View {
    @Environment(AppModel.self) private var model
    var name: String
    @State private var answer: LauncherQuotaAnswer?
    @State private var loading = false
    @State private var failed: String?

    var body: some View {
        Section {
            if let failed {
                Label(failed, systemImage: Theme.Icon.error).font(Theme.Font.label).foregroundStyle(Theme.danger)
            } else if let a = answer {
                if let q = a.quota { report(q) } else {
                    Text(a.error ?? String(localized: "Couldn't read quota")).font(Theme.Font.label).foregroundStyle(.secondary)
                }
            } else {
                HStack(spacing: Theme.Space.s) {
                    ProgressView()
                    Text("Reading quota…").font(Theme.Font.label).foregroundStyle(.secondary)
                }
            }
        } header: {
            HStack {
                Text(answer?.quota.map { String(localized: "Quota · \($0.gatewayName)") } ?? String(localized: "Quota"))
                Spacer()
                if answer?.unsupported != true {
                    Button { Task { await load(refresh: true) } } label: {
                        if loading { ProgressView().controlSize(.mini) } else { ThemedIcon(Theme.Icon.retry) }
                    }
                    .disabled(loading)
                    .accessibilityLabel("Refresh Quota")
                }
            }
        } footer: {
            if let a = answer, a.quota != nil {
                Text(footer(a))
            }
        }
        .task { await load(refresh: false) }
    }

    @ViewBuilder private func report(_ q: LauncherQuota) -> some View {
        if let note = q.note { Text(note).font(Theme.Font.label).foregroundStyle(.secondary) }
        ForEach(q.budget ?? [], id: \.label) { w in QuotaWindowRow(window: w) }
        ForEach(q.pools ?? [], id: \.name) { p in QuotaPoolRow(pool: p) }
        if let usage = q.usage, !usage.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text("Usage for This Key").font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                ForEach(usage, id: \.label) { u in
                    LabeledContent(u.label) {
                        Text(QuotaText.usage(u)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .font(Theme.Font.caption)
                }
            }
            .padding(.vertical, Theme.Space.xxs)
        }
        if (q.pools ?? []).isEmpty, (q.budget ?? []).isEmpty, q.note == nil {
            Text("This key has no quota limit.").font(Theme.Font.label).foregroundStyle(.secondary)
        }
    }

    private func footer(_ a: LauncherQuotaAnswer) -> String {
        var parts: [String] = []
        if let k = a.quota?.keyName, !k.isEmpty { parts.append(String(localized: "Key name on the gateway: \(k)")) }
        if let m = a.machine, !m.isEmpty { parts.append(String(localized: "Read by \(model.machine(model.hubRef(m))?.name ?? m), and the key never leaves that computer")) }
        if let t = a.checkedAt { parts.append(String(localized: "Checked \(Fmt.relative(t)); opening again within a minute reuses this result")) }
        return parts.joined(separator: String(localized: "; ", comment: "Separator between footer sentences")) + String(localized: "。", comment: "Sentence-ending period after the quota footer")
    }

    private func load(refresh: Bool) async {
        guard let api = model.api else { return }
        loading = true
        defer { loading = false }
        do {
            answer = try await api.launcherQuota(name, refresh: refresh)
            failed = nil
        } catch {
            if let shown = error.shown { failed = shown }
        }
    }
}

/// An account or provider: its name and state, then its windows; per-model windows folded under it.
struct QuotaPoolRow: View {
    var pool: LauncherQuota.Pool
    @State private var showModels = false

    /// Something wrong with it (cooling down, used up, unreadable); "no quota interface" is just how it is.
    private var trouble: Bool { !["available", "ok", "unsupported", nil].contains(pool.state) }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                Text(pool.name).font(Theme.Font.secondaryStrong).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                if let d = pool.detail, !d.isEmpty {
                    Text(d).font(Theme.Font.label).foregroundStyle(trouble ? Theme.cautionText : Color.secondary).lineLimit(2)
                        .multilineTextAlignment(.trailing)
                }
            }
            ForEach(pool.windows ?? [], id: \.label) { w in QuotaWindowRow(window: w) }
            if let models = pool.models, !models.isEmpty {
                DisclosureGroup(isExpanded: $showModels) {
                    ForEach(models, id: \.model) { m in
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text(m.model).font(Theme.Font.label.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                            ForEach(m.windows, id: \.label) { w in QuotaWindowRow(window: w) }
                        }
                    }
                } label: {
                    Text("By Model (\(models.count))").font(Theme.Font.caption).lineLimit(1)
                }
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }
}

/// One window: its name, how much is used (of what), a bar, when it resets.
struct QuotaWindowRow: View {
    var window: LauncherQuota.Window

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.sm) {
                Text(window.label).font(Theme.Font.caption).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                Text(QuotaText.amount(window)).font(Theme.Font.caption.monospacedDigit())
                    .foregroundStyle(window.exhausted == true ? Theme.danger : Color.secondary).lineLimit(1)
            }
            if let r = window.usedRatio {
                ProgressView(value: min(max(r, 0), 1))
                    .tint(QuotaText.tint(r, exhausted: window.exhausted == true))
                    .accessibilityLabel("\(window.label): \(Int((r * 100).rounded()))% used")
            }
            if let reset = window.resetsAt {
                Text(QuotaText.reset(reset)).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
    }
}

/// How quota numbers read.
enum QuotaText {
    static func tint(_ ratio: Double, exhausted: Bool) -> Color {
        if exhausted || ratio >= 0.9 { return Theme.danger }
        if ratio >= 0.7 { return Theme.caution }
        return Theme.accent
    }

    /// "64% used", "6401 / 10000", "$2,100 / $3,000", "Used up".
    static func amount(_ w: LauncherQuota.Window) -> String {
        if w.exhausted == true && (w.usedRatio ?? 1) >= 1 { return String(localized: "Used up") }
        switch w.unit {
        case "usd":
            if let u = w.used, let l = w.limit { return "$\(money(u)) / $\(money(l))" }
        case "tokens":
            if let u = w.used, let l = w.limit { return String(localized: "\(count(u)) / \(count(l)) token") }
        case "requests":
            if let u = w.used, let l = w.limit { return String(localized: "\(count(u)) / \(count(l)) requests") }
        case "credits":
            if let u = w.used, let l = w.limit { return "\(count(u)) / \(count(l))" }
        default: break
        }
        if let r = w.usedRatio { return String(localized: "\(Int((r * 100).rounded()))% used") }
        return "—"
    }

    static func usage(_ u: LauncherQuota.Usage) -> String {
        var s = String(localized: "\(count(Double(u.requests))) requests · \(count(Double(u.tokens))) tokens")
        if let c = u.costUSD { s += " · $\(money(c))" }
        return s
    }

    static func reset(_ d: Date) -> String {
        d.timeIntervalSinceNow <= 0 ? String(localized: "Reset") : String(localized: "Resets \(d.formatted(.dateTime.month().day().hour().minute()))")
    }

    /// 「1.2 万」, 「3.4 亿」 (Chinese); other languages: 12K, 340M.
    static func count(_ v: Double) -> String {
        func short(_ x: Double) -> String {
            let s = String(format: "%.1f", x)
            return s.hasSuffix(".0") ? String(s.dropLast(2)) : s
        }
        // 「万」 / 「亿」 grouping is Chinese; other UI languages use the locale's compact notation (12K, 「1.2万」).
        if Bundle.main.preferredLocalizations.first?.hasPrefix("zh") == false, abs(v) >= 1e4 {
            return v.formatted(.number.precision(.fractionLength(0...1)).notation(.compactName))
        }
        switch abs(v) {
        case 1e8...: return short(v / 1e8) + " 亿" // i18n-ignore: Chinese-only path
        case 1e4...: return short(v / 1e4) + " 万" // i18n-ignore: Chinese-only path
        default: return String(format: "%.0f", v)
        }
    }

    static func money(_ v: Double) -> String { v >= 100 ? String(format: "%.0f", v) : String(format: "%.2f", v) }
}
