import BeautifulUI
import MarkdownUI
import SwiftUI

extension InteractionKind {
    var title: String {
        switch self {
        case .question: String(localized: "Claude Has a Question")
        case .plan: String(localized: "Plan Awaiting Approval")
        case .permission: String(localized: "Needs Your Permission")
        case .screen: String(localized: "Terminal Needs You")
        }
    }

    var symbol: String {
        switch self {
        case .question: "questionmark.bubble.fill"
        case .plan: "list.bullet.clipboard.fill"
        case .permission: "hand.raised.fill"
        case .screen: Theme.Icon.terminalFilled
        }
    }
}

extension Interaction {
    /// The assistant asks through its ask_user tool: it is Persona asking, not a Claude Code agent; a Codex card names
    /// Codex.
    var title: String {
        if toolName == "ask_user" { return String(localized: "Persona Has a Question") }
        if isSecretRun { return secretRunTitle }
        if let name = agentName {
            switch kind {
            case .question: return String(localized: "\(name) Has a Question")
            case .permission: return String(localized: "\(name) Needs Your Permission")
            default: break
            }
        }
        return kind.title
    }

    /// Describe the actual remembered rule, not just the command or file in this request.
    var alwaysScopeDetail: String? {
        if let alwaysLabel, !alwaysLabel.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return alwaysLabel }
        guard agentName == nil else { return nil }
        let keys = alwaysKeys ?? []
        if !keys.isEmpty, keys.allSatisfy({ $0.hasPrefix("tool:Read(") && $0.hasSuffix("/**)") }) {
            return String(localized: "All reads in this directory in this conversation")
        }
        if !keys.isEmpty, keys.allSatisfy({ $0.hasPrefix("tool:Edit(") && $0.hasSuffix("/**)") }) {
            return String(localized: "All file edits in this directory in this conversation")
        }
        if keys.isEmpty || keys.allSatisfy({ $0 == "tool:" + (toolName ?? "") }) {
            if toolName == "Bash" { return String(localized: "All commands in this conversation") }
            return String(localized: "All uses of this tool in this conversation")
        }
        return String(localized: "The approved scope remains allowed in this conversation")
    }

    /// The agent's name on a card not from Claude Code: one the node read off a Codex / TRAEX TUI, or an agent
    /// runtime's own ("DSH", the hub's agent_label; docs/integrations/runtimes.md). Nil for Claude's.
    var agentName: String? {
        switch agent {
        case "codex": "Codex"
        case "traex": "TRAEX"
        case nil, "", "claude": nil
        default: agentLabelHint ?? agent
        }
    }
}

/// Compact card on the chat list: enough context plus one-tap answers for simple questions. A terminal dialog
/// reads like the chat's panel: its question, the command it is about, its options as quick answers.
struct InteractionPreviewCard: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    /// Its machine's name and its agent's label ("Claude", "Codex"): given, not read here. The Agents list holds what
    /// it shows still while it is scrolled (`AgentsFeed`); a card reading them itself was drawn again with every
    /// push, measured itself anew, and the list moved under the finger.
    var machine: String?
    var agentLabel = "Claude"
    /// The list's cell is the card (same width and corners as the agents' rows): no chrome of its own.
    var inCell = false
    var open: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                Image(systemName: interaction.kind.symbol)
                    .foregroundStyle(StatusPalette.needsYou.dot)
                    .scaledFrame(width: Theme.Size.icon) // one column: the titles line up card to card
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.heading).lineLimit(1)
                    Text(interaction.placeLine(machine: machine))
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer()
                #if !os(macOS) // a phone's row says it opens; a Mac's list row does not (#2113)
                Image(systemName: Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                #endif
            }
            if !summary.isEmpty {
                Text(summary)
                    .font(Theme.Font.body).lineHeight(.body)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .modifier(UnderTitle())
            }
            if let detail, !detail.isEmpty {
                Text(detail)
                    .font(Theme.Font.code)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .padding(Theme.Space.s)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    .modifier(UnderTitle())
            }
            quickActions
        }
        .modifier(PreviewChrome(on: !inCell))
        .contentShape(.rect)
        .onTapGesture(perform: open)
    }

    /// The Mac: the summary and the command under the title, on its leading edge (after the icon's column), as a list
    /// row's lines sit under its title (#2318). iOS: on the card's edge, as it always was.
    private struct UnderTitle: ViewModifier {
        func body(content: Content) -> some View {
            #if os(macOS)
            HStack(spacing: Theme.Space.s) { // the header row's icon column and its gap
                Spacer(minLength: 0).scaledFrame(width: Theme.Size.icon)
                content
            }
            #else
            content
            #endif
        }
    }

    private struct PreviewChrome: ViewModifier {
        var on: Bool
        func body(content: Content) -> some View {
            if on { content.card() } else { content.padding(.vertical, Theme.Space.xs) }
        }
    }

    /// A terminal dialog that reads as a question is titled like one (as in the chat's panel).
    private var title: String {
        if interaction.kind == .screen, interaction.screenDialog != nil {
            return String(localized: "\(agentLabel) Has a Question")
        }
        return interaction.title
    }

    /// Under the summary, in code type: the command a terminal dialog is about, or (no dialog read) the last lines
    /// the terminal shows.
    private var detail: String? {
        guard interaction.kind == .screen else { return nil }
        if let d = interaction.screenDialog { return d.detail.map { ScreenParser.plainLines($0, max: 3) } }
        return ScreenParser.waitingOn(interaction.screen ?? "")
    }

    private var summary: String { interaction.askLine(includeToolName: true) ?? "" }

    @ViewBuilder private var quickActions: some View {
        switch interaction.kind {
        case .question:
            if let qs = interaction.questions, qs.count == 1, let q = qs.first, !q.multiSelect, q.options.count <= 4 {
                FlowButtons(labels: q.options.map(\.label)) { label in
                    Task { await model.resolve(interaction, Resolution(answers: [q.question: label])) }
                }
            }
        case .permission:
            if interaction.requiresInput {
                Button { open() } label: { Text("Open to Fill In").lineLimit(1) }
                    .themedButton(prominent: true)
            } else {
                HStack {
                    Button(interaction.agentName == nil ? String(localized: "Allow [Notification Actions ce7ef2]") : String(localized: "Approve [Interaction Views 905819]")) { Task { await model.resolve(interaction, Resolution(allow: true)) } }
                        .themedButton(prominent: true)
                    Button("Deny") { Task { await model.resolve(interaction, Resolution(allow: false)) } }
                        .themedButton()
                }
                .controlSize(.small)
            }
        case .screen:
            // Only when the hub read the dialog too (it checks the screen before pressing the key).
            if interaction.dialog != nil, let d = interaction.screenDialog {
                // Every plain option: a length cap here silently dropped options whose labels ran long — wrapped
                // terminal rows rejoin into one label, and a "(Recommended)" suffix (drawn as the Recommended chip, not
                // words) pushed the first option over most often — and the row still looked complete (#1069).
                // Long labels scroll in the row, single-line, as the hook path's already do.
                let plain = d.options.filter { $0.kind == .plain }
                if !d.multi, (1...4).contains(plain.count) { // several ticks go together: in the full card
                    FlowButtons(labels: plain.map(\.label)) { label in
                        guard let opt = plain.first(where: { $0.label == label }) else { return }
                        Task { await model.resolve(interaction, Resolution(option: opt.key)) }
                    }
                }
            }
        default:
            EmptyView()
        }
    }
}

struct FlowButtons: View {
    var labels: [String]
    var action: (String) -> Void

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.s) {
                ForEach(labels, id: \.self) { label in
                    let (text, recommended) = QuestionForm.display(label)
                    Button { action(label) } label: {
                        HStack(spacing: Theme.Space.sm) {
                            Text(text).lineLimit(1)
                            if recommended { RecommendedTag() }
                        }
                    }
                    .themedButton()
                    .controlSize(.small)
                    .accessibilityLabel(text + (recommended ? String(localized: ", recommended", comment: "Accessibility suffix: the recommended option") : ""))
                }
            }
        }
        .scrollClipDisabled()
    }
}

