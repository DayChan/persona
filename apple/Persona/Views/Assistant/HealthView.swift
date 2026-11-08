import SwiftUI

// Health (docs/assistant/health.md, #2691): the health space, apart from Persona's other conversations, like ChatGPT's Health. Its
// home says what it is, asks something to start, lists its past conversations and its own memory, where its data comes
// from and what stays private. Its conversations run in a directory of their own on the assistant's machine, so the
// assistant's other conversations never see them and its memory is its own (`?space=health`). The web's `/health`.

/// Health's state and what it asks the hub (MVVM, docs/apple/ios.md「逻辑层的选型」): the space's conversations
/// (`GET /api/v1/assistant?space=health`) and how many memories it keeps.
@Observable final class HealthModel {
    private(set) var fetched: [Session] = []
    private(set) var memoryCount: Int?
    private(set) var reportCount: Int?
    private(set) var loaded = false
    /// Why the list could not be read; shown only while there is nothing to show.
    private(set) var error: String?
    @ObservationIgnored private let deps: AssistantDeps

    init(deps: AssistantDeps = .live) { self.deps = deps }

    func load(machine: String) async {
        guard let api = deps.api() else { return }
        async let memory = try? api.assistantMemory(space: Session.healthSpace)
        async let reports = try? api.healthReports(machine: machine)
        do {
            fetched = try await api.healthConversations()
            error = nil
        } catch {
            self.error = error.shown ?? self.error
        }
        if let files = await memory { memoryCount = files.filter { $0.name != AssistantMemoryEntry.index }.count }
        if let list = await reports { reportCount = list.count }
        loaded = true
    }

    /// The space's open conversations, newest first: the hub's list, with what the pushes say since (a title, its
    /// last line, a new one) over it. An archived one leaves the list.
    nonisolated static func merge(_ fetched: [Session], live: [Session]) -> [Session] {
        var by: [SessionKey: Session] = [:]
        for s in fetched { by[s.key] = s }
        for s in live where s.isHealth { by[s.key] = s }
        return by.values.filter { $0.archived != true }
            .sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
    }

    /// One-tap questions (the web's): each starts a health conversation with it.
    static let suggestions = [String(localized: "How did I sleep this week?"), String(localized: "How do my steps over the last 7 days compare with last week?"), String(localized: "Log my weight today: 62.5 kg [Health View 1a24cb]"), String(localized: "Log my weight today: 62.5 kg [Health View c5fdcd]")]
}

struct HealthView: View {
    @Environment(AppModel.self) private var model
    /// A conversation is being started (the Persona page shows it coming).
    var disabled: Bool
    @Binding var launcher: String?
    /// Starts a health conversation with the message (`AssistantView.start(…, space:)`).
    var send: (String, [ImagePayload]) -> Void
    @State private var page = HealthModel()
    // Upload a Checkup Report (docs/assistant/health.md「体检报告」): the picked files go to reports/; once they are up the conversation starts.
    @State private var showReports = false
    @State private var reports = FileUploads()

    /// The assistant's machine (the hub's unless set otherwise), where the health space's directory is.
    private var machine: MachineRef { model.hubRef(model.assistantConfig?.machineID ?? model.hubMachineID) }

    private func uploadReports(_ urls: [URL]) {
        let send = send
        reports.startAll(urls, machine: machine, model: model, dest: Session.healthSpace) { paths in
            let names = paths.map(PathInsert.quoted).joined(separator: " ")
            send(String(localized: "This is my checkup report. Please go through it item by item: \(names)"), [])
        }
    }

    var body: some View {
        // The field under the list, not over it (a safe-area inset): the Mac's list ran on under the field.
        VStack(spacing: 0) {
            sections(HealthModel.merge(page.fetched, live: model.healthConversations))
                .themedPage()
            if !reports.items.isEmpty { UploadsBar(uploads: reports).padding(.horizontal, Theme.Space.page) }
            Group {
                if AppStyle.current.usesBeautifulUI {
                    BeautifulStarterComposer(disabled: disabled, launcher: $launcher, placeholder: String(localized: "Ask about your health…"), uploadDest: Session.healthSpace, send: send)
                } else {
                    StarterComposer(disabled: disabled, placeholder: String(localized: "Ask about your health…"), uploadDest: Session.healthSpace, send: send)
                }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.top, Theme.Space.s)
            .padding(.bottom, Theme.Space.s)
            .frame(maxWidth: ChatView.readableWidth)
            .frame(maxWidth: .infinity)
            .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
        }
        .background(Theme.groupedPage)
        .pageTitle(String(localized: "Health"))
        .fileUploadPicker(isPresented: $showReports) { uploadReports($0) }
        #if DEBUG
        .task { // `-showReportUpload <name>`: a made-up report uploaded as if picked, to check the whole way without the picker
            if let name = UserDefaults.standard.string(forKey: "showReportUpload") {
                try? await Task.sleep(for: .seconds(3))
                let url = FileManager.default.temporaryDirectory.appending(path: name)
                try? Data("%PDF-1.7 demo".utf8).write(to: url)
                uploadReports([url])
            }
        }
        #endif
        .task { await page.load(machine: machine.id) }
        .refreshable { await page.load(machine: machine.id) }
    }

