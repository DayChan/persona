import SwiftUI

/// A stable card location is supplied by the owning assistant conversation, never inferred from model JSON.
nonisolated struct AssistantCardLocation: Hashable, Sendable {
    var key: String
    var scope: AssistantViewScope
}

extension EnvironmentValues {
    @Entry var assistantCardConversation: SessionKey? = nil
}

/// Hosts the shared Extension components, card-local navigation and one native confirmation.
struct InteractiveAssistantViewCard: View {
    var source: ExtensionView
    var version: Int64
    var complete: Bool
    var registered: Bool
    var location: AssistantCardLocation
    @State private var model: AssistantViewModel
    @State private var openedFile: FileRef?
    @State private var openedKnowledge: KBEntry?
    @State private var openedWeb: LinkTarget?
    @Environment(AppModel.self) private var app
    @Environment(\.openLink) private var openLink

    init(view: ExtensionView, version: Int64 = 1, complete: Bool = true, registered: Bool,
         location: AssistantCardLocation, deps: AssistantViewModel.Deps) {
        source = view; self.version = version; self.complete = complete; self.registered = registered; self.location = location
        _model = State(initialValue: AssistantViewsStore.shared.presentation(key: location.key, scope: location.scope,
            view: view, version: version, complete: complete, registered: registered, deps: deps))
    }
    private var link: Binding<LinkTarget?> {
        Binding(get: { nil }, set: { if let url = $0?.url { openCardURL(url) } })
    }
    var body: some View {
        let supported = AssistantRichMarkdown.supports(model.root)
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if let title = model.view.title, !title.isEmpty {
                HStack(spacing: Theme.Space.s) {
                    if model.page != nil {
                        Button { model.back() } label: { ThemedIcon(Theme.Icon.back) }
                            .themedButton().accessibilityLabel("Back to the card's previous page")
                    }
                    Text(title).font(Theme.Font.heading).accessibilityAddTraits(.isHeader)
                }
            } else if model.page != nil {
                Button { model.back() } label: { Label("Back [Task Sheets 572cf4]", systemImage: Theme.Icon.back).lineLimit(1) }
                    .themedButton()
            }
            if supported {
                ExtensionViewStack(view: model.view, model: model, cards: false, titles: true, link: link)
                    .environment(\.assistantHTMLRendering, AssistantHTMLRenderingContext(version: model.version, fallback: model.root.fallback,
                        answered: model.record != nil || model.attempt != nil,
                        enabled: AssistantHTMLSafety.executionVerified && app.features(location.scope.group).contains("assistant_html_bridge"),
                        bridgeAvailable: model.registered && app.features(location.scope.group).contains("assistant_html_bridge"),
                        onRequest: { request, component, expectedVersion in
                            let result = await model.html(request, component: component, expectedVersion: expectedVersion)
                            ExtensionOutcome.show(result, app: app, link: link)
                            if case .reply = request { return model.record != nil }
                            return result.url != nil
                        }))
            }
            if !supported || model.view.sections.flatMap(\.items).isEmpty, let fallback = model.view.fallback ?? model.root.fallback {
                Text(fallback).font(Theme.Font.body).textSelection(.enabled)
            }
            if let record = model.record {
                HStack(spacing: Theme.Space.s) {
                    ThemedIcon(Theme.Icon.success).foregroundStyle(Theme.positive)
                    Text(status(record)).font(Theme.Font.caption).foregroundStyle(.secondary)
                        .accessibilityIdentifier("assistant-view-answer")
                }
                if let error = record.error, !error.isEmpty { Text(error).font(Theme.Font.caption).foregroundStyle(Theme.danger) }
                if let text = record.text, !text.isEmpty { Text(text).font(Theme.Font.secondary).textSelection(.enabled) }
                ForEach((record.values ?? [:]).keys.sorted(), id: \.self) { name in
                    Text("\(name): \(record.values?[name] ?? "")").font(Theme.Font.secondary).textSelection(.enabled)
                }
            } else if let attempt = model.attempt, !attempt.sending {
                Text("The submission isn't confirmed yet; it won't change the choice or send twice.").font(Theme.Font.caption).foregroundStyle(Theme.caution)
                Button { Task { ExtensionOutcome.show(await model.retry(), app: app, link: link) } } label: {
                    Label("Retry Original Choice", systemImage: Theme.Icon.retry).lineLimit(1)
                }
                .themedButton().accessibilityIdentifier("assistant-view-retry")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("assistant-view-card")
        .accessibilityValue(model.pending != nil ? String(localized: "Waiting for confirmation") : model.record != nil ? String(localized: "Selected [Interactive Assistant View Card 3f4ebc]") : String(localized: "Ready [Interactive Assistant View Card 1ee890]"))
        .alert(model.pending?.action.confirm ?? (model.changed ? String(localized: "The card changed. Submit with the new content?") : String(localized: "Confirm Card Action")), isPresented: Binding(
            get: { model.pending != nil }, set: { if !$0 { model.decline() } }), presenting: model.pending) { _ in
                Button {
                    guard let selection = model.claimConfirmation() else { return }
                    Task { ExtensionOutcome.show(await model.confirm(selection), app: app, link: link) }
                } label: { Text("OK [Interactive Assistant View Card fac2a6]").lineLimit(1) }
                    .accessibilityIdentifier("assistant-view-confirm")
                Button("Cancel", role: .cancel) { model.decline() }
            } message: { _ in
                Text(confirmationText)
            }
        #if !os(macOS)
        .pluginLinks($openedWeb)
        #endif
        .sheet(item: $openedFile) { FileViewer(ref: $0) }
        .sheet(item: $openedKnowledge) { entry in
            NavigationStack { KnowledgeDetailView(entry: entry) }
        }
        .onAppear { model.update(source, version: version, complete: complete); model.registered = registered }
        .onDisappear { model.decline() }
        .onChange(of: source) { _, next in model.update(next, version: version, complete: complete) }
        .onChange(of: version) { _, next in model.update(source, version: next, complete: complete) }
        .onChange(of: complete) { _, next in model.update(source, version: version, complete: next) }
        .onChange(of: registered) { _, next in model.registered = next }
        .onChange(of: model.record) { _, record in if record != nil { model.decline() } }
    }
    private func openCardURL(_ url: URL) {
        guard AssistantViewLink.allowed(url) else { return }
        if url.scheme?.lowercased() == "https" {
            if let openLink { openLink(url) } else { openedWeb = LinkTarget(url: url) }
            return
        }
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return }
        let parts = components.percentEncodedPath.split(separator: "/").compactMap { String($0).removingPercentEncoding }
        switch components.host?.lowercased() {
        case "session" where parts.count == 2:
            app.open(SessionKey(group: location.scope.group, machineID: parts[0], sessionID: parts[1]))
        case "task" where parts.count == 1:
            app.selectHub(location.scope.group)
            TasksStore.shared.detailTask = parts[0]
        case "file" where parts.count == 2:
            openedFile = FileRef(group: location.scope.group, machineID: parts[0], path: parts[1])
        case "kb" where parts.count >= 2:
            guard KBKind(rawValue: parts[0]) != nil else {
                app.toast = Toast(String(localized: "Can't open this Knowledge path"), style: .error)
                return
            }
            app.selectHub(location.scope.group)
            let id = parts.dropFirst().joined(separator: "/")
            openedKnowledge = KBEntry(kind: parts[0], id: id, name: id, relevance: 0)
        default: app.toast = Toast(String(localized: "Can't open this card target"), style: .error)
        }
    }
    private var confirmationText: String {
        guard let selection = model.pending else { return "" }
        var text = selection.action.reply ?? selection.action.open ?? ""
        for (name, value) in selection.values.sorted(by: { $0.key < $1.key }) { text += String(localized: "\\n\(name): \(value)") }
        return text
    }
    private func status(_ record: ExtensionViewAction) -> String {
        switch record.status {
        case "pending": String(localized: "Selected · Waiting to send")
        case "sent": String(localized: "Selected · Sent")
        case "failed": String(localized: "Selected · Send failed")
        default: String(localized: "Selected · Syncing status")
        }
    }
}
