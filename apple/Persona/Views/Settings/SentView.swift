import SwiftUI

// Settings › Sent as You (#5278, docs/extensions/connectors.md "Sent as the user"): what Persona wrote in the user's name through the
// connectors, newest first. A row has the connector, where it went, how long ago, the first lines of what it said, how the
// gate let it through (orange where the user did not decide) and the chat that asked, which opens. A row opens a sheet with
// every field and the whole text. Load Earlier follows `next`; the chips (Lark and the connectors the loaded rows show)
// filter by connector. The logic and the words are in SentModel.swift; the web page is SentSettings.tsx.

/// The badge of how a send was let through: neutral, or orange where the user did not decide.
private struct SentBadge: View {
    var allowed: SentAllowed

    var body: some View {
        Chip(text: allowed.label, look: allowed.tone == .orange ? .tone(StatusTone(dot: Theme.caution, text: Theme.cautionText)) : .neutral, shrinks: true)
    }
}

/// Settings › Sent as You.
struct SentView: View {
    @Environment(AppModel.self) private var app
    @State private var page: SentModel
    @State private var detail: SentItem?

    init(model: SentModel = SentModel()) { _page = State(initialValue: model) }

    var body: some View {
        SettingsForm {
            if page.showsChips {
                #if os(macOS)
                chips
                #else
                Section { chips }
                    .listSectionSpacing(.compact)
                #endif
            }
            content
        }
        .themedPage()
        .detailTitle(String(localized: "Sent as You"))
        .task { await page.load() }
        .refreshable { await page.load() }
        #if os(macOS)
        .macRefreshes { await page.load() } // ⌘R: no pull to refresh on the Mac
        #endif
        .sheet(item: $detail) { item in
            SentDetailSheet(item: item, openChat: openChat)
        }
    }

    // MARK: The chips

    private var chips: some View {
        FlowLayoutRow {
            FilterChip(title: String(localized: "All"), on: page.filter == nil) { Task { await page.choose(nil) } }
                .accessibilityIdentifier("sent.filter.all")
            ForEach(page.choices) { c in
                FilterChip(title: c.name, on: page.filter == c.id) { Task { await page.choose(c.id) } }
                    .accessibilityIdentifier("sent.filter.\(c.id)")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .listRowBackground(Color.clear)
        .listRowInsets(EdgeInsets(top: Theme.Space.xxs, leading: 0, bottom: Theme.Space.xxs, trailing: 0))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(String(localized: "Filter by connector"))
    }

    // MARK: The list and its states

    @ViewBuilder private var content: some View {
        if let problem = page.problem {
            problemSection(problem)
        } else if let items = page.items {
            if items.isEmpty { emptySection } else { listSections(items) }
        } else {
            Section { ProgressView().frame(maxWidth: .infinity) }
        }
    }

    @ViewBuilder private func listSections(_ items: [SentItem]) -> some View {
        Section {
            ForEach(items) { item in
                SentRow(item: item, openDetail: { detail = item }, openChat: openChat)
            }
        } footer: {
            Text("What Persona sent in your name through your connectors, newest first. Select a row for the full text.")
        }
        if page.next != nil {
            Section {
                Button { Task { await page.loadMore() } } label: {
                    HStack(spacing: Theme.Space.m) {
                        Text("Load Earlier").lineLimit(1)
                        Spacer(minLength: 0)
                        if page.loadingMore { ProgressView() }
                    }
                }
                .modifier(SettingsButtonLook())
                .disabled(page.loadingMore)
                .accessibilityIdentifier("sent.loadEarlier")
                if let why = page.moreError {
                    Text(why).font(Theme.Font.label).foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("sent.loadEarlier.error")
                }
            }
        }
    }

    /// Nothing recorded: what is and is not recorded, or under a chip, that connector has nothing (Show All).
    private var emptySection: some View {
        Section {
            if let filter = page.filter {
                Text("Nothing recorded for \(SentText.connectorName(filter)) yet.").foregroundStyle(.secondary)
                    .accessibilityIdentifier("sent.emptyFiltered")
                Button { Task { await page.choose(nil) } } label: { Text("Show All [Sent as You]").lineLimit(1) }
                    .modifier(SettingsButtonLook())
                    .accessibilityIdentifier("sent.showAll")
            } else {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    Text("No sends recorded yet").font(Theme.Font.bodyStrong)
                    Text("Recorded: what Persona sends in your name through a connector (mail, GitHub comments, Lark writes), and sends an extension reports itself.")
                    Text("Not recorded: what you send yourself in the app, writes that were refused, declined or failed, and anything lark-cli writes.")
                    Text("Records are kept for 90 days by default.")
                }
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                .accessibilityIdentifier("sent.empty")
            }
        }
    }

    /// The hub's three answers that say what is wrong in a line; any other failure shows the usual words and Retry.
    private func problemSection(_ problem: SentProblem) -> some View {
        Section {
            Text(problem.text)
                .foregroundStyle(problem.kind == .error ? Theme.danger : .secondary)
                .accessibilityIdentifier("sent.problem.\(problem.kind)")
            if problem.kind == .error {
                Button { Task { await page.retry() } } label: { Label("Retry", systemImage: Theme.Icon.retry).lineLimit(1) }
                    .modifier(SettingsButtonLook())
                    .accessibilityIdentifier("sent.retry")
            }
        }
    }

    // MARK: The chat that asked

    /// Opens the chat a row names, in the hub's group the ledger was read from (a sheet over it is closed first).
    private func openChat(machine: String, session: String) {
        detail = nil
        let key = SessionKey(group: page.group, machineID: machine, sessionID: session)
        app.openLinked(key, item: nil)
    }
}

/// A send in the list: the connector, where it went, when, its first lines, how it was let through and who asked.
private struct SentRow: View {
    var item: SentItem
    var openDetail: () -> Void
    var openChat: (String, String) -> Void

