import SwiftUI

// UI version 6's components and parts (docs/extensions.md「UI version 6」), drawn by `ExtensionItemRow`: controls in
// rows (a switch, a slider, a stepper), a row's extra actions, trend lines, and tiles in a grid or a carousel.

/// An extension's `spark`: a small trend line in its color, its tone's, or the quiet ink.
struct ExtensionSparkView: View {
    var spark: ExtensionSpark
    var width: CGFloat? = Theme.Size.sparkWidth
    var height: CGFloat = Theme.Size.sparkHeight

    var body: some View {
        if spark.values.count >= 2 {
            TrendLine(values: Array(spark.values.prefix(60)),
                      color: ExtensionUIStyle.mark(color: spark.color, tone: spark.tone, fallback: .secondary),
                      fill: spark.fill == true, width: width, height: height)
        }
    }
}

// MARK: Controls

nonisolated extension ExtensionRowControl {
    /// What it is now as the protocol writes it: a switch "true" / "false", a number as the shortest decimal.
    var wire: String {
        kind == "switch" ? (on == true ? "true" : "false") : Self.number(value ?? min ?? 0)
    }

    static func number(_ v: Double) -> String {
        v == v.rounded() && abs(v) < 1e15 ? String(Int64(v)) : String(v)
    }

    /// A slider's or a stepper's step (a slider's default: a hundredth of its range).
    var stepSize: Double {
        if let s = step, s > 0 { return s }
        if let lo = min, let hi = max, hi > lo { return (hi - lo) / 100 }
        return 1
    }

    /// A number written as the control says: its `display` for the value it was drawn with, else the number and unit.
    func written(_ v: Double) -> String {
        if let d = display, !d.isEmpty, let drawn = value, abs(drawn - v) < stepSize / 2 { return d }
        let decimals = stepSize < 1 ? Swift.min(3, Swift.max(1, Int((-log10(stepSize)).rounded(.up)))) : 0
        let n = v.formatted(.number.precision(.fractionLength(decimals)))
        guard let u = unit, !u.isEmpty else { return n }
        return n + " " + u
    }
}

/// A row with a control (UI version 6): the row's look, a switch or a stepper at its end, a slider on a line of its
/// own under it. Changing it shows the new value at once and sends it (a slider when the finger lifts, a stepper
/// 0.6 s after the last tap); a failure puts back the extension's value with the error as a toast. Tapping the rest of
/// the row does the row's own action.
struct ExtensionControlRow: View {
    var row: ExtensionRowComponent
    var control: ExtensionRowControl
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @Environment(AppModel.self) private var app
    @State private var asking: String?