/// 「推荐」 beside the option Claude suggests ("… (Recommended)", `QuestionForm.display`): the question panel, the
/// terminal dialog and the quick answers on the Agents card draw the same tag, in the AI's color (it is Claude's
/// suggestion; the accent would read as something to tap).
struct RecommendedTag: View {
    var body: some View { Chip(text: String(localized: "Recommended"), look: .tint(Theme.ai)) }
}

/// Full interaction UI pinned above the composer in a chat. Capped in height (scrolls inside) and collapsible,
/// so a long question never hides the conversation it is about.
struct InteractionPanel: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var collapsed = false
    @State private var formHeight: CGFloat = 0
    /// What the page has (PendingCards measures it): the form's cap. Not `containerRelativeFrame`: inside the cards'
    /// scroll area the container is that area, and every card cut itself off at half of it (#926).
    @Environment(\.cardRoom) private var room
    @Environment(\.cardFits) private var fits
    /// The header row's height: with the card's margins, what of `fits` the form may not have.
    @State private var headerHeight: CGFloat = 0
    /// The plan full screen. Its button sits in this header: floating over the plan it covered the last line.
    @State private var showFullPlan = false

    /// The form's cap while the keyboard is up (`cardFits`): the area less the card's header and margins; at least a tap
    /// target, so a sliver of a form never shows (a small iPhone with the shell's composer leaves the cards little more).
    private var fitsCap: CGFloat {
        guard fits > 0, headerHeight > 0 else { return .infinity }
        let row = headerAway ? 0 : headerHeight + Theme.Space.m // the row and the gap under it
        return max(fits - row - Theme.Space.m - Theme.Space.l, Theme.Size.tapTarget) // and the margins
    }

    /// The keyboard leaves the card so little that under its header row the form would show the question and hardly
    /// a choice (a small iPhone, the shell's composer): the row (its title, ⋯ and fold) steps aside while the keyboard
    /// is up, so the first choice shows (#1912). The question's own line (`header · 1/2`) stays.
    private var headerAway: Bool {
        guard fits > 0, headerHeight > 0, !collapsed else { return false }
        return fits - headerHeight - Theme.Space.m - Theme.Space.m - Theme.Space.l < Theme.Size.box120
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if !headerAway { header }
            if !collapsed {
                ScrollView {
                    form.onGeometryChange(for: CGFloat.self) { $0.size.height } action: { formHeight = $0 }
                }
                .scrollBounceBehavior(.basedOnSize)
                // At most a little over half of what the page has (not the screen: iPhone Duo's displays differ), and
                // with the keyboard up no more than lets the whole card fit the cards' area.
                .frame(height: room > 0 ? min(formHeight == 0 ? room : formHeight, room * 0.55, fitsCap) : nil)
                .transition(.opacity)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Theme.Space.m)
        .padding(.bottom, Theme.Space.l)
        .foregroundStyle(Theme.primaryText)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .transition(.move(edge: .bottom).combined(with: .opacity))
    }

    /// The title, ⋯ and fold; a tap anywhere on it folds the card.
    private var header: some View {
        HStack(spacing: Theme.Space.sm) {
            Image(systemName: interaction.kind.symbol)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(StatusPalette.needsYou.dot)
            Text(panelTitle).font(Theme.Font.labelStrong).foregroundStyle(StatusPalette.needsYou.text).lineLimit(1)
            VoiceAskMark(interaction: interaction) // Asking by Voice (docs/assistant/voice.md 1.2 H)
            if collapsed {
                Text(previewText).font(Theme.Font.secondary).foregroundStyle(.primary).lineLimit(1)
            }
            Spacer(minLength: 4)
            if interaction.kind == .plan && !collapsed {
                Button { showFullPlan = true } label: {
                    Image(systemName: Theme.Icon.enlarge)
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("View plan full screen")
            }
            Menu {
                Button { Task { await model.resolve(interaction, .dismissed) } } label: {
                    Label("Handle in Terminal on Computer", systemImage: Theme.Icon.machine)
                }
            } label: {
                Image(systemName: Theme.Icon.more)
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                    .contentShape(.rect)
            }
            .labelOnlyMenu() // just the ⋯, next to the fold ⌄
            #if os(macOS)
            .help("More")
            #endif
            Image(systemName: Theme.Icon.expand)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
                .rotationEffect(.degrees(collapsed ? 180 : 0))
                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
        }
        .contentShape(.rect)
        .onTapGesture { withAnimation(Theme.Motion.standard) { collapsed.toggle() } }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { headerHeight = $0 }
    }

    @ViewBuilder private var form: some View {
        switch interaction.kind {
        case .question: QuestionForm(interaction: interaction)
        case .plan: PlanForm(interaction: interaction, showFull: $showFullPlan)
        case .permission:
            if interaction.agentName != nil { AgentPermissionForm(interaction: interaction) } else { PermissionForm(interaction: interaction) }
        case .screen: ScreenForm(interaction: interaction)
        }
    }

    private var previewText: String {
        switch interaction.kind {
        case .question: interaction.questions?.first?.question ?? ""
        case .plan: PlanText.title(interaction.plan ?? "")
        case .permission: interaction.summary ?? ""
        case .screen: interaction.screenDialog?.question ?? ""
        }
    }

    /// A terminal dialog that reads as a question is titled like one.
    private var panelTitle: String {
        if interaction.kind == .screen, interaction.screenDialog != nil {
            let name = model.session(interaction.sessionKey)?.agentLabel ?? "Claude"
            return String(localized: "\(name) Has a Question")
        }
        return interaction.title
    }
}

