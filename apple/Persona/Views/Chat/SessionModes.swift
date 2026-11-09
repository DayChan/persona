import SwiftUI

/// Claude Code's permission modes and effort levels as the app shows them (server: node/modes.go).
nonisolated enum SessionModes {
    struct Option: Identifiable, Hashable {
        var id: String
        var title: String
        var detail: String
        var symbol: String
    }

    static let permissionModes: [Option] = [
        Option(id: "default", title: String(localized: "Ask First"), detail: String(localized: "Asks you before editing files or running commands"), symbol: Theme.Icon.askFirst),
        Option(id: "acceptEdits", title: String(localized: "Auto-Edit"), detail: String(localized: "Edits files without asking, still asks before commands"), symbol: Theme.Icon.editFile),
        Option(id: "plan", title: String(localized: "Plan Only"), detail: String(localized: "Reads code and writes a plan, acts only once you approve"), symbol: Theme.Icon.plan),
        Option(id: "auto", title: String(localized: "Auto", comment: "Permission mode: the agent judges the risk itself"), detail: String(localized: "Judges the risk itself and does safe things right away"), symbol: "wand.and.sparkles"),
        Option(id: "bypassPermissions", title: String(localized: "Allow Everything"), detail: String(localized: "Never asks. Only available when starting"), symbol: Theme.Icon.allowEverything),
    ]

    /// "auto" is the model's own default level (node/modes.go). Replies are recorded at the level they actually ran at,
    /// so after the next reply the chip shows that level instead.
    static let effortLevels: [Option] = [
        Option(id: "auto", title: String(localized: "Auto", comment: "Thinking effort level"), detail: String(localized: "The model's own default level"), symbol: Theme.Icon.effortAuto),
        Option(id: "low", title: String(localized: "Low", comment: "Thinking effort level"), detail: String(localized: "Fastest, fine for simple things"), symbol: "gauge.with.dots.needle.0percent"),
        Option(id: "medium", title: String(localized: "Medium [Session Modes a567bd]", comment: "Thinking effort level"), detail: String(localized: "A bit faster"), symbol: Theme.Icon.context),
        Option(id: "high", title: String(localized: "High [Session Modes b1c278]", comment: "Thinking effort level"), detail: String(localized: "Default for most tasks"), symbol: Theme.Icon.effort),
        Option(id: "xhigh", title: String(localized: "Extra High", comment: "Thinking effort level"), detail: String(localized: "More careful, but slower and pricier"), symbol: "gauge.with.dots.needle.67percent"),
        Option(id: "max", title: String(localized: "Max", comment: "Thinking effort level"), detail: String(localized: "Slowest and most careful"), symbol: "gauge.with.dots.needle.100percent"),
    ]

    /// Codex's reasoning efforts (node/codex_effort.go); a thread offers its model's, in `Session.efforts`. Their symbols
    /// come from Claude Code's levels without auto (`startEffortLevels`), so `[0]` is low.
    static let codexEffortLevels: [Option] = [
        Option(id: "minimal", title: String(localized: "Minimal", comment: "Thinking effort level"), detail: String(localized: "Barely thinks, fastest"), symbol: startEffortLevels[0].symbol),
        Option(id: "low", title: String(localized: "Low", comment: "Thinking effort level"), detail: String(localized: "Fast, fine for simple things"), symbol: startEffortLevels[0].symbol),
        Option(id: "medium", title: String(localized: "Medium [Session Modes a567bd]", comment: "Thinking effort level"), detail: String(localized: "Balanced speed"), symbol: startEffortLevels[1].symbol),
        Option(id: "high", title: String(localized: "High [Session Modes b1c278]", comment: "Thinking effort level"), detail: String(localized: "More careful"), symbol: startEffortLevels[2].symbol),
        Option(id: "xhigh", title: String(localized: "Extra High", comment: "Thinking effort level"), detail: String(localized: "More careful, but slower and pricier"), symbol: startEffortLevels[3].symbol),
        Option(id: "max", title: String(localized: "Max", comment: "Thinking effort level"), detail: String(localized: "Slowest and most careful"), symbol: startEffortLevels[4].symbol),
        Option(id: "ultra", title: String(localized: "Ultra", comment: "Thinking effort level"), detail: String(localized: "Most careful, and splits work across subagents on its own"), symbol: startEffortLevels[4].symbol),
    ]

    /// Modes a running session can switch to (bypassPermissions only at start, see node/modes.go).
    static let switchableModes = permissionModes.filter { $0.id != "bypassPermissions" }

    /// Levels for something not started yet: auto starts without --effort, which is what Use Launcher's already does.
    static let startEffortLevels = effortLevels.filter { $0.id != "auto" }

    /// Every mode for an assistant conversation (the main one too, new or running): the node restarts its Claude in
    /// Allow Everything (#2416), so the option doesn't say "only when new".
    static let assistantModes = permissionModes.map { o in
        o.id == "bypassPermissions" ? Option(id: o.id, title: o.title, detail: String(localized: "Edits files and runs commands without asking"), symbol: o.symbol) : o
    }

    /// The modes a running session can switch to: every one for an assistant conversation, `switchableModes` otherwise.
    static func switchableModes(_ session: Session?) -> [Option] {
        session?.assistant == true ? assistantModes : switchableModes
    }

    static func mode(_ id: String?) -> Option? { permissionModes.first { $0.id == id } }
    static func effort(_ id: String?) -> Option? { effortLevels.first { $0.id == id } ?? codexEffortLevels.first { $0.id == id } }

    /// A running session's effort choices: its own levels when the hub lists them (a Codex thread on its app-server),
    /// Claude Code's otherwise; a level this app does not know yet keeps its id.
    static func effortOptions(_ session: Session?) -> [Option] {
        guard let ids = session?.efforts, !ids.isEmpty else { return effortLevels }
        return ids.map { id in codexEffortLevels.first { $0.id == id } ?? Option(id: id, title: id, detail: "", symbol: Theme.Icon.effort) }
    }

    /// The model can be switched on the running session (#3119): Claude Code in a herdr pane, which takes `/model`.
    /// Codex and TRAEX pick theirs in a full-screen menu the phone cannot drive; the assistant's has its own chip.
    static func switchesModel(_ s: Session?) -> Bool {
        guard let s else { return false }
        return s.canSend && s.canTerminal && s.paneID != nil && s.isClaude && !s.isTraex && !s.assistant
    }

    /// Why the model cannot be switched right now (nil when it can): the node refuses the same cases.
    static func modelBusy(_ s: Session?) -> String? {
        switch s?.state {
        case .working?: String(localized: "Working. Switch models after it stops.")
        case .needsYou?: String(localized: "Waiting for your answer. Switch models after you reply.")
        case .offline?: StateName.notRunning.text
        default: s?.starting == true ? String(localized: "Starting [Session Modes 087739]") : nil
        }
    }

    /// The launcher whose /model list and typed IDs a session's switch offers: the session's own, plain claude when it
    /// started without one (the web's `sessionLauncher`).
    static func launcher(_ s: Session?) -> String {
        guard let l = s?.launcher, !l.isEmpty else { return "claude" }
        return l
    }

    /// What applying a model pick sends: the alias, unless the session already runs it (Default too, while it runs a
    /// typed ID: that is not the default, #1644).
    @MainActor static func modelPatch(_ picked: String, current: String?) -> String? {
        picked == PromptItems.runningModelID(current) ? nil : picked
    }

    static func modeTint(_ id: String?) -> Color {
        switch id {
        // Neutral: amber is the working state and would read as Waiting for you. Only "allow everything" warns.
        case "bypassPermissions": Theme.danger
        default: .secondary
        }
    }

    /// "Ask First" for known modes, the raw value otherwise (Codex approval policies, newer Claude Code modes).
    /// For a mode or effort the session has not reported.
    static var unknown: String { String(localized: "Unknown [Session Modes 4d8c1c]") }
    static func modeLabel(_ id: String?) -> String? { id.map { mode($0)?.title ?? $0 } }
    static func effortLabel(_ id: String?) -> String? { id.map { effort($0)?.title ?? $0 } }

    /// The shell composer's model button's two words (#1091, #1117), ChatGPT's "6.1 Sol Medium": the model, drawn in the text's
    /// ink, and the effort, in the quieter ink after it.
    struct ComposerTitle: Equatable, Sendable {
        var model: String
        var effort: String?
        /// The workflow mark after the effort (#1307): ultracode on, or a Codex thread at "ultra".
        var mark: Ultracode.Mark? = nil
        /// For VoiceOver and the UI tests: "Opus 5.5 High", "Opus 5.5 High, ultracode on".
        var spoken: String {
            let words = effort.map { "\(model) \($0)" } ?? model
            guard let extra = mark?.spoken else { return words }
            return words + String(localized: ", ", comment: "List separator") + extra
        }
    }

    /// "Opus 5.5" + "High"; the model alone; "Thinking" + "High" with no model known; nil with neither.
    static func composerTitle(model: String?, effort: String?) -> ComposerTitle? {
        switch (model, effort) {
        case let (m?, e): ComposerTitle(model: m, effort: e)
        case let (nil, e?): ComposerTitle(model: String(localized: "Thinking [Message Views 8cf010]", comment: "Composer button title when no model is known: thinking effort follows"), effort: e)
        case (nil, nil): nil
        }
    }

    /// GPT display names only: keep the version and every variant, without changing the id used by the agent.
    static func gptModelName(_ model: String?) -> String? {
        guard let raw = Fmt.model(model)?.trimmingCharacters(in: .whitespacesAndNewlines),
              raw.lowercased().hasPrefix("gpt-") else { return nil }
        let parts = raw.dropFirst(4).split(separator: "-", omittingEmptySubsequences: false)
        guard !parts.isEmpty, parts.allSatisfy({ !$0.isEmpty }) else { return nil }
        return parts.map { part in
            guard let first = part.first, first.isLetter else { return String(part) }
            return String(first).uppercased() + part.dropFirst().lowercased()
        }.joined(separator: " ")
    }

    /// A model's short name for that button: "claude-opus-5-5-20260101" → "Opus 5.5", "opus" / "opus[1m]" → "Opus",
    /// "gpt-6.1-sol" → "6.1 Sol"; other names retain the existing `Fmt.model` behavior.
    static func modelName(_ model: String?) -> String? {
        guard let model, !model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              let raw = Fmt.model(model) else { return nil }
        if let gpt = gptModelName(raw) { return gpt }
        let lower = raw.lowercased()
        for family in ["fable", "opus", "sonnet", "haiku"] {
            guard let r = lower.range(of: family) else { continue }
            // The version follows the family in today's ids; a date (8 digits) and older ids' leading version do not.
            let after = lower[r.upperBound...].split { $0 == "-" || $0 == "." || $0 == "_" }
            let version = after.prefix { $0.allSatisfy(\.isNumber) && $0.count <= 2 }.prefix(2).joined(separator: ".")
            return family.capitalized + (version.isEmpty ? "" : " " + version)
        }
        return raw
    }
}