    private var key: String { row.key ?? control.name }
    private var enabled: Bool { model?.drawsControls == true && control.disabled != true }
    private var shown: String { model?.controlValues[key] ?? control.wire }
    private var number: Double { Double(shown) ?? control.value ?? 0 }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            head
            if control.kind == "slider" { slider }
        }
        .confirmationDialog(control.action.confirm ?? "", isPresented: Binding(get: { asking != nil }, set: { if !$0 { cancel() } }),
                            titleVisibility: .visible) {
            Button { if let v = asking { asking = nil; send(v) } } label: { Text(row.title).lineLimit(1) }
                .accessibilityIdentifier("plugin-ui-confirm")
            Button(String(localized: "取消"), role: .cancel) { cancel() }
        }
        .accessibilityIdentifier("plugin-ui-control-\(key)")
    }

    @ViewBuilder private var head: some View {
        let label = ExtensionComponentRowLabel(row: row, model: model, chevron: row.action != nil,
                                               trailing: control.kind == "slider" ? AnyView(sliderValue) : AnyView(inline))
        if let action = row.action, let model {
            // The row's own action on its words; the control beside them stays the control's.
            ExtensionActionButton(action: action, title: row.title, model: model, link: $link) { label }
                .buttonStyle(.plain)
                .foregroundStyle(Color.primary)
        } else {
            label
        }
    }

    @ViewBuilder private var inline: some View {
        HStack(spacing: Theme.Space.s) {
            if model?.slowControls.contains(key) == true { ProgressView().controlSize(.small) }
            switch control.kind {
            case "switch":
                Toggle(isOn: Binding(get: { shown == "true" }, set: { change($0 ? "true" : "false") })) {
                    Text(row.title)
                }
                .labelsHidden()
                .tint(Theme.accent)
                .accessibilityIdentifier("plugin-ui-switch-\(key)")
            case "stepper":
                Text(control.written(number)).font(Theme.Font.body.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                    .contentTransition(.numericText())
                Stepper(value: Binding(get: { number }, set: { change(ExtensionRowControl.number(clamped($0)), after: .milliseconds(600)) }),
                        in: (control.min ?? -Double.greatestFiniteMagnitude)...(control.max ?? Double.greatestFiniteMagnitude),
                        step: control.stepSize) {
                    Text(row.title)
                }
                .labelsHidden()
                .accessibilityValue(control.written(number))
                .accessibilityIdentifier("plugin-ui-stepper-\(key)")
            default:
                EmptyView()
            }
        }
        .disabled(!enabled)
        .fixedSize()
    }

    private var sliderValue: some View {
        HStack(spacing: Theme.Space.s) {
            if model?.slowControls.contains(key) == true { ProgressView().controlSize(.small) }
            Text(control.written(number)).font(Theme.Font.body.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    @ViewBuilder private var slider: some View {
        let lo = control.min ?? 0, hi = Swift.max(control.max ?? 100, lo + control.stepSize)
        Slider(value: Binding(get: { Swift.min(Swift.max(number, lo), hi) },
                              set: { model?.preview(key, ExtensionRowControl.number($0)) }),
               in: lo...hi, step: control.stepSize) {
            Text(row.title)
        } onEditingChanged: { editing in
            if !editing { change(shown) }
        }
        .labelsHidden()
        .tint(Theme.accent)
        .disabled(!enabled)
        .accessibilityValue(control.written(number))
        .accessibilityIdentifier("plugin-ui-slider-\(key)")
    }

    private func clamped(_ v: Double) -> Double {
        Swift.min(Swift.max(v, control.min ?? -Double.greatestFiniteMagnitude), control.max ?? Double.greatestFiniteMagnitude)
    }

    private func change(_ value: String, after delay: Duration = .zero) {
        if control.action.confirm?.isEmpty == false {
            model?.preview(key, value)
            asking = value
            return
        }
        send(value, after: delay)
    }

    private func cancel() {
        asking = nil
        model?.preview(key, nil)
    }

    private func send(_ value: String, after delay: Duration = .zero) {
        guard let model else { return }
        Task {
            guard let o = await model.change(key, control, to: value, after: delay) else { return }
            ExtensionOutcome.show(o, app: app, link: $link)
        }
    }
}

// MARK: Row actions

enum ExtensionRowActions {
    /// A row's or a tile's `actions` as the design system's row actions, sent to the extension.
    @MainActor static func items(_ actions: [ExtensionRowAction]?, model: (any ExtensionViewControlling)?, app: AppModel,
                                 link: Binding<LinkTarget?>) -> [RowActionItem] {
        guard let model, let actions else { return [] }
        let swipes = actions.filter { $0.swipe == true }.prefix(2).map(\.title)
        return actions.prefix(4).enumerated().map { i, a in
            RowActionItem(id: "\(i):\(a.title)", title: a.title, symbol: a.icon.map(ExtensionUIStyle.icon),
                          destructive: a.role == "destructive", swipe: swipes.contains(a.title),
                          confirm: a.action.confirm) {
                guard model.isEnabled(a.action) else { return }
                Task { ExtensionOutcome.show(await model.perform(a.action), app: app, link: link) }
            }
        }
    }
}

// MARK: Tiles

/// An extension's tile (a grid's or a carousel's): its picture on top or its icon in its color, the title, the state
/// line and the value on one line each, a badge, a trend line, a switch; `active` fills it with its color's tint and
/// rings it. The whole tile does its action; its menu has its extra actions.
struct ExtensionTileView: View {
    var tile: ExtensionTile
    var model: (any ExtensionViewControlling)?
    /// A carousel's tile has a fixed width; a grid's takes its column.
    var minHeight: CGFloat? = Theme.Size.tileHeight
    @Binding var link: LinkTarget?
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme

    private var tint: Color { ExtensionUIStyle.color(tile.color) ?? Theme.accent }
    private var active: Bool { tile.active == true }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
        content
            .frame(maxWidth: .infinity, alignment: .topLeading)
            .scaledFrame(minHeight: minHeight, alignment: .topLeading)
            .background { shape.fill(active ? AnyShapeStyle(tint.opacity(scheme == .dark ? 0.2 : 0.14)) : AnyShapeStyle(Theme.Fill.well)) }
            .overlay { shape.strokeBorder(active ? tint : Color.clear, lineWidth: Theme.Size.lineMedium) }
            .clipShape(shape)
            .contentShape(shape)
            .onTapGesture { tap() }
            .rowActions(ExtensionRowActions.items(tile.actions, model: model, app: app, link: $link))
            .accessibilityElement(children: .combine)
            .accessibilityLabel(spoken)
            .accessibilityAddTraits(tile.action != nil ? .isButton : [])
            .accessibilityAction { tap() }
            .accessibilityIdentifier("plugin-ui-tile-\(tile.key ?? tile.title)")
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: Theme.Space.flush) {
            if let img = tile.image, img.url != nil || img.path != nil {
                ExtensionThumbnailImage(url: img.url, path: img.path, points: Theme.Size.tileRegular)
                    .aspectRatio(16 / 9, contentMode: .fit)
                    .frame(maxWidth: .infinity)
                    .clipped()
            }
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .top, spacing: Theme.Space.s) {
                    if tile.image == nil, let i = tile.icon, !i.isEmpty {
                        Image(systemName: ExtensionUIStyle.icon(i))
                            .symbolVariant(active ? .fill : .none)
                            .font(Theme.Font.heading)
                            .foregroundStyle(tile.color == nil && !active ? Theme.accent : tint)
                            .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                    }
                    Spacer(minLength: 0)
                    if let b = tile.badge { Chip(text: b.text, look: ExtensionUIStyle.badge(b)) }
                    if let c = tile.control, c.kind == "switch" { tileSwitch(c) }
                }
                Spacer(minLength: 0)
                Text(tile.title).font(Theme.Font.labelStrong).foregroundStyle(Color.primary).lineLimit(1)
                if let s = tile.subtitle, !s.isEmpty {
                    Text(s).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
                HStack(spacing: Theme.Space.s) {
                    if let v = tile.value, !v.isEmpty {
                        Text(v).font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(Color.primary).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                    if let sp = tile.spark { ExtensionSparkView(spark: sp) }
                }
            }
            .padding(Theme.Space.m)
        }
    }

    private func tileSwitch(_ c: ExtensionRowControl) -> some View {
        let key = tile.key ?? c.name
        let on = (model?.controlValues[key] ?? c.wire) == "true"
        return Toggle(isOn: Binding(get: { on }, set: { v in
            guard let model else { return }
            Task {
                guard let o = await model.change(key, c, to: v ? "true" : "false", after: .zero) else { return }
                ExtensionOutcome.show(o, app: app, link: $link)
            }
        })) { Text(tile.title) }
            .labelsHidden()
            .tint(tint)
            .controlSize(.small)
            .disabled(model?.drawsControls != true || c.disabled == true)
            .fixedSize()
            .accessibilityIdentifier("plugin-ui-switch-\(key)")
    }

    private var spoken: String {
        var parts = [tile.title]
        if let s = tile.subtitle, !s.isEmpty { parts.append(s) }
        if let v = tile.value, !v.isEmpty { parts.append(v) }
        if let b = tile.badge { parts.append(b.text) }
        if active { parts.append(String(localized: "已开启")) }
        return parts.joined(separator: String(localized: "，"))
    }

    private func tap() {
        guard let a = tile.action, let model, model.isEnabled(a) else { return }
        Task { ExtensionOutcome.show(await model.perform(a), app: app, link: $link) }
    }
}

/// An extension's `grid`: tiles in as many columns as fit (two on a phone), at most `max_columns` (4 when it does not
/// say).
struct ExtensionGridItem: View {
    var grid: ExtensionGridComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?

    var body: some View {
        let most = min(max(grid.maxColumns ?? 4, 1), 6)
        ViewThatFits(in: .horizontal) {
            ForEach((1...most).reversed(), id: \.self) { n in
                columns(n).frame(minWidth: CGFloat(n) * Theme.Size.tileMin + CGFloat(n - 1) * Theme.Space.m)
            }
        }
        .accessibilityElement(children: .contain)
    }

    private func columns(_ n: Int) -> some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: Theme.Space.m), count: n), spacing: Theme.Space.m) {
            ForEach(Array(grid.tiles.prefix(60).enumerated()), id: \.offset) { _, t in
                ExtensionTileView(tile: t, model: model, link: $link)
            }
        }
    }
}