    var body: some View {
        let name = SentText.connectorName(item.connector)
        let target = SentText.targetText(item)
        let lines = SentText.previewLines(item)
        let allowed = SentText.allowedBy(item.letThrough)
        let origin = SentText.originInfo(item.origin)
        // What names the chat link for VoiceOver (every row has the same words): the connector and where it went.
        let what = target == "—" ? name : "\(name) \(target)"
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Button(action: openDetail) {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                        Text(name).font(Theme.Font.bodyStrong).lineLimit(1).layoutPriority(1)
                        Text(target).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                        Spacer(minLength: Theme.Space.s)
                        Text(Fmt.relative(item.at)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).monospacedDigit()
                    }
                    if lines.isEmpty {
                        Text("No text recorded").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    } else {
                        ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                            Text(line).font(Theme.Font.secondary).lineLimit(1)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("sent.row.\(item.id)")

            FlowLayoutRow(spacing: Theme.Space.s) {
                SentBadge(allowed: allowed)
                if !origin.extensionID.isEmpty {
                    Chip(text: String(localized: "Extension-reported"), look: .neutral, shrinks: true)
                    Text(origin.extensionID).font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1)
                }
                if let chat = origin.conversation {
                    Button { openChat(chat.machine, chat.session) } label: {
                        HStack(spacing: Theme.Space.xxs) {
                            Text("Open Chat").lineLimit(1)
                            ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label)
                        }
                        .font(Theme.Font.label)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel(String(localized: "Open Chat: \(what)"))
                    .accessibilityIdentifier("sent.chat.\(item.id)")
                } else if origin.extensionID.isEmpty {
                    Text("No Chat [Sent as You]").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }
}

/// A label over its value in the details; the value wraps and can be selected.
private struct SentFact<Value: View>: View {
    var title: String
    var code = false
    /// One element for VoiceOver (the label and its value); not where the value holds a button.
    var combine = true
    @ViewBuilder var value: Value

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(code ? Theme.Font.code : Theme.Font.label).foregroundStyle(.secondary)
            value.frame(maxWidth: .infinity, alignment: .leading)
        }
        .accessibilityElement(children: combine ? .combine : .contain)
    }
}

/// One send in full: when, the action, every field of the target, how it was let through (the rule, the card), the chat, the
/// message id when there is one and the whole text, which can be copied.
private struct SentDetailSheet: View {
    @Environment(\.dismiss) private var dismiss
    var item: SentItem
    var openChat: (String, String) -> Void
    /// The copy button says Copied for a moment: in the sheet itself, as the app's toast can sit under a sheet on the Mac.
    @State private var copied = false

