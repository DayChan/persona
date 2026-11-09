import SwiftUI

/// "Rule for This Chat / Sender / Repository…" (an item's menu, or Edit in Rules; docs/assistant/inbox.md "Policies"):
/// where its items go after triage and what the AI twin does with them. The hub decides in code; this only sets it.
/// The choices are inline lists, not segmented controls: four tiers in their words don't fit a phone's width.
struct InboxPolicySheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var model: InboxPolicyModel

    init(target: InboxPolicyTarget, deps: InboxPolicyModel.Deps = .live) {
        _model = State(initialValue: InboxPolicyModel(target: target, deps: deps))
    }

    var body: some View {
        NavigationStack {
            Form {
                if model.loaded {
                    choices
                } else {
                    Section { ProgressView().frame(maxWidth: .infinity) }
                }
            }
            .themedGroupedPage()
            .navigationTitle(title)
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Text("Cancel").lineLimit(1) }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button { save() } label: {
                        if model.busy { ProgressView().controlSize(.small) } else { Text("Save").lineLimit(1) }
                    }
                    .disabled(!model.canSave)
                    .accessibilityIdentifier("inbox.policy.save")
                }
            }
            .task { await model.load() }
        }
        #if os(macOS)
        .macSheet()
        #endif
    }

    @ViewBuilder private var choices: some View {
        @Bindable var model = model
        Section {
            Picker("Where It Goes [Inbox Policy]", selection: $model.tier) {
                ForEach(InboxPolicy.Tier.allCases, id: \.self) { Text($0.title).lineLimit(1).tag($0) }
            }
            .pickerStyle(.inline)
            .labelsHidden()
            .accessibilityIdentifier("inbox.policy.tier")
        } header: {
            Text("Where It Goes [Inbox Policy]")
        } footer: {
            Text(intro)
        }
        Section {
            Picker("AI Twin [Inbox Policy]", selection: $model.twin) {
                ForEach(InboxPolicy.Twin.allCases, id: \.self) { Text($0.title).lineLimit(1).tag($0) }
            }
            .pickerStyle(.inline)
            .labelsHidden()
            .accessibilityIdentifier("inbox.policy.twin")
        } header: {
            Text("AI Twin [Inbox Policy]")
        } footer: {
            Text("AI Twin First hands these to the AI twin even when they are not Needs You, before the others. Never AI Twin keeps them yours. The AI twin must be on.")
        }
        if model.policy != nil {
            Section {
                Button(role: .destructive) { save(remove: true) } label: {
                    Text("Remove Rule [Inbox Policy]").lineLimit(1).frame(maxWidth: .infinity)
                }
                .disabled(model.busy)
                .accessibilityIdentifier("inbox.policy.remove")
            }
        }
    }

    private var title: String { String(localized: "Rule for \(model.target.option.shortName)") }

    /// "Lark · Chat. The hub applies it…": what it covers, then how it works.
    private var intro: String {
        String(localized: "\(model.target.line). The hub applies it in code to what comes in from here, after the AI sorts it; a mute still wins.")
    }

    private func save(remove: Bool = false) {
        Task { if await model.save(remove: remove) { dismiss() } }
    }
}

/// "Rule for This Chat / Sender / Repository…" for each thing the item can be muted by, its name under it (an item's
/// menus, after the mute options).
struct InboxRuleButtons: View {
    var item: InboxItem
    var open: (InboxPolicyTarget) -> Void

    var body: some View {
        ForEach(item.mutes ?? [], id: \.self) { m in
            Button { open(InboxPolicyTarget(item: item, option: m)) } label: {
                Label(m.policyTitle, systemImage: Theme.Icon.inboxRule)
                Text(m.shortName)
            }
        }
    }
}

/// Rules (the Inbox's More menu, beside Muted): every source's rules, newest first. A tap edits one; a swipe or a long
/// press removes it.
struct InboxRulesView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var model: InboxRulesModel
    @State private var editing: InboxPolicyTarget?

    init(deps: InboxRulesModel.Deps = .live) { _model = State(initialValue: InboxRulesModel(deps: deps)) }

    var body: some View {
        NavigationStack {
            LoadableContent(state: model.state, retry: { Task { await model.load() } }) { rules in
                List {
                    ForEach(rules) { p in row(p).themedRow() }
                }
                .listStyle(.plain)
                .overlay {
                    if rules.isEmpty {
                        EmptyStateView(symbol: Theme.Icon.inboxRule, title: String(localized: "No Rules [Inbox Policy]"), message: Self.hint)
                    }
                }
            }
            .themedPage()
            .navigationTitle("Rules [Inbox Policy]")
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            #if os(macOS)
            .macSheetDone { dismiss() }
            #else
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) }
                }
            }
            #endif
            .task {
                await model.load()
                #if DEBUG
                // `-openInboxRule edit`: Edit on the newest rule, prefilled (screenshots).
                if UserDefaults.standard.string(forKey: "openInboxRule") == "edit", let p = model.state.value?.first {
                    editing = InboxPolicyTarget(policy: p)
                }
                #endif
            }
            .sheet(item: $editing, onDismiss: { Task { await model.load() } }) { InboxPolicySheet(target: $0) }
        }
        #if os(macOS)
        .macSheet()
        #endif
    }

    private static var hint: String {
        #if os(macOS)
        String(localized: "Right-click an item in the Inbox and pick a rule for its chat, sender, or repository.")
        #else
        String(localized: "Touch and hold an item in the Inbox and pick a rule for its chat, sender, or repository.")
        #endif
    }

    private func row(_ p: InboxPolicy) -> some View {
        Button { editing = InboxPolicyTarget(policy: p) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.inboxRule).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(p.name).font(Theme.Font.body).lineLimit(1)
                    Text(p.detail).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
            }
            .padding(.vertical, Theme.Space.xs)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("inbox-rule")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button(role: .destructive) { remove(p) } label: { Label("Remove Rule [Inbox Policy]", systemImage: Theme.Icon.delete) }
        }
        .contextMenu {
            Button { editing = InboxPolicyTarget(policy: p) } label: { Label("Edit Rule [Inbox Policy]", systemImage: Theme.Icon.edit) }
            Button(role: .destructive) { remove(p) } label: { Label("Remove Rule [Inbox Policy]", systemImage: Theme.Icon.delete) }
        }
    }

    private func remove(_ p: InboxPolicy) {
        Task { await model.remove(p) }
    }
}
