import Observation
import SwiftUI

// The inbox_item slot (docs/extensions/extensions.md「UI 插槽」, #1682): a plugin that contributes a UI with placement inbox_item for
// one of its connectors draws the detail of that connector's inbox items. The app asks the hub which sources have one
// (GET /api/v1/plugins/-/ui?placement=inbox_item, read once per hub); an item of such a source opens as the plugin's
// View, drawn as a plugin_page is, with `context.item` the item's id. Anything else opens today's detail: a source no
// plugin draws, a hub without UI slots or without this one (an empty list, an error), and a plugin that cannot or will
// not draw this item (an error, 404 unsupported, no view).

/// Which sources' inbox items a plugin draws, for the hub the inbox is of.
@MainActor @Observable final class InboxItemSlots {
    static let shared = InboxItemSlots()

    /// The hub's inbox_item contributions in its order; nil until read for this hub.
    private(set) var places: [ExtensionUIPlace]?
    @ObservationIgnored private var lastHub: String?
    @ObservationIgnored private var reading: Task<Void, Never>?
    @ObservationIgnored private let deps: ExtensionViewModel.Deps
    @ObservationIgnored private let features: () -> Set<String>

    init(deps: ExtensionViewModel.Deps = .live, features: @escaping () -> Set<String> = { AppModel.shared.hubFeatures }) {
        self.deps = deps
        self.features = features
    }

    /// The hub may draw items: it has UI slots, or it has not said yet what it has (its hello is not in: an item opened
    /// at launch must not settle for today's detail; asking costs one request, and a hub without the slot lists
    /// nothing). One that said it has no slots is never asked, and its items open as they always did.
    var available: Bool {
        let f = features()
        return f.isEmpty || f.hasExtensionFeature("ui_slots")
    }

    /// The contribution that draws items of `source`: the first the hub lists for that connector.
    func place(for source: String) -> ExtensionUIPlace? {
        places?.first { $0.connector == source }
    }

    /// `item` opens as today's detail without asking anything: the hub has no slots, or its list is read and no plugin
    /// draws the item's source.
    func plain(_ item: InboxItem) -> Bool {
        guard available else { return true }
        guard let places else { return false }
        return !places.contains { $0.connector == item.source }
    }

    /// Asks the hub again (on another hub, what the last one said is forgotten first). A list that cannot be read keeps
    /// what there was; read for the first time, it is taken as empty, so nothing waits on it.
    func reload(for hub: String? = nil) async {
        if let hub, hub != lastHub {
            lastHub = hub
            places = nil
            reading = nil
        }
        if let reading { return await reading.value }
        let t = Task { await read() }
        reading = t
        await t.value
        if reading == t { reading = nil }
    }

    /// What the hub said, asking it first when nothing is known yet.
    func known() async -> [ExtensionUIPlace] {
        if places == nil { await reload() }
        return places ?? []
    }

    private func read() async {
        let hub = lastHub
        // Not asked (no slots, not paired): every item is today's (`plain`), and the next reload asks.
        guard available, let api = deps.api() else {
            places = nil
            return
        }
        let list = try? await api.pluginUI(placement: "inbox_item")
        guard hub == lastHub else { return } // paired with another hub meanwhile
        if let list {
            let next = list.filter { $0.placement == "inbox_item" && $0.connector?.isEmpty == false }
            if next != places { places = next }
        } else if places == nil {
            places = []
        }
    }
}

/// One inbox item's body as the slot decides it: asking (the hub's list, then the plugin's View), the plugin's View, or
/// today's body. Decided once per opening: a plugin that fails later (pulling to refresh) keeps its last View.
@MainActor @Observable final class InboxItemSlotModel {
    enum Phase: Equatable {
        case asking
        case plugin
        case plain
    }

    private(set) var phase: Phase
    /// The plugin's View of the item, once it drew one.
    private(set) var page: ExtensionViewModel?
    /// The contribution drawing the item (its title says where Back goes back to).
    private(set) var place: ExtensionUIPlace?
    /// Why the plugin's page is not there, said in a line over today's body with Retry: the plugin failed (502), the hub
    /// could not be reached. Nil when today's body is drawn quietly: no plugin for the source, or the hub refused or
    /// had nothing to draw (403 — a device that may only read —, 404, `unsupported`).
    private(set) var failure: String?
    /// The plugin opened the item's own source (persona://inbox/<id>/source, View in Chat): today's body, with a way
    /// back to the plugin's page, which stays as it was.
    var showsSource = false
    @ObservationIgnored private let item: String
    @ObservationIgnored private let source: String
    @ObservationIgnored private let slots: InboxItemSlots
    @ObservationIgnored private let deps: ExtensionViewModel.Deps

    init(item: InboxItem, slots: InboxItemSlots = .shared, deps: ExtensionViewModel.Deps = .live) {
        self.item = item.id
        self.source = item.source
        self.slots = slots
        self.deps = deps
        // Known not to be a plugin's: today's body at once, with no spinner first.
        phase = slots.plain(item) ? .plain : .asking
    }

    /// Finds the plugin that draws the item and has it draw; anything short of a View is today's body.
    func start() async {
        guard phase == .asking else { return }
        _ = await slots.known()
        guard let place = slots.place(for: source) else {
            phase = .plain
            return
        }
        self.place = place
        let page = ExtensionViewModel(plugin: place.owner, ui: place.id, context: ExtensionUIContext.here(item: item), deps: deps)
        await page.load()
        if page.view != nil, !page.unsupported {
            self.page = page
            failure = nil
            phase = .plugin
            return
        }
        failure = Self.quiet(page) ? nil : (page.error ?? String(localized: "Something Went Wrong"))
        phase = .plain
    }