/// The two chips above the composer — permission mode and effort level, like the footer of Claude Code's TUI.
/// Tapping one changes it on the running session. A Codex thread on its app-server gets the effort alone, among its own
/// levels (`Session.setsEffortOnly`).
struct SessionModeBar: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?
    @State private var changing = false
    /// "Other Model…": a model ID typed by hand (#1644).
    @State private var typingModel = false

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SessionModeBar")
        #endif
        let mode = session?.permissionMode
        let effort = session?.effort
        HStack(spacing: Theme.Space.s) {
            if session?.setsEffortOnly != true {
                Menu {
                    Picker("Permission Mode", selection: Binding(get: { mode ?? "" }, set: { new in change(mode: new) })) {
                        ForEach(options(SessionModes.switchableModes(session), current: mode)) { o in
                            Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id)
                        }
                    }
                    .pickerStyle(.inline)
                } label: {
                    ModeChip(symbol: SessionModes.mode(mode)?.symbol ?? Theme.Icon.askFirst,
                             text: SessionModes.modeLabel(mode) ?? String(localized: "Permission Mode"), tint: SessionModes.modeTint(mode), known: mode != nil)
                }
                .labelOnlyMenu()
                .accessibilityLabel("Permission Mode: \(SessionModes.modeLabel(mode) ?? SessionModes.unknown)")
            }
            Menu {
                // Claude Code's ultracode (#1307): its own switch under the effort, not a level of it (declared before it where
                // the menu lists its items the other way round, `UltracodeMenuSection.leadsEffort`).
                if UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
                Picker("Thinking Effort", selection: Binding(get: { effort ?? "" }, set: { new in change(effort: new) })) {
                    ForEach(SessionModes.effortOptions(session)) { o in
                        Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id)
                    }
                }
                .pickerStyle(.inline)
                if !UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
                // The model in the same chip (#3119, the web's #2434): Claude Code takes `/model`, typed by the node.
                if SessionModes.switchesModel(session) {
                    let busy = SessionModes.modelBusy(session)
                    Section("Model [Assistant View c98e11]") {
                        // A typed model (#1644) is a row of its own, by its ID: no alias is ticked for it.
                        Picker("Model [Assistant View c98e11]", selection: Binding(get: { PromptItems.runningModelID(session?.model) }, set: { id in change(model: id) })) {
                            ForEach(PromptItems.runningModels(session?.model), id: \.id) { m in
                                Text(m.name).tag(m.id)
                            }
                        }
                        .pickerStyle(.inline)
                        .disabled(busy != nil)
                        Button { typingModel = true } label: { Label("Other Model…", systemImage: Theme.Icon.keyboard) }
                            .disabled(busy != nil)
                            .accessibilityIdentifier("sessionModes.otherModel")
                        // A menu draws no section footers: the note is a row of its own (not tappable).
                        Text(busy ?? String(localized: "The model you pick also becomes the default for new sessions"))
                    }
                }
            } label: {
                ModeChip(symbol: SessionModes.effort(effort)?.symbol ?? Theme.Icon.effort,
                         text: effort.map { String(localized: "Thinking \(SessionModes.effortLabel($0) ?? $0) [Session Modes e77236]") } ?? String(localized: "Thinking Effort"), tint: .secondary, known: effort != nil,
                         mark: Ultracode.chipMark(session))
            }
            .labelOnlyMenu()
            .accessibilityLabel(effortSpoken)
            if changing { ProgressView().controlSize(.mini) }
            Spacer(minLength: 0)
        }
        .disabled(changing)
        .animation(Theme.Motion.standard, value: changing)
        .sheet(isPresented: $typingModel) { SessionTypedModelSheet(key: key, session: session) }
    }

    /// The effort chip read out, with ", ultracode on" while it carries the mark (#1307).
    private var effortSpoken: String {
        let effort = session?.effort
        let words = SessionModes.switchesModel(session)
            ? String(localized: "Thinking effort and model: \(SessionModes.effortLabel(effort) ?? String(localized: "Unknown [Session Modes 4d8c1c]")), \(SessionModes.modelName(session?.model) ?? String(localized: "Unknown model"))")
            : String(localized: "Thinking effort: \(SessionModes.effortLabel(effort) ?? String(localized: "Unknown [Session Modes 4d8c1c]"))")
        guard let extra = Ultracode.chipMark(session)?.spoken else { return words }
        return words + String(localized: ", ", comment: "List separator") + extra
    }

    /// The switchable options plus the current one when it is not among them (bypassPermissions, an unknown mode).
    private func options(_ list: [SessionModes.Option], current: String?) -> [SessionModes.Option] {
        guard let current, !list.contains(where: { $0.id == current }) else { return list }
        return list + [SessionModes.mode(current) ?? .init(id: current, title: current, detail: "", symbol: Theme.Icon.help)]
    }

    private func change(mode: String? = nil, effort: String? = nil, model picked: String? = nil) {
        let newModel = picked.flatMap { SessionModes.modelPatch($0, current: session?.model) }
        if picked != nil, newModel == nil { return } // the model it runs already
        guard mode != session?.permissionMode || effort != session?.effort || newModel != nil, !changing else { return }
        changing = true
        Task {
            await model.setModes(key, permissionMode: mode, effort: effort, model: newModel)
            changing = false
        }
    }
}