/// AskUserQuestion, iOS-list style: the question, its options in one grouped box (numbered like the terminal, a
/// check when picked, Recommended for the suggested one), "Other" as the box's last row. One question, one choice: a tap answers.
/// Several questions: "header · 1/2" on top, small Previous / Next / Submit below.
struct QuestionForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var picks: [String: Set<String>] = [:]
    @State private var other: [String: String] = [:]
    @State private var page = 0
    @State private var sending = false
    @State private var writing = false
    @FocusState private var otherFocused: Bool

    private var questions: [Question] { interaction.questions ?? [] }

    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUIQuestionForm(interaction: interaction) } else { plain }
    }

    @ViewBuilder private var plain: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            DelegateNotes(interaction: interaction)
            if questions.indices.contains(page) {
                let q = questions[page]
                if questions.count > 1 || q.header != nil || q.multiSelect {
                    Text([q.header.flatMap { $0.isEmpty ? nil : $0 },
                          questions.count > 1 ? "\(page + 1)/\(questions.count)" : nil,
                          q.multiSelect ? String(localized: "Select multiple") : nil].compactMap { $0 }.joined(separator: " · "))
                        .monospacedDigit()
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.secondary)
                }
                Text(q.question)
                    .font(Theme.Font.heading)
                    .fixedSize(horizontal: false, vertical: true)
                options(q)
                    .id(page)
                    .transition(.asymmetric(insertion: .move(edge: .trailing).combined(with: .opacity), removal: .opacity))
            }
            if needsFooter { footer }
        }
        .onChange(of: page) { _, p in writing = questions.indices.contains(p) && !(other[questions[p].question] ?? "").isEmpty }
    }

    // MARK: Options

    private func options(_ q: Question) -> some View {
        VStack(spacing: 0) {
            ForEach(Array(q.options.enumerated()), id: \.element.label) { i, opt in
                if i > 0 { Divider().padding(.leading, 50) }
                option(q, opt, number: i + 1)
            }
            Divider().padding(.leading, 50)
            otherRow(q)
        }
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }

    private func option(_ q: Question, _ opt: QuestionOption, number: Int) -> some View {
        let selected = picks[q.question, default: []].contains(opt.label)
        let (label, recommended) = Self.display(opt.label)
        let assistantPick = assistantPicks(q).contains(opt.label)
        return Button { pick(q, opt) } label: {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                ZStack {
                    Circle().fill(selected ? Theme.accent : Theme.Fill.chip)
                    if selected {
                        Image(systemName: Theme.Icon.done).font(Theme.Font.labelHeavy).dynamicTypeSize(...DynamicTypeSize.xxLarge).foregroundStyle(.white)
                    } else {
                        Text("\(number)").font(Theme.Font.labelStrong.monospacedDigit()).foregroundStyle(.secondary)
                    }
                }
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.sm) {
                        Text(label).font(Theme.Font.heading).foregroundStyle(.primary)
                        if recommended { RecommendedTag() }
                        if assistantPick { AssistantPickTag() }
                    }
                    if let d = opt.description, !d.isEmpty {
                        Text(d).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 0)
            }
            .multilineTextAlignment(.leading)
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .background(selected ? Theme.Fill.chip : .clear)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(sending)
        .accessibilityLabel(label + (recommended ? String(localized: ", recommended", comment: "Accessibility suffix: the recommended option") : "")
                            + (assistantPick ? String(localized: ", the assistant's pick", comment: "Accessibility suffix: the option the assistant recommends") : "")
                            + (selected ? String(localized: ", selected", comment: "Accessibility suffix: a selected option") : ""))
    }

    /// The options the assistant recommends on `q` (#544: its `delegate.recommend` in the options' own words).
    private func assistantPicks(_ q: Question) -> [String] {
        let picks = Delegated.recommendedLabels(questions, interaction.delegate?.recommend)
        guard let i = questions.firstIndex(where: { $0.question == q.question }), picks.indices.contains(i) else { return [] }
        return picks[i]
    }

    /// "Other": a quiet row; tapping it turns it into a field with its own send arrow.
    @ViewBuilder private func otherRow(_ q: Question) -> some View {
        let text = Binding(get: { other[q.question] ?? "" }, set: { other[q.question] = $0 })
        HStack(alignment: .center, spacing: Theme.Space.m) {
            Image(systemName: Theme.Icon.edit)
                .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xxLarge)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                .background(Theme.Fill.chip, in: .circle)
            if writing {
                TextField(String(localized: "Write your answer"), text: text, axis: .vertical)
                    .lineLimit(1...4)
                    .focused($otherFocused)
                    .onChange(of: text.wrappedValue) { _, v in
                        if !v.trimmingCharacters(in: .whitespaces).isEmpty, !q.multiSelect { picks[q.question] = [] }
                    }
                if !needsFooter, !text.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Button { submit() } label: {
                        Image(systemName: Theme.Icon.submit).font(Theme.Font.title).foregroundStyle(Theme.accent)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Send answer")
                }
            } else {
                Text("Other answer…").foregroundStyle(.secondary)
                Spacer(minLength: 0)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .contentShape(.rect)
        .onTapGesture {
            guard !writing else { return }
            withAnimation(Theme.Motion.standard) { writing = true }
            otherFocused = true
        }
    }

    // MARK: Footer (several questions, or several choices)

    private var needsFooter: Bool { questions.count > 1 || questions.first?.multiSelect == true }

    private var footer: some View {
        HStack(spacing: Theme.Space.m) {
            if page > 0 {
                Button { withAnimation(Theme.Motion.standard) { page -= 1 } } label: { Text("Previous [Interaction Views 39b1c2]").lineLimit(1) }
                    .themedButton()
            }
            Spacer(minLength: 0)
            Button { advance() } label: {
                HStack(spacing: Theme.Space.sm) {
                    if sending { ProgressView().controlSize(.small).tint(.white) }
                    Text(isLast ? String(localized: "Submit") : String(localized: "Next [Interaction Views f82c68]")).lineLimit(1)
                }
                .padding(.horizontal, Theme.Space.s)
            }
            .themedButton(prominent: true)
            .disabled(!canAdvance || sending)
        }
        .controlSize(.regular)
    }

    // MARK: Logic

    /// "Take the longest part (Recommended)" → ("Take the longest part", true). The answer sent back keeps the original label.
    static func display(_ label: String) -> (String, Bool) { QuestionOption.display(label) }

    private func pick(_ q: Question, _ opt: QuestionOption) {
        Haptics.tap()
        var set = picks[q.question, default: []]
        if q.multiSelect {
            if set.contains(opt.label) { set.remove(opt.label) } else { set.insert(opt.label) }
        } else {
            set = [opt.label]
            other[q.question] = ""
            writing = false
        }
        withAnimation(.snappy(duration: 0.2)) { picks[q.question] = set }
        guard !q.multiSelect else { return }
        if questions.count == 1 {
            submit() // one question, one choice: the tap is the answer
        } else if page < questions.count - 1 {
            Task { try? await Task.sleep(for: .milliseconds(250)); withAnimation(Theme.Motion.standard) { page += 1 } }
        }
    }

    private var isLast: Bool { page >= questions.count - 1 || allAnswered }

    private func advance() {
        if isLast { submit() } else { withAnimation(Theme.Motion.standard) { page += 1 } }
    }

    private func answer(_ q: Question) -> String? {
        let custom = (other[q.question] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !custom.isEmpty { return custom }
        let set = picks[q.question, default: []]
        guard !set.isEmpty else { return nil }
        return q.options.map(\.label).filter(set.contains).joined(separator: ", ")
    }

    private var allAnswered: Bool { questions.allSatisfy { answer($0) != nil } }
    private var canAdvance: Bool { questions.indices.contains(page) && answer(questions[page]) != nil }

    private func submit() {
        if !allAnswered {
            if let next = questions.firstIndex(where: { answer($0) == nil }) { withAnimation(Theme.Motion.standard) { page = next } }
            return
        }
        sending = true
        otherFocused = false
        var answers: [String: String] = [:]
        for q in questions { answers[q.question] = answer(q) }
        Task {
            await model.resolve(interaction, Resolution(answers: answers))
            sending = false
        }
    }
}

struct PlanForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @Binding var showFull: Bool
    @State private var feedback = ""
    @State private var rejecting = false

    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUIPlanForm(interaction: interaction, showFull: $showFull) } else { plain }
    }

    @ViewBuilder private var plain: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            ScrollView {
                Markdown(MarkdownMemo.content(interaction.plan ?? ""))
                    .markdownTheme(.persona)
                    .markdownTextStyle { ForegroundColor(Theme.primaryText) }
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .scaledFrame(maxHeight: Theme.Size.box200)
            .scrollBounceBehavior(.basedOnSize)
            if rejecting {
                // The card's own agent: a runtime's plan (DSH…) names it, as AgentPermissionForm does; nil is Claude Code.
                TextField(String(localized: "Tell \(interaction.agentName ?? "Claude") what to change…"), text: $feedback, axis: .vertical)
                    .lineLimit(2...6)
                    .padding(Theme.Space.m)
                    .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
                HStack {
                    Button("Cancel") { withAnimation { rejecting = false } }.themedButton()
                    Button {
                        Task { await model.resolve(interaction, Resolution(approve: false, feedback: feedback)) }
                    } label: { Text("Send Back [Notification Actions cd3150]").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity) }
                        .themedButton(prominent: true)
                }
            } else {
                Button {
                    Task { await model.resolve(interaction, Resolution(approve: true, mode: "bypassPermissions")) }
                } label: {
                    Label("Approve & Auto-Run", systemImage: Theme.Icon.run).lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
                .controlSize(.large)
                HStack {
                    Button {
                        Task { await model.resolve(interaction, Resolution(approve: true, mode: "acceptEdits")) }
                    } label: { Text("Auto-Accept Edits").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity) }
                    Button { withAnimation { rejecting = true } } label: { Text("Send Back [Notification Actions a20f13]").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity) }
                }
                .themedButton()
            }
        }
        .sheet(isPresented: $showFull) {
            NavigationStack {
                ScrollView {
                    Markdown(MarkdownMemo.content(interaction.plan ?? "")).markdownTheme(.persona)
                        .markdownTextStyle { ForegroundColor(Theme.primaryText) }.padding()
                }
                .navigationTitle(PlanText.title(interaction.plan ?? ""))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { showFull = false } } }
            }
        }
    }
}

