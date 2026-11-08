#if os(iOS) || os(watchOS)
import AppIntents
import SwiftUI
import WidgetKit

struct ExtensionWidgetConfiguration: WidgetConfigurationIntent {
    nonisolated static let title: LocalizedStringResource = "Extension Widget"
    nonisolated static let description = IntentDescription("Show a read-only widget from a paired hub extension.")
    @Parameter(title: "Widget") var widget: ExtensionWidgetEntity?
    nonisolated init() {}
    nonisolated init(widget: ExtensionWidgetEntity) { self.widget = widget }
}

nonisolated struct ExtensionWidgetEntry: TimelineEntry {
    var date: Date
    var cache: ExtensionSurfaceCache?
    var title: String
    var url: URL?
    var stale: Bool
    var needsSync: Bool = false
}

struct ExtensionWidgetProvider: AppIntentTimelineProvider {
    typealias Intent = ExtensionWidgetConfiguration
    typealias Entry = ExtensionWidgetEntry

    nonisolated func recommendations() -> [AppIntentRecommendation<Intent>] {
        let supported = Set(ExtensionSystemWidget.families.map(ExtensionSystemWidget.family))
        return ExtensionSurfaceStore.shared.read().groups.sorted { $0.key < $1.key }.flatMap { group, state in
            state.widgets.filter { !supported.isDisjoint(with: $0.families) }.map { place in
                let entity = ExtensionWidgetEntity(id: ExtensionSurfaceKey(group: group, owner: place.extension, contribution: place.id).id,
                                                   title: place.title, source: state.credential.name + " · " + (place.extensionName ?? place.extension))
                return AppIntentRecommendation(intent: Intent(widget: entity), description: Text(verbatim: entity.title + " · " + entity.source))
            }
        }
    }

    nonisolated func placeholder(in context: Context) -> Entry {
        Entry(date: .now, cache: nil, title: interfaceText("Extension Widget"), stale: false)
    }
    nonisolated func snapshot(for configuration: Intent, in context: Context) async -> Entry {
        cached(configuration, family: ExtensionSystemWidget.family(context.family))
    }
    nonisolated func timeline(for configuration: Intent, in context: Context) async -> Timeline<Entry> {
        let family = ExtensionSystemWidget.family(context.family)
        if let id = configuration.widget?.id, let key = ExtensionSurfaceKey(id: id),
           let group = ExtensionSurfaceStore.shared.read().groups[key.group] {
            let client = ExtensionSurfaceClient(credential: group.credential)
            // A successful empty catalog removes revoked/disabled contributions before consulting offline content.
            if let features = try? await client.refreshCatalog(group: key.group, expected: group),
               features.contains("extension_widgets"),
               let configurations = try? await ExtensionWidgetReconciliation.configurations() {
                try? await client.reportWidgets(group: key.group, expected: group, configurations: configurations)
            }
            _ = await client.widget(key, family: family)
        }
        let entry = cached(configuration, family: family)
        let expiry = entry.cache?.answer.expiration ?? .distantPast
        let due = !entry.stale && expiry > entry.date ? expiry : entry.date.addingTimeInterval(15 * 60)
        var entries = [entry]
        if !entry.stale, entry.cache != nil {
            var expired = entry; expired.date = due; expired.stale = true
            entries.append(expired)
        }
        return Timeline(entries: entries, policy: .after(due))
    }

    private nonisolated func cached(_ configuration: Intent, family: String) -> Entry {
        let snapshot = ExtensionSurfaceStore.shared.read()
        guard let id = configuration.widget?.id, let key = ExtensionSurfaceKey(id: id),
              let group = snapshot.groups[key.group] else {
            return Entry(date: .now, cache: nil, title: interfaceText("Choose an extension widget"), stale: false)
        }
        let needsSync = group.credential.isAnswerOnly && group.credential.capabilities == nil
        guard let place = group.widgets.first(where: { $0.extension == key.owner && $0.id == key.contribution && $0.families.contains(family) }) else {
            return Entry(date: .now, cache: nil,
                         title: interfaceText(needsSync ? "Open Persona on iPhone to sync extension capabilities." : "Choose an extension widget"),
                         stale: false, needsSync: needsSync)
        }
        let cache = snapshot.cache(key, family: family)
        return Entry(date: .now, cache: cache, title: place.title,
                     url: ExtensionSystemLink(owner: place.extension, ui: place.opens, group: key.group).url,
                     stale: cache.map { !$0.fresh(at: .now) } ?? false, needsSync: needsSync)
    }
}