/// An extension's `carousel`: tiles in a line that scrolls sideways, snapping to a tile, the next one's edge showing;
/// narrow ones for scenes, regular ones for rooms, wide ones (most of the width) for cameras.
struct ExtensionCarouselItem: View {
    var carousel: ExtensionCarouselComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @State private var width: CGFloat = Theme.Size.box300

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let t = carousel.title, !t.isEmpty {
                Text(t).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                    .padding(.horizontal, Theme.Space.page)
            }
            ScrollView(.horizontal) {
                LazyHStack(alignment: .top, spacing: Theme.Space.m) {
                    ForEach(Array(carousel.tiles.prefix(20).enumerated()), id: \.offset) { _, t in
                        ExtensionTileView(tile: t, model: model, minHeight: carousel.width == "narrow" ? Theme.Size.box80 : Theme.Size.tileHeight,
                                          link: $link)
                            .frame(width: tileWidth)
                    }
                }
                .scrollTargetLayout()
                .padding(.horizontal, Theme.Space.page)
            }
            .scrollTargetBehavior(.viewAligned)
            .scrollIndicators(.hidden)
            .scrollClipDisabled()
            .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(carousel.title ?? "")
    }

    private var tileWidth: CGFloat {
        switch carousel.width {
        case "narrow": Theme.Size.tileNarrow
        case "wide": max(Theme.Size.tileRegular, (width - Theme.Space.page * 2) * Theme.Size.tileWideShare)
        default: Theme.Size.tileRegular
        }
    }
}