struct PermissionForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction

    var body: some View {
        // One view per card: another card in this place starts with an empty password field (a field still being
        // typed in keeps its UIKit text when only the binding is cleared).
        if interaction.isSecretRun { SecretRunForm(interaction: interaction).id(interaction.id) }
        else if interaction.requiresInput { PermissionInputForm(interaction: interaction) }
        else if AppStyle.current.usesBeautifulUI { BUIPermissionForm(interaction: interaction) } else { plain }
    }

    @ViewBuilder private var plain: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                Image(systemName: ToolPresentation.symbol(interaction.toolName ?? "")).foregroundStyle(Theme.accent)
                Text(interaction.toolName ?? String(localized: "Tool")).font(Theme.Font.secondaryStrong)
            }
            Text(interaction.summary ?? interaction.toolInput?.pretty ?? "")
                .font(Theme.Font.code)
                .padding(Theme.Space.m)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .textSelection(.enabled)
            HStack {
                Button { Task { await model.resolve(interaction, Resolution(allow: false)) } } label: {
                    Text("Deny").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton()
                Button { Task { await model.resolve(interaction, Resolution(allow: true)) } } label: {
                    Text("Allow [Notification Actions ce7ef2]").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
            }
            .controlSize(.large)
            if interaction.sessionID != "" {
                Button("Always Allow in Session") {
                    Task { await model.resolve(interaction, Resolution(allow: true, always: true)) }
                }
                #if os(macOS)
                // A text link as on iOS: the Mac's default style made it a third push button under Deny / Allow.
                .buttonStyle(.link)
                #endif
                // One line on every platform (button labels never wrap): large text or a narrow card shrinks it.
                .lineLimit(1)
                .minimumScaleFactor(0.8)
                .font(Theme.Font.caption)
                .frame(maxWidth: .infinity)
                if let scope = interaction.alwaysScopeDetail {
                    Text(scope).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
        }
    }
}

/// A Codex / TRAEX approval read off its TUI (docs/integrations/codex.md「提问和审批」): the command or the edits in full, the agent's
/// reason, Deny / Approve, Always Approve naming what it would allow (only when the TUI offers it), and Deny and Explain: the reason
/// stops the turn and goes to the agent as its next message.
struct AgentPermissionForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var explaining = false
    @State private var why = ""
    @FocusState private var whyFocused: Bool

    private var name: String { interaction.agentName ?? "Codex" }

    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUIPermissionForm(interaction: interaction) } else { plain }
    }

    @ViewBuilder private var plain: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                Image(systemName: ToolPresentation.symbol(interaction.toolName ?? "")).foregroundStyle(Theme.accent)
                Text(interaction.toolName ?? String(localized: "Tool")).font(Theme.Font.secondaryStrong)
            }
            if let reason = interaction.reason, !reason.isEmpty {
                Text(reason)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Text(interaction.summary ?? interaction.toolInput?.pretty ?? "")
                .font(Theme.Font.code)
                .padding(Theme.Space.m)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .textSelection(.enabled)
            HStack {
                Button { Task { await model.resolve(interaction, Resolution(allow: false)) } } label: {
                    Text("Deny").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton()
                Button { Task { await model.resolve(interaction, Resolution(allow: true)) } } label: {
                    Text("Approve [Interaction Views 905819]").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
            }
            .controlSize(.large)
            if let always = interaction.alwaysLabel, !always.isEmpty {
                Button {
                    Task { await model.resolve(interaction, Resolution(allow: true, always: true)) }
                } label: {
                    Text("Always Approve · \(always)").lineLimit(1).minimumScaleFactor(0.8)
                }
                #if os(macOS)
                .buttonStyle(.link)
                #endif
                .font(Theme.Font.caption)
                .frame(maxWidth: .infinity)
                .accessibilityIdentifier("codex-always")
            }
            reasonRow
        }
    }

    /// "Deny and Explain…": a quiet row that turns into a field with its own send arrow.
    @ViewBuilder private var reasonRow: some View {
        if explaining {
            HStack(alignment: .center, spacing: Theme.Space.m) {
                TextField(String(localized: "Tell \(name) what to do instead"), text: $why, axis: .vertical)
                    .lineLimit(1...4)
                    .focused($whyFocused)
                    .accessibilityIdentifier("codex-deny-reason")
                if !why.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Button {
                        whyFocused = false
                        Task { await model.resolve(interaction, Resolution(allow: false, message: why)) }
                    } label: {
                        Image(systemName: Theme.Icon.send)
                            .font(Theme.Font.labelHeavy)
                            .foregroundStyle(.white)
                            .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                            .background(Theme.accent, in: .circle)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Deny and send reason")
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        } else {
            Button {
                withAnimation(Theme.Motion.standard) { explaining = true }
                whyFocused = true
            } label: {
                Text("Deny and Explain…").lineLimit(1)
            }
            #if os(macOS)
            .buttonStyle(.link)
            #endif
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity)
            .accessibilityIdentifier("codex-deny-explain")
        }
    }
}

/// The terminal waiting on a choice (AskUserQuestion that bypassed the hook, a permission prompt…). The dialog is read
/// off the screen and laid out like a question: header, question, numbered options with their descriptions, "Other answer…"
/// for "Type something." The raw terminal and its keys stay one tap away; a screen with no dialog shows them directly.
struct ScreenForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var text = ""
    @State private var writing = false
    @State private var sending: String?
    /// A multi-select question's ticks and free text on the card; nil until changed here (what the computer shows).
    @State private var picks: Set<String>?
    @State private var freeText: String?
    @State private var showTerminal = false
    @FocusState private var focused: Bool

    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUIScreenForm(interaction: interaction) } else {
            plain.terminalCover($showTerminal, for: interaction)
        }
    }

    @ViewBuilder private var plain: some View {
        let screen = interaction.screen ?? ""
        if let d = interaction.screenDialog {
            dialog(d, screen: screen)
        } else {
            VStack(alignment: .leading, spacing: Theme.Space.m) { terminal(screen) }
        }
    }

    private func dialog(_ d: ScreenParser.Dialog, screen: String) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if !d.steps.isEmpty {
                ScreenSteps(steps: d.steps)
            }
            if let eyebrow = Self.eyebrow(d) {
                Text(eyebrow).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
            }
            if !d.question.isEmpty {
                Text(d.question).font(Theme.Font.heading).fixedSize(horizontal: false, vertical: true)
            }
            if let detail = d.detail {
                Text(detail)
                    .font(Theme.Font.code)
                    .foregroundStyle(.secondary)
                    .padding(Theme.Space.m)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            }
            if d.review && d.incomplete {
                Label("Some questions are unanswered", systemImage: Theme.Icon.error)
                    .font(Theme.Font.captionMedium)
                    .foregroundStyle(Theme.caution)
            }
            let choices = d.options.filter { $0.kind != .chat }
            VStack(spacing: 0) {
                ForEach(Array(choices.enumerated()), id: \.element.key) { i, opt in
                    if i > 0 { Divider().padding(.leading, 50) }
                    switch (opt.kind, d.multi) {
                    case (.typeSomething, true): freeTextRow(opt)
                    case (.typeSomething, false): typeRow(opt)
                    case (_, true): check(opt, d)
                    default: choice(opt)
                    }
                }
            }
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            if d.multi { multiFooter(d) }
            HStack(spacing: Theme.Space.l) {
                if let chat = d.options.first(where: { $0.kind == .chat }) {
                    Button { send(chat.key, answer(chat)) } label: {
                        Label("Reply in Chat Instead", systemImage: "bubble.left").lineLimit(1)
                    }
                }
                Spacer(minLength: 0)
                // The whole live terminal of that session, the one Terminal opens in its chat (#563): its keys, its scrolling.
                Button { Haptics.tap(); showTerminal = true } label: {
                    Label("View Terminal", systemImage: Theme.Icon.terminal).lineLimit(1)
                }
                .accessibilityIdentifier("screen-card-terminal")
            }
            .font(Theme.Font.captionMedium)
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
        }
    }

    private func choice(_ opt: ScreenParser.Dialog.Option) -> some View {
        let (label, recommended) = QuestionForm.display(opt.label)
        return Button { send(opt.key, answer(opt)) } label: {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                ZStack {
                    Circle().fill(sending == opt.key ? Theme.accent : Theme.Fill.chip)
                    if sending == opt.key {
                        ProgressView().controlSize(.mini).tint(.white)
                    } else {
                        Text(opt.key).font(Theme.Font.labelStrong.monospacedDigit()).foregroundStyle(.secondary)
                    }
                }
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.sm) {
                        Text(label).font(Theme.Font.heading).foregroundStyle(.primary)
                        if recommended { RecommendedTag() }
                    }
                    if let d = opt.detail {
                        Text(d).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 0)
            }
            .multilineTextAlignment(.leading)
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .background(sending == opt.key ? Theme.Fill.chip : .clear)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(sending != nil)
        .accessibilityLabel(label + (recommended ? String(localized: ", recommended", comment: "Accessibility suffix: the recommended option") : ""))
    }

    /// The header, and Select multiple for a multi-select question.
    nonisolated static func eyebrow(_ d: ScreenParser.Dialog) -> String? {
        let parts = [d.steps.isEmpty ? d.header.flatMap { $0.isEmpty ? nil : $0 } : nil, d.multi ? String(localized: "Select multiple") : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    // MARK: Multi-select (the terminal's check boxes, sent together)

    /// What is ticked: as changed on the card, else what the computer shows.
    private func picked(_ d: ScreenParser.Dialog) -> Set<String> {
        picks ?? Set(d.options.filter { $0.kind == .plain && $0.checked }.map(\.key))
    }

    private func typed(_ d: ScreenParser.Dialog) -> String {
        (freeText ?? d.options.first { $0.kind == .typeSomething }?.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func check(_ opt: ScreenParser.Dialog.Option, _ d: ScreenParser.Dialog) -> some View {
        let on = picked(d).contains(opt.key)
        let (label, recommended) = QuestionForm.display(opt.label)
        return Button {
            Haptics.tap()
            var set = picked(d)
            if on { set.remove(opt.key) } else { set.insert(opt.key) }
            withAnimation(Theme.Motion.standard) { picks = set }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                ZStack {
                    Circle().fill(on ? Theme.accent : Theme.Fill.chip)
                    if on {
                        Image(systemName: Theme.Icon.done).font(Theme.Font.labelHeavy).dynamicTypeSize(...DynamicTypeSize.xxLarge).foregroundStyle(.white)
                    } else {
                        Text(opt.key).font(Theme.Font.labelStrong.monospacedDigit()).foregroundStyle(.secondary)
                    }
                }
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.sm) {
                        Text(label).font(Theme.Font.heading).foregroundStyle(.primary)
                        if recommended { RecommendedTag() }
                    }
                    if let detail = opt.detail {
                        Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 0)
            }
            .multilineTextAlignment(.leading)
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .background(on ? Theme.Fill.chip : .clear)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(sending != nil)
        .accessibilityLabel(label + (recommended ? String(localized: ", recommended", comment: "Accessibility suffix: the recommended option") : "") + (on ? String(localized: ", selected", comment: "Accessibility suffix: a selected option") : ""))
    }

    /// The free-text field of a multi-select question: sent with the ticks, checked when it holds something.
    private func freeTextRow(_ opt: ScreenParser.Dialog.Option) -> some View {
        HStack(alignment: .center, spacing: Theme.Space.m) {
            Image(systemName: Theme.Icon.edit)
                .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xxLarge)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                .background(Theme.Fill.chip, in: .circle)
            TextField(String(localized: "Other answer (optional)"), text: Binding(get: { freeText ?? opt.text ?? "" }, set: { freeText = $0 }), axis: .vertical)
                .lineLimit(1...4)
                .focused($focused)
                .disabled(sending != nil)
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
    }

    /// Submit (Next when another question follows): the ticks and the free text go to the terminal together.
    private func multiFooter(_ d: ScreenParser.Dialog) -> some View {
        HStack(spacing: Theme.Space.m) {
            Spacer(minLength: 0)
            Button {
                focused = false
                let set = picked(d)
                let t = typed(d)
                send("multi", Resolution(text: t.isEmpty ? nil : t, options: d.options.map(\.key).filter(set.contains)))
            } label: {
                HStack(spacing: Theme.Space.sm) {
                    if sending == "multi" { ProgressView().controlSize(.small).tint(.white) }
                    Text(d.submit == "Next" ? String(localized: "Next [Interaction Views f82c68]") : String(localized: "Submit")).lineLimit(1)
                }
                .padding(.horizontal, Theme.Space.s)
            }
            .themedButton(prominent: true)
            .disabled(sending != nil || (picked(d).isEmpty && typed(d).isEmpty))
        }
        .controlSize(.regular)
    }

    /// "Type something.": a quiet row that turns into a field; sending picks the option, types, presses enter.
    private func typeRow(_ opt: ScreenParser.Dialog.Option) -> some View {
        HStack(alignment: .center, spacing: Theme.Space.m) {
            Image(systemName: Theme.Icon.edit)
                .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xxLarge)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                .background(Theme.Fill.chip, in: .circle)
            if writing {
                TextField(String(localized: "Write your answer"), text: $text, axis: .vertical)
                    .lineLimit(1...4)
                    .focused($focused)
                    .submitLabel(.send)
                if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Button {
                        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
                        focused = false
                        send(opt.key, answer(opt, text: t))
                    } label: {
                        if sending == opt.key { ProgressView().controlSize(.small) }
                        else { Image(systemName: Theme.Icon.submit).font(Theme.Font.title).foregroundStyle(Theme.accent) }
                    }
                    .buttonStyle(.plain)
                    .disabled(sending != nil)
                    .accessibilityLabel("Send answer")
                }
            } else {
                Text("Other answer…").foregroundStyle(.secondary)
                Spacer(minLength: 0)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .contentShape(.rect)
        .onTapGesture {
            guard !writing else { return }
            withAnimation(Theme.Motion.standard) { writing = true }
            focused = true
        }
    }

    /// The raw screen, its keys and a line of text: for anything the dialog reading did not catch.
    @ViewBuilder private func terminal(_ screen: String) -> some View {
        TerminalText(text: screen, maxLines: 18)
            .frame(maxHeight: 220)
        TerminalKeyBar { keys in Task { await model.resolve(interaction, Resolution(keys: keys)) } }
        HStack {
            Group {
                if interaction.secret == true {
                    // A password the terminal asks for (sudo, ssh): typed once, never offered to the system's passwords.
                    SecretField(String(localized: "Enter password (not saved)"), filled: String(localized: "Filled in, tap Send"), text: $text, identifier: "terminal.secret")
                } else {
                    TextField(String(localized: "Type text to send to the terminal"), text: $text)
                }
            }
            .textFieldStyle(.plain)
            .padding(Theme.Space.m)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            Button {
                let t = text
                text = ""
                Task { await model.resolve(interaction, Resolution(keys: ["enter"], text: t)) }
            } label: { Image(systemName: Theme.Icon.enter) }
                .themedButton(prominent: true)
                .disabled(text.isEmpty)
                .accessibilityIdentifier("terminal.send")
        }
    }

    /// The answer for an option: by its key when the hub read the dialog (it checks the screen first), else the keys.
    private func answer(_ opt: ScreenParser.Dialog.Option, text: String? = nil) -> Resolution {
        if interaction.dialog != nil { return Resolution(text: text, option: opt.key) }
        if let text { return Resolution(keys: ["enter"], text: text, preKeys: [opt.key]) }
        return Resolution(keys: [opt.key])
    }

    private func send(_ key: String, _ r: Resolution) {
        Haptics.tap()
        sending = key
        Task {
            await model.resolve(interaction, r)
            sending = nil
        }
    }
}

/// A multi-question dialog's questions, from the terminal's tab bar: done ones checked, the rest numbered.
struct ScreenSteps: View {
    var steps: [ScreenParser.Dialog.Step]

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.sm) {
                ForEach(Array(steps.enumerated()), id: \.offset) { i, s in
                    HStack(spacing: Theme.Space.xs) {
                        Group {
                            if s.done { Image(systemName: Theme.Icon.done).font(.system(size: 8, weight: .bold)) }
                            else { Text("\(i + 1)").font(.system(size: 9, weight: .bold).monospacedDigit()) } // type-scale: glyph (a step number in a 16 pt circle)
                        }
                        .foregroundStyle(s.done ? .white : .secondary)
                        .scaledFrame(width: Theme.Size.mark, height: Theme.Size.mark)
                        .background(s.done ? Theme.positive : Color.primary.opacity(0.1), in: .circle)
                        Text(s.label).lineLimit(1)
                    }
                    .font(Theme.Font.labelMedium)
                    .foregroundStyle(s.done ? .secondary : .primary)
                    .padding(.leading, Theme.Space.xs).padding(.trailing, Theme.Space.s).padding(.vertical, Theme.Space.xs)
                    .background(Color.primary.opacity(0.05), in: .capsule)
                }
            }
        }
        .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(steps.count) questions, \(steps.filter(\.done).count) answered")
    }
}

