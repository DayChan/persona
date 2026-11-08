import SwiftUI

/// The data is read by the non-empty settings page; an empty List row does not start a task of its own.
/// Confirms before replacing; after choosing, the page's Save button is still needed.
struct ExtensionTriageTemplatePicker: View {
    @Binding var text: String
    var templates: [ExtensionTextTemplate]
    @State private var selected: ExtensionTextTemplate?

    var body: some View {
        if !templates.isEmpty {
            Menu {
                ForEach(templates) { template in
                    Button { selected = template } label: { Text(template.title).lineLimit(1) }
                        .accessibilityIdentifier("extension-triage-template-\(template.id)")
                }
            } label: {
                Label { Text("Fill from Template").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.insert) }
            }
            .confirmationDialog("Replace the Current Rule Draft?", isPresented: Binding(
                get: { selected != nil }, set: { if !$0 { selected = nil } }), titleVisibility: .visible) {
                if let template = selected {
                    Button {
                        text = ExtensionTextContributions.filling(template)
                        selected = nil
                    } label: { Text("Replace Draft").lineLimit(1) }
                    .accessibilityIdentifier("extension-triage-template-confirm")
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("The template replaces your current draft. It isn't saved to the hub automatically.")
            }
        }
    }
}
