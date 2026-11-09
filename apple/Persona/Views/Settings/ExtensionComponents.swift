import MarkdownUI
import SwiftUI

// The components of an extension's View (docs/extensions/extensions.md「声明式 UI」「UI 版本 3」), drawn by `ExtensionItemRow`.

/// A row's look: what leads it (an icon, an avatar or a thumbnail), its title and subtitle, its value and badge, and a
/// chevron when it can be used (a spinner while it runs).
struct ExtensionComponentRowLabel: View {
    var row: ExtensionRowComponent
    var model: (any ExtensionViewControlling)?
    var chevron: Bool
    /// What goes at its end before the chevron (a switch, a stepper: UI version 6's controls).
    var trailing: AnyView? = nil
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            leading
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(row.title).font(Theme.Font.body).lineLimit(1).minimumScaleFactor(0.8)
                if let s = row.subtitle, !s.isEmpty { Text(s).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2) }
            }
            Spacer(minLength: Theme.Space.s)
            if let sp = row.spark { ExtensionSparkView(spark: sp) }
            if let v = row.value, !v.isEmpty { ExtensionChangingText(v).font(Theme.Font.body.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1) }
            if let b = row.badge {
                // Drawn again with another tone, its colors change; with other words, they fade in (#37).
                Chip(text: b.text, look: ExtensionUIStyle.badge(b))
                    .contentTransition(.opacity)
                    .animation(reduceMotion ? nil : Theme.Motion.state, value: b)
            }
            if let trailing { trailing }
            if let action = row.action, model?.selected(action.id) == true {
                ThemedIcon(Theme.Icon.success).font(Theme.Font.labelStrong).foregroundStyle(Theme.positive)
                    .accessibilityLabel("Selected [Extension Components 7ecf1c]")
            } else if chevron {
                if let a = row.action, model?.running == a.id {
                    ProgressView().controlSize(.small)
                } else {
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                }
            }
        }
        .contentShape(.rect)
    }

    @ViewBuilder private var leading: some View {
        if let t = row.thumbnail, t.url.flatMap(Self.image) != nil || t.path != nil {
            let wide = t.aspect == ExtensionThumbnailAspect.wide
            ExtensionThumbnailImage(url: t.url, path: t.path, points: wide ? Theme.Size.box80 : Theme.Size.box50)
                .scaledFrame(width: wide ? Theme.Size.box80 : Theme.Size.box50, height: Theme.Size.box50)
                .clipShape(.rect(cornerRadius: Theme.Radius.badge))
                .accessibilityHidden(true)
        } else if let a = row.avatar, a.url != nil || a.path != nil || a.text?.isEmpty == false {
            ExtensionAvatarImage(url: a.url, path: a.path, text: a.text ?? "")
                .accessibilityHidden(true)
        } else if let i = row.icon, !i.isEmpty {
            ThemedIcon(ExtensionUIStyle.icon(i))
                .font(Theme.Font.heading)
                .foregroundStyle(ExtensionUIStyle.color(row.iconColor) ?? Theme.accent)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                .accessibilityHidden(true)
        }
    }

    /// An image the extension points at: https only, loaded without any of the app's credentials.
    static func image(_ s: String) -> URL? {
        guard let u = URL(string: s), u.scheme?.lowercased() == "https" else { return nil }
        return u
    }
}

/// A picture an extension points at (a row's avatar or thumbnail): loaded with a session that sends no cookies or
/// credentials, a placeholder while it loads and when it cannot be loaded.
struct ExtensionRemoteImage: View {
    var url: URL
    /// An avatar's letters when the picture fails.
    var fallback: String?
    @State private var image: Image?
    @State private var failed = false