struct TerminalText: View {
    var text: String
    var maxLines: Int = 40

    var body: some View {
        let lines = text.components(separatedBy: "\n")
        let shown = lines.suffix(maxLines).joined(separator: "\n")
        ScrollView([.vertical, .horizontal]) {
            Text(shown)
                .font(Theme.Font.label.monospaced())
                .foregroundStyle(Theme.terminalText)
                .textSelection(.enabled)
                .padding(Theme.Space.m)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .defaultScrollAnchor(.bottomLeading)
        .background(Theme.terminalBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}

#if DEBUG
extension Interaction {
    /// A two-question AskUserQuestion (descriptions, a recommended option, a multi-select) for screenshots.
    /// The terminal waiting on an AskUserQuestion dialog (the hook did not catch it), for screenshots.
    static func sampleScreen(_ key: SessionKey) -> Interaction? {
        let screen = [
            " Ran 1 shell command",
            "────────────────────────────────────────────────",
            " ←  ☐ 取消部署  ☐ 推送代码  ✔ Submit  →", // i18n-ignore
            "",
            " │预览环境第一次构建失败：我加的图片压缩库要更新的 Node，", // i18n-ignore
            " │换版本后构建通过，修复也已推送（3f2a9c1）。但失败的那次部署 #128", // i18n-ignore
            " │卡在构建阶段，没法重新构建。要取消它吗？", // i18n-ignore
            "",
            "❯ 1. 同意取消 #128 (Recommended)", // i18n-ignore
            "     用 deploy cancel 128 取消，然后用修复后的 commit 重新部署一次。", // i18n-ignore
            "  2. 先不取消", // i18n-ignore
            "     我停在这里；预览环境和 e2e 都先不动，上一个版本也暂时保留。", // i18n-ignore
            "  3. Type something.",
            "────────────────────────────────────────────────",
            "  4. Chat about this",
            "",
            " Enter to select · ↑/↓ to navigate · Esc to cancel",
        ].joined(separator: "\n")
        let payload: [String: Any] = ["id": "sample-s", "machine_id": key.machineID, "session_id": key.sessionID, "kind": "screen", "screen": screen]
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return nil }
        return try? JSON.decoder.decode(Interaction.self, from: data)
    }

    /// A Bash permission request (Allow / Deny / Always Allow in Session), for screenshots.
    static func samplePermission(_ key: SessionKey) -> Interaction? {
        let payload: [String: Any] = [
            "id": "sample-p", "machine_id": key.machineID, "session_id": key.sessionID, "kind": "permission",
            "tool_name": "Bash", "summary": "xcodebuild test -scheme Weather -destination 'platform=iOS Simulator,name=iPhone 17'",
            "tool_input": ["command": "xcodebuild test -scheme Weather -destination 'platform=iOS Simulator,name=iPhone 17'"],
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return nil }
        return try? JSON.decoder.decode(Interaction.self, from: data)
    }

    /// A Codex approval read off its TUI (Approve / Deny / Always Approve / Deny and Explain), for screenshots.
    static func sampleCodexApproval(_ key: SessionKey) -> Interaction? {
        let command = "curl -s 'http://localhost:8080/orders?page=1' -o /dev/null -w '%{time_total}'"
        let payload: [String: Any] = [
            "id": "sample-cxa", "machine_id": key.machineID, "session_id": key.sessionID, "kind": "permission", "agent": "codex",
            "tool_name": "Bash", "summary": command, "tool_input": ["command": command],
            "reason": "Allow this command to access localhost:8080 over the network?",
            "always_label": "以后不再问以 curl -s 开头的命令", // i18n-ignore
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return nil }
        return try? JSON.decoder.decode(Interaction.self, from: data)
    }

    /// Codex's request_user_input in Plan mode (two questions with headers and descriptions), for screenshots.
    static func sampleCodexQuestion(_ key: SessionKey) -> Interaction? {
        // i18n-ignore
        let json = """
        {"id":"sample-cxq","machine_id":"\(key.machineID)","session_id":"\(key.sessionID)","kind":"question","agent":"codex",
         "tool_name":"request_user_input","questions":[
         {"question":"订单列表的索引怎么加？","header":"索引","multi_select":false,"options":[
          {"label":"(user_id, created_at) 联合索引 (Recommended)","description":"覆盖按用户分页的查询，写入稍慢一点。"},
          {"label":"只给 user_id 加索引","description":"改动最小，排序仍要回表。"}]},
         {"question":"改完以后怎么验证？","header":"验证","multi_select":false,"options":[
          {"label":"预发环境压测","description":"用上周的流量回放跑 10 分钟。"},
          {"label":"灰度 10% 上线","description":"看 30 分钟的 p95 再全量。"}]}]}
        """
        return try? JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
    }

    static func sampleQuestion(_ key: SessionKey) -> Interaction? {
        // i18n-ignore
        let json = """
        {"id":"sample-q","machine_id":"\(key.machineID)","session_id":"\(key.sessionID)","kind":"question","questions":[
         {"question":"一轮对话里有后台任务把 Claude 唤醒时，哪一段算「真正的回复」？","header":"真正的回复","multi_select":false,"options":[
          {"label":"取最长的那段 (Recommended)","description":"唤醒前后各有一段收尾的话，取最长的作为回复；其余收进工作行和抽屉。"},
          {"label":"永远取最后一段","description":"所有工具合成一行，回复永远是最后一段话。"},
          {"label":"永远取唤醒前那段","description":"唤醒后的工具和回复全部收进「后台任务完成」。"}]},
         {"question":"抽屉里要显示哪些内容？","header":"抽屉内容","multi_select":true,"options":[
          {"label":"工具调用","description":"唤醒后跑的每个工具"},
          {"label":"思考","description":null},
          {"label":"回复","description":"唤醒后 Claude 说的话"}]}]}
        """
        return try? JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
    }
}
#endif

// MARK: - The BeautifulUI style's forms

/// How the interaction forms fit BeautifulUI's approval card into the panel: no card of its own (the panel is the
/// surface), the panel's width, no × (the panel has its menu) and no Start over (the panel goes once answered).
extension ApprovalCardOptions {
    static func panel(sending: Bool = false, autoAdvance: Bool = true) -> ApprovalCardOptions {
        var o = ApprovalCardOptions()
        o.drawsSurface = false
        o.maxWidth = nil
        o.showsDismiss = false
        o.allowsStartOver = false
        o.autoAdvance = autoAdvance
        o.isSending = sending
        return o
    }
}

/// The BeautifulUI style's AskUserQuestion: its approval card, a question at a time with its step counter; the options with
/// their descriptions and Recommended for the suggested one; one choice moves on by itself (and on the last question,
/// sends), several wait for Next / Submit; "Other answer…" is the card's own field.
struct BUIQuestionForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction

    private var questions: [Question] { interaction.questions ?? [] }

    var body: some View {
        if interaction.isDelegating || interaction.delegate != nil {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                DelegateNotes(interaction: interaction)
                card
            }
        } else {
            card
        }
    }

