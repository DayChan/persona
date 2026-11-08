import SwiftUI

// The main conversation's Compact context and New Main Chat (#1762, the hub's side in #1727; docs/assistant/assistant.md「手动压缩」
// 「新建主对话」): the menu entries, the note above the composer while it compacts, and the question before a new one.

extension AppModel {
    /// Compact context is offered: on the main conversation, when its hub can.
    func canCompact(_ key: SessionKey?) -> Bool {
        guard let key, key == assistantMain else { return false }
        return features(key.group).contains(MainConversationActions.compactFeature)
    }

    /// New Main Chat… is offered: on the main conversation, when its hub can.
    func canStartNewMain(_ key: SessionKey?) -> Bool {
        guard let key, key == assistantMain else { return false }
        return features(key.group).contains(MainConversationActions.newMainFeature)
    }

    /// Asks the hub to compact `key`; its progress comes on the session (`compaction`). A refusal (already answering, already compacting…)
    /// is the hub's own words.
    func compactConversation(_ key: SessionKey) async {
        guard let api = api(key.group) else { return }
        compactionRequests[key] = Date()
        Haptics.tap()
        do {
            try await api.compactConversation(key.sessionID, machineID: key.machineID)
        } catch {
            compactionRequests[key] = nil
            Haptics.error()
            toast = Toast(String(localized: "Couldn't start compacting: \(error.shown ?? String(localized: "Try again later."))"), style: .error)
        }
    }

    /// Archives the main conversation and leaves none: back on the empty main page, the next message starts the new
    /// one (with the old one's summary in front of it when `carry`). False when the hub refused (the toast says why).
    func startNewMain(carry: Bool) async -> Bool {
        guard let main = assistantMain, let api = api(main.group) else { return false }
        do {
            let r = try await api.newMainConversation(carry: carry, expect: main.sessionID)
            await toEmptyMain()
            Haptics.success()
            toast = Toast(r.carried == true ? String(localized: "New main chat started. It'll include a summary of the old one.") : String(localized: "New main chat started"), style: .success)
            return true
        } catch let e as APIError where e.code == "main_changed" {
            // Another device replaced it already: show what the hub has now.
            await toEmptyMain()
            toast = Toast(String(localized: "The main chat was replaced on another device. Refreshed."), style: .info)
            return true
        } catch {
            Haptics.error()
            toast = Toast(String(localized: "Couldn't start a new chat: \(error.shown ?? String(localized: "Try again later."))"), style: .error)
            return false
        }
    }

    private func toEmptyMain() async {
        assistantCurrent = nil
        await refreshAssistant()
    }
}

/// The menu entries (the top bar's menu, the Mac's toolbar menu, the sidebar row's context menu); nothing where they
/// do not apply. In its own view: the compaction state it reads changes with every push.
struct MainConversationMenu: View {
    @Environment(AppModel.self) private var model
    var key: SessionKey?

    var body: some View {
        if let key, model.canCompact(key) {
            let running = model.session(key)?.compaction?.state == .running || model.compactionRequests[key] != nil
            Button { Task { await model.compactConversation(key) } } label: {
                Label(running ? String(localized: "Compacting Context…") : String(localized: "Compact context"), systemImage: Theme.Icon.compactContext)
            }
            .disabled(running)
        }
        if model.canStartNewMain(key) {
            Button { model.newMainAsked = true } label: { Label("New Main Chat…", systemImage: Theme.Icon.newConversation) }
        }
    }
}

/// Works out what the composer's note shows for `key`'s compaction (`AppModel.compactionShown`): running while it runs or
/// was just asked for; done for a moment and failed until closed, only for a change this chat saw or one that just
/// finished. Drawn behind the composer, so it lives as long as the chat does.
struct CompactionTracker: View {
    @Environment(AppModel.self) private var model
    var key: SessionKey
    /// The state this chat saw last.
    @State private var seen: CompactionRun.State?

    private var run: CompactionRun? { model.session(key)?.compaction }

    private var signature: String {
        guard let run else { return "" }
        return [MainConversationActions.key(run), run.finishedAt.map { "\($0.timeIntervalSince1970)" } ?? "", run.error ?? ""].joined(separator: "|")
    }

    var body: some View {
        Color.clear
            .allowsHitTesting(false)
            .accessibilityHidden(true)
            .onChange(of: signature, initial: true) { old, new in
                // The hub's first word after Compact context was asked: it speaks for itself from now on.
                if old != new { model.compactionRequests[key] = nil }
                update()
                seen = run?.state
            }
            .onChange(of: model.compactionDismissed[key]) { update() }
            .task(id: model.compactionRequests[key]) { await requestExpires() }
            .task(id: model.compactionShown[key]) { await doneFades() }
    }