    var body: some View {
        ZStack {
            if let image {
                image.resizable().scaledToFill()
            } else if failed, let f = fallback, !f.isEmpty {
                Monogram(name: f, size: Theme.Size.wellL, fill: Theme.Fill.chip, font: Theme.Font.labelStrong, letters: 2)
            } else {
                Rectangle().fill(Theme.Fill.chip)
            }
        }
        .task(id: url) {
            image = nil
            failed = false
            #if DEBUG && os(macOS)
            let captureLoad = MacExtensionSnapshotBatch.beginImageLoad(url)
            defer { MacExtensionSnapshotBatch.finishImageLoad(captureLoad, loaded: image != nil) }
            #endif
            guard let data = try? await ExtensionRemoteImage.session.data(from: url).0, let img = UIImage(data: data) else {
                failed = true
                return
            }
            image = Image(uiImage: img)
        }
    }

    /// No cookies, no credentials, no cache shared with the app's own requests.
    static let session: URLSession = {
        let c = URLSessionConfiguration.ephemeral
        c.httpShouldSetCookies = false
        c.urlCredentialStorage = nil
        c.httpAdditionalHeaders = [:]
        return URLSession(configuration: c)
    }()
}

/// An extension's `markdown`: the safe subset (headings, paragraphs, lists, emphasis, code, quotes, links) in the chat's
/// theme. Pictures are never loaded; links open in the app, https only.
struct ExtensionMarkdown: View {
    var text: String
    @Binding var link: LinkTarget?

    var body: some View {
        Markdown(text)
            .markdownTheme(.persona)
            .markdownImageProvider(NoImages())
            .markdownInlineImageProvider(NoImages())
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .environment(\.openURL, OpenURLAction { url in
                if url.scheme?.lowercased() == "https" { link = LinkTarget(url: url) }
                return .handled
            })
            .accessibilityIdentifier("plugin-ui-markdown")
    }

    /// The subset has no pictures: whatever an image points at is not loaded.
    private struct NoImages: ImageProvider, InlineImageProvider {
        func makeImage(url: URL?) -> some View { EmptyView() }
        func image(with url: URL, label: String) async throws -> Image { throw URLError(.unsupportedURL) }
    }
}

/// An extension's `table`: its columns' titles, its rows, a cell on one line each; wider than the screen it scrolls
/// sideways. Its caption under it.
struct ExtensionTable: View {
    var table: ExtensionTableComponent

    var body: some View {
        let columns = Array(table.columns.prefix(8))
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ScrollView(.horizontal) {
                Grid(alignment: .leading, horizontalSpacing: Theme.Space.l, verticalSpacing: Theme.Space.s) {
                    GridRow {
                        ForEach(Array(columns.enumerated()), id: \.offset) { _, c in
                            Text(c.title).font(Theme.Font.labelStrong).foregroundStyle(.secondary).lineLimit(1)
                                .gridColumnAlignment(c.align == ExtensionTableAlign.end ? .trailing : .leading)
                        }
                    }
                    Divider().gridCellUnsizedAxes(.horizontal)
                    ForEach(Array(table.rows.prefix(100).enumerated()), id: \.offset) { _, row in
                        GridRow {
                            ForEach(Array(columns.indices), id: \.self) { i in
                                Text(i < row.count ? row[i] : "")
                                    .font(columns[i].align == ExtensionTableAlign.end ? Theme.Font.secondary.monospacedDigit() : Theme.Font.secondary)
                                    .lineLimit(1)
                                    .fixedSize()
                            }
                        }
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
            .textSelection(.enabled)
            if let c = table.caption, !c.isEmpty { Text(c).font(Theme.Font.label).foregroundStyle(.secondary) }
        }
        .accessibilityIdentifier("plugin-ui-table")
    }
}

/// An extension's `empty`: the app's empty state with its icon, words and at most one button.
struct ExtensionEmpty: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    var empty: ExtensionEmptyComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?

    @State private var asking = false

    var body: some View {
        EmptyStateView(symbol: ExtensionUIStyle.icon(empty.icon), title: empty.title, message: empty.detail ?? "",
                       action: button, tint: ExtensionUIStyle.color(empty.iconColor))
            .frame(maxWidth: .infinity)
            .listRowBackground(Color.clear)
            .accessibilityIdentifier("plugin-ui-empty")
            .disabled(model?.managesConfirmation == true && empty.button.map { model?.isEnabled($0.action) != true } == true)
            .confirmationDialog(empty.button?.action.confirm ?? "", isPresented: $asking, titleVisibility: .visible) {
                Button { run() } label: { Text(empty.button?.title ?? String(localized: "OK [Interactive Assistant View Card fac2a6]")).lineLimit(1) }
                    .accessibilityIdentifier("plugin-ui-confirm")
                Button("Cancel", role: .cancel) {}
            }
    }

    private var button: (title: String, run: () -> Void)? {
        guard let b = empty.button, let model else { return nil }
        return (b.title, {
            if b.action.confirm?.isEmpty == false, !model.managesConfirmation { asking = true } else { run() }
        })
    }

    private func run() {
        guard let b = empty.button, let model, model.isEnabled(b.action) else { return }
        let execute = model.prepared(b.action, with: [:], displayed: displayed)
        Task { ExtensionOutcome.show(await execute(), app: app, link: $link) }
    }
}

/// A section's `more`: a row at its end that loads the next part when it scrolls into view (once, until the extension
/// answers) or is tapped.
struct ExtensionMoreRow: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    var more: ExtensionMore
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?

