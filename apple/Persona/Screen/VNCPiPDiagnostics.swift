#if os(iOS)
import AVKit
import SwiftUI

/// Picture in Picture Diagnostics (#1839; VNC page › …): whether this device can show the small window and what it did, step by step, so a
/// phone whose small window does not come up says where it stopped. 「复制全部」 is what to send.
struct VNCPiPDiagnostics: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    private var facts: [(String, String)] {
        let audio = AVAudioSession.sharedInstance()
        return [
            (String(localized: "Picture in Picture supported"), VNCPiP.supported ? String(localized: "Yes [V N C Pi P Diagnostics b5141d]") : String(localized: "No")),
            (String(localized: "Picture in Picture open"), VNCPiP.shared.active ? String(localized: "Yes [V N C Pi P Diagnostics b5141d]") : String(localized: "No")),
            (String(localized: "Audio session"), Self.categories[audio.category] ?? audio.category.rawValue),
            (String(localized: "Background audio"), (Bundle.main.object(forInfoDictionaryKey: "UIBackgroundModes") as? [String])?.contains("audio") == true ? String(localized: "Yes [V N C Pi P Diagnostics 90ce0f]") : String(localized: "None [V N C Pi P Diagnostics f12780]")),
        ]
    }

    private static let categories: [AVAudioSession.Category: String] = [
        .playback: String(localized: "Play"), .soloAmbient: String(localized: "Default (no background)"), .ambient: String(localized: "Ambient"), .playAndRecord: String(localized: "Record [V N C Pi P Diagnostics 270b17]"), .record: String(localized: "Record [Recordings Section ef2821]"),
    ]

    private var text: String {
        (facts.map { String(localized: "\($0.0): \($0.1)", comment: "Diagnostics fact: name and value") } + VNCPiPLog.lines).joined(separator: "\n")
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    ForEach(facts, id: \.0) { label, value in
                        LabeledContent {
                            Text(value).textSelection(.enabled)
                        } label: {
                            Text(label).lineLimit(1)
                        }
                        .font(Theme.Font.secondary)
                    }
                }
                Section("Recent Steps") {
                    if VNCPiPLog.lines.isEmpty {
                        Text("Picture in Picture hasn't been opened yet.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                    ForEach(Array(VNCPiPLog.lines.enumerated().reversed()), id: \.offset) { _, line in
                        Text(line).font(Theme.Font.code).textSelection(.enabled)
                    }
                }
                Section {
                    Button {
                        UIPasteboard.general.string = text
                        model.toast = Toast(String(localized: "Copied [V N C Pi P Diagnostics 8f6f8d]"), style: .success)
                    } label: {
                        Label("Copy All [V N C Pi P Diagnostics 75c393]", systemImage: Theme.Icon.copy).lineLimit(1)
                    }
                } footer: {
                    Text("It's also in the system log (Console, subsystem persona, category vnc).")
                }
            }
            .themedPage()
            .navigationTitle("Picture in Picture Diagnostics")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { ThemedIcon(Theme.Icon.close) }
                        .accessibilityLabel("Close")
                }
            }
        }
        .accessibilityIdentifier("vnc-pip-diagnostics")
    }
}
#endif
