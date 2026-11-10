import SwiftUI
import WidgetKit

@main
struct PersonaWidgetsBundle: WidgetBundle {
    init() {
        // The app's interface language (App Group), for this extension's next launch (docs/apple/ios.md「Interface language」).
        LanguageStore.shared.follow(LanguageStore.shared.groupResolved)
    }

    var body: some Widget {
        ExtensionSystemWidget()
        FleetLiveActivity()
        CompatibleFleetLiveActivity()
        RecordingLiveActivity()
        MeetingRecordingControl()
        AskPersonaControl()
        PersonaAgentsControl()
        CompatibleAssistantControl()
        CompatibleAgentsControl()
    }
}
