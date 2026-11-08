import SwiftUI
import WidgetKit

@main
struct PersonaWidgetsBundle: WidgetBundle {
    init() {
        // The app's interface language (App Group), for this extension's next launch (docs/apple/ios.md「界面语言」).
        LanguageStore.shared.follow(LanguageStore.shared.groupResolved)
    }

    var body: some Widget {
        ExtensionSystemWidget()
        FleetLiveActivity()
        CompatibleFleetLiveActivity()
        AskPersonaControl()
        PersonaAgentsControl()
        CompatibleAssistantControl()
        CompatibleAgentsControl()
    }
}
