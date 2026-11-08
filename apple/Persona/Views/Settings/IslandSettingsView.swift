import SwiftUI

// The Dynamic Island is an iPhone's: its preview lives in the widget's sources (LiveActivity/), and a Mac has no island.
#if os(iOS)

/// Settings › Notifications › Dynamic Island: what the compact Dynamic Island shows on each side of the camera. Kept per device on the hub
/// (prefs.island), which fills in only the numbers this device shows (docs/apple/live-activity.md).
struct IslandSettingsView: View {
    @Binding var island: IslandPrefs
    @Environment(AppModel.self) private var model
    /// Dynamic Island Diagnostics, opened by touching and holding the first preview (not a row: it is for a phone that draws the wrong
    /// persona, when asked to).
    @State private var diagnostics = false

    var body: some View {
        List {
            Section {
                VStack(spacing: Theme.Space.l) {
                    preview("Normally", Self.sample(needsYou: false), island)
                        .contentShape(.rect)
                        .onLongPressGesture { diagnostics = true }
                    preview("When an Agent Needs You", Self.sample(needsYou: true), island)
                    VStack(spacing: Theme.Space.sm) {
                        IslandSharedPreview(state: Self.sample(needsYou: false))
                        Text("Shared with Other Apps").font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, Theme.Space.sm)
            } footer: {
                Text("When an agent needs you, the left side always switches to a highlighted count. When another app also has a Live Activity, the Dynamic Island leaves Persona only a small slot that shows the count of the most urgent kind (Needs you > Working > Unread), whatever the style above.")
            }

            Section("Style") {
                ForEach(IslandPrefs.Preset.allCases, id: \.self) { preset in
                    Button {
                        island.preset = preset
                        if preset == .custom, island.left == nil, island.right == nil {
                            (island.left, island.right) = (.status, .tpm)
                        }
                    } label: {
                        HStack(spacing: Theme.Space.m) {
                            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                Text(preset.title).foregroundStyle(.primary).lineLimit(1)
                                Text(Self.summary(preset)).font(Theme.Font.caption).foregroundStyle(.secondary)
                            }
                            Spacer(minLength: Theme.Space.s)
                            if island.preset == preset {
                                ThemedIcon(Theme.Icon.done).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                            }
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(island.preset == preset ? .isSelected : [])
                }
            }

            if island.preset == .custom {
                Section {
                    Picker("Left", selection: side(\.left)) {
                        ForEach(IslandSlot.allCases.filter { $0 != .none }, id: \.self) { Text($0.title).tag($0) }
                    }
                    Picker("Right", selection: side(\.right)) {
                        ForEach(IslandSlot.allCases, id: \.self) { Text($0.title).tag($0) }
                    }
                } header: {
                    Text("Custom")
                } footer: {
                    Text("“Status count” shows the most urgent kind on the left (Needs you > Working > Unread) and the next kind on the right.")
                }
            }

            if island.sides.left == .load || island.sides.right == .load {
                Section("Load From") {
                    Picker(String(localized: "picker.machine", defaultValue: "Machine"), selection: Binding(get: { island.machine ?? "" }, set: { island.machine = $0.isEmpty ? nil : $0 })) {
                        Text("Busiest").tag("")
                        ForEach(model.machines) { Text($0.name).tag($0.id) }
                    }
                    .pickerStyle(.inline)
                    .labelsHidden()
                }
            }

            Section {
            } footer: {
                Text("Changes to Needs you, Working, and Done update right away; numbers like tokens and load update at most once a minute (the system limits Live Activity pushes), and in real time while you're in the app.")
            }
        }
        .themedPage()
        .navigationTitle("Dynamic Island")
        .navigationBarTitleDisplayMode(.inline)
        .navigationDestination(isPresented: $diagnostics) { IslandDiagnosticsView() }
        #if DEBUG
        // `-openIslandDiagnostics YES` (with `-openIslandSettings YES -openNotificationSettings YES -tab settings`).
        .task {
            if UserDefaults.standard.bool(forKey: "openIslandDiagnostics") {
                try? await Task.sleep(for: .milliseconds(600))
                diagnostics = true
            }
        }
        #endif
    }

    private func preview(_ title: LocalizedStringKey, _ state: FleetState, _ island: IslandPrefs) -> some View {
        VStack(spacing: Theme.Space.sm) {
            IslandPreview(state: state.tailored(island: island, details: false), look: model.fleetLook)
            Text(title).font(Theme.Font.label).foregroundStyle(.secondary)
        }
    }

    private func side(_ path: WritableKeyPath<IslandPrefs, IslandSlot?>) -> Binding<IslandSlot> {
        Binding { island[keyPath: path] ?? .status } set: { island[keyPath: path] = $0 }
    }

    static func summary(_ preset: IslandPrefs.Preset) -> String {
        switch preset {
        case .a: String(localized: "Most urgent count on the left, next kind on the right: Needs you, Working, Unread")
        case .b: String(localized: "Status on the left, tokens per minute across all agents on the right")
        case .c: String(localized: "Status on the left, tokens used today on the right")
        case .custom: String(localized: "Pick one item for each side")
        }
    }

    /// Made-up numbers for the previews.
    static func sample(needsYou: Bool) -> FleetState {
        FleetState(phase: needsYou ? .needsYou : .working, needsYou: needsYou ? 2 : 0, working: 3, unread: 1,
                   workingSince: Date.now.timeIntervalSince1970 - 754, assistant: true,
                   stats: .init(tpm: 12000, tokens: 42_000_000, requests: 1200, loads: [.init(id: "", name: "", percent: 35)]))
    }
}
#endif
