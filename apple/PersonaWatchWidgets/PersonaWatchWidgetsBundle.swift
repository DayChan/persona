import SwiftUI
import WidgetKit

@main
struct PersonaWatchWidgetsBundle: WidgetBundle {
    init() { LanguageStore.shared.follow(LanguageStore.shared.groupResolved) }
    var body: some Widget {
        ExtensionSystemWidget()
        PetComplication() // the Milk Frog on the watch face (docs/apple/persona.md)
    }
}
