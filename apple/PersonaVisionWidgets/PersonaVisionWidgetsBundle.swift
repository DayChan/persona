import SwiftUI
import WidgetKit

@main
struct PersonaVisionWidgetsBundle: WidgetBundle {
    init() { LanguageStore.shared.follow(LanguageStore.shared.groupResolved) }
    var body: some Widget { ExtensionSystemWidget() }
}