    private func update() {
        let shown = MainConversationActions.shown(run, seen: seen, now: Date(), dismissed: model.compactionDismissed[key])
        if model.compactionShown[key] != shown { model.compactionShown[key] = shown }
    }

    /// Asked, and the hub said nothing for a while: the note goes back to what the hub says.
    private func requestExpires() async {
        guard let at = model.compactionRequests[key] else { return }
        try? await Task.sleep(for: .seconds(MainConversationActions.requestGrace))
        if !Task.isCancelled, model.compactionRequests[key] == at { model.compactionRequests[key] = nil }
    }

    private func doneFades() async {
        guard case .done = model.compactionShown[key] else { return }
        try? await Task.sleep(for: .seconds(MainConversationActions.doneShown))
        if !Task.isCancelled, case .done = model.compactionShown[key] { model.compactionShown[key] = nil }
    }
}

extension AppModel {
    /// What the composer's note shows for `key`: asked here, running before the hub says so; else what the hub said.
    func compactionNote(_ key: SessionKey) -> MainConversationActions.Shown? {
        compactionRequests[key] != nil ? .running : compactionShown[key]
    }
}

/// The note's place above the composer: nothing at all while there is nothing to say.
struct CompactionSlot: View {
    @Environment(AppModel.self) private var model
    var key: SessionKey

    var body: some View {
        if let shown = model.compactionNote(key) {
            CompactionNote(key: key, shown: shown)
                .transition(.move(edge: .bottom).combined(with: .opacity))
        }
    }
}

/// On the composer: the tracker behind it (assistant conversations only) and the note's animation.
struct CompactionTracking: ViewModifier {
    @Environment(AppModel.self) private var model
    var key: SessionKey

    func body(content: Content) -> some View {
        content
            .animation(Theme.Motion.standard, value: model.compactionNote(key))
            .background { if model.session(key)?.assistant == true { CompactionTracker(key: key) } }
    }
}

/// Above the composer: Compacting Context…, Context compacted for a moment, Compacting failed: <why> with a close button.
struct CompactionNote: View {
    @Environment(AppModel.self) private var model
    var key: SessionKey
    var shown: MainConversationActions.Shown

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            switch shown {
            case .running:
                ProgressView().controlSize(.mini)
                Text("Compacting Context…").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 0)
            case .done:
                ThemedIcon(Theme.Icon.successFilled).font(Theme.Font.labelStrong).foregroundStyle(Theme.positive)
                Text("Context compacted").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 0)
            case .failed(let k, let error):
                ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.labelStrong).foregroundStyle(Theme.danger)
                Text("Compacting failed: \(error)").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
                Spacer(minLength: 0)
                Button {
                    withAnimation(Theme.Motion.standard) {
                        model.compactionDismissed[key] = k
                        model.compactionShown[key] = nil
                    }
                } label: {
                    ThemedIcon(Theme.Icon.close).font(Theme.Font.labelStrong)
                        .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .accessibilityLabel("Close")
                .accessibilityIdentifier("chat.compaction.close")
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.compaction")
    }

    private var isFailed: Bool { if case .failed = shown { true } else { false } }
}

/// The question before New Main Chat: the main conversation is archived (renamed Old main chat · to <date>, under Archived), the
/// next message starts the new one; Include Old Chat Summary on by default.
struct NewMainSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var carry = true
    @State private var busy = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text("The current main chat will be archived as “\(MainConversationActions.archivedTitle())” and you can find it under Archived. The new main chat starts with your next message.")
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Section {
                    Toggle(isOn: $carry) { Text("Include Old Chat Summary").lineLimit(1) }
                        .disabled(busy)
                        .accessibilityIdentifier("newmain.carry")
                } footer: {
                    Text("If the chat hasn't been compacted, only the last few messages come along. For a full summary, compact the context first.")
                }
            }
            .navigationTitle("New Main Chat?")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Text("Cancel").lineLimit(1) }
                        .disabled(busy)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await go() } } label: {
                        if busy { ProgressView().controlSize(.small) } else { Text("New Main Chat").lineLimit(1) }
                    }
                    .disabled(busy)
                    .accessibilityIdentifier("newmain.confirm")
                }
            }
        }
        #if os(macOS)
        .macSheet()
        #else
        .presentationDetents([.medium])
        #endif
    }

    private func go() async {
        busy = true
        let done = await model.startNewMain(carry: carry)
        busy = false
        if done { dismiss() }
    }
}