struct ExtensionSystemWidget: Widget {
    nonisolated static let kind = "PersonaExtensionWidget"
    var body: some WidgetConfiguration {
        AppIntentConfiguration(kind: Self.kind, intent: ExtensionWidgetConfiguration.self, provider: ExtensionWidgetProvider()) { entry in
            ExtensionWidgetEntryView(entry: entry)
        }
        .configurationDisplayName("Extension Widget")
        .description("Show a read-only widget from a paired hub extension.")
        .supportedFamilies(Self.families)
    }
    nonisolated static var families: [WidgetFamily] {
        #if os(watchOS)
        [.accessoryCircular, .accessoryRectangular, .accessoryInline]
        #else
        [.systemSmall, .systemMedium, .systemLarge, .accessoryCircular, .accessoryRectangular, .accessoryInline]
        #endif
    }
    nonisolated static func family(_ family: WidgetFamily) -> String {
        switch family {
        #if !os(watchOS)
        case .systemSmall: "small"
        case .systemMedium: "medium"
        case .systemLarge: "large"
        #endif
        case .accessoryCircular: "accessory_circular"
        case .accessoryInline: "accessory_inline"
        default: "accessory_rectangular"
        }
    }
}

private struct ExtensionWidgetEntryView: View {
    var entry: ExtensionWidgetEntry
    @Environment(\.widgetFamily) private var family
    var body: some View {
        VStack(alignment: .leading, spacing: ExtensionSurfaceTokens.compactSpacing) {
            if let cache = entry.cache {
                ExtensionSurfaceView(view: cache.answer.view, family: ExtensionSystemWidget.family(family))
                if entry.needsSync, family != .accessoryInline, family != .accessoryCircular {
                    Text("Open Persona on iPhone to sync extension capabilities.")
                        .font(ExtensionSurfaceTokens.label).foregroundStyle(ExtensionSurfaceTokens.secondaryInk).lineLimit(2)
                } else if entry.stale, family != .accessoryInline, family != .accessoryCircular {
                    Text("Cached content — open Persona to refresh")
                        .font(ExtensionSurfaceTokens.label).foregroundStyle(ExtensionSurfaceTokens.secondaryInk).lineLimit(1)
                }
            } else {
                Text(verbatim: entry.title).font(ExtensionSurfaceTokens.label).lineLimit(1)
                if family != .accessoryInline, family != .accessoryCircular {
                    Text("Open Persona to load this widget").font(ExtensionSurfaceTokens.label).lineLimit(2)
                }
            }
        }
        .widgetURL(entry.url)
        .environment(\.locale, LanguageStore.drawingLocale)
        .containerBackground(for: .widget) { ExtensionSurfaceTokens.surface }
    }
}

/// Call from foreground reconciliation, never submit a partial report on configuration-read failure.
nonisolated enum ExtensionWidgetReconciliation {
    static func configurations() async throws -> [(ExtensionSurfaceKey, String)] {
        let infos = try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<[WidgetInfo], Error>) in
            WidgetCenter.shared.getCurrentConfigurations { continuation.resume(with: $0) }
        }
        return infos.compactMap { info in
            guard info.kind == ExtensionSystemWidget.kind,
                  let intent = info.widgetConfigurationIntent(of: ExtensionWidgetConfiguration.self),
                  let id = intent.widget?.id, let key = ExtensionSurfaceKey(id: id) else { return nil }
            return (key, ExtensionSystemWidget.family(info.family))
        }
    }
    static func reportConfigurations(group: String? = nil) async {
        guard let configurations = try? await configurations() else { return }
        await withTaskGroup(of: Void.self) { tasks in
            for (id, state) in ExtensionSurfaceStore.shared.read().groups where group == nil || id == group {
                tasks.addTask {
                    try? await ExtensionSurfaceClient(credential: state.credential)
                        .reportWidgets(group: id, expected: state, configurations: configurations)
                }
            }
        }
    }

    static func invalidate(group: String?, owner: String, contribution: String?) {
        try? ExtensionSurfaceStore.shared.invalidate(group: group, owner: owner, contribution: contribution)
        WidgetCenter.shared.reloadTimelines(ofKind: ExtensionSystemWidget.kind)
    }
}
#endif