    private func sections(_ list: [Session]) -> some View {
        ScrollViewReader { proxy in
            List {
                Section { hero }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                Section {
                    Button { showReports = true } label: {
                        Label { Text("Upload a Checkup Report").foregroundStyle(Theme.Hue.pink).lineLimit(1) } icon: {
                            ThemedIcon(Theme.Icon.document).foregroundStyle(Theme.Hue.pink)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .disabled(disabled || !reports.items.isEmpty)
                    .accessibilityIdentifier("health-upload-report")
                    ForEach(HealthModel.suggestions, id: \.self) { q in
                        Button { send(q, []) } label: {
                            Label { Text(q).foregroundStyle(Color.primary).lineLimit(1) } icon: {
                                ThemedIcon(Theme.Icon.ai).foregroundStyle(Theme.Hue.pink)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .disabled(disabled)
                        .accessibilityIdentifier("health-suggestion")
                    }
                } header: {
                    Text("Try Asking")
                }
                Section {
                    NavigationLink { HealthReportsView(machine: machine) } label: {
                        HStack {
                            Label { Text("Checkup reports").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.document) }
                            Spacer(minLength: Theme.Space.s)
                            if let n = page.reportCount, n > 0 {
                                Text("\(n) [Health View cb8680]").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                    }
                    .accessibilityIdentifier("health-reports")
                }
                Section {
                    if !list.isEmpty {
                        ForEach(list) { s in row(s) }
                    } else if !page.loaded {
                        ProgressView().frame(maxWidth: .infinity)
                    } else if let error = page.error {
                        Text("Couldn't load health chats: \(error)").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    } else {
                        Text("No health chats yet. Ask a question below to start the first one.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                } header: {
                    Text("Health Chats")
                }
                Section {
                    NavigationLink { AssistantMemoryView(space: Session.healthSpace) } label: {
                        HStack {
                            Label { Text("Health Memory").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.memory) }
                            Spacer(minLength: Theme.Space.s)
                            if let n = page.memoryCount, n > 0 {
                                Text("\(n) items [Assistant Profile View 29b8db]").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                    }
                    .accessibilityIdentifier("health-memory")
                } footer: {
                    Text("Goals, allergies, medications and other things noted in health chats are kept separately and never flow into other chats. You can view and delete them anytime.")
                }
                Section {
                    source(Theme.Icon.health, "Apple Health", String(localized: "Reads summaries of steps, sleep, heart rate, weight and more through your iPhone. Persona needs to be open on the phone with access granted."))
                    source(Theme.Icon.edit, String(localized: "Log Data"), String(localized: "Say “log my weight” or “I drank 300 ml of water”; after you confirm on the phone it's saved to Health."))
                    source(Theme.Icon.document, String(localized: "Checkup Reports and Lab Results"), String(localized: "Choose Upload a checkup report and pick a PDF or a photo: each item is read out and the ones outside the reference range are marked. The original and its record stay in Health, so years can be compared."))
                } header: {
                    Text("Data Sources")
                }
                Section {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        Label { Text("Privacy").font(Theme.Font.heading) } icon: { ThemedIcon(Theme.Icon.privacy) }
                        ForEach(Self.privacy, id: \.self) { line in
                            Text("· " + line).font(Theme.Font.secondary).foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, Theme.Space.xs)
                    .id("health-privacy")
                }
            }
            #if DEBUG
            // `-showHealthBottom YES`: scrolled to the privacy note, for screenshots of the lower sections.
            .task(id: page.loaded) {
                guard page.loaded, UserDefaults.standard.bool(forKey: "showHealthBottom") else { return }
                try? await Task.sleep(for: .milliseconds(300))
                proxy.scrollTo("health-privacy", anchor: .bottom)
            }
            #endif
        }
    }

    private static let privacy = [
        String(localized: "Health chats stay here: Persona's other chats can't see or read them."),
        String(localized: "Chats and memory stay on the hub machine and never go into Knowledge."),
        String(localized: "This offers explanations and suggestions, not a substitute for a doctor's diagnosis or treatment."),
    ]

    private var hero: some View {
        VStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.healthFilled)
                .font(Theme.Font.title)
                .foregroundStyle(Theme.Hue.pink)
                .scaledFrame(width: Theme.Size.box56, height: Theme.Size.box56)
                .background(Theme.Hue.pink.opacity(0.14), in: .circle)
                .accessibilityHidden(true)
            Text("Ask About Your Health").font(Theme.Font.title).lineLimit(1)
            Text("Sleep, steps, heart rate, weight and diet, answered from the health data on your phone. Chats and memory here are kept apart from Persona's other chats.")
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, Theme.Space.l)
    }

    private func row(_ s: Session) -> some View {
        let title = s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title
        return Button { model.openAssistantConversation(s.key) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.healthFilled)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.Hue.pink)
                    .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                    .background(Theme.Hue.pink.opacity(0.14), in: .circle)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    if let last = s.lastText, !last.isEmpty {
                        Text(last).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if let at = s.updatedAt {
                    Text(Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("health-conversation")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button { Task { await model.archiveConversation(s.key, true); await page.load(machine: machine.id) } } label: {
                Label("Archive", systemImage: Theme.Icon.archive)
            }
        }
        .contextMenu {
            Button { Task { await model.archiveConversation(s.key, true); await page.load(machine: machine.id) } } label: {
                Label("Archive", systemImage: Theme.Icon.archive)
            }
        }
    }

    private func source(_ symbol: String, _ title: String, _ body: String) -> some View {
        Label {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.body).lineLimit(1)
                Text(body).font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        } icon: {
            ThemedIcon(symbol).foregroundStyle(symbol == Theme.Icon.health ? Theme.Hue.pink : .secondary)
        }
    }
}