    var body: some View {
        let name = SentText.connectorName(item.connector)
        let fields = SentText.targetFields(item.target)
        let allowed = SentText.allowedBy(item.letThrough)
        let origin = SentText.originInfo(item.origin)
        NavigationStack {
            SettingsForm {
                // The order the web's details have: when, the action, where it went, how it was let through, who asked, the text.
                Section {
                    SentFact(title: String(localized: "Sent [Sent as You]")) {
                        Text(item.at.map { $0.formatted(date: .abbreviated, time: .standard) } ?? "—").font(Theme.Font.body)
                    }
                    SentFact(title: String(localized: "Action [Sent as You]")) {
                        Text(item.action.isEmpty ? "—" : item.action).font(Theme.Font.code).textSelection(.enabled)
                    }
                }
                Section {
                    if fields.isEmpty {
                        Text(verbatim: "—").foregroundStyle(.secondary)
                    } else {
                        ForEach(fields) { f in
                            SentFact(title: f.key, code: true) { Text(f.value).font(Theme.Font.body).textSelection(.enabled) }
                        }
                    }
                } header: {
                    Text("Sent To").textCase(nil)
                }
                Section {
                    SentFact(title: String(localized: "Allowed [Sent as You]")) {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            SentBadge(allowed: allowed)
                            if !allowed.detail.isEmpty { Text(allowed.detail).font(Theme.Font.secondary).foregroundStyle(.secondary) }
                        }
                    }
                    if !item.letThrough.interactionID.isEmpty {
                        SentFact(title: String(localized: "Card [Sent as You]")) {
                            Text(item.letThrough.interactionID).font(Theme.Font.code).textSelection(.enabled)
                        }
                    }
                    if !origin.extensionID.isEmpty {
                        SentFact(title: String(localized: "Extension [Sent as You]")) {
                            HStack(spacing: Theme.Space.s) {
                                Text(origin.extensionID).font(Theme.Font.body)
                                Chip(text: String(localized: "Extension-reported"), look: .neutral, shrinks: true)
                            }
                        }
                    }
                    SentFact(title: String(localized: "Chat"), combine: false) {
                        if let chat = origin.conversation {
                            Button { openChat(chat.machine, chat.session) } label: {
                                HStack(spacing: Theme.Space.xxs) {
                                    Text("Open Chat").lineLimit(1)
                                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label)
                                }
                            }
                            .buttonStyle(.borderless)
                            .accessibilityIdentifier("sent.detail.chat")
                        } else {
                            Text("No Chat [Sent as You]").font(Theme.Font.body).foregroundStyle(.secondary)
                        }
                    }
                    if !item.messageRef.isEmpty {
                        SentFact(title: String(localized: "Message ID")) {
                            Text(item.messageRef).font(Theme.Font.code).textSelection(.enabled)
                        }
                    }
                }
                Section {
                    if item.text.isEmpty {
                        Text("No text recorded").foregroundStyle(.secondary)
                    } else {
                        Text(item.text).font(Theme.Font.body).textSelection(.enabled)
                            .accessibilityIdentifier("sent.detail.text")
                        Button {
                            UIPasteboard.general.string = item.text
                            Haptics.success()
                            copied = true
                            AccessibilityNotification.Announcement(String(localized: "Copied [V N C Pi P Diagnostics 8f6f8d]")).post()
                        } label: {
                            if copied {
                                Label("Copied [V N C Pi P Diagnostics 8f6f8d]", systemImage: Theme.Icon.done).lineLimit(1)
                            } else {
                                Label("Copy Text", systemImage: Theme.Icon.copy).lineLimit(1)
                            }
                        }
                        .modifier(SettingsButtonLook())
                        .accessibilityIdentifier("sent.detail.copy")
                        .task(id: copied) { await clearCopied() }
                    }
                } header: {
                    Text("Text [Sent as You]").textCase(nil)
                } footer: {
                    if SentText.textCut(item.text) {
                        Text("The text is cut at \(SentText.textMost.formatted()) characters here. The message itself may be longer.")
                            .accessibilityIdentifier("sent.detail.cut")
                    }
                }
            }
            .themedPage()
            .navigationTitle(name)
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            #if os(macOS)
            .macSheetDone { dismiss() }
            #else
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1)
                        .accessibilityIdentifier("sent.detail.done")
                }
            }
            #endif
        }
        #if os(macOS)
        .macSheet()
        #else
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
        #endif
        .accessibilityIdentifier("sent.detail")
    }

    /// Copied goes back to Copy Text after a moment (a method: a multi-line closure in the modifier chain slows the type-check).
    private func clearCopied() async {
        guard copied else { return }
        do { try await Task.sleep(for: .seconds(2)) } catch { return }
        copied = false
    }
}