    var body: some View {
        Button { load() } label: {
            HStack(spacing: Theme.Space.s) {
                Text(more.title?.isEmpty == false ? more.title! : String(localized: "Load More")).lineLimit(1)
                if model.loadingMore == more.action.id { ProgressView().controlSize(.small) }
            }
            .frame(maxWidth: .infinity)
        }
        .disabled(model.loadingMore != nil || (model.managesConfirmation && !model.isEnabled(more.action)))
        .onAppear { if !model.managesConfirmation { load() } }
        .accessibilityIdentifier("plugin-ui-more")
    }

    private func load() {
        guard model.loadingMore == nil else { return }
        if let page = model as? ExtensionViewModel, let displayed {
            let execute = page.preparedMore(more, displayed: displayed)
            Task { ExtensionOutcome.show(await execute(), app: app, link: $link) }
        } else { Task { ExtensionOutcome.show(await model.more(more), app: app, link: $link) } }
    }
}

/// The page's `filter`: its choices as chips under the search field, the picked one on; picking another asks the
/// extension for the view at once.
struct ExtensionFilterChips: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    var filter: ExtensionFilter
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?

    var body: some View {
        let picked = model.values[filter.name] ?? filter.value
        ScrollView(.horizontal) {
            HStack(spacing: Theme.Space.s) {
                ForEach(filter.choices.prefix(20)) { c in
                    FilterChip(title: c.label, on: c.value == picked) {
                        if let displayed {
                            let before = model.values[filter.name]
                            model.values[filter.name] = c.value
                            let execute = model.prepared(filter.action, with: [filter.name: c.value, "q": model.query], displayed: displayed)
                            Task {
                                let outcome = await execute()
                                if outcome.error != nil || outcome.cancelled { model.values[filter.name] = before }
                                ExtensionOutcome.show(outcome, app: app, link: $link)
                            }
                        } else { Task { ExtensionOutcome.show(await model.filter(c.value), app: app, link: $link) } }
                    }
                    .accessibilityIdentifier("plugin-ui-filter-\(c.value)")
                }
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.xs)
        }
        .disabled(model.running != nil || (model.managesConfirmation && !model.isEnabled(filter.action)))
        .accessibilityLabel(filter.title ?? String(localized: "Filter"))
    }
}