    private var card: some View {
        let picks = Delegated.recommendedLabels(questions, interaction.delegate?.recommend)
        return ApprovalCard(questions: zip(questions, picks).map { Self.card($0, picks: $1) }, skipLabel: nil, continueLabel: String(localized: "Next [Interaction Views f82c68]"), sendLabel: String(localized: "Submit"),
                     customPlaceholder: String(localized: "Other answer…"), sentMessage: String(localized: "Sent"), options: .panel()) { answers in
            Haptics.tap()
            Task { await model.resolve(interaction, Resolution(answers: Self.answers(answers, questions))) }
        }
    }

    /// `picks`: the options the assistant recommends (#544).
    static func card(_ q: Question, picks: [String] = []) -> ApprovalQuestion {
        ApprovalQuestion(
            id: q.question, q.question, kind: q.multiSelect ? .multiple : .single,
            choices: q.options.map { o in
                let (label, recommended) = display(o.label)
                let tag = recommended ? String(localized: "Recommended") : picks.contains(o.label) ? String(localized: "Assistant's pick") : nil
                return ApprovalQuestion.Choice(label, detail: o.description, tag: tag)
            },
            eyebrow: eyebrow(q)
        )
    }

    /// "header · Select multiple" above the question, when there is either.
    private static func eyebrow(_ q: Question) -> String? {
        let parts = [q.header.flatMap { $0.isEmpty ? nil : $0 }, q.multiSelect ? String(localized: "Select multiple") : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// The card's answers as the hub wants them: per question, what was typed, else the picked labels as Claude wrote
    /// them (with "(Recommended)"), in the options' order.
    static func answers(_ a: ApprovalAnswers, _ questions: [Question]) -> [String: String] {
        var out: [String: String] = [:]
        for (n, q) in questions.enumerated() {
            let custom = (a.custom[n] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            if !custom.isEmpty { out[q.question] = custom; continue }
            let picked = Set(a.picks[n] ?? [])
            let labels = q.options.enumerated().filter { picked.contains($0.offset) }.map(\.element.label)
            if !labels.isEmpty { out[q.question] = labels.joined(separator: ", ") }
        }
        return out
    }

    /// "Take the longest part (Recommended)" → ("Take the longest part", true). The answer sent back keeps the original label.
    static func display(_ label: String) -> (String, Bool) { QuestionOption.display(label) }
}

/// The BeautifulUI style's ExitPlanMode: an approval card with the plan (Markdown, scrolls; full screen from the panel's
/// header), then how to go on. A tap on an approval sends it, as the plain form's buttons do; to send the plan back,
/// write what to change in the card's field and send.
struct BUIPlanForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @Binding var showFull: Bool

    private static let choices = [
        ApprovalQuestion.Choice(String(localized: "Approve & Auto-Run"), detail: String(localized: "No more confirmations after this")),
        ApprovalQuestion.Choice(String(localized: "Approve, Auto-Accept Edits"), detail: String(localized: "File edits go through; other actions still ask")),
    ]

    var body: some View {
        ApprovalCard(questions: [ApprovalQuestion(String(localized: "Go ahead with this plan?"), choices: Self.choices,
                                                  customPlaceholder: String(localized: "Send back with what to change…"))],
                     skipLabel: nil, sendLabel: String(localized: "Send"), sentMessage: String(localized: "Sent"),
                     options: .panel()) { answers in
            Haptics.tap()
            Task { await model.resolve(interaction, Self.resolution(answers)) }
        } context: { _ in
            ScrollView {
                Markdown(MarkdownMemo.content(interaction.plan ?? ""))
                    .markdownTheme(.persona)
                    .markdownTextStyle { ForegroundColor(Theme.primaryText) }
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .scaledFrame(maxHeight: Theme.Size.box200)
            .scrollBounceBehavior(.basedOnSize)
        }
        .sheet(isPresented: $showFull) {
            NavigationStack {
                ScrollView {
                    Markdown(MarkdownMemo.content(interaction.plan ?? "")).markdownTheme(.persona)
                        .markdownTextStyle { ForegroundColor(Theme.primaryText) }.padding()
                }
                .navigationTitle(PlanText.title(interaction.plan ?? ""))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { showFull = false } } }
            }
        }
    }

    static func resolution(_ a: ApprovalAnswers) -> Resolution {
        let feedback = (a.custom[0] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !feedback.isEmpty { return Resolution(approve: false, feedback: feedback) }
        switch a.picks[0]?.first {
        case 0: return Resolution(approve: true, mode: "bypassPermissions")
        case 1: return Resolution(approve: true, mode: "acceptEdits")
        default: return Resolution(approve: false, feedback: "")
        }
    }
}

/// The BeautifulUI style's tool approval: an approval card with the tool and what it will run on top, then Allow /
/// Always Allow in Session / Deny. A tap sends, as the plain form's buttons do. Typing in the field denies with that reason,
/// which Claude reads. A Codex card (docs/integrations/codex.md「提问和审批」) offers Approve / Always Approve (what it allows, only when Codex
/// does) / Deny, with Codex's reason over the command.
struct BUIPermissionForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction

    private var choices: [ApprovalQuestion.Choice] {
        if interaction.agentName != nil {
            var c = [ApprovalQuestion.Choice(String(localized: "Approve [Interaction Views 905819]"))]
            if let always = interaction.alwaysLabel, !always.isEmpty { c.append(ApprovalQuestion.Choice(String(localized: "Always Approve"), detail: always)) }
            c.append(ApprovalQuestion.Choice(String(localized: "Deny")))
            return c
        }
        var c = [ApprovalQuestion.Choice(String(localized: "Allow [Notification Actions ce7ef2]"))]
        if interaction.sessionID != "" { c.append(ApprovalQuestion.Choice(String(localized: "Always Allow in Session"), detail: interaction.alwaysScopeDetail)) }
        c.append(ApprovalQuestion.Choice(String(localized: "Deny")))
        return c
    }

    var body: some View {
        let choices = choices
        let tool = interaction.toolName ?? String(localized: "this tool")
        let ask = interaction.agentName == nil ? String(localized: "Allow \(tool)?") : String(localized: "Approve \(tool)?")
        ApprovalCard(questions: [ApprovalQuestion(ask, choices: choices,
                                                  customPlaceholder: String(localized: "Deny and explain why…"))],
                     skipLabel: nil, sendLabel: String(localized: "Send"), sentMessage: String(localized: "Sent"),
                     options: .panel()) { answers in
            Haptics.tap()
            Task { await model.resolve(interaction, Self.resolution(answers, choices: choices)) }
        } context: { _ in
            if interaction.agentName != nil, let reason = interaction.reason, !reason.isEmpty {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    Text(reason)
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    command
                }
            } else {
                command
            }
        }
    }