    /// Retry on the line over today's body: asks the plugin again.
    func retry() async {
        guard phase == .plain, failure != nil else { return }
        failure = nil
        phase = .asking
        await start()
    }

    /// A refused or empty draw is no failure to tell: 403 (a device that may only read), 404 (the item gone, the
    /// connector not the plugin's now), `unsupported` (the plugin does not draw this item).
    private static func quiet(_ page: ExtensionViewModel) -> Bool {
        page.unsupported || page.failedStatus == 403 || page.failedStatus == 404
    }

    /// A link the plugin's page opens: the item's own source shows today's body (true); anything else is the page's
    /// to open (false). Another item's source never gets here (`ExtensionViewModel.opens`), and is dropped if it does.
    func open(_ url: URL) -> Bool {
        guard let target = InboxSourceLink.item(url) else { return false }
        if target == item { showsSource = true }
        return true
    }

    /// The item changed (new messages, filed or moved back): its plugin draws it again.
    func changed() async {
        guard phase == .plugin, let page else { return }
        await page.refresh()
    }
}

/// An inbox item's body: the plugin's View when a plugin draws it (`InboxItemSlotModel`), today's body otherwise —
/// with a line saying the plugin failed, or a way back to the plugin's page after it opened the item's source. The
/// detail's header around it (the title, the AI's summary, AI Draft Reply / Hand to AI, Done) stays the app's.
struct InboxItemSlotContent<Plain: View>: View {
    var item: InboxItem
    var slot: InboxItemSlotModel
    @ViewBuilder var plain: Plain

    var body: some View {
        Group {
            switch slot.phase {
            case .plain:
                if let failure = slot.failure {
                    plain.safeAreaInset(edge: .top, spacing: 0) { failed(failure) }
                } else {
                    plain
                }
            case .plugin:
                if slot.showsSource {
                    plain.safeAreaInset(edge: .top, spacing: 0) { back }
                } else if let page = slot.page {
                    InboxExtensionItemPage(item: item, page: page, slot: slot)
                }
            case .asking:
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .accessibilityIdentifier("inbox-plugin-item-loading")
            }
        }
        .task { await slot.start() }
        // What the plugin drew is of the item as it was: new messages, Done or Move Back draw it again.
        .onChange(of: InboxItemSlotChange(item)) { Task { await slot.changed() } }
    }

    private var name: String { slot.place.map { $0.ownerName?.isEmpty == false ? $0.ownerName! : $0.title } ?? String(localized: "Extensions") }

    /// The plugin failed: today's body under a line saying so, with Retry.
    private func failed(_ why: String) -> some View {
        InboxSlotStrip {
            Label { Text("\(name) couldn't show details: \(why)").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.plugins) }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .help(why)
            Button { Task { await slot.retry() } } label: { Label("Retry [Components b8784c]", systemImage: Theme.Icon.retry).lineLimit(1) }
                .themedButton(prominent: false)
                .controlSize(.small)
                .accessibilityIdentifier("inbox-plugin-item-retry")
        }
    }

    /// The item's source opened by the plugin: back to its page.
    private var back: some View {
        InboxSlotStrip {
            Button { slot.showsSource = false } label: {
                Label("Back to \(slot.place?.title ?? String(localized: "Extensions")) [Inbox Item Slot a550aa]", systemImage: Theme.Icon.back).lineLimit(1)
            }
            .buttonStyle(.borderless)
            .accessibilityIdentifier("inbox-plugin-item-back")
            Spacer(minLength: 0)
        }
    }
}

/// A strip over an inbox item's body, as the action bar is drawn: the bar's material, a line under it.
private struct InboxSlotStrip<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        HStack(spacing: Theme.Space.s) { content }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.s)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.bar)
            .overlay(alignment: .bottom) { Divider() }
    }
}

/// What of an item makes its plugin draw it again.
nonisolated private struct InboxItemSlotChange: Hashable {
    var version: String?
    var updatedAt: Date?
    var count: Int
    var done: Bool

    init(_ item: InboxItem) {
        version = item.version
        updatedAt = item.updatedAt
        count = item.count
        done = item.isDone
    }
}

/// An inbox item's body drawn by its plugin: the View as a plugin_page draws it (`ExtensionViewRenderer`), pulled to
/// refresh and drawn again after its `refresh_after`. The item's own source (View in Chat) switches to today's body;
/// other links and `open_url` open as the inbox's other links do (`openURL`: a Feishu document in the reader, a web
/// page in the link card, beside the item on a Mac).
struct InboxExtensionItemPage: View {
    var item: InboxItem
    var page: ExtensionViewModel
    var slot: InboxItemSlotModel
    @Environment(\.openURL) private var openURL

    /// The renderer hands its links here.
    private var links: Binding<LinkTarget?> {
        Binding(get: { nil }, set: { target in
            guard let url = target?.url, !slot.open(url), !AppModel.shared.openIfExtensionSettings(url) else { return }
            openURL(url)
        })
    }

    var body: some View {
        SettingsForm {
            if let v = page.view {
                ExtensionViewRenderer(view: v, model: page, link: links)
            }
        }
        .themedPage()
        .navigationTitle(item.title)
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await page.load() }
        .pluginRefresh(page)
        .extensionSearch(page)
        .extensionPushes([page])
        .accessibilityIdentifier("inbox-plugin-item")
    }
}