nonisolated extension ExtensionComponent {
    /// A grid or a carousel: tiles that lay themselves out, not a row of a list.
    var isTiles: Bool {
        switch self {
        case .grid, .carousel: true
        default: false
        }
    }
}

extension ExtensionViewModel {
    /// Where its page's `ref` pictures are loaded: this extension, on this page's hub; an expired token draws the page
    /// again.
    var imageScope: ExtensionImageScope {
        ExtensionImageScope(owner: plugin, api: { [weak self] in self?.hubAPI }, expired: { [weak self] in
            guard let self else { return }
            Task { await self.refresh() }
        })
    }
}

extension View {
    /// The page's `ref` pictures load through its extension (nil: an assistant card, which has none).
    func extensionImages(_ model: (any ExtensionViewControlling)?) -> some View {
        environment(\.extensionImageScope, (model as? ExtensionViewModel)?.imageScope)
    }
}

/// UI version 4–5's code-hosting components in a card or a section (a whole page draws them as the code page): what
/// they say, read-only; a composer writes and sends as on the page.
struct ExtensionCodeSummary: View {
    var item: ExtensionComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @Environment(AppModel.self) private var app

    var body: some View {
        switch item {
        case .header(let h): CodeHeaderView(header: h)
        case .breadcrumb(let b):
            Text(b.crumbs.map(\.title).joined(separator: " / ")).font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1)
                .truncationMode(.head)
        case .fileTree(let t):
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                ForEach(Array(t.entries.filter { $0.kind != "dir" }.prefix(12).enumerated()), id: \.offset) { _, e in
                    fileLine(e.path, status: e.status, additions: e.additions, deletions: e.deletions)
                }
                if t.entries.count > 12 {
                    Text(String(localized: "还有 \(t.entries.count - 12) 个文件")).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
        case .code(let c):
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                if let p = c.path, !p.isEmpty { Text(p).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head) }
                Text(c.text.split(separator: "\n", omittingEmptySubsequences: false).prefix(20).joined(separator: "\n"))
                    .font(Theme.Font.code).lineLimit(20).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(Theme.Space.s)
                    .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control))
            }
        case .diff(let d):
            fileLine(d.path, status: d.status, additions: d.additions, deletions: d.deletions)
        case .commentThread(let t):
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                if let title = t.title, !title.isEmpty { Text(title).font(Theme.Font.labelStrong).lineLimit(1) }
                ForEach(Array(t.comments.prefix(5).enumerated()), id: \.offset) { _, c in
                    CodeCommentView(author: c.author, avatar: c.avatar?.url, avatarPath: c.avatar?.path, at: c.at,
                                    markdown: c.body ?? "", badge: c.badge.map { ($0.text, $0.tone) }, event: c.event == true,
                                    link: $link)
                }
            }
        case .composer(let c):
            if let model {
                CodeComposerView(placeholder: c.placeholder ?? "", text: Binding(get: { model.values[c.name] ?? "" }, set: { model.values[c.name] = $0 }),
                                 buttons: c.buttons.map { b in (b.title, b.tone, b.action.id, { send(b, name: c.name, model) }) },
                                 running: model.running, identifier: "code-composer-\(c.name)")
            }
        default: EmptyView()
        }
    }

    private func send(_ b: ExtensionComposerButton, name: String, _ model: any ExtensionViewControlling) {
        let text = model.values[name] ?? ""
        Task { @MainActor in
            let o = await model.perform(b.action, with: [name: text])
            ExtensionOutcome.show(o, app: app, link: $link)
        }
    }

    private func fileLine(_ path: String, status: String?, additions: Int?, deletions: Int?) -> some View {
        HStack(spacing: Theme.Space.s) {
            if let l = CodeFileStatus.letter(status) { Text(l).font(Theme.Font.code).foregroundStyle(CodeFileStatus.color(status)) }
            Text(path).font(Theme.Font.code).lineLimit(1).truncationMode(.head)
            Spacer(minLength: Theme.Space.s)
            CodeChangeCounts(additions: additions, deletions: deletions)
        }
        .accessibilityElement(children: .combine)
    }
}
