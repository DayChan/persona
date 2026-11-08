import SwiftUI
import WidgetKit

@main
struct PersonaWatchWidgetsBundle: WidgetBundle {
    init() { LanguageStore.shared.follow(LanguageStore.shared.groupResolved) }
    var body: some Widget { ExtensionSystemWidget() }
}
