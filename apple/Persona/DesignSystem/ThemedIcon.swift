import BeautifulUI
import SwiftUI

/// An SF Symbol in the current style's icon language (`IconSpec`, docs/apple/icons.md): the style's weight, its outline
/// form under a style that draws outlines, and its own symbol where the style swaps one. Size and color come from the
/// caller's `.font` and `.foregroundStyle`, as for `Image(systemName:)`. Liquid Glass, Clay, Island and Lark draw the
/// name as written.
struct ThemedIcon: View {
    var name: String

    init(_ name: String) {
        self.name = name
    }

    var body: some View {
        let spec = StyleSpec.current.icons
        let image = Image(systemName: Theme.Icon.styled(name, in: spec))
        if let weight = spec.weight {
            image.fontWeight(weight)
        } else {
            image
        }
    }
}

/// A row's mark on a tinted rounded tile (Settings rows, the assistant's suggestions and profile, Needs You on the home
/// page). Persona's one entry to BeautifulUI's `BUIIconTile`: every tile is one size and draws the style's outline symbol.
struct IconTile: View {
    var name: String
    var tint: Color

    init(_ name: String, tint: Color) {
        self.name = name
        self.tint = tint
    }

    var body: some View {
        BUIIconTile(Theme.Icon.styled(name), tint: tint)
    }
}
