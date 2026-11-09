import AppIntents
import SwiftUI
import WidgetKit

/// Control Center / Lock Screen / Action Button: open the assistant to ask something.
struct AskPersonaControl: ControlWidget {
    // New controls keep the previous stable kind so they remain usable after reinstalling the previous app.
    var body: some ControlWidgetConfiguration { Self.configuration(kind: LegacyNames.controlAskKind) }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaAssistantIntent()) {
                Label("Ask Persona", systemImage: "sparkles")
            }
        }
        .displayName("Ask Persona")
        .description("Opens the Persona assistant.")
    }
}

/// Control: open the Agents page.
struct PersonaAgentsControl: ControlWidget {
    var body: some ControlWidgetConfiguration { Self.configuration(kind: LegacyNames.controlAgentsKind) }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaAgentsIntent()) {
                Label("Agents", systemImage: "person.crop.circle.badge.checkmark")
            }
        }
        .displayName("Persona Agents")
        .description("Opens the Agents tab in Persona.")
    }
}

/// Control Center / Lock Screen / Action Button: the main conversation with the keyboard up, so you just type.
struct TypeToPersonaControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: "persona.control.type") {
            ControlWidgetButton(action: StartPersonaTypingIntent()) {
                // `Theme.Icon.keyboard`'s symbol: this extension does not build `Theme.Icon`.
                Label("Type to Persona", systemImage: "keyboard")
            }
        }
        .displayName("Type to Persona")
        .description("Opens Persona's main chat with the keyboard up.")
    }
}

// The interim kinds `persona.control.ask` / `persona.control.agents` are no longer registered: they listed a second "Ask Persona"
// and "Persona Agents" in the control gallery, and WidgetKit cannot keep a kind working while leaving it out of the gallery. The
// stable kinds above are the ones every TestFlight build had (docs/apple/siri.md, "Controls").