/// A card's explicit search control. Unlike extension-page debounce, typing cannot send an assistant reply.
struct ExtensionSearchRow: View {
    var search: ExtensionSearch
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?
    @Environment(AppModel.self) private var app

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            TextField(search.placeholder ?? String(localized: "Search"), text: Binding(get: { model.query }, set: { model.query = $0 }))
                .font(Theme.Font.body).textInputAutocapitalization(.never).autocorrectionDisabled()
                .onSubmit { run() }
            Button { run() } label: { ThemedIcon(Theme.Icon.search) }
                .themedButton().accessibilityLabel("Search")
        }
        .padding(Theme.Space.s)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control))
        .disabled(!model.isEnabled(search.action))
        .accessibilityIdentifier("assistant-view-search")
    }
    private func run() { Task { ExtensionOutcome.show(await model.search(), app: app, link: $link) } }
}

/// What using a control came to, on screen: an error or the extension's word as a toast, its link in the app.
enum ExtensionOutcome {
    @MainActor static func show(_ o: ExtensionViewModel.Outcome, app: AppModel, link: Binding<LinkTarget?>) {
        if let e = o.error {
            Haptics.error()
            app.toast = Toast(e, style: .error)
        } else if let t = o.toast {
            app.toast = Toast(t, style: .success)
        }
        if let url = o.url, !app.openIfExtensionSettings(url) { link.wrappedValue = LinkTarget(url: url) }
    }
}

/// An extension's `chart`: its title, the chart (bars or lines, stacked when it says so), what it says under it. A
/// series' tone is a role color, without one it takes the next data hue; values the extension did not write are compact
/// numbers with its unit. Compact (a card) when the environment says so.
struct ExtensionChartItem: View {
    var item: ExtensionChartComponent
    @Environment(\.pluginUICompact) private var compact
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var series: [ExtensionChartSeries] {
        // A candlestick, a donut and a pie have one series; the others up to four.
        Array(item.series.prefix(["candlestick", "donut", "pie"].contains(item.kind) ? 1 : 4))
    }
    /// The x axis: the first series' labels (the others line up with them).
    private var labels: [String] { series.first?.points.map(\.label) ?? [] }

    var body: some View {
        if !labels.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let t = item.title, !t.isEmpty {
                    Text(t).font(compact ? Theme.Font.labelStrong : Theme.Font.secondaryStrong).foregroundStyle(compact ? .secondary : .primary).lineLimit(1)
                }
                DataChart(kind: kind, labels: labels,
                          series: series.enumerated().map { i, s in
                              DataChart.Series(name: s.name?.isEmpty == false ? s.name! : String(localized: "Series \(i + 1)"),
                                               color: ExtensionUIStyle.color(s.color) ?? ExtensionUIStyle.series(s.tone),
                                               values: s.points.map(\.value), displays: s.points.map(\.display))
                          },
                          stacked: item.stacked == true, format: format, compact: compact,
                          candles: candles,
                          rising: item.rising == "red" ? Theme.Market.red : Theme.Market.green,
                          falling: item.rising == "red" ? Theme.Market.green : Theme.Market.red,
                          center: item.center)
                // Drawn again with other values, the bars and lines move to them (#37).
                .animation(reduceMotion ? nil : Theme.Motion.layout, value: item.series)
                if let d = item.detail, !d.isEmpty, !compact {
                    Text(d).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(3)
                }
            }
            .padding(.vertical, Theme.Space.xs)
        }
    }

    /// The kind this app draws: UI version 6's area, candlestick, donut and pie; an unknown one as bars.
    private var kind: DataChart.Kind {
        switch item.kind {
        case "line": .line
        case "area": .area
        case "candlestick": candles.isEmpty ? .line : .candlestick
        case "donut": .donut
        case "pie": .pie
        default: .bar
        }
    }

    /// A candlestick's prices: every point needs all four (else it is drawn as the closes' line).
    private var candles: [DataChart.Candle] {
        guard item.kind == "candlestick", let s = item.series.first else { return [] }
        let out = s.points.compactMap { p -> DataChart.Candle? in
            guard let o = p.open, let h = p.high, let l = p.low else { return nil }
            return DataChart.Candle(open: o, high: h, low: l, close: p.close ?? p.value, volume: p.volume)
        }
        return out.count == s.points.count ? out : []
    }

    private func format(_ v: Double) -> String {
        let n = DataChart.compact(v)
        guard let u = item.unit, !u.isEmpty else { return n }
        return n + " " + u
    }
}