/// The shell composer's model menu for a running conversation (#1091): the model (a choice where Claude Code takes
/// `/model`, otherwise the one it runs), the effort, and the permission mode a level down. Effort and mode are the same
/// choices as the `SessionModeBar` used above platform composers.
struct SessionModesMenu: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?
    /// Claude Code in a herdr pane: picking a model sends `/model <alias>`.
    var picksModel = false
    /// Effort and permission mode can change (Claude Code, the assistant; not Codex or TRAEX).
    var setsModes = true
    /// Only the effort can change, among the session's own levels (a Codex thread on its app-server, `Session.setsEffortOnly`).
    var setsEffortOnly = false
    /// "Other Model…" where a model can be picked: the composer opens `SessionTypedModelSheet` (#1644).
    var otherModel: (() -> Void)? = nil

    /// The button's title for this conversation; the effort carries the workflow mark while ultracode is on (#1307).
    static func title(_ session: Session?, setsModes: Bool) -> SessionModes.ComposerTitle? {
        var title = SessionModes.composerTitle(model: SessionModes.modelName(session?.model),
                                               effort: setsModes ? SessionModes.effortLabel(session?.effort) : nil)
        if setsModes { title?.mark = Ultracode.chipMark(session) }
        return title
    }

    var body: some View {
        let mode = session?.permissionMode
        let effort = session?.effort
        // Allow Everything is said here, not on the composer's button (#1117). First: a menu opening upward from the button puts
        // its first item next to it, where a menu too tall for the screen is never cut off.
        if setsModes, mode == "bypassPermissions" {
            Section { BypassWarning() }
        }
        Section("Model [Assistant View c98e11]") {
            if picksModel {
                // A typed model (#1644) is a row of its own, by its ID: no alias is ticked for it, and Default switches back.
                Picker("Model [Assistant View c98e11]", selection: Binding(get: { PromptItems.runningModelID(session?.model) }, set: { id in
                    guard case .send(let text) = PromptItems.modelPick(id, running: session?.model) else { return }
                    model.sendNow(key, text: text, images: []) // photos and quotes stay for the message
                })) {
                    ForEach(PromptItems.runningModels(session?.model), id: \.id) { m in
                        Text(m.name).tag(m.id)
                    }
                }
                .pickerStyle(.inline)
                if let otherModel {
                    // Goes through POST …/modes (the node's checks), not a typed `/model` message.
                    let busy = SessionModes.modelBusy(session)
                    Button(action: otherModel) {
                        Label { Text("Other Model…"); if let busy { Text(busy) } } icon: { ThemedIcon(Theme.Icon.keyboard) }
                    }
                    .disabled(busy != nil)
                    .accessibilityIdentifier("sessionModes.otherModel")
                }
            } else {
                Text(SessionModes.modelName(session?.model) ?? String(localized: "Unknown model")) // what it runs; changed where it was started
            }
        }
        if setsModes || setsEffortOnly {
            // Claude Code's ultracode (#1307): its own switch under the effort, not a level of it (declared before it where
            // the menu lists its items the other way round, `UltracodeMenuSection.leadsEffort`).
            if UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
            Section("Thinking Effort") {
                Picker("Thinking Effort", selection: Binding(get: { effort ?? "" }, set: { new in change(effort: new) })) {
                    ForEach(SessionModes.effortOptions(session)) { o in
                        Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id)
                    }
                }
                .pickerStyle(.inline)
            }
            if !UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
        }
        if setsModes {
            Picker(selection: Binding(get: { mode ?? "" }, set: { new in change(mode: new) })) {
                let list = SessionModes.switchableModes(session)
                ForEach(list + (mode.flatMap { m in list.contains { $0.id == m } ? nil : SessionModes.mode(m) }.map { [$0] } ?? [])) { o in
                    Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id)
                }
            } label: {
                Label("Permission Mode: \(SessionModes.modeLabel(mode) ?? SessionModes.unknown)", systemImage: SessionModes.mode(mode)?.symbol ?? Theme.Icon.askFirst)
            }
            .pickerStyle(.menu)
        }
    }

    private func change(mode: String? = nil, effort: String? = nil) {
        guard mode != session?.permissionMode || effort != session?.effort else { return }
        Task { await model.setModes(key, permissionMode: mode, effort: effort) }
    }
}