    private var command: some View {
        HStack(alignment: .top, spacing: Theme.Space.s) {
            Image(systemName: ToolPresentation.symbol(interaction.toolName ?? "")).foregroundStyle(Theme.accent)
            Text(interaction.summary ?? interaction.toolInput?.pretty ?? "")
                .font(Theme.Font.code)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(Theme.Space.m)
        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }

    static func resolution(_ a: ApprovalAnswers, choices: [ApprovalQuestion.Choice]) -> Resolution {
        let reason = (a.custom[0] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !reason.isEmpty { return Resolution(allow: false, message: reason) }
        // The labels are localized: compare with the same localized words the choices were built from.
        switch a.picks[0]?.first.map({ choices[$0].label }) {
        case String(localized: "Allow [Notification Actions ce7ef2]"), String(localized: "Approve [Interaction Views 905819]"): return Resolution(allow: true)
        case String(localized: "Always Allow in Session"), String(localized: "Always Approve"): return Resolution(allow: true, always: true)
        default: return Resolution(allow: false)
        }
    }
}

extension View {
    /// A screen card's View Terminal: the session's whole live terminal comes up over the page (TerminalCover), the same as
    /// Terminal in its chat; dragged down it is the card again.
    func terminalCover(_ shown: Binding<Bool>, for ix: Interaction) -> some View {
        fullScreenCover(isPresented: shown) {
            TerminalCover(key: SessionKey(machineID: ix.machineID, sessionID: ix.sessionID)).presentationBackground(.clear)
        }
    }
}

/// The BeautifulUI style's terminal waiting on a choice (AskUserQuestion that bypassed the hook, a permission prompt…).
/// The dialog is read off the screen and laid out as an approval card: question, numbered options with their descriptions, "Other answer…"
/// for "Type something." The raw terminal and its keys stay one tap away; a screen with no dialog shows them directly.
struct BUIScreenForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var text = ""
    @State private var sending: String?
    @State private var showTerminal = false

    var body: some View {
        let screen = interaction.screen ?? ""
        if let d = interaction.screenDialog {
            dialog(d, screen: screen).terminalCover($showTerminal, for: interaction)
        } else {
            VStack(alignment: .leading, spacing: Theme.Space.m) { terminal(screen) }
        }
    }

    /// The dialog as an approval card: the terminal's numbered options (Recommended for the suggested one), "Type something."
    /// as the card's field, the dialog's detail on top. A tap sends the option to the terminal, as the plain form does.
    private func dialog(_ d: ScreenParser.Dialog, screen: String) -> some View {
        let choices = d.options.filter { $0.kind != .chat && $0.kind != .typeSomething }
        let typing = d.options.first { $0.kind == .typeSomething }
        // A multi-select question starts from what the computer shows: its ticks and its free text.
        let shown = d.multi
            ? ApprovalAnswers(picks: [0: choices.indices.filter { choices[$0].checked }], custom: typing?.text.map { [0: $0] } ?? [:])
            : ApprovalAnswers()
        return VStack(alignment: .leading, spacing: Theme.Space.m) {
            if !d.steps.isEmpty { BUIScreenSteps(steps: d.steps) }
            ApprovalCard(
                questions: [ApprovalQuestion(
                    d.question.isEmpty ? (d.header ?? "") : d.question, kind: d.multi ? .multiple : .single,
                    choices: choices.map { o in
                        let (label, recommended) = QuestionForm.display(o.label)
                        return ApprovalQuestion.Choice(label, detail: o.detail, tag: recommended ? String(localized: "Recommended") : nil)
                    },
                    eyebrow: d.question.isEmpty ? nil : ScreenForm.eyebrow(d),
                    allowsCustom: typing != nil, customPlaceholder: String(localized: "Other answer…"))],
                answers: shown,
                skipLabel: nil, sendLabel: d.multi ? (d.submit == "Next" ? String(localized: "Next [Interaction Views f82c68]") : String(localized: "Submit")) : String(localized: "Send"), sentMessage: String(localized: "Sent"),
                options: .panel(sending: sending != nil)
            ) { answers in
                let text = (answers.custom[0] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                if d.multi {
                    // The card holds the whole answer: what is not picked here is unticked at the computer.
                    let picked = Set(answers.picks[0] ?? [])
                    let keys = choices.enumerated().filter { picked.contains($0.offset) }.map(\.element.key)
                    send("multi", Resolution(text: text.isEmpty ? nil : text, options: keys))
                } else if let typing, !text.isEmpty {
                    send(typing.key, answer(typing, text: text))
                } else if let n = answers.picks[0]?.first, choices.indices.contains(n) {
                    send(choices[n].key, answer(choices[n]))
                }
            } context: { _ in
                if d.review && d.incomplete {
                    Label("Some questions are unanswered", systemImage: Theme.Icon.error)
                        .font(Theme.Font.captionMedium)
                        .foregroundStyle(Theme.caution)
                }
                if let detail = d.detail {
                    Text(detail)
                        .font(Theme.Font.code)
                        .foregroundStyle(.secondary)
                        .padding(Theme.Space.m)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                }
            }
            HStack(spacing: Theme.Space.l) {
                if let chat = d.options.first(where: { $0.kind == .chat }) {
                    Button { send(chat.key, answer(chat)) } label: {
                        Label("Reply in Chat Instead", systemImage: "bubble.left").lineLimit(1)
                    }
                }
                Spacer(minLength: 0)
                // The whole live terminal of that session, the one Terminal opens in its chat (#563): its keys, its scrolling.
                Button { Haptics.tap(); showTerminal = true } label: {
                    Label("View Terminal", systemImage: Theme.Icon.terminal).lineLimit(1)
                }
                .accessibilityIdentifier("screen-card-terminal")
            }
            .font(Theme.Font.captionMedium)
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
        }
    }

    /// The raw screen, its keys and a line of text: for anything the dialog reading did not catch.
    @ViewBuilder private func terminal(_ screen: String) -> some View {
        TerminalText(text: screen, maxLines: 18)
            .frame(maxHeight: 220)
        TerminalKeyBar { keys in Task { await model.resolve(interaction, Resolution(keys: keys)) } }
        HStack {
            Group {
                if interaction.secret == true {
                    // A password the terminal asks for (sudo, ssh): typed once, never offered to the system's passwords.
                    SecretField(String(localized: "Enter password (not saved)"), filled: String(localized: "Filled in, tap Send"), text: $text, identifier: "terminal.secret")
                } else {
                    TextField(String(localized: "Type text to send to the terminal"), text: $text)
                }
            }
            .textFieldStyle(.plain)
            .padding(Theme.Space.m)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            Button {
                let t = text
                text = ""
                Task { await model.resolve(interaction, Resolution(keys: ["enter"], text: t)) }
            } label: { Image(systemName: Theme.Icon.enter) }
                .themedButton(prominent: true)
                .disabled(text.isEmpty)
                .accessibilityIdentifier("terminal.send")
        }
    }

    /// The answer for an option: by its key when the hub read the dialog (it checks the screen first), else the keys.
    private func answer(_ opt: ScreenParser.Dialog.Option, text: String? = nil) -> Resolution {
        if interaction.dialog != nil { return Resolution(text: text, option: opt.key) }
        if let text { return Resolution(keys: ["enter"], text: text, preKeys: [opt.key]) }
        return Resolution(keys: [opt.key])
    }

    private func send(_ key: String, _ r: Resolution) {
        Haptics.tap()
        sending = key
        Task {
            await model.resolve(interaction, r)
            sending = nil
        }
    }
}

/// A multi-question dialog's questions, from the terminal's tab bar, as status pills: done ones green with a check,
/// the rest numbered.
struct BUIScreenSteps: View {
    var steps: [ScreenParser.Dialog.Step]

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.sm) {
                ForEach(Array(steps.enumerated()), id: \.offset) { i, s in
                    BUIPill(s.done ? s.label : "\(i + 1) \(s.label)", tone: s.done ? .green : .neutral,
                            icon: s.done ? .symbol(Theme.Icon.done) : nil)
                }
            }
        }
        .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(steps.count) questions, \(steps.filter(\.done).count) answered")
    }
}