/// An extension's `stat_grid`: a few numbers side by side (two or three a row on an iPhone, all of them in a row when
/// there is room), each with what it is and a line under it in its tone.
struct ExtensionStatGrid: View {
    var stats: [ExtensionStat]

    var body: some View {
        let shown = Array(stats.prefix(6))
        ViewThatFits(in: .horizontal) {
            row(shown, columns: shown.count)
            grid(shown, columns: shown.count == 4 ? 2 : 3)
            grid(shown, columns: 2)
        }
        .padding(.vertical, Theme.Space.xs)
    }

    private func row(_ items: [ExtensionStat], columns: Int) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.l) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, s in cell(s).fixedSize() }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func grid(_ items: [ExtensionStat], columns: Int) -> some View {
        Grid(alignment: .topLeading, horizontalSpacing: Theme.Space.l, verticalSpacing: Theme.Space.m) {
            ForEach(Array(stride(from: 0, to: items.count, by: columns)), id: \.self) { start in
                GridRow {
                    ForEach(start..<min(start + columns, items.count), id: \.self) { i in
                        cell(items[i]).frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        }
    }

    private func cell(_ s: ExtensionStat) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(s.label).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            ExtensionChangingText(s.value).font(Theme.Font.heading.monospacedDigit()).lineLimit(1).minimumScaleFactor(0.6)
            if let d = s.detail, !d.isEmpty {
                Text(d).font(Theme.Font.label).foregroundStyle(ExtensionUIStyle.tone(s.tone)).lineLimit(1).minimumScaleFactor(0.8)
            }
            if let sp = s.spark { ExtensionSparkView(spark: sp) }
        }
        .accessibilityElement(children: .combine)
    }
}

/// An extension's `segmented`: a few choices, one picked; picking another sends it to the extension at once (its
/// action, with `{name: value}`), whose answer is the view for it.
struct ExtensionSegmented: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    var item: ExtensionSegmentedComponent
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?

    var body: some View {
        let choices = Array(item.choices.prefix(5))
        if choices.count >= 2 {
            SegmentedChoice(item.name, choices.map { ($0.value, $0.label) }, selection: Binding(
                get: { model.values[item.name] ?? (item.value.isEmpty ? choices[0].value : item.value) },
                set: { v in
                    if let displayed {
                        let before = model.values[item.name]
                        model.values[item.name] = v
                        let execute = model.prepared(item.action, with: [item.name: v], displayed: displayed)
                        Task {
                            let outcome = await execute()
                            if outcome.error != nil || outcome.cancelled { model.values[item.name] = before }
                            ExtensionOutcome.show(outcome, app: app, link: $link)
                        }
                    } else { Task { ExtensionOutcome.show(await model.choose(item.name, v, action: item.action), app: app, link: $link) } }
                }))
            // It has no label of its own (the name is the extension's key): a Mac picker would write the key beside it.
            .labelsHidden()
            .disabled(!model.isEnabled(item.action))
            .accessibilityIdentifier("plugin-ui-segmented-\(item.name)")
            .padding(.vertical, Theme.Space.xxs)
        }
    }
}

