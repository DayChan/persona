import SwiftUI

/// Settings › Connectors › Lark Bot (docs/extensions/bots.md): talk to Persona from Feishu. The user makes a custom app with a bot on the
/// open platform, enters its id and secret here, pairs by sending the bot a code, and switches on what it may do.
struct FeishuBotView: View {
    @State private var model = FeishuBotModel()
    @State private var confirmUnpair = false

    var body: some View {
        SettingsForm {
            ChannelLinkSection(channelID: "feishu")
            statusSection
            credentialsSection
            if model.status?.hasSecret == true {
                pairingSection
                featuresSection
            }
            if let agents = model.status?.agents, !agents.isEmpty {
                Section("Agents You've Chatted With in Lark") {
                    ForEach(agents, id: \.sessionID) { a in
                        LabeledContent(a.title) { Text(a.machineID).lineLimit(1) }
                    }
                }
            }
            setupSection
        }
        .themedPage()
        .detailTitle(String(localized: "Lark Bot"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await model.load() }
        .refreshable { await model.load() }
        .task(id: model.status?.pairing?.code) { // waiting for the code: look again until it is used
            guard model.status?.pairing != nil else { return }
            while !Task.isCancelled, model.status?.pairing != nil, model.status?.owner == nil {
                try? await Task.sleep(for: .seconds(3))
                await model.load()
            }
        }
        .confirmationDialog("Unpair?", isPresented: $confirmUnpair, titleVisibility: .visible) {
            Button("Unpair", role: .destructive) { Task { await model.unpair() } }
        } message: {
            Text("The bot will stop responding to the current Lark account until you pair again.")
        }
    }

    // MARK: Sections

    private var statusSection: some View {
        Section {
            HStack(spacing: Theme.Space.m) {
                BrandMark(Theme.Brand.lark, side: Theme.Size.wellM)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(model.status?.botName ?? String(localized: "Lark Bot")).font(Theme.Font.heading).lineLimit(1)
                    HStack(spacing: Theme.Space.sm) {
                        Circle().fill(tint).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                        Text(model.stateText).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if model.loading || model.saving { ProgressView() }
            }
            .padding(.vertical, Theme.Space.xs)
            Toggle("Turn On Lark Bot", isOn: Binding(get: { model.status?.enabled ?? false }, set: { on in Task { await model.setEnabled(on) } }))
                .disabled(model.status == nil || model.saving)
            if let at = model.status?.lastEventAt, at > 0 {
                LabeledContent("Last Received") {
                    TimelineView(.periodic(from: .now, by: 30)) { ctx in
                        Text(Fmt.relative(Date(timeIntervalSince1970: TimeInterval(at)), now: ctx.date)).lineLimit(1)
                    }
                }
            }
            if let e = model.error, !e.isEmpty {
                LoadProblem(text: e, labeled: false)
            }
        } footer: {
            Text("Messaging this bot directly in Lark is talking to Persona. The bot only responds to you after pairing, and never in groups.")
        }
    }

    private var credentialsSection: some View {
        Section {
            #if os(macOS)
            // A Mac form puts the title on the left and the field on the right: the title stays the field's name, what
            // to type goes in the prompt (#1221).
            TextField("App ID", text: $model.appID, prompt: Text("Required")).autocorrectionDisabled()
            SecureField("App Secret", text: $model.appSecret, prompt: Text(model.status?.hasSecret == true ? String(localized: "Saved. Leave empty to keep") : String(localized: "Required")))
            #else
            TextField("App ID", text: $model.appID)
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                .font(Theme.Font.code)
            SecretField(model.status?.hasSecret == true ? String(localized: "App Secret (saved, leave empty to keep)") : "App Secret", filled: String(localized: "Entered. Tap Verify and Save"),
                        text: $model.appSecret, identifier: "bot.appSecret")
            #endif
            Picker("Platform", selection: $model.domain) {
                Text("Lark [Inbox Event Views 7e4419]").tag("feishu")
                Text("Lark").tag("lark")
                if model.domain != "feishu" && model.domain != "lark" { Text(model.domain).tag(model.domain) }
            }
            #if os(macOS)
            // A standard button at the group's trailing end, not a bar the width of the form (#1221).
            HStack {
                Spacer(minLength: 0)
                Button { Task { await model.saveCredentials() } } label: { Text("Verify and Save").lineLimit(1) }
                    .formButton(prominent: true)
                    .disabled(!model.credentialsChanged || model.appID.isEmpty || model.saving)
            }
            #else
            Button {
                Task { await model.saveCredentials() }
            } label: {
                Text("Verify and Save").lineLimit(1).frame(maxWidth: .infinity)
            }
            .disabled(!model.credentialsChanged || model.appID.isEmpty || model.saving)
            #endif
        } header: {
            Text("App Credentials")
        } footer: {
            if model.status?.fromConfig == true {
                Text("Currently using the credentials in manager.json. Saving here switches to these.")
            } else {
                Text("The App Secret is stored encrypted on the hub and won't be shown again.")
            }
        }
    }

    @ViewBuilder private var pairingSection: some View {
        Section {
            if let owner = model.status?.owner {
                LabeledContent("Paired") { Text(owner.name.isEmpty ? String(localized: "Lark user") : owner.name).lineLimit(1) }
                if let target = model.status?.target, !target.isEmpty {
                    LabeledContent("Messages Go To") { Text(target).lineLimit(1) }
                }
                Button {
                    Task { await model.test() }
                } label: {
                    Label("Send Test Message", systemImage: Theme.Icon.sendMail).lineLimit(1)
                }
                .disabled(model.saving || model.status?.connected != true)
                Button(role: .destructive) { confirmUnpair = true } label: {
                    Label("Unpair", systemImage: Theme.Icon.close).lineLimit(1)
                }
            } else if let p = model.status?.pairing, Date(timeIntervalSince1970: TimeInterval(p.expiresAt)) > .now {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    Text("Send this number to the bot in Lark").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    Text(p.code).font(Theme.Font.display).monospacedDigit().textSelection(.enabled)
                    TimelineView(.periodic(from: .now, by: 1)) { ctx in
                        let left = max(0, Int(Date(timeIntervalSince1970: TimeInterval(p.expiresAt)).timeIntervalSince(ctx.date)))
                        Text("Valid for \(left / 60) min \(left % 60) s").font(Theme.Font.label).foregroundStyle(.tertiary).monospacedDigit()
                    }
                }
                .padding(.vertical, Theme.Space.xs)
                Button { UIPasteboard.general.string = p.code; Haptics.success() } label: {
                    Label("Copy Pairing Code [Feishu Bot View f5d60b]", systemImage: Theme.Icon.copy).lineLimit(1)
                }
            } else {
                Button {
                    Task { await model.pair() }
                } label: {
                    Label("Pair", systemImage: Theme.Icon.pairing).lineLimit(1)
                }
                .disabled(model.saving || model.status?.enabled != true)
            }
        } header: {
            Text("Pair")
        } footer: {
            if model.status?.owner == nil {
                Text(model.status?.enabled == true ? String(localized: "Tap Pair to get a 6-digit number, then send it to the bot in Lark within 10 minutes.") : String(localized: "Turn on the Lark bot before pairing."))
            }
        }
    }

    private var featuresSection: some View {
        Section {
            feature(\.main, Theme.Icon.persona, "Chat with Persona", "Messages go to the main chat (/new, /list to switch threads); replies come back to Lark")
            feature(\.cards, Theme.Icon.needsYou, "Questions and Approvals", "When Persona or an agent you drive from Lark needs an answer or approval, you get a card in Lark. One tap does it")
            feature(\.allCards, Theme.Icon.notifications, "Questions from All Agents", "Questions and approvals from every agent on all your computers go to Lark")
                .disabled(!features.cards)
            feature(\.pushes, Theme.Icon.unmute, "Also Send Notifications to Lark", "Persona's notifications and scheduled task results are also sent to Lark")
            feature(\.sync, Theme.Icon.inProgress, "Sync Main Chat", "Replies in the main chat, from the app or scheduled tasks, are also sent to Lark")
            feature(\.reports, Theme.Icon.inProgress, "Send Task Progress to Lark", "When tasks from the main chat finish, wait for you or get interrupted, they're combined into one card sent to Lark, with stages and links")
            // Shown only when the hub's answer has it, like the board below.
            if model.hubKnowsOnlyConclusions {
                feature(\.onlyConclusions, Theme.Icon.concluded, "Only Conclusions to Lark", "Tasks from the main chat no longer send their progress (a finished turn, waiting for you, interrupted), which stays in the to-do list and the briefing; a conclusion card still comes when there is one: done, can't be done, or needs your decision")
                    .disabled(!features.reports) // with task progress off the hub sends neither progress nor conclusions
            }
            feature(\.agents, Theme.Icon.agents, "Drive Agents", "/agents picks Claude Code / Codex on any of your computers to chat with in Lark; /start opens a new one")
            // A hub from before these leaves them out of its answer: nothing to show, since it would ignore a change.
            if model.hubKnowsBoard {
                feature(\.board, Theme.Icon.pinned, "Waiting-for-You Board in Lark", "/tasks sends one card listing everything waiting for you, then keeps it up to date in place instead of sending a new one")
            }
            if model.hubKnowsNudge { nudgePicker }
        } header: {
            Text("What It Can Do")
        } footer: {
            Text("The main chat can use all your data (Lark, email, your phone's calendar and location…), and the bot's messages stay in Lark. Anything done on your behalf still asks you first.")
        }
    }

    private var nudgePicker: some View {
        Picker(selection: Binding(get: { model.nudgeAfter }, set: { m in Task { await model.setNudgeAfter(m) } })) {
            ForEach(model.nudgeChoices, id: \.self) { Text(FeishuBotModel.nudgeLabel($0)).tag($0) }
        } label: {
            Label {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Nudge About Unanswered Cards")
                    Text("A card still unanswered after this long gets one reply under it in Lark (in a group topic it mentions you), outside your quiet hours")
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            } icon: {
                ThemedIcon(Theme.Icon.replyReminder).foregroundStyle(Theme.accent)
            }
        }
        .disabled(model.saving)
    }

    private var setupSection: some View {
        Section {
            step(1, "Create a custom app on the Lark Open Platform and enable the Bot capability")
            step(2, "Under Permissions, enable: read and send direct messages (im:message, im:message:send_as_bot), receive direct messages sent to the bot, and upload and download message resources. Reading contact names helps too (contact:user.base:readonly)")
            step(3, "In Events & Callbacks, choose “Receive events through persistent connection” and subscribe to “Receive messages” and “Card action callbacks”")
            step(4, "Publish a version (your company's admin may need to approve it), then enter the App ID and App Secret above")
        } header: {
            Text("How to Set Up the Bot")
        } footer: {
            Text("The hub opens the persistent connection itself, so it doesn't need a public address.")
        }
    }

    // MARK: Pieces

    private var features: FeishuBotStatus.Features { model.features }

    private var tint: Color {
        guard let st = model.status, st.enabled else { return .secondary }
        if !st.connected { return Theme.danger }
        return st.owner == nil ? Theme.caution : Theme.positive
    }

    private func feature(_ key: WritableKeyPath<FeishuBotStatus.Features, Bool>, _ symbol: String, _ title: LocalizedStringResource, _ detail: LocalizedStringResource) -> some View {
        SettingsSwitchRow(title: String(localized: title), detail: String(localized: detail), symbol: symbol, iconColor: Theme.accent,
                          isOn: Binding(get: { model.features[keyPath: key] }, set: { on in Task { await model.set(key, on) } }))
        .disabled(model.saving)
    }

    private func step(_ n: Int, _ text: LocalizedStringKey) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            Text("\(n)").font(Theme.Font.captionStrong).foregroundStyle(Theme.accent).monospacedDigit()
            Text(text).font(Theme.Font.caption).foregroundStyle(.secondary)
        }
        .padding(.vertical, Theme.Space.xxs)
    }
}