/// A card on no session (`personad ask` outside herdr, #4059), answered where it is listed: there is no chat to open for
/// it. It goes away once answered (here or anywhere else).
struct SessionlessCardSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var interaction: Interaction

    var body: some View {
        NavigationStack {
            ScrollView {
                InteractionPanel(interaction: interaction)
                    .disabled(!model.canAnswer(interaction))
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.vertical, Theme.Space.l)
            }
            .themedPage()
            .navigationTitle(interaction.sessionlessPlace ?? String(localized: "Script"))
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            #if os(macOS)
            .macSheetDone { dismiss() }
            #else
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1)
                }
            }
            #endif
        }
        .presentationDetents([.medium, .large])
        .onChange(of: model.pendingInteractions.contains { $0.uid == interaction.uid }) { _, pending in
            if !pending { dismiss() }
        }
        #if os(macOS)
        .macSheet()
        #endif
    }
}

extension Interaction {
    /// The line under a card's title in a list: its session's title; a sessionless card says where it comes from, and
    /// Script when its title does not already name who asks (`personad ask` labels its cards Script, docs/integrations/script-cli.md).
    func placeLine(machine: String?) -> String {
        let mark = (agentLabelHint ?? "").isEmpty ? String(localized: "Script") : nil
        let parts: [String?] = isSessionless ? [mark, sessionlessPlace, machine] : [sessionTitle, machine]
        return parts.compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
    }
}