/// A button or row that sends its action back to the extension, asking first when it says so (a `push` opens its page
/// without asking the extension); the answer's word is a toast, its link opens in the app.
struct ExtensionActionButton<Content: View>: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    var action: ExtensionAction
    var title: String
    var destructive = false
    var model: any ExtensionViewControlling
    var values: [String: String] = [:]
    @Binding var link: LinkTarget?
    @ViewBuilder var label: Content
    @State private var asking = false

    var body: some View {
        Button(role: destructive ? .destructive : nil) {
            if action.confirm?.isEmpty == false, !model.managesConfirmation { asking = true } else { run() }
        } label: {
            label
        }
        // A Mac form draws a button in the accent whatever its role: red as on the phone (#1512).
        .tint(destructive ? Theme.danger : nil)
        .disabled(!model.isEnabled(action))
        .accessibilityIdentifier("plugin-ui-action-\(action.id)")
        .confirmationDialog(action.confirm ?? "", isPresented: $asking, titleVisibility: .visible) {
            Button(role: destructive ? .destructive : nil) { run() } label: { Text(title.isEmpty ? String(localized: "OK [Interactive Assistant View Card fac2a6]") : title).lineLimit(1) }
                .accessibilityIdentifier("plugin-ui-confirm")
            Button("Cancel", role: .cancel) {}
        }
    }

    private func run() {
        let execute = model.prepared(action, with: values, displayed: displayed)
        if model.managesConfirmation && displayed == nil { Keyboard.dismiss() }
        Task {
            // Let the conversation's simultaneous focus-dismiss gesture settle before presenting a native alert.
            if model.managesConfirmation && displayed == nil { await Task.yield() }
            ExtensionOutcome.show(await execute(), app: app, link: $link)
        }
    }
}

/// A field of an extension's View: text, a secret (typed in hidden, never filled in for you), a number, a switch, a
/// choice.
struct ExtensionFieldItem: View {
    @Environment(\.extensionFieldFocus) private var focus
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(\.scenePhase) private var phase
    @State private var secretDraft = ""
    @State private var choosing = false
    var field: ExtensionFieldComponent
    var model: any ExtensionViewControlling