/// "Other Model…" for a running Claude Code session (#1644): a model ID typed by hand, with the IDs typed before for its
/// launcher on that computer, goes through POST …/modes (the node types `/model <id>` behind its own checks), never as a
/// message.
struct SessionTypedModelSheet: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?

    var body: some View {
        TypedModelSheet(machine: key.machineRef, launcher: SessionModes.launcher(session), initial: PromptItems.unlistedModel(session?.model) ?? "",
                        note: String(localized: "The model you pick also becomes the default for new sessions")) { id in
            Task { await model.setModes(key, model: id) }
        }
    }
}

/// The model menu's warning while everything is allowed (Allow Everything): red (a destructive item: its words and its shield),
/// as the composer's button used to be. Picking it does nothing; the permission mode is chosen a few rows up.
struct BypassWarning: View {
    var body: some View {
        Button(role: .destructive) {} label: {
            Label {
                Text("Allow Everything")
                Text("Edits files and runs commands without asking")
            } icon: {
                // A menu draws its images in the accent unless they carry their own colors.
                ThemedIcon(Theme.Icon.allowEverything).symbolRenderingMode(.palette).foregroundStyle(Theme.danger)
            }
        }
        .tint(Theme.danger)
    }
}

/// One tappable chip of `SessionModeBar` / the assistant's starter row.
struct ModeChip: View {
    var symbol: String
    var text: String
    var tint: Color
    var known = true
    /// The workflow mark after the label (#1307): ultracode on, or a Codex thread at "ultra". The chip's menu says it
    /// to VoiceOver (`SessionModeBar.effortSpoken`).
    var mark: Ultracode.Mark? = nil

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(symbol).font(Theme.Font.labelStrong)
            Text(text).lineLimit(1)
            if mark != nil {
                UltracodeMark(font: Theme.Font.labelStrong, spoken: nil).transition(.opacity)
            }
            ThemedIcon(Theme.Icon.choices).font(Theme.Font.labelHeavy).imageScale(.small).opacity(0.6)
        }
        .animation(UltracodeMark.motion, value: mark)
        .font(Theme.Font.labelMedium)
        .foregroundStyle(known ? (tint == .secondary ? Color.primary : tint) : Color.secondary)
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.sm)
        .background((known && tint != .secondary ? tint : Color.primary).opacity(known && tint != .secondary ? 0.12 : 0.06), in: .capsule)
        .contentShape(.capsule)
    }
}

