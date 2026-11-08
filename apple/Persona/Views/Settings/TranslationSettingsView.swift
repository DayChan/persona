import SwiftUI

/// Settings › Translation (docs/apple/ios.md「翻译」): on / off, the target language, and what gets translated: mail (on), and the
/// Feishu chats picked here (none: Feishu has too many messages to translate them all).
struct TranslationSettingsView: View {
    @Environment(AppModel.self) private var model
    @AppStorage(TranslationPrefs.enabledKey) private var enabled = false
    @AppStorage(TranslationPrefs.targetKey) private var target = TranslationPrefs.defaultTarget
    @AppStorage(TranslationPrefs.mailKey) private var mail = true
    @AppStorage(TranslationPrefs.chatsKey) private var chatsRaw = ""
    @State private var picking = false

    private var chats: [TranslationPrefs.Chat] { TranslationPrefs.chats(chatsRaw) }
    private var larkChats: Bool { model.larkFeatures.chats }

    var body: some View {
        SettingsForm {
            Section {
                Toggle(isOn: $enabled) { Label("Auto-Translate", systemImage: Theme.Icon.translate) }
                ModelConfigurationLink(title: String(localized: "Translation Model"), purposeID: "translate")
            } footer: {
                Text("Content not in the target language is translated automatically, and you can switch back to the original anytime. Translation uses the translation model in Model Configuration.")
            }
            if enabled {
                Section {
                    Picker("Target Language", selection: $target) {
                        ForEach(TranslationPrefs.languages) { Text($0.name).tag($0.code) }
                    }
                }
                Section {
                    Toggle(isOn: $mail) { Label("Mail [Assistant View 3d708e]", systemImage: Theme.Icon.mail) }
                    // Feishu chats are picked from the hub's (`/api/v1/lark/…`, the Lark extension's).
                    if larkChats {
                        Button { picking = true } label: {
                            LabeledContent {
                                Text(chats.isEmpty ? String(localized: "None [Translation Settings View bcb53d]") : String(localized: "\(chats.count) Chats [Claude Dirs Section 537bee]")).foregroundStyle(.secondary).lineLimit(1)
                            } label: {
                                Label { Text("Lark Chats").foregroundStyle(Color.primary) } icon: { ThemedIcon(Theme.Icon.conversation) }
                            }
                        }
                        .accessibilityHint("Choose Lark chats to translate")
                    }
                } header: {
                    Text("What to Translate")
                } footer: {
                    if larkChats { Text("Lark has a lot of messages, so they aren't translated by default. Only messages from others in the chats selected here are translated.") }
                }
                if larkChats, !chats.isEmpty {
                    Section("Lark Chats to Translate") {
                        ForEach(chats) { c in
                            Text(c.name).lineLimit(1)
                                #if os(macOS)
                                // A Mac form has no swipe to delete: the row's menu takes it off (#1221).
                                .contextMenu {
                                    Button("Stop Translating", role: .destructive) {
                                        chatsRaw = TranslationPrefs.encode(chats.filter { $0.id != c.id })
                                    }
                                }
                                #endif
                        }
                            .onDelete { idx in
                                var list = chats
                                list.remove(atOffsets: idx)
                                chatsRaw = TranslationPrefs.encode(list)
                            }
                    }
                }
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Translation"))
        .navigationBarTitleDisplayMode(.inline)
        .animation(Theme.Motion.state, value: enabled)
        // The hub translates new mail before it is opened: it needs to know.
        .onChange(of: TranslatePrefsBody(enabled: enabled, target: target, mail: mail)) { _, _ in
            Translator.shared.syncPrefs(hubHas: nil)
        }
        .sheet(isPresented: $picking) {
            TranslationChatPicker(selected: chats) { chatsRaw = TranslationPrefs.encode($0) }
        }
    }
}

/// Picks the Feishu chats to translate: the recent ones (and the ones picked before), searchable by name.
struct TranslationChatPicker: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State var selected: [TranslationPrefs.Chat]
    var save: ([TranslationPrefs.Chat]) -> Void
    @State private var conversations: [LarkConversation] = []
    @State private var query = ""
    @State private var loading = true
    @State private var error: String?
    /// The chats picked as the sheet opened: Cancel asks first once they differ (Mac).
    @State private var opened: Set<String>?

    /// The picked ones first (so a chat no longer in the recent list can still be unpicked), then the recent ones.
    private var rows: [TranslationPrefs.Chat] {
        var seen = Set(selected.map(\.id))
        var out = selected
        for c in conversations where !seen.contains(c.id) {
            seen.insert(c.id)
            out.append(.init(id: c.id, name: c.name))
        }
        return query.isEmpty ? out : out.filter { $0.name.localizedCaseInsensitiveContains(query) }
    }

    var body: some View {
        NavigationStack {
            List {
                if let error { Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger) }
                ForEach(rows) { c in
                    let on = selected.contains { $0.id == c.id }
                    Button {
                        if on { selected.removeAll { $0.id == c.id } } else { selected.append(c) }
                        Haptics.tap()
                    } label: {
                        HStack(spacing: Theme.Space.m) {
                            let conv = conversations.first { $0.id == c.id }
                            LarkAvatarImage(name: c.name, url: conv?.avatar, isGroup: conv?.isGroup ?? true, size: Theme.Size.wellM)
                            Text(c.name).foregroundStyle(.primary).lineLimit(1)
                            Spacer(minLength: Theme.Space.s)
                            ThemedIcon(on ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                                .foregroundStyle(on ? Theme.accent : Color.secondary)
                        }
                    }
                    .accessibilityAddTraits(on ? .isSelected : [])
                }
            }
            .overlay { if loading && conversations.isEmpty { ProgressView() } }
            .pageSearchable(text: $query, prompt: String(localized: "Search Chats [Personal Agent List View feb955]"))
            .navigationTitle("Chats to Translate")
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: opened.map { $0 != Set(selected.map(\.id)) } ?? false) { dismiss() }
            .onAppear { if opened == nil { opened = Set(selected.map(\.id)) } }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done [Extension System Presentation c0b3fb]") {
                        save(selected)
                        dismiss()
                    }
                }
            }
            .task { await load() }
        }
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func load() async {
        guard let api = model.api else { return }
        defer { loading = false }
        do {
            // The pinned list and the conversation page are independent GETs; run them together.
            async let pinned = api.larkPinned()
            async let page = api.larkConversations()
            var list = try await pinned
            list += try await page.conversations.filter { c in !list.contains { $0.id == c.id } }
            conversations = list
        } catch {
            self.error = error.shown
        }
    }
}