    private var name: String { field.name }
    private var label: String { field.label.isEmpty ? name : field.label }
    private var privateInput: Bool {
        guard let page = model as? ExtensionViewModel else { return false }
        return page.stream?.tree != nil
    }
    private var text: Binding<String> {
        if field.kind == "secret", privateInput {
            return Binding(get: { secretDraft }, set: { secretDraft = $0 })
        }
        return Binding(get: { model.values[name] ?? "" }, set: { model.values[name] = $0 })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            control
            if let d = field.detail, !d.isEmpty {
                Text(d).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(3)
            }
        }
        .disabled(!model.fieldsEnabled)
        .accessibilityIdentifier("plugin-ui-field-\(name)")
        .onDisappear { secretDraft = "" }
        .onChange(of: displayed) { _, _ in secretDraft = "" }
        .onChange(of: field.kind) { _, _ in secretDraft = "" }
        .onChange(of: phase) { _, phase in if phase != .active { secretDraft = "" } }
        .onTabVisibility(appear: {}, disappear: { secretDraft = "" })
    }

    @ViewBuilder private var control: some View {
        switch field.kind {
        case "toggle":
            Toggle(isOn: Binding(get: { model.values[name] == "true" }, set: { model.values[name] = $0 ? "true" : "false" })) {
                Text(label).lineLimit(2)
            }
        case "choice":
            Picker(selection: text) {
                if !(field.choices ?? []).contains(where: { $0.value == (model.values[name] ?? "") }) {
                    Text(field.placeholder ?? String(localized: "None [Extension Components 3005ea]")).tag(model.values[name] ?? "")
                }
                ForEach(field.choices ?? []) { c in Text(c.label).tag(c.value) }
            } label: {
                Text(label).lineLimit(2)
            }
        case "secret":
            labeled {
                SecretField(field.placeholder ?? "", filled: privateInput ? String(localized: "Hidden") : String(localized: "Filled in; sent when you submit"), text: text)
                    .modifier(ExtensionFieldFocus(binding: focus, key: field.key ?? name))
            }
        case "number":
            labeled {
                TextField(field.placeholder ?? "", text: text).keyboardType(.decimalPad)
                    .modifier(ExtensionFieldFocus(binding: focus, key: field.key ?? name))
            }
        case "date", "time", "datetime":
            DatePicker(selection: date, in: bounds, displayedComponents: components) { Text(label).lineLimit(2) }
                .datePickerStyle(.compact)
                .environment(\.timeZone, ExtensionFieldValue.zone)
        case "multichoice":
            multichoice
        case "multiline":
            labeled {
                TextField(field.placeholder ?? "", text: text, axis: .vertical)
                    .lineLimit(3...8)
                    .modifier(ExtensionFieldFocus(binding: focus, key: field.key ?? name))
            }
        default:
            labeled {
                TextField(field.placeholder ?? "", text: text)
                    .modifier(ExtensionFieldFocus(binding: focus, key: field.key ?? name))
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }
        }
    }

    // MARK: Dates and times

    private var components: DatePickerComponents {
        switch field.kind {
        case "date": .date
        case "time": .hourAndMinute
        default: [.date, .hourAndMinute]
        }
    }

    private var date: Binding<Date> {
        Binding(get: { ExtensionFieldValue.date(model.values[name] ?? "", kind: field.kind) ?? Self.now(field.kind) },
                set: { model.values[name] = ExtensionFieldValue.write($0, kind: field.kind) })
    }

    /// What an empty date field starts on: now, to the minute.
    private static func now(_ kind: String) -> Date {
        ExtensionFieldValue.date(ExtensionFieldValue.write(.now, kind: kind), kind: kind) ?? .now
    }

    private var bounds: ClosedRange<Date> {
        let lo = field.earliest.flatMap { ExtensionFieldValue.date($0, kind: field.kind) } ?? .distantPast
        let hi = field.latest.flatMap { ExtensionFieldValue.date($0, kind: field.kind) } ?? .distantFuture
        return lo <= hi ? lo...hi : lo...lo
    }

    // MARK: Several choices

    private var picked: [String] { ExtensionFieldValue.chosen(model.values[name] ?? "") }

    private func toggle(_ v: String) {
        var now = picked
        if let i = now.firstIndex(of: v) { now.remove(at: i) } else { now.append(v) }
        // In the extension's order, whatever order they were picked in.
        let order = (field.choices ?? []).map(\.value)
        now.sort { (order.firstIndex(of: $0) ?? .max) < (order.firstIndex(of: $1) ?? .max) }
        model.values[name] = ExtensionFieldValue.write(chosen: now)
    }

    /// Up to six choices: a row each with a check; more: a row that says how many, opening a list to search.
    @ViewBuilder private var multichoice: some View {
        let choices = field.choices ?? []
        if choices.count <= 6 {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(label).font(Theme.Font.labelStrong).foregroundStyle(.secondary).lineLimit(2)
                ForEach(choices) { c in check(c) }
            }
            .padding(.vertical, Theme.Space.xxs)
        } else {
            Button { choosing = true } label: {
                HStack(spacing: Theme.Space.s) {
                    Text(label).foregroundStyle(Color.primary).lineLimit(2)
                    Spacer(minLength: Theme.Space.s)
                    Text(picked.isEmpty ? String(localized: "None [Extension Components 3005ea]") : String(localized: "\(picked.count) chosen")).foregroundStyle(.secondary).lineLimit(1)
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .sheet(isPresented: $choosing) {
                ExtensionMultiChoiceSheet(title: label, choices: choices, picked: picked, toggle: toggle)
            }
        }
    }

    private func check(_ c: ExtensionChoice) -> some View {
        let on = picked.contains(c.value)
        return Button { toggle(c.value) } label: {
            HStack(spacing: Theme.Space.s) {
                Image(systemName: on ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                    .foregroundStyle(on ? Theme.accent : Color.secondary)
                Text(c.label).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: 0)
            }
            .scaledFrame(minHeight: Theme.Size.wellL)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
        .accessibilityIdentifier("plugin-ui-choice-\(name)-\(c.value)")
    }

    /// The label above the field.
    private func labeled(@ViewBuilder _ content: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(label).font(Theme.Font.labelStrong).foregroundStyle(.secondary).lineLimit(2)
            #if os(macOS)
            content().labelsHidden() // a Mac form would put the label beside an empty box (#1221)
            #else
            content()
            #endif
        }
        .padding(.vertical, Theme.Space.xxs)
    }
}