/// Mode and effort for something not started yet (a new agent or assistant conversation): "" keeps the launcher's own.
struct NewSessionModePicker: View {
    @Binding var mode: String
    @Binding var effort: String
    var modes: [SessionModes.Option] = SessionModes.permissionModes

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            Menu {
                Picker("Permission Mode", selection: $mode) {
                    Text("Use Launcher's").tag("")
                    ForEach(modes) { o in Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id) }
                }
                .pickerStyle(.inline)
            } label: {
                ModeChip(symbol: SessionModes.mode(mode)?.symbol ?? Theme.Icon.askFirst,
                         text: SessionModes.mode(mode)?.title ?? String(localized: "Permissions: Default"), tint: SessionModes.modeTint(mode), known: !mode.isEmpty)
            }
            .labelOnlyMenu()
            Menu {
                Picker("Thinking Effort", selection: $effort) {
                    Text("Use Launcher's").tag("")
                    ForEach(SessionModes.startEffortLevels) { o in Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id) }
                }
                .pickerStyle(.inline)
            } label: {
                ModeChip(symbol: SessionModes.effort(effort)?.symbol ?? Theme.Icon.effort,
                         text: SessionModes.effort(effort).map { String(localized: "Thinking \($0.title) [Session Modes e77236]") } ?? String(localized: "Thinking: Default"), tint: .secondary, known: !effort.isEmpty)
            }
            .labelOnlyMenu()
        }
    }
}