/// Many choices of a `multichoice` field (more than six): a list to search, each with a check.
struct ExtensionMultiChoiceSheet: View {
    var title: String
    var choices: [ExtensionChoice]
    @State var picked: [String]
    var toggle: (String) -> Void
    @State private var query = ""
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                ForEach(choices.filter { query.isEmpty || $0.label.localizedCaseInsensitiveContains(query) }) { c in
                    Button {
                        toggle(c.value)
                        if let i = picked.firstIndex(of: c.value) { picked.remove(at: i) } else { picked.append(c.value) }
                    } label: {
                        HStack(spacing: Theme.Space.s) {
                            Text(c.label).foregroundStyle(Color.primary).lineLimit(1)
                            Spacer(minLength: 0)
                            if picked.contains(c.value) { Image(systemName: Theme.Icon.done).foregroundStyle(Theme.accent) }
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(picked.contains(c.value) ? [.isButton, .isSelected] : .isButton)
                }
            }
            .searchable(text: $query)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text("Done").lineLimit(1) }
                }
            }
        }
        #if os(macOS)
        .frame(minWidth: Theme.Size.box380, minHeight: Theme.Size.box380)
        #endif
    }
}

/// UI version 6's field values as the protocol writes them: a date `YYYY-MM-DD`, a time `HH:MM` (24 hours), a date
/// and time `YYYY-MM-DDTHH:MM` without an offset, in the app's time zone (the context's `tz`); several choices a JSON
/// array of their values.
nonisolated enum ExtensionFieldValue {
    static var zone: TimeZone { .current }

    private static func calendar(_ tz: TimeZone) -> Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = tz
        return c
    }

    static func date(_ s: String, kind: String, in tz: TimeZone = zone) -> Date? {
        let digits = s.split(whereSeparator: { "-T:".contains($0) }).map { Int($0) }
        guard !digits.contains(nil) else { return nil }
        let n = digits.compactMap { $0 }
        var c = DateComponents()
        switch kind {
        case "date":
            guard n.count == 3, s.count == 10 else { return nil }
            (c.year, c.month, c.day) = (n[0], n[1], n[2])
        case "time":
            guard n.count == 2, s.count == 5, n[0] < 24, n[1] < 60 else { return nil }
            // A time alone is on a fixed day, so that two of them compare by the clock only.
            (c.year, c.month, c.day, c.hour, c.minute) = (2000, 1, 1, n[0], n[1])
        default:
            guard n.count == 5, s.count == 16 else { return nil }
            (c.year, c.month, c.day, c.hour, c.minute) = (n[0], n[1], n[2], n[3], n[4])
        }
        guard c.month.map({ (1...12).contains($0) }) ?? true, c.day.map({ (1...31).contains($0) }) ?? true else { return nil }
        let cal = calendar(tz)
        guard let d = cal.date(from: c) else { return nil }
        // No rolling over (02-30 is not 03-02).
        let back = cal.dateComponents([.year, .month, .day], from: d)
        guard back.year == c.year, back.month == c.month, back.day == c.day else { return nil }
        return d
    }

    static func write(_ d: Date, kind: String, in tz: TimeZone = zone) -> String {
        let c = calendar(tz).dateComponents([.year, .month, .day, .hour, .minute], from: d)
        let day = String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
        let clock = String(format: "%02d:%02d", c.hour ?? 0, c.minute ?? 0)
        switch kind {
        case "date": return day
        case "time": return clock
        default: return day + "T" + clock
        }
    }

    /// The values a multichoice holds; anything that is not a JSON array of strings is none.
    static func chosen(_ s: String) -> [String] {
        guard let d = s.data(using: .utf8), let a = try? JSONDecoder().decode([String].self, from: d) else { return [] }
        return a
    }

    static func write(chosen: [String]) -> String {
        guard let d = try? JSONEncoder().encode(chosen), let s = String(data: d, encoding: .utf8) else { return "[]" }
        return s
    }
}
